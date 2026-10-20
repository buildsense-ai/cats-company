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

func captureDeliveryClaim(t *testing.T, db *Adapter, in store.SkillHubPublicationCaptureInput) *store.SkillHubPublicationCaptureLease {
	t.Helper()
	l, err := db.ClaimSkillHubPublicationCaptureDelivery(context.Background(), in.EntryID, in.Binding)
	if err != nil || l == nil || l.Validate() != nil {
		t.Fatalf("capture claim: %+v %v", l, err)
	}
	return l
}

func captureDeliveryIDs(t *testing.T, db *Adapter, in store.SkillHubPublicationCaptureInput) []string {
	t.Helper()
	b := in.Binding
	ids, err := db.ListSkillHubPublicationCaptureDeliveries(context.Background(), store.SkillHubPublicationRuntimeSelector{
		ActorUID: b.ActorUID, BotUID: b.BotUID, DeviceID: b.Source.DeviceID, BodyID: b.Source.BodyID, InstallationID: b.Source.InstallationID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPostgresSkillHubPublicationCaptureDeliveryRecovery(t *testing.T) {
	db, dsn, in, _ := captureFixture(t)
	ctx := context.Background()
	if ids := captureDeliveryIDs(t, db, in); len(ids) != 1 || ids[0] != in.EntryID {
		t.Fatal(ids)
	}
	first := captureDeliveryClaim(t, db, in)
	if len(captureDeliveryIDs(t, db, in)) != 0 {
		t.Fatal("leased entry listed")
	}
	if _, err := db.ClaimSkillHubPublicationCaptureDelivery(ctx, in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal(err)
	}
	if err := db.FinishSkillHubPublicationCaptureDelivery(ctx, *first, "captured"); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("no evidence accepted", err)
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	deliveryExec(t, db, `UPDATE skillhub_publication_capture_deliveries SET lease_until=clock_timestamp()-interval '1 second'`)
	second := captureDeliveryClaim(t, reopened, in)
	if second.Attempt != 2 || second.Token == first.Token {
		t.Fatal("restart reset/forked lease")
	}
	if err := db.FinishSkillHubPublicationCaptureDelivery(ctx, *first, "retry"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("old worker settled new lease", err)
	}
	for _, mutate := range []func(*store.SkillHubPublicationCaptureLease){
		func(l *store.SkillHubPublicationCaptureLease) { l.Attempt++ },
		func(l *store.SkillHubPublicationCaptureLease) { l.ExpiresAt = l.ExpiresAt.Add(time.Second) },
		func(l *store.SkillHubPublicationCaptureLease) { l.Binding.ConnectionID += "-other" },
	} {
		bad := *second
		mutate(&bad)
		if err := db.FinishSkillHubPublicationCaptureDelivery(ctx, bad, "retry"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
			t.Fatal("forged lease settled", err)
		}
	}
	if err := reopened.FinishSkillHubPublicationCaptureDelivery(ctx, *second, "retry"); err != nil {
		t.Fatal(err)
	}
	if len(captureDeliveryIDs(t, db, in)) != 0 {
		t.Fatal("backoff ignored")
	}
	deliveryExec(t, db, `UPDATE skillhub_publication_capture_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second'`)
	third := captureDeliveryClaim(t, reopened, in)
	if third.Attempt != 3 {
		t.Fatal("budget reset")
	}
	recordCaptureForTest(t, db, in)
	// Crash after storing evidence but before finishing: no more runtime work.
	if len(captureDeliveryIDs(t, reopened, in)) != 0 {
		t.Fatal("persisted evidence recaptured")
	}
	if _, err := reopened.ClaimSkillHubPublicationCaptureDelivery(ctx, in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal(err)
	}
	if err := reopened.FinishSkillHubPublicationCaptureDelivery(ctx, *third, "retry"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.db.QueryRow(`SELECT state FROM skillhub_publication_capture_deliveries`).Scan(&state); err != nil || state != "captured" {
		t.Fatal("evidence lost to timeout", state, err)
	}
}

func TestPostgresSkillHubPublicationCaptureDeliveryBudget(t *testing.T) {
	for _, crash := range []bool{false, true} {
		db, _, in, _ := captureFixture(t)
		for n := 1; n <= 8; n++ {
			l := captureDeliveryClaim(t, db, in)
			if l.Attempt != n {
				t.Fatal("attempt", l.Attempt, n)
			}
			if crash {
				deliveryExec(t, db, `UPDATE skillhub_publication_capture_deliveries SET lease_until=clock_timestamp()-interval '1 second'`)
			} else {
				if err := db.FinishSkillHubPublicationCaptureDelivery(context.Background(), *l, "retry"); err != nil {
					t.Fatal(err)
				}
				deliveryExec(t, db, `UPDATE skillhub_publication_capture_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second'`)
			}
		}
		if crash {
			if len(captureDeliveryIDs(t, db, in)) != 1 {
				t.Fatal("last crashed lease must be settled")
			}
			if _, err := db.ClaimSkillHubPublicationCaptureDelivery(context.Background(), in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationExhausted) {
				t.Fatal(err)
			}
		}
		if len(captureDeliveryIDs(t, db, in)) != 0 {
			t.Fatal("exhausted queue spinning")
		}
		var state, code string
		if err := db.db.QueryRow(`SELECT state,last_code FROM skillhub_publication_capture_deliveries`).Scan(&state, &code); err != nil || state != "needs_attention" || code != "exhausted" {
			t.Fatal(state, code, err)
		}
	}
}

func TestPostgresSkillHubPublicationCaptureDeliveryAuthority(t *testing.T) {
	for _, mode := range []string{"cancel", "owner", "body", "human_disabled", "unverified", "device", "install"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := captureFixture(t)
			l := captureDeliveryClaim(t, db, in)
			switch mode {
			case "cancel":
				if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			case "owner":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1`, other)
			case "body":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed'`)
			case "human_disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			case "unverified":
				deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET delivery_state='needs_user_action',source_verified_at=NULL,last_code='publication.source_changed'`)
			case "device":
				in.Binding.Source.DeviceID += "-other"
				l.Binding = in.Binding
			case "install":
				in.Binding.Source.InstallationID += "-other"
				l.Binding = in.Binding
			}
			if len(captureDeliveryIDs(t, db, in)) != 0 {
				t.Fatal("unauthorized list")
			}
			if err := db.FinishSkillHubPublicationCaptureDelivery(context.Background(), *l, "retry"); err == nil {
				t.Fatal("revoked finish")
			}
			if _, err := db.ClaimSkillHubPublicationCaptureDelivery(context.Background(), in.EntryID, in.Binding); err == nil {
				t.Fatal("revoked claim")
			}
		})
	}
}

func TestPostgresSkillHubPublicationCaptureDeliveryConcurrentClaim(t *testing.T) {
	db, _, in, _ := captureFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.ClaimSkillHubPublicationCaptureDelivery(context.Background(), in.EntryID, in.Binding)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, store.ErrSkillHubPublicationBusy) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
}

func TestPostgresSkillHubPublicationCaptureDeliveryScopeBeforeLimit(t *testing.T) {
	db, _, in, _ := captureFixture(t)
	ctx := context.Background()
	// An older verified entry belongs to a different installation.
	want := store.SkillHubPublicationEntryInput{ActorUID: in.Binding.ActorUID, BotUID: in.Binding.BotUID,
		Entrypoint: "workspace", RequestID: "second-device", Source: in.Binding.Source}
	want.Source.DeviceID, want.Source.InstallationID = "device-two", "install-two"
	e, _, err := db.CreateSkillHubPublicationEntry(ctx, want, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	next := in
	next.EntryID, next.Binding.Source = e.ID, want.Source
	// An unverified source cannot be captured even by a trusted caller.
	if _, err := db.ClaimSkillHubPublicationCaptureDelivery(ctx, e.ID, next.Binding); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("unverified claim", err)
	}
	probe := deliveryClaim(t, db, e.ID, next.Binding)
	if err := db.FinishSkillHubPublicationDelivery(ctx, *probe, ""); err != nil {
		t.Fatal(err)
	}
	if ids := captureDeliveryIDs(t, db, next); len(ids) != 1 || ids[0] != e.ID {
		t.Fatal("wrong installation consumed LIMIT", ids)
	}
}

func TestPostgresSkillHubPublicationCaptureDeliveryReconnect(t *testing.T) {
	db, _, in, _ := captureFixture(t)
	ctx := context.Background()
	old := captureDeliveryClaim(t, db, in)
	if _, err := db.BumpBotConnectionGeneration(in.Binding.BotUID); err != nil {
		t.Fatal(err)
	}
	next := in
	next.Binding.Source.RouteGeneration++
	next.Binding.NodeID, next.Binding.ConnectionID = "node-next", "socket-next"
	if _, err := db.ClaimSkillHubPublicationCaptureDelivery(ctx, in.EntryID, next.Binding); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("reconnect bypassed live lease", err)
	}
	if err := db.FinishSkillHubPublicationCaptureDelivery(ctx, *old, "retry"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("old socket settled", err)
	}
	deliveryExec(t, db, `UPDATE skillhub_publication_capture_deliveries SET lease_until=clock_timestamp()-interval '1 second'`)
	current := captureDeliveryClaim(t, db, next)
	if current.Attempt != 2 || current.Binding != next.Binding {
		t.Fatal("reconnect reset budget")
	}
	recordCaptureForTest(t, db, next)
	if err := db.FinishSkillHubPublicationCaptureDelivery(ctx, *current, "captured"); err != nil {
		t.Fatal(err)
	}
}

func captureDeliveryMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000044_skillhub_publication_capture_deliveries." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPostgresSkillHubPublicationCaptureDeliveryMigration(t *testing.T) {
	up, down := captureDeliveryMigration(t, "up"), captureDeliveryMigration(t, "down")
	if strings.Join(strings.Fields(up), " ") != strings.Join(strings.Fields(createSkillHubPublicationCaptureDeliveriesTable), " ") {
		t.Fatal("migration/startup mismatch")
	}
	db, _, in, _ := captureFixture(t)
	deliveryExec(t, db, down)
	deliveryExec(t, db, up)
	l := captureDeliveryClaim(t, db, in)
	deliveryExec(t, db, up)
	if _, err := db.db.Exec(down); err == nil {
		t.Fatal("rollback erased retry budget")
	}
	if err := db.FinishSkillHubPublicationCaptureDelivery(context.Background(), *l, "rejected"); err != nil {
		t.Fatal(err)
	}
	if len(captureDeliveryIDs(t, db, in)) != 0 {
		t.Fatal("terminal rejection retried")
	}
}
