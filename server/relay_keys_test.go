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
		case r.Method == http.MethodPost && r.URL.Path == "/internal/users/7/key/reveal":
			writeJSON(w, http.StatusOK, relayKeyResponse{
				Configured: true,
				Key: &relayKeyInfo{
					ID:     "vk-1",
					Name:   "my key",
					Prefix: "sk-bf-cu...rent",
					State:  "active",
					Key:    "sk-bf-current",
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
	revealHandler := OwnerMiddlewareWithDB(store)(keyHandler.HandleReveal)

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

	revealReq := httptest.NewRequest(http.MethodPost, "/api/relay/key/reveal", nil)
	revealReq.Header.Set("Authorization", "Bearer "+userToken)
	revealRec := httptest.NewRecorder()

	revealHandler(revealRec, revealReq)

	if revealRec.Code != http.StatusOK {
		t.Fatalf("reveal status=%d body=%s", revealRec.Code, revealRec.Body.String())
	}
	if !strings.Contains(revealRec.Body.String(), "sk-bf-current") {
		t.Fatalf("expected current key in reveal response, body=%s", revealRec.Body.String())
	}
}

func TestRelayUsageSummaryUsesCurrentUserRelayData(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-usage-secret")
	t.Setenv("CATS_RELAY_DEFAULT_MODEL", "MiniMax-M3")

	userToken, err := GenerateToken(7, "charlie", "charlie@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer relay-admin-secret" {
			t.Fatalf("missing relay admin auth: %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/internal/usage/users" {
			t.Fatalf("unexpected relay admin request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("search"); got != "7" {
			t.Fatalf("search query = %q, want 7", got)
		}
		if got := r.URL.Query().Get("include_governance"); got != "1" {
			t.Fatalf("include_governance = %q, want 1", got)
		}
		writeJSON(w, http.StatusOK, commercialRelayUsageResponse{Users: []commercialRelayUsageUser{
			{
				UID:        7,
				Username:   "charlie",
				Configured: true,
				Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
					{
						Provider: "minimax-m2-anthropic",
						Model:    "MiniMax-M2.7",
						Budget:   commercialRelayBudget{MaxLimit: 1000, CurrentUsage: 900, ResetDuration: "1M"},
					},
					{
						Provider: "minimax-m3-anthropic",
						Model:    "MiniMax-M3",
						Budget:   commercialRelayBudget{MaxLimit: 500, CurrentUsage: 250, ResetDuration: "1M", LastReset: "2026-06-08 03:29:30+00:00"},
					},
				}},
			},
		}})
	}))
	defer admin.Close()

	t.Setenv("CATS_RELAY_ADMIN_URL", admin.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-secret")

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "charlie", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayKeyHandlerFromEnv().HandleUsage)
	req := httptest.NewRequest(http.MethodGet, "/api/relay/usage", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
	}
	var out relayUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if !out.Configured || out.Summary == nil {
		t.Fatalf("expected configured usage summary, got %+v", out)
	}
	if out.Summary.Source != "relay" || out.Summary.Model != "MiniMax-M3" || out.Summary.Percent != 50 || out.Summary.RemainingCNY != 250 {
		t.Fatalf("unexpected usage summary: %+v", out.Summary)
	}
	if out.Summary.ResetDuration != "1M" || out.Summary.LastReset == "" {
		t.Fatalf("expected reset metadata in usage summary, got %+v", out.Summary)
	}
}

func TestRelayUsageSummaryUsesRequestedModel(t *testing.T) {
	user := &commercialRelayUsageUser{
		UID:        8,
		Configured: true,
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{
				Provider:      "minimax-m2-anthropic",
				Model:         "MiniMax-M2.7",
				AllowedModels: []string{"MiniMax-M2.7"},
				Budget:        commercialRelayBudget{MaxLimit: 1000, CurrentUsage: 900, ResetDuration: "1M"},
			},
			{
				Provider:      "minimax-m3-anthropic",
				Model:         "MiniMax-M3",
				AllowedModels: []string{"minimax-m3"},
				Budget:        commercialRelayBudget{MaxLimit: 500, CurrentUsage: 100, ResetDuration: "1M"},
			},
		}},
	}

	out := buildRelayUsageResponse(user, "minimax_m3")

	if !out.Configured || out.Summary == nil {
		t.Fatalf("expected configured usage summary, got %+v", out)
	}
	if out.Summary.Source != "relay" || out.Summary.Model != "MiniMax-M3" || out.Summary.Percent != 20 || out.Summary.RemainingCNY != 400 {
		t.Fatalf("expected requested model summary, got %+v", out.Summary)
	}
}

func TestRelayUsageSummaryUsesCurrentDeviceModel(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-current-device-model-secret")

	userToken, err := GenerateToken(10, "frank", "frank@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, commercialRelayUsageResponse{Users: []commercialRelayUsageUser{{
			UID:        10,
			Configured: true,
			Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
				{
					Provider: "minimax-m2-anthropic",
					Model:    "MiniMax-M2.7",
					Budget:   commercialRelayBudget{MaxLimit: 1000, CurrentUsage: 900, ResetDuration: "1M"},
				},
				{
					Provider:      "minimax-m3-anthropic",
					Model:         "MiniMax-M3",
					AllowedModels: []string{"minimax-m3"},
					Budget:        commercialRelayBudget{MaxLimit: 500, CurrentUsage: 125, ResetDuration: "1M"},
				},
			}},
		}}})
	}))
	defer admin.Close()

	t.Setenv("CATS_RELAY_ADMIN_URL", admin.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-secret")
	t.Setenv("CATS_RELAY_DEFAULT_MODEL", "MiniMax-M2.7")

	keyHandler := NewRelayKeyHandlerFromEnv()
	keyHandler.SetDeviceModelStatusResolver(func(uid int64) (DeviceModelStatus, bool) {
		if uid != 10 {
			return DeviceModelStatus{}, false
		}
		return DeviceModelStatus{Source: "relay", Model: "MiniMax-M3", UpdatedAt: 1782790000000}, true
	})
	store := relayConfigOwnerStore{users: map[int64]*types.User{
		10: {ID: 10, Username: "frank", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(keyHandler.HandleUsage)
	req := httptest.NewRequest(http.MethodGet, "/api/relay/usage", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
	}
	var out relayUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if !out.Configured || out.Summary == nil {
		t.Fatalf("expected configured usage summary, got %+v", out)
	}
	if out.Summary.Model != "MiniMax-M3" || out.Summary.Percent != 25 || out.Summary.RemainingCNY != 375 {
		t.Fatalf("expected current device model summary, got %+v", out.Summary)
	}
}

func TestRelayUsageSummaryUsesCurrentCustomModel(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-current-custom-model-secret")

	userToken, err := GenerateToken(11, "grace", "grace@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	keyHandler := NewRelayKeyHandlerFromEnv()
	keyHandler.SetDeviceModelStatusResolver(func(uid int64) (DeviceModelStatus, bool) {
		if uid != 11 {
			return DeviceModelStatus{}, false
		}
		return DeviceModelStatus{Source: "custom", Model: "gpt-5.5", UpdatedAt: 1782790000000}, true
	})
	store := relayConfigOwnerStore{users: map[int64]*types.User{
		11: {ID: 11, Username: "grace", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(keyHandler.HandleUsage)
	req := httptest.NewRequest(http.MethodGet, "/api/relay/usage", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
	}
	var out relayUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if !out.Configured || out.Summary == nil {
		t.Fatalf("expected custom usage summary, got %+v", out)
	}
	if out.Summary.Source != "custom" || out.Summary.Model != "gpt-5.5" || out.Summary.LimitCNY != 0 {
		t.Fatalf("expected current custom summary without quota, got %+v", out.Summary)
	}
}

func TestRelayUsageSummarySkipsQuotaForCustomModel(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("relay-custom-usage-secret")

	userToken, err := GenerateToken(9, "erin", "erin@example.com")
	if err != nil {
		t.Fatalf("GenerateToken human: %v", err)
	}

	store := relayConfigOwnerStore{users: map[int64]*types.User{
		9: {ID: 9, Username: "erin", AccountType: types.AccountHuman, State: 0},
	}}
	handler := OwnerMiddlewareWithDB(store)(NewRelayKeyHandlerFromEnv().HandleUsage)
	req := httptest.NewRequest(http.MethodGet, "/api/relay/usage?source=custom&model=gpt-5.5", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
	}
	var out relayUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if !out.Configured || out.Summary == nil {
		t.Fatalf("expected custom usage summary, got %+v", out)
	}
	if out.Summary.Source != "custom" || out.Summary.Model != "gpt-5.5" || out.Summary.LimitCNY != 0 {
		t.Fatalf("expected custom summary without quota, got %+v", out.Summary)
	}
}

func TestRelayUsageSummaryMarksOverLimit(t *testing.T) {
	user := &commercialRelayUsageUser{
		UID:        8,
		Configured: true,
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{
				Provider: "minimax-m3-anthropic",
				Model:    "MiniMax-M3",
				Budget:   commercialRelayBudget{MaxLimit: 500, CurrentUsage: 745.63, ResetDuration: "1M"},
			},
		}},
	}

	out := buildRelayUsageResponse(user, "MiniMax-M3")

	if !out.Configured || out.Summary == nil {
		t.Fatalf("expected configured usage summary, got %+v", out)
	}
	if out.Summary.Status != "over_limit" || out.Summary.RemainingCNY != 0 {
		t.Fatalf("expected over-limit summary, got %+v", out.Summary)
	}
	if out.Summary.Percent <= 100 {
		t.Fatalf("expected percent over 100, got %+v", out.Summary)
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
