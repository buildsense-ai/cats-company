package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func descriptionReleaseTerminalMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000064_skillhub_publication_description_release_terminal." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationDescriptionReleaseTerminalMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(descriptionReleaseTerminalMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionReleaseTerminalTable), " ") {
		t.Fatal("000064 differs from startup")
	}
}

func terminalReleaseFixture(t *testing.T) (*Adapter, string, int64, int64, store.SkillHubPublicationDescriptionReleaseObservation, store.SkillHubPublicationDescriptionReleaseTerminal) {
	t.Helper()
	db, dsn, in, input, _, foreign := repairBudgetFixture(t)
	ctx, actor := context.Background(), in.Binding.ActorUID
	app := acceptanceApplication(t, db, actor, input)
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, acceptanceReport(*app)); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, in.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	target := store.SkillHubPublicationCommitTarget{CandidateHash: app.Result.Derivation.DerivedCandidateHash, ManifestHash: strings.Repeat("a", 64),
		PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.0", UploadID: "upl_test", ReleaseAt: "2026-10-26T01:00:00.000Z"}
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *c, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthorizeSkillHubPublicationDescriptionRelease(ctx, in.EntryID, app.ID, target); err != nil {
		t.Fatal(err)
	}
	o, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, in.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	terminal := store.SkillHubPublicationDescriptionReleaseTerminal{ApplicationID: app.ID, EntryID: in.EntryID, PermitID: o.Permit.ID,
		IntentHash: o.Intent.IdentityHash, Target: target, State: "published", Result: &store.SkillHubPublicationDescriptionReleaseResult{
			SkillID: target.SkillID, Version: target.Version, PublishedAt: target.ReleaseAt, Checksum: strings.Repeat("b", 64), PackageID: "pkg_12345678-1234-4123-8123-123456789abc"}}
	return db, dsn, actor, foreign, *o, terminal
}

func TestPostgresSkillHubPublicationDescriptionTerminalConcurrentRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, actor, foreign, o, terminal := terminalReleaseFixture(t)
	ctx, id := context.Background(), terminal.EntryID
	if r, err := db.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, id); err != nil || r != nil {
		t.Fatal("unrecorded result", r, err)
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var wg sync.WaitGroup
	results := make(chan *store.SkillHubPublicationDescriptionReleaseTerminalReceipt, 4)
	errs := make(chan error, 4)
	for _, adapter := range []*Adapter{db, peer, db, peer} {
		wg.Add(1)
		go func(a *Adapter) {
			defer wg.Done()
			r, err := a.RecordSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, o, terminal)
			results <- r
			errs <- err
		}(adapter)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *store.SkillHubPublicationDescriptionReleaseTerminalReceipt
	for r := range results {
		if r == nil || r.ValidateFor(o) != nil {
			t.Fatal("invalid receipt", r)
		}
		if first == nil {
			first = r
		} else if !reflect.DeepEqual(r, first) {
			t.Fatal("duplicate changed receipt")
		}
	}
	if got, err := peer.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, id); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("restart recovery", got, err)
	}
	changed := terminal
	p := *terminal.Result
	p.Checksum = strings.Repeat("c", 64)
	changed.Result = &p
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, o, changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("changed terminal replaced evidence", err)
	}
	for _, q := range []string{`UPDATE skillhub_publication_description_release_terminals SET payload=payload WHERE application_id=$1`,
		`DELETE FROM skillhub_publication_description_release_terminals WHERE application_id=$1`} {
		if _, err := db.db.Exec(q, terminal.ApplicationID); err == nil {
			t.Fatal("terminal history mutable")
		}
	}
	if _, err := db.db.Exec(descriptionReleaseTerminalMigration(t, "down")); err == nil {
		t.Fatal("nonempty rollback deleted evidence")
	}
	deliveryExec(t, db, `UPDATE bot_config SET enabled=false,owner_id=$1,body_id='offline' WHERE user_id=(SELECT bot_uid FROM skillhub_publication_entries WHERE entry_id=$2)`, foreign, id)
	if got, err := peer.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, id); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("offline/transferred bot erased evidence", err)
	}
	if _, err := db.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, foreign, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("new owner read old publication", err)
	}
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
	if _, err := db.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("disabled reader", err)
	}
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, o, terminal); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("disabled writer", err)
	}
}

func TestPostgresSkillHubPublicationDescriptionTerminalFailureAndMigration(t *testing.T) {
	t.Parallel()
	db, _, actor, _, o, terminal := terminalReleaseFixture(t)
	for _, direction := range []string{"down", "down", "up", "up"} {
		deliveryExec(t, db, descriptionReleaseTerminalMigration(t, direction))
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	// Exercise the SQL guard directly, not only the Go validation path.
	for _, mode := range []string{"progress", "wrong-target", "wrong-permit", "private-error", "extra-field"} {
		raw, err := json.Marshal(terminal)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "progress":
			payload["state"] = "queued"
		case "wrong-target":
			payload["target"].(map[string]any)["version"] = "2.0.0"
		case "wrong-permit":
			payload["permitId"] = "pubdescpermit_" + strings.Repeat("f", 32)
		case "private-error":
			payload["state"], payload["result"], payload["errorCode"] = "publish_failed", nil, "/private/error"
		case "extra-field":
			payload["privatePath"] = "/private/source"
		}
		raw, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO skillhub_publication_description_release_terminals
			(application_id,entry_id,permit_id,intent_identity_hash,payload,identity_hash) VALUES($1,$2,$3,$4,$5,$6)`,
			terminal.ApplicationID, terminal.EntryID, terminal.PermitID, terminal.IntentHash, raw, strings.Repeat("a", 64)); err == nil {
			t.Fatal("SQL accepted invalid terminal", mode)
		}
	}
	terminal.State, terminal.Result, terminal.ErrorCode = "publish_failed", nil, "skillhub.publication_description_release_commit_rejected"
	r, err := db.RecordSkillHubPublicationDescriptionReleaseTerminal(context.Background(), actor, o, terminal)
	if err != nil || r == nil || r.Terminal.State != "publish_failed" {
		t.Fatal("failure not durable", r, err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, terminal.EntryID).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy publish side effect", count, err)
	}
	if _, err := db.CancelSkillHubPublicationEntry(context.Background(), actor, terminal.EntryID); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("terminal receipt cleared cancellation fence", err)
	}
}
