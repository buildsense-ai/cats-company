package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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

	var foreignKey string
	err := db.db.QueryRow(
		`SELECT CONSTRAINT_NAME
		 FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
		 WHERE CONSTRAINT_SCHEMA = DATABASE()
		   AND TABLE_NAME = 'push_subscriptions'
		   AND REFERENCED_TABLE_NAME = 'users'
		 LIMIT 1`,
	).Scan(&foreignKey)
	if err == nil {
		foreignKey = strings.ReplaceAll(foreignKey, "`", "``")
		if _, err := db.db.Exec(fmt.Sprintf("ALTER TABLE push_subscriptions DROP FOREIGN KEY `%s`", foreignKey)); err != nil {
			t.Fatalf("drop legacy push subscription foreign key: %v", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inspect push subscription foreign key: %v", err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("upgrade schema: %v", err)
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
		t.Fatalf("push subscription user foreign key was not restored: rule=%q err=%v", deleteRule, err)
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
	source, err := db.GetConversationTaskStatusForSource(topicID, sourceUID)
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
}
