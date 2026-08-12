// Package server implements Cats Company user registration and authentication.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// UserHandler handles user-related API requests.
type UserHandler struct {
	db                       store.Store
	relayRegistrationCreate  func(context.Context, int64, string) error
	relayRegistrationDelays  []time.Duration
	relayRegistrationTimeout time.Duration
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(db store.Store) *UserHandler {
	return &UserHandler{
		db:                       db,
		relayRegistrationDelays:  []time.Duration{0, 2 * time.Second, 10 * time.Second},
		relayRegistrationTimeout: defaultRelayAdminTimeout,
	}
}

// SetRelayRegistrationProvisioning asynchronously provisions a relay key for
// newly registered users. Relay availability must never gate account creation.
// The create closure is idempotent: it queries the existing key first and only
// creates when none exists (query-before-create), so a retried run after a lost
// response cannot mint duplicate keys.
func (h *UserHandler) SetRelayRegistrationProvisioning(admin *RelayAdminClient) {
	if h == nil {
		return
	}
	if admin == nil {
		h.relayRegistrationCreate = nil
		return
	}
	h.relayRegistrationCreate = func(ctx context.Context, uid int64, username string) error {
		var existing relayKeyResponse
		if err := admin.Do(ctx, http.MethodGet, fmt.Sprintf("/internal/users/%d/key", uid), nil, &existing); err == nil {
			if existing.Configured {
				return nil // already provisioned; keep it idempotent
			}
		} else if !isNotFoundRelayError(err) {
			// GET failed for a non-404 reason; let the retry policy decide.
			return err
		}
		var out relayKeyResponse
		return admin.Do(ctx, http.MethodPost, fmt.Sprintf("/internal/users/%d/key", uid), relayKeyProxyRequest{
			Name:     "CatsCo 模型服务 Key",
			Username: username,
		}, &out)
	}
}

// RegisterRequest is the JSON body for user registration.
type RegisterRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Code        string `json:"code,omitempty"`
}

// SendCodeRequest is the JSON body for sending verification code.
type SendCodeRequest struct {
	Email string `json:"email"`
}

// ResetPasswordRequest is the JSON body for resetting a password by email code.
type ResetPasswordRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// LoginRequest is the JSON body for login.
type LoginRequest struct {
	Account    string `json:"account"` // 支持用户名或邮箱
	Password   string `json:"password"`
	Persistent bool   `json:"persistent,omitempty"`
}

// UpdateProfileRequest is the JSON body for updating the current user's profile.
type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// HandleSendCode handles POST /api/auth/send-code
func (h *UserHandler) HandleSendCode(w http.ResponseWriter, r *http.Request) {
	var req SendCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Email = strings.TrimSpace(req.Email)

	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email required"})
		return
	}

	// Check if email already registered
	existingUser, err := h.db.GetUserByEmail(req.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if existingUser != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email already registered"})
		return
	}

	code, err := sendVerificationCode(req.Email)
	if err != nil {
		fmt.Printf("[EMAIL_ERROR] Failed to send verification code to %s: %v\n", req.Email, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to send verification code"})
		return
	}

	resp := map[string]interface{}{"success": true}
	if exposeVerificationCodeInResponse() {
		resp["devCode"] = code
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleResetPasswordSendCode handles POST /api/auth/reset-password/send-code.
func (h *UserHandler) HandleResetPasswordSendCode(w http.ResponseWriter, r *http.Request) {
	var req SendCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Email = strings.TrimSpace(req.Email)

	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email required"})
		return
	}

	existingUser, err := h.db.GetUserByEmail(req.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	resp := map[string]interface{}{"success": true}
	if existingUser != nil {
		code, err := sendVerificationCodeForPurpose(req.Email, verificationPurposePasswordReset)
		if err != nil {
			fmt.Printf("[EMAIL_ERROR] Failed to send password reset code to %s: %v\n", req.Email, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to send verification code"})
			return
		}
		if exposeVerificationCodeInResponse() {
			resp["devCode"] = code
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleResetPassword handles POST /api/auth/reset-password.
func (h *UserHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Code = strings.TrimSpace(req.Code)

	if req.Email == "" || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email and code required"})
		return
	}
	if len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password min 6 chars"})
		return
	}

	user, err := h.db.GetUserByEmail(req.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if user == nil || !verifyCodeForPurpose(req.Email, req.Code, verificationPurposePasswordReset) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired verification code"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := h.db.UpdateUserPasswordHash(user.ID, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reset password"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// HandleRegister handles POST /api/auth/register
func (h *UserHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Username = strings.TrimSpace(req.Username)

	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email required"})
		return
	}
	if len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password min 6 chars"})
		return
	}

	email := req.Email
	username := email
	if req.Username != "" {
		username = req.Username
	} else if atIndex := strings.IndexRune(email, '@'); atIndex > 0 {
		username = email[:atIndex]
	}

	if len(username) < 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username min 3 chars"})
		return
	}

	existingEmail, err := h.db.GetUserByEmail(email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if existingEmail != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email already registered"})
		return
	}

	existingUsername, err := h.db.GetUserByUsername(username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if existingUsername != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "username taken"})
		return
	}

	if req.Code == "" || !verifyCode(email, req.Code) {
		fmt.Printf("[REGISTER_ERROR] Invalid code for %s\n", email)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired verification code"})
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)

	user := &types.User{
		Username:    username,
		Email:       email,
		DisplayName: displayName,
		AccountType: types.AccountHuman,
		PassHash:    hash,
	}

	uid, err := h.db.CreateUser(user)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email already exists"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
	h.provisionRegisteredUserRelayKey(uid, username)
}

func (h *UserHandler) provisionRegisteredUserRelayKey(uid int64, username string) {
	if h == nil || h.relayRegistrationCreate == nil || uid <= 0 {
		return
	}
	create := h.relayRegistrationCreate
	delays := append([]time.Duration(nil), h.relayRegistrationDelays...)
	if len(delays) == 0 {
		delays = []time.Duration{0}
	}
	timeout := h.relayRegistrationTimeout
	if timeout <= 0 {
		timeout = defaultRelayAdminTimeout
	}

	go func() {
		var lastErr error
		for attempt, delay := range delays {
			if delay > 0 {
				timer := time.NewTimer(delay)
				<-timer.C
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			lastErr = create(ctx, uid, username)
			cancel()
			if lastErr == nil {
				log.Printf("relay registration key provisioned: uid=%d username=%s", uid, username)
				return
			}
			log.Printf(
				"relay registration key provisioning failed: uid=%d attempt=%d/%d error=%v",
				uid,
				attempt+1,
				len(delays),
				lastErr,
			)
			if !retryableRelayError(lastErr) {
				log.Printf("relay registration key provisioning stopped: permanent error uid=%d error=%v", uid, lastErr)
				return
			}
		}
	}()
}

// retryableRelayError reports whether a relay admin failure may succeed on a
// retry. Transport/timeout errors and HTTP 408/429/5xx are retryable; permanent
// 4xx responses are not, so a doomed request is not hammered repeatedly.
func retryableRelayError(err error) bool {
	if err == nil {
		return false
	}
	var relayErr relayAdminError
	if !errors.As(err, &relayErr) {
		return true // network / transport / timeout
	}
	switch relayErr.status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return relayErr.status >= http.StatusInternalServerError
	}
}

func isNotFoundRelayError(err error) bool {
	var relayErr relayAdminError
	return errors.As(err, &relayErr) && relayErr.status == http.StatusNotFound
}

// HandleLogin handles POST /api/auth/login
func (h *UserHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	// 判断是邮箱还是用户名
	var user *types.User
	var err error
	if strings.Contains(req.Account, "@") {
		user, err = h.db.GetUserByEmail(req.Account)
	} else {
		user, err = h.db.GetUserByUsername(req.Account)
	}

	if err != nil || user == nil {
		fmt.Printf("[LOGIN_ERROR] User not found: %s, err: %v\n", req.Account, err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user not found"})
		return
	}

	if err := bcrypt.CompareHashAndPassword(user.PassHash, []byte(req.Password)); err != nil {
		fmt.Printf("[LOGIN_ERROR] Password mismatch for %s: %v\n", req.Account, err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "password mismatch"})
		return
	}
	if user.State != 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "user account is disabled"})
		return
	}

	tokenGenerator := GenerateToken
	if req.Persistent {
		tokenGenerator = GeneratePersistentUserToken
	}
	token, err := tokenGenerator(user.ID, user.Username, user.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":        token,
		"uid":          user.ID,
		"username":     user.Username,
		"email":        user.Email,
		"display_name": user.DisplayName,
		"avatar_url":   user.AvatarURL,
		"account_type": user.AccountType,
		"persistent":   req.Persistent,
	})
}

// HandleMe handles GET /api/me — returns the authenticated user's profile.
func (h *UserHandler) HandleMe(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	user, err := h.db.GetUser(uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":          user.ID,
		"username":     user.Username,
		"email":        user.Email,
		"display_name": user.DisplayName,
		"avatar_url":   user.AvatarURL,
		"account_type": user.AccountType,
		"created_at":   user.CreatedAt,
	})
}

// HandleUpdateMe handles POST /api/me/update — updates the authenticated user's profile.
func (h *UserHandler) HandleUpdateMe(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	req.DisplayName = strings.TrimSpace(req.DisplayName)

	if err := h.db.UpdateUser(uid, req.DisplayName, req.AvatarURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update profile"})
		return
	}

	user, err := h.db.GetUser(uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load updated profile"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":          user.ID,
		"username":     user.Username,
		"email":        user.Email,
		"display_name": user.DisplayName,
		"avatar_url":   user.AvatarURL,
		"account_type": user.AccountType,
	})
}

// autoAddAssistantFriend adds the default AI assistant as a friend for new users.
func autoAddAssistantFriend(db store.Store, uid int64) {
	assistant, _ := db.GetUserByUsername("ai_assistant")
	if assistant != nil {
		db.CreateFriendRequest(assistant.ID, uid, "你好！我是 AI 助手，有什么可以帮你的？")
		db.AcceptFriendRequest(assistant.ID, uid)
	}
}
