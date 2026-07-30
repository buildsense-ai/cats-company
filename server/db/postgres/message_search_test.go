package postgres

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPostgresMatchingArtifactName(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		query string
		want  string
	}{
		{name: "block name case insensitive", raw: `[{"type":"file","name":"Quarterly Report.PDF"}]`, query: "report.pdf", want: "Quarterly Report.PDF"},
		{name: "payload title", raw: `[{"type":"video","payload":{"title":"Product Demo.MP4"}}]`, query: "demo", want: "Product Demo.MP4"},
		{name: "non artifact ignored", raw: `[{"type":"text","name":"secret-report.pdf","text":"hello"}]`, query: "secret", want: ""},
		{name: "unrelated payload ignored", raw: `[{"type":"audio","payload":{"url":"/report.mp3"}}]`, query: "report", want: ""},
		{name: "invalid json", raw: `{`, query: "report", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := postgresMatchingArtifactName([]byte(tc.raw), tc.query); got != tc.want {
				t.Fatalf("postgresMatchingArtifactName()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyMatchingArtifactName(t *testing.T) {
	tests := []struct{ content, query, want string }{
		{`{"type":"file","payload":{"name":"Old Report.PDF","url":"/uploads/old.pdf"}}`, "report", "Old Report.PDF"},
		{`{"filename":"Archive.ZIP"}`, "archive", "Archive.ZIP"},
		{`{"url":"/uploads/secret-report.pdf"}`, "report", ""},
	}
	for _, tc := range tests {
		if got := postgresLegacyMatchingArtifactName(tc.content, tc.query); got != tc.want {
			t.Fatalf("legacy artifact name=%q, want %q", got, tc.want)
		}
	}
}

func TestPostgresMessageSearchSnippetCentersUnicodeMatch(t *testing.T) {
	content := strings.Repeat("前", 130) + "命中词" + strings.Repeat("后", 130)
	got := postgresMessageSearchSnippet(content, "命中词")
	if !strings.Contains(got, "命中词") {
		t.Fatalf("snippet does not contain match: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet should indicate truncation: %q", got)
	}
	if count := utf8.RuneCountInString(strings.Trim(got, "…")); count > 160 {
		t.Fatalf("snippet body has %d runes, want at most 160", count)
	}
	if got := postgresMessageSearchSnippet("  short text  ", "text"); got != "short text" {
		t.Fatalf("short snippet=%q", got)
	}
}

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

func TestFileMessageContentDoesNotBecomeMessageMatch(t *testing.T) {
	legacyContent := `{"type":"file","payload":{"url":"/uploads/secret-report.pdf"}}`
	if postgresMessageContentMatches("file", legacyContent, "secret-report") {
		t.Fatal("legacy file JSON metadata must not become a message-body match")
	}
	if !postgresMessageContentMatches("text", "the secret-report is ready", "secret-report") {
		t.Fatal("text message content should remain searchable")
	}
}

func TestSearchCandidateSemanticFilteringAndPagination(t *testing.T) {
	if postgresShouldIncludeSearchCandidate("all", false, "") {
		t.Fatal("all search must drop metadata-only false positives")
	}
	if !postgresShouldIncludeSearchCandidate("all", true, "") {
		t.Fatal("all search must retain content matches")
	}
	if !postgresShouldIncludeSearchCandidate("all", false, "Report.PDF") {
		t.Fatal("all search must retain verified artifact-name matches")
	}
	if postgresShouldIncludeSearchCandidate("artifact", true, "") {
		t.Fatal("artifact search must not retain content-only matches")
	}
	if !postgresShouldContinueSearch(0, 20, 200, 200) {
		t.Fatal("a full false-positive page must continue to the next page")
	}
	if postgresShouldContinueSearch(20, 20, 200, 200) {
		t.Fatal("search must stop once the requested result limit is met")
	}
	if postgresShouldContinueSearch(0, 20, 199, 200) {
		t.Fatal("search must stop after a short final page")
	}
}
