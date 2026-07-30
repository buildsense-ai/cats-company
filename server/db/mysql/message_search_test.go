package mysql

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatchingArtifactName(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		query string
		want  string
	}{
		{name: "block name case insensitive", raw: `[{"type":"file","name":"Quarterly Report.PDF"}]`, query: "report.pdf", want: "Quarterly Report.PDF"},
		{name: "payload filename", raw: `[{"type":"image","payload":{"filename":"Launch Diagram.PNG"}}]`, query: "diagram", want: "Launch Diagram.PNG"},
		{name: "non artifact ignored", raw: `[{"type":"text","name":"secret-report.pdf","text":"hello"}]`, query: "secret", want: ""},
		{name: "unrelated payload ignored", raw: `[{"type":"file","payload":{"url":"/report.pdf"}}]`, query: "report", want: ""},
		{name: "invalid json", raw: `{`, query: "report", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchingArtifactName([]byte(tc.raw), tc.query); got != tc.want {
				t.Fatalf("matchingArtifactName()=%q, want %q", got, tc.want)
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
		if got := legacyMatchingArtifactName(tc.content, tc.query); got != tc.want {
			t.Fatalf("legacy artifact name=%q, want %q", got, tc.want)
		}
	}
}

func TestMessageSearchSnippetCentersUnicodeMatch(t *testing.T) {
	content := strings.Repeat("前", 130) + "命中词" + strings.Repeat("后", 130)
	got := messageSearchSnippet(content, "命中词")
	if !strings.Contains(got, "命中词") {
		t.Fatalf("snippet does not contain match: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet should indicate truncation: %q", got)
	}
	if count := utf8.RuneCountInString(strings.Trim(got, "…")); count > 160 {
		t.Fatalf("snippet body has %d runes, want at most 160", count)
	}
	if got := messageSearchSnippet("  short text  ", "text"); got != "short text" {
		t.Fatalf("short snippet=%q", got)
	}
}

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
		"LOCATE(LOWER(?), LOWER(m.content))",
		"ORDER BY m.created_at DESC, m.id DESC",
	}
	for _, fragment := range required {
		if !strings.Contains(mysqlMessageSearchQuery, fragment) {
			t.Errorf("query missing access/search fragment %q", fragment)
		}
	}
}

func TestFileMessageContentDoesNotBecomeMessageMatch(t *testing.T) {
	legacyContent := `{"type":"file","payload":{"url":"/uploads/secret-report.pdf"}}`
	if mysqlMessageContentMatches("file", legacyContent, "secret-report") {
		t.Fatal("legacy file JSON metadata must not become a message-body match")
	}
	if !mysqlMessageContentMatches("text", "the secret-report is ready", "secret-report") {
		t.Fatal("text message content should remain searchable")
	}
}

func TestSearchCandidateSemanticFilteringAndPagination(t *testing.T) {
	if mysqlShouldIncludeSearchCandidate("all", false, "") {
		t.Fatal("all search must drop metadata-only false positives")
	}
	if !mysqlShouldIncludeSearchCandidate("all", true, "") {
		t.Fatal("all search must retain content matches")
	}
	if !mysqlShouldIncludeSearchCandidate("all", false, "Report.PDF") {
		t.Fatal("all search must retain verified artifact-name matches")
	}
	if mysqlShouldIncludeSearchCandidate("artifact", true, "") {
		t.Fatal("artifact search must not retain content-only matches")
	}
	if !mysqlShouldContinueSearch(0, 20, 200, 200) {
		t.Fatal("a full false-positive page must continue to the next page")
	}
	if mysqlShouldContinueSearch(20, 20, 200, 200) {
		t.Fatal("search must stop once the requested result limit is met")
	}
	if mysqlShouldContinueSearch(0, 20, 199, 200) {
		t.Fatal("search must stop after a short final page")
	}
}
