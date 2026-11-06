package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type durableSelectionTestStore struct {
	store.Store
	mu      sync.Mutex
	records map[string]store.ConversationDeviceSelection
	fail    bool
}

func (s *durableSelectionTestStore) GetConversationDeviceSelection(_ context.Context, actor int64, session string) (*store.ConversationDeviceSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return nil, errors.New("database unavailable")
	}
	record, ok := s.records[fmt.Sprintf("%d/%s", actor, session)]
	if !ok {
		return nil, nil
	}
	return &record, nil
}

func (s *durableSelectionTestStore) CompareConversationDeviceSelection(_ context.Context, actor int64, session, expected string, next store.ConversationDeviceSelection) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, errors.New("database unavailable")
	}
	if s.records == nil {
		s.records = make(map[string]store.ConversationDeviceSelection)
	}
	key := fmt.Sprintf("%d/%s", actor, session)
	if s.records[key].Revision != expected {
		return false, nil
	}
	s.records[key] = next
	return true, nil
}

func TestDurableSelectionSurvivesRegistryAndRuntimeRestart(t *testing.T) {
	db := &durableSelectionTestStore{}
	before := newUserDeviceRegistry(time.Minute).withSelectionStore(db).withSharedRuntime(newSharedMemoryRuntimeState())
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	first, err := before.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
	if err != nil {
		t.Fatal(err)
	}
	a := UserDevice{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	b := UserDevice{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	old := before.grantsForDevices(7, "grp_1", "group", 42, "cloud", []UserDevice{a})[0]

	// New server process and empty Redis/runtime state: only B reconnects first.
	after := newUserDeviceRegistry(time.Minute).withSelectionStore(db).withSharedRuntime(newSharedMemoryRuntimeState())
	after.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	selection, target := after.selectDeviceForTurn(7, "grp_1", "group", 42, []UserDevice{b})
	if target != nil || selection.Status != DeviceSelectionUnavailable || selection.SelectedDevice.DeviceID != "a" {
		t.Fatalf("restart silently chose B: %#v", selection)
	}
	selection, target = after.selectDeviceForTurn(7, "grp_1", "group", 42, []UserDevice{b, a})
	if target == nil || target.DeviceID != "a" || selection.Revision != first.Revision {
		t.Fatal("durable preference expired or reordered")
	}
	if _, ok := after.lookupGrant(old.GrantID); ok {
		t.Fatal("old ephemeral grant survived restart")
	}
	newGrants := after.grantsForDevices(7, "grp_1", "group", 42, "cloud", []UserDevice{a})
	if len(newGrants) != 1 || !newGrants[0].Permanent || newGrants[0].ExpiresAt != permanentDeviceGrantExpiresAt {
		t.Fatal("durable selection did not issue a permanent device authorization")
	}
	if _, err := after.confirmDeviceSelection(7, session, "b", "user_confirmation", first.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := after.lookupGrant(newGrants[0].GrantID); ok {
		t.Fatal("durable switch did not invalidate old grant")
	}
}

func TestDurableSelectionDatabaseFailureNeverFallsBack(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	db := &durableSelectionTestStore{}
	r := newUserDeviceRegistry(time.Minute).withSelectionStore(db)
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	_, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
	if err != nil {
		t.Fatal(err)
	}
	r.preferences[7] = map[string]deviceSelectionPreference{session: {DeviceID: "b", Revision: "unsafe-runtime", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}
	db.fail = true
	ctx := r.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}}, nil)
	if len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 0 || ctx.Selection.Status != DeviceSelectionUnavailable || ctx.Selection.SelectionSource != "selection_store_unavailable" {
		t.Fatal("database failure used runtime fallback")
	}
	if _, err := r.confirmDeviceSelection(7, session, "b", "user_confirmation", ""); err == nil {
		t.Fatal("database write failure ignored")
	}
}

func TestDurableSelectionMigratesExpiredRuntimeTargetNotNewestDevice(t *testing.T) {
	db := &durableSelectionTestStore{}
	r := newUserDeviceRegistry(time.Minute)
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	old, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return time.Now().Add(time.Hour) }
	r.withSelectionStore(db)
	migrated, err := r.selectionRecord(7, session)
	if err != nil || !migrated.Durable || migrated.DeviceID != "a" || migrated.Revision == old.Revision {
		t.Fatalf("invalid migration: %#v %v", migrated, err)
	}
	otherNode := newUserDeviceRegistry(time.Minute).withSelectionStore(db)
	if _, err := otherNode.confirmDeviceSelection(7, session, "b", "user_confirmation", migrated.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := r.selectionRecord(7, session)
	if err != nil || current.DeviceID != "b" {
		t.Fatal("legacy cache overwrote another server's confirmation")
	}
}
