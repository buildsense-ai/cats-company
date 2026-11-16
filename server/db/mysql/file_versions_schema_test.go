package mysql

import (
	"strings"
	"testing"
)

func TestFileVersionBindingsSchemaColumnsUniqueAndIndex(t *testing.T) {
	for _, column := range []string{
		"id BIGINT AUTO_INCREMENT PRIMARY KEY", "topic_id VARCHAR(64) NOT NULL",
		"source_message_id BIGINT NOT NULL", "source_attachment_index INT NOT NULL",
		"source_file_key VARCHAR(255) NOT NULL", "latest_file_key VARCHAR(255) NOT NULL",
		"latest_url VARCHAR(1024) NOT NULL", "latest_name VARCHAR(255) NOT NULL",
		"latest_size BIGINT NOT NULL DEFAULT 0", "latest_mime_type VARCHAR(255) NOT NULL",
		"updated_by_uid BIGINT NOT NULL", "created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP",
		"updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP",
	} {
		if !strings.Contains(createFileVersionBindingsTable, column) {
			t.Fatalf("file_version_bindings missing %q", column)
		}
	}
	if !strings.Contains(createFileVersionBindingsTable, "UNIQUE KEY uk_file_version_bindings_source (topic_id, source_message_id, source_attachment_index)") {
		t.Fatal("file_version_bindings unique source triple missing")
	}
	if !strings.Contains(createFileVersionBindingsTable, "INDEX idx_file_version_bindings_topic_updated (topic_id, updated_at)") {
		t.Fatal("file_version_bindings topic index missing")
	}
}
