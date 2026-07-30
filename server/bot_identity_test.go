package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botIdentityTestStore struct {
	store.Store
	users   map[int64]*types.User
	botKeys map[string]int64
}

func (s botIdentityTestStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
}

func (s botIdentityTestStore) GetBotByAPIKey(apiKey string) (int64, error) {
	return s.botKeys[apiKey], nil
}

func TestHandleBotIdentityReturnsOnlyAuthenticatedBotUID(t *testing.T) {
	const apiKey = "cc_7_runtime"
	db := botIdentityTestStore{
		users: map[int64]*types.User{
			7: {ID: 7, Username: "active-bot", AccountType: types.AccountBot, State: 0},
		},
		botKeys: map[string]int64{apiKey: 7},
	}
	handler := BotAPIKeyMiddlewareWithDB(db)(HandleBotIdentity)
	req := httptest.NewRequest(http.MethodGet, "/api/bot/identity", nil)
	req.Header.Set("Authorization", "ApiKey "+apiKey)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q want=application/json", got)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("response fields=%v want only uid", payload)
	}
	var uid string
	if err := json.Unmarshal(payload["uid"], &uid); err != nil {
		t.Fatalf("decode uid: %v", err)
	}
	if uid != "7" {
		t.Fatalf("uid=%q want=7", uid)
	}
}

func TestHandleBotIdentityPreservesUIDBeyondJavaScriptSafeInteger(t *testing.T) {
	const uid int64 = 9007199254740993
	const apiKey = "cc_20000000000001_runtime"
	db := botIdentityTestStore{
		users: map[int64]*types.User{
			uid: {ID: uid, Username: "large-uid-bot", AccountType: types.AccountBot, State: 0},
		},
		botKeys: map[string]int64{apiKey: uid},
	}
	handler := BotAPIKeyMiddlewareWithDB(db)(HandleBotIdentity)
	req := httptest.NewRequest(http.MethodGet, "/api/bot/identity", nil)
	req.Header.Set("Authorization", "ApiKey "+apiKey)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["uid"] != "9007199254740993" {
		t.Fatalf("uid=%q want exact decimal string", payload["uid"])
	}
}

func TestHandleBotIdentityRejectsUntrustedCredentials(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("bot-identity-test-secret")

	botToken, err := GenerateToken(7, "active-bot", "bot@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	const activeBotKey = "cc_7_runtime"
	const disabledBotKey = "cc_8_runtime"
	const mismatchedBotKey = "cc_9_runtime"
	db := botIdentityTestStore{
		users: map[int64]*types.User{
			7: {ID: 7, Username: "active-bot", AccountType: types.AccountBot, State: 0},
			8: {ID: 8, Username: "disabled-bot", AccountType: types.AccountBot, State: 1},
			9: {ID: 9, Username: "mismatched-bot", AccountType: types.AccountBot, State: 0},
		},
		botKeys: map[string]int64{
			activeBotKey:     7,
			disabledBotKey:   8,
			mismatchedBotKey: 7,
		},
	}
	handler := BotAPIKeyMiddlewareWithDB(db)(HandleBotIdentity)
	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "missing api key", wantStatus: http.StatusUnauthorized},
		{name: "jwt is not accepted", authorization: "Bearer " + botToken, wantStatus: http.StatusUnauthorized},
		{name: "malformed api key", authorization: "ApiKey invalid", wantStatus: http.StatusUnauthorized},
		{name: "unknown api key", authorization: "ApiKey cc_6_unknown", wantStatus: http.StatusUnauthorized},
		{name: "key uid does not match database", authorization: "ApiKey " + mismatchedBotKey, wantStatus: http.StatusUnauthorized},
		{name: "disabled bot", authorization: "ApiKey " + disabledBotKey, wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/bot/identity", nil)
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

func TestHandleBotIdentityAllowsOnlyGET(t *testing.T) {
	const apiKey = "cc_7_runtime"
	db := botIdentityTestStore{
		users: map[int64]*types.User{
			7: {ID: 7, Username: "active-bot", AccountType: types.AccountBot, State: 0},
		},
		botKeys: map[string]int64{apiKey: 7},
	}
	handler := BotAPIKeyMiddlewareWithDB(db)(HandleBotIdentity)
	req := httptest.NewRequest(http.MethodPost, "/api/bot/identity", nil)
	req.Header.Set("Authorization", "ApiKey "+apiKey)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusMethodNotAllowed, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow=%q want=%q", got, http.MethodGet)
	}
}

func TestHandleBotIdentityRejectsMissingTrustedContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/bot/identity", nil)
	rec := httptest.NewRecorder()

	HandleBotIdentity(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
