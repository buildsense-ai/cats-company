package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// ConversationHandler serves chat-list summaries without per-topic N+1 fetches.
type ConversationHandler struct {
	db  store.Store
	hub *Hub
}

type conversationTitleStore interface {
	GetConversationTitles(ownerID int64, topicIDs []string) (map[string]string, error)
	UpdateConversationTitle(ownerID int64, topicID, title string) (bool, error)
}

type updateConversationTitleRequest struct {
	TopicID string `json:"topic_id"`
	Name    string `json:"name"`
}

// NewConversationHandler creates a new ConversationHandler.
func NewConversationHandler(db store.Store, hub *Hub) *ConversationHandler {
	return &ConversationHandler{db: db, hub: hub}
}

// Handle serves conversation listing and task-title updates.
func (h *ConversationHandler) Handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.HandleList(w, r)
	case http.MethodPatch:
		h.HandleUpdateTitle(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
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
	projectTopics := h.loadProjectTopics(uid)

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
	taskStatusByTopic := h.taskStatusesForTopics(topicIDs)

	conversations := make([]*types.ConversationSummary, 0, len(topicIDs))
	for _, friend := range friends {
		topicID := p2pTopicID(uid, friend.ID)
		summary := buildFriendConversationSummary(topicID, friend, latestByTopic[topicID], h.hub)
		summary.TaskStatus = taskStatusByTopic[topicID]
		applyProjectTopic(summary, projectTopics[topicID])
		conversations = append(conversations, summary)
	}

	for _, bot := range ownerConversationBots {
		topicID := p2pTopicID(uid, bot.ID)
		summary := buildFriendConversationSummary(topicID, bot, latestByTopic[topicID], h.hub)
		summary.TaskStatus = taskStatusByTopic[topicID]
		applyProjectTopic(summary, projectTopics[topicID])
		conversations = append(conversations, summary)
	}
	for _, group := range groups {
		topicID := "grp_" + formatInt64(group.ID)
		summary := buildGroupConversationSummary(topicID, group, latestByTopic[topicID])
		summary.TaskStatus = taskStatusByTopic[topicID]
		applyProjectTopic(summary, projectTopics[topicID])
		conversations = append(conversations, summary)
	}

	if titleDB, ok := h.db.(conversationTitleStore); ok {
		titles, titleErr := titleDB.GetConversationTitles(uid, topicIDs)
		if titleErr != nil {
			log.Printf("conversations: failed to load custom titles for uid=%d: %v", uid, titleErr)
		} else {
			for _, conversation := range conversations {
				if conversation.IsGroup {
					continue
				}
				if title := strings.TrimSpace(titles[conversation.ID]); title != "" {
					conversation.Name = title
				}
			}
		}
	}

	sort.SliceStable(conversations, conversationLess(conversations))

	writeJSON(w, http.StatusOK, map[string]interface{}{"conversations": conversations})
}

func (h *ConversationHandler) taskStatusesForTopics(topicIDs []string) map[string]*types.ConversationTaskStatus {
	if h == nil || h.db == nil || len(topicIDs) == 0 {
		return map[string]*types.ConversationTaskStatus{}
	}
	statusStore, ok := h.db.(store.ConversationTaskStatusStore)
	if !ok {
		return map[string]*types.ConversationTaskStatus{}
	}
	statuses, err := statusStore.GetConversationTaskStatuses(topicIDs)
	if err != nil {
		log.Printf("conversations: failed to load task statuses: %v", err)
		return map[string]*types.ConversationTaskStatus{}
	}
	return statuses
}

func (h *ConversationHandler) loadProjectTopics(uid int64) map[string]*types.ProjectTopic {
	byTopic := make(map[string]*types.ProjectTopic)
	projects, ok := h.db.(store.ProjectTopicStore)
	if !ok {
		return byTopic
	}
	assignments, err := projects.ListProjectTopics(uid)
	if err != nil {
		log.Printf("conversations: failed to list project topics for uid=%d: %v", uid, err)
		return byTopic
	}
	for _, assignment := range assignments {
		if assignment == nil || assignment.TopicID == "" {
			continue
		}
		byTopic[assignment.TopicID] = assignment
	}
	return byTopic
}

func applyProjectTopic(summary *types.ConversationSummary, assignment *types.ProjectTopic) {
	if summary == nil || assignment == nil {
		return
	}
	summary.ProjectID = assignment.ProjectID
	summary.ProjectName = assignment.ProjectName
}

// HandleUpdateTitle changes the current user's custom title for a P2P task.
func (h *ConversationHandler) HandleUpdateTitle(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	var req updateConversationTitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	req.TopicID = strings.TrimSpace(req.TopicID)
	req.Name = strings.TrimSpace(req.Name)
	if !p2pTopicIncludesUID(req.TopicID, uid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task"})
		return
	}
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 80 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task name must be 1 to 80 characters"})
		return
	}

	titleDB, ok := h.db.(conversationTitleStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "task rename is unavailable"})
		return
	}
	updated, err := titleDB.UpdateConversationTitle(uid, req.TopicID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to rename task"})
		return
	}
	if !updated {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       true,
		"topic_id": req.TopicID,
		"name":     req.Name,
	})
}

func p2pTopicIncludesUID(topicID string, uid int64) bool {
	parts := strings.Split(topicID, "_")
	if len(parts) != 3 || parts[0] != "p2p" {
		return false
	}
	first, firstErr := strconv.ParseInt(parts[1], 10, 64)
	second, secondErr := strconv.ParseInt(parts[2], 10, 64)
	return firstErr == nil && secondErr == nil && (first == uid || second == uid)
}

func buildFriendConversationSummary(topicID string, friend *types.User, latest *types.Message, hub *Hub) *types.ConversationSummary {
	isBot := friend.BotDisclose || friend.AccountType == types.AccountBot
	isOnline := hub != nil && hub.IsOnline(friend.ID)
	if isBot {
		isOnline = hub != nil && hub.BotBodyStatus(friend.ID).Active
	}
	summary := &types.ConversationSummary{
		ID:        topicID,
		Name:      displayNameOrUsername(friend.DisplayName, friend.Username),
		IsGroup:   false,
		FriendID:  friend.ID,
		AvatarURL: friend.AvatarURL,
		IsBot:     isBot,
		IsOnline:  isOnline,
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
		ID:          topicID,
		Name:        group.Name,
		IsGroup:     true,
		GroupID:     group.ID,
		AvatarURL:   group.AvatarURL,
		HasBot:      group.HasBot,
		IsAgentTask: group.Kind == types.GroupKindAgentTask,
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
