package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestRelayConfigDefaults(t *testing.T) {
	handler := NewRelayConfigHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/relay/config", nil)
	rec := httptest.NewRecorder()

	handler.HandleConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body relayConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.BaseURL != defaultRelayBaseURL {
		t.Fatalf("unexpected base url: %s", body.BaseURL)
	}
	if body.DefaultModel == "" {
		t.Fatal("expected default model")
	}
	if len(body.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(body.Endpoints))
	}
	if body.Endpoints[0].BaseURL != defaultRelayBaseURL+"/v1" {
		t.Fatalf("unexpected openai endpoint: %s", body.Endpoints[0].BaseURL)
	}
	if body.Endpoints[1].BaseURL != defaultRelayBaseURL+"/anthropic" {
		t.Fatalf("unexpected anthropic endpoint: %s", body.Endpoints[1].BaseURL)
	}
}

func TestRelayConfigUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("CATS_RELAY_PUBLIC_BASE_URL", "https://relay.example.com/")
	t.Setenv("CATS_RELAY_OPENAI_BASE_URL", "https://openai.example.com/v1/")
	t.Setenv("CATS_RELAY_ANTHROPIC_BASE_URL", "https://anthropic.example.com/anthropic/")
	t.Setenv("CATS_RELAY_DEFAULT_MODEL", "custom-model")

	handler := NewRelayConfigHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/relay/config", nil)
	rec := httptest.NewRecorder()

	handler.HandleConfig(rec, req)

	var body relayConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.BaseURL != "https://relay.example.com" {
		t.Fatalf("unexpected base url: %s", body.BaseURL)
	}
	if body.DefaultModel != "custom-model" {
		t.Fatalf("unexpected model: %s", body.DefaultModel)
	}
	if body.Endpoints[0].BaseURL != "https://openai.example.com/v1" {
		t.Fatalf("unexpected openai endpoint: %s", body.Endpoints[0].BaseURL)
	}
	if body.Endpoints[1].BaseURL != "https://anthropic.example.com/anthropic" {
		t.Fatalf("unexpected anthropic endpoint: %s", body.Endpoints[1].BaseURL)
	}
}

func TestRelayConfigRejectsUnsupportedMethod(t *testing.T) {
	handler := NewRelayConfigHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/relay/config", nil)
	rec := httptest.NewRecorder()

	handler.HandleConfig(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type relayConfigOwnerStore struct {
	store.Store
	users map[int64]*types.User
}

func (s relayConfigOwnerStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
}

func TestRelayConfigRouteRequiresHumanJWT(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-config-test-secret")

	userToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}
	botToken, err := GenerateToken(2, "bot", "")
	if err != nil {
		t.Fatalf("GenerateToken bot: %v", err)
	}
	disabledToken, err := GenerateToken(3, "disabled", "disabled@example.com")
	if err != nil {
		t.Fatalf("GenerateToken disabled: %v", err)
	}

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
		2: {ID: 2, Username: "bot", AccountType: types.AccountBot, State: 0},
		3: {ID: 3, Username: "disabled", AccountType: types.AccountHuman, State: 1},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayConfigHandler().HandleConfig)

	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "human jwt", authorization: "Bearer " + userToken, wantStatus: http.StatusOK},
		{name: "missing auth", authorization: "", wantStatus: http.StatusUnauthorized},
		{name: "bot api key", authorization: "ApiKey cc_2_fake", wantStatus: http.StatusUnauthorized},
		{name: "bot jwt", authorization: "Bearer " + botToken, wantStatus: http.StatusForbidden},
		{name: "disabled human jwt", authorization: "Bearer " + disabledToken, wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/relay/config", nil)
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
