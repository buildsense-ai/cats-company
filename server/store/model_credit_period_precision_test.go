package store

import (
	"testing"
	"time"
)

func TestModelCreditMonthlyGrantKeyPreservesPeriodInstant(t *testing.T) {
	start := time.Date(2026, time.November, 13, 13, 47, 29, 123456000, time.UTC)
	key := ModelCreditMonthlyGrantKey(42, 7, start)
	parsed, ok := ParseModelCreditMonthlyGrantKey(42, key)
	if !ok || !parsed.Equal(start) {
		t.Fatalf("monthly period identity lost its instant: key=%q got=%s want=%s", key, parsed.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
	}
	later := start.Add(time.Hour)
	if key == ModelCreditMonthlyGrantKey(42, 7, later) {
		t.Fatal("distinct subscription periods on the same day share a grant identity")
	}
	if ModelCreditMonthlyExpireKey(42, start) == ModelCreditMonthlyExpireKey(42, later) {
		t.Fatal("distinct subscription periods on the same day share an expiry identity")
	}
}
