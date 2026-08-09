package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	taskStatusType             = "task_status"
	maxTaskRunIDLength         = 128
	maxTaskSummaryLength       = 500
	maxTaskErrorLength         = 2000
	defaultActiveTaskStatusTTL = 6 * time.Hour
)

var allowedTaskStatusStates = map[string]bool{
	"idle":      true,
	"running":   true,
	"completed": true,
	"failed":    true,
	"cancelled": true,
	"stale":     true,
	"waiting":   true,
}

func isTaskStatusPayload(payload *normalizedMessagePayload) bool {
	return payload != nil && payload.ExplicitDisplayType && payload.DisplayType == taskStatusType
}

func canPublishTaskStatus(accountType types.AccountType) bool {
	return accountType == types.AccountBot || accountType == types.AccountService
}

func (h *MessageHandler) handleTaskStatus(uid int64, topicID string, payload *normalizedMessagePayload) (*types.ConversationTaskStatus, error) {
	status, sourceStatus, err := persistConversationTaskStatus(h.db, uid, topicID, payload)
	if err != nil {
		return nil, err
	}
	if h != nil && h.hub != nil {
		h.hub.observeGroupAgentTaskStatus(sourceStatus)
		h.hub.observeAgentPushTaskStatus(sourceStatus)
		h.hub.fanoutConversationTaskStatus(uid, status, nil)
	}
	return status, nil
}

func (h *Hub) handleTaskStatusPub(client *Client, msg *MsgClientPub, topicID string, payload *normalizedMessagePayload) {
	if h == nil || client == nil {
		return
	}
	if !canPublishTaskStatus(client.accountType) {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topicID, Code: 403, Text: "task_status requires a bot or service account"},
		})
		return
	}
	status, sourceStatus, err := persistConversationTaskStatus(h.db, client.uid, topicID, payload)
	if err != nil {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topicID, Code: 400, Text: err.Error()},
		})
		return
	}
	h.SendToClient(client, taskStatusAck(msg.ID, topicID, status))
	h.observeGroupAgentTaskStatus(sourceStatus)
	h.observeAgentPushTaskStatus(sourceStatus)
	h.fanoutConversationTaskStatus(client.uid, status, client)
}

func persistConversationTaskStatus(db store.Store, uid int64, topicID string, payload *normalizedMessagePayload) (*types.ConversationTaskStatus, *types.ConversationTaskStatus, error) {
	statusStore, ok := db.(store.ConversationTaskStatusStore)
	if !ok {
		return nil, nil, errors.New("conversation task status store unavailable")
	}
	status, err := normalizeConversationTaskStatus(uid, topicID, payload)
	if err != nil {
		return nil, nil, err
	}
	if !isGroupTopic(topicID) && db != nil {
		if err := db.CreateTopic(topicID, "p2p", uid); err != nil {
			return nil, nil, fmt.Errorf("ensure task status topic: %w", err)
		}
	}
	aggregate, err := statusStore.UpsertConversationTaskStatus(status)
	if err != nil {
		return nil, nil, err
	}
	return aggregate, status, nil
}

func isTerminalTaskStatus(state string) bool {
	return types.IsTerminalConversationTaskState(state)
}

func normalizeConversationTaskStatus(uid int64, topicID string, payload *normalizedMessagePayload) (*types.ConversationTaskStatus, error) {
	if strings.TrimSpace(topicID) == "" {
		return nil, errors.New("topic_id required")
	}
	body := taskStatusBody(payload)
	state := normalizeTaskStatusState(firstTaskStatusString(body, payload.Metadata, "state", "status"))
	if state == "" {
		return nil, errors.New("task_status state required")
	}
	if !allowedTaskStatusStates[state] {
		return nil, fmt.Errorf("unsupported task_status state %q", state)
	}

	now := time.Now().UTC()
	updatedAt := firstTaskStatusTime(body, payload.Metadata, "updated_at", "updatedAt")
	if updatedAt == nil {
		updatedAt = &now
	}
	expiresAt := firstTaskStatusTime(body, payload.Metadata, "expires_at", "expiresAt")
	if expiresAt == nil && (state == "running" || state == "waiting") {
		defaultExpiry := now.Add(defaultActiveTaskStatusTTL)
		expiresAt = &defaultExpiry
	}
	status := &types.ConversationTaskStatus{
		TopicID:   topicID,
		RunID:     truncateUTF8(firstTaskStatusString(body, payload.Metadata, "run_id", "runId", "run"), maxTaskRunIDLength),
		State:     state,
		Summary:   truncateUTF8(firstTaskStatusString(body, payload.Metadata, "summary", "text", "message"), maxTaskSummaryLength),
		Error:     truncateUTF8(firstTaskStatusString(body, payload.Metadata, "error", "error_message", "errorMessage"), maxTaskErrorLength),
		SourceUID: uid,
		UpdatedAt: *updatedAt,
		ExpiresAt: expiresAt,
	}
	return status, nil
}

func taskStatusBody(payload *normalizedMessagePayload) map[string]interface{} {
	if payload == nil {
		return nil
	}
	switch value := payload.DisplayContent.(type) {
	case map[string]interface{}:
		if nested, ok := value["payload"].(map[string]interface{}); ok {
			return nested
		}
		return value
	case string:
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(value), &parsed); err == nil {
			if nested, ok := parsed["payload"].(map[string]interface{}); ok {
				return nested
			}
			return parsed
		}
	}
	return nil
}

func normalizeTaskStatusState(value string) string {
	value = strings.ToLower(strings.TrimSpace(stripMessageNullBytes(value)))
	switch value {
	case "done", "complete", "success", "succeeded":
		return "completed"
	case "error", "errored", "failure":
		return "failed"
	case "canceled", "cancelled", "stopped":
		return "cancelled"
	case "timeout", "timed_out", "stuck":
		return "stale"
	case "in_progress", "processing", "working":
		return "running"
	default:
		return value
	}
}

func firstTaskStatusString(body map[string]interface{}, metadata map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringFromMap(body, key); value != "" {
			return value
		}
	}
	for _, key := range keys {
		if value := stringFromMap(metadata, key); value != "" {
			return value
		}
	}
	return ""
}

func firstTaskStatusTime(body map[string]interface{}, metadata map[string]interface{}, keys ...string) *time.Time {
	for _, key := range keys {
		if value := timeFromMap(body, key); value != nil {
			return value
		}
	}
	for _, key := range keys {
		if value := timeFromMap(metadata, key); value != nil {
			return value
		}
	}
	return nil
}

func stringFromMap(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	switch value := values[key].(type) {
	case string:
		return strings.TrimSpace(stripMessageNullBytes(value))
	case fmt.Stringer:
		return strings.TrimSpace(stripMessageNullBytes(value.String()))
	}
	return ""
}

func timeFromMap(values map[string]interface{}, key string) *time.Time {
	if values == nil {
		return nil
	}
	switch value := values[key].(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	case time.Time:
		parsed := value.UTC()
		return &parsed
	}
	return nil
}

func truncateUTF8(value string, maxRunes int) string {
	if maxRunes <= 0 || value == "" || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

func taskStatusAck(id, topicID string, status *types.ConversationTaskStatus) *ServerMessage {
	return &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:    id,
			Topic: topicID,
			Code:  200,
			Text:  "ok",
			Params: map[string]interface{}{
				"seq":         0,
				"task_status": status,
			},
		},
	}
}

func (h *Hub) fanoutConversationTaskStatus(sourceUID int64, status *types.ConversationTaskStatus, exclude *Client) {
	if h == nil || status == nil {
		return
	}
	msg := &ServerMessage{TaskStatus: status}
	if isGroupTopic(status.TopicID) {
		groupID := extractGroupID(status.TopicID)
		if groupID == 0 {
			return
		}
		// Channel-managed groups keep human members out of internal bot traffic.
		// Status events follow the same visibility policy as regular messages.
		if !h.isChannelManagedGroup(groupID) {
			h.SendToUserExcept(sourceUID, msg, exclude)
		}
		h.broadcastToGroup(groupID, msg, sourceUID)
		return
	}
	peerUID := extractPeerUID(status.TopicID, sourceUID)
	if peerUID == 0 {
		return
	}
	h.SendToUserExcept(sourceUID, msg, exclude)
	h.SendToUser(peerUID, msg)
}

func (h *Hub) observeAgentPushTaskStatus(status *types.ConversationTaskStatus) {
	if h == nil || h.agentPush == nil || status == nil {
		return
	}
	h.agentPush.observeStatus(status)
}

func (h *Hub) scheduleDisconnectedBotTaskRecovery(sourceUID int64, disconnectedAt time.Time) {
	if h == nil || sourceUID <= 0 {
		return
	}
	grace := h.taskGrace
	if grace < 0 {
		grace = 0
	}
	generation := h.botConnectionEpoch(sourceUID)
	time.AfterFunc(grace, func() {
		h.recoverDisconnectedBotTasksIfSameGeneration(sourceUID, disconnectedAt, generation)
	})
}

// botConnectionEpoch returns the current connection generation for a bot.
//
// With a generation store the value is cluster-wide (persisted and bumped in
// the shared database), so a reconnect on another node bumps the same counter
// this node reads and an old timer can never recover a fresh generation. The
// per-process map is only a fallback for stores without generation support.
func (h *Hub) botConnectionEpoch(uid int64) uint64 {
	if h == nil {
		return 0
	}
	if genStore, ok := h.db.(store.ConversationTaskGenerationStore); ok {
		if generation, err := genStore.BotConnectionGeneration(uid); err == nil {
			return generation
		}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.botConnectionEpochs[uid]
}

// recoverDisconnectedBotTasksIfSameGeneration recovers disconnected bot tasks
// only when the bot's connection generation still matches the one observed when
// the recovery timer was scheduled. A bot that reconnects (and thus bumps the
// generation) and disconnects again must not have its freshly-reconnected work
// marked stale by an older timer with a stale disconnectedAt timestamp.
func (h *Hub) recoverDisconnectedBotTasksIfSameGeneration(sourceUID int64, disconnectedAt time.Time, generation uint64) {
	if h == nil || h.botConnectionEpoch(sourceUID) != generation {
		return
	}
	h.recoverDisconnectedBotTasks(sourceUID, disconnectedAt, generation)
}

func (h *Hub) recoverDisconnectedBotTasks(sourceUID int64, disconnectedAt time.Time, generation uint64) {
	// Local clients and a cluster-wide lease held by another node both mean the
	// bot is still reachable; only recover when it is offline everywhere.
	if h == nil || h.IsOnline(sourceUID) || h.botOnlineElsewhere(sourceUID) {
		return
	}
	recoveryStore, ok := h.db.(store.ConversationTaskStatusRecoveryStore)
	if !ok {
		return
	}

	statuses, err := recoveryStore.ListActiveConversationTaskStatusesForSource(sourceUID, disconnectedAt)
	if err != nil {
		log.Printf("task status recovery: list failed for uid=%d: %v", sourceUID, err)
		return
	}
	for _, candidate := range statuses {
		recovered, updated, err := recoveryStore.MarkConversationTaskStatusStaleIfUnchanged(
			candidate.TopicID, sourceUID, candidate.RunID, disconnectedAt, generation)
		if err != nil {
			log.Printf("task status recovery: persist failed for uid=%d topic=%s: %v", sourceUID, candidate.TopicID, err)
			continue
		}
		if !updated {
			// A concurrent reconnect or a newer run already won the race; do not fanout.
			continue
		}
		h.observeGroupAgentTaskStatus(recovered)
		h.fanoutConversationTaskStatus(sourceUID, recovered, nil)
		log.Printf("task status recovery: marked stale uid=%d topic=%s run=%s", sourceUID, candidate.TopicID, candidate.RunID)
	}
}

// runConversationTaskReaper periodically re-scans durable active task rows and
// recovers any that have outlived the grace period while their bot is offline.
//
// It is the durable complement to the per-disconnect time.AfterFunc: a process
// crash/restart or a transient List/CAS error can lose the one-shot timer, and
// without this reaper the row would stay running/waiting until expiry. The
// reaper reuses the same generation fence and compare-and-set, so it is safe
// to run on multiple nodes: only one CAS wins per row, and a bot that
// reconnected (bumping generation) or updated its run after the cutoff is
// never marked stale.
func (h *Hub) runConversationTaskReaper() {
	if h == nil {
		return
	}
	interval := h.taskReaperInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	grace := h.taskGrace
	if grace < 0 {
		grace = 0
	}

	// Startup sweep: catch rows whose disconnect timer was lost in a previous
	// process before the first tick.
	h.recoverStaleDisconnectedBotTasks(time.Now().Add(-grace))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		h.recoverStaleDisconnectedBotTasks(time.Now().Add(-grace))
	}
}

// recoverStaleDisconnectedBotTasks marks stale every active source run that was
// last updated before the cutoff and whose bot is offline everywhere. Each row
// is fenced on the bot's current cluster-wide generation, so a reconnect on any
// node (which bumps the generation) makes the CAS a no-op. Errors are logged
// and retried on the next sweep, matching the reaper's durable-retry contract.
func (h *Hub) recoverStaleDisconnectedBotTasks(cutoff time.Time) {
	if h == nil || h.db == nil {
		return
	}
	recoveryStore, ok := h.db.(store.ConversationTaskStatusRecoveryStore)
	if !ok {
		return
	}

	statuses, err := recoveryStore.ListAllActiveConversationTaskStatusesBefore(cutoff)
	if err != nil {
		log.Printf("task status reaper: list failed: %v", err)
		return
	}
	for _, candidate := range statuses {
		if candidate.SourceUID <= 0 {
			continue
		}
		// Local clients and a cluster-wide lease held by another node both mean
		// the bot is still reachable; only recover when it is offline everywhere.
		if h.IsOnline(candidate.SourceUID) || h.botOnlineElsewhere(candidate.SourceUID) {
			continue
		}
		// Use the bot's current generation: a reconnect on any node bumps it and
		// makes the CAS a no-op, so a fresh connection's work is never recovered.
		generation := h.botConnectionEpoch(candidate.SourceUID)
		recovered, updated, err := recoveryStore.MarkConversationTaskStatusStaleIfUnchanged(
			candidate.TopicID, candidate.SourceUID, candidate.RunID, cutoff, generation)
		if err != nil {
			log.Printf("task status reaper: persist failed for uid=%d topic=%s: %v", candidate.SourceUID, candidate.TopicID, err)
			continue
		}
		if !updated {
			// A concurrent reconnect or a newer run already won the race; do not fanout.
			continue
		}
		h.observeGroupAgentTaskStatus(recovered)
		h.fanoutConversationTaskStatus(candidate.SourceUID, recovered, nil)
		log.Printf("task status reaper: marked stale uid=%d topic=%s run=%s", candidate.SourceUID, candidate.TopicID, candidate.RunID)
	}
}
