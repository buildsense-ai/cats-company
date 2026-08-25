package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
)

const desktopConnectTTL = 5 * time.Minute

type DesktopConnectHandler struct {
	db       store.Store
	sessions *desktopConnectSessionStore
}

type desktopConnectSession struct {
	Code      string
	UID       int64
	CreatedAt time.Time
	ExpiresAt time.Time
	ClaimedAt *time.Time
}

type desktopConnectSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*desktopConnectSession
}

func NewDesktopConnectHandler(db store.Store) *DesktopConnectHandler {
	return &DesktopConnectHandler{
		db: db,
		sessions: &desktopConnectSessionStore{
			sessions: make(map[string]*desktopConnectSession),
		},
	}
}

func (h *DesktopConnectHandler) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	user, err := h.db.GetUser(uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
		return
	}
	if user.State != 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "user account is disabled"})
		return
	}

	session := h.sessions.create(uid)
	httpBaseURL, wsURL := desktopConnectBaseURLs(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code":          session.Code,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
		"http_base_url": httpBaseURL,
		"server_url":    wsURL,
		"deeplink_url":  "catsco://connect?code=" + session.Code + "&base=" + url.QueryEscape(httpBaseURL),
	})
}

func (h *DesktopConnectHandler) HandleExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	session, status, msg := h.sessions.claim(req.Code)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}

	user, err := h.db.GetUser(session.UID)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
		return
	}
	if user.State != 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "user account is disabled"})
		return
	}
	token, err := GeneratePersistentUserToken(user.ID, user.Username, user.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})
		return
	}

	httpBaseURL, wsURL := desktopConnectBaseURLs(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":         token,
		"uid":           user.ID,
		"username":      user.Username,
		"email":         user.Email,
		"display_name":  user.DisplayName,
		"avatar_url":    user.AvatarURL,
		"account_type":  user.AccountType,
		"persistent":    true,
		"http_base_url": httpBaseURL,
		"server_url":    wsURL,
	})
}

func (h *DesktopConnectHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	code := r.URL.Query().Get("code")
	session, state := h.sessions.status(code)
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "desktop connect session not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"state":      state,
		"expires_at": session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *desktopConnectSessionStore) create(uid int64) *desktopConnectSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now())
	now := time.Now()
	session := &desktopConnectSession{
		Code:      randomDesktopConnectCode(),
		UID:       uid,
		CreatedAt: now,
		ExpiresAt: now.Add(desktopConnectTTL),
	}
	s.sessions[session.Code] = session
	return session
}

func (s *desktopConnectSessionStore) claim(code string) (*desktopConnectSession, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupLocked(now)
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, http.StatusBadRequest, "code required"
	}
	session := s.sessions[code]
	if session == nil {
		return nil, http.StatusNotFound, "desktop connect session not found"
	}
	if now.After(session.ExpiresAt) {
		delete(s.sessions, code)
		return nil, http.StatusGone, "desktop connect session expired"
	}
	if session.ClaimedAt != nil {
		return nil, http.StatusConflict, "desktop connect session already used"
	}
	session.ClaimedAt = &now
	return session, 0, ""
}

func (s *desktopConnectSessionStore) status(code string) (*desktopConnectSession, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupLocked(now)
	session := s.sessions[strings.TrimSpace(code)]
	if session == nil {
		return nil, "missing"
	}
	if session.ClaimedAt != nil {
		return session, "claimed"
	}
	return session, "pending"
}

func (s *desktopConnectSessionStore) cleanupLocked(now time.Time) {
	for code, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, code)
		}
	}
}

func randomDesktopConnectCode() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}

func desktopConnectBaseURLs(r *http.Request) (string, string) {
	if requestOrigin := trustedPublicRequestOrigin(r); requestOrigin == "https://app.catsco.cn" {
		return requestOrigin, desktopConnectWSURLFromHTTPBase(requestOrigin)
	}
	if httpBaseURL, wsURL := configuredDesktopConnectBaseURLs(); httpBaseURL != "" && wsURL != "" {
		return httpBaseURL, wsURL
	}

	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = "app.catsco.cc"
		proto = "https"
	}
	httpBaseURL := proto + "://" + host
	wsProto := "ws"
	if proto == "https" {
		wsProto = "wss"
	}
	return httpBaseURL, wsProto + "://" + host + "/v0/channels"
}

func trustedPublicRequestOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if proto != "https" || (host != "app.catsco.cc" && host != "app.catsco.cn") {
		return ""
	}
	return "https://" + host
}

func configuredDesktopConnectBaseURLs() (string, string) {
	httpBaseURL := strings.TrimRight(firstEnv("CATSCO_PUBLIC_BASE_URL", "PUBLIC_BASE_URL"), "/")
	wsURL := strings.TrimRight(firstEnv("CATSCO_PUBLIC_WS_URL", "PUBLIC_WS_URL"), "/")
	if httpBaseURL == "" {
		return "", ""
	}
	if wsURL == "" {
		wsURL = desktopConnectWSURLFromHTTPBase(httpBaseURL)
	}
	if wsURL == "" {
		return "", ""
	}
	return httpBaseURL, wsURL
}

func desktopConnectWSURLFromHTTPBase(httpBaseURL string) string {
	parsed, err := url.Parse(httpBaseURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return ""
	}
	parsed.Path = "/v0/channels"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
