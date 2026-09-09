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
