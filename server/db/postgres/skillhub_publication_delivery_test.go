package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func publicationDeliveryFixture(t *testing.T) (*Adapter, string, string, store.SkillHubPublicationDeliveryBinding, int64) {
	t.Helper()
	db, dsn, in, other := publicationDatabase(t)
	entry, _, err := db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BumpBotConnectionGeneration(in.BotUID); err != nil {
		t.Fatal(err)
	}
	return db, dsn, entry.ID, store.SkillHubPublicationDeliveryBinding{ActorUID: in.ActorUID, BotUID: in.BotUID, Source: in.Source, NodeID: "node-a", ConnectionID: "socket-a"}, other
}

func deliveryExec(t *testing.T, db *Adapter, query string, args ...any) {
	t.Helper()
	if _, err := db.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func deliveryClaim(t *testing.T, db *Adapter, id string, b store.SkillHubPublicationDeliveryBinding) *store.SkillHubPublicationDeliveryLeaseRecord {
	t.Helper()
	l, err := db.ClaimSkillHubPublicationDelivery(context.Background(), id, b)
	if err != nil || l == nil {
		t.Fatalf("claim: %+v %v", l, err)
	}
	return l
}

func TestPostgresSkillHubPublicationDeliveryPersistence(t *testing.T) {
	t.Parallel()
	db, dsn, id, b, _ := publicationDeliveryFixture(t)
	ctx := context.Background()
	ids, err := db.ListSkillHubPublicationDeliveries(ctx, b.BotUID, 32)
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("due: %v %v", ids, err)
	}
	l := deliveryClaim(t, db, id, b)
	if l.Attempt != 1 || l.ExpiresAt.IsZero() || !store.ValidSkillHubPublicationDeliveryToken(l.Token) {
		t.Fatalf("lease: %+v", l)
	}
	if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatalf("double claim: %v", err)
	}
	if ids, err := db.ListSkillHubPublicationDeliveries(ctx, b.BotUID, 32); err != nil || len(ids) != 0 {
		t.Fatalf("leased listed: %v %v", ids, err)
	}
	// Browser/requester closure is irrelevant: reopen the durable store, not an RPC map.
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.FinishSkillHubPublicationDelivery(ctx, *l, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.FinishSkillHubPublicationDelivery(ctx, *l, ""); err != nil {
		t.Fatalf("lost ack replay: %v", err)
	}
	if err := reopened.FinishSkillHubPublicationDelivery(ctx, *l, "publication.source_changed"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("changed receipt: %v", err)
	}
	if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatalf("verified reclaim: %v", err)
	}
	var state string
	var verified bool
	var attempts int
	if err := db.db.QueryRow(`SELECT delivery_state, source_verified_at IS NOT NULL, delivery_attempts FROM skillhub_publication_outbox WHERE entry_id=$1`, id).Scan(&state, &verified, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "source_verified" || !verified || attempts != 1 {
		t.Fatalf("result: %s %v %d", state, verified, attempts)
	}
	entry, err := db.GetSkillHubPublicationEntry(ctx, b.ActorUID, id)
	if err != nil || entry == nil || entry.State != "waiting_runtime" || entry.Input.Source != b.Source {
		t.Fatalf("intent changed: %+v %v", entry, err)
	}
	publicationCounts(t, db, 1) // No new intent, upload or capture event.
}

func TestPostgresSkillHubPublicationDeliveryReconnectFence(t *testing.T) {
	t.Parallel()
	db, dsn, id, b, _ := publicationDeliveryFixture(t)
	ctx := context.Background()
	old := deliveryClaim(t, db, id, b)
	if _, err := db.BumpBotConnectionGeneration(b.BotUID); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSkillHubPublicationDelivery(ctx, *old, ""); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("old generation: %v", err)
	}
	b.NodeID, b.ConnectionID, b.Source.RouteGeneration = "node-b", "socket-b", 2
	if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatalf("premature steal: %v", err)
	}
	deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, id)
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current := deliveryClaim(t, reopened, id, b)
	if current.Token == old.Token || current.Attempt != 2 {
		t.Fatal("fence reused")
	}
	// Even copying a newer generation/node onto an old receipt cannot consume the lease.
	old.Binding = b
	if err := db.FinishSkillHubPublicationDelivery(ctx, *old, ""); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("stale token: %v", err)
	}
	wrong := *current
	wrong.Binding.ConnectionID = "another-socket"
	if err := db.FinishSkillHubPublicationDelivery(ctx, wrong, ""); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("wrong socket: %v", err)
	}
	if err := db.FinishSkillHubPublicationDelivery(ctx, *current, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSkillHubPublicationDeliveryConcurrentClaims(t *testing.T) {
	t.Parallel()
	db, dsn, id, b, _ := publicationDeliveryFixture(t)
	other := &Adapter{}
	if err := other.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := db
			if i%2 == 1 {
				target = other
			}
			_, err := target.ClaimSkillHubPublicationDelivery(context.Background(), id, b)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, store.ErrSkillHubPublicationBusy) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("claim winners: %d", success)
	}
	var attempts int
	if err := db.db.QueryRow(`SELECT delivery_attempts FROM skillhub_publication_outbox WHERE entry_id=$1`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d %v", attempts, err)
	}
}

func TestPostgresSkillHubPublicationDeliveryAuthority(t *testing.T) {
	t.Parallel()
	db, _, id, b, other := publicationDeliveryFixture(t)
	ctx := context.Background()
	l := deliveryClaim(t, db, id, b)
	for _, change := range []func(*store.SkillHubPublicationDeliveryBinding){
		func(b *store.SkillHubPublicationDeliveryBinding) { b.ActorUID = other },
		func(b *store.SkillHubPublicationDeliveryBinding) { b.BotUID = other },
		func(b *store.SkillHubPublicationDeliveryBinding) { b.Source.DeviceID = "other" },
		func(b *store.SkillHubPublicationDeliveryBinding) { b.Source.BodyID = "other" },
		func(b *store.SkillHubPublicationDeliveryBinding) { b.Source.InstallationID = "other" },
		func(b *store.SkillHubPublicationDeliveryBinding) { b.Source.WorkspaceGeneration = "other" },
		func(b *store.SkillHubPublicationDeliveryBinding) { b.Source.LocalSkillID = "other" },
	} {
		bad := b
		change(&bad)
		if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, bad); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("binding: %v", err)
		}
		wrong := *l
		wrong.Binding = bad
		if err := db.FinishSkillHubPublicationDelivery(ctx, wrong, ""); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("receipt binding: %v", err)
		}
	}
	for _, tc := range []struct {
		change, restore         string
		changeArgs, restoreArgs []any
	}{
		{`UPDATE users SET state=1 WHERE id=$1`, `UPDATE users SET state=0 WHERE id=$1`, []any{b.ActorUID}, []any{b.ActorUID}},
		{`UPDATE users SET state=1 WHERE id=$1`, `UPDATE users SET state=0 WHERE id=$1`, []any{b.BotUID}, []any{b.BotUID}},
		{`UPDATE users SET account_type='bot' WHERE id=$1`, `UPDATE users SET account_type='human' WHERE id=$1`, []any{b.ActorUID}, []any{b.ActorUID}},
		{`UPDATE bot_config SET enabled=false WHERE user_id=$1`, `UPDATE bot_config SET enabled=true WHERE user_id=$1`, []any{b.BotUID}, []any{b.BotUID}},
		{`UPDATE bot_config SET owner_id=$2 WHERE user_id=$1`, `UPDATE bot_config SET owner_id=$2 WHERE user_id=$1`, []any{b.BotUID, other}, []any{b.BotUID, b.ActorUID}},
		{`UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, `UPDATE bot_config SET body_id=$2 WHERE user_id=$1`, []any{b.BotUID}, []any{b.BotUID, b.Source.BodyID}},
	} {
		deliveryExec(t, db, tc.change, tc.changeArgs...)
		if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("authority claim: %v", err)
		}
		if err := db.FinishSkillHubPublicationDelivery(ctx, *l, ""); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("authority receipt: %v", err)
		}
		if ids, err := db.ListSkillHubPublicationDeliveries(ctx, b.BotUID, 32); err != nil || len(ids) != 0 {
			t.Fatalf("unauthorized schedule: %v %v", ids, err)
		}
		deliveryExec(t, db, tc.restore, tc.restoreArgs...)
	}
	deliveryExec(t, db, `DELETE FROM bot_connection_generations WHERE bot_uid=$1`, b.BotUID)
	if err := db.FinishSkillHubPublicationDelivery(ctx, *l, ""); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("missing generation: %v", err)
	}
}

func TestPostgresSkillHubPublicationDeliveryReceiptRace(t *testing.T) {
	t.Parallel()
	db, _, id, b, _ := publicationDeliveryFixture(t)
	l := deliveryClaim(t, db, id, b)
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- db.FinishSkillHubPublicationDelivery(context.Background(), *l, "") }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	if err := db.db.QueryRow(`SELECT delivery_attempts FROM skillhub_publication_outbox WHERE entry_id=$1`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("duplicate receipts: %d %v", attempts, err)
	}
}

func TestPostgresSkillHubPublicationDeliveryAuthorityRace(t *testing.T) {
	t.Parallel()
	for _, receipt := range []bool{false, true} {
		db, _, id, b, other := publicationDeliveryFixture(t)
		var lease *store.SkillHubPublicationDeliveryLeaseRecord
		if receipt {
			lease = deliveryClaim(t, db, id, b)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tx, err := db.db.BeginTx(ctx, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, b.BotUID); err != nil {
			tx.Rollback()
			cancel()
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			if receipt {
				result <- db.FinishSkillHubPublicationDelivery(ctx, *lease, "")
			} else {
				_, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b)
				result <- err
			}
		}()
		select {
		case err := <-result:
			tx.Rollback()
			cancel()
			t.Fatalf("used uncommitted authority: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := tx.Commit(); err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			cancel()
			t.Fatalf("stale authority: %v", err)
		}
		cancel()
	}
}

func TestPostgresSkillHubPublicationDeliveryExpiredReceipt(t *testing.T) {
	t.Parallel()
	db, _, id, b, _ := publicationDeliveryFixture(t)
	l := deliveryClaim(t, db, id, b)
	deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, id)
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *l, ""); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("expired receipt: %v", err)
	}
	newLease := deliveryClaim(t, db, id, b) // Same socket/generation after worker death still gets a new fence.
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *l, ""); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatalf("old same-route fence: %v", err)
	}
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *newLease, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSkillHubPublicationDeliveryRetries(t *testing.T) {
	t.Parallel()
	for _, crashLast := range []bool{false, true} {
		db, _, id, b, _ := publicationDeliveryFixture(t)
		ctx := context.Background()
		for attempt := 1; attempt <= store.SkillHubPublicationDeliveryAttempts; attempt++ {
			l := deliveryClaim(t, db, id, b)
			if l.Attempt != attempt {
				t.Fatalf("attempt: %d", l.Attempt)
			}
			if crashLast && attempt == store.SkillHubPublicationDeliveryAttempts {
				deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, id)
				ids, err := db.ListSkillHubPublicationDeliveries(ctx, b.BotUID, 32)
				if err != nil || len(ids) != 1 {
					t.Fatalf("last crash hidden: %v %v", ids, err)
				}
				if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationExhausted) {
					t.Fatalf("exhaustion: %v", err)
				}
				break
			}
			if err := db.FinishSkillHubPublicationDelivery(ctx, *l, "publication.runtime_timeout"); err != nil {
				t.Fatal(err)
			}
			if err := db.FinishSkillHubPublicationDelivery(ctx, *l, "publication.runtime_timeout"); err != nil {
				t.Fatalf("retry ack replay: %v", err)
			}
			if attempt < store.SkillHubPublicationDeliveryAttempts {
				if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
					t.Fatalf("ignored backoff: %v", err)
				}
				deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET next_attempt_at=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, id)
			}
		}
		if err := db.CreateSchema(); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
			t.Fatalf("attempt budget reset: %v", err)
		}
		var state, code string
		if err := db.db.QueryRow(`SELECT delivery_state,last_code FROM skillhub_publication_outbox WHERE entry_id=$1`, id).Scan(&state, &code); err != nil {
			t.Fatal(err)
		}
		if state != "needs_user_action" || code != "publication.delivery_exhausted" {
			t.Fatalf("exhausted result: %s %s", state, code)
		}
	}
}

func TestPostgresSkillHubPublicationDeliveryRejectedAndCancelled(t *testing.T) {
	t.Parallel()
	db, _, id, b, _ := publicationDeliveryFixture(t)
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.ClaimSkillHubPublicationDelivery(cancelled, id, b); err == nil {
		t.Fatal("cancelled claim succeeded")
	}
	l := deliveryClaim(t, db, id, b)
	if l.Attempt != 1 {
		t.Fatal("cancelled claim consumed attempt")
	}
	if err := db.FinishSkillHubPublicationDelivery(cancelled, *l, ""); err == nil {
		t.Fatal("cancelled receipt succeeded")
	}
	if err := db.FinishSkillHubPublicationDelivery(ctx, *l, "raw secret path"); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		t.Fatalf("raw error: %v", err)
	}
	if err := db.FinishSkillHubPublicationDelivery(ctx, *l, "publication.source_unsafe"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatalf("unsafe retry: %v", err)
	}
	if ids, err := db.ListSkillHubPublicationDeliveries(ctx, b.BotUID, 32); err != nil || len(ids) != 0 {
		t.Fatalf("unsafe due: %v %v", ids, err)
	}
	for _, limit := range []int{0, 33} {
		if _, err := db.ListSkillHubPublicationDeliveries(ctx, b.BotUID, limit); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
			t.Fatal("unbounded list")
		}
	}
}
