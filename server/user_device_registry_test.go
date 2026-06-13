package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestUserDeviceRegistryRegistersAndIssuesScopedGrants(t *testing.T) {
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	registry := newUserDeviceRegistry(time.Minute)
	registry.grantTT = 2 * time.Minute
	registry.now = func() time.Time { return now }

	device, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID:       " laptop-main ",
		DisplayName:    " Alice Laptop ",
		BodyID:         " body-main ",
		InstallationID: " install-main ",
		Capabilities:   []string{"read_file", "unknown", "write_file", "edit_file", "send_file", "read_file"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	if device.DeviceID != "laptop-main" || device.DisplayName != "Alice Laptop" {
		t.Fatalf("unexpected registered device: %#v", device)
	}
	if got := device.Capabilities; len(got) != 4 || got[0] != DeviceGrantReadFile || got[1] != DeviceGrantWriteFile || got[2] != DeviceGrantEditFile || got[3] != DeviceGrantSendFile {
		t.Fatalf("unexpected capabilities: %#v", got)
	}

	grants := registry.grantsForTurn(7, "p2p_7_42", "p2p", 42, "body-agent")
	if len(grants) != 1 {
		t.Fatalf("grants len = %d, want 1", len(grants))
	}
	grant := grants[0]
	if grant.Kind != "user_device_grant" || grant.Source != "catscompany" || grant.Status != "active" {
		t.Fatalf("unexpected grant envelope: %#v", grant)
	}
	if grant.OwnerUserID != "usr7" || grant.ActorUserID != "usr7" || grant.AgentID != "usr42" {
		t.Fatalf("unexpected grant identity: %#v", grant)
	}
	if grant.TopicID != "p2p_7_42" || grant.TopicType != "p2p" || grant.SessionKey != "session:v2:catscompany:p2p:p2p_7_42:agent:usr42" {
		t.Fatalf("unexpected grant route: %#v", grant)
	}
	if grant.DeviceID != "laptop-main" || grant.DeviceBodyID != "body-main" || grant.DeviceInstallationID != "install-main" {
		t.Fatalf("unexpected grant device: %#v", grant)
	}
	if len(grant.Operations) != 4 || grant.Operations[0] != DeviceGrantReadFile || grant.Operations[1] != DeviceGrantWriteFile || grant.Operations[2] != DeviceGrantEditFile || grant.Operations[3] != DeviceGrantSendFile {
		t.Fatalf("grant should expose registered file operations, got %#v", grant.Operations)
	}
	if grant.CreatedAt != unixMillis(now) || grant.ExpiresAt != unixMillis(now.Add(2*time.Minute)) {
		t.Fatalf("unexpected grant times: %#v", grant)
	}

	registry.now = func() time.Time { return now.Add(2*time.Minute + time.Second) }
	if grants := registry.grantsForTurn(7, "p2p_7_42", "p2p", 42, "body-agent"); len(grants) != 0 {
		t.Fatalf("expired device still issued grants: %#v", grants)
	}
}

func TestUserDeviceRegistrySelectsMentionedDeviceAndRemembersPreference(t *testing.T) {
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	registry := newUserDeviceRegistry(10 * time.Minute)
	registry.now = func() time.Time { return now }
	if _, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID:     "alice-laptop",
		DisplayName:  "Alice Laptop",
		BodyID:       "body-laptop",
		Capabilities: []string{"read_file"},
	}); err != nil {
		t.Fatalf("register laptop: %v", err)
	}
	registry.now = func() time.Time { return now.Add(time.Second) }
	if _, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID:     "alice-desktop",
		DisplayName:  "Alice Desktop",
		BodyID:       "body-desktop",
		Capabilities: []string{"read_file", "send_file"},
	}); err != nil {
		t.Fatalf("register desktop: %v", err)
	}

	ctx := registry.turnContext(7, "p2p_7_42", "p2p", 42, "body-agent", "请在 Alice Laptop 上读取文件")
	if ctx.Selection == nil || ctx.Selection.Status != DeviceSelectionSelected || ctx.Selection.SelectionSource != "explicit_mention" {
		t.Fatalf("unexpected explicit selection: %#v", ctx.Selection)
	}
	if ctx.Selection.SelectedDevice == nil || ctx.Selection.SelectedDevice.DeviceID != "alice-laptop" {
		t.Fatalf("selected device = %#v, want alice-laptop", ctx.Selection.SelectedDevice)
	}
	if got := ctx.Selection.SelectedDevice.Operations; len(got) != 1 || got[0] != DeviceGrantReadFile {
		t.Fatalf("selection should expose selected device operations: %#v", got)
	}
	if len(ctx.Grants) != 1 || ctx.Grants[0].DeviceID != "alice-laptop" {
		t.Fatalf("grants should be scoped to explicit selected device: %#v", ctx.Grants)
	}

	followup := registry.turnContext(7, "p2p_7_42", "p2p", 42, "body-agent", "继续读取")
	if followup.Selection == nil || followup.Selection.SelectionSource != "conversation_preference" {
		t.Fatalf("unexpected follow-up selection: %#v", followup.Selection)
	}
	if followup.Selection.SelectedDevice == nil || followup.Selection.SelectedDevice.DeviceID != "alice-laptop" {
		t.Fatalf("follow-up selected device = %#v, want alice-laptop", followup.Selection.SelectedDevice)
	}

	otherTopic := registry.turnContext(7, "p2p_7_99", "p2p", 99, "body-agent", "继续读取")
	if otherTopic.Selection == nil || otherTopic.Selection.SelectionSource != "most_recent_online" {
		t.Fatalf("unexpected other topic selection: %#v", otherTopic.Selection)
	}
	if otherTopic.Selection.SelectedDevice == nil || otherTopic.Selection.SelectedDevice.DeviceID != "alice-desktop" {
		t.Fatalf("other topic selected device = %#v, want alice-desktop", otherTopic.Selection.SelectedDevice)
	}
	if got := otherTopic.Selection.SelectedDevice.Operations; len(got) != 2 || got[0] != DeviceGrantReadFile || got[1] != DeviceGrantSendFile {
		t.Fatalf("other topic selected device should expose send_file: %#v", got)
	}
}

func TestDeviceHandlerRegistersHumanAndBotOwnerDevices(t *testing.T) {
	store := &deviceHandlerStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice"},
			42: {ID: 42, Username: "agent", AccountType: types.AccountBot},
		},
		botOwners: map[int64]int64{42: 7},
	}
	hub := NewHub(store, nil)
	handler := NewDeviceHandler(store, hub)

	registerReq := httptest.NewRequest(http.MethodPost, "/api/devices/register", bytes.NewBufferString(`{
		"device_id": "alice-laptop",
		"display_name": "Alice Laptop",
		"capabilities": ["read_file", "send_file"]
	}`))
	registerReq = registerReq.WithContext(context.WithValue(registerReq.Context(), uidKey, int64(7)))
	registerRec := httptest.NewRecorder()
	handler.HandleRegisterDevice(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("human register status = %d, body=%s", registerRec.Code, registerRec.Body.String())
	}

	botReq := httptest.NewRequest(http.MethodPost, "/api/devices/register", bytes.NewBufferString(`{
		"device_id": "bot-body-runtime",
		"body_id": "body-main"
	}`))
	botReq = botReq.WithContext(context.WithValue(botReq.Context(), uidKey, int64(42)))
	botRec := httptest.NewRecorder()
	handler.HandleRegisterDevice(botRec, botReq)
	if botRec.Code != http.StatusOK {
		t.Fatalf("bot register status = %d, body=%s", botRec.Code, botRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), uidKey, int64(7)))
	listRec := httptest.NewRecorder()
	handler.HandleListDevices(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var out struct {
		Devices []UserDevice `json:"devices"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(out.Devices) != 2 {
		t.Fatalf("devices len = %d, want 2: %#v", len(out.Devices), out.Devices)
	}
	for _, device := range out.Devices {
		if device.OwnerUserID != "usr7" {
			t.Fatalf("device registered to wrong owner: %#v", device)
		}
	}
}

func TestDeviceHandlerReportsOwnerScopedDeviceRPCStatus(t *testing.T) {
	store := &deviceHandlerStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice"},
			42: {ID: 42, Username: "agent", AccountType: types.AccountBot},
		},
		botOwners: map[int64]int64{42: 7},
	}
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)
	hub.db = store
	handler := NewDeviceHandler(store, hub)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-status",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
		Payload:   map[string]interface{}{"path": "redacted-from-status"},
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	req := httptest.NewRequest(http.MethodGet, "/api/devices/rpc-status", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleDeviceRPCStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rpc status response = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Pending []DeviceRPCPendingStatus `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode rpc status: %v", err)
	}
	if len(out.Pending) != 1 || out.Pending[0].RequestID != "rpc-status" || out.Pending[0].DeviceID != "alice-laptop" {
		t.Fatalf("unexpected rpc status: %#v", out.Pending)
	}
	if strings.Contains(rec.Body.String(), "redacted-from-status") {
		t.Fatalf("rpc status must not expose payload: %s", rec.Body.String())
	}

	filteredReq := httptest.NewRequest(http.MethodGet, "/api/devices/rpc-status?agent_id=usr99", nil)
	filteredReq = filteredReq.WithContext(context.WithValue(filteredReq.Context(), uidKey, int64(7)))
	filteredRec := httptest.NewRecorder()
	handler.HandleDeviceRPCStatus(filteredRec, filteredReq)
	if filteredRec.Code != http.StatusOK {
		t.Fatalf("filtered rpc status response = %d, body=%s", filteredRec.Code, filteredRec.Body.String())
	}
	var filtered struct {
		Pending []DeviceRPCPendingStatus `json:"pending"`
	}
	if err := json.Unmarshal(filteredRec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered rpc status: %v", err)
	}
	if len(filtered.Pending) != 0 {
		t.Fatalf("agent filter should hide other agent pending rpc: %#v", filtered.Pending)
	}
}

func TestBotRecipientIdentityIncludesCurrentActorDeviceGrants(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot},
			50: {ID: 50, Username: "bob", DisplayName: "Bob"},
		},
	}
	hub := NewHub(store, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"read_file", "send_file"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	targetClient := &Client{
		uid:         7,
		accountType: types.AccountHuman,
		send:        make(chan []byte, 1),
	}
	hub.addClient(targetClient)
	hub.bindDeviceClient(7, device, targetClient)
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		displayName: "Agent Runtime",
		send:        make(chan []byte, 1),
	}
	hub.addClient(botClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_7_42",
		Content: json.RawMessage(`"查一下本机文件"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "p2p_7_42", 0, payload, 99, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	grant := firstDeviceGrantMap(t, identity)
	if grant["ownerUserId"] != "usr7" || grant["actorUserId"] != "usr7" || grant["agentId"] != "usr42" {
		t.Fatalf("unexpected grant identity: %#v", grant)
	}
	if grant["topicId"] != "p2p_7_42" || grant["topicType"] != "p2p" || grant["agentBodyId"] != "body-agent" {
		t.Fatalf("unexpected grant scope: %#v", grant)
	}
	if grant["deviceId"] != "alice-laptop" || grant["deviceBodyId"] != "body-device" {
		t.Fatalf("unexpected grant device: %#v", grant)
	}
	selection := deviceSelectionMap(t, identity)
	if selection["status"] != "selected" || selection["selectionSource"] != "single_active_device" {
		t.Fatalf("unexpected device selection: %#v", selection)
	}
	selectedDevice, ok := selection["selectedDevice"].(map[string]interface{})
	if !ok || selectedDevice["deviceId"] != "alice-laptop" {
		t.Fatalf("unexpected selected device: %#v", selection["selectedDevice"])
	}

	humanMsg := hub.messageForRecipient(7, 50, "p2p_7_50", 0, payload, 100)
	humanIdentity := metadataMapFromServerMessage(t, humanMsg, "catsco_identity")
	if _, ok := humanIdentity["device_grants"]; ok {
		t.Fatalf("human recipient should not receive device grants: %#v", humanIdentity["device_grants"])
	}
	if _, ok := humanIdentity["device_selection"]; ok {
		t.Fatalf("human recipient should not receive device selection: %#v", humanIdentity["device_selection"])
	}
}

func TestBotRecipientIdentityUsesLinkedChannelDeviceOwner(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[42] = &types.User{ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_weixin_user", DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.owners[42] = 7
	db.friends[friendKey(9, 42)] = types.FriendAccepted
	db.friends[friendKey(42, 9)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-100",
		ActorUID:      100,
		CanonicalUID:  7,
		OwnerUID:      7,
		AgentUID:      42,
		Status:        "active",
	}); err != nil {
		t.Fatalf("seed channel binding: %v", err)
	}

	hub := NewHub(db, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	targetClient := &Client{
		uid:                  7,
		accountType:          types.AccountHuman,
		deviceOwnerUID:       7,
		deviceID:             "alice-laptop",
		deviceBodyID:         "body-device",
		deviceInstallationID: "install-device",
		send:                 make(chan []byte, 1),
	}
	hub.addClient(targetClient)
	hub.bindDeviceClient(7, device, targetClient)
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 1),
	}
	hub.addClient(botClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_100_42",
		Content: json.RawMessage(`"查一下我的电脑文件"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(100, "p2p_100_42", 0, payload, 199, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok || permissions["device_owner_user_id"] != "usr7" || permissions["device_owner_source"] != "channel_identity_link" {
		t.Fatalf("unexpected permissions: %#v", identity["permissions"])
	}
	grant := firstDeviceGrantMap(t, identity)
	if grant["ownerUserId"] != "usr7" || grant["actorUserId"] != "usr100" || grant["deviceId"] != "alice-laptop" {
		t.Fatalf("unexpected delegated grant: %#v", grant)
	}
	if grant["identitySource"] != "channel_identity_link" {
		t.Fatalf("delegated grant must be explicitly marked as channel delegated: %#v", grant)
	}
	selection := deviceSelectionMap(t, identity)
	if selection["ownerUserId"] != "usr7" || selection["actorUserId"] != "usr100" {
		t.Fatalf("unexpected delegated selection: %#v", selection)
	}
}

func TestBotRecipientIdentityUsesCanonicalUserDeviceNotAgentOwnerDevice(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[42] = &types.User{ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_weixin_user", DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.owners[42] = 7
	db.friends[friendKey(9, 42)] = types.FriendAccepted
	db.friends[friendKey(42, 9)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-100",
		ActorUID:      100,
		CanonicalUID:  9,
		OwnerUID:      7,
		AgentUID:      42,
		Status:        "active",
	}); err != nil {
		t.Fatalf("seed channel binding: %v", err)
	}

	hub := NewHub(db, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	ownerDevice, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "owner-laptop",
		DisplayName:    "Owner Laptop",
		BodyID:         "body-owner",
		InstallationID: "install-owner",
		Capabilities:   []string{"read_file", "write_file"},
	})
	if err != nil {
		t.Fatalf("register owner device: %v", err)
	}
	bobDevice, err := hub.userDevices.register(9, RegisterUserDeviceRequest{
		DeviceID:       "bob-laptop",
		DisplayName:    "Bob Laptop",
		BodyID:         "body-bob",
		InstallationID: "install-bob",
		Capabilities:   []string{"read_file", "write_file"},
	})
	if err != nil {
		t.Fatalf("register bob device: %v", err)
	}
	ownerClient := &Client{uid: 7, accountType: types.AccountHuman, deviceOwnerUID: 7, deviceID: "owner-laptop", deviceBodyID: "body-owner", deviceInstallationID: "install-owner", send: make(chan []byte, 1)}
	bobClient := &Client{uid: 9, accountType: types.AccountHuman, deviceOwnerUID: 9, deviceID: "bob-laptop", deviceBodyID: "body-bob", deviceInstallationID: "install-bob", send: make(chan []byte, 1)}
	botClient := &Client{uid: 42, accountType: types.AccountBot, bodyID: "body-agent", send: make(chan []byte, 1)}
	hub.addClient(ownerClient)
	hub.addClient(bobClient)
	hub.addClient(botClient)
	hub.bindDeviceClient(7, ownerDevice, ownerClient)
	hub.bindDeviceClient(9, bobDevice, bobClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_100_42",
		Content: json.RawMessage(`"在我的电脑上写一个文件"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(100, "p2p_100_42", 0, payload, 201, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok || permissions["device_owner_user_id"] != "usr9" || permissions["device_owner_source"] != "channel_identity_link" {
		t.Fatalf("unexpected permissions: %#v", identity["permissions"])
	}
	grant := firstDeviceGrantMap(t, identity)
	if grant["ownerUserId"] != "usr9" || grant["actorUserId"] != "usr100" || grant["deviceId"] != "bob-laptop" {
		t.Fatalf("delegated channel grant must target Bob's device, not owner device: %#v", grant)
	}
	selection := deviceSelectionMap(t, identity)
	if selection["ownerUserId"] != "usr9" || selection["actorUserId"] != "usr100" {
		t.Fatalf("unexpected delegated selection: %#v", selection)
	}
}

func TestBotRecipientIdentityDoesNotUseAgentOwnerDeviceWithoutChannelLink(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[42] = &types.User{ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_weixin_user", DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.owners[42] = 7
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "weixin",
		ChannelAppID:  "wx_app",
		ChannelUserID: "openid-100",
		ActorUID:      100,
		OwnerUID:      7,
		AgentUID:      42,
		Status:        "active",
	}); err != nil {
		t.Fatalf("seed channel binding: %v", err)
	}

	hub := NewHub(db, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-owner-device",
		InstallationID: "install-owner-device",
		Capabilities:   []string{"read_file", "write_file"},
	})
	if err != nil {
		t.Fatalf("register owner device: %v", err)
	}
	targetClient := &Client{
		uid:                  7,
		accountType:          types.AccountHuman,
		deviceOwnerUID:       7,
		deviceID:             "alice-laptop",
		deviceBodyID:         "body-owner-device",
		deviceInstallationID: "install-owner-device",
		send:                 make(chan []byte, 1),
	}
	hub.addClient(targetClient)
	hub.bindDeviceClient(7, device, targetClient)
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 1),
	}
	hub.addClient(botClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_100_42",
		Content: json.RawMessage(`"在你的电脑上创建文件"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(100, "p2p_100_42", 0, payload, 200, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing permissions: %#v", identity)
	}
	if _, ok := permissions["device_owner_user_id"]; ok {
		t.Fatalf("unlinked channel actor must not receive device owner permissions: %#v", permissions)
	}
	if _, ok := identity["device_grants"]; ok {
		t.Fatalf("unlinked channel actor should not receive owner device grants: %#v", identity["device_grants"])
	}
	if _, ok := identity["device_selection"]; ok {
		t.Fatalf("unlinked channel actor should not receive device selection: %#v", identity["device_selection"])
	}
}

func TestBotRecipientIdentityDoesNotGrantActiveButUnroutableDevice(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot},
		},
	}
	hub := NewHub(store, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	if _, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:     "alice-laptop",
		DisplayName:  "Alice Laptop",
		Capabilities: []string{"read_file"},
	}); err != nil {
		t.Fatalf("register device: %v", err)
	}
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 1),
	}
	hub.addClient(botClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_7_42",
		Content: json.RawMessage(`"查一下本机文件"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "p2p_7_42", 0, payload, 99, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	if _, ok := identity["device_grants"]; ok {
		t.Fatalf("unroutable device should not receive grants: %#v", identity["device_grants"])
	}
	selection := deviceSelectionMap(t, identity)
	if selection["status"] != string(DeviceSelectionUnavailable) || selection["selectionSource"] != "no_routable_devices" {
		t.Fatalf("unexpected selection for unroutable device: %#v", selection)
	}
	candidates, ok := selection["candidates"].([]interface{})
	if !ok || len(candidates) != 1 {
		t.Fatalf("expected one unavailable candidate: %#v", selection["candidates"])
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok || candidate["deviceId"] != "alice-laptop" || candidate["routable"] != false || candidate["unavailableReason"] != "route_unavailable" {
		t.Fatalf("unexpected unavailable candidate: %#v", candidate)
	}
}

func TestHistoryMessagesDoNotReissueDeviceGrantsForBotRecipient(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
			42: {ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot},
		},
		history: []*types.Message{{
			ID:      31,
			TopicID: "p2p_7_42",
			FromUID: 7,
			Content: "missed file request",
			MsgType: "text",
		}},
	}
	hub := NewHub(store, nil)
	if _, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:     "alice-laptop",
		Capabilities: []string{"read_file"},
	}); err != nil {
		t.Fatalf("register device: %v", err)
	}
	botClient := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 2),
	}
	hub.addClient(botClient)

	hub.handleGet(botClient, &MsgClientGet{
		ID:    "history-device-grants",
		Topic: "p2p_7_42",
		What:  "history",
		SeqID: 0,
	})

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	if _, ok := identity["device_grants"]; ok {
		t.Fatalf("history message should not receive executable device grants: %#v", identity["device_grants"])
	}
	if _, ok := identity["device_selection"]; ok {
		t.Fatalf("history message should not receive executable device selection: %#v", identity["device_selection"])
	}
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok {
		t.Fatalf("permissions = %#v, want object", identity["permissions"])
	}
	if permissions["replay"] != true || permissions["device_access"] != "non_executable_history" {
		t.Fatalf("unexpected history permissions: %#v", permissions)
	}

	var ctrl ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &ctrl)
	if ctrl.Ctrl == nil || ctrl.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected history completion ctrl: %#v", ctrl.Ctrl)
	}
}

func firstDeviceGrantMap(t *testing.T, identity map[string]interface{}) map[string]interface{} {
	t.Helper()
	grants, ok := identity["device_grants"].([]interface{})
	if !ok || len(grants) == 0 {
		t.Fatalf("device_grants = %#v, want non-empty array", identity["device_grants"])
	}
	grant, ok := grants[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first device grant = %#v, want object", grants[0])
	}
	return grant
}

func deviceSelectionMap(t *testing.T, identity map[string]interface{}) map[string]interface{} {
	t.Helper()
	selection, ok := identity["device_selection"].(map[string]interface{})
	if !ok {
		t.Fatalf("device_selection = %#v, want object", identity["device_selection"])
	}
	return selection
}

type deviceHandlerStore struct {
	store.Store
	users     map[int64]*types.User
	botOwners map[int64]int64
}

func (s *deviceHandlerStore) GetUser(id int64) (*types.User, error) {
	if user, ok := s.users[id]; ok {
		return user, nil
	}
	return nil, errors.New("user not found")
}

func (s *deviceHandlerStore) GetBotOwner(botUID int64) (int64, error) {
	if owner, ok := s.botOwners[botUID]; ok {
		return owner, nil
	}
	return 0, errors.New("owner not found")
}
