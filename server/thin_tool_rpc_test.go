package server

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestThinToolRPCRejectsWrongDeviceResultWithoutConsumingPending(t *testing.T) {
	hub := NewHub(nil, nil)
	agent := &Client{
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 4),
	}
	target := &Client{
		uid:            7,
		accountType:    types.AccountHuman,
		deviceOwnerUID: 7,
		deviceID:       "alice-laptop",
		send:           make(chan []byte, 4),
	}
	wrong := &Client{
		uid:            8,
		accountType:    types.AccountHuman,
		deviceOwnerUID: 8,
		deviceID:       "bob-laptop",
		send:           make(chan []byte, 4),
	}
	hub.addClient(agent)
	hub.addClient(target)
	hub.addClient(wrong)

	expiresAt := time.Now().Add(time.Minute)
	if !hub.thinToolRPC.add(thinToolRPCPending{
		requestID:      "thin-1",
		requester:      agent,
		requesterRoute: hub.clientRoute(agent),
		targetRoute:    hub.clientRoute(target),
		targetOwnerUID: 7,
		targetDeviceID: "alice-laptop",
		toolName:       "glob",
		createdAt:      time.Now(),
		expiresAt:      expiresAt,
	}) {
		t.Fatal("failed to add thin tool rpc pending request")
	}

	hub.handleThinToolRPCResult(wrong, &MsgThinToolRPC{
		ID:        "wrong-result",
		Type:      thinToolRPCTypeResult,
		RequestID: "thin-1",
		Result:    map[string]interface{}{"ok": true},
	})

	var wrongAck ServerMessage
	decodeQueuedServerMessage(t, wrong.send, &wrongAck)
	if wrongAck.Ctrl == nil || wrongAck.Ctrl.Code != http.StatusForbidden {
		t.Fatalf("wrong device ack = %#v, want 403", wrongAck.Ctrl)
	}
	if _, ok := hub.thinToolRPC.get("thin-1"); !ok {
		t.Fatal("wrong device result consumed pending request")
	}
	if drainOne(agent.send) {
		t.Fatal("requester should not receive result from wrong device")
	}

	hub.handleThinToolRPCResult(target, &MsgThinToolRPC{
		ID:        "target-result",
		Type:      thinToolRPCTypeResult,
		RequestID: "thin-1",
		DeviceID:  "spoofed-device",
		ToolName:  "execute_shell",
		Result:    map[string]interface{}{"ok": true},
	})

	var result ServerMessage
	decodeQueuedServerMessage(t, agent.send, &result)
	if result.ThinToolRPC == nil || result.ThinToolRPC.RequestID != "thin-1" || result.ThinToolRPC.DeviceID != "alice-laptop" {
		t.Fatalf("requester result = %#v, want thin_tool_rpc result from target", result.ThinToolRPC)
	}
	if result.ThinToolRPC.ToolName != "glob" {
		t.Fatalf("requester tool = %q, want canonical pending tool glob", result.ThinToolRPC.ToolName)
	}
	var targetAck ServerMessage
	decodeQueuedServerMessage(t, target.send, &targetAck)
	if targetAck.Ctrl == nil || targetAck.Ctrl.Code != http.StatusOK {
		t.Fatalf("target ack = %#v, want 200", targetAck.Ctrl)
	}
	if _, ok := hub.thinToolRPC.get("thin-1"); ok {
		t.Fatal("target result should finish pending request")
	}
}

func TestThinToolRPCFinishMatchingDoesNotClaimReusedRequestID(t *testing.T) {
	router := newThinToolRPCRouter(time.Minute)
	now := time.Now()
	oldRequester := &Client{connectionID: "requester-old"}
	newRequester := &Client{connectionID: "requester-new"}
	target := runtimeRoute{NodeID: "node-a", ConnectionID: "target", ExpiresAt: now.Add(time.Minute)}
	oldPending := thinToolRPCPending{
		requestID:      "reused-id",
		requester:      oldRequester,
		requesterRoute: runtimeRoute{NodeID: "node-a", ConnectionID: oldRequester.connectionID, ExpiresAt: now.Add(time.Minute)},
		targetRoute:    target,
		createdAt:      now,
		expiresAt:      now.Add(30 * time.Second),
	}
	if !router.add(oldPending) {
		t.Fatal("failed to add old pending request")
	}
	if cancelled := router.cancelByRequesterRoute(oldPending.requesterRoute); len(cancelled) != 1 {
		t.Fatalf("cancelled = %d, want 1", len(cancelled))
	}
	newPending := oldPending
	newPending.requester = newRequester
	newPending.requesterRoute = runtimeRoute{NodeID: "node-a", ConnectionID: newRequester.connectionID, ExpiresAt: now.Add(time.Minute)}
	newPending.createdAt = now.Add(time.Nanosecond)
	if !router.add(newPending) {
		t.Fatal("failed to reuse request ID after cancellation")
	}
	if _, ok := router.finishMatching(oldPending); ok {
		t.Fatal("old request claimed a newer pending request with the same ID")
	}
	if current, ok := router.get(newPending.requestID); !ok || current.requester != newRequester {
		t.Fatal("new pending request was not preserved")
	}
}

func TestThinToolRPCTimeoutKeepsRequesterRouteDeliverable(t *testing.T) {
	db := &agentTestStore{owners: map[int64]int64{42: 7}}
	hub := NewHub(db, nil)
	if _, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:     "alice-laptop",
		Status:       "online",
		Capabilities: []string{string(DeviceGrantSkillHubWorkspaceGet)},
	}); err != nil {
		t.Fatal(err)
	}
	requester := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	target := &Client{uid: 77, accountType: types.AccountBot, send: make(chan []byte, 2)}
	hub.addClient(requester)
	hub.addClient(target)
	hub.bindDeviceClient(7, UserDevice{DeviceID: "alice-laptop"}, target)

	expiresAt := time.Now().Add(100 * time.Millisecond)
	hub.handleThinToolRPCRequest(requester, &MsgThinToolRPC{
		ID:                "timeout-msg",
		Type:              thinToolRPCTypeRequest,
		RequestID:         "timeout-request",
		TargetOwnerUserID: "usr7",
		TargetDeviceID:    "alice-laptop",
		ToolName:          string(DeviceGrantSkillHubWorkspaceGet),
		Payload:           map[string]interface{}{"bot_uid": "42"},
		ExpiresAt:         unixMillis(expiresAt),
	})

	pending, ok := hub.thinToolRPC.get("timeout-request")
	if !ok {
		t.Fatal("request was not left pending")
	}
	if !pending.requesterRoute.ExpiresAt.After(pending.expiresAt) {
		t.Fatalf("requester route expires at %v, want after request expiry %v", pending.requesterRoute.ExpiresAt, pending.expiresAt)
	}
	drainOne(requester.send) // request acknowledgement
	drainOne(target.send)    // forwarded request
	time.Sleep(time.Until(expiresAt) + 25*time.Millisecond)
	if got := hub.expireThinToolRPCRequests(time.Now()); got != 1 {
		t.Fatalf("expired = %d, want 1", got)
	}
	var timeout ServerMessage
	decodeQueuedServerMessage(t, requester.send, &timeout)
	if timeout.ThinToolRPC == nil || timeout.ThinToolRPC.Error == nil || timeout.ThinToolRPC.Error.Code != "thin_tool_rpc_timeout" {
		t.Fatalf("timeout message = %#v", timeout.ThinToolRPC)
	}
}

func TestThinToolRPCRequesterDisconnectReleasesPendingCapacity(t *testing.T) {
	hub := NewHub(nil, nil)
	requester := &Client{
		uid:         42,
		accountType: types.AccountHuman,
		send:        make(chan []byte, 1),
	}
	hub.addClient(requester)
	otherRequester := &Client{uid: 43, accountType: types.AccountHuman, send: make(chan []byte, 1)}
	hub.addClient(otherRequester)
	target := &Client{uid: 77, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(target)

	now := time.Now()
	requesterRoute := hub.clientRoute(requester)
	targetRoute := hub.clientRoute(target)
	for i := 0; i < maxThinToolRPCPendingPerDevice; i++ {
		if !hub.thinToolRPC.add(thinToolRPCPending{
			requestID:      fmt.Sprintf("device-pending-%d", i),
			requester:      requester,
			requesterRoute: requesterRoute,
			targetRoute:    targetRoute,
			targetOwnerUID: 7,
			targetDeviceID: "alice-laptop",
			createdAt:      now,
			expiresAt:      now.Add(time.Minute),
		}) {
			t.Fatalf("failed to add pending request %d", i)
		}
	}
	if hub.thinToolRPC.add(thinToolRPCPending{
		requestID:      "over-device-limit",
		requester:      otherRequester,
		requesterRoute: hub.clientRoute(otherRequester),
		targetRoute:    targetRoute,
		targetOwnerUID: 7,
		targetDeviceID: "alice-laptop",
		createdAt:      now,
		expiresAt:      now.Add(time.Minute),
	}) {
		t.Fatal("request over device limit was accepted")
	}

	go hub.Run()
	hub.unregister <- requester
	eventually(t, func() bool {
		for i := 0; i < maxThinToolRPCPendingPerDevice; i++ {
			if _, ok := hub.thinToolRPC.get(fmt.Sprintf("device-pending-%d", i)); ok {
				return false
			}
		}
		return true
	}, "requester unregister should clear all same-device pending requests")

	if !hub.thinToolRPC.add(thinToolRPCPending{
		requestID:      "after-requester-disconnect",
		requester:      otherRequester,
		requesterRoute: hub.clientRoute(otherRequester),
		targetRoute:    targetRoute,
		targetOwnerUID: 7,
		targetDeviceID: "alice-laptop",
		createdAt:      now,
		expiresAt:      now.Add(time.Minute),
	}) {
		t.Fatal("requester unregister did not release same-device pending capacity")
	}
}

func TestThinToolRPCTargetReplacementCancelsOnlyOldRouteAndNotifiesRequester(t *testing.T) {
	hub := NewHub(nil, nil)
	now := time.Now()
	requester := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	oldTarget := &Client{uid: 77, accountType: types.AccountBot, send: make(chan []byte, 1)}
	newTarget := &Client{uid: 78, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(requester)
	hub.addClient(oldTarget)
	hub.addClient(newTarget)
	device := UserDevice{DeviceID: "alice-laptop", BodyID: "body-device", InstallationID: "install-device"}
	hub.bindDeviceClient(7, device, oldTarget)

	oldRoute := hub.clientRoute(oldTarget)
	newRoute := hub.clientRoute(newTarget)
	for _, pending := range []thinToolRPCPending{
		{
			requestID:      "old-target-request",
			requester:      requester,
			requesterRoute: hub.clientRoute(requester),
			targetRoute:    oldRoute,
			targetOwnerUID: 7,
			targetDeviceID: device.DeviceID,
			toolName:       "skillhub_workspace_get",
			createdAt:      now,
			expiresAt:      now.Add(time.Minute),
		},
		{
			requestID:      "new-target-request",
			requester:      requester,
			requesterRoute: hub.clientRoute(requester),
			targetRoute:    newRoute,
			targetOwnerUID: 7,
			targetDeviceID: device.DeviceID,
			toolName:       "skillhub_workspace_get",
			createdAt:      now,
			expiresAt:      now.Add(time.Minute),
		},
	} {
		if !hub.thinToolRPC.add(pending) {
			t.Fatalf("failed to add pending request %q", pending.requestID)
		}
	}

	hub.bindDeviceClient(7, device, newTarget)

	if _, ok := hub.thinToolRPC.get("old-target-request"); ok {
		t.Fatal("target replacement left the old route request pending")
	}
	if _, ok := hub.thinToolRPC.get("new-target-request"); !ok {
		t.Fatal("target replacement removed a request for the new route")
	}
	var result ServerMessage
	decodeQueuedServerMessage(t, requester.send, &result)
	if result.ThinToolRPC == nil || result.ThinToolRPC.RequestID != "old-target-request" {
		t.Fatalf("requester received %#v, want cancellation for old target request", result.ThinToolRPC)
	}
	if result.ThinToolRPC.Error == nil || result.ThinToolRPC.Error.Code != "target_device_unavailable" {
		t.Fatalf("replacement error = %#v, want target_device_unavailable", result.ThinToolRPC.Error)
	}
	if drainOne(requester.send) {
		t.Fatal("requester received a cancellation for the new target route")
	}
}

func TestSkillHubThinToolRPCAuthorization(t *testing.T) {
	db := &agentTestStore{owners: map[int64]int64{42: 7, 43: 8, 44: 7}}
	hub := NewHub(db, nil)
	if _, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID: "alice-laptop",
		Status:   "online",
		Capabilities: []string{
			string(DeviceGrantSkillHubWorkspaceGet),
			string(DeviceGrantSkillHubSkillShare),
		},
	}); err != nil {
		t.Fatal(err)
	}
	alice := &Client{uid: 7, accountType: types.AccountHuman}

	valid := &MsgThinToolRPC{
		TargetOwnerUserID: "usr7",
		TargetDeviceID:    "alice-laptop",
		ToolName:          string(DeviceGrantSkillHubWorkspaceGet),
		Payload:           map[string]interface{}{"bot_uid": "42"},
	}
	if err := hub.authorizeSkillHubThinToolRPC(alice, valid, 7, "alice-laptop", valid.ToolName); err != nil {
		t.Fatalf("valid SkillHub request rejected: %v", err)
	}
	if _, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		BotUID:      42,
		DeviceID:    "alice-server",
		RuntimeRole: "server",
		Status:      "online",
		Capabilities: []string{
			string(DeviceGrantSkillHubWorkspaceGet),
			string(DeviceGrantSkillHubBotSwitch),
		},
	}); err != nil {
		t.Fatal(err)
	}
	serverRequest := &MsgThinToolRPC{
		TargetOwnerUserID: "usr7",
		TargetDeviceID:    "alice-server",
		ToolName:          string(DeviceGrantSkillHubWorkspaceGet),
		Payload:           map[string]interface{}{"bot_uid": "42"},
	}
	if err := hub.authorizeSkillHubThinToolRPC(alice, serverRequest, 7, "alice-server", serverRequest.ToolName); err != nil {
		t.Fatalf("bound server SkillHub request rejected: %v", err)
	}
	serverRequest.Payload = map[string]interface{}{"bot_uid": "44"}
	if err := hub.authorizeSkillHubThinToolRPC(alice, serverRequest, 7, "alice-server", serverRequest.ToolName); err == nil {
		t.Fatal("server Runtime unexpectedly authorized a different owner Bot")
	}
	serverRequest.Payload = map[string]interface{}{"bot_uid": "42"}
	serverRequest.ToolName = string(DeviceGrantSkillHubBotSwitch)
	if err := hub.authorizeSkillHubThinToolRPC(alice, serverRequest, 7, "alice-server", serverRequest.ToolName); err == nil {
		t.Fatal("server Runtime unexpectedly authorized Bot switching")
	}

	tests := []struct {
		name     string
		ownerUID int64
		deviceID string
		toolName string
		botUID   string
	}{
		{name: "other owner", ownerUID: 8, deviceID: "alice-laptop", toolName: string(DeviceGrantSkillHubWorkspaceGet), botUID: "42"},
		{name: "other bot owner", ownerUID: 7, deviceID: "alice-laptop", toolName: string(DeviceGrantSkillHubWorkspaceGet), botUID: "43"},
		{name: "generic human tool", ownerUID: 7, deviceID: "alice-laptop", toolName: "execute_shell", botUID: "42"},
		{name: "missing capability", ownerUID: 7, deviceID: "alice-laptop", toolName: string(DeviceGrantSkillHubSkillFinalize), botUID: "42"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg := &MsgThinToolRPC{
				TargetOwnerUserID: formatUID(test.ownerUID),
				TargetDeviceID:    test.deviceID,
				ToolName:          test.toolName,
				Payload:           map[string]interface{}{"bot_uid": test.botUID},
			}
			if err := hub.authorizeSkillHubThinToolRPC(alice, msg, test.ownerUID, test.deviceID, test.toolName); err == nil {
				t.Fatal("request unexpectedly authorized")
			}
		})
	}
}

func TestParseThinToolRPCBotUIDRejectsNonIntegerNumbers(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected int64
	}{
		{name: "string", value: "42", expected: 42},
		{name: "JSON integer", value: float64(42), expected: 42},
		{name: "fraction", value: 42.9, expected: 0},
		{name: "zero", value: float64(0), expected: 0},
		{name: "negative", value: int64(-42), expected: 0},
		{name: "overflow", value: float64(1 << 63), expected: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := parseThinToolRPCBotUID(map[string]interface{}{"bot_uid": test.value})
			if actual != test.expected {
				t.Fatalf("parseThinToolRPCBotUID(%v) = %d, want %d", test.value, actual, test.expected)
			}
		})
	}
}
