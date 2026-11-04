package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func deviceGrantForID(t *testing.T, grants []ScopedDeviceGrant, id string) ScopedDeviceGrant {
	t.Helper()
	for _, grant := range grants {
		if grant.DeviceID == id {
			return grant
		}
	}
	t.Fatalf("missing grant for %s: %#v", id, grants)
	return ScopedDeviceGrant{}
}

func TestDeviceTargetAuthorizationWireCompatibility(t *testing.T) {
	for _, version := range []string{"", "1"} {
		t.Run("v2="+version, func(t *testing.T) {
			t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", version)
			hub, _ := deviceSelectionFixture(t)
			bot := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud", send: make(chan []byte, 8)}
			hub.addClient(bot)
			session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
			// No default: an old consumer must never receive a first-device grant.
			identity := hub.buildCatscoIdentityMetadata(7, 42, "p2p_7_42", 1, "在 b 上读取")
			if _, ok := identity["device_grants"]; ok {
				t.Fatal("no default issued executable legacy grants")
			}
			_, hasExplicit := identity["device_target_authorizations"]
			if hasExplicit != (version == "1") {
				t.Fatal("explicit envelope escaped the v2 gate")
			}
			if _, err := hub.userDevices.confirmDeviceSelection(7, session, "b", "test", ""); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"读取桌面", "在 a 上读取", "两台都读取", "不要操作 a", "你上次为什么操作 a"} {
				identity = hub.buildCatscoIdentityMetadata(7, 42, "p2p_7_42", 2, text)
				// Decode just the old field, like an unupgraded SDK. Heartbeat
				// ordering and explicit mentions cannot widen it or change index 0.
				wire, err := json.Marshal(identity)
				if err != nil {
					t.Fatal(err)
				}
				var legacy struct {
					Grants []ScopedDeviceGrant `json:"device_grants"`
				}
				if err := json.Unmarshal(wire, &legacy); err != nil {
					t.Fatal(err)
				}
				if len(legacy.Grants) != 1 || legacy.Grants[0].DeviceID != "b" || legacy.Grants[0].TargetScope != "" {
					t.Fatalf("legacy default grant widened/reordered for %q: %s", text, wire)
				}
				selection := identity["device_selection"].(*DeviceSelection)
				if selection.SelectedDevice.DeviceID != "b" {
					t.Fatal("message text changed the default")
				}
				if version == "1" {
					envelope := identity["device_target_authorizations"].(map[string]interface{})
					grants := envelope["grants"].([]ScopedDeviceGrant)
					if envelope["schema"] != "catsco.device-targets.v1" || len(grants) != 2 {
						t.Fatalf("bad explicit envelope: %#v", envelope)
					}
				} else if _, ok := identity["device_target_authorizations"]; ok {
					t.Fatal("v1 consumer received multi-device authorizations")
				}
			}
		})
	}
}

func TestExplicitDeviceAuthorizationRequiresExactRequestScope(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	// The explicit protocol must never downgrade to unscoped compatibility.
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "")
	hub, _ := deviceSelectionFixture(t)
	bot := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud"}
	bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability})
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	pref, err := hub.userDevices.confirmDeviceSelection(7, session, "a", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	devices, unavailable := hub.userDeviceRouteCandidates(7)
	ctx := hub.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", devices, unavailable)
	defaultGrant := ctx.Grants[0]
	explicitGrant := deviceGrantForID(t, ctx.ExplicitGrants, "b")
	for _, tc := range []struct {
		name, scope, device string
		grant               ScopedDeviceGrant
		allowed             bool
	}{
		{"default A", "", "a", defaultGrant, true},
		{"default grant cannot reach B", "", "b", defaultGrant, false},
		{"explicit grant is not a default", "", "b", explicitGrant, false},
		{"explicit B", "explicit_device", "b", explicitGrant, true},
		{"explicit B cannot reach A", "explicit_device", "a", explicitGrant, false},
		{"missing exact target", "explicit_device", "", explicitGrant, false},
		{"unknown scope", "unknown", "b", explicitGrant, false},
		{"cannot upgrade default grant", "explicit_device", "a", defaultGrant, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deviceReq := &MsgDeviceRPC{TargetScope: tc.scope, DeviceID: tc.device, SessionKey: session, ActorUserID: "usr7"}
			err := validateDeviceRPCGrant(bot, deviceReq, tc.grant, DeviceGrantReadFile)
			if (err == nil) != tc.allowed {
				t.Fatalf("device_rpc authorization = %v, allowed %v", err, tc.allowed)
			}
			thin := &MsgThinToolRPC{TargetScope: tc.scope, GrantID: tc.grant.GrantID, SessionKey: session, ActorUserID: "usr7", TargetDeviceID: tc.device}
			err = hub.authorizeOrdinaryThinToolRPC(bot, thin, 7, tc.device, "read_file")
			if (err == nil) != tc.allowed {
				t.Fatalf("thin_tool_rpc authorization = %v, allowed %v", err, tc.allowed)
			}
		})
	}
	if err := hub.authorizeOrdinaryThinToolRPC(bot, &MsgThinToolRPC{TargetScope: "explicit_device"}, 7, "b", "read_file"); err == nil {
		t.Fatal("explicit request downgraded to unscoped compatibility")
	}
	bot.setRuntimeCapabilities(nil)
	if err := hub.authorizeOrdinaryThinToolRPC(bot, &MsgThinToolRPC{TargetScope: "explicit_device", GrantID: explicitGrant.GrantID, SessionKey: session, ActorUserID: "usr7", TargetDeviceID: "b"}, 7, "b", "read_file"); err == nil {
		t.Fatal("legacy reconnect executed an explicit grant")
	}
	if _, err := hub.userDevices.confirmDeviceSelection(7, session, "b", "test", pref.Revision); err != nil {
		t.Fatal(err)
	}
	for _, grant := range append(ctx.Grants, ctx.ExplicitGrants...) {
		if _, ok := hub.userDevices.lookupGrant(grant.GrantID); ok {
			t.Fatal("pre-switch capability survived")
		}
	}
}

func TestExplicitDeviceRPCRequiresNegotiatedClientAndAuditsDowngrade(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "")
	for _, capabilities := range []string{"undeclared", "cleared by hi"} {
		t.Run(capabilities, func(t *testing.T) {
			hub, _ := deviceSelectionFixture(t)
			bot := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud", send: make(chan []byte, 8)}
			hub.addClient(bot)
			session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
			if _, err := hub.userDevices.confirmDeviceSelection(7, session, "a", "test", ""); err != nil {
				t.Fatal(err)
			}
			devices, unavailable := hub.userDeviceRouteCandidates(7)
			ctx := hub.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", devices, unavailable)
			grant := deviceGrantForID(t, ctx.ExplicitGrants, "b")
			_, target := hub.findDeviceRPCTarget(7, UserDevice{DeviceID: "b"})
			if target == nil {
				t.Fatal("missing B receiver")
			}
			if capabilities == "cleared by hi" {
				bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability})
				hub.handleHi(bot, "cloud", &MsgClientHi{ID: "legacy-hi"})
				var hi ServerMessage
				decodeQueuedServerMessage(t, bot.send, &hi)
				if hi.Ctrl == nil || hi.Ctrl.Code != http.StatusOK {
					t.Fatalf("legacy hi failed: %#v", hi)
				}
			}
			request := &MsgDeviceRPC{ID: "explicit-rpc", Type: "request", RequestID: "explicit-b", GrantID: grant.GrantID,
				TargetScope: "explicit_device", DeviceID: "b", Operation: "read_file", SessionKey: session, ActorUserID: "usr7"}
			hub.handleDeviceRPCRequest(bot, request)
			var ack ServerMessage
			decodeQueuedServerMessage(t, bot.send, &ack)
			if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusForbidden || ack.Ctrl.Text != "explicit device authorization requires a negotiated client upgrade" {
				t.Fatalf("legacy explicit RPC was not rejected by negotiation: %#v", ack)
			}
			if len(target.send) != 0 || len(hub.DeviceRPCStatus(7)) != 0 {
				t.Fatal("rejected request reached the receiver or pending router")
			}
			events := hub.listDeviceAudit(7, 10)
			if len(events) != 1 || events[0].Phase != "rpc_rejected" || events[0].Result != "denied" || events[0].DeviceID != "b" || events[0].AgentID != "usr42" || events[0].SessionKey != session {
				t.Fatalf("missing scoped rejection audit: %#v", events)
			}
			// The same live grant must work once the authenticated peer opts in.
			hub.handleHi(bot, "cloud", &MsgClientHi{ID: "upgraded-hi", RuntimeCapabilities: []string{xiaobaDeviceTargetsCapability}})
			decodeQueuedServerMessage(t, bot.send, &ack)
			hub.handleDeviceRPCRequest(bot, request)
			decodeQueuedServerMessage(t, bot.send, &ack)
			if ack.Ctrl == nil || ack.Ctrl.Code != http.StatusOK {
				t.Fatalf("upgraded explicit RPC failed: %#v", ack)
			}
			var forwarded ServerMessage
			decodeQueuedServerMessage(t, target.send, &forwarded)
			if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.DeviceID != "b" || forwarded.DeviceRPC.TargetScope != "explicit_device" {
				t.Fatalf("explicit B was not forwarded exactly: %#v", forwarded)
			}
		})
	}
}

func TestExplicitDeviceAuthorizationExpiryAndRolloutDisable(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	r := newUserDeviceRegistry(time.Hour)
	now := time.Now()
	r.now = func() time.Time { return now }
	a := UserDevice{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	b := UserDevice{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	ctx := r.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{a, b}, nil)
	grant := deviceGrantForID(t, ctx.ExplicitGrants, "b")
	if _, ok := r.lookupGrant(grant.GrantID); !ok {
		t.Fatal("fresh explicit capability rejected")
	}
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "")
	if _, ok := r.lookupGrant(grant.GrantID); ok {
		t.Fatal("disabling v2 left explicit capability executable")
	}
	legacy := r.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{a, b}, nil)
	if len(legacy.Grants) != 0 || len(legacy.ExplicitGrants) != 0 {
		t.Fatal("disabled v2 issued non-default grants")
	}
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	now = now.Add(r.grantTT + time.Second)
	if _, ok := r.lookupGrant(grant.GrantID); !ok {
		t.Fatal("permanent explicit capability expired while its selection revision remained active")
	}
}

func TestExplicitDeviceTargetsKeepConversationDefault(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	for _, backend := range []string{"memory", "shared", "redis", "durable"} {
		t.Run(backend, func(t *testing.T) {
			r := newUserDeviceRegistry(time.Hour)
			if backend == "shared" {
				r.withSharedRuntime(newSharedMemoryRuntimeState())
			}
			if backend == "durable" {
				r.withSelectionStore(&durableSelectionTestStore{})
			}
			if backend == "redis" {
				url, closeRedis := newRedisRuntimeTestServer(t)
				defer closeRedis()
				state := newRedisRuntimeStateForTest(t, url, t.Name())
				defer state.Close()
				r.withSharedRuntime(state)
			}
			a := UserDevice{OwnerUID: 7, DeviceID: "a", DisplayName: "Eason", OS: "windows", Capabilities: []DeviceGrantOperation{DeviceGrantWriteFile}}
			b := UserDevice{OwnerUID: 7, DeviceID: "b", DisplayName: "FFFFFFFK", OS: "windows", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
			foreign := UserDevice{OwnerUID: 8, DeviceID: "private", Capabilities: []DeviceGrantOperation{DeviceGrantWriteFile}}
			session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
			first, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
			if err != nil {
				t.Fatal(err)
			}
			var oldGrants []ScopedDeviceGrant
			for _, order := range [][]UserDevice{{b, foreign, a}, {a, foreign, b}} {
				ctx := r.turnContextForOwnerDevices(7, 7, "p2p_7_42", "p2p", 42, "cloud", order, nil)
				if ctx.Selection.Status != DeviceSelectionSelected || ctx.Selection.SelectedDevice.DeviceID != "a" || ctx.Selection.Revision != first.Revision {
					t.Fatalf("device order changed default: %#v", ctx.Selection)
				}
				if len(ctx.Grants) != 1 || ctx.Grants[0].DeviceID != "a" || ctx.Grants[0].TargetScope != "" || len(ctx.ExplicitGrants) != 2 || len(ctx.Selection.Candidates) != 2 {
					t.Fatal("missing own device or leaked another account")
				}
				for _, grant := range ctx.ExplicitGrants {
					if grant.TargetScope != "explicit_device" || grant.SelectionRevision != first.Revision || grant.OwnerUserID != "usr7" {
						t.Fatalf("invalid grant: %#v", grant)
					}
					if _, ok := r.lookupGrant(grant.GrantID); !ok {
						t.Fatal("own explicit grant rejected")
					}
				}
				bGrant := deviceGrantForID(t, ctx.ExplicitGrants, "b")
				if len(bGrant.Operations) != 1 || bGrant.Operations[0] != DeviceGrantReadFile {
					t.Fatal("per-device capabilities widened")
				}
				oldGrants = append(oldGrants, ctx.Grants...)
				oldGrants = append(oldGrants, ctx.ExplicitGrants...)
			}
			// Both task-specific grants are invalid after a UI selection change,
			// including a grant for the newly selected device.
			if _, err := r.confirmDeviceSelection(7, session, "b", "user_confirmation", first.Revision); err != nil {
				t.Fatal(err)
			}
			for _, grant := range oldGrants {
				if _, ok := r.lookupGrant(grant.GrantID); ok {
					t.Fatal("pre-switch grant survived")
				}
			}
		})
	}
}

func TestExplicitTargetsWithoutDefaultAndWithOfflineDefault(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	r := newUserDeviceRegistry(time.Hour)
	a := UserDevice{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	b := UserDevice{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	ctx := r.turnContextForOwnerDevices(7, 7, "grp_1", "group", 42, "cloud", []UserDevice{a, b}, nil)
	if ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil || len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 2 {
		t.Fatalf("explicit task should not auto-select a default: %#v", ctx)
	}
	for _, grant := range ctx.ExplicitGrants {
		if _, ok := r.lookupGrant(grant.GrantID); !ok {
			t.Fatal("grant without default rejected")
		}
	}
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	if _, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", ""); err != nil {
		t.Fatal(err)
	}
	ctx = r.turnContextForOwnerDevices(7, 7, "grp_1", "group", 42, "cloud", []UserDevice{b}, []UserDevice{a})
	if ctx.Selection.Status != DeviceSelectionUnavailable || ctx.Selection.SelectedDevice.DeviceID != "a" || len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 1 || ctx.ExplicitGrants[0].DeviceID != "b" {
		t.Fatalf("offline default was replaced or explicit B denied: %#v", ctx)
	}
	if _, ok := r.lookupGrant(ctx.ExplicitGrants[0].GrantID); !ok {
		t.Fatal("explicit B denied because default A is offline")
	}
	old := ctx.ExplicitGrants[0]
	now := time.Now().Add(r.preferenceTTL + time.Minute)
	r.now = func() time.Time { return now }
	ctx = r.turnContextForOwnerDevices(7, 7, "grp_1", "group", 42, "cloud", []UserDevice{b}, []UserDevice{a})
	if ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil || len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 1 {
		t.Fatal("expired default must not choose B or block an independent B grant")
	}
	if _, ok := r.lookupGrant(old.GrantID); !ok {
		t.Fatal("permanent exact-target grant expired with the default-selection lease")
	}
	if _, ok := r.lookupGrant(ctx.ExplicitGrants[0].GrantID); !ok {
		t.Fatal("fresh exact-target grant rejected")
	}
}

func TestDelegatedChannelDevicesRemainSelectedOnly(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	r := newUserDeviceRegistry(time.Hour)
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 8)
	if _, err := r.confirmDeviceSelection(8, session, "a", "test", ""); err != nil {
		t.Fatal(err)
	}
	devices := []UserDevice{
		{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
		{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
	}
	ctx := r.turnContextForOwnerDevices(8, 7, "grp_1", "group", 42, "cloud", devices, nil)
	if len(ctx.Grants) != 1 || ctx.Grants[0].DeviceID != "a" || ctx.Grants[0].TargetScope != "" || len(ctx.ExplicitGrants) != 0 {
		t.Fatal("same-account policy leaked into delegated channel flow")
	}
}

func TestExplicitTargetAfterPickerSnapshotWithoutDefault(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	hub, handler := deviceSelectionFixture(t)
	snapshot := selectionSnapshot(t, handler, 7, "p2p_7_42", "42")
	if snapshot["status"] != "needs_selection" {
		t.Fatal("fixture unexpectedly has a default")
	}
	devices, unavailable := hub.userDeviceRouteCandidates(7)
	ctx := hub.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", devices, unavailable)
	if ctx.Selection.SelectedDevice != nil || ctx.Selection.Revision != snapshot["revision"] || len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 2 {
		t.Fatal("text chose a default or lost snapshot revision")
	}
	for _, grant := range ctx.ExplicitGrants {
		if _, ok := hub.userDevices.lookupGrant(grant.GrantID); !ok {
			t.Fatal("empty picker slot rejected exact target grant")
		}
	}
}
