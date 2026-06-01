package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestRelayKeyRouteRequiresHumanJWT(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-key-test-secret")

	userToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}
	botToken, err := GenerateToken(2, "bot", "")
	if err != nil {
		t.Fatalf("GenerateToken bot: %v", err)
	}

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer relay-admin-secret" {
			t.Fatalf("missing relay admin auth: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/internal/users/1/key" {
			t.Fatalf("unexpected relay admin path: %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, relayKeyResponse{Configured: false})
	}))
	defer admin.Close()

	t.Setenv("CATS_RELAY_ADMIN_URL", admin.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-secret")

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
		2: {ID: 2, Username: "bot", AccountType: types.AccountBot, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayKeyHandlerFromEnv().HandleKey)

	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "human jwt", authorization: "Bearer " + userToken, wantStatus: http.StatusOK},
		{name: "missing auth", authorization: "", wantStatus: http.StatusUnauthorized},
		{name: "bot api key", authorization: "ApiKey cc_2_fake", wantStatus: http.StatusUnauthorized},
		{name: "bot jwt", authorization: "Bearer " + botToken, wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/relay/key", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestRelayKeyCreateAndRotateProxy(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-key-create-secret")

	userToken, err := GenerateToken(7, "charlie", "charlie@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	var seenCreateBody relayKeyProxyRequest
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer relay-admin-secret" {
			t.Fatalf("missing relay admin auth: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/internal/users/7/key":
			if err := json.NewDecoder(r.Body).Decode(&seenCreateBody); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			writeJSON(w, http.StatusOK, relayKeyResponse{
				Configured: true,
				Key: &relayKeyInfo{
					ID:     "vk-1",
					Name:   "my key",
					Prefix: "sk-bf-cr...ated",
					State:  "active",
					Key:    "sk-bf-created",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/internal/users/7/key/rotate":
			writeJSON(w, http.StatusOK, relayKeyResponse{
				Configured: true,
				Key: &relayKeyInfo{
					ID:     "vk-1",
					Name:   "my key",
					Prefix: "sk-bf-ro...ated",
					State:  "active",
					Key:    "sk-bf-rotated",
				},
			})
		default:
			t.Fatalf("unexpected relay admin request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer admin.Close()

	t.Setenv("CATS_RELAY_ADMIN_URL", admin.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-secret")

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "charlie", AccountType: types.AccountHuman, State: 0},
	}}
	keyHandler := NewRelayKeyHandlerFromEnv()
	createHandler := OwnerMiddlewareWithDB(store)(keyHandler.HandleKey)
	rotateHandler := OwnerMiddlewareWithDB(store)(keyHandler.HandleRotate)

	createReq := httptest.NewRequest(http.MethodPost, "/api/relay/key", strings.NewReader(`{"name":"my key"}`))
	createReq.Header.Set("Authorization", "Bearer "+userToken)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()

	createHandler(createRec, createReq)

	if createRec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	if seenCreateBody.Name != "my key" || seenCreateBody.Username != "charlie" {
		t.Fatalf("unexpected create body: %+v", seenCreateBody)
	}
	if !strings.Contains(createRec.Body.String(), "sk-bf-created") {
		t.Fatalf("expected one-time key in create response, body=%s", createRec.Body.String())
	}

	rotateReq := httptest.NewRequest(http.MethodPost, "/api/relay/key/rotate", nil)
	rotateReq.Header.Set("Authorization", "Bearer "+userToken)
	rotateRec := httptest.NewRecorder()

	rotateHandler(rotateRec, rotateReq)

	if rotateRec.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", rotateRec.Code, rotateRec.Body.String())
	}
	if !strings.Contains(rotateRec.Body.String(), "sk-bf-rotated") {
		t.Fatalf("expected one-time key in rotate response, body=%s", rotateRec.Body.String())
	}
}

func TestRelayKeyGetStripsPlaintextFromAdminResponse(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-key-get-secret")

	userToken, err := GenerateToken(8, "dana", "dana@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/users/8/key" {
			t.Fatalf("unexpected relay admin request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"configured": true,
			"key": map[string]interface{}{
				"id":                 "vk-unsafe",
				"name":               "unsafe admin response",
				"prefix":             "sk-bf-un...safe",
				"state":              "active",
				"key":                "sk-bf-should-not-leak",
				"bifrost_created_at": "2026-05-22T00:00:00Z",
				"bifrost_updated_at": "2026-05-22T00:00:00Z",
			},
		})
	}))
	defer admin.Close()

	t.Setenv("CATS_RELAY_ADMIN_URL", admin.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-secret")

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		8: {ID: 8, Username: "dana", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayKeyHandlerFromEnv().HandleKey)
	req := httptest.NewRequest(http.MethodGet, "/api/relay/key", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-bf-should-not-leak") {
		t.Fatalf("GET response leaked plaintext key: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "bifrost_") {
		t.Fatalf("GET response leaked relay implementation fields: %s", rec.Body.String())
	}
}

func TestRelayKeyDeleteStripsPlaintextFromAdminResponse(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-key-delete-secret")

	userToken, err := GenerateToken(9, "erin", "erin@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/internal/users/9/key" {
			t.Fatalf("unexpected relay admin request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, relayKeyResponse{
			Configured: false,
			Key: &relayKeyInfo{
				ID:     "vk-deleted",
				Name:   "deleted key",
				Prefix: "sk-bf-de...eted",
				State:  "revoked",
				Key:    "sk-bf-delete-should-not-leak",
			},
		})
	}))
	defer admin.Close()

	t.Setenv("CATS_RELAY_ADMIN_URL", admin.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-secret")

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		9: {ID: 9, Username: "erin", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayKeyHandlerFromEnv().HandleKey)
	req := httptest.NewRequest(http.MethodDelete, "/api/relay/key", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-bf-delete-should-not-leak") {
		t.Fatalf("DELETE response leaked plaintext key: %s", rec.Body.String())
	}
}

func TestRelayKeyRouteReturnsServiceUnavailableWhenDisabled(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-key-disabled-secret")

	userToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayKeyHandlerFromEnv().HandleKey)
	req := httptest.NewRequest(http.MethodGet, "/api/relay/key", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
