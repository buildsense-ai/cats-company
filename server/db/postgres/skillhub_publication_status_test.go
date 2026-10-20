package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func readPublicationStatus(t *testing.T, db *Adapter, uid int64, id, state string, attempts int) *store.SkillHubPublicationSourceStatus {
	t.Helper()
	s, err := db.GetSkillHubPublicationSourceStatus(context.Background(), uid, id)
	if err != nil || s == nil || s.EntryID != id || s.ActorUID != uid || s.State != state || s.Attempts != attempts || s.ObservedAt.IsZero() || s.NextAttemptAt.IsZero() {
		t.Fatalf("status: %+v %v", s, err)
	}
	return s
}

func TestPostgresSkillHubPublicationSourceStatusLifecycle(t *testing.T) {
	t.Parallel()
	db, dsn, id, b, _ := publicationDeliveryFixture(t)
	s := readPublicationStatus(t, db, b.ActorUID, id, "pending", 0)
	if s.LeaseUntil != nil || s.SourceVerifiedAt != nil || s.LastCode != "" {
		t.Fatal("new ledger has execution evidence")
	}
	lease := deliveryClaim(t, db, id, b)
	s = readPublicationStatus(t, db, b.ActorUID, id, "leased", 1)
	if s.LeaseUntil == nil || !s.LeaseUntil.Equal(lease.ExpiresAt) || !s.LeaseUntil.After(s.ObservedAt) {
		t.Fatal("lease time mismatch")
	}
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, "publication.runtime_timeout"); err != nil {
		t.Fatal(err)
	}
	s = readPublicationStatus(t, db, b.ActorUID, id, "pending", 1)
	if s.LastCode != "publication.runtime_timeout" || !s.NextAttemptAt.After(s.ObservedAt) {
		t.Fatal("lost retry evidence")
	}
	deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET next_attempt_at=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, id)
	lease = deliveryClaim(t, db, id, b)
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
		t.Fatal(err)
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = readPublicationStatus(t, reopened, b.ActorUID, id, "source_verified", 2)
	if s.SourceVerifiedAt == nil || s.SourceVerifiedAt.After(s.ObservedAt) || s.LastCode != "" {
		t.Fatal("lost verification evidence")
	}
	entry, err := db.GetSkillHubPublicationEntry(context.Background(), b.ActorUID, id)
	if err != nil || entry == nil || entry.State != "waiting_runtime" {
		t.Fatal("probe changed publication state")
	}
	publicationCounts(t, db, 1)
}

func TestPostgresSkillHubPublicationSourceStatusReadDoesNotRecover(t *testing.T) {
	t.Parallel()
	db, _, id, b, _ := publicationDeliveryFixture(t)
	deliveryClaim(t, db, id, b)
	deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET delivery_attempts=8,lease_until=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, id)
	var before, after string
	if err := db.db.QueryRow(`SELECT row_to_json(o)::text FROM skillhub_publication_outbox o WHERE entry_id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		s := readPublicationStatus(t, db, b.ActorUID, id, "leased", 8)
		if s.LeaseUntil == nil || s.LeaseUntil.After(s.ObservedAt) {
			t.Fatal("expected expired lease")
		}
	}
	if err := db.db.QueryRow(`SELECT row_to_json(o)::text FROM skillhub_publication_outbox o WHERE entry_id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("read changed ledger")
	}
	if _, err := db.ClaimSkillHubPublicationDelivery(context.Background(), id, b); !errors.Is(err, store.ErrSkillHubPublicationExhausted) {
		t.Fatal(err)
	}
	s := readPublicationStatus(t, db, b.ActorUID, id, "needs_user_action", 8)
	if s.LastCode != "publication.delivery_exhausted" {
		t.Fatal("lost terminal code")
	}
}

func TestPostgresSkillHubPublicationSourceStatusPrivacy(t *testing.T) {
	t.Parallel()
	db, _, id, b, other := publicationDeliveryFixture(t)
	ctx := context.Background()
	for _, uid := range []int64{other, b.BotUID, 999999} {
		if s, err := db.GetSkillHubPublicationSourceStatus(ctx, uid, id); err != nil || s != nil {
			t.Fatalf("disclosed to %d: %+v %v", uid, s, err)
		}
	}
	if s, err := db.GetSkillHubPublicationSourceStatus(ctx, b.ActorUID, "pubentry_ffffffffffffffffffffffffffffffff"); err != nil || s != nil {
		t.Fatalf("missing: %+v %v", s, err)
	}
	// A new Bot owner is not the publisher of the existing private task.
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$2,enabled=false,body_id='new-body' WHERE user_id=$1`, b.BotUID, other)
	readPublicationStatus(t, db, b.ActorUID, id, "pending", 0)
	if s, err := db.GetSkillHubPublicationSourceStatus(ctx, other, id); err != nil || s != nil {
		t.Fatal("new owner inherited history")
	}
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, b.ActorUID)
	if s, err := db.GetSkillHubPublicationSourceStatus(ctx, b.ActorUID, id); err != nil || s != nil {
		t.Fatal("disabled actor read history")
	}
	deliveryExec(t, db, `UPDATE users SET state=0,account_type='bot' WHERE id=$1`, b.ActorUID)
	if s, err := db.GetSkillHubPublicationSourceStatus(ctx, b.ActorUID, id); err != nil || s != nil {
		t.Fatal("nonhuman read history")
	}
	for _, tc := range []struct {
		uid int64
		id  string
	}{{0, id}, {b.ActorUID, "bad"}, {b.ActorUID, "pubentry_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}} {
		if _, err := db.GetSkillHubPublicationSourceStatus(ctx, tc.uid, tc.id); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
			t.Fatalf("invalid: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.GetSkillHubPublicationSourceStatus(canceled, b.ActorUID, id); err == nil {
		t.Fatal("canceled read succeeded")
	}
}

func TestPostgresSkillHubPublicationSourceStatusConcurrentReceipt(t *testing.T) {
	t.Parallel()
	db, _, id, b, _ := publicationDeliveryFixture(t)
	lease := deliveryClaim(t, db, id, b)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				s, err := db.GetSkillHubPublicationSourceStatus(context.Background(), b.ActorUID, id)
				if err != nil || s == nil {
					t.Errorf("read: %+v %v", s, err)
					return
				}
				if s.State == "leased" {
					if s.SourceVerifiedAt != nil || s.LastCode != "" {
						t.Error("torn leased observation")
					}
				} else if s.State == "source_verified" {
					if s.SourceVerifiedAt == nil || s.LastCode != "" {
						t.Error("torn verified observation")
					}
				} else {
					t.Errorf("state: %s", s.State)
				}
			}
		}()
	}
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	readPublicationStatus(t, db, b.ActorUID, id, "source_verified", 1)
}
