package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func descriptionReleaseIntentMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000060_skillhub_publication_description_release_intent." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationDescriptionReleaseIntentMigrationMatchesStartup(t *testing.T) {
	b, err := os.ReadFile("../migrations/postgres/000060_skillhub_publication_description_release_intent.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(string(b)), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionReleaseIntentTable), " ") {
		t.Fatal("000060 differs from startup schema")
	}
}

func TestSkillHubPublicationDescriptionReleaseIntentDownIsSafeWhenAbsent(t *testing.T) {
	t.Parallel()
	db, _ := publicationEmptyDatabase(t)
	deliveryExec(t, db, descriptionReleaseIntentMigration(t, "down"))
}

func TestPostgresSkillHubPublicationDescriptionReleaseIntentPersistsExactTarget(t *testing.T) {
	t.Parallel()
	db, dsn, in, repairInput, _, foreign := repairBudgetFixture(t)
	ctx, actor := context.Background(), in.Binding.ActorUID
	app := acceptanceApplication(t, db, actor, repairInput)
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, acceptanceReport(*app)); err != nil {
		t.Fatal(err)
	}
	continuation, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, in.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	target := store.SkillHubPublicationCommitTarget{CandidateHash: app.Result.Derivation.DerivedCandidateHash,
		ManifestHash: strings.Repeat("a", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test",
		Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.0", UploadID: "upl_test",
		ReleaseAt: "2026-10-26T01:00:00.000Z", ConfirmVersionPublish: false}
	first, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *continuation, target)
	if err != nil || first == nil || first.ValidateFor(*continuation) != nil {
		t.Fatal("record exact human-confirmed target", err)
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	replayed, err := peer.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *continuation, target)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatal("restart did not recover exact release intent", err)
	}
	if got, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, in.EntryID); err != nil || !reflect.DeepEqual(first, got) {
		t.Fatal("exact current-authority read", err)
	}
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *continuation, store.SkillHubPublicationCommitTarget{
		CandidateHash: app.Result.Derivation.DerivedCandidateHash, ManifestHash: strings.Repeat("b", 64), PublisherUserID: "usr_test",
		PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.1", UploadID: "upl_other",
		ReleaseAt: "2026-10-26T01:00:01.000Z"}); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("different target replaced immutable release intent", err)
	}
	if _, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, foreign, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("foreign user read release intent", err)
	}
	for _, q := range []string{
		`UPDATE skillhub_publication_description_release_intents SET target=target WHERE application_id=$1`,
		`DELETE FROM skillhub_publication_description_release_intents WHERE application_id=$1`,
	} {
		if _, err := db.db.Exec(q, app.ID); err == nil {
			t.Fatal("release intent history mutated", q)
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, in.EntryID).Scan(&count); err != nil || count != 0 {
		t.Fatal("description confirmation was incorrectly turned into the legacy commit intent", err)
	}
	if _, err := db.db.Exec(`CREATE OR REPLACE FUNCTION fail_description_release_permit_test() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'transient permit insert failure' USING ERRCODE='40001'; END; $$`); err != nil {
		t.Fatal("create transient failure trigger function", err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER fail_description_release_permit_test BEFORE INSERT ON skillhub_publication_description_release_permits
		FOR EACH ROW EXECUTE FUNCTION fail_description_release_permit_test()`); err != nil {
		t.Fatal("create transient failure trigger", err)
	}
	if _, err := db.AuthorizeSkillHubPublicationDescriptionRelease(ctx, in.EntryID, app.ID, target); err == nil || errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("transient database failure was not preserved for retry", err)
	}
	if _, err := db.db.Exec(`DROP TRIGGER fail_description_release_permit_test ON skillhub_publication_description_release_permits`); err != nil {
		t.Fatal("drop transient failure trigger", err)
	}
	if _, err := db.db.Exec(`DROP FUNCTION fail_description_release_permit_test()`); err != nil {
		t.Fatal("drop transient failure trigger function", err)
	}

	permit, err := db.AuthorizeSkillHubPublicationDescriptionRelease(ctx, in.EntryID, app.ID, target)
	if err != nil || permit == nil || permit.Validate() != nil {
		t.Fatal("authorize exact derived target", err)
	}
	recovered, err := peer.AuthorizeSkillHubPublicationDescriptionRelease(ctx, in.EntryID, app.ID, target)
	if err != nil || !reflect.DeepEqual(permit, recovered) {
		t.Fatal("retry did not recover the same immutable permit", err)
	}
	if _, err := db.CancelSkillHubPublicationEntry(ctx, actor, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("local cancellation was allowed after permit issuance", err)
	}
	if _, err := db.RequestSkillHubPublicationCancellation(ctx, actor, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("cancellation intent path was allowed after permit issuance", err)
	}
	if _, err := db.db.Exec(`UPDATE skillhub_publication_entries SET state='cancelled' WHERE entry_id=$1`, in.EntryID); err == nil {
		t.Fatal("direct entry cancellation bypassed the permit guard")
	}
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_cancel_intents(entry_id,cancel_id,requires_cloud) VALUES($1,$2,true)`,
		in.EntryID, "pubcancel_"+strings.Repeat("f", 32)); err == nil {
		t.Fatal("direct cancellation intent bypassed the permit guard")
	}
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_commit_intents(entry_id,target,identity_hash) VALUES($1,'{}',$2)`, in.EntryID, strings.Repeat("f", 64)); err == nil {
		t.Fatal("derived candidate entered the legacy receipt commit path")
	}
	if _, err := db.db.Exec(`UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID); err != nil {
		t.Fatal("revoke runtime owner authority", err)
	}
	recovered, err = peer.AuthorizeSkillHubPublicationDescriptionRelease(ctx, in.EntryID, app.ID, target)
	if err != nil || !reflect.DeepEqual(permit, recovered) {
		t.Fatal("existing permit was not recoverable after authority changed", err)
	}
}

func TestSkillHubPublicationDescriptionReleasePermitMigrationMatchesStartup(t *testing.T) {
	b, err := os.ReadFile("../migrations/postgres/000061_skillhub_publication_description_release_permit.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(string(b)), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionReleasePermitTable), " ") {
		t.Fatal("000061 differs from startup permit schema")
	}
}

func TestSkillHubPublicationDescriptionReleasePermitDownIsSafeWhenAbsent(t *testing.T) {
	t.Parallel()
	db, _ := publicationEmptyDatabase(t)
	deliveryExec(t, db, descriptionReleasePermitMigration(t))
}

func descriptionReleasePermitMigration(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000061_skillhub_publication_description_release_permit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Roll back later FK dependants first, without changing applied 000061.
	return descriptionReconcileMigration(t, "down") + descriptionReleaseTerminalMigration(t, "down") + string(b)
}
