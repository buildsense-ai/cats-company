package server

import (
	"context"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// Reuse the message-turn coordinator: a real final answer replaces a generic
// status candidate, and retries/status heartbeats cannot notify twice per run.
func (h *Hub) notifyNativeTaskStatus(status *types.ConversationTaskStatus) {
	if h != nil && h.nativePush != nil {
		h.nativePush.enqueueActivityStatus(status)
	}
	if h == nil || !h.nativePush.Enabled() || h.nativeAgentPush == nil || status == nil || status.RunID == "" || status.SourceUID <= 0 {
		return
	}
	if status.State != "waiting" && status.State != "completed" && status.State != "failed" {
		return
	}
	if status.ExpiresAt != nil && status.State == "waiting" && !status.ExpiresAt.After(time.Now()) {
		return
	}
	if status.State == "waiting" && !h.nativeAgentPush.isCurrentNativeWaiting(status) {
		return
	}
	recipients := []int64{}
	if isGroupTopic(status.TopicID) {
		id := extractGroupID(status.TopicID)
		if h.isChannelManagedGroup(id) {
			return
		}
		members, err := h.db.GetGroupMembers(id)
		if err != nil {
			return
		}
		for _, m := range members {
			if m.UserID != status.SourceUID {
				recipients = append(recipients, m.UserID)
			}
		}
	} else if peer := extractPeerUID(status.TopicID, status.SourceUID); peer > 0 {
		recipients = append(recipients, peer)
	}
	for _, uid := range recipients {
		user, err := h.db.GetUser(uid)
		if err != nil || user == nil || user.AccountType != types.AccountHuman || user.State != 0 {
			continue
		}
		body := "任务已完成，点按查看结果。"
		if status.State == "waiting" {
			body = "任务需要你确认，点按继续。"
		}
		if status.State == "failed" {
			body = "任务遇到问题，点按查看详情。"
		}
		deliver := func(string) bool {
			return h.nativePush.Enqueue(uid, PushNotification{Title: h.pushNotificationTitle(uid, status.TopicID), Body: body, URL: status.TopicID, Tag: "task-" + status.State}, func(ctx context.Context) bool {
				return !h.isConversationNotificationsMuted(ctx, uid, status.TopicID)
			})
		}
		scope := agentPushScope(status.SourceUID, status.TopicID)
		if status.State == "waiting" {
			h.nativeAgentPush.deliverOnce(agentPushTurnDeliveryKey(uid, scope, status.RunID+":waiting"), func() bool { return deliver(body) })
		} else {
			h.nativeAgentPush.addNativeStatusFallback(uid, scope, status.RunID, deliver)
		}
	}
}

func (c *agentPushTurnCoordinator) isCurrentNativeWaiting(status *types.ConversationTaskStatus) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := agentPushScope(status.SourceUID, status.TopicID)
	turn := c.trackedTurns[newAgentPushTrackedTurnKey(scope, status.RunID)]
	return turn != nil && !turn.terminal && !turn.superseded && c.currentRuns[scope].runID == status.RunID
}

func (c *agentPushTurnCoordinator) addNativeStatusFallback(uid int64, scope agentPushScopeKey, runID string, deliver func(string) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := newAgentPushTrackedTurnKey(scope, runID)
	turn := c.trackedTurns[key]
	if turn == nil || !turn.terminal || turn.superseded {
		return
	}
	if _, exists := turn.candidates[uid]; exists {
		return
	}
	if c.deliveryRecordedLocked(agentPushTurnDeliveryKey(uid, scope, runID), time.Now()) {
		return
	}
	// The fallback closure owns its generic body. Keep the candidate text empty
	// so a late real reply replaces the summary instead of appending to it.
	turn.candidates[uid] = c.appendCandidateLocked(agentPushCandidate{}, "", deliver)
	c.resetTerminalDeliveryTimerLocked(key, turn)
}
