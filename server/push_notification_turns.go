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
	mu        sync.Mutex
	delivered map[string]time.Time
	active    map[string]*activeAgentPushTurn
}

type activeAgentPushTurn struct {
	runID      string
	candidates map[int64]func() bool
	expiresAt  time.Time
}

func newAgentPushTurnCoordinator() *agentPushTurnCoordinator {
	return &agentPushTurnCoordinator{
		delivered: make(map[string]time.Time),
		active:    make(map[string]*activeAgentPushTurn),
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
		c.mu.Lock()
		now := time.Now()
		c.removeExpiredActiveLocked(now)
		if status.ExpiresAt != nil && !status.ExpiresAt.After(now) {
			c.mu.Unlock()
			return
		}
		expiresAt := now.Add(defaultActiveTaskStatusTTL)
		if status.ExpiresAt != nil && status.ExpiresAt.After(now) {
			expiresAt = *status.ExpiresAt
			if expiresAt.After(now.Add(defaultActiveTaskStatusTTL)) {
				expiresAt = now.Add(defaultActiveTaskStatusTTL)
			}
		}
		if active := c.active[scope]; active == nil {
			if len(c.active) >= maxActiveAgentPushTurns {
				c.mu.Unlock()
				return
			}
			c.active[scope] = &activeAgentPushTurn{runID: runID, candidates: make(map[int64]func() bool), expiresAt: expiresAt}
		} else if active.runID != runID {
			c.active[scope] = &activeAgentPushTurn{runID: runID, candidates: make(map[int64]func() bool), expiresAt: expiresAt}
		} else {
			active.expiresAt = expiresAt
		}
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.removeExpiredActiveLocked(time.Now())
	active := c.active[scope]
	if active == nil || active.runID != runID {
		c.mu.Unlock()
		return
	}
	delete(c.active, scope)
	candidates := active.candidates
	c.mu.Unlock()

	for recipientUID, deliver := range candidates {
		key := fmt.Sprintf("turn:%d:%d:%s:%s", recipientUID, status.SourceUID, status.TopicID, runID)
		c.deliverOnce(key, deliver)
	}
}

func (c *agentPushTurnCoordinator) removeExpiredActiveLocked(now time.Time) {
	for scope, active := range c.active {
		if active == nil || !now.Before(active.expiresAt) {
			delete(c.active, scope)
		}
	}
}

func (c *agentPushTurnCoordinator) observeVisibleMessage(recipientUID, senderUID int64, msg *ServerMessage, deliver func() bool) bool {
	if c == nil || recipientUID <= 0 || senderUID <= 0 || msg == nil || msg.Data == nil || deliver == nil {
		return false
	}
	scope := agentPushScope(senderUID, msg.Data.Topic)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeExpiredActiveLocked(time.Now())
	active := c.active[scope]
	if active == nil {
		return false
	}
	active.candidates[recipientUID] = deliver
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
