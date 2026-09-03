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

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestUserTokenExpirationPolicy(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("user-token-expiration-test-secret")

	webToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	webClaims, err := ParseToken(webToken)
	if err != nil {
		t.Fatalf("ParseToken web: %v", err)
	}
	if webClaims.TokenType != userTokenType {
		t.Fatalf("web token type=%q", webClaims.TokenType)
	}
	if webClaims.ExpiresAt == nil {
		t.Fatal("ordinary web token must expire")
	}
	remaining := time.Until(webClaims.ExpiresAt.Time)
	if remaining < 6*24*time.Hour || remaining > 8*24*time.Hour {
		t.Fatalf("ordinary web token lifetime=%v, want about 7 days", remaining)
	}

	persistentToken, err := GeneratePersistentUserToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GeneratePersistentUserToken: %v", err)
	}
	persistentClaims, err := ParseToken(persistentToken)
	if err != nil {
		t.Fatalf("ParseToken persistent: %v", err)
	}
	if persistentClaims.TokenType != persistentUserTokenType {
		t.Fatalf("persistent token type=%q", persistentClaims.TokenType)
	}
	if persistentClaims.ExpiresAt != nil {
		t.Fatalf("persistent token unexpectedly expires at %v", persistentClaims.ExpiresAt.Time)
	}
}

type authStateTestStore struct {
	store.Store
	users      map[int64]*types.User
	botKeys    map[string]int64
	getUserErr error
}

func (s authStateTestStore) GetUser(id int64) (*types.User, error) {
	if s.getUserErr != nil {
		return nil, s.getUserErr
	}
	return s.users[id], nil
}

func TestAuthMiddlewareWithDBReturnsServerErrorWhenUserLookupFails(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("auth-state-lookup-error-test-secret")

	token, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	db := authStateTestStore{getUserErr: errors.New("database unavailable")}
	handler := AuthMiddlewareWithDB(db)(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func TestHandleMeDistinguishesMissingUserFromLookupFailure(t *testing.T) {
	requestWithUID := func(db store.Store) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(1)))
		rec := httptest.NewRecorder()
		NewUserHandler(db).HandleMe(rec, req)
		return rec
	}

	t.Run("missing user", func(t *testing.T) {
		rec := requestWithUID(authStateTestStore{users: map[int64]*types.User{}})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})

	t.Run("lookup failure", func(t *testing.T) {
		rec := requestWithUID(authStateTestStore{getUserErr: errors.New("database unavailable")})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
	})
}

func (s authStateTestStore) GetUserByUsername(username string) (*types.User, error) {
	for _, user := range s.users {
		if strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	return nil, nil
}

func (s authStateTestStore) GetUserByEmail(email string) (*types.User, error) {
	for _, user := range s.users {
		if strings.EqualFold(user.Email, email) {
			return user, nil
		}
	}
	return nil, nil
}

func (s authStateTestStore) GetBotByAPIKey(apiKey string) (int64, error) {
	return s.botKeys[apiKey], nil
}

func TestAuthMiddlewareWithDBRejectsDisabledJWTAndDisabledBotAPIKey(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("auth-state-test-secret")

	activeToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken active: %v", err)
	}
	disabledToken, err := GenerateToken(2, "bob", "bob@example.com")
	if err != nil {
		t.Fatalf("GenerateToken disabled: %v", err)
	}
	activePersistentToken, err := GeneratePersistentUserToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GeneratePersistentUserToken active: %v", err)
	}
	disabledPersistentToken, err := GeneratePersistentUserToken(2, "bob", "bob@example.com")
	if err != nil {
		t.Fatalf("GeneratePersistentUserToken disabled: %v", err)
	}

	const activeBotKey = "cc_7_test"
	const disabledBotKey = "cc_8_test"
	store := authStateTestStore{
		users: map[int64]*types.User{
			1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
			2: {ID: 2, Username: "bob", AccountType: types.AccountHuman, State: 1},
			7: {ID: 7, Username: "active-bot", AccountType: types.AccountBot, State: 0},
			8: {ID: 8, Username: "disabled-bot", AccountType: types.AccountBot, State: 1},
		},
		botKeys: map[string]int64{activeBotKey: 7, disabledBotKey: 8},
	}
	handler := AuthMiddlewareWithDB(store)(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int64{"uid": UIDFromContext(r.Context())})
	})

	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "active jwt", authorization: "Bearer " + activeToken, wantStatus: http.StatusOK},
		{name: "disabled jwt", authorization: "Bearer " + disabledToken, wantStatus: http.StatusForbidden},
		{name: "active persistent jwt", authorization: "Bearer " + activePersistentToken, wantStatus: http.StatusOK},
		{name: "disabled persistent jwt", authorization: "Bearer " + disabledPersistentToken, wantStatus: http.StatusForbidden},
		{name: "active bot api key", authorization: "ApiKey " + activeBotKey, wantStatus: http.StatusOK},
		{name: "disabled bot api key", authorization: "ApiKey " + disabledBotKey, wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestOpenAICompatibleAuthMiddlewareWithDBAcceptsBearerBotAPIKey(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("openai-auth-state-test-secret")

	activeToken, err := GenerateToken(1, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken active: %v", err)
	}

	const activeBotKey = "cc_7_openai"
	const disabledBotKey = "cc_8_openai"
	store := authStateTestStore{
		users: map[int64]*types.User{
			1: {ID: 1, Username: "alice", AccountType: types.AccountHuman, State: 0},
			7: {ID: 7, Username: "active-bot", AccountType: types.AccountBot, State: 0},
			8: {ID: 8, Username: "disabled-bot", AccountType: types.AccountBot, State: 1},
		},
		botKeys: map[string]int64{activeBotKey: 7, disabledBotKey: 8},
	}
	handler := OpenAICompatibleAuthMiddlewareWithDB(store)(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int64{"uid": UIDFromContext(r.Context())})
	})

	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "active jwt remains supported", authorization: "Bearer " + activeToken, wantStatus: http.StatusOK},
		{name: "openai bearer bot key", authorization: "Bearer " + activeBotKey, wantStatus: http.StatusOK},
		{name: "historical api key scheme", authorization: "ApiKey " + activeBotKey, wantStatus: http.StatusOK},
		{name: "disabled bearer bot key", authorization: "Bearer " + disabledBotKey, wantStatus: http.StatusForbidden},
		{name: "unknown bearer bot key", authorization: "Bearer cc_9_unknown", wantStatus: http.StatusUnauthorized},
		{name: "arbitrary invalid bearer", authorization: "Bearer not-a-jwt-or-bot-key", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestJWTAuthMiddlewareWithDBRejectsDisabledJWTAndAPIKey(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("jwt-auth-state-test-secret")

	disabledToken, err := GenerateToken(2, "bob", "bob@example.com")
	if err != nil {
		t.Fatalf("GenerateToken disabled: %v", err)
	}

	store := authStateTestStore{
		users:   map[int64]*types.User{2: {ID: 2, Username: "bob", AccountType: types.AccountHuman, State: 1}},
		botKeys: map[string]int64{"cc_7_test": 7},
	}
	handler := JWTAuthMiddlewareWithDB(store)(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	disabledReq := httptest.NewRequest(http.MethodGet, "/human-only", nil)
	disabledReq.Header.Set("Authorization", "Bearer "+disabledToken)
	disabledRec := httptest.NewRecorder()
	handler(disabledRec, disabledReq)
	if disabledRec.Code != http.StatusForbidden {
		t.Fatalf("disabled status=%d body=%s", disabledRec.Code, disabledRec.Body.String())
	}

	apiKeyReq := httptest.NewRequest(http.MethodGet, "/human-only", nil)
	apiKeyReq.Header.Set("Authorization", "ApiKey cc_7_test")
	apiKeyRec := httptest.NewRecorder()
	handler(apiKeyRec, apiKeyReq)
	if apiKeyRec.Code != http.StatusUnauthorized {
		t.Fatalf("api key status=%d body=%s", apiKeyRec.Code, apiKeyRec.Body.String())
	}
}

func TestBotAPIKeyMiddlewareWithDBRejectsJWTAndDisabledBot(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("bot-api-key-only-test-secret")

	botToken, err := GenerateToken(7, "active-bot", "bot@example.com")
	if err != nil {
		t.Fatalf("GenerateToken bot: %v", err)
	}
	const activeBotKey = "cc_7_runtime"
	const disabledBotKey = "cc_8_runtime"
	db := authStateTestStore{
		users: map[int64]*types.User{
			7: {ID: 7, Username: "active-bot", AccountType: types.AccountBot, State: 0},
			8: {ID: 8, Username: "disabled-bot", AccountType: types.AccountBot, State: 1},
		},
		botKeys: map[string]int64{activeBotKey: 7, disabledBotKey: 8},
	}
	handler := BotAPIKeyMiddlewareWithDB(db)(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int64{"uid": UIDFromContext(r.Context())})
	})

	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "active api key", authorization: "ApiKey " + activeBotKey, wantStatus: http.StatusOK},
		{name: "bot jwt", authorization: "Bearer " + botToken, wantStatus: http.StatusUnauthorized},
		{name: "disabled api key", authorization: "ApiKey " + disabledBotKey, wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/bot-runtime-only", nil)
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestLoginRejectsDisabledUser(t *testing.T) {
	passHash, err := bcrypt.GenerateFromPassword([]byte("pass123456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	handler := NewUserHandler(authStateTestStore{users: map[int64]*types.User{
		5: {
			ID:          5,
			Username:    "disabled",
			Email:       "disabled@example.com",
			PassHash:    passHash,
			AccountType: types.AccountHuman,
			State:       1,
		},
	}})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"account":"disabled@example.com","password":"pass123456"}`))
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestLoginPersistentTokenRequiresExplicitRequest(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("persistent-login-test-secret")

	passHash, err := bcrypt.GenerateFromPassword([]byte("pass123456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	handler := NewUserHandler(authStateTestStore{users: map[int64]*types.User{
		6: {
			ID:          6,
			Username:    "desktop-user",
			Email:       "desktop@example.com",
			PassHash:    passHash,
			AccountType: types.AccountHuman,
			State:       0,
		},
	}})

	for _, tc := range []struct {
		name       string
		body       string
		persistent bool
	}{
		{
			name: "ordinary web login",
			body: `{"account":"desktop@example.com","password":"pass123456"}`,
		},
		{
			name:       "persistent desktop login",
			body:       `{"account":"desktop@example.com","password":"pass123456","persistent":true}`,
			persistent: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.HandleLogin(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Token      string `json:"token"`
				Persistent bool   `json:"persistent"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Persistent != tc.persistent {
				t.Fatalf("persistent=%v want=%v", response.Persistent, tc.persistent)
			}
			claims, err := ParseToken(response.Token)
			if err != nil {
				t.Fatalf("ParseToken: %v", err)
			}
			if tc.persistent && claims.ExpiresAt != nil {
				t.Fatalf("persistent token unexpectedly expires at %v", claims.ExpiresAt.Time)
			}
			if !tc.persistent && claims.ExpiresAt == nil {
				t.Fatal("ordinary web login token must expire")
			}
		})
	}
}

func TestServeWSRejectsDisabledJWT(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("ws-disabled-test-secret")

	token, err := GenerateToken(10, "disabled", "disabled@example.com")
	if err != nil {
		t.Fatalf("GenerateToken disabled: %v", err)
	}

	hub := NewHub(authStateTestStore{users: map[int64]*types.User{
		10: {ID: 10, Username: "disabled", AccountType: types.AccountHuman, State: 1},
	}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v0/channels?token="+token, nil)
	rec := httptest.NewRecorder()

	ServeWS(hub, rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestServeWSRejectsDisabledBotAPIKey(t *testing.T) {
	const disabledBotKey = "cc_b_test"
	hub := NewHub(authStateTestStore{
		users: map[int64]*types.User{
			11: {ID: 11, Username: "disabled-bot", AccountType: types.AccountBot, State: 1},
		},
		botKeys: map[string]int64{disabledBotKey: 11},
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v0/channels?api_key="+disabledBotKey, nil)
	rec := httptest.NewRecorder()

	ServeWS(hub, rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}
