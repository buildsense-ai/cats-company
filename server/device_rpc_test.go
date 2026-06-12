package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestDeviceRPCRoutesRequestToSelectedDeviceAndReturnsResult(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-1",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
		Payload:   map[string]interface{}{"path": "catsco://opaque"},
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil {
		t.Fatalf("target received %#v, want device_rpc request", forwarded)
	}
	if forwarded.DeviceRPC.Type != "request" || forwarded.DeviceRPC.RequestID != "rpc-1" {
		t.Fatalf("unexpected request envelope: %#v", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.GrantID != grant.GrantID || forwarded.DeviceRPC.ActorUserID != "usr7" || forwarded.DeviceRPC.AgentBodyID != "body-agent" {
		t.Fatalf("request was not canonicalized from grant: %#v", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.OwnerUserID != "usr7" || forwarded.DeviceRPC.IdentitySource != userDeviceGrantIdentitySrc {
		t.Fatalf("request missing owner identity: %#v", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.DeviceBodyID != "body-device" || forwarded.DeviceRPC.DeviceInstallationID != "install-device" {
		t.Fatalf("request missing target device identity: %#v", forwarded.DeviceRPC)
	}

	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("unexpected request ack: %#v", ack.Ctrl)
	}
	paramsJSON, _ := json.Marshal(ack.Ctrl.Params)
	if !containsJSONText(paramsJSON, "expires_at") || !containsJSONText(paramsJSON, "read_file") {
		t.Fatalf("request ack missing routing metadata: %s", paramsJSON)
	}
	pending := hub.DeviceRPCStatus(7)
	if len(pending) != 1 || pending[0].RequestID != "rpc-1" || pending[0].DeviceID != "alice-laptop" {
		t.Fatalf("unexpected pending status: %#v", pending)
	}
	if !pending[0].RequesterConnected || !pending[0].TargetConnected {
		t.Fatalf("pending status should include live route flags: %#v", pending[0])
	}

	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "rpc-result-1",
		Type:      "result",
		RequestID: "rpc-1",
		Result:    map[string]interface{}{"ok": true},
	})

	var targetAck ServerMessage
	decodeQueuedServerMessage(t, target.send, &targetAck)
	if targetAck.Ctrl == nil || targetAck.Ctrl.Code != 200 {
		t.Fatalf("unexpected result ack: %#v", targetAck.Ctrl)
	}

	var result ServerMessage
	decodeQueuedServerMessage(t, agent.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.Type != "result" || result.DeviceRPC.RequestID != "rpc-1" {
		t.Fatalf("agent received %#v, want device_rpc result", result)
	}
	if result.DeviceRPC.DeviceID != "alice-laptop" || result.DeviceRPC.GrantID != grant.GrantID {
		t.Fatalf("result missing canonical scope: %#v", result.DeviceRPC)
	}
	resultMap, ok := result.DeviceRPC.Result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		t.Fatalf("unexpected result payload: %#v", result.DeviceRPC.Result)
	}
	if pending := hub.DeviceRPCStatus(7); len(pending) != 0 {
		t.Fatalf("finished rpc should not remain pending: %#v", pending)
	}
}

func TestDeviceRPCRoutesDelegatedChannelActorGrantToDeviceOwner(t *testing.T) {
	hub := NewHub(nil, nil)
	now := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	hub.userDevices.now = func() time.Time { return now }
	hub.deviceRPC.now = func() time.Time { return now }
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
	grants := hub.userDevices.grantsForOwnerDevices(100, 7, "p2p_100_42", "p2p", 42, "body-agent", []UserDevice{device})
	if len(grants) != 1 {
		t.Fatalf("grantsForOwnerDevices returned %d grants", len(grants))
	}
	grant := grants[0]
	if grant.OwnerUserID != "usr7" || grant.ActorUserID != "usr100" {
		t.Fatalf("unexpected delegated grant: %#v", grant)
	}
	agent := &Client{
		hub:         hub,
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 4),
	}
	target := &Client{
		hub:                  hub,
		uid:                  77,
		accountType:          types.AccountBot,
		bodyID:               "body-device",
		installationID:       "install-device",
		deviceOwnerUID:       7,
		deviceID:             "alice-laptop",
		deviceBodyID:         "body-device",
		deviceInstallationID: "install-device",
		send:                 make(chan []byte, 4),
	}
	hub.addClient(agent)
	hub.addClient(target)
	hub.bindDeviceClient(7, device, target)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-delegated",
		Type:      "request",
		RequestID: "rpc-delegated",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.ActorUserID != "usr100" || forwarded.DeviceRPC.DeviceID != "alice-laptop" {
		t.Fatalf("unexpected delegated request: %#v", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.OwnerUserID != "usr7" || forwarded.DeviceRPC.IdentitySource != channelDeviceGrantIdentitySrc {
		t.Fatalf("delegated request missing owner identity: %#v", forwarded.DeviceRPC)
	}
	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected delegated ack: %#v", ack.Ctrl)
	}
	if pending := hub.DeviceRPCStatus(100); len(pending) != 0 {
		t.Fatalf("channel actor should not own device pending status: %#v", pending)
	}
	pending := hub.DeviceRPCStatus(7)
	if len(pending) != 1 || pending[0].ActorUserID != "usr100" || pending[0].OwnerUserID != "usr7" {
		t.Fatalf("unexpected owner-scoped pending: %#v", pending)
	}
}

func TestDeviceRPCRoutesWriteFileOperationToSelectedDevice(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-write",
		Type:      "request",
		RequestID: "rpc-write",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "write_file",
		ToolName:  "write_file",
		Payload:   map[string]interface{}{"args": map[string]interface{}{"file_path": "draft.txt", "content": "hello"}},
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.Operation != "write_file" || forwarded.DeviceRPC.ToolName != "write_file" {
		t.Fatalf("target received %#v, want write_file device_rpc request", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.OwnerUserID != "usr7" || forwarded.DeviceRPC.IdentitySource != userDeviceGrantIdentitySrc {
		t.Fatalf("write request missing owner identity: %#v", forwarded.DeviceRPC)
	}
	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected write request ack: %#v", ack.Ctrl)
	}
}

func TestDeviceRPCDoesNotBroadcastToSiblingConnections(t *testing.T) {
	hub, agent, target, sibling, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-1",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})

	var targetMsg ServerMessage
	decodeQueuedServerMessage(t, target.send, &targetMsg)
	if targetMsg.DeviceRPC == nil {
		t.Fatalf("target received %#v, want device_rpc", targetMsg)
	}
	if drainOne(sibling.send) {
		t.Fatal("sibling connection should not receive selected-device RPC")
	}
}

func TestDeviceRPCRejectsResultFromWrongDeviceConnection(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)
	wrong := &Client{
		uid:         88,
		accountType: types.AccountBot,
		bodyID:      "body-other",
		send:        make(chan []byte, 2),
	}
	hub.addClient(wrong)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-1",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	hub.handleDeviceRPC(wrong, &MsgDeviceRPC{
		ID:        "wrong-result",
		Type:      "result",
		RequestID: "rpc-1",
		Result:    map[string]interface{}{"ok": true},
	})

	var wrongAck ServerMessage
	decodeQueuedServerMessage(t, wrong.send, &wrongAck)
	if wrongAck.Ctrl == nil || wrongAck.Ctrl.Code != 403 {
		t.Fatalf("wrong client ack = %#v, want 403", wrongAck.Ctrl)
	}

	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "right-result",
		Type:      "result",
		RequestID: "rpc-1",
		Result:    map[string]interface{}{"ok": true},
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	var result ServerMessage
	decodeQueuedServerMessage(t, agent.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.RequestID != "rpc-1" {
		t.Fatalf("expected original pending request to remain for target result, got %#v", result)
	}
}

func TestDeviceRPCRejectsOfflineDevice(t *testing.T) {
	hub, agent, _, _, grant := newDeviceRPCTestFixture(t, false)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-offline",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 404 {
		t.Fatalf("offline device ack = %#v, want 404", ack.Ctrl)
	}
}

func TestDeviceRPCRejectsShellOperationEvenWhenGranted(t *testing.T) {
	hub, agent, _, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-shell",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "execute_shell",
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 403 || !strings.Contains(ack.Ctrl.Text, "not supported") {
		t.Fatalf("unsupported rpc operation ack = %#v, want 403 unsupported", ack.Ctrl)
	}
}

func TestDeviceRPCTimeoutNotifiesRequesterAndClearsPending(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-timeout",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	expiredAt := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC).Add(defaultDeviceRPCTTL + time.Millisecond)
	hub.deviceRPC.now = func() time.Time { return expiredAt }
	if status := hub.DeviceRPCStatus(7); len(status) != 1 || status[0].RequestID != "rpc-timeout" {
		t.Fatalf("status query must not silently delete expired pending rpc: %#v", status)
	}
	if got := hub.expireDeviceRPCRequests(expiredAt); got != 1 {
		t.Fatalf("expired requests = %d, want 1", got)
	}
	var result ServerMessage
	decodeQueuedServerMessage(t, agent.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.Error == nil || result.DeviceRPC.Error.Code != "device_rpc_timeout" {
		t.Fatalf("agent timeout result = %#v, want device_rpc_timeout", result.DeviceRPC)
	}
	if result.DeviceRPC.OwnerUserID != grant.OwnerUserID || result.DeviceRPC.IdentitySource != grant.IdentitySource {
		t.Fatalf("timeout result missing owner identity: %#v", result.DeviceRPC)
	}
	if pending := hub.DeviceRPCStatus(7); len(pending) != 0 {
		t.Fatalf("timeout should clear pending status: %#v", pending)
	}
}

func TestDeviceRPCDoesNotRouteByBareBodyOrInstallationID(t *testing.T) {
	hub, agent, _, _, grant := newDeviceRPCTestFixture(t, false)
	collision := &Client{
		hub:            hub,
		uid:            99,
		accountType:    types.AccountBot,
		bodyID:         "body-device",
		installationID: "install-device",
		send:           make(chan []byte, 2),
	}
	hub.addClient(collision)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-collision",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 404 {
		t.Fatalf("bare body collision ack = %#v, want 404", ack.Ctrl)
	}
	if drainOne(collision.send) {
		t.Fatal("bare body/install collision should not receive selected-device RPC")
	}
}

func TestDeviceRPCRejectsResultFromSupersededDeviceConnection(t *testing.T) {
	hub, agent, oldTarget, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-rebind",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	decodeQueuedServerMessage(t, oldTarget.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	device, ok := hub.userDevices.activeDevice(7, grant.DeviceID)
	if !ok {
		t.Fatal("expected active device")
	}
	newTarget := &Client{
		hub:         hub,
		uid:         78,
		accountType: types.AccountBot,
		bodyID:      "body-device-new",
		send:        make(chan []byte, 2),
	}
	hub.addClient(newTarget)
	hub.bindDeviceClient(7, device, newTarget)

	hub.handleDeviceRPC(oldTarget, &MsgDeviceRPC{
		ID:        "stale-result",
		Type:      "result",
		RequestID: "rpc-rebind",
		Result:    map[string]interface{}{"ok": true},
	})

	var staleAck ServerMessage
	decodeQueuedServerMessage(t, oldTarget.send, &staleAck)
	if staleAck.Ctrl == nil || staleAck.Ctrl.Code != 403 {
		t.Fatalf("stale device ack = %#v, want 403", staleAck.Ctrl)
	}
}

func TestDeviceRPCRejectsConnectorResultWithoutScope(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)
	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-no-result-scope",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	target.deviceConnector = &DeviceConnectorClaims{
		UID:      7,
		DeviceID: grant.DeviceID,
		Scopes:   []string{"device:ws", "device:register"},
	}
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "rpc-result-msg-1",
		Type:      "result",
		RequestID: "rpc-no-result-scope",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Result:    map[string]interface{}{"ok": true},
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, target.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusForbidden {
		t.Fatalf("connector result ack = %#v, want 403", ack.Ctrl)
	}
	if pending := hub.DeviceRPCStatus(7); len(pending) != 1 || pending[0].RequestID != "rpc-no-result-scope" {
		t.Fatalf("request should remain pending after forbidden result: %#v", pending)
	}
}

func TestDeviceRPCRejectsRevokedConnectorResult(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)
	target.uid = 7
	target.accountType = types.AccountHuman
	target.deviceConnector = &DeviceConnectorClaims{
		UID:            7,
		DeviceID:       grant.DeviceID,
		InstallationID: target.deviceInstallationID,
		Scopes:         []string{"device:ws", "device:register", "device:rpc_result"},
	}
	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-revoked-connector",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	hub.revokeDeviceConnectorDevice(7, grant.DeviceID)
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "rpc-result-msg-1",
		Type:      "result",
		RequestID: "rpc-revoked-connector",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Result:    map[string]interface{}{"ok": true},
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, target.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusForbidden {
		t.Fatalf("revoked connector result ack = %#v, want 403", ack.Ctrl)
	}
	if pending := hub.DeviceRPCStatus(7); len(pending) != 1 || pending[0].RequestID != "rpc-revoked-connector" {
		t.Fatalf("request should remain pending after revoked result: %#v", pending)
	}
}

func TestHiRejectsRevokedConnectorRegistration(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{
		hub:         hub,
		uid:         7,
		accountType: types.AccountHuman,
		send:        make(chan []byte, 1),
		deviceConnector: &DeviceConnectorClaims{
			UID:            7,
			DeviceID:       "alice-laptop",
			InstallationID: "install-device",
			Scopes:         []string{"device:ws", "device:register", "device:rpc_result"},
		},
	}
	hub.addClient(client)
	hub.revokeDeviceConnectorDevice(7, "alice-laptop")

	hub.handleHi(client, "Alice Laptop", &MsgClientHi{
		ID: "hi-revoked",
		Device: &MsgClientHiDevice{
			DeviceID:       "alice-laptop",
			InstallationID: "install-device",
			Capabilities:   []string{"read_file"},
		},
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, client.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusBadRequest {
		t.Fatalf("revoked connector hi ack = %#v, want 400", ack.Ctrl)
	}
	if got := hub.getDeviceClient(7, "alice-laptop"); got != nil {
		t.Fatalf("revoked connector should not bind device client: %#v", got)
	}
}

func TestSharedRuntimeRoutesDeviceRPCAcrossHubs(t *testing.T) {
	shared := newSharedMemoryRuntimeState()
	hubAgent := NewHubWithRuntime(nil, nil, shared, "node-agent")
	hubDevice := NewHubWithRuntime(nil, nil, shared, "node-device")
	now := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	hubAgent.userDevices.now = func() time.Time { return now }
	hubAgent.deviceRPC.now = func() time.Time { return now }
	hubDevice.userDevices.now = func() time.Time { return now }
	hubDevice.deviceRPC.now = func() time.Time { return now }

	device, err := hubDevice.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"read_file", "grep"},
	})
	if err != nil {
		t.Fatalf("register device on node-device: %v", err)
	}
	grants := hubAgent.userDevices.grantsForDevices(7, "p2p_7_42", "p2p", 42, "body-agent", []UserDevice{device})
	if len(grants) != 1 {
		t.Fatalf("shared grantsForDevices returned %d grants", len(grants))
	}

	agent := &Client{
		hub:         hubAgent,
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 4),
	}
	target := &Client{
		hub:         hubDevice,
		uid:         77,
		accountType: types.AccountHuman,
		bodyID:      "body-device",
		send:        make(chan []byte, 4),
	}
	hubAgent.addClient(agent)
	hubDevice.addClient(target)
	hubDevice.bindDeviceClient(7, device, target)

	hubAgent.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-cross-node",
		GrantID:   grants[0].GrantID,
		DeviceID:  device.DeviceID,
		Operation: "read_file",
		ToolName:  "read_file",
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.RequestID != "rpc-cross-node" {
		t.Fatalf("target on node-device received %#v, want cross-node rpc request", forwarded)
	}
	if forwarded.DeviceRPC.OwnerUserID != grants[0].OwnerUserID || forwarded.DeviceRPC.IdentitySource != grants[0].IdentitySource {
		t.Fatalf("cross-node request missing owner identity: %#v", forwarded.DeviceRPC)
	}
	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("agent request ack = %#v, want 200", ack.Ctrl)
	}
	if pending := hubDevice.DeviceRPCStatus(7, "usr42"); len(pending) != 1 || !pending[0].RequesterConnected || !pending[0].TargetConnected {
		t.Fatalf("shared pending status from node-device = %#v, want connected request/target", pending)
	}

	hubDevice.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "rpc-result-1",
		Type:      "result",
		RequestID: "rpc-cross-node",
		Result:    map[string]interface{}{"ok": true},
	})

	var targetAck ServerMessage
	decodeQueuedServerMessage(t, target.send, &targetAck)
	if targetAck.Ctrl == nil || targetAck.Ctrl.Code != 200 {
		t.Fatalf("target result ack = %#v, want 200", targetAck.Ctrl)
	}
	var result ServerMessage
	decodeQueuedServerMessage(t, agent.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.Type != "result" || result.DeviceRPC.RequestID != "rpc-cross-node" {
		t.Fatalf("agent on node-agent received %#v, want cross-node rpc result", result)
	}
	if result.DeviceRPC.OwnerUserID != grants[0].OwnerUserID || result.DeviceRPC.IdentitySource != grants[0].IdentitySource {
		t.Fatalf("cross-node result missing owner identity: %#v", result.DeviceRPC)
	}
	if pending := hubAgent.DeviceRPCStatus(7, "usr42"); len(pending) != 0 {
		t.Fatalf("shared pending should be cleared from node-agent: %#v", pending)
	}
}

func TestBoundDeviceTouchKeepsLiveConnectionActive(t *testing.T) {
	hub, _, target, _, grant := newDeviceRPCTestFixture(t, true)
	now := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC).Add(defaultUserDeviceTTL + time.Minute)
	hub.userDevices.now = func() time.Time { return now }

	if _, ok := hub.userDevices.activeDevice(7, grant.DeviceID); ok {
		t.Fatal("expected device to expire before live websocket touch")
	}
	target.touchBoundDevice()
	if _, ok := hub.userDevices.activeDevice(7, grant.DeviceID); !ok {
		t.Fatal("expected live websocket touch to refresh bound device")
	}
}

func TestHiCanBindLiveDeviceConnection(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{
		hub:         hub,
		uid:         7,
		accountType: types.AccountHuman,
		remoteAddr:  "test",
		displayName: "Alice",
		send:        make(chan []byte, 1),
	}
	hub.addClient(client)

	hub.handleHi(client, "Alice", &MsgClientHi{
		ID: "hi-1",
		Device: &MsgClientHiDevice{
			DeviceID:       "alice-laptop",
			DisplayName:    "Alice Laptop",
			BodyID:         "body-device",
			InstallationID: "install-device",
			Capabilities:   []string{"read_file"},
		},
	})

	if got := hub.getDeviceClient(7, "alice-laptop"); got != client {
		t.Fatalf("bound device client = %#v, want current client", got)
	}
	var ack ServerMessage
	decodeQueuedServerMessage(t, client.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("unexpected hi ack: %#v", ack.Ctrl)
	}
	paramsJSON, _ := json.Marshal(ack.Ctrl.Params)
	if !json.Valid(paramsJSON) || !containsJSONText(paramsJSON, "device_rpc") {
		t.Fatalf("hi ack params missing device_rpc feature: %s", paramsJSON)
	}
}

func newDeviceRPCTestFixture(t *testing.T, bindTarget bool) (*Hub, *Client, *Client, *Client, ScopedDeviceGrant) {
	t.Helper()
	hub := NewHub(nil, nil)
	now := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	hub.userDevices.now = func() time.Time { return now }
	hub.deviceRPC.now = func() time.Time { return now }

	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"read_file", "write_file", "send_file"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	grants := hub.userDevices.grantsForDevices(7, "p2p_7_42", "p2p", 42, "body-agent", []UserDevice{device})
	if len(grants) != 1 {
		t.Fatalf("grantsForDevices returned %d grants", len(grants))
	}

	agent := &Client{
		hub:         hub,
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 4),
	}
	target := &Client{
		hub:                  hub,
		uid:                  77,
		accountType:          types.AccountBot,
		bodyID:               "body-device",
		installationID:       "install-device",
		deviceOwnerUID:       7,
		deviceID:             "alice-laptop",
		deviceBodyID:         "body-device",
		deviceInstallationID: "install-device",
		send:                 make(chan []byte, 4),
	}
	sibling := &Client{
		hub:         hub,
		uid:         77,
		accountType: types.AccountBot,
		bodyID:      "body-sibling",
		send:        make(chan []byte, 4),
	}
	hub.addClient(agent)
	if bindTarget {
		hub.addClient(target)
		hub.bindDeviceClient(7, device, target)
	}
	hub.addClient(sibling)
	return hub, agent, target, sibling, grants[0]
}

func containsJSONText(raw []byte, text string) bool {
	return strings.Contains(string(raw), text)
}
