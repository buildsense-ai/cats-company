package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// A lateral plan change must not buy another provider month, while a reopen or
// forward move has to resume the provider subscription.
func TestCommercialAdjustmentRenewsCloudWorkersGating(t *testing.T) {
	now := time.Now().UTC()
	oldExpiry := now.Add(10 * 24 * time.Hour)
	laterExpiry := now.Add(60 * 24 * time.Hour)
	forwardExpiry := now.Add(40 * 24 * time.Hour)
	cases := []struct {
		name    string
		action  string
		result  *types.CommercialAccountAdjustmentResult
		preview *commercialAdjustmentPreview
		want    bool
	}{
		{
			name: "extend always renews", action: commercialAdjustmentExtend,
			result:  &types.CommercialAccountAdjustmentResult{Applied: true, ExpiresAt: &forwardExpiry},
			preview: &commercialAdjustmentPreview{}, want: true,
		},
		{
			name: "reopen without previous paid window", action: commercialAdjustmentChangePlan,
			result:  &types.CommercialAccountAdjustmentResult{Applied: true, ExpiresAt: &forwardExpiry},
			preview: &commercialAdjustmentPreview{}, want: true,
		},
		{
			name: "forward plan change", action: commercialAdjustmentChangePlan,
			result:  &types.CommercialAccountAdjustmentResult{Applied: true, ExpiresAt: &forwardExpiry},
			preview: &commercialAdjustmentPreview{PreviousExpiresAt: &oldExpiry}, want: true,
		},
		{
			name: "lateral plan change does not buy a month", action: commercialAdjustmentChangePlan,
			result:  &types.CommercialAccountAdjustmentResult{Applied: true, ExpiresAt: &oldExpiry},
			preview: &commercialAdjustmentPreview{PreviousExpiresAt: &laterExpiry}, want: false,
		},
		{
			name: "quota adjustments never renew", action: commercialAdjustmentIncrease,
			result:  &types.CommercialAccountAdjustmentResult{Applied: true, ExpiresAt: &forwardExpiry},
			preview: &commercialAdjustmentPreview{}, want: false,
		},
		{
			name: "idempotent replay stays local", action: commercialAdjustmentExtend,
			result:  &types.CommercialAccountAdjustmentResult{Applied: false},
			preview: &commercialAdjustmentPreview{}, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commercialAdjustmentRenewsCloudWorkers(tc.action, tc.result, tc.preview); got != tc.want {
				t.Fatalf("renew=%v want %v", got, tc.want)
			}
		})
	}
}
