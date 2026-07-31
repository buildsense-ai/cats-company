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
	if MessageSearchContentMatches("image", "visible caption", "visible") {
		t.Fatal("non-text messages must not become message-body matches")
	}
	if !MessageSearchContentMatches("text", "visible reply", "reply") {
		t.Fatal("text messages must remain searchable")
	}
}

func TestMessageSearchHasInternalBlocks(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "no blocks", raw: "", want: false},
		{name: "visible text", raw: `[{"type":"text","text":"hello"}]`, want: false},
		{name: "visible attachment", raw: `[{"type":"text","text":"see file"},{"type":"file","name":"report.pdf"}]`, want: false},
		{name: "thinking", raw: `[{"type":"thinking","thinking":"secret"}]`, want: true},
		{name: "tool use", raw: `[{"type":"tool_use","name":"grep"}]`, want: true},
		{name: "tool result", raw: `[{"type":"tool_result","content":"secret"}]`, want: true},
		{name: "runtime plan", raw: `[{"type":"runtime_plan","content":"secret"}]`, want: true},
		{name: "invalid json fails closed", raw: `{`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessageSearchHasInternalBlocks([]byte(tc.raw)); got != tc.want {
				t.Fatalf("MessageSearchHasInternalBlocks()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestMixedInternalBlocksOnlyExposeArtifactName(t *testing.T) {
	raw := []byte(`[
		{"type":"tool_result","content":"secret tool output"},
		{"type":"file","name":"Visible Report.PDF"}
	]`)
	if !MessageSearchHasInternalBlocks(raw) {
		t.Fatal("mixed message must suppress its body from search")
	}
	if got := MatchingArtifactName(raw, "report"); got != "Visible Report.PDF" {
		t.Fatalf("artifact name=%q, want Visible Report.PDF", got)
	}
}

func TestMatchMessageSearchCandidate(t *testing.T) {
	tests := []struct {
		name       string
		candidate  MessageSearchCandidate
		query      string
		searchType string
		wantOK     bool
		wantType   string
		wantName   string
	}{
		{
			name: "visible message",
			candidate: MessageSearchCandidate{
				Result:      MessageSearchResult{Content: "visible reply"},
				MessageType: "text",
			},
			query:      "reply",
			searchType: MessageSearchAll,
			wantOK:     true,
			wantType:   MessageSearchMessage,
		},
		{
			name: "internal body suppressed",
			candidate: MessageSearchCandidate{
				Result:        MessageSearchResult{Content: "secret reply"},
				MessageType:   "text",
				ContentBlocks: []byte(`[{"type":"tool_result","content":"secret reply"}]`),
			},
			query:      "secret",
			searchType: MessageSearchAll,
		},
		{
			name: "mixed message returns verified artifact",
			candidate: MessageSearchCandidate{
				Result:      MessageSearchResult{Content: "secret tool output"},
				MessageType: "text",
				ContentBlocks: []byte(`[
					{"type":"tool_result","content":"secret tool output"},
					{"type":"file","name":"Visible Report.PDF"}
				]`),
			},
			query:      "report",
			searchType: MessageSearchAll,
			wantOK:     true,
			wantType:   MessageSearchArtifact,
			wantName:   "Visible Report.PDF",
		},
		{
			name: "legacy file",
			candidate: MessageSearchCandidate{
				Result:      MessageSearchResult{Content: `"{\"filename\":\"Old Report.PDF\"}"`},
				MessageType: "file",
			},
			query:      "report",
			searchType: MessageSearchArtifact,
			wantOK:     true,
			wantType:   MessageSearchArtifact,
			wantName:   "Old Report.PDF",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MatchMessageSearchCandidate(tc.candidate, tc.query, tc.searchType)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.ContentType != tc.wantType || got.ArtifactName != tc.wantName {
				t.Fatalf("result type=%q artifact=%q, want type=%q artifact=%q",
					got.ContentType, got.ArtifactName, tc.wantType, tc.wantName)
			}
		})
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
		{`{"type":"file","payload":{"name":"Old Report.PDF","url":"/uploads/unrelated-secret.pdf"}}`, "report", "Old Report.PDF"},
		{`{"type":"file","payload":{"name":"Old Report.PDF","url":"/uploads/unrelated-secret.pdf"}}`, "secret", ""},
		{`{"filename":"Archive.ZIP"}`, "archive", "Archive.ZIP"},
		{`"{\"filename\":\"Escaped Report.PDF\"}"`, "report", "Escaped Report.PDF"},
		{`{"filename":"R\u0065port.pdf"}`, "report", "Report.pdf"},
		{`{"filename":"Q1 \"Final\" Report.pdf"}`, "report", `Q1 "Final" Report.pdf`},
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
