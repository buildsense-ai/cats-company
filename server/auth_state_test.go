package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type authStateTestStore struct {
	store.Store
	users   map[int64]*types.User
	botKeys map[string]int64
}

func (s authStateTestStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
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
