package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func deliverInboundChannelTextToAgent(db store.Store, hub *Hub, actorUID, agentUID int64, text, clientMsgID, source string, metadata map[string]interface{}) error {
	if actorUID <= 0 || agentUID <= 0 {
		return errors.New("invalid actor or agent")
	}
	agent, err := db.GetUser(agentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		return errors.New("agent unavailable")
	}
	topicID := p2pTopicID(actorUID, agentUID)
	if err := db.CreateTopic(topicID, "p2p", actorUID); err != nil {
		return fmt.Errorf("create agent topic: %w", err)
	}
	rawContent, _ := json.Marshal(text)
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:     topicID,
		ClientMsgID: clientMsgID,
		Content:     rawContent,
		Type:        "text",
		Metadata:    metadata,
	})
	if err != nil {
		return err
	}
	result, err := saveNormalizedMessage(db, topicID, actorUID, 0, payload)
	if err != nil {
		if source == "" {
			source = "channel"
		}
		return fmt.Errorf("save inbound %s message: %w", source, err)
	}
	if !result.Duplicate && hub != nil {
		hub.fanoutNormalizedMessage(actorUID, topicID, 0, payload, result.ID, nil)
	}
	return nil
}
