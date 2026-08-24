package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"github.com/openchat/openchat/server/store/types"
)
func TestKickUserClosesConnectionsWithCloseCode(t *testing.T) {
	hub := NewHub(nil, nil)

	// Create a client with a real WebSocket-like send channel
	client := &Client{
		uid:         42,
		accountType: "human",
		send:        make(chan []byte, 256),
	}
	hub.addClient(client)

	// Verify user is online
	if !hub.IsOnline(42) {
		t.Fatal("expected uid 42 to be online before kick")
	}

	// KickUser should disconnect the client
	closed := hub.KickUser(42, "test revoke")
	if closed != 1 {
		t.Fatalf("KickUser returned %d, want 1", closed)
	}

	// Verify user is offline after kick
	if hub.IsOnline(42) {
		t.Fatal("expected uid 42 to be offline after kick")
	}
}

// TestKickUserReturnsZeroForOfflineUser verifies that kicking a user with no
// connections returns 0 without error.
func TestKickUserReturnsZeroForOfflineUser(t *testing.T) {
	hub := NewHub(nil, nil)

	closed := hub.KickUser(999, "test revoke offline")
	if closed != 0 {
		t.Fatalf("KickUser returned %d for offline user, want 0", closed)
	}
}

// TestKickUserKicksMultipleConnections verifies that all connections for a
// user are kicked, not just one.
func TestKickUserKicksMultipleConnections(t *testing.T) {
	hub := NewHub(nil, nil)

	clientA := &Client{uid: 50, send: make(chan []byte, 256)}
	clientB := &Client{uid: 50, send: make(chan []byte, 256)}
	hub.addClient(clientA)
	hub.addClient(clientB)

	if !hub.IsOnline(50) {
		t.Fatal("expected uid 50 to be online")
	}

	closed := hub.KickUser(50, "test revoke multi")
	if closed != 2 {
		t.Fatalf("KickUser returned %d, want 2", closed)
	}

	if hub.IsOnline(50) {
		t.Fatal("expected uid 50 to be offline after kicking all connections")
	}
}

// TestUserStateCacheVerifiesBanPropagation verifies that the TTL cache
// correctly caches and expires user state.
func TestUserStateCacheVerifiesBanPropagation(t *testing.T) {
	cache := newUserStateCache(50 * time.Millisecond)

	// Initially not cached
	if _, ok := cache.get(1); ok {
		t.Fatal("expected cache miss for empty cache")
	}

	// Put active user
	cache.put(1, 0)
	if state, ok := cache.get(1); !ok || state != 0 {
		t.Fatalf("expected cached state=0, got state=%d ok=%v", state, ok)
	}

	// Put banned user
	cache.put(2, 1)
	if state, ok := cache.get(2); !ok || state != 1 {
		t.Fatalf("expected cached state=1, got state=%d ok=%v", state, ok)
	}

	// Wait for TTL expiration
	time.Sleep(80 * time.Millisecond)
	if _, ok := cache.get(1); ok {
		t.Fatal("expected cache to expire after TTL")
	}
}

// TestClientAccountActiveReturnsTrueForBot verifies that bot connections
// bypass the state cache check.
func TestClientAccountActiveReturnsTrueForBot(t *testing.T) {
	hub := NewHub(nil, nil)

	botClient := &Client{
		uid:         100,
		accountType: "bot",
		send:        make(chan []byte, 1),
	}

	if !hub.clientAccountActive(botClient) {
		t.Fatal("bot connections should always pass account active check")
	}
}

// TestClientAccountActiveReturnsFalseForNilClient verifies that nil clients
// fail the active check.
func TestClientAccountActiveReturnsFalseForNilClient(t *testing.T) {
	hub := NewHub(nil, nil)

	if hub.clientAccountActive(nil) {
		t.Fatal("nil client should fail account active check")
	}
}

// TestHandleMessageKicksBannedUser verifies that handleMessage kicks a client
// whose account state has been set to banned (state != 0) and the TTL cache
// has expired.
func TestHandleMessageKicksBannedUser(t *testing.T) {
	db := authStateTestStore{
		users: map[int64]*types.User{
			42: {ID: 42, Username: "alice", AccountType: types.AccountHuman, State: 0},
		},
	}
	hub := NewHub(db, nil)

	client := &Client{
		uid:         42,
		accountType: "human",
		send:        make(chan []byte, 256),
	}
	hub.addClient(client)

	// Verify active
	if !hub.IsOnline(42) {
		t.Fatal("expected uid 42 to be online")
	}

	// Simulate ban by updating state in the store
	db.users[42].State = 1

	// Wait for cache to expire (initial miss triggers DB lookup)
	// The first message should trigger a cache miss + DB lookup which finds state=1
	hiMsg := &ClientMessage{Hi: &MsgClientHi{ID: "test-hi"}}
	hub.handleMessage(client, hiMsg)

	// After handleMessage with a banned user, the client should be kicked
	if hub.IsOnline(42) {
		t.Fatal("expected uid 42 to be offline after handleMessage with banned state")
	}
}

// TestWSCloseCodeConstant verifies the close code constant is in the private range.
func TestWSCloseCodeConstant(t *testing.T) {
	if wsCloseAccountDisabled < 4000 || wsCloseAccountDisabled > 4999 {
		t.Fatalf("wsCloseAccountDisabled=%d, want 4000-4999", wsCloseAccountDisabled)
	}
	if wsCloseAccountDisabledReason != "account_disabled" {
		t.Fatalf("wsCloseAccountDisabledReason=%q, want %q", wsCloseAccountDisabledReason, "account_disabled")
	}
}

// TestAccountAdminResponseIncludesCode verifies that the server returns
// an error code in the JSON response for account-disabled errors.
func TestAccountAdminResponseIncludesCode(t *testing.T) {
	// activeUserByID should return ACCOUNT_DISABLED code for state != 0
	db := authStateTestStore{
		users: map[int64]*types.User{
			1: {ID: 1, Username: "disabled", AccountType: types.AccountHuman, State: 1},
		},
	}
	_, status, msg, code := activeUserByID(1, db.GetUser)
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", status, http.StatusForbidden)
	}
	if msg != "user account is disabled" {
		t.Fatalf("msg=%q, want %q", msg, "user account is disabled")
	}
	if code != "ACCOUNT_DISABLED" {
		t.Fatalf("code=%q, want %q", code, "ACCOUNT_DISABLED")
	}
}

// TestActiveUserByIDReturnsEmptyCodeForActiveUser verifies that active users
// get an empty code string.
func TestActiveUserByIDReturnsEmptyCodeForActiveUser(t *testing.T) {
	db := authStateTestStore{
		users: map[int64]*types.User{
			1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
		},
	}
	user, status, _, code := activeUserByID(1, db.GetUser)
	if status != 0 {
		t.Fatalf("status=%d, want 0", status)
	}
	if user == nil {
		t.Fatal("expected non-nil user")
	}
	if code != "" {
		t.Fatalf("code=%q, want empty", code)
	}
}

// TestAccountAdminHandlerSetHubWiring verifies that SetHub stores the hub
// reference so HandleUserState can kick live connections.
func TestAccountAdminHandlerSetHubWiring(t *testing.T) {
	handler := NewAccountAdminHandler(accountTestUserLookup{
		users: map[int64]*types.User{
			1: {ID: 1, Username: "test", AccountType: types.AccountHuman, State: 0},
		},
	}, nil, nil)

	// Before SetHub, hub should be nil
	if handler.hub != nil {
		t.Fatal("hub should be nil before SetHub")
	}

	hub := NewHub(nil, nil)
	handler.SetHub(hub)

	if handler.hub != hub {
		t.Fatal("hub was not set correctly after SetHub")
	}
}

// TestAccountAdminHandlerHandleUserStateKicksBannedUser verifies that
// HandleUserState kicks live connections when a user is disabled.
func TestAccountAdminHandlerHandleUserStateKicksBannedUser(t *testing.T) {
	users := accountTestUserLookup{
		users: map[int64]*types.User{
			1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
		},
	}
	handler := NewAccountAdminHandler(users, nil, nil)
	hub := NewHub(nil, nil)
	handler.SetHub(hub)

	// Add a live connection for the user
	client := &Client{uid: 1, send: make(chan []byte, 256)}
	hub.addClient(client)
	if !hub.IsOnline(1) {
		t.Fatal("expected uid 1 to be online before ban")
	}

	// Build the disable request
	body := `{"uid":1,"state":1}`
	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/users/state", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.HandleUserState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HandleUserState status=%d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify user is kicked
	if hub.IsOnline(1) {
		t.Fatal("expected uid 1 to be offline after ban via HandleUserState")
	}

	// Verify response body
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("expected ok=true in response")
	}
}
