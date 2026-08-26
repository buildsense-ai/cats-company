package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	artifactTaskContract              = "catsco.artifact-task.v1"
	artifactTaskRefContract           = "catsco.artifact-task-ref.v1"
	artifactTaskSnapshotContract      = "catsco.artifact-task-snapshot.v1"
	artifactTaskStatusContract        = "catsco.artifact-task-status.v1"
	artifactTaskRefMetadataKey        = "artifact_task_ref"
	artifactTaskTTLDefault            = 6 * time.Hour
	artifactTaskTombstoneTTLDefault   = 10 * time.Minute
	artifactTaskAgentFinishGrace      = 5 * time.Second
	artifactTaskStoreMaxEntries       = 4096
	artifactTaskRequestMaxBody        = 96 * 1024
	artifactTaskOpaqueRandomBytes     = 32
	artifactTaskVisibleMessageMaxRune = 700
)

var (
	artifactTaskIDPattern  = regexp.MustCompile(`^atk_[A-Za-z0-9_-]{43}$`)
	artifactTaskRefPattern = regexp.MustCompile(`^atr_[A-Za-z0-9_-]{43}$`)
)

type artifactTaskState string

const (
	artifactTaskSubmitted artifactTaskState = "submitted"
	artifactTaskRunning   artifactTaskState = "running"
	artifactTaskCompleted artifactTaskState = "completed"
	artifactTaskFailed    artifactTaskState = "failed"
)

type artifactTask struct {
	ID               string
	Ref              string
	ActorUID         int64
	TopicID          string
	AgentUID         int64
	Artifact         ArtifactContextRecord
	DisplayedVersion int64
	PreviewRoute     runtimeRoute
	Intent           ArtifactTaskIntent
	Payload          json.RawMessage
	PageContext      map[string]interface{}
	Status           artifactTaskState
	Code             string
	Message          string
	Delivered        bool
	RunID            string
	AgentState       string
	AgentFinishedAt  time.Time
	ResultID         string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        time.Time
	RetainUntil      time.Time
}

type artifactTaskDeliveryRef struct {
	Ref      string
	TaskID   string
	AgentUID int64
}

type artifactTaskStore struct {
	mu           sync.Mutex
	byRef        map[string]*artifactTask
	byID         map[string]*artifactTask
	ttl          time.Duration
	tombstoneTTL time.Duration
	agentGrace   time.Duration
	maxEntries   int
	now          func() time.Time
}

type ArtifactTaskHandler struct {
	hub *Hub
}

type artifactTaskCreateRequest struct {
	TopicID        string                    `json:"topic_id"`
	ArtifactRef    map[string]interface{}    `json:"artifact_ref"`
	IntentID       string                    `json:"intent_id"`
	Payload        json.RawMessage           `json:"payload"`
	PageContext    map[string]interface{}    `json:"page_context,omitempty"`
	PreviewSession artifactPreviewSessionRef `json:"preview_session"`
}

type artifactTaskPublicStatus struct {
	ContractVersion string `json:"contract_version"`
	TaskID          string `json:"task_id"`
	Status          string `json:"status"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	ResultID        string `json:"result_id,omitempty"`
	UpdatedAt       string `json:"updated_at"`
	ExpiresAt       string `json:"expires_at"`
}

func newArtifactTaskStore(ttl, tombstoneTTL, agentGrace time.Duration, maxEntries int) *artifactTaskStore {
	if ttl <= 0 {
		ttl = artifactTaskTTLDefault
	}
	if tombstoneTTL <= 0 {
		tombstoneTTL = artifactTaskTombstoneTTLDefault
	}
	if agentGrace <= 0 {
		agentGrace = artifactTaskAgentFinishGrace
	}
	if maxEntries <= 0 {
		maxEntries = artifactTaskStoreMaxEntries
	}
	return &artifactTaskStore{
		byRef:        make(map[string]*artifactTask),
		byID:         make(map[string]*artifactTask),
		ttl:          ttl,
		tombstoneTTL: tombstoneTTL,
		agentGrace:   agentGrace,
		maxEntries:   maxEntries,
		now:          time.Now,
	}
}

func NewArtifactTaskHandler(hub *Hub) *ArtifactTaskHandler {
	return &ArtifactTaskHandler{hub: hub}
}

func (h *Hub) SetArtifactTaskIntentResolver(resolver ArtifactTaskIntentResolver) {
	if h != nil {
		h.artifactTaskIntentResolver = resolver
	}
}

func newArtifactTaskOpaque(prefix string) (string, error) {
	value := make([]byte, artifactTaskOpaqueRandomBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func cloneArtifactTask(value artifactTask) artifactTask {
	value.Payload = append(json.RawMessage(nil), value.Payload...)
	value.Intent.InputSchema = append(json.RawMessage(nil), value.Intent.InputSchema...)
	value.PageContext = cloneArtifactPageContext(value.PageContext)
	return value
}

func (s *artifactTaskStore) create(candidate artifactTask) (artifactTask, error) {
	if s == nil || candidate.ActorUID <= 0 || candidate.AgentUID <= 0 || candidate.TopicID == "" ||
		!validArtifactContextRecord(candidate.Artifact, candidate.Artifact.ID) || candidate.DisplayedVersion <= 0 ||
		candidate.PreviewRoute.NodeID == "" || candidate.PreviewRoute.ConnectionID == "" ||
		candidate.Intent.ID == "" || candidate.Intent.ResultSink == "" || len(candidate.Payload) == 0 {
		return artifactTask{}, errors.New("invalid Artifact task")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	if len(s.byID) >= s.maxEntries && !s.evictTerminalLocked() {
		return artifactTask{}, errors.New("Artifact task store is full")
	}
	for attempts := 0; attempts < 8; attempts++ {
		id, err := newArtifactTaskOpaque("atk_")
		if err != nil {
			return artifactTask{}, err
		}
		ref, err := newArtifactTaskOpaque("atr_")
		if err != nil {
			return artifactTask{}, err
		}
		if s.byID[id] != nil || s.byRef[ref] != nil {
			continue
		}
		candidate.ID = id
		candidate.Ref = ref
		candidate.Status = artifactTaskSubmitted
		candidate.CreatedAt = now
		candidate.UpdatedAt = now
		candidate.ExpiresAt = now.Add(s.ttl)
		stored := cloneArtifactTask(candidate)
		s.byID[id] = &stored
		s.byRef[ref] = &stored
		return cloneArtifactTask(stored), nil
	}
	return artifactTask{}, errors.New("failed to allocate Artifact task identity")
}

func (s *artifactTaskStore) claimDelivery(ref string, actorUID int64, topicID string, agentUID int64) (*artifactTaskDeliveryRef, error) {
	if s == nil || !artifactTaskRefPattern.MatchString(ref) {
		return nil, errors.New("Artifact task reference is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byRef[ref]
	if task == nil || task.ActorUID != actorUID || task.TopicID != topicID || task.AgentUID != agentUID ||
		artifactTaskTerminal(task.Status) || !now.Before(task.ExpiresAt) {
		return nil, errors.New("Artifact task reference does not match this message")
	}
	if task.Delivered {
		return nil, errors.New("Artifact task was already delivered")
	}
	task.Delivered = true
	task.UpdatedAt = now
	return &artifactTaskDeliveryRef{Ref: task.Ref, TaskID: task.ID, AgentUID: task.AgentUID}, nil
}

func (s *artifactTaskStore) validateDelivery(ref string, taskID string, actorUID int64, topicID string, agentUID int64) *artifactTaskDeliveryRef {
	if s == nil || !artifactTaskRefPattern.MatchString(ref) || !artifactTaskIDPattern.MatchString(taskID) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byRef[ref]
	if task == nil || task.ID != taskID || !task.Delivered || task.ActorUID != actorUID || task.TopicID != topicID ||
		task.AgentUID != agentUID || artifactTaskTerminal(task.Status) || !now.Before(task.ExpiresAt) {
		return nil
	}
	return &artifactTaskDeliveryRef{Ref: task.Ref, TaskID: task.ID, AgentUID: task.AgentUID}
}

func (s *artifactTaskStore) forActor(taskID string, actorUID int64) (artifactTask, bool) {
	if s == nil || !artifactTaskIDPattern.MatchString(taskID) || actorUID <= 0 {
		return artifactTask{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byID[taskID]
	if task == nil || task.ActorUID != actorUID {
		return artifactTask{}, false
	}
	s.convergeLocked(task, now)
	return cloneArtifactTask(*task), true
}

func (s *artifactTaskStore) forBot(ref string, botUID int64) (artifactTask, bool) {
	if s == nil || !artifactTaskRefPattern.MatchString(ref) || botUID <= 0 {
		return artifactTask{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byRef[ref]
	if task == nil || task.AgentUID != botUID || !task.Delivered || artifactTaskTerminal(task.Status) || !now.Before(task.ExpiresAt) {
		return artifactTask{}, false
	}
	return cloneArtifactTask(*task), true
}

func (s *artifactTaskStore) withWritable(ref, taskID string, use func(artifactTask)) bool {
	if s == nil || use == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byRef[ref]
	if task == nil || task.ID != taskID || !task.Delivered || artifactTaskTerminal(task.Status) || !now.Before(task.ExpiresAt) {
		return false
	}
	use(cloneArtifactTask(*task))
	return true
}

func (s *artifactTaskStore) observeRun(ref string, sourceUID int64, topicID string, status *types.ConversationTaskStatus) bool {
	if s == nil || status == nil || !artifactTaskRefPattern.MatchString(ref) || status.RunID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byRef[ref]
	if task == nil || task.AgentUID != sourceUID || task.TopicID != topicID || !task.Delivered || artifactTaskTerminal(task.Status) {
		return false
	}
	if task.RunID != "" && task.RunID != status.RunID {
		return false
	}
	if task.RunID == "" {
		task.RunID = status.RunID
	}
	task.AgentState = status.State
	task.UpdatedAt = now
	switch status.State {
	case "running", "waiting":
		task.Status = artifactTaskRunning
	case "completed":
		if task.Status == artifactTaskSubmitted {
			task.Status = artifactTaskRunning
		}
		task.AgentFinishedAt = now
	case "failed", "cancelled", "stale":
		s.failLocked(task, "agent_"+status.State, status.Error, now)
	default:
		return false
	}
	return true
}

func (s *artifactTaskStore) completeResult(taskID, resultID string, outcome artifactResultDeliveryOutcome) bool {
	if s == nil || !artifactTaskIDPattern.MatchString(taskID) || !artifactResultIDPattern.MatchString(resultID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byID[taskID]
	if task == nil {
		return false
	}
	if task.Status == artifactTaskCompleted {
		return task.ResultID == resultID
	}
	if task.Status == artifactTaskFailed {
		return false
	}
	task.ResultID = resultID
	task.UpdatedAt = now
	if outcome.Status == "applied" {
		task.Status = artifactTaskCompleted
		task.Code = ""
		task.Message = ""
		task.RetainUntil = now.Add(s.tombstoneTTL)
		return true
	}
	code := strings.TrimSpace(outcome.Code)
	if code == "" {
		code = "result_" + strings.TrimSpace(outcome.Status)
	}
	s.failLocked(task, code, outcome.Message, now)
	return true
}

func (s *artifactTaskStore) failForActor(taskID string, actorUID int64, code, message string) bool {
	if s == nil || !artifactTaskIDPattern.MatchString(taskID) || actorUID <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	task := s.byID[taskID]
	if task == nil || task.ActorUID != actorUID || artifactTaskTerminal(task.Status) {
		return false
	}
	s.failLocked(task, code, message, now)
	return true
}

func (s *artifactTaskStore) failLocked(task *artifactTask, code, message string, now time.Time) {
	if task == nil || artifactTaskTerminal(task.Status) {
		return
	}
	task.Status = artifactTaskFailed
	task.Code = truncateUTF8(strings.TrimSpace(code), 64)
	task.Message = truncateUTF8(strings.TrimSpace(message), 500)
	task.UpdatedAt = now
	task.RetainUntil = now.Add(s.tombstoneTTL)
}

func (s *artifactTaskStore) convergeLocked(task *artifactTask, now time.Time) {
	if task == nil || artifactTaskTerminal(task.Status) {
		return
	}
	if !now.Before(task.ExpiresAt) {
		s.failLocked(task, "task_expired", "Artifact task expired before a result was applied", now)
		return
	}
	if !task.AgentFinishedAt.IsZero() && !now.Before(task.AgentFinishedAt.Add(s.agentGrace)) {
		s.failLocked(task, "result_not_applied", "Agent turn finished without an applied Artifact result", now)
	}
}

func (s *artifactTaskStore) cleanupLocked(now time.Time) {
	for _, task := range s.byID {
		s.convergeLocked(task, now)
	}
	for id, task := range s.byID {
		if !artifactTaskTerminal(task.Status) || task.RetainUntil.IsZero() || now.Before(task.RetainUntil) {
			continue
		}
		delete(s.byID, id)
		delete(s.byRef, task.Ref)
	}
}

func (s *artifactTaskStore) evictTerminalLocked() bool {
	var candidate *artifactTask
	for _, task := range s.byID {
		if !artifactTaskTerminal(task.Status) {
			continue
		}
		if candidate == nil || task.UpdatedAt.Before(candidate.UpdatedAt) {
			candidate = task
		}
	}
	if candidate == nil {
		return false
	}
	delete(s.byID, candidate.ID)
	delete(s.byRef, candidate.Ref)
	return true
}

func artifactTaskTerminal(state artifactTaskState) bool {
	return state == artifactTaskCompleted || state == artifactTaskFailed
}

func artifactTaskStatus(task artifactTask) artifactTaskPublicStatus {
	return artifactTaskPublicStatus{
		ContractVersion: artifactTaskStatusContract,
		TaskID:          task.ID,
		Status:          string(task.Status),
		Code:            task.Code,
		Message:         task.Message,
		RunID:           task.RunID,
		ResultID:        task.ResultID,
		UpdatedAt:       task.UpdatedAt.Format(time.RFC3339Nano),
		ExpiresAt:       task.ExpiresAt.Format(time.RFC3339Nano),
	}
}

func normalizeArtifactTaskRef(value string) (string, bool) {
	return value, value != "" && value == strings.TrimSpace(value) && artifactTaskRefPattern.MatchString(value)
}

func (h *Hub) extractArtifactTaskDelivery(actorUID int64, topicID string, metadata map[string]interface{}) (map[string]interface{}, *artifactTaskDeliveryRef, error) {
	clean := metadataWithoutArtifactContext(metadata)
	if metadata == nil {
		return clean, nil, nil
	}
	raw, exists := metadata[artifactTaskRefMetadataKey]
	if !exists {
		return clean, nil, nil
	}
	ref, ok := raw.(string)
	if !ok {
		return clean, nil, errors.New("Artifact task reference is invalid")
	}
	ref, ok = normalizeArtifactTaskRef(ref)
	if !ok || h == nil || h.artifactTasks == nil {
		return clean, nil, errors.New("Artifact task reference is unavailable")
	}
	agentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok {
		return clean, nil, errors.New("Artifact task topic does not have a current Agent")
	}
	delivery, err := h.artifactTasks.claimDelivery(ref, actorUID, topicID, agentUID)
	return clean, delivery, err
}

func (h *Hub) validatedArtifactTaskDeliveryRef(actorUID int64, topicID string, delivery *artifactTaskDeliveryRef, recipientUID int64) *artifactTaskDeliveryRef {
	if h == nil || h.artifactTasks == nil || delivery == nil || recipientUID <= 0 || delivery.AgentUID != recipientUID {
		return nil
	}
	agentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok || agentUID != recipientUID {
		return nil
	}
	return h.artifactTasks.validateDelivery(delivery.Ref, delivery.TaskID, actorUID, topicID, recipientUID)
}

func withArtifactTaskDeliveryRef(metadata map[string]interface{}, delivery *artifactTaskDeliveryRef, recipientUID int64) map[string]interface{} {
	if delivery == nil || delivery.AgentUID != recipientUID {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	next[artifactTaskRefMetadataKey] = delivery.Ref
	return next
}

func artifactTaskRefFromStatusPayload(payload *normalizedMessagePayload) string {
	if payload == nil {
		return ""
	}
	body := taskStatusBody(payload)
	raw := firstTaskStatusString(body, payload.Metadata, artifactTaskRefMetadataKey)
	ref, ok := normalizeArtifactTaskRef(raw)
	if !ok {
		return ""
	}
	return ref
}

func (h *Hub) observeArtifactTaskStatus(sourceUID int64, topicID string, payload *normalizedMessagePayload, status *types.ConversationTaskStatus) {
	if h == nil || h.artifactTasks == nil || status == nil {
		return
	}
	ref := artifactTaskRefFromStatusPayload(payload)
	if ref == "" {
		return
	}
	h.artifactTasks.observeRun(ref, sourceUID, topicID, status)
}

func (h *ArtifactTaskHandler) HandleUserTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	switch r.Method {
	case http.MethodPost:
		h.handleCreate(w, r)
	case http.MethodGet:
		h.handleStatus(w, r)
	case http.MethodDelete:
		h.handleDeliveryFailure(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *ArtifactTaskHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	actorUID := UIDFromContext(r.Context())
	if actorUID <= 0 || h == nil || h.hub == nil || h.hub.artifactTasks == nil ||
		h.hub.artifactContextResolver == nil || h.hub.artifactTaskIntentResolver == nil ||
		h.hub.artifactPreviewSessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Artifact tasks are unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, artifactTaskRequestMaxBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request artifactTaskCreateRequest
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Artifact task request"})
		return
	}
	request.TopicID = strings.TrimSpace(request.TopicID)
	request.IntentID = strings.TrimSpace(request.IntentID)
	candidate, ok := parseArtifactRefCandidate(map[string]interface{}{artifactRefMetadataKey: request.ArtifactRef})
	if !ok || request.TopicID == "" || candidate.DisplayedVersion <= 0 ||
		!artifactResultSinkIDPattern.MatchString(request.IntentID) || len(request.Payload) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Artifact task identity"})
		return
	}
	agentUID, ok := h.hub.artifactAgentForTopic(actorUID, request.TopicID)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Artifact task topic mismatch"})
		return
	}
	previewRoute, ok := h.hub.artifactPreviewSessions.verify(request.PreviewSession, actorUID)
	if !ok || !h.hub.artifactPreviewRouteConnected(actorUID, previewRoute) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Artifact preview is not connected"})
		return
	}
	resolutionCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	record, err := h.hub.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, agentUID, candidate.ID)
	cancel()
	if err != nil || !validArtifactContextRecord(record, candidate.ID) ||
		(record.PublishVersion > 0 && candidate.DisplayedVersion > int64(record.PublishVersion)) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Artifact task target is unavailable"})
		return
	}
	intentCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	intent, err := h.hub.artifactTaskIntentResolver.ResolveArtifactTaskIntent(
		intentCtx,
		record,
		candidate.DisplayedVersion,
		request.IntentID,
	)
	cancel()
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Artifact task intent is unavailable"})
		return
	}
	payload, err := validateArtifactTaskPayload(intent.InputSchema, request.Payload)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Artifact task payload does not match its intent"})
		return
	}
	var pageContext map[string]interface{}
	if request.PageContext != nil {
		pageContext, _ = parseArtifactPageContextCandidate(map[string]interface{}{artifactPageContextMetadataKey: request.PageContext})
	}
	task, err := h.hub.artifactTasks.create(artifactTask{
		ActorUID:         actorUID,
		TopicID:          request.TopicID,
		AgentUID:         agentUID,
		Artifact:         record,
		DisplayedVersion: candidate.DisplayedVersion,
		PreviewRoute:     previewRoute,
		Intent:           intent,
		Payload:          payload,
		PageContext:      pageContext,
	})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Artifact task could not be created"})
		return
	}
	artifactTitle := strings.TrimSpace(record.Title)
	if artifactTitle == "" {
		artifactTitle = record.ID
	}
	visibleMessage := truncateUTF8(fmt.Sprintf("来自「%s」：%s", artifactTitle, intent.Title), artifactTaskVisibleMessageMaxRune)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"contract_version": artifactTaskRefContract,
		"task_id":          task.ID,
		"task_ref":         task.Ref,
		"status":           task.Status,
		"visible_message":  visibleMessage,
		"expires_at":       task.ExpiresAt.Format(time.RFC3339Nano),
	})
}

func (h *ArtifactTaskHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	actorUID := UIDFromContext(r.Context())
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if actorUID <= 0 || h == nil || h.hub == nil || h.hub.artifactTasks == nil || !artifactTaskIDPattern.MatchString(taskID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Artifact task"})
		return
	}
	task, ok := h.hub.artifactTasks.forActor(taskID, actorUID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Artifact task not found"})
		return
	}
	if !artifactTaskTerminal(task.Status) && !h.hub.artifactPreviewRouteConnected(actorUID, task.PreviewRoute) {
		h.hub.artifactTasks.failForActor(task.ID, actorUID, "preview_disconnected", "Artifact preview disconnected before the task completed")
		task, _ = h.hub.artifactTasks.forActor(taskID, actorUID)
	}
	writeJSON(w, http.StatusOK, artifactTaskStatus(task))
}

func (h *ArtifactTaskHandler) handleDeliveryFailure(w http.ResponseWriter, r *http.Request) {
	actorUID := UIDFromContext(r.Context())
	var request struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !artifactTaskIDPattern.MatchString(request.TaskID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Artifact task"})
		return
	}
	if h == nil || h.hub == nil || h.hub.artifactTasks == nil ||
		!h.hub.artifactTasks.failForActor(request.TaskID, actorUID, "turn_delivery_failed", "Artifact task could not be delivered to the conversation") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Artifact task not found"})
		return
	}
	if h.hub.artifactResultWritebacks != nil {
		h.hub.artifactResultWritebacks.invalidateTask(request.TaskID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "task_id": request.TaskID})
}

func (h *ArtifactTaskHandler) HandleBotRead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	botUID := UIDFromContext(r.Context())
	ref, ok := normalizeArtifactTaskRef(r.URL.Query().Get("task_ref"))
	if botUID <= 0 || !ok || h == nil || h.hub == nil || h.hub.artifactTasks == nil || h.hub.artifactContextResolver == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Artifact task reference"})
		return
	}
	task, ok := h.hub.artifactTasks.forBot(ref, botUID)
	if !ok {
		writeJSON(w, http.StatusGone, map[string]string{"error": "Artifact task is missing or expired"})
		return
	}
	currentAgentUID, ok := h.hub.artifactAgentForTopic(task.ActorUID, task.TopicID)
	if !ok || currentAgentUID != botUID || !h.hub.artifactPreviewRouteConnected(task.ActorUID, task.PreviewRoute) {
		h.hub.artifactTasks.failForActor(task.ID, task.ActorUID, "preview_disconnected", "Artifact preview is no longer connected")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Artifact task target is no longer connected"})
		return
	}
	resolutionCtx, cancel := context.WithTimeout(r.Context(), artifactContextResolutionTimeout)
	record, err := h.hub.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, botUID, task.Artifact.ID)
	cancel()
	if err != nil || !validArtifactContextRecord(record, task.Artifact.ID) ||
		(record.PublishVersion > 0 && task.DisplayedVersion > int64(record.PublishVersion)) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Artifact task target is unavailable"})
		return
	}
	target, err := h.hub.issueArtifactTaskWritebackIfActive(task.Ref, task.ID)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Artifact task writeback is unavailable"})
		return
	}
	artifact := map[string]interface{}{
		"id":                record.ID,
		"title":             strings.TrimSpace(record.Title),
		"kind":              record.Kind,
		"url":               strings.TrimSpace(record.URL),
		"agent_uid":         strconv.FormatInt(botUID, 10),
		"topic_id":          task.TopicID,
		"displayed_version": task.DisplayedVersion,
		"latest_version":    record.PublishVersion,
		"currently_visible": true,
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"contract_version": artifactTaskSnapshotContract,
		"status":           "ok",
		"trusted": map[string]interface{}{
			"artifact": artifact,
			"task": map[string]interface{}{
				"task_id":     task.ID,
				"intent_id":   task.Intent.ID,
				"result_sink": task.Intent.ResultSink,
				"created_at":  task.CreatedAt.Format(time.RFC3339Nano),
				"expires_at":  task.ExpiresAt.Format(time.RFC3339Nano),
			},
			"writeback_target": map[string]interface{}{
				"contract_version": artifactWritebackTargetContract,
				"writeback_ref":    target.Ref,
				"task_id":          task.ID,
				"expires_at":       target.ExpiresAt.Format(time.RFC3339Nano),
			},
		},
		"untrusted": map[string]interface{}{
			"intent":       task.Intent,
			"payload":      json.RawMessage(task.Payload),
			"page_context": cloneArtifactPageContext(task.PageContext),
		},
		"trust": map[string]string{
			"artifact":     "server_validated",
			"task":         "server_validated",
			"intent":       "artifact_authored_untrusted",
			"payload":      "page_supplied_untrusted",
			"page_context": "page_observed_untrusted",
		},
	})
}
