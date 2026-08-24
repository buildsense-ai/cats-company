package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type cloudWorkerCreditAdminTestStore struct {
	uid       int64
	count     int
	sourceRef string
	expiresAt *time.Time
}

func (s *cloudWorkerCreditAdminTestStore) GrantCloudWorkerCredits(uid int64, count int, sourceRef string, expiresAt *time.Time) (int, error) {
	s.uid, s.count, s.sourceRef, s.expiresAt = uid, count, sourceRef, expiresAt
	return count, nil
}

func TestHandleCloudWorkerCreditsRequiresLocalAndValidates(t *testing.T) {
	store := &cloudWorkerCreditAdminTestStore{}
	h := NewAccountAdminHandler(nil, nil, nil)
	h.SetCloudWorkerCreditAdmin(store)

	remote := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/cloud-worker-credits", strings.NewReader(`{"uid":895,"count":2,"source_ref":"internal-895","expires_at":"2099-01-01T00:00:00Z"}`))
	remote.RemoteAddr = "203.0.113.10:1234"
	remoteRec := httptest.NewRecorder()
	h.HandleCloudWorkerCredits(remoteRec, remote)
	if remoteRec.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d body=%s", remoteRec.Code, remoteRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/cloud-worker-credits", strings.NewReader(`{"uid":895,"count":2,"source_ref":"internal-895","expires_at":"2099-01-01T00:00:00Z"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.HandleCloudWorkerCredits(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.uid != 895 || store.count != 2 || store.sourceRef != "internal-895" || store.expiresAt == nil {
		t.Fatalf("unexpected grant=%+v", store)
	}
}

func TestCloudWorkerTenantNameCollapsesProviderSeparators(t *testing.T) {
	for _, tc := range []struct {
		username string
		want     string
	}{
		{username: "bot-codex-paid-flow--9387", want: "bot-bot-codex-paid-flow-9387"},
		{username: "bot-codex-paid-flow-", want: "bot-bot-codex-paid-flow"},
	} {
		if got := cloudWorkerTenantName(tc.username); got != tc.want {
			t.Fatalf("tenant(%q)=%q want %q", tc.username, got, tc.want)
		}
	}
}
