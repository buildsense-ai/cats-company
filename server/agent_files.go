package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultAgentFilesLimit = 40
	maxAgentFilesLimit     = 100
)

type agentFileMessageStore interface {
	ListAgentFileMessages(agentUID int64, topicID string, beforeID int64, limit int) ([]*types.Message, error)
}

type agentFileMessageCursorStore interface {
	ListAgentFileMessagesWithCursor(agentUID int64, topicID string, beforeID int64, beforeCreatedAt time.Time, limit int) ([]*types.Message, error)
}

type topicFileMessageStore interface {
	ListTopicFileMessages(topicID string, beforeID int64, limit int) ([]*types.Message, error)
}

type topicFileMessageCursorStore interface {
	ListTopicFileMessagesWithCursor(topicID string, beforeID int64, beforeCreatedAt time.Time, limit int) ([]*types.Message, error)
}

func listAgentFileMessages(db interface{}, agentUID int64, topicID string, beforeID int64, beforeCreatedAt time.Time, limit int) ([]*types.Message, bool, error) {
	if cursorStore, ok := db.(agentFileMessageCursorStore); ok {
		messages, err := cursorStore.ListAgentFileMessagesWithCursor(agentUID, topicID, beforeID, beforeCreatedAt, limit)
		return messages, true, err
	}
	// Keep focused/legacy stores working; SQL adapters take the cursor-aware
	// path above, while older stores continue to use their ID-only contract.
	if legacyStore, ok := db.(agentFileMessageStore); ok {
		messages, err := legacyStore.ListAgentFileMessages(agentUID, topicID, beforeID, limit)
		return messages, true, err
	}
	return nil, false, nil
}

func listTopicFileMessages(db interface{}, topicID string, beforeID int64, beforeCreatedAt time.Time, limit int) ([]*types.Message, bool, error) {
	if cursorStore, ok := db.(topicFileMessageCursorStore); ok {
		messages, err := cursorStore.ListTopicFileMessagesWithCursor(topicID, beforeID, beforeCreatedAt, limit)
		return messages, true, err
	}
	// Keep focused/legacy stores working; SQL adapters take the cursor-aware
	// path above, while older stores continue to use their ID-only contract.
	if legacyStore, ok := db.(topicFileMessageStore); ok {
		messages, err := legacyStore.ListTopicFileMessages(topicID, beforeID, limit)
		return messages, true, err
	}
	return nil, false, nil
}

type agentFileRecord struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	FileKey    string    `json:"file_key,omitempty"`
	Thumbnail  string    `json:"thumbnail,omitempty"`
	Width      int64     `json:"width,omitempty"`
	Height     int64     `json:"height,omitempty"`
	MimeType   string    `json:"mime_type,omitempty"`
	Size       int64     `json:"size,omitempty"`
	MessageID  int64     `json:"message_id"`
	TopicID    string    `json:"topic_id"`
	TopicName  string    `json:"topic_name,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	BlockIndex int       `json:"block_index"`
}

func (h *CloudArtifactHandler) handleAgentFiles(w http.ResponseWriter, r *http.Request, agentUID int64) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent files unavailable"})
		return
	}
	agent, _, status, err := accessibleAgentUser(h.db, viewerUID, agentUID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	topicID := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	if topicID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic_id is required"})
		return
	}
	topicName, status, err := accessibleAgentFileTopic(h.db, viewerUID, agent, topicID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	limit := queryIntInRange(r, "limit", defaultAgentFilesLimit, 1, maxAgentFilesLimit)
	beforeID := queryInt64(r, "before_id")
	beforeCreatedAt := queryTime(r, "before_created_at")
	messages, ok, err := listAgentFileMessages(h.db, agentUID, topicID, beforeID, beforeCreatedAt, limit+1)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "agent files unavailable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load file history"})
		return
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	files := agentFilesFromMessages(messages, map[string]string{topicID: topicName})
	nextBeforeID := int64(0)
	nextBeforeCreatedAt := ""
	if hasMore && len(messages) > 0 {
		lastMessage := messages[len(messages)-1]
		nextBeforeID = lastMessage.ID
		nextBeforeCreatedAt = formatFileCursorTime(lastMessage.CreatedAt)
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_uid":              agentUID,
		"topic_id":               topicID,
		"count":                  len(files),
		"files":                  files,
		"has_more":               hasMore,
		"next_before_id":         nextBeforeID,
		"next_before_created_at": nextBeforeCreatedAt,
	})
}

// HandleTopicFiles lists all file-bearing messages in one conversation, regardless of sender.
func (h *CloudArtifactHandler) HandleTopicFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation files unavailable"})
		return
	}
	topicID, ok := parseTopicFilesAPIPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "conversation not found"})
		return
	}
	topicName, status, err := accessibleTopicFiles(h.db, viewerUID, topicID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	limit := queryIntInRange(r, "limit", defaultAgentFilesLimit, 1, maxAgentFilesLimit)
	beforeID := queryInt64(r, "before_id")
	beforeCreatedAt := queryTime(r, "before_created_at")
	messages, ok, err := listTopicFileMessages(h.db, topicID, beforeID, beforeCreatedAt, limit+1)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "conversation files unavailable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load file history"})
		return
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	files := agentFilesFromMessages(messages, map[string]string{topicID: topicName})
	nextBeforeID := int64(0)
	nextBeforeCreatedAt := ""
	if hasMore && len(messages) > 0 {
		lastMessage := messages[len(messages)-1]
		nextBeforeID = lastMessage.ID
		nextBeforeCreatedAt = formatFileCursorTime(lastMessage.CreatedAt)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"topic_id":               topicID,
		"topic_name":             topicName,
		"count":                  len(files),
		"files":                  files,
		"has_more":               hasMore,
		"next_before_id":         nextBeforeID,
		"next_before_created_at": nextBeforeCreatedAt,
	})
}

func accessibleTopicFiles(db store.Store, viewerUID int64, topicID string) (string, int, error) {
	if strings.HasPrefix(topicID, "p2p_") {
		parts := strings.Split(topicID, "_")
		if len(parts) != 3 {
			return "", http.StatusBadRequest, fmt.Errorf("invalid topic_id")
		}
		firstUID, firstErr := strconv.ParseInt(parts[1], 10, 64)
		secondUID, secondErr := strconv.ParseInt(parts[2], 10, 64)
		if firstErr != nil || secondErr != nil || firstUID <= 0 || secondUID <= 0 || topicID != p2pTopicID(firstUID, secondUID) {
			return "", http.StatusBadRequest, fmt.Errorf("invalid topic_id")
		}
		if viewerUID != firstUID && viewerUID != secondUID {
			return "", http.StatusForbidden, fmt.Errorf("conversation is not accessible")
		}
		peerUID := firstUID
		if peerUID == viewerUID {
			peerUID = secondUID
		}
		topicName := ""
		if peer, userErr := db.GetUser(peerUID); userErr == nil && peer != nil {
			topicName = displayNameOrUsername(peer.DisplayName, peer.Username)
		}
		if titles, ok := db.(conversationTitleStore); ok {
			customTitles, titleErr := titles.GetConversationTitles(viewerUID, []string{topicID})
			if titleErr == nil {
				if title := strings.TrimSpace(customTitles[topicID]); title != "" {
					topicName = title
				}
			}
		}
		return topicName, 0, nil
	}

	if !isGroupTopic(topicID) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid topic_id")
	}
	groupID := extractGroupID(topicID)
	if groupID <= 0 || topicID != "grp_"+strconv.FormatInt(groupID, 10) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid topic_id")
	}
	isMember, err := db.IsGroupMember(groupID, viewerUID)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("failed to check conversation access")
	}
	if !isMember {
		return "", http.StatusForbidden, fmt.Errorf("conversation is not accessible")
	}
	topicName := ""
	if groups, groupErr := db.GetUserGroups(viewerUID); groupErr == nil {
		for _, group := range groups {
			if group != nil && group.ID == groupID {
				topicName = strings.TrimSpace(group.Name)
				break
			}
		}
	}
	return topicName, 0, nil
}

func accessibleAgentFileTopic(db store.Store, viewerUID int64, agent *types.User, topicID string) (string, int, error) {
	privateTopicID := p2pTopicID(viewerUID, agent.ID)
	if strings.HasPrefix(topicID, "p2p_") {
		if topicID != privateTopicID {
			return "", http.StatusForbidden, fmt.Errorf("conversation is not accessible")
		}
		topicName := displayNameOrUsername(agent.DisplayName, agent.Username)
		if titles, ok := db.(conversationTitleStore); ok {
			customTitles, titleErr := titles.GetConversationTitles(viewerUID, []string{topicID})
			if titleErr == nil {
				if title := strings.TrimSpace(customTitles[topicID]); title != "" {
					topicName = title
				}
			}
		}
		return topicName, 0, nil
	}

	if !isGroupTopic(topicID) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid topic_id")
	}
	groupID := extractGroupID(topicID)
	if groupID <= 0 || topicID != "grp_"+strconv.FormatInt(groupID, 10) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid topic_id")
	}
	isMember, err := db.IsGroupMember(groupID, viewerUID)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("failed to check conversation access")
	}
	if !isMember {
		return "", http.StatusForbidden, fmt.Errorf("conversation is not accessible")
	}

	topicName := ""
	if groups, groupErr := db.GetUserGroups(viewerUID); groupErr == nil {
		for _, group := range groups {
			if group != nil && group.ID == groupID {
				topicName = strings.TrimSpace(group.Name)
				break
			}
		}
	}
	return topicName, 0, nil
}

func agentFilesFromMessages(messages []*types.Message, topicNames map[string]string) []agentFileRecord {
	files := make([]agentFileRecord, 0)
	seen := make(map[string]struct{})
	for _, message := range messages {
		if message == nil {
			continue
		}
		attachments := fileAttachmentsFromMessage(message)
		for blockIndex, attachment := range attachments {
			key := strings.TrimSpace(attachment.URL)
			if key == "" {
				key = strings.TrimSpace(attachment.FileKey)
			}
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			attachmentType := strings.ToLower(strings.TrimSpace(attachment.Type))
			if attachmentType != "image" {
				attachmentType = "file"
			}
			files = append(files, agentFileRecord{
				ID:         fmt.Sprintf("%d:%d", message.ID, blockIndex),
				Type:       attachmentType,
				Name:       firstNonEmpty(attachment.Name, channelOutboundFileNameFromURL(attachment.URL), attachment.FileKey, "文件"),
				URL:        attachment.URL,
				FileKey:    attachment.FileKey,
				Thumbnail:  attachment.Thumbnail,
				Width:      attachment.Width,
				Height:     attachment.Height,
				MimeType:   attachment.MimeType,
				Size:       attachment.Size,
				MessageID:  message.ID,
				TopicID:    message.TopicID,
				TopicName:  topicNames[message.TopicID],
				CreatedAt:  message.CreatedAt,
				BlockIndex: blockIndex,
			})
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		left, right := files[i], files[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.MessageID != right.MessageID {
			return left.MessageID > right.MessageID
		}
		return left.BlockIndex < right.BlockIndex
	})
	return files
}

func fileAttachmentsFromMessage(message *types.Message) []channelOutboundAttachment {
	attachments := make([]channelOutboundAttachment, 0)
	for _, block := range message.ContentBlocks {
		kind := strings.ToLower(strings.TrimSpace(block.Type))
		if kind != "file" && kind != "image" {
			continue
		}
		if attachment, ok := channelOutboundAttachmentFromPayloadMap(kind, block.Payload); ok {
			attachments = append(attachments, attachment)
		}
	}
	if len(attachments) > 0 || !isLegacyFileMessageType(message.MsgType) {
		return attachments
	}

	payload := legacyFilePayload(message.Content)
	if attachment, ok := channelOutboundAttachmentFromPayloadMap(legacyFileMessageType(message), payload); ok {
		attachments = append(attachments, attachment)
	}
	return attachments
}

func isLegacyFileMessageType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "file" || value == "image"
}

func legacyFileMessageType(message *types.Message) string {
	if message != nil && strings.EqualFold(strings.TrimSpace(message.MsgType), "image") {
		return "image"
	}
	return "file"
}

func legacyFilePayload(content string) map[string]interface{} {
	var value interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &value); err != nil {
		return nil
	}
	if encoded, ok := value.(string); ok {
		if err := json.Unmarshal([]byte(strings.TrimSpace(encoded)), &value); err != nil {
			return nil
		}
	}
	rich, _ := value.(map[string]interface{})
	if rich == nil {
		return nil
	}
	if payload, ok := rich["payload"].(map[string]interface{}); ok {
		return payload
	}
	return rich
}

func parseAgentFilesAPIPath(value string) (int64, bool) {
	relative := strings.TrimPrefix(value, "/api/agents/")
	if relative == value {
		return 0, false
	}
	parts := strings.Split(strings.Trim(relative, "/"), "/")
	if len(parts) != 2 || parts[1] != "files" {
		return 0, false
	}
	agentUID, err := strconv.ParseInt(parts[0], 10, 64)
	return agentUID, err == nil && agentUID > 0
}

func parseTopicFilesAPIPath(value string) (string, bool) {
	relative := strings.TrimPrefix(value, "/api/topics/")
	if relative == value {
		return "", false
	}
	parts := strings.Split(strings.Trim(relative, "/"), "/")
	if len(parts) != 2 || parts[1] != "files" {
		return "", false
	}
	topicID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(topicID) == "" {
		return "", false
	}
	return topicID, true
}

func queryIntInRange(r *http.Request, key string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil || value < minimum {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func queryInt64(r *http.Request, key string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(key)), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func queryTime(r *http.Request, key string) time.Time {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func formatFileCursorTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
