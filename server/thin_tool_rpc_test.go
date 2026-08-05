package server

import (
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

func TestSkillHubThinToolRPCAuthorization(t *testing.T) {
	db := &agentTestStore{owners: map[int64]int64{42: 7, 43: 8}}
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
