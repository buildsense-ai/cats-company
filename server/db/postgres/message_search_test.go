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
		"STRPOS(LOWER(m.content), LOWER($3))",
		"ORDER BY m.created_at DESC, m.id DESC",
	}
	for _, fragment := range required {
		if !strings.Contains(postgresMessageSearchQuery, fragment) {
			t.Errorf("query missing access/search fragment %q", fragment)
		}
	}
}
