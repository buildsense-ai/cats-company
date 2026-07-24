package server

import (
	"net/http"
	"strconv"
)

// BotIdentityHandler returns the minimum identity needed by trusted companion
// services after BotAPIKeyMiddlewareWithDB has authenticated the caller. It
// deliberately does not expose model configuration or provider credentials.
func BotIdentityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"uid": strconv.FormatInt(uid, 10),
	})
}
