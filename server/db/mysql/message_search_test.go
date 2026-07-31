package mysql

import (
	"strings"
	"testing"
)

func TestMySQLMessageSearchQueryFiltersAccessInSelection(t *testing.T) {
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
		"SELECT ? AS search_type, LOWER(?) AS needle",
		"m.msg_type = 'text'",
		"JSON_SEARCH(m.content_blocks, 'one', 'tool_use', NULL, '$[*].type')",
		"FROM JSON_TABLE(",
		"COALESCE(m.content_blocks, JSON_ARRAY())",
		"artifact.block_type IN ('file', 'image', 'audio', 'video')",
		"artifact.payload_filename",
		"'tool_use'",
		"'tool_result'",
		"'thinking'",
		"'runtime_plan'",
		"JSON_VALID(content)",
		"JSON_VALID(JSON_UNQUOTE(content))",
		"JSON_TYPE(JSON_EXTRACT(m.search_legacy_content, '$.payload')) = 'OBJECT'",
		"legacy_file.file_name",
		"LOCATE(search.needle, LOWER(m.content))",
		"LOCATE(search.needle, LOWER(COALESCE(artifact.payload_title, '')))",
		"LOCATE(search.needle, LOWER(COALESCE(legacy_file.title, '')))",
		"ORDER BY m.created_at DESC, m.id DESC",
	}
	for _, fragment := range required {
		if !strings.Contains(mysqlMessageSearchQuery, fragment) {
			t.Errorf("query missing access/search fragment %q", fragment)
		}
	}
	if strings.Contains(mysqlMessageSearchQuery, "LEFT JOIN JSON_TABLE(") {
		t.Error("legacy JSON expansion must stay inside the file-search branch")
	}
	if strings.Contains(mysqlMessageSearchQuery, "CONCAT_WS(' ',") {
		t.Error("filename fields must be matched independently")
	}
}
