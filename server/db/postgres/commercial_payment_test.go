package postgres

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresCommercialPaymentContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_commercial_test_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create commercial test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open commercial test postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create commercial test schema objects: %v", err)
	}
	testPostgresMigrationFiles(t, db)
	ownerID, err := db.CreateUser(&types.User{
		Username: "commercial-owner", Email: "commercial-owner@example.test", DisplayName: "Commercial Owner",
		AccountType: types.AccountHuman, PassHash: []byte("commercial-owner-hash"),
	})
	if err != nil {
		t.Fatalf("create commercial test owner: %v", err)
	}
	testCommercialPaymentContract(t, db, ownerID)
	testCommercialOfficialPlanUpgradeUsers(t, db)
	testCommercialRelayBaselineContract(t, db)
}

func TestPostgresCommercialRelayBaselineContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_commercial_baseline_test_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create commercial baseline test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open commercial baseline postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create commercial baseline schema objects: %v", err)
	}
	testCommercialRelayBaselineContract(t, db)
}

func testCommercialRelayBaselineContract(t *testing.T, db *Adapter) {
	t.Helper()
	freeUID, err := db.CreateUser(&types.User{
		Username: "commercial-free-baseline", Email: "commercial-free-baseline@example.test", DisplayName: "Free Baseline",
		AccountType: types.AccountHuman, PassHash: []byte("commercial-free-baseline-hash"),
	})
	if err != nil {
		t.Fatalf("create free baseline user: %v", err)
	}
	anchor := time.Date(2026, 8, 1, 8, 30, 0, 0, time.UTC)
	budgets := map[string]float64{"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-v4-flash": 100}
	created, err := db.EnsureCommercialRelayBaseline(freeUID, "free", budgets, anchor)
	if err != nil || !created {
		t.Fatalf("create free relay baseline: created=%v err=%v", created, err)
	}
	created, err = db.EnsureCommercialRelayBaseline(freeUID, "free", budgets, anchor)
	if err != nil || created {
		t.Fatalf("free relay baseline was not idempotent: created=%v err=%v", created, err)
	}
	summary, err := db.GetCommercialSummary(freeUID)
	if err != nil || summary.TotalCNY != 1600 || len(summary.Entitlements) != 1 || len(summary.Grants) != 3 {
		t.Fatalf("unexpected free relay baseline summary: summary=%#v err=%v", summary, err)
	}
	if summary.Entitlements[0].PlanSlug != "catsco-free" || !summary.Entitlements[0].StartsAt.Equal(anchor) {
		t.Fatalf("free baseline entitlement mismatch: %#v", summary.Entitlements[0])
	}

	legacyUID, err := db.CreateUser(&types.User{
		Username: "commercial-legacy-baseline", Email: "commercial-legacy-baseline@example.test", DisplayName: "Legacy Baseline",
		AccountType: types.AccountHuman, PassHash: []byte("commercial-legacy-baseline-hash"),
	})
	if err != nil {
		t.Fatalf("create legacy baseline user: %v", err)
	}
	created, err = db.EnsureCommercialRelayBaseline(legacyUID, "legacy", map[string]float64{"gpt-5.6-terra": 5000}, anchor)
	if err != nil || !created {
		t.Fatalf("create legacy relay baseline: created=%v err=%v", created, err)
	}
	legacySummary, err := db.GetCommercialSummary(legacyUID)
	if err != nil || legacySummary.TotalCNY != 5000 || len(legacySummary.Entitlements) != 1 {
		t.Fatalf("unexpected legacy relay baseline summary: summary=%#v err=%v", legacySummary, err)
	}
	if legacySummary.Entitlements[0].PlanSlug != "catsco-legacy-custom" {
		t.Fatalf("legacy baseline was not classified separately: %#v", legacySummary.Entitlements[0])
	}

	manualUID, err := db.CreateUser(&types.User{
		Username: "commercial-manual-baseline", Email: "commercial-manual-baseline@example.test", DisplayName: "Manual Baseline",
		AccountType: types.AccountHuman, PassHash: []byte("commercial-manual-baseline-hash"),
	})
	if err != nil {
		t.Fatalf("create manual baseline user: %v", err)
	}
	if _, err := db.db.Exec(`
		INSERT INTO commercial_quota_grants(uid, grant_type, model, amount_cny, reset_duration, effective_at, source_ref, note)
		VALUES ($1, 'manual', 'MiniMax-M3', 77, '1M', $2, 'pre-migration-manual', 'preserve manual quota')`, manualUID, anchor); err != nil {
		t.Fatalf("seed manual relay quota: %v", err)
	}
	created, err = db.EnsureCommercialRelayBaseline(manualUID, "legacy", nil, anchor)
	if err != nil || !created {
		t.Fatalf("attach legacy entitlement to manual quota: created=%v err=%v", created, err)
	}
	manualSummary, err := db.GetCommercialSummary(manualUID)
	if err != nil || manualSummary.TotalCNY != 77 || len(manualSummary.Grants) != 1 || len(manualSummary.Entitlements) != 1 {
		t.Fatalf("manual quota changed during legacy migration: summary=%#v err=%v", manualSummary, err)
	}
	if manualSummary.Entitlements[0].PlanSlug != "catsco-legacy-custom" {
		t.Fatalf("manual quota did not receive the legacy package: %#v", manualSummary.Entitlements[0])
	}
}

func testCommercialOfficialPlanUpgradeUsers(t *testing.T, db *Adapter) {
	t.Helper()
	paidUpgradeUID, err := db.CreateUser(&types.User{
		Username: "commercial-paid-upgrade", Email: "commercial-paid-upgrade@example.test", DisplayName: "Paid Upgrade",
		AccountType: types.AccountHuman, PassHash: []byte("commercial-paid-upgrade-hash"),
	})
	if err != nil {
		t.Fatalf("create paid upgrade user: %v", err)
	}
	inviteUpgradeUID, err := db.CreateUser(&types.User{
		Username: "commercial-invite-upgrade", Email: "commercial-invite-upgrade@example.test", DisplayName: "Invite Upgrade",
		AccountType: types.AccountHuman, PassHash: []byte("commercial-invite-upgrade-hash"),
	})
	if err != nil {
		t.Fatalf("create invite upgrade user: %v", err)
	}
	testCommercialOfficialPlanUpgrade(t, db, paidUpgradeUID, inviteUpgradeUID)
}

func TestTruncateCommercialErrorPreservesUTF8(t *testing.T) {
	value := strings.Repeat("付", 200)
	got := truncateCommercialError(value)
	if len(got) > 500 || !utf8.ValidString(got) {
		t.Fatalf("invalid truncated error: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestCommercialPlanHasPositiveBudget(t *testing.T) {
	if commercialPlanHasPositiveBudget(&types.CommercialPlan{}) {
		t.Fatal("empty plan must not be purchasable")
	}
	if !commercialPlanHasPositiveBudget(&types.CommercialPlan{MonthlyBudget: 1}) {
		t.Fatal("monthly budget should make a plan purchasable")
	}
	if !commercialPlanHasPositiveBudget(&types.CommercialPlan{ModelBudgets: map[string]float64{"MiniMax-M3": 1}}) {
		t.Fatal("model budget should make a plan purchasable")
	}
}

func testCommercialPaymentContract(t *testing.T, db *Adapter, uid int64) {
	t.Helper()

	paidPlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:                "pg-paid-plan",
		Name:                "PostgreSQL 付费包",
		PriceFen:            2990,
		Currency:            "CNY",
		SaleState:           "test",
		PurchaseLimit:       1,
		ModelBudgets:        map[string]float64{"MiniMax-M3": 30},
		InternalQuotaTokens: 50_000_000,
		DurationDays:        30,
	})
	if err != nil {
		t.Fatalf("create paid commercial plan: %v", err)
	}
	savedPlan, err := db.GetCommercialPlan(paidPlanID)
	if err != nil || savedPlan == nil || savedPlan.InternalQuotaTokens != 50_000_000 {
		t.Fatalf("persist internal plan capacity: plan=%#v err=%v", savedPlan, err)
	}

	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	created, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         "CCPGPAID0001",
		UID:             uid,
		PlanID:          paidPlanID,
		Channel:         "test",
		ClientRequestID: "pg_paid_request_0001",
		ExpiresAt:       &expiresAt,
	})
	if err != nil {
		t.Fatalf("create commercial order: %v", err)
	}
	if created.AmountFen != 2990 || created.PlanName != "PostgreSQL 付费包" || created.Status != "created" {
		t.Fatalf("unexpected commercial order snapshot: %#v", created)
	}

	idempotent, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         "CCPGPAIDIGNORED",
		UID:             uid,
		PlanID:          paidPlanID,
		Channel:         "test",
		ClientRequestID: "pg_paid_request_0001",
		ExpiresAt:       &expiresAt,
	})
	if err != nil || idempotent.ID != created.ID || idempotent.OrderNo != created.OrderNo {
		t.Fatalf("commercial order idempotency failed: order=%#v err=%v", idempotent, err)
	}
	openOrder, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         "CCPGPAIDOPENIGNORED",
		UID:             uid,
		PlanID:          paidPlanID,
		Channel:         "test",
		ClientRequestID: "pg_paid_request_0002",
		ExpiresAt:       &expiresAt,
	})
	if err != nil || openOrder.ID != created.ID || openOrder.OrderNo != created.OrderNo {
		t.Fatalf("open commercial order was duplicated: order=%#v err=%v", openOrder, err)
	}

	pending, claimed, err := db.BeginCommercialOrderPayment(created.OrderNo, expiresAt)
	if err != nil || !claimed || pending.Status != "pending" {
		t.Fatalf("begin commercial payment: order=%#v claimed=%v err=%v", pending, claimed, err)
	}
	_, claimedAgain, err := db.BeginCommercialOrderPayment(created.OrderNo, expiresAt)
	if err != nil || claimedAgain {
		t.Fatalf("commercial payment intent was claimed twice: claimed=%v err=%v", claimedAgain, err)
	}
	pending, err = db.SetCommercialOrderPaymentIntent(created.OrderNo, "https://openapi.alipay.test/gateway.do", expiresAt)
	if err != nil || pending.CheckoutURL == "" {
		t.Fatalf("save commercial payment intent: order=%#v err=%v", pending, err)
	}

	confirmation := &types.CommercialPaymentConfirmation{
		Channel:         "test",
		EventID:         "pg-payment-event-0001",
		ProviderTradeNo: "PG-TRADE-0001",
		AmountFen:       2990,
		Currency:        "CNY",
		PaidAt:          time.Now().UTC(),
		PayloadHash:     strings.Repeat("a", 64),
	}
	fulfilled, changed, err := db.FulfillCommercialOrder(created.OrderNo, confirmation)
	if err != nil || !changed || fulfilled.Status != "fulfilled" {
		t.Fatalf("fulfill commercial order: order=%#v changed=%v err=%v", fulfilled, changed, err)
	}
	aliasRetry, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         "CCPGPAIDALIASRETRY",
		UID:             uid,
		PlanID:          paidPlanID,
		Channel:         "test",
		ClientRequestID: "pg_paid_request_0002",
		ExpiresAt:       &expiresAt,
	})
	if err != nil || aliasRetry.ID != created.ID || aliasRetry.Status != "fulfilled" {
		t.Fatalf("open-order request alias lost idempotency after fulfillment: order=%#v err=%v", aliasRetry, err)
	}
	required, err := db.CommercialRelaySyncRequired(uid)
	if err != nil || !required {
		t.Fatalf("fulfilled paid entitlement must remain relay-sync eligible: required=%v err=%v", required, err)
	}
	duplicate, changed, err := db.FulfillCommercialOrder(created.OrderNo, confirmation)
	if err != nil || changed || duplicate.Status != "fulfilled" {
		t.Fatalf("duplicate payment callback was not idempotent: order=%#v changed=%v err=%v", duplicate, changed, err)
	}

	summary, err := db.GetCommercialSummary(uid)
	if err != nil || len(summary.Entitlements) != 1 || summary.TotalsByModel["MiniMax-M3"] != 30 {
		t.Fatalf("commercial fulfillment summary mismatch: summary=%#v err=%v", summary, err)
	}
	if _, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         "CCPGPAID0002",
		UID:             uid,
		PlanID:          paidPlanID,
		Channel:         "test",
		ClientRequestID: "pg_paid_request_0003",
		ExpiresAt:       &expiresAt,
	}); err == nil || !strings.Contains(err.Error(), "purchase limit") {
		t.Fatalf("expected purchase limit rejection, got %v", err)
	}
	refundRequestNo := "CCRF-" + created.OrderNo
	const testRefundClaimTTL = 10 * time.Millisecond
	refunding, claimed, err := db.BeginCommercialOrderRefund(created.OrderNo, refundRequestNo, testRefundClaimTTL)
	if err != nil || !claimed || refunding.Status != "refunding" || refunding.RefundRequestNo != refundRequestNo {
		t.Fatalf("begin commercial refund: order=%#v claimed=%v err=%v", refunding, claimed, err)
	}
	if duplicateClaim, claimedAgain, claimErr := db.BeginCommercialOrderRefund(created.OrderNo, refundRequestNo, testRefundClaimTTL); claimErr != nil || claimedAgain || duplicateClaim.Status != "refunding" {
		t.Fatalf("active refund claim was not exclusive: order=%#v claimed=%v err=%v", duplicateClaim, claimedAgain, claimErr)
	}
	time.Sleep(25 * time.Millisecond)
	if reclaimed, reclaimedClaim, reclaimErr := db.BeginCommercialOrderRefund(created.OrderNo, refundRequestNo, testRefundClaimTTL); reclaimErr != nil || !reclaimedClaim || reclaimed.Status != "refunding" {
		t.Fatalf("stale refund claim was not recoverable: order=%#v claimed=%v err=%v", reclaimed, reclaimedClaim, reclaimErr)
	}
	refunded, changed, err := db.CompleteCommercialOrderRefund(created.OrderNo, &types.CommercialRefundConfirmation{
		Channel:         confirmation.Channel,
		EventID:         refundRequestNo,
		ProviderTradeNo: confirmation.ProviderTradeNo,
		RefundRequestNo: refundRequestNo,
		AmountFen:       confirmation.AmountFen,
		Currency:        confirmation.Currency,
		RefundedAt:      time.Now().UTC(),
		PayloadHash:     strings.Repeat("b", 64),
	})
	if err != nil || !changed || refunded.Status != "refunded" || refunded.RefundedAt == nil {
		t.Fatalf("complete commercial refund: order=%#v changed=%v err=%v", refunded, changed, err)
	}
	refundedAgain, changed, err := db.CompleteCommercialOrderRefund(created.OrderNo, &types.CommercialRefundConfirmation{
		Channel: confirmation.Channel, EventID: refundRequestNo, ProviderTradeNo: confirmation.ProviderTradeNo,
		RefundRequestNo: refundRequestNo, AmountFen: confirmation.AmountFen, Currency: confirmation.Currency,
	})
	if err != nil || changed || refundedAgain.Status != "refunded" {
		t.Fatalf("duplicate commercial refund was not idempotent: order=%#v changed=%v err=%v", refundedAgain, changed, err)
	}
	refundedSummary, err := db.GetCommercialSummary(uid)
	if err != nil || len(refundedSummary.Entitlements) != 0 || refundedSummary.TotalsByModel["MiniMax-M3"] != 0 {
		t.Fatalf("refunded commercial summary retained quota: summary=%#v err=%v", refundedSummary, err)
	}
	var revokedGrants, reversalEntries int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE grant_type = 'order' AND source_ref = $1 AND revoked_at IS NOT NULL`, created.OrderNo).Scan(&revokedGrants); err != nil {
		t.Fatalf("count revoked order grants: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_ledger WHERE source_type = 'refund' AND entry_type = 'revoke' AND amount_cny < 0`).Scan(&reversalEntries); err != nil {
		t.Fatalf("count refund reversal entries: %v", err)
	}
	if revokedGrants != 1 || reversalEntries != 1 {
		t.Fatalf("refund audit mismatch: revoked_grants=%d reversal_entries=%d", revokedGrants, reversalEntries)
	}

	replayOrder, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         "CCPGREPLAY0001",
		UID:             uid,
		PlanID:          paidPlanID,
		Channel:         "test",
		ClientRequestID: "pg_replay_request_0001",
		ExpiresAt:       &expiresAt,
	})
	if err != nil {
		t.Fatalf("create payment-event replay order: %v", err)
	}
	replayOrder, claimed, err = db.BeginCommercialOrderPayment(replayOrder.OrderNo, expiresAt)
	if err != nil || !claimed || replayOrder.Status != "pending" {
		t.Fatalf("begin payment-event replay order: order=%#v claimed=%v err=%v", replayOrder, claimed, err)
	}
	replayedConfirmation := *confirmation
	replayedConfirmation.ProviderTradeNo = "PG-TRADE-REPLAY"
	if _, _, err := db.FulfillCommercialOrder(replayOrder.OrderNo, &replayedConfirmation); err == nil || !strings.Contains(err.Error(), "event was already used") {
		t.Fatalf("payment event replay was not rejected: %v", err)
	}
	replayOrder, err = db.GetCommercialOrder(uid, replayOrder.OrderNo)
	if err != nil || replayOrder.Status != "pending" {
		t.Fatalf("event replay changed the target order: order=%#v err=%v", replayOrder, err)
	}
	var replayEntitlements int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_entitlements WHERE source = 'order' AND source_ref = $1`, replayOrder.OrderNo).Scan(&replayEntitlements); err != nil || replayEntitlements != 0 {
		t.Fatalf("event replay created entitlement: count=%d err=%v", replayEntitlements, err)
	}
	if closed, changed, err := db.CancelCommercialOrder(uid, replayOrder.OrderNo, "cleanup replay test"); err != nil || !changed || closed.Status != "closed" {
		t.Fatalf("close payment-event replay order: order=%#v changed=%v err=%v", closed, changed, err)
	}
	if _, err := db.ClaimCommercialTrial(uid, "pg-paid-plan"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected paid plan to be rejected as a trial, got %v", err)
	}

	trialPlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "pg-trial-plan",
		Name:         "PostgreSQL 体验包",
		Currency:     "CNY",
		SaleState:    "hidden",
		ModelBudgets: map[string]float64{"deepseek-v4-flash": 5},
		DurationDays: 7,
	})
	if err != nil || trialPlanID <= 0 {
		t.Fatalf("create trial commercial plan: id=%d err=%v", trialPlanID, err)
	}
	trialSummary, err := db.ClaimCommercialTrial(uid, "pg-trial-plan")
	if err != nil || trialSummary.TotalsByModel["deepseek-v4-flash"] != 5 {
		t.Fatalf("claim commercial trial: summary=%#v err=%v", trialSummary, err)
	}
	if _, err := db.ClaimCommercialTrial(uid, "pg-trial-plan"); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("expected duplicate trial rejection, got %v", err)
	}

	managed := []*types.CommercialManagedRelayBudget{{
		UID: uid, Model: "MiniMax-M3", Provider: "minimax", AllowedModels: []string{"MiniMax-M3"}, MaxLimit: 30, ResetDuration: "1M",
	}}
	if err := db.ReplaceCommercialManagedRelayBudgets(uid, managed); err != nil {
		t.Fatalf("save managed relay budgets: %v", err)
	}
	storedManaged, err := db.ListCommercialManagedRelayBudgets(uid)
	if err != nil || len(storedManaged) != 1 || storedManaged[0].Provider != "minimax" || storedManaged[0].MaxLimit != 30 {
		t.Fatalf("managed relay budget mismatch: budgets=%#v err=%v", storedManaged, err)
	}

	testConcurrentCommercialOpenOrderCoalescing(t, db, uid)
}

func testCommercialOfficialPlanUpgrade(t *testing.T, db *Adapter, paidUID, inviteUID int64) {
	t.Helper()
	personalID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: commercialPersonalPlanSlug, Name: "个人版", PriceFen: 39900, Currency: "CNY", SaleState: "test",
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 100}, DurationDays: 30,
	})
	if err != nil {
		t.Fatalf("create personal plan: %v", err)
	}
	proID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: commercialProPlanSlug, Name: "专业版", PriceFen: 79900, Currency: "CNY", SaleState: "test",
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 300}, DurationDays: 30,
	})
	if err != nil {
		t.Fatalf("create pro plan: %v", err)
	}

	personalOrder := createAndFulfillCommercialTestOrder(t, db, paidUID, personalID, "CCTIERPERSONAL", "tier_personal_request", "tier-personal-event")
	if _, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo: "CCTIERPERSONALREPEAT", UID: paidUID, PlanID: personalID, Channel: "test",
		ClientRequestID: "tier_personal_repeat_request",
	}); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("active personal plan could be repurchased: %v", err)
	}
	bonusExpiry := time.Now().UTC().Add(7 * 24 * time.Hour)
	bonus, err := db.GrantCommercialQuota(&types.CommercialQuotaGrant{
		UID: paidUID, PlanID: personalID, GrantType: "bonus", Model: "gpt-5.6-terra",
		AmountCNY: 25, ExpiresAt: &bonusExpiry,
	})
	if err != nil {
		t.Fatalf("create personal plan bonus before upgrade: %v", err)
	}

	createAndFulfillCommercialTestOrder(t, db, paidUID, proID, "CCTIERPRO", "tier_pro_request", "tier-pro-event")
	summary, err := db.GetCommercialSummary(paidUID)
	if err != nil || len(summary.Entitlements) != 1 || summary.Entitlements[0].PlanSlug != commercialProPlanSlug || summary.TotalsByModel["gpt-5.6-terra"] != 325 {
		t.Fatalf("paid upgrade did not replace personal quota: summary=%#v err=%v", summary, err)
	}
	var bonusRevokedAt sql.NullTime
	if err := db.db.QueryRow(`SELECT revoked_at FROM commercial_quota_grants WHERE id = $1`, bonus.ID).Scan(&bonusRevokedAt); err != nil || bonusRevokedAt.Valid {
		t.Fatalf("independent bonus was revoked during upgrade: revoked_at=%v err=%v", bonusRevokedAt, err)
	}
	var revokedPersonalGrants, upgradeLedger int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE uid = $1 AND plan_id = $2 AND revoked_at IS NOT NULL`, paidUID, personalID).Scan(&revokedPersonalGrants); err != nil {
		t.Fatalf("count revoked personal grants: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_ledger WHERE uid = $1 AND source_type = 'upgrade' AND entry_type = 'revoke'`, paidUID).Scan(&upgradeLedger); err != nil {
		t.Fatalf("count upgrade ledger entries: %v", err)
	}
	if revokedPersonalGrants != 1 || upgradeLedger != 1 {
		t.Fatalf("paid upgrade audit mismatch: revoked=%d ledger=%d", revokedPersonalGrants, upgradeLedger)
	}
	var personalState string
	if err := db.db.QueryRow(`SELECT state FROM commercial_entitlements WHERE uid = $1 AND source_ref = $2`, paidUID, personalOrder.OrderNo).Scan(&personalState); err != nil || personalState != "revoked" {
		t.Fatalf("personal entitlement was not revoked: state=%q err=%v", personalState, err)
	}
	for _, blocked := range []struct {
		planID int64
		want   string
	}{
		{personalID, "below active plan"},
		{proID, "already active"},
	} {
		if _, err := db.CreateCommercialOrder(&types.CommercialOrder{
			OrderNo: fmt.Sprintf("CCTIERBLOCKED%d", blocked.planID), UID: paidUID, PlanID: blocked.planID, Channel: "test",
			ClientRequestID: fmt.Sprintf("tier_blocked_request_%d", blocked.planID),
		}); err == nil || !strings.Contains(err.Error(), blocked.want) {
			t.Fatalf("plan tier purchase was not blocked: plan=%d err=%v", blocked.planID, err)
		}
	}

	personalInvite := &types.CommercialInviteCode{Code: "TIER-PERSONAL", PlanID: personalID, MaxRedemptions: 1}
	if _, err := db.CreateCommercialInviteCode(personalInvite); err != nil {
		t.Fatalf("create personal invite: %v", err)
	}
	proInvite := &types.CommercialInviteCode{Code: "TIER-PRO", PlanID: proID, MaxRedemptions: 1}
	if _, err := db.CreateCommercialInviteCode(proInvite); err != nil {
		t.Fatalf("create pro invite: %v", err)
	}
	pendingExpiresAt := time.Now().UTC().Add(20 * time.Minute)
	pendingPersonal, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo: "CCTIERINVITEPENDING", UID: inviteUID, PlanID: personalID, Channel: "test",
		ClientRequestID: "tier_invite_pending_request", ExpiresAt: &pendingExpiresAt,
	})
	if err != nil {
		t.Fatalf("create pending order before invite: %v", err)
	}
	if _, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo: "CCTIERCROSSPENDING", UID: inviteUID, PlanID: proID, Channel: "test",
		ClientRequestID: "tier_cross_pending_request", ExpiresAt: &pendingExpiresAt,
	}); err == nil || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("cross-tier pending order was not blocked: %v", err)
	}
	if _, err := db.RedeemCommercialInvite(inviteUID, personalInvite.Code); err == nil || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("invite redemption ignored pending payment order: %v", err)
	}
	if _, changed, err := db.CancelCommercialOrder(inviteUID, pendingPersonal.OrderNo, "continue tier test"); err != nil || !changed {
		t.Fatalf("cancel pending order before invite: changed=%v err=%v", changed, err)
	}
	if _, err := db.RedeemCommercialInvite(inviteUID, personalInvite.Code); err != nil {
		t.Fatalf("redeem personal invite: %v", err)
	}
	inviteSummary, err := db.RedeemCommercialInvite(inviteUID, proInvite.Code)
	if err != nil || len(inviteSummary.Entitlements) != 1 || inviteSummary.Entitlements[0].PlanSlug != commercialProPlanSlug || inviteSummary.TotalsByModel["gpt-5.6-terra"] != 300 {
		t.Fatalf("invite upgrade did not replace personal quota: summary=%#v err=%v", inviteSummary, err)
	}
}

func createAndFulfillCommercialTestOrder(t *testing.T, db *Adapter, uid, planID int64, orderNo, requestID, eventID string) *types.CommercialOrder {
	t.Helper()
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	order, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo: orderNo, UID: uid, PlanID: planID, Channel: "test", ClientRequestID: requestID, ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("create tier order %s: %v", orderNo, err)
	}
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: "test", EventID: eventID, ProviderTradeNo: "trade-" + eventID,
		AmountFen: order.AmountFen, Currency: order.Currency, PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("c", 64),
	}
	fulfilled, changed, err := db.FulfillCommercialOrder(order.OrderNo, confirmation)
	if err != nil || !changed || fulfilled.Status != "fulfilled" {
		t.Fatalf("fulfill tier order %s: order=%#v changed=%v err=%v", orderNo, fulfilled, changed, err)
	}
	return fulfilled
}

func testConcurrentCommercialOpenOrderCoalescing(t *testing.T, db *Adapter, uid int64) {
	t.Helper()
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "pg-concurrent-plan", Name: "PostgreSQL 并发下单包", PriceFen: 100, Currency: "CNY",
		SaleState: "test", ModelBudgets: map[string]float64{"gpt-5.6-terra": 1}, DurationDays: 30,
	})
	if err != nil {
		t.Fatalf("create concurrent commercial plan: %v", err)
	}

	const workers = 16
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	start := make(chan struct{})
	orderNos := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			order, createErr := db.CreateCommercialOrder(&types.CommercialOrder{
				OrderNo:         fmt.Sprintf("CCPGCONCURRENT%04d", index),
				UID:             uid,
				PlanID:          planID,
				Channel:         "test",
				ClientRequestID: fmt.Sprintf("pg_concurrent_request_%04d", index),
				ExpiresAt:       &expiresAt,
			})
			if createErr != nil {
				errs <- createErr
				return
			}
			orderNos <- order.OrderNo
		}(i)
	}
	close(start)
	wg.Wait()
	close(orderNos)
	close(errs)
	for createErr := range errs {
		if createErr != nil {
			t.Fatalf("concurrent commercial order failed: %v", createErr)
		}
	}
	var canonical string
	for orderNo := range orderNos {
		if canonical == "" {
			canonical = orderNo
		}
		if orderNo != canonical {
			t.Fatalf("concurrent requests produced multiple open orders: first=%s other=%s", canonical, orderNo)
		}
	}
	if canonical == "" {
		t.Fatal("concurrent requests returned no order")
	}
	var openOrders, aliases int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_orders WHERE uid = $1 AND plan_id = $2 AND status IN ('created','pending')`, uid, planID).Scan(&openOrders); err != nil {
		t.Fatalf("count concurrent open orders: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_order_request_ids WHERE uid = $1 AND order_no = $2`, uid, canonical).Scan(&aliases); err != nil {
		t.Fatalf("count concurrent request aliases: %v", err)
	}
	if openOrders != 1 || aliases != workers {
		t.Fatalf("concurrent order coalescing mismatch: open_orders=%d aliases=%d", openOrders, aliases)
	}
}
