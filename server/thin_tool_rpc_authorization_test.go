package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestThinToolRPCScopedAuthorization(t *testing.T) {
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "required")
	for _, name := range []string{
		"valid shared bot", "missing grant", "unknown grant", "missing session", "wrong session",
		"wrong actor", "wrong owner", "wrong device", "wrong bot", "wrong body", "missing body",
		"expired", "revoked", "wrong operation", "unknown tool", "expired request", "device replaced",
		"capability removed", "send file", "import file", "server target",
	} {
		t.Run(name, func(t *testing.T) {
			// Bot owner 8 is not the speaking user 7. This is intentional: shared
			// cloud Bots must work when user 7 has a valid server-issued grant.
			hub := NewHub(&agentTestStore{owners: map[int64]int64{42: 8}}, nil)
			device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
				DeviceID: "scoped-a", BodyID: "desktop-a", InstallationID: "install-a",
				RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file", "send_file"},
			})
			if err != nil {
				t.Fatal(err)
			}
			requester := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud-body", send: make(chan []byte, 8)}
			receiver := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
			hub.addClient(requester)
			hub.addClient(receiver)
			hub.bindDeviceClient(7, device, receiver)
			grant := hub.userDevices.grantsForDevices(7, "p2p_7_42", "p2p", 42, "cloud-body", []UserDevice{device})[0]
			msg := &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: "scoped-test", GrantID: grant.GrantID,
				ActorUserID: grant.ActorUserID, SessionKey: grant.SessionKey,
				TargetOwnerUserID: "usr7", TargetDeviceID: device.DeviceID, ToolName: "read_file"}
			valid := name == "valid shared bot" || name == "send file" || name == "import file"
			switch name {
			case "missing grant":
				msg.GrantID = ""
			case "unknown grant":
				msg.GrantID = "not-issued"
			case "missing session":
				msg.SessionKey = ""
			case "wrong session":
				msg.SessionKey = "another-session"
			case "wrong actor":
				msg.ActorUserID = "usr8"
			case "wrong owner":
				msg.TargetOwnerUserID = "usr8"
			case "wrong device":
				msg.TargetDeviceID = "scoped-b"
			case "wrong bot":
				requester.uid = 43
			case "wrong body":
				requester.bodyID = "other-cloud-body"
			case "missing body":
				requester.bodyID = ""
			case "expired":
				hub.userDevices.now = func() time.Time { return time.UnixMilli(grant.ExpiresAt + 1) }
			case "revoked":
				grant.Status = "revoked"
				hub.userDevices.rememberGrants([]ScopedDeviceGrant{grant})
			case "wrong operation":
				msg.ToolName = "execute_shell"
			case "unknown tool":
				msg.ToolName = "unchecked_plugin_tool"
			case "expired request":
				msg.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
			case "device replaced", "capability removed", "server target":
				req := RegisterUserDeviceRequest{DeviceID: device.DeviceID, BodyID: device.BodyID, InstallationID: device.InstallationID, RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file", "send_file"}}
				if name == "device replaced" {
					req.InstallationID = "other-installation"
				}
				if name == "capability removed" {
					req.Capabilities = []string{"glob"}
				}
				if name == "server target" {
					req.RuntimeRole = "server"
				}
				if _, err := hub.userDevices.register(7, req); err != nil {
					t.Fatal(err)
				}
			case "send file":
				msg.ToolName = "send_file"
			case "import file":
				msg.ToolName = "import_file"
			}
			hub.handleMessage(requester, &ClientMessage{ThinToolRPC: msg})
			if valid {
				var forwarded ServerMessage
				decodeQueuedServerMessage(t, receiver.send, &forwarded)
				if forwarded.ThinToolRPC == nil || forwarded.ThinToolRPC.GrantID != grant.GrantID || forwarded.ThinToolRPC.ExpiresAt > grant.ExpiresAt {
					t.Fatalf("expected scoped forward bounded by grant: %#v", forwarded.ThinToolRPC)
				}
			} else {
				if drainOne(receiver.send) {
					t.Fatal("unauthorized request reached device")
				}
				if _, ok := hub.thinToolRPC.get(msg.RequestID); ok {
					t.Fatal("unauthorized pending request retained")
				}
				var response ServerMessage
				decodeQueuedServerMessage(t, requester.send, &response)
				if response.ThinToolRPC == nil || response.ThinToolRPC.Error == nil {
					t.Fatalf("expected explicit denial: %#v", response)
				}
			}
		})
	}
}

func TestThinToolRPCLegacyCompatibilityChecksTarget(t *testing.T) {
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "")
	hub := NewHub(&agentTestStore{owners: map[int64]int64{42: 7}}, nil)
	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID: "legacy-a", RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud-body", send: make(chan []byte, 1)}
	if err := hub.authorizeOrdinaryThinToolRPC(client, &MsgThinToolRPC{ToolName: "read_file"}, 7, device.DeviceID, "read_file"); err != nil {
		t.Fatalf("legacy target should remain compatible: %v", err)
	}
	if err := hub.authorizeOrdinaryThinToolRPC(client, &MsgThinToolRPC{ToolName: "read_file"}, 7, "missing", "read_file"); err == nil {
		t.Fatal("legacy target validation accepted an unknown device")
	}
}
