package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestFileVersionBindingsSchemaColumnsUniqueAndIndex(t *testing.T) {
	for _, column := range []string{
		"id BIGSERIAL PRIMARY KEY", "topic_id VARCHAR(64) NOT NULL REFERENCES topics(id)",
		"source_message_id BIGINT NOT NULL", "source_attachment_index INTEGER NOT NULL",
		"source_file_key VARCHAR(255) NOT NULL", "latest_file_key VARCHAR(255) NOT NULL",
		"latest_url VARCHAR(1024) NOT NULL", "latest_name VARCHAR(255) NOT NULL",
		"latest_size BIGINT NOT NULL DEFAULT 0", "latest_mime_type VARCHAR(255) NOT NULL",
		"updated_by_uid BIGINT NOT NULL REFERENCES users(id)", "created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP",
	} {
		if !strings.Contains(createFileVersionBindingsTable, column) {
			t.Fatalf("file_version_bindings missing %q", column)
		}
	}
	if !strings.Contains(createFileVersionBindingsTable, "CONSTRAINT uk_file_version_bindings_source UNIQUE (topic_id, source_message_id, source_attachment_index)") {
		t.Fatal("file_version_bindings unique source triple missing")
	}
	if !strings.Contains(createFileVersionBindingsTable, "CREATE INDEX IF NOT EXISTS idx_file_version_bindings_topic_updated") {
		t.Fatal("file_version_bindings topic index missing")
	}
}

func TestFileVersionBindingsSchemaMigrationUpDown(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000067_file_version_bindings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../migrations/postgres/000067_file_version_bindings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(up)) != strings.TrimSpace(createFileVersionBindingsTable) {
		t.Fatal("startup schema and 000067 up migration differ")
	}
	if !strings.Contains(strings.ToLower(string(down)), "drop table if exists file_version_bindings") {
		t.Fatal("down migration does not remove exactly the new table")
	}
	if strings.Contains(strings.ToLower(string(down)), "messages") {
		t.Fatal("down migration must not touch messages")
	}
}
