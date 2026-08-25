package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

const (
	artifactWritebackTargetContract = "catsco.artifact-writeback-target.v1"
	artifactResultContract          = "catsco.artifact-result.v1"
	artifactResultReceiptContract   = "catsco.artifact-result-receipt.v1"
	artifactResultDeliveryContract  = "catsco.artifact-result-delivery.v1"

	artifactWritebackTTLDefault      = 30 * time.Minute
	artifactResultDeliveryTTLDefault = 22 * time.Second
	artifactResultStoreMaxEntries    = 4096
	artifactResultRequestMaxBody     = 80 * 1024
	artifactResultPayloadMaxBytes    = 64 * 1024
	artifactResultReceiptMaxBytes    = 8 * 1024
	artifactResultRandomBytes        = 32
)

var (
	artifactWritebackRefPattern = regexp.MustCompile(`^awr_[A-Za-z0-9_-]{43}$`)
	artifactResultIDPattern     = regexp.MustCompile(`^arr_[A-Za-z0-9_-]{43}$`)
	artifactResultSinkIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9][0-9]*$`)
	artifactResultCodePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	artifactRuntimeNodePattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

type artifactWritebackTarget struct {
	Ref              string
	ContextRef       string
	ActorUID         int64
	TopicID          string
	AgentUID         int64
	ArtifactID       string
	DisplayedVersion int64
	SnapshotRevision uint64
	PreviewRoute     runtimeRoute
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

type artifactResultDeliveryOutcome struct {
	Status             string
	Code               string
	Message            string
	ApplicationReceipt json.RawMessage
}

type artifactResultDeliveryState struct {
	ResultID    string
	RequestHash [sha256.Size]byte
	Target      artifactWritebackTarget
	Done        chan struct{}
	Outcome     artifactResultDeliveryOutcome
	Completed   bool
	// SendClaimed is the linearization point between preview invalidation and delivery.
	SendClaimed bool
	WaitUntil   time.Time
	RetainUntil time.Time
	CreatedAt   time.Time
}

type artifactResultWritebackStore struct {
	mu        sync.Mutex
	tickets   map[string]artifactWritebackTarget
	byContext map[string]string
	// invalidated prevents a delayed Bot read from reissuing a target after replacement.
	invalidated map[string]time.Time
	deliveries  map[string]*artifactResultDeliveryState
	ticketTTL   time.Duration
	deliveryTTL time.Duration
	maxEntries  int
	now         func() time.Time
}

type ArtifactResultHandler struct {
	hub *Hub
}

type artifactResultSubmitRequest struct {
	ContractVersion       string          `json:"contract_version"`
	WritebackRef          string          `json:"writeback_ref"`
	ArtifactID            string          `json:"artifact_id"`
	DisplayedVersion      int64           `json:"displayed_version"`
	SinkID                string          `json:"sink_id"`
	ResultID              string          `json:"result_id"`
	ExpectedStateRevision string          `json:"expected_state_revision,omitempty"`
	Payload               json.RawMessage `json:"payload"`
}

type artifactApplicationReceipt struct {
	ContractVersion string          `json:"contract_version"`
	ResultID        string          `json:"result_id"`
	Status          string          `json:"status"`
	Code            string          `json:"code,omitempty"`
	Message         string          `json:"message,omitempty"`
	Receipt         json.RawMessage `json:"receipt,omitempty"`
}

func newArtifactResultWritebackStore(ticketTTL, deliveryTTL time.Duration, maxEntries int) *artifactResultWritebackStore {
	if ticketTTL <= 0 {
		ticketTTL = artifactWritebackTTLDefault
	}
	if deliveryTTL <= 0 {
		deliveryTTL = artifactResultDeliveryTTLDefault
	}
	if maxEntries <= 0 {
		maxEntries = artifactResultStoreMaxEntries
	}
	return &artifactResultWritebackStore{
		tickets:     make(map[string]artifactWritebackTarget),
		byContext:   make(map[string]string),
		invalidated: make(map[string]time.Time),
		deliveries:  make(map[string]*artifactResultDeliveryState),
		ticketTTL:   ticketTTL,
		deliveryTTL: deliveryTTL,
		maxEntries:  maxEntries,
		now:         time.Now,
	}
}

func NewArtifactResultHandler(hub *Hub) *ArtifactResultHandler {
	return &ArtifactResultHandler{hub: hub}
}

func newArtifactResultOpaqueRef(prefix string) (string, error) {
	value := make([]byte, artifactResultRandomBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *artifactResultWritebackStore) issue(snapshot artifactContextSnapshot) (artifactWritebackTarget, error) {
	if s == nil || snapshot.Ref == "" || snapshot.ActorUID <= 0 || snapshot.AgentUID <= 0 ||
		!validArtifactID(snapshot.Artifact.ID) || snapshot.DisplayedVersion <= 0 ||
		snapshot.PreviewRoute.NodeID == "" || snapshot.PreviewRoute.ConnectionID == "" {
		return artifactWritebackTarget{}, errors.New("invalid Artifact writeback target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	if invalidUntil := s.invalidated[snapshot.Ref]; !invalidUntil.IsZero() && now.Before(invalidUntil) {
		return artifactWritebackTarget{}, errors.New("Artifact context is no longer current")
	}
	if ref := s.byContext[snapshot.Ref]; ref != "" {
		if target, ok := s.tickets[ref]; ok && now.Before(target.ExpiresAt) &&
			target.ActorUID == snapshot.ActorUID && target.TopicID == snapshot.TopicID &&
			target.AgentUID == snapshot.AgentUID && target.ArtifactID == snapshot.Artifact.ID &&
			target.DisplayedVersion == snapshot.DisplayedVersion && target.SnapshotRevision == snapshot.Revision &&
			target.PreviewRoute.matches(snapshot.PreviewRoute) {
			return target, nil
		}
	}
	if len(s.tickets) >= s.maxEntries {
		return artifactWritebackTarget{}, errors.New("Artifact writeback target store is full")
	}
	ref, err := newArtifactResultOpaqueRef("awr_")
	if err != nil {
		return artifactWritebackTarget{}, err
	}
	target := artifactWritebackTarget{
		Ref:              ref,
		ContextRef:       snapshot.Ref,
		ActorUID:         snapshot.ActorUID,
		TopicID:          snapshot.TopicID,
		AgentUID:         snapshot.AgentUID,
		ArtifactID:       snapshot.Artifact.ID,
		DisplayedVersion: snapshot.DisplayedVersion,
		SnapshotRevision: snapshot.Revision,
		PreviewRoute:     snapshot.PreviewRoute,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.ticketTTL),
	}
	s.tickets[target.Ref] = target
	s.byContext[target.ContextRef] = target.Ref
	return target, nil
}

func (s *artifactResultWritebackStore) target(ref string) (artifactWritebackTarget, bool) {
	if s == nil || !artifactWritebackRefPattern.MatchString(ref) {
		return artifactWritebackTarget{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	target, ok := s.tickets[ref]
	return target, ok && now.Before(target.ExpiresAt)
}

func (s *artifactResultWritebackStore) invalidateContext(contextRef string) {
	if s == nil || contextRef == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	s.invalidated[contextRef] = now.Add(s.ticketTTL)
	ref := s.byContext[contextRef]
	delete(s.byContext, contextRef)
	delete(s.tickets, ref)
	for _, delivery := range s.deliveries {
		if delivery.Completed || delivery.SendClaimed || delivery.Target.ContextRef != contextRef {
			continue
		}
		s.completeLocked(delivery, artifactResultDeliveryOutcome{
			Status: "target_mismatch",
			Code:   "artifact_preview_changed",
		})
	}
}

func (s *artifactResultWritebackStore) claimDeliveryForSend(delivery *artifactResultDeliveryState) (artifactWritebackTarget, bool) {
	if s == nil || delivery == nil {
		return artifactWritebackTarget{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	currentDelivery := s.deliveries[delivery.ResultID]
	if currentDelivery != delivery || delivery.Completed || delivery.SendClaimed {
		return artifactWritebackTarget{}, false
	}
	currentTarget, ok := s.tickets[delivery.Target.Ref]
	if !ok || !now.Before(currentTarget.ExpiresAt) || currentTarget != delivery.Target {
		s.completeLocked(delivery, artifactResultDeliveryOutcome{
			Status: "target_mismatch",
			Code:   "artifact_preview_changed",
		})
		return artifactWritebackTarget{}, false
	}
	delivery.SendClaimed = true
	return delivery.Target, true
}

func (s *artifactResultWritebackStore) startDelivery(
	request artifactResultSubmitRequest,
	target artifactWritebackTarget,
	requestHash [sha256.Size]byte,
) (*artifactResultDeliveryState, bool, string) {
	if s == nil {
		return nil, false, "unavailable"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	existing := s.deliveries[request.ResultID]
	if existing != nil {
		if existing.RequestHash != requestHash {
			return nil, false, "result_id_conflict"
		}
		if !existing.Completed || artifactResultApplicationTerminal(existing.Outcome.Status) {
			return existing, false, ""
		}
	}
	current, ok := s.tickets[target.Ref]
	if !ok || !now.Before(current.ExpiresAt) || current != target {
		return nil, false, "expired"
	}
	if existing == nil && len(s.deliveries) >= s.maxEntries {
		return nil, false, "unavailable"
	}
	delivery := &artifactResultDeliveryState{
		ResultID:    request.ResultID,
		RequestHash: requestHash,
		Target:      target,
		Done:        make(chan struct{}),
		WaitUntil:   now.Add(s.deliveryTTL),
		RetainUntil: target.ExpiresAt,
		CreatedAt:   now,
	}
	s.deliveries[request.ResultID] = delivery
	return delivery, true, ""
}

func artifactResultApplicationTerminal(status string) bool {
	return status == "applied" || status == "rejected" || status == "failed"
}

func (s *artifactResultWritebackStore) completePlatform(
	delivery *artifactResultDeliveryState,
	status, code, message string,
) {
	if s == nil || delivery == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !delivery.Completed {
		s.completeLocked(delivery, artifactResultDeliveryOutcome{
			Status:  status,
			Code:    code,
			Message: message,
		})
	}
}

func (s *artifactResultWritebackStore) completeReceipt(msg *MsgArtifactResult, receipt json.RawMessage, sourceRoute runtimeRoute) bool {
	if s == nil || msg == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery := s.deliveries[msg.ResultID]
	if delivery == nil || delivery.Completed || !delivery.SendClaimed || !delivery.Target.PreviewRoute.matches(sourceRoute) {
		return false
	}
	target := delivery.Target
	actorUID, _ := strconv.ParseInt(msg.ActorUID, 10, 64)
	if actorUID != target.ActorUID || msg.ContextRef != target.ContextRef ||
		msg.WritebackRef != target.Ref || msg.TopicID != target.TopicID ||
		msg.AgentUID != strconv.FormatInt(target.AgentUID, 10) ||
		msg.ArtifactID != target.ArtifactID || msg.DisplayedVersion != target.DisplayedVersion {
		return false
	}
	application, err := normalizeArtifactApplicationReceipt(receipt, msg.ResultID)
	if err != nil {
		invalid := artifactApplicationReceipt{
			ContractVersion: artifactResultReceiptContract,
			ResultID:        msg.ResultID,
			Status:          "failed",
			Code:            "invalid_receipt",
		}
		s.completeLocked(delivery, artifactResultDeliveryOutcome{
			Status:             "failed",
			Code:               "invalid_receipt",
			ApplicationReceipt: invalid.raw(),
		})
		return true
	}
	s.completeLocked(delivery, artifactResultDeliveryOutcome{
		Status:             application.Status,
		Code:               application.Code,
		Message:            application.Message,
		ApplicationReceipt: application.raw(),
	})
	return true
}

func (s *artifactResultWritebackStore) outcome(resultID string) (artifactResultDeliveryOutcome, bool) {
	if s == nil {
		return artifactResultDeliveryOutcome{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery := s.deliveries[resultID]
	if delivery == nil || !delivery.Completed {
		return artifactResultDeliveryOutcome{}, false
	}
	return delivery.Outcome, true
}

func (s *artifactResultWritebackStore) outcomeFor(delivery *artifactResultDeliveryState) (artifactResultDeliveryOutcome, bool) {
	if s == nil || delivery == nil {
		return artifactResultDeliveryOutcome{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !delivery.Completed {
		return artifactResultDeliveryOutcome{}, false
	}
	return delivery.Outcome, true
}

func (s *artifactResultWritebackStore) completeLocked(delivery *artifactResultDeliveryState, outcome artifactResultDeliveryOutcome) {
	if delivery == nil || delivery.Completed {
		return
	}
	delivery.Outcome = outcome
	delivery.Completed = true
	close(delivery.Done)
}

func (s *artifactResultWritebackStore) cleanupLocked(now time.Time) {
	for contextRef, expiresAt := range s.invalidated {
		if !now.Before(expiresAt) {
			delete(s.invalidated, contextRef)
		}
	}
	for ref, target := range s.tickets {
		if now.Before(target.ExpiresAt) {
			continue
		}
		delete(s.tickets, ref)
		if s.byContext[target.ContextRef] == ref {
			delete(s.byContext, target.ContextRef)
		}
	}
	for resultID, delivery := range s.deliveries {
		if !delivery.Completed && !now.Before(delivery.WaitUntil) {
			s.completeLocked(delivery, artifactResultDeliveryOutcome{
				Status: "delivery_timeout",
				Code:   "artifact_result_delivery_timeout",
			})
		}
		if !now.Before(delivery.RetainUntil) {
			delete(s.deliveries, resultID)
		}
	}
}

func (r artifactApplicationReceipt) raw() json.RawMessage {
	value, _ := json.Marshal(r)
	return value
}

func (h *ArtifactResultHandler) HandleBotResults(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil || h.hub == nil || h.hub.artifactResultWritebacks == nil || h.hub.artifactContextResolver == nil {
		writeArtifactResultDelivery(w, http.StatusServiceUnavailable, "", artifactResultDeliveryOutcome{
			Status: "unavailable",
			Code:   "artifact_result_unavailable",
		})
		return
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeArtifactResultDelivery(w, http.StatusUnauthorized, "", artifactResultDeliveryOutcome{
			Status: "target_mismatch",
			Code:   "artifact_result_authentication_failed",
		})
		return
	}

	request, err := decodeArtifactResultSubmitRequest(w, r)
	if err != nil {
		writeArtifactResultDelivery(w, http.StatusBadRequest, request.ResultID, artifactResultDeliveryOutcome{
			Status:  "rejected",
			Code:    "invalid_result_request",
			Message: err.Error(),
		})
		return
	}
	target, ok := h.hub.artifactResultWritebacks.target(request.WritebackRef)
	if !ok {
		writeArtifactResultDelivery(w, http.StatusGone, request.ResultID, artifactResultDeliveryOutcome{
			Status: "expired",
			Code:   "artifact_writeback_ref_expired",
		})
		return
	}
	if target.AgentUID != botUID || target.ArtifactID != request.ArtifactID ||
		target.DisplayedVersion != request.DisplayedVersion {
		writeArtifactResultDelivery(w, http.StatusForbidden, request.ResultID, artifactResultDeliveryOutcome{
			Status: "target_mismatch",
			Code:   "artifact_result_target_mismatch",
		})
		return
	}
	currentAgentUID, ok := h.hub.artifactAgentForTopic(target.ActorUID, target.TopicID)
	if !ok || currentAgentUID != botUID {
		writeArtifactResultDelivery(w, http.StatusForbidden, request.ResultID, artifactResultDeliveryOutcome{
			Status: "target_mismatch",
			Code:   "artifact_result_topic_mismatch",
		})
		return
	}
	resolutionCtx, cancel := context.WithTimeout(r.Context(), artifactContextResolutionTimeout)
	record, resolveErr := h.hub.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, botUID, target.ArtifactID)
	cancel()
	if resolveErr != nil || !validArtifactContextRecord(record, target.ArtifactID) ||
		(record.PublishVersion > 0 && target.DisplayedVersion > int64(record.PublishVersion)) {
		writeArtifactResultDelivery(w, http.StatusServiceUnavailable, request.ResultID, artifactResultDeliveryOutcome{
			Status: "unavailable",
			Code:   "artifact_result_target_unavailable",
		})
		return
	}

	requestHash := hashArtifactResultRequest(request, target)
	delivery, created, startStatus := h.hub.artifactResultWritebacks.startDelivery(request, target, requestHash)
	if startStatus != "" {
		statusCode := http.StatusServiceUnavailable
		status := startStatus
		if startStatus == "result_id_conflict" {
			statusCode = http.StatusConflict
			status = "rejected"
		} else if startStatus == "expired" {
			statusCode = http.StatusGone
		}
		writeArtifactResultDelivery(w, statusCode, request.ResultID, artifactResultDeliveryOutcome{
			Status: status,
			Code:   "artifact_result_" + startStatus,
		})
		return
	}

	if created {
		target, claimed := h.hub.artifactResultWritebacks.claimDeliveryForSend(delivery)
		if claimed {
			message := &ServerMessage{ArtifactResult: &MsgArtifactResult{
				Type:                  "request",
				OriginNodeID:          h.hub.nodeID,
				ActorUID:              strconv.FormatInt(target.ActorUID, 10),
				ContextRef:            target.ContextRef,
				WritebackRef:          target.Ref,
				TopicID:               target.TopicID,
				AgentUID:              strconv.FormatInt(target.AgentUID, 10),
				ArtifactID:            target.ArtifactID,
				DisplayedVersion:      target.DisplayedVersion,
				SinkID:                request.SinkID,
				ResultID:              request.ResultID,
				ExpectedStateRevision: request.ExpectedStateRevision,
				Payload:               request.Payload,
			}}
			delivered := h.hub.sendArtifactResultToRoute(target.PreviewRoute, message.ArtifactResult)
			if !delivered {
				h.hub.artifactResultWritebacks.completePlatform(
					delivery,
					"not_connected",
					"artifact_preview_not_connected",
					"",
				)
			}
		}
	}

	wait := time.NewTimer(h.hub.artifactResultWritebacks.deliveryTTL + time.Second)
	defer wait.Stop()
	select {
	case <-delivery.Done:
	case <-wait.C:
		h.hub.artifactResultWritebacks.completePlatform(
			delivery,
			"delivery_timeout",
			"artifact_result_delivery_timeout",
			"",
		)
	case <-r.Context().Done():
		return
	}
	outcome, ok := h.hub.artifactResultWritebacks.outcomeFor(delivery)
	if !ok {
		outcome = artifactResultDeliveryOutcome{
			Status: "unavailable",
			Code:   "artifact_result_outcome_unavailable",
		}
	}
	writeArtifactResultDelivery(w, http.StatusOK, request.ResultID, outcome)
}

func (h *Hub) handleArtifactResultReceipt(client *Client, msg *MsgArtifactResult) {
	if h == nil || client == nil || msg == nil || client.accountType != types.AccountHuman ||
		strings.TrimSpace(msg.Type) != "receipt" || !artifactRuntimeNodePattern.MatchString(msg.OriginNodeID) ||
		!artifactContextRefPattern.MatchString(msg.ContextRef) ||
		!artifactWritebackRefPattern.MatchString(msg.WritebackRef) ||
		!artifactResultIDPattern.MatchString(msg.ResultID) || !validArtifactID(msg.ArtifactID) ||
		msg.DisplayedVersion <= 0 || len(msg.Receipt) == 0 || len(msg.Receipt) > artifactResultReceiptMaxBytes {
		return
	}
	canonical := &MsgArtifactResult{
		Type:             "receipt",
		OriginNodeID:     msg.OriginNodeID,
		ActorUID:         strconv.FormatInt(client.uid, 10),
		ContextRef:       msg.ContextRef,
		WritebackRef:     msg.WritebackRef,
		TopicID:          strings.TrimSpace(msg.TopicID),
		AgentUID:         strings.TrimSpace(msg.AgentUID),
		ArtifactID:       msg.ArtifactID,
		DisplayedVersion: msg.DisplayedVersion,
		ResultID:         msg.ResultID,
		Receipt:          append(json.RawMessage(nil), msg.Receipt...),
	}
	sourceRoute := h.clientRoute(client)
	if canonical.OriginNodeID == h.nodeID {
		h.acceptArtifactResultReceipt(canonical, sourceRoute)
		return
	}
	if h.sharedRuntime != nil {
		h.sharedRuntime.deliverArtifactResultReceipt(canonical.OriginNodeID, canonical, sourceRoute, time.Now())
	}
}

func (h *Hub) acceptArtifactResultReceipt(msg *MsgArtifactResult, sourceRoute runtimeRoute) bool {
	return h != nil && h.artifactResultWritebacks != nil &&
		h.artifactResultWritebacks.completeReceipt(msg, msg.Receipt, sourceRoute)
}

func (h *Hub) sendArtifactResultToLocalRoute(route runtimeRoute, msg *MsgArtifactResult) bool {
	if h == nil || msg == nil || route.ConnectionID == "" {
		return false
	}
	client := h.getClientByConnectionID(route.ConnectionID)
	actorUID, _ := strconv.ParseInt(msg.ActorUID, 10, 64)
	if client == nil || actorUID <= 0 || client.uid != actorUID ||
		client.accountType != types.AccountHuman || client.deviceConnector != nil {
		return false
	}
	h.SendToClient(client, &ServerMessage{ArtifactResult: msg})
	return true
}

func (h *Hub) sendArtifactResultToRoute(route runtimeRoute, msg *MsgArtifactResult) bool {
	if h == nil || msg == nil || route.NodeID == "" || route.ConnectionID == "" {
		return false
	}
	if route.NodeID == h.nodeID {
		return h.sendArtifactResultToLocalRoute(route, msg)
	}
	return h.sharedRuntime != nil && h.sharedRuntime.deliverArtifactResult(route, msg, time.Now())
}

func decodeArtifactResultSubmitRequest(w http.ResponseWriter, r *http.Request) (artifactResultSubmitRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, artifactResultRequestMaxBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request artifactResultSubmitRequest
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("Artifact result request is invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return request, errors.New("Artifact result request contains trailing data")
	}
	if request.ContractVersion != artifactResultContract ||
		!artifactWritebackRefPattern.MatchString(request.WritebackRef) ||
		!validArtifactID(request.ArtifactID) || request.DisplayedVersion <= 0 ||
		!artifactResultSinkIDPattern.MatchString(request.SinkID) ||
		!artifactResultIDPattern.MatchString(request.ResultID) ||
		!validArtifactResultRevision(request.ExpectedStateRevision) {
		return request, errors.New("Artifact result request fields are invalid")
	}
	payload, err := normalizeBoundedArtifactJSON(request.Payload, artifactResultPayloadMaxBytes, 16_384)
	if err != nil {
		return request, errors.New("Artifact result payload is invalid or too large")
	}
	request.Payload = payload
	return request, nil
}

func normalizeArtifactApplicationReceipt(raw json.RawMessage, expectedResultID string) (artifactApplicationReceipt, error) {
	if len(raw) == 0 || len(raw) > artifactResultReceiptMaxBytes {
		return artifactApplicationReceipt{}, errors.New("Artifact result receipt is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var receipt artifactApplicationReceipt
	if err := decoder.Decode(&receipt); err != nil || ensureJSONEOF(decoder) != nil {
		return artifactApplicationReceipt{}, errors.New("Artifact result receipt is invalid")
	}
	if receipt.ContractVersion != artifactResultReceiptContract || receipt.ResultID != expectedResultID ||
		!artifactResultIDPattern.MatchString(receipt.ResultID) ||
		(receipt.Status != "applied" && receipt.Status != "rejected" && receipt.Status != "failed") ||
		(receipt.Code != "" && !artifactResultCodePattern.MatchString(receipt.Code)) ||
		!validArtifactResultMessage(receipt.Message) {
		return artifactApplicationReceipt{}, errors.New("Artifact result receipt fields are invalid")
	}
	if len(receipt.Receipt) > 0 {
		normalized, err := normalizeBoundedArtifactJSON(receipt.Receipt, artifactResultReceiptMaxBytes, 2_048)
		if err != nil {
			return artifactApplicationReceipt{}, errors.New("Artifact result receipt payload is invalid")
		}
		receipt.Receipt = normalized
	}
	return receipt, nil
}

func normalizeBoundedArtifactJSON(raw json.RawMessage, maxBytes, maxNodes int) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxBytes {
		return nil, errors.New("JSON size is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil || ensureJSONEOF(decoder) != nil {
		return nil, errors.New("JSON is invalid")
	}
	nodes := 0
	if !validBoundedArtifactJSONValue(value, 0, &nodes, maxNodes) {
		return nil, errors.New("JSON exceeds structural limits")
	}
	normalized, err := json.Marshal(value)
	if err != nil || len(normalized) == 0 || len(normalized) > maxBytes {
		return nil, errors.New("JSON exceeds size limits")
	}
	return normalized, nil
}

func validBoundedArtifactJSONValue(value interface{}, depth int, nodes *int, maxNodes int) bool {
	(*nodes)++
	if depth > 12 || *nodes > maxNodes {
		return false
	}
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return true
	case string:
		return utf8.ValidString(typed) && utf8.RuneCountInString(typed) <= 16_384
	case []interface{}:
		if len(typed) > 1_000 {
			return false
		}
		for _, item := range typed {
			if !validBoundedArtifactJSONValue(item, depth+1, nodes, maxNodes) {
				return false
			}
		}
		return true
	case map[string]interface{}:
		if len(typed) > 256 {
			return false
		}
		for key, item := range typed {
			if key == "__proto__" || key == "constructor" || key == "prototype" ||
				!utf8.ValidString(key) || utf8.RuneCountInString(key) > 128 ||
				!validBoundedArtifactJSONValue(item, depth+1, nodes, maxNodes) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func validArtifactResultRevision(value string) bool {
	return value == "" || (value == strings.TrimSpace(value) && len(value) <= 128 &&
		!strings.ContainsAny(value, "\x00\r\n"))
}

func validArtifactResultMessage(value string) bool {
	return value == "" || (value == strings.TrimSpace(value) && utf8.RuneCountInString(value) <= 2_000 &&
		!strings.ContainsAny(value, "\x00\r\n"))
}

func hashArtifactResultRequest(request artifactResultSubmitRequest, target artifactWritebackTarget) [sha256.Size]byte {
	canonical, _ := json.Marshal(struct {
		ActorUID              int64           `json:"actor_uid"`
		TopicID               string          `json:"topic_id"`
		AgentUID              int64           `json:"agent_uid"`
		ArtifactID            string          `json:"artifact_id"`
		DisplayedVersion      int64           `json:"displayed_version"`
		SinkID                string          `json:"sink_id"`
		ResultID              string          `json:"result_id"`
		ExpectedStateRevision string          `json:"expected_state_revision,omitempty"`
		Payload               json.RawMessage `json:"payload"`
	}{
		ActorUID:              target.ActorUID,
		TopicID:               target.TopicID,
		AgentUID:              target.AgentUID,
		ArtifactID:            request.ArtifactID,
		DisplayedVersion:      request.DisplayedVersion,
		SinkID:                request.SinkID,
		ResultID:              request.ResultID,
		ExpectedStateRevision: request.ExpectedStateRevision,
		Payload:               request.Payload,
	})
	return sha256.Sum256(canonical)
}

func writeArtifactResultDelivery(w http.ResponseWriter, statusCode int, resultID string, outcome artifactResultDeliveryOutcome) {
	response := map[string]interface{}{
		"contract_version": artifactResultDeliveryContract,
		"status":           outcome.Status,
	}
	if resultID != "" {
		response["result_id"] = resultID
	}
	if outcome.Code != "" {
		response["code"] = outcome.Code
	}
	if outcome.Message != "" {
		response["message"] = outcome.Message
	}
	if len(outcome.ApplicationReceipt) > 0 {
		response["application_receipt"] = outcome.ApplicationReceipt
	}
	writeJSON(w, statusCode, response)
}
