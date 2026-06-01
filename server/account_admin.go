package server

import (
	"embed"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

//go:embed account_admin.html
var accountAdminAssets embed.FS

// AccountAdminHandler exposes a tiny local-only operator UI for the account
// center. It intentionally lives outside /api so public nginx routes do not
// expose it.
type AccountAdminHandler struct {
	users        AccountAdminUserLookup
	services     AccountServiceVerifier
	serviceStore AccountAdminServiceStore
}

type AccountAdminUserLookup interface {
	AccountUserLookup
	GetUserByUsername(username string) (*types.User, error)
	GetUserByEmail(email string) (*types.User, error)
	ListAdminUsers(query string, limit, offset int) ([]*types.User, error)
	CountAdminUsers(query string) (int, error)
	SearchUsers(query string, limit int) ([]*types.User, error)
	UpdateUserState(uid int64, state int) error
}

type AccountAdminServiceStore interface {
	CreateAuthService(service *types.AuthService) (int64, error)
	ListAuthServices() ([]*types.AuthService, error)
	RevokeAuthService(id int64) error
}

func NewAccountAdminHandler(users AccountAdminUserLookup, services AccountServiceVerifier, serviceStore AccountAdminServiceStore) *AccountAdminHandler {
	return &AccountAdminHandler{users: users, services: services, serviceStore: serviceStore}
}

func (h *AccountAdminHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}

	body, err := accountAdminAssets.ReadFile("account_admin.html")
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin page unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (h *AccountAdminHandler) HandleUserLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}

	uid, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("uid")), 10, 64)
	if err != nil || uid <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	user, err := h.users.GetUser(uid)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if user == nil {
		writeAccountAdminJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"user": accountUserPayload(user),
	})
}

func (h *AccountAdminHandler) HandleUserList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}

	page := parseAccountAdminPositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parseAccountAdminPositiveInt(r.URL.Query().Get("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	offset := (page - 1) * pageSize

	users, err := h.users.ListAdminUsers(query, pageSize, offset)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	count, err := h.users.CountAdminUsers(query)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	payload := make([]accountUserResponse, 0, len(users))
	for _, user := range users {
		payload = append(payload, accountUserPayload(user))
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"users":      payload,
		"count":      count,
		"page":       page,
		"page_size":  pageSize,
		"total_page": accountAdminTotalPages(count, pageSize),
		"query":      query,
	})
}

func (h *AccountAdminHandler) HandleUserSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "empty query"})
		return
	}

	users, err := h.searchUsers(query)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}

	payload := make([]accountUserResponse, 0, len(users))
	for _, user := range users {
		payload = append(payload, accountUserPayload(user))
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"users": payload,
		"count": len(payload),
	})
}

func (h *AccountAdminHandler) HandleUserState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}

	var req struct {
		UID   int64 `json:"uid"`
		State int   `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UID <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user state request"})
		return
	}
	if req.State != 0 && req.State != 1 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported user state"})
		return
	}
	user, err := h.users.GetUser(req.UID)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if user == nil {
		writeAccountAdminJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if err := h.users.UpdateUserState(req.UID, req.State); err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update user state"})
		return
	}
	user.State = req.State
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"user": accountUserPayload(user),
	})
}

func (h *AccountAdminHandler) searchUsers(query string) ([]*types.User, error) {
	const limit = 20

	seen := map[int64]bool{}
	var out []*types.User
	add := func(user *types.User) {
		if user == nil || seen[user.ID] {
			return
		}
		seen[user.ID] = true
		out = append(out, user)
	}

	if uid, err := strconv.ParseInt(query, 10, 64); err == nil && uid > 0 {
		user, err := h.users.GetUser(uid)
		if err != nil {
			return nil, err
		}
		add(user)
		return out, nil
	}

	if strings.Contains(query, "@") {
		user, err := h.users.GetUserByEmail(query)
		if err != nil {
			return nil, err
		}
		add(user)
	}

	user, err := h.users.GetUserByUsername(query)
	if err != nil {
		return nil, err
	}
	add(user)

	matches, err := h.users.SearchUsers(query, limit)
	if err != nil {
		return nil, err
	}
	for _, match := range matches {
		if match == nil || seen[match.ID] {
			continue
		}
		full, err := h.users.GetUser(match.ID)
		if err != nil {
			return nil, err
		}
		add(full)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func parseAccountAdminPositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func accountAdminTotalPages(count, pageSize int) int {
	if count <= 0 || pageSize <= 0 {
		return 0
	}
	return (count + pageSize - 1) / pageSize
}

func (h *AccountAdminHandler) HandleServices(w http.ResponseWriter, r *http.Request) {
	if !h.requireLocal(w, r) {
		return
	}
	if h.serviceStore == nil {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth service store unavailable"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleListServices(w, r)
	case http.MethodPost:
		h.handleCreateService(w, r)
	default:
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *AccountAdminHandler) HandleRevokeService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}
	if h.serviceStore == nil {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth service store unavailable"})
		return
	}

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid service id"})
		return
	}
	if err := h.serviceStore.RevokeAuthService(req.ID); err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke service"})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *AccountAdminHandler) handleListServices(w http.ResponseWriter, _ *http.Request) {
	services, err := h.serviceStore.ListAuthServices()
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list services"})
		return
	}
	if services == nil {
		services = []*types.AuthService{}
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"services": services})
}

func (h *AccountAdminHandler) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug   string   `json:"slug"`
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	slug := normalizeAuthServiceSlug(req.Slug)
	if slug == "" {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid slug"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = slug
	}
	scopes := normalizeAuthServiceScopes(req.Scopes)
	token, tokenPrefix, tokenHash, err := GenerateAccountServiceToken()
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	id, err := h.serviceStore.CreateAuthService(&types.AuthService{
		Slug:        slug,
		Name:        name,
		TokenPrefix: tokenPrefix,
		TokenHash:   tokenHash,
		Scopes:      scopes,
		State:       0,
	})
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create service"})
		return
	}

	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"service": map[string]interface{}{
			"id":           id,
			"slug":         slug,
			"name":         name,
			"token_prefix": tokenPrefix,
			"scopes":       scopes,
			"state":        0,
		},
		"token": token,
	})
}

func (h *AccountAdminHandler) requireLocal(w http.ResponseWriter, r *http.Request) bool {
	if isLocalAdminRequest(r) {
		return true
	}
	writeAccountAdminJSON(w, http.StatusForbidden, map[string]string{"error": "local access only"})
	return false
}

func isLocalAdminRequest(r *http.Request) bool {
	if !isLocalAdminAddress(r.RemoteAddr) {
		return false
	}
	for _, raw := range forwardedAdminAddresses(r) {
		if !isLocalAdminAddress(raw) {
			return false
		}
	}
	return true
}

func isLocalAdminAddress(raw string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		host = raw
	}
	host = strings.Trim(strings.TrimSpace(host), `"[]`)
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// SSH tunnels and Docker host port forwarding commonly appear as loopback
	// or a private bridge address inside the container. Public clients are not
	// accepted here, and the route is not exposed by public nginx config.
	return ip.IsLoopback() || ip.IsPrivate()
}

func forwardedAdminAddresses(r *http.Request) []string {
	var out []string
	for _, item := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		out = append(out, realIP)
	}
	for _, forwarded := range r.Header.Values("Forwarded") {
		for _, section := range strings.Split(forwarded, ",") {
			for _, part := range strings.Split(section, ";") {
				key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if ok && strings.EqualFold(key, "for") {
					out = append(out, strings.TrimSpace(value))
				}
			}
		}
	}
	return out
}

var authServiceSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,62}[a-z0-9]$`)

func normalizeAuthServiceSlug(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !authServiceSlugPattern.MatchString(slug) {
		return ""
	}
	return slug
}

func normalizeAuthServiceScopes(scopes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	return out
}

func writeAccountAdminJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
