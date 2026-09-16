package postgres

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresCommercialInviteRedeemerUIDs(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_invite_redeemers_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create invite redeemers test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open invite redeemers postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create invite redeemers schema objects: %v", err)
	}

	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "invite-redeemers", Name: "Invite Redeemers", MonthlyBudget: 5, DurationDays: 30,
	})
	if err != nil {
		t.Fatalf("create invite redeemers plan: %v", err)
	}
	firstUID, err := db.CreateUser(&types.User{
		Username: "invite-redeemer-one", Email: "invite-redeemer-one@example.test", DisplayName: "Invite Redeemer One",
		AccountType: types.AccountHuman, PassHash: []byte("invite-redeemer-one-hash"),
	})
	if err != nil {
		t.Fatalf("create first redeemer: %v", err)
	}
	secondUID, err := db.CreateUser(&types.User{
		Username: "invite-redeemer-two", Email: "invite-redeemer-two@example.test", DisplayName: "Invite Redeemer Two",
		AccountType: types.AccountHuman, PassHash: []byte("invite-redeemer-two-hash"),
	})
	if err != nil {
		t.Fatalf("create second redeemer: %v", err)
	}
	if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
		CreateOnly: true, Code: "CATS-REDEEM-MANY", PlanID: planID, MaxRedemptions: 3,
	}); err != nil {
		t.Fatalf("create shared invite code: %v", err)
	}
	if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
		CreateOnly: true, Code: "CATS-REDEEM-NONE", PlanID: planID, MaxRedemptions: 1,
	}); err != nil {
		t.Fatalf("create untouched invite code: %v", err)
	}
	if _, err := db.RedeemCommercialInvite(firstUID, "CATS-REDEEM-MANY"); err != nil {
		t.Fatalf("redeem shared invite as first user: %v", err)
	}
	if _, err := db.RedeemCommercialInvite(secondUID, "CATS-REDEEM-MANY"); err != nil {
		t.Fatalf("redeem shared invite as second user: %v", err)
	}

	invites, err := db.ListCommercialInviteCodes(10)
	if err != nil {
		t.Fatalf("list invite codes: %v", err)
	}
	byCode := map[string]*types.CommercialInviteCode{}
	for _, invite := range invites {
		byCode[invite.Code] = invite
	}
	shared := byCode["CATS-REDEEM-MANY"]
	if shared == nil {
		t.Fatalf("shared invite code missing from list: %#v", invites)
	}
	if shared.RedeemedCount != 2 {
		t.Fatalf("shared redeem count = %d, want 2", shared.RedeemedCount)
	}
	if len(shared.RedeemerUIDs) != 2 || shared.RedeemerUIDs[0] != firstUID || shared.RedeemerUIDs[1] != secondUID {
		t.Fatalf("shared redeemer uids = %v, want [%d %d]", shared.RedeemerUIDs, firstUID, secondUID)
	}
	untouched := byCode["CATS-REDEEM-NONE"]
	if untouched == nil {
		t.Fatalf("untouched invite code missing from list: %#v", invites)
	}
	if len(untouched.RedeemerUIDs) != 0 {
		t.Fatalf("untouched redeemer uids = %v, want empty", untouched.RedeemerUIDs)
	}
}
