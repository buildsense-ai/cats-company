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
	expiresAt                 *time.Time
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
	return types.CloudWorkerCreditSelection{Profile: s.profile, BillingMode: s.billing, ExpiresAt: s.expiresAt}, true, nil
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

// The packed adapter directory carries route.sh and the routed verbs, but not
// billing-worker.sh. Repointing renew at it therefore moved the billing-script
// lookup to a directory that never has the file, and every trial conversion or
// expiry suspend failed with "no such file or directory" — a paid customer's
// trial could not be converted and an expired trial kept billing on demand.
//
// These cases pin the resolution order that makes that impossible: explicit
// config, then next to renew (the pre-adapter layout), then the image's ops
// directory.
func TestResolveCloudWorkerBillingScriptPrefersExplicitConfig(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "explicit.sh")
	got, err := resolveCloudWorkerBillingScript(configured, "/nowhere/renew.sh", "/nowhere/ops")
	if err != nil || got != configured {
		t.Fatalf("explicit config not honoured: got=%q err=%v", got, err)
	}
}

func TestResolveCloudWorkerBillingScriptUsesRenewDirectoryWhenPresent(t *testing.T) {
	root := t.TempDir()
	next := filepath.Join(root, "billing-worker.sh")
	if err := os.WriteFile(next, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	ops := t.TempDir()
	if err := os.WriteFile(filepath.Join(ops, "billing-worker.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveCloudWorkerBillingScript("", filepath.Join(root, "renew.sh"), ops)
	if err != nil || got != next {
		t.Fatalf("renew directory must win over ops: got=%q err=%v", got, err)
	}
}

// This is the layout production actually has today: renew lives in the packed
// adapter directory with no billing script beside it, and the image provides it
// under the ops directory.
func TestResolveCloudWorkerBillingScriptFallsBackToOpsDirectory(t *testing.T) {
	packed := t.TempDir()
	ops := t.TempDir()
	want := filepath.Join(ops, "billing-worker.sh")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveCloudWorkerBillingScript("", filepath.Join(packed, "renew.sh"), ops)
	if err != nil || got != want {
		t.Fatalf("ops fallback not used: got=%q err=%v", got, err)
	}
}

func TestResolveCloudWorkerBillingScriptNamesEveryCandidateWhenMissing(t *testing.T) {
	packed := t.TempDir()
	ops := t.TempDir()
	_, err := resolveCloudWorkerBillingScript("", filepath.Join(packed, "renew.sh"), ops)
	if err == nil {
		t.Fatal("missing script must be an error, not a silent skip")
	}
	for _, want := range []string{
		filepath.Join(packed, "billing-worker.sh"),
		filepath.Join(ops, "billing-worker.sh"),
		"CATSCO_WORKER_BILLING_SCRIPT",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q so an operator can fix it, got: %v", want, err)
		}
	}
}

// A missing script must release the claim: nothing reached the provider, so the
// next sweep or payment has to be able to retry instead of waiting out the
// 30-minute claim window.
func TestCloudWorkerBillingMissingScriptReleasesClaim(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	stub := &billingLifecycleStub{claim: true}
	h.credits = stub
	packed := t.TempDir()
	h.renewScript = filepath.Join(packed, "renew.sh")
	t.Setenv("CATSCO_WORKER_BILLING_SCRIPT", "")
	err := h.runBillingAction(types.CloudWorkerLifecycle{ID: 1, TenantName: "bot-trial"}, "convert", false)
	if err == nil {
		t.Fatal("missing billing script must fail")
	}
	if stub.completed {
		t.Fatal("a failed run must be recorded as an error, not a success")
	}
	if !strings.Contains(stub.errorText, "billing script") {
		t.Fatalf("claim was not released with the reason: %q", stub.errorText)
	}
}
