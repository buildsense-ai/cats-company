package postgres

import (
	"fmt"
	"testing"
	"time"
)

// Exercises the real summary queries consumed by peer eligibility, catalog and
// selection. No production DSN: commercialPolicyTestDB requires CATS_PG_TEST_DSN
// and creates/drops its own isolated schema.
func TestPostgresCommercialSummaryLegacyGrantValidity(t *testing.T) {
	db := commercialPolicyTestDB(t)
	planID := seedGLM53MigrationPlan(t, db, "legacy-validity", "Legacy", `{}`)
	now := time.Now().UTC().Truncate(time.Second)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, source := range []string{"order", "invite", "operator_plan", "trial", "free", "legacy"} {
		for _, state := range []string{"valid", "future_entitlement", "expired_entitlement", "revoked_entitlement", "missing_entitlement", "future_grant", "expired_grant", "revoked_grant"} {
			t.Run(source+"/"+state, func(t *testing.T) {
				uid := createGLM53MigrationUser(t, db, source+"-"+state)
				entStart, entEnd := past, future
				entState := "active"
				switch state {
				case "future_entitlement":
					entStart, entEnd = future, future.Add(time.Hour)
				case "expired_entitlement":
					entStart, entEnd = past.Add(-time.Hour), past
				case "revoked_entitlement":
					entState = "revoked"
				}
				entSource := source
				if source == "operator_plan" {
					entSource = "operator"
				}
				if state != "missing_entitlement" {
					if _, err := db.db.Exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at)
						VALUES ($1,$2,$3,'original',$4,$5,$6)`, uid, planID, entSource, entState, entStart, entEnd); err != nil {
						t.Fatal(err)
					}
				}
				// A different purchase of the same plan is valid. It must never
				// restore the original package's expired/revoked/orphan grant.
				// A trial is unique per user and cannot be purchased twice.
				if source != "trial" {
					if _, err := db.db.Exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at)
						VALUES ($1,$2,$3,'new-purchase','active',$4,$5)`, uid, planID, entSource, past, future); err != nil {
						t.Fatal(err)
					}
				}
				grantStart, grantEnd := past, future
				var revoked interface{}
				if state == "future_grant" {
					grantStart, grantEnd = future, future.Add(time.Hour)
				}
				if state == "expired_grant" {
					grantStart, grantEnd = past.Add(-time.Hour), past
				}
				if state == "revoked_grant" {
					revoked = past
				}
				if _, err := db.db.Exec(`INSERT INTO commercial_quota_grants(uid,plan_id,grant_type,source_ref,model,amount_cny,effective_at,expires_at,revoked_at)
					VALUES ($1,$2,$3,'original','MiniMax-M3',500,$4,$5,$6)`, uid, planID, source, grantStart, grantEnd, revoked); err != nil {
					t.Fatal(err)
				}
				summary, err := db.GetCommercialSummary(uid)
				if err != nil {
					t.Fatal(err)
				}
				want := 0.0
				if state == "valid" {
					want = 500
				}
				if summary.TotalCNY != want || summary.TotalsByModel["MiniMax-M3"] != want || len(summary.Grants) != int(want/500) {
					t.Fatalf("%s/%s summary=%#v, want quota %v", source, state, summary, want)
				}
				for _, ent := range summary.Entitlements {
					if ent.SourceRef == "original" && state != "valid" && state != "future_grant" && state != "expired_grant" && state != "revoked_grant" {
						t.Fatalf("ineffective entitlement leaked: %#v", ent)
					}
				}
			})
		}
	}
}

func TestPostgresCommercialSummaryTokenQuotaStartsAtAndRevocation(t *testing.T) {
	db := commercialPolicyTestDB(t)
	planID := seedGLM53MigrationPlan(t, db, "token-validity", "Token", `{}`)
	if _, err := db.db.Exec(`INSERT INTO commercial_plan_token_quotas(plan_id,token_quota,reset_duration) VALUES ($1,1000000,'1M')`, planID); err != nil {
		t.Fatal(err)
	}
	for index, state := range []string{"valid", "future", "expired", "revoked"} {
		t.Run(state, func(t *testing.T) {
			uid := createGLM53MigrationUser(t, db, fmt.Sprintf("token-validity-%d", index))
			now := time.Now().UTC()
			start, end, entState := now.Add(-time.Hour), now.Add(time.Hour), "active"
			if state == "future" {
				start, end = now.Add(time.Hour), now.Add(2*time.Hour)
			}
			if state == "expired" {
				start, end = now.Add(-2*time.Hour), now.Add(-time.Hour)
			}
			if state == "revoked" {
				entState = "revoked"
			}
			if _, err := db.db.Exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at)
				VALUES ($1,$2,'operator','token',$3,$4,$5)`, uid, planID, entState, start, end); err != nil {
				t.Fatal(err)
			}
			summary, err := db.GetCommercialSummary(uid)
			if err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if state == "valid" {
				want = 1000000
			}
			if summary.TokenQuota != want {
				t.Fatalf("%s token quota=%d want %d", state, summary.TokenQuota, want)
			}
		})
	}
}

func TestPostgresCommercialSummaryHistoricalBaselineSupplements(t *testing.T) {
	db := commercialPolicyTestDB(t)
	for _, source := range []string{"free", "legacy"} {
		planSlug, grantRef, entitlementRef := "catsco-free", "catsco-free-v2", "catsco-free-v1"
		if source == "legacy" {
			planSlug, grantRef, entitlementRef = "catsco-legacy-custom", "independent-model-supplement", "relay-quota-migration-v1"
		}
		planID := seedGLM53MigrationPlan(t, db, planSlug, source, `{}`)
		for _, state := range []string{"valid", "future", "expired", "revoked", "missing", "different_lifetime", "original_revoked", "unrelated_reference", "grant_revoked"} {
			t.Run(source+"/"+state, func(t *testing.T) {
				uid := createGLM53MigrationUser(t, db, source+"-supplement-"+state)
				now := time.Now().UTC().Truncate(time.Second)
				start, end := now.Add(-time.Hour), now.Add(time.Hour)
				entStart, entEnd, entState, entRef := start, end, "active", entitlementRef
				switch state {
				case "future":
					entStart = now.Add(time.Minute)
				case "expired":
					entEnd = now.Add(-time.Minute)
				case "revoked":
					entState = "revoked"
				case "different_lifetime":
					entEnd = end.Add(time.Hour)
				case "unrelated_reference":
					entRef = "new-purchase"
				}
				if state != "missing" {
					if _, err := db.db.Exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, uid, planID, source, entRef, entState, entStart, entEnd); err != nil {
						t.Fatal(err)
					}
				}
				if state == "original_revoked" {
					if _, err := db.db.Exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at) VALUES ($1,$2,$3,$4,'revoked',$5,$6)`, uid, planID, source, grantRef, start, end); err != nil {
						t.Fatal(err)
					}
				}
				var revoked interface{}
				if state == "grant_revoked" {
					revoked = start
				}
				grantStart := start
				if source == "free" {
					grantStart = start.Add(time.Minute)
				}
				if _, err := db.db.Exec(`INSERT INTO commercial_quota_grants(uid,plan_id,grant_type,source_ref,model,amount_cny,effective_at,expires_at,revoked_at) VALUES ($1,$2,$3,$4,'MiniMax-M3',500,$5,$6,$7)`, uid, planID, source, grantRef, grantStart, end, revoked); err != nil {
					t.Fatal(err)
				}
				summary, err := db.GetCommercialSummary(uid)
				if err != nil {
					t.Fatal(err)
				}
				want := 0.0
				if state == "valid" {
					want = 500
				}
				if summary.TotalCNY != want {
					t.Fatalf("supplement total=%v want=%v", summary.TotalCNY, want)
				}
			})
		}
	}
}
