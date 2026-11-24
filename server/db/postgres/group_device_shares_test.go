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
	migration, err := os.ReadFile("../migrations/postgres/000082_group_device_sharing.up.sql")
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
		t.Fatal("stale approval revived revoked share", err)
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
	request := store.GroupDeviceShareRequest{ID: "request-a", RequesterUID: actors[0], OwnerUID: actors[1], TopicID: topic, ClientRequestID: "request-1", Description: "read the selected PC", CreatedAt: time.Now().UnixMilli()}
	pending, err := db.CreateGroupDeviceShareRequest(ctx, request)
	if err != nil || pending.Status != "pending" {
		t.Fatal(pending, err)
	}
	share.ID = "ignored-on-upsert"
	resolved, err := db.ResolveGroupDeviceShareRequest(ctx, pending.ID, 1, "approved", &share)
	if err != nil || resolved.Status != "approved" {
		t.Fatal(resolved, err)
	}
	repeated, err := db.ResolveGroupDeviceShareRequest(ctx, pending.ID, 1, "approved", &share)
	if err != nil || repeated.Revision != resolved.Revision {
		t.Fatal("approval retry", repeated, err)
	}
	if _, err = db.ResolveGroupDeviceShareRequest(ctx, pending.ID, 1, "rejected", nil); !errors.Is(err, store.ErrDeviceShareConflict) {
		t.Fatal("stale rejection", err)
	}
	t.Run("concurrent approval and revocation", func(t *testing.T) {
		for i := 0; i < 8; i++ {
			current, err := db.GetGroupDeviceShare(ctx, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			active := share
			active.Status = "active"
			current, err = db.SaveGroupDeviceShare(ctx, active, current.Revision)
			if err != nil {
				t.Fatal(err)
			}
			request.ID = fmt.Sprintf("race-request-%d", i)
			request.ClientRequestID = request.ID
			r, err := db.CreateGroupDeviceShareRequest(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ready := make(chan struct{})
			done := make(chan error, 2)
			go func() {
				<-ready
				_, e := db.ResolveGroupDeviceShareRequest(raceCtx, r.ID, r.Revision, "approved", &active)
				done <- e
			}()
			revocation := *current
			revocation.Status = "revoked"
			go func() { <-ready; _, e := db.SaveGroupDeviceShare(raceCtx, revocation, current.Revision); done <- e }()
			close(ready)
			for n := 0; n < 2; n++ {
				if e := <-done; e != nil && !errors.Is(e, store.ErrDeviceShareConflict) {
					cancel()
					t.Fatal("concurrent transition failed", e)
				}
			}
			cancel()
			after, e := db.GetGroupDeviceShareRequest(ctx, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			permission, e := db.GetGroupDeviceShare(ctx, saved.ID)
			if e != nil {
				t.Fatal(e)
			}
			if permission.Status == "revoked" && after.Status == "approved" {
				t.Fatal("revoked share retained actionable approval")
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
	unlinkRequests := []struct {
		id, device, want string
		owner            int64
	}{
		{"unselected-request", "", "pending", actors[1]},
		{"other-device-request", "other-device", "pending", actors[1]},
		{"unlinked-device-request", "same-device-id", "invalidated", actors[1]},
		{"other-owner-request", "same-device-id", "pending", actors[2]},
	}
	for _, tc := range unlinkRequests {
		_, err = db.CreateGroupDeviceShareRequest(ctx, store.GroupDeviceShareRequest{
			ID: tc.id, ClientRequestID: tc.id, RequesterUID: actors[0], OwnerUID: tc.owner,
			TopicID: topic, DeviceID: tc.device, CreatedAt: time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.InvalidateDeviceShares(ctx, actors[1], "same-device-id"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range unlinkRequests {
		after, err := db.GetGroupDeviceShareRequest(ctx, tc.id)
		if err != nil || after == nil || after.Status != tc.want {
			t.Fatalf("unlink request %s: %+v, %v; want %s", tc.id, after, err, tc.want)
		}
	}
	if err = db.RegisterDeviceShareBinding(ctx, actors[1], "other-device", "other-install", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	otherPermission := share
	otherPermission.ID, otherPermission.DeviceID = "other-device-share", "other-device"
	otherPermission.DeviceEpoch, otherPermission.Status = 1, "active"
	if approved, err := db.ResolveGroupDeviceShareRequest(ctx, "unselected-request", 1, "approved", &otherPermission); err != nil || approved.DeviceID != "other-device" {
		t.Fatal("unselected request could not approve a different device after unlink", approved, err)
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
		t.Fatal("in-flight old-binding approval was accepted", err)
	}
	oldRequest, err := db.GetGroupDeviceShareRequest(ctx, pending.ID)
	if err != nil || oldRequest.Status != "invalidated" {
		t.Fatal("old approval card remained actionable", oldRequest, err)
	}

	t.Run("approval completes before concurrent group deletion", func(t *testing.T) {
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
			t.Fatalf("group deletion locked a permission before its members, blocking approval: %v", err)
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
			t.Fatal("group deletion stalled after approval committed")
		}
		after, err := db.GetGroupDeviceShare(ctx, permission.ID)
		if err != nil || after == nil || after.Status != "revoked" {
			t.Fatal("group deletion retained live permission", after, err)
		}
	})

}
