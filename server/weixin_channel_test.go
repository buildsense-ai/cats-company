package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type fakeWeixinAPI struct {
	appID string
	qrs   []string
	sends []fakeWeixinSend
}

type fakeWeixinSend struct {
	OpenID string
	Text   string
}

func (f *fakeWeixinAPI) AppID() string {
	return f.appID
}

func (f *fakeWeixinAPI) CreatePermanentQRCode(ctx context.Context, sceneKey string) (*WeixinQRCode, error) {
	f.qrs = append(f.qrs, sceneKey)
	return &WeixinQRCode{
		Ticket:   "ticket-" + sceneKey,
		ImageURL: "https://mp.weixin.qq.com/cgi-bin/showqrcode?ticket=ticket-" + sceneKey,
		URL:      "http://weixin.qq.com/q/" + sceneKey,
	}, nil
}

func (f *fakeWeixinAPI) SendTextMessage(ctx context.Context, openID string, text string) error {
	f.sends = append(f.sends, fakeWeixinSend{OpenID: openID, Text: text})
	return nil
}

func TestWeixinURLVerification(t *testing.T) {
	handler := NewWeixinChannelHandler(newChannelAgentTestStore(), nil, WeixinChannelConfig{
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	req := httptest.NewRequest(http.MethodGet, "/api/channels/weixin/events?timestamp=1&nonce=2&echostr=ok&signature="+weixinTestSignature("token-1", "1", "2"), nil)
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWeixinURLVerificationRejectsBadSignature(t *testing.T) {
	handler := NewWeixinChannelHandler(newChannelAgentTestStore(), nil, WeixinChannelConfig{
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	req := httptest.NewRequest(http.MethodGet, "/api/channels/weixin/events?timestamp=1&nonce=2&echostr=ok&signature=bad", nil)
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWeixinURLVerificationRequiresTokenInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	handler := NewWeixinChannelHandler(newChannelAgentTestStore(), nil, WeixinChannelConfig{}, &fakeWeixinAPI{appID: "wx_app"})
	req := httptest.NewRequest(http.MethodGet, "/api/channels/weixin/events?timestamp=1&nonce=2&echostr=ok", nil)
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWeixinScanEventBindsActor(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[subscribe]]></Event><EventKey><![CDATA[qrscene_scene-weixin]]></EventKey></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
	})
	if err != nil || binding == nil {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if binding.ActorUID <= 0 || binding.AgentUID != 43 || binding.OwnerUID != 7 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if len(db.topics) != 1 || db.topics[0] != "p2p_"+itoa(binding.ActorUID)+"_43" {
		t.Fatalf("topics=%+v binding=%+v", db.topics, binding)
	}
	if !strings.Contains(rec.Body.String(), "Contract Agent") {
		t.Fatalf("reply=%s", rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "设备授权") || !strings.Contains(body, "/channel-device-link") || !strings.Contains(body, "binding_id=") || !strings.Contains(body, "link_token=") {
		t.Fatalf("scan reply should include device link guidance, body=%s", body)
	}
}

func TestWeixinScanEventForExistingSubscriberBindsActor(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[SCAN]]></Event><EventKey><![CDATA[scene-weixin]]></EventKey></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
	})
	if err != nil || binding == nil {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if binding.ActorUID <= 0 || binding.AgentUID != 43 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
}

func TestWeixinEncryptedEventAcknowledgesWithoutParsing(t *testing.T) {
	db := newChannelAgentTestStore()
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	body := `<xml><Encrypt><![CDATA[encrypted]]></Encrypt></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&encrypt_type=aes&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.bindings) != 0 || len(db.messages) != 0 {
		t.Fatalf("bindings=%d messages=%d", len(db.bindings), len(db.messages))
	}
}

func TestWeixinScanEventRejectsAppIDMismatch(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "other_wx_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[SCAN]]></Event><EventKey><![CDATA[scene-weixin]]></EventKey></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "入口不存在或已失效") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
	})
	if err != nil {
		t.Fatalf("resolve binding: %v", err)
	}
	if binding != nil {
		t.Fatalf("unexpected binding: %+v", binding)
	}
}

func TestWeixinQRCodeRejectsAppIDMismatch(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "other_wx_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	api := &fakeWeixinAPI{appID: "wx_app"}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID: "wx_app",
	}, api)
	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-entry/weixin-qrcode?scene_key=scene-weixin", nil)
	rec := httptest.NewRecorder()
	handler.HandleQRCode(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(api.qrs) != 0 {
		t.Fatalf("unexpected qr calls: %+v", api.qrs)
	}
}

func TestWeixinTextMessageDeliversToBoundAgent(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "weixin-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeWeixinAPI{appID: "wx_app"}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, api)
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[查一下合同进度]]></Content><MsgId>msg-1</MsgId></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.messages) != 1 {
		t.Fatalf("messages=%d", len(db.messages))
	}
	if db.messages[0].TopicID != "p2p_8_43" || db.messages[0].FromUID != 8 || db.messages[0].Content != "查一下合同进度" {
		t.Fatalf("message=%+v", db.messages[0])
	}
}

func TestWeixinTextMessageDeduplicatesRetry(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "weixin-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[查一下合同进度]]></Content><MsgId>msg-1</MsgId></xml>`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.HandleEvents(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if len(db.messages) != 1 {
		t.Fatalf("messages=%d", len(db.messages))
	}
}

func TestWeixinTextMessagePromptsWhenUnbound(t *testing.T) {
	api := &fakeWeixinAPI{appID: "wx_app"}
	handler := NewWeixinChannelHandler(newChannelAgentTestStore(), nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, api)
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[你好]]></Content><MsgId>msg-1</MsgId></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "请先扫描虚拟员工入口二维码") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWeixinQRCodeEndpointRedirectsToOfficialQR(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	api := &fakeWeixinAPI{appID: "wx_app"}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID: "wx_app",
	}, api)
	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-entry/weixin-qrcode?scene_key=scene-weixin", nil)
	rec := httptest.NewRecorder()
	handler.HandleQRCode(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "https://mp.weixin.qq.com/cgi-bin/showqrcode?ticket=ticket-scene-weixin" {
		t.Fatalf("location=%s", got)
	}
	if len(api.qrs) != 1 || api.qrs[0] != "scene-weixin" {
		t.Fatalf("qrs=%+v", api.qrs)
	}
}

func TestWeixinAPIClientCreatesQRCodeAndSendsText(t *testing.T) {
	tokenCalls := 0
	qrCalls := 0
	sendCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			tokenCalls++
			if r.URL.Query().Get("grant_type") != "client_credential" || r.URL.Query().Get("appid") != "wx_app" || r.URL.Query().Get("secret") != "secret" {
				t.Fatalf("unexpected token query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/cgi-bin/qrcode/create":
			qrCalls++
			if r.URL.Query().Get("access_token") != "token-1" {
				t.Fatalf("unexpected qr token: %s", r.URL.RawQuery)
			}
			var payload struct {
				ActionName string `json:"action_name"`
				ActionInfo struct {
					Scene struct {
						SceneStr string `json:"scene_str"`
					} `json:"scene"`
				} `json:"action_info"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode qr payload: %v", err)
			}
			if payload.ActionName != "QR_LIMIT_STR_SCENE" || payload.ActionInfo.Scene.SceneStr != "scene-weixin" {
				t.Fatalf("unexpected qr payload: %+v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ticket": "ticket-1",
				"url":    "http://weixin.qq.com/q/scene-weixin",
			})
		case "/cgi-bin/message/custom/send":
			sendCalls++
			if r.URL.Query().Get("access_token") != "token-1" {
				t.Fatalf("unexpected send token: %s", r.URL.RawQuery)
			}
			var payload struct {
				ToUser  string `json:"touser"`
				MsgType string `json:"msgtype"`
				Text    struct {
					Content string `json:"content"`
				} `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode send payload: %v", err)
			}
			if payload.ToUser != "openid-1" || payload.MsgType != "text" || payload.Text.Content != "合同进度正常。" {
				t.Fatalf("unexpected send payload: %+v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"errcode": 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newWeixinAPIClient(WeixinChannelConfig{
		AppID:         "wx_app",
		AppSecret:     "secret",
		APIBaseURL:    srv.URL,
		QRShowBaseURL: "https://show.example/qrcode",
	})
	qr, err := client.CreatePermanentQRCode(context.Background(), "scene-weixin")
	if err != nil {
		t.Fatalf("create qr: %v", err)
	}
	if qr.Ticket != "ticket-1" || qr.ImageURL != "https://show.example/qrcode?ticket=ticket-1" || qr.URL == "" {
		t.Fatalf("qr=%+v", qr)
	}
	if _, err := client.CreatePermanentQRCode(context.Background(), "scene-weixin"); err != nil {
		t.Fatalf("create cached qr: %v", err)
	}
	if err := client.SendTextMessage(context.Background(), "openid-1", "合同进度正常。"); err != nil {
		t.Fatalf("send text: %v", err)
	}
	if tokenCalls != 1 || qrCalls != 1 || sendCalls != 1 {
		t.Fatalf("token=%d qr=%d send=%d", tokenCalls, qrCalls, sendCalls)
	}
}

func TestWeixinOutboundForwardsBotReply(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "weixin-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeWeixinAPI{appID: "wx_app"}
	dispatcher := NewChannelOutboundDispatcher(db, nil, "").WithWeixin(api, "wx_app")

	if err := dispatcher.ForwardBotReply(context.Background(), 8, 43, "p2p_8_43", "合同进度正常。"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(api.sends) != 1 {
		t.Fatalf("sends=%+v", api.sends)
	}
	if api.sends[0].OpenID != "openid-1" || api.sends[0].Text != "合同进度正常。" {
		t.Fatalf("send=%+v", api.sends[0])
	}
}

func TestChannelOutboundDispatcherPreservesFeishuAndWeixin(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "feishu-alice", DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "weixin-alice", DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "feishu_app",
		ChannelUserID: "ou_user",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	}); err != nil {
		t.Fatalf("seed feishu binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      9,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	}); err != nil {
		t.Fatalf("seed weixin binding: %v", err)
	}
	hub := NewHub(db, nil)
	feishuAPI := &fakeFeishuAPI{appID: "feishu_app"}
	weixinAPI := &fakeWeixinAPI{appID: "wx_app"}
	NewFeishuChannelHandler(db, hub, FeishuChannelConfig{AppID: "feishu_app"}, feishuAPI).InstallOutboundDispatcher()
	NewWeixinChannelHandler(db, hub, WeixinChannelConfig{AppID: "wx_app"}, weixinAPI).InstallOutboundDispatcher()

	if err := hub.channelOut.ForwardBotReply(context.Background(), 8, 43, "p2p_8_43", "飞书回复"); err != nil {
		t.Fatalf("forward feishu: %v", err)
	}
	if err := hub.channelOut.ForwardBotReply(context.Background(), 9, 43, "p2p_9_43", "微信回复"); err != nil {
		t.Fatalf("forward weixin: %v", err)
	}
	if len(feishuAPI.sends) != 1 || feishuAPI.sends[0].ReceiveID != "ou_user" || feishuAPI.sends[0].Text != "飞书回复" {
		t.Fatalf("feishu sends=%+v", feishuAPI.sends)
	}
	if len(weixinAPI.sends) != 1 || weixinAPI.sends[0].OpenID != "openid-1" || weixinAPI.sends[0].Text != "微信回复" {
		t.Fatalf("weixin sends=%+v", weixinAPI.sends)
	}
}

func TestWeixinEntryResponseUsesConfiguredQRCode(t *testing.T) {
	t.Setenv("CATSCO_WEIXIN_APP_ID", "wx_app")
	t.Setenv("CATSCO_WEIXIN_APP_SECRET", "secret")
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
	})
	if resp.ChannelQRURL != "https://app.catsco.cc/api/channel-agent-entry/weixin-qrcode?scene_key=scene-weixin" {
		t.Fatalf("channel_qr_url=%s", resp.ChannelQRURL)
	}
}

func TestWeixinEntryResponseOmitsQRCodeForAppIDMismatch(t *testing.T) {
	t.Setenv("CATSCO_WEIXIN_APP_ID", "wx_app")
	t.Setenv("CATSCO_WEIXIN_APP_SECRET", "secret")
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "other_wx_app",
	})
	if resp.ChannelQRURL != "" {
		t.Fatalf("channel_qr_url=%s", resp.ChannelQRURL)
	}
}

func TestWeixinEntryUsesConfiguredAppID(t *testing.T) {
	t.Setenv("CATSCO_WEIXIN_APP_ID", "wx_app")
	got := canonicalEntryChannelAppID("weixin", "operator-input")
	if got != "wx_app" {
		t.Fatalf("appid=%s", got)
	}
}

func weixinTestSignature(token, timestamp, nonce string) string {
	parts := []string{token, timestamp, nonce}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
