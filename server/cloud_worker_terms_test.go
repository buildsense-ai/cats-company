package server

import (
	"encoding/json"
	"fmt"
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

type sharedTermStub struct {
	billingLifecycleStub
	row          types.CloudWorkerLifecycle
	sets, claims int
	completeHook func()
}

func (s *sharedTermStub) GetCloudWorkerBillingLifecycle(string) (*types.CloudWorkerLifecycle, error) {
	value := s.row
	return &value, nil
}
func (s *sharedTermStub) SetSharedCloudWorkerTerm(_ int64, mode string, expires *time.Time, retention int) (*types.CloudWorkerLifecycle, error) {
	s.sets++
	s.row.ExpiryMode = mode
	s.row.PackageExpiresAt = time.Time{}
	s.row.DeleteAfter = time.Time{}
	if expires != nil {
		s.row.PackageExpiresAt = *expires
		s.row.DeleteAfter = expires.Add(time.Duration(retention) * 24 * time.Hour)
	}
	s.row.RetentionDays = retention
	s.row.SyncPending = true
	value := s.row
	return &value, nil
}
func (s *sharedTermStub) ClaimSharedCloudWorkerSync(int64) (*types.CloudWorkerLifecycle, bool, error) {
	if !s.row.SyncPending || s.row.BillingAction != "" {
		return nil, false, nil
	}
	s.claims++
	s.row.BillingAction = "sync"
	value := s.row
	return &value, true, nil
}
func (s *sharedTermStub) CompleteSharedCloudWorkerSync(_ int64, expires *time.Time, mode, text string) error {
	s.row.BillingAction = ""
	s.errorText = text
	if s.completeHook != nil {
		s.completeHook()
	}
	if mode == s.row.ExpiryMode && ((expires == nil && s.row.PackageExpiresAt.IsZero()) || (expires != nil && expires.Equal(s.row.PackageExpiresAt))) {
		s.row.SyncPending = text != ""
	}
	return nil
}
func (s *sharedTermStub) ListSharedCloudWorkerSyncPending(int) ([]types.CloudWorkerLifecycle, error) {
	return nil, nil
}
func (s *sharedTermStub) ClaimSharedCloudWorkerExpiry(int64, time.Time) (bool, error) {
	return false, nil
}
func (s *sharedTermStub) CompleteSharedCloudWorkerExpiry(int64, time.Time, string) error { return nil }

func newSharedTermHandler(t *testing.T) (*CloudWorkerHandler, *sharedTermStub) {
	t.Helper()
	h, base := newCloudWorkerTestHandler("")
	h.db = &deploymentTestStore{cloudWorkerTestStore: base, deployments: map[string]types.CloudWorkerDeployment{
		"bot-shared": {Profile: types.CloudWorkerPackedShared, Env: map[string]string{}},
	}}
	h.renewScript = filepath.Join(t.TempDir(), "renew.sh")
	s := &sharedTermStub{row: types.CloudWorkerLifecycle{ID: 1, OwnerUID: 7, WorkerUID: 10, TenantName: "bot-shared", State: "active", ExpiryMode: "fixed", RetentionDays: 7}}
	h.credits = s
	return h, s
}
func sharedTermRequest(h *CloudWorkerHandler, action, mode string, expires *time.Time) *httptest.ResponseRecorder {
	body := map[string]interface{}{"tenant_name": "bot-shared", "action": action, "note": "contract test"}
	if mode != "" {
		body["expiry_mode"] = mode
	}
	if expires != nil {
		body["expires_at"] = expires
	}
	w := httptest.NewRecorder()
	h.HandleAdminBilling(w, cloudWorkerRequest(7, http.MethodPost, "/billing", body))
	return w
}
func writeSharedTermScript(t *testing.T, path, result string, exit int) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' '" + result + "'\nexit " + fmt.Sprint(exit) + "\n"
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestSharedTermRejectsInvalidOrDestructiveRequests(t *testing.T) {
	future := time.Now().UTC().Add(72 * time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	for _, tc := range []struct {
		name, mode, state string
		expires           *time.Time
		status            int
	}{
		{"fixed missing", "fixed", "active", nil, 400},
		{"fixed past", "fixed", "active", &past, 400},
		{"fixed shortening", "fixed", "active", &future, 409},
		{"permanent with deadline", "permanent", "active", &future, 400},
		{"follow with deadline", "follow_plan", "active", &future, 400},
		{"unknown mode", "forever", "active", nil, 400},
		{"deleted", "permanent", "deleted", nil, 409},
		{"deleting", "permanent", "delete_running", nil, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s := newSharedTermHandler(t)
			s.row.State = tc.state
			s.row.PackageExpiresAt = future.Add(time.Hour)
			w := sharedTermRequest(h, "set_term", tc.mode, tc.expires)
			if w.Code != tc.status || s.sets != 0 || s.claims != 0 {
				t.Fatalf("status=%d body=%s sets=%d claims=%d", w.Code, w.Body, s.sets, s.claims)
			}
		})
	}
}

func TestSharedTermFailureRetriesSameAbsoluteDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract")
	}
	h, s := newSharedTermHandler(t)
	deadline := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)
	writeSharedTermScript(t, h.renewScript, "resume failed", 1)
	w := sharedTermRequest(h, "set_term", "fixed", &deadline)
	if w.Code != 502 || !s.row.SyncPending || !s.row.PackageExpiresAt.Equal(deadline) || !strings.Contains(w.Body.String(), "cloud_worker_term_sync_pending") {
		t.Fatalf("failed recovery lost target: %d %s %+v", w.Code, w.Body, s.row)
	}
	result, _ := json.Marshal(map[string]interface{}{"status": "renewed", "expires_at": deadline.Truncate(time.Second), "resumed": true})
	// Validate the invocation as well as its parsed response: same container,
	// same absolute target and retention, with no subscription-cycle purchase.
	body := "#!/bin/sh\n[ \"$1\" = --name ] && [ \"$2\" = bot-shared ] && [ \"$3\" = --expires-at ] && [ \"$4\" = '" + deadline.Format(time.RFC3339) + "' ] && [ \"$5\" = --grace-days ] && [ \"$6\" = 7 ] && [ \"$#\" = 6 ] || exit 17\nprintf '%s\\n' '" + string(result) + "'\n"
	if err := os.WriteFile(h.renewScript, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	w = sharedTermRequest(h, "sync_term", "", nil)
	if w.Code != 200 || s.row.SyncPending || s.sets != 1 || !s.row.PackageExpiresAt.Equal(deadline) || !strings.Contains(w.Body.String(), "原机器已恢复") {
		t.Fatalf("retry changed target or failed: %d %s %+v", w.Code, w.Body, s.row)
	}
	w = sharedTermRequest(h, "sync_term", "", nil)
	if w.Code != 200 || s.claims != 2 || s.sets != 1 {
		t.Fatalf("repeated sync ran again: %d claims=%d sets=%d", w.Code, s.claims, s.sets)
	}
}

func TestSharedTermRequiresExactRemoteConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract")
	}
	for _, tc := range []struct {
		name, result string
		status       int
	}{
		{"permanent", `{"status":"renewed","expires_at":null,"permanent":true,"resumed":false}`, 200},
		{"missing permanent confirmation", `{"status":"renewed","expires_at":null}`, 502},
		{"wrong finite deadline", `{"status":"renewed","expires_at":"2099-01-01T00:00:00Z"}`, 502},
		{"malformed", `ok`, 502},
		{"skipped", `{"status":"skipped"}`, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s := newSharedTermHandler(t)
			writeSharedTermScript(t, h.renewScript, tc.result, 0)
			w := sharedTermRequest(h, "set_term", "permanent", nil)
			if w.Code != tc.status || s.row.SyncPending != (tc.status != 200) || !s.row.PackageExpiresAt.IsZero() || !s.row.DeleteAfter.IsZero() {
				t.Fatalf("status=%d body=%s row=%+v", w.Code, w.Body, s.row)
			}
		})
	}
}

func TestSharedTermOlderCompletionCannotReportNewTargetSynced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract")
	}
	h, s := newSharedTermHandler(t)
	writeSharedTermScript(t, h.renewScript, `{"status":"renewed","expires_at":null,"permanent":true}`, 0)
	s.completeHook = func() {
		s.row.ExpiryMode = "follow_plan"
		s.row.PackageExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	w := sharedTermRequest(h, "set_term", "permanent", nil)
	if w.Code != 502 || !s.row.SyncPending {
		t.Fatalf("stale completion cleared new target: %d %s %+v", w.Code, w.Body, s.row)
	}
}

func TestSharedTermExpiredGoalPausesWithoutRenewal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract")
	}
	for _, status := range []string{"expired", "renewed"} {
		t.Run(status, func(t *testing.T) {
			h, s := newSharedTermHandler(t)
			deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
			s.row.PackageExpiresAt = deadline
			s.row.ExpiryMode = "follow_plan"
			s.row.SyncPending = true
			result, _ := json.Marshal(map[string]interface{}{"status": status, "expires_at": deadline.Truncate(time.Second)})
			body := "#!/bin/sh\n[ \"$4\" = '" + deadline.Format(time.RFC3339) + "' ] && [ \"$7\" = --expire-if-due ] && [ \"$#\" = 7 ] || exit 17\nprintf '%s\\n' '" + string(result) + "'\n"
			if err := os.WriteFile(h.renewScript, []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			w := sharedTermRequest(h, "sync_term", "", nil)
			if status == "expired" {
				if w.Code != 200 || s.row.SyncPending || !strings.Contains(w.Body.String(), "到期暂停") {
					t.Fatalf("expired goal did not pause: %d %s %+v", w.Code, w.Body, s.row)
				}
			} else if w.Code != 502 || !s.row.SyncPending {
				t.Fatalf("renewed response incorrectly confirmed expiry: %d %s", w.Code, w.Body)
			}
		})
	}
}
