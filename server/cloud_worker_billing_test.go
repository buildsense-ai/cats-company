package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type configuredCreditStub struct {
	profileCreditStub
	billing, requestedBilling string
}

func (s *configuredCreditStub) CloudWorkerConfiguredCreditSummary(int64, string, string) (int, int, error) {
	return 1, 1, nil
}
func (s *configuredCreditStub) GrantCloudWorkerConfiguredCredits(int64, int, string, *time.Time, string, string) (int, error) {
	return 1, nil
}
func (s *configuredCreditStub) ReserveCloudWorkerConfiguredCredit(_ int64, _ string, profile, billing string) (types.CloudWorkerCreditSelection, bool, error) {
	s.requested = profile
	s.requestedBilling = billing
	return types.CloudWorkerCreditSelection{Profile: s.profile, BillingMode: s.billing}, true, nil
}

func TestCloudWorkerBillingCannotBeSelectedByFrontend(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	db := &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}}
	h.db = db
	credits := &configuredCreditStub{profileCreditStub: profileCreditStub{quotaCreditStub: quotaCreditStub{total: 1, available: 1}, profile: "private_nat"}, billing: "ondemand"}
	h.credits = credits
	r := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]interface{}{"username": "bot-test", "billing_mode": "month", "deployment_profile": "public_ip"})
	w := httptest.NewRecorder()
	h.HandleCreate(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
	if credits.requestedBilling != "" || credits.requested != "" {
		t.Fatal("frontend selected internal billing or network")
	}
	for _, deployment := range db.deployments {
		if deployment.Env["CTYUN_WORKER_BILLING_MODE"] != "ondemand" {
			t.Fatal("assigned trial billing lost")
		}
	}
}

type billingLifecycleStub struct {
	quotaCreditStub
	completed, claim bool
	errorText        string
}

func (s *billingLifecycleStub) GetCloudWorkerBillingLifecycle(string) (*types.CloudWorkerLifecycle, error) {
	return &types.CloudWorkerLifecycle{ID: 1, BillingMode: "ondemand"}, nil
}
func (s *billingLifecycleStub) RequestCloudWorkerConversion(int64) (bool, error) { return true, nil }
func (s *billingLifecycleStub) ClaimCloudWorkerBillingAction(int64, string, bool) (bool, error) {
	return s.claim, nil
}
func (s *billingLifecycleStub) CompleteCloudWorkerBillingAction(_ int64, _ string, _ time.Time, text string) error {
	s.completed = text == ""
	s.errorText = text
	return nil
}
func (s *billingLifecycleStub) ClaimCloudWorkerTrialRelease(int64) (bool, error) { return false, nil }

func TestCloudWorkerBillingConversionRequiresConfirmedAutoRenewDisable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX billing script contract")
	}
	for _, confirmed := range []string{"false", "true"} {
		t.Run(confirmed, func(t *testing.T) {
			h, _ := newCloudWorkerTestHandler("")
			stub := &billingLifecycleStub{claim: true}
			h.credits = stub
			root := t.TempDir()
			h.renewScript = filepath.Join(root, "renew-worker.sh")
			output := `{"expires_at":"2030-01-01T00:00:00Z","auto_renew_disabled":` + confirmed + `}`
			if err := os.WriteFile(filepath.Join(root, "billing-worker.sh"), []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			err := h.runBillingAction(types.CloudWorkerLifecycle{ID: 1, TenantName: "bot-trial"}, "convert", false)
			if confirmed == "false" {
				if err == nil || stub.completed || !strings.Contains(stub.errorText, "not confirmed") {
					t.Fatal("unconfirmed conversion committed")
				}
			} else if err != nil || !stub.completed {
				t.Fatal("confirmed conversion not committed", err)
			}
		})
	}
}
