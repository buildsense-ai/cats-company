package server

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	agentPushTurnDedupTTL    = 10 * time.Minute
	maxTrackedAgentPushTurns = 4096
	maxActiveAgentPushTurns  = 1024
	maxAgentPushResponses    = 16
	maxAgentPushSegments     = 256
)

type agentPushTurnCoordinator struct {
	mu             sync.Mutex
	delivered      map[agentPushDeliveryKey]time.Time
	inFlight       map[agentPushDeliveryKey]struct{}
	trackedTurns   map[agentPushTrackedTurnKey]*agentPushTurn
	currentRuns    map[agentPushScopeKey]agentPushCurrentRun
	pendingByScope map[agentPushScopeKey]*pendingAgentPushMessages
	nextCandidate  uint64
}

type agentPushScopeKey struct {
	senderUID int64
	topicID   string
}

type agentPushTrackedTurnKey struct {
	scope agentPushScopeKey
	runID string
}

type agentPushDeliveryKey struct {
	recipientUID int64
	scope        agentPushScopeKey
	runID        string
}

type agentPushCurrentRun struct {
	runID     string
	updatedAt time.Time
}

type agentPushTurn struct {
	runID      string
	terminal   bool
	superseded bool
	updatedAt  time.Time
	candidates map[int64]agentPushCandidate
	responses  map[string]*agentPushResponse
	expiresAt  time.Time
	timer      *time.Timer
}

type pendingAgentPushMessages struct {
	candidates map[int64]agentPushCandidate
	expiresAt  time.Time
	timer      *time.Timer
}

type agentPushCandidate struct {
	id       uint64
	body     string
	response *agentPushResponse
	deliver  func(string) bool
}

type agentPushResponse struct {
	segmentCount int
	segments     map[int]string
}

func (response *agentPushResponse) complete() bool {
	return response != nil && response.segmentCount > 0 && len(response.segments) == response.segmentCount
}

func (response *agentPushResponse) body() string {
	if response == nil || response.segmentCount <= 0 {
		return ""
	}
	segments := make([]string, 0, response.segmentCount)
	for index := 0; index < response.segmentCount; index++ {
		if segment := strings.TrimSpace(response.segments[index]); segment != "" {
			segments = append(segments, segment)
		}
	}
	return strings.Join(segments, " ")
}

func newAgentPushTurnCoordinator() *agentPushTurnCoordinator {
	return &agentPushTurnCoordinator{
		delivered:      make(map[agentPushDeliveryKey]time.Time),
		inFlight:       make(map[agentPushDeliveryKey]struct{}),
		trackedTurns:   make(map[agentPushTrackedTurnKey]*agentPushTurn),
		currentRuns:    make(map[agentPushScopeKey]agentPushCurrentRun),
		pendingByScope: make(map[agentPushScopeKey]*pendingAgentPushMessages),
	}
}

func agentPushScope(senderUID int64, topicID string) agentPushScopeKey {
	return agentPushScopeKey{senderUID: senderUID, topicID: strings.TrimSpace(topicID)}
}

func newAgentPushTrackedTurnKey(scope agentPushScopeKey, runID string) agentPushTrackedTurnKey {
	return agentPushTrackedTurnKey{scope: scope, runID: runID}
}

func agentPushTurnDeliveryKey(recipientUID int64, scope agentPushScopeKey, runID string) agentPushDeliveryKey {
	return agentPushDeliveryKey{recipientUID: recipientUID, scope: scope, runID: runID}
}

func (c *agentPushTurnCoordinator) observeStatus(status *types.ConversationTaskStatus) {
	if c == nil || status == nil || status.SourceUID <= 0 || strings.TrimSpace(status.TopicID) == "" || strings.TrimSpace(status.RunID) == "" {
		return
	}
	state := strings.TrimSpace(status.State)
	terminal := isTerminalTaskStatus(state)
	if state != "running" && state != "waiting" && !terminal {
		return
	}

	now := time.Now()
	if !terminal && status.ExpiresAt != nil && !status.ExpiresAt.After(now) {
		return
	}
	scope := agentPushScope(status.SourceUID, status.TopicID)
	runID := truncateUTF8(strings.TrimSpace(status.RunID), 128)
	turnKey := newAgentPushTrackedTurnKey(scope, runID)

	c.mu.Lock()
	c.removeExpiredLocked(now)
	turn := c.trackedTurns[turnKey]
	if turn != nil && turn.terminal && !terminal {
		c.mu.Unlock()
		return
	}
	if turn == nil {
		if len(c.trackedTurns) >= maxActiveAgentPushTurns {
			c.mu.Unlock()
			return
		}
		turn = newAgentPushTurn(runID)
		c.trackedTurns[turnKey] = turn
	}

	if !terminal {
		statusUpdatedAt := agentPushStatusEventTime(status, now)
		if !turn.updatedAt.IsZero() && (statusUpdatedAt.IsZero() || statusUpdatedAt.Before(turn.updatedAt)) {
			c.mu.Unlock()
			return
		}
		if !statusUpdatedAt.IsZero() {
			turn.updatedAt = statusUpdatedAt
		}
		orderingTime := statusUpdatedAt
		current := c.currentRuns[scope]
		makeCurrent := !turn.superseded && (current.runID == "" || current.runID == runID || !orderingTime.Before(current.updatedAt))
		if makeCurrent {
			if current.runID != "" && current.runID != runID {
				if previous := c.trackedTurns[newAgentPushTrackedTurnKey(scope, current.runID)]; previous != nil {
					previous.superseded = true
				}
			}
			c.currentRuns[scope] = agentPushCurrentRun{runID: runID, updatedAt: orderingTime}
			c.attachPendingLocked(scope, turn)
		}

		expiresAt := now.Add(defaultActiveTaskStatusTTL)
		if status.ExpiresAt != nil && status.ExpiresAt.Before(expiresAt) {
			expiresAt = *status.ExpiresAt
		}
		turn.expiresAt = expiresAt
		c.resetTurnTimerLocked(turnKey, turn)
		c.mu.Unlock()
		return
	}

	current := c.currentRuns[scope]
	statusUpdatedAt := agentPushStatusEventTime(status, now)
	if !turn.updatedAt.IsZero() && statusUpdatedAt.Before(turn.updatedAt) {
		c.mu.Unlock()
		return
	}
	if current.runID == "" || current.runID == runID {
		c.attachPendingLocked(scope, turn)
		c.currentRuns[scope] = agentPushCurrentRun{runID: runID, updatedAt: statusUpdatedAt}
	}
	turn.updatedAt = statusUpdatedAt
	turn.terminal = true
	turn.expiresAt = now.Add(agentPushTurnDedupTTL)
	c.resetTurnTimerLocked(turnKey, turn)
	candidates := candidateValues(turn.candidates)
	c.mu.Unlock()
	c.deliverTurnCandidates(scope, runID, candidates)
}

func agentPushStatusEventTime(status *types.ConversationTaskStatus, fallback time.Time) time.Time {
	if status != nil {
		if !status.EventUpdatedAt.IsZero() {
			return status.EventUpdatedAt
		}
		if !status.UpdatedAt.IsZero() {
			return status.UpdatedAt
		}
	}
	return fallback
}

func (c *agentPushTurnCoordinator) attachPendingLocked(scope agentPushScopeKey, turn *agentPushTurn) {
	pending := c.pendingByScope[scope]
	if pending == nil || turn == nil {
		return
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	for recipientUID, candidate := range pending.candidates {
		turn.candidates[recipientUID] = candidate
	}
	delete(c.pendingByScope, scope)
}

func (c *agentPushTurnCoordinator) resetTurnTimerLocked(turnKey agentPushTrackedTurnKey, turn *agentPushTurn) {
	runID := turn.runID
	resetAgentPushTimer(&turn.timer, turn.expiresAt, func() {
		c.expireTurn(turnKey, runID)
	})
}

func (c *agentPushTurnCoordinator) expireTurn(turnKey agentPushTrackedTurnKey, runID string) {
	c.mu.Lock()
	turn := c.trackedTurns[turnKey]
	if turn == nil || turn.runID != runID || time.Now().Before(turn.expiresAt) {
		c.mu.Unlock()
		return
	}
	c.removeTurnLocked(turnKey, turn)
	c.mu.Unlock()
}

func (c *agentPushTurnCoordinator) removeTurnLocked(turnKey agentPushTrackedTurnKey, turn *agentPushTurn) {
	if turn != nil && turn.timer != nil {
		turn.timer.Stop()
	}
	delete(c.trackedTurns, turnKey)
	if current := c.currentRuns[turnKey.scope]; turn != nil && current.runID == turn.runID {
		delete(c.currentRuns, turnKey.scope)
	}
}

func (c *agentPushTurnCoordinator) resetPendingTimerLocked(scope agentPushScopeKey, pending *pendingAgentPushMessages) {
	resetAgentPushTimer(&pending.timer, pending.expiresAt, func() {
		c.mu.Lock()
		current := c.pendingByScope[scope]
		if current == pending && !time.Now().Before(current.expiresAt) {
			delete(c.pendingByScope, scope)
		}
		c.mu.Unlock()
	})
}

func resetAgentPushTimer(timer **time.Timer, expiresAt time.Time, callback func()) {
	if *timer != nil {
		(*timer).Stop()
	}
	delay := time.Until(expiresAt)
	if delay < 0 {
		delay = 0
	}
	*timer = time.AfterFunc(delay, callback)
}

func newAgentPushTurn(runID string) *agentPushTurn {
	return &agentPushTurn{
		runID:      runID,
		candidates: make(map[int64]agentPushCandidate),
		responses:  make(map[string]*agentPushResponse),
	}
}

func (c *agentPushTurnCoordinator) newCandidateLocked(body string, response *agentPushResponse, deliver func(string) bool) agentPushCandidate {
	c.nextCandidate++
	return agentPushCandidate{id: c.nextCandidate, body: body, response: response, deliver: deliver}
}

func candidateValues(candidates map[int64]agentPushCandidate) map[int64]agentPushCandidate {
	values := make(map[int64]agentPushCandidate, len(candidates))
	for recipientUID, candidate := range candidates {
		values[recipientUID] = snapshotAgentPushCandidate(candidate)
	}
	return values
}

func snapshotAgentPushCandidate(candidate agentPushCandidate) agentPushCandidate {
	if candidate.response != nil && candidate.response.complete() {
		candidate.body = candidate.response.body()
		candidate.response = nil
	}
	return candidate
}

func (c *agentPushTurnCoordinator) deliverTurnCandidates(scope agentPushScopeKey, runID string, candidates map[int64]agentPushCandidate) {
	turnKey := newAgentPushTrackedTurnKey(scope, runID)
	for recipientUID, candidate := range candidates {
		// A non-nil response is an intentionally incomplete snapshot. It stays
		// queued until every declared segment has arrived.
		if candidate.response != nil {
			continue
		}
		deliveryKey := agentPushTurnDeliveryKey(recipientUID, scope, runID)
		c.deliverOnce(deliveryKey, func() bool {
			return candidate.deliver(candidate.body)
		})

		c.mu.Lock()
		turn := c.trackedTurns[turnKey]
		if turn != nil {
			current, ok := turn.candidates[recipientUID]
			if ok && current.id == candidate.id && c.deliveryRecordedLocked(deliveryKey, time.Now()) {
				delete(turn.candidates, recipientUID)
			}
		}
		c.mu.Unlock()
	}
}

func (c *agentPushTurnCoordinator) observeVisibleMessage(recipientUID, senderUID int64, msg *ServerMessage, deliver func() bool) bool {
	if deliver == nil {
		return false
	}
	return c.observeVisibleMessageBody(recipientUID, senderUID, msg, pushNotificationMessageBody(msg), func(string) bool {
		return deliver()
	})
}

func (c *agentPushTurnCoordinator) observeVisibleMessageBody(
	recipientUID, senderUID int64,
	msg *ServerMessage,
	body string,
	deliver func(string) bool,
) bool {
	if c == nil || recipientUID <= 0 || senderUID <= 0 || msg == nil || msg.Data == nil || deliver == nil || !shouldNotifyOfflineForMessage(msg) {
		return false
	}

	now := time.Now()
	scope := agentPushScope(senderUID, msg.Data.Topic)
	runID := agentPushMessageCorrelationID(msg)

	c.mu.Lock()
	c.removeExpiredLocked(now)
	if runID == "" {
		currentRunID := c.currentRuns[scope].runID
		if currentRunID != "" {
			currentTurn := c.trackedTurns[newAgentPushTrackedTurnKey(scope, currentRunID)]
			deliveryKey := agentPushTurnDeliveryKey(recipientUID, scope, currentRunID)
			_, deliveryInFlight := c.inFlight[deliveryKey]
			if currentTurn == nil || !currentTurn.terminal || (!deliveryInFlight && !c.deliveryRecordedLocked(deliveryKey, now)) {
				runID = currentRunID
			}
		}
	}
	responseKind, responseID, segmentIndex, segmentCount, hasResponseEnvelope := agentPushResponseEnvelope(msg)
	if hasResponseEnvelope && responseKind == "progress" {
		c.mu.Unlock()
		return true
	}
	if runID == "" {
		candidate := c.newCandidateLocked(body, nil, deliver)
		pending := c.pendingByScope[scope]
		if pending == nil {
			if len(c.pendingByScope) >= maxActiveAgentPushTurns {
				c.mu.Unlock()
				return true
			}
			pending = &pendingAgentPushMessages{
				candidates: make(map[int64]agentPushCandidate),
				expiresAt:  now.Add(defaultActiveTaskStatusTTL),
			}
			c.pendingByScope[scope] = pending
			c.resetPendingTimerLocked(scope, pending)
		}
		pending.candidates[recipientUID] = candidate
		c.mu.Unlock()
		return true
	}

	turnKey := newAgentPushTrackedTurnKey(scope, runID)
	turn := c.trackedTurns[turnKey]
	if turn == nil {
		if len(c.trackedTurns) >= maxActiveAgentPushTurns {
			c.mu.Unlock()
			return true
		}
		turn = newAgentPushTurn(runID)
		turn.expiresAt = now.Add(defaultActiveTaskStatusTTL)
		c.trackedTurns[turnKey] = turn
		c.resetTurnTimerLocked(turnKey, turn)
	}
	var response *agentPushResponse
	if hasResponseEnvelope && responseKind == "final" {
		response = turn.responses[responseID]
		if response == nil && len(turn.responses) < maxAgentPushResponses {
			response = &agentPushResponse{segmentCount: segmentCount, segments: make(map[int]string)}
			turn.responses[responseID] = response
		}
		if response == nil || response.segmentCount != segmentCount {
			c.mu.Unlock()
			return true
		}
		response.segments[segmentIndex] = body
	}
	candidate := c.newCandidateLocked(body, response, deliver)
	turn.candidates[recipientUID] = candidate
	terminal := turn.terminal
	deliveryCandidate := snapshotAgentPushCandidate(candidate)
	c.mu.Unlock()
	if terminal {
		c.deliverTurnCandidates(scope, runID, map[int64]agentPushCandidate{
			recipientUID: deliveryCandidate,
		})
	}
	return true
}

func (c *agentPushTurnCoordinator) deliverOnce(key agentPushDeliveryKey, deliver func() bool) bool {
	if c == nil || key.recipientUID <= 0 || key.scope.senderUID <= 0 || key.scope.topicID == "" || key.runID == "" || deliver == nil {
		return false
	}

	now := time.Now()
	c.mu.Lock()
	c.removeExpiredLocked(now)
	if c.deliveryRecordedLocked(key, now) {
		c.mu.Unlock()
		return false
	}
	if _, ok := c.inFlight[key]; ok {
		c.mu.Unlock()
		return false
	}
	if len(c.delivered)+len(c.inFlight) >= maxTrackedAgentPushTurns {
		c.mu.Unlock()
		return false
	}
	c.inFlight[key] = struct{}{}
	c.mu.Unlock()

	delivered := deliver()

	c.mu.Lock()
	delete(c.inFlight, key)
	if delivered {
		c.delivered[key] = time.Now().Add(agentPushTurnDedupTTL)
	}
	c.mu.Unlock()
	return delivered
}

func (c *agentPushTurnCoordinator) deliveryRecordedLocked(key agentPushDeliveryKey, now time.Time) bool {
	expiresAt, ok := c.delivered[key]
	return ok && now.Before(expiresAt)
}

func (c *agentPushTurnCoordinator) removeExpiredLocked(now time.Time) {
	for key, expiresAt := range c.delivered {
		if !now.Before(expiresAt) {
			delete(c.delivered, key)
		}
	}
	for scope, pending := range c.pendingByScope {
		if !now.Before(pending.expiresAt) {
			if pending.timer != nil {
				pending.timer.Stop()
			}
			delete(c.pendingByScope, scope)
		}
	}
	for turnKey, turn := range c.trackedTurns {
		if !now.Before(turn.expiresAt) {
			c.removeTurnLocked(turnKey, turn)
		}
	}
}

func agentPushMessageCorrelationID(msg *ServerMessage) string {
	if msg == nil || msg.Data == nil {
		return ""
	}
	correlationID := firstMetadataString(
		msg.Data.Metadata,
		"run_id", "runId",
		"turn_id", "turnId",
	)
	return truncateUTF8(correlationID, 128)
}

func agentPushResponseEnvelope(msg *ServerMessage) (kind, responseID string, segmentIndex, segmentCount int, ok bool) {
	if msg == nil || msg.Data == nil || agentPushMessageCorrelationID(msg) == "" {
		return "", "", 0, 0, false
	}
	metadata := msg.Data.Metadata
	kind = strings.ToLower(firstMetadataString(metadata, "response_kind", "responseKind"))
	if kind != "progress" && kind != "final" {
		return "", "", 0, 0, false
	}
	responseID = truncateUTF8(firstMetadataString(metadata, "response_id", "responseId"), 128)
	if responseID == "" {
		return "", "", 0, 0, false
	}
	segmentIndex, indexOK := agentPushMetadataInteger(metadata, "segment_index", "segmentIndex")
	segmentCount, countOK := agentPushMetadataInteger(metadata, "segment_count", "segmentCount")
	if !indexOK || !countOK || segmentCount <= 0 || segmentCount > maxAgentPushSegments || segmentIndex < 0 || segmentIndex >= segmentCount {
		return "", "", 0, 0, false
	}
	return kind, responseID, segmentIndex, segmentCount, true
}

func agentPushMetadataInteger(metadata map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		value, exists := metadata[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed, true
		case int32:
			return int(typed), true
		case int64:
			return int(typed), int64(int(typed)) == typed
		case float64:
			parsed := int(typed)
			return parsed, float64(parsed) == typed
		case json.Number:
			parsed, err := typed.Int64()
			return int(parsed), err == nil && int64(int(parsed)) == parsed
		}
	}
	return 0, false
}
