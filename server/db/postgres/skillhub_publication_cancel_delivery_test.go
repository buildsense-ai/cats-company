package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func publicationCancelDeliveryMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000039_skillhub_publication_cancel_deliveries." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestSkillHubPublicationCancelDeliveryMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationCancelDeliveryMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationCancellationDeliveriesTable), " ") {
		t.Fatal("000039 differs from startup")
	}
}
func cancelDeliveryFixture(t *testing.T) (*Adapter, string, store.SkillHubPublicationUploadInput, int64) {
	t.Helper()
	db, dsn, in, other := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	recordReceipt(t, db, receiptForGrant(g), false)
	requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "ready")
	return db, dsn, in, other
}
func cancellationScope(in store.SkillHubPublicationUploadInput) store.SkillHubPublicationCancellationScope {
	return store.SkillHubPublicationCancellationScope{Issuer: in.Issuer, Audience: in.Audience}
}
func claimCancellation(t *testing.T, db *Adapter, in store.SkillHubPublicationUploadInput) *store.SkillHubPublicationCancellationLease {
	t.Helper()
	l, err := db.ClaimSkillHubPublicationCancellation(context.Background(), cancellationScope(in))
	if err != nil || l == nil || l.Validate() != nil {
		t.Fatalf("claim: %+v %v", l, err)
	}
	return l
}
func confirmedCancellation() store.SkillHubPublicationCancellationOutcome {
	return store.SkillHubPublicationCancellationOutcome{Kind: "confirmed", CancelledAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), ObservedAt: time.Date(2026, 10, 19, 0, 0, 1, 0, time.UTC)}
}
func TestPostgresSkillHubPublicationCancelDeliveryLifecycle(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := cancelDeliveryFixture(t)
	ctx := context.Background()
	before := cancellationIntentSnapshot(t, db, in.EntryID)
	l := claimCancellation(t, db, in)
	if l.Attempt != 1 {
		t.Fatal("attempt")
	}
	if next, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err != nil || next != nil {
		t.Fatal("duplicate claim", err)
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.FinishSkillHubPublicationCancellation(ctx, *l, confirmedCancellation()); err != nil {
		t.Fatal(err)
	}
	r, err := db.GetSkillHubPublicationCancellationDelivery(ctx, in.Binding.ActorUID, in.EntryID)
	if err != nil || r == nil || r.State != "confirmed" || r.CancelledAt == nil || !r.CancelledAt.Equal(confirmedCancellation().CancelledAt) {
		t.Fatalf("result: %+v %v", r, err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if next, err := reopened.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err != nil || next != nil {
		t.Fatal("confirmed reclaimed", err)
	}
	if err := db.FinishSkillHubPublicationCancellation(ctx, *l, store.SkillHubPublicationCancellationOutcome{Kind: "retry"}); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("late failure overwrote confirmation", err)
	}
	if cancellationIntentSnapshot(t, db, in.EntryID) != before {
		t.Fatal("intent rewritten")
	}
}
func TestPostgresSkillHubPublicationCancelDeliveryLateReceiptAndScope(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"local_only", "waiting_receipt", "legacy"} {
		t.Run(stage, func(t *testing.T) {
			db, _, in, _ := uploadGrantFixture(t)
			ctx := context.Background()
			var g *store.SkillHubPublicationUploadGrant
			if stage != "local_only" {
				g, _ = issueUpload(t, db, in)
			}
			if stage == "legacy" {
				if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			} else {
				requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, stage)
			}
			if l, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err != nil || l != nil {
				t.Fatal("no receipt sent", err)
			}
			if g == nil {
				return
			}
			recordReceipt(t, db, receiptForGrant(g), true)
			if stage == "legacy" {
				if l, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err != nil || l != nil {
					t.Fatal("legacy auto-enrolled", err)
				}
				return
			}
			wrong := cancellationScope(in)
			wrong.Audience = "https://other.example"
			if l, err := db.ClaimSkillHubPublicationCancellation(ctx, wrong); err != nil || l != nil {
				t.Fatal("wrong peer", err)
			}
			claimCancellation(t, db, in)
		})
	}
}
func TestPostgresSkillHubPublicationCancelDeliveryConcurrencyAndFencing(t *testing.T) {
	t.Parallel()
	db, _, in, _ := cancelDeliveryFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	leases := make(chan *store.SkillHubPublicationCancellationLease, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in))
			if err != nil {
				t.Error(err)
			}
			if l != nil {
				leases <- l
			}
		}()
	}
	wg.Wait()
	close(leases)
	if len(leases) != 1 {
		t.Fatal("more than one owner")
	}
	old := <-leases
	deliveryExec(t, db, `UPDATE skillhub_publication_cancel_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, in.EntryID)
	newLease := claimCancellation(t, db, in)
	if newLease.Attempt != 2 || old.Token == newLease.Token {
		t.Fatal("lease not fenced")
	}
	if err := db.FinishSkillHubPublicationCancellation(ctx, *old, confirmedCancellation()); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal(err)
	}
	wrong := *newLease
	wrong.Receipt.EventID = "pubevent_12345678-1234-4123-8123-123456789abc"
	if err := db.FinishSkillHubPublicationCancellation(ctx, wrong, confirmedCancellation()); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal(err)
	}
	if err := db.FinishSkillHubPublicationCancellation(ctx, *newLease, confirmedCancellation()); err != nil {
		t.Fatal(err)
	}
}
func TestPostgresSkillHubPublicationCancelDeliveryBudget(t *testing.T) {
	t.Parallel()
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "responses", true: "crashes"}[crash], func(t *testing.T) {
			db, _, in, _ := cancelDeliveryFixture(t)
			ctx := context.Background()
			for attempt := 1; attempt <= 8; attempt++ {
				l := claimCancellation(t, db, in)
				if l.Attempt != attempt {
					t.Fatal("attempt reset")
				}
				if crash {
					deliveryExec(t, db, `UPDATE skillhub_publication_cancel_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, in.EntryID)
				} else {
					if err := db.FinishSkillHubPublicationCancellation(ctx, *l, store.SkillHubPublicationCancellationOutcome{Kind: "retry"}); err != nil {
						t.Fatal(err)
					}
					if n, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err != nil || n != nil {
						t.Fatal("ignored backoff", err)
					}
					if attempt < 8 {
						deliveryExec(t, db, `UPDATE skillhub_publication_cancel_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, in.EntryID)
					}
				}
			}
			if l, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err != nil || l != nil {
				t.Fatal("budget exceeded", err)
			}
			r, err := db.GetSkillHubPublicationCancellationDelivery(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil || r.State != "exhausted" || r.Attempts != 8 || r.CancelledAt != nil {
				t.Fatalf("budget result %+v %v", r, err)
			}
		})
	}
}
func TestPostgresSkillHubPublicationCancelDeliveryAuthorityAndConflict(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"confirmed", "not_found", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			db, _, in, other := cancelDeliveryFixture(t)
			ctx := context.Background()
			deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1,enabled=false WHERE user_id=$2`, other, in.Binding.BotUID)
			l := claimCancellation(t, db, in)
			out := store.SkillHubPublicationCancellationOutcome{Kind: kind}
			if kind == "confirmed" {
				out = confirmedCancellation()
			}
			if err := db.FinishSkillHubPublicationCancellation(ctx, *l, out); err != nil {
				t.Fatal(err)
			}
			r, err := db.GetSkillHubPublicationCancellationDelivery(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil || r == nil {
				t.Fatal(err)
			}
			if kind != "confirmed" && (r.State != "needs_attention" || r.CancelledAt != nil || r.LastCode != kind) {
				t.Fatal("conflict claimed as success")
			}
			for _, uid := range []int64{other, in.Binding.BotUID} {
				if r, err := db.GetSkillHubPublicationCancellationDelivery(ctx, uid, in.EntryID); err != nil || r != nil {
					t.Fatal("history inherited", err)
				}
			}
			deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			if r, err := db.GetSkillHubPublicationCancellationDelivery(ctx, in.Binding.ActorUID, in.EntryID); err != nil || r != nil {
				t.Fatal("disabled read", err)
			}
		})
	}
}
func TestPostgresSkillHubPublicationCancelDeliveryMigrationAndGuards(t *testing.T) {
	t.Parallel()
	db, _, in, _ := cancelDeliveryFixture(t)
	ctx := context.Background()
	up, down := publicationCancelDeliveryMigration(t, "up"), publicationCancelDeliveryMigration(t, "down")
	for _, sql := range []string{down, down, up, up} {
		deliveryExec(t, db, sql)
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_cancel_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration scheduled work", err)
	}
	l := claimCancellation(t, db, in)
	if err := db.FinishSkillHubPublicationCancellation(ctx, *l, confirmedCancellation()); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{down, `DELETE FROM skillhub_publication_cancel_deliveries`, `UPDATE skillhub_publication_cancel_deliveries SET state='pending',remote_cancelled_at=NULL,remote_observed_at=NULL`, `UPDATE skillhub_publication_cancel_deliveries SET remote_cancelled_at=remote_cancelled_at-interval '1 second'`} {
		if _, err := db.db.Exec(sql); err == nil {
			t.Fatal("evidence lost")
		}
	}
	deliveryExec(t, db, up)
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
}
func TestPostgresSkillHubPublicationCancelDeliveryAtomicFailure(t *testing.T) {
	t.Parallel()
	db, _, in, _ := cancelDeliveryFixture(t)
	ctx := context.Background()
	deliveryExec(t, db, `CREATE FUNCTION fail_cancel_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fault'; END; $$;
 CREATE TRIGGER fail_cancel_delivery BEFORE INSERT ON skillhub_publication_cancel_deliveries FOR EACH ROW EXECUTE FUNCTION fail_cancel_delivery();`)
	if _, err := db.ClaimSkillHubPublicationCancellation(ctx, cancellationScope(in)); err == nil {
		t.Fatal("expected fault")
	}
	deliveryExec(t, db, `DROP TRIGGER fail_cancel_delivery ON skillhub_publication_cancel_deliveries; DROP FUNCTION fail_cancel_delivery();`)
	l := claimCancellation(t, db, in)
	if l.Attempt != 1 {
		t.Fatal("failed claim consumed attempt")
	}
}
