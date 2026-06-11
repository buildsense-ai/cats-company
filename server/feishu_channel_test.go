package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type fakeFeishuAPI struct {
	appID    string
	identity *FeishuUserIdentity
	sends    []fakeFeishuSend
}

type fakeFeishuSend struct {
	ReceiveIDType string
	ReceiveID     string
	Text          string
}

func (f *fakeFeishuAPI) AppID() string {
	return f.appID
}

func (f *fakeFeishuAPI) ExchangeOAuthCode(ctx context.Context, code string, redirectURI string) (*FeishuUserIdentity, error) {
	return f.identity, nil
}

func (f *fakeFeishuAPI) SendTextMessage(ctx context.Context, receiveIDType string, receiveID string, text string) error {
	f.sends = append(f.sends, fakeFeishuSend{ReceiveIDType: receiveIDType, ReceiveID: receiveID, Text: text})
	return nil
}

func TestFeishuEventURLVerification(t *testing.T) {
	handler := NewFeishuChannelHandler(newChannelAgentTestStore(), nil, FeishuChannelConfig{
		EventVerificationToken: "verify-token",
	}, &fakeFeishuAPI{appID: "cli_app"})

	body := `{"schema":"2.0","challenge":"challenge-value","header":{"event_type":"url_verification","token":"verify-token"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Challenge != "challenge-value" {
		t.Fatalf("challenge=%q", resp.Challenge)
	}
}

func TestFeishuOAuthCallbackBindsActor(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	api := &fakeFeishuAPI{
		appID: "cli_app",
		identity: &FeishuUserIdentity{
			OpenID: "ou_user",
			UserID: "user_1",
			Name:   "Feishu Alice",
		},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID:            "cli_app",
		AppSecret:        "secret",
		OAuthRedirectURI: "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback",
	}, api)
	state, err := handler.signOAuthState(feishuOAuthState{
		SceneKey:  entry.SceneKey,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Nonce:     "nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-1&state="+state, nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthCallback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "设备授权") || !strings.Contains(body, "/channel-device-link") || !strings.Contains(body, "binding_id=") || !strings.Contains(body, "link_token=") {
		t.Fatalf("oauth success page should include device link guidance, body=%s", body)
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
	})
	if err != nil || binding == nil {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if binding.ActorUID <= 0 || binding.AgentUID != 43 || binding.OwnerUID != 7 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	actor := db.users[binding.ActorUID]
	if actor == nil || actor.Username == "" || actor.DisplayName != "Feishu Alice" {
		t.Fatalf("actor=%+v", actor)
	}
}

func TestFeishuOAuthCallbackUsesConfiguredAppID(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu-legacy",
		Channel:      "feishu",
		ChannelAppID: "legacy_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	api := &fakeFeishuAPI{
		appID: "cloud_app",
		identity: &FeishuUserIdentity{
			OpenID: "ou_user",
			Name:   "Feishu Alice",
		},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID:            "cloud_app",
		AppSecret:        "secret",
		OAuthRedirectURI: "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback",
	}, api)
	state, err := handler.signOAuthState(feishuOAuthState{
		SceneKey:  entry.SceneKey,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Nonce:     "nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-1&state="+state, nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthCallback(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelAppID:  "cloud_app",
		ChannelUserID: "ou_user",
	})
	if err != nil || binding == nil {
		t.Fatalf("cloud app binding=%+v err=%v", binding, err)
	}
	legacy, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelAppID:  "legacy_app",
		ChannelUserID: "ou_user",
	})
	if err != nil {
		t.Fatalf("legacy resolve: %v", err)
	}
	if legacy != nil {
		t.Fatalf("legacy app should not receive OAuth binding: %+v", legacy)
	}
}

func TestFeishuMessageEventDeliversToBoundAgent(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "feishu-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	content, _ := json.Marshal(map[string]string{"text": "查一下合同进度"})
	eventBody := map[string]interface{}{
		"schema": "2.0",
		"header": map[string]interface{}{
			"event_type": "im.message.receive_v1",
			"app_id":     "cli_app",
		},
		"event": map[string]interface{}{
			"sender": map[string]interface{}{
				"sender_type": "user",
				"sender_id": map[string]interface{}{
					"open_id": "ou_user",
				},
			},
			"message": map[string]interface{}{
				"message_id":   "om_msg_1",
				"chat_id":      "oc_chat_1",
				"chat_type":    "p2p",
				"message_type": "text",
				"content":      string(content),
			},
		},
	}
	body, _ := json.Marshal(eventBody)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.messages) != 1 {
		t.Fatalf("messages=%d", len(db.messages))
	}
	if db.messages[0].TopicID != "p2p_8_43" || db.messages[0].FromUID != 8 || db.messages[0].Content != "查一下合同进度" {
		t.Fatalf("message=%+v", db.messages[0])
	}
	if len(api.sends) != 0 {
		t.Fatalf("unexpected immediate sends: %+v", api.sends)
	}
}

func TestFeishuOutboundForwardsBotReply(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "feishu-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")

	if err := dispatcher.ForwardBotReply(context.Background(), 8, 43, "p2p_8_43", "合同进度正常。"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(api.sends) != 1 {
		t.Fatalf("sends=%+v", api.sends)
	}
	if api.sends[0].ReceiveIDType != "open_id" || api.sends[0].ReceiveID != "ou_user" || api.sends[0].Text != "合同进度正常。" {
		t.Fatalf("send=%+v", api.sends[0])
	}
}
