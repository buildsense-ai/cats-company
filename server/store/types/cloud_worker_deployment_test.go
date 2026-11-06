package types

import "testing"

// A packed worker runs in a container the cluster already owns. There is no
// cloud subscription to buy, convert or suspend, so the month/ondemand
// dimension must collapse to month before anything persists it.
//
// Leaving "ondemand" on a shared worker is not cosmetic: the expiry sweeper
// routes on-demand lifecycles into the ECS billing scripts (convert a trial,
// suspend at trial expiry). Those scripts do not exist on the packed path, so
// the sweep fails every minute and never reaches the expire/archive step — the
// worker never expires at all.
func TestCloudWorkerBillingForProfileCollapsesSharedFleet(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		billing string
		want    string
	}{
		{"shared ignores ondemand", CloudWorkerPackedShared, CloudWorkerOnDemand, CloudWorkerMonthly},
		{"shared keeps month", CloudWorkerPackedShared, CloudWorkerMonthly, CloudWorkerMonthly},
		{"shared empty defaults to month", CloudWorkerPackedShared, "", CloudWorkerMonthly},
		{"dedicated keeps ondemand", CloudWorkerPrivateNAT, CloudWorkerOnDemand, CloudWorkerOnDemand},
		{"public keeps ondemand", CloudWorkerPublicIP, CloudWorkerOnDemand, CloudWorkerOnDemand},
		{"dedicated empty defaults to month", CloudWorkerPrivateNAT, "", CloudWorkerMonthly},
		// The helper collapses the shared fleet; it is not a validator, so an
		// unknown billing mode passes through for the caller to reject.
		{"unknown billing passes through for validation", "mystery", "hashrate", "hashrate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CloudWorkerBillingForProfile(tc.profile, tc.billing); got != tc.want {
				t.Fatalf("CloudWorkerBillingForProfile(%q,%q)=%q want %q", tc.profile, tc.billing, got, tc.want)
			}
		})
	}
}
