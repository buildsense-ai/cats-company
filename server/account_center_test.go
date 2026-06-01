package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type accountTestUserLookup struct {
	users map[int64]*types.User
	err   error
}

func (s accountTestUserLookup) GetUser(id int64) (*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.users[id], nil
}

func (s accountTestUserLookup) GetUserByUsername(username string) (*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	for _, user := range s.users {
		if strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	return nil, nil
}

func (s accountTestUserLookup) GetUserByEmail(email string) (*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	for _, user := range s.users {
		if strings.EqualFold(user.Email, email) {
			return user, nil
		}
	}
	return nil, nil
}

func (s accountTestUserLookup) ListAdminUsers(query string, limit, offset int) ([]*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 20
	}
	var all []*types.User
	for _, user := range s.users {
		if query == "" ||
			strings.Contains(strings.ToLower(user.Username), query) ||
			strings.Contains(strings.ToLower(user.Email), query) ||
			strings.Contains(strings.ToLower(user.DisplayName), query) ||
			strings.EqualFold(strconvFormatInt64(user.ID), query) {
			all = append(all, user)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if offset >= len(all) {
		return []*types.User{}, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (s accountTestUserLookup) CountAdminUsers(query string) (int, error) {
	users, err := s.ListAdminUsers(query, len(s.users), 0)
	if err != nil {
		return 0, err
	}
	return len(users), nil
}

func (s accountTestUserLookup) UpdateUserState(uid int64, state int) error {
	if s.err != nil {
		return s.err
	}
	if user := s.users[uid]; user != nil {
		user.State = state
	}
	return nil
}

func (s accountTestUserLookup) SearchUsers(query string, limit int) ([]*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = len(s.users)
	}
	var out []*types.User
	for _, user := range s.users {
		if strings.Contains(strings.ToLower(user.Username), query) || strings.Contains(strings.ToLower(user.DisplayName), query) {
			out = append(out, user)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func strconvFormatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestEnvAccountServiceVerifierSupportsPlainAndHashTokens(t *testing.T) {
	sum := sha256.Sum256([]byte("hashed-secret"))
	verifier, err := NewEnvAccountServiceVerifier("relay=plain-secret;worker=sha256:" + hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("NewEnvAccountServiceVerifier: %v", err)
	}

	if service, ok := verifier.Verify("plain-secret"); !ok || service.Slug != "relay" {
		t.Fatalf("plain token not verified: service=%+v ok=%v", service, ok)
	}
	if service, ok := verifier.Verify("hashed-secret"); !ok || service.Slug != "worker" {
		t.Fatalf("hashed token not verified: service=%+v ok=%v", service, ok)
	}
	if _, ok := verifier.Verify("wrong-secret"); ok {
		t.Fatal("unexpected verification success for wrong token")
	}
}

func TestAccountServiceVerifierSupportsDatabaseTokens(t *testing.T) {
	store := newAccountTestAuthServiceStore()
	token, prefix, hash, err := GenerateAccountServiceToken()
	if err != nil {
		t.Fatalf("GenerateAccountServiceToken: %v", err)
	}
	if _, err := store.CreateAuthService(&types.AuthService{
		Slug:        "cats-relay",
		Name:        "Cats Relay",
		TokenPrefix: prefix,
		TokenHash:   hash,
		Scopes:      []string{"account.introspect"},
		State:       0,
	}); err != nil {
		t.Fatalf("CreateAuthService: %v", err)
	}

	verifier, err := NewAccountServiceVerifier("", store)
	if err != nil {
		t.Fatalf("NewAccountServiceVerifier: %v", err)
	}
	service, ok := verifier.Verify(token)
	if !ok {
		t.Fatal("expected database token to verify")
	}
	if service.Slug != "cats-relay" || service.Source != "db" {
		t.Fatalf("unexpected service: %+v", service)
	}
}

func TestAccountServiceVerifierRejectsRevokedDatabaseTokens(t *testing.T) {
	store := newAccountTestAuthServiceStore()
	token, prefix, hash, err := GenerateAccountServiceToken()
	if err != nil {
		t.Fatalf("GenerateAccountServiceToken: %v", err)
	}
	id, err := store.CreateAuthService(&types.AuthService{
		Slug:        "cats-relay",
		Name:        "Cats Relay",
		TokenPrefix: prefix,
		TokenHash:   hash,
		State:       0,
	})
	if err != nil {
		t.Fatalf("CreateAuthService: %v", err)
	}
	if err := store.RevokeAuthService(id); err != nil {
		t.Fatalf("RevokeAuthService: %v", err)
	}

	verifier, err := NewAccountServiceVerifier("", store)
	if err != nil {
		t.Fatalf("NewAccountServiceVerifier: %v", err)
	}
	if _, ok := verifier.Verify(token); ok {
		t.Fatal("unexpected verification success for revoked token")
	}
}

func createTestDBServiceToken(t *testing.T, store *accountTestAuthServiceStore, slug string, scopes []string) string {
	t.Helper()
	token, prefix, hash, err := GenerateAccountServiceToken()
	if err != nil {
		t.Fatalf("GenerateAccountServiceToken: %v", err)
	}
	if _, err := store.CreateAuthService(&types.AuthService{
		Slug:        slug,
		Name:        slug,
		TokenPrefix: prefix,
		TokenHash:   hash,
		Scopes:      scopes,
		State:       0,
	}); err != nil {
		t.Fatalf("CreateAuthService: %v", err)
	}
	return token
}

func TestAccountCenterIntrospectActiveToken(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("account-center-test-secret")
	token, err := GenerateToken(42, "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	verifier, err := NewEnvAccountServiceVerifier("relay=service-secret")
	if err != nil {
		t.Fatalf("NewEnvAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{
		42: {
			ID:          42,
			Username:    "alice",
			Email:       "alice@example.com",
			DisplayName: "Alice",
			AccountType: types.AccountHuman,
			State:       0,
			CreatedAt:   time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
		},
	}}, verifier)

	req := httptest.NewRequest(http.MethodPost, "/api/account/introspect", strings.NewReader(`{"token":"`+token+`"}`))
	req.Header.Set("Authorization", "Service service-secret")
	rec := httptest.NewRecorder()

	handler.HandleIntrospect(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["active"] != true {
		t.Fatalf("expected active token, got %v", body)
	}
	user, ok := body["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing user payload: %v", body)
	}
	if user["uid"].(float64) != 42 || user["username"] != "alice" {
		t.Fatalf("unexpected user payload: %v", user)
	}
}

func TestAccountCenterIntrospectInvalidUserToken(t *testing.T) {
	verifier, err := NewEnvAccountServiceVerifier("relay=service-secret")
	if err != nil {
		t.Fatalf("NewEnvAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{}}, verifier)

	req := httptest.NewRequest(http.MethodPost, "/api/account/introspect", strings.NewReader(`{"token":"not-a-jwt"}`))
	req.Header.Set("Authorization", "Service service-secret")
	rec := httptest.NewRecorder()

	handler.HandleIntrospect(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["active"] != false || body["error"] != "invalid_or_expired_token" {
		t.Fatalf("unexpected invalid token response: %v", body)
	}
}

func TestAccountCenterIntrospectDisabledUserToken(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("account-center-disabled-test-secret")
	token, err := GenerateToken(88, "disabled", "disabled@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	verifier, err := NewEnvAccountServiceVerifier("relay=service-secret")
	if err != nil {
		t.Fatalf("NewEnvAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{
		88: {
			ID:          88,
			Username:    "disabled",
			Email:       "disabled@example.com",
			AccountType: types.AccountHuman,
			State:       1,
		},
	}}, verifier)

	req := httptest.NewRequest(http.MethodPost, "/api/account/introspect", strings.NewReader(`{"token":"`+token+`"}`))
	req.Header.Set("Authorization", "Service service-secret")
	rec := httptest.NewRecorder()

	handler.HandleIntrospect(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["active"] != false || body["error"] != "user_not_available" {
		t.Fatalf("unexpected disabled token response: %v", body)
	}
}

func TestAccountCenterRejectsMissingServiceToken(t *testing.T) {
	verifier, err := NewEnvAccountServiceVerifier("relay=service-secret")
	if err != nil {
		t.Fatalf("NewEnvAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{}}, verifier)

	req := httptest.NewRequest(http.MethodPost, "/api/account/introspect", strings.NewReader(`{"token":"x"}`))
	rec := httptest.NewRecorder()

	handler.HandleIntrospect(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAccountCenterGetUser(t *testing.T) {
	verifier, err := NewEnvAccountServiceVerifier("relay=service-secret")
	if err != nil {
		t.Fatalf("NewEnvAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{
		7: {
			ID:          7,
			Username:    "bob",
			DisplayName: "Bob",
			AccountType: types.AccountHuman,
			State:       0,
		},
	}}, verifier)

	req := httptest.NewRequest(http.MethodGet, "/api/account/users/7", nil)
	req.Header.Set("Authorization", "Service service-secret")
	rec := httptest.NewRecorder()

	handler.HandleGetUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["uid"].(float64) != 7 || body["username"] != "bob" {
		t.Fatalf("unexpected user response: %v", body)
	}
}

func TestAccountCenterEnforcesDatabaseTokenScopes(t *testing.T) {
	store := newAccountTestAuthServiceStore()
	usersReadToken := createTestDBServiceToken(t, store, "users-reader", []string{"account.users.read"})
	introspectToken := createTestDBServiceToken(t, store, "login-checker", []string{"account.introspect"})
	verifier, err := NewAccountServiceVerifier("", store)
	if err != nil {
		t.Fatalf("NewAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{
		7: {ID: 7, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman, State: 0},
	}}, verifier)

	introspectReq := httptest.NewRequest(http.MethodPost, "/api/account/introspect", strings.NewReader(`{"token":"x"}`))
	introspectReq.Header.Set("Authorization", "Service "+usersReadToken)
	introspectRec := httptest.NewRecorder()
	handler.HandleIntrospect(introspectRec, introspectReq)
	if introspectRec.Code != http.StatusForbidden {
		t.Fatalf("introspect status=%d body=%s", introspectRec.Code, introspectRec.Body.String())
	}

	allowedIntrospectReq := httptest.NewRequest(http.MethodPost, "/api/account/introspect", strings.NewReader(`{"token":"x"}`))
	allowedIntrospectReq.Header.Set("Authorization", "Service "+introspectToken)
	allowedIntrospectRec := httptest.NewRecorder()
	handler.HandleIntrospect(allowedIntrospectRec, allowedIntrospectReq)
	if allowedIntrospectRec.Code != http.StatusOK {
		t.Fatalf("allowed introspect status=%d body=%s", allowedIntrospectRec.Code, allowedIntrospectRec.Body.String())
	}

	getUserReq := httptest.NewRequest(http.MethodGet, "/api/account/users/7", nil)
	getUserReq.Header.Set("Authorization", "Service "+introspectToken)
	getUserRec := httptest.NewRecorder()
	handler.HandleGetUser(getUserRec, getUserReq)
	if getUserRec.Code != http.StatusForbidden {
		t.Fatalf("get user status=%d body=%s", getUserRec.Code, getUserRec.Body.String())
	}

	allowedGetUserReq := httptest.NewRequest(http.MethodGet, "/api/account/users/7", nil)
	allowedGetUserReq.Header.Set("Authorization", "Service "+usersReadToken)
	allowedGetUserRec := httptest.NewRecorder()
	handler.HandleGetUser(allowedGetUserRec, allowedGetUserReq)
	if allowedGetUserRec.Code != http.StatusOK {
		t.Fatalf("allowed get user status=%d body=%s", allowedGetUserRec.Code, allowedGetUserRec.Body.String())
	}
}

func TestAccountCenterEmptyDatabaseScopesAllowCurrentEndpoints(t *testing.T) {
	store := newAccountTestAuthServiceStore()
	token := createTestDBServiceToken(t, store, "legacy-service", nil)
	verifier, err := NewAccountServiceVerifier("", store)
	if err != nil {
		t.Fatalf("NewAccountServiceVerifier: %v", err)
	}
	handler := NewAccountCenterHandler(accountTestUserLookup{users: map[int64]*types.User{
		7: {ID: 7, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman, State: 0},
	}}, verifier)

	req := httptest.NewRequest(http.MethodGet, "/api/account/users/7", nil)
	req.Header.Set("Authorization", "Service "+token)
	rec := httptest.NewRecorder()
	handler.HandleGetUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
