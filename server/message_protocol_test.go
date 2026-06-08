package server

import (
	"context"
	"encoding/json"
	"errors"
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

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_42", nil)
	forbiddenReq = forbiddenReq.WithContext(context.WithValue(forbiddenReq.Context(), uidKey, int64(99)))
	forbiddenRec := httptest.NewRecorder()
	handler.HandleGetMessages(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusBadRequest {
		t.Fatalf("forbidden get messages status=%d body=%s, want invalid p2p topic", forbiddenRec.Code, forbiddenRec.Body.String())
	}
}

type identityMessageStore struct {
	store.Store
	users        map[int64]*types.User
	groupMembers []*types.GroupMember
	history      []*types.Message
}

func (s *identityMessageStore) GetUser(id int64) (*types.User, error) {
	if user, ok := s.users[id]; ok {
		return user, nil
	}
	return nil, errors.New("user not found")
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
	return s.GetMessages(topicID, limit, offset)
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
