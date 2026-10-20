package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func readCloudStatus(t *testing.T, db *Adapter, uid int64, id, state string, recorded bool) *store.SkillHubPublicationCloudStatus {
	t.Helper()
	s, err := db.GetSkillHubPublicationCloudStatus(context.Background(), uid, id)
	if err != nil || s == nil || s.EntryID != id || s.ActorUID != uid || s.LocalIntentState != state || s.ObservedAt.IsZero() || (s.ReceiptRecordedAt != nil) != recorded {
		t.Fatalf("cloud status: %+v %v", s, err)
	}
	if recorded && (s.ReceiptRecordedAt.IsZero() || s.ReceiptRecordedAt.After(s.ObservedAt)) {
		t.Fatal("invalid receipt time")
	}
	return s
}

func cloudStatusLedgerSnapshot(t *testing.T, db *Adapter, id string) string {
	t.Helper()
	var snapshot string
	if err := db.db.QueryRow(`SELECT json_build_object('entry',row_to_json(e),
		'outbox',(SELECT row_to_json(o) FROM skillhub_publication_outbox o WHERE o.entry_id=e.entry_id),
		'grant',(SELECT row_to_json(g) FROM skillhub_publication_upload_grants g WHERE g.entry_id=e.entry_id),
		'receipt',(SELECT row_to_json(r) FROM skillhub_publication_cloud_receipts r WHERE r.entry_id=e.entry_id))::text
		FROM skillhub_publication_entries e WHERE e.entry_id=$1`, id).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPostgresSkillHubPublicationCloudStatusLifecycle(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := uploadGrantFixture(t)
	readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "waiting_runtime", false)
	g, _ := issueUpload(t, db, in)
	// A grant alone is not evidence that Cloud accepted anything.
	before := cloudStatusLedgerSnapshot(t, db, in.EntryID)
	for i := 0; i < 3; i++ {
		readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "waiting_runtime", false)
	}
	if cloudStatusLedgerSnapshot(t, db, in.EntryID) != before {
		t.Fatal("read wrote ledger")
	}
	r := receiptForGrant(g)
	recordReceipt(t, db, r, false)
	first := readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "waiting_runtime", true)
	ageUpload(t, db, in.EntryID, "6 seconds")
	issueUpload(t, db, in)
	ageUpload(t, db, in.EntryID, "121 seconds")
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before = cloudStatusLedgerSnapshot(t, db, in.EntryID)
	for i := 0; i < 3; i++ {
		s := readCloudStatus(t, reopened, in.Binding.ActorUID, in.EntryID, "waiting_runtime", true)
		if !s.ReceiptRecordedAt.Equal(*first.ReceiptRecordedAt) {
			t.Fatal("receipt timestamp changed")
		}
	}
	if cloudStatusLedgerSnapshot(t, db, in.EntryID) != before {
		t.Fatal("recovery read mutated ledger")
	}
	if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("read enabled local-only cancellation")
	}
}

func TestPostgresSkillHubPublicationCloudStatusLateReceiptAfterCancellation(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal(err)
	}
	readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "cancelled", false)
	recordReceipt(t, db, receiptForGrant(g), true)
	before := cloudStatusLedgerSnapshot(t, db, in.EntryID)
	readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "cancelled", true)
	if cloudStatusLedgerSnapshot(t, db, in.EntryID) != before {
		t.Fatal("read revived intent")
	}
}

func TestPostgresSkillHubPublicationCloudStatusPrivacy(t *testing.T) {
	t.Parallel()
	db, _, in, other := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	recordReceipt(t, db, receiptForGrant(g), false)
	ctx := context.Background()
	for _, uid := range []int64{other, in.Binding.BotUID, 999999} {
		if s, err := db.GetSkillHubPublicationCloudStatus(ctx, uid, in.EntryID); err != nil || s != nil {
			t.Fatal("private receipt disclosed")
		}
	}
	if s, err := db.GetSkillHubPublicationCloudStatus(ctx, in.Binding.ActorUID, "pubentry_"+strings.Repeat("f", 32)); err != nil || s != nil {
		t.Fatal("unknown entry exists")
	}
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1,enabled=false,body_id='different' WHERE user_id=$2`, other, in.Binding.BotUID)
	readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "waiting_runtime", true)
	if s, err := db.GetSkillHubPublicationCloudStatus(ctx, other, in.EntryID); err != nil || s != nil {
		t.Fatal("new owner inherited receipt")
	}
	for _, accountType := range []string{"human", "bot", "service"} {
		state := 0
		if accountType == "human" {
			state = 1
		}
		deliveryExec(t, db, `UPDATE users SET account_type=$1,state=$2 WHERE id=$3`, accountType, state, in.Binding.ActorUID)
		if s, err := db.GetSkillHubPublicationCloudStatus(ctx, in.Binding.ActorUID, in.EntryID); err != nil || s != nil {
			t.Fatal("disabled/nonhuman can read")
		}
	}
	for _, tc := range []struct {
		uid int64
		id  string
	}{{0, in.EntryID}, {in.Binding.ActorUID, "bad"}} {
		if _, err := db.GetSkillHubPublicationCloudStatus(ctx, tc.uid, tc.id); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
			t.Fatal("accepted invalid scope")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.GetSkillHubPublicationCloudStatus(cancelled, in.Binding.ActorUID, in.EntryID); err == nil {
		t.Fatal("cancelled context ignored")
	}
}

func TestPostgresSkillHubPublicationCloudStatusConcurrentReceipt(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	// Reads may observe either committed side of the callback, without changing
	// local cancellation or inventing a Cloud execution state.
	if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				s, err := db.GetSkillHubPublicationCloudStatus(context.Background(), in.Binding.ActorUID, in.EntryID)
				if err != nil || s == nil {
					t.Errorf("read: %v", err)
					return
				}
				if s.LocalIntentState != "cancelled" || s.ObservedAt.IsZero() || (s.ReceiptRecordedAt != nil && s.ReceiptRecordedAt.After(s.ObservedAt)) {
					t.Error("inconsistent snapshot")
				}
			}
		}()
	}
	recordReceipt(t, db, receiptForGrant(g), true)
	wg.Wait()
	readCloudStatus(t, db, in.Binding.ActorUID, in.EntryID, "cancelled", true)
}
