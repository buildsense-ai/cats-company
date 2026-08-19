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
		ID:        "rpc-progress-1",
		Type:      "progress",
		RequestID: "rpc-1",
		Progress: &MsgDeviceRPCProgress{
			Processed: 37,
			Total:     int64Ptr(100),
			Provider:  "codex",
			Phase:     "importing",
		},
	})

	var progressAck ServerMessage
	decodeQueuedServerMessage(t, target.send, &progressAck)
	if progressAck.Ctrl == nil || progressAck.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected progress ack: %#v", progressAck.Ctrl)
	}
	var progress ServerMessage
	decodeQueuedServerMessage(t, agent.send, &progress)
	if progress.DeviceRPC == nil || progress.DeviceRPC.Type != "progress" || progress.DeviceRPC.RequestID != "rpc-1" {
		t.Fatalf("agent received %#v, want device_rpc progress", progress.DeviceRPC)
	}
	if progress.DeviceRPC.Progress == nil || progress.DeviceRPC.Progress.Processed != 37 || progress.DeviceRPC.Progress.Total == nil || *progress.DeviceRPC.Progress.Total != 100 {
		t.Fatalf("unexpected progress payload: %#v", progress.DeviceRPC.Progress)
	}
	if pending := hub.DeviceRPCStatus(7); len(pending) != 1 || pending[0].RequestID != "rpc-1" {
		t.Fatalf("progress must preserve pending request: %#v", pending)
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

func TestDeviceRPCResultUsesReconnectedRequesterRoute(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-reconnect",
		Type:      deviceRPCTypeRequest,
		RequestID: "rpc-reconnect",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	var forwarded, requestAck ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	decodeQueuedServerMessage(t, agent.send, &requestAck)

	if removed, _, _, _ := hub.removeClient(agent); !removed {
		t.Fatal("requester connection was not removed")
	}
	reconnected := &Client{
		hub:         hub,
		uid:         agent.uid,
		accountType: agent.accountType,
		bodyID:      agent.bodyID,
		send:        make(chan []byte, 4),
	}
	hub.addClient(reconnected)

	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "rpc-result-reconnect",
		Type:      deviceRPCTypeResult,
		RequestID: "rpc-reconnect",
		Result:    map[string]interface{}{"ok": true},
	})

	var targetAck, result ServerMessage
	decodeQueuedServerMessage(t, target.send, &targetAck)
	if targetAck.Ctrl == nil || targetAck.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected result ack: %#v", targetAck.Ctrl)
	}
	decodeQueuedServerMessage(t, reconnected.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.Type != deviceRPCTypeResult || result.DeviceRPC.RequestID != "rpc-reconnect" {
		t.Fatalf("reconnected requester received %#v, want device_rpc result", result.DeviceRPC)
	}
	select {
	case stale := <-agent.send:
		t.Fatalf("stale requester received result: %s", stale)
	default:
	}
}

func TestDeviceRPCTerminalClaimAndTimeoutAreExclusive(t *testing.T) {
	now := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	router := newDeviceRPCRouter(time.Minute)
	router.now = func() time.Time { return now }
	pending := deviceRPCPending{
		requestID: "rpc-terminal-result",
		agentUID:  42,
		ownerUID:  7,
		deviceID:  "alice-laptop",
		createdAt: now,
		expiresAt: now.Add(time.Minute),
	}
	if ok, reason := router.add(pending); !ok {
		t.Fatalf("add pending: %s", reason)
	}
	if _, ok := router.claim(pending.requestID); !ok {
		t.Fatal("result did not claim live request")
	}
	if expired := router.expire(pending.expiresAt); len(expired) != 0 {
		t.Fatalf("claimed result also expired: %#v", expired)
	}

	pending.requestID = "rpc-terminal-timeout"
	pending.expiresAt = now.Add(time.Minute)
	if ok, reason := router.add(pending); !ok {
		t.Fatalf("add timeout pending: %s", reason)
	}
	if _, ok := router.get(pending.requestID); !ok {
		t.Fatal("pending request missing before timeout")
	}
	if expired := router.expire(pending.expiresAt); len(expired) != 1 || expired[0].requestID != pending.requestID {
		t.Fatalf("timeout did not claim request: %#v", expired)
	}
	if _, ok := router.claim(pending.requestID); ok {
		t.Fatal("stale result claimed request after timeout")
	}
}

func TestUserExternalHistoryRPCRoutesOnlyToOwnedCapableDevice(t *testing.T) {
	hub := NewHub(nil, nil)
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	hub.userDevices.now = func() time.Time { return now }
	hub.deviceRPC.now = func() time.Time { return now }
	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"external_history"},
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	user := &Client{hub: hub, uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	target := &Client{
		hub:                  hub,
		uid:                  77,
		accountType:          types.AccountBot,
		deviceOwnerUID:       7,
		deviceID:             device.DeviceID,
		deviceBodyID:         device.BodyID,
		deviceInstallationID: device.InstallationID,
		send:                 make(chan []byte, 4),
	}
	hub.addClient(user)
	hub.addClient(target)
	hub.bindDeviceClient(7, device, target)

	hub.handleDeviceRPC(user, &MsgDeviceRPC{
		ID:        "web-1",
		Type:      "request",
		RequestID: "external-1",
		DeviceID:  device.DeviceID,
		Operation: "external_history",
		Payload:   map[string]interface{}{"action": "preview", "provider": "codex", "updatedSince": "7d"},
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.Operation != "external_history" {
		t.Fatalf("target received %#v, want external_history request", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.ActorUserID != "usr7" || forwarded.DeviceRPC.OwnerUserID != "usr7" {
		t.Fatalf("request identity was not owner scoped: %#v", forwarded.DeviceRPC)
	}
	var ack ServerMessage
	decodeQueuedServerMessage(t, user.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusOK {
		t.Fatalf("unexpected request ack: %#v", ack.Ctrl)
	}

	now = now.Add(50 * time.Second)
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "progress-1",
		Type:      "progress",
		RequestID: "external-1",
		Progress: &MsgDeviceRPCProgress{
			Processed: 37,
			Total:     int64Ptr(100),
			Provider:  "codex",
			Phase:     "importing",
		},
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	var progress ServerMessage
	decodeQueuedServerMessage(t, user.send, &progress)
	if progress.DeviceRPC == nil || progress.DeviceRPC.Type != "progress" || progress.DeviceRPC.Progress.Processed != 37 {
		t.Fatalf("user received %#v, want exact external history progress", progress.DeviceRPC)
	}

	now = now.Add(50 * time.Second)

	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "result-1",
		Type:      "result",
		RequestID: "external-1",
		Result:    map[string]interface{}{"ok": true, "content": `{"selectedCount":2}`},
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	var result ServerMessage
	decodeQueuedServerMessage(t, user.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.RequestID != "external-1" {
		t.Fatalf("user received %#v, want external history result", result.DeviceRPC)
	}
}

func TestDeviceRPCProgressPreservesNullableTotalAndValidatesZeroNegative(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-null-total",
		Type:      "request",
		RequestID: "rpc-null-total",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "read_file",
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	decodeQueuedServerMessage(t, agent.send, &ServerMessage{})

	// total=nil must serialize as explicit JSON null (not omitted) for discovering.
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "progress-null",
		Type:      "progress",
		RequestID: "rpc-null-total",
		Progress:  &MsgDeviceRPCProgress{Processed: 0, Total: nil, Phase: "discovering"},
	})
	decodeQueuedServerMessage(t, target.send, &ServerMessage{})
	var progressNull ServerMessage
	decodeQueuedServerMessage(t, agent.send, &progressNull)
	if progressNull.DeviceRPC == nil || progressNull.DeviceRPC.Progress == nil {
		t.Fatalf("expected forwarded progress: %#v", progressNull.DeviceRPC)
	}
	if progressNull.DeviceRPC.Progress.Total != nil {
		t.Fatalf("total must stay nil for discovering, got %v", *progressNull.DeviceRPC.Progress.Total)
	}
	raw, _ := json.Marshal(progressNull.DeviceRPC.Progress)
	if !strings.Contains(string(raw), `"total":null`) {
		t.Fatalf("forwarded progress must preserve explicit total:null, got %s", raw)
	}

	// total=0 with processed=0 is valid (stable empty catalog).
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "progress-zero",
		Type:      "progress",
		RequestID: "rpc-null-total",
		Progress:  &MsgDeviceRPCProgress{Processed: 0, Total: int64Ptr(0), Phase: "importing"},
	})
	var ackZero ServerMessage
	decodeQueuedServerMessage(t, target.send, &ackZero)
	if ackZero.Ctrl == nil || ackZero.Ctrl.Code != http.StatusOK {
		t.Fatalf("total=0 processed=0 must be accepted: %#v", ackZero.Ctrl)
	}

	// total=0 with processed>0 is rejected.
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "progress-zero-bad",
		Type:      "progress",
		RequestID: "rpc-null-total",
		Progress:  &MsgDeviceRPCProgress{Processed: 1, Total: int64Ptr(0), Phase: "importing"},
	})
	var ackZeroBad ServerMessage
	decodeQueuedServerMessage(t, target.send, &ackZeroBad)
	if ackZeroBad.Ctrl == nil || ackZeroBad.Ctrl.Code != http.StatusBadRequest {
		t.Fatalf("processed>0 with total=0 must be rejected: %#v", ackZeroBad.Ctrl)
	}

	// negative total is rejected.
	hub.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "progress-neg",
		Type:      "progress",
		RequestID: "rpc-null-total",
		Progress:  &MsgDeviceRPCProgress{Processed: 0, Total: int64Ptr(-1), Phase: "importing"},
	})
	var ackNeg ServerMessage
	decodeQueuedServerMessage(t, target.send, &ackNeg)
	if ackNeg.Ctrl == nil || ackNeg.Ctrl.Code != http.StatusBadRequest {
		t.Fatalf("negative total must be rejected: %#v", ackNeg.Ctrl)
	}
}

func TestUserExternalHistoryRPCRejectsDeviceWithoutCapability(t *testing.T) {
	hub, _, target, _, _ := newDeviceRPCTestFixture(t, true)
	user := &Client{hub: hub, uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	hub.addClient(user)

	hub.handleDeviceRPC(user, &MsgDeviceRPC{
		ID:        "web-denied",
		Type:      "request",
		RequestID: "external-denied",
		DeviceID:  "alice-laptop",
		Operation: "external_history",
		Payload:   map[string]interface{}{"action": "status"},
	})

	var response ServerMessage
	decodeQueuedServerMessage(t, user.send, &response)
	if response.Ctrl == nil || response.Ctrl.Code != http.StatusForbidden {
		t.Fatalf("unexpected response: %#v", response.Ctrl)
	}
	if drainOne(target.send) {
		t.Fatal("incapable device must not receive external history RPC")
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

func TestDeviceRPCRejectsShellOperationWithoutGrant(t *testing.T) {
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
	if ack.Ctrl == nil || ack.Ctrl.Code != 403 || !strings.Contains(ack.Ctrl.Text, "not allowed by grant") {
		t.Fatalf("shell without grant ack = %#v, want 403 not allowed by grant", ack.Ctrl)
	}
}

func TestDeviceRPCRoutesShellOperationWhenGrantedAndAuditsCommand(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)
	grant.Operations = append(grant.Operations, DeviceGrantExecuteShell)
	hub.userDevices.rememberGrants([]ScopedDeviceGrant{grant})

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-shell",
		Type:      "request",
		RequestID: "rpc-shell",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: "execute_shell",
		ToolName:  "execute_shell",
		Payload: map[string]interface{}{
			"args": map[string]interface{}{"command": "echo remote-shell"},
		},
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.Operation != "execute_shell" || forwarded.DeviceRPC.ToolName != "execute_shell" {
		t.Fatalf("target received %#v, want execute_shell device_rpc request", forwarded.DeviceRPC)
	}
	if forwarded.DeviceRPC.GrantID != grant.GrantID || forwarded.DeviceRPC.DeviceID != grant.DeviceID || forwarded.DeviceRPC.SessionKey != grant.SessionKey {
		t.Fatalf("shell request missing canonical grant scope: %#v", forwarded.DeviceRPC)
	}

	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusOK {
		t.Fatalf("shell rpc ack = %#v, want 200", ack.Ctrl)
	}

	events := hub.listDeviceAudit(7, 10)
	if len(events) == 0 {
		t.Fatal("execute_shell rpc should create a device audit event")
	}
	event := events[0]
	if event.Phase != "rpc_forwarded" || event.Operation != "execute_shell" || event.Command != "echo remote-shell" {
		t.Fatalf("unexpected shell audit event: %#v", event)
	}
	if event.ActorUserID != grant.ActorUserID || event.AgentID != grant.AgentID || event.DeviceID != grant.DeviceID || event.SessionKey != grant.SessionKey {
		t.Fatalf("shell audit event missing scope: %#v", event)
	}
}

func TestDeviceRPCRoutesCommonDirectoryResolutionToSelectedDevice(t *testing.T) {
	hub, agent, target, _, grant := newDeviceRPCTestFixture(t, true)

	hub.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-resolve-dir",
		GrantID:   grant.GrantID,
		DeviceID:  grant.DeviceID,
		Operation: string(DeviceGrantResolveDir),
		Payload:   map[string]interface{}{"args": map[string]interface{}{"directory": "desktop"}},
	})

	var forwarded ServerMessage
	decodeQueuedServerMessage(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.Operation != string(DeviceGrantResolveDir) {
		t.Fatalf("target received %#v, want resolve_common_directory device_rpc request", forwarded)
	}
	if forwarded.DeviceRPC.GrantID != grant.GrantID || forwarded.DeviceRPC.DeviceID != grant.DeviceID {
		t.Fatalf("resolve directory request missing canonical scope: %#v", forwarded.DeviceRPC)
	}

	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("resolve directory rpc ack = %#v, want 200", ack.Ctrl)
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
			RuntimeRole:    "desktop",
			Capabilities:   []string{"read_file"},
		},
	})

	if got := hub.getDeviceClient(7, "alice-laptop"); got != client {
		t.Fatalf("bound device client = %#v, want current client", got)
	}
	devices := hub.userDevices.list(7)
	if len(devices) != 1 || devices[0].RuntimeRole != "desktop" {
		t.Fatalf("registered runtime role = %#v, want desktop", devices)
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
		Capabilities:   []string{"read_file", "resolve_common_directory", "write_file", "send_file"},
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

func int64Ptr(v int64) *int64 {
	return &v
}
