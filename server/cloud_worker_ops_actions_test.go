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

// renewScriptFixture writes an executable script that prints one provider JSON
// line, in whichever form the local OS can run. Renewal assertions read that
// line, so the fixture keeps them portable instead of skipping on Windows.
func renewScriptFixture(t *testing.T, jsonLine string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "renew-worker.cmd")
		if err := os.WriteFile(script, []byte("@echo off\r\necho "+jsonLine+"\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	script := filepath.Join(dir, "renew-worker.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '"+jsonLine+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestCloudWorkerAdminProvisionAdditionalBypassesDuplicateGuard pins the
// operator escape hatch: internal accounts sometimes run several bots, so the
// console can ask for one more instance explicitly. Without the flag the
// duplicate guard must keep rejecting the same request (covered by
// TestCloudWorkerAdminProvisionRejectsAccountWithExistingWorker).
func TestCloudWorkerAdminProvisionAdditionalBypassesDuplicateGuard(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1611), "username": "bot-1601-abcdef", "tenant_name": "bot-1601-abcdef"},
	}
	body := `{"uid":1601,"source_ref":"add-guard-1","deployment_profile":"private_nat","billing_mode":"month","additional":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud-workers/provision", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAdminProvision(rec, req)

	if rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "cloud_worker_exists") {
		t.Fatalf("additional=true must not hit the duplicate guard: %s", rec.Body.String())
	}
}

// TestCloudWorkerAdminProvisionAdditionalSkipsOwnershipLookup keeps the flag
// usable when the roster itself is unreadable: the guard it replaces is the only
// reason the ownership list is read, and an account that legitimately owns
// several workers must not be blocked by a lookup it does not need.
func TestCloudWorkerAdminProvisionAdditionalSkipsOwnershipLookup(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("")
	ts.listBotsErr = errWorkerLookupUnavailable
	body := `{"uid":1601,"source_ref":"add-guard-2","deployment_profile":"private_nat","billing_mode":"month","additional":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud-workers/provision", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAdminProvision(rec, req)

	if rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), "failed to check existing cloud workers") {
		t.Fatalf("additional=true must not require the ownership lookup: %s", rec.Body.String())
	}
}

// renewLifecycleStub reports one monthly lifecycle and records the expiry a
// renewal writes back.
type renewLifecycleStub struct {
	quotaCreditStub
	item     types.CloudWorkerLifecycle
	extended *time.Time
	grace    int
}

func (s *renewLifecycleStub) GetCloudWorkerBillingLifecycle(string) (*types.CloudWorkerLifecycle, error) {
	item := s.item
	return &item, nil
}
func (s *renewLifecycleStub) RequestCloudWorkerConversion(int64) (bool, error) { return true, nil }
func (s *renewLifecycleStub) ClaimCloudWorkerBillingAction(int64, string, bool) (bool, error) {
	return true, nil
}
func (s *renewLifecycleStub) CompleteCloudWorkerBillingAction(int64, string, time.Time, string) error {
	return nil
}
func (s *renewLifecycleStub) ClaimCloudWorkerTrialRelease(int64) (bool, error) { return false, nil }
func (s *renewLifecycleStub) ExtendCloudWorkerLifecycle(_ int64, expiresAt time.Time, graceDays int) error {
	value := expiresAt
	s.extended = &value
	s.grace = graceDays
	return nil
}

func postBilling(t *testing.T, h *CloudWorkerHandler, tenant, action string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"tenant_name":"` + tenant + `","action":"` + action + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud-workers/billing", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleAdminBilling(rec, req)
	return rec
}

// TestCloudWorkerAdminBillingRejectsRenewingTrials keeps renewal off the trial
// billing machine: a trial buys its month through convert, which claims the
// conversion before ordering. Renewing it here would leave a paid instance on
// the on-demand lifecycle path.
func TestCloudWorkerAdminBillingRejectsRenewingTrials(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.renewScript = "unused-renew.sh"
	h.credits = &renewLifecycleStub{item: types.CloudWorkerLifecycle{
		ID: 9, TenantName: "bot-trial", OwnerUID: 813, State: "active", BillingMode: types.CloudWorkerOnDemand,
	}}

	rec := postBilling(t, h, "bot-trial", "renew")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want %d (body=%s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cloud_worker_renew_requires_monthly") {
		t.Fatalf("expected renew_requires_monthly code, got %s", rec.Body.String())
	}
}

// TestCloudWorkerAdminBillingRejectsRenewDuringAnotherAction makes sure a
// renewal cannot race an in-flight conversion or shutdown.
func TestCloudWorkerAdminBillingRejectsRenewDuringAnotherAction(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.renewScript = "unused-renew.sh"
	h.credits = &renewLifecycleStub{item: types.CloudWorkerLifecycle{
		ID: 9, TenantName: "bot-month", OwnerUID: 813, State: "active", BillingMode: types.CloudWorkerMonthly,
		BillingAction: "convert", BillingActionStartedAt: time.Now().Add(-time.Minute),
	}}

	rec := postBilling(t, h, "bot-month", "renew")

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "cloud_worker_billing_busy") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCloudWorkerAdminBillingRenewIgnoresStaleBillingMarker covers the crashed
// process case: the store lets a new action replace a claim older than its
// stale window, so a leftover marker must not block a paid renewal forever.
func TestCloudWorkerAdminBillingRenewIgnoresStaleBillingMarker(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	// Exits 0 without an expiry, so the provider step reports applied without
	// rewriting the lifecycle date.
	h.renewScript = renewScriptFixture(t, `{"status":"renewed"}`)
	h.credits = &renewLifecycleStub{item: types.CloudWorkerLifecycle{
		ID: 9, TenantName: "bot-month", OwnerUID: 813, State: "active", BillingMode: types.CloudWorkerMonthly,
		BillingAction: "suspend", BillingActionStartedAt: time.Now().Add(-2 * time.Hour),
	}}

	rec := postBilling(t, h, "bot-month", "renew")

	if rec.Code != http.StatusOK {
		t.Fatalf("a stale billing marker must not block renewal: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCloudWorkerAdminBillingRenewsMonthlyWorker runs the operator renewal
// through the same provider step the payment path uses. The script here returns
// no expiry, which must still be reported as a completed provider renewal while
// the lifecycle date stays untouched: inventing a date would silently extend or
// shorten what the customer paid for.
func TestCloudWorkerAdminBillingRenewsMonthlyWorker(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	// Exits 0 without reporting an expiry, like a provider that completed but
	// has not published the new date yet.
	h.renewScript = renewScriptFixture(t, `{"status":"renewed"}`)
	stub := &renewLifecycleStub{item: types.CloudWorkerLifecycle{
		ID: 17, TenantName: "bot-bot-813-875650e773", OwnerUID: 813, State: "active", BillingMode: types.CloudWorkerMonthly, RetentionDays: 15,
	}}
	h.credits = stub

	rec := postBilling(t, h, "bot-bot-813-875650e773", "renew")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"action":"renew"`) {
		t.Fatalf("expected renew action in response, got %s", rec.Body.String())
	}
	if stub.extended != nil {
		t.Fatalf("a provider response without expires_at must not rewrite the lifecycle date: %v", stub.extended.UTC().Format(time.RFC3339))
	}
}

// TestCloudWorkerAdminBillingRenewWritesProviderExpiry covers the confirmed
// renewal: the provider date becomes the lifecycle expiry with the row's own
// retention window, exactly like the paid renewal path.
func TestCloudWorkerAdminBillingRenewWritesProviderExpiry(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.renewScript = renewScriptFixture(t, `{"status":"renewed","expires_at":"2030-01-01T00:00:00Z","auto_renew_disabled":true}`)
	stub := &renewLifecycleStub{item: types.CloudWorkerLifecycle{
		ID: 17, TenantName: "bot-bot-813-875650e773", OwnerUID: 813, State: "active", BillingMode: types.CloudWorkerMonthly, RetentionDays: 7,
	}}
	h.credits = stub

	rec := postBilling(t, h, "bot-bot-813-875650e773", "renew")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.extended == nil || !stub.extended.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("lifecycle expiry not written from provider response: %v", stub.extended)
	}
	if stub.grace != 7 {
		t.Fatalf("grace days=%d want the row's own retention 7", stub.grace)
	}
	if !strings.Contains(rec.Body.String(), "2030-01-01T00:00:00Z") {
		t.Fatalf("response must report the provider expiry: %s", rec.Body.String())
	}
}
