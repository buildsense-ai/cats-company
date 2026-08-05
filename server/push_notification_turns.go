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
	agentPushFallbackTimeout = 30 * time.Second
	maxTrackedAgentPushTurns = 4096
	maxActiveAgentPushTurns  = 1024
)

type agentPushTurnCoordinator struct {
	mu              sync.Mutex
	delivered       map[string]time.Time
	active          map[string]*activeAgentPushTurn
	fallbackTimeout time.Duration
}

type activeAgentPushTurn struct {
	runID        string
	candidates   map[int64]agentPushCandidate
	expiresAt    time.Time
	hardDeadline time.Time
	timer        *time.Timer
}

type agentPushCandidate struct {
	key     string
	deliver func() bool
}

func newAgentPushTurnCoordinator() *agentPushTurnCoordinator {
	return newAgentPushTurnCoordinatorWithTimeout(agentPushFallbackTimeout)
}

func newAgentPushTurnCoordinatorWithTimeout(fallbackTimeout time.Duration) *agentPushTurnCoordinator {
	if fallbackTimeout <= 0 {
		fallbackTimeout = agentPushFallbackTimeout
	}
	return &agentPushTurnCoordinator{
		delivered:       make(map[string]time.Time),
		active:          make(map[string]*activeAgentPushTurn),
		fallbackTimeout: fallbackTimeout,
	}
}

func agentPushScope(senderUID int64, topicID string) string {
	return fmt.Sprintf("%d:%s", senderUID, strings.TrimSpace(topicID))
}

func (c *agentPushTurnCoordinator) observeStatus(status *types.ConversationTaskStatus) {
	if c == nil || status == nil || status.SourceUID <= 0 || strings.TrimSpace(status.TopicID) == "" || strings.TrimSpace(status.RunID) == "" {
		return
	}
	scope := agentPushScope(status.SourceUID, status.TopicID)
	runID := truncateUTF8(strings.TrimSpace(status.RunID), 128)
	if !isTerminalTaskStatus(status.State) {
		if status.State != "running" && status.State != "waiting" {
			return
		}
		now := time.Now()
		if status.ExpiresAt != nil && !status.ExpiresAt.After(now) {
			return
		}
		expiresAt := now.Add(defaultActiveTaskStatusTTL)
		if status.ExpiresAt != nil && status.ExpiresAt.Before(expiresAt) {
			expiresAt = *status.ExpiresAt
		}

		var abandoned []agentPushCandidate
		c.mu.Lock()
		active := c.active[scope]
		if active == nil {
			if len(c.active) >= maxActiveAgentPushTurns {
				c.mu.Unlock()
				return
			}
			active = &activeAgentPushTurn{runID: runID, candidates: make(map[int64]agentPushCandidate)}
			c.active[scope] = active
		} else if active.runID != runID {
			if active.timer != nil {
				active.timer.Stop()
			}
			abandoned = candidateValues(active.candidates)
			active = &activeAgentPushTurn{runID: runID, candidates: make(map[int64]agentPushCandidate)}
			c.active[scope] = active
		}
		active.expiresAt = expiresAt
		c.resetActiveTimerLocked(scope, active)
		c.mu.Unlock()
		c.deliverCandidates(abandoned)
		return
	}

	c.mu.Lock()
	active := c.active[scope]
	if active == nil || active.runID != runID {
		c.mu.Unlock()
		return
	}
	delete(c.active, scope)
	if active.timer != nil {
		active.timer.Stop()
	}
	candidates := candidateValues(active.candidates)
	c.mu.Unlock()
	c.deliverCandidates(candidates)
}

func (c *agentPushTurnCoordinator) resetActiveTimerLocked(scope string, active *activeAgentPushTurn) {
	if active.timer != nil {
		active.timer.Stop()
	}
	deadline := active.expiresAt
	if !active.hardDeadline.IsZero() && active.hardDeadline.Before(deadline) {
		deadline = active.hardDeadline
	}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	runID := active.runID
	active.timer = time.AfterFunc(delay, func() {
		c.expireActiveTurn(scope, runID)
	})
}

func (c *agentPushTurnCoordinator) expireActiveTurn(scope, runID string) {
	c.mu.Lock()
	active := c.active[scope]
	if active == nil || active.runID != runID {
		c.mu.Unlock()
		return
	}
	deadline := active.expiresAt
	if !active.hardDeadline.IsZero() && active.hardDeadline.Before(deadline) {
		deadline = active.hardDeadline
	}
	if time.Now().Before(deadline) {
		c.mu.Unlock()
		return
	}
	delete(c.active, scope)
	candidates := candidateValues(active.candidates)
	c.mu.Unlock()
	c.deliverCandidates(candidates)
}

func candidateValues(candidates map[int64]agentPushCandidate) []agentPushCandidate {
	values := make([]agentPushCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, candidate)
	}
	return values
}

func (c *agentPushTurnCoordinator) deliverCandidates(candidates []agentPushCandidate) {
	for _, candidate := range candidates {
		c.deliverOnce(candidate.key, candidate.deliver)
	}
}

func (c *agentPushTurnCoordinator) observeVisibleMessage(recipientUID, senderUID int64, msg *ServerMessage, deliver func() bool) bool {
	if c == nil || recipientUID <= 0 || senderUID <= 0 || msg == nil || msg.Data == nil || deliver == nil {
		return false
	}
	scope := agentPushScope(senderUID, msg.Data.Topic)
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.active[scope]
	if active == nil || !time.Now().Before(active.expiresAt) {
		return false
	}
	key := agentPushTurnKey(recipientUID, senderUID, msg)
	if key == "" {
		key = fmt.Sprintf("turn:%d:%d:%s:%s", recipientUID, senderUID, msg.Data.Topic, active.runID)
	}
	active.candidates[recipientUID] = agentPushCandidate{key: key, deliver: deliver}
	if active.hardDeadline.IsZero() {
		active.hardDeadline = time.Now().Add(c.fallbackTimeout)
		c.resetActiveTimerLocked(scope, active)
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
	if expiresAt, ok := c.delivered[key]; ok && now.Before(expiresAt) {
		c.mu.Unlock()
		return false
	}
	if len(c.delivered) >= maxTrackedAgentPushTurns {
		c.mu.Unlock()
		return false
	}
	expiresAt := now.Add(agentPushTurnDedupTTL)
	c.delivered[key] = expiresAt
	c.mu.Unlock()

	if deliver() {
		return true
	}

	c.mu.Lock()
	if c.delivered[key] == expiresAt {
		delete(c.delivered, key)
	}
	c.mu.Unlock()
	return false
}

func (c *agentPushTurnCoordinator) removeExpiredLocked(now time.Time) {
	for key, expiresAt := range c.delivered {
		if !now.Before(expiresAt) {
			delete(c.delivered, key)
		}
	}
}

func agentPushTurnKey(recipientUID, senderUID int64, msg *ServerMessage) string {
	if recipientUID <= 0 || senderUID <= 0 || msg == nil || msg.Data == nil {
		return ""
	}
	data := msg.Data
	turnID := firstMetadataString(
		data.Metadata,
		"turn_id", "turnId",
		"response_id", "responseId",
		"run_id", "runId",
		"stream_id", "streamId",
	)
	if turnID != "" {
		turnID = truncateUTF8(turnID, 128)
		return fmt.Sprintf("turn:%d:%d:%s:%s", recipientUID, senderUID, data.Topic, turnID)
	}
	return ""
}

func isCompletedAgentMessage(msg *ServerMessage) bool {
	if !shouldNotifyOfflineForMessage(msg) {
		return false
	}
	data := msg.Data
	if firstMetadataString(
		data.Metadata,
		"turn_id", "turnId",
		"response_id", "responseId",
		"run_id", "runId",
		"stream_id", "streamId",
	) == "" {
		return false
	}
	return metadataBool(data.Metadata, "turn_complete") ||
		metadataBool(data.Metadata, "turnComplete")
}
