package sessionfork

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
)

// Production does not write role='user': the plain-text send path stores only
// topic, sender, content and type, so human rows carry an empty role while bot
// rows carry 'assistant'. These fixtures mirror that shape; a role-based rule
// would count zero turns against them.
const agentUID int64 = 9

func human() message { return message{from: 7, role: sql.NullString{}} }
func bot() message {
	return message{from: agentUID, role: sql.NullString{String: "assistant", Valid: true}}
}
func botPlain() message {
	return message{from: agentUID, role: sql.NullString{}}
}

func TestCountCompleteTurns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []message
		want     int64
	}{
		{"empty range", nil, 0},
		{"single answered turn", []message{human(), bot()}, 1},
		{"two answered turns", []message{human(), bot(), human(), bot()}, 2},
		// The runtime still holds this trailing turn. Counting it would make the
		// runtime cut one turn further than the cloud range intends.
		{"trailing unanswered user", []message{human(), bot(), human()}, 1},
		// A user sending twice before any answer is one turn, not two. Both sides
		// must fold it the same way or the runtime cuts between the two messages.
		{"consecutive user messages fold", []message{human(), human(), bot()}, 1},
		{"assistant only", []message{bot(), bot()}, 0},
		// Any agent message answers the open turn, even without a role: production
		// writes ~6% of agent rows with an empty role, and requiring
		// role='assistant' would undercount turns here while the runtime counted
		// them.
		{"agent without role still answers", []message{human(), botPlain()}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := countCompleteTurns(tc.messages, agentUID); got != tc.want {
				t.Fatalf("countCompleteTurns(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// The regression this guards: counting by `role` alone returns zero on real
// data, which made every bounded copy fail its anchor check and every turn count
// read as "unbounded".
func TestCountCompleteTurnsOnProductionShape(t *testing.T) {
	history := []message{human(), bot(), human(), bot()}
	if got := countCompleteTurns(history, agentUID); got != 2 {
		t.Fatalf("production-shaped history counts %d turns, want 2", got)
	}
}

// A copy may only be cut where the turn actually ends. The regression this
// guards: a reply longer than the 4000-character segment limit is stored as
// several messages, so one turn reads [user, agent, agent, agent] in the cloud. An
// anchor on the first agent segment is "answered" but is not the turn's end, and
// accepting it truncated the copy while the runtime kept the whole turn — the very
// mismatch between visible history and model context this feature must avoid.
func TestAnchorClosesTurn(t *testing.T) {
	segmented := []message{human(), bot(), bot(), bot()}
	for _, tc := range []struct {
		name   string
		all    []message
		anchor int
		want   bool
	}{
		{"single answer closes the turn", []message{human(), bot()}, 1, true},
		// The regression: the first of three reply segments must be rejected.
		{"first of three reply segments", segmented, 1, false},
		{"middle of three reply segments", segmented, 2, false},
		{"last of three reply segments", segmented, 3, true},
		{"anchor on the user message", []message{human(), bot()}, 0, false},
		{"more turns follow", []message{human(), bot(), human(), bot()}, 1, true},
		{"anchor on trailing user message", []message{human(), bot(), human()}, 2, false},
		{"agent message before more agent work", []message{human(), bot(), bot(), human()}, 1, false},
		{"out of range", []message{human(), bot()}, 5, false},
		{"empty conversation", nil, 0, false},
		// Regression: a conversation that opens with an agent message scores zero
		// turns at that anchor. Zero is indistinguishable from "unbounded" on the
		// wire (`source_turn_count` is omitempty), so the runtime would copy the
		// whole transcript while the cloud copied a prefix. Production has such
		// conversations: 10 two-member human+bot groups start with a bot message.
		{"agent-only conversation", []message{bot()}, 0, false},
		{"agent first, anchor before the first user message", []message{bot(), human(), bot()}, 0, false},
		{"agent first, anchor on the answered turn", []message{bot(), human(), bot()}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorClosesTurn(tc.all, tc.anchor, agentUID); got != tc.want {
				t.Fatalf("anchorClosesTurn(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A copy is named after its source with a running suffix. The suffix is computed
// from the owner's existing names, and a name the user already ended with "(n)"
// is taken whole: guessing which parenthetical is a copy marker would rewrite a
// name the user chose.
func TestCopySuffix(t *testing.T) {
	for _, tc := range []struct {
		base, name string
		want       int64
		ok         bool
	}{
		{"周报", "周报(1)", 1, true},
		{"周报", "周报(12)", 12, true},
		{"周报", "周报", 0, false},
		{"周报", "周报(1) 副本", 0, false},
		{"周报", "周报(abc)", 0, false},
		{"周报", "周报()", 0, false},
		{"周报", "周报(0)", 0, false},
		{"周报", "周报(-1)", 0, false},
		{"周报", "周报(1)(2)", 0, false},
		{"周报(1)", "周报(1)(3)", 3, true},
		{"", "(1)", 1, true},
		// A base that overflows the column is stored trimmed, so recognition has to
		// trim the same way. Matching against the untrimmed base would find nothing
		// and every copy after the first would be named "(1)" again.
		{strings.Repeat("周", 126), strings.Repeat("周", 125) + "(1)", 1, true},
		{strings.Repeat("周", 126), strings.Repeat("周", 125) + "(7)", 7, true},
		{strings.Repeat("周", 126), strings.Repeat("周", 124) + "(1)", 0, false},
		// A wide suffix leaves less room for the stem, and has to be recognised too.
		{strings.Repeat("周", 128), strings.Repeat("周", 122) + "(1000)", 1000, true},
		{strings.Repeat("周", 128), strings.Repeat("周", 123) + "(1000)", 0, false},
	} {
		got, ok := copySuffix(tc.base, tc.name)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("copySuffix(%q, %q) = %d,%v, want %d,%v", tc.base, tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// nextCopyTitle is the function that produces the stored name, so this calls it
// rather than repeating its arithmetic: the arithmetic being right proves nothing
// about the name that actually reaches the column.
func TestNextCopyTitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
		next int64
		want string
	}{
		{"first copy", "周报", 1, "周报(1)"},
		{"second copy", "周报", 2, "周报(2)"},
		{"wide suffix", "周报", 42, "周报(42)"},
		{"takes a name ending in (n) whole", "周报(1)", 2, "周报(1)(2)"},
	} {
		if got := nextCopyTitle(tc.base, tc.next); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A source name may already fill the column. Appending a suffix without trimming
// would overflow groups.name/topics.name (VARCHAR(128)); MySQL runs in strict mode
// here, so the insert would fail and the user would get a generic 500 instead of a
// copy. Trimming must be by rune, or a multi-byte character gets cut in half.
func TestNextCopyTitleRespectsColumnWidth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		baseRunes int
		next      int64
		wantRunes int
	}{
		{"a short name is untouched", 10, 1, 13},
		{"exactly fills the column", 125, 1, 128},
		{"one over", 126, 1, 128},
		{"a full-width name", 128, 1, 128},
		{"a long suffix leaves less room", 128, 100, 128},
	} {
		base := strings.Repeat("周", tc.baseRunes)
		got := nextCopyTitle(base, tc.next)
		if n := len([]rune(got)); n != tc.wantRunes {
			t.Fatalf("%s: %d runes, want %d (%q)", tc.name, n, tc.wantRunes, got)
		}
		if n := len([]rune(got)); n > copyNameLimit {
			t.Fatalf("%s: %d runes exceeds the column width %d", tc.name, n, copyNameLimit)
		}
		// The suffix has to survive the trim, or the name no longer identifies a copy.
		wantSuffix := "(" + strconv.FormatInt(tc.next, 10) + ")"
		if !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("%s: %q lost its suffix %q", tc.name, got, wantSuffix)
		}
	}
}

// Generation and recognition have to agree. Testing them apart is what let a
// length guard turn into duplicate names: the name was written trimmed while the
// lookup matched the untrimmed base, so the second copy of a long-named
// conversation was called "(1)" as well.
func TestNextCopyTitleNumbersCopiesOfALongName(t *testing.T) {
	base := strings.Repeat("周", 126)
	first := nextCopyTitle(base, 1)
	if n, ok := copySuffix(base, first); !ok || n != 1 {
		t.Fatalf("the first copy is not recognised: %q -> %d,%v", first, n, ok)
	}
	second := nextCopyTitle(base, 2)
	if first == second {
		t.Fatalf("two copies share a name: %q", first)
	}
	if n, ok := copySuffix(base, second); !ok || n != 2 {
		t.Fatalf("the second copy is not recognised: %q -> %d,%v", second, n, ok)
	}
	if len([]rune(second)) > copyNameLimit {
		t.Fatalf("the second copy overflows the column: %d runes", len([]rune(second)))
	}
}
