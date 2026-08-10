package server

import (
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	agentPushTurnDedupTTL        = 10 * time.Minute
	agentPushTerminalSettleDelay = 750 * time.Millisecond
	maxTrackedAgentPushTurns     = 4096
	maxActiveAgentPushTurns      = 1024
)

type agentPushTurnCoordinator struct {
	mu             sync.Mutex
	delivered      map[agentPushDeliveryKey]time.Time
	inFlight       map[agentPushDeliveryKey]struct{}
	trackedTurns   map[agentPushTrackedTurnKey]*agentPushTurn
	currentRuns    map[agentPushScopeKey]agentPushCurrentRun
	pendingByScope map[agentPushScopeKey]*pendingAgentPushMessages
	nextCandidate  uint64
	settleDelay    time.Duration
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
	runID           string
	terminal        bool
	superseded      bool
	updatedAt       time.Time
	candidates      map[int64]agentPushCandidate
	expiresAt       time.Time
	timer           *time.Timer
	deliveryTimer   *time.Timer
	deliveryTimerID uint64
}

type pendingAgentPushMessages struct {
	candidates map[int64]agentPushCandidate
	expiresAt  time.Time
	timer      *time.Timer
}

type agentPushCandidate struct {
	id      uint64
	deliver func() bool
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

func newHubAgentPushTurnCoordinator() *agentPushTurnCoordinator {
	coordinator := newAgentPushTurnCoordinator()
	coordinator.settleDelay = agentPushTerminalSettleDelay
	return coordinator
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
		turn = &agentPushTurn{runID: runID, candidates: make(map[int64]agentPushCandidate)}
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
	if len(candidates) > 0 && c.settleDelay > 0 {
		c.resetTurnDeliveryTimerLocked(turnKey, turn)
		c.mu.Unlock()
		return
	}
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
	if turn != nil && turn.deliveryTimer != nil {
		turn.deliveryTimer.Stop()
	}
	delete(c.trackedTurns, turnKey)
	if current := c.currentRuns[turnKey.scope]; turn != nil && current.runID == turn.runID {
		delete(c.currentRuns, turnKey.scope)
	}
}

func (c *agentPushTurnCoordinator) resetTurnDeliveryTimerLocked(turnKey agentPushTrackedTurnKey, turn *agentPushTurn) {
	if turn == nil || c.settleDelay <= 0 {
		return
	}
	if turn.deliveryTimer != nil {
		turn.deliveryTimer.Stop()
	}
	runID := turn.runID
	turn.deliveryTimerID++
	timerID := turn.deliveryTimerID
	turn.deliveryTimer = time.AfterFunc(c.settleDelay, func() {
		c.mu.Lock()
		current := c.trackedTurns[turnKey]
		if current == nil || current != turn || current.runID != runID || !current.terminal || current.deliveryTimerID != timerID {
			c.mu.Unlock()
			return
		}
		current.deliveryTimer = nil
		candidates := candidateValues(current.candidates)
		c.mu.Unlock()
		c.deliverTurnCandidates(turnKey.scope, runID, candidates)
	})
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

func (c *agentPushTurnCoordinator) newCandidateLocked(deliver func() bool) agentPushCandidate {
	c.nextCandidate++
	return agentPushCandidate{id: c.nextCandidate, deliver: deliver}
}

func candidateValues(candidates map[int64]agentPushCandidate) map[int64]agentPushCandidate {
	values := make(map[int64]agentPushCandidate, len(candidates))
	for recipientUID, candidate := range candidates {
		values[recipientUID] = candidate
	}
	return values
}

func (c *agentPushTurnCoordinator) deliverTurnCandidates(scope agentPushScopeKey, runID string, candidates map[int64]agentPushCandidate) {
	turnKey := newAgentPushTrackedTurnKey(scope, runID)
	for recipientUID, candidate := range candidates {
		deliveryKey := agentPushTurnDeliveryKey(recipientUID, scope, runID)
		c.deliverOnce(deliveryKey, candidate.deliver)

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
	candidate := c.newCandidateLocked(deliver)
	if runID == "" {
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
		turn = &agentPushTurn{
			runID:      runID,
			candidates: make(map[int64]agentPushCandidate),
			expiresAt:  now.Add(defaultActiveTaskStatusTTL),
		}
		c.trackedTurns[turnKey] = turn
		c.resetTurnTimerLocked(turnKey, turn)
	}
	turn.candidates[recipientUID] = candidate
	terminal := turn.terminal
	if terminal && c.settleDelay > 0 {
		c.resetTurnDeliveryTimerLocked(turnKey, turn)
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	if terminal {
		c.deliverTurnCandidates(scope, runID, map[int64]agentPushCandidate{recipientUID: candidate})
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
