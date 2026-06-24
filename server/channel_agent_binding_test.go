package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestNormalizeChannelAgentAccessModeAcceptsPublic(t *testing.T) {
	if got := types.NormalizeChannelAgentAccessMode(""); got != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("empty access mode = %q, want approval_required", got)
	}
	if got := types.NormalizeChannelAgentAccessMode("unknown"); got != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("unknown access mode = %q, want approval_required", got)
	}
	if got := types.NormalizeChannelAgentAccessMode(types.ChannelAgentAccessPublic); got != types.ChannelAgentAccessPublic {
		t.Fatalf("explicit public access mode = %q, want public", got)
	}
}

func TestChannelAgentEntryAndBindingFlow(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "", "openid-7"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.bodyIDs[43] = "body-contract"
	handler := NewChannelAgentBindingHandler(db, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"weixin","access_mode":"public"}`))
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
	if created.Entry.AccessMode != types.ChannelAgentAccessPublic {
		t.Fatalf("public access mode should be persisted: %+v", created.Entry)
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
	var confirmed struct {
		Status  string                    `json:"status"`
		Binding types.ChannelAgentBinding `json:"binding"`
	}
	if err := json.Unmarshal(confirmRec.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("decode confirm response: %v", err)
	}
	if confirmed.Status != "needs_catsco_login" || confirmed.Binding.ID <= 0 || confirmed.Binding.CanonicalUID != 0 {
		t.Fatalf("confirm should only create account-link placeholder: %+v", confirmed)
	}
	token := validChannelAgentLinkToken(t, &confirmed.Binding)
	linkBody := `{"binding_id":` + strconv.FormatInt(confirmed.Binding.ID, 10) + `,"link_token":"` + token + `"}`
	linkReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(linkBody))
	linkReq = linkReq.WithContext(context.WithValue(linkReq.Context(), uidKey, int64(8)))
	linkRec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(linkRec, linkReq)
	if linkRec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", linkRec.Code, linkRec.Body.String())
	}
	var linked struct {
		Status         string                    `json:"status"`
		Binding        types.ChannelAgentBinding `json:"binding"`
		DeviceLinked   bool                      `json:"device_linked"`
		DeviceOwnerUID int64                     `json:"device_owner_uid"`
	}
	if err := json.Unmarshal(linkRec.Body.Bytes(), &linked); err != nil {
		t.Fatalf("decode link response: %v", err)
	}
	if linked.Status != "linked" || linked.Binding.Status != types.ChannelAgentBindingActive || linked.Binding.CanonicalUID != 8 {
		t.Fatalf("public link should activate without friend approval: %+v", linked)
	}
	if !linked.DeviceLinked || linked.DeviceOwnerUID != 8 || !linked.Binding.DeviceAccessEnabled {
		t.Fatalf("public account binding should enable the linked CatsCo user's own devices: %+v", linked)
	}
	if db.friends[friendKey(8, 43)] == types.FriendPending || db.friends[friendKey(43, 8)] == types.FriendPending {
		t.Fatalf("public link should not create friend requests: %+v", db.friends)
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
	if err := deliverInboundChannelTextToAgent(db, nil, linked.Binding.ActorUID, 43, "公开入口你好", "msg-public-flow", "weixin", nil); err != nil {
		t.Fatalf("public confirm/link flow should deliver without friend approval: %v", err)
	}
	if len(db.messages) != 1 || len(db.topics) != 1 {
		t.Fatalf("public confirm/link delivery should create one message/topic messages=%+v topics=%+v", db.messages, db.topics)
	}
}

func TestChannelAgentEntryCanKeepApprovalAndPublicEntries(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	for _, mode := range []string{types.ChannelAgentAccessApprovalRequired, types.ChannelAgentAccessPublic} {
		req := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"weixin","access_mode":"`+mode+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
		rec := httptest.NewRecorder()
		handler.HandleAgentEntries(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("create %s status=%d body=%s", mode, rec.Code, rec.Body.String())
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/agent-entries?agent_uid=43", nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), uidKey, int64(7)))
	listRec := httptest.NewRecorder()
	handler.HandleAgentEntries(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Entries []channelAgentEntryResponse `json:"entries"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	modes := map[string]bool{}
	for _, entry := range listed.Entries {
		if entry.Channel == "weixin" {
			modes[entry.AccessMode] = true
		}
	}
	if !modes[types.ChannelAgentAccessApprovalRequired] || !modes[types.ChannelAgentAccessPublic] {
		t.Fatalf("expected both access entries, got %+v entries=%+v", modes, listed.Entries)
	}
}

func TestCreateChannelIdentityMobileLinkRequiresExistingAccess(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:   "scene-private-weixin",
		Channel:    "weixin",
		AccessMode: types.ChannelAgentAccessApprovalRequired,
		OwnerUID:   7,
		AgentUID:   43,
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewChannelAgentBindingHandler(db, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/mobile-link", strings.NewReader(`{"agent_uid":43,"channel":"weixin"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(9)))
	rec := httptest.NewRecorder()
	handler.HandleCreateChannelIdentityMobileLink(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "must add") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestCreateChannelIdentityMobileLinkUsesExistingFriendAccess(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	t.Setenv("CATSCO_WEIXIN_APP_ID", "wx_app")
	t.Setenv("CATSCO_WEIXIN_APP_SECRET", "wx_secret")
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
	handler := NewChannelAgentBindingHandler(db, nil)

	req := httptest.NewRequest(http.MethodPost, "https://app.catsco.cc/api/channel-agent-bindings/mobile-link", strings.NewReader(`{"agent_uid":43,"channel":"weixin"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(9)))
	rec := httptest.NewRecorder()
	handler.HandleCreateChannelIdentityMobileLink(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp channelAgentMobileLinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Entry.ID != entry.ID || resp.SceneKey == "" || !strings.HasPrefix(resp.SceneKey, "m.") {
		t.Fatalf("unexpected mobile response: %+v", resp)
	}
	if resp.QRKind != "weixin_official_qr" || !strings.Contains(resp.ChannelQRURL, url.QueryEscape(resp.SceneKey)) {
		t.Fatalf("unexpected QR metadata: %+v", resp)
	}
	previewReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-entry/preview?scene_key="+url.QueryEscape(resp.SceneKey), nil)
	previewRec := httptest.NewRecorder()
	handler.HandleAgentEntryPreview(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("mobile preview status=%d body=%s", previewRec.Code, previewRec.Body.String())
	}
	var previewed struct {
		Entry channelAgentEntryResponse `json:"entry"`
	}
	if err := json.Unmarshal(previewRec.Body.Bytes(), &previewed); err != nil {
		t.Fatalf("decode mobile preview: %v", err)
	}
	if !strings.Contains(previewed.Entry.EntryURL, resp.SceneKey) {
		t.Fatalf("mobile preview should preserve mobile scene in entry URL: %+v", previewed.Entry)
	}
	resolved, canonicalUID, err := resolveChannelIdentityMobileLink(db, resp.SceneKey, "weixin", "wx_app", false)
	if err != nil || resolved == nil || resolved.ID != entry.ID || canonicalUID != 9 {
		t.Fatalf("resolve mobile link entry=%+v canonical=%d err=%v", resolved, canonicalUID, err)
	}
	consumed, canonicalUID, err := resolveChannelIdentityMobileLink(db, resp.SceneKey, "weixin", "wx_app", true)
	if err != nil || consumed == nil || consumed.ID != entry.ID || canonicalUID != 9 {
		t.Fatalf("consume mobile link entry=%+v canonical=%d err=%v", consumed, canonicalUID, err)
	}
	reused, _, err := resolveChannelIdentityMobileLink(db, resp.SceneKey, "weixin", "wx_app", true)
	if err != nil {
		t.Fatalf("reuse should not error: %v", err)
	}
	if reused != nil {
		t.Fatalf("mobile link should be one-time, reused=%+v", reused)
	}
}

func TestCreateFeishuChannelIdentityMobileLinkUsesOAuthShortQRCode(t *testing.T) {
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "mobile-link-test-secret")
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_app")
	t.Setenv("CATSCO_FEISHU_APP_SECRET", "secret")
	t.Setenv("CATSCO_FEISHU_ENTRY_URL_TEMPLATE", "https://applink.feishu.cn/client/app/open?app_id={app_id}&scene={scene_key}&landing={landing_url_encoded}")
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
	handler := NewChannelAgentBindingHandler(db, nil)

	req := httptest.NewRequest(http.MethodPost, "https://app.catsco.cc/api/channel-agent-bindings/mobile-link", strings.NewReader(`{"agent_uid":43,"channel":"feishu"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(9)))
	rec := httptest.NewRecorder()
	handler.HandleCreateChannelIdentityMobileLink(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp channelAgentMobileLinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantQR := "https://app.catsco.cc/api/f/" + url.PathEscape(resp.SceneKey)
	if resp.Entry.ID != entry.ID || resp.SceneKey == "" || !strings.HasPrefix(resp.SceneKey, "m.") {
		t.Fatalf("unexpected mobile response: %+v", resp)
	}
	if resp.QRKind != "feishu_oauth_entry" || resp.QRValue != wantQR || resp.ChannelQRURL != wantQR {
		t.Fatalf("unexpected QR metadata: %+v want=%s", resp, wantQR)
	}
	if resp.Entry.FeishuEntryStatus == nil || resp.Entry.FeishuEntryStatus.NativeShortURL != "https://app.catsco.cc/api/fn/"+url.PathEscape(resp.SceneKey) {
		t.Fatalf("unexpected feishu status: %+v", resp.Entry.FeishuEntryStatus)
	}
}

func TestChannelIdentityMobileLinkRejectsInvalidOrRevokedAccess(t *testing.T) {
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

	wrongApp, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.wrong-app",
		EntryID:      entry.ID,
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		CanonicalUID: 9,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create wrong app link: %v", err)
	}
	resolved, _, err := resolveChannelIdentityMobileLink(db, wrongApp.SceneKey, "weixin", "wx_other", true)
	if err != nil {
		t.Fatalf("wrong app should not error: %v", err)
	}
	if resolved != nil {
		t.Fatalf("wrong app should not resolve: %+v", resolved)
	}
	// The failed app mismatch must not consume the code; a correct scan can still use it.
	resolved, canonicalUID, err := resolveChannelIdentityMobileLink(db, wrongApp.SceneKey, "weixin", "wx_app", true)
	if err != nil || resolved == nil || canonicalUID != 9 {
		t.Fatalf("correct app should still consume link, resolved=%+v canonical=%d err=%v", resolved, canonicalUID, err)
	}

	expired, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.expired",
		EntryID:      entry.ID,
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		CanonicalUID: 9,
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("create expired link: %v", err)
	}
	resolved, _, err = resolveChannelIdentityMobileLink(db, expired.SceneKey, "weixin", "wx_app", true)
	if err != nil {
		t.Fatalf("expired link should not error: %v", err)
	}
	if resolved != nil {
		t.Fatalf("expired link should not resolve: %+v", resolved)
	}

	db.friends[friendKey(9, 43)] = types.FriendRejected
	revokedAccess, err := db.CreateChannelIdentityMobileLink(&types.ChannelIdentityMobileLink{
		SceneKey:     "m.revoked-access",
		EntryID:      entry.ID,
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		CanonicalUID: 9,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create revoked access link: %v", err)
	}
	resolved, _, err = resolveChannelIdentityMobileLink(db, revokedAccess.SceneKey, "weixin", "wx_app", true)
	if err == nil || resolved != nil {
		t.Fatalf("revoked access should fail closed, resolved=%+v err=%v", resolved, err)
	}
}

func TestMobileChannelBindingDeviceAccessFollowsAgentOwnership(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "friend", DisplayName: "Friend", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-friend"), DisplayName: "Weixin Friend", AccountType: types.AccountHuman}
	db.users[101] = &types.User{ID: 101, Username: channelActorUsername("weixin", "wx_app", "openid-owner"), DisplayName: "Weixin Owner", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "owner-agent", DisplayName: "Owner Agent", AccountType: types.AccountBot}
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

	friendBinding, _, err := bindOrRequestChannelAgentAccessWithCanonical(db, db, entry, 100, "weixin", "wx_app", "openid-friend", "", "p2p", 9)
	if err != nil {
		t.Fatalf("bind friend mobile identity: %v", err)
	}
	if friendBinding == nil || friendBinding.Status != types.ChannelAgentBindingActive || !friendBinding.DeviceAccessEnabled {
		t.Fatalf("friend mobile binding should use the friend's own device context: %+v", friendBinding)
	}
	hub := NewHub(db, nil)
	friendMetadata := withChannelBindingDeliveryMetadata(map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-friend",
		"channel_conversation_type": "p2p",
	}, friendBinding)
	if ownerUID, source := hub.deviceAccessOwnerUID(100, 43, friendMetadata); ownerUID != 9 || source != "channel_identity_link" {
		t.Fatalf("legacy channel actor should only delegate to the friend's own device owner, owner=%d source=%s", ownerUID, source)
	}
	if ownerUID, source := hub.deviceAccessOwnerUID(9, 43, friendMetadata); ownerUID != 9 || source != "actor" {
		t.Fatalf("canonical friend mobile actor should use its own device context, owner=%d source=%s", ownerUID, source)
	}

	ownerBinding, _, err := bindOrRequestChannelAgentAccessWithCanonical(db, db, entry, 101, "weixin", "wx_app", "openid-owner", "", "p2p", 7)
	if err != nil {
		t.Fatalf("bind owner mobile identity: %v", err)
	}
	if ownerBinding == nil || ownerBinding.Status != types.ChannelAgentBindingActive || !ownerBinding.DeviceAccessEnabled {
		t.Fatalf("owner mobile binding should inherit own device access: %+v", ownerBinding)
	}
	ownerMetadata := withChannelBindingDeliveryMetadata(map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-owner",
		"channel_conversation_type": "p2p",
	}, ownerBinding)
	if ownerUID, source := hub.deviceAccessOwnerUID(101, 43, ownerMetadata); ownerUID != 7 || source != "channel_identity_link" {
		t.Fatalf("owner mobile binding should expose owner device access, owner=%d source=%s", ownerUID, source)
	}
	if ownerUID, source := hub.deviceAccessOwnerUID(7, 43, ownerMetadata); ownerUID != 7 || source != "actor" {
		t.Fatalf("canonical owner actor should use its own device context, owner=%d source=%s", ownerUID, source)
	}
}

func TestChannelAgentApprovalEnablesApplicantDeviceAccess(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "friend", DisplayName: "Friend", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-friend"), DisplayName: "Weixin Friend", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "owner-agent", DisplayName: "Owner Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-approval-weixin",
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

	binding, _, err := bindOrRequestChannelAgentAccessWithCanonical(db, db, entry, 100, "weixin", "wx_app", "openid-friend", "", "p2p", 9)
	if err != nil {
		t.Fatalf("request friend access: %v", err)
	}
	if binding == nil || binding.Status != types.ChannelAgentBindingPendingApproval || binding.DeviceAccessEnabled {
		t.Fatalf("unapproved friend binding should wait without device access: %+v", binding)
	}

	if err := db.AcceptFriendRequest(9, 43); err != nil {
		t.Fatalf("accept friend request: %v", err)
	}
	activated, err := db.ActivateChannelAgentBindingsForCanonicalUser(9, 43, 7)
	if err != nil {
		t.Fatalf("activate friend access: %v", err)
	}
	if len(activated) != 1 || activated[0].Status != types.ChannelAgentBindingActive || !activated[0].DeviceAccessEnabled {
		t.Fatalf("approved friend binding should enable applicant device access: %+v", activated)
	}
	metadata := withChannelBindingDeliveryMetadata(map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-friend",
		"channel_conversation_type": "p2p",
	}, activated[0])
	hub := NewHub(db, nil)
	if ownerUID, source := hub.deviceAccessOwnerUID(100, 43, metadata); ownerUID != 9 || source != "channel_identity_link" {
		t.Fatalf("legacy channel actor should delegate only to applicant device owner, owner=%d source=%s", ownerUID, source)
	}
	if ownerUID, source := hub.deviceAccessOwnerUID(9, 43, metadata); ownerUID != 9 || source != "actor" {
		t.Fatalf("canonical mobile actor should use own device context after approval, owner=%d source=%s", ownerUID, source)
	}
}

func TestChannelDeviceAccessDoesNotLeakAcrossConversations(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "visitor", DisplayName: "Visitor", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-shared"), DisplayName: "Weixin Visitor", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted

	p2pBinding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-shared",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     true,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed p2p binding: %v", err)
	}
	groupBinding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-shared",
		ChannelConversationID:   "chat-group",
		ChannelConversationType: "group",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     false,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed group binding: %v", err)
	}

	hub := NewHub(db, nil)
	p2pMetadata := withChannelBindingDeliveryMetadata(map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-shared",
		"channel_conversation_type": "p2p",
	}, p2pBinding)
	if ownerUID, source := hub.deviceAccessOwnerUID(100, 43, p2pMetadata); ownerUID != 8 || source != "channel_identity_link" {
		t.Fatalf("p2p binding should keep explicit device access, owner=%d source=%s", ownerUID, source)
	}
	groupMetadata := withChannelBindingDeliveryMetadata(map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-shared",
		"channel_conversation_id":   "chat-group",
		"channel_conversation_type": "group",
	}, groupBinding)
	if ownerUID, source := hub.deviceAccessOwnerUID(100, 43, groupMetadata); ownerUID != 0 || source != "channel_identity_unlinked" {
		t.Fatalf("group binding must not inherit p2p device access, owner=%d source=%s", ownerUID, source)
	}
}

func TestChannelDeliveryUsesConversationScopedBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "visitor", DisplayName: "Visitor", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-shared"), DisplayName: "Weixin Visitor", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted

	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-shared",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     true,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed p2p binding: %v", err)
	}
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-shared",
		ChannelConversationID:   "chat-group",
		ChannelConversationType: "group",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     false,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingPendingApproval,
	}); err != nil {
		t.Fatalf("seed pending group binding: %v", err)
	}

	err := deliverInboundChannelTextToAgent(db, nil, 100, 43, "群聊消息", "wx-group-msg", "weixin", map[string]interface{}{
		"source_channel":            "weixin",
		"channel_app_id":            "wx_app",
		"channel_user_id":           "openid-shared",
		"channel_conversation_id":   "chat-group",
		"channel_conversation_type": "group",
	})
	if err == nil {
		t.Fatalf("group delivery must not fall back to the p2p active binding")
	}
	if len(db.messages) != 0 || len(db.topics) != 0 {
		t.Fatalf("pending group binding should not create side effects messages=%+v topics=%+v", db.messages, db.topics)
	}
}

func TestMobileChannelBindingRejectsExternalIdentityCanonicalConflict(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[10] = &types.User{ID: 10, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[100] = &types.User{ID: 100, Username: channelActorUsername("weixin", "wx_app", "openid-same-1"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[101] = &types.User{ID: 101, Username: channelActorUsername("weixin", "wx_app", "openid-same-2"), DisplayName: "Weixin Bob", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent-a", DisplayName: "Agent A", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "agent-b", DisplayName: "Agent B", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(9, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 9)] = types.FriendAccepted
	db.friends[friendKey(10, 44)] = types.FriendAccepted
	db.friends[friendKey(44, 10)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-same",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            9,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed identity binding: %v", err)
	}
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-second-agent",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     44,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed second entry: %v", err)
	}

	_, _, err = bindOrRequestChannelAgentAccessWithCanonical(db, db, entry, 101, "weixin", "wx_app", "openid-same", "", "p2p", 10)
	if !errors.Is(err, store.ErrChannelAgentBindingAlreadyLinked) {
		t.Fatalf("expected canonical identity conflict, got %v", err)
	}
}

func TestChannelAgentEntryApprovalRequiredCreatesPendingAccess(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
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
	if created.Entry.AccessMode != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("new entry should default to approval_required: %+v", created.Entry)
	}

	confirmBody := `{"scene_key":"` + created.Entry.SceneKey + `","channel_user_id":"openid-visitor","channel_conversation_type":"p2p"}`
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", bytes.NewBufferString(confirmBody))
	confirmRec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmRec.Code, confirmRec.Body.String())
	}
	var pendingLogin struct {
		Status         string                    `json:"status"`
		Binding        types.ChannelAgentBinding `json:"binding"`
		AccountLinkURL string                    `json:"account_link_url"`
	}
	if err := json.Unmarshal(confirmRec.Body.Bytes(), &pendingLogin); err != nil {
		t.Fatalf("decode pending response: %v", err)
	}
	if pendingLogin.Status != "needs_catsco_login" || pendingLogin.Binding.ID <= 0 || pendingLogin.AccountLinkURL == "" {
		t.Fatalf("unexpected login-required response: %+v", pendingLogin)
	}

	resolveReq := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=weixin&channel_user_id=openid-visitor", nil)
	resolveRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(resolveRec, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", resolveRec.Code, resolveRec.Body.String())
	}
	var unresolved struct {
		Bound  bool   `json:"bound"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &unresolved); err != nil {
		t.Fatalf("decode unresolved response: %v", err)
	}
	if unresolved.Bound || unresolved.Status != "needs_catsco_login" || len(db.bindings) != 1 {
		t.Fatalf("unlinked channel identity should only resolve to login gate: %+v bindings=%+v", unresolved, db.bindings)
	}

	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	token := validChannelAgentLinkToken(t, &pendingLogin.Binding)
	linkBody := `{"binding_id":` + strconv.FormatInt(pendingLogin.Binding.ID, 10) + `,"link_token":"` + token + `"}`
	linkReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(linkBody))
	linkReq = linkReq.WithContext(context.WithValue(linkReq.Context(), uidKey, int64(9)))
	linkRec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(linkRec, linkReq)
	if linkRec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", linkRec.Code, linkRec.Body.String())
	}
	var linkedPending struct {
		Status       string                    `json:"status"`
		Binding      types.ChannelAgentBinding `json:"binding"`
		CanonicalUID int64                     `json:"canonical_uid"`
	}
	if err := json.Unmarshal(linkRec.Body.Bytes(), &linkedPending); err != nil {
		t.Fatalf("decode link response: %v", err)
	}
	if linkedPending.Status != "pending_approval" || linkedPending.CanonicalUID != 9 || linkedPending.Binding.ActorUID == 9 {
		t.Fatalf("link should keep channel actor but request approval for canonical user: %+v", linkedPending)
	}

	friendHandler := NewFriendHandler(db)
	pendingReq := httptest.NewRequest(http.MethodGet, "/api/friends/pending?agent_uid=43", nil)
	pendingReq = pendingReq.WithContext(context.WithValue(pendingReq.Context(), uidKey, int64(7)))
	pendingRec := httptest.NewRecorder()
	friendHandler.HandleGetPendingRequests(pendingRec, pendingReq)
	if pendingRec.Code != http.StatusOK {
		t.Fatalf("pending status=%d body=%s", pendingRec.Code, pendingRec.Body.String())
	}
	var pendingFriends struct {
		Requests []*types.FriendRequest `json:"requests"`
	}
	if err := json.Unmarshal(pendingRec.Body.Bytes(), &pendingFriends); err != nil {
		t.Fatalf("decode pending friends: %v", err)
	}
	if len(pendingFriends.Requests) != 1 || pendingFriends.Requests[0].FromUserID != 9 {
		t.Fatalf("owner should see bot pending friend request: %+v", pendingFriends.Requests)
	}

	acceptReq := httptest.NewRequest(http.MethodPost, "/api/friends/accept", bytes.NewBufferString(`{"agent_uid":43,"user_id":9}`))
	acceptReq = acceptReq.WithContext(context.WithValue(acceptReq.Context(), uidKey, int64(7)))
	acceptRec := httptest.NewRecorder()
	friendHandler.HandleAcceptRequest(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", acceptRec.Code, acceptRec.Body.String())
	}

	resolveAfterRec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(resolveAfterRec, resolveReq)
	if resolveAfterRec.Code != http.StatusOK {
		t.Fatalf("resolve after accept status=%d body=%s", resolveAfterRec.Code, resolveAfterRec.Body.String())
	}
	var resolved struct {
		Bound    bool  `json:"bound"`
		AgentUID int64 `json:"agent_uid"`
	}
	if err := json.Unmarshal(resolveAfterRec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolved response: %v", err)
	}
	if !resolved.Bound || resolved.AgentUID != 43 {
		t.Fatalf("expected approved access to resolve binding: %+v", resolved)
	}
}

func TestChannelAgentDeliveryRejectsUnlinkedActiveBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[8] = &types.User{ID: 8, Username: "channel-user", DisplayName: "Channel User", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-legacy",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	err := deliverInboundChannelTextToAgent(db, nil, 8, 43, "你好", "msg-1", "weixin", nil)
	if err == nil {
		t.Fatalf("expected unlinked channel binding to be rejected")
	}
	if len(db.messages) != 0 || len(db.topics) != 0 {
		t.Fatalf("unlinked delivery should not create side effects messages=%+v topics=%+v", db.messages, db.topics)
	}
}

func TestChannelAgentPublicBindingDeliversWithoutFriendApproval(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-public",
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
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-public",
		ActorUID:      80,
		CanonicalUID:  9,
		OwnerUID:      7,
		AgentUID:      43,
		EntryID:       entry.ID,
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := deliverInboundChannelTextToAgent(db, nil, 80, 43, "你好", "msg-public", "weixin", nil); err != nil {
		t.Fatalf("public binding should deliver without friend approval: %v", err)
	}
	if len(db.messages) != 1 || len(db.topics) != 1 {
		t.Fatalf("public delivery should create one message/topic messages=%+v topics=%+v", db.messages, db.topics)
	}
	if db.messages[0].FromUID != 9 || db.messages[0].TopicID != p2pTopicID(9, 43) || db.topics[0] != p2pTopicID(9, 43) {
		t.Fatalf("mobile delivery should continue canonical p2p topic, message=%+v topics=%+v", db.messages[0], db.topics)
	}
}

func TestChannelAgentPublicBindingRejectsExplicitlyRejectedUser(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-public",
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
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-public",
		ActorUID:      80,
		CanonicalUID:  9,
		OwnerUID:      7,
		AgentUID:      43,
		EntryID:       entry.ID,
		Status:        types.ChannelAgentBindingRejected,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := deliverInboundChannelTextToAgent(db, nil, 80, 43, "你好", "msg-public", "weixin", nil); err == nil {
		t.Fatalf("rejected public binding should not deliver")
	}
	if len(db.messages) != 0 || len(db.topics) != 0 {
		t.Fatalf("rejected public delivery should not create side effects messages=%+v topics=%+v", db.messages, db.topics)
	}
}

func TestChannelAgentResolveRejectsInvalidPublicCanonicalUser(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[9] = &types.User{ID: 9, Username: "bot-canonical", DisplayName: "Bot Canonical", AccountType: types.AccountBot}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-public",
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
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-public",
		ActorUID:      80,
		CanonicalUID:  9,
		OwnerUID:      7,
		AgentUID:      43,
		EntryID:       entry.ID,
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	handler := NewChannelAgentBindingHandler(db, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=weixin&channel_app_id=wx_app&channel_user_id=openid-public", nil)
	rec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resolved struct {
		Bound  bool   `json:"bound"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolved.Bound || resolved.Status != "not_allowed" {
		t.Fatalf("invalid canonical user should not resolve as bound: %+v", resolved)
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

func TestChannelAgentBindingResolveDoesNotFallbackAcrossConversations(t *testing.T) {
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
	if resolved.Bound || resolved.AgentUID != 0 {
		t.Fatalf("group/private conversation must not reuse empty-conversation binding: %+v", resolved)
	}
}

func TestChannelAgentAccessRequestDoesNotFallbackAcrossConversations(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:   "scene-private-only",
		Channel:    "feishu",
		OwnerUID:   7,
		AgentUID:   43,
		Status:     "active",
		AccessMode: types.ChannelAgentAccessApprovalRequired,
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	if _, err := db.RequestChannelAgentAccess(&types.ChannelAgentAccessRequest{
		EntryID:       entry.ID,
		Channel:       "feishu",
		ChannelUserID: "ou_user",
		ActorUID:      8,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "pending",
	}); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channel-agent-bindings/resolve?channel=feishu&channel_user_id=ou_user&channel_conversation_id=oc_group", nil)
	rec := httptest.NewRecorder()
	handler.HandleResolveChannelAgentBinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resolved struct {
		Bound  bool   `json:"bound"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolved.Bound || resolved.Status == "pending" {
		t.Fatalf("group/private conversation must not reuse empty-conversation pending request: %+v", resolved)
	}
}

func TestChannelAgentBindingConfirmRequiresTokenInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	_, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey: "scene-prod-token",
		Channel:  "feishu",
		OwnerUID: 7,
		AgentUID: 43,
		Status:   "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewChannelAgentBindingHandler(db, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", bytes.NewBufferString(`{"scene_key":"scene-prod-token","channel_user_id":"ou_user"}`))
	rec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.bindings) != 0 {
		t.Fatalf("confirm without token should not create binding: %+v", db.bindings)
	}
}

func TestChannelAgentBindingLinkUser(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_feishu_user", DisplayName: "Feishu User", AccountType: types.AccountHuman}
	db.owners[43] = 7
	db.bodyIDs[43] = "body-contract"
	handler := NewChannelAgentBindingHandler(db, nil)

	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      100,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	linkURL := channelBindingDeviceLinkURL(httptest.NewRequest(http.MethodGet, "https://app.catsco.cc/e/demo", nil), binding)
	if linkURL == "" {
		t.Fatalf("device link url is empty")
	}
	token, err := signChannelBindingLinkToken(channelAgentLinkTokenPayload{
		BindingID: binding.ID,
		ActorUID:  binding.ActorUID,
		AgentUID:  binding.AgentUID,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign link token: %v", err)
	}
	body := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + token + `","device_access":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", rec.Code, rec.Body.String())
	}
	var linked struct {
		Status        string                    `json:"status"`
		Binding       types.ChannelAgentBinding `json:"binding"`
		CanonicalUID  int64                     `json:"canonical_uid"`
		DeviceLinked  bool                      `json:"device_linked"`
		DeviceOwnerID int64                     `json:"device_owner_uid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &linked); err != nil {
		t.Fatalf("decode link response: %v", err)
	}
	if linked.Status != "linked" || linked.CanonicalUID != 7 || linked.Binding.CanonicalUID != 7 || !linked.DeviceLinked || linked.DeviceOwnerID != 7 {
		t.Fatalf("unexpected link response: %+v", linked)
	}
}

func TestChannelAgentPublicLinkDoesNotActivateApprovalBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "visitor", DisplayName: "Visitor", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_public", DisplayName: "Channel Public", AccountType: types.AccountHuman}
	db.users[101] = &types.User{ID: 101, Username: "ch_private", DisplayName: "Channel Private", AccountType: types.AccountHuman}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	approvalEntry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:   "approval-scene",
		Channel:    "feishu",
		AccessMode: types.ChannelAgentAccessApprovalRequired,
		OwnerUID:   7,
		AgentUID:   43,
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("seed approval entry: %v", err)
	}
	publicEntry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:   "public-scene",
		Channel:    "feishu",
		AccessMode: types.ChannelAgentAccessPublic,
		OwnerUID:   7,
		AgentUID:   43,
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("seed public entry: %v", err)
	}
	approvalBinding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelUserID: "ou-private",
		ActorUID:      101,
		CanonicalUID:  9,
		OwnerUID:      7,
		AgentUID:      43,
		EntryID:       approvalEntry.ID,
		Status:        types.ChannelAgentBindingPendingApproval,
	})
	if err != nil {
		t.Fatalf("seed approval binding: %v", err)
	}
	publicBinding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelUserID: "ou-public",
		ActorUID:      100,
		OwnerUID:      7,
		AgentUID:      43,
		EntryID:       publicEntry.ID,
		Status:        types.ChannelAgentBindingPendingLogin,
	})
	if err != nil {
		t.Fatalf("seed public binding: %v", err)
	}

	body := `{"binding_id":` + strconv.FormatInt(publicBinding.ID, 10) + `,"link_token":"` + validChannelAgentLinkToken(t, publicBinding) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(9)))
	rec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", rec.Code, rec.Body.String())
	}

	updatedPublic, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelUserID: "ou-public",
		AgentUID:      43,
	})
	if err != nil || updatedPublic == nil {
		t.Fatalf("resolve public binding err=%v binding=%+v", err, updatedPublic)
	}
	if updatedPublic.Status != types.ChannelAgentBindingActive || updatedPublic.CanonicalUID != 9 {
		t.Fatalf("public binding should be active for linked CatsCo user, got %+v", updatedPublic)
	}
	updatedApproval, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:       "feishu",
		ChannelUserID: "ou-private",
		AgentUID:      43,
	})
	if err != nil || updatedApproval == nil {
		t.Fatalf("resolve approval binding err=%v binding=%+v", err, updatedApproval)
	}
	if updatedApproval.Status != types.ChannelAgentBindingPendingApproval || updatedApproval.ID != approvalBinding.ID {
		t.Fatalf("public link should not activate approval binding, got %+v", updatedApproval)
	}
}

func TestChannelAgentFriendCanExplicitlyEnableOwnDeviceAccess(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "visitor", DisplayName: "Visitor", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_weixin_user", DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	handler := NewChannelAgentBindingHandler(db, nil)

	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:             "weixin",
		ChannelUserID:       "openid-friend",
		ActorUID:            100,
		CanonicalUID:        8,
		DeviceAccessEnabled: false,
		OwnerUID:            7,
		AgentUID:            43,
		Status:              types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	body := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + validChannelAgentLinkToken(t, binding) + `","device_access":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(8)))
	rec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", rec.Code, rec.Body.String())
	}
	var linked struct {
		Binding        types.ChannelAgentBinding `json:"binding"`
		DeviceLinked   bool                      `json:"device_linked"`
		DeviceOwnerUID int64                     `json:"device_owner_uid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &linked); err != nil {
		t.Fatalf("decode link response: %v", err)
	}
	if !linked.DeviceLinked || linked.DeviceOwnerUID != 8 || !linked.Binding.DeviceAccessEnabled {
		t.Fatalf("friend explicit device authorization should target friend device owner: %+v", linked)
	}
	trustedMetadata := withChannelBindingDeliveryMetadata(map[string]interface{}{
		"source_channel":            "weixin",
		"channel_user_id":           "openid-friend",
		"channel_conversation_type": "p2p",
	}, &linked.Binding)
	deviceOwnerUID, reason := NewHub(db, nil).deviceAccessOwnerUID(100, 43, trustedMetadata)
	if deviceOwnerUID != 8 || reason != "channel_identity_link" {
		t.Fatalf("device access owner should be friend after explicit authorization, owner=%d reason=%q", deviceOwnerUID, reason)
	}
}

func TestChannelAgentBindingLinkUserRefreshesCurrentAgentBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "visitor", DisplayName: "Visitor", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "target-agent", DisplayName: "Target Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "other-agent", DisplayName: "Other Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_weixin_user", DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	handler := NewChannelAgentBindingHandler(db, nil)

	target, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelUserID:           "openid-shared",
		ChannelConversationID:   "chat-1",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     false,
		OwnerUID:                7,
		AgentUID:                43,
		Status:                  types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed target binding: %v", err)
	}
	other, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelUserID:           "openid-shared",
		ChannelConversationID:   "chat-1",
		ChannelConversationType: "p2p",
		ActorUID:                100,
		CanonicalUID:            8,
		DeviceAccessEnabled:     false,
		OwnerUID:                7,
		AgentUID:                44,
		Status:                  types.ChannelAgentBindingActive,
	})
	if err != nil {
		t.Fatalf("seed other binding: %v", err)
	}
	otherKey := bindingKey(other.Channel, other.ChannelAppID, other.ChannelUserID, other.ChannelConversationID, other.AgentUID)
	db.bindings[otherKey].UpdatedAt = target.UpdatedAt.Add(time.Hour)

	body := `{"binding_id":` + strconv.FormatInt(target.ID, 10) + `,"link_token":"` + validChannelAgentLinkToken(t, target) + `","device_access":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(8)))
	rec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status=%d body=%s", rec.Code, rec.Body.String())
	}
	var linked struct {
		Binding        types.ChannelAgentBinding `json:"binding"`
		DeviceLinked   bool                      `json:"device_linked"`
		DeviceOwnerUID int64                     `json:"device_owner_uid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &linked); err != nil {
		t.Fatalf("decode link response: %v", err)
	}
	if linked.Binding.AgentUID != 43 || linked.Binding.ID != target.ID || !linked.DeviceLinked || linked.DeviceOwnerUID != 8 {
		t.Fatalf("link response should refresh target agent binding only: %+v", linked)
	}
}

func TestChannelAgentBindingLinkUserRejectsInvalidTokensBeforeStoreUpdate(t *testing.T) {
	db, handler, binding := newChannelAgentLinkHarness(t)

	expiredToken, err := signChannelBindingLinkToken(channelAgentLinkTokenPayload{
		BindingID: binding.ID,
		ActorUID:  binding.ActorUID,
		AgentUID:  binding.AgentUID,
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	cases := []struct {
		name      string
		bindingID int64
		token     string
	}{
		{name: "expired", bindingID: binding.ID, token: expiredToken},
		{name: "tampered", bindingID: binding.ID, token: validChannelAgentLinkToken(t, binding) + "x"},
		{name: "binding mismatch", bindingID: binding.ID + 1, token: validChannelAgentLinkToken(t, binding)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"binding_id":` + strconv.FormatInt(tc.bindingID, 10) + `,"link_token":"` + tc.token + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
			rec := httptest.NewRecorder()
			handler.HandleLinkChannelAgentBindingUser(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(db.linkCalls) != 0 {
				t.Fatalf("invalid token should not call store: %+v", db.linkCalls)
			}
			if binding.CanonicalUID != 0 {
				t.Fatalf("invalid token changed canonical uid: %+v", binding)
			}
		})
	}
}

func TestChannelAgentBindingLinkUserIsIdempotentForSameUser(t *testing.T) {
	db, handler, binding := newChannelAgentLinkHarness(t)
	token := validChannelAgentLinkToken(t, binding)

	for i := 0; i < 2; i++ {
		body := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + token + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
		rec := httptest.NewRecorder()
		handler.HandleLinkChannelAgentBindingUser(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if binding.CanonicalUID != 7 {
		t.Fatalf("canonical uid changed: %+v", binding)
	}
	if len(db.linkCalls) != 2 {
		t.Fatalf("expected two idempotent link calls, got %+v", db.linkCalls)
	}
}

func TestChannelAgentBindingLinkUserRejectsAlreadyLinkedDifferentUser(t *testing.T) {
	db, handler, binding := newChannelAgentLinkHarness(t)
	db.users[8] = &types.User{ID: 8, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	binding.CanonicalUID = 7
	token := validChannelAgentLinkToken(t, binding)

	body := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(8)))
	rec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if binding.CanonicalUID != 7 {
		t.Fatalf("conflict overwrote canonical uid: %+v", binding)
	}
}

func TestChannelAgentBindingLinkUserRejectsBotOrServiceAccount(t *testing.T) {
	db, handler, binding := newChannelAgentLinkHarness(t)
	db.users[9] = &types.User{ID: 9, Username: "bot-owner", AccountType: types.AccountBot}
	token := validChannelAgentLinkToken(t, binding)

	body := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/link-user", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(9)))
	rec := httptest.NewRecorder()
	handler.HandleLinkChannelAgentBindingUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.linkCalls) != 0 {
		t.Fatalf("forbidden account should not call store: %+v", db.linkCalls)
	}
	if binding.CanonicalUID != 0 {
		t.Fatalf("forbidden account changed canonical uid: %+v", binding)
	}
}

func newChannelAgentLinkHarness(t *testing.T) (*channelAgentTestStore, *ChannelAgentBindingHandler, *types.ChannelAgentBinding) {
	t.Helper()
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_feishu_user", DisplayName: "Feishu User", AccountType: types.AccountHuman}
	db.owners[43] = 7
	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_user",
		ActorUID:      100,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	stored := db.bindings[bindingKey(binding.Channel, binding.ChannelAppID, binding.ChannelUserID, binding.ChannelConversationID, binding.AgentUID)]
	if stored == nil {
		t.Fatalf("seeded binding missing from fake store")
	}
	return db, NewChannelAgentBindingHandler(db, nil), stored
}

func validChannelAgentLinkToken(t *testing.T, binding *types.ChannelAgentBinding) string {
	t.Helper()
	token, err := signChannelBindingLinkToken(channelAgentLinkTokenPayload{
		BindingID: binding.ID,
		ActorUID:  binding.ActorUID,
		AgentUID:  binding.AgentUID,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign link token: %v", err)
	}
	return token
}

func TestChannelAgentBindingUsesEntryChannelAppID(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("feishu", "cli_app", "ou_user"), DisplayName: "Feishu Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	handler := NewChannelAgentBindingHandler(db, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/agent-entries", bytes.NewBufferString(`{"agent_uid":43,"channel":"feishu","channel_app_id":"cli_app","access_mode":"public"}`))
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
		Bound    bool   `json:"bound"`
		Status   string `json:"status"`
		AgentUID int64  `json:"agent_uid"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolved.Bound || resolved.Status != "needs_catsco_login" || resolved.AgentUID != 43 {
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

func TestChannelAgentBindingRescanSwitchesToCurrentEntry(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "visitor", DisplayName: "Visitor", AccountType: types.AccountHuman}
	db.users[80] = &types.User{ID: 80, Username: channelActorUsername("weixin", "wx_app", "openid-7"), DisplayName: "Weixin Visitor", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "sales-agent", DisplayName: "Sales Agent", AccountType: types.AccountBot}
	db.users[44] = &types.User{ID: 44, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.owners[44] = 7
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-7",
		ChannelConversationType: "p2p",
		ActorUID:                80,
		CanonicalUID:            8,
		OwnerUID:                7,
		AgentUID:                43,
		EntryID:                 101,
		Status:                  types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-contract-agent",
		Channel:      "weixin",
		ChannelAppID: "wx_app",
		OwnerUID:     7,
		AgentUID:     44,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewChannelAgentBindingHandler(db, nil)

	body := `{"scene_key":"` + entry.SceneKey + `","channel":"weixin","channel_app_id":"wx_app","channel_user_id":"openid-7"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", rec.Code, rec.Body.String())
	}
	var confirmed struct {
		Status  string                    `json:"status"`
		Binding types.ChannelAgentBinding `json:"binding"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if confirmed.Status != "pending_approval" || confirmed.Binding.AgentUID != 44 || confirmed.Binding.EntryID != entry.ID {
		t.Fatalf("expected current entry pending approval, got status=%s binding=%+v", confirmed.Status, confirmed.Binding)
	}
	if confirmed.Binding.CanonicalUID != 8 || confirmed.Binding.Status != types.ChannelAgentBindingPendingApproval {
		t.Fatalf("expected existing CatsCo user to be preserved as pending approval, got %+v", confirmed.Binding)
	}
	if db.friends[friendKey(8, 44)] != types.FriendPending {
		t.Fatalf("expected a friend request for the newly scanned virtual employee, friends=%+v", db.friends)
	}
	route, err := db.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-7",
		ChannelConversationType: "p2p",
	})
	if err != nil {
		t.Fatalf("resolve route: %v", err)
	}
	if route == nil || route.AgentUID != 44 {
		t.Fatalf("expected channel route to point at current entry agent, got %+v", route)
	}
	oldBinding, err := db.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 "weixin",
		ChannelAppID:            "wx_app",
		ChannelUserID:           "openid-7",
		ChannelConversationType: "p2p",
		AgentUID:                43,
	})
	if err != nil || oldBinding == nil || oldBinding.AgentUID != 43 {
		t.Fatalf("old binding should remain available for explicit switch-back, got %+v err=%v", oldBinding, err)
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

func TestChannelAgentBindingConfirmAuth(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey: "scene-weixin",
		Channel:  "weixin",
		OwnerUID: 7,
		AgentUID: 43,
		Status:   "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	handler := NewChannelAgentBindingHandler(db, nil)
	body := `{"scene_key":"` + entry.SceneKey + `","channel_user_id":"openid-7"}`

	t.Setenv("APP_ENV", "production")
	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "")
	openReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", strings.NewReader(body))
	openRec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(openRec, openReq)
	if openRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected production confirm without token to be unauthorized, got status=%d body=%s", openRec.Code, openRec.Body.String())
	}

	t.Setenv("CATSCO_CHANNEL_BINDING_TOKEN", "secret")
	bearerReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/confirm", strings.NewReader(body))
	bearerReq.Header.Set("Authorization", "Bearer secret")
	bearerRec := httptest.NewRecorder()
	handler.HandleConfirmChannelAgentBinding(bearerRec, bearerReq)
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
	if _, err := db.RegenerateChannelAgentEntry(entry.ID, 7, "scene-new", ""); err != nil {
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

func TestChannelAgentEntryRegenerateRefreshesConfiguredFeishuAppID(t *testing.T) {
	t.Setenv("CATSCO_FEISHU_APP_ID", "cli_current")
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "dev-agent", DisplayName: "Dev Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	handler := NewChannelAgentBindingHandler(db, nil)

	entry, err := db.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     "scene-old",
		Channel:      "feishu",
		ChannelAppID: "cli_legacy",
		AccessMode:   types.ChannelAgentAccessApprovalRequired,
		OwnerUID:     7,
		AgentUID:     43,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("seed entry: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agent-entries/"+strconv.FormatInt(entry.ID, 10)+"/regenerate", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleAgentEntryByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Entry channelAgentEntryResponse `json:"entry"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Entry.ChannelAppID != "cli_current" {
		t.Fatalf("regenerated channel app id = %q, want cli_current", resp.Entry.ChannelAppID)
	}
	if old := db.entries[entry.ID]; old == nil || old.Status != "revoked" {
		t.Fatalf("old entry status = %#v, want revoked", old)
	}
}

func TestCreateChannelGroupMobileLinkRequiresGroupMembership(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "outsider", DisplayName: "Outsider", AccountType: types.AccountHuman}
	groupID, err := db.CreateGroup("Virtual Team", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	topicID := "grp_" + strconv.FormatInt(groupID, 10)
	handler := NewChannelAgentBindingHandler(db, nil)

	body := `{"group_id":` + strconv.FormatInt(groupID, 10) + `,"topic_id":"` + topicID + `","channel":"weixin"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/group-mobile-link", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleCreateChannelGroupMobileLink(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("member create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp channelGroupMobileLinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.GroupID != groupID || resp.TopicID != topicID || resp.GroupName != "Virtual Team" || !strings.HasPrefix(resp.SceneKey, "g.") {
		t.Fatalf("unexpected group mobile response: %+v", resp)
	}
	link, err := db.GetChannelGroupMobileLink(resp.SceneKey)
	if err != nil || link == nil || link.CanonicalUID != 7 || link.GroupID != groupID || link.TopicID != topicID {
		t.Fatalf("stored link = %#v err=%v", link, err)
	}

	outsiderReq := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/group-mobile-link", bytes.NewBufferString(body))
	outsiderReq = outsiderReq.WithContext(context.WithValue(outsiderReq.Context(), uidKey, int64(8)))
	outsiderRec := httptest.NewRecorder()
	handler.HandleCreateChannelGroupMobileLink(outsiderRec, outsiderReq)
	if outsiderRec.Code != http.StatusForbidden {
		t.Fatalf("outsider status=%d body=%s", outsiderRec.Code, outsiderRec.Body.String())
	}
}

type channelAgentTestStore struct {
	store.Store
	users          map[int64]*types.User
	owners         map[int64]int64
	bodyIDs        map[int64]string
	entries        map[int64]*types.ChannelAgentEntry
	accessRequests map[string]*types.ChannelAgentAccessRequest
	bindings       map[string]*types.ChannelAgentBinding
	mobileLinks    map[string]*types.ChannelIdentityMobileLink
	groups         map[int64]*types.Group
	groupMembers   map[int64]map[int64]*types.GroupMember
	groupLinks     map[string]*types.ChannelGroupMobileLink
	groupBindings  map[string]*types.ChannelGroupBinding
	routes         map[string]*types.ChannelAgentRoute
	friends        map[string]types.FriendStatus
	messages       []*types.Message
	clientIDs      map[string]int64
	topics         []string
	linkCalls      []channelAgentLinkCall
	linkErr        error
	nextID         int64
}

type channelAgentLinkCall struct {
	BindingID    int64
	ActorUID     int64
	AgentUID     int64
	CanonicalUID int64
}

func newChannelAgentTestStore() *channelAgentTestStore {
	return &channelAgentTestStore{
		users:          map[int64]*types.User{},
		owners:         map[int64]int64{},
		bodyIDs:        map[int64]string{},
		entries:        map[int64]*types.ChannelAgentEntry{},
		accessRequests: map[string]*types.ChannelAgentAccessRequest{},
		bindings:       map[string]*types.ChannelAgentBinding{},
		mobileLinks:    map[string]*types.ChannelIdentityMobileLink{},
		groups:         map[int64]*types.Group{},
		groupMembers:   map[int64]map[int64]*types.GroupMember{},
		groupLinks:     map[string]*types.ChannelGroupMobileLink{},
		groupBindings:  map[string]*types.ChannelGroupBinding{},
		routes:         map[string]*types.ChannelAgentRoute{},
		friends:        map[string]types.FriendStatus{},
		messages:       []*types.Message{},
		clientIDs:      map[string]int64{},
		topics:         []string{},
		linkCalls:      []channelAgentLinkCall{},
		nextID:         1,
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

func (s *channelAgentTestStore) CreateFriendRequest(fromUID, toUID int64, message string) (int64, error) {
	s.friends[friendKey(fromUID, toUID)] = types.FriendPending
	return s.nextID, nil
}

func (s *channelAgentTestStore) AcceptFriendRequest(fromUID, toUID int64) error {
	s.friends[friendKey(fromUID, toUID)] = types.FriendAccepted
	s.friends[friendKey(toUID, fromUID)] = types.FriendAccepted
	return nil
}

func (s *channelAgentTestStore) RejectFriendRequest(fromUID, toUID int64) error {
	s.friends[friendKey(fromUID, toUID)] = types.FriendRejected
	return nil
}

func (s *channelAgentTestStore) GetFriends(uid int64) ([]*types.User, error) {
	var out []*types.User
	for key, status := range s.friends {
		if status != types.FriendAccepted {
			continue
		}
		fromUID, toUID := parseFriendKey(key)
		if fromUID == uid {
			if user := s.users[toUID]; user != nil {
				out = append(out, user)
			}
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) GetPendingRequests(uid int64) ([]*types.FriendRequest, error) {
	var out []*types.FriendRequest
	for key, status := range s.friends {
		if status != types.FriendPending {
			continue
		}
		fromUID, toUID := parseFriendKey(key)
		if toUID != uid {
			continue
		}
		req := &types.FriendRequest{
			ID:         fromUID,
			FromUserID: fromUID,
			ToUserID:   toUID,
			Status:     status,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if user := s.users[fromUID]; user != nil {
			req.FromUsername = user.Username
			req.DisplayName = user.DisplayName
		}
		out = append(out, req)
	}
	return out, nil
}

func (s *channelAgentTestStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.friends[friendKey(uid1, uid2)] == types.FriendAccepted, nil
}

func (s *channelAgentTestStore) IsBlocked(uid, blockedUID int64) (bool, error) {
	return s.friends[friendKey(uid, blockedUID)] == types.FriendBlocked, nil
}

func (s *channelAgentTestStore) CreateGroup(name string, ownerID int64) (int64, error) {
	id := s.nextID
	s.nextID++
	s.groups[id] = &types.Group{
		ID:         id,
		Name:       name,
		OwnerID:    ownerID,
		MaxMembers: 100,
		CreatedAt:  time.Now(),
	}
	_ = s.AddGroupMember(id, ownerID, "owner")
	return id, nil
}

func (s *channelAgentTestStore) GetGroup(groupID int64) (*types.Group, error) {
	group := s.groups[groupID]
	if group == nil {
		return nil, nil
	}
	next := *group
	return &next, nil
}

func (s *channelAgentTestStore) AddGroupMember(groupID, userID int64, role string) error {
	if groupID <= 0 || userID <= 0 {
		return nil
	}
	if role == "" {
		role = "member"
	}
	if s.groupMembers[groupID] == nil {
		s.groupMembers[groupID] = map[int64]*types.GroupMember{}
	}
	member := &types.GroupMember{
		ID:       s.nextID,
		GroupID:  groupID,
		UserID:   userID,
		Role:     role,
		JoinedAt: time.Now(),
	}
	s.nextID++
	if user := s.users[userID]; user != nil {
		member.Username = user.Username
		member.DisplayName = user.DisplayName
		member.AvatarURL = user.AvatarURL
		member.IsBot = user.AccountType == types.AccountBot
	}
	s.groupMembers[groupID][userID] = member
	return nil
}

func (s *channelAgentTestStore) RemoveGroupMember(groupID, userID int64) error {
	if members := s.groupMembers[groupID]; members != nil {
		delete(members, userID)
	}
	return nil
}

func (s *channelAgentTestStore) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	var out []*types.GroupMember
	for _, member := range s.groupMembers[groupID] {
		next := *member
		out = append(out, &next)
	}
	return out, nil
}

func (s *channelAgentTestStore) GetUserGroups(userID int64) ([]*types.Group, error) {
	var out []*types.Group
	for groupID, members := range s.groupMembers {
		if members[userID] == nil {
			continue
		}
		if group := s.groups[groupID]; group != nil {
			next := *group
			out = append(out, &next)
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) IsGroupMember(groupID, userID int64) (bool, error) {
	return s.groupMembers[groupID] != nil && s.groupMembers[groupID][userID] != nil, nil
}

func (s *channelAgentTestStore) GetGroupMemberCount(groupID int64) (int, error) {
	return len(s.groupMembers[groupID]), nil
}

func (s *channelAgentTestStore) GetGroupBotCount(groupID int64) (int, error) {
	count := 0
	for userID := range s.groupMembers[groupID] {
		if user := s.users[userID]; user != nil && user.AccountType == types.AccountBot {
			count++
		}
	}
	return count, nil
}

func (s *channelAgentTestStore) UpdateMemberRole(groupID, userID int64, role string) error {
	if member := s.groupMembers[groupID][userID]; member != nil {
		member.Role = role
	}
	return nil
}

func (s *channelAgentTestStore) DeleteGroup(groupID int64) error {
	delete(s.groups, groupID)
	delete(s.groupMembers, groupID)
	return nil
}

func (s *channelAgentTestStore) GetMemberRole(groupID, userID int64) (string, error) {
	if member := s.groupMembers[groupID][userID]; member != nil {
		return member.Role, nil
	}
	return "", nil
}

func (s *channelAgentTestStore) IsMemberMuted(groupID, userID int64) (bool, error) {
	if member := s.groupMembers[groupID][userID]; member != nil {
		return member.Muted, nil
	}
	return false, nil
}

func (s *channelAgentTestStore) SetMemberMuted(groupID, userID int64, muted bool) error {
	if member := s.groupMembers[groupID][userID]; member != nil {
		member.Muted = muted
	}
	return nil
}

func (s *channelAgentTestStore) CanManageMember(groupID, actorID, targetID int64) (bool, error) {
	role, _ := s.GetMemberRole(groupID, actorID)
	return role == "owner" || role == "admin", nil
}

func (s *channelAgentTestStore) SetGroupAnnouncement(groupID int64, announcement string) error {
	if group := s.groups[groupID]; group != nil {
		group.Announcement = announcement
	}
	return nil
}

func (s *channelAgentTestStore) UpdateGroupProfile(groupID int64, name, avatarURL string) error {
	if group := s.groups[groupID]; group != nil {
		if name != "" {
			group.Name = name
		}
		group.AvatarURL = avatarURL
	}
	return nil
}

func (s *channelAgentTestStore) IsUserBot(userID int64) (bool, error) {
	user := s.users[userID]
	return user != nil && user.AccountType == types.AccountBot, nil
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
	if strings.TrimSpace(clientMsgID) != "" {
		key := topicID + "\x00" + strconv.FormatInt(fromUID, 10) + "\x00" + clientMsgID
		if id := s.clientIDs[key]; id > 0 {
			return id, true, nil
		}
		id, err := s.SaveMessageWithBlocks(topicID, fromUID, content, blocks, mode, role, msgType)
		if err != nil {
			return 0, false, err
		}
		s.clientIDs[key] = id
		return id, false, nil
	}
	id, err := s.SaveMessageWithBlocks(topicID, fromUID, content, blocks, mode, role, msgType)
	return id, false, err
}

func (s *channelAgentTestStore) EnsureChannelAgentEntry(entry *types.ChannelAgentEntry) (*types.ChannelAgentEntry, error) {
	accessMode := types.NormalizeChannelAgentAccessMode(entry.AccessMode)
	for _, existing := range s.entries {
		if existing.OwnerUID == entry.OwnerUID && existing.AgentUID == entry.AgentUID && existing.Channel == entry.Channel && existing.ChannelAppID == entry.ChannelAppID && existing.AccessMode == accessMode && existing.Status == "active" {
			return cloneEntry(existing), nil
		}
	}
	now := time.Now()
	next := cloneEntry(entry)
	next.ID = s.nextID
	s.nextID++
	next.AccessMode = types.NormalizeChannelAgentAccessMode(next.AccessMode)
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

func (s *channelAgentTestStore) ListChannelAgentEntriesByChannelApp(channel, channelAppID string) ([]*types.ChannelAgentEntry, error) {
	var out []*types.ChannelAgentEntry
	for _, entry := range s.entries {
		if entry.Channel == channel && entry.ChannelAppID == channelAppID && entry.Status == "active" {
			out = append(out, cloneEntry(entry))
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) RegenerateChannelAgentEntry(id, ownerUID int64, sceneKey, nextChannelAppID string) (*types.ChannelAgentEntry, error) {
	entry := s.entries[id]
	if entry == nil || entry.OwnerUID != ownerUID || entry.Status != "active" {
		return nil, nil
	}
	channelAppID := entry.ChannelAppID
	if strings.TrimSpace(nextChannelAppID) != "" {
		channelAppID = strings.TrimSpace(nextChannelAppID)
	}
	entry.Status = "revoked"
	return s.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     sceneKey,
		Channel:      entry.Channel,
		ChannelAppID: channelAppID,
		AccessMode:   entry.AccessMode,
		OwnerUID:     ownerUID,
		AgentUID:     entry.AgentUID,
	})
}

func (s *channelAgentTestStore) GetChannelAgentEntryByID(id int64) (*types.ChannelAgentEntry, error) {
	return cloneEntry(s.entries[id]), nil
}

func (s *channelAgentTestStore) CreateChannelIdentityMobileLink(link *types.ChannelIdentityMobileLink) (*types.ChannelIdentityMobileLink, error) {
	if link == nil {
		return nil, nil
	}
	now := time.Now()
	next := cloneMobileLink(link)
	next.ID = s.nextID
	s.nextID++
	next.Channel = normalizeChannel(next.Channel)
	if next.Status == "" {
		next.Status = "active"
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = now
	}
	next.UpdatedAt = now
	s.mobileLinks[strings.TrimSpace(next.SceneKey)] = next
	return cloneMobileLink(next), nil
}

func (s *channelAgentTestStore) GetChannelIdentityMobileLink(sceneKey string) (*types.ChannelIdentityMobileLink, error) {
	return cloneMobileLink(s.mobileLinks[strings.TrimSpace(sceneKey)]), nil
}

func (s *channelAgentTestStore) ConsumeChannelIdentityMobileLink(sceneKey, channel, channelAppID string) (*types.ChannelIdentityMobileLink, error) {
	link := s.mobileLinks[strings.TrimSpace(sceneKey)]
	if link == nil || link.Status != "active" || !link.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	if normalizeChannel(link.Channel) != normalizeChannel(channel) {
		return nil, nil
	}
	if expectedAppID := strings.TrimSpace(channelAppID); expectedAppID != "" && strings.TrimSpace(link.ChannelAppID) != expectedAppID {
		return nil, nil
	}
	now := time.Now()
	link.Status = "consumed"
	link.ConsumedAt = &now
	link.UpdatedAt = now
	return cloneMobileLink(link), nil
}

func (s *channelAgentTestStore) CreateChannelGroupMobileLink(link *types.ChannelGroupMobileLink) (*types.ChannelGroupMobileLink, error) {
	if link == nil {
		return nil, nil
	}
	now := time.Now()
	next := cloneGroupMobileLink(link)
	next.ID = s.nextID
	s.nextID++
	next.Channel = normalizeChannel(next.Channel)
	if next.Status == "" {
		next.Status = "active"
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = now
	}
	next.UpdatedAt = now
	s.groupLinks[strings.TrimSpace(next.SceneKey)] = next
	return cloneGroupMobileLink(next), nil
}

func (s *channelAgentTestStore) GetChannelGroupMobileLink(sceneKey string) (*types.ChannelGroupMobileLink, error) {
	return cloneGroupMobileLink(s.groupLinks[strings.TrimSpace(sceneKey)]), nil
}

func (s *channelAgentTestStore) ConsumeChannelGroupMobileLink(sceneKey, channel, channelAppID string) (*types.ChannelGroupMobileLink, error) {
	link := s.groupLinks[strings.TrimSpace(sceneKey)]
	if link == nil || link.Status != "active" || !link.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	if normalizeChannel(link.Channel) != normalizeChannel(channel) {
		return nil, nil
	}
	if expectedAppID := strings.TrimSpace(channelAppID); expectedAppID != "" && strings.TrimSpace(link.ChannelAppID) != expectedAppID {
		return nil, nil
	}
	now := time.Now()
	link.Status = "consumed"
	link.ConsumedAt = &now
	link.UpdatedAt = now
	return cloneGroupMobileLink(link), nil
}

func (s *channelAgentTestStore) UpsertChannelGroupBinding(binding *types.ChannelGroupBinding) (*types.ChannelGroupBinding, error) {
	if binding == nil {
		return nil, nil
	}
	now := time.Now()
	next := cloneGroupBinding(binding)
	next.Channel = normalizeChannel(next.Channel)
	if next.ChannelConversationType == "" {
		next.ChannelConversationType = "p2p"
	}
	key := groupBindingKey(next.Channel, next.ChannelAppID, next.ChannelUserID, next.ChannelConversationID, next.TopicID)
	if existing := s.groupBindings[key]; existing != nil {
		next.ID = existing.ID
		if next.ActorUID <= 0 {
			next.ActorUID = existing.ActorUID
		}
		if next.CanonicalUID <= 0 {
			next.CanonicalUID = existing.CanonicalUID
		}
		if next.GroupID <= 0 {
			next.GroupID = existing.GroupID
		}
		if next.TopicID == "" {
			next.TopicID = existing.TopicID
		}
		next.BoundAt = existing.BoundAt
	} else {
		next.ID = s.nextID
		s.nextID++
		next.BoundAt = now
	}
	if next.Status == "" {
		next.Status = types.ChannelAgentBindingActive
	}
	next.SelectedAt = now
	if next.Status == types.ChannelAgentBindingActive {
		next.LastUsedAt = &now
	}
	next.UpdatedAt = now
	s.groupBindings[key] = next
	return cloneGroupBinding(next), nil
}

func (s *channelAgentTestStore) ResolveChannelGroupBinding(query types.ChannelGroupBindingQuery) (*types.ChannelGroupBinding, error) {
	var selected *types.ChannelGroupBinding
	for _, binding := range s.groupBindings {
		if binding.Channel != normalizeChannel(query.Channel) || binding.ChannelAppID != query.ChannelAppID || binding.ChannelUserID != query.ChannelUserID {
			continue
		}
		if query.ChannelConversationID != "" && binding.ChannelConversationID != query.ChannelConversationID {
			continue
		}
		if query.ChannelConversationType != "" && binding.ChannelConversationType != query.ChannelConversationType {
			continue
		}
		if query.ActorUID > 0 && binding.ActorUID != query.ActorUID {
			continue
		}
		if query.GroupID > 0 && binding.GroupID != query.GroupID {
			continue
		}
		if strings.TrimSpace(query.TopicID) != "" && binding.TopicID != strings.TrimSpace(query.TopicID) {
			continue
		}
		if selected == nil || binding.UpdatedAt.After(selected.UpdatedAt) {
			selected = binding
		}
	}
	if selected == nil {
		return nil, nil
	}
	now := time.Now()
	selected.LastUsedAt = &now
	selected.UpdatedAt = now
	return cloneGroupBinding(selected), nil
}

func (s *channelAgentTestStore) ListChannelGroupBindingsForTopic(topicID string) ([]*types.ChannelGroupBinding, error) {
	var out []*types.ChannelGroupBinding
	for _, binding := range s.groupBindings {
		if binding.TopicID == topicID {
			out = append(out, cloneGroupBinding(binding))
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) RequestChannelAgentAccess(request *types.ChannelAgentAccessRequest) (*types.ChannelAgentAccessRequest, error) {
	now := time.Now()
	next := *request
	key := accessRequestKey(next.EntryID, next.Channel, next.ChannelAppID, next.ChannelUserID, next.ChannelConversationID)
	if existing := s.accessRequests[key]; existing != nil {
		next.ID = existing.ID
		next.RequestedAt = existing.RequestedAt
		if existing.Status == "approved" {
			next.Status = "approved"
			next.ReviewedAt = existing.ReviewedAt
			next.ReviewedByUID = existing.ReviewedByUID
		}
	} else {
		next.ID = s.nextID
		s.nextID++
		next.RequestedAt = now
	}
	if next.Status == "" {
		next.Status = "pending"
	}
	next.UpdatedAt = now
	s.accessRequests[key] = &next
	return cloneAccessRequest(&next), nil
}

func (s *channelAgentTestStore) ResolveChannelAgentAccessRequest(query types.ChannelAgentBindingQuery) (*types.ChannelAgentAccessRequest, error) {
	if request := s.accessRequestsByConversation(query, query.ChannelConversationID); request != nil {
		return cloneAccessRequest(request), nil
	}
	return nil, nil
}

func (s *channelAgentTestStore) accessRequestsByConversation(query types.ChannelAgentBindingQuery, conversationID string) *types.ChannelAgentAccessRequest {
	for _, request := range s.accessRequests {
		if request.Channel == query.Channel && request.ChannelAppID == query.ChannelAppID && request.ChannelUserID == query.ChannelUserID && request.ChannelConversationID == conversationID && (request.Status == "pending" || request.Status == "rejected") {
			if query.AgentUID > 0 && request.AgentUID != query.AgentUID {
				continue
			}
			if query.ActorUID > 0 && request.ActorUID != query.ActorUID {
				continue
			}
			return request
		}
	}
	return nil
}

func (s *channelAgentTestStore) ApproveChannelAgentAccessRequestsForActor(actorUID, agentUID, reviewerUID int64) ([]*types.ChannelAgentBinding, error) {
	var out []*types.ChannelAgentBinding
	for _, request := range s.accessRequests {
		if request.ActorUID != actorUID || request.AgentUID != agentUID || request.Status != "pending" {
			continue
		}
		now := time.Now()
		request.Status = "approved"
		request.ReviewedByUID = reviewerUID
		request.ReviewedAt = &now
		request.UpdatedAt = now
		binding, err := s.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
			Channel:                 request.Channel,
			ChannelAppID:            request.ChannelAppID,
			ChannelUserID:           request.ChannelUserID,
			ChannelConversationID:   request.ChannelConversationID,
			ChannelConversationType: request.ChannelConversationType,
			ActorUID:                request.ActorUID,
			OwnerUID:                request.OwnerUID,
			AgentUID:                request.AgentUID,
			EntryID:                 request.EntryID,
			Status:                  "active",
			DeviceAccessEnabled:     true,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	return out, nil
}

func (s *channelAgentTestStore) RejectChannelAgentAccessRequestsForActor(actorUID, agentUID, reviewerUID int64) error {
	for _, request := range s.accessRequests {
		if request.ActorUID == actorUID && request.AgentUID == agentUID && request.Status == "pending" {
			now := time.Now()
			request.Status = "rejected"
			request.ReviewedByUID = reviewerUID
			request.ReviewedAt = &now
			request.UpdatedAt = now
		}
	}
	return nil
}

func (s *channelAgentTestStore) ActivateChannelAgentBindingsForCanonicalUser(canonicalUID, agentUID, reviewerUID int64) ([]*types.ChannelAgentBinding, error) {
	var out []*types.ChannelAgentBinding
	for _, binding := range s.bindings {
		if binding.CanonicalUID == canonicalUID && binding.AgentUID == agentUID && (binding.Status == types.ChannelAgentBindingPendingApproval || binding.Status == types.ChannelAgentBindingActive) {
			binding.Status = types.ChannelAgentBindingActive
			binding.DeviceAccessEnabled = true
			binding.BoundAt = time.Now()
			binding.LastUsedAt = &binding.BoundAt
			binding.UpdatedAt = binding.BoundAt
			out = append(out, cloneBinding(binding))
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) RejectChannelAgentBindingsForCanonicalUser(canonicalUID, agentUID, reviewerUID int64) error {
	for _, binding := range s.bindings {
		if binding.CanonicalUID == canonicalUID && binding.AgentUID == agentUID && (binding.Status == types.ChannelAgentBindingPendingApproval || binding.Status == types.ChannelAgentBindingActive || binding.Status == types.ChannelAgentBindingPendingLogin) {
			binding.Status = types.ChannelAgentBindingRejected
			binding.UpdatedAt = time.Now()
		}
	}
	return nil
}

func (s *channelAgentTestStore) GetChannelAgentEntryBySceneKey(sceneKey string) (*types.ChannelAgentEntry, error) {
	for _, entry := range s.entries {
		if entry.SceneKey == sceneKey {
			return cloneEntry(entry), nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) ListChannelAgentBindingsForAgent(ownerUID, agentUID int64) ([]*types.ChannelAgentBinding, error) {
	var out []*types.ChannelAgentBinding
	for _, binding := range s.bindings {
		if binding.OwnerUID == ownerUID && binding.AgentUID == agentUID {
			out = append(out, cloneBinding(binding))
		}
	}
	return out, nil
}

func (s *channelAgentTestStore) UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error) {
	now := time.Now()
	next := cloneBinding(binding)
	key := bindingKey(next.Channel, next.ChannelAppID, next.ChannelUserID, next.ChannelConversationID, next.AgentUID)
	if existing := s.bindings[key]; existing != nil {
		next.ID = existing.ID
		if next.ActorUID <= 0 {
			next.ActorUID = existing.ActorUID
		}
		if next.CanonicalUID <= 0 {
			next.CanonicalUID = existing.CanonicalUID
		}
		if next.EntryID <= 0 {
			next.EntryID = existing.EntryID
		}
		if !next.DeviceAccessEnabled {
			next.DeviceAccessEnabled = existing.DeviceAccessEnabled
		}
		next.BoundAt = existing.BoundAt
	} else {
		next.ID = s.nextID
		s.nextID++
		next.BoundAt = now
	}
	if next.Status == "" {
		next.Status = types.ChannelAgentBindingActive
	}
	if next.Status == types.ChannelAgentBindingActive {
		next.LastUsedAt = &now
	}
	next.UpdatedAt = now
	s.bindings[key] = next
	return cloneBinding(next), nil
}

func (s *channelAgentTestStore) ResolveChannelAgentBinding(query types.ChannelAgentBindingQuery) (*types.ChannelAgentBinding, error) {
	if query.AgentUID > 0 {
		if binding := s.bindings[bindingKey(query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID, query.AgentUID)]; binding != nil && (query.ActorUID <= 0 || binding.ActorUID == query.ActorUID) {
			return cloneBinding(binding), nil
		}
		return nil, nil
	}
	var selected *types.ChannelAgentBinding
	for _, binding := range s.bindings {
		if binding.Channel != query.Channel || binding.ChannelAppID != query.ChannelAppID || binding.ChannelUserID != query.ChannelUserID || binding.ChannelConversationID != query.ChannelConversationID {
			continue
		}
		if query.ActorUID > 0 && binding.ActorUID != query.ActorUID {
			continue
		}
		if selected == nil || binding.UpdatedAt.After(selected.UpdatedAt) {
			selected = binding
		}
	}
	if selected != nil {
		return cloneBinding(selected), nil
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

func (s *channelAgentTestStore) ResolveChannelAgentBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	for _, binding := range s.bindings {
		if binding.ActorUID == actorUID && binding.AgentUID == agentUID && binding.Status == "active" {
			return cloneBinding(binding), nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) ResolveChannelAgentBindingForChannelUser(channel, channelAppID, channelUserID string) (*types.ChannelAgentBinding, error) {
	var selected *types.ChannelAgentBinding
	for _, binding := range s.bindings {
		if binding.Channel != channel || binding.ChannelAppID != channelAppID || binding.ChannelUserID != channelUserID || binding.CanonicalUID <= 0 {
			continue
		}
		if selected == nil || binding.UpdatedAt.After(selected.UpdatedAt) {
			selected = binding
		}
	}
	if selected == nil {
		return nil, nil
	}
	return cloneBinding(selected), nil
}

func (s *channelAgentTestStore) ResolveChannelAgentDeviceAccessBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	for _, binding := range s.bindings {
		if binding.ActorUID == actorUID && binding.AgentUID == agentUID && binding.Status == "active" && binding.CanonicalUID > 0 && binding.DeviceAccessEnabled {
			return cloneBinding(binding), nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64, enableDeviceAccess bool) (*types.ChannelAgentBinding, error) {
	s.linkCalls = append(s.linkCalls, channelAgentLinkCall{
		BindingID:    bindingID,
		ActorUID:     actorUID,
		AgentUID:     agentUID,
		CanonicalUID: canonicalUID,
	})
	if s.linkErr != nil {
		return nil, s.linkErr
	}
	for _, binding := range s.bindings {
		if binding.ID == bindingID && binding.ActorUID == actorUID && binding.AgentUID == agentUID && (binding.Status == types.ChannelAgentBindingPendingLogin || binding.Status == types.ChannelAgentBindingPendingApproval || binding.Status == types.ChannelAgentBindingActive || binding.Status == types.ChannelAgentBindingRejected) {
			if binding.CanonicalUID > 0 && binding.CanonicalUID != canonicalUID {
				return nil, store.ErrChannelAgentBindingAlreadyLinked
			}
			for _, existing := range s.bindings {
				if existing.ID == binding.ID {
					continue
				}
				if existing.Channel == binding.Channel && existing.ChannelAppID == binding.ChannelAppID && existing.ChannelUserID == binding.ChannelUserID && existing.CanonicalUID > 0 && existing.CanonicalUID != canonicalUID {
					return nil, store.ErrChannelAgentBindingAlreadyLinked
				}
			}
			if binding.Status == types.ChannelAgentBindingRejected {
				return cloneBinding(binding), nil
			}
			binding.CanonicalUID = canonicalUID
			if binding.Status != types.ChannelAgentBindingActive {
				binding.Status = types.ChannelAgentBindingPendingApproval
			}
			if enableDeviceAccess {
				binding.DeviceAccessEnabled = true
			}
			binding.UpdatedAt = time.Now()
			return cloneBinding(binding), nil
		}
	}
	return nil, nil
}

func (s *channelAgentTestStore) UpsertChannelAgentRoute(route *types.ChannelAgentRoute) (*types.ChannelAgentRoute, error) {
	now := time.Now()
	next := cloneRoute(route)
	key := routeKey(next.Channel, next.ChannelAppID, next.ChannelUserID, next.ChannelConversationID, next.ChannelConversationType)
	if existing := s.routes[key]; existing != nil {
		next.ID = existing.ID
		if next.ActorUID <= 0 {
			next.ActorUID = existing.ActorUID
		}
	} else {
		next.ID = s.nextID
		s.nextID++
	}
	if next.ChannelConversationType == "" {
		next.ChannelConversationType = "p2p"
	}
	if next.Source == "" {
		next.Source = "manual"
	}
	next.SelectedAt = now
	next.UpdatedAt = now
	next.LastUsedAt = &now
	s.routes[key] = next
	return cloneRoute(next), nil
}

func (s *channelAgentTestStore) ResolveChannelAgentRoute(query types.ChannelAgentRouteQuery) (*types.ChannelAgentRoute, error) {
	if query.ChannelConversationType == "" {
		query.ChannelConversationType = "p2p"
	}
	route := s.routes[routeKey(query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID, query.ChannelConversationType)]
	if route == nil {
		return nil, nil
	}
	if query.ActorUID > 0 && route.ActorUID > 0 && route.ActorUID != query.ActorUID {
		return nil, nil
	}
	now := time.Now()
	route.LastUsedAt = &now
	return cloneRoute(route), nil
}

func cloneEntry(entry *types.ChannelAgentEntry) *types.ChannelAgentEntry {
	if entry == nil {
		return nil
	}
	next := *entry
	return &next
}

func cloneMobileLink(link *types.ChannelIdentityMobileLink) *types.ChannelIdentityMobileLink {
	if link == nil {
		return nil
	}
	next := *link
	if link.ConsumedAt != nil {
		consumedAt := *link.ConsumedAt
		next.ConsumedAt = &consumedAt
	}
	return &next
}

func cloneGroupMobileLink(link *types.ChannelGroupMobileLink) *types.ChannelGroupMobileLink {
	if link == nil {
		return nil
	}
	next := *link
	if link.ConsumedAt != nil {
		consumedAt := *link.ConsumedAt
		next.ConsumedAt = &consumedAt
	}
	return &next
}

func cloneBinding(binding *types.ChannelAgentBinding) *types.ChannelAgentBinding {
	if binding == nil {
		return nil
	}
	next := *binding
	return &next
}

func cloneGroupBinding(binding *types.ChannelGroupBinding) *types.ChannelGroupBinding {
	if binding == nil {
		return nil
	}
	next := *binding
	return &next
}

func cloneRoute(route *types.ChannelAgentRoute) *types.ChannelAgentRoute {
	if route == nil {
		return nil
	}
	next := *route
	return &next
}

func cloneAccessRequest(request *types.ChannelAgentAccessRequest) *types.ChannelAgentAccessRequest {
	if request == nil {
		return nil
	}
	next := *request
	return &next
}

func channelIdentityKey(channel, appID, userID, conversationID string) string {
	return channel + "\x00" + appID + "\x00" + userID + "\x00" + conversationID
}

func bindingKey(channel, appID, userID, conversationID string, agentUID int64) string {
	return channelIdentityKey(channel, appID, userID, conversationID) + "\x00" + strconv.FormatInt(agentUID, 10)
}

func routeKey(channel, appID, userID, conversationID, conversationType string) string {
	if conversationType == "" {
		conversationType = "p2p"
	}
	return channelIdentityKey(channel, appID, userID, conversationID) + "\x00" + conversationType
}

func groupBindingKey(channel, appID, userID, conversationID, topicID string) string {
	return channelIdentityKey(channel, appID, userID, conversationID) + "\x00" + topicID
}

func accessRequestKey(entryID int64, channel, appID, userID, conversationID string) string {
	return strconv.FormatInt(entryID, 10) + "\x00" + channelIdentityKey(channel, appID, userID, conversationID)
}

func friendKey(fromUID, toUID int64) string {
	return strconv.FormatInt(fromUID, 10) + "\x00" + strconv.FormatInt(toUID, 10)
}

func parseFriendKey(key string) (int64, int64) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 2 {
		return 0, 0
	}
	fromUID, _ := strconv.ParseInt(parts[0], 10, 64)
	toUID, _ := strconv.ParseInt(parts[1], 10, 64)
	return fromUID, toUID
}
