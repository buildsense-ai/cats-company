package server

import (
	"errors"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type serviceBotPromptWriter interface {
	SaveMessageWithMetadata(string, int64, string, []types.ContentBlock, string, string, string, int64, string, map[string]interface{}) (int64, bool, error)
}

// Service prompts are another message ingress. A configured artifact URL must
// undergo the same source-use check and atomic binding as HTTP/WebSocket sends.
func saveServiceBotPrompt(db serviceBotPromptWriter, topic string, uid int64, prompt string, metadata map[string]interface{}) (int64, bool, error) {
	if len(MessageFileResources(&types.Message{Content: prompt, MsgType: "text"})) == 0 {
		return db.SaveMessageWithMetadata(topic, uid, prompt, nil, "", "", "text", 0, "", metadata)
	}
	messages, ok := db.(store.MessageStore)
	if !ok {
		return 0, false, errors.New("file message persistence unavailable")
	}
	saved, err := saveNormalizedMessage(messages, topic, uid, 0, &normalizedMessagePayload{StoredContent: prompt, StoredType: "text", Metadata: metadata})
	if err != nil {
		return 0, false, err
	}
	return saved.ID, saved.Duplicate, nil
}
