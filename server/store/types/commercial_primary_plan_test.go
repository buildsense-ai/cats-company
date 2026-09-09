package types

import (
	"testing"
	"time"
)

func TestPrimaryCommercialEntitlement(t *testing.T) {
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	expiry := now.AddDate(0, 0, 10)
	paid := &CommercialEntitlement{ID: 1, PlanID: 7, Source: "operator", State: "active", StartsAt: now.Add(-time.Hour).Add(713058 * time.Microsecond), ExpiresAt: &expiry}
	free := &CommercialEntitlement{ID: 2, PlanID: 18, Source: "free", State: "active", StartsAt: now.Add(-time.Hour)}
	for _, items := range [][]*CommercialEntitlement{{free, paid}, {paid, free}} {
		if got := PrimaryCommercialEntitlement(items, now); got != paid {
			t.Fatalf("Free hid paid package: %+v", got)
		}
	}
	laterBaseline := *free
	laterBaseline.StartsAt = now.Add(-time.Minute)
	if got := PrimaryCommercialEntitlement([]*CommercialEntitlement{&laterBaseline, paid}, now); got != paid {
		t.Fatal("newer baseline hid explicit package")
	}
	future := *paid
	future.StartsAt = now.Add(time.Hour)
	expired := *paid
	expired.ExpiresAt = &now
	revoked := *paid
	revoked.State = "revoked"
	if got := PrimaryCommercialEntitlement([]*CommercialEntitlement{nil, &future, &expired, &revoked, free}, now); got != free {
		t.Fatal("inactive entitlement selected")
	}
	permanent := *paid
	permanent.ExpiresAt = nil
	if got := PrimaryCommercialEntitlement([]*CommercialEntitlement{free, &permanent}, now); got != &permanent || got.ExpiresAt != nil {
		t.Fatal("permanent package expiry replaced by baseline")
	}
}
