package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/taskstatus/contracttest"
	"github.com/openchat/openchat/server/store/types"
)

func taskRunTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	schema := fmt.Sprintf("cats_task_runs_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`) })
	a := &Adapter{}
	if err := a.Open(dsnWithSearchPath(t, dsn, schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateSchema(); err != nil {
		t.Fatalf("schema not idempotent: %v", err)
	}
	return a
}

func TestPostgresConversationTaskRunDurability(t *testing.T) {
	a := taskRunTestAdapter(t)
	contracttest.Run(t, a.db, a, true)
}

func TestPostgresConversationTaskRunMigration(t *testing.T) {
	a := taskRunTestAdapter(t)
	uid, err := a.CreateUser(&types.User{Username: "task-migration", AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	topic := "task_migration"
	if err := a.CreateTopic(topic, "p2p", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpsertConversationTaskStatus(&types.ConversationTaskStatus{TopicID: topic, SourceUID: uid, RunID: "legacy", State: "completed"}); err != nil {
		t.Fatal(err)
	}
	load := func(suffix string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", "000052_conversation_task_runs."+suffix+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if _, err := a.db.Exec(load("down")); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var state string
	if err := a.db.QueryRow(`SELECT state FROM conversation_task_status_sources WHERE topic_id = $1`, topic).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("rollback damaged legacy projection: %q %v", state, err)
	}
	// The numbered migrations originally predated the publisher event clock;
	// upgrading directly from them must work without a prior CreateSchema run.
	if _, err := a.db.Exec(`ALTER TABLE conversation_task_status_sources DROP COLUMN event_updated_at`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := a.db.Exec(load("up")); err != nil {
			t.Fatalf("upgrade %d: %v", i, err)
		}
	}
	runs, err := a.ListConversationTaskRuns(topic, 0, 100)
	if err != nil || len(runs) != 1 || runs[0].State != "completed" {
		t.Fatalf("backfill: %+v %v", runs, err)
	}
	// A restart must not overwrite an existing terminal run with a stale
	// projection left behind by an older binary.
	if _, err := a.db.Exec(`UPDATE conversation_task_status_sources SET state = 'running' WHERE topic_id = $1`, topic); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	runs, err = a.ListConversationTaskRuns(topic, 0, 100)
	if err != nil || len(runs) != 1 || runs[0].State != "completed" {
		t.Fatalf("restart resurrected terminal: %+v %v", runs, err)
	}
}
