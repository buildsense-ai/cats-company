package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type fakeWeixinAPI struct {
	appID string
	qrs   []string
	sends []fakeWeixinSend
	media map[string]fakeWeixinMedia
}

type fakeWeixinSend struct {
	OpenID string
	Text   string
}

type fakeWeixinMedia struct {
	FileName    string
	ContentType string
	Body        string
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

func (f *fakeWeixinAPI) DownloadMedia(ctx context.Context, mediaID string) (*channelMediaDownload, error) {
	if f.media == nil {
		return nil, errors.New("missing media")
	}
	media, ok := f.media[mediaID]
	if !ok {
		return nil, errors.New("unknown media")
	}
	return &channelMediaDownload{
		Body:        io.NopCloser(strings.NewReader(media.Body)),
		FileName:    media.FileName,
		ContentType: media.ContentType,
	}, nil
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
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "wx_app", "openid-1"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessPublic,
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
	if binding.ActorUID <= 0 || binding.AgentUID != 43 || binding.OwnerUID != 7 || binding.CanonicalUID != 0 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if len(db.topics) != 0 {
		t.Fatalf("topics=%+v binding=%+v", db.topics, binding)
	}
	if !strings.Contains(rec.Body.String(), "Contract Agent") {
		t.Fatalf("reply=%s", rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "登录 CatsCo") || !strings.Contains(body, "/channel-device-link") || !strings.Contains(body, "binding_id=") || !strings.Contains(body, "link_token=") {
		t.Fatalf("scan reply should require CatsCo account link, body=%s", body)
	}
}

func TestWeixinMobileIdentityLinkReusesExistingCatsCoFriend(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(9, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 9)] = types.FriendAccepted
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-private-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	mobileLink, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.weixin-mobile",
		EntryID:      entry.ID,
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		CanonicalUID: 9,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create mobile link: %v", err)
	}
	sceneKey := mobileLink.SceneKey
	api := &fakeWeixinAPI{appID: "wx_app"}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, api)

	qrReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-entry/weixin-qrcode?scene_key="+url.QueryEscape(sceneKey), nil)
	qrRec := httptest.NewRecorder()
	handler.HandleQRCode(qrRec, qrReq)
	if qrRec.Code != http.StatusFound {
		t.Fatalf("qr status=%d body=%s", qrRec.Code, qrRec.Body.String())
	}
	if len(api.qrs) != 1 || api.qrs[0] != sceneKey {
		t.Fatalf("expected QR to use mobile scene, qrs=%+v scene=%s", api.qrs, sceneKey)
	}

	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-mobile]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[subscribe]]></Event><EventKey><![CDATA[qrscene_` + sceneKey + `]]></EventKey></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("scan status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-mobile",
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
	if body := rec.Body.String(); strings.Contains(body, "需要登录") || strings.Contains(body, "好友申请") || strings.Contains(body, "管理员通过") {
		t.Fatalf("mobile link should bind directly, reply=%s", body)
	}
	reused, _, err := resolveChannelIdentityMobileLink(db, sceneKey, "weixin", "wx_app", true)
	if err != nil {
		t.Fatalf("reused mobile link should not error: %v", err)
	}
	if reused != nil {
		t.Fatalf("mobile link should be consumed by scan event, reused=%+v", reused)
	}
}

func TestWeixinMobileAgentScanOverridesExistingGroupBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[99] = &types.User{ID: 99, Username: "Annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[223] = &types.User{ID: 223, Username: channelActorUsername("weixin", "wx_app", "openid-mobile"), DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.users[255] = &types.User{ID: 255, Username: "Gauz Mem", DisplayName: "Gauz Mem", AccountType: types.AccountHuman}
	db.users[256] = &types.User{ID: 256, Username: "bot-virtual-catsco", DisplayName: "Virtual Catsco", AccountType: types.AccountBot}
	db.owners[256] = 255
	db.friends[friendKey(99, 256)] = types.FriendAccepted
	db.friends[friendKey(256, 99)] = types.FriendAccepted
	db.groups[505] = &types.Group{ID: 505, Name: "Gauz Mobile Group", OwnerID: 255}
	db.groupMembers[505] = map[int64]*types.GroupMember{
		255: &types.GroupMember{GroupID: 505, UserID: 255, Role: "owner"},
		256: &types.GroupMember{GroupID: 505, UserID: 256, Role: "member"},
	}
	if _, err := db.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-mobile",
		ActorUID:      223,
		CanonicalUID:  255,
		GroupID:       505,
		TopicID:       "grp_505",
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed group binding: %v", err)
	}
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-virtual-catsco",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     255,
		AgentUID:     256,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	link, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.annika-virtual-catsco",
		EntryID:      entry.ID,
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		CanonicalUID: 99,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create mobile link: %v", err)
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})

	scanBody := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-mobile]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[subscribe]]></Event><EventKey><![CDATA[qrscene_` + link.SceneKey + `]]></EventKey></xml>`
	scanReq := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(scanBody))
	scanRec := httptest.NewRecorder()
	handler.HandleEvents(scanRec, scanReq)
	if scanRec.Code != http.StatusOK {
		t.Fatalf("scan status=%d body=%s", scanRec.Code, scanRec.Body.String())
	}
	route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-mobile",
		ChannelConversationType: "p2p",
		ActorUID:                223,
	})
	if err != nil || route == nil || route.AgentUID != 256 {
		t.Fatalf("agent scan should select Virtual Catsco route=%+v err=%v", route, err)
	}

	textBody := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-mobile]]></FromUserName><CreateTime>2</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[你是谁]]></Content><MsgId>msg-after-agent-scan</MsgId></xml>`
	textReq := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=2&nonce=3&signature="+weixinTestSignature("token-1", "2", "3"), strings.NewReader(textBody))
	textRec := httptest.NewRecorder()
	handler.HandleEvents(textRec, textReq)
	if textRec.Code != http.StatusOK {
		t.Fatalf("text status=%d body=%s", textRec.Code, textRec.Body.String())
	}
	if len(db.messages) != 1 {
		t.Fatalf("messages=%+v", db.messages)
	}
	if db.messages[0].TopicID != p2pTopicID(99, 256) || db.messages[0].FromUID != 99 {
		t.Fatalf("text should route to Annika's private agent topic, got %+v", db.messages[0])
	}
}

func TestWeixinApprovalRequiredScanCreatesPendingAccess(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "wx_app", "openid-1"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-private-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
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
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-private]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[subscribe]]></Event><EventKey><![CDATA[qrscene_scene-private-weixin]]></EventKey></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-private",
	}); err != nil || binding == nil || binding.CanonicalUID != 0 {
		t.Fatalf("private scan should only create login placeholder binding=%+v err=%v", binding, err)
	}
	access, err := db.ResolveChannelAgentAccessRequest(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-private",
	})
	if err != nil || access != nil {
		t.Fatalf("unlinked scan should not create access request, access=%+v err=%v", access, err)
	}
	if len(db.topics) != 0 {
		t.Fatalf("private scan should not create chat topic before approval: %+v", db.topics)
	}
	if body := rec.Body.String(); !strings.Contains(body, "登录 CatsCo") || !strings.Contains(body, "/channel-device-link") {
		t.Fatalf("private scan reply should request CatsCo account link, body=%s", body)
	}
}

func TestWeixinApprovedPrivateBindingProvidesDeviceLinkOnRequest(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "wx_app", "openid-1"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-private-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
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
	scanBody := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-private]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[subscribe]]></Event><EventKey><![CDATA[qrscene_scene-private-weixin]]></EventKey></xml>`
	scanReq := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(scanBody))
	scanRec := httptest.NewRecorder()
	handler.HandleEvents(scanRec, scanReq)
	if scanRec.Code != http.StatusOK {
		t.Fatalf("scan status=%d body=%s", scanRec.Code, scanRec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-private",
	})
	if err != nil || binding == nil || binding.CanonicalUID != 0 {
		t.Fatalf("expected login placeholder binding=%+v err=%v", binding, err)
	}
	token := validChannelAgentLinkToken(t, binding)
	linkBody := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + token + `"}`
	linkReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(linkBody))
	linkReq = linkReq.WithContext(context.WithValue(linkReq.Context(), uidKey, int64(9)))
	linkRec := httptest.NewRecorder()
	NewChannelAgentBindingHandler(db, nil).HandleLinkChannelAgentBindingUser(linkRec, linkReq)
	if linkRec.Code != http.StatusOK || !strings.Contains(linkRec.Body.String(), "pending_approval") {
		t.Fatalf("link status=%d body=%s", linkRec.Code, linkRec.Body.String())
	}
	if err := db.AcceptFriendRequest(9, 43); err != nil {
		t.Fatalf("accept friend: %v", err)
	}
	if _, err := db.ActivateChannelAgentBindingsForCanonicalUser(9, 43, 7); err != nil {
		t.Fatalf("activate binding: %v", err)
	}

	textBody := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-private]]></FromUserName><CreateTime>2</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[查一下合同进度]]></Content><MsgId>msg-1</MsgId></xml>`
	textReq := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=2&nonce=3&signature="+weixinTestSignature("token-1", "2", "3"), strings.NewReader(textBody))
	textRec := httptest.NewRecorder()
	handler.HandleEvents(textRec, textReq)

	if textRec.Code != http.StatusOK {
		t.Fatalf("text status=%d body=%s", textRec.Code, textRec.Body.String())
	}
	if strings.TrimSpace(textRec.Body.String()) != "success" {
		t.Fatalf("approved text should be delivered, body=%s", textRec.Body.String())
	}
	if len(db.messages) != 1 || db.messages[0].FromUID != 9 || db.messages[0].TopicID != p2pTopicID(9, 43) {
		t.Fatalf("approved channel user should continue canonical CatsCo conversation: %+v", db.messages)
	}
}

func TestWeixinRejectedChannelBindingCannotChat(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "wx_app", "openid-reject"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(9, 43)] = types.FriendPending
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-reject",
		ActorUID:      8,
		CanonicalUID:  9,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        types.ChannelAgentBindingPendingApproval,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	friendHandler := NewFriendHandler(db)
	rejectReq := httptest.NewRequest(http.MethodPost, "/api/friends/reject", strings.NewReader(`{"agent_uid":43,"user_id":9}`))
	rejectReq = rejectReq.WithContext(context.WithValue(rejectReq.Context(), uidKey, int64(7)))
	rejectRec := httptest.NewRecorder()
	friendHandler.HandleRejectRequest(rejectRec, rejectReq)
	if rejectRec.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", rejectRec.Code, rejectRec.Body.String())
	}
	binding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-reject",
	})
	if err != nil || binding == nil || binding.Status != types.ChannelAgentBindingRejected {
		t.Fatalf("binding should be rejected: binding=%+v err=%v", binding, err)
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, &fakeWeixinAPI{appID: "wx_app"})
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-reject]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[你好]]></Content><MsgId>msg-1</MsgId></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "暂未通过") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.messages) != 0 {
		t.Fatalf("rejected channel user should not deliver messages: %+v", db.messages)
	}
}

func TestWeixinScanEventForExistingSubscriberBindsActor(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "wx_app", "openid-1"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-weixin",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessPublic,
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
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		CanonicalUID:  8,
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

func TestWeixinImageMessageDeliversAttachmentBlocks(t *testing.T) {
	t.Cleanup(func() { _ = os.RemoveAll("uploads") })
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "weixin-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		CanonicalUID:  8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeWeixinAPI{
		appID: "wx_app",
		media: map[string]fakeWeixinMedia{
			"media-1": {FileName: "homework.jpg", ContentType: "image/jpeg", Body: "fake-jpeg"},
		},
	}
	handler := NewWeixinChannelHandler(db, nil, WeixinChannelConfig{
		AppID:      "wx_app",
		EventToken: "token-1",
	}, api)
	body := `<xml><ToUserName><![CDATA[gh_app]]></ToUserName><FromUserName><![CDATA[openid-1]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[image]]></MsgType><PicUrl><![CDATA[https://wx.example/image.jpg]]></PicUrl><MediaId><![CDATA[media-1]]></MediaId><MsgId>msg-img-1</MsgId></xml>`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/weixin/events?timestamp=1&nonce=2&signature="+weixinTestSignature("token-1", "1", "2"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.messages) != 1 {
		t.Fatalf("messages=%d", len(db.messages))
	}
	msg := db.messages[0]
	if msg.TopicID != "p2p_8_43" || msg.FromUID != 8 || msg.MsgType != "image" {
		t.Fatalf("message=%+v", msg)
	}
	if len(msg.ContentBlocks) != 1 || msg.ContentBlocks[0].Type != "image" {
		t.Fatalf("content blocks=%+v", msg.ContentBlocks)
	}
	payload := msg.ContentBlocks[0].Payload
	if payload["name"] != "homework.jpg" || payload["mime_type"] != "image/jpeg" {
		t.Fatalf("payload=%+v", payload)
	}
	if url, _ := payload["url"].(string); !strings.HasPrefix(url, "/uploads/images/") {
		t.Fatalf("url=%+v", payload["url"])
	}
}

func TestWeixinTextMessageDeduplicatesRetry(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "weixin-alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		CanonicalUID:  8,
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
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-1",
		ActorUID:      8,
		CanonicalUID:  8,
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

func TestWeixinOutboundUsesRecordedCanonicalReplyRoute(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-mobile"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-mobile",
		ActorUID:      100,
		CanonicalUID:  8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeWeixinAPI{appID: "wx_app"}
	dispatcher := NewChannelOutboundDispatcher(db, nil, "").WithWeixin(api, "wx_app")
	topicID := p2pTopicID(8, 43)
	dispatcher.RecordInboundReplyRoute(topicID, 8, binding)

	if err := dispatcher.ForwardBotReply(context.Background(), 8, 43, topicID, "移动端回复"); err != nil {
		t.Fatalf("forward recorded route: %v", err)
	}
	if len(api.sends) != 1 || api.sends[0].OpenID != "openid-mobile" || api.sends[0].Text != "移动端回复" {
		t.Fatalf("recorded route send=%+v", api.sends)
	}

	dispatcher.ClearInboundReplyRoute(topicID, 8, 43)
	if err := dispatcher.ForwardBotReply(context.Background(), 8, 43, topicID, "网页回复"); err != nil {
		t.Fatalf("forward after clear: %v", err)
	}
	if len(api.sends) != 1 {
		t.Fatalf("canonical web reply should not reuse mobile channel route, sends=%+v", api.sends)
	}
}

func TestWeixinInboundCanonicalDeliveryRecordsReplyRoute(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-mobile"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:             "weixin",
		ChannelAppID:        "wx_app",
		ChannelUserID:       "openid-mobile",
		ActorUID:            100,
		CanonicalUID:        8,
		OwnerUID:            7,
		AgentUID:            43,
		DeviceAccessEnabled: true,
		Status:              types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	api := &fakeWeixinAPI{appID: "wx_app"}
	hub := NewHub(db, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	device, err := hub.userDevices.register(8, RegisterUserDeviceRequest{
		DeviceID:       "alice-phone-linked-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-alice",
		InstallationID: "install-alice",
		Capabilities:   []string{"read_file", "write_file"},
	})
	if err != nil {
		t.Fatalf("register canonical user device: %v", err)
	}
	deviceClient := &Client{uid: 8, accountType: types.AccountHuman, deviceOwnerUID: 8, deviceID: "alice-phone-linked-laptop", deviceBodyID: "body-alice", deviceInstallationID: "install-alice", send: make(chan []byte, 1)}
	botClient := &Client{uid: 43, accountType: types.AccountBot, bodyID: "body-agent", send: make(chan []byte, 1)}
	hub.addClient(deviceClient)
	hub.addClient(botClient)
	hub.bindDeviceClient(8, device, deviceClient)
	dispatcher := NewChannelOutboundDispatcher(db, nil, "").WithWeixin(api, "wx_app")
	hub.SetChannelOutboundDispatcher(dispatcher)
	topicID := p2pTopicID(8, 43)

	if err := deliverInboundChannelTextToAgent(db, hub, 100, 43, "移动端继续", "wx-msg-1", "weixin", map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-mobile",
		"channel_conversation_type": "p2p",
	}); err != nil {
		t.Fatalf("deliver inbound: %v", err)
	}
	if len(db.messages) != 1 || db.messages[0].FromUID != 8 || db.messages[0].TopicID != topicID {
		t.Fatalf("mobile message should persist in canonical topic, messages=%+v", db.messages)
	}
	var inbound ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &inbound)
	identity := metadataMapFromServerMessage(t, &inbound, "catsco_identity")
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok || permissions["device_owner_user_id"] != "usr8" || permissions["device_owner_source"] != "actor" {
		t.Fatalf("canonical mobile inbound should use the CatsCo user's own device context: %#v", identity["permissions"])
	}
	grant := firstDeviceGrantMap(t, identity)
	if grant["ownerUserId"] != "usr8" || grant["actorUserId"] != "usr8" || grant["deviceId"] != "alice-phone-linked-laptop" {
		t.Fatalf("canonical mobile inbound should issue self-owner device grant: %#v", grant)
	}

	if err := dispatcher.ForwardBotReply(context.Background(), 8, 43, topicID, "继续处理"); err != nil {
		t.Fatalf("forward recorded reply: %v", err)
	}
	if len(api.sends) != 1 || api.sends[0].OpenID != "openid-mobile" || api.sends[0].Text != "继续处理" {
		t.Fatalf("mobile reply route sends=%+v", api.sends)
	}
}

func TestChannelOutboundDispatcherPreservesFeishuAndWeixin(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "feishu-alice", DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "weixin-alice", DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	db.friends[friendKey(9, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 9)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "feishu_app",
		ChannelUserID: "ou_user",
		ActorUID:      8,
		CanonicalUID:  8,
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
		CanonicalUID:  9,
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
	if resp.QRKind != "weixin_official_qr" || resp.QRValue != resp.ChannelQRURL {
		t.Fatalf("unexpected qr metadata kind=%s value=%s", resp.QRKind, resp.QRValue)
	}
}

func TestFeishuEntryResponseRequiresNativeEntryTemplate(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
	})
	if resp.FeishuOAuthURL != "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/start?scene_key=scene-feishu" {
		t.Fatalf("feishu_oauth_url=%s", resp.FeishuOAuthURL)
	}
	if resp.QRKind != "feishu_native_unconfigured" || resp.QRValue != "" {
		t.Fatalf("unexpected qr metadata kind=%s value=%s", resp.QRKind, resp.QRValue)
	}
	if resp.FeishuEntryStatus == nil || resp.FeishuEntryStatus.Ready || resp.FeishuEntryStatus.Status != "missing_native_template" {
		t.Fatalf("unexpected feishu status: %+v", resp.FeishuEntryStatus)
	}
}

func TestFeishuEntryResponseWithoutAppIDDoesNotFallBackToWebQRCode(t *testing.T) {
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey: "scene-feishu",
		Channel:  "feishu",
	})
	if resp.FeishuOAuthURL != "" || resp.ChannelQRURL != "" || resp.QRValue != "" {
		t.Fatalf("feishu entry should not expose web/oauth qr without app id: %+v", resp)
	}
	if resp.QRKind != "feishu_native_unconfigured" {
		t.Fatalf("qr_kind=%s", resp.QRKind)
	}
	if resp.FeishuEntryStatus == nil || resp.FeishuEntryStatus.Status != "missing_app_id" {
		t.Fatalf("unexpected feishu status: %+v", resp.FeishuEntryStatus)
	}
}

func TestFeishuEntryResponseUsesOAuthShortLinkForQRCode(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}&scene={scene_key}&landing={landing_url_encoded}")
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
	})
	if resp.FeishuOAuthURL != "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/start?scene_key=scene-feishu" {
		t.Fatalf("feishu_oauth_url=%s", resp.FeishuOAuthURL)
	}
	want := "https://app.catsco.cc/api/f/scene-feishu"
	if resp.ChannelQRURL != want {
		t.Fatalf("channel_qr_url=%s", resp.ChannelQRURL)
	}
	if resp.QRKind != "feishu_oauth_entry" || resp.QRValue != want {
		t.Fatalf("unexpected qr metadata kind=%s value=%s", resp.QRKind, resp.QRValue)
	}
	if resp.FeishuEntryStatus == nil || !resp.FeishuEntryStatus.Ready || resp.FeishuEntryStatus.NativeShortURL != "https://app.catsco.cc/api/fn/scene-feishu" {
		t.Fatalf("unexpected feishu status: %+v", resp.FeishuEntryStatus)
	}
	if resp.FeishuEntryStatus.NativeURL != "https://applink.feishu.cn/client/app/open?app_id=cli_app&scene=scene-feishu&landing=https%3A%2F%2Fapp.catsco.cc%2Fe%2Fscene-feishu" {
		t.Fatalf("native_url=%s", resp.FeishuEntryStatus.NativeURL)
	}
}

func TestFeishuEntryResponseRejectsNativeEntryForAppIDMismatch(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}&scene={scene_key}")
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "legacy_app",
	})
	if resp.ChannelQRURL != "" || resp.QRValue != "" || resp.QRKind != "feishu_native_unconfigured" {
		t.Fatalf("mismatched feishu app should not expose native qr: %+v", resp)
	}
	if resp.FeishuOAuthURL == "" {
		t.Fatalf("oauth auxiliary link should remain available")
	}
	if resp.FeishuEntryStatus == nil || resp.FeishuEntryStatus.Ready || resp.FeishuEntryStatus.Status != "app_mismatch" {
		t.Fatalf("unexpected feishu status: %+v", resp.FeishuEntryStatus)
	}
}

func TestFeishuEntryResponseRejectsTemplateWithoutSceneCarrier(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}")
	handler := NewChannelAgentBindingHandler(newChannelAgentTestStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/agent-entries", nil)
	resp := handler.entryResponse(req, &types.ChannelAgentEntry{
		SceneKey:     "scene-feishu",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
	})
	if resp.ChannelQRURL != "" || resp.QRValue != "" || resp.QRKind != "feishu_native_unconfigured" {
		t.Fatalf("template without scene carrier should not expose native qr: %+v", resp)
	}
	if resp.FeishuEntryStatus == nil || resp.FeishuEntryStatus.Ready || resp.FeishuEntryStatus.Status != "native_template_missing_scene" {
		t.Fatalf("unexpected feishu status: %+v", resp.FeishuEntryStatus)
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
