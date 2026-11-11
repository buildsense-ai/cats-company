package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const (
	dailyNotesQueryPath = "/catsco/user/knowledge/v1/query"
	// A CatsLog Daily Notes page is eight days; its proof work takes seconds.
	dailyNotesTimeout = 25 * time.Second
	dailyNotesMaxBody = 8 << 20
)

var (
	dailyNotesDocumentID = regexp.MustCompile(`^akd-[0-9a-f]{24}$`)
	dailyNotesEntryID    = regexp.MustCompile(`^ake-[0-9a-f]{24}$`)
)

// DailyNotesHandler serves an owner the CatsLog Daily Notes of their own
// Agent. CatsLog verifies the forwarded account bearer itself and derives the
// uid from it; this handler only binds the Agent and keeps the selector to
// the Daily Notes lane, so no CatsLog operator credential is involved.
type DailyNotesHandler struct {
	db      store.Store
	baseURL string
	client  *http.Client
	timeout time.Duration
	maxBody int64
}

// NewDailyNotesHandler returns a handler for the CatsLog server at baseURL.
// An empty baseURL leaves the feature unavailable.
func NewDailyNotesHandler(db store.Store, baseURL string) *DailyNotesHandler {
	return &DailyNotesHandler{
		db:      db,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{},
		timeout: dailyNotesTimeout,
		maxBody: dailyNotesMaxBody,
	}
}

type dailyNotesRequest struct {
	Operation  string `json:"operation"`
	Cursor     string `json:"cursor,omitempty"`
	DocumentID string `json:"document_id,omitempty"`
	EntryID    string `json:"entry_id,omitempty"`
}

// HandleQuery handles POST /api/agents/{uid}/daily-notes.
func (h *DailyNotesHandler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	agentUID, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || agentUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent uid"})
		return
	}
	// Another account's Agent reads the same as one that does not exist.
	if ownerUID, err := h.db.GetBotOwner(agentUID); err != nil || ownerUID != viewerUID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "daily_notes_not_found"})
		return
	}
	upstreamQuery, ok := dailyNotesUpstreamQuery(r, agentUID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	token := extractToken(r)
	if h.baseURL == "" || token == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "daily_notes_unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+dailyNotesQueryPath, bytes.NewReader(upstreamQuery))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "daily_notes_unavailable"})
		return
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := h.client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "daily_notes_timeout"})
		} else {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "daily_notes_unavailable"})
		}
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, h.maxBody+1))
	if err != nil || int64(len(body)) > h.maxBody {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "daily_notes_timeout"})
		} else {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "daily_notes_unavailable"})
		}
		return
	}
	switch {
	case response.StatusCode == http.StatusOK:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case response.StatusCode == http.StatusTooManyRequests:
		retry := response.Header.Get("Retry-After")
		if _, err := strconv.Atoi(retry); err != nil {
			retry = "2"
		}
		w.Header().Set("Retry-After", retry)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "daily_notes_busy"})
	case response.StatusCode == http.StatusNotFound:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "daily_notes_not_found"})
	case response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusGatewayTimeout:
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "daily_notes_timeout"})
	default:
		// A CatsLog 401 means CatsLog could not verify the bearer. It must not
		// read as a CatsCompany 401, which would sign the user out.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "daily_notes_unavailable"})
	}
}

// dailyNotesUpstreamQuery decodes the bounded caller request and builds the
// CatsLog query. The Agent comes from the path; the lane is fixed.
func dailyNotesUpstreamQuery(r *http.Request, agentUID int64) ([]byte, bool) {
	var q dailyNotesRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&q) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(q.Cursor) > 4096 {
		return nil, false
	}
	upstream := map[string]any{"agent_id": formatUID(agentUID), "operation": q.Operation}
	switch q.Operation {
	case "graph":
		if q.DocumentID != "" || q.EntryID != "" {
			return nil, false
		}
		graph := map[string]string{"lane": "diaries"}
		if q.Cursor != "" {
			graph["cursor"] = q.Cursor
		}
		upstream["graph"] = graph
	case "read":
		if !dailyNotesDocumentID.MatchString(q.DocumentID) || (q.EntryID != "" && !dailyNotesEntryID.MatchString(q.EntryID)) {
			return nil, false
		}
		read := map[string]string{"document_id": q.DocumentID}
		if q.EntryID != "" {
			read["entry_id"] = q.EntryID
		}
		if q.Cursor != "" {
			read["cursor"] = q.Cursor
		}
		upstream["read"] = read
	default:
		return nil, false
	}
	payload, err := json.Marshal(upstream)
	return payload, err == nil
}
