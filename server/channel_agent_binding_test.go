package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestChannelAgentEntryAndBindingFlow(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.bodyIDs[43] = "body-contract"
	handler := NewChannelAgentBindingHandler(db, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"weixin"}`))
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), uidKey, int64(7)))
	createRec := httptest.NewRecorder()
	handler.HandleAgentEntries(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Entry channelAgentEntryResponse `json:"entry"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Entry.SceneKey == "" || created.Entry.EntryURL == "" || created.Entry.Channel != "weixin" {
		t.Fatalf("unexpected created entry: %+v", created.Entry)
	}

	previewReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-entry/preview?scene_key="+created.Entry.SceneKey, nil)
	previewRec := httptest.NewRecorder()
	handler.HandleAgentEntryPreview(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewRec.Code, previewRec.Body.String())
	}

	confirmBody := `{"scene_key":"` + created.Entry.SceneKey + `","channel_user_id":"openid-7","channel_conversation_type":"p2p"}`
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", bytes.NewBufferString(confirmBody))
	confirmRec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmRec.Code, confirmRec.Body.String())
	}

	resolveReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=weixin&channel_user_id=openid-7", nil)
	resolveRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(resolveRec, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", resolveRec.Code, resolveRec.Body.String())
	}
	var resolved struct {
		Bound       bool   `json:"bound"`
		AgentUID    int64  `json:"agent_uid"`
		AgentID     string `json:"agent_id"`
		AgentBodyID string `json:"agent_body_id"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if !resolved.Bound || resolved.AgentUID != 43 || resolved.AgentID != "usr43" || resolved.AgentBodyID != "body-contract" {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
}

func TestChannelAgentEntryRejectsNonOwner(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 99
	handler := NewChannelAgentBindingHandler(db, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"feishu"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleAgentEntries(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelAgentBindingResolveFallsBackToUserDefault(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelUserID: "ou_user",
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=feishu&channel_user_id=ou_user&channel_conversation_id=oc_group", nil)
	rec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resolved struct {
		Bound    bool  `json:"bound"`
		AgentUID int64 `json:"agent_uid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if !resolved.Bound || resolved.AgentUID != 43 {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
}

func TestChannelAgentBindingUsesEntryChannelAppID(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"feishu","channel_app_id":"cli_app"}`))
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), uidKey, int64(7)))
	createRec := httptest.NewRecorder()
	handler.HandleAgentEntries(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Entry channelAgentEntryResponse `json:"entry"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Entry.ChannelAppID != "cli_app" {
		t.Fatalf("expected entry app id, got %+v", created.Entry)
	}

	confirmBody := `{"scene_key":"` + created.Entry.SceneKey + `","channel":"feishu","channel_user_id":"ou_user","channel_conversation_type":"p2p"}`
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", bytes.NewBufferString(confirmBody))
	confirmRec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmRec.Code, confirmRec.Body.String())
	}
	var confirmed struct {
		Binding types.ChannelAgentBinding `json:"binding"`
	}
	if err := json.Unmarshal(confirmRec.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("decode confirm response: %v", err)
	}
	if confirmed.Binding.ChannelAppID != "cli_app" {
		t.Fatalf("expected binding app id from entry, got %+v", confirmed.Binding)
	}

	resolveReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=feishu&channel_app_id=cli_app&channel_user_id=ou_user", nil)
	resolveRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(resolveRec, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", resolveRec.Code, resolveRec.Body.String())
	}
	var resolved struct {
		Bound    bool  `json:"bound"`
		AgentUID int64 `json:"agent_uid"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if !resolved.Bound || resolved.AgentUID != 43 {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=feishu&channel_app_id=other_app&channel_user_id=ou_user", nil)
	otherRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(otherRec, otherReq)
	if otherRec.Code != http.StatusOK {
		t.Fatalf("other resolve status=%d body=%s", otherRec.Code, otherRec.Body.String())
	}
	var other struct {
		Bound bool `json:"bound"`
	}
	if err := json.Unmarshal(otherRec.Body.Bytes(), &other); err != nil {
		t.Fatalf("decode other response: %v", err)
	}
	if other.Bound {
		t.Fatalf("expected other app id to stay unbound")
	}
}

func TestChannelAgentBindingUsesConfiguredFeishuAppID(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cloud_feishu_app")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"feishu","channel_app_id":"operator_input"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleAgentEntries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Entry channelAgentEntryResponse `json:"entry"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.Entry.ChannelAppID != "cloud_feishu_app" {
		t.Fatalf("expected configured Feishu app id, got %+v", created.Entry)
	}
}

func TestChannelAgentBindingRejectsEntryAppIDMismatch(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene_cli_app",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	body := `{"scene_key":"` + entry.SceneKey + `","channel":"feishu","channel_app_id":"other_app","channel_user_id":"ou_user"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelAgentBindingResolveAuth(t *testing.T) {
	db := newChannelAgentTestStore()
	handler := NewChannelAgentBindingHandler(db, nil)

	t.Setenv("APP_ENV", "production")
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "")

	openReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=weixin&channel_user_id=openid", nil)
	openRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(openRec, openReq)
	if openRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected production resolve without token to be unauthorized, got status=%d body=%s", openRec.Code, openRec.Body.String())
	}

	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "secret")
	queryTokenReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=weixin&channel_user_id=openid&resolve_token=secret", nil)
	queryTokenRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(queryTokenRec, queryTokenReq)
	if queryTokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected query token to be rejected, got status=%d body=%s", queryTokenRec.Code, queryTokenRec.Body.String())
	}

	bearerReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=weixin&channel_user_id=openid", nil)
	bearerReq.Header.Set("Authorization", "Bearer secret")
	bearerRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(bearerRec, bearerReq)
	if bearerRec.Code != http.StatusOK {
		t.Fatalf("expected bearer token to be accepted, got status=%d body=%s", bearerRec.Code, bearerRec.Body.String())
	}
}

func TestChannelAgentEntryRegenerateRequiresActiveEntry(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey: "scene-old",
		Channel:  "weixin",
		OwnerUID: 7,
		AgentUID: 43,
		Status:   "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	if _, err := db.RegenerateChannelAgentEntry(entry.ID, 7, "scene-new"); err != nil {
		t.Fatalf("first regenerate: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agent-entries/"+strconv.FormatInt(entry.ID, 10)+"/regenerate", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleAgentEntryByID(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type channelAgentTestStore struct {
	store.Store
	users    map[int64]*types.User
	owners   map[int64]int64
	bodyIDs  map[int64]string
	entries  map[int64]*types.ChannelAgentEntry
	bindings map[string]*types.ChannelAgentBinding
	messages []*types.Message
	topics   []string
	nextID   int64
}

func newChannelAgentTestStore() *channelAgentTestStore {
	return &channelAgentTestStore{
		users:    map[int64]*types.User{},
		owners:   map[int64]int64{},
		bodyIDs:  map[int64]string{},
		entries:  map[int64]*types.ChannelAgentEntry{},
		bindings: map[string]*types.ChannelAgentBinding{},
		messages: []*types.Message{},
		topics:   []string{},
		nextID:   1,
	}
}

func (s *channelAgentTestStore) CreateUser(u *types.User) (int64, error) {
	next := *u
	next.ID = s.nextID
	s.nextID++
	if next.AccountType == "" {
		next.AccountType = types.AccountHuman
	}
	now := time.Now()
	next.CreatedAt = now
	next.UpdatedAt = now
	s.users[next.ID] = &next
	return next.ID, nil
}

func (s *channelAgentTestStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
}

func (s *channelAgentTestStore) GetUserByUsername(username string) (*types.User, error) {
	for _, user := range s.users {
		if user.Username == username {
			return user, nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) GetBotOwner(botUID int64) (int64, error) {
	return s.owners[botUID], nil
}

func (s *channelAgentTestStore) GetBotBodyID(botUID int64) (string, error) {
	return s.bodyIDs[botUID], nil
}

func (s *channelAgentTestStore) CreateTopic(id, topicType string, ownerID int64) error {
	s.topics = append(s.topics, id)
	return nil
}

func (s *channelAgentTestStore) SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error) {
	id := s.nextID
	s.nextID++
	s.messages = append(s.messages, &types.Message{ID: id, TopicID: topicID, FromUID: fromUID, Content: content, MsgType: msgType, CreatedAt: time.Now()})
	return id, nil
}

func (s *channelAgentTestStore) SaveMessageWithBlocks(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string) (int64, error) {
	id, err := s.SaveMessage(topicID, fromUID, content, msgType)
	if err != nil {
		return 0, err
	}
	s.messages[len(s.messages)-1].ContentBlocks = blocks
	s.messages[len(s.messages)-1].Mode = mode
	s.messages[len(s.messages)-1].Role = role
	return id, nil
}

func (s *channelAgentTestStore) SaveMessageWithReply(topicID string, fromUID int64, content, msgType string, replyTo int64) (int64, error) {
	return s.SaveMessage(topicID, fromUID, content, msgType)
}

func (s *channelAgentTestStore) SaveMessageIdempotent(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (int64, bool, error) {
	for _, message := range s.messages {
		if message.TopicID == topicID && message.FromUID == fromUID && message.Content == content {
			return message.ID, true, nil
		}
	}
	id, err := s.SaveMessageWithBlocks(topicID, fromUID, content, blocks, mode, role, msgType)
	return id, false, err
}

func (s *channelAgentTestStore) EnsureChannelAgentEntry(entry *types.ChannelAgentEntry) (*types.ChannelAgentEntry, error) {
	for _, existing := range s.entries {
		if existing.OwnerUID == entry.OwnerUID && existing.AgentUID == entry.AgentUID && existing.Channel == entry.Channel && existing.ChannelAppID == entry.ChannelAppID && existing.Status == "active" {
			return cloneEntry(existing), nil
		}
	}
	now := time.Now()
	next := cloneEntry(entry)
	next.ID = s.nextID
	s.nextID++
	next.Status = "active"
	next.CreatedAt = now
	next.UpdatedAt = now
	s.entries[next.ID] = next
	return cloneEntry(next), nil
}

func (s *channelAgentTestStore) ListChannelAgentEntries(ownerUID, agentUID int64) ([]*types.ChannelAgentEntry, error) {
	var out []*types.ChannelAgentEntry
	for _, entry := range s.entries {
		if entry.OwnerUID == ownerUID && entry.AgentUID == agentUID && entry.Status == "active" {
			out = append(out, cloneEntry(entry))
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) RegenerateChannelAgentEntry(id, ownerUID int64, sceneKey string) (*types.ChannelAgentEntry, error) {
	entry := s.entries[id]
	if entry == nil || entry.OwnerUID != ownerUID || entry.Status != "active" {
		return nil, nil
	}
	entry.Status = "revoked"
	return s.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     sceneKey,
		Channel:      entry.Channel,
		ChannelAppID: entry.ChannelAppID,
		OwnerUID:     ownerUID,
		AgentUID:     entry.AgentUID,
	})
}

func (s *channelAgentTestStore) GetChannelAgentEntryBySceneKey(sceneKey string) (*types.ChannelAgentEntry, error) {
	for _, entry := range s.entries {
		if entry.SceneKey == sceneKey {
			return cloneEntry(entry), nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error) {
	now := time.Now()
	next := cloneBinding(binding)
	next.ID = s.nextID
	s.nextID++
	next.Status = "active"
	next.BoundAt = now
	next.UpdatedAt = now
	s.bindings[bindingKey(next.Channel, next.ChannelAppID, next.ChannelUserID, next.ChannelConversationID)] = next
	return cloneBinding(next), nil
}

func (s *channelAgentTestStore) ResolveChannelAgentBinding(query types.ChannelAgentBindingQuery) (*types.ChannelAgentBinding, error) {
	if binding := s.bindings[bindingKey(query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID)]; binding != nil {
		return cloneBinding(binding), nil
	}
	if query.ChannelConversationID != "" {
		if binding := s.bindings[bindingKey(query.Channel, query.ChannelAppID, query.ChannelUserID, "")]; binding != nil {
			return cloneBinding(binding), nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) ResolveChannelAgentBindingForActor(channel, channelAppID string, actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	for _, binding := range s.bindings {
		if binding.Channel == channel && binding.ChannelAppID == channelAppID && binding.ActorUID == actorUID && binding.AgentUID == agentUID && binding.Status == "active" {
			return cloneBinding(binding), nil
		}
	}
	return nil, nil
}

func cloneEntry(entry *types.ChannelAgentEntry) *types.ChannelAgentEntry {
	if entry == nil {
		return nil
	}
	next := *entry
	return &next
}

func cloneBinding(binding *types.ChannelAgentBinding) *types.ChannelAgentBinding {
	if binding == nil {
		return nil
	}
	next := *binding
	return &next
}

func bindingKey(channel, appID, userID, conversationID string) string {
	return channel + "\x00" + appID + "\x00" + userID + "\x00" + conversationID
}
