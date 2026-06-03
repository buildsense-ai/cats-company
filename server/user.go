// Package server implements Cats Company user registration and authentication.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// UserHandler handles user-related API requests.
type UserHandler struct {
	db store.Store
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(db store.Store) *UserHandler {
	return &UserHandler{db: db}
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
	Account  string `json:"account"` // 支持用户名或邮箱
	Password string `json:"password"`
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

	// 邮箱注册模式
	if req.Email != "" {
		if len(req.Password) < 6 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password min 6 chars"})
			return
		}

		email := req.Email
		username := email
		if req.Username != "" {
			username = req.Username
		} else {
			// 从邮箱提取用户名
			atIndex := 0
			for i, c := range email {
				if c == '@' {
					atIndex = i
					break
				}
			}
			if atIndex > 0 {
				username = email[:atIndex]
			}
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

		_, err = h.db.CreateUser(user)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email already exists"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	// 原有的用户名注册模式
	if len(req.Username) < 3 || len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username min 3 chars, password min 6 chars"})
		return
	}

	// Check if username exists
	existing, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "username taken"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Username
	}

	user := &types.User{
		Username:    req.Username,
		Email:       req.Email,
		Phone:       req.Phone,
		DisplayName: displayName,
		AccountType: types.AccountHuman,
		PassHash:    hash,
	}

	uid, err := h.db.CreateUser(user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registration failed"})
		return
	}

	token, err := GenerateToken(uid, req.Username, req.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})
		return
	}

	// Auto-add default AI assistant as friend
	autoAddAssistantFriend(h.db, uid)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token":        token,
		"uid":          uid,
		"username":     req.Username,
		"display_name": displayName,
		"avatar_url":   user.AvatarURL,
		"account_type": user.AccountType,
	})
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

	token, err := GenerateToken(user.ID, user.Username, user.Email)
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
