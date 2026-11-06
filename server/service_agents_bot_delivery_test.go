package server

import (
	"encoding/json"
	"testing"
)

// bot-as-tool 的**跨仓契约**：平台推给 bot 的那条 WS 消息必须同时带两样东西，
// 否则功能会静默失效 —— 而失效了看不出来，只表现为「上下文还是串的」。
//
//  1. `catsco_bot_session_id`：bot 据此算会话身份（一次调用一个会话）。
//     只写进消息表而漏了推送不行：bot 收的是实时推送，不轮询数据库。
//  2. `catsco_identity`（`permissions.source = server_canonical_message`）：
//     bot 侧只在**可信信封**下采纳会话 id。没有它，任何能发消息的人塞一个
//     metadata 就能凭空开任意多会话，把上下文隔离变成 DoS 面。
//
// 这个测试就是在钉住这两条，改任何一边都会红。
func TestDeliverServiceBotPromptCarriesSessionAndTrustedIdentity(t *testing.T) {
	botUID := int64(982)
	callerUID := int64(38)
	topic := "p2p_38_982"
	const msgID = int64(4242)
	const sessionID = "sess_abc123"

	client := &Client{uid: botUID, send: make(chan []byte, 4)}
	hub := &Hub{clients: map[int64]map[*Client]struct{}{botUID: {client: {}}}}

	if ok := hub.DeliverServiceBotPrompt(callerUID, botUID, topic, "查 A", msgID, sessionID); !ok {
		t.Fatal("应该推送成功")
	}

	var message ServerMessage
	select {
	case payload := <-client.send:
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	default:
		t.Fatal("应该收到推送")
	}
	if message.Data == nil {
		t.Fatal("推送缺少 data")
	}
	metadata := message.Data.Metadata
	if metadata == nil {
		t.Fatalf("推送缺少 metadata: %+v", message.Data)
	}

	// 1) 会话 id 随推送到达
	if got, _ := metadata[botSessionMetadataKey].(string); got != sessionID {
		t.Fatalf("metadata[%s] = %q, want %q", botSessionMetadataKey, got, sessionID)
	}

	// 2) 可信信封在场，且 channel_seq 就是这条 prompt 的 id
	//    （bot 侧的 reply_to 要回填它，平台据此认领结果）
	identity, _ := metadata["catsco_identity"].(map[string]interface{})
	if identity == nil {
		t.Fatalf("推送缺少 catsco_identity，bot 侧会拒绝采纳会话 id: %+v", metadata)
	}
	permissions, _ := identity["permissions"].(map[string]interface{})
	if got, _ := permissions["source"].(string); got != "server_canonical_message" {
		t.Fatalf("permissions.source = %q, want server_canonical_message", got)
	}
	topicIdentity, _ := identity["topic"].(map[string]interface{})
	if got, _ := topicIdentity["channel_seq"].(float64); int64(got) != msgID {
		t.Fatalf("topic.channel_seq = %v, want %d（bot 要回填成 reply_to）", topicIdentity["channel_seq"], msgID)
	}

	// 3) 仍然是「有人派活」
	if flag, _ := metadata["catsco_bot_call"].(bool); !flag {
		t.Fatalf("catsco_bot_call 应为 true: %+v", metadata)
	}
}

// 会话 id 为空时不该塞一个空键进去（bot 侧会把空值当「没有会话」，
// 但多一个空键只会让抓包时更难判断）。
func TestDeliverServiceBotPromptOmitsEmptySession(t *testing.T) {
	botUID := int64(982)
	client := &Client{uid: botUID, send: make(chan []byte, 4)}
	hub := &Hub{clients: map[int64]map[*Client]struct{}{botUID: {client: {}}}}

	if ok := hub.DeliverServiceBotPrompt(38, botUID, "p2p_38_982", "hi", 7, "  "); !ok {
		t.Fatal("应该推送成功")
	}
	var message ServerMessage
	if err := json.Unmarshal(<-client.send, &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if message.Data == nil {
		t.Fatal("推送缺少 data")
	}
	metadata := message.Data.Metadata
	if _, exists := metadata[botSessionMetadataKey]; exists {
		t.Fatalf("空白会话 id 不该写进 metadata: %+v", metadata)
	}
}
