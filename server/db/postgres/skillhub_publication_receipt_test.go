package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func receiptForGrant(g *store.SkillHubPublicationUploadGrant) store.SkillHubPublicationCloudReceipt {
	return store.SkillHubPublicationCloudReceipt{Query: g.Input.Query(g.Epoch), DelegationID: g.ID, IdempotencyKey: g.IdempotencyKey,
		TaskID: "pubtask_11111111-1111-4111-8111-111111111111", EventID: "pubevent_22222222-2222-4222-8222-222222222222"}
}

func recordReceipt(t *testing.T, db *Adapter, r store.SkillHubPublicationCloudReceipt, cancelled bool) {
	t.Helper()
	got, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), r)
	if err != nil || got == nil || got.Receipt != r || got.LocalIntentCancelled != cancelled {
		t.Fatalf("receipt mismatch: %+v %v", got, err)
	}
}

func receiptSnapshot(t *testing.T, db *Adapter, id string) string {
	t.Helper()
	var out string
	if err := db.db.QueryRow(`SELECT row_to_json(r)::text FROM skillhub_publication_cloud_receipts r WHERE entry_id=$1`, id).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPostgresSkillHubPublicationReceiptRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := uploadGrantFixture(t)
	g, token := issueUpload(t, db, in)
	r := receiptForGrant(g)
	// Response lost, process reconstructed; renewal rotates the token/epoch.
	ageUpload(t, db, in.EntryID, "6 seconds")
	_, next := issueUpload(t, db, in)
	ageUpload(t, db, in.EntryID, "121 seconds")
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recordReceipt(t, reopened, r, false)
	before := receiptSnapshot(t, db, in.EntryID)
	for i := 0; i < 2; i++ {
		if err := reopened.CreateSchema(); err != nil {
			t.Fatal(err)
		}
		recordReceipt(t, reopened, r, false)
	}
	if receiptSnapshot(t, db, in.EntryID) != before {
		t.Fatal("replay rewrote evidence")
	}
	if strings.Contains(before, token.Reveal()) || strings.Contains(before, next.Reveal()) || strings.Contains(before, "node-a") {
		t.Fatal("secret/route retained in receipt")
	}
	e, err := db.GetSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
	if err != nil || e.State != "waiting_runtime" {
		t.Fatal("receipt changed execution state")
	}
	if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatalf("local-only cancel claimed Cloud cancellation: %v", err)
	}
	// Even a currently issued epoch cannot replace the original receipt epoch.
	changed := r
	changed.Query.Epoch = 2
	if _, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("acceptance epoch rewritten")
	}
}

func TestPostgresSkillHubPublicationReceiptStrictBindings(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	r := receiptForGrant(g)
	for name, change := range map[string]func(*store.SkillHubPublicationCloudReceipt){
		"unknown entry": func(x *store.SkillHubPublicationCloudReceipt) {
			x.Query.EntryID = "pubentry_" + strings.Repeat("f", 32)
		},
		"issuer":   func(x *store.SkillHubPublicationCloudReceipt) { x.Query.Issuer = "https://evil.example" },
		"audience": func(x *store.SkillHubPublicationCloudReceipt) { x.Query.Audience = "https://evil.example" },
		"grant":    func(x *store.SkillHubPublicationCloudReceipt) { x.DelegationID = "pubgrant_" + strings.Repeat("f", 32) },
		"key": func(x *store.SkillHubPublicationCloudReceipt) {
			x.IdempotencyKey = "pubcandidate_" + strings.Repeat("f", 32)
		},
		"request":      func(x *store.SkillHubPublicationCloudReceipt) { x.Query.RequestHash = strings.Repeat("f", 64) },
		"source":       func(x *store.SkillHubPublicationCloudReceipt) { x.Query.SourceSnapshotHash = strings.Repeat("f", 64) },
		"size":         func(x *store.SkillHubPublicationCloudReceipt) { x.Query.RequestBytes++ },
		"future epoch": func(x *store.SkillHubPublicationCloudReceipt) { x.Query.Epoch = 2 },
		"bad epoch":    func(x *store.SkillHubPublicationCloudReceipt) { x.Query.Epoch = 9 },
		"revision":     func(x *store.SkillHubPublicationCloudReceipt) { x.Query.Revision = 2 },
		"action":       func(x *store.SkillHubPublicationCloudReceipt) { x.Query.Action = "publication.commit" },
		"task format":  func(x *store.SkillHubPublicationCloudReceipt) { x.TaskID = "private-path" },
		"event format": func(x *store.SkillHubPublicationCloudReceipt) { x.EventID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := r
			change(&bad)
			if got, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), bad); err == nil || got != nil {
				t.Fatal("mismatched scope accepted")
			}
		})
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_cloud_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed check wrote evidence")
	}
	recordReceipt(t, db, r, false)
	for _, field := range []string{"task", "event"} {
		bad := r
		if field == "task" {
			bad.TaskID = strings.ReplaceAll(r.TaskID, "1", "3")
		} else {
			bad.EventID = strings.ReplaceAll(r.EventID, "2", "3")
		}
		if _, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), bad); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
			t.Fatalf("replacement accepted: %v", err)
		}
	}
}

func TestPostgresSkillHubPublicationReceiptAfterAuthorityLoss(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "human disabled", "transfer", "body changed", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := uploadGrantFixture(t)
			g, token := issueUpload(t, db, in)
			switch mode {
			case "cancel":
				if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			case "human disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			case "transfer":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "body changed":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
			case "disconnect":
				if _, err := db.BumpBotConnectionGeneration(in.Binding.BotUID); err != nil {
					t.Fatal(err)
				}
			}
			recordReceipt(t, db, receiptForGrant(g), mode == "cancel")
			if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, 1); err == nil {
				t.Fatal("receipt restored revoked authority")
			}
			if mode == "cancel" {
				e, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
				if err != nil || e.State != "cancelled" {
					t.Fatal("late receipt revived cancellation")
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationReceiptConcurrentAndCancel(t *testing.T) {
	t.Parallel()
	for _, raceCancel := range []bool{false, true} {
		db, _, in, _ := uploadGrantFixture(t)
		g, _ := issueUpload(t, db, in)
		r := receiptForGrant(g)
		start := make(chan struct{})
		errs := make(chan error, 9)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), r)
				errs <- err
			}()
		}
		cancelResult := make(chan error, 1)
		if raceCancel {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
				cancelResult <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		cancelled := false
		if raceCancel {
			err := <-cancelResult
			cancelled = err == nil
			if err != nil && !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatal(err)
			}
		}
		recordReceipt(t, db, r, cancelled)
		var count int
		if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_cloud_receipts`).Scan(&count); err != nil || count != 1 {
			t.Fatal("duplicate evidence")
		}
		e, err := db.GetSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
		if err != nil || (e.State == "cancelled") != cancelled {
			t.Fatal("cancel ordering lost")
		}
	}
}

func TestPostgresSkillHubPublicationReceiptCrossEntryCollision(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	first := receiptForGrant(g)
	recordReceipt(t, db, first, false)
	b := in.Binding
	e, _, err := db.CreateSkillHubPublicationEntry(context.Background(), store.SkillHubPublicationEntryInput{ActorUID: b.ActorUID, BotUID: b.BotUID, Entrypoint: "workspace", RequestID: "second-receipt", Source: b.Source}, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	in.EntryID = e.ID
	lease := deliveryClaim(t, db, e.ID, b)
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
		t.Fatal(err)
	}
	recordCaptureForTest(t, db, captureForUpload(in))
	g, _ = issueUpload(t, db, in)
	second := receiptForGrant(g)
	for _, kind := range []string{"task", "event"} {
		bad := second
		if kind == "task" {
			bad.EventID = strings.ReplaceAll(bad.EventID, "2", "3")
		} else {
			bad.TaskID = strings.ReplaceAll(bad.TaskID, "1", "3")
		}
		if _, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), bad); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
			t.Fatalf("cross-entry %s reused: %v", kind, err)
		}
	}
	second.EventID = strings.ReplaceAll(second.EventID, "2", "3")
	second.TaskID = strings.ReplaceAll(second.TaskID, "1", "3")
	recordReceipt(t, db, second, false)
}

func TestPostgresSkillHubPublicationReceiptAtomicFailureAndGuards(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	r := receiptForGrant(g)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.RecordSkillHubPublicationCloudReceipt(ctx, r); err == nil {
		t.Fatal("ignored cancellation")
	}
	deliveryExec(t, db, `CREATE FUNCTION reject_cloud_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$;
	CREATE TRIGGER reject_cloud_receipt BEFORE INSERT ON skillhub_publication_cloud_receipts FOR EACH ROW EXECUTE FUNCTION reject_cloud_receipt();`)
	if got, err := db.RecordSkillHubPublicationCloudReceipt(context.Background(), r); err == nil || got != nil {
		t.Fatal("failed insert acknowledged")
	}
	if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal("failed receipt blocked cancellation")
	}
	deliveryExec(t, db, `DROP TRIGGER reject_cloud_receipt ON skillhub_publication_cloud_receipts`)
	recordReceipt(t, db, r, true)
	before := receiptSnapshot(t, db, in.EntryID)
	for _, sql := range []string{`UPDATE skillhub_publication_cloud_receipts SET acceptance_epoch=2`, `DELETE FROM skillhub_publication_cloud_receipts`, `DELETE FROM skillhub_publication_entries`} {
		if _, err := db.db.Exec(sql); err == nil {
			t.Fatal("retained evidence mutated/deleted")
		}
	}
	if receiptSnapshot(t, db, in.EntryID) != before {
		t.Fatal("evidence changed")
	}
}
