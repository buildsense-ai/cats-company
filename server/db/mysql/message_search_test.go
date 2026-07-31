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
		"m.msg_type = 'text'",
		"JSON_SEARCH(m.content_blocks, 'one', 'tool_use', NULL, '$[*].type')",
		"JSON_SEARCH(m.content_blocks, 'one', 'file', NULL, '$[*].type')",
		"'$[*].payload.filename'",
		"'tool_use'",
		"'tool_result'",
		"'thinking'",
		"'runtime_plan'",
		"JSON_VALID(m.content)",
		"'$.payload.file_name'",
		"LOCATE(LOWER(?), LOWER(m.content))",
		"ORDER BY m.created_at DESC, m.id DESC",
	}
	for _, fragment := range required {
		if !strings.Contains(mysqlMessageSearchQuery, fragment) {
			t.Errorf("query missing access/search fragment %q", fragment)
		}
	}
}
