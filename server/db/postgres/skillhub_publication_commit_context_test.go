package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationCommitContextPrivacyAndRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, in, c, other := commitFixture(t)
	ctx := context.Background()
	before := cloudStatusLedgerSnapshot(t, db, in.EntryID)
	for _, current := range []bool{false, true} {
		s, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, current)
		if err != nil || s == nil || s.Receipt == nil || *s.Receipt != c.Receipt || s.Intent != nil {
			t.Fatalf("context %v: %+v %v", current, s, err)
		}
		for _, actor := range []int64{other, in.Binding.BotUID} {
			got, err := db.GetSkillHubPublicationCommitContext(ctx, actor, in.EntryID, current)
			if err != nil || got != nil {
				t.Fatal("disclosed to other actor", err)
			}
		}
	}
	if cloudStatusLedgerSnapshot(t, db, in.EntryID) != before {
		t.Fatal("context read wrote ledger")
	}
	i := recordCommit(t, db, in.Binding.ActorUID, c)
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, true)
	if err != nil || got == nil || got.Intent == nil || *got.Intent != *i {
		t.Fatal("restart lost exact intent", err)
	}
	// Offline transport alone is not a reason to block an uploaded candidate.
	deliveryExec(t, db, `UPDATE bot_connection_generations SET generation=generation+1 WHERE bot_uid=$1`, in.Binding.BotUID)
	if got, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, true); err != nil || got == nil {
		t.Fatal("offline blocks candidate", err)
	}
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1,enabled=false,body_id='changed' WHERE user_id=$2`, other, in.Binding.BotUID)
	if _, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, true); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("stale owner authorized", err)
	}
	if got, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, false); err != nil || got == nil || got.Intent == nil {
		t.Fatal("original actor lost history", err)
	}
	if got, err := db.GetSkillHubPublicationCommitContext(ctx, other, in.EntryID, false); err != nil || got != nil {
		t.Fatal("new owner inherited history", err)
	}
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
	if got, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, false); err != nil || got != nil {
		t.Fatal("disabled actor history", err)
	}
}

func TestPostgresSkillHubPublicationCommitContextMissingReceiptAndCancellation(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	ctx := context.Background()
	s, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, true)
	if err != nil || s == nil || s.Receipt != nil || s.Intent != nil {
		t.Fatal("invented acceptance", err)
	}
	g, _ := issueUpload(t, db, in)
	if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal(err)
	}
	recordReceipt(t, db, receiptForGrant(g), true)
	if _, err := db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, true); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("cancelled can plan", err)
	}
	s, err = db.GetSkillHubPublicationCommitContext(ctx, in.Binding.ActorUID, in.EntryID, false)
	if err != nil || s == nil || s.Entry.State != "cancelled" || s.Receipt == nil {
		t.Fatal("lost cancellation history", err)
	}
}
