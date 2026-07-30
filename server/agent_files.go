package server

import (
	"encoding/json"
	"fmt"
	"net/http"
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

type agentFileRecord struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	FileKey    string    `json:"file_key,omitempty"`
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
	fileDB, ok := h.db.(agentFileMessageStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "agent files unavailable"})
		return
	}

	limit := queryIntInRange(r, "limit", defaultAgentFilesLimit, 1, maxAgentFilesLimit)
	beforeID := queryInt64(r, "before_id")
	messages, err := fileDB.ListAgentFileMessages(agentUID, topicID, beforeID, limit+1)
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
	if hasMore && len(messages) > 0 {
		nextBeforeID = messages[len(messages)-1].ID
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_uid":      agentUID,
		"topic_id":       topicID,
		"count":          len(files),
		"files":          files,
		"has_more":       hasMore,
		"next_before_id": nextBeforeID,
	})
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
			files = append(files, agentFileRecord{
				ID:         fmt.Sprintf("%d:%d", message.ID, blockIndex),
				Name:       firstNonEmpty(attachment.Name, channelOutboundFileNameFromURL(attachment.URL), attachment.FileKey, "文件"),
				URL:        attachment.URL,
				FileKey:    attachment.FileKey,
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
	return files
}

func fileAttachmentsFromMessage(message *types.Message) []channelOutboundAttachment {
	attachments := make([]channelOutboundAttachment, 0)
	for _, block := range message.ContentBlocks {
		if strings.ToLower(strings.TrimSpace(block.Type)) != "file" {
			continue
		}
		if attachment, ok := channelOutboundAttachmentFromPayloadMap("file", block.Payload); ok {
			attachments = append(attachments, attachment)
		}
	}
	if len(attachments) > 0 || message.MsgType != "file" {
		return attachments
	}

	payload := legacyFilePayload(message.Content)
	if attachment, ok := channelOutboundAttachmentFromPayloadMap("file", payload); ok {
		attachments = append(attachments, attachment)
	}
	return attachments
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
