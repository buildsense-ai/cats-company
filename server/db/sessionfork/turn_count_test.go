package sessionfork

import (
	"database/sql"
	"testing"
)

func role(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }

func TestCountCompleteTurns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roles []string
		want  int64
	}{
		{"empty range", nil, 0},
		{"single answered turn", []string{"user", "assistant"}, 1},
		{"two answered turns", []string{"user", "assistant", "user", "assistant"}, 2},
		// The runtime still holds this trailing turn. Counting it would make the
		// runtime cut one turn further than the cloud range intends.
		{"trailing unanswered user", []string{"user", "assistant", "user"}, 1},
		// A user message arriving before any answer collapses into one turn, so
		// the count cannot overstate how much history was copied.
		{"consecutive user messages", []string{"user", "user", "assistant"}, 1},
		{"assistant only", []string{"assistant", "assistant"}, 0},
		// Tool traffic and unknown roles carry no turn boundary of their own.
		{"tool messages ignored", []string{"user", "assistant", "tool", "tool", "assistant"}, 1},
		{"unknown roles ignored", []string{"system", "user", "assistant"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make([]message, 0, len(tc.roles))
			for _, r := range tc.roles {
				messages = append(messages, message{role: role(r)})
			}
			if got := countCompleteTurns(messages); got != tc.want {
				t.Fatalf("countCompleteTurns(%v) = %d, want %d", tc.roles, got, tc.want)
			}
		})
	}
}

func TestCountCompleteTurnsIgnoresNullRoles(t *testing.T) {
	messages := []message{{role: role("user")}, {role: sql.NullString{}}, {role: role("assistant")}}
	if got := countCompleteTurns(messages); got != 1 {
		t.Fatalf("null role must not create a turn boundary: %d", got)
	}
}
