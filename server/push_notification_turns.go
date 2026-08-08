package server

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	agentPushTurnDedupTTL    = 10 * time.Minute
	maxTrackedAgentPushTurns = 4096
	maxActiveAgentPushTurns  = 1024
)

type agentPushTurnCoordinator struct {
	mu            sync.Mutex
	delivered     map[string]time.Time
	inFlight      map[string]struct{}
	turns         map[string]*agentPushTurn
	current       map[string]agentPushCurrentRun
	pending       map[string]*pendingAgentPushMessages
	nextCandidate uint64
}

type agentPushCurrentRun struct {
	runID     string
	updatedAt time.Time
}

type agentPushTurn struct {
	runID      string
	terminal   bool
	updatedAt  time.Time
	candidates map[int64]agentPushCandidate
	expiresAt  time.Time
	timer      *time.Timer
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
		delivered: make(map[string]time.Time),
		inFlight:  make(map[string]struct{}),
		turns:     make(map[string]*agentPushTurn),
		current:   make(map[string]agentPushCurrentRun),
		pending:   make(map[string]*pendingAgentPushMessages),
	}
}

func agentPushScope(senderUID int64, topicID string) string {
	return fmt.Sprintf("%d:%s", senderUID, strings.TrimSpace(topicID))
}

func agentPushTrackedTurnKey(scope, runID string) string {
	return scope + "\x00" + runID
}

func agentPushTurnDeliveryKey(recipientUID int64, scope, runID string) string {
	return fmt.Sprintf("turn:%d:%s:%s", recipientUID, scope, runID)
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
	turnKey := agentPushTrackedTurnKey(scope, runID)

	c.mu.Lock()
	c.removeExpiredLocked(now)
	turn := c.turns[turnKey]
	if turn != nil && turn.terminal && !terminal {
		c.mu.Unlock()
		return
	}
	if turn == nil {
		if len(c.turns) >= maxActiveAgentPushTurns {
			c.mu.Unlock()
			return
		}
		turn = &agentPushTurn{runID: runID, candidates: make(map[int64]agentPushCandidate)}
		c.turns[turnKey] = turn
	}

	if !terminal {
		statusUpdatedAt := status.UpdatedAt
		if !turn.updatedAt.IsZero() && (statusUpdatedAt.IsZero() || statusUpdatedAt.Before(turn.updatedAt)) {
			c.mu.Unlock()
			return
		}
		if !statusUpdatedAt.IsZero() {
			turn.updatedAt = statusUpdatedAt
		}
		orderingTime := statusUpdatedAt
		if orderingTime.IsZero() {
			orderingTime = now
		}
		current := c.current[scope]
		makeCurrent := current.runID == "" || current.runID == runID || !orderingTime.Before(current.updatedAt)
		if makeCurrent {
			c.current[scope] = agentPushCurrentRun{runID: runID, updatedAt: orderingTime}
			c.attachPendingLocked(scope, turn)
		}

		expiresAt := now.Add(defaultActiveTaskStatusTTL)
		if status.ExpiresAt != nil && status.ExpiresAt.Before(expiresAt) {
			expiresAt = *status.ExpiresAt
		}
		turn.expiresAt = expiresAt
		c.resetTurnTimerLocked(scope, turnKey, turn)
		c.mu.Unlock()
		return
	}

	current := c.current[scope]
	if !turn.updatedAt.IsZero() && (status.UpdatedAt.IsZero() || status.UpdatedAt.Before(turn.updatedAt)) {
		c.mu.Unlock()
		return
	}
	if current.runID == runID {
		c.attachPendingLocked(scope, turn)
		delete(c.current, scope)
	}
	if !status.UpdatedAt.IsZero() {
		turn.updatedAt = status.UpdatedAt
	}
	turn.terminal = true
	turn.expiresAt = now.Add(agentPushTurnDedupTTL)
	c.resetTurnTimerLocked(scope, turnKey, turn)
	candidates := candidateValues(turn.candidates)
	c.mu.Unlock()
	c.deliverTurnCandidates(scope, runID, candidates)
}

func (c *agentPushTurnCoordinator) attachPendingLocked(scope string, turn *agentPushTurn) {
	pending := c.pending[scope]
	if pending == nil || turn == nil {
		return
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	for recipientUID, candidate := range pending.candidates {
		turn.candidates[recipientUID] = candidate
	}
	delete(c.pending, scope)
}

func (c *agentPushTurnCoordinator) resetTurnTimerLocked(scope, turnKey string, turn *agentPushTurn) {
	if turn.timer != nil {
		turn.timer.Stop()
	}
	delay := time.Until(turn.expiresAt)
	if delay < 0 {
		delay = 0
	}
	runID := turn.runID
	turn.timer = time.AfterFunc(delay, func() {
		c.expireTurn(scope, turnKey, runID)
	})
}

func (c *agentPushTurnCoordinator) expireTurn(scope, turnKey, runID string) {
	c.mu.Lock()
	turn := c.turns[turnKey]
	if turn == nil || turn.runID != runID || time.Now().Before(turn.expiresAt) {
		c.mu.Unlock()
		return
	}
	c.removeTurnLocked(scope, turnKey, turn)
	c.mu.Unlock()
}

func (c *agentPushTurnCoordinator) removeTurnLocked(scope, turnKey string, turn *agentPushTurn) {
	if turn != nil && turn.timer != nil {
		turn.timer.Stop()
	}
	delete(c.turns, turnKey)
	if current := c.current[scope]; turn != nil && current.runID == turn.runID {
		delete(c.current, scope)
	}
}

func (c *agentPushTurnCoordinator) resetPendingTimerLocked(scope string, pending *pendingAgentPushMessages) {
	if pending.timer != nil {
		pending.timer.Stop()
	}
	delay := time.Until(pending.expiresAt)
	if delay < 0 {
		delay = 0
	}
	pending.timer = time.AfterFunc(delay, func() {
		c.mu.Lock()
		current := c.pending[scope]
		if current == pending && !time.Now().Before(current.expiresAt) {
			delete(c.pending, scope)
		}
		c.mu.Unlock()
	})
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

func (c *agentPushTurnCoordinator) deliverTurnCandidates(scope, runID string, candidates map[int64]agentPushCandidate) {
	turnKey := agentPushTrackedTurnKey(scope, runID)
	for recipientUID, candidate := range candidates {
		deliveryKey := agentPushTurnDeliveryKey(recipientUID, scope, runID)
		c.deliverOnce(deliveryKey, candidate.deliver)

		c.mu.Lock()
		turn := c.turns[turnKey]
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
		runID = c.current[scope].runID
	}
	candidate := c.newCandidateLocked(deliver)
	if runID == "" {
		pending := c.pending[scope]
		if pending == nil {
			if len(c.pending) >= maxActiveAgentPushTurns {
				c.mu.Unlock()
				return true
			}
			pending = &pendingAgentPushMessages{
				candidates: make(map[int64]agentPushCandidate),
				expiresAt:  now.Add(defaultActiveTaskStatusTTL),
			}
			c.pending[scope] = pending
			c.resetPendingTimerLocked(scope, pending)
		}
		pending.candidates[recipientUID] = candidate
		c.mu.Unlock()
		return true
	}

	turnKey := agentPushTrackedTurnKey(scope, runID)
	turn := c.turns[turnKey]
	if turn == nil {
		if len(c.turns) >= maxActiveAgentPushTurns {
			c.mu.Unlock()
			return true
		}
		turn = &agentPushTurn{
			runID:      runID,
			candidates: make(map[int64]agentPushCandidate),
			expiresAt:  now.Add(defaultActiveTaskStatusTTL),
		}
		c.turns[turnKey] = turn
		c.resetTurnTimerLocked(scope, turnKey, turn)
	}
	turn.candidates[recipientUID] = candidate
	terminal := turn.terminal
	c.mu.Unlock()
	if terminal {
		c.deliverTurnCandidates(scope, runID, map[int64]agentPushCandidate{recipientUID: candidate})
	}
	return true
}

func (c *agentPushTurnCoordinator) deliverOnce(key string, deliver func() bool) bool {
	if c == nil || strings.TrimSpace(key) == "" || deliver == nil {
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

func (c *agentPushTurnCoordinator) deliveryRecordedLocked(key string, now time.Time) bool {
	expiresAt, ok := c.delivered[key]
	return ok && now.Before(expiresAt)
}

func (c *agentPushTurnCoordinator) removeExpiredLocked(now time.Time) {
	for key, expiresAt := range c.delivered {
		if !now.Before(expiresAt) {
			delete(c.delivered, key)
		}
	}
	for scope, pending := range c.pending {
		if !now.Before(pending.expiresAt) {
			if pending.timer != nil {
				pending.timer.Stop()
			}
			delete(c.pending, scope)
		}
	}
	for turnKey, turn := range c.turns {
		if !now.Before(turn.expiresAt) {
			scope := strings.SplitN(turnKey, "\x00", 2)[0]
			c.removeTurnLocked(scope, turnKey, turn)
		}
	}
}

func agentPushTurnKey(recipientUID, senderUID int64, msg *ServerMessage) string {
	if recipientUID <= 0 || senderUID <= 0 || msg == nil || msg.Data == nil {
		return ""
	}
	dedupID := agentPushMessageDedupID(msg)
	if dedupID != "" {
		return fmt.Sprintf("turn:%d:%d:%s:%s", recipientUID, senderUID, msg.Data.Topic, dedupID)
	}
	return ""
}

func agentPushMessageDedupID(msg *ServerMessage) string {
	if msg == nil || msg.Data == nil {
		return ""
	}
	dedupID := firstMetadataString(
		msg.Data.Metadata,
		"turn_id", "turnId",
		"response_id", "responseId",
		"run_id", "runId",
		"stream_id", "streamId",
	)
	return truncateUTF8(dedupID, 128)
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

// Message text and metadata are not authoritative completion signals. Task-status
// lifecycle events decide when an automated work unit may notify.
func isCompletedAgentMessage(*ServerMessage) bool {
	return false
}
