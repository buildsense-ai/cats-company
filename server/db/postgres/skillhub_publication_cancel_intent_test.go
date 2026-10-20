package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func requestCancelIntent(t *testing.T, db *Adapter, actor int64, id, state string) *store.SkillHubPublicationCancellationIntent {
	t.Helper()
	r, err := db.RequestSkillHubPublicationCancellation(context.Background(), actor, id)
	if err != nil || r == nil || r.EntryID != id || r.State != state || len(r.ID) != 42 || !strings.HasPrefix(r.ID, "pubcancel_") || r.RequestedAt.IsZero() {
		t.Fatalf("cancel intent: %+v %v", r, err)
	}
	return r
}

func cancellationIntentSnapshot(t *testing.T, db *Adapter, id string) string {
	t.Helper()
	var value string
	if err := db.db.QueryRow(`SELECT row_to_json(c)::text FROM skillhub_publication_cancel_intents c WHERE entry_id=$1`, id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPostgresSkillHubPublicationCancellationIntentLifecycle(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"no_grant", "grant", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			db, dsn, in, _ := uploadGrantFixture(t)
			ctx := context.Background()
			original, _ := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
			var g *store.SkillHubPublicationUploadGrant
			var token store.SkillHubPublicationUploadToken
			state := "local_only"
			if stage != "no_grant" {
				g, token = issueUpload(t, db, in)
				state = "waiting_receipt"
			}
			if stage == "receipt" {
				recordReceipt(t, db, receiptForGrant(g), false)
				state = "ready"
				// Existing HTTP-facing operation still refuses linked cancellation.
				if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
					t.Fatalf("legacy cancellation changed: %v", err)
				}
			}
			first := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, state)
			before := cancellationIntentSnapshot(t, db, in.EntryID)
			reopened := &Adapter{}
			if err := reopened.Open(dsn); err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				repeat := requestCancelIntent(t, reopened, in.Binding.ActorUID, in.EntryID, state)
				if *repeat != *first {
					t.Fatal("retry replaced durable cancellation")
				}
				read, err := reopened.GetSkillHubPublicationCancellationIntent(ctx, in.Binding.ActorUID, in.EntryID)
				if err != nil || read == nil || *read != *first {
					t.Fatalf("read: %+v %v", read, err)
				}
			}
			if cancellationIntentSnapshot(t, db, in.EntryID) != before {
				t.Fatal("read/replay rewrote evidence")
			}
			e, err := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil || e.State != "cancelled" || e.Input != original.Input || e.IdentityHash != original.IdentityHash {
				t.Fatal("local intent not safely cancelled")
			}
			if _, _, err := db.IssueSkillHubPublicationUploadGrant(ctx, in); err == nil {
				t.Fatal("issued after cancellation")
			}
			if g != nil {
				if _, err := db.CheckSkillHubPublicationUploadGrant(ctx, token, in, 1); err == nil {
					t.Fatal("cancelled upload remains active")
				}
				// Receipt can arrive after original cancel response or a restart.
				recordReceipt(t, reopened, receiptForGrant(g), true)
				ready := requestCancelIntent(t, reopened, in.Binding.ActorUID, in.EntryID, "ready")
				if ready.ID != first.ID || ready.RequestedAt != first.RequestedAt {
					t.Fatal("late receipt replaced cancellation")
				}
				if cancellationIntentSnapshot(t, db, in.EntryID) != before {
					t.Fatal("late receipt rewrote intent")
				}
			}
			if strings.Contains(before, token.Reveal()) && token.Reveal() != "" {
				t.Fatal("stored bearer")
			}
			for _, secret := range []string{in.Binding.Source.DeviceID, in.Binding.Source.LocalSkillID, in.RequestHash} {
				if strings.Contains(before, secret) {
					t.Fatal("retained unnecessary private metadata")
				}
			}
			publicationCounts(t, db, 1)
		})
	}
}

func TestPostgresSkillHubPublicationCancellationIntentAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"friend", "bot", "disabled_human", "transferred_original", "transferred_new", "disabled_bot", "rebound_bot"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := uploadGrantFixture(t)
			actor, denied := in.Binding.ActorUID, true
			switch mode {
			case "friend":
				actor = other
			case "bot":
				actor = in.Binding.BotUID
			case "disabled_human":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
			case "transferred_original", "transferred_new":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
				if mode == "transferred_new" {
					actor = other
				}
			case "disabled_bot":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
				denied = false
			case "rebound_bot":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
				denied = false
			}
			_, err := db.RequestSkillHubPublicationCancellation(context.Background(), actor, in.EntryID)
			if denied && !errors.Is(err, store.ErrSkillHubPublicationDenied) {
				t.Fatalf("authority: %v", err)
			}
			if !denied && err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_cancel_intents`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if denied && count != 0 {
				t.Fatal("denied request wrote an intent")
			}
		})
	}
}

func TestPostgresSkillHubPublicationCancellationIntentHistoricalRead(t *testing.T) {
	t.Parallel()
	db, _, in, other := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	first := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "waiting_receipt")
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
	recordReceipt(t, db, receiptForGrant(g), true)
	ctx := context.Background()
	r, err := db.GetSkillHubPublicationCancellationIntent(ctx, in.Binding.ActorUID, in.EntryID)
	if err != nil || r == nil || r.ID != first.ID || r.State != "ready" {
		t.Fatalf("original read: %+v %v", r, err)
	}
	for _, uid := range []int64{other, in.Binding.BotUID} {
		if r, err := db.GetSkillHubPublicationCancellationIntent(ctx, uid, in.EntryID); err != nil || r != nil {
			t.Fatal("history inherited")
		}
	}
	for _, uid := range []int64{in.Binding.ActorUID, other} {
		if _, err := db.RequestSkillHubPublicationCancellation(ctx, uid, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("transfer permitted mutation")
		}
	}
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
	if r, err := db.GetSkillHubPublicationCancellationIntent(ctx, in.Binding.ActorUID, in.EntryID); err != nil || r != nil {
		t.Fatal("disabled human read")
	}
	for _, pair := range []struct {
		uid int64
		id  string
	}{{0, in.EntryID}, {in.Binding.ActorUID, "bad"}} {
		if _, err := db.RequestSkillHubPublicationCancellation(ctx, pair.uid, pair.id); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
			t.Fatal("invalid request accepted")
		}
		if _, err := db.GetSkillHubPublicationCancellationIntent(ctx, pair.uid, pair.id); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
			t.Fatal("invalid read accepted")
		}
	}
}

func TestPostgresSkillHubPublicationCancellationIntentConcurrentReceipt(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan *store.SkillHubPublicationCancellationIntent, 8)
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r, err := db.RequestSkillHubPublicationCancellation(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil {
				errs <- err
			} else {
				results <- r
			}
		}()
		go func() {
			defer wg.Done()
			_, err := db.RecordSkillHubPublicationCloudReceipt(ctx, receiptForGrant(g))
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	final := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "ready")
	for r := range results {
		if r == nil || r.ID != final.ID || r.RequestedAt != final.RequestedAt || (r.State != "waiting_receipt" && r.State != "ready") {
			t.Fatal("parallel request diverged")
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_cancel_intents`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate intent", err)
	}
}

func TestPostgresSkillHubPublicationCancellationIntentUploadRace(t *testing.T) {
	t.Parallel()
	for i := 0; i < 5; i++ {
		db, _, in, _ := uploadGrantFixture(t)
		ctx := context.Background()
		var wg sync.WaitGroup
		var grant *store.SkillHubPublicationUploadGrant
		var grantErr, cancelErr error
		var intent *store.SkillHubPublicationCancellationIntent
		wg.Add(2)
		go func() { defer wg.Done(); grant, _, grantErr = db.IssueSkillHubPublicationUploadGrant(ctx, in) }()
		go func() {
			defer wg.Done()
			intent, cancelErr = db.RequestSkillHubPublicationCancellation(ctx, in.Binding.ActorUID, in.EntryID)
		}()
		wg.Wait()
		if cancelErr != nil || intent == nil {
			t.Fatal("cancel race", cancelErr)
		}
		if grantErr == nil {
			if intent.State != "waiting_receipt" {
				t.Fatal("lost in-flight upload possibility")
			}
			recordReceipt(t, db, receiptForGrant(grant), true)
			requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "ready")
		} else if intent.State != "local_only" {
			t.Fatalf("cancel won but local state incorrect: %+v %v", intent, grantErr)
		}
		if _, _, err := db.IssueSkillHubPublicationUploadGrant(ctx, in); err == nil {
			t.Fatal("issued after settled cancellation")
		}
	}
}

func TestPostgresSkillHubPublicationCancellationIntentAtomicFailure(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	ctx := context.Background()
	deliveryExec(t, db, `CREATE FUNCTION fail_cancel_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$;
		CREATE TRIGGER fail_cancel_insert BEFORE INSERT ON skillhub_publication_cancel_intents FOR EACH ROW EXECUTE FUNCTION fail_cancel_insert();`)
	if _, err := db.RequestSkillHubPublicationCancellation(ctx, in.Binding.ActorUID, in.EntryID); err == nil {
		t.Fatal("injected fault did not fail")
	}
	e, err := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
	if err != nil || e.State != "waiting_runtime" {
		t.Fatal("partial cancellation persisted")
	}
	if r, err := db.GetSkillHubPublicationCancellationIntent(ctx, in.Binding.ActorUID, in.EntryID); err != nil || r != nil {
		t.Fatal("partial intent persisted")
	}
	deliveryExec(t, db, `DROP TRIGGER fail_cancel_insert ON skillhub_publication_cancel_intents; DROP FUNCTION fail_cancel_insert();`)
	first := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "local_only")
	// Treat the first reply as lost and recover from the same durable intent.
	second := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "local_only")
	if *first != *second {
		t.Fatal("lost reply allocated replacement")
	}
}

func TestPostgresSkillHubPublicationCancellationIntentGuardsAndLegacy(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	ctx := context.Background()
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_cancel_intents(entry_id,cancel_id,requires_cloud) VALUES($1,$2,false)`, in.EntryID, "pubcancel_"+strings.Repeat("1", 32)); err == nil {
		t.Fatal("uncancelled intent accepted")
	}
	if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal(err)
	}
	if r, err := db.GetSkillHubPublicationCancellationIntent(ctx, in.Binding.ActorUID, in.EntryID); err != nil || r != nil {
		t.Fatal("legacy operation silently opted in")
	}
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_cancel_intents(entry_id,cancel_id,requires_cloud) VALUES($1,$2,true)`, in.EntryID, "pubcancel_"+strings.Repeat("2", 32)); err == nil {
		t.Fatal("contradictory upload evidence accepted")
	}
	requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "local_only")
	before := cancellationIntentSnapshot(t, db, in.EntryID)
	for _, statement := range []string{
		`UPDATE skillhub_publication_cancel_intents SET requires_cloud=true WHERE entry_id=$1`,
		`UPDATE skillhub_publication_cancel_intents SET requested_at=clock_timestamp() WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_cancel_intents WHERE entry_id=$1`,
		`UPDATE skillhub_publication_entries SET state='waiting_runtime' WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_entries WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(statement, in.EntryID); err == nil {
			t.Fatal("retained intent altered", statement)
		}
	}
	if cancellationIntentSnapshot(t, db, in.EntryID) != before {
		t.Fatal("guard failure")
	}
	if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal("legacy idempotent replay regressed", err)
	}
}
