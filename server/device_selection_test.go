package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestDeviceSelectionVersionedState(t *testing.T) {
	for _, backend := range []string{"local", "shared-memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			r := newUserDeviceRegistry(time.Hour)
			if backend == "shared-memory" {
				r.withSharedRuntime(newSharedMemoryRuntimeState())
			}
			if backend == "redis" {
				url, closeRedis := newRedisRuntimeTestServer(t)
				defer closeRedis()
				state := newRedisRuntimeStateForTest(t, url, t.Name())
				defer state.Close()
				r.withSharedRuntime(state)
			}
			session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
			first, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
			if err != nil {
				t.Fatal(err)
			}
			device := UserDevice{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
			grant := r.grantsForDevices(7, "grp_1", "group", 42, "cloud", []UserDevice{device})[0]
			if _, ok := r.lookupGrant(grant.GrantID); !ok {
				t.Fatal("valid grant rejected")
			}
			if _, err := r.confirmDeviceSelection(7, session, "b", "user_confirmation", ""); !errors.Is(err, errDeviceSelectionConflict) {
				t.Fatalf("replay accepted: %v", err)
			}
			second, err := r.confirmDeviceSelection(7, session, "b", "user_confirmation", first.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := r.lookupGrant(grant.GrantID); ok {
				t.Fatal("A grant survived switch to B")
			}
			r.refreshDeviceSelection(7, session, first)
			r.rememberDeviceSelection(7, session, "a", "stale_turn")
			current, _ := r.selectionRecord(7, session)
			if current.DeviceID != "b" || current.Revision != second.Revision {
				t.Fatal("stale renewal reverted selection")
			}
			third, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", second.Revision)
			if err != nil || third.Revision == first.Revision {
				t.Fatal("switch back did not rotate revision")
			}
			if _, ok := r.lookupGrant(grant.GrantID); ok {
				t.Fatal("A-B-A resurrected old grant")
			}
			var successes atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					if _, err := r.confirmDeviceSelection(7, session, fmt.Sprint(i), "user_confirmation", third.Revision); err == nil {
						successes.Add(1)
					}
				}(i)
			}
			wg.Wait()
			if successes.Load() != 1 {
				t.Fatalf("concurrent CAS winners = %d", successes.Load())
			}
			for _, scope := range []struct {
				actor   int64
				session string
			}{{8, session}, {7, buildCatsCoSessionKey("grp_1", "group", "usr43", 7)}, {7, buildCatsCoSessionKey("grp_2", "group", "usr42", 7)}} {
				other, err := r.selectionRecord(scope.actor, scope.session)
				if err != nil || other.DeviceID != "" {
					t.Fatal("selection crossed actor/bot/topic")
				}
			}
		})
	}
}

func TestDeviceSelectionCanRemainUnspecified(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	for _, backend := range []string{"local", "shared-memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			r := newUserDeviceRegistry(time.Hour)
			if backend == "shared-memory" {
				r.withSharedRuntime(newSharedMemoryRuntimeState())
			}
			if backend == "redis" {
				url, cleanup := newRedisRuntimeTestServer(t)
				defer cleanup()
				state := newRedisRuntimeStateForTest(t, url, t.Name())
				defer state.Close()
				r.withSharedRuntime(state)
			}
			session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
			pinned, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", "", 7)
			if err != nil {
				t.Fatal(err)
			}
			device := UserDevice{OwnerUID: 7, OwnerUserID: "usr7", DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
			before := r.turnContextForOwnerDevicesWithExplicitTargets(7, 7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{device}, nil, true)
			if len(before.Grants) != 1 || len(before.ExplicitGrants) != 1 {
				t.Fatal("missing initial grants")
			}
			cleared, err := r.confirmDeviceSelection(7, session, "", deviceSelectionUnspecifiedSource, pinned.Revision, 7)
			if err != nil || !cleared.unspecified() || cleared.Revision == pinned.Revision {
				t.Fatal("clear did not rotate version", err)
			}
			for _, g := range append(before.Grants, before.ExplicitGrants...) {
				if _, ok := r.lookupGrant(g.GrantID); ok {
					t.Fatal("old grant survived clear")
				}
			}
			r.rememberDeviceSelection(7, session, "a", "single_active_device", 7)
			after := r.turnContextForOwnerDevicesWithExplicitTargets(7, 7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{device}, nil, true)
			if after.Selection.SelectedDevice != nil || after.Selection.SelectionSource != deviceSelectionUnspecifiedSource || len(after.Grants) != 0 || len(after.ExplicitGrants) != 1 {
				t.Fatal("clear was replaced by a single-device pin, or lost exact-device authorization")
			}
			if _, ok := r.lookupGrant(after.ExplicitGrants[0].GrantID); !ok {
				t.Fatal("new exact-device grant rejected")
			}
			if _, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", pinned.Revision, 7); !errors.Is(err, errDeviceSelectionConflict) {
				t.Fatal("pre-clear request restored old pin")
			}
		})
	}
}

func TestSingleOnlineDeviceDoesNotBecomeDefault(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	for _, topic := range []struct{ id, kind string }{{"p2p_7_42", "p2p"}, {"grp_1", "group"}} {
		t.Run(topic.kind, func(t *testing.T) {
			r := newUserDeviceRegistry(time.Hour).withSelectionStore(&durableSelectionTestStore{})
			device := UserDevice{OwnerUID: 7, OwnerUserID: "usr7", DeviceID: "a", Routable: true,
				Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
			devices := []UserDevice{device}
			assertUnspecified := func(ctx DeviceTurnContext) {
				t.Helper()
				if ctx.Selection == nil || ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil ||
					len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 1 {
					t.Fatalf("one online device must be available only as an explicit target: %#v", ctx)
				}
				if _, ok := r.lookupGrant(ctx.ExplicitGrants[0].GrantID); !ok {
					t.Fatal("explicit grant rejected without a default")
				}
			}
			turn := func() DeviceTurnContext {
				return r.turnContextForOwnerDevicesWithExplicitTargets(7, 7, topic.id, topic.kind, 42, "cloud", devices, nil, true)
			}
			first := turn()
			assertUnspecified(first)
			old, err := r.confirmDeviceSelection(7, first.Selection.SessionKey, "a", deviceSelectionAutomaticSource, first.Selection.Revision, 7)
			if err != nil {
				t.Fatal(err)
			}
			if r.grantSelectionCurrent(ScopedDeviceGrant{
				ActorUserID: "usr7", SessionKey: first.Selection.SessionKey, DeviceID: "a", SelectionRevision: old.Revision,
			}) {
				t.Fatal("old default grant still matched the automatic pick")
			}
			afterUpgrade := turn()
			assertUnspecified(afterUpgrade)
			if afterUpgrade.Selection.Revision != old.Revision {
				t.Fatal("ignoring an old automatic pick must preserve the CAS revision")
			}
		})
	}
}

func TestConversationDeviceSelectionClearAPI(t *testing.T) {
	_, handler := deviceSelectionFixture(t)
	snapshot := selectionSnapshot(t, handler, 7, "p2p_7_42", "usr42")
	pin := fmt.Sprintf(`{"device_id":"a","expected_revision":%q}`, snapshot["revision"])
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, pin); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	snapshot = selectionSnapshot(t, handler, 7, "p2p_7_42", "usr42")
	missing := fmt.Sprintf(`{"expected_revision":%q}`, snapshot["revision"])
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, missing); rec.Code != 400 {
		t.Fatal("missing target must not clear")
	}
	clear := fmt.Sprintf(`{"device_id":"","expected_revision":%q}`, snapshot["revision"])
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, clear); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	for i := 0; i < 2; i++ {
		after := selectionSnapshot(t, handler, 7, "p2p_7_42", "usr42")
		if after["status"] != "unspecified" || after["selected_device_id"] != "" || after["selected_owner_uid"] != float64(0) || after["revision"] == snapshot["revision"] {
			t.Fatal("clear not retained", after)
		}
	}
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, clear); rec.Code != 409 {
		t.Fatal("stale clear accepted")
	}
	for _, actor := range []int64{0, 8, 42} {
		if rec := selectionRequest(t, handler, actor, "p2p_7_42", "usr42", http.MethodPost, clear); rec.Code < 400 {
			t.Fatal("unauthorized clear accepted")
		}
	}
}

func deviceSelectionFixture(t *testing.T) (*Hub, *DeviceHandler) {
	t.Helper()
	db := &identityMessageStore{users: map[int64]*types.User{
		7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
		8:  {ID: 8, Username: "bob", AccountType: types.AccountHuman},
		42: {ID: 42, Username: "cloud", AccountType: types.AccountBot},
		43: {ID: 43, Username: "other-bot", AccountType: types.AccountBot},
	}, friendPairs: map[string]bool{agentPairKey(7, 42): true, agentPairKey(8, 42): true}, groupMembers: []*types.GroupMember{
		{GroupID: 1, UserID: 7}, {GroupID: 1, UserID: 8}, {GroupID: 1, UserID: 42}, {GroupID: 1, UserID: 43},
	}}
	hub := NewHub(db, nil)
	for _, item := range []struct {
		owner    int64
		id, role string
		online   bool
	}{{7, "a", "desktop", true}, {7, "b", "desktop", true}, {7, "offline", "desktop", false}, {7, "server", "server", true}, {8, "private", "desktop", true}} {
		device, err := hub.userDevices.register(item.owner, RegisterUserDeviceRequest{DeviceID: item.id, DisplayName: "Same name", RuntimeRole: item.role, Status: "online", Capabilities: []string{"read_file"}})
		if err != nil {
			t.Fatal(err)
		}
		if item.online {
			client := &Client{uid: item.owner, accountType: types.AccountHuman, send: make(chan []byte, 8)}
			hub.addClient(client)
			hub.bindDeviceClient(item.owner, device, client)
		}
	}
	return hub, NewDeviceHandler(db, hub)
}

func selectionRequest(t *testing.T, handler *DeviceHandler, actor int64, topic, bot, method, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/conversations/device-selection?topic_id="+topic+"&agent_id="+bot, bytes.NewBufferString(payload))
	if actor > 0 {
		token, err := GenerateToken(actor, "test", "")
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	OwnerMiddlewareWithDB(handler.db)(handler.HandleConversationSelection)(rec, req)
	return rec
}

func selectionSnapshot(t *testing.T, handler *DeviceHandler, actor int64, topic, bot string) map[string]interface{} {
	t.Helper()
	rec := selectionRequest(t, handler, actor, topic, bot, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConversationDeviceSelectionAPI(t *testing.T) {
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "required")
	hub, handler := deviceSelectionFixture(t)
	requester := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud-body", send: make(chan []byte, 8)}
	hub.addClient(requester)
	snapshot := selectionSnapshot(t, handler, 7, "p2p_7_42", "usr42")
	if snapshot["status"] != "unspecified" || len(snapshot["devices"].([]interface{})) != 3 {
		t.Fatalf("directory: %#v", snapshot)
	}
	revision := snapshot["revision"].(string)
	if revision == "" {
		t.Fatal("empty first-confirmation revision")
	}
	payload := fmt.Sprintf(`{"device_id":"a","expected_revision":%q}`, revision)
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, payload); rec.Code != 200 {
		t.Fatalf("confirm: %s", rec.Body.String())
	}
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, payload); rec.Code != 409 {
		t.Fatal("replayed confirmation accepted")
	}
	ctx := hub.buildCatscoIdentityMetadata(7, 42, "p2p_7_42", 1, "读取我的文件")
	grants, ok := ctx["device_grants"].([]ScopedDeviceGrant)
	if !ok || len(grants) != 1 || grants[0].SelectionRevision == "" {
		t.Fatalf("confirmed selection did not reach message grants: %#v", ctx)
	}
	grant := deviceGrantForID(t, grants, "a")
	request := &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: "confirmed-selection", GrantID: grant.GrantID,
		ActorUserID: grant.ActorUserID, SessionKey: grant.SessionKey, TargetOwnerUserID: "usr7", TargetDeviceID: "a", ToolName: "read_file"}
	if err := hub.authorizeOrdinaryThinToolRPC(requester, request, 7, "a", "read_file"); err != nil {
		t.Fatal(err)
	}
	snapshot = selectionSnapshot(t, handler, 7, "p2p_7_42", "usr42")
	if snapshot["selected_device_id"] != "a" {
		t.Fatal("confirmed target not persisted")
	}
	for _, tc := range []struct {
		name, device string
		code         int
	}{{"other account", "private", 403}, {"server", "server", 403}, {"offline", "offline", 409}} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"device_id":%q,"expected_revision":%q}`, tc.device, snapshot["revision"])
			if rec := selectionRequest(t, handler, 7, "p2p_7_42", "usr42", http.MethodPost, payload); rec.Code != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		name       string
		actor      int64
		topic, bot string
	}{{"anonymous", 0, "p2p_7_42", "42"}, {"bot JWT", 42, "p2p_7_42", "42"}, {"other participant", 8, "p2p_7_42", "42"}, {"unrelated bot", 7, "p2p_7_42", "43"}, {"not group member", 7, "grp_2", "42"}} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := selectionRequest(t, handler, tc.actor, tc.topic, tc.bot, http.MethodGet, ""); rec.Code < 400 {
				t.Fatal("unauthorized snapshot accepted")
			}
		})
	}
	for _, payload := range []string{`{"device_id":"b"}`, `{"device_id":"b","expected_revision":""}`, `{"device_id":"b","expected_revision":"x","actor_user_id":8}`, `{} {}`} {
		if rec := selectionRequest(t, handler, 7, "p2p_7_42", "42", http.MethodPost, payload); rec.Code != 400 {
			t.Fatalf("malformed: %d", rec.Code)
		}
	}
	group := selectionSnapshot(t, handler, 7, "grp_1", "")
	if len(group["agents"].([]interface{})) != 2 {
		t.Fatal("group bot chooser missing")
	}
	other := selectionSnapshot(t, handler, 8, "grp_1", "42")
	otherDevices := other["devices"].([]interface{})
	if len(otherDevices) != 1 || otherDevices[0].(map[string]interface{})["deviceId"] != "private" {
		t.Fatal("group actor directory not isolated")
	}
	if other["status"] != "unspecified" || other["selected_device_id"] != "" {
		t.Fatal("the only group device was implicitly selected")
	}
	switchPayload := fmt.Sprintf(`{"device_id":"b","expected_revision":%q}`, snapshot["revision"])
	if rec := selectionRequest(t, handler, 7, "p2p_7_42", "42", http.MethodPost, switchPayload); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if err := hub.authorizeOrdinaryThinToolRPC(requester, request, 7, "a", "read_file"); err == nil {
		t.Fatal("ordinary RPC accepted pre-switch grant")
	}
	ctx = hub.buildCatscoIdentityMetadata(7, 42, "p2p_7_42", 2, "读取我的文件")
	grants, ok = ctx["device_grants"].([]ScopedDeviceGrant)
	if !ok || len(grants) != 1 {
		t.Fatal("new turn did not select B")
	}
	request.GrantID, request.TargetDeviceID = deviceGrantForID(t, grants, "b").GrantID, "b"
	if err := hub.authorizeOrdinaryThinToolRPC(requester, request, 7, "b", "read_file"); err != nil {
		t.Fatal(err)
	}

	// A device credential cannot become a user confirmation credential.
	deviceToken, err := GenerateDeviceConnectorToken(DeviceConnectorTokenInput{UID: 7, DeviceID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/device-selection?topic_id=p2p_7_42&agent_id=42", nil)
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	rec := httptest.NewRecorder()
	handler.HandleConversationSelection(rec, req)
	if rec.Code < 400 {
		t.Fatal("device credential accepted as human confirmation")
	}
}
