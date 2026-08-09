package mysql

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestMySQLConversationTaskStatusContract(t *testing.T) {
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}

	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	var deleteRule string
	if err := db.db.QueryRow(
		`SELECT DELETE_RULE
		 FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
		 WHERE CONSTRAINT_SCHEMA = DATABASE()
		   AND TABLE_NAME = 'push_subscriptions'
		   AND REFERENCED_TABLE_NAME = 'users'
		 LIMIT 1`,
	).Scan(&deleteRule); err != nil || deleteRule != "CASCADE" {
		t.Fatalf("push subscription user foreign key missing from new schema: rule=%q err=%v", deleteRule, err)
	}

	suffix := time.Now().UnixNano()
	sourceUID, err := db.CreateUser(&types.User{
		Username:    fmt.Sprintf("task-status-%d", suffix),
		DisplayName: "Task Status Bot",
		AccountType: types.AccountBot,
		PassHash:    []byte("test"),
	})
	if err != nil {
		t.Fatalf("create source user: %v", err)
	}
	defer db.db.Exec(`DELETE FROM users WHERE id = ?`, sourceUID)

	topicID := fmt.Sprintf("task_status_%d", suffix)
	if err := db.CreateTopic(topicID, "p2p", sourceUID); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	eventAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	receivedAfter := time.Now().UTC()
	if _, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID: topicID, RunID: "run-clock", State: "running", SourceUID: sourceUID,
		ExpiresAt: &expiry, EventUpdatedAt: eventAt,
	}); err != nil {
		t.Fatalf("upsert publisher-clock status: %v", err)
	}
	clockStatus, err := db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || clockStatus == nil {
		t.Fatalf("load publisher-clock status: status=%+v err=%v", clockStatus, err)
	}
	if clockStatus.UpdatedAt.Before(receivedAfter) {
		t.Fatalf("server updated_at=%v, want at/after receipt %v", clockStatus.UpdatedAt, receivedAfter)
	}
	if !clockStatus.EventUpdatedAt.Equal(eventAt) {
		t.Fatalf("event_updated_at=%v, want publisher time %v", clockStatus.EventUpdatedAt, eventAt)
	}
	if candidates, err := db.ListAllActiveConversationTaskStatusesBefore(receivedAfter.Add(-time.Minute)); err != nil {
		t.Fatalf("list clock-skew reaper candidates: %v", err)
	} else {
		for _, candidate := range candidates {
			if candidate.TopicID == topicID {
				t.Fatalf("old publisher clock made fresh task reapable: %+v", candidate)
			}
		}
	}
	if _, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID: topicID, RunID: "run-clock", State: "waiting", SourceUID: sourceUID,
		ExpiresAt: &expiry, EventUpdatedAt: eventAt.Add(-time.Minute),
	}); !errors.Is(err, store.ErrConversationTaskStatusStale) {
		t.Fatalf("older publisher event error=%v, want %v", err, store.ErrConversationTaskStatusStale)
	}
	if _, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID: topicID, RunID: "run-clock", State: "completed", SourceUID: sourceUID,
		EventUpdatedAt: eventAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("complete publisher-clock run: %v", err)
	}
	implicitStatus := &types.ConversationTaskStatus{
		TopicID: topicID, RunID: "run-implicit-clock", State: "completed", SourceUID: sourceUID,
	}
	if _, err := db.UpsertConversationTaskStatus(implicitStatus); err != nil {
		t.Fatalf("upsert implicit-clock status: %v", err)
	}
	if implicitStatus.EventUpdatedAt.IsZero() {
		t.Fatal("implicit event time was not propagated to the caller after commit")
	}
	persistedImplicitStatus, err := db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || persistedImplicitStatus == nil {
		t.Fatalf("load implicit-clock status: status=%+v err=%v", persistedImplicitStatus, err)
	}
	if !implicitStatus.EventUpdatedAt.Equal(persistedImplicitStatus.EventUpdatedAt) {
		t.Fatalf("caller event time=%v, want persisted event time %v", implicitStatus.EventUpdatedAt, persistedImplicitStatus.EventUpdatedAt)
	}
	upsert := func(runID, state string) {
		t.Helper()
		if _, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
			TopicID: topicID, RunID: runID, State: state, SourceUID: sourceUID,
			ExpiresAt: func() *time.Time {
				if state == "running" {
					return &expiry
				}
				return nil
			}(),
		}); err != nil {
			t.Fatalf("upsert status %s/%s: %v", runID, state, err)
		}
	}

	upsert("run-terminal", "completed")
	if _, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID: topicID, RunID: "run-terminal", State: "running", SourceUID: sourceUID, ExpiresAt: &expiry,
	}); err == nil {
		t.Fatal("terminal run resumed through the store")
	}

	upsert("run-superseded", "running")
	upsert("run-current", "running")
	if _, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID: topicID, RunID: "run-superseded", State: "completed", SourceUID: sourceUID,
	}); !errors.Is(err, store.ErrConversationTaskRunSuperseded) {
		t.Fatalf("late terminal error = %v, want %v", err, store.ErrConversationTaskRunSuperseded)
	}
	source, err := db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || source == nil || source.RunID != "run-current" || source.State != "running" {
		t.Fatalf("late terminal replaced active run: status=%+v err=%v", source, err)
	}

	upsert("run-transition-race", "running")
	startTransitionRace := make(chan struct{})
	transitionResults := make(chan struct {
		state string
		err   error
	}, 2)
	for _, state := range []string{"running", "completed"} {
		go func(state string) {
			<-startTransitionRace
			_, updateErr := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
				TopicID: topicID, RunID: "run-transition-race", State: state, SourceUID: sourceUID,
				ExpiresAt: func() *time.Time {
					if state == "running" {
						return &expiry
					}
					return nil
				}(),
			})
			transitionResults <- struct {
				state string
				err   error
			}{state: state, err: updateErr}
		}(state)
	}
	close(startTransitionRace)
	for range 2 {
		result := <-transitionResults
		if result.state == "completed" && result.err != nil {
			t.Fatalf("complete concurrent task run: %v", result.err)
		}
	}
	source, err = db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || source == nil || source.State != "completed" {
		t.Fatalf("concurrent progress resumed terminal run: status=%+v err=%v", source, err)
	}

	upsert("run-late-progress", "completed")
	if _, err := db.db.Exec(
		`UPDATE conversation_task_statuses
		 SET state = 'running', source_uid = ?, expires_at = ?
		 WHERE topic_id = ?`,
		sourceUID, expiry, topicID,
	); err != nil {
		t.Fatalf("simulate late legacy progress: %v", err)
	}
	source, err = db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || source == nil || source.RunID != "run-late-progress" || source.State != "completed" {
		t.Fatalf("legacy progress resumed terminal run: status=%+v err=%v", source, err)
	}

	upsert("run-legacy-1", "completed")
	if _, err := db.db.Exec(
		`UPDATE conversation_task_statuses
		 SET run_id = ?, state = 'running', source_uid = ?, expires_at = ?
		 WHERE topic_id = ?`,
		"run-legacy-2", sourceUID, expiry, topicID,
	); err != nil {
		t.Fatalf("simulate legacy writer: %v", err)
	}
	aggregates, err := db.GetConversationTaskStatuses([]string{topicID})
	if err != nil || aggregates[topicID] == nil ||
		aggregates[topicID].RunID != "run-legacy-2" || aggregates[topicID].State != "running" {
		t.Fatalf("legacy aggregate was not synchronized: status=%+v err=%v", aggregates[topicID], err)
	}
	source, err = db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || source == nil || source.RunID != "run-legacy-2" || source.State != "running" {
		t.Fatalf("legacy status was not synchronized: status=%+v err=%v", source, err)
	}

	if _, err := db.db.Exec(
		`UPDATE conversation_task_statuses
		 SET run_id = ?, state = 'completed', source_uid = ?, expires_at = NULL
		 WHERE topic_id = ?`,
		"run-legacy-1", sourceUID, topicID,
	); err != nil {
		t.Fatalf("simulate late legacy completion: %v", err)
	}
	source, err = db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || source == nil || source.RunID != "run-legacy-2" || source.State != "running" {
		t.Fatalf("late legacy completion replaced active run: status=%+v err=%v", source, err)
	}

	if _, err := db.db.Exec(
		`UPDATE conversation_task_statuses
		 SET run_id = ?, state = 'completed', source_uid = ?, expires_at = NULL
		 WHERE topic_id = ?`,
		"run-legacy-2", sourceUID, topicID,
	); err != nil {
		t.Fatalf("simulate matching legacy completion: %v", err)
	}
	source, err = db.GetConversationTaskStatusForSource(topicID, sourceUID)
	if err != nil || source == nil || source.RunID != "run-legacy-2" || source.State != "completed" {
		t.Fatalf("legacy completion was not synchronized: status=%+v err=%v", source, err)
	}

	// CAS recovery semantics: stale only when the row still matches the
	// disconnected run, was not updated after the disconnection, and the
	// cluster-wide bot connection generation still matches the snapshot.
	generation, err := db.BumpBotConnectionGeneration(sourceUID)
	if err != nil {
		t.Fatalf("bump generation: %v", err)
	}
	if generation != 1 {
		t.Fatalf("initial generation = %d, want 1", generation)
	}
	pastDisconnectedAt := time.Now().UTC().Add(-2 * time.Second)
	upsert("run-cas-mismatch", "running")
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-other", pastDisconnectedAt, generation); err != nil || updated {
		t.Fatalf("run id mismatch CAS updated=%v err=%v", updated, err)
	}
	// Explicitly terminate the scenario-1 active run before switching to another
	// run, otherwise the transition validator rejects the next scenario with
	// ErrConversationTaskRunSuperseded.
	upsert("run-cas-mismatch", "completed")
	upsert("run-cas-terminal", "completed")
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-terminal", pastDisconnectedAt, generation); err != nil || updated {
		t.Fatalf("terminal run CAS updated=%v err=%v", updated, err)
	}
	// Newer progress after the disconnection must win: updated_at > disconnectedAt.
	upsert("run-cas-newer", "running")
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-newer", pastDisconnectedAt, generation); err != nil || updated {
		t.Fatalf("newer progress CAS updated=%v err=%v", updated, err)
	}
	futureDisconnectedAt := time.Now().UTC().Add(time.Minute)
	recovered, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-newer", futureDisconnectedAt, generation)
	if err != nil || !updated {
		t.Fatalf("active run CAS did not update: updated=%v err=%v", updated, err)
	}
	if recovered == nil || recovered.State != "stale" || recovered.RunID != "run-cas-newer" {
		t.Fatalf("recovered status = %+v", recovered)
	}
	// A newer connection generation (any node) must win: bumping the cluster-wide
	// generation makes an old timer's CAS a no-op even when offline elsewhere.
	upsert("run-cas-gen", "running")
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-gen", futureDisconnectedAt, generation); err != nil || !updated {
		t.Fatalf("baseline generation CAS updated=%v err=%v", updated, err)
	}
	upsert("run-cas-gen2", "running")
	if _, err := db.BumpBotConnectionGeneration(sourceUID); err != nil {
		t.Fatalf("bump generation for newer connection: %v", err)
	}
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-gen2", futureDisconnectedAt, generation); err != nil || updated {
		t.Fatalf("stale generation CAS updated=%v err=%v", updated, err)
	}
	// Two concurrent recoveries of the same run: exactly one wins. Use the
	// current (bumped) generation so both candidates pass the fence.
	currentGeneration, err := db.BotConnectionGeneration(sourceUID)
	if err != nil {
		t.Fatalf("read current generation: %v", err)
	}
	upsert("run-cas-race", "running")
	startCASRace := make(chan struct{})
	casResults := make(chan bool, 2)
	for range 2 {
		go func() {
			<-startCASRace
			_, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(topicID, sourceUID, "run-cas-race", futureDisconnectedAt, currentGeneration)
			casResults <- err == nil && updated
		}()
	}
	close(startCASRace)
	casWins := 0
	for range 2 {
		if <-casResults {
			casWins++
		}
	}
	if casWins != 1 {
		t.Fatalf("concurrent CAS wins = %d, want 1", casWins)
	}

	// Same-wall-clock-second cutoff: progress written later within the same
	// second as the disconnection must not be recovered. This is only correct
	// with fractional-second precision (TIMESTAMP(6)); second-precision
	// TIMESTAMP would truncate updated_at to the same second and pass the CAS.
	// (ON UPDATE CURRENT_TIMESTAMP rewrites updated_at on UPDATE, so seed the
	// row with an explicit fractional timestamp via INSERT, which is kept.)
	microTopic := fmt.Sprintf("task_micro_%d", time.Now().UnixNano())
	if err := db.CreateTopic(microTopic, "p2p", sourceUID); err != nil {
		t.Fatalf("create micro topic: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, created_at, updated_at)
		 VALUES (?, ?, 'run-cas-micro', 'running', '', '', NULL, CURRENT_TIMESTAMP(6), ?)`,
		microTopic, sourceUID, time.Date(2026, 8, 5, 12, 0, 0, 500_000_000, time.UTC),
	); err != nil {
		t.Fatalf("seed microsecond task status: %v", err)
	}
	sameSecondDisconnectedAt := time.Date(2026, 8, 5, 12, 0, 0, 100_000_000, time.UTC)
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(microTopic, sourceUID, "run-cas-micro", sameSecondDisconnectedAt, currentGeneration); err != nil || updated {
		t.Fatalf("same-second newer progress CAS updated=%v err=%v (TIMESTAMP precision regression?)", updated, err)
	}
	// Verify the column actually has fractional precision in the live schema.
	var updatedAt time.Time
	if err := db.db.QueryRow(
		`SELECT updated_at FROM conversation_task_status_sources WHERE topic_id = ? AND source_uid = ?`,
		microTopic,
		sourceUID,
	).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if updatedAt.Nanosecond() == 0 {
		t.Fatalf("updated_at lost fractional precision: %s", updatedAt.Format(time.RFC3339Nano))
	}
	// With fractional precision, the same second is distinguishable: a
	// disconnectedAt after the microsecond write recovers.
	afterWriteDisconnectedAt := time.Date(2026, 8, 5, 12, 0, 0, 900_000_000, time.UTC)
	if _, updated, err := db.MarkConversationTaskStatusStaleIfUnchanged(microTopic, sourceUID, "run-cas-micro", afterWriteDisconnectedAt, currentGeneration); err != nil || !updated {
		t.Fatalf("same-second cutoff CAS updated=%v err=%v", updated, err)
	}

	// Reaper list semantics: only active, unexpired rows last updated at/before
	// the cutoff are returned. Terminal and fresh rows must be excluded.
	cutoff := time.Now().UTC().Add(-time.Minute)
	seedReaperRow := func(topicID, runID, state string, updatedAt time.Time) {
		t.Helper()
		if err := db.CreateTopic(topicID, "p2p", sourceUID); err != nil {
			t.Fatalf("create reaper topic %s: %v", topicID, err)
		}
		if _, err := db.db.Exec(
			`INSERT INTO conversation_task_status_sources
			   (topic_id, source_uid, run_id, state, summary, error, expires_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, '', '', NULL, CURRENT_TIMESTAMP(6), ?)`,
			topicID, sourceUID, runID, state, updatedAt,
		); err != nil {
			t.Fatalf("seed reaper row %s: %v", runID, err)
		}
	}
	reaperOld := fmt.Sprintf("task_reaper_old_%d", time.Now().UnixNano())
	reaperFresh := fmt.Sprintf("task_reaper_fresh_%d", time.Now().UnixNano())
	reaperTerminal := fmt.Sprintf("task_reaper_term_%d", time.Now().UnixNano())
	seedReaperRow(reaperOld, "run-reaper-old", "running", cutoff.Add(-30*time.Second))
	seedReaperRow(reaperFresh, "run-reaper-fresh", "running", time.Now().UTC())
	seedReaperRow(reaperTerminal, "run-reaper-terminal", "completed", cutoff.Add(-30*time.Second))

	reaped, err := db.ListAllActiveConversationTaskStatusesBefore(cutoff)
	if err != nil {
		t.Fatalf("list reaper candidates: %v", err)
	}
	foundOld := false
	for _, st := range reaped {
		switch st.TopicID {
		case reaperOld:
			foundOld = true
		case reaperFresh, reaperTerminal:
			t.Fatalf("reaper returned excluded row %s (state=%s)", st.TopicID, st.State)
		}
	}
	if !foundOld {
		t.Fatalf("reaper did not return run-reaper-old; got %d rows", len(reaped))
	}
}
