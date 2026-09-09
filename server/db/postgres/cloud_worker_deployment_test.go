package postgres

import (
	"fmt"
	"github.com/openchat/openchat/server/store/types"
	"os"
	"testing"
	"time"
)

// Runs inside the real PostgreSQL commercial contract, including migration,
// grant idempotency, typed reservation, invite redemption and admin reads.
func testCloudWorkerDeploymentContract(t *testing.T, db *Adapter) {
	ownerID, err := db.CreateUser(&types.User{Username: "deployment-owner", AccountType: types.AccountHuman, PassHash: []byte("test-hash")})
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(48 * time.Hour)
	if count, err := db.GrantCloudWorkerCredits(ownerID, 1, "paid-default", &expiry); err != nil || count != 1 {
		t.Fatalf("grant default: %d %v", count, err)
	}
	if count, err := db.GrantCloudWorkerProfileCredits(ownerID, 1, "enterprise", &expiry, types.CloudWorkerPublicIP); err != nil || count != 1 {
		t.Fatalf("grant public: %d %v", count, err)
	}
	if count, err := db.GrantCloudWorkerProfileCredits(ownerID, 1, "enterprise", &expiry, types.CloudWorkerPublicIP); err != nil || count != 0 {
		t.Fatalf("idempotent grant: %d %v", count, err)
	}
	if _, err := db.GrantCloudWorkerProfileCredits(ownerID, 1, "enterprise", &expiry, types.CloudWorkerPrivateNAT); err == nil {
		t.Fatal("same operation changed profile")
	}
	profile, reserved, err := db.ReserveCloudWorkerProfileCredit(ownerID, "reserve-public", "")
	if err != nil || !reserved || profile != types.CloudWorkerPublicIP {
		t.Fatalf("public credit priority: %q %v %v", profile, reserved, err)
	}
	profile, reserved, err = db.ReserveCloudWorkerProfileCredit(ownerID, "reserve-private", types.CloudWorkerPrivateNAT)
	if err != nil || !reserved || profile != types.CloudWorkerPrivateNAT {
		t.Fatalf("private credit selection: %q %v %v", profile, reserved, err)
	}
	if err := db.ReleaseCloudWorkerCredit(ownerID, "reserve-public"); err != nil {
		t.Fatal(err)
	}
	_, available, err := db.CloudWorkerProfileCreditSummary(ownerID, types.CloudWorkerPublicIP)
	if err != nil || available != 1 {
		t.Fatalf("released public credit unavailable: %d %v", available, err)
	}
	if err := db.SaveBotConfigWithOwner(ownerID, ownerID, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTenantName(ownerID, "bot-public-profile"); err != nil {
		t.Fatal(err)
	}
	deployment := types.CloudWorkerDeployment{Profile: types.CloudWorkerPublicIP, Env: map[string]string{"CTYUN_WORKER_REGION_ID": "foshan-test"}}
	if err := db.SetCloudWorkerDeployment(ownerID, deployment); err != nil {
		t.Fatal(err)
	}
	value, err := db.GetCloudWorkerDeployment("bot-public-profile")
	if err != nil || value == nil || value.Env["CTYUN_WORKER_REGION_ID"] != "foshan-test" {
		t.Fatalf("deployment roundtrip: %+v %v", value, err)
	}
	all, err := db.ListCloudWorkerDeployments()
	if err != nil || all["bot-public-profile"].Profile != types.CloudWorkerPublicIP {
		t.Fatal("deployment inventory missing")
	}
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{Slug: "enterprise-worker-test", Name: "Enterprise test", Currency: "CNY", SaleState: "hidden", DurationDays: 30, MonthlyBudget: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "PUBLIC-WORKER-TEST", PlanID: planID, MaxRedemptions: 1, CloudWorkerCredits: 2, CloudWorkerProfile: types.CloudWorkerPublicIP}); err != nil {
		t.Fatal(err)
	}
	inviteUser, err := db.CreateUser(&types.User{Username: "invited-worker-owner", AccountType: types.AccountHuman, PassHash: []byte("test-hash")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RedeemCommercialInvite(inviteUser, "PUBLIC-WORKER-TEST"); err != nil {
		t.Fatal(err)
	}
	_, available, err = db.CloudWorkerProfileCreditSummary(inviteUser, types.CloudWorkerPublicIP)
	if err != nil || available != 2 {
		t.Fatalf("invite lost public profile: %d %v", available, err)
	}
	_, privateAvailable, err := db.CloudWorkerProfileCreditSummary(inviteUser, types.CloudWorkerPrivateNAT)
	if err != nil || privateAvailable != 0 {
		t.Fatalf("invite leaked private credits: %d %v", privateAvailable, err)
	}
	invites, err := db.ListCommercialInviteCodes(100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, invite := range invites {
		if invite.Code == "PUBLIC-WORKER-TEST" {
			found = invite.CloudWorkerProfile == types.CloudWorkerPublicIP
		}
	}
	if !found {
		t.Fatal("admin invite list lost profile")
	}
	testCloudWorkerTrialBilling(t, db, ownerID)
}

func testCloudWorkerTrialBilling(t *testing.T, db *Adapter, owner int64) {
	expiry := time.Now().UTC().Add(time.Hour)
	if _, err := db.GrantCloudWorkerConfiguredCredits(owner, 1, "trial-no-expiry", nil, "public_ip", "ondemand"); err == nil {
		t.Fatal("unbounded trial granted")
	}
	if _, err := db.GrantCloudWorkerConfiguredCredits(owner, 1, "trial", &expiry, "public_ip", "ondemand"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GrantCloudWorkerConfiguredCredits(owner, 1, "trial", &expiry, "public_ip", "month"); err == nil {
		t.Fatal("idempotency changed billing mode")
	}
	selection, reserved, err := db.ReserveCloudWorkerConfiguredCredit(owner, "trial-reservation", "public_ip", "ondemand")
	if err != nil || !reserved || selection.BillingMode != "ondemand" {
		t.Fatalf("trial reservation: %+v %v", selection, err)
	}
	worker, err := db.CreateUser(&types.User{Username: "trial-billing-bot", AccountType: types.AccountBot, PassHash: []byte("test-only")})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveBotConfigWithOwner(worker, owner, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = db.SetTenantName(worker, "bot-trial-billing"); err != nil {
		t.Fatal(err)
	}
	if err = db.SetCloudWorkerDeployment(worker, types.CloudWorkerDeployment{Profile: "public_ip", Env: map[string]string{"CTYUN_WORKER_REGION_ID": "foshan", "CTYUN_WORKER_BILLING_MODE": "ondemand"}}); err != nil {
		t.Fatal(err)
	}
	if err = db.CommitCloudWorkerCredit(owner, "trial-reservation", worker, "bot-trial-billing", 15); err != nil {
		t.Fatal(err)
	}
	item, err := db.GetCloudWorkerBillingLifecycle("bot-trial-billing")
	if err != nil {
		t.Fatal(err)
	}
	if item.BillingMode != "ondemand" || item.DeleteAfter.Sub(item.PackageExpiresAt) != 72*time.Hour {
		t.Fatalf("trial retention %+v", item)
	}
	if ok, err := db.ClaimCloudWorkerBillingAction(item.ID, "suspend", true); err != nil || ok {
		t.Fatal("trial stopped before expiry", err)
	}
	if _, err = db.db.Exec(`UPDATE cloud_worker_lifecycles SET package_expires_at=CURRENT_TIMESTAMP-INTERVAL '1 minute',delete_after=CURRENT_TIMESTAMP+INTERVAL '3 days' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	due, err := db.ListCloudWorkerLifecycleDue(time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("production expiry scan: %v", err)
	}
	found := false
	for _, candidate := range due {
		if candidate.ID == item.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("expiry scan omitted expired trial")
	}
	if ok, err := db.ClaimCloudWorkerBillingAction(item.ID, "suspend", true); err != nil || !ok {
		t.Fatal("expiry suspension not claimed", err)
	}
	if ok, _ := db.ClaimCloudWorkerTrialRelease(item.ID); ok {
		t.Fatal("release raced pending stop")
	}
	if err = db.CompleteCloudWorkerBillingAction(item.ID, "suspend", time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	item, err = db.GetCloudWorkerBillingLifecycle(item.TenantName)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != "delete_pending" || time.Until(item.DeleteAfter) < 71*time.Hour {
		t.Fatalf("stopped trial retention %+v", item)
	}
	if ok, err := db.ClaimCloudWorkerLifecycleDeletion(item.ID); err != nil || ok {
		t.Fatal("retention released early", err)
	}
	if ok, err := db.RequestCloudWorkerConversion(item.ID); err != nil || !ok {
		t.Fatal("conversion request", err)
	}
	if _, err = db.db.Exec(`UPDATE cloud_worker_lifecycles SET delete_after=CURRENT_TIMESTAMP-INTERVAL '1 day' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.ClaimCloudWorkerLifecycleDeletion(item.ID); ok {
		t.Fatal("stale deletion overrode conversion intent")
	}
	if ok, _ := db.ClaimCloudWorkerTrialRelease(item.ID); ok {
		t.Fatal("manual release overrode conversion intent")
	}
	if _, err = db.db.Exec(`UPDATE cloud_worker_lifecycles SET billing_action='suspend',billing_action_started_at=CURRENT_TIMESTAMP WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimCloudWorkerBillingAction(item.ID, "convert", false); err != nil || ok {
		t.Fatal("conversion raced a live suspension", err)
	}
	if _, err = db.db.Exec(`UPDATE cloud_worker_lifecycles SET billing_action_started_at=CURRENT_TIMESTAMP-INTERVAL '31 minutes' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimCloudWorkerBillingAction(item.ID, "convert", false); err != nil || !ok {
		t.Fatal("conversion claim", err)
	}
	if err = db.CompleteCloudWorkerBillingAction(item.ID, "convert", time.Now().UTC().AddDate(0, 1, 0), ""); err != nil {
		t.Fatal(err)
	}
	item, err = db.GetCloudWorkerBillingLifecycle(item.TenantName)
	if err != nil {
		t.Fatal(err)
	}
	if item.BillingMode != "month" || item.ConversionPending || item.State != "active" {
		t.Fatalf("conversion completion %+v", item)
	}
	deployment, err := db.GetCloudWorkerDeployment(item.TenantName)
	if err != nil || deployment.Env["CTYUN_WORKER_BILLING_MODE"] != "month" {
		t.Fatal("deployment billing not reconciled", err)
	}
	if ok, _ := db.ClaimCloudWorkerLifecycleDeletion(item.ID); ok {
		t.Fatal("converted monthly worker deleted by stale task")
	}
	plan, err := db.CreateCommercialPlan(&types.CommercialPlan{Slug: "trial-plan", Name: "Trial", SaleState: "hidden", DurationDays: 3, CloudWorkerBillingMode: "ondemand"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "TRIAL-PLAN-INVITE", PlanID: plan, CloudWorkerCredits: 1}); err != nil {
		t.Fatal(err)
	}
	invites, err := db.ListCommercialInviteCodes(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, invite := range invites {
		if invite.Code == "TRIAL-PLAN-INVITE" && invite.CloudWorkerBillingMode != "ondemand" {
			t.Fatal("invite did not inherit plan billing")
		}
	}
}

func TestPostgresCloudWorkerDeploymentContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_worker_profile_%d", time.Now().UnixNano())
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
	testCloudWorkerDeploymentContract(t, db)
}
