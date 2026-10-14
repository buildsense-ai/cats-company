package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// The relay identity sweep mirrors owner-platform usernames/emails onto relay
// key rows so the relay console can be searched by email. These tests pin the
// diff-only behavior (no writes when relay is already current), the 404 skip
// for keys deleted mid-sweep, and the abort-on-real-error contract that lets
// the retry loop heal transient relay failures.

func newIdentitySyncRelay(t *testing.T, page commercialRelayUsageResponse, post func(uid int64, body map[string]string) int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/internal/usage/users"):
			_ = json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/internal/users/") && strings.HasSuffix(r.URL.Path, "/identity"):
			var uid int64
			if _, err := fmt.Sscanf(r.URL.Path, "/internal/users/%d/identity", &uid); err != nil {
				t.Errorf("parse uid from %s: %v", r.URL.Path, err)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode identity body: %v", err)
			}
			status := post(uid, body)
			if status != http.StatusOK {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "relay admin request failed"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

func TestRelayIdentitySyncMirrorsOnlyDiffs(t *testing.T) {
	store := &commercialRelaySyncTestStore{users: map[int64]*types.User{
		11: {ID: 11, Username: "alice", Email: "alice@example.com"},
		12: {ID: 12, Username: "bob", Email: "bob@example.com"},
		13: {ID: 13, Username: "carol", Email: "carol@example.com"},
	}}

	var mu sync.Mutex
	pushes := map[int64]map[string]string{}
	relay := newIdentitySyncRelay(t, commercialRelayUsageResponse{
		Users: []commercialRelayUsageUser{
			// 11: relay already has the username, only the email is missing.
			{UID: 11, Username: "alice", Configured: true},
			// 12: relay is already current → no write.
			{UID: 12, Username: "bob", Email: "bob@example.com", Configured: true},
			// 13: stale username and missing email → both fields push.
			{UID: 13, Username: "old-carol", Configured: true},
			// 14: released/revoked key → never touched.
			{UID: 14, Configured: false},
		},
		TotalCount: 4,
	}, func(uid int64, body map[string]string) int {
		mu.Lock()
		defer mu.Unlock()
		pushes[uid] = body
		return http.StatusOK
	})
	defer relay.Close()

	syncer := NewCommercialRelaySyncer(
		store,
		&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()},
		CommercialRelaySyncerOptions{},
	)
	if err := syncer.syncRelayIdentitiesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushes) != 2 {
		t.Fatalf("expected exactly 2 identity pushes, got %#v", pushes)
	}
	if got := pushes[11]; got["email"] != "alice@example.com" || len(got) != 1 {
		t.Fatalf("uid 11 should push only the missing email: %#v", got)
	}
	if got := pushes[13]; got["email"] != "carol@example.com" || got["username"] != "carol" {
		t.Fatalf("uid 13 should push both fields: %#v", got)
	}
}

func TestRelayIdentitySyncSkipsKeysDeletedMidSweep(t *testing.T) {
	store := &commercialRelaySyncTestStore{users: map[int64]*types.User{
		15: {ID: 15, Username: "dave", Email: "dave@example.com"},
		16: {ID: 16, Username: "erin", Email: "erin@example.com"},
	}}

	var mu sync.Mutex
	pushes := []int64{}
	relay := newIdentitySyncRelay(t, commercialRelayUsageResponse{
		Users: []commercialRelayUsageUser{
			{UID: 15, Configured: true},
			{UID: 16, Configured: true},
		},
		TotalCount: 2,
	}, func(uid int64, body map[string]string) int {
		mu.Lock()
		pushes = append(pushes, uid)
		mu.Unlock()
		if uid == 15 {
			// The key row was revoked between listing and push.
			return http.StatusNotFound
		}
		return http.StatusOK
	})
	defer relay.Close()

	syncer := NewCommercialRelaySyncer(
		store,
		&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()},
		CommercialRelaySyncerOptions{},
	)
	if err := syncer.syncRelayIdentitiesOnce(context.Background()); err != nil {
		t.Fatalf("a vanished key must not fail the sweep: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushes) != 2 || pushes[0] != 15 || pushes[1] != 16 {
		t.Fatalf("uid 16 must still be processed after uid 15 vanished: %#v", pushes)
	}
}

func TestRelayIdentitySyncAbortsOnRealRelayFailure(t *testing.T) {
	store := &commercialRelaySyncTestStore{users: map[int64]*types.User{
		17: {ID: 17, Username: "frank", Email: "frank@example.com"},
	}}
	relay := newIdentitySyncRelay(t, commercialRelayUsageResponse{
		Users:      []commercialRelayUsageUser{{UID: 17, Configured: true}},
		TotalCount: 1,
	}, func(uid int64, body map[string]string) int {
		return http.StatusInternalServerError
	})
	defer relay.Close()

	syncer := NewCommercialRelaySyncer(
		store,
		&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()},
		CommercialRelaySyncerOptions{},
	)
	if err := syncer.syncRelayIdentitiesOnce(context.Background()); err == nil {
		t.Fatal("a relay 500 must surface so the retry loop can heal it")
	}
}
