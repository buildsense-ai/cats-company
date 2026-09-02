package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const (
	// maxAgentArtifactTagsPerArtifact caps the tag set of one artifact so the
	// management panel stays readable and rows stay small.
	maxAgentArtifactTagsPerArtifact = 12
	// maxAgentArtifactTagRunes counts Unicode runes, so Chinese tags get the
	// same budget as Latin ones.
	maxAgentArtifactTagRunes = 32
)

var (
	errAgentArtifactTagInvalid = errors.New("artifact tag is invalid")
	errAgentArtifactTagLimit   = errors.New("too many artifact tags")
)

type cloudArtifactTagsResponse struct {
	Tags []string `json:"tags"`
}

type cloudArtifactTagCountsResponse struct {
	Tags []store.AgentArtifactTagCount `json:"tags"`
}

type cloudArtifactTagsRequest struct {
	Tags []string `json:"tags"`
}

// normalizeAgentArtifactTags trims, collapses internal whitespace, validates
// characters and length, then dedupes while preserving order. Tags form a
// per-agent namespace, so normalization must be stable across writers.
func normalizeAgentArtifactTags(values []string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		tag := strings.Join(strings.Fields(value), " ")
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > maxAgentArtifactTagRunes {
			return nil, errAgentArtifactTagInvalid
		}
		for _, symbol := range tag {
			switch {
			case symbol == ' ', symbol == '-', symbol == '_', symbol == '.':
			case unicode.IsLetter(symbol), unicode.IsDigit(symbol):
			default:
				return nil, errAgentArtifactTagInvalid
			}
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		normalized = append(normalized, tag)
	}
	if len(normalized) > maxAgentArtifactTagsPerArtifact {
		return nil, errAgentArtifactTagLimit
	}
	return normalized, nil
}

func agentArtifactTagStore(h *CloudArtifactHandler) (store.AgentArtifactTagStore, bool) {
	if h == nil || h.db == nil {
		return nil, false
	}
	tags, ok := h.db.(store.AgentArtifactTagStore)
	return tags, ok
}

// mergeAgentArtifactTags decorates agent-scoped artifacts with their local
// tag annotations. Tag lookup failures degrade to empty tag lists instead of
// failing the whole artifact list.
func (h *CloudArtifactHandler) mergeAgentArtifactTags(agentUID int64, artifacts []cloudArtifact) {
	if len(artifacts) == 0 {
		return
	}
	// Tags are CatsCo-local annotations: never surface a value this process
	// did not read from the tag store, even on degradation or if upstream
	// ever adds a same-named field.
	for i := range artifacts {
		artifacts[i].Tags = nil
	}
	tags, ok := agentArtifactTagStore(h)
	if !ok {
		return
	}
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		ids = append(ids, artifact.ID)
	}
	byArtifact, err := tags.ListAgentArtifactTags(agentUID, ids)
	if err != nil {
		return
	}
	for i := range artifacts {
		artifacts[i].Tags = byArtifact[artifacts[i].ID]
	}
}

func (h *CloudArtifactHandler) handleAgentArtifactTagCollection(w http.ResponseWriter, r *http.Request, agentUID int64) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	tags, ok := agentArtifactTagStore(h)
	if !ok {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	counts, err := tags.ListAgentArtifactTagCounts(agentUID)
	if err != nil {
		writeArtifactError(w, http.StatusInternalServerError, "artifact_request_failed")
		return
	}
	if counts == nil {
		counts = []store.AgentArtifactTagCount{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cloudArtifactTagCountsResponse{Tags: counts})
}

func (h *CloudArtifactHandler) handleAgentArtifactTagsRead(w http.ResponseWriter, agentUID int64, artifactID string) {
	tags, ok := agentArtifactTagStore(h)
	if !ok {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	byArtifact, err := tags.ListAgentArtifactTags(agentUID, []string{artifactID})
	if err != nil {
		writeArtifactError(w, http.StatusInternalServerError, "artifact_request_failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cloudArtifactTagsResponse{Tags: byArtifact[artifactID]})
}

// agentArtifactTagTargetFound confirms the tag target resolves to an active
// artifact in this agent's managed collection before any local tag row is
// written. Tag writes must never outlive the resolver: rows for unknown,
// recycled, or foreign artifact IDs would pollute ListAgentArtifactTagCounts
// and the editor suggestions. The check mirrors the managed active list the
// panel renders (same endpoint, validation, and node-URL acceptance), so a
// tag can only be written to an artifact the panel would actually display.
func (h *CloudArtifactHandler) agentArtifactTagTargetFound(w http.ResponseWriter, r *http.Request, collectionURL, managementToken, publicBaseURL string, agentUID int64, artifactID string) bool {
	if collectionURL == "" {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return false
	}
	target := collectionURL + "?status=active"
	body, err := h.requestManagement(r, http.MethodGet, target, nil, managementToken)
	if err != nil {
		writeArtifactUpstreamError(w, err)
		return false
	}
	var list cloudArtifactManagementList
	if err := json.Unmarshal(body, &list); err != nil || validateManagedArtifactList(list, "active") != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return false
	}
	if validateManagedArtifactNodeURLs(list.Artifacts, publicBaseURL, agentUID) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return false
	}
	for _, artifact := range list.Artifacts {
		if artifact.ID == artifactID {
			return true
		}
	}
	writeArtifactError(w, http.StatusNotFound, "artifact_not_found")
	return false
}

func (h *CloudArtifactHandler) handleAgentArtifactTagsReplace(w http.ResponseWriter, r *http.Request, viewerUID int64, agentUID int64, artifactID string) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024))
	decoder.DisallowUnknownFields()
	var request cloudArtifactTagsRequest
	if err := decoder.Decode(&request); err != nil {
		writeArtifactError(w, http.StatusBadRequest, "artifact_tag_request_invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeArtifactError(w, http.StatusBadRequest, "artifact_tag_request_invalid")
		return
	}
	normalized, err := normalizeAgentArtifactTags(request.Tags)
	if err != nil {
		if errors.Is(err, errAgentArtifactTagLimit) {
			writeArtifactError(w, http.StatusBadRequest, "artifact_tag_limit_exceeded")
			return
		}
		writeArtifactError(w, http.StatusBadRequest, "artifact_tag_invalid")
		return
	}
	tags, ok := agentArtifactTagStore(h)
	if !ok {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	stored, err := tags.ReplaceAgentArtifactTags(agentUID, artifactID, normalized, viewerUID)
	if err != nil {
		writeArtifactError(w, http.StatusInternalServerError, "artifact_request_failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cloudArtifactTagsResponse{Tags: stored})
}

func (h *CloudArtifactHandler) handleAgentArtifactTagDelete(w http.ResponseWriter, agentUID int64, artifactID, tag string) {
	normalized, err := normalizeAgentArtifactTags([]string{tag})
	if err != nil || len(normalized) == 0 {
		writeArtifactError(w, http.StatusBadRequest, "artifact_tag_invalid")
		return
	}
	tags, ok := agentArtifactTagStore(h)
	if !ok {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	removed, err := tags.DeleteAgentArtifactTag(agentUID, artifactID, normalized[0])
	if err != nil {
		writeArtifactError(w, http.StatusInternalServerError, "artifact_request_failed")
		return
	}
	if !removed {
		writeArtifactError(w, http.StatusNotFound, "artifact_tag_not_found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// purgeAgentArtifactTagsWithRetry removes every tag row for one artifact,
// retrying transient store failures. Purge is best-effort hygiene: a failure
// is logged, and any residue converges via the managed-list reconciliation
// sweep (a deleted artifact never passes target validation again, so writes
// cannot resurrect its rows).
func purgeAgentArtifactTagsWithRetry(tags store.AgentArtifactTagStore, agentUID int64, artifactID string) {
	const attempts = 3
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		if err = tags.PurgeAgentArtifactTags(agentUID, artifactID); err == nil {
			return
		}
	}
	log.Printf("cloud artifact: agent artifact tag purge failed after %d attempts agent=%d artifact=%s: %v",
		attempts, agentUID, artifactID, err)
}

// reconcileAgentArtifactTags drops tag rows whose artifact is no longer in
// the agent's active managed list. Deleted artifacts, purge failures, and
// the validate/commit race on writes would otherwise keep feeding tag counts
// forever. Best-effort: failures are logged, never surfaced to the caller.
func (h *CloudArtifactHandler) reconcileAgentArtifactTags(agentUID int64, artifacts []cloudArtifact) {
	tags, ok := agentArtifactTagStore(h)
	if !ok || len(artifacts) == 0 {
		return
	}
	ids, err := tags.ListAgentArtifactTagArtifactIDs(agentUID)
	if err != nil {
		log.Printf("cloud artifact: list agent artifact tag ids failed agent=%d: %v", agentUID, err)
		return
	}
	if len(ids) == 0 {
		return
	}
	active := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		active[artifact.ID] = true
	}
	for _, id := range ids {
		if !active[id] {
			purgeAgentArtifactTagsWithRetry(tags, agentUID, id)
		}
	}
}
