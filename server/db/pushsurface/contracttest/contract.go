// Package contracttest verifies background surface persistence on real engines.
package contracttest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type Adapter interface {
	store.PushSubscriptionStore
	store.PushSubscriptionVersionStore
	store.ConversationTaskStatusStore
	store.NativeSurfaceStore
	CreateTopic(string, string, int64) error
	CreateUser(*types.User) (int64, error)
}

func Run(t *testing.T, db Adapter, cleanupUser func(int64), cleanupTopic func(string)) {
	t.Helper()
	ctx := context.Background()
	var users []int64
	for i := range 2 {
		uid, err := db.CreateUser(&types.User{Username: fmt.Sprintf("surface-%d-%d", time.Now().UnixNano(), i), AccountType: types.AccountHuman, PassHash: []byte("test")})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, uid)
		t.Cleanup(func() { cleanupUser(uid) })
	}
	row := &types.PushSubscription{UID: users[0], Endpoint: fmt.Sprintf("surface://%d/registration-current", users[0]), RegistrationID: "registration-current", P256DH: `{"s":"account-scope","e":123}`, Auth: "surface-v1:hash"}
	if stored, err := db.ReplacePushSubscription(ctx, row, nil, 10); err != nil || !stored {
		t.Fatalf("persist surface: %v %v", stored, err)
	}
	// A delayed initial create cannot replace the first committed version.
	stale := *row
	stale.Auth = "stale-hash"
	if ok, err := db.ReplacePushSubscription(ctx, &stale, nil, 10); err != nil || ok {
		t.Fatalf("conflicting insert: %v %v", ok, err)
	}
	if rows, err := db.ListPushSubscriptions(ctx, users[1]); err != nil || len(rows) != 0 {
		t.Fatalf("cross-account list: %v %v", rows, err)
	}
	if err := db.DeletePushSubscriptionVersion(ctx, users[1], row.Endpoint, row.RegistrationID, row.P256DH, row.Auth); err != nil {
		t.Fatal(err)
	}
	var claimed atomic.Int32
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ok, err := db.CompareAndSwapPushSubscriptionAuth(ctx, row.UID, row.Endpoint, row.RegistrationID, row.P256DH, row.Auth, "surface-v1:rotated")
			if err != nil {
				t.Error(err)
			}
			if ok {
				claimed.Add(1)
			}
		}()
	}
	workers.Wait()
	if claimed.Load() != 1 {
		t.Fatal("concurrent delivery claims", claimed.Load())
	}
	if err := db.DeletePushSubscriptionVersion(ctx, row.UID, row.Endpoint, row.RegistrationID, row.P256DH, row.Auth); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListPushSubscriptions(ctx, row.UID)
	if err != nil || len(rows) != 1 || rows[0].Auth != "surface-v1:rotated" {
		t.Fatalf("stale deletion removed rotation: %v %v", rows, err)
	}
	if ok, err := db.CompareAndSwapPushSubscriptionMetadata(ctx, row.UID, row.Endpoint, row.RegistrationID, row.P256DH, "new-metadata", "surface-v1:rotated"); err != nil || !ok {
		t.Fatalf("metadata CAS: %v %v", ok, err)
	}
	if err := db.DeletePushSubscriptionVersion(ctx, row.UID, row.Endpoint, row.RegistrationID, row.P256DH, "surface-v1:rotated"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListPushSubscriptions(ctx, row.UID)
	if err != nil || len(rows) != 1 || rows[0].P256DH != "new-metadata" || rows[0].Auth != "surface-v1:rotated" {
		t.Fatalf("metadata refresh lost watermark or stale delete won: %v %v", rows, err)
	}
	// An expired renewal is fenced by generation, metadata and hash too.
	replacement := *row
	replacement.Auth = "replacement-hash"
	if ok, err := db.ReplacePushSubscription(ctx, &replacement, row, 10); err != nil || ok {
		t.Fatalf("stale renewal overwrote rotated version: %v %v", ok, err)
	}
	row.P256DH = "new-metadata"
	// Fill the same account with browser and activity namespaces: all durable
	// transports consume the existing limit, and refreshing does not add rows.
	for i := range 9 {
		endpoint := fmt.Sprintf("https://example.test/%d/%d", row.UID, i)
		if i%2 == 0 {
			endpoint = fmt.Sprintf("activitykit://sandbox/bundle/%d-%d", row.UID, i)
		}
		if ok, err := db.UpsertPushSubscription(ctx, &types.PushSubscription{UID: row.UID, Endpoint: endpoint, RegistrationID: "other-registration", P256DH: "metadata", Auth: "auth"}, 10); err != nil || !ok {
			t.Fatalf("fill combined limit: %v %v", ok, err)
		}
	}
	if ok, err := db.ReplacePushSubscription(ctx, &types.PushSubscription{UID: row.UID, Endpoint: fmt.Sprintf("surface://%d/extra", row.UID), RegistrationID: "extra-registration", P256DH: "metadata", Auth: "hash"}, nil, 10); err != nil || ok {
		t.Fatalf("limit weakened: stored=%v err=%v", ok, err)
	}
	if err := db.DeletePushSubscriptionVersion(ctx, row.UID, row.Endpoint, row.RegistrationID, row.P256DH, "surface-v1:rotated"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListPushSubscriptions(ctx, row.UID)
	if err != nil || len(rows) != 9 {
		t.Fatalf("retire only matching surface version: %d %v", len(rows), err)
	}
	t.Run("overlapping-durable-runs", func(t *testing.T) {
		topic := fmt.Sprintf("p2p_%d_%d", users[0], users[1])
		if err := db.CreateTopic(topic, "p2p", users[0]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupTopic(topic) })
		expiry := time.Now().Add(time.Hour)
		write := func(run, state string) {
			t.Helper()
			_, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: users[1], RunID: run, State: state, ExpiresAt: &expiry})
			if err != nil {
				t.Fatal(err)
			}
		}
		write("A", "running")
		write("B", "running")
		write("B", "completed")
		projection, err := db.GetConversationTaskStatusForSource(topic, users[1])
		if err != nil || projection == nil || projection.RunID != "A" {
			t.Fatal("expected older active projection", projection, err)
		}
		reader := db.NativeSurfaceReader()
		exact, err := reader.TaskRun(ctx, topic, users[1], "B")
		if err != nil || exact == nil || exact.State != "completed" {
			t.Fatal("lost B completion", exact, err)
		}
		write("C", "running")
		exact, err = reader.TaskRun(ctx, topic, users[1], "B")
		if err != nil || exact == nil || exact.State != "completed" {
			t.Fatal("other run hid exact B completion", exact, err)
		}

	})

	t.Run("late-first-seen-terminal-ingestion", func(t *testing.T) {
		topic := fmt.Sprintf("p2p_%d_%d", users[0], users[1])
		if err := db.CreateTopic(topic, "p2p", users[0]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupTopic(topic) })
		reader := db.NativeSurfaceReader()
		expiry := time.Now().Add(time.Hour)
		base := time.Now().Add(-5 * time.Minute).UTC()
		write := func(run, state string, event time.Time) {
			t.Helper()
			_, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: users[1], RunID: run, State: state, EventUpdatedAt: event, ExpiresAt: &expiry})
			if err != nil {
				t.Fatal(err)
			}
		}
		write("B", "running", base.Add(time.Minute))
		write("A", "completed", base) // Later insertion, older publisher terminal receipt.
		projection, err := db.GetConversationTaskStatusForSource(topic, users[1])
		if err != nil || projection == nil || projection.RunID != "B" || projection.State != "running" {
			t.Fatal("late A changed active projection", projection, err)
		}
		for i, state := range []string{"running", "waiting", "completed"} {
			if i > 0 {
				write("B", state, base.Add(time.Duration(i+1)*time.Minute))
			}
			exact, err := reader.TaskRun(ctx, topic, users[1], "B")
			if err != nil || exact == nil || exact.State != state {
				t.Fatal("B lifecycle lost after late A", exact, err)
			}
		}
	})

}
