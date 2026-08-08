package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestPubMessageNormalizesLikeHTTPRequest(t *testing.T) {
	cases := []struct {
		name     string
		content  json.RawMessage
		msgType  string
		metadata map[string]interface{}
	}{
		{
			name:    "tool use",
			content: json.RawMessage(`"glob"`),
			msgType: "tool_use",
			metadata: map[string]interface{}{
				"id": "call_1",
				"input": map[string]interface{}{
					"pattern": "**/*.md",
				},
			},
		},
		{
			name:    "image content",
			content: json.RawMessage(`{"type":"image","payload":{"url":"/uploads/a.png","name":"a.png","size":12}}`),
			msgType: "image",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpPayload, err := normalizeMessageRequest(&SendMessageRequest{
				TopicID:  "grp_80",
				Type:     tc.msgType,
				Content:  tc.content,
				Metadata: tc.metadata,
			})
			if err != nil {
				t.Fatalf("normalize HTTP request: %v", err)
			}

			wsReq := messageRequestFromPub(&MsgClientPub{
				Topic:    "grp_80",
				Type:     tc.msgType,
				Content:  tc.content,
				Metadata: tc.metadata,
			})
			wsPayload, err := normalizeMessageRequest(wsReq)
			if err != nil {
				t.Fatalf("normalize WebSocket pub: %v", err)
			}

			if !reflect.DeepEqual(httpPayload, wsPayload) {
				t.Fatalf("payload mismatch\nHTTP: %#v\nWS:   %#v", httpPayload, wsPayload)
			}
		})
	}
}

func TestRuntimePlanMessageIsTransient(t *testing.T) {
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_1_2",
		Type:    "runtime_plan",
		Content: json.RawMessage(`{"revision":1,"steps":[{"text":"检查链路","status":"in_progress"}]}`),
		Metadata: map[string]interface{}{
			"transient": true,
		},
	})
	if err != nil {
		t.Fatalf("normalize runtime plan: %v", err)
	}

	if !isTransientRuntimePayload(payload) {
		t.Fatalf("runtime_plan with transient metadata should not be stored")
	}
	if payload.DisplayType != "runtime_plan" {
		t.Fatalf("DisplayType = %q, want runtime_plan", payload.DisplayType)
	}
}

func TestVideoMessageDoesNotExpandDurableAgentContext(t *testing.T) {
	if isDurableAgentContextMessage(&types.Message{}, "video") {
		t.Fatal("video unexpectedly entered durable agent context")
	}
}

func TestRuntimePlanMessageIsTransientWithoutMetadata(t *testing.T) {
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_1_2",
		Type:    "runtime_plan",
		Content: json.RawMessage(`{"revision":2,"steps":[],"updatedAt":1780295905379}`),
	})
	if err != nil {
		t.Fatalf("normalize empty runtime plan: %v", err)
	}

	if !isTransientRuntimePayload(payload) {
		t.Fatalf("runtime_plan should not be stored even without transient metadata")
	}
	if payload.StoredContent == "" {
		t.Fatalf("StoredContent should keep payload for fanout")
	}
}

func TestAgentExecutionDetailsAreTransient(t *testing.T) {
	for _, messageType := range []string{"thinking", "tool_use", "tool_result"} {
		payload, err := normalizeMessageRequest(&SendMessageRequest{
			TopicID: "grp_80",
			Type:    messageType,
			Content: json.RawMessage(`"debug detail"`),
		})
		if err != nil {
			t.Fatalf("normalize %s: %v", messageType, err)
		}
		if isTransientRuntimePayload(payload) {
			t.Fatalf("%s must remain stored for the CatsCo working view", messageType)
		}
		if !isInternalChannelOutboundPayload(payload) {
			t.Fatalf("%s should remain visible in CatsCo but must not be forwarded to external channels", messageType)
		}
	}
}

func TestLegacyAndBlockOnlyWorkingMessagesStayOffExternalChannels(t *testing.T) {
	cases := []*SendMessageRequest{
		{TopicID: "grp_80", Type: "text", Content: json.RawMessage(`"AI文本: 正在读取文件"`)},
		{TopicID: "grp_80", Type: "debug", Content: json.RawMessage(`"internal status"`)},
		{TopicID: "grp_80", Type: "text", Content: json.RawMessage(`"tool output"`), ContentBlocks: []types.ContentBlock{{Type: "tool_result", Content: "private debug output"}}},
	}
	for _, request := range cases {
		payload, err := normalizeMessageRequest(request)
		if err != nil {
			t.Fatalf("normalize working payload: %v", err)
		}
		if isTransientRuntimePayload(payload) || !isInternalChannelOutboundPayload(payload) {
			t.Fatalf("working payload must stay in CatsCo and off external channels: %#v", payload)
		}
	}
}

func TestContentBlocksKeepAttachmentPayload(t *testing.T) {
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Type:    "text",
		Content: json.RawMessage(`"帮我看这张图"`),
		ContentBlocks: []types.ContentBlock{
			{Type: "text", Text: "帮我看这张图"},
			{
				Type: "image",
				Payload: map[string]interface{}{
					"file_key": "images/a.png",
					"url":      "/uploads/images/a.png",
					"name":     "a.png",
					"size":     float64(12),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	if len(payload.ContentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(payload.ContentBlocks))
	}
	if got := payload.ContentBlocks[1].Payload["url"]; got != "/uploads/images/a.png" {
		t.Fatalf("attachment payload url was not preserved: %#v", got)
	}
}

func TestMessagePayloadStripsNullBytesBeforeStore(t *testing.T) {
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:     "grp_80",
		Type:        "text\x00",
		Content:     json.RawMessage(`"hello\u0000world"`),
		ClientMsgID: "client\x00id",
		Metadata: map[string]interface{}{
			"client_msg_id": "metadata\x00id",
			"nested": map[string]interface{}{
				"text": "meta\x00value",
			},
		},
		ContentBlocks: []types.ContentBlock{
			{Type: "text", Text: "block\x00text"},
			{
				Type: "tool_use",
				ID:   "call\x001",
				Name: "read\x00file",
				Input: map[string]interface{}{
					"path": "a\x00.txt",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	if payload.StoredContent != "helloworld" || payload.DisplayContent != "helloworld" {
		t.Fatalf("content was not sanitized: stored=%q display=%q", payload.StoredContent, payload.DisplayContent)
	}
	if payload.DisplayType != "text" || payload.ClientMsgID != "clientid" {
		t.Fatalf("top-level fields were not sanitized: type=%q client=%q", payload.DisplayType, payload.ClientMsgID)
	}
	if payload.Metadata["nested"].(map[string]interface{})["text"] != "metavalue" {
		t.Fatalf("metadata was not sanitized: %#v", payload.Metadata)
	}
	if payload.ContentBlocks[0].Text != "blocktext" || payload.ContentBlocks[1].ID != "call1" {
		t.Fatalf("content blocks were not sanitized: %#v", payload.ContentBlocks)
	}
	if payload.ContentBlocks[1].Input["path"] != "a.txt" {
		t.Fatalf("content block input was not sanitized: %#v", payload.ContentBlocks[1].Input)
	}
}

func TestClientMessageIDNormalizesFromTopLevelAndMetadata(t *testing.T) {
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:     "p2p_1_2",
		ClientMsgID: " catsco-explicit ",
		Content:     json.RawMessage(`"hello"`),
		Metadata: map[string]interface{}{
			"client_msg_id": "catsco-metadata",
		},
	})
	if err != nil {
		t.Fatalf("normalize explicit client_msg_id: %v", err)
	}
	if payload.ClientMsgID != "catsco-explicit" {
		t.Fatalf("ClientMsgID = %q, want explicit value", payload.ClientMsgID)
	}

	payload, err = normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_1_2",
		Content: json.RawMessage(`"hello"`),
		Metadata: map[string]interface{}{
			"client_msg_id": " catsco-metadata ",
		},
	})
	if err != nil {
		t.Fatalf("normalize metadata client_msg_id: %v", err)
	}
	if payload.ClientMsgID != "catsco-metadata" {
		t.Fatalf("ClientMsgID = %q, want metadata value", payload.ClientMsgID)
	}
}

func TestSaveNormalizedMessageUsesIdempotentStore(t *testing.T) {
	store := &idempotentMessageStore{id: 42, duplicate: true}
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:     "p2p_1_2",
		ClientMsgID: "catsco-1",
		Content:     json.RawMessage(`"hello"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	result, err := saveNormalizedMessage(store, "p2p_1_2", 1, 0, payload)
	if err != nil {
		t.Fatalf("save normalized message: %v", err)
	}
	if result.ID != 42 || !result.Duplicate {
		t.Fatalf("result = %#v, want id=42 duplicate=true", result)
	}
	if store.clientMsgID != "catsco-1" || store.calls != 1 {
		t.Fatalf("idempotent save was not used: store=%#v", store)
	}
}

func TestExtractPeerUIDRequiresSenderInTopic(t *testing.T) {
	if got := extractPeerUID("p2p_1_2", 1); got != 2 {
		t.Fatalf("extractPeerUID for uid 1 = %d, want 2", got)
	}
	if got := extractPeerUID("p2p_1_2", 2); got != 1 {
		t.Fatalf("extractPeerUID for uid 2 = %d, want 1", got)
	}
	if got := extractPeerUID("p2p_1_2", 3); got != 0 {
		t.Fatalf("extractPeerUID for non-member uid = %d, want 0", got)
	}
}

func TestFanoutMessageAddsCanonicalCatscoIdentityForBotRecipient(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		},
	}
	hub := NewHub(store, nil)
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-mac",
		displayName: "Dev Agent Runtime",
		send:        make(chan []byte, 1),
	}
	hub.addClient(botClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_7_42",
		Content: json.RawMessage(`"hello"`),
		Metadata: map[string]interface{}{
			"keep":            "yes",
			"catsco_identity": map[string]interface{}{"spoofed": true},
		},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "p2p_7_42", 0, payload, 15, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	actor := nestedMap(t, identity, "actor")
	agent := nestedMap(t, identity, "agent")
	topic := nestedMap(t, identity, "topic")
	permissions := nestedMap(t, identity, "permissions")

	if msg.Data.Metadata["keep"] != "yes" {
		t.Fatalf("expected original metadata to be preserved: %#v", msg.Data.Metadata)
	}
	if actor["user_id"] != "usr7" || actor["display_name"] != "Alice" {
		t.Fatalf("unexpected actor identity: %#v", actor)
	}
	if actor["account_type"] != string(types.AccountHuman) || actor["is_bot"] != false {
		t.Fatalf("unexpected actor account type: %#v", actor)
	}
	if agent["agent_id"] != "usr42" || agent["body_id"] != "body-mac" || agent["display_name"] != "Dev Agent Runtime" {
		t.Fatalf("unexpected agent identity: %#v", agent)
	}
	if topic["topic_id"] != "p2p_7_42" || topic["type"] != "p2p" || topic["channel_seq"] != float64(15) {
		t.Fatalf("unexpected topic identity: %#v", topic)
	}
	if permissions["source"] != "server_canonical_message" {
		t.Fatalf("unexpected permissions snapshot: %#v", permissions)
	}
}

func TestGroupFanoutAddsRecipientBotIdentity(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "review_agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42},
		},
	}
	hub := NewHub(store, nil)
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-review",
		displayName: "Review Runtime",
		send:        make(chan []byte, 1),
	}
	hub.addClient(botClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"@usr42 请看一下"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 22, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	agent := nestedMap(t, identity, "agent")
	topic := nestedMap(t, identity, "topic")

	if agent["agent_id"] != "usr42" || agent["body_id"] != "body-review" {
		t.Fatalf("unexpected group recipient agent identity: %#v", agent)
	}
	if topic["topic_id"] != "grp_80" || topic["type"] != "group" || topic["channel_seq"] != float64(22) {
		t.Fatalf("unexpected group topic identity: %#v", topic)
	}
}

func TestHistoryMessagesIncludeCanonicalCatscoIdentityForBotRecipient(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		},
		history: []*types.Message{{
			ID:      31,
			TopicID: "p2p_7_42",
			FromUID: 7,
			Content: "missed message",
			MsgType: "text",
		}},
	}
	hub := NewHub(store, nil)
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-mac",
		displayName: "Dev Agent Runtime",
		send:        make(chan []byte, 2),
	}
	hub.addClient(botClient)

	hub.handleGet(botClient, &MsgClientGet{
		ID:    "history-1",
		Topic: "p2p_7_42",
		What:  "history",
		SeqID: 0,
	})

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	agent := nestedMap(t, identity, "agent")
	topic := nestedMap(t, identity, "topic")

	if agent["agent_id"] != "usr42" || agent["body_id"] != "body-mac" {
		t.Fatalf("unexpected history recipient agent identity: %#v", agent)
	}
	if topic["topic_id"] != "p2p_7_42" || topic["type"] != "p2p" || topic["channel_seq"] != float64(31) {
		t.Fatalf("unexpected history topic identity: %#v", topic)
	}

	var ctrl ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &ctrl)
	if ctrl.Ctrl == nil || ctrl.Ctrl.Code != 200 || ctrl.Ctrl.Text != "history complete" {
		t.Fatalf("unexpected history completion ctrl: %#v", ctrl.Ctrl)
	}
}

func TestHandleGetMessagesAuthorizesAndMarksReplayHistory(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		},
		history: []*types.Message{{
			ID:      31,
			TopicID: "p2p_7_42",
			FromUID: 7,
			Content: "missed message",
			MsgType: "text",
		}},
	}
	hub := NewHub(store, nil)
	handler := NewMessageHandler(store, hub)

	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rec := httptest.NewRecorder()
	handler.HandleGetMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get messages status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode get messages response: %v", err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages len=%d, want 1", len(body.Messages))
	}
	metadata, ok := body.Messages[0]["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata = %#v, want object", body.Messages[0]["metadata"])
	}
	identity, ok := metadata["catsco_identity"].(map[string]interface{})
	if !ok {
		t.Fatalf("catsco_identity = %#v, want object", metadata["catsco_identity"])
	}
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok || permissions["replay"] != true || permissions["device_access"] != "non_executable_history" {
		t.Fatalf("unexpected replay permissions: %#v", permissions)
	}
	if _, ok := identity["device_grants"]; ok {
		t.Fatalf("REST history must not reissue grants: %#v", identity["device_grants"])
	}
	if store.getUsersByIDsCalls != 1 {
		t.Fatalf("history identity batch calls=%d, want 1", store.getUsersByIDsCalls)
	}

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_42", nil)
	forbiddenReq = forbiddenReq.WithContext(context.WithValue(forbiddenReq.Context(), uidKey, int64(99)))
	forbiddenRec := httptest.NewRecorder()
	handler.HandleGetMessages(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusBadRequest {
		t.Fatalf("forbidden get messages status=%d body=%s, want invalid p2p topic", forbiddenRec.Code, forbiddenRec.Body.String())
	}
}

func TestHandleGetMessagesBuildsAgentContextForGroupHistory(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
			43: {ID: 43, Username: "other_agent", DisplayName: "Other Agent", AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42},
			{GroupID: 80, UserID: 43},
		},
		history: []*types.Message{
			{ID: 1, TopicID: "grp_80", FromUID: 7, Content: "大家看一下", MsgType: "text"},
			{ID: 2, TopicID: "grp_80", FromUID: 7, Content: "@usr43 只让另一个机器人处理", MsgType: "text", ContentBlocks: []types.ContentBlock{{Type: "text", Text: "@usr43 只让另一个机器人处理", Payload: map[string]interface{}{"mentions": []string{"usr43"}}}}},
			{ID: 3, TopicID: "grp_80", FromUID: 7, Content: "@usr42 请继续", MsgType: "text", ContentBlocks: []types.ContentBlock{{Type: "text", Text: "@usr42 请继续", Payload: map[string]interface{}{"mentions": []string{"usr42"}}}}},
			{ID: 4, TopicID: "grp_80", FromUID: 7, Content: "@所有人 一起处理", MsgType: "text", ContentBlocks: []types.ContentBlock{{Type: "text", Text: "@所有人 一起处理", Payload: map[string]interface{}{"mentions": []string{structuredMentionAllBots}}}}},
			{ID: 5, TopicID: "grp_80", FromUID: 42, Content: "我来处理", MsgType: "text"},
			{ID: 6, TopicID: "grp_80", FromUID: 43, Content: "另一个机器人的回答", MsgType: "text"},
			{
				ID: 7, TopicID: "grp_80", FromUID: 42, Content: "处理中", MsgType: "text",
				ContentBlocks: []types.ContentBlock{{Type: "thinking", Thinking: "处理中"}},
			},
			{
				ID: 8, TopicID: "grp_80", FromUID: 42, Content: "最终回答", MsgType: "text",
				ContentBlocks: []types.ContentBlock{
					{Type: "thinking", Thinking: "内部推理"},
					{Type: "assistant_text", Text: "最终回答"},
				},
			},
		},
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=grp_80&agent_context=1&before_id=9&limit=20", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rec := httptest.NewRecorder()
	handler.HandleGetMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent context status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Messages []map[string]interface{} `json:"messages"`
		AgentUID float64                  `json:"agent_uid"`
		HasMore  bool                     `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode agent context response: %v", err)
	}
	if body.AgentUID != 42 || body.HasMore || len(body.Messages) != 8 {
		t.Fatalf("unexpected agent context envelope: %#v", body)
	}

	wantEligible := []bool{true, false, true, true, true, false, false, false}
	wantRoles := []string{"user", "user", "user", "user", "assistant", "other_agent", "assistant", "assistant"}
	for i, message := range body.Messages {
		if message["context_eligible"] != wantEligible[i] || message["context_role"] != wantRoles[i] {
			t.Fatalf("message %d context=%#v, want eligible=%v role=%s", i, message, wantEligible[i], wantRoles[i])
		}
	}
	if body.Messages[3]["context_reason"] != "group_message_targets_all_agents" {
		t.Fatalf("all-bots context reason=%#v", body.Messages[3]["context_reason"])
	}
	otherAgentMetadata := nestedMap(t, body.Messages[5], "metadata")
	otherAgentIdentity := nestedMap(t, otherAgentMetadata, "catsco_identity")
	otherAgentActor := nestedMap(t, otherAgentIdentity, "actor")
	if otherAgentActor["account_type"] != string(types.AccountBot) || otherAgentActor["is_bot"] != true {
		t.Fatalf("unexpected restored bot actor identity: %#v", otherAgentActor)
	}
}

func TestHandleGetMessagesAgentContextRequiresBotCredentials(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			42: {ID: 42, Username: "dev_agent", AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42},
		},
		history: []*types.Message{
			{ID: 1, TopicID: "grp_80", FromUID: 7, Content: "group history", MsgType: "text"},
		},
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=grp_80&agent_context=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleGetMessages(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent context status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

func TestHandleGetMessagesAgentContextUsesStableBeforeCursor(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice"},
			42: {ID: 42, Username: "dev_agent", AccountType: types.AccountBot},
		},
		history: []*types.Message{
			{ID: 1, TopicID: "p2p_7_42", FromUID: 7, Content: "one", MsgType: "text"},
			{ID: 2, TopicID: "p2p_7_42", FromUID: 42, Content: "two", MsgType: "text"},
			{ID: 3, TopicID: "p2p_7_42", FromUID: 7, Content: "three", MsgType: "text"},
			{ID: 4, TopicID: "p2p_7_42", FromUID: 7, Content: "current", MsgType: "text"},
		},
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_42&agent_context=1&before_id=4&limit=2", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rec := httptest.NewRecorder()
	handler.HandleGetMessages(rec, req)

	var body struct {
		Messages     []map[string]interface{} `json:"messages"`
		HasMore      bool                     `json:"has_more"`
		NextBeforeID float64                  `json:"next_before_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode agent context cursor response: %v", err)
	}
	if !body.HasMore || body.NextBeforeID != 2 || len(body.Messages) != 2 {
		t.Fatalf("unexpected cursor response: %#v", body)
	}
	if body.Messages[0]["id"] != float64(2) || body.Messages[1]["id"] != float64(3) {
		t.Fatalf("messages=%#v, want ids 2,3", body.Messages)
	}
}

func TestHandleGetMessagesUsesAfterSequenceCursor(t *testing.T) {
	history := make([]*types.Message, 0, 401)
	for id := int64(1); id <= 401; id++ {
		history = append(history, &types.Message{ID: id, TopicID: "p2p_7_42", FromUID: 7, Content: fmt.Sprintf("message-%d", id), MsgType: "text"})
	}
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice"},
			42: {ID: 42, Username: "dev_agent", AccountType: types.AccountBot},
		},
		history: history,
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	readPage := func(after int64) struct {
		Messages   []map[string]interface{} `json:"messages"`
		HasMore    bool                     `json:"has_more"`
		NextCursor float64                  `json:"next_cursor"`
	} {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/messages?topic_id=p2p_7_42&after_seq=%d&limit=200", after), nil)
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
		rec := httptest.NewRecorder()
		handler.HandleGetMessages(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("after_seq=%d status=%d body=%s", after, rec.Code, rec.Body.String())
		}
		var body struct {
			Messages   []map[string]interface{} `json:"messages"`
			HasMore    bool                     `json:"has_more"`
			NextCursor float64                  `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode after_seq=%d response: %v", after, err)
		}
		return body
	}

	first := readPage(0)
	if !first.HasMore || first.NextCursor != 200 || len(first.Messages) != 200 {
		t.Fatalf("first after_seq page=%#v", first)
	}
	if first.Messages[0]["id"] != float64(1) || first.Messages[199]["id"] != float64(200) {
		t.Fatalf("first after_seq page is not contiguous from the cursor: %#v .. %#v", first.Messages[0], first.Messages[199])
	}
	second := readPage(int64(first.NextCursor))
	if !second.HasMore || second.NextCursor != 400 || len(second.Messages) != 200 {
		t.Fatalf("second after_seq page=%#v", second)
	}
	if second.Messages[0]["id"] != float64(201) || second.Messages[199]["id"] != float64(400) {
		t.Fatalf("second after_seq page skipped or duplicated messages: %#v .. %#v", second.Messages[0], second.Messages[199])
	}
	third := readPage(int64(second.NextCursor))
	if third.HasMore || third.NextCursor != 401 || len(third.Messages) != 1 || third.Messages[0]["id"] != float64(401) {
		t.Fatalf("third after_seq page=%#v", third)
	}
}

func TestHandleGetMessagesRejectsInvalidAfterSequenceCursor(t *testing.T) {
	store := &identityMessageStore{
		users:   map[int64]*types.User{7: {ID: 7, Username: "alice"}, 42: {ID: 42, Username: "dev_agent", AccountType: types.AccountBot}},
		history: []*types.Message{{ID: 1, TopicID: "p2p_7_42", FromUID: 7, Content: "one", MsgType: "text"}},
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	for _, target := range []string{
		"/api/messages?topic_id=p2p_7_42&after_seq=-1",
		"/api/messages?topic_id=p2p_7_42&after_seq=not-a-number",
		"/api/messages?topic_id=p2p_7_42&after_seq=0&after_seq=1",
		"/api/messages?topic_id=p2p_7_42&after_seq=0&latest=1",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
		rec := httptest.NewRecorder()
		handler.HandleGetMessages(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("target=%s status=%d body=%s, want bad request", target, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleGetMessagesUsesStableBeforeCursor(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice"},
			42: {ID: 42, Username: "dev_agent", AccountType: types.AccountBot},
		},
		history: []*types.Message{
			{ID: 1, TopicID: "p2p_7_42", FromUID: 7, Content: "one", MsgType: "text"},
			{ID: 2, TopicID: "p2p_7_42", FromUID: 42, Content: "two", MsgType: "text"},
			{ID: 3, TopicID: "p2p_7_42", FromUID: 7, Content: "three", MsgType: "text"},
			{ID: 4, TopicID: "p2p_7_42", FromUID: 7, Content: "current", MsgType: "text"},
		},
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_42&latest=1&before_id=4&limit=2", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rec := httptest.NewRecorder()
	handler.HandleGetMessages(rec, req)

	var body struct {
		Messages     []map[string]interface{} `json:"messages"`
		HasMore      bool                     `json:"has_more"`
		NextBeforeID float64                  `json:"next_before_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode web history cursor response: %v", err)
	}
	if rec.Code != http.StatusOK || !body.HasMore || body.NextBeforeID != 2 || len(body.Messages) != 2 {
		t.Fatalf("unexpected web history cursor response: status=%d body=%#v", rec.Code, body)
	}
	if body.Messages[0]["id"] != float64(2) || body.Messages[1]["id"] != float64(3) {
		t.Fatalf("messages=%#v, want ids 2,3", body.Messages)
	}
}

type identityMessageStore struct {
	store.Store
	users              map[int64]*types.User
	groupMembers       []*types.GroupMember
	history            []*types.Message
	getUsersByIDsCalls int
}

func (s *identityMessageStore) GetUser(id int64) (*types.User, error) {
	if user, ok := s.users[id]; ok {
		return user, nil
	}
	return nil, errors.New("user not found")
}

func (s *identityMessageStore) GetUsersByIDs(ids []int64) (map[int64]*types.User, error) {
	s.getUsersByIDsCalls++
	users := make(map[int64]*types.User, len(ids))
	for _, id := range ids {
		if user, ok := s.users[id]; ok {
			users[id] = user
		}
	}
	return users, nil
}

func (s *identityMessageStore) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	var members []*types.GroupMember
	for _, member := range s.groupMembers {
		if member.GroupID == groupID {
			members = append(members, member)
		}
	}
	return members, nil
}

func (s *identityMessageStore) GetGroup(groupID int64) (*types.Group, error) {
	return &types.Group{ID: groupID, Kind: types.GroupKindStandard}, nil
}

func (s *identityMessageStore) IsChannelManagedGroup(groupID int64) (bool, error) {
	return false, nil
}

func (s *identityMessageStore) IsGroupMember(groupID, userID int64) (bool, error) {
	for _, member := range s.groupMembers {
		if member.GroupID == groupID && member.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

func (s *identityMessageStore) IsUserBot(userID int64) (bool, error) {
	user, ok := s.users[userID]
	if !ok {
		return false, errors.New("user not found")
	}
	return user.AccountType == types.AccountBot, nil
}

func (s *identityMessageStore) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	var messages []*types.Message
	for _, message := range s.history {
		if message.TopicID == topicID && message.ID > sinceID {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func (s *identityMessageStore) GetMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	var messages []*types.Message
	for _, message := range s.history {
		if message.TopicID == topicID {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func (s *identityMessageStore) GetLatestMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	messages, _ := s.GetMessages(topicID, 0, 0)
	if offset >= len(messages) {
		return nil, nil
	}
	messages = messages[offset:]
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages, nil
}

func (s *identityMessageStore) GetLatestMessagesBefore(topicID string, beforeID int64, limit int) ([]*types.Message, error) {
	var messages []*types.Message
	for _, message := range s.history {
		if message.TopicID == topicID && (beforeID <= 0 || message.ID < beforeID) {
			messages = append(messages, message)
		}
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages, nil
}

func decodeQueuedServerMessage(t *testing.T, ch <-chan []byte, msg *ServerMessage) {
	t.Helper()
	select {
	case raw := <-ch:
		if err := json.Unmarshal(raw, msg); err != nil {
			t.Fatalf("decode server message: %v", err)
		}
	default:
		t.Fatal("expected queued server message")
	}
}

func metadataMapFromServerMessage(t *testing.T, msg *ServerMessage, key string) map[string]interface{} {
	t.Helper()
	if msg.Data == nil {
		t.Fatal("expected data message")
	}
	value, ok := msg.Data.Metadata[key].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata[%s] = %#v, want object", key, msg.Data.Metadata[key])
	}
	return value
}

func nestedMap(t *testing.T, values map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	value, ok := values[key].(map[string]interface{})
	if !ok {
		t.Fatalf("%s = %#v, want object", key, values[key])
	}
	return value
}

type idempotentMessageStore struct {
	id          int64
	duplicate   bool
	calls       int
	clientMsgID string
}

func (s *idempotentMessageStore) CreateTopic(id, topicType string, ownerID int64) error { return nil }
func (s *idempotentMessageStore) SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error) {
	return 0, errors.New("legacy SaveMessage should not be called")
}
func (s *idempotentMessageStore) SaveMessageWithBlocks(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string) (int64, error) {
	return 0, errors.New("legacy SaveMessageWithBlocks should not be called")
}
func (s *idempotentMessageStore) SaveMessageWithReply(topicID string, fromUID int64, content, msgType string, replyTo int64) (int64, error) {
	return 0, errors.New("legacy SaveMessageWithReply should not be called")
}
func (s *idempotentMessageStore) SaveMessageIdempotent(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (int64, bool, error) {
	s.calls++
	s.clientMsgID = clientMsgID
	return s.id, s.duplicate, nil
}
func (s *idempotentMessageStore) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	return nil, nil
}
func (s *idempotentMessageStore) GetMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	return nil, nil
}
func (s *idempotentMessageStore) GetLatestMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	return nil, nil
}
func (s *idempotentMessageStore) GetLatestMessagesForTopics(topicIDs []string) (map[string]*types.Message, error) {
	return nil, nil
}
