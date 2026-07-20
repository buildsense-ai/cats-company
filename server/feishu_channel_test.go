package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var feishuTestEventClock atomic.Int64

type fakeFeishuAPI struct {
	appID         string
	identity      *FeishuUserIdentity
	users         map[string]*FeishuUserIdentity
	sends         []fakeFeishuSend
	media         map[string]fakeFeishuMedia
	attachmentErr error
}

type fakeFeishuBotIdentityAPI struct {
	*fakeFeishuAPI
	openID string
	err    error
	calls  atomic.Int64
}

func (f *fakeFeishuBotIdentityAPI) BotOpenID(context.Context) (string, error) {
	f.calls.Add(1)
	return f.openID, f.err
}

type fakeFeishuSend struct {
	ReceiveIDType string
	ReceiveID     string
	Text          string
	MsgType       string
	Attachment    channelOutboundAttachment
}

type fakeFeishuMedia struct {
	FileName    string
	ContentType string
	Body        string
}

func (f *fakeFeishuAPI) AppID() string {
	return f.appID
}

func (f *fakeFeishuAPI) BotOpenID(context.Context) (string, error) {
	return "ou_bot", nil
}

func (f *fakeFeishuAPI) ExchangeOAuthCode(ctx context.Context, code string, redirectURI string) (*FeishuUserIdentity, error) {
	return f.identity, nil
}

func (f *fakeFeishuAPI) GetUserIdentity(ctx context.Context, openID string) (*FeishuUserIdentity, error) {
	if f.users == nil {
		return nil, errors.New("missing user identity")
	}
	identity := f.users[openID]
	if identity == nil {
		return nil, errors.New("unknown user identity")
	}
	return identity, nil
}

func (f *fakeFeishuAPI) SendTextMessage(ctx context.Context, receiveIDType string, receiveID string, text string) error {
	f.sends = append(f.sends, fakeFeishuSend{ReceiveIDType: receiveIDType, ReceiveID: receiveID, Text: text, MsgType: "text"})
	return nil
}

func (f *fakeFeishuAPI) SendAttachmentMessage(ctx context.Context, receiveIDType string, receiveID string, attachment channelOutboundAttachment) error {
	if f.attachmentErr != nil {
		return f.attachmentErr
	}
	f.sends = append(f.sends, fakeFeishuSend{ReceiveIDType: receiveIDType, ReceiveID: receiveID, MsgType: attachment.Type, Attachment: attachment})
	return nil
}

func (f *fakeFeishuAPI) DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (*channelMediaDownload, error) {
	if f.media == nil {
		return nil, errors.New("missing media")
	}
	media, ok := f.media[fileKey]
	if !ok {
		return nil, errors.New("unknown media")
	}
	return &channelMediaDownload{
		Body:        io.NopCloser(strings.NewReader(media.Body)),
		FileName:    media.FileName,
		ContentType: media.ContentType,
	}, nil
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

func TestFeishuConfiguredListTrimsAndDeduplicates(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS", " ou_one,OU_TWO；ou_one\nou_three ")

	got := feishuConfiguredList("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS")
	want := []string{"ou_one", "OU_TWO", "ou_three"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("configured list=%v want=%v", got, want)
	}
}

func TestFeishuMentionIsBotUsesConfiguredGroupIdentity(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS", "ou_primary,ou_secondary")
	t.Setenv("CATSCO_FEISHU_GROUP_BOT_ALIASES", "catsco_飞书专用,项目助手")

	mentionByID := feishuMessageMention{}
	mentionByID.ID.OpenID = "ou_secondary"
	if !feishuMentionIsBot(mentionByID) {
		t.Fatal("configured bot open ID should match")
	}
	if !feishuMentionIsBot(feishuMessageMention{Name: "项目助手"}) {
		t.Fatal("configured bot alias should match")
	}
	humanWithBotName := feishuMessageMention{Name: "catsco_飞书专用"}
	humanWithBotName.ID.OpenID = "ou_human"
	if feishuMentionIsBot(humanWithBotName) {
		t.Fatal("a formal mention with a different immutable ID must not match by display name")
	}
	if feishuMentionIsBot(feishuMessageMention{Name: "项目助手临时"}) {
		t.Fatal("bot alias should require an exact mention name")
	}
	if feishuMentionIsBot(feishuMessageMention{Name: "catsco_临时成员"}) {
		t.Fatal("an unrelated mention containing catsco must not match")
	}
}

func TestHTTPFeishuBotOpenIDCachesConcurrentDiscovery(t *testing.T) {
	var botCalls atomic.Int64
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "tenant_access_token": "tenant-token", "expire": 3600})
		case "/open-apis/bot/v3/info":
			if r.Header.Get("Authorization") != "Bearer tenant-token" {
				t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
			}
			botCalls.Add(1)
			time.Sleep(20 * time.Millisecond)
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "msg": "ok", "bot": map[string]string{"open_id": "ou_discovered_bot"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	client := newFeishuAPIClient(FeishuChannelConfig{AppID: "cli_app", AppSecret: "secret", APIBaseURL: apiServer.URL})
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			openID, err := client.BotOpenID(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if openID != "ou_discovered_bot" {
				errs <- fmt.Errorf("open_id=%q", openID)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := botCalls.Load(); got != 1 {
		t.Fatalf("bot info calls=%d, want 1", got)
	}
}

func TestHTTPFeishuBotOpenIDRetriesAfterFailure(t *testing.T) {
	var botCalls atomic.Int64
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "tenant_access_token": "tenant-token", "expire": 3600})
		case "/open-apis/bot/v3/info":
			if botCalls.Add(1) == 1 {
				writeJSON(w, http.StatusOK, map[string]interface{}{"code": 999, "msg": "temporary failure"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "bot": map[string]string{"open_id": "ou_retry_bot"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	client := newFeishuAPIClient(FeishuChannelConfig{AppID: "cli_app", AppSecret: "secret", APIBaseURL: apiServer.URL})
	if _, err := client.BotOpenID(context.Background()); err == nil {
		t.Fatal("first bot identity discovery should fail")
	}
	if _, err := client.BotOpenID(context.Background()); err == nil {
		t.Fatal("negative cache should preserve the discovery error during backoff")
	}
	if got := botCalls.Load(); got != 1 {
		t.Fatalf("bot info calls during backoff=%d, want 1", got)
	}
	client.mu.Lock()
	client.botRetryAfter = time.Time{}
	client.mu.Unlock()
	openID, err := client.BotOpenID(context.Background())
	if err != nil || openID != "ou_retry_bot" {
		t.Fatalf("retry open_id=%q err=%v", openID, err)
	}
	if got := botCalls.Load(); got != 2 {
		t.Fatalf("bot info calls=%d, want 2", got)
	}
}

func TestHTTPFeishuBotOpenIDLeaderCancellationDoesNotCancelSharedLoad(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var botCalls atomic.Int64
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "tenant_access_token": "tenant-token", "expire": 3600})
		case "/open-apis/bot/v3/info":
			if botCalls.Add(1) == 1 {
				close(started)
			}
			<-release
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "bot": map[string]string{"open_id": "ou_shared_bot"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	client := newFeishuAPIClient(FeishuChannelConfig{AppID: "cli_app", AppSecret: "secret", APIBaseURL: apiServer.URL})
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := client.BotOpenID(leaderCtx)
		leaderResult <- err
	}()
	<-started
	waiterResult := make(chan error, 1)
	go func() {
		openID, err := client.BotOpenID(context.Background())
		if err == nil && openID != "ou_shared_bot" {
			err = fmt.Errorf("open_id=%q", openID)
		}
		waiterResult <- err
	}()
	cancelLeader()
	leaderErr := <-leaderResult
	close(release)
	if !errors.Is(leaderErr, context.Canceled) {
		t.Fatalf("leader error=%v, want context canceled", leaderErr)
	}
	if err := <-waiterResult; err != nil {
		t.Fatal(err)
	}
	if got := botCalls.Load(); got != 1 {
		t.Fatalf("bot info calls=%d, want 1", got)
	}
}

func TestHTTPFeishuBotOpenIDRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   map[string]interface{}
	}{
		{name: "http error", status: http.StatusServiceUnavailable, body: map[string]interface{}{"error": "unavailable"}},
		{name: "missing open id", status: http.StatusOK, body: map[string]interface{}{"code": 0, "bot": map[string]string{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "tenant_access_token": "tenant-token", "expire": 3600})
				case "/open-apis/bot/v3/info":
					writeJSON(w, tt.status, tt.body)
				default:
					http.NotFound(w, r)
				}
			}))
			defer apiServer.Close()

			client := newFeishuAPIClient(FeishuChannelConfig{AppID: "cli_app", AppSecret: "secret", APIBaseURL: apiServer.URL})
			if openID, err := client.BotOpenID(context.Background()); err == nil {
				t.Fatalf("open_id=%q, want error", openID)
			}
		})
	}
}

func TestFeishuGroupMentionBotIdentityFailureIsRetryable(t *testing.T) {
	db := newChannelAgentTestStore()
	api := &fakeFeishuBotIdentityAPI{
		fakeFeishuAPI: &fakeFeishuAPI{appID: "cli_app", users: map[string]*FeishuUserIdentity{
			"ou_member": {OpenID: "ou_member", Name: "Member"},
		}},
		err: errors.New("bot info unavailable"),
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	rec := sendFeishuTextEventWithMentionsUnchecked(t, handler, "cli_app", "tenant_1", "ou_member", "oc_group", "group", "om_retry", "@_user_1 hello", []map[string]interface{}{
		{"key": "@_user_1", "name": "catsco_飞书专用", "id": map[string]interface{}{"open_id": "ou_bot"}},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.calls.Load() != 1 {
		t.Fatalf("bot identity calls=%d", api.calls.Load())
	}
}

func TestFeishuGroupMentionRejectsEmptyBotIdentity(t *testing.T) {
	db := newChannelAgentTestStore()
	api := &fakeFeishuBotIdentityAPI{
		fakeFeishuAPI: &fakeFeishuAPI{appID: "cli_app", users: map[string]*FeishuUserIdentity{
			"ou_member": {OpenID: "ou_member", Name: "Member"},
		}},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	rec := sendFeishuTextEventWithMentionsUnchecked(t, handler, "cli_app", "tenant_1", "ou_member", "oc_group", "group", "om_empty", "@_user_1 hello", []map[string]interface{}{
		{"key": "@_user_1", "name": "catsco_飞书专用", "id": map[string]interface{}{"open_id": "ou_bot"}},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFeishuGroupMentionRetryDeliversOnce(t *testing.T) {
	db := newChannelAgentTestStore()
	db.nextID = 100
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, _ = db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, CanonicalUID: 7, OwnerUID: 7, AgentUID: 43, Status: types.ChannelAgentBindingActive})
	_, _ = db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, AgentUID: 43, Source: "entry_scan"})
	api := &fakeFeishuBotIdentityAPI{
		fakeFeishuAPI: &fakeFeishuAPI{appID: "cli_app", users: map[string]*FeishuUserIdentity{
			"ou_owner":  {OpenID: "ou_owner", Name: "Owner"},
			"ou_member": {OpenID: "ou_member", Name: "Member"},
		}},
		openID: "ou_bot",
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_retry_group", "cli_app", "tenant_1", "ou_owner", "oc_retry_group", "Retry Group")
	binding, _ := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_retry_group")
	if binding == nil || binding.GroupID <= 0 {
		t.Fatalf("native group binding=%+v", binding)
	}
	db.groupMembers[binding.GroupID][43].IsBot = false
	hub := NewHub(db, nil)
	botClient := &Client{hub: hub, uid: 43, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.clients[43] = map[*Client]struct{}{botClient: {}}
	handler.hub = hub
	mentions := []map[string]interface{}{
		{"key": "@_user_1", "name": "catsco_飞书专用", "id": map[string]interface{}{"open_id": "ou_bot"}},
	}

	api.err = errors.New("temporary bot info failure")
	first := sendFeishuTextEventWithMentionsUnchecked(t, handler, "cli_app", "tenant_1", "ou_member", "oc_retry_group", "group", "om_retry_once", "@_user_1 hello", mentions)
	if first.Code != http.StatusInternalServerError || len(db.messages) != 0 || len(botClient.send) != 0 {
		t.Fatalf("first status=%d messages=%d bot=%d", first.Code, len(db.messages), len(botClient.send))
	}

	api.err = nil
	second := sendFeishuTextEventWithMentions(t, handler, "cli_app", "tenant_1", "ou_member", "oc_retry_group", "group", "om_retry_once", "@_user_1 hello", mentions)
	if second.Code != http.StatusOK || len(db.messages) != 1 || len(botClient.send) != 1 {
		t.Fatalf("second status=%d messages=%d bot=%d", second.Code, len(db.messages), len(botClient.send))
	}
}

func TestFeishuGroupCommandDoesNotRequireBotIdentity(t *testing.T) {
	db := newChannelAgentTestStore()
	api := &fakeFeishuBotIdentityAPI{
		fakeFeishuAPI: &fakeFeishuAPI{appID: "cli_app", users: map[string]*FeishuUserIdentity{
			"ou_member": {OpenID: "ou_member", Name: "Member"},
		}},
		err: errors.New("bot info unavailable"),
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	rec := sendFeishuTextEvent(t, handler, "cli_app", "ou_member", "oc_group", "group", "om_current", "/当前目标")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.calls.Load() != 0 {
		t.Fatalf("gateway command should not discover bot identity, calls=%d", api.calls.Load())
	}
}

func TestFeishuHandlerDiscoversBotIdentityForFormalMention(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS", "")
	api := &fakeFeishuBotIdentityAPI{fakeFeishuAPI: &fakeFeishuAPI{appID: "cli_app"}, openID: "ou_discovered_bot"}
	handler := NewFeishuChannelHandler(newChannelAgentTestStore(), nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	event := &feishuMessageEvent{}
	event.Message.Mentions = []feishuMessageMention{{Key: "@_user_1", Name: "catsco_飞书专用"}}
	event.Message.Mentions[0].ID.OpenID = "ou_discovered_bot"

	botIDs, err := handler.resolveFeishuBotOpenIDs(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !feishuEventMentionsBotWithIDs(event, botIDs) {
		t.Fatal("discovered immutable bot ID should trigger the group message")
	}
	if got := normalizeFeishuMessageMentionsWithBotIDs("@_user_1 请整理", event, botIDs); got != "请整理" {
		t.Fatalf("normalized text=%q", got)
	}
	if api.calls.Load() != 1 {
		t.Fatalf("bot identity calls=%d", api.calls.Load())
	}
}

func TestFeishuHandlerUsesConfiguredBotIdentityWithoutDiscovery(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS", "ou_configured_bot")
	api := &fakeFeishuBotIdentityAPI{fakeFeishuAPI: &fakeFeishuAPI{appID: "cli_app"}, err: errors.New("must not be called")}
	handler := NewFeishuChannelHandler(newChannelAgentTestStore(), nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	event := &feishuMessageEvent{}
	event.Message.Mentions = []feishuMessageMention{{Key: "@_user_1", Name: "catsco_飞书专用"}}
	event.Message.Mentions[0].ID.OpenID = "ou_configured_bot"

	botIDs, err := handler.resolveFeishuBotOpenIDs(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !feishuEventMentionsBotWithIDs(event, botIDs) {
		t.Fatal("configured immutable bot ID should trigger the group message")
	}
	if api.calls.Load() != 0 {
		t.Fatalf("configured ID should bypass discovery, calls=%d", api.calls.Load())
	}
}

type feishuRouteFailureStore struct {
	*channelAgentTestStore
}

func (s *feishuRouteFailureStore) UpsertChannelAgentRoute(*types.ChannelAgentRoute) (*types.ChannelAgentRoute, error) {
	return nil, errors.New("route store unavailable")
}

type feishuActorFailureStore struct {
	*channelAgentTestStore
}

func (s *feishuActorFailureStore) GetUserByUsername(string) (*types.User, error) {
	return nil, errors.New("actor store unavailable")
}

func TestHTTPFeishuSendAttachmentUploadsAndSendsNativeFile(t *testing.T) {
	const fileName = "20260714_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.txt"
	filePath := filepath.Join(uploadDir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("mkdir upload fixture: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("skill list"), 0o600); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(filePath) })

	var uploaded, sent bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "tenant_access_token": "tenant-token", "expire": 3600})
		case "/open-apis/im/v1/files":
			if r.Header.Get("Authorization") != "Bearer tenant-token" {
				t.Fatalf("upload authorization=%q", r.Header.Get("Authorization"))
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			if r.FormValue("file_type") != "stream" || r.FormValue("file_name") != "可用Skill清单.txt" {
				t.Fatalf("upload fields type=%q name=%q", r.FormValue("file_type"), r.FormValue("file_name"))
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("read multipart file: %v", err)
			}
			defer file.Close()
			body, _ := io.ReadAll(file)
			if header.Filename != "可用Skill清单.txt" || string(body) != "skill list" {
				t.Fatalf("uploaded file name=%q body=%q", header.Filename, body)
			}
			uploaded = true
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0, "data": map[string]string{"file_key": "file-key-1"}})
		case "/open-apis/im/v1/messages":
			var payload struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode send payload: %v", err)
			}
			if r.URL.Query().Get("receive_id_type") != "chat_id" || payload.ReceiveID != "oc_native" || payload.MsgType != "file" || !strings.Contains(payload.Content, "file-key-1") {
				t.Fatalf("send query=%q payload=%+v", r.URL.RawQuery, payload)
			}
			sent = true
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	client := newFeishuAPIClient(FeishuChannelConfig{AppID: "cli_app", AppSecret: "secret", APIBaseURL: apiServer.URL})
	client.http = apiServer.Client()
	err := client.SendAttachmentMessage(context.Background(), "chat_id", "oc_native", channelOutboundAttachment{
		Type: "file", Name: "可用Skill清单.txt", URL: "/uploads/files/" + fileName, MimeType: "text/plain",
	})
	if err != nil {
		t.Fatalf("send attachment: %v", err)
	}
	if !uploaded || !sent {
		t.Fatalf("uploaded=%t sent=%t", uploaded, sent)
	}
}

func TestHTTPFeishuSendAttachmentRejectsUploadSymlink(t *testing.T) {
	const fileName = "20260714_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.txt"
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(uploadDir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(linkPath) })
	client := newFeishuAPIClient(FeishuChannelConfig{AppID: "cli_app", AppSecret: "secret"})
	err := client.SendAttachmentMessage(context.Background(), "chat_id", "oc_native", channelOutboundAttachment{Type: "file", Name: "outside.txt", URL: "/uploads/files/" + fileName})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink attachment error=%v", err)
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
	if body := rec.Body.String(); !strings.Contains(body, "登录 CatsCo") || !strings.Contains(body, "/channel-account-link") || !strings.Contains(body, "binding_id=") || !strings.Contains(body, "link_token=") {
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
	if len(api.sends) != 1 {
		t.Fatalf("expected binding welcome message, sends=%+v", api.sends)
	}
	if send := api.sends[0]; send.ReceiveIDType != "open_id" || send.ReceiveID != "ou_mobile" || !strings.Contains(send.Text, "Contract Agent") || !strings.Contains(send.Text, "虚拟员工") {
		t.Fatalf("unexpected binding welcome message: %+v", send)
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
	api.identity = &FeishuUserIdentity{OpenID: "ou_second", UserID: "user_second", Name: "Second Scanner"}
	second := httptest.NewRecorder()
	handler.HandleOAuthCallback(second, httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-2&state="+state, nil))
	if second.Code != http.StatusNotFound {
		t.Fatalf("second scanner status=%d body=%s", second.Code, second.Body.String())
	}
	secondBinding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_second",
	})
	if err != nil || secondBinding != nil {
		t.Fatalf("consumed mobile link must not bind a second identity: binding=%+v err=%v", secondBinding, err)
	}
}

func TestFeishuOAuthCallbackConsumesMobileLinkBeforeRouteMutation(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(9, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 9)] = types.FriendAccepted
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey: "scene-route-failure", Channel: "feishu", ChannelAppID: "cli_app",
		AccessMode: types.ChannelAgentAccessApprovalRequired, OwnerUID: 7, AgentUID: 43, Status: "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	link, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey: "m.route-failure", EntryID: entry.ID, Channel: "feishu", ChannelAppID: "cli_app",
		CanonicalUID: 9, ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create mobile link: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app", identity: &FeishuUserIdentity{OpenID: "ou_mobile", UserID: "user_mobile", Name: "Alice"}}
	handler := NewFeishuChannelHandler(&feishuRouteFailureStore{channelAgentTestStore: db}, nil, FeishuChannelConfig{
		AppID: "cli_app", AppSecret: "secret", OAuthRedirectURI: "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback",
	}, api)
	state, err := handler.signOAuthState(feishuOAuthState{SceneKey: link.SceneKey, ExpiresAt: time.Now().Add(time.Minute).Unix(), Nonce: "nonce"})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.HandleOAuthCallback(rec, httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-1&state="+state, nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := db.GetChannelIdentityMobileLink(link.SceneKey)
	if err != nil || stored == nil || stored.Status != "consumed" || stored.ConsumedAt == nil {
		t.Fatalf("one-time mobile link must remain consumed after a downstream failure, link=%+v err=%v", stored, err)
	}
}

func TestFeishuOAuthCallbackMobileIdentityLinkRejectsDifferentCatsCoUserAfterClaim(t *testing.T) {
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
	if got := db.mobileLinks[mobileLink.SceneKey]; got == nil || got.Status != "consumed" || got.ConsumedAt == nil {
		t.Fatalf("claimed one-time mobile link must not be reusable after a binding conflict, got=%+v", got)
	}
}

func TestFeishuGroupOAuthCallbackClaimsOneTimeLinkBeforeBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	groupID, err := db.CreateGroup("英语备课组", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	link, err := db.CreateChannelGroupMobileLink(&types.ChannelGroupMobileLink{
		SceneKey: "g.feishu-once", Channel: "feishu", ChannelAppID: "cli_app", CanonicalUID: 7,
		GroupID: groupID, TopicID: fmt.Sprintf("grp_%d", groupID), ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create group mobile link: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app", identity: &FeishuUserIdentity{OpenID: "ou_first", Name: "First Scanner"}}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID: "cli_app", AppSecret: "secret", OAuthRedirectURI: "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback",
	}, api)
	state, err := handler.signOAuthState(feishuOAuthState{SceneKey: link.SceneKey, ExpiresAt: time.Now().Add(time.Minute).Unix(), Nonce: "nonce"})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	first := httptest.NewRecorder()
	handler.HandleOAuthCallback(first, httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-1&state="+state, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first scanner status=%d body=%s", first.Code, first.Body.String())
	}

	api.identity = &FeishuUserIdentity{OpenID: "ou_second", Name: "Second Scanner"}
	second := httptest.NewRecorder()
	handler.HandleOAuthCallback(second, httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/oauth/feishu/callback?code=code-2&state="+state, nil))
	if second.Code != http.StatusNotFound {
		t.Fatalf("second scanner status=%d body=%s", second.Code, second.Body.String())
	}
	secondBinding, err := db.ResolveChannelGroupBinding(types.ChannelGroupBindingQuery{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_second", ChannelConversationType: "p2p",
	})
	if err != nil || secondBinding != nil {
		t.Fatalf("consumed group link must not bind a second identity: binding=%+v err=%v", secondBinding, err)
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

func TestFeishuOAuthStartUsesIndexAuthorizeURL(t *testing.T) {
	tests := []struct {
		name         string
		authorizeURL string
	}{
		{name: "default"},
		{name: "legacy accounts authorize url", authorizeURL: "https://accounts.feishu.cn/open-apis/authen/v1/authorize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				AppID:             "cli_app",
				AppSecret:         "secret",
				OAuthAuthorizeURL: tt.authorizeURL,
			}, &fakeFeishuAPI{appID: "cli_app"})
			req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/start?scene_key=scene-feishu", nil)
			rec := httptest.NewRecorder()
			handler.HandleOAuthStart(rec, req)

			if rec.Code != http.StatusFound {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			loc := rec.Header().Get("Location")
			parsed, err := url.Parse(loc)
			if err != nil {
				t.Fatalf("parse location %q: %v", loc, err)
			}
			if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != "https://open.feishu.cn/open-apis/authen/v1/index" {
				t.Fatalf("authorize url=%s", got)
			}
			q := parsed.Query()
			if q.Get("app_id") != "cli_app" {
				t.Fatalf("app_id=%s", q.Get("app_id"))
			}
			if q.Get("redirect_uri") != "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/callback" {
				t.Fatalf("redirect_uri=%s", q.Get("redirect_uri"))
			}
			if q.Get("state") == "" {
				t.Fatalf("state is empty")
			}
		})
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
	wantOAuth := "https://app.catsco.cc/api/channel-agent-bindings/oauth/feishu/start?scene_key=scene-feishu"
	want := "https://applink.feishu.cn/client/app/open?app_id=cli_app&scene=scene-feishu&oauth=" + url.QueryEscape(wantOAuth)
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("redirect=%s", got)
	}
}

func TestFeishuNativeEntryShortLinkRedirectsMobileLinkToNativeEntry(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}&scene={scene_key}")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "virtual-catsco", DisplayName: "Virtual Catsco", AccountType: types.AccountBot}
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
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{
		AppID: "cli_app",
	}, &fakeFeishuAPI{appID: "cli_app"})
	req := httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/api/fn/"+mobileLink.SceneKey, nil)
	rec := httptest.NewRecorder()
	handler.HandleNativeEntryShortLink(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := "https://applink.feishu.cn/client/app/open?app_id=cli_app&scene=m.feishu-mobile"
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

func TestFeishuFileMessageDeliversAttachmentBlocks(t *testing.T) {
	os.RemoveAll("uploads")
	t.Cleanup(func() { os.RemoveAll("uploads") })

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
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
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
	api := &fakeFeishuAPI{
		appID: "cli_app",
		media: map[string]fakeFeishuMedia{
			"file-key-1": {
				FileName:    "contract.pdf",
				ContentType: "application/pdf",
				Body:        "%PDF-1.4 fake",
			},
		},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	content, _ := json.Marshal(map[string]interface{}{
		"file_key":  "file-key-1",
		"file_name": "contract.pdf",
		"size":      13,
	})
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
				"message_id":   "om_file_1",
				"chat_id":      "oc_chat_1",
				"chat_type":    "p2p",
				"message_type": "file",
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
	msg := db.messages[0]
	if msg.TopicID != "p2p_8_43" || msg.FromUID != 8 || msg.MsgType != "file" {
		t.Fatalf("message=%+v", msg)
	}
	if len(msg.ContentBlocks) != 1 || msg.ContentBlocks[0].Type != "file" {
		t.Fatalf("content blocks=%+v", msg.ContentBlocks)
	}
	payload := msg.ContentBlocks[0].Payload
	if payload["name"] != "contract.pdf" || payload["mime_type"] != "application/pdf" {
		t.Fatalf("payload=%+v", payload)
	}
	url, _ := payload["url"].(string)
	if !strings.HasPrefix(url, "/uploads/files/") {
		t.Fatalf("url=%q", url)
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
	if len(api.sends) != 1 || !strings.Contains(api.sends[0].Text, "点击「移动端使用」") {
		t.Fatalf("send=%+v", api.sends)
	}
}

func TestFeishuRosterCommandRequiresCatsCoScan(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "private-agent", DisplayName: "Private Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "public-agent", DisplayName: "Public Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	if _, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-private",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	}); err != nil {
		t.Fatalf("seed private entry: %v", err)
	}
	if _, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-public",
		Channel:      "feishu",
		ChannelAppID: "cli_app",
		AccessMode:   types.ChannelAgentAccessPublic,
		OwnerUID:     7,
		AgentUID:     44,
		Status:       "active",
	}); err != nil {
		t.Fatalf("seed public entry: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_new", "oc_chat_1", "p2p", "om_list", "员工列表")
	if len(api.sends) != 1 {
		t.Fatalf("sends=%+v", api.sends)
	}
	reply := api.sends[0].Text
	if !strings.Contains(reply, "点击「移动端使用」") {
		t.Fatalf("reply should ask user to scan from CatsCo: %s", reply)
	}
	if strings.Contains(reply, "Public Agent") || strings.Contains(reply, "Private Agent") {
		t.Fatalf("roster command should not list virtual employees: %s", reply)
	}
}

func TestFeishuNumberSelectionRequiresCatsCoScan(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "owned-agent", DisplayName: "Owned Agent", AccountType: types.AccountBot}
	db.owners[43] = 9
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_owner",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            9,
		OwnerUID:                9,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed canonical identity: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_owner", "oc_chat_1", "p2p", "om_list", "员工列表")
	if len(api.sends) != 1 || !strings.Contains(api.sends[0].Text, "点击「移动端使用」") || strings.Contains(api.sends[0].Text, "Owned Agent") {
		t.Fatalf("list command should require CatsCo scan, sends=%+v", api.sends)
	}
	sendFeishuTextEvent(t, handler, "cli_app", "ou_owner", "oc_chat_1", "p2p", "om_select", "1")
	if len(api.sends) != 2 || !strings.Contains(api.sends[1].Text, "点击「移动端使用」") || strings.Contains(api.sends[1].Text, "Owned Agent") {
		t.Fatalf("number selection should require CatsCo scan, sends=%+v", api.sends)
	}
	entries, err := db.ListChannelAgentEntries(9, 43)
	if err != nil || len(entries) != 0 {
		t.Fatalf("number selection should not create feishu entry, entries=%+v err=%v", entries, err)
	}
	route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_owner",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
	})
	if err != nil || route != nil {
		t.Fatalf("number selection should not create current route, route=%+v err=%v", route, err)
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

func TestFeishuAgentRouteOverridesExistingGroupBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", DisplayName: "Arrow", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "bot-village-chief", DisplayName: "烙馍村村长", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu User", AccountType: types.AccountHuman}
	db.owners[218] = 85
	db.friends[friendKey(85, 218)] = types.FriendAccepted
	db.friends[friendKey(218, 85)] = types.FriendAccepted
	db.groups[500] = &types.Group{ID: 500, Name: "解答万物", OwnerID: 85}
	db.groupMembers[500] = map[int64]*types.GroupMember{
		85:  &types.GroupMember{GroupID: 500, UserID: 85, Role: "owner"},
		218: &types.GroupMember{GroupID: 500, UserID: 218, Role: "member"},
	}
	if _, err := db.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      453,
		CanonicalUID:  85,
		GroupID:       500,
		TopicID:       "grp_500",
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed group binding: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                453,
		AgentUID:                218,
		Source:                  "oauth",
	}); err != nil {
		t.Fatalf("seed stale conversation route: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                453,
		CanonicalUID:            85,
		OwnerUID:                85,
		AgentUID:                218,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed agent binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                453,
		AgentUID:                218,
		Source:                  "oauth",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	groupBinding, err := db.ResolveChannelGroupBinding(types.ChannelGroupBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                453,
	})
	if err != nil || groupBinding == nil || groupBinding.Status != types.ChannelAgentBindingRevoked {
		t.Fatalf("agent scan must persistently revoke the previous group target, binding=%+v err=%v", groupBinding, err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_after_agent_scan", "你好")
	if len(db.messages) != 1 {
		t.Fatalf("messages=%+v", db.messages)
	}
	if db.messages[0].TopicID != "p2p_85_218" || db.messages[0].FromUID != 85 || db.messages[0].Content != "你好" {
		t.Fatalf("agent route should override older group binding, message=%+v", db.messages[0])
	}
}

func TestFeishuCurrentCommandDoesNotEnterBoundCatsCoGroup(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", DisplayName: "Arrow", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "bot-loginspector-3363", DisplayName: "log_inspector", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu User", AccountType: types.AccountHuman}
	db.groups[500] = &types.Group{ID: 500, Name: "查云端log", OwnerID: 85}
	db.groupMembers[500] = map[int64]*types.GroupMember{
		85:  {GroupID: 500, UserID: 85, Role: "owner"},
		218: {GroupID: 500, UserID: 218, Role: "member"},
	}
	if _, err := db.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                453,
		CanonicalUID:            85,
		GroupID:                 500,
		TopicID:                 "grp_500",
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed group binding: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_current", "/当前目标")
	if len(db.messages) != 0 {
		t.Fatalf("gateway command must not enter CatsCo group history: %+v", db.messages)
	}
	if len(api.sends) != 1 || api.sends[0].ReceiveIDType != "open_id" || api.sends[0].ReceiveID != "ou_user" ||
		!strings.Contains(api.sends[0].Text, "当前目标是 CatsCo 群聊「查云端log」") ||
		!strings.Contains(api.sends[0].Text, "群内虚拟员工：log_inspector") {
		t.Fatalf("current target reply=%+v", api.sends)
	}
	sendFeishuTextEventWithMentions(t, handler, "cli_app", "", "ou_user", "oc_chat_1", "p2p", "om_other_mention", "@_user_1 看看最近的日志", []map[string]interface{}{
		{"key": "@_user_1", "name": "陈大为", "id": map[string]interface{}{"open_id": "ou_chen"}},
	})
	if len(db.messages) != 1 || db.messages[0].Content != "@陈大为 看看最近的日志" {
		t.Fatalf("private chat must preserve non-bot mention and enter target: %+v", db.messages)
	}
}

func TestFeishuBoundOfflineAgentReturnsUnavailable(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
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
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
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
		Source:                  "entry_scan",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, NewHub(db, nil), FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_offline", "查合同")
	if len(db.messages) != 0 {
		t.Fatalf("offline agent should not receive message: %+v", db.messages)
	}
	if len(api.sends) != 1 || !strings.Contains(api.sends[0].Text, "虚拟员工暂时不可用") {
		t.Fatalf("send=%+v", api.sends)
	}
}

func TestFeishuOAuthBaseRouteOverridesOlderP2PRoute(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "dev-agent", DisplayName: "Dev Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "virtual-catsco", DisplayName: "Virtual Catsco", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	db.friends[friendKey(8, 44)] = types.FriendAccepted
	db.friends[friendKey(44, 8)] = types.FriendAccepted
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
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed old conversation binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                43,
		Source:                  "manual",
	}); err != nil {
		t.Fatalf("seed old conversation route: %v", err)
	}
	oldRouteKey := routeKey("feishu", "cli_app", "ou_user", "oc_chat_1", "p2p")
	if route := db.routes[oldRouteKey]; route != nil {
		route.SelectedAt = time.Now().Add(-time.Hour)
		route.UpdatedAt = route.SelectedAt
	}
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                44,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed newer oauth base binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                44,
		Source:                  "oauth",
	}); err != nil {
		t.Fatalf("seed newer oauth base route: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_after_oauth_scan", "你是谁")
	if len(db.messages) != 1 || db.messages[0].TopicID != "p2p_8_44" || db.messages[0].Content != "你是谁" {
		t.Fatalf("message should follow newer oauth base route: %+v", db.messages)
	}
	route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
	})
	if err != nil || route == nil || route.AgentUID != 44 {
		t.Fatalf("conversation route should be refreshed to scanned agent, got %+v err=%v", route, err)
	}
}

func TestFeishuGatewaySwitchCommandRequiresCatsCoScan(t *testing.T) {
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
	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_switch_b", "切换到 Finance Agent")
	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_msg_b", "查报销")
	if len(db.messages) != 0 {
		t.Fatalf("switch command should not select or deliver without CatsCo scan: %+v", db.messages)
	}
	if len(api.sends) != 4 {
		t.Fatalf("expected guidance replies, sends=%+v", api.sends)
	}
	for _, send := range api.sends {
		if !strings.Contains(send.Text, "点击「移动端使用」") || strings.Contains(send.Text, "已切换到") {
			t.Fatalf("unexpected switch guidance: %+v", send)
		}
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
	route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
	})
	if err != nil || route != nil {
		t.Fatalf("switch command should not create current route, route=%+v err=%v", route, err)
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
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "chat-1",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     false,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed stale conversation binding: %v", err)
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
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p", ActorUID: 8, AgentUID: 43,
	}); err != nil {
		t.Fatalf("seed route: %v", err)
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

func TestFeishuOutboundForwardsWebReplyByCanonicalBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", DisplayName: "Arrow", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "bot-village-chief", DisplayName: "烙馍村村长", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu User", AccountType: types.AccountHuman}
	db.owners[218] = 85
	db.friends[friendKey(85, 218)] = types.FriendAccepted
	db.friends[friendKey(218, 85)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                453,
		CanonicalUID:            85,
		OwnerUID:                85,
		AgentUID:                218,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p", ActorUID: 453, AgentUID: 218,
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")

	if err := dispatcher.ForwardBotReply(context.Background(), 85, 218, "p2p_85_218", "网页端私聊回复"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(api.sends) != 1 {
		t.Fatalf("sends=%+v", api.sends)
	}
	if api.sends[0].ReceiveIDType != "open_id" || api.sends[0].ReceiveID != "ou_user" || api.sends[0].Text != "网页端私聊回复" {
		t.Fatalf("send=%+v", api.sends[0])
	}
}

func TestFeishuRecordedReplyRouteStopsAfterPrivateRouteRemoval(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "agent", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), AccountType: types.AccountHuman}
	db.owners[218] = 85
	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p",
		ActorUID: 453, CanonicalUID: 85, OwnerUID: 85, AgentUID: 218, Status: types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p", ActorUID: 453, AgentUID: 218,
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")
	dispatcher.RecordInboundReplyRoute("p2p_85_218", 85, binding)

	delete(db.routes, routeKey("feishu", "cli_app", "ou_user", "", "p2p"))
	if err := dispatcher.ForwardBotReply(context.Background(), 85, 218, "p2p_85_218", "reply after unbind"); err != nil {
		t.Fatalf("forward after route removal: %v", err)
	}
	if len(api.sends) != 0 {
		t.Fatalf("stale recorded route must not send after unbind: %+v", api.sends)
	}
}

func TestFeishuWebReplySkipsAgentSupersededByAnotherAgent(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "agent-a", AccountType: types.AccountBot}
	db.users[219] = &types.User{ID: 219, Username: "agent-b", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), AccountType: types.AccountHuman}
	for _, agentUID := range []int64{218, 219} {
		db.owners[agentUID] = 85
		db.friends[friendKey(85, agentUID)] = types.FriendAccepted
		db.friends[friendKey(agentUID, 85)] = types.FriendAccepted
		if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
			Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p",
			ActorUID: 453, CanonicalUID: 85, OwnerUID: 85, AgentUID: agentUID, Status: types.ChannelAgentBindingActive,
		}); err != nil {
			t.Fatalf("seed agent %d binding: %v", agentUID, err)
		}
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p", ActorUID: 453, AgentUID: 219,
	}); err != nil {
		t.Fatalf("select agent B: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")

	if err := dispatcher.ForwardBotReply(context.Background(), 85, 218, "p2p_85_218", "旧员工网页回复"); err != nil {
		t.Fatalf("forward old agent reply: %v", err)
	}
	if len(api.sends) != 0 {
		t.Fatalf("superseded agent reply must not reach Feishu: %+v", api.sends)
	}
}

func TestFeishuWebReplySkipsAgentSupersededByGroup(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "agent-a", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), AccountType: types.AccountHuman}
	db.groups[500] = &types.Group{ID: 500, Name: "查云端log", OwnerID: 85}
	db.groupMembers[500] = map[int64]*types.GroupMember{85: {GroupID: 500, UserID: 85, Role: "owner"}, 218: {GroupID: 500, UserID: 218, Role: "member"}}
	db.owners[218] = 85
	db.friends[friendKey(85, 218)] = types.FriendAccepted
	db.friends[friendKey(218, 85)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p",
		ActorUID: 453, CanonicalUID: 85, OwnerUID: 85, AgentUID: 218, Status: types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p", ActorUID: 453, AgentUID: 218,
	}); err != nil {
		t.Fatalf("select agent: %v", err)
	}
	if _, err := db.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p",
		ActorUID: 453, CanonicalUID: 85, GroupID: 500, TopicID: "grp_500", Status: types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("select group: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")

	if err := dispatcher.ForwardBotReply(context.Background(), 85, 218, "p2p_85_218", "旧员工网页回复"); err != nil {
		t.Fatalf("forward old agent reply: %v", err)
	}
	if len(api.sends) != 0 {
		t.Fatalf("agent reply superseded by group must not reach Feishu: %+v", api.sends)
	}
	if route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_user", ChannelConversationType: "p2p", ActorUID: 453,
	}); err != nil || route != nil {
		t.Fatalf("group selection must clear the previous agent route, route=%+v err=%v", route, err)
	}
}

func TestChannelAgentRouteDoesNotWinTimestampTieWithGroup(t *testing.T) {
	selectedAt := time.Now()
	route := &types.ChannelAgentRoute{AgentUID: 218, SelectedAt: selectedAt}
	group := &types.ChannelGroupBinding{GroupID: 500, SelectedAt: selectedAt}
	if channelAgentRouteSelectedAfterGroup(route, group) {
		t.Fatal("a timestamp tie must not let the stale agent route override the group")
	}
}

func TestFeishuGroupOutboundSkipsOlderGroupBindingAfterAgentScan(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[85] = &types.User{ID: 85, Username: "arrowhaken", DisplayName: "Arrow", AccountType: types.AccountHuman}
	db.users[218] = &types.User{ID: 218, Username: "bot-village-chief", DisplayName: "烙馍村村长", AccountType: types.AccountBot}
	db.users[453] = &types.User{ID: 453, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu User", AccountType: types.AccountHuman}
	db.groups[500] = &types.Group{ID: 500, Name: "解答万物", OwnerID: 85}
	db.groupMembers[500] = map[int64]*types.GroupMember{
		85:  &types.GroupMember{GroupID: 500, UserID: 85, Role: "owner"},
		218: &types.GroupMember{GroupID: 500, UserID: 218, Role: "member"},
	}
	if _, err := db.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      453,
		CanonicalUID:  85,
		GroupID:       500,
		TopicID:       "grp_500",
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed group binding: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                453,
		AgentUID:                218,
		Source:                  "oauth",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")

	if err := dispatcher.ForwardGroupBotReply(context.Background(), 218, "grp_500", "网页端群聊回复"); err != nil {
		t.Fatalf("forward group: %v", err)
	}
	if len(api.sends) != 0 {
		t.Fatalf("older group binding should be superseded by later agent scan, sends=%+v", api.sends)
	}
}

func TestFeishuConversationBindingInheritsDeviceAccess(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     false,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		AgentUID:                43,
		Source:                  "oauth",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})

	binding, err := handler.resolveCurrentFeishuBinding("cli_app", "ou_user", "chat-1", "p2p", 100)
	if err != nil {
		t.Fatalf("resolve binding: %v", err)
	}
	if binding == nil || binding.ChannelConversationID != "chat-1" || binding.CanonicalUID != 8 || !binding.DeviceAccessEnabled {
		t.Fatalf("conversation binding should inherit canonical device access: %+v", binding)
	}
}

func TestFeishuP2PScanRouteOverridesStaleConversationRoute(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "dev-agent", DisplayName: "Dev Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "virtual-catsco", DisplayName: "Virtual Catsco", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	db.friends[friendKey(8, 44)] = types.FriendAccepted
	db.friends[friendKey(44, 8)] = types.FriendAccepted
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
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed stale conversation binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                43,
		Source:                  "manual",
	}); err != nil {
		t.Fatalf("seed stale conversation route: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                44,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed fresh base binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                44,
		Source:                  "entry_scan",
	}); err != nil {
		t.Fatalf("seed fresh base route: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_after_scan", "你好")
	if len(db.messages) != 1 || db.messages[0].TopicID != "p2p_8_44" || db.messages[0].Content != "你好" {
		t.Fatalf("message should follow latest scanned agent route: %+v", db.messages)
	}
	route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
	})
	if err != nil || route == nil || route.AgentUID != 44 {
		t.Fatalf("conversation route should switch to scanned agent, got %+v err=%v", route, err)
	}
}

func TestFeishuP2PNewerConversationRouteKeepsManualSelection(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "dev-agent", DisplayName: "Dev Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "virtual-catsco", DisplayName: "Virtual Catsco", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	db.friends[friendKey(8, 44)] = types.FriendAccepted
	db.friends[friendKey(44, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                44,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed base binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                44,
		Source:                  "entry_scan",
	}); err != nil {
		t.Fatalf("seed base route: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
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
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed manual binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            "cli_app",
		ChannelUserID:           "ou_user",
		ChannelConversationID:   "oc_chat_1",
		ChannelConversationType: "p2p",
		ActorUID:                8,
		AgentUID:                43,
		Source:                  "manual",
	}); err != nil {
		t.Fatalf("seed manual route: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})

	sendFeishuTextEvent(t, handler, "cli_app", "ou_user", "oc_chat_1", "p2p", "om_manual", "继续")
	if len(db.messages) != 1 || db.messages[0].TopicID != "p2p_8_43" || db.messages[0].Content != "继续" {
		t.Fatalf("newer manual route should keep current conversation agent: %+v", db.messages)
	}
}

func TestFeishuBotAddedCreatesIndependentGroupFromCurrentAgent(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "log-inspector", DisplayName: "log_inspector", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "saturday", DisplayName: "Saturday", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p",
		ActorUID: 8, CanonicalUID: 7, OwnerUID: 7, AgentUID: 43, Status: types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p",
		ActorUID: 8, AgentUID: 43, Source: "entry_scan",
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_add_1", "cli_app", "tenant_1", "ou_owner", "oc_native_1", "英语年级组")
	binding, err := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_native_1")
	if err != nil || binding == nil || binding.Status != types.ChannelNativeGroupActive || binding.GroupID <= 0 {
		t.Fatalf("native binding=%+v err=%v", binding, err)
	}
	group := db.groups[binding.GroupID]
	if group == nil || group.OwnerID != 7 || group.Name != "飞书｜英语年级组" {
		t.Fatalf("group=%+v", group)
	}
	if member := db.groupMembers[binding.GroupID][43]; member == nil || !member.IsBot {
		t.Fatalf("log_inspector should be inherited: %+v", db.groupMembers[binding.GroupID])
	}
	if db.groupMembers[binding.GroupID][44] != nil {
		t.Fatalf("unselected agent must not be inherited")
	}
	groupCount := len(db.groups)
	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_add_2", "cli_app", "tenant_1", "ou_owner", "oc_native_1", "英语年级组")
	if len(db.groups) != groupCount {
		t.Fatalf("repeated add created duplicate group: before=%d after=%d", groupCount, len(db.groups))
	}
	if len(api.sends) != 2 || api.sends[0].ReceiveIDType != "chat_id" || api.sends[0].ReceiveID != "oc_native_1" ||
		!strings.Contains(api.sends[0].Text, "本群已接入虚拟员工「log_inspector」") ||
		!strings.Contains(api.sends[0].Text, "https://app.catsco.cc") ||
		!strings.Contains(api.sends[0].Text, "发送 /当前目标") {
		t.Fatalf("welcome sends=%+v", api.sends)
	}
	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_add_2", "cli_app", "tenant_1", "ou_owner", "oc_native_1", "英语年级组")
	if len(api.sends) != 2 {
		t.Fatalf("duplicate event id must not send another welcome: %+v", api.sends)
	}

	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.deleted_v1", "evt_delete_1", "cli_app", "tenant_1", "ou_owner", "oc_native_1", "英语年级组")
	binding, _ = db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_native_1")
	if binding == nil || binding.Status != types.ChannelNativeGroupDisconnected {
		t.Fatalf("deleted binding=%+v", binding)
	}
}

func TestFeishuBotMembershipIgnoresStaleAddAfterDelete(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, _ = db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, CanonicalUID: 7, OwnerUID: 7, AgentUID: 43, Status: types.ChannelAgentBindingActive})
	_, _ = db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, AgentUID: 43, Source: "entry_scan"})
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuBotMembershipEventAt(t, handler, "im.chat.member.bot.added_v1", "evt_add_old", 1000, "cli_app", "tenant_1", "ou_owner", "oc_ordered", "英语组")
	sendFeishuBotMembershipEventAt(t, handler, "im.chat.member.bot.deleted_v1", "evt_delete_new", 1000, "cli_app", "tenant_1", "ou_owner", "oc_ordered", "英语组")
	binding, _ := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_ordered")
	if binding == nil || binding.Status != types.ChannelNativeGroupDisconnected {
		t.Fatalf("delete should disconnect native group: %+v", binding)
	}
	sendsAfterDelete := len(api.sends)

	sendFeishuBotMembershipEventAt(t, handler, "im.chat.member.bot.added_v1", "evt_add_old", 1000, "cli_app", "tenant_1", "ou_owner", "oc_ordered", "英语组")
	binding, _ = db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_ordered")
	if binding == nil || binding.Status != types.ChannelNativeGroupDisconnected || len(api.sends) != sendsAfterDelete {
		t.Fatalf("stale add must not reactivate or send welcome: binding=%+v sends=%d want=%d", binding, len(api.sends), sendsAfterDelete)
	}

	sendFeishuBotMembershipEventAt(t, handler, "im.chat.member.bot.added_v1", "evt_add_new", 4000, "cli_app", "tenant_1", "ou_owner", "oc_ordered", "英语组")
	binding, _ = db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_ordered")
	if binding == nil || binding.Status != types.ChannelNativeGroupActive || len(api.sends) != sendsAfterDelete+1 {
		t.Fatalf("newer add should restore the managed group: binding=%+v sends=%d", binding, len(api.sends))
	}
}

func TestFeishuBotMembershipFailureReturnsRetryableErrorAndReleasesClaim(t *testing.T) {
	db := newChannelAgentTestStore()
	handler := NewFeishuChannelHandler(&feishuActorFailureStore{channelAgentTestStore: db}, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})
	eventBody := map[string]interface{}{
		"schema": "2.0",
		"header": map[string]interface{}{
			"event_type": "im.chat.member.bot.added_v1", "event_id": "evt_retry", "create_time": "1000",
			"app_id": "cli_app", "tenant_key": "tenant_1",
		},
		"event": map[string]interface{}{
			"chat_id": "oc_retry_failure", "operator_id": map[string]interface{}{"open_id": "ou_owner"}, "name": "英语组",
		},
	}
	body, _ := json.Marshal(eventBody)
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed membership initialization must remain retryable: status=%d body=%s", rec.Code, rec.Body.String())
	}
	identity := &types.ChannelNativeGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_app", TenantKey: "tenant_1", ConversationID: "oc_retry_failure",
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(identity, true, "evt_retry", 1_000_000); err != nil || !applied {
		t.Fatalf("failed handler must release the event claim: applied=%v err=%v", applied, err)
	}
}

func TestFeishuNativeGroupCreationDoesNotOverrideNewerDelete(t *testing.T) {
	db := newChannelAgentTestStore()
	identity := &types.ChannelNativeGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_app", TenantKey: "tenant_1",
		ConversationID: "oc_race", ConversationName: "飞书｜英语组", OperatorChannelUserID: "ou_owner",
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(identity, true, "evt_add", 1000); err != nil || !applied {
		t.Fatalf("apply add: applied=%v err=%v", applied, err)
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(identity, false, "evt_delete", 2000); err != nil || !applied {
		t.Fatalf("apply delete: applied=%v err=%v", applied, err)
	}
	materialized := cloneNativeGroupBinding(identity)
	materialized.CanonicalUID = 7
	materialized.SourceAgentUID = 43
	result, created, err := db.EnsureChannelNativeGroup(materialized, materialized.ConversationName, []int64{43})
	if err != nil {
		t.Fatalf("ensure stale add: %v", err)
	}
	if created || result == nil || result.Status != types.ChannelNativeGroupDisconnected || result.GroupID != 0 || len(db.groups) != 0 {
		t.Fatalf("newer delete must win over in-flight creation: created=%v result=%+v groups=%+v", created, result, db.groups)
	}
}

func TestFeishuPendingNativeGroupEventRetriesAfterClaimExpires(t *testing.T) {
	db := newChannelAgentTestStore()
	identity := &types.ChannelNativeGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_app", TenantKey: "tenant_1",
		ConversationID: "oc_retry", ConversationName: "飞书｜英语组", OperatorChannelUserID: "ou_owner",
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(identity, true, "evt_add", 1000); err != nil || !applied {
		t.Fatalf("apply add: applied=%v err=%v", applied, err)
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(identity, true, "evt_add", 1000); !errors.Is(err, store.ErrChannelNativeGroupEventBusy) || applied {
		t.Fatalf("active claim must report a busy retry: applied=%v err=%v", applied, err)
	}
	key := nativeGroupTestKey("feishu", "cli_app", "tenant_1", "oc_retry")
	state := db.nativeEvents[key]
	state.ClaimedAt = time.Now().Add(-61 * time.Second).UnixMilli()
	db.nativeEvents[key] = state
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(identity, true, "evt_add", 1000); err != nil || !applied {
		t.Fatalf("expired pending claim must allow retry: applied=%v err=%v", applied, err)
	}
}

func TestFeishuBotAddedCopiesOnlyVirtualEmployeesFromCurrentCatsCoGroup(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "coworker", DisplayName: "Coworker", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "log-inspector", DisplayName: "log_inspector", AccountType: types.AccountBot}
	sourceGroupID, _ := db.CreateGroup("查云端log", 7)
	_ = db.AddGroupMember(sourceGroupID, 9, "member")
	_ = db.AddGroupMember(sourceGroupID, 43, "member")
	db.groupMembers[sourceGroupID][43].IsBot = false
	if _, err := db.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p",
		ActorUID: 8, CanonicalUID: 7, GroupID: sourceGroupID, TopicID: fmt.Sprintf("grp_%d", sourceGroupID), Status: types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed group binding: %v", err)
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})

	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_group_add", "cli_app", "tenant_1", "ou_owner", "oc_native_group", "研发讨论")
	binding, err := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_native_group")
	if err != nil || binding == nil || binding.SourceKind != "group" || binding.SourceGroupID != sourceGroupID {
		t.Fatalf("native binding=%+v err=%v", binding, err)
	}
	members := db.groupMembers[binding.GroupID]
	if members[7] == nil || members[43] == nil {
		t.Fatalf("owner and virtual employee should be inherited: %+v", members)
	}
	if members[9] != nil || len(members) != 2 {
		t.Fatalf("human source members must not be copied: %+v", members)
	}
}

func TestFeishuPendingNativeGroupCanOnlyBeInitializedByOriginalOperator(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "other", DisplayName: "Other", AccountType: types.AccountHuman}
	db.users[10] = &types.User{ID: 10, Username: channelActorUsername("feishu", "cli_app", "ou_other"), DisplayName: "Other in Feishu", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "owner-agent", DisplayName: "Owner Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "other-agent", DisplayName: "Other Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 9
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)

	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_pending", "cli_app", "tenant_1", "ou_owner", "oc_pending", "英语备课组")
	pending, _ := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_pending")
	if pending == nil || pending.Status != types.ChannelNativeGroupPending || pending.OperatorChannelUserID != "ou_owner" {
		t.Fatalf("pending binding=%+v", pending)
	}

	_, _ = db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_other", ChannelConversationType: "p2p", ActorUID: 10, CanonicalUID: 9, OwnerUID: 9, AgentUID: 44, Status: types.ChannelAgentBindingActive})
	_, _ = db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_other", ChannelConversationType: "p2p", ActorUID: 10, AgentUID: 44, Source: "entry_scan"})
	sendFeishuNativeGroupTextEvent(t, handler, "cli_app", "tenant_1", "ou_other", "oc_pending", "om_other", "@_user_1 请开始", true)
	pending, _ = db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_pending")
	if pending.GroupID != 0 || pending.Status != types.ChannelNativeGroupPending {
		t.Fatalf("another member must not claim pending group: %+v", pending)
	}

	_, _ = db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, CanonicalUID: 7, OwnerUID: 7, AgentUID: 43, Status: types.ChannelAgentBindingActive})
	_, _ = db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, AgentUID: 43, Source: "entry_scan"})
	sendFeishuNativeGroupTextEvent(t, handler, "cli_app", "tenant_1", "ou_owner", "oc_pending", "om_owner", "/当前目标", false)
	active, _ := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_pending")
	if active == nil || active.Status != types.ChannelNativeGroupActive || active.GroupID <= 0 {
		t.Fatalf("operator should initialize pending group: %+v", active)
	}
	if active.ConversationName != "飞书｜英语备课组" || db.groupMembers[active.GroupID][43] == nil || db.groupMembers[active.GroupID][44] != nil {
		t.Fatalf("initialized group should preserve name and operator target: binding=%+v members=%+v", active, db.groupMembers[active.GroupID])
	}
}

func TestFeishuNativeGroupRecordsOrdinaryMessageAndRoutesMentionToSameTopic(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS", "ou_bot")
	db := newChannelAgentTestStore()
	db.nextID = 100
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, _ = db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, CanonicalUID: 7, OwnerUID: 7, AgentUID: 43, Status: types.ChannelAgentBindingActive})
	_, _ = db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, AgentUID: 43, Source: "entry_scan"})
	api := &fakeFeishuAPI{appID: "cli_app", users: map[string]*FeishuUserIdentity{
		"ou_member": {OpenID: "ou_member", Name: "张老师"},
	}}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_add", "cli_app", "tenant_1", "ou_owner", "oc_native", "英语组")
	binding, _ := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_native")
	db.groupMembers[binding.GroupID][43].IsBot = false
	hub := NewHub(db, nil)
	humanClient := &Client{hub: hub, uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	botClient := &Client{hub: hub, uid: 43, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.clients[7] = map[*Client]struct{}{humanClient: {}}
	hub.clients[43] = map[*Client]struct{}{botClient: {}}
	handler.hub = hub

	sendFeishuNativeGroupTextEvent(t, handler, "cli_app", "tenant_1", "ou_member", "oc_native", "om_plain", "王老师建议先复习单词", false)
	if len(humanClient.send) != 0 || len(botClient.send) != 0 {
		t.Fatalf("ordinary managed-group message should remain hidden and not wake the bot: human=%d bot=%d", len(humanClient.send), len(botClient.send))
	}
	sendFeishuTextEventWithMentions(t, handler, "cli_app", "tenant_1", "ou_member", "oc_native", "group", "om_other_mention", "@_user_1 请陈老师补充意见", []map[string]interface{}{
		{"key": "@_user_1", "name": "陈大为", "id": map[string]interface{}{"open_id": "ou_chen"}},
	})
	if len(humanClient.send) != 0 || len(botClient.send) != 0 {
		t.Fatalf("mentioning another member must not wake the virtual employee: human=%d bot=%d", len(humanClient.send), len(botClient.send))
	}
	sendFeishuNativeGroupTextEvent(t, handler, "cli_app", "tenant_1", "ou_member", "oc_native", "om_typed_mention", "@catsco 请先不要回答", false)
	if len(botClient.send) != 0 {
		t.Fatalf("typed @catsco text without a formal mention must not wake the virtual employee: bot=%d", len(botClient.send))
	}
	sendFeishuNativeGroupTextEvent(t, handler, "cli_app", "tenant_1", "ou_member", "oc_native", "om_mention", "@_user_1 请整理大家的意见", true)
	if len(botClient.send) != 1 {
		t.Fatalf("mentioned message should wake the virtual employee: bot=%d", len(botClient.send))
	}
	if len(db.messages) != 4 {
		t.Fatalf("messages=%+v", db.messages)
	}
	for _, message := range db.messages {
		if message.TopicID != binding.TopicID {
			t.Fatalf("message routed to %s, want %s", message.TopicID, binding.TopicID)
		}
	}
	memberActor := db.users[db.messages[0].FromUID]
	if memberActor == nil || memberActor.DisplayName != "张老师" || db.groupMembers[binding.GroupID][memberActor.ID] != nil {
		t.Fatalf("feishu participant=%+v members=%+v", memberActor, db.groupMembers[binding.GroupID])
	}
	if db.messages[1].Content != "@陈大为 请陈老师补充意见" {
		t.Fatalf("other member mention content=%q", db.messages[1].Content)
	}
	if db.messages[3].Content != "请整理大家的意见" {
		t.Fatalf("bot mention content=%q", db.messages[3].Content)
	}
}

func TestFeishuNativeGroupOutboundReplyUsesNativeChat(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_owner"), DisplayName: "Owner in Feishu", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, _ = db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, CanonicalUID: 7, OwnerUID: 7, AgentUID: 43, Status: types.ChannelAgentBindingActive})
	_, _ = db.UpsertChannelAgentRoute(&types.ChannelAgentRoute{Channel: "feishu", ChannelAppID: "cli_app", ChannelUserID: "ou_owner", ChannelConversationType: "p2p", ActorUID: 8, AgentUID: 43, Source: "entry_scan"})
	api := &fakeFeishuAPI{appID: "cli_app"}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, api)
	sendFeishuBotMembershipEvent(t, handler, "im.chat.member.bot.added_v1", "evt_add", "cli_app", "tenant_1", "ou_owner", "oc_native", "英语组")
	binding, _ := db.ResolveChannelNativeGroup("feishu", "cli_app", "tenant_1", "oc_native")
	api.sends = nil

	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")
	if err := dispatcher.ForwardGroupBotReply(context.Background(), 43, binding.TopicID, "整理结果"); err != nil {
		t.Fatalf("forward native group reply: %v", err)
	}
	if len(api.sends) != 1 || api.sends[0].ReceiveIDType != "chat_id" || api.sends[0].ReceiveID != "oc_native" || api.sends[0].Text != "整理结果" {
		t.Fatalf("native group sends=%+v", api.sends)
	}
}

func TestFeishuNativeGroupCurrentUsesInternalBotIdentity(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", DisplayName: "烙馍村村长", AccountType: types.AccountBot}
	db.groups[500] = &types.Group{ID: 500, Name: "飞书｜英语组", OwnerID: 7}
	db.groupMembers[500] = map[int64]*types.GroupMember{
		7:  {GroupID: 500, UserID: 7, Role: "owner", IsBot: false},
		43: {GroupID: 500, UserID: 43, Role: "member", IsBot: false},
	}
	handler := NewFeishuChannelHandler(db, nil, FeishuChannelConfig{AppID: "cli_app"}, &fakeFeishuAPI{appID: "cli_app"})
	reply := handler.formatFeishuNativeGroupCurrent(&types.ChannelNativeGroupBinding{Status: types.ChannelNativeGroupActive, GroupID: 500, ConversationName: "飞书｜英语组"})
	if !strings.Contains(reply, "烙馍村村长") || strings.Contains(reply, "暂无虚拟员工") {
		t.Fatalf("current target should use internal account type instead of public disclosure flag: %s", reply)
	}
}

func TestFeishuNativeGroupOutboundSendsNativeAttachment(t *testing.T) {
	db := newChannelAgentTestStore()
	binding := &types.ChannelNativeGroupBinding{ID: 1, Channel: "feishu", ChannelAppID: "cli_app", TenantKey: "tenant_1", ConversationID: "oc_native", TopicID: "grp_500", Status: types.ChannelNativeGroupActive}
	db.nativeGroups[nativeGroupTestKey("feishu", "cli_app", "tenant_1", "oc_native")] = binding
	api := &fakeFeishuAPI{appID: "cli_app"}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")
	message := channelOutboundMessage{Text: "清单已整理完成。", Attachments: []channelOutboundAttachment{{Type: "file", Name: "可用Skill清单.txt", URL: "/uploads/files/generated.txt", MimeType: "text/plain"}}}
	if err := dispatcher.ForwardGroupBotReplyMessage(context.Background(), 43, binding.TopicID, message); err != nil {
		t.Fatalf("forward native attachment: %v", err)
	}
	if len(api.sends) != 2 || api.sends[0].MsgType != "text" || api.sends[1].MsgType != "file" {
		t.Fatalf("expected text followed by a native file message, sends=%+v", api.sends)
	}
}

func TestFeishuNativeGroupOutboundFallsBackToAttachmentLink(t *testing.T) {
	db := newChannelAgentTestStore()
	binding := &types.ChannelNativeGroupBinding{ID: 1, Channel: "feishu", ChannelAppID: "cli_app", TenantKey: "tenant_1", ConversationID: "oc_native", TopicID: "grp_500", Status: types.ChannelNativeGroupActive}
	db.nativeGroups[nativeGroupTestKey("feishu", "cli_app", "tenant_1", "oc_native")] = binding
	api := &fakeFeishuAPI{appID: "cli_app", attachmentErr: errors.New("upload denied")}
	dispatcher := NewChannelOutboundDispatcher(db, api, "cli_app")
	message := channelOutboundMessage{Attachments: []channelOutboundAttachment{
		{Type: "file", Name: "报告.txt", URL: "/uploads/files/report.txt"},
		{Type: "file", Name: "数据.txt", URL: "/uploads/files/data.txt"},
	}}
	if err := dispatcher.ForwardGroupBotReplyMessage(context.Background(), 43, binding.TopicID, message); err != nil {
		t.Fatalf("fallback should keep delivery successful: %v", err)
	}
	if len(api.sends) != 1 || api.sends[0].MsgType != "text" || !strings.Contains(api.sends[0].Text, "report.txt") || !strings.Contains(api.sends[0].Text, "data.txt") {
		t.Fatalf("fallback sends=%+v", api.sends)
	}
}

func sendFeishuBotMembershipEvent(t *testing.T, handler *FeishuChannelHandler, eventType, eventID, appID, tenantKey, operatorOpenID, chatID, name string) *httptest.ResponseRecorder {
	eventTime := feishuTestEventClock.Add(1)
	return sendFeishuBotMembershipEventAt(t, handler, eventType, eventID, eventTime, appID, tenantKey, operatorOpenID, chatID, name)
}

func sendFeishuBotMembershipEventAt(t *testing.T, handler *FeishuChannelHandler, eventType, eventID string, eventTime int64, appID, tenantKey, operatorOpenID, chatID, name string) *httptest.ResponseRecorder {
	t.Helper()
	eventBody := map[string]interface{}{
		"schema": "2.0",
		"header": map[string]interface{}{"event_type": eventType, "event_id": eventID, "create_time": strconv.FormatInt(eventTime, 10), "app_id": appID, "tenant_key": tenantKey},
		"event": map[string]interface{}{
			"chat_id": chatID, "operator_id": map[string]interface{}{"open_id": operatorOpenID}, "operator_tenant_key": tenantKey, "name": name,
		},
	}
	body, _ := json.Marshal(eventBody)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("membership event status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func sendFeishuNativeGroupTextEvent(t *testing.T, handler *FeishuChannelHandler, appID, tenantKey, openID, chatID, messageID, text string, mentionBot bool) *httptest.ResponseRecorder {
	t.Helper()
	content, _ := json.Marshal(map[string]string{"text": text})
	message := map[string]interface{}{"message_id": messageID, "chat_id": chatID, "chat_type": "group", "message_type": "text", "content": string(content)}
	if mentionBot {
		message["mentions"] = []map[string]interface{}{{"key": "@_user_1", "name": "catsco_飞书专用", "id": map[string]interface{}{"open_id": "ou_bot"}}}
	}
	eventBody := map[string]interface{}{
		"schema": "2.0",
		"header": map[string]interface{}{"event_type": "im.message.receive_v1", "app_id": appID, "tenant_key": tenantKey},
		"event": map[string]interface{}{
			"sender":  map[string]interface{}{"sender_type": "user", "sender_id": map[string]interface{}{"open_id": openID}},
			"message": message,
		},
	}
	body, _ := json.Marshal(eventBody)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("native group event status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func sendFeishuTextEventWithMentions(t *testing.T, handler *FeishuChannelHandler, appID, tenantKey, openID, chatID, chatType, messageID, text string, mentions []map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	rec := sendFeishuTextEventWithMentionsUnchecked(t, handler, appID, tenantKey, openID, chatID, chatType, messageID, text, mentions)
	if rec.Code != http.StatusOK {
		t.Fatalf("message event status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func sendFeishuTextEventWithMentionsUnchecked(t *testing.T, handler *FeishuChannelHandler, appID, tenantKey, openID, chatID, chatType, messageID, text string, mentions []map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	content, _ := json.Marshal(map[string]string{"text": text})
	message := map[string]interface{}{
		"message_id": messageID, "chat_id": chatID, "chat_type": chatType, "message_type": "text", "content": string(content), "mentions": mentions,
	}
	eventBody := map[string]interface{}{
		"schema": "2.0",
		"header": map[string]interface{}{"event_type": "im.message.receive_v1", "app_id": appID, "tenant_key": tenantKey},
		"event": map[string]interface{}{
			"sender":  map[string]interface{}{"sender_type": "user", "sender_id": map[string]interface{}{"open_id": openID}},
			"message": message,
		},
	}
	body, _ := json.Marshal(eventBody)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleEvents(rec, req)
	return rec
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
