package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationDescriptionReleaseOutcomeHistoricalRead(t *testing.T) {
	t.Parallel()
	db, dsn, in, repairInput, _, foreign := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	app := acceptanceApplication(t, db, actor, repairInput)
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, acceptanceReport(*app)); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	target := store.SkillHubPublicationCommitTarget{CandidateHash: app.Result.Derivation.DerivedCandidateHash,
		ManifestHash: strings.Repeat("a", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher",
		SkillID: "publisher/example", Version: "1.0.0", UploadID: "upl_test", ReleaseAt: "2026-10-26T01:00:00.000Z"}
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *c, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("read issued missing permit", err)
	}
	permit, err := db.AuthorizeSkillHubPublicationDescriptionRelease(ctx, id, app.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil || first == nil || first.ValidateFor(actor, id) != nil || !reflect.DeepEqual(first.Permit, *permit) {
		t.Fatal("observation", first, err)
	}
	// Historical evidence survives Runtime loss/transfer. It is not a new grant
	// to the Bot's new owner and does not require the private task to remain live.
	deliveryExec(t, db, `UPDATE bot_config SET enabled=false,owner_id=$1,body_id='offline-body' WHERE user_id=$2`, foreign, in.Binding.BotUID)
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	for i := 0; i < 2; i++ {
		got, err := peer.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatal("historical recovery", got, err)
		}
	}
	for _, uid := range []int64{foreign, in.Binding.BotUID} {
		if _, err := peer.GetSkillHubPublicationDescriptionReleaseObservation(ctx, uid, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("foreign reader", uid, err)
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_description_release_permits WHERE application_id=$1`, app.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("permit mutation", count, err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy commit mutation", count, err)
	}
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
	if _, err := peer.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("disabled publisher read", err)
	}
}
