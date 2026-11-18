package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestGPT62GrantBudgetGatePreservesPool(t *testing.T) {
	for slug, total := range map[string]float64{"catsco-personal": 11000, "catsco-pro": 33000, "catsco-internal-all-models-50k": 50006} {
		t.Run(slug, func(t *testing.T) {
			sellable := []string{"gpt-6.1-sol", "MiniMax-M3"}
			base := map[string]float64{sellable[0]: total - 100, sellable[1]: 100}
			withModel := reconcilePlanModelBudgetsWithModel(base, sellable, false, types.GPT62Sol)
			if withModel[types.GPT62Sol] <= 0 || withModel[sellable[0]] <= 0 {
				t.Fatalf("missing model: %v", withModel)
			}
			for _, eligible := range []bool{false, true} {
				db := &Adapter{}
				db.SetGPT62CatalogGate(func() bool { return eligible })
				for _, plan := range []string{slug, "catsco-trial-custom", "catsco-free", "catsco-pro-v2"} {
					for _, active := range []bool{false, true} {
						budgets := db.gpt62GrantBudgets(withModel, plan, active)
						if (budgets[types.GPT62Sol] > 0) != (eligible && plan == slug && active) {
							t.Fatalf("plan=%s active=%v catalog=%v budgets=%v", plan, active, eligible, budgets)
						}
						if sum := roundBudget(budgets[sellable[0]] + budgets[sellable[1]] + budgets[types.GPT62Sol]); sum != total {
							t.Fatalf("pool=%v want=%v", sum, total)
						}
					}
				}
			}
			withoutModel := reconcilePlanModelBudgets(withModel, sellable, false)
			if withoutModel[types.GPT62Sol] != 0 || roundBudget(withoutModel[sellable[0]]+withoutModel[sellable[1]]) != total {
				t.Fatalf("bad removal: %v", withoutModel)
			}
		})
	}
}

func TestPostgresGPT62PlanReconcile(t *testing.T) {
	db := commercialPolicyTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	type fixture struct {
		id, uid  int64
		total    float64
		eligible bool
	}
	var plans []fixture
	for _, item := range []struct {
		slug     string
		total    float64
		eligible bool
	}{
		{"catsco-personal", 11000, true}, {"catsco-pro", 33000, true}, {"catsco-internal-all-models-50k", 50006, true}, {"catsco-trial-custom-gpt62", 3299, false},
	} {
		id := seedGLM53MigrationPlan(t, db, item.slug, item.slug, fmt.Sprintf(`{"gpt-6.1-sol":%v}`, item.total))
		uid := createGLM53MigrationUser(t, db, item.slug)
		if _, err := db.db.Exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at) VALUES($1,$2,'order','fixture','active',$3,$4)`, uid, id, start, end); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO commercial_quota_grants(uid,plan_id,grant_type,model,amount_cny,effective_at,expires_at,source_ref) VALUES($1,$2,'order','gpt-6.1-sol',$3,$4,$5,'fixture')`, uid, id, item.total, start, end); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, fixture{id, uid, item.total, item.eligible})
	}
	for _, phase := range []struct {
		sellable, internal []string
		allowed            bool
	}{
		{[]string{"gpt-6.1-sol"}, []string{types.GPT62Sol}, true},
		{[]string{"gpt-6.1-sol"}, []string{types.GPT62Sol}, true},
		{[]string{"gpt-6.1-sol", types.GPT62Sol}, []string{types.GPT62Sol}, false},
		{[]string{"gpt-6.1-sol"}, nil, false},
	} {
		if err := db.ReconcileCommercialPlanModelsWithInternal(context.Background(), phase.sellable, phase.internal, true); err != nil {
			t.Fatal(err)
		}
		for _, plan := range plans {
			var raw []byte
			if err := db.db.QueryRow(`SELECT model_budgets FROM commercial_plans WHERE id=$1`, plan.id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			budgets := decodeModelBudgets(raw)
			want := phase.allowed && plan.eligible
			if (budgets[types.GPT62Sol] > 0) != want || roundBudget(budgets[types.GPT62Sol]+budgets["gpt-6.1-sol"]) != plan.total {
				t.Fatalf("plan=%d budgets=%v want allowed=%v total=%v", plan.id, budgets, want, plan.total)
			}
			summary, err := db.GetCommercialSummary(plan.uid)
			if err != nil {
				t.Fatal(err)
			}
			if (summary.TotalsByModel[types.GPT62Sol] > 0) != want || roundBudget(summary.TotalCNY) != plan.total {
				t.Fatalf("uid=%d grants=%v total=%v", plan.uid, summary.TotalsByModel, summary.TotalCNY)
			}
			for _, grant := range summary.Grants {
				if !grant.EffectiveAt.Equal(start) || grant.ExpiresAt == nil || !grant.ExpiresAt.Equal(end) {
					t.Fatalf("changed package term: %+v", grant)
				}
			}
		}
	}
}
