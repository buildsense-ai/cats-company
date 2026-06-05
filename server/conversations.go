package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// ConversationHandler serves chat-list summaries without per-topic N+1 fetches.
type ConversationHandler struct {
	db  store.Store
	hub *Hub
}

// NewConversationHandler creates a new ConversationHandler.
func NewConversationHandler(db store.Store, hub *Hub) *ConversationHandler {
	return &ConversationHandler{db: db, hub: hub}
}

// HandleList handles GET /api/conversations
func (h *ConversationHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	friends, err := h.db.GetFriends(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get friends"})
		return
	}

	groups, err := h.db.GetUserGroups(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get groups"})
		return
	}

	ownedBots, err := h.db.ListBotsByOwner(uid)
	if err != nil {
		log.Printf("conversations: failed to list owner bots for uid=%d: %v", uid, err)
		ownedBots = nil
	}
	ownerBotUsers := ownerBotUsersFromMaps(ownedBots)

	topicIDs := make([]string, 0, len(friends)+len(groups)+len(ownerBotUsers))
	seenP2P := make(map[int64]struct{})
	ownerConversationBots := make([]*types.User, 0, len(ownerBotUsers))
	for _, friend := range friends {
		seenP2P[friend.ID] = struct{}{}
		topicIDs = append(topicIDs, p2pTopicID(uid, friend.ID))
	}
	for _, bot := range ownerBotUsers {
		if _, ok := seenP2P[bot.ID]; ok {
			continue
		}
		seenP2P[bot.ID] = struct{}{}
		ownerConversationBots = append(ownerConversationBots, bot)
		topicIDs = append(topicIDs, p2pTopicID(uid, bot.ID))
	}
	for _, group := range groups {
		topicIDs = append(topicIDs, "grp_"+formatInt64(group.ID))
	}

	latestByTopic, err := h.db.GetLatestMessagesForTopics(topicIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load latest messages"})
		return
	}

	conversations := make([]*types.ConversationSummary, 0, len(topicIDs))
	for _, friend := range friends {
		topicID := p2pTopicID(uid, friend.ID)
		summary := buildFriendConversationSummary(topicID, friend, latestByTopic[topicID], h.hub)
		conversations = append(conversations, summary)
	}
	for _, bot := range ownerConversationBots {
		topicID := p2pTopicID(uid, bot.ID)
		summary := buildFriendConversationSummary(topicID, bot, latestByTopic[topicID], h.hub)
		conversations = append(conversations, summary)
	}
	for _, group := range groups {
		topicID := "grp_" + formatInt64(group.ID)
		summary := buildGroupConversationSummary(topicID, group, latestByTopic[topicID])
		conversations = append(conversations, summary)
	}

	sort.SliceStable(conversations, conversationLess(conversations))

	writeJSON(w, http.StatusOK, map[string]interface{}{"conversations": conversations})
}

func buildFriendConversationSummary(topicID string, friend *types.User, latest *types.Message, hub *Hub) *types.ConversationSummary {
	summary := &types.ConversationSummary{
		ID:        topicID,
		Name:      displayNameOrUsername(friend.DisplayName, friend.Username),
		IsGroup:   false,
		FriendID:  friend.ID,
		AvatarURL: friend.AvatarURL,
		IsBot:     friend.BotDisclose || friend.AccountType == types.AccountBot,
		IsOnline:  hub != nil && hub.IsOnline(friend.ID),
	}
	applyLatestMessage(summary, latest)
	return summary
}

func ownerBotUsersFromMaps(bots []map[string]interface{}) []*types.User {
	users := make([]*types.User, 0, len(bots))
	for _, bot := range bots {
		uid := mapID(bot["id"])
		if uid <= 0 {
			continue
		}
		username := mapString(bot["username"])
		displayName := mapString(bot["display_name"])
		users = append(users, &types.User{
			ID:          uid,
			Username:    username,
			DisplayName: displayName,
			AvatarURL:   mapString(bot["avatar_url"]),
			AccountType: types.AccountBot,
			BotDisclose: true,
		})
	}
	return users
}

func buildGroupConversationSummary(topicID string, group *types.Group, latest *types.Message) *types.ConversationSummary {
	summary := &types.ConversationSummary{
		ID:        topicID,
		Name:      group.Name,
		IsGroup:   true,
		GroupID:   group.ID,
		AvatarURL: group.AvatarURL,
		HasBot:    group.HasBot,
	}
	applyLatestMessage(summary, latest)
	applyGroupCreatedTime(summary, group)
	return summary
}

func applyGroupCreatedTime(summary *types.ConversationSummary, group *types.Group) {
	if summary == nil || group == nil || group.CreatedAt.IsZero() {
		return
	}
	if summary.LastTime != nil && !group.CreatedAt.After(*summary.LastTime) {
		return
	}
	t := group.CreatedAt
	summary.LastTime = &t
}

func applyLatestMessage(summary *types.ConversationSummary, latest *types.Message) {
	if summary == nil || latest == nil {
		return
	}

	summary.Preview = summarizeConversationMessage(latest)
	summary.LatestSeq = latest.ID
	t := latest.CreatedAt
	summary.LastTime = &t
}

func summarizeConversationMessage(msg *types.Message) string {
	if msg == nil {
		return ""
	}

	switch msg.MsgType {
	case "image":
		return "[图片]"
	case "file":
		if name := richPayloadField(msg.Content, "name"); name != "" {
			return name
		}
		return "[文件]"
	case "card":
		if title := richPayloadField(msg.Content, "title"); title != "" {
			return title
		}
		if text := richPayloadField(msg.Content, "text"); text != "" {
			return text
		}
		return "[卡片]"
	case "link_preview":
		if title := richPayloadField(msg.Content, "title"); title != "" {
			return title
		}
		if url := richPayloadField(msg.Content, "url"); url != "" {
			return url
		}
		return "[链接]"
	default:
		if text := richPayloadField(msg.Content, "text"); text != "" {
			return text
		}
		return msg.Content
	}
}

func richPayloadField(content, field string) string {
	if content == "" {
		return ""
	}

	var rich struct {
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(content), &rich); err != nil {
		return ""
	}
	if rich.Payload == nil {
		return ""
	}
	if value, ok := rich.Payload[field].(string); ok {
		return value
	}
	return ""
}

func displayNameOrUsername(displayName, username string) string {
	if displayName != "" {
		return displayName
	}
	return username
}

// conversationLess returns a sort comparison function for ConversationSummary slices.
// Conversations are sorted by LastTime descending; nil LastTime items sink to the bottom.
func conversationLess(items []*types.ConversationSummary) func(int, int) bool {
	return func(i, j int) bool {
		left := items[i].LastTime
		right := items[j].LastTime
		switch {
		case left == nil && right == nil:
			return items[i].Name < items[j].Name
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			if left.Equal(*right) {
				return conversationTieLess(items[i], items[j])
			}
			return left.After(*right)
		}
	}
}

func conversationTieLess(left, right *types.ConversationSummary) bool {
	if left == nil || right == nil {
		return right != nil
	}
	if left.IsGroup != right.IsGroup {
		return left.IsGroup
	}
	if left.IsGroup {
		return left.GroupID > right.GroupID
	}
	if left.FriendID != right.FriendID {
		return left.FriendID > right.FriendID
	}
	return left.ID > right.ID
}

func formatInt64(v int64) string {
	if v == 0 {
		return "0"
	}

	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + (v % 10))
		v /= 10
	}
	return string(buf[i:])
}
