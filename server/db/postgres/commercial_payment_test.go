package postgres

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

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
		ClientRequestID: "pg_paid_request_0002",
		ExpiresAt:       &expiresAt,
	}); err == nil || !strings.Contains(err.Error(), "purchase limit") {
		t.Fatalf("expected purchase limit rejection, got %v", err)
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
}
