package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestNormalizeChannelAgentAccessModeDefaultsToApprovalRequired(t *testing.T) {
	if got := types.NormalizeChannelAgentAccessMode(""); got != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("empty access mode = %q, want approval_required", got)
	}
	if got := types.NormalizeChannelAgentAccessMode("unknown"); got != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("unknown access mode = %q, want approval_required", got)
	}
	if got := types.NormalizeChannelAgentAccessMode(types.ChannelAgentAccessPublic); got != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("explicit public access mode = %q, want approval_required", got)
	}
}

func TestChannelAgentEntryAndBindingFlow(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "annika", DisplayName: "Annika", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: channelActorUsername("weixin", "", "openid-7"), DisplayName: "Weixin Alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "contract-agent", DisplayName: "Contract Agent", AccountType: types.AccountBot}
	db.owners[43] = 7
	db.bodyIDs[43] = "body-contract"
	db.friends[friendKey(8, 43)] = types.FriendAccepted
	db.friends[friendKey(43, 8)] = types.FriendAccepted
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
	if created.Entry.AccessMode != types.ChannelAgentAccessApprovalRequired {
		t.Fatalf("public access mode should be normalized to approval_required: %+v", created.Entry)
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
	var pending struct {
		Status        string                          `json:"status"`
		AccessRequest types.ChannelAgentAccessRequest `json:"access_request"`
	}
	if err := json.Unmarshal(confirmRec.Body.Bytes(), &pending); err != nil {
		t.Fatalf("decode pending response: %v", err)
	}
	if pending.Status != "pending_approval" || pending.AccessRequest.ActorUID <= 0 || pending.AccessRequest.AgentUID != 43 {
		t.Fatalf("unexpected pending response: %+v", pending)
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
	if unresolved.Bound || unresolved.Status != "pending" || len(db.bindings) != 0 {
		t.Fatalf("pending access should not create an active binding: %+v bindings=%+v", unresolved, db.bindings)
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
	if len(pendingFriends.Requests) != 1 || pendingFriends.Requests[0].FromUserID != pending.AccessRequest.ActorUID {
		t.Fatalf("owner should see bot pending friend request: %+v", pendingFriends.Requests)
	}

	acceptReq := httptest.NewRequest(http.MethodPost, "/api/friends/accept", bytes.NewBufferString(`{"agent_uid":43,"user_id":`+strconv.FormatInt(pending.AccessRequest.ActorUID, 10)+`}`))
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
	body := `{"binding_id":` + strconv.FormatInt(binding.ID, 10) + `,"link_token":"` + token + `"}`
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
	stored := db.bindings[bindingKey(binding.Channel, binding.ChannelAppID, binding.ChannelUserID, binding.ChannelConversationID)]
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
	users          map[int64]*types.User
	owners         map[int64]int64
	bodyIDs        map[int64]string
	entries        map[int64]*types.ChannelAgentEntry
	accessRequests map[string]*types.ChannelAgentAccessRequest
	bindings       map[string]*types.ChannelAgentBinding
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
	for _, existing := range s.entries {
		if existing.OwnerUID == entry.OwnerUID && existing.AgentUID == entry.AgentUID && existing.Channel == entry.Channel && existing.ChannelAppID == entry.ChannelAppID && existing.Status == "active" {
			accessMode := types.NormalizeChannelAgentAccessMode(entry.AccessMode)
			if existing.AccessMode != accessMode {
				existing.AccessMode = accessMode
				existing.UpdatedAt = time.Now()
			}
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
		AccessMode:   entry.AccessMode,
		OwnerUID:     ownerUID,
		AgentUID:     entry.AgentUID,
	})
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
	key := bindingKey(next.Channel, next.ChannelAppID, next.ChannelUserID, next.ChannelConversationID)
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
		next.BoundAt = existing.BoundAt
	} else {
		next.ID = s.nextID
		s.nextID++
		next.BoundAt = now
	}
	next.Status = "active"
	next.UpdatedAt = now
	s.bindings[key] = next
	return cloneBinding(next), nil
}

func (s *channelAgentTestStore) ResolveChannelAgentBinding(query types.ChannelAgentBindingQuery) (*types.ChannelAgentBinding, error) {
	if binding := s.bindings[bindingKey(query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID)]; binding != nil {
		return cloneBinding(binding), nil
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

func (s *channelAgentTestStore) LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64) (*types.ChannelAgentBinding, error) {
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
		if binding.ID == bindingID && binding.ActorUID == actorUID && binding.AgentUID == agentUID && binding.Status == "active" {
			if binding.CanonicalUID > 0 && binding.CanonicalUID != canonicalUID {
				return nil, store.ErrChannelAgentBindingAlreadyLinked
			}
			binding.CanonicalUID = canonicalUID
			binding.UpdatedAt = time.Now()
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

func cloneAccessRequest(request *types.ChannelAgentAccessRequest) *types.ChannelAgentAccessRequest {
	if request == nil {
		return nil
	}
	next := *request
	return &next
}

func bindingKey(channel, appID, userID, conversationID string) string {
	return channel + "\x00" + appID + "\x00" + userID + "\x00" + conversationID
}

func accessRequestKey(entryID int64, channel, appID, userID, conversationID string) string {
	return strconv.FormatInt(entryID, 10) + "\x00" + bindingKey(channel, appID, userID, conversationID)
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
