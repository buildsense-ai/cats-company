package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func repairModelMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000066_skillhub_publication_repair_model_call." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationRepairModelCallMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(repairModelMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationRepairModelCallsTable), " ") {
		t.Fatal("000066 differs from startup")
	}
}

func repairModelFixture(t *testing.T) (*Adapter, string, store.SkillHubPublicationUploadInput, store.SkillHubPublicationRepairLease, store.SkillHubPublicationRepairModelCallInput, int64) {
	t.Helper()
	db, dsn, in, task, _, other := repairBudgetFixture(t)
	createRepairBudget(t, db, in.Binding.ActorUID, task)
	r := claimRepairBudget(t, db, in.Binding.ActorUID, in.EntryID, store.SkillHubPublicationRepairRoundInput{
		RequestID: "model-round-1", Evidence: task.Evidence}, "original-worker")
	return db, dsn, in, r.Lease, store.SkillHubPublicationRepairModelCallInput{RequestHash: strings.Repeat("a", 64)}, other
}

func startRepairModel(t *testing.T, db *Adapter, actor int64, l store.SkillHubPublicationRepairLease, in store.SkillHubPublicationRepairModelCallInput, wantDispatch bool) *store.SkillHubPublicationRepairModelCall {
	t.Helper()
	c, dispatch, err := db.StartSkillHubPublicationRepairModelCall(context.Background(), actor, l, in)
	if err != nil || c == nil || c.Validate() != nil || dispatch != wantDispatch {
		t.Fatalf("model start dispatch=%v want=%v call=%v err=%v", dispatch, wantDispatch, c != nil, err)
	}
	return c
}

func TestPostgresSkillHubPublicationRepairModelCallRecoveryAndReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, l, request, _ := repairModelFixture(t)
	ctx, actor := context.Background(), in.Binding.ActorUID
	first := startRepairModel(t, db, actor, l, request, true)
	// Simulate lost committed response/reconstruction: no second dispatch grant.
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got := startRepairModel(t, reopened, actor, l, request, false); !reflect.DeepEqual(got, first) {
		t.Fatal("lost reply reset dispatch")
	}
	request.RequestHash = strings.Repeat("b", 64)
	if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, request); !errors.Is(err, store.ErrSkillHubPublicationConflict) || grant {
		t.Fatal("changed model request accepted", err)
	}
	output := store.SkillHubPublicationRepairModelOutput{Text: "untrusted format proposal 中文"}
	complete, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, output)
	if err != nil || complete == nil || complete.Output == nil || *complete.Output != output {
		t.Fatal("complete", err)
	}
	if replay, err := reopened.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, output); err != nil || !reflect.DeepEqual(replay, complete) {
		t.Fatal("lost complete reply not recovered", err)
	}
	if got, err := reopened.GetSkillHubPublicationRepairModelCall(ctx, actor, in.EntryID, 1); err != nil || !reflect.DeepEqual(got, complete) {
		t.Fatal("private output recovery", err)
	}
	if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "replacement"}); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("immutable output changed", err)
	}
	if _, err := db.db.Exec(repairModelMigration(t, "down")); err == nil {
		t.Fatal("rollback erased dispatch")
	}
	if got := getRepairBudget(t, db, actor, in.EntryID); len(got.Rounds) != 1 || got.State() != "running" {
		t.Fatal("model journal changed budget/state")
	}
	complete.Output.Text = "caller mutation"
	got, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, in.EntryID, 1)
	if err != nil || got.Output.Text != output.Text {
		t.Fatal("caller altered retained output", err)
	}
	t.Log("lost dispatch/output replies recover one immutable call, no second paid dispatch authorization")
}

func TestPostgresSkillHubPublicationRepairModelCallConcurrentRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, in, l, request, _ := repairModelFixture(t)
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var wg sync.WaitGroup
	grants := make(chan bool, 8)
	errs := make(chan error, 8)
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := db
			if i%2 == 0 {
				a = peer
			}
			c, grant, err := a.StartSkillHubPublicationRepairModelCall(context.Background(), in.Binding.ActorUID, l, request)
			errs <- err
			grants <- grant
			if c != nil {
				ids <- c.CallID
			}
		}(i)
	}
	wg.Wait()
	close(grants)
	close(errs)
	close(ids)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	wins := 0
	for grant := range grants {
		if grant {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("dispatch winners", wins)
	}
	identity := ""
	for id := range ids {
		if identity != "" && identity != id {
			t.Fatal("multiple call identities")
		}
		identity = id
	}
	t.Log("eight concurrent starts yield one dispatch authorization and the same retained call")
}

func TestPostgresSkillHubPublicationRepairModelCallLeaseFencing(t *testing.T) {
	t.Parallel()
	for _, withOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "retained_output"}[withOutput], func(t *testing.T) {
			db, _, in, l, request, _ := repairModelFixture(t)
			ctx, actor := context.Background(), in.Binding.ActorUID
			first := startRepairModel(t, db, actor, l, request, true)
			output := store.SkillHubPublicationRepairModelOutput{Text: "saved output"}
			if withOutput {
				if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, output); err != nil {
					t.Fatal(err)
				}
			}
			expireRepairBudget(t, db, in.EntryID)
			if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, request); !errors.Is(err, store.ErrSkillHubPublicationStale) || grant {
				t.Fatal("expired dispatch", err)
			}
			if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, output); !errors.Is(err, store.ErrSkillHubPublicationStale) {
				t.Fatal("expired complete", err)
			}
			next := claimRepairBudget(t, db, actor, in.EntryID, l.Input, "replacement-worker").Lease
			got := startRepairModel(t, db, actor, next, request, false)
			if got.CallID != first.CallID || !sameRepairModelLease(got.DispatchLease, first.DispatchLease) || (got.Output != nil) != withOutput {
				t.Fatal("takeover replaced call evidence")
			}
			for _, lease := range []store.SkillHubPublicationRepairLease{l, next} {
				if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, lease, first.CallID, output); !errors.Is(err, store.ErrSkillHubPublicationStale) {
					t.Fatal("late/replacement worker injected output", err)
				}
			}
			if !withOutput {
				finishRepairBudget(t, db, actor, next, store.SkillHubPublicationRepairResult{Outcome: "stopped", Evidence: next.Input.Evidence, Code: "model_result_uncertain"})
				if getRepairBudget(t, db, actor, in.EntryID).State() != "stopped" {
					t.Fatal("unknown call did not stop")
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationRepairModelCallAuthorityAndPrivacy(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"transfer", "cancel", "human_disabled", "body", "member", "bot_disabled"} {
		t.Run(change, func(t *testing.T) {
			db, _, in, l, request, other := repairModelFixture(t)
			ctx, actor := context.Background(), in.Binding.ActorUID
			first := startRepairModel(t, db, actor, l, request, true)
			if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "private output"}); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "transfer":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "cancel":
				requestCancelIntent(t, db, actor, in.EntryID, "ready")
			case "human_disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
			case "body":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
			case "bot_disabled":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
			case "member":
				task := getRepairBudget(t, db, actor, in.EntryID)
				group, _ := store.ParseSkillHubPublicationRepairTopic(task.Input.TopicID)
				deliveryExec(t, db, `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, group, in.Binding.BotUID)
			}
			if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, request); err == nil || grant {
				t.Fatal("authority loss granted dispatch")
			}
			if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "private output"}); err == nil {
				t.Fatal("authority loss allowed result write")
			}
			for _, reader := range []int64{actor, other, in.Binding.BotUID} {
				got, err := db.GetSkillHubPublicationRepairModelCall(ctx, reader, in.EntryID, 1)
				want := reader == actor && change != "human_disabled"
				if err != nil || (got != nil) != want {
					t.Fatal("private original-active-publisher history", reader, err)
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationRepairModelCallDatabaseGuards(t *testing.T) {
	t.Parallel()
	db, _, in, l, request, _ := repairModelFixture(t)
	actor, ctx := in.Binding.ActorUID, context.Background()
	first := startRepairModel(t, db, actor, l, request, true)
	for _, query := range []string{
		`DELETE FROM skillhub_publication_repair_model_calls WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_model_calls SET request_hash=repeat('b',64) WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_model_calls SET dispatch_lease=jsonb_set(dispatch_lease,'{Token}',to_jsonb(repeat('0',32))) WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_model_calls SET output_text='partial' WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_model_calls SET output_text='bad hash',output_hash=repeat('a',64),completed_at=clock_timestamp() WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_model_calls SET output_text=repeat('x',65537),output_hash=repeat('a',64),completed_at=clock_timestamp() WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(query, in.EntryID); err == nil {
			t.Fatal("SQL erased/rebound dispatch", query)
		}
	}
	if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "original"}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE skillhub_publication_repair_model_calls SET output_text=NULL,output_hash=NULL,completed_at=NULL WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_model_calls SET output_text='changed' WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(query, in.EntryID); err == nil {
			t.Fatal("SQL replaced result", query)
		}
	}
}

func TestPostgresSkillHubPublicationRepairModelCallRoundIsolationAndCancellationRace(t *testing.T) {
	t.Parallel()
	t.Run("round_isolation", func(t *testing.T) {
		db, _, in, l, request, _ := repairModelFixture(t)
		actor, ctx := in.Binding.ActorUID, context.Background()
		first := startRepairModel(t, db, actor, l, request, true)
		if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "round one"}); err != nil {
			t.Fatal(err)
		}
		nextEvidence := l.Input.Evidence
		nextEvidence.CandidateHash = strings.Repeat("e", 64)
		finishRepairBudget(t, db, actor, l, store.SkillHubPublicationRepairResult{Outcome: "rejected", Evidence: nextEvidence})
		next := claimRepairBudget(t, db, actor, in.EntryID, store.SkillHubPublicationRepairRoundInput{RequestID: "model-round-2", Evidence: nextEvidence}, "next-worker").Lease
		second := startRepairModel(t, db, actor, next, request, true)
		if second.CallID == first.CallID {
			t.Fatal("round identities aliased")
		}
		if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, request); !errors.Is(err, store.ErrSkillHubPublicationStale) || grant {
			t.Fatal("completed prior round dispatched", err)
		}
		if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, next, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "wrong round"}); !errors.Is(err, store.ErrSkillHubPublicationStale) {
			t.Fatal("cross-round completion", err)
		}
		got, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, in.EntryID, 1)
		if err != nil || got == nil || got.CallID != first.CallID || got.Output.Text != "round one" {
			t.Fatal("prior history changed", err)
		}
		if rounds := getRepairBudget(t, db, actor, in.EntryID).Rounds; len(rounds) != 2 {
			t.Fatal("journal spent repair round")
		}
	})
	t.Run("cancel_race", func(t *testing.T) {
		db, _, in, l, request, _ := repairModelFixture(t)
		actor, ctx := in.Binding.ActorUID, context.Background()
		var wg sync.WaitGroup
		start := make(chan struct{})
		var grant bool
		var call *store.SkillHubPublicationRepairModelCall
		var startErr, cancelErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			call, grant, startErr = db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, request)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, cancelErr = db.RequestSkillHubPublicationCancellation(ctx, actor, in.EntryID)
		}()
		close(start)
		wg.Wait()
		if cancelErr != nil || (startErr != nil && !errors.Is(startErr, store.ErrSkillHubPublicationSettled)) {
			t.Fatal("race", startErr, cancelErr)
		}
		got, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, in.EntryID, 1)
		if err != nil || (got != nil) != grant || (call != nil) != grant {
			t.Fatal("dispatch commit disagrees with grant", err)
		}
		if _, again, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, request); err == nil || again {
			t.Fatal("cancelled replay granted", err)
		}
	})
}

func TestPostgresSkillHubPublicationRepairModelCallInvalidAndCancelled(t *testing.T) {
	t.Parallel()
	db, _, in, l, request, other := repairModelFixture(t)
	ctx, actor := context.Background(), in.Binding.ActorUID
	wrong := l
	wrong.Token = strings.Repeat("0", 32)
	for _, lease := range []store.SkillHubPublicationRepairLease{wrong, {}} {
		if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, lease, request); err == nil || grant {
			t.Fatal("invalid/forged lease", err)
		}
	}
	if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, other, l, request); err == nil || grant {
		t.Fatal("other actor granted", err)
	}
	if _, grant, err := db.StartSkillHubPublicationRepairModelCall(ctx, actor, l, store.SkillHubPublicationRepairModelCallInput{}); err == nil || grant {
		t.Fatal("empty request granted", err)
	}
	if _, err := db.CompleteSkillHubPublicationRepairModelCall(ctx, actor, l, "pubmodel_"+strings.Repeat("1", 32), store.SkillHubPublicationRepairModelOutput{Text: "no dispatch"}); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("completed without dispatch", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, grant, err := db.StartSkillHubPublicationRepairModelCall(cancelled, actor, l, request); err == nil || grant {
		t.Fatal("cancelled start granted", err)
	}
	if got, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, in.EntryID, 1); err != nil || got != nil {
		t.Fatal("invalid start retained dispatch", err)
	}
	first := startRepairModel(t, db, actor, l, request, true)
	if _, err := db.CompleteSkillHubPublicationRepairModelCall(cancelled, actor, l, first.CallID, store.SkillHubPublicationRepairModelOutput{Text: "cancelled result"}); err == nil {
		t.Fatal("cancelled result committed")
	}
	if got, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, in.EntryID, 1); err != nil || got == nil || got.Output != nil {
		t.Fatal("cancelled result stored", err)
	}
}
