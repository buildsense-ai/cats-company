package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const fileMediaCookieName = "catsco_file_media"
const fileMediaSessionTTL = 15 * time.Minute

// FileMediaAuth authenticates file reads. The caller must separately authorize
// UIDFromContext against the message containing the requested file.
type FileMediaAuth struct {
	db       store.Store
	sessions store.FileAccessStore
	hub      *Hub
}

func NewFileMediaAuth(db store.Store, hub *Hub) *FileMediaAuth {
	sessions, _ := db.(store.FileAccessStore)
	return &FileMediaAuth{db: db, sessions: sessions, hub: hub}
}

func fileMediaHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// headerActor never falls back to cookies or URL credentials. A supplied header
// is authoritative, including when it is invalid.
func (a *FileMediaAuth) headerActor(r *http.Request) (int64, string, int, string) {
	if a == nil || a.db == nil {
		return 0, "", http.StatusServiceUnavailable, "authentication service unavailable"
	}
	auth := r.Header.Get("Authorization")
	scheme, credential, ok := strings.Cut(auth, " ")
	credential = strings.TrimSpace(credential)
	if !ok || credential == "" {
		return 0, "", http.StatusUnauthorized, "unauthorized"
	}
	var uid int64
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		claims, err := ParseToken(credential)
		if err != nil {
			return 0, "", http.StatusUnauthorized, "invalid or expired token"
		}
		uid = claims.UID
	case strings.EqualFold(scheme, "ApiKey"):
		parsed, err := ParseAPIKey(credential)
		if err != nil {
			return 0, "", http.StatusUnauthorized, "unauthorized"
		}
		verified, err := a.db.GetBotByAPIKey(credential)
		if err != nil || verified != parsed {
			return 0, "", http.StatusUnauthorized, "unauthorized"
		}
		uid = parsed
	default:
		return 0, "", http.StatusUnauthorized, "unauthorized"
	}
	if uid <= 0 {
		return 0, "", http.StatusUnauthorized, "unauthorized"
	}
	if _, status, msg := activeUserByID(uid, a.db.GetUser); status != 0 {
		return 0, "", status, msg
	}
	return uid, fileMediaHash(credential), 0, ""
}

func (a *FileMediaAuth) cookieSession(r *http.Request) (*store.FileMediaSession, error) {
	cookie, err := r.Cookie(fileMediaCookieName)
	if err != nil || len(cookie.Value) != 64 {
		return nil, nil
	}
	if _, err := hex.DecodeString(cookie.Value); err != nil {
		return nil, nil
	}
	return a.sessions.GetFileMediaSession(r.Context(), fileMediaHash(cookie.Value))
}

func (a *FileMediaAuth) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", "Cookie")
		w.Header().Add("Vary", "Authorization")
		if a == nil || a.db == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication service unavailable"})
			return
		}
		// Ordinary Authorization wins over both the media cookie and connector headers.
		auth := r.Header.Get("Authorization")
		if auth != "" && !strings.HasPrefix(auth, "DeviceConnector ") {
			uid, _, status, msg := a.headerActor(r)
			if status != 0 {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), uidKey, uid)))
			return
		}
		if auth != "" || r.Header.Get("X-CatsCo-Connector-Token") != "" || r.URL.Query().Has("connector_token") {
			DeviceConnectorDownloadAuthWithDB(a.db, a.hub)(next)(w, r)
			return
		}
		if a.sessions == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "media session store unavailable"})
			return
		}
		session, err := a.cookieSession(r)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "media session store unavailable"})
			return
		}
		if session == nil || session.ActorUID <= 0 || !time.Now().Before(session.ExpiresAt) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired media session"})
			return
		}
		if _, status, msg := activeUserByID(session.ActorUID, a.db.GetUser); status != 0 {
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), uidKey, session.ActorUID)))
	}
}

// fileMediaOrigin requires an exact origin, including scheme and port. Forwarded
// host headers do not expand the accepted origin; proxies must preserve Host.
func fileMediaOrigin(r *http.Request) (string, bool) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// The existing reverse proxy supplies the external scheme, while Host remains
	// the authority used by the browser and by the host-only cookie.
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	origin := scheme + "://" + r.Host
	supplied := r.Header.Get("Origin")
	parsed, err := url.Parse(supplied)
	return origin, err == nil && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && supplied == origin
}

func fileMediaCookie(r *http.Request, value string, expires time.Time, maxAge int) *http.Cookie {
	origin, _ := fileMediaOrigin(r)
	return &http.Cookie{Name: fileMediaCookieName, Value: value, Path: "/", HttpOnly: true,
		Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge}
}

// HandleSession is registered directly at /api/files/media-session. Mutations
// require ordinary header authentication and a matching browser Origin.
func (a *FileMediaAuth) HandleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if _, valid := fileMediaOrigin(r); !valid {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid origin"})
		return
	}
	uid, parent, status, msg := a.headerActor(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if a.sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "media session store unavailable"})
		return
	}
	// Revoke the prior cookie before setting another actor, even on account switch.
	old, err := a.cookieSession(r)
	if err == nil && old != nil && r.Method == http.MethodPost {
		err = a.sessions.RevokeFileMediaSessions(r.Context(), old.ParentHash)
	}
	if err == nil && r.Method == http.MethodDelete {
		err = a.sessions.RevokeFileMediaSessions(r.Context(), parent)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "media session store unavailable"})
		return
	}
	// A late logout from the previous account must not clear the new account's
	// cookie. Its header parent is still revoked, regardless of cookie identity.
	if r.Method == http.MethodPost || old == nil || old.ParentHash == parent {
		http.SetCookie(w, fileMediaCookie(r, "", time.Unix(1, 0), -1))
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "media session unavailable"})
		return
	}
	token := hex.EncodeToString(random[:])
	expires := time.Now().Add(fileMediaSessionTTL)
	record := store.FileMediaSession{TokenHash: fileMediaHash(token), ActorUID: uid, ParentHash: parent, ExpiresAt: expires}
	if err := a.sessions.CreateFileMediaSession(r.Context(), record); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "media session store unavailable"})
		return
	}
	http.SetCookie(w, fileMediaCookie(r, token, expires, int(fileMediaSessionTTL.Seconds())))
	writeJSON(w, http.StatusOK, map[string]interface{}{"expires_in": int(fileMediaSessionTTL.Seconds())})
}
