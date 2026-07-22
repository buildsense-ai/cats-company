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
		OS:             "win32",
		BodyID:         " body-main ",
		InstallationID: " install-main ",
		Capabilities:   []string{"read_file", "unknown", "resolve_common_directory", "write_file", "edit_file", "send_file", "read_file"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	if device.DeviceID != "laptop-main" || device.DisplayName != "Alice Laptop" {
		t.Fatalf("unexpected registered device: %#v", device)
	}
	if device.OS != "windows" {
		t.Fatalf("device OS = %q, want windows", device.OS)
	}
	if got := device.Capabilities; len(got) != 5 || got[0] != DeviceGrantReadFile || got[1] != DeviceGrantResolveDir || got[2] != DeviceGrantWriteFile || got[3] != DeviceGrantEditFile || got[4] != DeviceGrantSendFile {
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
	if len(grant.Operations) != 5 || grant.Operations[0] != DeviceGrantReadFile || grant.Operations[1] != DeviceGrantResolveDir || grant.Operations[2] != DeviceGrantWriteFile || grant.Operations[3] != DeviceGrantEditFile || grant.Operations[4] != DeviceGrantSendFile {
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

func TestUserDeviceRegistryStoresCurrentModelStatus(t *testing.T) {
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	registry := newUserDeviceRegistry(time.Minute)
	registry.now = func() time.Time { return now }

	device, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID: "laptop-main",
		Status:   "online",
		ModelStatus: &DeviceModelStatus{
			Source:          "relay",
			Model:           "MiniMax-M3",
			ReasoningEffort: " high ",
		},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	if device.ModelStatus == nil {
		t.Fatalf("expected model status")
	}
	if device.ModelStatus.Source != "relay" || device.ModelStatus.Model != "MiniMax-M3" || device.ModelStatus.ReasoningEffort != "high" || device.ModelStatus.UpdatedAt != unixMillis(now) {
		t.Fatalf("unexpected model status: %#v", device.ModelStatus)
	}

	hub := &Hub{userDevices: registry}
	status, ok := LatestDeviceModelStatus(hub, 7)
	if !ok {
		t.Fatalf("expected latest model status")
	}
	if status.Source != "relay" || status.Model != "MiniMax-M3" {
		t.Fatalf("unexpected latest model status: %#v", status)
	}
}

func TestDeviceModelStatusForBodyDoesNotUseAnotherNewerDevice(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	registry := newUserDeviceRegistry(10 * time.Minute)
	registry.now = func() time.Time { return now }
	if _, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID: "agent-device",
		BodyID:   "body-agent",
		ModelStatus: &DeviceModelStatus{
			Source: "relay",
			Model:  "gpt-5.6-sol",
		},
	}); err != nil {
		t.Fatalf("register agent device: %v", err)
	}
	registry.now = func() time.Time { return now.Add(time.Minute) }
	if _, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID: "owner-laptop",
		BodyID:   "body-owner-laptop",
		ModelStatus: &DeviceModelStatus{
			Source: "relay",
			Model:  "minimax-m2.7",
		},
	}); err != nil {
		t.Fatalf("register owner laptop: %v", err)
	}

	hub := &Hub{userDevices: registry}
	latest, ok := LatestDeviceModelStatus(hub, 7)
	if !ok || latest.Model != "minimax-m2.7" {
		t.Fatalf("latest owner model=%+v ok=%v", latest, ok)
	}
	bot, ok := DeviceModelStatusForBody(hub, 7, "body-agent")
	if !ok || bot.Model != "gpt-5.6-sol" {
		t.Fatalf("bound bot model=%+v ok=%v", bot, ok)
	}
	if _, ok := DeviceModelStatusForBody(hub, 7, "body-missing"); ok {
		t.Fatal("a missing body must not fall back to another owner device")
	}
}

func TestDeviceModelStatusForBodySupportsLegacyDeviceIDBinding(t *testing.T) {
	registry := newUserDeviceRegistry(time.Minute)
	registry.now = func() time.Time { return time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) }
	if _, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID: "legacy-body-id",
		ModelStatus: &DeviceModelStatus{
			Source: "custom",
			Model:  "private-model",
		},
	}); err != nil {
		t.Fatalf("register legacy device: %v", err)
	}

	status, ok := DeviceModelStatusForBody(&Hub{userDevices: registry}, 7, "legacy-body-id")
	if !ok || status.Source != "custom" || status.Model != "private-model" {
		t.Fatalf("status=%+v ok=%v", status, ok)
	}
}

func TestUserDeviceRegistryNormalizesCustomModelStatus(t *testing.T) {
	registry := newUserDeviceRegistry(time.Minute)
	registry.now = func() time.Time { return time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC) }

	device, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID: "laptop-main",
		ModelStatus: &DeviceModelStatus{
			Source: "custom",
		},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	if device.ModelStatus == nil || device.ModelStatus.Source != "custom" || device.ModelStatus.Model != "自定义模型" {
		t.Fatalf("unexpected custom model status: %#v", device.ModelStatus)
	}
}

func TestUserDeviceRegistryIssuesShellGrantWhenDeviceCapabilityIsRegistered(t *testing.T) {
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	registry := newUserDeviceRegistry(time.Minute)
	registry.now = func() time.Time { return now }

	device, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID:     "shell-laptop",
		DisplayName:  "Shell Laptop",
		BodyID:       "body-shell",
		Capabilities: []string{"read_file", "execute_shell"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	grants := registry.grantsForTurn(7, "p2p_7_42", "p2p", 42, "body-agent")
	if len(grants) != 1 {
		t.Fatalf("grants len = %d, want 1", len(grants))
	}
	if grants[0].DeviceID != device.DeviceID {
		t.Fatalf("grant should target registered shell device, got %#v", grants[0])
	}
	if !hasGrantOperation(grants[0].Operations, DeviceGrantExecuteShell) {
		t.Fatalf("grant should expose execute_shell when registered, got %#v", grants[0].Operations)
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

func TestUserDeviceRegistryGroupSessionKeyScopesActor(t *testing.T) {
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	registry := newUserDeviceRegistry(10 * time.Minute)
	registry.now = func() time.Time { return now }
	if _, err := registry.register(7, RegisterUserDeviceRequest{
		DeviceID:     "alice-laptop",
		DisplayName:  "Alice Laptop",
		BodyID:       "body-laptop",
		Capabilities: []string{"read_file"},
	}); err != nil {
		t.Fatalf("register alice device: %v", err)
	}
	if _, err := registry.register(8, RegisterUserDeviceRequest{
		DeviceID:     "bob-laptop",
		DisplayName:  "Bob Laptop",
		BodyID:       "body-bob",
		Capabilities: []string{"read_file"},
	}); err != nil {
		t.Fatalf("register bob device: %v", err)
	}

	alice := registry.turnContext(7, "grp_80", "group", 42, "body-agent", "看文件")
	bob := registry.turnContext(8, "grp_80", "group", 42, "body-agent", "看文件")
	if len(alice.Grants) != 1 || len(bob.Grants) != 1 {
		t.Fatalf("expected one grant each, alice=%#v bob=%#v", alice.Grants, bob.Grants)
	}
	if alice.Grants[0].SessionKey != "session:v2:catscompany:group:grp_80%3Aactor%3Ausr7:agent:usr42" {
		t.Fatalf("unexpected alice group session key: %s", alice.Grants[0].SessionKey)
	}
	if bob.Grants[0].SessionKey != "session:v2:catscompany:group:grp_80%3Aactor%3Ausr8:agent:usr42" {
		t.Fatalf("unexpected bob group session key: %s", bob.Grants[0].SessionKey)
	}
	if alice.Grants[0].SessionKey == bob.Grants[0].SessionKey {
		t.Fatalf("group grants should be scoped by actor")
	}
	if alice.Selection == nil || alice.Selection.SessionKey != alice.Grants[0].SessionKey {
		t.Fatalf("alice selection should use the same scoped key as grant: %#v", alice.Selection)
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
		OS:             "windows",
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
	runtime := metadataMapFromServerMessage(t, &msg, "xiaoba_runtime")
	if runtime["schema"] != "xiaoba.runtime.v1" {
		t.Fatalf("unexpected xiaoba runtime schema: %#v", runtime)
	}
	devices, ok := runtime["devices"].([]interface{})
	if !ok || len(devices) != 1 {
		t.Fatalf("xiaoba runtime devices = %#v, want one device", runtime["devices"])
	}
	runtimeDevice, ok := devices[0].(map[string]interface{})
	if !ok {
		t.Fatalf("xiaoba runtime device = %#v, want object", devices[0])
	}
	if runtimeDevice["userId"] != "usr7" || runtimeDevice["userName"] != "Alice" || runtimeDevice["deviceId"] != "alice-laptop" || runtimeDevice["label"] != "Alice Laptop" || runtimeDevice["os"] != "windows" {
		t.Fatalf("unexpected xiaoba runtime device: %#v", runtimeDevice)
	}

	humanMsg := hub.messageForRecipient(7, 50, "p2p_7_50", 0, payload, 100)
	humanIdentity := metadataMapFromServerMessage(t, humanMsg, "catsco_identity")
	if _, ok := humanIdentity["device_grants"]; ok {
		t.Fatalf("human recipient should not receive device grants: %#v", humanIdentity["device_grants"])
	}
	if _, ok := humanIdentity["device_selection"]; ok {
		t.Fatalf("human recipient should not receive device selection: %#v", humanIdentity["device_selection"])
	}
	if _, ok := humanMsg.Data.Metadata["xiaoba_runtime"]; ok {
		t.Fatalf("human recipient should not receive xiaoba runtime facts: %#v", humanMsg.Data.Metadata["xiaoba_runtime"])
	}

	payload.Metadata = map[string]interface{}{"channel_native_group_binding_id": int64(1)}
	nativeGroupMsg := hub.messageForRecipient(7, 42, "grp_80", 0, payload, 101)
	nativeGroupIdentity := metadataMapFromServerMessage(t, nativeGroupMsg, "catsco_identity")
	if _, ok := nativeGroupIdentity["device_grants"]; ok {
		t.Fatalf("native channel group must not receive device grants: %#v", nativeGroupIdentity["device_grants"])
	}
	if _, ok := nativeGroupIdentity["device_selection"]; ok {
		t.Fatalf("native channel group must not receive device selection: %#v", nativeGroupIdentity["device_selection"])
	}
	if _, ok := nativeGroupMsg.Data.Metadata["xiaoba_runtime"]; ok {
		t.Fatalf("native channel group must not receive xiaoba runtime facts: %#v", nativeGroupMsg.Data.Metadata["xiaoba_runtime"])
	}
}

func TestXiaobaRuntimeMetadataIncludesGroupHumanReadyDevices(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			42: {ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7, Username: "alice", DisplayName: "Alice"},
			{GroupID: 80, UserID: 9, Username: "bob", DisplayName: "Bob"},
			{GroupID: 80, UserID: 42, Username: "agent", DisplayName: "Agent", IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	aliceDevice, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:    "alice-laptop",
		DisplayName: "Alice Laptop",
		OS:          "windows",
	})
	if err != nil {
		t.Fatalf("register Alice device: %v", err)
	}
	bobDevice, err := hub.userDevices.register(9, RegisterUserDeviceRequest{
		DeviceID:    "bob-mac",
		DisplayName: "Bob Mac",
		OS:          "darwin",
	})
	if err != nil {
		t.Fatalf("register Bob device: %v", err)
	}
	aliceClient := &Client{uid: 7, accountType: types.AccountHuman, deviceOwnerUID: 7, deviceID: "alice-laptop", send: make(chan []byte, 1)}
	bobClient := &Client{uid: 9, accountType: types.AccountHuman, deviceOwnerUID: 9, deviceID: "bob-mac", send: make(chan []byte, 1)}
	botClient := &Client{uid: 42, accountType: types.AccountBot, bodyID: "body-agent", send: make(chan []byte, 1)}
	hub.addClient(aliceClient)
	hub.addClient(bobClient)
	hub.addClient(botClient)
	hub.bindDeviceClient(7, aliceDevice, aliceClient)
	hub.bindDeviceClient(9, bobDevice, bobClient)

	runtime := hub.buildXiaobaRuntimeMetadata(7, 42, "grp_80")
	if runtime["schema"] != "xiaoba.runtime.v1" {
		t.Fatalf("unexpected xiaoba runtime schema: %#v", runtime)
	}
	devices, ok := runtime["devices"].([]map[string]interface{})
	if !ok || len(devices) != 2 {
		t.Fatalf("runtime devices = %#v, want two devices", runtime["devices"])
	}
	if devices[0]["userId"] != "usr7" || devices[0]["userName"] != "Alice" || devices[0]["deviceId"] != "alice-laptop" || devices[0]["os"] != "windows" {
		t.Fatalf("unexpected Alice runtime device: %#v", devices[0])
	}
	if devices[1]["userId"] != "usr9" || devices[1]["userName"] != "Bob" || devices[1]["deviceId"] != "bob-mac" || devices[1]["os"] != "macos" {
		t.Fatalf("unexpected Bob runtime device: %#v", devices[1])
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
	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:             "weixin",
		ChannelAppID:        "wx_app",
		ChannelUserID:       "openid-100",
		ActorUID:            100,
		CanonicalUID:        7,
		DeviceAccessEnabled: true,
		OwnerUID:            7,
		AgentUID:            42,
		Status:              "active",
	})
	if err != nil {
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
		Metadata: withChannelBindingDeliveryMetadata(map[string]interface{}{
			"source_channel":            "weixin",
			"channel_app_id":            "wx_app",
			"channel_user_id":           "openid-100",
			"channel_conversation_type": "p2p",
		}, binding),
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

func TestBotRecipientIdentityRejectsSpoofedChannelDeviceOwnerMetadata(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman}
	db.users[42] = &types.User{ID: 42, Username: "agent", DisplayName: "Agent", AccountType: types.AccountBot}
	db.users[100] = &types.User{ID: 100, Username: "ch_weixin_user", DisplayName: "Weixin User", AccountType: types.AccountHuman}
	db.owners[42] = 7
	_, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:             "weixin",
		ChannelAppID:        "wx_app",
		ChannelUserID:       "openid-100",
		ActorUID:            100,
		CanonicalUID:        7,
		DeviceAccessEnabled: true,
		OwnerUID:            7,
		AgentUID:            42,
		Status:              "active",
	})
	if err != nil {
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
		Metadata: map[string]interface{}{
			"source_channel":                   "weixin",
			"channel_app_id":                   "wx_app",
			"channel_user_id":                  "openid-100",
			"channel_conversation_type":        "p2p",
			"channel_actor_uid":                float64(100),
			"channel_canonical_uid":            float64(7),
			"channel_identity_trust":           "weixin_official_account_callback",
			"channel_binding_delivery_trusted": true,
		},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(100, "p2p_100_42", 0, payload, 198, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing permissions: %#v", identity)
	}
	if _, ok := permissions["device_owner_user_id"]; ok {
		t.Fatalf("untrusted channel metadata must not grant device owner permissions: %#v", permissions)
	}
	if _, ok := identity["device_grants"]; ok {
		t.Fatalf("untrusted channel metadata must not issue delegated device grants: %#v", identity["device_grants"])
	}
	if _, ok := identity["device_selection"]; ok {
		t.Fatalf("untrusted channel metadata must not select delegated device: %#v", identity["device_selection"])
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
	binding, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:             "weixin",
		ChannelAppID:        "wx_app",
		ChannelUserID:       "openid-100",
		ActorUID:            100,
		CanonicalUID:        9,
		DeviceAccessEnabled: true,
		OwnerUID:            7,
		AgentUID:            42,
		Status:              "active",
	})
	if err != nil {
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
		Metadata: withChannelBindingDeliveryMetadata(map[string]interface{}{
			"source_channel":            "weixin",
			"channel_app_id":            "wx_app",
			"channel_user_id":           "openid-100",
			"channel_conversation_type": "p2p",
		}, binding),
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

	canonicalPayload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_9_42",
		Content: json.RawMessage(`"移动端继续操作我的电脑"`),
		Metadata: withChannelBindingDeliveryMetadata(map[string]interface{}{
			"source_channel":            "weixin",
			"channel_app_id":            "wx_app",
			"channel_user_id":           "openid-100",
			"channel_conversation_type": "p2p",
		}, binding),
	})
	if err != nil {
		t.Fatalf("normalize canonical request: %v", err)
	}
	hub.fanoutNormalizedMessage(9, "p2p_9_42", 0, canonicalPayload, 202, nil)

	var canonicalMsg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &canonicalMsg)
	canonicalIdentity := metadataMapFromServerMessage(t, &canonicalMsg, "catsco_identity")
	canonicalPermissions, ok := canonicalIdentity["permissions"].(map[string]interface{})
	if !ok || canonicalPermissions["device_owner_user_id"] != "usr9" || canonicalPermissions["device_owner_source"] != "actor" {
		t.Fatalf("canonical mobile actor should use own device permissions: %#v", canonicalIdentity["permissions"])
	}
	canonicalGrant := firstDeviceGrantMap(t, canonicalIdentity)
	if canonicalGrant["ownerUserId"] != "usr9" || canonicalGrant["actorUserId"] != "usr9" || canonicalGrant["deviceId"] != "bob-laptop" {
		t.Fatalf("canonical mobile grant should target Bob's own device: %#v", canonicalGrant)
	}
}

func TestBotRecipientIdentityKeepsActorDeviceForDirectFriendChatWithExistingChannelBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	db.users[42] = &types.User{ID: 42, Username: "shared-agent", DisplayName: "Shared Agent", AccountType: types.AccountBot}
	db.owners[42] = 7
	db.friends[friendKey(9, 42)] = types.FriendAccepted
	db.friends[friendKey(42, 9)] = types.FriendAccepted
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:             "weixin",
		ChannelAppID:        "wx_app",
		ChannelUserID:       "openid-bob",
		ActorUID:            9,
		CanonicalUID:        9,
		DeviceAccessEnabled: true,
		OwnerUID:            7,
		AgentUID:            42,
		Status:              "active",
	}); err != nil {
		t.Fatalf("seed channel binding: %v", err)
	}

	hub := NewHub(db, nil)
	hub.userDevices.now = func() time.Time { return time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC) }
	ownerDevice, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "owner-cloud",
		DisplayName:    "Owner Cloud",
		BodyID:         "body-owner-cloud",
		InstallationID: "install-owner-cloud",
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
		Capabilities:   []string{"read_file", "write_file", "edit_file"},
	})
	if err != nil {
		t.Fatalf("register bob device: %v", err)
	}
	ownerClient := &Client{uid: 7, accountType: types.AccountHuman, deviceOwnerUID: 7, deviceID: "owner-cloud", deviceBodyID: "body-owner-cloud", deviceInstallationID: "install-owner-cloud", send: make(chan []byte, 1)}
	bobClient := &Client{uid: 9, accountType: types.AccountHuman, deviceOwnerUID: 9, deviceID: "bob-laptop", deviceBodyID: "body-bob", deviceInstallationID: "install-bob", send: make(chan []byte, 1)}
	botClient := &Client{uid: 42, accountType: types.AccountBot, bodyID: "body-agent", send: make(chan []byte, 1)}
	hub.addClient(ownerClient)
	hub.addClient(bobClient)
	hub.addClient(botClient)
	hub.bindDeviceClient(7, ownerDevice, ownerClient)
	hub.bindDeviceClient(9, bobDevice, bobClient)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_9_42",
		Content: json.RawMessage(`"在我的电脑上创建一个文件"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(9, "p2p_9_42", 0, payload, 203, nil)

	var msg ServerMessage
	decodeQueuedServerMessage(t, botClient.send, &msg)
	identity := metadataMapFromServerMessage(t, &msg, "catsco_identity")
	permissions, ok := identity["permissions"].(map[string]interface{})
	if !ok || permissions["device_owner_user_id"] != "usr9" || permissions["device_owner_source"] != "actor" {
		t.Fatalf("direct friend chat should keep actor device permissions: %#v", identity["permissions"])
	}
	grant := firstDeviceGrantMap(t, identity)
	if grant["ownerUserId"] != "usr9" || grant["actorUserId"] != "usr9" || grant["agentId"] != "usr42" {
		t.Fatalf("direct friend chat grant should target actor identity: %#v", grant)
	}
	if grant["deviceId"] != "bob-laptop" || grant["deviceId"] == "owner-cloud" {
		t.Fatalf("direct friend chat must target Bob's device, not owner cloud: %#v", grant)
	}
	selection := deviceSelectionMap(t, identity)
	if selection["ownerUserId"] != "usr9" || selection["actorUserId"] != "usr9" || selection["status"] != string(DeviceSelectionSelected) {
		t.Fatalf("unexpected direct friend selection: %#v", selection)
	}
	selectedDevice, ok := selection["selectedDevice"].(map[string]interface{})
	if !ok || selectedDevice["deviceId"] != "bob-laptop" {
		t.Fatalf("direct friend selection should choose Bob's device: %#v", selection["selectedDevice"])
	}
}

func TestActorLooksLikeChannelIdentity(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[1] = &types.User{ID: 1, Username: "ch_weixin_openid", DisplayName: "Weixin Actor", AccountType: types.AccountHuman}
	db.users[2] = &types.User{ID: 2, Username: "ch_feishu_openid", DisplayName: "Feishu Actor", AccountType: types.AccountHuman}
	db.users[3] = &types.User{ID: 3, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman}
	hub := NewHub(db, nil)

	if !hub.actorLooksLikeChannelIdentity(1) {
		t.Fatalf("weixin shadow actor should be treated as channel identity")
	}
	if !hub.actorLooksLikeChannelIdentity(2) {
		t.Fatalf("feishu shadow actor should be treated as channel identity")
	}
	if hub.actorLooksLikeChannelIdentity(3) {
		t.Fatalf("regular CatsCo user should not be treated as channel identity")
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
	if _, ok := msg.Data.Metadata["xiaoba_runtime"]; ok {
		t.Fatalf("history message should not receive xiaoba runtime facts: %#v", msg.Data.Metadata["xiaoba_runtime"])
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

func hasGrantOperation(operations []DeviceGrantOperation, expected DeviceGrantOperation) bool {
	for _, operation := range operations {
		if operation == expected {
			return true
		}
	}
	return false
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
