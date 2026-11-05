// Package nativesurface contains the narrow, cancellable SQL contract used by
// background surfaces. These reads never reconcile or mutate task projections.
package nativesurface

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store/types"
)

type Reader struct {
	DB       *sql.DB
	Postgres bool
}

func (r Reader) query(q string) string {
	if !r.Postgres {
		return strings.ReplaceAll(q, `"groups"`, "`groups`")
	}
	n := 0
	var b strings.Builder
	for _, c := range q {
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (r Reader) User(ctx context.Context, uid int64) (*types.User, error) {
	u := &types.User{}
	err := r.DB.QueryRowContext(ctx, r.query(`SELECT id, username, display_name, account_type, state FROM users WHERE id = ?`), uid).Scan(&u.ID, &u.Username, &u.DisplayName, &u.AccountType, &u.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (r Reader) GroupAccess(ctx context.Context, gid, uid int64) (bool, bool, error) {
	var managed bool
	err := r.DB.QueryRowContext(ctx, r.query(`SELECT group_kind = 'channel_managed' FROM "groups" WHERE id = ?`), gid).Scan(&managed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil || managed {
		return managed, false, err
	}
	var count int
	err = r.DB.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM group_members WHERE group_id = ? AND user_id = ?`), gid, uid).Scan(&count)
	return false, count > 0, err
}

func (r Reader) Recipients(ctx context.Context, gid int64) ([]int64, error) {
	rows, err := r.DB.QueryContext(ctx, r.query(`SELECT user_id FROM group_members WHERE group_id = ?`), gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

func (r Reader) PeerAccess(ctx context.Context, uid, peer int64) (bool, error) {
	var count int
	err := r.DB.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM friends WHERE from_user_id = ? AND to_user_id = ? AND status = 'accepted'`), uid, peer).Scan(&count)
	if err != nil || count > 0 {
		return count > 0, err
	}
	err = r.DB.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM bot_config WHERE user_id = ? AND owner_id = ?`), peer, uid).Scan(&count)
	return count > 0, err
}

func (r Reader) TaskRun(ctx context.Context, topic string, source int64, run string) (*types.ConversationTaskStatus, error) {
	s := &types.ConversationTaskStatus{}
	// The ledger retains only the latest receipt per run, not a proven start
	// order. A higher insertion ID can be an older, first-seen terminal event.
	// Keep lifecycle delivery bound to this exact run rather than inferring
	// supersession from another run's row ID or latest publisher timestamp.
	err := r.DB.QueryRowContext(ctx, r.query(`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
  FROM conversation_task_runs WHERE topic_id = ? AND source_uid = ? AND run_id = ?`), topic, source, run).
		Scan(&s.TopicID, &s.RunID, &s.State, &s.Summary, &s.Error, &s.SourceUID, &s.UpdatedAt, &s.EventUpdatedAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (r Reader) CurrentTask(ctx context.Context, topic string) (*types.ConversationTaskStatus, error) {
	s := &types.ConversationTaskStatus{}
	err := r.DB.QueryRowContext(ctx, r.query(`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
		FROM conversation_task_runs WHERE topic_id = ? AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END, event_updated_at DESC, source_uid DESC, id DESC LIMIT 1`), topic, time.Now().UTC()).
		Scan(&s.TopicID, &s.RunID, &s.State, &s.Summary, &s.Error, &s.SourceUID, &s.UpdatedAt, &s.EventUpdatedAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (r Reader) Title(ctx context.Context, uid int64, topic string, gid, peer int64) (string, error) {
	var title string
	err := r.DB.QueryRowContext(ctx, r.query(`SELECT title FROM conversation_titles WHERE user_id = ? AND topic_id = ?`), uid, topic).Scan(&title)
	if err == nil && title != "" {
		return title, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if gid > 0 {
		err = r.DB.QueryRowContext(ctx, r.query(`SELECT name FROM "groups" WHERE id = ?`), gid).Scan(&title)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return title, err
	}
	user, err := r.User(ctx, peer)
	if err != nil || user == nil {
		return "", err
	}
	if user.DisplayName != "" {
		return user.DisplayName, nil
	}
	return user.Username, nil
}

// ReplaceSubscription serializes account capacity with ordinary push writes.
// An expired version is replaced in place only if every observed field matches.
func (r Reader) ReplaceSubscription(ctx context.Context, row, previous *types.PushSubscription, limit int) (bool, error) {
	if row == nil || limit <= 0 {
		return false, errors.New("invalid subscription")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var uid int64
	if err = tx.QueryRowContext(ctx, r.query(`SELECT id FROM users WHERE id = ? FOR UPDATE`), row.UID).Scan(&uid); err != nil {
		return false, err
	}
	if previous != nil {
		if previous.UID != row.UID || previous.Endpoint != row.Endpoint {
			return false, nil
		}
		result, err := tx.ExecContext(ctx, r.query(`UPDATE push_subscriptions SET p256dh = ?, auth = ?, registration_id = ? WHERE uid = ? AND endpoint = ? AND registration_id = ? AND p256dh = ? AND auth = ?`),
			row.P256DH, row.Auth, row.RegistrationID, row.UID, row.Endpoint, previous.RegistrationID, previous.P256DH, previous.Auth)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return false, err
		}
	} else {
		var count int
		if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM push_subscriptions WHERE uid = ?`), row.UID).Scan(&count); err != nil {
			return false, err
		}
		if count >= limit {
			return false, nil
		}
		q := `INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id) VALUES (?, ?, ?, ?, ?) ON CONFLICT (endpoint) DO NOTHING`
		if !r.Postgres {
			q = `INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id) VALUES (?, ?, ?, ?, ?)`
		}
		result, err := tx.ExecContext(ctx, r.query(q), row.UID, row.Endpoint, row.P256DH, row.Auth, row.RegistrationID)
		var duplicate *mysqlDriver.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
