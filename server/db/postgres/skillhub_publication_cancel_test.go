package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationCancellationLifecycle(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"pending", "leased", "source_verified", "needs_user_action"} {
		t.Run(phase, func(t *testing.T) {
			db, dsn, id, b, _ := publicationDeliveryFixture(t)
			ctx := context.Background()
			original, err := db.GetSkillHubPublicationEntry(ctx, b.ActorUID, id)
			if err != nil {
				t.Fatal(err)
			}
			var lease *store.SkillHubPublicationDeliveryLeaseRecord
			if phase != "pending" {
				lease = deliveryClaim(t, db, id, b)
			}
			if phase == "source_verified" || phase == "needs_user_action" {
				code := ""
				if phase == "needs_user_action" {
					code = "publication.source_changed"
				}
				if err := db.FinishSkillHubPublicationDelivery(ctx, *lease, code); err != nil {
					t.Fatal(err)
				}
			}
			var before string
			if err := db.db.QueryRow(`SELECT row_to_json(o)::text FROM skillhub_publication_outbox o WHERE entry_id=$1`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			cancelled, err := db.CancelSkillHubPublicationEntry(ctx, b.ActorUID, id)
			if err != nil || cancelled == nil || cancelled.State != "cancelled" || cancelled.Input != original.Input || cancelled.IdentityHash != original.IdentityHash || !cancelled.CreatedAt.Equal(original.CreatedAt) {
				t.Fatalf("cancel: %+v %v", cancelled, err)
			}
			reopened := &Adapter{}
			if err := reopened.Open(dsn); err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				repeat, err := reopened.CancelSkillHubPublicationEntry(ctx, b.ActorUID, id)
				if err != nil || repeat.State != "cancelled" {
					t.Fatalf("repeated cancel: %+v %v", repeat, err)
				}
			}
			replay, yes, err := reopened.CreateSkillHubPublicationEntry(ctx, original.Input, store.DefaultSkillHubPublicationEntryPolicy())
			if err != nil || !yes || replay.ID != id || replay.State != "cancelled" {
				t.Fatalf("replay resurrected entry: %+v %v", replay, err)
			}
			byRequest, err := reopened.GetSkillHubPublicationEntryByRequest(ctx, b.ActorUID, original.Input.RequestID)
			if err != nil || byRequest.State != "cancelled" {
				t.Fatal("lost cancellation response not recoverable")
			}
			if ids, err := reopened.ListSkillHubPublicationDeliveries(ctx, b.BotUID, 32); err != nil || len(ids) != 0 {
				t.Fatalf("cancelled entry scheduled: %v %v", ids, err)
			}
			s := store.SkillHubPublicationRuntimeSelector{ActorUID: b.ActorUID, BotUID: b.BotUID, DeviceID: b.Source.DeviceID, BodyID: b.Source.BodyID, InstallationID: b.Source.InstallationID}
			if ids, err := reopened.ListSkillHubPublicationRuntimeDeliveries(ctx, s, 32); err != nil || len(ids) != 0 {
				t.Fatalf("cancelled runtime entry scheduled: %v %v", ids, err)
			}
			if _, err := reopened.ClaimSkillHubPublicationDelivery(ctx, id, b); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
				t.Fatalf("claim after cancellation: %v", err)
			}
			if lease != nil {
				if err := reopened.FinishSkillHubPublicationDelivery(ctx, *lease, ""); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
					t.Fatalf("late receipt: %v", err)
				}
			}
			status, err := reopened.GetSkillHubPublicationSourceStatus(ctx, b.ActorUID, id)
			if err != nil || status.State != "cancelled" {
				t.Fatalf("source status: %+v %v", status, err)
			}
			var after string
			if err := db.db.QueryRow(`SELECT row_to_json(o)::text FROM skillhub_publication_outbox o WHERE entry_id=$1`, id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatal("cancellation erased/rewrote lease or receipt evidence")
			}
			publicationCounts(t, db, 1)
		})
	}
}

func TestPostgresSkillHubPublicationCancellationAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"friend", "bot", "disabled_actor", "transfer_original", "transfer_new", "disabled_bot", "rebound_bot"} {
		t.Run(mode, func(t *testing.T) {
			db, _, id, b, other := publicationDeliveryFixture(t)
			actor := b.ActorUID
			wantDenied := true
			switch mode {
			case "friend":
				actor = other
			case "bot":
				actor = b.BotUID
			case "disabled_actor":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
			case "transfer_original", "transfer_new":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, b.BotUID)
				if mode == "transfer_new" {
					actor = other
				}
			case "disabled_bot":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, b.BotUID)
				wantDenied = false
			case "rebound_bot":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='different-body' WHERE user_id=$1`, b.BotUID)
				wantDenied = false
			}
			_, err := db.CancelSkillHubPublicationEntry(context.Background(), actor, id)
			if wantDenied && !errors.Is(err, store.ErrSkillHubPublicationDenied) {
				t.Fatalf("authority: %v", err)
			}
			if !wantDenied && err != nil {
				t.Fatal(err)
			}
			var state string
			if err := db.db.QueryRow(`SELECT state FROM skillhub_publication_entries WHERE entry_id=$1`, id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if (state == "cancelled") == wantDenied {
				t.Fatalf("unexpected mutation: %s", state)
			}
		})
	}
}

func TestPostgresSkillHubPublicationCancellationConcurrentReceipt(t *testing.T) {
	t.Parallel()
	for i := 0; i < 6; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			db, dsn, id, b, _ := publicationDeliveryFixture(t)
			lease := deliveryClaim(t, db, id, b)
			other := &Adapter{}
			if err := other.Open(dsn); err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			start := make(chan struct{})
			var wg sync.WaitGroup
			var cancelErr, finishErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, cancelErr = db.CancelSkillHubPublicationEntry(context.Background(), b.ActorUID, id)
			}()
			go func() {
				defer wg.Done()
				<-start
				finishErr = other.FinishSkillHubPublicationDelivery(context.Background(), *lease, "")
			}()
			close(start)
			wg.Wait()
			if cancelErr != nil || (finishErr != nil && !errors.Is(finishErr, store.ErrSkillHubPublicationSettled)) {
				t.Fatalf("cancel=%v receipt=%v", cancelErr, finishErr)
			}
			e, err := db.GetSkillHubPublicationEntry(context.Background(), b.ActorUID, id)
			if err != nil || e.State != "cancelled" {
				t.Fatal("concurrent receipt resurrected intent")
			}
			if err := other.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
				t.Fatalf("post-cancel receipt accepted: %v", err)
			}
		})
	}
}

func TestPostgresSkillHubPublicationCancellationConcurrentClaim(t *testing.T) {
	t.Parallel()
	db, _, id, b, _ := publicationDeliveryFixture(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, err := db.CancelSkillHubPublicationEntry(context.Background(), b.ActorUID, id)
				results <- err
			} else {
				_, err := db.ClaimSkillHubPublicationDelivery(context.Background(), id, b)
				results <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, store.ErrSkillHubPublicationSettled) && !errors.Is(err, store.ErrSkillHubPublicationBusy) {
			t.Fatal(err)
		}
	}
	if _, err := db.ClaimSkillHubPublicationDelivery(context.Background(), id, b); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatalf("cancelled work reclaimed: %v", err)
	}
	publicationCounts(t, db, 1)
}
