package postgres

import (
	"strings"
	"testing"
)

func TestJSONBytesSupportsDriverRepresentations(t *testing.T) {
	if got := string(jsonBytes([]byte(`[]`))); got != `[]` {
		t.Fatalf("byte representation=%q", got)
	}
	if got := string(jsonBytes(`[]`)); got != `[]` {
		t.Fatalf("string representation=%q", got)
	}
	if got := string(jsonBytes([]interface{}{map[string]interface{}{"type": "file"}})); !strings.Contains(got, `"type":"file"`) {
		t.Fatalf("structured representation=%q", got)
	}
}

func TestPostgresMessageSearchQueryFiltersAccessInSelection(t *testing.T) {
	required := []string{
		"gm.user_id = viewer.id",
		"gm.user_id IS NOT NULL",
		"viewer.account_type <> 'human'",
		"peer.id IS NOT NULL",
		"peer_bot.owner_id = viewer.id",
		"f.from_user_id = viewer.id",
		"f.to_user_id = peer.id",
		"f.status = 'accepted'",
		"sender.account_type IN ('human', 'bot')",
		"END AS search_blocks_valid",
		"jsonb_typeof(typed_block) NOT IN ('object', 'null')",
		"jsonb_object_keys(typed_block)",
		"typed_block->'payload' ? 'filename'",
		"m.search_blocks_valid",
		"m.msg_type = 'text'",
		"jsonb_array_elements(m.content_blocks)",
		"artifact->>'type' IN ('file', 'image', 'audio', 'video')",
		"artifact->'payload'->>'filename'",
		"'tool_use'",
		"'tool_result'",
		"'thinking'",
		"'runtime_plan'",
		"pg_input_is_valid(content, 'jsonb')",
		"pg_input_is_valid(content::jsonb #>> '{}', 'jsonb')",
		"jsonb_typeof(m.search_legacy_content->'payload') = 'object'",
		"jsonb_typeof(legacy_file.content->'filename') = 'string'",
		"legacy_file.content->>'file_name'",
		"STRPOS(LOWER(m.content), LOWER($3))",
		"STRPOS(LOWER(COALESCE(artifact->'payload'->>'title', '')), LOWER($3))",
		"STRPOS(LOWER(legacy_file.content->>'title'), LOWER($3))",
		"ORDER BY m.created_at DESC, m.id DESC",
	}
	for _, fragment := range required {
		if !strings.Contains(postgresMessageSearchQuery, fragment) {
			t.Errorf("query missing access/search fragment %q", fragment)
		}
	}
	if strings.Contains(postgresMessageSearchQuery, "LEFT JOIN LATERAL (") {
		t.Error("legacy JSON expansion must stay inside the file-search branch")
	}
	if strings.Contains(postgresMessageSearchQuery, "CONCAT_WS(' ',") {
		t.Error("filename fields must be matched independently")
	}
}
