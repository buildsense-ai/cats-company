package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type agentIdentityE2EStore struct {
	store.Store
	mu            sync.Mutex
	nextMessageID int64
	users         map[int64]*types.User
	owners        map[int64]int64
	friendPairs   map[string]bool
	createdTopics []string
	savedMessages []types.Message
}

func (s *agentIdentityE2EStore) GetUser(id int64) (*types.User, error) {
	if user, ok := s.users[id]; ok {
		return user, nil
	}
	return nil, errors.New("user not found")
}

func (s *agentIdentityE2EStore) GetBotOwner(botUID int64) (int64, error) {
	owner, ok := s.owners[botUID]
	if !ok {
		return 0, errors.New("bot owner not found")
	}
	return owner, nil
}

func (s *agentIdentityE2EStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.friendPairs[agentPairKey(uid1, uid2)], nil
}

func (s *agentIdentityE2EStore) CreateTopic(id, topicType string, ownerID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createdTopics = append(s.createdTopics, id)
	return nil
}

func (s *agentIdentityE2EStore) SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextMessageID++
	s.savedMessages = append(s.savedMessages, types.Message{
		ID:      s.nextMessageID,
		TopicID: topicID,
		FromUID: fromUID,
		Content: content,
		MsgType: msgType,
	})
	return s.nextMessageID, nil
}

func (s *agentIdentityE2EStore) SaveMessageWithBlocks(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string) (int64, error) {
	return s.SaveMessage(topicID, fromUID, content, msgType)
}

func (s *agentIdentityE2EStore) SaveMessageWithReply(topicID string, fromUID int64, content, msgType string, replyTo int64) (int64, error) {
	return s.SaveMessage(topicID, fromUID, content, msgType)
}

func (s *agentIdentityE2EStore) SaveMessageIdempotent(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (int64, bool, error) {
	id, err := s.SaveMessage(topicID, fromUID, content, msgType)
	return id, false, err
}

func (s *agentIdentityE2EStore) snapshotSavedMessages() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := make([]types.Message, len(s.savedMessages))
	copy(messages, s.savedMessages)
	return messages
}

func TestTwoActorsMessageSameOnlineAgentWithDistinctCanonicalIdentity(t *testing.T) {
	store := &agentIdentityE2EStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			8:  {ID: 8, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "school-agent", DisplayName: "School Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{
			agentPairKey(7, 43): true,
			agentPairKey(8, 43): true,
		},
	}
	hub := NewHub(store, nil)
	botClient := &Client{
		uid:         43,
		accountType: types.AccountBot,
		bodyID:      "body-cloud-vm",
		displayName: "School Agent Runtime",
		send:        make(chan []byte, 8),
	}
	hub.addClient(botClient)

	agentHandler := NewAgentHandler(store, hub)
	messageHandler := NewMessageHandler(store, hub)

	openAgentForActor(t, agentHandler, 7, 43, "p2p_7_43")
	openAgentForActor(t, agentHandler, 8, 43, "p2p_8_43")

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, actor := range []struct {
		uid     int64
		topicID string
		content string
	}{
		{uid: 7, topicID: "p2p_7_43", content: "alice task"},
		{uid: 8, topicID: "p2p_8_43", content: "bob task"},
	} {
		wg.Add(1)
		go func(actor struct {
			uid     int64
			topicID string
			content string
		}) {
			defer wg.Done()
			if err := sendAgentE2EMessage(messageHandler, actor.uid, actor.topicID, actor.content); err != nil {
				errCh <- err
			}
		}(actor)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	received := map[string]map[string]interface{}{}
	for i := 0; i < 2; i++ {
		var msg ServerMessage
		decodeQueuedServerMessage(t, botClient.send, &msg)
		if msg.Data == nil {
			t.Fatalf("message %d missing data: %+v", i, msg)
		}
		identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
		actor := nestedMap(t, identity, "actor")
		agent := nestedMap(t, identity, "agent")
		topic := nestedMap(t, identity, "topic")
		received[topic["topic_id"].(string)] = map[string]interface{}{
			"actor":   actor,
			"agent":   agent,
			"topic":   topic,
			"data":    msg.Data,
			"content": msg.Data.Content,
		}
	}

	assertAgentIdentityForTopic(t, received, "p2p_7_43", "usr7", "Alice", "alice task")
	assertAgentIdentityForTopic(t, received, "p2p_8_43", "usr8", "Bob", "bob task")
	assertSavedMessage(t, store.snapshotSavedMessages(), "p2p_7_43", 7, "alice task")
	assertSavedMessage(t, store.snapshotSavedMessages(), "p2p_8_43", 8, "bob task")
}

func TestSendMessageRejectsUnavailableAgentTopic(t *testing.T) {
	store := &agentIdentityE2EStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "school-agent", DisplayName: "School Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{},
	}
	handler := NewMessageHandler(store, NewHub(store, nil))
	body := bytes.NewBufferString(`{"topic_id":"p2p_7_43","type":"text","content":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/messages/send", body)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleSendMessage(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if messages := store.snapshotSavedMessages(); len(messages) != 0 {
		t.Fatalf("saved messages = %#v, want none", messages)
	}
}

func openAgentForActor(t *testing.T, handler *AgentHandler, actorUID int64, agentUID int64, wantTopic string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/open", bytes.NewBufferString(`{"agent_uid":`+strconv.FormatInt(agentUID, 10)+`}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, actorUID))
	rec := httptest.NewRecorder()

	handler.HandleOpenAgent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("open agent actor=%d status=%d body=%s", actorUID, rec.Code, rec.Body.String())
	}
	var body struct {
		Agent AgentSummary `json:"agent"`
		Topic string       `json:"topic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode open agent response: %v", err)
	}
	if body.Agent.UID != agentUID || body.Topic != wantTopic || body.Agent.TopicID != wantTopic {
		t.Fatalf("open agent actor=%d response=%+v, want agent=%d topic=%s", actorUID, body, agentUID, wantTopic)
	}
}

func sendAgentE2EMessage(handler *MessageHandler, actorUID int64, topicID string, content string) error {
	body, err := json.Marshal(map[string]interface{}{
		"topic_id": topicID,
		"type":     "text",
		"content":  content,
	})
	if err != nil {
		return fmt.Errorf("marshal message request: %w", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/messages/send", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, actorUID))
	rec := httptest.NewRecorder()

	handler.HandleSendMessage(rec, req)

	if rec.Code != http.StatusOK {
		return fmt.Errorf("send message actor=%d status=%d body=%s", actorUID, rec.Code, rec.Body.String())
	}
	return nil
}

func assertAgentIdentityForTopic(t *testing.T, received map[string]map[string]interface{}, topicID string, actorID string, actorName string, content string) {
	t.Helper()
	item, ok := received[topicID]
	if !ok {
		t.Fatalf("missing received message for topic %s: %#v", topicID, received)
	}
	actor := item["actor"].(map[string]interface{})
	agent := item["agent"].(map[string]interface{})
	topic := item["topic"].(map[string]interface{})
	data := item["data"].(*MsgServerData)
	if data.Topic != topicID || data.From != actorID || data.Content != content {
		t.Fatalf("topic %s data=%#v, want topic=%s from=%s content=%q", topicID, data, topicID, actorID, content)
	}
	if actor["user_id"] != actorID || actor["display_name"] != actorName {
		t.Fatalf("topic %s actor=%#v, want %s/%s", topicID, actor, actorID, actorName)
	}
	if agent["agent_id"] != "usr43" || agent["body_id"] != "body-cloud-vm" || agent["display_name"] != "School Agent Runtime" {
		t.Fatalf("topic %s agent=%#v, want usr43/body-cloud-vm/School Agent Runtime", topicID, agent)
	}
	if topic["type"] != "p2p" || topic["channel_seq"] == nil {
		t.Fatalf("topic %s snapshot=%#v, want p2p with channel_seq", topicID, topic)
	}
}

func assertSavedMessage(t *testing.T, messages []types.Message, topicID string, fromUID int64, content string) {
	t.Helper()
	for _, message := range messages {
		if message.TopicID == topicID && message.FromUID == fromUID && message.Content == content {
			return
		}
	}
	t.Fatalf("missing saved message topic=%s from=%d content=%q in %#v", topicID, fromUID, content, messages)
}
