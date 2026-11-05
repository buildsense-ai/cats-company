package mysql

import (
	"context"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store/types"
	"regexp"
	"strings"
	"testing"
	"time"
)

func surfaceSQL(q string) string {
	if !false {
		return q
	}
	i := 0
	var b strings.Builder
	for _, c := range q {
		if c == '?' {
			i++
			fmt.Fprintf(&b, "$%d", i)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func TestNativeSurfaceAtomicEnrollment(t *testing.T) {
	for _, mode := range []string{"insert", "conflict", "limit", "replace", "stale-version", "cross-account"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &Adapter{db: db}
			row := &types.PushSubscription{UID: 7, Endpoint: "surface://7/registration-current", RegistrationID: "generation-new", P256DH: "new-meta", Auth: "new-hash"}
			var previous *types.PushSubscription
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(surfaceSQL(`SELECT id FROM users WHERE id = ? FOR UPDATE`))).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			want := mode == "insert" || mode == "replace"
			switch mode {
			case "replace", "stale-version", "cross-account":
				previous = &types.PushSubscription{UID: 7, Endpoint: row.Endpoint, RegistrationID: "generation-old", P256DH: "expired-meta", Auth: "old-hash"}
				if mode == "cross-account" {
					previous.UID = 9
				} else {
					n := int64(1)
					if mode == "stale-version" {
						n = 0
					}
					mock.ExpectExec(regexp.QuoteMeta(surfaceSQL(`UPDATE push_subscriptions SET p256dh = ?, auth = ?, registration_id = ? WHERE uid = ? AND endpoint = ? AND registration_id = ? AND p256dh = ? AND auth = ?`))).WithArgs(row.P256DH, row.Auth, row.RegistrationID, row.UID, row.Endpoint, previous.RegistrationID, previous.P256DH, previous.Auth).WillReturnResult(sqlmock.NewResult(0, n))
				}
			default:
				count := 9
				if mode == "limit" {
					count = 10
				}
				mock.ExpectQuery(regexp.QuoteMeta(surfaceSQL(`SELECT COUNT(*) FROM push_subscriptions WHERE uid = ?`))).WithArgs(row.UID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
				if mode != "limit" {
					q := `INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id) VALUES (?, ?, ?, ?, ?)`
					if false {
						q += ` ON CONFLICT (endpoint) DO NOTHING`
					}
					exp := mock.ExpectExec(regexp.QuoteMeta(surfaceSQL(q))).WithArgs(row.UID, row.Endpoint, row.P256DH, row.Auth, row.RegistrationID)
					if mode == "conflict" {
						if false {
							exp.WillReturnResult(sqlmock.NewResult(0, 0))
						} else {
							exp.WillReturnError(&mysqlDriver.MySQLError{Number: 1062, Message: "duplicate"})
						}
					} else {
						exp.WillReturnResult(sqlmock.NewResult(1, 1))
					}
				}
			}
			if want {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			ok, err := a.ReplacePushSubscription(context.Background(), row, previous, 10)
			if err != nil || ok != want {
				t.Fatal(ok, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeSurfaceSQLReadsHonorContext(t *testing.T) {
	for _, stage := range []string{"user", "managed", "membership", "recipients", "friend", "owner", "task", "current", "title", "group-title"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			reader := (&Adapter{db: db}).NativeSurfaceReader()
			pattern := ""
			switch stage {
			case "user":
				pattern = "SELECT id, username, display_name, account_type, state FROM users"
			case "managed":
				pattern = "SELECT group_kind"
			case "membership":
				mock.ExpectQuery("SELECT group_kind").WillReturnRows(sqlmock.NewRows([]string{"managed"}).AddRow(false))
				pattern = "SELECT COUNT.*FROM group_members"
			case "recipients":
				pattern = "SELECT user_id FROM group_members"
			case "friend":
				pattern = "SELECT COUNT.*FROM friends"
			case "owner":
				mock.ExpectQuery("SELECT COUNT.*FROM friends").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				pattern = "SELECT COUNT.*FROM bot_config"
			case "task":
				pattern = "SELECT topic_id, run_id.*"
			case "current":
				pattern = "SELECT topic_id, run_id.*"
			case "title":
				pattern = "SELECT title FROM conversation_titles"
			case "group-title":
				mock.ExpectQuery("SELECT title FROM conversation_titles").WillReturnRows(sqlmock.NewRows([]string{"title"}))
				pattern = "SELECT name FROM"
			}
			mock.ExpectQuery(pattern).WillDelayFor(time.Second).WillReturnError(errors.New("late query"))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			start := time.Now()
			switch stage {
			case "user":
				_, err = reader.User(ctx, 7)
			case "managed", "membership":
				_, _, err = reader.GroupAccess(ctx, 80, 7)
			case "recipients":
				_, err = reader.Recipients(ctx, 80)
			case "friend", "owner":
				_, err = reader.PeerAccess(ctx, 7, 8)
			case "task":
				_, err = reader.TaskRun(ctx, "p2p_7_8", 8, "run-B")
			case "current":
				_, err = reader.CurrentTask(ctx, "p2p_7_8")
			case "title", "group-title":
				_, err = reader.Title(ctx, 7, "grp_80", 80, 0)
			}
			if err == nil || time.Since(start) > 500*time.Millisecond {
				t.Fatal("query was not canceled", err, time.Since(start))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeSurfaceExactLedgerRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader := (&Adapter{db: db}).NativeSurfaceReader()
	now := time.Now()
	// Query shape must depend only on the registered identity. Neither a
	// later-inserted old terminal receipt nor an ambiguous other active run
	// proves execution/start order in this latest-receipt-only ledger.
	for _, state := range []string{"running", "waiting", "completed"} {
		query := `SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at FROM conversation_task_runs WHERE topic_id = ? AND source_uid = ? AND run_id = ?`
		mock.ExpectQuery(regexp.QuoteMeta(surfaceSQL(query))).WithArgs("p2p_7_8", int64(8), "B").WillReturnRows(sqlmock.NewRows([]string{"topic", "run", "state", "summary", "error", "source", "updated", "event", "expires"}).AddRow("p2p_7_8", "B", state, "", "", 8, now, now, nil))
		status, err := reader.TaskRun(context.Background(), "p2p_7_8", 8, "B")
		if err != nil || status == nil || status.RunID != "B" || status.State != state {
			t.Fatal(status, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
