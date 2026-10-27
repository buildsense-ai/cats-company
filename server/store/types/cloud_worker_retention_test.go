package types

import "testing"

// Product rule (2026-10-27): a 7-day package keeps a customer's data for 3 days
// after expiry, a monthly package for 7 days — the same shape as the ECS
// monthly instance retention window. An explicit plan value always wins.
func TestCloudWorkerRetentionDays(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		duration   int
		want       int
	}{
		{name: "7-day package keeps 3 days", configured: 0, duration: 7, want: 3},
		{name: "1-day trial keeps 3 days", configured: 0, duration: 1, want: 3},
		{name: "30-day package keeps 7 days", configured: 0, duration: 30, want: 7},
		{name: "90-day package keeps 7 days", configured: 0, duration: 90, want: 7},
		// Unknown package length must not shorten a customer's recovery window.
		{name: "unknown duration keeps the legacy 15 days", configured: 0, duration: 0, want: 15},
		{name: "negative duration keeps the legacy 15 days", configured: 0, duration: -1, want: 15},
		// An operator-set value overrides the derived rule.
		{name: "explicit plan value wins", configured: 10, duration: 7, want: 10},
		{name: "explicit value is capped at 90", configured: 200, duration: 30, want: 90},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CloudWorkerRetentionDays(tc.configured, tc.duration); got != tc.want {
				t.Fatalf("retention=%d want %d (configured=%d duration=%d)", got, tc.want, tc.configured, tc.duration)
			}
		})
	}
}
