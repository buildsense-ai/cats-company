package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type userRegistrationTestStore struct {
	store.Store
	usersByUsername map[string]*types.User
	usersByEmail    map[string]*types.User
	createdUsers    []*types.User
}

func newUserRegistrationTestStore() *userRegistrationTestStore {
	return &userRegistrationTestStore{
		usersByUsername: make(map[string]*types.User),
		usersByEmail:    make(map[string]*types.User),
	}
}

func (s *userRegistrationTestStore) CreateUser(user *types.User) (int64, error) {
	copyUser := *user
	copyUser.ID = int64(len(s.createdUsers) + 1)
	s.createdUsers = append(s.createdUsers, &copyUser)
	s.usersByUsername[copyUser.Username] = &copyUser
	if copyUser.Email != "" {
		s.usersByEmail[copyUser.Email] = &copyUser
	}
	return copyUser.ID, nil
}

func (s *userRegistrationTestStore) GetUserByUsername(username string) (*types.User, error) {
	return s.usersByUsername[username], nil
}

func (s *userRegistrationTestStore) GetUserByEmail(email string) (*types.User, error) {
	return s.usersByEmail[email], nil
}

func performUserRequest(t *testing.T, handler http.HandlerFunc, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func responseError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	value, _ := payload["error"].(string)
	return value
}

func TestHandleRegisterRequiresEmail(t *testing.T) {
	db := newUserRegistrationTestStore()
	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"username": "username-only",
		"password": "secret123",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := responseError(t, rec); got != "email required" {
		t.Fatalf("error = %q, want %q", got, "email required")
	}
	if len(db.createdUsers) != 0 {
		t.Fatalf("created %d users without an email", len(db.createdUsers))
	}
}

func TestHandleRegisterRequiresVerificationCode(t *testing.T) {
	db := newUserRegistrationTestStore()
	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"email":    "unverified-registration@example.com",
		"username": "unverified-user",
		"password": "secret123",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := responseError(t, rec); got != "invalid or expired verification code" {
		t.Fatalf("error = %q, want %q", got, "invalid or expired verification code")
	}
	if len(db.createdUsers) != 0 {
		t.Fatalf("created %d users without a verification code", len(db.createdUsers))
	}
}

func TestHandleRegisterAcceptsVerifiedEmail(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "verified-registration@example.com"
	code := "613204"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"email":        email,
		"username":     "verified-user",
		"password":     "secret123",
		"display_name": "Verified User",
		"code":         code,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(db.createdUsers) != 1 {
		t.Fatalf("created users = %d, want 1", len(db.createdUsers))
	}
	created := db.createdUsers[0]
	if created.Email != email || created.Username != "verified-user" {
		t.Fatalf("created user = %#v", created)
	}
	if err := bcrypt.CompareHashAndPassword(created.PassHash, []byte("secret123")); err != nil {
		t.Fatalf("stored password hash does not match: %v", err)
	}
}

func TestHandleLoginAllowsExistingUserWithoutEmail(t *testing.T) {
	db := newUserRegistrationTestStore()
	hash, err := bcrypt.GenerateFromPassword([]byte("legacy123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	db.usersByUsername["legacy-user"] = &types.User{
		ID:          93,
		Username:    "legacy-user",
		DisplayName: "Legacy User",
		AccountType: types.AccountHuman,
		PassHash:    hash,
	}

	rec := performUserRequest(t, NewUserHandler(db).HandleLogin, map[string]string{
		"account":  "legacy-user",
		"password": "legacy123",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["token"] == "" || payload["username"] != "legacy-user" {
		t.Fatalf("unexpected login response: %#v", payload)
	}
}
