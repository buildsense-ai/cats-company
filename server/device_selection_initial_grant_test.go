package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// A new task can deliver its first message before the picker mounts or its
// GET completes. Reading the picker afterwards must not revoke that task.
func TestExplicitDeviceGrantBeforeFirstPickerRead(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	for _, backend := range []string{"memory", "durable", "authorization-only-redis"} {
		for _, topic := range []string{"p2p_7_42", "grp_1"} {
			t.Run(backend+"/"+topic, func(t *testing.T) {
				hub, handler := deviceSelectionFixture(t)
				if backend != "memory" {
					hub.userDevices.withSelectionStore(&durableSelectionTestStore{})
				}
				if backend == "authorization-only-redis" {
					mr := miniredis.RunT(t)
					hub.userDevices.authorization = authorizationStoreForTest(t, "redis://"+mr.Addr())
				}
				bot := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud", send: make(chan []byte, 8)}
				bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability})
				hub.addClient(bot)
				identity := hub.buildCatscoIdentityMetadata(7, 42, topic, 1, "read b")
				selection := identity["device_selection"].(*DeviceSelection)
				if selection.Status != DeviceSelectionNeedsSelection || selection.SelectedDevice != nil {
					t.Fatal("first message unexpectedly chose a default device")
				}
				if grants, _ := identity["device_grants"].([]ScopedDeviceGrant); len(grants) != 0 {
					t.Fatal("unselected conversation received default grants")
				}
				envelope := identity["device_target_authorizations"].(map[string]interface{})
				grant := deviceGrantForID(t, envelope["grants"].([]ScopedDeviceGrant), "b")
				req := &MsgThinToolRPC{GrantID: grant.GrantID, TargetScope: "explicit_device", TargetDeviceID: "b",
					SessionKey: grant.SessionKey, ActorUserID: "usr7"}
				if err := hub.authorizeOrdinaryThinToolRPC(bot, req, 7, "b", "read_file"); err != nil {
					t.Fatalf("explicit target before any picker GET: %v", err)
				}
				for i := 0; i < 2; i++ {
					snapshot := selectionSnapshot(t, handler, 7, topic, "42")
					if err := hub.authorizeOrdinaryThinToolRPC(bot, req, 7, "b", "read_file"); err != nil {
						t.Fatalf("picker GET revoked first-message grant: %v", err)
					}
					if selection.Revision == "" || snapshot["revision"] != selection.Revision || snapshot["selected_device_id"] != "" {
						t.Fatal("first message and picker must share a stable unselected revision")
					}
				}
				// A real default change still revokes the old turn's authority.
				rec := selectionRequest(t, handler, 7, topic, "42", http.MethodPost,
					`{"device_id":"a","expected_revision":"`+selection.Revision+`"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("choose A: %d %s", rec.Code, rec.Body.String())
				}
				if err := hub.authorizeOrdinaryThinToolRPC(bot, req, 7, "b", "read_file"); err == nil {
					t.Fatal("default change left an old explicit grant usable")
				}
			})
		}
	}
}

func TestConcurrentFirstDeviceMessagesKeepEveryExplicitGrant(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	for _, backend := range []string{"memory", "shared", "redis", "durable", "authorization-only-redis"} {
		t.Run(backend, func(t *testing.T) {
			r := newUserDeviceRegistry(time.Hour)
			switch backend {
			case "shared":
				r.withSharedRuntime(newSharedMemoryRuntimeState())
			case "redis":
				mr := miniredis.RunT(t)
				state := newRedisRuntimeStateForTest(t, "redis://"+mr.Addr(), t.Name())
				t.Cleanup(func() { _ = state.Close() })
				r.withSharedRuntime(state)
			case "durable":
				r.withSelectionStore(&durableSelectionTestStore{})
			case "authorization-only-redis":
				r.withSelectionStore(&durableSelectionTestStore{})
				mr := miniredis.RunT(t)
				r.authorization = authorizationStoreForTest(t, "redis://"+mr.Addr())
			}
			devices := []UserDevice{
				{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
				{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
			}
			const requests = 8
			results := make(chan DeviceTurnContext, requests)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < requests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- r.turnContextForDevices(7, "grp_1", "group", 42, "cloud", devices, nil)
				}()
			}
			// Race a picker initialization against the incoming messages too.
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
				if _, err := r.ensureDeviceSelectionRevision(7, session); err != nil {
					t.Errorf("concurrent picker initialization: %v", err)
				}
			}()
			close(start)
			wg.Wait()
			close(results)
			session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
			current, err := r.ensureDeviceSelectionRevision(7, session)
			if err != nil || current.Revision == "" || current.DeviceID != "" {
				t.Fatalf("invalid unselected snapshot: %+v, %v", current, err)
			}
			for result := range results {
				if result.Selection.Revision != current.Revision || result.Selection.Status != DeviceSelectionNeedsSelection ||
					len(result.Grants) != 0 || len(result.ExplicitGrants) != 2 {
					t.Fatal("concurrent initialization changed the default or lost explicit authorization")
				}
				for _, grant := range result.ExplicitGrants {
					if _, ok := r.lookupGrant(grant.GrantID); !ok {
						t.Fatal("another initializer revoked an issued explicit grant")
					}
				}
			}
		})
	}
}

type firstSelectionConfirmationWinsStore struct {
	durableSelectionTestStore
	confirmed bool
}

func (s *firstSelectionConfirmationWinsStore) CompareConversationDeviceSelection(ctx context.Context, actor int64, session, expected string, next store.ConversationDeviceSelection) (bool, error) {
	if !s.confirmed {
		s.confirmed = true
		// Model a user confirmation committed by another registry after the
		// initializer read the empty slot but before its compare-and-swap.
		winner := store.ConversationDeviceSelection{DeviceID: "b", Revision: "user-confirmed", Source: "user_confirmation"}
		if _, err := s.durableSelectionTestStore.CompareConversationDeviceSelection(ctx, actor, session, "", winner); err != nil {
			return false, err
		}
	}
	return s.durableSelectionTestStore.CompareConversationDeviceSelection(ctx, actor, session, expected, next)
}

func TestFirstDeviceRevisionPreservesConcurrentUserConfirmation(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	r := newUserDeviceRegistry(time.Hour).withSelectionStore(&firstSelectionConfirmationWinsStore{})
	devices := []UserDevice{
		{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
		{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
	}
	result := r.turnContextForDevices(7, "grp_1", "group", 42, "cloud", devices, nil)
	if result.Selection.Status != DeviceSelectionSelected || result.Selection.SelectedDevice.DeviceID != "b" ||
		result.Selection.Revision != "user-confirmed" || len(result.Grants) != 1 || result.Grants[0].DeviceID != "b" || len(result.ExplicitGrants) != 2 {
		t.Fatal("initialization overwrote or failed to reflect the user's concurrent confirmation")
	}
	for _, grant := range append(result.Grants, result.ExplicitGrants...) {
		if grant.SelectionRevision != "user-confirmed" {
			t.Fatal("concurrent confirmation and issued authority have different revisions")
		}
		if _, ok := r.lookupGrant(grant.GrantID); !ok {
			t.Fatal("concurrent confirmation's new grant was revoked by initialization")
		}
	}
}

type firstSelectionWriteFailureStore struct{ durableSelectionTestStore }

func (s *firstSelectionWriteFailureStore) CompareConversationDeviceSelection(context.Context, int64, string, string, store.ConversationDeviceSelection) (bool, error) {
	return false, errors.New("database write unavailable")
}

func TestFirstDeviceRevisionWriteFailureDeniesExplicitGrants(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	r := newUserDeviceRegistry(time.Hour).withSelectionStore(&firstSelectionWriteFailureStore{})
	devices := []UserDevice{
		{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
		{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
	}
	result := r.turnContextForDevices(7, "grp_1", "group", 42, "cloud", devices, nil)
	if len(result.Grants) != 0 || len(result.ExplicitGrants) != 0 || result.Selection.Status != DeviceSelectionUnavailable ||
		result.Selection.SelectionSource != "selection_store_unavailable" {
		t.Fatal("failed revision initialization issued unversioned authority or chose a default")
	}
}
