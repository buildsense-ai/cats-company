package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const artifactSourceTTL = 2 * time.Minute

// PrivateArtifactSourceURL is used only for the configured management service's
// immediate source fetch. It never changes a stored message or upload payload.
func (h *UploadHandler) PrivateArtifactSourceURL(r *http.Request, uid int64, raw string) (string, error) {
	resource := canonicalFileResource(raw)
	if h == nil || h.fileAccess == nil || h.fileAccess.files == nil || !strings.HasPrefix(resource, "upload:files/") || !h.fileAccess.CanUseResource(r.Context(), uid, resource) {
		return "", errors.New("artifact source unavailable")
	}
	_, credential, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || strings.TrimSpace(credential) == "" {
		return "", errors.New("artifact source identity unavailable")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.Host == "" {
		return "", errors.New("invalid artifact source URL")
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	token := "p." + base64.RawURLEncoding.EncodeToString([]byte(resource)) + "." + hex.EncodeToString(random[:])
	record := store.FileMediaSession{TokenHash: fileMediaHash(token), ActorUID: uid, ParentHash: fileMediaHash(strings.TrimSpace(credential)), ExpiresAt: time.Now().Add(artifactSourceTTL)}
	if err = h.fileAccess.files.CreateFileMediaSession(r.Context(), record); err != nil {
		return "", err
	}
	query := target.Query()
	query.Set("publish_token", token)
	target.RawQuery = query.Encode()
	return target.String(), nil
}

// UploadMiddleware accepts a short-lived source credential only for its exact
// upload object. Explicit client credentials remain authoritative. The handler
// still performs the current message/draft read check before touching storage.
func (a *FileMediaAuth) UploadMiddleware(access *FileAccessService, next http.HandlerFunc) http.HandlerFunc {
	ordinary := a.Middleware(next)
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("publish_token")
		if token == "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-CatsCo-Connector-Token") != "" || r.URL.Query().Has("connector_token") {
			ordinary(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if a == nil || a.sessions == nil || a.db == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		parts := strings.Split(token, ".")
		requested := canonicalFileResource(r.URL.Path)
		if len(parts) != 3 || parts[0] != "p" || r.URL.RawPath != "" || !strings.HasPrefix(requested, "upload:files/") {
			http.NotFound(w, r)
			return
		}
		scope, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || string(scope) != requested {
			http.NotFound(w, r)
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
		if !access.CanReadResource(r.Context(), session.ActorUID, requested, 0) {
			http.NotFound(w, r)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), uidKey, session.ActorUID)))
	}
}
