package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresCommercialTrialDatabaseClock(t *testing.T) {
	db := newCommercialRefundSafetyAdapter(t)
	uid, err := db.CreateUser(&types.User{
		Username: "trial-clock-user", Email: "trial-clock@example.test", DisplayName: "Trial clock",
		AccountType: types.AccountHuman, PassHash: []byte("trial-clock-hash"),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const slug = "pg-trial-clock"
	if _, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: slug, Name: "Trial clock", Currency: "CNY", SaleState: "hidden",
		ModelBudgets: map[string]float64{"deepseek-v4-flash": 5}, DurationDays: 7,
	}); err != nil {
		t.Fatalf("create trial plan: %v", err)
	}

	summary, err := db.ClaimCommercialTrial(uid, slug)
	if err != nil {
		t.Fatalf("claim trial: %v", err)
	}
	if len(summary.Entitlements) != 1 || summary.Entitlements[0].SourceRef != slug || summary.TotalsByModel["deepseek-v4-flash"] != 5 {
		t.Fatal("trial entitlement and quota must be visible immediately after claim")
	}
	var starts, created, expires time.Time
	if err := db.db.QueryRow(`SELECT starts_at, created_at, expires_at
		FROM commercial_entitlements WHERE uid = $1 AND source = 'trial'`, uid).Scan(&starts, &created, &expires); err != nil {
		t.Fatalf("read trial timestamps: %v", err)
	}
	if !starts.Equal(created) || expires.Sub(starts) != 7*24*time.Hour {
		t.Fatalf("trial timing mismatch: starts=%s created=%s expires=%s", starts, created, expires)
	}
	if _, err := db.ClaimCommercialTrial(uid, slug); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("duplicate trial claim: %v", err)
	}
}
