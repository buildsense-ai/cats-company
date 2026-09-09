package postgres

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresCommercialPlanManagement(t *testing.T) {
	db := commercialPolicyTestDB(t)
	uid := createGLM53MigrationUser(t, db, "plan-management")
	makePlan := func(slug string) int64 {
		t.Helper()
		id := seedGLM53MigrationPlan(t, db, slug, slug, `{}`)
		if _, err := db.db.Exec(`UPDATE commercial_plans SET sale_state='hidden' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	grantSQL := `INSERT INTO commercial_quota_grants(uid,plan_id,model,amount_cny,effective_at,expires_at) VALUES ($1,$2,'test-model',1,CURRENT_TIMESTAMP-interval '1 day',$3)`
	entitlementSQL := `INSERT INTO commercial_entitlements(uid,plan_id,starts_at,expires_at) VALUES ($1,$2,$3,$4)`
	orderSQL := `INSERT INTO commercial_orders(order_no,uid,plan_id,plan_slug,plan_name,plan_duration_days,plan_monthly_budget_cny,plan_model_budgets,amount_fen,currency,channel,status,client_request_id) VALUES ($1,$2,$3,'fixture','fixture',30,0,'{}',1,'CNY','test',$4,$1)`
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	t.Run("archive preserves history and rejects stale writes", func(t *testing.T) {
		id := makePlan("archive-history")
		exec(entitlementSQL, uid, id, past.Add(-time.Hour), past)
		exec(grantSQL, uid, id, past)
		exec(orderSQL, "archive-history-order", uid, id, "fulfilled")
		if err := db.DeleteCommercialPlan(id, "archive-history", ""); err != nil {
			t.Fatal(err)
		}
		plans, err := db.ListCommercialPlans(true)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range plans {
			if p.ID == id {
				t.Fatal("archived plan remained in catalog")
			}
		}
		p, err := db.GetCommercialPlan(id)
		if err != nil || p == nil || p.State != 1 {
			t.Fatalf("historical plan missing: %v", err)
		}
		var count int
		if err := db.db.QueryRow(`SELECT (SELECT count(*) FROM commercial_entitlements WHERE plan_id=$1)+(SELECT count(*) FROM commercial_quota_grants WHERE plan_id=$1)+(SELECT count(*) FROM commercial_orders WHERE plan_id=$1)`, id).Scan(&count); err != nil || count != 3 {
			t.Fatalf("history lost: %d %v", count, err)
		}
		if _, err := db.CreateCommercialPlan(&types.CommercialPlan{Slug: "archive-history", Name: "stale editor"}); err == nil {
			t.Fatal("stale editor resurrected plan")
		}
		if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "ARCHIVED-PLAN", PlanID: id, MaxRedemptions: 1}); err == nil {
			t.Fatal("created invite for archived plan")
		}
		if _, err := db.GrantCommercialQuota(&types.CommercialQuotaGrant{UID: uid, PlanID: id, AmountCNY: 1}); err == nil {
			t.Fatal("created grant for archived plan")
		}
		if err := db.DeleteCommercialPlan(id, "archive-history", ""); !errors.Is(err, types.ErrCommercialPlanNotFound) {
			t.Fatalf("second delete: %v", err)
		}
	})
	t.Run("protect sale active future invitations orders and system plans", func(t *testing.T) {
		for _, kind := range []string{"public", "test", "active", "grant", "future", "invite", "order", "trial", "system"} {
			t.Run(kind, func(t *testing.T) {
				slug := "protected-" + kind
				if kind == "system" {
					slug = "catsco-free"
				}
				id := makePlan(slug)
				trial := ""
				switch kind {
				case "public", "test":
					exec(`UPDATE commercial_plans SET sale_state=$2 WHERE id=$1`, id, kind)
				case "active":
					exec(entitlementSQL, uid, id, past, future)
				case "grant":
					exec(grantSQL, uid, id, future)
				case "future":
					exec(entitlementSQL, uid, id, future, future.Add(time.Hour))
				case "invite":
					if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "VALID-INVITE", PlanID: id, MaxRedemptions: 1}); err != nil {
						t.Fatal(err)
					}
				case "order":
					exec(orderSQL, "protected-order", uid, id, "paid")
				case "trial":
					trial = slug
				}
				usage, err := db.ListCommercialPlanUsage([]int64{id}, trial)
				if err != nil || len(usage) != 1 || usage[0].CanDelete || usage[0].DeleteBlockedBy == "" {
					t.Fatalf("usage guard: %+v %v", usage, err)
				}
				if err := db.DeleteCommercialPlan(id, slug, trial); !errors.Is(err, types.ErrCommercialPlanDeleteConflict) {
					t.Fatalf("delete accepted: %v", err)
				}
			})
		}
	})
	t.Run("deduplicate and keyset paginate only current users", func(t *testing.T) {
		id := makePlan("users-pagination")
		for i := 0; i < 31; i++ {
			user := createGLM53MigrationUser(t, db, fmt.Sprintf("plan-user-%d", i))
			exec(entitlementSQL, user, id, past, future)
			exec(grantSQL, user, id, future)
		}
		exec(entitlementSQL, uid, id, past.Add(-time.Hour), past)
		exec(entitlementSQL, uid, id, future, future.Add(time.Hour))
		usage, err := db.ListCommercialPlanUsage([]int64{id}, "")
		if err != nil || len(usage) != 1 || usage[0].ActiveUsers != 31 || usage[0].ScheduledUsers != 1 {
			t.Fatalf("counts: %+v %v", usage, err)
		}
		first, err := db.ListCommercialPlanUsers(id, 0, 25)
		if err != nil || len(first.UIDs) != 25 || !first.HasMore {
			t.Fatalf("first page: %+v %v", first, err)
		}
		second, err := db.ListCommercialPlanUsers(id, first.NextUID, 25)
		if err != nil || len(second.UIDs) != 6 || second.HasMore {
			t.Fatalf("second page: %+v %v", second, err)
		}
		if second.UIDs[0] <= first.NextUID {
			t.Fatal("duplicate user between pages")
		}
	})
	t.Run("concurrent grant protects plan", func(t *testing.T) {
		id := makePlan("concurrent-grant")
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(grantSQL, uid, id, future); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- db.DeleteCommercialPlan(id, "concurrent-grant", "") }()
		select {
		case err := <-done:
			t.Fatalf("archive bypassed uncommitted grant: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, types.ErrCommercialPlanDeleteConflict) {
				t.Fatalf("grant did not protect plan: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("archive blocked")
		}
	})
	t.Run("permanent internal package through operator and invite", func(t *testing.T) {
		plan := &types.CommercialPlan{Slug: "internal-permanent", Name: "Internal permanent", SaleState: "hidden", DurationDays: -1, ModelBudgets: map[string]float64{"gpt-5.6-terra": 20000, "gpt-5.6-sol": 30000}}
		id, err := db.CreateCommercialPlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		user := createGLM53MigrationUser(t, db, "permanent-user")
		prior := makePlan("prior-package")
		exec(entitlementSQL, user, prior, past, future)
		applied, err := db.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{UID: user, Action: "change_plan", PlanID: id, OperationID: "permanent-change", EffectiveAt: time.Now().UTC()})
		if err != nil || !applied.Applied || applied.NextTotalCNY != 50000 || applied.ExpiresAt != nil {
			t.Fatalf("permanent assignment: %+v %v", applied, err)
		}
		check := func(userID int64) {
			t.Helper()
			summary, err := db.GetCommercialSummary(userID)
			if err != nil {
				t.Fatal(err)
			}
			if summary.TotalCNY != 50000 {
				t.Fatalf("total=%v", summary.TotalCNY)
			}
			for _, g := range summary.Grants {
				if g.PlanID == id && (g.ExpiresAt != nil || g.ResetDuration != "1M") {
					t.Fatalf("grant not monthly/permanent: %+v", g)
				}
			}
			if p := types.PrimaryCommercialEntitlement(summary.Entitlements, time.Now().UTC()); p == nil || p.PlanID != id || p.ExpiresAt != nil {
				t.Fatalf("wrong permanent primary: %+v", p)
			}
		}
		check(user)
		if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "PERMANENT-INVITE", PlanID: id, MaxRedemptions: 1}); err != nil {
			t.Fatal(err)
		}
		invited := createGLM53MigrationUser(t, db, "permanent-invited")
		if _, err := db.RedeemCommercialInvite(invited, "PERMANENT-INVITE"); err != nil {
			t.Fatal(err)
		}
		check(invited)
		plan.Slug = "public-permanent"
		plan.SaleState = "public"
		if _, err := db.CreateCommercialPlan(plan); err == nil {
			t.Fatal("public permanent plan allowed")
		}
	})
}
