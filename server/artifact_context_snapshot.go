package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	artifactContextSnapshotContract       = "catsco.artifact-context-snapshot.v1"
	artifactContextRefContract            = "catsco.artifact-context-ref.v1"
	artifactContextSnapshotTTLDefault     = 5 * time.Minute
	artifactContextTombstoneTTLDefault    = 5 * time.Minute
	artifactContextSnapshotMaxEntries     = 4096
	artifactContextSnapshotRequestMaxBody = 32 * 1024
	artifactContextRefRandomBytes         = 32
)

var artifactContextRefPattern = regexp.MustCompile(`^acr_[A-Za-z0-9_-]{43}$`)

type artifactContextSnapshotState string

const (
	artifactContextSnapshotActive      artifactContextSnapshotState = "ok"
	artifactContextSnapshotExpired     artifactContextSnapshotState = "expired"
	artifactContextSnapshotReplaced    artifactContextSnapshotState = "replaced"
	artifactContextSnapshotInvalidated artifactContextSnapshotState = "invalidated"
	artifactContextSnapshotNotFound    artifactContextSnapshotState = "not_found"
	artifactContextSnapshotMismatch    artifactContextSnapshotState = "mismatch"
	artifactContextSnapshotUnavailable artifactContextSnapshotState = "unavailable"
)

type artifactContextSnapshotKey struct {
	actorUID int64
	topicID  string
}

type artifactContextSnapshot struct {
	Ref              string
	ActorUID         int64
	TopicID          string
	AgentUID         int64
	Artifact         ArtifactContextRecord
	DisplayedVersion int64
	PageContext      map[string]interface{}
	CreatedAt        time.Time
	ObservedAt       string
	ExpiresAt        time.Time
	Revision         uint64
	State            artifactContextSnapshotState
	RetiredAt        time.Time
}

type artifactContextSnapshotStore struct {
	mu           sync.Mutex
	byRef        map[string]*artifactContextSnapshot
	current      map[artifactContextSnapshotKey]string
	ttl          time.Duration
	tombstoneTTL time.Duration
	maxEntries   int
	now          func() time.Time
}

type artifactContextDeliveryRef struct {
	Ref      string
	AgentUID int64
}

type ArtifactContextSnapshotHandler struct {
	hub *Hub
}

type artifactContextSnapshotCreateRequest struct {
	TopicID     string                 `json:"topic_id"`
	ArtifactRef map[string]interface{} `json:"artifact_ref"`
	PageContext map[string]interface{} `json:"page_context,omitempty"`
}

type artifactContextSnapshotInvalidateRequest struct {
	ContextRef string `json:"context_ref"`
}

func newArtifactContextSnapshotStore(ttl, tombstoneTTL time.Duration, maxEntries int) *artifactContextSnapshotStore {
	if ttl <= 0 {
		ttl = artifactContextSnapshotTTLDefault
	}
	if tombstoneTTL <= 0 {
		tombstoneTTL = artifactContextTombstoneTTLDefault
	}
	if maxEntries <= 0 {
		maxEntries = artifactContextSnapshotMaxEntries
	}
	return &artifactContextSnapshotStore{
		byRef:        make(map[string]*artifactContextSnapshot),
		current:      make(map[artifactContextSnapshotKey]string),
		ttl:          ttl,
		tombstoneTTL: tombstoneTTL,
		maxEntries:   maxEntries,
		now:          time.Now,
	}
}

func NewArtifactContextSnapshotHandler(hub *Hub) *ArtifactContextSnapshotHandler {
	return &ArtifactContextSnapshotHandler{hub: hub}
}

func newArtifactContextRef() (string, error) {
	value := make([]byte, artifactContextRefRandomBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "acr_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func normalizeArtifactContextRef(value string) (string, bool) {
	if value == "" || value != strings.TrimSpace(value) || !artifactContextRefPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func (s *artifactContextSnapshotStore) create(snapshot artifactContextSnapshot) (artifactContextSnapshot, error) {
	if s == nil {
		return artifactContextSnapshot{}, errors.New("snapshot store unavailable")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	ref := ""
	for attempt := 0; attempt < 4; attempt++ {
		candidate, err := newArtifactContextRef()
		if err != nil {
			return artifactContextSnapshot{}, err
		}
		if _, exists := s.byRef[candidate]; !exists {
			ref = candidate
			break
		}
	}
	if ref == "" {
		return artifactContextSnapshot{}, errors.New("failed to allocate unique context_ref")
	}

	key := artifactContextSnapshotKey{actorUID: snapshot.ActorUID, topicID: snapshot.TopicID}
	revision := uint64(1)
	if previousRef := s.current[key]; previousRef != "" {
		if previous := s.byRef[previousRef]; previous != nil {
			revision = previous.Revision + 1
			s.retireLocked(previous, artifactContextSnapshotReplaced, now)
		}
	}
	for len(s.byRef) >= s.maxEntries {
		if !s.evictRetiredLocked() {
			return artifactContextSnapshot{}, errors.New("snapshot store is full")
		}
	}

	snapshot.Ref = ref
	snapshot.PageContext = cloneArtifactPageContext(snapshot.PageContext)
	snapshot.CreatedAt = now
	snapshot.ExpiresAt = now.Add(s.ttl)
	snapshot.Revision = revision
	snapshot.State = artifactContextSnapshotActive
	snapshot.RetiredAt = time.Time{}
	stored := snapshot
	s.byRef[ref] = &stored
	s.current[key] = ref
	return cloneArtifactContextSnapshot(stored), nil
}

func (s *artifactContextSnapshotStore) lookup(ref string) (artifactContextSnapshot, artifactContextSnapshotState) {
	if s == nil {
		return artifactContextSnapshot{}, artifactContextSnapshotUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	snapshot := s.byRef[ref]
	if snapshot == nil {
		return artifactContextSnapshot{}, artifactContextSnapshotNotFound
	}
	if snapshot.State != artifactContextSnapshotActive {
		return artifactContextSnapshot{}, snapshot.State
	}
	key := artifactContextSnapshotKey{actorUID: snapshot.ActorUID, topicID: snapshot.TopicID}
	if s.current[key] != ref {
		s.retireLocked(snapshot, artifactContextSnapshotReplaced, now)
		return artifactContextSnapshot{}, artifactContextSnapshotReplaced
	}
	return cloneArtifactContextSnapshot(*snapshot), artifactContextSnapshotActive
}

func (s *artifactContextSnapshotStore) currentRef(actorUID int64, topicID string) string {
	if s == nil || actorUID <= 0 || topicID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now().UTC())
	return s.current[artifactContextSnapshotKey{actorUID: actorUID, topicID: topicID}]
}

func (s *artifactContextSnapshotStore) delivery(ref string, actorUID int64, topicID string, currentAgentUID int64) (*artifactContextDeliveryRef, artifactContextSnapshotState) {
	snapshot, status := s.lookup(ref)
	if status != artifactContextSnapshotActive {
		return nil, status
	}
	if snapshot.ActorUID != actorUID || snapshot.TopicID != topicID || currentAgentUID <= 0 || snapshot.AgentUID != currentAgentUID {
		return nil, artifactContextSnapshotMismatch
	}
	return &artifactContextDeliveryRef{Ref: snapshot.Ref, AgentUID: snapshot.AgentUID}, artifactContextSnapshotActive
}

func (s *artifactContextSnapshotStore) invalidate(ref string, actorUID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	snapshot := s.byRef[ref]
	if snapshot == nil || snapshot.ActorUID != actorUID {
		return false
	}
	if snapshot.State == artifactContextSnapshotActive {
		s.retireLocked(snapshot, artifactContextSnapshotInvalidated, now)
	}
	return true
}

func (s *artifactContextSnapshotStore) cleanupLocked(now time.Time) {
	for ref, snapshot := range s.byRef {
		if snapshot.State == artifactContextSnapshotActive && !now.Before(snapshot.ExpiresAt) {
			s.retireLocked(snapshot, artifactContextSnapshotExpired, now)
		}
		if snapshot.State != artifactContextSnapshotActive && !snapshot.RetiredAt.IsZero() &&
			!now.Before(snapshot.RetiredAt.Add(s.tombstoneTTL)) {
			delete(s.byRef, ref)
		}
	}
}

func (s *artifactContextSnapshotStore) retireLocked(snapshot *artifactContextSnapshot, state artifactContextSnapshotState, now time.Time) {
	if snapshot == nil || snapshot.State != artifactContextSnapshotActive {
		return
	}
	snapshot.State = state
	snapshot.RetiredAt = now
	key := artifactContextSnapshotKey{actorUID: snapshot.ActorUID, topicID: snapshot.TopicID}
	if s.current[key] == snapshot.Ref {
		delete(s.current, key)
	}
}

func (s *artifactContextSnapshotStore) evictRetiredLocked() bool {
	var candidate *artifactContextSnapshot
	for _, snapshot := range s.byRef {
		if snapshot.State == artifactContextSnapshotActive {
			continue
		}
		if candidate == nil || snapshot.RetiredAt.Before(candidate.RetiredAt) {
			candidate = snapshot
		}
	}
	if candidate == nil {
		return false
	}
	key := artifactContextSnapshotKey{actorUID: candidate.ActorUID, topicID: candidate.TopicID}
	if s.current[key] == candidate.Ref {
		delete(s.current, key)
	}
	delete(s.byRef, candidate.Ref)
	return true
}

func cloneArtifactContextSnapshot(snapshot artifactContextSnapshot) artifactContextSnapshot {
	snapshot.PageContext = cloneArtifactPageContext(snapshot.PageContext)
	return snapshot
}

func cloneArtifactPageContext(value map[string]interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var cloned map[string]interface{}
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil
	}
	return cloned
}

func (h *Hub) extractArtifactContextDelivery(actorUID int64, topicID string, metadata map[string]interface{}) (map[string]interface{}, *artifactContextDeliveryRef) {
	clean := metadataWithoutArtifactContext(metadata)
	if h == nil || h.artifactContextSnapshots == nil || metadata == nil {
		return clean, nil
	}
	raw, ok := metadata[artifactContextRefMetadataKey].(string)
	if !ok {
		return clean, nil
	}
	ref, ok := normalizeArtifactContextRef(raw)
	if !ok {
		return clean, nil
	}
	topicID = strings.TrimSpace(topicID)
	currentAgentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok {
		return clean, nil
	}
	delivery, status := h.artifactContextSnapshots.delivery(ref, actorUID, topicID, currentAgentUID)
	if status != artifactContextSnapshotActive {
		return clean, nil
	}
	return clean, delivery
}

func (h *Hub) validatedArtifactContextDeliveryRef(actorUID int64, topicID string, delivery *artifactContextDeliveryRef, recipientUID int64) *artifactContextDeliveryRef {
	if h == nil || h.artifactContextSnapshots == nil || delivery == nil || recipientUID <= 0 || delivery.AgentUID != recipientUID {
		return nil
	}
	topicID = strings.TrimSpace(topicID)
	currentAgentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok || currentAgentUID != delivery.AgentUID {
		return nil
	}
	current, status := h.artifactContextSnapshots.delivery(delivery.Ref, actorUID, topicID, currentAgentUID)
	if status != artifactContextSnapshotActive || current == nil || current.AgentUID != recipientUID {
		return nil
	}
	return current
}

func withArtifactContextDeliveryRef(metadata map[string]interface{}, delivery *artifactContextDeliveryRef, recipientUID int64) map[string]interface{} {
	if delivery == nil || recipientUID <= 0 || delivery.AgentUID != recipientUID {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	next[artifactContextRefMetadataKey] = delivery.Ref
	return next
}

func validArtifactContextRecord(record ArtifactContextRecord, artifactID string) bool {
	return record.ID == artifactID && validArtifactID(record.ID) &&
		strings.TrimSpace(record.Title) != "" &&
		(record.Kind == "html" || record.Kind == "mini_app") &&
		validArtifactContextURL(record.URL)
}

func (h *ArtifactContextSnapshotHandler) HandleUserSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if h == nil || h.hub == nil || h.hub.artifactContextSnapshots == nil {
		writeArtifactContextStatus(w, http.StatusServiceUnavailable, artifactContextSnapshotUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.handleCreate(w, r)
	case http.MethodDelete:
		h.handleInvalidate(w, r)
	default:
		w.Header().Set("Allow", http.MethodPost+", "+http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *ArtifactContextSnapshotHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	actorUID := UIDFromContext(r.Context())
	if actorUID <= 0 {
		writeArtifactContextStatus(w, http.StatusUnauthorized, artifactContextSnapshotMismatch)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, artifactContextSnapshotRequestMaxBody)
	var req artifactContextSnapshotCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.TopicID = strings.TrimSpace(req.TopicID)
	candidate, ok := parseArtifactRefCandidate(map[string]interface{}{artifactRefMetadataKey: req.ArtifactRef})
	if !ok || req.TopicID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid artifact snapshot"})
		return
	}
	agentUID, ok := h.hub.artifactAgentForTopic(actorUID, req.TopicID)
	if !ok {
		writeArtifactContextStatus(w, http.StatusUnprocessableEntity, artifactContextSnapshotMismatch)
		return
	}
	if h.hub.artifactContextResolver == nil {
		writeArtifactContextStatus(w, http.StatusServiceUnavailable, artifactContextSnapshotUnavailable)
		return
	}

	resolutionCtx, cancel := context.WithTimeout(r.Context(), artifactContextResolutionTimeout)
	defer cancel()
	record, err := h.hub.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, agentUID, candidate.ID)
	if err != nil || !validArtifactContextRecord(record, candidate.ID) {
		reason := "resolver returned an invalid artifact"
		if err != nil {
			reason = truncateUTF8(strings.TrimSpace(err.Error()), 240)
		}
		log.Printf("[artifact_context_snapshot] create rejected topic=%s actor=%s agent=%s artifact=%s reason=%s", req.TopicID, formatUID(actorUID), formatUID(agentUID), candidate.ID, reason)
		writeArtifactContextStatus(w, http.StatusUnprocessableEntity, artifactContextSnapshotUnavailable)
		return
	}
	if record.PublishVersion > 0 && candidate.DisplayedVersion > int64(record.PublishVersion) {
		writeArtifactContextStatus(w, http.StatusConflict, artifactContextSnapshotReplaced)
		return
	}
	currentAgentUID, ok := h.hub.artifactAgentForTopic(actorUID, req.TopicID)
	if !ok || currentAgentUID != agentUID {
		writeArtifactContextStatus(w, http.StatusUnprocessableEntity, artifactContextSnapshotMismatch)
		return
	}

	var pageContext map[string]interface{}
	if req.PageContext != nil {
		pageContext, _ = parseArtifactPageContextCandidate(map[string]interface{}{artifactPageContextMetadataKey: req.PageContext})
	}
	displayedVersion := int64(0)
	if candidate.DisplayedVersion > 0 && record.PublishVersion > 0 {
		displayedVersion = candidate.DisplayedVersion
	}
	observedAt := ""
	if pageContext != nil {
		observedAt = firstMetadataString(pageContext, "observed_at")
	}
	previousContextRef := h.hub.artifactContextSnapshots.currentRef(actorUID, req.TopicID)
	snapshot, err := h.hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID:         actorUID,
		TopicID:          req.TopicID,
		AgentUID:         agentUID,
		Artifact:         record,
		DisplayedVersion: displayedVersion,
		PageContext:      pageContext,
		ObservedAt:       observedAt,
	})
	if err != nil {
		writeArtifactContextStatus(w, http.StatusServiceUnavailable, artifactContextSnapshotUnavailable)
		return
	}
	if previousContextRef != "" && previousContextRef != snapshot.Ref && h.hub.artifactResultWritebacks != nil {
		h.hub.artifactResultWritebacks.invalidateContext(previousContextRef)
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"contract_version": artifactContextRefContract,
		"context_ref":      snapshot.Ref,
		"expires_at":       snapshot.ExpiresAt.Format(time.RFC3339Nano),
		"revision":         snapshot.Revision,
	})
}

func (h *ArtifactContextSnapshotHandler) handleInvalidate(w http.ResponseWriter, r *http.Request) {
	actorUID := UIDFromContext(r.Context())
	if actorUID <= 0 {
		writeArtifactContextStatus(w, http.StatusUnauthorized, artifactContextSnapshotMismatch)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var req artifactContextSnapshotInvalidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ref, ok := normalizeArtifactContextRef(req.ContextRef)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid context_ref"})
		return
	}
	h.hub.artifactContextSnapshots.invalidate(ref, actorUID)
	if h.hub.artifactResultWritebacks != nil {
		h.hub.artifactResultWritebacks.invalidateContext(ref)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *ArtifactContextSnapshotHandler) HandleBotRead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil || h.hub == nil || h.hub.artifactContextSnapshots == nil || h.hub.artifactContextResolver == nil {
		writeArtifactContextStatus(w, http.StatusServiceUnavailable, artifactContextSnapshotUnavailable)
		return
	}
	botUID := UIDFromContext(r.Context())
	ref, ok := normalizeArtifactContextRef(r.URL.Query().Get("context_ref"))
	if botUID <= 0 || !ok {
		writeArtifactContextStatus(w, http.StatusBadRequest, artifactContextSnapshotNotFound)
		return
	}
	snapshot, status := h.hub.artifactContextSnapshots.lookup(ref)
	if status != artifactContextSnapshotActive {
		writeArtifactContextLookupStatus(w, status)
		return
	}
	if snapshot.AgentUID != botUID {
		writeArtifactContextStatus(w, http.StatusForbidden, artifactContextSnapshotMismatch)
		return
	}
	currentAgentUID, ok := h.hub.artifactAgentForTopic(snapshot.ActorUID, snapshot.TopicID)
	if !ok || currentAgentUID != botUID {
		writeArtifactContextStatus(w, http.StatusForbidden, artifactContextSnapshotMismatch)
		return
	}

	resolutionCtx, cancel := context.WithTimeout(r.Context(), artifactContextResolutionTimeout)
	defer cancel()
	record, err := h.hub.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, botUID, snapshot.Artifact.ID)
	if err != nil || !validArtifactContextRecord(record, snapshot.Artifact.ID) {
		writeArtifactContextStatus(w, http.StatusServiceUnavailable, artifactContextSnapshotUnavailable)
		return
	}
	if snapshot.DisplayedVersion > 0 && record.PublishVersion > 0 && snapshot.DisplayedVersion > int64(record.PublishVersion) {
		writeArtifactContextStatus(w, http.StatusGone, artifactContextSnapshotReplaced)
		return
	}
	current, currentStatus := h.hub.artifactContextSnapshots.lookup(ref)
	if currentStatus != artifactContextSnapshotActive || current.Revision != snapshot.Revision {
		writeArtifactContextLookupStatus(w, currentStatus)
		return
	}
	currentAgentUID, ok = h.hub.artifactAgentForTopic(snapshot.ActorUID, snapshot.TopicID)
	if !ok || currentAgentUID != botUID || current.AgentUID != botUID {
		writeArtifactContextStatus(w, http.StatusForbidden, artifactContextSnapshotMismatch)
		return
	}

	artifact := map[string]interface{}{
		"id":                record.ID,
		"agent_uid":         strconv.FormatInt(botUID, 10),
		"title":             strings.TrimSpace(record.Title),
		"kind":              record.Kind,
		"url":               strings.TrimSpace(record.URL),
		"topic_id":          snapshot.TopicID,
		"currently_visible": true,
	}
	if snapshot.DisplayedVersion > 0 {
		artifact["displayed_version"] = snapshot.DisplayedVersion
	}
	if record.PublishVersion > 0 {
		artifact["latest_version"] = record.PublishVersion
	}
	trust := map[string]string{
		"artifact":     "server_validated",
		"page_context": "untrusted_page_supplied",
	}
	if snapshot.PageContext != nil {
		if _, hasSemanticContext := snapshot.PageContext["semantic_context"]; hasSemanticContext {
			trust["semantic_context"] = "untrusted_page_supplied"
		}
	}
	response := map[string]interface{}{
		"contract_version": artifactContextSnapshotContract,
		"status":           artifactContextSnapshotActive,
		"artifact":         artifact,
		"created_at":       snapshot.CreatedAt.Format(time.RFC3339Nano),
		"expires_at":       snapshot.ExpiresAt.Format(time.RFC3339Nano),
		"revision":         snapshot.Revision,
		"trust":            trust,
	}
	if snapshot.ObservedAt != "" {
		response["observed_at"] = snapshot.ObservedAt
	}
	if snapshot.PageContext != nil {
		response["page_context"] = cloneArtifactPageContext(snapshot.PageContext)
	}
	if snapshot.DisplayedVersion > 0 && h.hub.artifactResultWritebacks != nil {
		if target, issueErr := h.hub.artifactResultWritebacks.issue(snapshot); issueErr == nil {
			response["writeback_target"] = map[string]interface{}{
				"contract_version": artifactWritebackTargetContract,
				"writeback_ref":    target.Ref,
				"expires_at":       target.ExpiresAt.Format(time.RFC3339Nano),
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func writeArtifactContextLookupStatus(w http.ResponseWriter, status artifactContextSnapshotState) {
	switch status {
	case artifactContextSnapshotExpired, artifactContextSnapshotReplaced, artifactContextSnapshotInvalidated:
		writeArtifactContextStatus(w, http.StatusGone, status)
	case artifactContextSnapshotMismatch:
		writeArtifactContextStatus(w, http.StatusForbidden, status)
	case artifactContextSnapshotUnavailable:
		writeArtifactContextStatus(w, http.StatusServiceUnavailable, status)
	default:
		writeArtifactContextStatus(w, http.StatusNotFound, artifactContextSnapshotNotFound)
	}
}

func writeArtifactContextStatus(w http.ResponseWriter, statusCode int, status artifactContextSnapshotState) {
	writeJSON(w, statusCode, map[string]interface{}{
		"contract_version": artifactContextSnapshotContract,
		"status":           status,
	})
}
