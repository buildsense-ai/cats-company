package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestBotBodyLeaseRejectsDifferentBodyAndAllowsSameBodyReconnect(t *testing.T) {
	now := time.Unix(100, 0)
	leases := newBotBodyLeaseManager(time.Minute)
	leases.now = func() time.Time { return now }

	first, err := leases.acquire(42, "body-a", "conn-a")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if first.Replaced {
		t.Fatal("first acquire must not be marked as replaced")
	}

	conflict, err := leases.acquire(42, "body-b", "conn-b")
	if !errors.Is(err, errBotBodyLeaseConflict) {
		t.Fatalf("second body acquire error = %v, want lease conflict", err)
	}
	if conflict.Lease.bodyID != "body-a" {
		t.Fatalf("conflict body = %q, want body-a", conflict.Lease.bodyID)
	}

	reconnect, err := leases.acquire(42, "body-a", "conn-a2")
	if err != nil {
		t.Fatalf("same body reconnect failed: %v", err)
	}
	if !reconnect.Replaced {
		t.Fatal("same body reconnect should replace the previous connection token")
	}

	if leases.release(42, "body-a", "conn-a") {
		t.Fatal("old connection token must not release the replacement lease")
	}
	if _, err := leases.acquire(42, "body-b", "conn-b2"); !errors.Is(err, errBotBodyLeaseConflict) {
		t.Fatalf("different body after stale release error = %v, want lease conflict", err)
	}

	if !leases.release(42, "body-a", "conn-a2") {
		t.Fatal("current connection token should release the lease")
	}
	if _, err := leases.acquire(42, "body-b", "conn-b3"); err != nil {
		t.Fatalf("different body should acquire after current release: %v", err)
	}
}

func TestBotBodyLeaseDoesNotExpireActiveConnectionByWallClock(t *testing.T) {
	now := time.Unix(200, 0)
	leases := newBotBodyLeaseManager(time.Minute)
	leases.now = func() time.Time { return now }

	if _, err := leases.acquire(7, "body-a", "conn-a"); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	now = now.Add(time.Minute + time.Second)
	if _, err := leases.acquire(7, "body-b", "conn-b"); !errors.Is(err, errBotBodyLeaseConflict) {
		t.Fatalf("active lease should still reject another body after wall-clock time passes: %v", err)
	}
	if !leases.isCurrent(7, "body-a", "conn-a") {
		t.Fatal("active lease should remain current until explicitly released")
	}
	if !leases.release(7, "body-a", "conn-a") {
		t.Fatal("current lease should release explicitly")
	}
	if _, err := leases.acquire(7, "body-b", "conn-b"); err != nil {
		t.Fatalf("released lease should allow a new body: %v", err)
	}
}

func TestBotBodyLeaseStatus(t *testing.T) {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	leases := newBotBodyLeaseManager(time.Minute)
	leases.now = func() time.Time { return now }

	if _, ok := leases.status(42); ok {
		t.Fatal("new lease manager should report no active body")
	}
	if _, err := leases.acquire(42, "body-a", "conn-a"); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	lease, ok := leases.status(42)
	if !ok {
		t.Fatal("expected active body status")
	}
	if lease.bodyID != "body-a" || !lease.acquiredAt.Equal(now) {
		t.Fatalf("unexpected lease status: %+v", lease)
	}
	if !leases.release(42, "body-a", "conn-a") {
		t.Fatal("release failed")
	}
	if _, ok := leases.status(42); ok {
		t.Fatal("released lease should not report active status")
	}
}

type botBodyStatusStore struct {
	store.Store
	ownerUID int64
	bodyID   string
	err      error
}

func (s *botBodyStatusStore) GetBotOwner(botUID int64) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	if botUID != 42 {
		return 0, errors.New("bot not found")
	}
	return s.ownerUID, nil
}

func (s *botBodyStatusStore) GetBotBodyID(botUID int64) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if botUID != 42 {
		return "", errors.New("bot not found")
	}
	return s.bodyID, nil
}

func TestHandleGetBotBodyStatus(t *testing.T) {
	hub := NewHub(nil, nil)
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	hub.bodyLeases.now = func() time.Time { return now }
	if _, err := hub.bodyLeases.acquire(42, "body-a", "conn-a"); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	handler := NewBotHandler(&botBodyStatusStore{ownerUID: 7, bodyID: "body-a"}, nil)
	handler.SetHub(hub)
	req := httptest.NewRequest(http.MethodGet, "/api/bots/body-status?uid=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotBodyStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body BotBodyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Active || !body.Bound || body.BotUID != 42 || body.BodyID != "body-a" {
		t.Fatalf("unexpected body status: %+v", body)
	}
	if body.ConnectedAt == nil || !body.ConnectedAt.Equal(now) {
		t.Fatalf("unexpected connected_at: %+v", body.ConnectedAt)
	}
}

func TestHandleGetBotBodyStatusReturnsOfflineBinding(t *testing.T) {
	handler := NewBotHandler(&botBodyStatusStore{ownerUID: 7, bodyID: "body-a"}, nil)
	handler.SetHub(NewHub(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/bots/body-status?uid=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotBodyStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body BotBodyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Active || !body.Bound || body.BotUID != 42 || body.BodyID != "body-a" || body.ConnectedAt != nil {
		t.Fatalf("unexpected offline body status: %+v", body)
	}
}

func TestHandleGetBotBodyStatusRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name     string
		ownerUID int64
		queryUID string
		store    *botBodyStatusStore
		want     int
	}{
		{
			name:     "missing owner context",
			ownerUID: 0,
			queryUID: "42",
			store:    &botBodyStatusStore{ownerUID: 7},
			want:     http.StatusUnauthorized,
		},
		{
			name:     "invalid uid",
			ownerUID: 7,
			queryUID: "not-a-number",
			store:    &botBodyStatusStore{ownerUID: 7},
			want:     http.StatusBadRequest,
		},
		{
			name:     "not found",
			ownerUID: 7,
			queryUID: "43",
			store:    &botBodyStatusStore{ownerUID: 7},
			want:     http.StatusNotFound,
		},
		{
			name:     "not owner",
			ownerUID: 8,
			queryUID: "42",
			store:    &botBodyStatusStore{ownerUID: 7},
			want:     http.StatusForbidden,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewBotHandler(tc.store, nil)
			handler.SetHub(NewHub(nil, nil))
			req := httptest.NewRequest(http.MethodGet, "/api/bots/body-status?uid="+tc.queryUID, nil)
			req = req.WithContext(context.WithValue(req.Context(), uidKey, tc.ownerUID))
			rec := httptest.NewRecorder()

			handler.HandleGetBotBodyStatus(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestNormalizeBotBodyID(t *testing.T) {
	valid := []string{"body-a", " body:a.1 ", "MacBook-Pro_01"}
	for _, value := range valid {
		got, err := normalizeBotBodyID(value)
		if err != nil {
			t.Fatalf("normalizeBotBodyID(%q) returned error: %v", value, err)
		}
		if strings.TrimSpace(value) != got {
			t.Fatalf("normalizeBotBodyID(%q) = %q", value, got)
		}
	}

	invalid := []string{"", " body with spaces ", "-starts-with-dash", strings.Repeat("a", maxBotBodyIdentityLength+1)}
	for _, value := range invalid {
		if _, err := normalizeBotBodyID(value); !errors.Is(err, errInvalidBotBodyID) {
			t.Fatalf("normalizeBotBodyID(%q) error = %v, want invalid body id", value, err)
		}
	}
}
