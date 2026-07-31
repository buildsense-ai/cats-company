package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
)

const (
	defaultMessageSearchLimit = 20
	maxMessageSearchLimit     = 100
	defaultMessageAroundLimit = 50
	maxMessageAroundLimit     = 100
)

// HandleSearchMessages handles GET /api/messages/search?q=...&type=all|message|artifact&limit=...
func (h *MessageHandler) HandleSearchMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q must contain at least 2 characters"})
		return
	}
	searchType := strings.TrimSpace(r.URL.Query().Get("type"))
	if searchType == "" {
		searchType = store.MessageSearchAll
	}
	if searchType != store.MessageSearchAll && searchType != store.MessageSearchMessage && searchType != store.MessageSearchArtifact {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be all, message, or artifact"})
		return
	}
	limit, ok := positiveBoundedQueryInt(r, "limit", defaultMessageSearchLimit, maxMessageSearchLimit)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 100"})
		return
	}
	searchDB, ok := h.db.(store.MessageSearchStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "message search unavailable"})
		return
	}
	results, err := searchDB.SearchMessages(UIDFromContext(r.Context()), query, searchType, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to search messages"})
		return
	}
	if results == nil {
		results = []*store.MessageSearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

func positiveBoundedQueryInt(r *http.Request, key string, defaultValue, maxValue int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return defaultValue, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maxValue {
		return 0, false
	}
	return value, true
}

func (h *MessageHandler) getMessagesAround(w http.ResponseWriter, r *http.Request, uid int64, topicID string, aroundID int64, limit int) bool {
	if h.hub == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "topic access unavailable"})
		return true
	}
	if code, text := h.hub.validateTopicReadAccess(uid, h.accountTypeForUID(uid), topicID); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return true
	}
	aroundDB, ok := h.db.(store.MessageAroundStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "message positioning unavailable"})
		return true
	}
	rawMsgs, err := aroundDB.GetMessagesAround(topicID, aroundID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load messages"})
		return true
	}
	targetFound := false
	for _, message := range rawMsgs {
		if message != nil && message.ID == aroundID && message.TopicID == topicID {
			targetFound = true
			break
		}
	}
	if !targetFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "message not found in topic"})
		return true
	}
	messages := make([]map[string]interface{}, 0, len(rawMsgs))
	identityUsers := h.loadHistoryUsers(uid, rawMsgs)
	for _, message := range rawMsgs {
		if formatted := h.hub.historyAPIMessageForRecipient(uid, message, identityUsers); formatted != nil {
			messages = append(messages, formatted)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages":  messages,
		"around_id": aroundID,
		"topic_id":  topicID,
	})
	return true
}

func parseAroundRequest(r *http.Request) (int64, int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("around_id"))
	if raw == "" {
		return 0, 0, false
	}
	aroundID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || aroundID <= 0 {
		return -1, 0, true
	}
	limit, ok := positiveBoundedQueryInt(r, "limit", defaultMessageAroundLimit, maxMessageAroundLimit)
	if !ok {
		return -1, 0, true
	}
	return aroundID, limit, true
}
