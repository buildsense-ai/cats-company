package server

import (
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const defaultGroupAgentTurnTTL = 30 * time.Minute

type groupAgentTurn struct {
	initiatorUID int64
	updatedAt    time.Time
}

type groupAgentTurnTracker struct {
	mu    sync.Mutex
	ttl   time.Duration
	turns map[int64]map[int64]groupAgentTurn
}

func newGroupAgentTurnTracker(ttl time.Duration) *groupAgentTurnTracker {
	if ttl <= 0 {
		ttl = defaultGroupAgentTurnTTL
	}
	return &groupAgentTurnTracker{
		ttl:   ttl,
		turns: make(map[int64]map[int64]groupAgentTurn),
	}
}

func (t *groupAgentTurnTracker) record(groupID, botUID, initiatorUID int64) {
	if t == nil || groupID <= 0 || botUID <= 0 || initiatorUID <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(time.Now())
	if t.turns[groupID] == nil {
		t.turns[groupID] = make(map[int64]groupAgentTurn)
	}
	t.turns[groupID][botUID] = groupAgentTurn{
		initiatorUID: initiatorUID,
		updatedAt:    time.Now(),
	}
}

func (t *groupAgentTurnTracker) initiatedBy(groupID, botUID, requesterUID int64) bool {
	if t == nil || groupID <= 0 || botUID <= 0 || requesterUID <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	groupTurns := t.turns[groupID]
	turn, ok := groupTurns[botUID]
	if !ok {
		return false
	}
	if time.Since(turn.updatedAt) > t.ttl {
		delete(groupTurns, botUID)
		if len(groupTurns) == 0 {
			delete(t.turns, groupID)
		}
		return false
	}
	return turn.initiatorUID == requesterUID
}

func (t *groupAgentTurnTracker) pruneExpiredLocked(now time.Time) {
	for groupID, groupTurns := range t.turns {
		for botUID, turn := range groupTurns {
			if now.Sub(turn.updatedAt) > t.ttl {
				delete(groupTurns, botUID)
			}
		}
		if len(groupTurns) == 0 {
			delete(t.turns, groupID)
		}
	}
}

func (t *groupAgentTurnTracker) clear(groupID, botUID int64) {
	if t == nil || groupID <= 0 || botUID <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	groupTurns := t.turns[groupID]
	delete(groupTurns, botUID)
	if len(groupTurns) == 0 {
		delete(t.turns, groupID)
	}
}

func isGroupAgentTurnRequest(msg *ServerMessage) bool {
	if msg == nil || msg.Data == nil || msg.Data.SeqID <= 0 {
		return false
	}
	msgType := strings.TrimSpace(firstNonEmpty(msg.Data.Type, msg.Data.MsgType))
	return msgType == "text" && strings.TrimSpace(normalizeContentText(msg.Data.Content)) != ""
}

func isFinalGroupAgentTurnMessage(msg *ServerMessage) bool {
	if msg == nil || msg.Data == nil || msg.Data.SeqID <= 0 {
		return false
	}
	msgType := strings.TrimSpace(firstNonEmpty(msg.Data.Type, msg.Data.MsgType))
	content := strings.TrimSpace(normalizeContentText(msg.Data.Content))
	return msgType == "text" &&
		content != "" &&
		!strings.HasPrefix(content, "AI文本:") &&
		!strings.HasPrefix(content, "AI文本：")
}

func (h *Hub) authorizeGroupStreamCancel(groupID, requesterUID int64, metadata map[string]interface{}) (int64, []*types.GroupMember, int, string) {
	if h == nil || h.db == nil || groupID <= 0 || requesterUID <= 0 {
		return 0, nil, 500, "cancel authorization unavailable"
	}
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		return 0, nil, 500, "group members unavailable"
	}

	targetBotUID := firstMetadataInt64(metadata, "target_bot_uid", "target_agent_uid")
	var (
		requesterIsMember bool
		requesterIsBot    bool
		targetIsBot       bool
		onlyBotUID        int64
		botCount          int
		memberCount       int
	)
	for _, member := range members {
		if member == nil {
			continue
		}
		memberCount++
		isBot := member.IsBot || h.isBotUser(member.UserID)
		if member.UserID == requesterUID {
			requesterIsMember = true
			requesterIsBot = isBot
		}
		if !isBot {
			continue
		}
		botCount++
		onlyBotUID = member.UserID
		if member.UserID == targetBotUID {
			targetIsBot = true
		}
	}
	if !requesterIsMember {
		return 0, nil, 403, "not a group member"
	}
	if targetBotUID == 0 && memberCount == 2 && botCount == 1 && !requesterIsBot {
		targetBotUID = onlyBotUID
		targetIsBot = true
	}
	if targetBotUID == 0 || !targetIsBot {
		return 0, nil, 403, "target agent required"
	}
	if memberCount == 2 && botCount == 1 && !requesterIsBot {
		return targetBotUID, members, 0, ""
	}
	if !h.groupTurns.initiatedBy(groupID, targetBotUID, requesterUID) {
		return 0, nil, 403, "only the current turn initiator can stop this agent"
	}
	return targetBotUID, members, 0, ""
}

func (h *Hub) fanoutGroupStreamCancel(
	requesterUID int64,
	topicID string,
	streamID string,
	targetBotUID int64,
	metadata map[string]interface{},
	members []*types.GroupMember,
) {
	if h == nil {
		return
	}
	streamMetadata := make(map[string]interface{}, len(metadata)+3)
	for key, value := range metadata {
		streamMetadata[key] = value
	}
	streamMetadata["stream_id"] = streamID
	streamMetadata["stream_event"] = "cancel"
	streamMetadata["target_bot_uid"] = targetBotUID
	message := &ServerMessage{
		Data: &MsgServerData{
			Topic:    topicID,
			From:     formatUID(requesterUID),
			SeqID:    0,
			Content:  "",
			Type:     "stream_cancel",
			MsgType:  "text",
			Metadata: streamMetadata,
			Mode:     "stream",
			Role:     "assistant",
		},
	}
	for _, member := range members {
		if member == nil || member.UserID == requesterUID {
			continue
		}
		isBot := member.IsBot || h.isBotUser(member.UserID)
		if isBot && member.UserID != targetBotUID {
			continue
		}
		h.SendToUser(member.UserID, message)
	}
}
