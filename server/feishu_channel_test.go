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
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessPublic,
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
	if body := rec.Body.String(); !strings.Contains(body, "登录 CatsCo") || !strings.Contains(body, "/channel-device-link") || !strings.Contains(body, "binding_id=") || !strings.Contains(body, "link_token=") {
		t.Fatalf("oauth success page should require CatsCo account link, body=%s", body)
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
	})
	if err != nil || binding == nil {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if binding.ActorUID <= 0 || binding.AgentUID != 43 || binding.OwnerUID != 7 || binding.CanonicalUID != 0 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	actor := db.users[binding.ActorUID]
	if actor == nil || actor.Username == "" || actor.DisplayName != "Feishu Alice" {
		t.Fatalf("actor=%+v", actor)
	}
}

func TestFeishuOAuthCallbackMobileIdentityLinkReusesExistingCatsCoFriend(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(9, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 9)] = types.FriendAccepted
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	mobileLink, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.feishu-mobile",
		EntryID:      entry.ID,
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		CanonicalUID: 9,
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create mobile link: %v", err)
	}
	api := &fakeFeishuAPI{
		appID: "cli_app",
		identity: &FeishuUserIdentity{
			OpenID: "ou_mobile",
			UserID: "user_mobile",
			Name:   "Feishu Mobile Alice",
		},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID:            "cli_app",
		AppSecret:        "secret",
		OAuthRedirectURI: "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback",
	}, api)
	state, err := handler.signOAuthState(feishuOAuthState{
		SceneKey:  mobileLink.SceneKey,
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
	if body := rec.Body.String(); strings.Contains(body, "需要登录") || strings.Contains(body, "好友申请") || strings.Contains(body, "管理员通过") || strings.Contains(body, "channel-device-link") || strings.Contains(body, "设备授权") {
		t.Fatalf("mobile link should bind directly, body=%s", body)
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_mobile",
	})
	if err != nil || binding == nil {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if binding.CanonicalUID != 9 || binding.OwnerUID != 7 || binding.AgentUID != 43 || binding.Status != types.ChannelAgentBindingActive {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if len(db.accessRequests) != 0 {
		t.Fatalf("mobile link should not create a new approval request: %+v", db.accessRequests)
	}
	reused, _, err := resolveChannelIdentityMobileLink(db, mobileLink.SceneKey, "feishu", "cli_app", true)
	if err != nil {
		t.Fatalf("reused mobile link should not error: %v", err)
	}
	if reused != nil {
		t.Fatalf("mobile link should be consumed by callback, reused=%+v", reused)
	}
}

func TestFeishuOAuthCallbackMobileIdentityLinkRejectsDifferentCatsCoUserWithoutConsuming(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[10] = &types.User{ID: 10, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("feishu", "cli_app", "ou_mobile"), DisplayName: "Feishu Mobile", AccountType: types.AccountHuman}
	db.owners[43] = 7
	db.friends[friendKey(9, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 9)] = types.FriendAccepted
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_mobile",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            10,
		OwnerUID:                7,
		AgentUID:                43,
		EntryID:                 entry.ID,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed existing channel identity: %v", err)
	}
	mobileLink, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.feishu-conflict",
		EntryID:      entry.ID,
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		CanonicalUID: 9,
		ExpiresAt:    time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create mobile link: %v", err)
	}
	api := &fakeFeishuAPI{
		appID: "cli_app",
		identity: &FeishuUserIdentity{
			OpenID: "ou_mobile",
			UserID: "user_mobile",
			Name:   "Feishu Mobile Alice",
		},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID:            "cli_app",
		AppSecret:        "secret",
		OAuthRedirectURI: "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback",
	}, api)
	state, err := handler.signOAuthState(feishuOAuthState{
		SceneKey:  mobileLink.SceneKey,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Nonce:     "nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-1&state="+state, nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthCallback(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "已经绑定到另一个 CatsCo 账号") {
		t.Fatalf("expected account conflict guidance, body=%s", body)
	}
	if got := db.mobileLinks[mobileLink.SceneKey]; got == nil || got.Status != "active" {
		t.Fatalf("mobile link should remain active after failed binding, got=%+v", got)
	}
}

func TestFeishuOAuthShortLinkRedirectsToStart(t *testing.T) {
	handler := NewFeishuChannelHandler(newChannelAgentTestStore(), nil, FeishuChannelConfig{
		AppID: "cli_app",
	}, &fakeFeishuAPI{appID: "cli_app"})
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/f/scene-feishu", nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthShortLink(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/start?scene_key=scene-feishu" {
		t.Fatalf("redirect=%s", got)
	}
}

func TestFeishuNativeEntryShortLinkRedirectsToNativeEntry(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}&scene={scene_key}&oauth={oauth_url_encoded}")
	db := newChannelAgentTestStore()
	if _, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	}); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID: "cli_app",
	}, &fakeFeishuAPI{appID: "cli_app"})
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/fn/scene-feishu", nil)
	rec := httptest.NewRecorder()
	handler.HandleNativeEntryShortLink(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := "https://applink.feishu.cn/client/app/open?app_id=cli_app&scene=scene-feishu&oauth=https%3A%2F%2Fapp.catsco.cc%2Fapi%2Fchannel-agent-bindings%2Foauth%2Ffeishu%2Fstart%3Fscene_key%3Dscene-feishu"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("redirect=%s", got)
	}
}

func TestFeishuNativeEntryShortLinkRequiresTemplate(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	db := newChannelAgentTestStore()
	if _, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	}); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID: "cli_app",
	}, &fakeFeishuAPI{appID: "cli_app"})
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/fn/scene-feishu", nil)
	rec := httptest.NewRecorder()
	handler.HandleNativeEntryShortLink(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "feishu native entry is not ready") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFeishuNativeEntryShortLinkRejectsAppIDMismatch(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}&scene={scene_key}")
	db := newChannelAgentTestStore()
	if _, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "legacy_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	}); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID: "cli_app",
	}, &fakeFeishuAPI{appID: "cli_app"})
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/fn/scene-feishu", nil)
	rec := httptest.NewRecorder()
	handler.HandleNativeEntryShortLink(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFeishuOAuthCallbackRejectsEntryAppIDMismatch(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cloud_app", "ou_user"), DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-feishu-legacy",
		Channel:      "feishu",
		ChannelAppID: "legacy_app",
		AccessMode:   types.ChannelAgentAccessPublic,
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
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelAppID:  "cloud_app",
		ChannelUserID: "ou_user",
	})
	if err != nil {
		t.Fatalf("cloud app resolve: %v", err)
	}
	if binding != nil {
		t.Fatalf("mismatched entry should not receive OAuth binding: %+v", binding)
	}
}

func TestFeishuMessageEventDeliversToBoundAgent(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                43,
		Source:                  "test",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
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

func TestFeishuMessageEventRequiresSelectedAgent(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  "active",
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	rec := sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_no_route", "查一下合同进度")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.messages) != 0 {
		t.Fatalf("message should not be delivered without selected route: %+v", db.messages)
	}
	if len(api.sends) != 1 || !strings.Contains(api.sends[0].Text, "请先选择一个虚拟员工") {
		t.Fatalf("send=%+v", api.sends)
	}
}

func TestFeishuP2PBindingInheritsActivatedBaseBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingPendingLogin,
	}); err != nil {
		t.Fatalf("seed base binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                43,
		Source:                  "oauth",
	}); err != nil {
		t.Fatalf("seed base route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_before_link", "查合同")
	if len(db.messages) != 0 {
		t.Fatalf("message should not deliver before CatsCo link: %+v", db.messages)
	}
	if len(api.sends) != 1 || !strings.Contains(api.sends[0].Text, "请先登录 CatsCo 账号") {
		t.Fatalf("send=%+v", api.sends)
	}

	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("activate base binding: %v", err)
	}
	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_after_link", "查合同")
	if len(db.messages) != 1 || db.messages[0].TopicID != "p2p_8_43" || db.messages[0].Content != "查合同" {
		t.Fatalf("message should deliver after base link activation: %+v", db.messages)
	}
	sessionBinding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		AgentUID:                43,
		ActorUID:                8,
	})
	if err != nil || sessionBinding == nil || sessionBinding.Status != types.ChannelAgentBindingActive || sessionBinding.CanonicalUID != 8 {
		t.Fatalf("session binding should inherit active base binding, got %+v err=%v", sessionBinding, err)
	}
}

func TestFeishuGatewaySwitchesCurrentAgentWithoutOverwritingBindings(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "finance-agent", DisplayName: "Finance Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	db.friends[friendKey(8, 44)] = types.FriendAccepted
	db.friends[friendKey(44, 8)] = types.FriendAccepted
	for _, seed := range []struct {
		scene string
		agent int64
	}{
		{"scene-contract", 43},
		{"scene-finance", 44},
	} {
		if _, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
			SceneKey:     seed.scene,
			Channel:      "feishu",
			ChannelAppID: "cli_app",
			AccessMode:   types.ChannelAgentAccessApprovalRequired,
			OwnerUID:     7,
			AgentUID:     seed.agent,
			Status:       "active",
		}); err != nil {
			t.Fatalf("seed entry: %v", err)
		}
		if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
			Channel:                 "feishu",
			ChannelAppID:            "cli_app",
			ChannelUserID:           "ou_user",
			ChannelConversationID:   "oc_chat_1",
			ChannelConversationType: "p2p",
			ActorUID:                8,
			CanonicalUID:            8,
			OwnerUID:                7,
			AgentUID:                seed.agent,
			Status:                  "active",
		}); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_switch_a", "切换到 Contract Agent")
	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_msg_a", "查合同")
	if len(db.messages) != 1 || db.messages[0].TopicID != "p2p_8_43" || db.messages[0].Content != "查合同" {
		t.Fatalf("contract delivery=%+v", db.messages)
	}
	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_switch_b", "切换到 Finance Agent")
	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_msg_b", "查报销")
	if len(db.messages) != 2 || db.messages[1].TopicID != "p2p_8_44" || db.messages[1].Content != "查报销" {
		t.Fatalf("finance delivery=%+v", db.messages)
	}
	for _, agentUID := range []int64{43, 44} {
		binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
			Channel:                 "feishu",
			ChannelAppID:            "cli_app",
			ChannelUserID:           "ou_user",
			ChannelConversationID:   "oc_chat_1",
			ChannelConversationType: "p2p",
			AgentUID:                agentUID,
		})
		if err != nil || binding == nil || binding.AgentUID != agentUID {
			t.Fatalf("binding for agent %d = %+v err=%v", agentUID, binding, err)
		}
	}
}

func TestFeishuGroupMessageIgnoredWithoutMentionOrCommand(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	rec := sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_group_1", "group", "om_group_1", "大家看一下这个合同")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(api.sends) != 0 || len(db.messages) != 0 {
		t.Fatalf("group message should be ignored, sends=%+v messages=%+v", api.sends, db.messages)
	}
}

func TestFeishuGroupMentionToOtherUserDoesNotTrigger(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	rec := sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_group_1", "group", "om_group_other", "@张三 帮我看合同")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(api.sends) != 0 || len(db.messages) != 0 {
		t.Fatalf("mentioning another user should not trigger CatsCo, sends=%+v messages=%+v", api.sends, db.messages)
	}
}

func TestFeishuGroupBindingLinksAreSentPrivately(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_group_1",
		ChannelConversationType: "group",
		ActorUID:                8,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingPendingLogin,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_group_1",
		ChannelConversationType: "group",
		ActorUID:                8,
		AgentUID:                43,
		Source:                  "manual",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_group_1", "group", "om_group_bind", "绑定账号")
	if len(api.sends) != 2 {
		t.Fatalf("expected private detail and group ack, sends=%+v", api.sends)
	}
	if api.sends[0].ReceiveIDType != "open_id" || api.sends[0].ReceiveID != "ou_user" || !strings.Contains(api.sends[0].Text, "请先登录 CatsCo 账号") {
		t.Fatalf("private send=%+v", api.sends[0])
	}
	if api.sends[1].ReceiveIDType != "chat_id" || api.sends[1].ReceiveID != "oc_group_1" || strings.Contains(api.sends[1].Text, "channel-device-link") {
		t.Fatalf("group ack should not contain binding link, send=%+v", api.sends[1])
	}
	if len(db.messages) != 0 {
		t.Fatalf("group bind command should not deliver to model: %+v", db.messages)
	}
}

func TestFeishuOutboundForwardsBotReply(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      8,
		CanonicalUID:  8,
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

func sendFeishuTextEvent(t *testing.T, handler *FeishuChannelHandler, appID, openID, chatID, chatType, messageID, text string) *httptest.ResponseRecorder {
	t.Helper()
	content, _ := json.Marshal(map[string]string{"text": text})
	eventBody := map[string]interface{}{
		"schema": "2.0",
		"header": map[string]interface{}{
			"event_type": "im.message.receive_v1",
			"app_id":     appID,
		},
		"event": map[string]interface{}{
			"sender": map[string]interface{}{
				"sender_type": "user",
				"sender_id": map[string]interface{}{
					"open_id": openID,
				},
			},
			"message": map[string]interface{}{
				"message_id":   messageID,
				"chat_id":      chatID,
				"chat_type":    chatType,
				"message_type": "text",
				"content":      string(content),
			},
		},
	}
	body, _ := json.Marshal(eventBody)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)
	return rec
}
