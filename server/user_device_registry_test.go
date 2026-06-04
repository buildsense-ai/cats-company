package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		Capabilities:   []string{"read_file", "unknown", "send_file", "read_file"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	if device.DeviceID != "laptop-main" || device.DisplayName != "Alice Laptop" {
		t.Fatalf("unexpected registered device: %#v", device)
	}
	if got := device.Capabilities; len(got) != 2 || got[0] != DeviceGrantReadFile || got[1] != DeviceGrantSendFile {
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
	if grant.CreatedAt != unixMillis(now) || grant.ExpiresAt != unixMillis(now.Add(2*time.Minute)) {
		t.Fatalf("unexpected grant times: %#v", grant)
	}

	registry.now = func() time.Time { return now.Add(2*time.Minute + time.Second) }
	if grants := registry.grantsForTurn(7, "p2p_7_42", "p2p", 42, "body-agent"); len(grants) != 0 {
		t.Fatalf("expired device still issued grants: %#v", grants)
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
	if _, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"read_file", "send_file"},
	}); err != nil {
		t.Fatalf("register device: %v", err)
	}
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

	humanMsg := hub.messageForRecipient(7, 50, "p2p_7_50", 0, payload, 100)
	humanIdentity := metadataMapFromServerMessage(t, humanMsg, "catsco_identity")
	if _, ok := humanIdentity["device_grants"]; ok {
		t.Fatalf("human recipient should not receive device grants: %#v", humanIdentity["device_grants"])
	}
}

func TestHistoryMessagesReissueDeviceGrantsForBotRecipient(t *testing.T) {
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
	grant := firstDeviceGrantMap(t, identity)
	if grant["topicId"] != "p2p_7_42" || grant["actorUserId"] != "usr7" || grant["agentBodyId"] != "body-agent" {
		t.Fatalf("unexpected history grant: %#v", grant)
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
