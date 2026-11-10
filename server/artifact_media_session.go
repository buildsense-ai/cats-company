package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const artifactMediaHeader = "X-Catsco-Artifact-Media"

// HandleArtifactSession issues a short-lived credential scoped to one artifact.
// It is never accepted by upload or ordinary API authentication.
func (a *FileMediaAuth) HandleArtifactSession(access *FileAccessService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if _, ok := fileMediaOrigin(r); !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		uid, parent, status, msg := a.headerActor(r)
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		if a.sessions == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var input struct {
			URL       string `json:"url"`
			MessageID int64  `json:"message_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resource := canonicalArtifactResource(input.URL)
		if resource == "" || !access.CanReadResource(r.Context(), uid, resource, input.MessageID) {
			http.NotFound(w, r)
			return
		}
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		token := base64.RawURLEncoding.EncodeToString([]byte(resource)) + "." + hex.EncodeToString(random[:])
		expires := time.Now().Add(fileMediaSessionTTL)
		if err := a.sessions.CreateFileMediaSession(r.Context(), store.FileMediaSession{TokenHash: fileMediaHash(token), ActorUID: uid, ParentHash: parent, ExpiresAt: expires}); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		target, err := url.Parse(input.URL)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		query := target.Query()
		query.Set("media_token", token)
		target.RawQuery = query.Encode()
		writeJSON(w, http.StatusOK, map[string]interface{}{"url": target.String(), "expires_in": int(fileMediaSessionTTL.Seconds())})
	}
}

// ArtifactMiddleware accepts resource-scoped worker credentials only behind the
// worker secret. Authorization headers and the platform media cookie retain the
// existing identity flow. The artifact handler still checks message access.
func (a *FileMediaAuth) ArtifactMiddleware(next http.HandlerFunc) http.HandlerFunc {
	ordinary := a.Middleware(next)
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(artifactMediaHeader)
		if token == "" || r.Header.Get("Authorization") != "" {
			ordinary(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		secret := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_ORIGIN_SECRET"))
		if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(secret), []byte(r.Header.Get(artifactOriginSecretHeader))) != 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if a == nil || a.sessions == nil || a.db == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// The origin is supplied by the authenticated worker, never inferred from
		// the callback Host (which belongs to the application server).
		origin := r.URL.Query().Get("origin")
		path := r.URL.Query().Get("path")
		if origin == "" || path == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requested := canonicalArtifactResource(strings.TrimRight(origin, "/") + path)
		scopePart, _, ok := strings.Cut(token, ".")
		scope, err := base64.RawURLEncoding.DecodeString(scopePart)
		if !ok || err != nil || requested == "" || string(scope) != requested {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		session, err := a.sessions.GetFileMediaSession(r.Context(), fileMediaHash(token))
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if session == nil || session.ActorUID <= 0 || !time.Now().Before(session.ExpiresAt) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if _, status, _ := activeUserByID(session.ActorUID, a.db.GetUser); status != 0 {
			w.WriteHeader(status)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), uidKey, session.ActorUID)))
	}
}
