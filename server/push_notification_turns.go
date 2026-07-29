package server

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	agentPushQuietWindow       = 750 * time.Millisecond
	agentPushTurnDedupTTL      = 10 * time.Minute
	maxPendingAgentPushTurns   = 1024
	maxDeliveredAgentPushTurns = 4096
)

type pendingAgentPush struct {
	timer   *time.Timer
	deliver func() bool
	ttl     time.Duration
}

type agentPushTurnCoordinator struct {
	mu        sync.Mutex
	pending   map[string]*pendingAgentPush
	delivered map[string]time.Time
	delay     time.Duration
}

func newAgentPushTurnCoordinator() *agentPushTurnCoordinator {
	return &agentPushTurnCoordinator{
		pending:   make(map[string]*pendingAgentPush),
		delivered: make(map[string]time.Time),
		delay:     agentPushQuietWindow,
	}
}

func (c *agentPushTurnCoordinator) schedule(key string, ttl time.Duration, deliver func() bool) bool {
	if c == nil || strings.TrimSpace(key) == "" || ttl < 0 || deliver == nil {
		return false
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeExpiredLocked(now)

	if expiresAt, ok := c.delivered[key]; ok && now.Before(expiresAt) {
		return false
	}
	if previous := c.pending[key]; previous != nil {
		previous.timer.Stop()
	}
	if len(c.pending) >= maxPendingAgentPushTurns {
		return false
	}

	pending := &pendingAgentPush{deliver: deliver, ttl: ttl}
	pending.timer = time.AfterFunc(c.delay, func() {
		c.fire(key, pending)
	})
	c.pending[key] = pending
	return true
}

func (c *agentPushTurnCoordinator) fire(key string, pending *pendingAgentPush) {
	c.mu.Lock()
	if c.pending[key] != pending {
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	c.removeExpiredLocked(time.Now())
	expiresAt := time.Time{}
	if pending.ttl > 0 {
		if len(c.delivered) >= maxDeliveredAgentPushTurns {
			for deliveredKey := range c.delivered {
				delete(c.delivered, deliveredKey)
				break
			}
		}
		expiresAt = time.Now().Add(pending.ttl)
		c.delivered[key] = expiresAt
	}
	c.mu.Unlock()

	if pending.deliver() {
		return
	}

	if !expiresAt.IsZero() {
		c.mu.Lock()
		if c.delivered[key] == expiresAt {
			delete(c.delivered, key)
		}
		c.mu.Unlock()
	}
}

func (c *agentPushTurnCoordinator) removeExpiredLocked(now time.Time) {
	for key, expiresAt := range c.delivered {
		if !now.Before(expiresAt) {
			delete(c.delivered, key)
		}
	}
}

func agentPushTurnKey(recipientUID, senderUID int64, msg *ServerMessage) (string, time.Duration) {
	if recipientUID <= 0 || senderUID <= 0 || msg == nil || msg.Data == nil {
		return "", 0
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
		return fmt.Sprintf("explicit:%d:%d:%s:%s", recipientUID, senderUID, data.Topic, turnID), agentPushTurnDedupTTL
	}
	return fmt.Sprintf("fallback:%d:%d:%s", recipientUID, senderUID, data.Topic), 0
}
