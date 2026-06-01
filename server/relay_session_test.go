package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestRelaySessionReturnsSignedRelayURL(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-session-jwt-secret")

	t.Setenv("CATS_RELAY_SSO_SECRET", "relay-sso-secret")
	t.Setenv("CATS_RELAY_PUBLIC_BASE_URL", "https://relay.example.com/")

	userToken, err := GenerateToken(12, "mira", "mira@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		12: {ID: 12, Username: "mira", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayConfigHandler().HandleSession)
	req := httptest.NewRequest(http.MethodPost, "/api/relay/session", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body relaySessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	parsed, err := url.Parse(body.URL)
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "relay.example.com" {
		t.Fatalf("unexpected relay url: %s", body.URL)
	}
	ticket := parsed.Query().Get("sso")
	if ticket == "" {
		t.Fatalf("missing sso ticket in %s", body.URL)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected ticket format: %s", ticket)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode ticket payload: %v", err)
	}
	var payload relaySSOPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.UID != 12 ||
		payload.Username != "mira" ||
		payload.Issuer != "catscompany" ||
		payload.Audience != "cats-relay" ||
		payload.JTI == "" ||
		payload.Expires <= payload.IssuedAt {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRelaySessionRequiresSSOSecret(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-session-disabled-secret")

	userToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayConfigHandler().HandleSession)
	req := httptest.NewRequest(http.MethodPost, "/api/relay/session", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
