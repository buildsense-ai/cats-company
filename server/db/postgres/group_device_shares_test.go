package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestGroupDeviceSharingPostgres(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for disposable-schema PostgreSQL acceptance")
	}
	root := &Adapter{}
	if err := root.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := fmt.Sprintf("sharing_test_%d", time.Now().UnixNano())
	if _, err := root.db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer root.db.Exec("DROP SCHEMA " + schema + " CASCADE")
	endpoint, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	db := &Adapter{}
	if err = db.Open(endpoint.String()); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var currentSchema string
	if err = db.db.QueryRow("SELECT current_schema()").Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatal("test schema isolation failed", currentSchema, err)
	}

	for _, ddl := range []string{createUsersTable, createTopicsTable, createGroupsTable, createGroupMembersTable, createConversationDeviceSelectionsTable} {
		if _, err = db.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate the pre-sharing schema with a retained owner-less selection.
	var legacyActor int64
	if err = db.db.QueryRow("INSERT INTO users(username,pass_hash) VALUES('legacy-user',$1) RETURNING id", []byte("fixture")).Scan(&legacyActor); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec("INSERT INTO conversation_device_selections VALUES($1,'before-upgrade','old-device','old-revision','legacy',1)", legacyActor); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../migrations/postgres/000084_group_device_sharing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{string(migration), string(migration), createGroupDeviceShares, upgradeConversationDeviceSelectionOwner} {
		if _, err = db.db.Exec(ddl); err != nil {
			t.Fatal("upgrade/repeated startup failed", err)
		}
	}
	ctx := context.Background()
	beforeUpgrade, err := db.GetConversationDeviceSelection(ctx, legacyActor, "before-upgrade")
	if err != nil || beforeUpgrade == nil || beforeUpgrade.OwnerUID != 0 || beforeUpgrade.DeviceID != "old-device" {
		t.Fatal("upgrade changed old selection", beforeUpgrade, err)
	}
	actors := []int64{}
	for _, name := range []string{"requester", "owner", "other-owner"} {
		var id int64
		if err = db.db.QueryRow("INSERT INTO users(username,pass_hash) VALUES($1,$2) RETURNING id", name, []byte("local-fixture")).Scan(&id); err != nil {
			t.Fatal(err)
		}
		actors = append(actors, id)
	}
	var group int64
	if err = db.db.QueryRow(`INSERT INTO groups(name,owner_id) VALUES('sharing',$1) RETURNING id`, actors[0]).Scan(&group); err != nil {
		t.Fatal(err)
	}
	for _, id := range actors {
		if err = db.AddGroupMember(group, id, "member"); err != nil {
			t.Fatal(err)
		}
	}
	topic := fmt.Sprintf("grp_%d", group)
	for _, owner := range actors[1:] {
		if err = db.RegisterDeviceShareBinding(ctx, owner, "same-device-id", "same-device-id", time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	share := store.GroupDeviceShare{DeviceEpoch: 1, ID: "share-a", OwnerUID: actors[1], GranteeUID: actors[0], TopicID: topic, DeviceID: "same-device-id", Operations: []string{"read_file"}, Status: "active", UpdatedAt: time.Now().UnixMilli()}
	saved, err := db.SaveGroupDeviceShare(ctx, share, 0)
	if err != nil || saved.Revision != 1 {
		t.Fatal(saved, err)
	}
	if err = db.UpdateGroupProfile(group, "renamed sharing group", ""); err != nil {
		t.Fatal(err)
	}
	renamed, err := db.GetGroupDeviceShare(ctx, saved.ID)
	if err != nil || renamed.Status != "active" || renamed.Revision != 1 {
		t.Fatal("group profile edit invalidated permission", renamed, err)
	}
	again, err := db.SaveGroupDeviceShare(ctx, share, 0)
	if err != nil || again.ID != saved.ID || again.Revision != 1 {
		t.Fatal("idempotent share", again, err)
	}
	other := share
	other.ID = "share-b"
	other.OwnerUID = actors[2]
	if _, err = db.SaveGroupDeviceShare(ctx, other, 0); err != nil {
		t.Fatal("device ID must be owner-scoped", err)
	}
	share.Status = "revoked"
	revoked, err := db.SaveGroupDeviceShare(ctx, share, 1)
	if err != nil || revoked.Revision != 2 {
		t.Fatal(revoked, err)
	}
	share.Status = "active"
	if _, err = db.SaveGroupDeviceShare(ctx, share, 0); !errors.Is(err, store.ErrDeviceShareConflict) {
		t.Fatal("stale authorization revived revoked share", err)
	}
	selection := store.ConversationDeviceSelection{DeviceID: "same-device-id", Revision: "v1", Source: "test", UpdatedAt: time.Now().UnixMilli()}
	if won, err := db.CompareConversationDeviceSelection(ctx, actors[0], "legacy-session", "", selection); err != nil || !won {
		t.Fatal(won, err)
	}
	legacy, err := db.GetConversationDeviceSelection(ctx, actors[0], "legacy-session")
	if err != nil || legacy.OwnerUID != 0 {
		t.Fatal("legacy selection compatibility", legacy, err)
	}
	selection.OwnerUID = actors[1]
	selection.Revision = "v2"
	if won, err := db.CompareConversationDeviceSelection(ctx, actors[0], "legacy-session", "v1", selection); err != nil || !won {
		t.Fatal(won, err)
	}
	selected, err := db.GetConversationDeviceSelection(ctx, actors[0], "legacy-session")
	if err != nil || selected.OwnerUID != actors[1] {
		t.Fatal("shared owner lost", selected, err)
	}
	t.Run("concurrent authorization and revocation use the same revision", func(t *testing.T) {
		for i := 0; i < 8; i++ {
			current, err := db.GetGroupDeviceShare(ctx, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			current, err = db.SaveGroupDeviceShare(ctx, share, current.Revision)
			if err != nil {
				t.Fatal(err)
			}
			updated, revoked := *current, *current
			updated.Operations = []string{"read_file", "write_file"}
			revoked.Status = "revoked"
			raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ready := make(chan struct{})
			done := make(chan error, 2)
			for _, change := range []store.GroupDeviceShare{updated, revoked} {
				go func() {
					<-ready
					_, err := db.SaveGroupDeviceShare(raceCtx, change, current.Revision)
					done <- err
				}()
			}
			close(ready)
			first, second := <-done, <-done
			cancel()
			if !(first == nil && errors.Is(second, store.ErrDeviceShareConflict) ||
				second == nil && errors.Is(first, store.ErrDeviceShareConflict)) {
				t.Fatalf("expected exactly one permission update, got %v, %v", first, second)
			}
			after, err := db.GetGroupDeviceShare(ctx, saved.ID)
			if err != nil || after == nil || after.Revision != current.Revision+1 {
				t.Fatal("concurrent updates did not advance revision once", after, err)
			}
		}
	})
	if err = db.RemoveGroupMember(group, actors[0]); err != nil {
		t.Fatal(err)
	}
	afterLeave, err := db.GetGroupDeviceShare(ctx, saved.ID)
	if err != nil || afterLeave.Status != "revoked" {
		t.Fatal("leave did not revoke", afterLeave, err)
	}
	if err = db.AddGroupMember(group, actors[0], "member"); err != nil {
		t.Fatal(err)
	}
	afterRejoin, err := db.GetGroupDeviceShare(ctx, saved.ID)
	if err != nil || afterRejoin.Status != "revoked" {
		t.Fatal("rejoin revived sharing", afterRejoin, err)
	}
	historicalRequests := []struct {
		id, device, status, want string
		owner                    int64
	}{
		{"unselected-request", "", "pending", "pending", actors[1]},
		{"other-device-request", "other-device", "pending", "pending", actors[1]},
		{"unlinked-device-request", "same-device-id", "pending", "invalidated", actors[1]},
		{"other-owner-request", "same-device-id", "pending", "pending", actors[2]},
		{"previously-approved-request", "same-device-id", "approved", "invalidated", actors[1]},
	}
	for _, tc := range historicalRequests {
		// Seed records written by older builds so unlink still exercises their cleanup.
		_, err = db.db.ExecContext(ctx, `INSERT INTO group_device_share_requests
			(id, requester_uid, owner_uid, topic_id, device_id, client_request_id, status, revision, created_at_ms, updated_at_ms)
			VALUES ($1,$2,$3,$4,$5,$1,$6,1,$7,$7)`, tc.id, actors[0], tc.owner, topic, tc.device, tc.status, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.InvalidateDeviceShares(ctx, actors[1], "same-device-id"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range historicalRequests {
		var status string
		err := db.db.QueryRowContext(ctx, "SELECT status FROM group_device_share_requests WHERE id=$1", tc.id).Scan(&status)
		if err != nil || status != tc.want {
			t.Fatalf("historical request %s after unlink: %q, %v; want %q", tc.id, status, err, tc.want)
		}
	}
	binding, err := db.GetDeviceShareBinding(ctx, actors[1], "same-device-id")
	if err != nil || binding.Active {
		t.Fatal("unlink did not persist binding revocation", binding, err)
	}
	if err = db.RegisterDeviceShareBinding(ctx, actors[1], "same-device-id", "same-device-id", 1); err == nil {
		t.Fatal("old signed binding resurrected after unlink")
	}
	if err = db.RegisterDeviceShareBinding(ctx, actors[1], "same-device-id", "same-device-id", time.Now().Add(time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	rebound, err := db.GetDeviceShareBinding(ctx, actors[1], "same-device-id")
	if err != nil || !rebound.Active || rebound.Epoch <= 1 {
		t.Fatal("new binding epoch missing", rebound, err)
	}
	current, err := db.GetGroupDeviceShare(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	share.Status = "active"
	share.DeviceEpoch = 1
	if _, err = db.SaveGroupDeviceShare(ctx, share, current.Revision); !errors.Is(err, store.ErrDeviceShareConflict) {
		t.Fatal("in-flight old-binding authorization was accepted", err)
	}
	t.Run("authorization completes before concurrent group deletion", func(t *testing.T) {
		if _, err := db.db.Exec(createMessagesTable); err != nil {
			t.Fatal(err)
		}
		var deletingGroup int64
		if err := db.db.QueryRow(`INSERT INTO groups(name,owner_id) VALUES('delete race',$1) RETURNING id`, actors[0]).Scan(&deletingGroup); err != nil {
			t.Fatal(err)
		}
		for _, uid := range actors[:2] {
			if err := db.AddGroupMember(deletingGroup, uid, "member"); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.RegisterDeviceShareBinding(ctx, actors[1], "delete-race-device", "delete-race-install", time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		permission := store.GroupDeviceShare{DeviceEpoch: 1, ID: "delete-race-share", OwnerUID: actors[1], GranteeUID: actors[0], TopicID: fmt.Sprintf("grp_%d", deletingGroup), DeviceID: "delete-race-device", Operations: []string{"read_file"}, Status: "active", UpdatedAt: time.Now().UnixMilli()}
		if _, err := db.SaveGroupDeviceShare(ctx, permission, 0); err != nil {
			t.Fatal(err)
		}
		approval, err := db.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer approval.Rollback()
		if err := lockDeviceShareMembers(ctx, approval, permission.TopicID, permission.OwnerUID, permission.GranteeUID); err != nil {
			t.Fatal(err)
		}
		deleteURL, err := url.Parse(endpoint.String())
		if err != nil {
			t.Fatal(err)
		}
		application := fmt.Sprintf("sharing-delete-%d", time.Now().UnixNano())
		options := deleteURL.Query()
		options.Set("application_name", application)
		deleteURL.RawQuery = options.Encode()
		deleting := &Adapter{}
		if err := deleting.Open(deleteURL.String()); err != nil {
			t.Fatal(err)
		}
		defer deleting.Close()
		done := make(chan error, 1)
		go func() { done <- deleting.DeleteGroup(deletingGroup) }()
		finished := false
		defer func() {
			_ = approval.Rollback()
			if !finished {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("group deletion did not finish after rollback")
				}
			}
		}()
		// Inspect the database lock wait rather than depending on goroutine timing.
		blocked := false
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			var waiting int
			if err := db.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, application).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				blocked = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !blocked {
			t.Fatal("group deletion did not wait for in-flight membership locks")
		}
		writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		if _, err := saveDeviceShare(writeCtx, approval, permission, 1); err != nil {
			t.Fatalf("group deletion locked a permission before its members, blocking authorization: %v", err)
		}
		if err := approval.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			finished = true
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("group deletion stalled after authorization committed")
		}
		after, err := db.GetGroupDeviceShare(ctx, permission.ID)
		if err != nil || after == nil || after.Status != "revoked" {
			t.Fatal("group deletion retained live permission", after, err)
		}
	})

}
