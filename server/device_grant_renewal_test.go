package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func grantRenewalFixture(t *testing.T) (*Hub, *Client, *MsgDeviceGrantRenewal, *time.Time) {
	t.Helper()
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	h, _ := deviceSelectionFixture(t)
	now := time.Now().Truncate(time.Millisecond)
	h.userDevices.now = func() time.Time { return now }
	h.bodyLeases.now = func() time.Time { return now }
	bot := &Client{hub: h, uid: 42, accountType: types.AccountBot, bodyID: "cloud", connectionID: "renew-cloud", send: make(chan []byte, 16)}
	bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability})
	h.addClient(bot)
	if _, err := h.bodyLeases.acquire(42, "cloud", bot.connectionID); err != nil {
		t.Fatal(err)
	}
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	if _, err := h.userDevices.confirmDeviceSelection(7, session, "a", "test", ""); err != nil {
		t.Fatal(err)
	}
	devices, unavailable := h.userDeviceRouteCandidates(7)
	turn := h.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", devices, unavailable)
	return h, bot, &MsgDeviceGrantRenewal{Type: "request", RequestID: "renew-1", GrantID: turn.Grants[0].GrantID}, &now
}

func TestDeviceGrantRenewalPreservesAuthorityAcrossTenMinutes(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "shared"}[shared], func(t *testing.T) {
			h, bot, msg, now := grantRenewalFixture(t)
			original, _ := h.userDevices.lookupGrant(msg.GrantID)
			if shared {
				h.userDevices.withSharedRuntime(newSharedMemoryRuntimeState())
				h.userDevices.rememberGrants([]ScopedDeviceGrant{original})
				// Keep selection revision identical in the shared store.
				h.userDevices.shared.compareDeviceSelection(7, original.SessionKey, "", deviceSelectionPreference{DeviceID: "a", Revision: original.SelectionRevision, ExpiresAt: unixMillis(now.Add(time.Hour))})
			}
			// Shared registry tests below exercise renewal persistence via the same hub routes.
			if shared {
				for _, id := range []string{"a", "b"} {
					_, err := h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: id, RuntimeRole: "desktop", Capabilities: []string{"read_file"}})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			*now = now.Add(5 * time.Minute)
			h.userDevices.touch(7, "a")
			next, err := h.renewDeviceGrant(bot, msg)
			if err != nil {
				t.Fatal(err)
			}
			if next.GrantID == original.GrantID || next.CreatedAt != original.CreatedAt || next.ExpiresAt <= original.ExpiresAt || next.DeviceID != "a" || next.SelectionRevision != original.SelectionRevision || next.TargetScope != "" {
				t.Fatalf("authority changed: %#v", next)
			}
			*now = now.Add(6 * time.Minute)
			if _, ok := h.userDevices.lookupGrant(original.GrantID); ok {
				t.Fatal("old expired grant revived")
			}
			if _, ok := h.userDevices.lookupGrant(next.GrantID); !ok {
				t.Fatal("renewed grant died at original ten-minute boundary")
			}
		})
	}
}

func TestDeviceGrantRenewalRejectsChangedAuthority(t *testing.T) {
	for _, name := range []string{"expired", "wrong bot", "wrong body", "human", "wiki", "connector", "unknown grant", "unknown request", "offline", "unregistered", "capability removed", "incarnation changed", "selection changed", "actor disabled", "friend removed", "24h limit", "runtime lease replaced"} {
		t.Run(name, func(t *testing.T) {
			h, bot, msg, now := grantRenewalFixture(t)
			old, _ := h.userDevices.lookupGrant(msg.GrantID)
			switch name {
			case "expired":
				*now = now.Add(11 * time.Minute)
			case "wrong bot":
				bot.uid = 43
			case "wrong body":
				bot.bodyID = "other"
			case "human":
				bot.accountType = types.AccountHuman
			case "wiki":
				bot.wikiAgentUID = 42
			case "connector":
				bot.deviceConnector = &DeviceConnectorClaims{}
			case "unknown grant":
				msg.GrantID = "missing"
			case "unknown request":
				msg.Type = "result"
			case "offline":
				_, _ = h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", Status: "offline", Capabilities: []string{"read_file"}})
			case "unregistered":
				h.userDevices.unregister(7, "a")
			case "capability removed":
				_, _ = h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", Capabilities: []string{"write_file"}})
			case "incarnation changed":
				_, _ = h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", BodyID: "replacement", Capabilities: []string{"read_file"}})
			case "selection changed":
				_, _ = h.userDevices.confirmDeviceSelection(7, old.SessionKey, "b", "test", old.SelectionRevision)
			case "actor disabled":
				h.db.(*identityMessageStore).users[7].State = 1
			case "friend removed":
				h.db.(*identityMessageStore).friendPairs = map[string]bool{}
			case "24h limit":
				old.RenewableUntil = unixMillis(*now)
				h.userDevices.rememberGrants([]ScopedDeviceGrant{old})
			case "runtime lease replaced":
				h.bodyLeases.release(42, "cloud", bot.connectionID)
			}
			if _, err := h.renewDeviceGrant(bot, msg); err == nil {
				t.Fatal("unauthorized renewal accepted")
			}
		})
	}
}

func TestDeviceGrantRenewalWireAndLegacyCompatibility(t *testing.T) {
	h, bot, msg, _ := grantRenewalFixture(t)
	h.handleMessage(bot, &ClientMessage{DeviceGrantRenewal: msg})
	var reply ServerMessage
	select {
	case raw := <-bot.send:
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("no direct renewal reply")
	}
	if reply.DeviceGrantRenewal == nil || reply.DeviceGrantRenewal.Grant == nil || reply.DeviceGrantRenewal.Error != nil {
		t.Fatalf("bad reply: %#v", reply.DeviceGrantRenewal)
	}
	if deviceConnectorMessageAllowed(&ClientMessage{DeviceGrantRenewal: msg}) {
		t.Fatal("connector can mint bot authority")
	}
	// A prior client omits the new message entirely; existing grant remains usable.
	if _, ok := h.userDevices.lookupGrant(msg.GrantID); !ok {
		t.Fatal("renewal invalidated old in-flight capability")
	}
}

func TestDeviceGrantRenewalExplicitTargetDoesNotChangeDefault(t *testing.T) {
	h, bot, msg, now := grantRenewalFixture(t)
	original, _ := h.userDevices.lookupGrant(msg.GrantID)
	devices, unavailable := h.userDeviceRouteCandidates(7)
	turn := h.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", devices, unavailable)
	explicit := deviceGrantForID(t, turn.ExplicitGrants, "b")
	msg.GrantID = explicit.GrantID
	*now = now.Add(4 * time.Minute)
	next, err := h.renewDeviceGrant(bot, msg)
	if err != nil {
		t.Fatal(err)
	}
	if next.TargetScope != "explicit_device" || next.DeviceID != "b" {
		t.Fatal("explicit scope widened")
	}
	selection, _ := h.userDevices.selectionRecord(7, original.SessionKey)
	if selection.DeviceID != "a" || selection.Revision != original.SelectionRevision {
		t.Fatal("explicit renewal changed default")
	}
	// Both the old and newly renewed explicit grants die after a selection revision change.
	_, _ = h.userDevices.confirmDeviceSelection(7, original.SessionKey, "b", "test", original.SelectionRevision)
	if _, ok := h.userDevices.lookupGrant(next.GrantID); ok {
		t.Fatal("renewal survived selection change")
	}
	if _, err := h.renewDeviceGrant(bot, msg); err == nil {
		t.Fatal("old grant resurrected after switch")
	}
}

func TestThinShellDeadlineMatchesRequestedExecutionButOtherToolsStayBounded(t *testing.T) {
	for _, tool := range []string{"execute_shell", "read_file"} {
		t.Run(tool, func(t *testing.T) {
			h, bot, msg, now := grantRenewalFixture(t)
			grant, _ := h.userDevices.lookupGrant(msg.GrantID)
			grant.Operations = []DeviceGrantOperation{DeviceGrantReadFile, DeviceGrantExecuteShell}
			h.userDevices.rememberGrants([]ScopedDeviceGrant{grant})
			_, _ = h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", RuntimeRole: "desktop", Capabilities: []string{"read_file", "execute_shell"}})
			h.thinToolRPC.now = func() time.Time { return *now }
			request := &MsgThinToolRPC{Type: "request", RequestID: "shell-deadline", GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID,
				TargetOwnerUserID: "usr7", TargetDeviceID: "a", ToolName: tool, ExpiresAt: unixMillis(now.Add(190 * time.Second)), Payload: map[string]interface{}{"args": map[string]interface{}{"timeout": 180000}}}
			h.handleThinToolRPCRequest(bot, request)
			pending, ok := h.thinToolRPC.get(request.RequestID)
			if !ok {
				t.Fatal("request not dispatched")
			}
			expected := 120 * time.Second
			if tool == "execute_shell" {
				expected = 190 * time.Second
			}
			if pending.expiresAt.Sub(*now) != expected {
				t.Fatalf("got %v want %v", pending.expiresAt.Sub(*now), expected)
			}
		})
	}
}

func TestDeviceGrantRenewalLogoutCannotBeUndoneByRelogin(t *testing.T) {
	h, bot, msg, now := grantRenewalFixture(t)
	old, _ := h.userDevices.lookupGrant(msg.GrantID)
	h.userDevices.unregister(7, "a")
	h.revokeDeviceConnectorDevice(7, "a")
	// Simulate re-registering the same stable device id/body/installation.
	*now = now.Add(time.Second)
	_, _ = h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", RuntimeRole: "desktop", Capabilities: []string{"read_file"}})
	if _, err := h.renewDeviceGrant(bot, msg); err == nil {
		t.Fatal("logout watermark was ignored")
	}
	request := &MsgThinToolRPC{GrantID: old.GrantID, SessionKey: old.SessionKey, ActorUserID: old.ActorUserID, TargetDeviceID: "a"}
	if err := h.authorizeOrdinaryThinToolRPC(bot, request, 7, "a", "read_file"); err == nil {
		t.Fatal("old task resumed after relogin")
	}
	devices, unavailable := h.userDeviceRouteCandidates(7)
	turn := h.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", devices, unavailable)
	if len(turn.Grants) != 1 {
		t.Fatal("fresh human turn lost authorization")
	}
	msg.GrantID = turn.Grants[0].GrantID
	if _, err := h.renewDeviceGrant(bot, msg); err != nil {
		t.Fatal("fresh post-login task cannot renew", err)
	}
}
