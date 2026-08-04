package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandleRegisterAsynchronouslyProvisionsRelayKey(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "relay-registration@example.com"
	code := "794215"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	type provisionCall struct {
		uid      int64
		username string
	}
	calls := make(chan provisionCall, 1)
	handler := NewUserHandler(db)
	handler.relayRegistrationDelays = []time.Duration{0}
	handler.relayRegistrationCreate = func(_ context.Context, uid int64, username string) error {
		calls <- provisionCall{uid: uid, username: username}
		return nil
	}

	rec := performUserRequest(t, handler.HandleRegister, map[string]string{
		"email":    email,
		"username": "relay-user",
		"password": "secret123",
		"code":     code,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	select {
	case call := <-calls:
		if call.uid != 1 || call.username != "relay-user" {
			t.Fatalf("relay provision call = %+v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("relay key provisioning was not scheduled")
	}
}

func TestHandleRegisterIgnoresRelayProvisioningFailure(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "relay-registration-failure@example.com"
	code := "137864"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	called := make(chan struct{}, 1)
	handler := NewUserHandler(db)
	handler.relayRegistrationDelays = []time.Duration{0}
	handler.relayRegistrationCreate = func(_ context.Context, _ int64, _ string) error {
		called <- struct{}{}
		return errors.New("relay unavailable")
	}

	rec := performUserRequest(t, handler.HandleRegister, map[string]string{
		"email":    email,
		"username": "relay-failure-user",
		"password": "secret123",
		"code":     code,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(db.createdUsers) != 1 {
		t.Fatalf("created users = %d, want 1", len(db.createdUsers))
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("relay provisioning failure path was not exercised")
	}
}

func TestProvisionRelayKeyStopsOnPermanentError(t *testing.T) {
	handler := NewUserHandler(nil)
	handler.relayRegistrationDelays = []time.Duration{0, 0, 0}
	calls := make(chan struct{}, 10)
	handler.relayRegistrationCreate = func(_ context.Context, _ int64, _ string) error {
		calls <- struct{}{}
		return relayAdminError{status: http.StatusBadRequest, message: "bad request"}
	}
	handler.provisionRegisteredUserRelayKey(7, "u")

	count := 0
	for {
		select {
		case <-calls:
			count++
			if count > 1 {
				t.Fatalf("permanent error was retried: calls=%d", count)
			}
		case <-time.After(300 * time.Millisecond):
			if count != 1 {
				t.Fatalf("permanent error calls = %d, want 1", count)
			}
			return
		}
	}
}

func TestProvisionRelayKeyRetriesTransientHTTPAndNetworkErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "http 500", err: relayAdminError{status: http.StatusInternalServerError, message: "boom"}},
		{name: "http 429", err: relayAdminError{status: http.StatusTooManyRequests, message: "slow down"}},
		{name: "network error", err: errors.New("connection reset")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewUserHandler(nil)
			handler.relayRegistrationDelays = []time.Duration{0, 0}
			calls := make(chan struct{}, 10)
			handler.relayRegistrationCreate = func(_ context.Context, _ int64, _ string) error {
				calls <- struct{}{}
				return tc.err
			}
			handler.provisionRegisteredUserRelayKey(7, "u")

			time.Sleep(200 * time.Millisecond)
			if got := len(calls); got != 2 {
				t.Fatalf("transient error calls = %d, want 2", got)
			}
		})
	}
}

func relayAdminTestClient(handler func(*http.Request) (*http.Response, error)) *RelayAdminClient {
	admin := &RelayAdminClient{baseURL: "http://relay.test", token: "t"}
	admin.client = &http.Client{Transport: roundTripFunc(handler)}
	return admin
}

func TestRelayRegistrationCreateSkipsWhenKeyAlreadyProvisioned(t *testing.T) {
	var posts int
	admin := relayAdminTestClient(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"configured":true}`)),
			}, nil
		}
		posts++
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})
	handler := NewUserHandler(nil)
	handler.SetRelayRegistrationProvisioning(admin)
	if handler.relayRegistrationCreate == nil {
		t.Fatal("relayRegistrationCreate not wired")
	}
	if err := handler.relayRegistrationCreate(context.Background(), 7, "u"); err != nil {
		t.Fatalf("create returned error for provisioned key: %v", err)
	}
	if posts != 0 {
		t.Fatalf("POST issued for already-provisioned key: %d", posts)
	}
}

func TestRelayRegistrationCreateCreatesWhenKeyNotFound(t *testing.T) {
	var posts int
	admin := relayAdminTestClient(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
			}, nil
		}
		posts++
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})
	handler := NewUserHandler(nil)
	handler.SetRelayRegistrationProvisioning(admin)
	if err := handler.relayRegistrationCreate(context.Background(), 7, "u"); err != nil {
		t.Fatalf("create returned error for missing key: %v", err)
	}
	if posts != 1 {
		t.Fatalf("POST issued = %d, want 1", posts)
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
