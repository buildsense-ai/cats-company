package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func deliverInboundChannelTextToAgent(db store.Store, hub *Hub, actorUID, agentUID int64, text, clientMsgID, source string, metadata map[string]interface{}) error {
	return deliverInboundChannelMessageToAgent(db, hub, actorUID, agentUID, text, nil, clientMsgID, source, metadata)
}

func deliverInboundChannelMessageToAgent(db store.Store, hub *Hub, actorUID, agentUID int64, text string, files []uploadPayload, clientMsgID, source string, metadata map[string]interface{}) error {
	if actorUID <= 0 || agentUID <= 0 {
		return errors.New("invalid actor or agent")
	}
	binding, err := resolveDeliverableChannelBinding(db, actorUID, agentUID, metadata)
	if err != nil {
		return err
	}
	agent, err := db.GetUser(agentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		return errors.New("agent unavailable")
	}
	conversationUID := channelBindingConversationActorUID(binding, actorUID)
	if conversationUID <= 0 {
		return errors.New("invalid channel conversation actor")
	}
	metadata = withChannelBindingDeliveryMetadata(metadata, binding)
	topicID := p2pTopicID(conversationUID, agentUID)
	if err := db.CreateTopic(topicID, "p2p", conversationUID); err != nil {
		return fmt.Errorf("create agent topic: %w", err)
	}
	displayText := strings.TrimSpace(text)
	if displayText == "" {
		displayText = channelMediaDisplaySummary(files)
	}
	rawContent, _ := json.Marshal(displayText)
	contentBlocks := channelInboundContentBlocks(text, files)
	messageType := "text"
	if strings.TrimSpace(text) == "" && len(files) == 1 {
		messageType = channelInboundMessageType(files[0])
	}
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:       topicID,
		ClientMsgID:   clientMsgID,
		Content:       rawContent,
		Type:          messageType,
		ContentBlocks: contentBlocks,
		Metadata:      metadata,
	})
	if err != nil {
		return err
	}
	result, err := saveNormalizedMessage(db, topicID, conversationUID, 0, payload)
	if err != nil {
		if source == "" {
			source = "channel"
		}
		return fmt.Errorf("save inbound %s message: %w", source, err)
	}
	if !result.Duplicate && hub != nil {
		hub.recordChannelInboundReplyRoute(topicID, conversationUID, binding)
		hub.fanoutNormalizedMessage(conversationUID, topicID, 0, payload, result.ID, nil)
	}
	return nil
}

func deliverInboundChannelTextToGroup(db store.Store, hub *Hub, canonicalUID int64, binding *types.ChannelGroupBinding, text, clientMsgID, source string, metadata map[string]interface{}) error {
	return deliverInboundChannelMessageToGroup(db, hub, canonicalUID, binding, text, nil, clientMsgID, source, metadata)
}

func recordInboundChannelTextToGroup(db store.Store, hub *Hub, actorUID int64, binding *types.ChannelGroupBinding, text, clientMsgID, source string, metadata map[string]interface{}) error {
	return deliverInboundChannelMessageToGroupWithTrigger(db, hub, actorUID, binding, text, nil, clientMsgID, source, metadata, false, false)
}

func deliverInboundChannelMessageToGroup(db store.Store, hub *Hub, canonicalUID int64, binding *types.ChannelGroupBinding, text string, files []uploadPayload, clientMsgID, source string, metadata map[string]interface{}) error {
	return deliverInboundChannelMessageToGroupWithTrigger(db, hub, canonicalUID, binding, text, files, clientMsgID, source, metadata, true, false)
}

func deliverInboundChannelMessageToGroupWithTrigger(db store.Store, hub *Hub, actorUID int64, binding *types.ChannelGroupBinding, text string, files []uploadPayload, clientMsgID, source string, metadata map[string]interface{}, triggerBots, allowChannelManaged bool) error {
	if actorUID <= 0 || binding == nil || strings.TrimSpace(binding.TopicID) == "" {
		return errors.New("invalid channel group binding")
	}
	if _, err := validateDeliverableChannelGroupBinding(db, binding, allowChannelManaged); err != nil {
		return err
	}
	if err := db.CreateTopic(binding.TopicID, "group", binding.CanonicalUID); err != nil {
		return fmt.Errorf("create group topic: %w", err)
	}
	displayText := strings.TrimSpace(text)
	if displayText == "" {
		displayText = channelMediaDisplaySummary(files)
	}
	rawContent, _ := json.Marshal(displayText)
	contentBlocks := channelInboundContentBlocks(text, files)
	messageType := "text"
	if strings.TrimSpace(text) == "" && len(files) == 1 {
		messageType = channelInboundMessageType(files[0])
	}
	trustedMetadata := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		trustedMetadata[key] = value
	}
	trustedMetadata[channelBindingDeliveryTrustMetadataKey] = channelBindingDeliveryTrustToken{}
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:       binding.TopicID,
		ClientMsgID:   clientMsgID,
		Content:       rawContent,
		Type:          messageType,
		ContentBlocks: contentBlocks,
		Metadata:      trustedMetadata,
	})
	if err != nil {
		return err
	}
	result, err := saveNormalizedMessage(db, binding.TopicID, actorUID, 0, payload)
	if err != nil {
		if source == "" {
			source = "channel"
		}
		return fmt.Errorf("save inbound %s group message: %w", source, err)
	}
	if !result.Duplicate && hub != nil {
		if triggerBots {
			hub.fanoutNormalizedMessage(actorUID, binding.TopicID, 0, payload, result.ID, nil)
		} else {
			hub.fanoutNormalizedGroupMessageToHumans(actorUID, binding.TopicID, payload, result.ID)
		}
	}
	return nil
}

func validateDeliverableChannelGroupBinding(db store.Store, binding *types.ChannelGroupBinding, allowChannelManaged bool) (*types.Group, error) {
	if db == nil || binding == nil {
		return nil, errors.New("channel group binding not available")
	}
	if binding.Status != types.ChannelAgentBindingActive {
		return nil, fmt.Errorf("channel group binding is %s", binding.Status)
	}
	if binding.CanonicalUID <= 0 || binding.GroupID <= 0 || strings.TrimSpace(binding.TopicID) == "" {
		return nil, errors.New("invalid channel group binding scope")
	}
	user, err := db.GetUser(binding.CanonicalUID)
	if err != nil || user == nil || user.AccountType != types.AccountHuman || user.State != 0 {
		return nil, errors.New("channel group binding user is not available")
	}
	group, err := db.GetGroup(binding.GroupID)
	if err != nil || group == nil {
		return nil, errors.New("channel group binding group is not available")
	}
	if group.Kind == types.GroupKindChannelManaged && !allowChannelManaged {
		return nil, errors.New("channel-managed groups cannot be used as mobile group bindings")
	}
	if parseGroupIDFromTopicID(binding.TopicID) != binding.GroupID {
		return nil, errors.New("channel group binding topic mismatch")
	}
	isMember, err := db.IsGroupMember(binding.GroupID, binding.CanonicalUID)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return nil, errors.New("channel group binding user is no longer a group member")
	}
	muted, err := db.IsMemberMuted(binding.GroupID, binding.CanonicalUID)
	if err != nil {
		return nil, err
	}
	if muted {
		return nil, errors.New("channel group binding user is muted")
	}
	return group, nil
}

func channelInboundContentBlocks(text string, files []uploadPayload) []types.ContentBlock {
	blocks := make([]types.ContentBlock, 0, len(files)+1)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, types.ContentBlock{Type: "text", Text: text})
	}
	for _, file := range files {
		blockType := channelInboundContentBlockType(file)
		blocks = append(blocks, types.ContentBlock{
			Type: blockType,
			Payload: map[string]interface{}{
				"file_key":  file.FileKey,
				"url":       file.URL,
				"name":      file.Name,
				"file_name": file.Name,
				"size":      file.Size,
				"mime_type": file.MimeType,
			},
		})
	}
	return blocks
}

func channelInboundContentBlockType(file uploadPayload) string {
	if file.Type == "image" {
		return "image"
	}
	mediaType, _, err := mime.ParseMediaType(file.MimeType)
	if err == nil && strings.HasPrefix(strings.ToLower(mediaType), "audio/") {
		return "audio"
	}
	return "file"
}

func channelInboundMessageType(file uploadPayload) string {
	if channelInboundContentBlockType(file) == "audio" {
		return "voice"
	}
	return file.Type
}
