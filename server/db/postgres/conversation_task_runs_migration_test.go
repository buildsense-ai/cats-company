package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationTaskRunStartupMigrationMatchesFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", "000052_conversation_task_runs.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.TrimSpace(migrateConversationTaskStatusSourcesAddEventUpdatedAt) + "\n\n" + strings.TrimSpace(createConversationTaskRunsTable) + "\n\n" + strings.TrimSpace(backfillConversationTaskRuns) + "\n\n" + strings.TrimSpace(createConversationTaskAggregateClock)
	if strings.TrimSpace(string(data)) != expected {
		t.Fatal("startup task-run schema and numbered migration differ")
	}
}
