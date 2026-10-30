package sessionfork

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

// copyTitle decides three things the pure helpers cannot: which name the copy
// takes, where that name comes from, and which existing names it has to avoid.
// Testing nextCopyTitle alone left the source-name lookup and the collision
// scan unexercised, and the collision bug that a length guard introduced lived
// exactly in that gap.
func TestCopyTitle(t *testing.T) {
	long := strings.Repeat("周", 126)
	for _, tc := range []struct {
		name string
		req  types.SessionForkRequest
		// sourceName and agentName are the two lookups; empty means no row.
		sourceName string
		agentName  string
		existing   []string
		want       string
	}{
		{
			name:       "names the copy after the source conversation",
			sourceName: "周报",
			want:       "周报(1)",
		},
		{
			name:       "keeps counting past an existing copy",
			sourceName: "周报",
			existing:   []string{"周报(1)", "周报(2)"},
			want:       "周报(3)",
		},
		{
			name:       "ignores names that are not copies of this source",
			sourceName: "周报",
			existing:   []string{"日报(1)", "周报 归档", "周报(一)"},
			want:       "周报(1)",
		},
		{
			name:       "takes a name that already ends in (n) whole",
			sourceName: "周报(1)",
			existing:   []string{"周报(1)(1)"},
			want:       "周报(1)(2)",
		},
		{
			name:      "falls back to the agent for a direct conversation",
			agentName: "CatsCo",
			want:      "CatsCo(1)",
		},
		{
			name: "falls back to a plain word when nothing is named",
			want: "会话(1)",
		},
		{
			name: "honours a name the caller supplied",
			req:  types.SessionForkRequest{Name: "我自己起的"},
			want: "我自己起的(1)",
		},
		{
			// The copy is stored trimmed to fit the column; recognition has to trim
			// the same way, or the second copy is also called (1).
			name:       "a source name that fills the column still numbers copies",
			sourceName: long,
			existing:   []string{strings.Repeat("周", 125) + "(1)"},
			want:       strings.Repeat("周", 125) + "(2)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			mock.ExpectBegin()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}

			req := tc.req
			req.SourceTopicID = "p2p_7_9"
			req.AgentUID = 9
			req.ActorUserID = 7

			if strings.TrimSpace(req.Name) == "" {
				sourceRow := sqlmock.NewRows([]string{"name"})
				if tc.sourceName != "" {
					sourceRow.AddRow(tc.sourceName)
				}
				mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM topics WHERE id = ?")).
					WithArgs("p2p_7_9").WillReturnRows(sourceRow)
			}
			if strings.TrimSpace(req.Name) == "" && tc.sourceName == "" {
				agentRow := sqlmock.NewRows([]string{"display_name"})
				if tc.agentName != "" {
					agentRow.AddRow(tc.agentName)
				}
				mock.ExpectQuery(regexp.QuoteMeta("SELECT display_name FROM users WHERE id = ?")).
					WithArgs(int64(9)).WillReturnRows(agentRow)
			}
			rows := sqlmock.NewRows([]string{"name"})
			for _, name := range tc.existing {
				rows.AddRow(name)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM topics WHERE owner_id = ?")).
				WithArgs(int64(7)).WillReturnRows(rows)

			got, err := SQLStore{DB: db}.copyTitle(context.Background(), tx, req)
			if err != nil {
				t.Fatalf("copyTitle: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if n := len([]rune(got)); n > copyNameLimit {
				t.Fatalf("%q is %d runes, past the %d-rune column", got, n, copyNameLimit)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A failed lookup must surface instead of silently naming the copy 「会话(1)」:
// the caller would create a conversation under a name the user never chose.
func TestCopyTitlePropagatesLookupFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM topics WHERE id = ?")).
		WithArgs("p2p_7_9").WillReturnError(sql.ErrConnDone)

	_, err = SQLStore{DB: db}.copyTitle(context.Background(), tx, types.SessionForkRequest{
		SourceTopicID: "p2p_7_9", AgentUID: 9, ActorUserID: 7,
	})
	if err == nil {
		t.Fatal("want the lookup error, got a name")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
