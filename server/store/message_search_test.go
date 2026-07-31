package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessageSearchPagerBoundsCandidateScanning(t *testing.T) {
	totalScanned := 0
	pages := 0

	_, err := CollectMessageSearchResults(20, func(pageLimit, _, _ int) ([]*MessageSearchResult, int, error) {
		pages++
		totalScanned += pageLimit
		return nil, pageLimit, nil
	})
	if err != nil {
		t.Fatalf("collect results: %v", err)
	}

	if totalScanned != maxMessageSearchCandidateRows {
		t.Fatalf("scanned=%d, want hard cap %d", totalScanned, maxMessageSearchCandidateRows)
	}
	if pages != 10 {
		t.Fatalf("pages=%d, want 10 bounded pages", pages)
	}
}

func TestMessageSearchPagerStopsForResultsAndShortPages(t *testing.T) {
	t.Run("result limit reached", func(t *testing.T) {
		calls := 0
		_, err := CollectMessageSearchResults(20, func(pageLimit, _, _ int) ([]*MessageSearchResult, int, error) {
			calls++
			return make([]*MessageSearchResult, 20), pageLimit, nil
		})
		if err != nil || calls != 1 {
			t.Fatalf("err=%v calls=%d, want one page", err, calls)
		}
	})

	t.Run("short final page", func(t *testing.T) {
		calls := 0
		_, err := CollectMessageSearchResults(20, func(_, _, _ int) ([]*MessageSearchResult, int, error) {
			calls++
			return nil, 199, nil
		})
		if err != nil || calls != 1 {
			t.Fatalf("err=%v calls=%d, want one short page", err, calls)
		}
	})
}

func TestMessageSearchCandidateSemantics(t *testing.T) {
	if ShouldIncludeMessageSearchCandidate(MessageSearchAll, false, "") {
		t.Fatal("all search must drop metadata-only false positives")
	}
	if !ShouldIncludeMessageSearchCandidate(MessageSearchAll, true, "") {
		t.Fatal("all search must retain content matches")
	}
	if !ShouldIncludeMessageSearchCandidate(MessageSearchAll, false, "Report.PDF") {
		t.Fatal("all search must retain verified artifact-name matches")
	}
	if ShouldIncludeMessageSearchCandidate(MessageSearchArtifact, true, "") {
		t.Fatal("artifact search must not retain content-only matches")
	}
	if MessageSearchContentMatches("file", `{"type":"file"}`, "file") {
		t.Fatal("file metadata must not become a message-body match")
	}
}

func TestMatchingArtifactName(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		query string
		want  string
	}{
		{name: "block name", raw: `[{"type":"file","name":"Quarterly Report.PDF"}]`, query: "report.pdf", want: "Quarterly Report.PDF"},
		{name: "payload filename", raw: `[{"type":"image","payload":{"filename":"Launch Diagram.PNG"}}]`, query: "diagram", want: "Launch Diagram.PNG"},
		{name: "non artifact ignored", raw: `[{"type":"text","name":"secret-report.pdf"}]`, query: "secret", want: ""},
		{name: "invalid json", raw: `{`, query: "report", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchingArtifactName([]byte(tc.raw), tc.query); got != tc.want {
				t.Fatalf("MatchingArtifactName()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyMatchingArtifactName(t *testing.T) {
	tests := []struct{ content, query, want string }{
		{`{"type":"file","payload":{"name":"Old Report.PDF"}}`, "report", "Old Report.PDF"},
		{`{"filename":"Archive.ZIP"}`, "archive", "Archive.ZIP"},
		{`{"url":"/uploads/secret-report.pdf"}`, "report", ""},
	}
	for _, tc := range tests {
		if got := LegacyMatchingArtifactName(tc.content, tc.query); got != tc.want {
			t.Fatalf("legacy artifact name=%q, want %q", got, tc.want)
		}
	}
}

func TestMessageSearchSnippetCentersUnicodeMatch(t *testing.T) {
	content := strings.Repeat("前", 130) + "命中词" + strings.Repeat("后", 130)
	got := MessageSearchSnippet(content, "命中词")
	if !strings.Contains(got, "命中词") {
		t.Fatalf("snippet does not contain match: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet should indicate truncation: %q", got)
	}
	if count := utf8.RuneCountInString(strings.Trim(got, "…")); count > 160 {
		t.Fatalf("snippet body has %d runes, want at most 160", count)
	}
}
