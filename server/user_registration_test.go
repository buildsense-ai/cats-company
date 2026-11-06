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
	passwordUpdates map[string]int
}

func newUserRegistrationTestStore() *userRegistrationTestStore {
	return &userRegistrationTestStore{
		usersByUsername: make(map[string]*types.User),
		usersByEmail:    make(map[string]*types.User),
		passwordUpdates: make(map[string]int),
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

func (s *userRegistrationTestStore) UpdateUserPasswordHash(uid int64, passHash []byte) error {
	for _, u := range s.createdUsers {
		if u.ID == uid {
			u.PassHash = passHash
			s.passwordUpdates[u.Email]++
			return nil
		}
	}
	return errors.New("user not found")
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
	if got := responseError(t, rec); got != "verification code required" {
		t.Fatalf("error = %q, want %q", got, "verification code required")
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

func TestHandleRegisterGeneratesInternalUsernameWhenOmitted(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "new.person@example.com"
	code := "835109"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"email":    email,
		"password": "secret123",
		"code":     code,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := db.createdUsers[0].Username; got != "new-person" {
		t.Fatalf("generated username = %q, want %q", got, "new-person")
	}
	if db.createdUsers[0].DisplayName != "" {
		t.Fatalf("display name = %q, want empty until onboarding", db.createdUsers[0].DisplayName)
	}
}

func TestHandleRegisterDisambiguatesGeneratedUsername(t *testing.T) {
	db := newUserRegistrationTestStore()
	db.usersByUsername["new-person"] = &types.User{ID: 91, Username: "new-person"}
	email := "new.person@another-example.com"
	code := "920418"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"email":    email,
		"password": "secret123",
		"code":     code,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	generated := db.createdUsers[0].Username
	if generated == "new-person" || !strings.HasPrefix(generated, "new-person-") || len(generated) > 64 {
		t.Fatalf("generated collision username = %q", generated)
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
	ready := make(chan int64, 1)
	handler := NewUserHandler(db)
	handler.SetRelayRegistrationReadyHook(func(uid int64) { ready <- uid })
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
	select {
	case uid := <-ready:
		if uid != 1 {
			t.Fatalf("relay ready uid=%d, want 1", uid)
		}
	case <-time.After(time.Second):
		t.Fatal("commercial relay initialization was not scheduled")
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

func TestHandleResetPasswordConsumesVerificationCode(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "reset-consumed@example.com"
	password := "newpass123"
	code := "482913"

	if _, err := db.CreateUser(&types.User{Username: "reset-user", Email: email, AccountType: types.AccountHuman}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	deleteVerificationCode(email, verificationPurposePasswordReset)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposePasswordReset) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposePasswordReset)

	handler := NewUserHandler(db)
	body := map[string]string{"email": email, "code": code, "password": password}

	rec := performUserRequest(t, handler.HandleResetPassword, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("first reset status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := db.passwordUpdates[email]; got != 1 {
		t.Fatalf("password updates = %d, want 1", got)
	}

	// Replaying the same consumed code must fail.
	rec2 := performUserRequest(t, handler.HandleResetPassword, body)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("replayed reset status = %d, want %d; body=%s", rec2.Code, http.StatusBadRequest, rec2.Body.String())
	}
	if got := db.passwordUpdates[email]; got != 1 {
		t.Fatalf("password updates after replay = %d, want 1", got)
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
	var posted relayKeyProxyRequest
	admin := relayAdminTestClient(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
			}, nil
		}
		posts++
		if err := json.NewDecoder(req.Body).Decode(&posted); err != nil {
			return nil, err
		}
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
	if posted.Name != "CatsCo API Key 7" || posted.Username != "u" {
		t.Fatalf("unexpected relay key create body: %+v", posted)
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

// 长邮箱是常态（企业邮箱 local part 20-30 字符很常见，理论上限 64）。
// 不填 username 时用户名由 email 推导，必须照常注册成功 —— 只是名字被截到
// maxUsernameLen 以内，不能因此报错或写进超长的名字。
func TestHandleRegisterAcceptsLongEmailLocalPart(t *testing.T) {
	cases := []struct {
		email string
		code  string
	}{
		{"zhangsan.li@company.example.com", "613204"},
		{"alexander.hernandez.rodriguez@enterprise.example.com", "613205"},
		{strings.Repeat("a", 60) + "@example.com", "613206"},
		{strings.Repeat("b", 64) + "@example.com", "613207"}, // RFC 的 local part 上限
	}
	for _, tc := range cases {
		db := newUserRegistrationTestStore()
		deleteVerificationCode(tc.email, verificationPurposeRegister)
		t.Cleanup(func() { deleteVerificationCode(tc.email, verificationPurposeRegister) })
		storeVerificationCode(tc.email, tc.code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

		rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
			"email":    tc.email,
			"password": "secret123",
			"code":     tc.code,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("email=%q (local %d chars) status = %d, want 200; body=%s",
				tc.email, len(strings.SplitN(tc.email, "@", 2)[0]), rec.Code, rec.Body.String())
		}
		if len(db.createdUsers) != 1 {
			t.Fatalf("email=%q created users = %d, want 1", tc.email, len(db.createdUsers))
		}
		got := db.createdUsers[0].Username
		if got == "" || len(got) > maxUsernameLen {
			t.Fatalf("email=%q generated username = %q (len %d), want 1..%d",
				tc.email, got, len(got), maxUsernameLen)
		}
		// 生成的名字必须落在现有用户名规则内（小写 + 数字 + _ -），便于后续拼容器名。
		if strings.ContainsAny(got, "@ .+") {
			t.Fatalf("email=%q generated username = %q 含非法字符", tc.email, got)
		}
	}
}

// username 是永久标识（没有改名入口），超长的必须在注册这一刻挡掉。
func TestHandleRegisterRejectsOverlongUsername(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "overlong-username@example.com"
	code := "552311"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"email":    email,
		"username": strings.Repeat("a", maxUsernameLen+1),
		"password": "secret123",
		"code":     code,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(db.createdUsers) != 0 {
		t.Fatalf("created %d users with overlong username", len(db.createdUsers))
	}
	if got := responseError(t, rec); !strings.Contains(got, "username max") {
		t.Fatalf("error = %q, want it to mention the username limit", got)
	}
}

// 边界正上方：正好 maxUsernameLen 应当放行。
func TestHandleRegisterAcceptsUsernameAtLimit(t *testing.T) {
	db := newUserRegistrationTestStore()
	email := "at-limit@example.com"
	code := "552312"
	deleteVerificationCode(email, verificationPurposeRegister)
	t.Cleanup(func() { deleteVerificationCode(email, verificationPurposeRegister) })
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)

	atLimit := strings.Repeat("a", maxUsernameLen)
	rec := performUserRequest(t, NewUserHandler(db).HandleRegister, map[string]string{
		"email":    email,
		"username": atLimit,
		"password": "secret123",
		"code":     code,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if db.createdUsers[0].Username != atLimit {
		t.Fatalf("username = %q, want %q", db.createdUsers[0].Username, atLimit)
	}
}
