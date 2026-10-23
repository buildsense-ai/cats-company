// Package contracttest exercises the same run/outbox behavior against both SQL
// adapters. Callers provide an isolated schema or database.
package contracttest

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type Adapter interface {
	store.ConversationTaskStatusStore
	store.ConversationTaskRunStore
	store.ConversationTaskStatusOutboxStore
	store.ConversationTaskStatusRecoveryStore
	store.ConversationTaskGenerationStore
	CreateUser(*types.User) (int64, error)
	CreateTopic(string, string, int64) error
}

func Run(t *testing.T, db *sql.DB, adapter Adapter, postgres bool) {
	t.Helper()
	query := func(q string) string {
		if !postgres {
			return q
		}
		for i := 1; strings.Contains(q, "?"); i++ {
			q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
		}
		return q
	}
	fixture := func(t *testing.T) (string, int64, int64) {
		t.Helper()
		var uids []int64
		for i := 0; i < 2; i++ {
			uid, err := adapter.CreateUser(&types.User{Username: fmt.Sprintf("task_run_%d_%d", time.Now().UnixNano(), i), AccountType: types.AccountBot, PassHash: []byte("test")})
			if err != nil {
				t.Fatal(err)
			}
			uids = append(uids, uid)
		}
		topic := fmt.Sprintf("task_run_%d", time.Now().UnixNano())
		if err := adapter.CreateTopic(topic, "p2p", uids[0]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec(query(`DELETE FROM topics WHERE id = ?`), topic)
			for _, uid := range uids {
				db.Exec(query(`DELETE FROM users WHERE id = ?`), uid)
			}
		})
		return topic, uids[0], uids[1]
	}
	upsert := func(t *testing.T, topic string, uid int64, run, state string) *types.ConversationTaskStatus {
		t.Helper()
		var expiry *time.Time
		if state == "running" {
			e := time.Now().UTC().Add(time.Hour)
			expiry = &e
		}
		a, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: uid, RunID: run, State: state, ExpiresAt: expiry})
		if err != nil {
			t.Fatalf("upsert %s/%s: %v", run, state, err)
		}
		return a
	}
	count := func(t *testing.T, table, topic string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query(`SELECT COUNT(*) FROM `+table+` WHERE topic_id = ?`), topic).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("overlapping runs keep every terminal and an active aggregate", func(t *testing.T) {
		topic, a, b := fixture(t)
		upsert(t, topic, a, "a-old", "running")
		upsert(t, topic, a, "a-new", "running")
		upsert(t, topic, b, "b", "running")
		if got := upsert(t, topic, a, "a-old", "failed"); got.State != "running" {
			t.Fatalf("late terminal hid active runs: %+v", got)
		}
		before := upsert(t, topic, b, "b", "completed")
		if before.State != "running" || before.RunID != "a-new" {
			t.Fatalf("aggregate=%+v", before)
		}
		summaries, err := adapter.GetConversationTaskStatuses([]string{topic})
		if err != nil || summaries[topic] == nil || summaries[topic].UpdatedAt.Before(before.UpdatedAt) {
			t.Fatalf("aggregate revision regressed: %+v %v", summaries, err)
		}
		source, err := adapter.GetConversationTaskStatusForSource(topic, a)
		if err != nil || source.RunID != "a-new" || source.State != "running" {
			t.Fatalf("source=%+v err=%v", source, err)
		}
		upsert(t, topic, a, "a-new", "completed")
		runs, err := adapter.ListConversationTaskRuns(topic, 0, 100)
		if err != nil || len(runs) != 3 {
			t.Fatalf("runs=%+v err=%v", runs, err)
		}
		states := map[string]string{}
		for _, run := range runs {
			states[run.RunID] = run.State
		}
		if states["a-old"] != "failed" || states["a-new"] != "completed" || states["b"] != "completed" {
			t.Fatalf("lost run: %v", states)
		}
		beforeCount := count(t, "conversation_task_status_outbox", topic)
		upsert(t, topic, a, "a-new", "completed")
		if got := count(t, "conversation_task_status_outbox", topic); got != beforeCount {
			t.Fatalf("terminal retry enqueued %d events, previously %d", got, beforeCount)
		}
		if _, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: a, RunID: "a-old", State: "running"}); !errors.Is(err, store.ErrConversationTaskRunTerminal) {
			t.Fatalf("late progress error=%v", err)
		}
	})

	t.Run("ledger aggregate fallback preserves publisher clock", func(t *testing.T) {
		for _, path := range []string{"summary", "source", "write"} {
			t.Run(path, func(t *testing.T) {
				topic, a, c := fixture(t)
				var now time.Time
				clock := `SELECT CURRENT_TIMESTAMP(6)`
				if postgres {
					clock = `SELECT clock_timestamp()`
				}
				if err := db.QueryRow(clock).Scan(&now); err != nil {
					t.Fatal(err)
				}
				base := now.UTC().Add(-3 * time.Minute).Truncate(time.Microsecond)
				expiry := now.Add(time.Hour)
				for i, run := range []string{"a", "b"} {
					if _, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: a, RunID: run, State: "running", EventUpdatedAt: base.Add(time.Duration(i) * time.Minute), ExpiresAt: &expiry}); err != nil {
						t.Fatal(err)
					}
				}
				// Expire B consistently in all projections without a wall-clock wait.
				// A subsequent write by another source falls back to A in the topic
				// aggregate, while this source's projection still points to B.
				for _, table := range []string{"conversation_task_runs", "conversation_task_status_sources", "conversation_task_statuses"} {
					if _, err := db.Exec(query(`UPDATE `+table+` SET expires_at = ? WHERE topic_id = ? AND run_id = 'b'`), now.Add(-time.Minute), topic); err != nil {
						t.Fatal(err)
					}
				}
				if result, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: c, RunID: "c", State: "completed", EventUpdatedAt: base.Add(2 * time.Minute)}); err != nil || result.RunID != "a" {
					t.Fatalf("fallback=%+v err=%v", result, err)
				}
				switch path {
				case "summary":
					if _, err := adapter.GetConversationTaskStatuses([]string{topic}); err != nil {
						t.Fatal(err)
					}
				case "source":
					if _, err := adapter.GetConversationTaskStatusForSource(topic, a); err != nil {
						t.Fatal(err)
					}
				case "write":
					upsert(t, topic, c, "c", "completed")
				}
				runs, err := adapter.ListConversationTaskRuns(topic, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, run := range runs {
					if run.RunID == "a" && !run.EventUpdatedAt.Equal(base) {
						t.Errorf("publisher clock changed from %v to %v", base, run.EventUpdatedAt)
					}
				}
				if _, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: a, RunID: "a", State: "completed", EventUpdatedAt: base.Add(150 * time.Second)}); err != nil {
					t.Fatalf("valid delayed terminal rejected: %v", err)
				}
				runs, err = adapter.ListConversationTaskRuns(topic, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, run := range runs {
					if run.RunID == "a" && run.State != "completed" {
						t.Fatalf("A remained active: %+v", run)
					}
				}
			})
		}
	})

	t.Run("identical terminal retry returns persisted aggregate revision", func(t *testing.T) {
		topic, a, b := fixture(t)
		upsert(t, topic, a, "a", "running")
		bRunning := upsert(t, topic, b, "b", "running")
		first := upsert(t, topic, b, "b", "completed")
		if first.RunID != "a" || !first.UpdatedAt.After(bRunning.UpdatedAt) {
			t.Fatalf("first completion=%+v, B running=%+v", first, bRunning)
		}
		beforeCount := count(t, "conversation_task_status_outbox", topic)
		retry := upsert(t, topic, b, "b", "completed")
		if retry.RunID != "a" || !retry.UpdatedAt.Equal(first.UpdatedAt) {
			t.Errorf("aggregate revision regressed: first=%v retry=%v", first.UpdatedAt, retry.UpdatedAt)
		}
		var persisted time.Time
		if err := db.QueryRow(query(`SELECT updated_at FROM conversation_task_statuses WHERE topic_id = ?`), topic).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if !persisted.Equal(first.UpdatedAt) {
			t.Fatal("no-op retry advanced stored aggregate revision")
		}
		if count(t, "conversation_task_status_outbox", topic) != beforeCount {
			t.Fatal("no-op retry enqueued a duplicate event")
		}
	})

	t.Run("legacy writes remain importable after ledger projections", func(t *testing.T) {
		topic, a, _ := fixture(t)
		upsert(t, topic, a, "legacy", "running")
		if _, err := db.Exec(query(`UPDATE conversation_task_statuses SET state = 'failed', summary = 'legacy failure', expires_at = NULL WHERE topic_id = ?`), topic); err != nil {
			t.Fatal(err)
		}
		source, err := adapter.GetConversationTaskStatusForSource(topic, a)
		if err != nil || source == nil || source.State != "failed" || source.Summary != "legacy failure" {
			t.Fatalf("genuine legacy write was skipped: %+v %v", source, err)
		}
	})

	t.Run("pagination is stable and scoped to topic", func(t *testing.T) {
		topic, a, _ := fixture(t)
		for i := 0; i < 5; i++ {
			upsert(t, topic, a, fmt.Sprintf("page-%d", i), "completed")
		}
		first, err := adapter.ListConversationTaskRuns(topic, 0, 2)
		if err != nil || len(first) != 2 {
			t.Fatalf("first=%v err=%v", first, err)
		}
		second, err := adapter.ListConversationTaskRuns(topic, first[1].ID, 2)
		if err != nil || len(second) != 2 || second[0].ID >= first[1].ID {
			t.Fatalf("second=%v err=%v", second, err)
		}
		other, err := adapter.ListConversationTaskRuns("missing", 0, 100)
		if err != nil || len(other) != 0 {
			t.Fatalf("cross-topic runs=%v err=%v", other, err)
		}
		topics := []string{topic}
		for i := 0; i < 101; i++ {
			topics = append(topics, fmt.Sprintf("missing_%d", i))
		}
		summaries, err := adapter.GetConversationTaskStatuses(topics)
		if err != nil || len(summaries) != 1 || summaries[topic] == nil {
			t.Fatalf("batched summaries=%v err=%v", summaries, err)
		}
	})

	t.Run("outbox failure rolls back run and projections", func(t *testing.T) {
		topic, a, _ := fixture(t)
		constraint := fmt.Sprintf("task_outbox_failure_%d", a)
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE conversation_task_status_outbox ADD CONSTRAINT %s CHECK (source_uid <> %d)`, constraint, a)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			verb := "DROP CHECK"
			if postgres {
				verb = "DROP CONSTRAINT"
			}
			if _, err := db.Exec(`ALTER TABLE conversation_task_status_outbox ` + verb + ` ` + constraint); err != nil {
				t.Error(err)
			}
		})
		status := &types.ConversationTaskStatus{TopicID: topic, SourceUID: a, RunID: "rollback", State: "running"}
		if _, err := adapter.UpsertConversationTaskStatus(status); err == nil || !strings.Contains(err.Error(), constraint) {
			t.Fatalf("outbox constraint should reject transaction: %v", err)
		}
		for _, table := range []string{"conversation_task_runs", "conversation_task_status_sources", "conversation_task_statuses", "conversation_task_status_outbox"} {
			if n := count(t, table, topic); n != 0 {
				t.Fatalf("partial commit in %s: %d", table, n)
			}
		}
		if !status.EventUpdatedAt.IsZero() {
			t.Fatal("rolled-back event was exposed as committed")
		}
	})

	t.Run("expired run does not hide another active run", func(t *testing.T) {
		topic, a, _ := fixture(t)
		upsert(t, topic, a, "live", "running")
		expired := time.Now().UTC().Add(-time.Minute)
		if _, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: a, RunID: "expired", State: "running", ExpiresAt: &expired}); err != nil {
			t.Fatal(err)
		}
		summaries, err := adapter.GetConversationTaskStatuses([]string{topic})
		if err != nil || summaries[topic] == nil || summaries[topic].RunID != "live" {
			t.Fatalf("summary=%v err=%v", summaries, err)
		}
		upsert(t, topic, a, "live", "completed")
		summaries, err = adapter.GetConversationTaskStatuses([]string{topic})
		if err != nil || summaries[topic] == nil || summaries[topic].State != "completed" {
			t.Fatalf("expired run became active: %v err=%v", summaries, err)
		}
	})

	t.Run("concurrent terminal beats progress", func(t *testing.T) {
		topic, a, _ := fixture(t)
		upsert(t, topic, a, "race", "running")
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, state := range []string{"running", "completed"} {
			wg.Add(1)
			go func(state string) {
				defer wg.Done()
				<-start
				_, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: a, RunID: "race", State: state})
				if state == "completed" && err != nil {
					t.Errorf("terminal: %v", err)
				}
			}(state)
		}
		close(start)
		wg.Wait()
		runs, err := adapter.ListConversationTaskRuns(topic, 0, 100)
		if err != nil || len(runs) != 1 || runs[0].State != "completed" {
			t.Fatalf("race resumed: %v err=%v", runs, err)
		}
	})

	t.Run("recovery retains every run and fences new connections", func(t *testing.T) {
		topic, a, _ := fixture(t)
		upsert(t, topic, a, "old", "running")
		upsert(t, topic, a, "new", "running")
		generation, err := adapter.BumpBotConnectionGeneration(a)
		if err != nil {
			t.Fatal(err)
		}
		cutoff := time.Now().UTC().Add(time.Minute)
		if _, updated, err := adapter.MarkConversationTaskStatusStaleIfUnchanged(topic, a, "old", cutoff, generation+1); err != nil || updated {
			t.Fatalf("generation fence: %v %v", updated, err)
		}
		aggregate, updated, err := adapter.MarkConversationTaskStatusStaleIfUnchanged(topic, a, "old", cutoff, generation)
		if err != nil || !updated || aggregate.State != "running" || aggregate.RunID != "new" {
			t.Fatalf("recovery hid another run: %+v %v %v", aggregate, updated, err)
		}
		if _, updated, err := adapter.MarkConversationTaskStatusStaleIfUnchanged(topic, a, "old", cutoff, generation); err != nil || updated {
			t.Fatalf("repeated recovery: %v %v", updated, err)
		}
		runs, err := adapter.ListConversationTaskRuns(topic, 0, 100)
		if err != nil || len(runs) != 2 {
			t.Fatalf("recovery runs=%v %v", runs, err)
		}
		if runs[1].State != "stale" || strings.Contains(runs[1].Summary, "已自动中止") {
			t.Fatalf("disconnect claimed runtime stopped: %+v", runs[1])
		}
	})

	t.Run("concurrent workers claim distinct events", func(t *testing.T) {
		topic, a, _ := fixture(t)
		upsert(t, topic, a, "claim-a", "completed")
		upsert(t, topic, a, "claim-b", "completed")
		start := make(chan struct{})
		type result struct {
			events []store.ConversationTaskStatusEvent
			err    error
		}
		results := make(chan result, 2)
		for range 2 {
			go func() {
				<-start
				events, err := adapter.ClaimConversationTaskStatusEvents(1, time.Minute)
				results <- result{events, err}
			}()
		}
		close(start)
		ids := map[int64]bool{}
		for range 2 {
			r := <-results
			if r.err != nil || len(r.events) != 1 {
				t.Fatalf("concurrent claim=%v err=%v", r.events, r.err)
			}
			event := r.events[0]
			if ids[event.ID] {
				t.Fatal("two workers own the same live claim")
			}
			ids[event.ID] = true
			if err := adapter.CompleteConversationTaskStatusEvent(event.ID, event.Claim); err != nil {
				t.Fatal(err)
			}
		}
		if n := count(t, "conversation_task_status_outbox", topic); n != 0 {
			t.Fatalf("uncompleted claims=%d", n)
		}
	})

	t.Run("claim lease survives crash and stale acknowledgement", func(t *testing.T) {
		topic, a, _ := fixture(t)
		upsert(t, topic, a, "outbox", "completed")
		first, err := adapter.ClaimConversationTaskStatusEvents(100, time.Minute)
		if err != nil || len(first) != 1 {
			t.Fatalf("first claim=%v err=%v", first, err)
		}
		if first[0].Status.RunID != "outbox" || first[0].Status.State != "completed" {
			t.Fatalf("receipt mutated: %+v", first[0])
		}
		if events, err := adapter.ClaimConversationTaskStatusEvents(100, time.Minute); err != nil || len(events) != 0 {
			t.Fatalf("live lease reclaimed: %v %v", events, err)
		}
		if _, err := db.Exec(query(`UPDATE conversation_task_status_outbox SET available_at = ? WHERE id = ?`), time.Now().UTC().Add(-time.Minute), first[0].ID); err != nil {
			t.Fatal(err)
		}
		second, err := adapter.ClaimConversationTaskStatusEvents(100, time.Minute)
		if err != nil || len(second) != 1 || second[0].Claim == first[0].Claim {
			t.Fatalf("restart claim=%v err=%v", second, err)
		}
		if err := adapter.CompleteConversationTaskStatusEvent(first[0].ID, first[0].Claim); err != nil {
			t.Fatal(err)
		}
		if n := count(t, "conversation_task_status_outbox", topic); n != 1 {
			t.Fatal("stale worker deleted a new claim")
		}
		if err := adapter.CompleteConversationTaskStatusEvent(second[0].ID, second[0].Claim); err != nil {
			t.Fatal(err)
		}
		if n := count(t, "conversation_task_status_outbox", topic); n != 0 {
			t.Fatal("acknowledged event retained")
		}
		if n := count(t, "conversation_task_runs", topic); n != 1 {
			t.Fatal("acknowledgement deleted run history")
		}
	})
}
