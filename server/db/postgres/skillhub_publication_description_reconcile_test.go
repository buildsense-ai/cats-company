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

func descriptionReconcileMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000065_skillhub_publication_description_reconcile." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationDescriptionReconcileMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(descriptionReconcileMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionReconcilesTable), " ") {
		t.Fatal("000065 differs from startup")
	}
}

func TestPostgresSkillHubPublicationDescriptionReconcileConcurrentRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, actor, other, o, terminal := terminalReleaseFixture(t)
	ctx := context.Background()
	scope := store.SkillHubPublicationCancellationScope{Issuer: o.Continuation.Application.Input.Receipt.Query.Issuer, Audience: o.Continuation.Application.Input.Receipt.Query.Audience}
	wrong := scope
	wrong.Audience = "https://other.example"
	if l, err := db.ClaimSkillHubPublicationDescriptionReconcile(ctx, wrong); err != nil || l != nil {
		t.Fatal("wrong scope", l, err)
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var wg sync.WaitGroup
	results := make(chan *store.SkillHubPublicationDescriptionReconcileLease, 8)
	errs := make(chan error, 8)
	for _, a := range []*Adapter{db, peer, db, peer, db, peer, db, peer} {
		wg.Add(1)
		go func(a *Adapter) {
			defer wg.Done()
			l, err := a.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope)
			results <- l
			errs <- err
		}(a)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *store.SkillHubPublicationDescriptionReconcileLease
	for l := range results {
		if l != nil {
			if first != nil {
				t.Fatal("multiple live leases")
			}
			first = l
		}
	}
	if first == nil || first.Validate() != nil || first.ActorUID != actor || first.EntryID != terminal.EntryID || first.ApplicationID != terminal.ApplicationID || first.Generation != 1 {
		t.Fatal("wrong lease", first)
	}
	if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, *first, "recorded"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("recorded without terminal", err)
	}
	bad := *first
	bad.ActorUID = other
	if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, bad, "progress"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("wrong actor finished lease", err)
	}
	// Expire only the scheduling lease in this isolated schema. The generation
	// and token must fence the previous process after restart/takeover.
	deliveryExec(t, db, `UPDATE skillhub_publication_description_reconciles SET lease_until=clock_timestamp()-interval '1 second' WHERE application_id=$1`, terminal.ApplicationID)
	second, err := peer.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope)
	if err != nil || second == nil || second.Generation != 2 || second.Token == first.Token {
		t.Fatal("takeover", second, err)
	}
	if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, *first, "retry"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("stale worker settled", err)
	}
	if err := peer.FinishSkillHubPublicationDescriptionReconcile(ctx, *second, "retry"); err != nil {
		t.Fatal(err)
	}
	var state string
	var failures int
	var delay float64
	if err := db.db.QueryRow(`SELECT state,failures,EXTRACT(EPOCH FROM next_attempt_at-clock_timestamp()) FROM skillhub_publication_description_reconciles WHERE application_id=$1`, terminal.ApplicationID).Scan(&state, &failures, &delay); err != nil || state != "pending" || failures != 1 || delay < 10 || delay > 16 {
		t.Fatal("durable retry/backoff", state, failures, delay, err)
	}
	if l, err := db.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope); err != nil || l != nil {
		t.Fatal("ignored backoff", l, err)
	}
	// Bot liveness/ownership cannot block historical observation recovery.
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1,enabled=false,body_id='offline' WHERE user_id=(SELECT bot_uid FROM skillhub_publication_entries WHERE entry_id=$2)`, other, terminal.EntryID)
	deliveryExec(t, db, `UPDATE skillhub_publication_description_reconciles SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE application_id=$1`, terminal.ApplicationID)
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
	if l, err := db.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope); err != nil || l != nil {
		t.Fatal("disabled original selected", l, err)
	}
	deliveryExec(t, db, `UPDATE users SET state=0 WHERE id=$1`, actor)
	third, err := db.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope)
	if err != nil || third == nil || third.Generation != 3 || third.ActorUID != actor {
		t.Fatal("historical actor recovery", third, err)
	}
	if _, err := peer.RecordSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, o, terminal); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, *third, "recorded"); err != nil {
		t.Fatal(err)
	}
	if l, err := peer.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope); err != nil || l != nil {
		t.Fatal("terminal was re-polled", l, err)
	}
	if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, *third, "retry"); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("settled schedule reset", err)
	}
	if _, err := db.db.Exec(descriptionReconcileMigration(t, "down")); err == nil {
		t.Fatal("nonempty rollback lost queue")
	}
	for _, q := range []string{`UPDATE skillhub_publication_description_reconciles SET state='pending' WHERE application_id=$1`, `DELETE FROM skillhub_publication_description_reconciles WHERE application_id=$1`} {
		if _, err := db.db.Exec(q, terminal.ApplicationID); err == nil {
			t.Fatal("terminal schedule reset/deleted")
		}
	}
}

func TestPostgresSkillHubPublicationDescriptionReconcileBackoffAndMigration(t *testing.T) {
	t.Parallel()
	db, _, _, _, o, terminal := terminalReleaseFixture(t)
	ctx := context.Background()
	for _, direction := range []string{"down", "down", "up", "up"} {
		deliveryExec(t, db, descriptionReconcileMigration(t, direction))
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	scope := store.SkillHubPublicationCancellationScope{Issuer: o.Continuation.Application.Input.Receipt.Query.Issuer, Audience: o.Continuation.Application.Input.Receipt.Query.Audience}
	for i := 0; i < 9; i++ {
		l, err := db.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope)
		if err != nil || l == nil {
			t.Fatal("poll exhausted", i, err)
		}
		if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, *l, "retry"); err != nil {
			t.Fatal(err)
		}
		deliveryExec(t, db, `UPDATE skillhub_publication_description_reconciles SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE application_id=$1`, terminal.ApplicationID)
	}
	l, err := db.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope)
	if err != nil || l == nil || l.Generation != 10 {
		t.Fatal("retry became terminal", l, err)
	}
	if err := db.FinishSkillHubPublicationDescriptionReconcile(ctx, *l, "progress"); err != nil {
		t.Fatal(err)
	}
	var failures, count int
	var next time.Time
	if err := db.db.QueryRow(`SELECT failures,next_attempt_at FROM skillhub_publication_description_reconciles WHERE application_id=$1`, terminal.ApplicationID).Scan(&failures, &next); err != nil || failures != 0 || time.Until(next) < 20*time.Second || time.Until(next) > 31*time.Second {
		t.Fatal("progress polling", failures, next, err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_description_release_terminals`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unknown converted into terminal", count, err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy commit side effect", count, err)
	}
}
