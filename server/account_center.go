package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openchat/openchat/server/store/types"
)

// AccountUserLookup is the narrow user boundary needed by the account center.
// Keeping this small makes the module easier to extract into a standalone auth
// service later.
type AccountUserLookup interface {
	GetUser(id int64) (*types.User, error)
}

type AccountService struct {
	ID     int64    `json:"id,omitempty"`
	Slug   string   `json:"slug"`
	Scopes []string `json:"scopes,omitempty"`
	Source string   `json:"source,omitempty"`
}

type AccountServiceVerifier interface {
	Configured() bool
	Verify(token string) (AccountService, bool)
}

type AccountServiceRegistry interface {
	GetAuthServiceByTokenHash(tokenHash string) (*types.AuthService, error)
	TouchAuthServiceLastUsed(id int64) error
}

type envAccountServiceVerifier struct {
	tokens map[string][32]byte
}

type accountServiceVerifier struct {
	env      AccountServiceVerifier
	registry AccountServiceRegistry
}

// NewEnvAccountServiceVerifier builds a service-token verifier from env config.
//
// Accepted forms, separated by comma, semicolon, or newline:
//
//	relay=plain-token
//	relay:plain-token
//	relay=sha256:<hex-encoded-sha256>
func NewEnvAccountServiceVerifier(raw string) (AccountServiceVerifier, error) {
	verifier := &envAccountServiceVerifier{tokens: map[string][32]byte{}}
	for _, item := range splitServiceTokenConfig(raw) {
		slug, secret, ok := strings.Cut(item, "=")
		if !ok {
			slug, secret, ok = strings.Cut(item, ":")
		}
		slug = strings.TrimSpace(slug)
		secret = strings.TrimSpace(secret)
		if slug == "" || secret == "" {
			return nil, fmt.Errorf("invalid service token entry")
		}

		var sum [32]byte
		if strings.HasPrefix(secret, "sha256:") {
			decoded, err := hex.DecodeString(strings.TrimPrefix(secret, "sha256:"))
			if err != nil || len(decoded) != sha256.Size {
				return nil, fmt.Errorf("invalid sha256 service token for %s", slug)
			}
			copy(sum[:], decoded)
		} else {
			sum = sha256.Sum256([]byte(secret))
		}
		verifier.tokens[slug] = sum
	}
	return verifier, nil
}

func NewAccountServiceVerifier(raw string, registry AccountServiceRegistry) (AccountServiceVerifier, error) {
	envVerifier, err := NewEnvAccountServiceVerifier(raw)
	if err != nil {
		return nil, err
	}
	return &accountServiceVerifier{env: envVerifier, registry: registry}, nil
}

func splitServiceTokenConfig(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	var out []string
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func (v *envAccountServiceVerifier) Configured() bool {
	return v != nil && len(v.tokens) > 0
}

func (v *envAccountServiceVerifier) Verify(token string) (AccountService, bool) {
	if v == nil || token == "" {
		return AccountService{}, false
	}
	sum := sha256.Sum256([]byte(token))
	for slug, expected := range v.tokens {
		if subtle.ConstantTimeCompare(sum[:], expected[:]) == 1 {
			return AccountService{Slug: slug, Source: "env"}, true
		}
	}
	return AccountService{}, false
}

func (v *accountServiceVerifier) Configured() bool {
	return (v.env != nil && v.env.Configured()) || v.registry != nil
}

func (v *accountServiceVerifier) Verify(token string) (AccountService, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return AccountService{}, false
	}
	if v.env != nil {
		if service, ok := v.env.Verify(token); ok {
			return service, true
		}
	}
	if v.registry == nil {
		return AccountService{}, false
	}
	dbService, err := v.registry.GetAuthServiceByTokenHash(HashAccountServiceToken(token))
	if err != nil || dbService == nil {
		return AccountService{}, false
	}
	_ = v.registry.TouchAuthServiceLastUsed(dbService.ID)
	return AccountService{
		ID:     dbService.ID,
		Slug:   dbService.Slug,
		Scopes: dbService.Scopes,
		Source: "db",
	}, true
}

func GenerateAccountServiceToken() (token string, prefix string, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = "cats_svc_" + base64.RawURLEncoding.EncodeToString(raw)
	prefix = token
	if len(prefix) > 20 {
		prefix = prefix[:20]
	}
	return token, prefix, HashAccountServiceToken(token), nil
}

func HashAccountServiceToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

type AccountCenterHandler struct {
	users    AccountUserLookup
	services AccountServiceVerifier
}

func NewAccountCenterHandler(users AccountUserLookup, services AccountServiceVerifier) *AccountCenterHandler {
	return &AccountCenterHandler{users: users, services: services}
}

type accountIntrospectRequest struct {
	Token string `json:"token"`
}

type accountUserResponse struct {
	UID         int64             `json:"uid"`
	Username    string            `json:"username"`
	Email       string            `json:"email,omitempty"`
	DisplayName string            `json:"display_name"`
	AvatarURL   string            `json:"avatar_url,omitempty"`
	AccountType types.AccountType `json:"account_type"`
	State       int               `json:"state"`
	CreatedAt   *time.Time        `json:"created_at,omitempty"`
}

func accountUserPayload(user *types.User) accountUserResponse {
	resp := accountUserResponse{
		UID:         user.ID,
		Username:    user.Username,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		AvatarURL:   user.AvatarURL,
		AccountType: user.AccountType,
		State:       user.State,
	}
	if !user.CreatedAt.IsZero() {
		resp.CreatedAt = &user.CreatedAt
	}
	return resp
}

func (h *AccountCenterHandler) HandleIntrospect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	service, ok := h.requireService(w, r)
	if !ok {
		return
	}
	if !accountServiceAllowsScope(service, "account.introspect") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "service scope denied"})
		return
	}

	var req accountIntrospectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
		return
	}

	claims, err := ParseToken(req.Token)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"active": false,
			"error":  "invalid_or_expired_token",
		})
		return
	}

	user, err := h.users.GetUser(claims.UID)
	if err != nil || user == nil || user.State != 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"active": false,
			"error":  "user_not_available",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active": true,
		"user":   accountUserPayload(user),
		"claims": map[string]interface{}{
			"issuer":     claims.Issuer,
			"issued_at":  jwtTime(claims.IssuedAt),
			"expires_at": jwtTime(claims.ExpiresAt),
		},
	})
}

func (h *AccountCenterHandler) HandleGetUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	service, ok := h.requireService(w, r)
	if !ok {
		return
	}
	if !accountServiceAllowsScope(service, "account.users.read") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "service scope denied"})
		return
	}

	rawUID := strings.TrimPrefix(r.URL.Path, "/api/account/users/")
	uid, err := strconv.ParseInt(rawUID, 10, 64)
	if err != nil || uid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	user, err := h.users.GetUser(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	writeJSON(w, http.StatusOK, accountUserPayload(user))
}

func (h *AccountCenterHandler) requireService(w http.ResponseWriter, r *http.Request) (AccountService, bool) {
	if h.services == nil || !h.services.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "account center service tokens are not configured"})
		return AccountService{}, false
	}

	token := extractServiceToken(r)
	service, ok := h.services.Verify(token)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid service token"})
		return AccountService{}, false
	}
	return service, true
}

func extractServiceToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Service ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Service "))
	}
	return ""
}

func accountServiceAllowsScope(service AccountService, scope string) bool {
	// Empty scopes keep legacy/internal tokens compatible and mean the service
	// can use all current account-center endpoints. Once scopes are configured,
	// they are enforced per endpoint.
	if len(service.Scopes) == 0 {
		return true
	}
	for _, item := range service.Scopes {
		if strings.EqualFold(strings.TrimSpace(item), scope) {
			return true
		}
	}
	return false
}

func jwtTime(t *jwt.NumericDate) interface{} {
	if t == nil {
		return nil
	}
	return t.Time
}
