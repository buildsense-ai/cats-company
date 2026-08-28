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
	artifactRuntimeRequestContract     = "catsco.artifact-runtime-request.v1"
	artifactRuntimeResponseContract    = "catsco.artifact-runtime-response.v1"
	artifactRuntimeObservationContract = "catsco.artifact-runtime-observation.v1"
	artifactRuntimeStateContract       = "catsco.artifact-runtime-state.v1"
	artifactRuntimeEventContract       = "catsco.artifact-runtime-event.v1"
	artifactRuntimeRequestMaxBody      = 512 * 1024
	artifactRuntimeStateMaxBytes       = 256 * 1024
	artifactRuntimeStateMaxNodes       = 32_768
	artifactRuntimeStateListMax        = 256
	artifactRuntimeEventListMax        = 100
	artifactRuntimeResolutionTimeout   = 4 * time.Second
)

var artifactRuntimeDocumentKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type ArtifactRuntimeHandler struct {
	hub   *Hub
	store store.ArtifactRuntimeStateStore
}

type artifactRuntimeUserRequest struct {
	ContractVersion string                    `json:"contract_version"`
	Operation       string                    `json:"operation"`
	TopicID         string                    `json:"topic_id"`
	ArtifactRef     map[string]interface{}    `json:"artifact_ref"`
	PreviewSession  artifactPreviewSessionRef `json:"preview_session"`
	Namespace       string                    `json:"namespace,omitempty"`
	Key             string                    `json:"key,omitempty"`
	BaseRevision    *int64                    `json:"base_revision,omitempty"`
	Value           json.RawMessage           `json:"value,omitempty"`
	Patch           json.RawMessage           `json:"patch,omitempty"`
	AfterEventID    int64                     `json:"after_event_id,omitempty"`
	Limit           int                       `json:"limit,omitempty"`
}

type artifactRuntimeAgentRequest struct {
	ContractVersion string          `json:"contract_version"`
	Operation       string          `json:"operation"`
	ContextRef      string          `json:"context_ref,omitempty"`
	TaskRef         string          `json:"task_ref,omitempty"`
	Namespace       string          `json:"namespace"`
	Key             string          `json:"key"`
	BaseRevision    *int64          `json:"base_revision"`
	Value           json.RawMessage `json:"value,omitempty"`
	Patch           json.RawMessage `json:"patch,omitempty"`
}

type artifactRuntimeAccess struct {
	ActorUID         int64
	AgentUID         int64
	TopicID          string
	DisplayedVersion int64
	Artifact         ArtifactContextRecord
	Manifest         ArtifactRuntimeManifest
	PageContext      map[string]interface{}
}

func NewArtifactRuntimeHandler(hub *Hub, db store.Store) *ArtifactRuntimeHandler {
	handler := &ArtifactRuntimeHandler{hub: hub}
	if runtimeStore, ok := db.(store.ArtifactRuntimeStateStore); ok {
		handler.store = runtimeStore
	}
	return handler
}

func (h *Hub) SetArtifactRuntimeManifestResolver(resolver ArtifactRuntimeManifestResolver) {
	if h != nil {
		h.artifactRuntimeResolver = resolver
	}
}

func (h *ArtifactRuntimeHandler) HandleUser(w http.ResponseWriter, r *http.Request) {
	artifactRuntimeNoStore(w)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeArtifactRuntimeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", 0)
		return
	}
	if h == nil || h.hub == nil || h.store == nil || h.hub.artifactContextResolver == nil ||
		h.hub.artifactRuntimeResolver == nil || h.hub.artifactPreviewSessions == nil {
		writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "Artifact Runtime is unavailable", 0)
		return
	}
	actorUID := UIDFromContext(r.Context())
	if actorUID <= 0 {
		writeArtifactRuntimeError(w, http.StatusUnauthorized, "authentication_failed", "Artifact Runtime authentication failed", 0)
		return
	}
	request, err := decodeArtifactRuntimeUserRequest(w, r)
	if err != nil {
		writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_request", err.Error(), 0)
		return
	}
	access, status, code, err := h.resolveViewerAccess(r.Context(), actorUID, request)
	if err != nil {
		writeArtifactRuntimeError(w, status, code, err.Error(), 0)
		return
	}
	h.handleOperation(w, r, access, request.Operation, request.Namespace, request.Key,
		request.BaseRevision, request.Value, request.Patch, request.AfterEventID, request.Limit, "viewer")
}

func (h *ArtifactRuntimeHandler) HandleBot(w http.ResponseWriter, r *http.Request) {
	artifactRuntimeNoStore(w)
	if h == nil || h.hub == nil || h.store == nil || h.hub.artifactContextResolver == nil ||
		h.hub.artifactRuntimeResolver == nil {
		writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "Artifact Runtime is unavailable", 0)
		return
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeArtifactRuntimeError(w, http.StatusUnauthorized, "authentication_failed", "Artifact Runtime authentication failed", 0)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleBotObserve(w, r, botUID)
	case http.MethodPost:
		h.handleBotApply(w, r, botUID)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeArtifactRuntimeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", 0)
	}
}

func (h *ArtifactRuntimeHandler) handleBotObserve(w http.ResponseWriter, r *http.Request, botUID int64) {
	contextRef := strings.TrimSpace(r.URL.Query().Get("context_ref"))
	taskRef := strings.TrimSpace(r.URL.Query().Get("task_ref"))
	access, status, code, err := h.resolveAgentAccess(r.Context(), botUID, contextRef, taskRef)
	if err != nil {
		writeArtifactRuntimeError(w, status, code, err.Error(), 0)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	key := r.URL.Query().Get("key")
	if (namespace == "") != (key == "") || (namespace != "" && (!validArtifactRuntimeNamespace(namespace) || !validArtifactRuntimeDocumentKey(key))) {
		writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_state_key", "Artifact Runtime State key is invalid", 0)
		return
	}
	if namespace != "" && !access.Manifest.allowsNamespace(namespace, false) {
		writeArtifactRuntimeError(w, http.StatusForbidden, "namespace_not_declared", "Artifact Runtime namespace is not declared", 0)
		return
	}
	states, err := h.store.ListArtifactRuntimeStates(r.Context(), access.AgentUID, access.Artifact.ID, artifactRuntimeStateListMax)
	if err != nil {
		writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "state_unavailable", "Artifact Runtime State is unavailable", 0)
		return
	}
	refs := artifactRuntimeStateReferences(states, access.Manifest)
	response := map[string]interface{}{
		"ok":               true,
		"contract_version": artifactRuntimeObservationContract,
		"status":           "ok",
		"trusted": map[string]interface{}{
			"artifact": artifactRuntimeArtifactResponse(access),
			"runtime":  access.Manifest,
		},
		"untrusted": map[string]interface{}{
			"view": artifactRuntimeViewFromPageContext(access.PageContext),
		},
		"state_refs": refs,
		"trust": map[string]string{
			"artifact": "server_validated",
			"runtime":  "immutable_manifest_validated",
			"view":     "page_authored_untrusted",
			"state":    "runtime_persisted",
		},
	}
	if namespace != "" {
		state, found, readErr := h.store.GetArtifactRuntimeState(r.Context(), access.AgentUID, access.Artifact.ID, namespace, key)
		if readErr != nil {
			writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "state_unavailable", "Artifact Runtime State is unavailable", 0)
			return
		}
		if found {
			response["state"] = artifactRuntimeStateResponse(state, true)
		} else {
			response["state"] = artifactRuntimeMissingStateResponse(namespace, key)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *ArtifactRuntimeHandler) handleBotApply(w http.ResponseWriter, r *http.Request, botUID int64) {
	var request artifactRuntimeAgentRequest
	if err := decodeArtifactRuntimeRequestBody(w, r, &request); err != nil {
		writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_request", "invalid Artifact Runtime request", 0)
		return
	}
	if request.ContractVersion != artifactRuntimeRequestContract ||
		(request.Operation != "state.put" && request.Operation != "state.patch") {
		writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_request", "invalid Artifact Runtime operation", 0)
		return
	}
	access, status, code, err := h.resolveAgentAccess(
		r.Context(), botUID, strings.TrimSpace(request.ContextRef), strings.TrimSpace(request.TaskRef),
	)
	if err != nil {
		writeArtifactRuntimeError(w, status, code, err.Error(), 0)
		return
	}
	h.handleOperation(w, r, access, request.Operation, request.Namespace, request.Key,
		request.BaseRevision, request.Value, request.Patch, 0, 0, "agent")
}

func (h *ArtifactRuntimeHandler) handleOperation(
	w http.ResponseWriter,
	r *http.Request,
	access artifactRuntimeAccess,
	operation, namespace, key string,
	baseRevision *int64,
	value, patch json.RawMessage,
	afterEventID int64,
	limit int,
	updatedBy string,
) {
	switch operation {
	case "connect":
		cursor, err := h.store.LatestArtifactRuntimeEventID(r.Context(), access.AgentUID, access.Artifact.ID)
		if err != nil {
			writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "events_unavailable", "Artifact Runtime events are unavailable", 0)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "contract_version": artifactRuntimeResponseContract,
			"operation": operation, "artifact": artifactRuntimeArtifactResponse(access),
			"runtime": access.Manifest, "event_cursor": cursor,
		})
	case "state.get":
		if !h.validateStateTarget(w, access, namespace, key, false) {
			return
		}
		state, found, err := h.store.GetArtifactRuntimeState(r.Context(), access.AgentUID, access.Artifact.ID, namespace, key)
		if err != nil {
			writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "state_unavailable", "Artifact Runtime State is unavailable", 0)
			return
		}
		response := artifactRuntimeMissingStateResponse(namespace, key)
		if found {
			response = artifactRuntimeStateResponse(state, true)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "contract_version": artifactRuntimeResponseContract,
			"operation": operation, "state": response,
		})
	case "state.list":
		states, err := h.store.ListArtifactRuntimeStates(r.Context(), access.AgentUID, access.Artifact.ID, artifactRuntimeStateListMax)
		if err != nil {
			writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "state_unavailable", "Artifact Runtime State is unavailable", 0)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "contract_version": artifactRuntimeResponseContract,
			"operation": operation, "state_refs": artifactRuntimeStateReferences(states, access.Manifest),
		})
	case "state.put", "state.patch":
		if !h.validateStateTarget(w, access, namespace, key, true) {
			return
		}
		if baseRevision == nil || *baseRevision < 0 {
			writeArtifactRuntimeError(w, http.StatusBadRequest, "base_revision_required", "Artifact Runtime base_revision is required", 0)
			return
		}
		var nextValue json.RawMessage
		var err error
		if operation == "state.put" {
			if len(patch) != 0 || len(value) == 0 {
				writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_state_value", "Artifact Runtime State value is invalid", 0)
				return
			}
			if tokenErr := validateArtifactRuntimeJSONTokens(value); tokenErr != nil {
				err = tokenErr
			} else {
				nextValue, err = normalizeBoundedArtifactJSON(value, artifactRuntimeStateMaxBytes, artifactRuntimeStateMaxNodes)
			}
		} else {
			if len(value) != 0 || len(patch) == 0 || *baseRevision <= 0 {
				writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_state_patch", "Artifact Runtime patch requires an existing revision", 0)
				return
			}
			current, found, readErr := h.store.GetArtifactRuntimeState(r.Context(), access.AgentUID, access.Artifact.ID, namespace, key)
			if readErr != nil {
				writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "state_unavailable", "Artifact Runtime State is unavailable", 0)
				return
			}
			if !found || current.Revision != *baseRevision {
				currentRevision := int64(0)
				if found {
					currentRevision = current.Revision
				}
				writeArtifactRuntimeError(w, http.StatusConflict, "revision_conflict", "Artifact Runtime revision changed", currentRevision)
				return
			}
			nextValue, err = applyArtifactRuntimePatch(current.Value, patch)
		}
		if err != nil {
			writeArtifactRuntimeError(w, http.StatusUnprocessableEntity, "invalid_state_change", err.Error(), 0)
			return
		}
		state, event, err := h.store.PutArtifactRuntimeState(r.Context(), &store.ArtifactRuntimeState{
			AgentUID: access.AgentUID, ArtifactID: access.Artifact.ID,
			Namespace: namespace, Key: key, Value: nextValue,
			UpdatedByUID: access.ActorUID, UpdatedBy: updatedBy,
		}, *baseRevision)
		if err != nil {
			var conflict *store.ArtifactRuntimeRevisionConflict
			if errors.As(err, &conflict) {
				writeArtifactRuntimeError(w, http.StatusConflict, "revision_conflict", "Artifact Runtime revision changed", conflict.CurrentRevision)
				return
			}
			writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "state_unavailable", "Artifact Runtime State could not be written", 0)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "applied": true, "contract_version": artifactRuntimeResponseContract,
			"operation": operation, "state": artifactRuntimeStateResponse(state, true),
			"event": artifactRuntimeEventResponse(event),
		})
	case "events.list":
		if afterEventID < 0 {
			writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_event_cursor", "Artifact Runtime event cursor is invalid", 0)
			return
		}
		if limit <= 0 {
			limit = 50
		}
		if limit > artifactRuntimeEventListMax {
			limit = artifactRuntimeEventListMax
		}
		events, err := h.store.ListArtifactRuntimeEvents(r.Context(), access.AgentUID, access.Artifact.ID, afterEventID, limit)
		if err != nil {
			writeArtifactRuntimeError(w, http.StatusServiceUnavailable, "events_unavailable", "Artifact Runtime events are unavailable", 0)
			return
		}
		encoded := make([]map[string]interface{}, 0, len(events))
		cursor := afterEventID
		for _, event := range events {
			if event.EventID > cursor {
				cursor = event.EventID
			}
			if !access.Manifest.allowsNamespace(event.Namespace, false) {
				continue
			}
			encoded = append(encoded, artifactRuntimeEventResponse(event))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "contract_version": artifactRuntimeResponseContract,
			"operation": operation, "events": encoded, "event_cursor": cursor,
		})
	default:
		writeArtifactRuntimeError(w, http.StatusBadRequest, "unsupported_operation", "Artifact Runtime operation is unsupported", 0)
	}
}

func (h *ArtifactRuntimeHandler) validateStateTarget(
	w http.ResponseWriter,
	access artifactRuntimeAccess,
	namespace, key string,
	write bool,
) bool {
	if !validArtifactRuntimeNamespace(namespace) || !validArtifactRuntimeDocumentKey(key) {
		writeArtifactRuntimeError(w, http.StatusBadRequest, "invalid_state_key", "Artifact Runtime State key is invalid", 0)
		return false
	}
	if !access.Manifest.allowsNamespace(namespace, write) {
		writeArtifactRuntimeError(w, http.StatusForbidden, "namespace_not_declared", "Artifact Runtime namespace is not declared", 0)
		return false
	}
	return true
}

func (h *ArtifactRuntimeHandler) resolveViewerAccess(
	ctx context.Context,
	actorUID int64,
	request artifactRuntimeUserRequest,
) (artifactRuntimeAccess, int, string, error) {
	if request.ContractVersion != artifactRuntimeRequestContract || strings.TrimSpace(request.TopicID) == "" {
		return artifactRuntimeAccess{}, http.StatusBadRequest, "invalid_request", errors.New("invalid Artifact Runtime identity")
	}
	candidate, ok := parseArtifactRefCandidate(map[string]interface{}{artifactRefMetadataKey: request.ArtifactRef})
	if !ok || candidate.DisplayedVersion <= 0 {
		return artifactRuntimeAccess{}, http.StatusBadRequest, "invalid_artifact", errors.New("invalid Artifact Runtime Artifact")
	}
	agentUID, ok := h.hub.artifactAgentForTopic(actorUID, strings.TrimSpace(request.TopicID))
	if !ok {
		return artifactRuntimeAccess{}, http.StatusForbidden, "topic_mismatch", errors.New("Artifact Runtime topic does not match")
	}
	route, ok := h.hub.artifactPreviewSessions.verify(request.PreviewSession, actorUID)
	if !ok || !h.hub.artifactPreviewRouteConnected(actorUID, route) {
		return artifactRuntimeAccess{}, http.StatusConflict, "preview_disconnected", errors.New("Artifact Runtime preview is not connected")
	}
	record, manifest, status, code, err := h.resolveArtifactRuntime(
		ctx, agentUID, candidate.ID, candidate.DisplayedVersion,
	)
	if err != nil {
		return artifactRuntimeAccess{}, status, code, err
	}
	return artifactRuntimeAccess{
		ActorUID: actorUID, AgentUID: agentUID, TopicID: strings.TrimSpace(request.TopicID),
		DisplayedVersion: candidate.DisplayedVersion, Artifact: record, Manifest: manifest,
	}, http.StatusOK, "", nil
}

func (h *ArtifactRuntimeHandler) resolveAgentAccess(
	ctx context.Context,
	botUID int64,
	contextRef, taskRef string,
) (artifactRuntimeAccess, int, string, error) {
	if (contextRef == "") == (taskRef == "") {
		return artifactRuntimeAccess{}, http.StatusBadRequest, "runtime_ref_required", errors.New("exactly one Artifact Runtime reference is required")
	}
	var access artifactRuntimeAccess
	access.ActorUID = botUID
	access.AgentUID = botUID
	if contextRef != "" {
		ref, ok := normalizeArtifactContextRef(contextRef)
		if !ok || h.hub.artifactContextSnapshots == nil {
			return artifactRuntimeAccess{}, http.StatusBadRequest, "context_ref_invalid", errors.New("Artifact context reference is invalid")
		}
		snapshot, state := h.hub.artifactContextSnapshots.lookup(ref)
		if state != artifactContextSnapshotActive || snapshot.AgentUID != botUID {
			return artifactRuntimeAccess{}, http.StatusGone, "context_ref_expired", errors.New("Artifact context reference is missing or expired")
		}
		currentAgentUID, ok := h.hub.artifactAgentForTopic(snapshot.ActorUID, snapshot.TopicID)
		if !ok || currentAgentUID != botUID {
			return artifactRuntimeAccess{}, http.StatusForbidden, "context_mismatch", errors.New("Artifact context belongs to another Agent")
		}
		access.TopicID = snapshot.TopicID
		access.DisplayedVersion = snapshot.DisplayedVersion
		access.PageContext = cloneArtifactPageContext(snapshot.PageContext)
		access.Artifact = snapshot.Artifact
	} else {
		ref, ok := normalizeArtifactTaskRef(taskRef)
		if !ok || h.hub.artifactTasks == nil {
			return artifactRuntimeAccess{}, http.StatusBadRequest, "task_ref_invalid", errors.New("Artifact task reference is invalid")
		}
		task, ok := h.hub.artifactTasks.forBot(ref, botUID)
		if !ok {
			return artifactRuntimeAccess{}, http.StatusGone, "task_ref_expired", errors.New("Artifact task reference is missing or expired")
		}
		currentAgentUID, validTopic := h.hub.artifactAgentForTopic(task.ActorUID, task.TopicID)
		if !validTopic || currentAgentUID != botUID {
			return artifactRuntimeAccess{}, http.StatusForbidden, "task_mismatch", errors.New("Artifact task belongs to another Agent")
		}
		access.TopicID = task.TopicID
		access.DisplayedVersion = task.DisplayedVersion
		access.PageContext = cloneArtifactPageContext(task.PageContext)
		access.Artifact = task.Artifact
	}
	if access.DisplayedVersion <= 0 || !validArtifactContextRecord(access.Artifact, access.Artifact.ID) {
		return artifactRuntimeAccess{}, http.StatusGone, "runtime_ref_expired", errors.New("Artifact Runtime reference has no current version")
	}
	record, manifest, status, code, err := h.resolveArtifactRuntime(
		ctx, botUID, access.Artifact.ID, access.DisplayedVersion,
	)
	if err != nil {
		return artifactRuntimeAccess{}, status, code, err
	}
	access.Artifact = record
	access.Manifest = manifest
	return access, http.StatusOK, "", nil
}

func (h *ArtifactRuntimeHandler) resolveArtifactRuntime(
	ctx context.Context,
	agentUID int64,
	artifactID string,
	displayedVersion int64,
) (ArtifactContextRecord, ArtifactRuntimeManifest, int, string, error) {
	resolutionCtx, cancel := context.WithTimeout(ctx, artifactRuntimeResolutionTimeout)
	defer cancel()
	record, err := h.hub.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, agentUID, artifactID)
	if err != nil || !validArtifactContextRecord(record, artifactID) ||
		(record.PublishVersion > 0 && displayedVersion > int64(record.PublishVersion)) {
		return ArtifactContextRecord{}, ArtifactRuntimeManifest{}, http.StatusServiceUnavailable,
			"artifact_unavailable", errors.New("Artifact Runtime target is unavailable")
	}
	manifest, err := h.hub.artifactRuntimeResolver.ResolveArtifactRuntimeManifest(resolutionCtx, record, displayedVersion)
	if err != nil {
		return ArtifactContextRecord{}, ArtifactRuntimeManifest{}, http.StatusUnprocessableEntity,
			"runtime_not_enabled", errors.New("this Artifact version has not enabled Runtime 0.1")
	}
	return record, manifest, http.StatusOK, "", nil
}

func decodeArtifactRuntimeUserRequest(w http.ResponseWriter, r *http.Request) (artifactRuntimeUserRequest, error) {
	var request artifactRuntimeUserRequest
	if err := decodeArtifactRuntimeRequestBody(w, r, &request); err != nil {
		return request, errors.New("invalid Artifact Runtime request")
	}
	request.TopicID = strings.TrimSpace(request.TopicID)
	return request, nil
}

func decodeArtifactRuntimeRequestBody(w http.ResponseWriter, r *http.Request, target interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, artifactRuntimeRequestMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil || validateArtifactRuntimeJSONTokens(body) != nil {
		return errors.New("invalid Artifact Runtime request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || ensureJSONEOF(decoder) != nil {
		return errors.New("invalid Artifact Runtime request")
	}
	return nil
}

func validArtifactRuntimeDocumentKey(value string) bool {
	return value == strings.TrimSpace(value) && artifactRuntimeDocumentKeyPattern.MatchString(value)
}

func validArtifactRuntimeNamespace(value string) bool {
	return len(value) <= 64 && artifactRuntimeNamePattern.MatchString(value)
}

func artifactRuntimeStateReferences(states []*store.ArtifactRuntimeState, manifest ArtifactRuntimeManifest) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(states))
	for _, state := range states {
		if state == nil || !manifest.allowsNamespace(state.Namespace, false) {
			continue
		}
		result = append(result, map[string]interface{}{
			"namespace": state.Namespace, "key": state.Key, "revision": state.Revision,
			"updated_at": state.UpdatedAt.Format(time.RFC3339Nano),
		})
	}
	return result
}

func artifactRuntimeStateResponse(state *store.ArtifactRuntimeState, includeValue bool) map[string]interface{} {
	response := map[string]interface{}{
		"contract_version": artifactRuntimeStateContract,
		"exists":           true, "namespace": state.Namespace, "key": state.Key,
		"revision": state.Revision, "updated_at": state.UpdatedAt.Format(time.RFC3339Nano),
	}
	if includeValue {
		response["value"] = json.RawMessage(append([]byte(nil), state.Value...))
	}
	return response
}

func artifactRuntimeMissingStateResponse(namespace, key string) map[string]interface{} {
	return map[string]interface{}{
		"contract_version": artifactRuntimeStateContract,
		"exists":           false, "namespace": namespace, "key": key, "revision": int64(0),
	}
}

func artifactRuntimeEventResponse(event *store.ArtifactRuntimeEvent) map[string]interface{} {
	return map[string]interface{}{
		"contract_version": artifactRuntimeEventContract,
		"event_id":         event.EventID, "type": event.EventType,
		"namespace": event.Namespace, "key": event.Key, "revision": event.Revision,
		"updated_by": event.UpdatedBy, "created_at": event.CreatedAt.Format(time.RFC3339Nano),
	}
}

func artifactRuntimeArtifactResponse(access artifactRuntimeAccess) map[string]interface{} {
	return map[string]interface{}{
		"id":                access.Artifact.ID,
		"agent_uid":         strconv.FormatInt(access.AgentUID, 10),
		"title":             strings.TrimSpace(access.Artifact.Title),
		"kind":              access.Artifact.Kind,
		"url":               strings.TrimSpace(access.Artifact.URL),
		"topic_id":          access.TopicID,
		"displayed_version": access.DisplayedVersion,
		"latest_version":    access.Artifact.PublishVersion,
		"currently_visible": true,
	}
}

func artifactRuntimeViewFromPageContext(pageContext map[string]interface{}) interface{} {
	semantic, ok := pageContext["semantic_context"].(map[string]interface{})
	if !ok {
		return nil
	}
	return semantic["runtime_view"]
}

func artifactRuntimeNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func writeArtifactRuntimeError(w http.ResponseWriter, status int, code, message string, currentRevision int64) {
	errorValue := map[string]interface{}{
		"code":    code,
		"message": message,
	}
	if code == "revision_conflict" {
		errorValue["current_revision"] = currentRevision
	}
	writeJSON(w, status, map[string]interface{}{
		"ok":               false,
		"contract_version": artifactRuntimeResponseContract,
		"error":            errorValue,
	})
}
