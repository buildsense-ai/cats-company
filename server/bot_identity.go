package server

import (
	"net/http"
	"strconv"
)

// HandleBotIdentity returns the authenticated bot UID established by
// BotAPIKeyMiddlewareWithDB. It intentionally exposes no profile or config data.
func HandleBotIdentity(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, map[string]string{"uid": strconv.FormatInt(uid, 10)})
}
