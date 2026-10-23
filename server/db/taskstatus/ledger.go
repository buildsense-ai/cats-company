// Package taskstatus implements the run ledger and transactional notification
// outbox shared by the PostgreSQL and MySQL adapters.
package taskstatus

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type Ledger struct {
	DB       *sql.DB
	Postgres bool
	// Reconcile imports writes made by older binaries into the source projection.
	Reconcile func(*sql.Tx, string) error
}

func (l Ledger) query(q string) string {
	if !l.Postgres {
		return q
	}
	var b strings.Builder
	n := 0
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

const columns = "topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at"

type scanner interface{ Scan(...interface{}) error }

func scanStatus(row scanner) (*types.ConversationTaskStatus, error) {
	s := &types.ConversationTaskStatus{}
	err := row.Scan(&s.TopicID, &s.RunID, &s.State, &s.Summary, &s.Error, &s.SourceUID, &s.UpdatedAt, &s.EventUpdatedAt, &s.ExpiresAt)
	return s, err
}

func (l Ledger) lockTopic(tx *sql.Tx, topic string) error {
	q := `INSERT INTO conversation_task_statuses (topic_id, run_id, state, summary, error) VALUES (?, '', 'idle', '', '') ON CONFLICT (topic_id) DO NOTHING`
	if !l.Postgres {
		q = `INSERT IGNORE INTO conversation_task_statuses (topic_id, run_id, state, summary, error) VALUES (?, '', 'idle', '', '')`
	}
	if _, err := tx.Exec(l.query(q), topic); err != nil {
		return err
	}
	var locked string
	if err := tx.QueryRow(l.query(`SELECT topic_id FROM conversation_task_statuses WHERE topic_id = ? FOR UPDATE`), topic).Scan(&locked); err != nil {
		return err
	}
	if l.Reconcile != nil {
		if err := l.Reconcile(tx, topic); err != nil {
			return err
		}
	}
	return l.importRuns(tx, "topic_id = ?", topic)
}

type execer interface {
	Exec(string, ...interface{}) (sql.Result, error)
}

func (l Ledger) importRuns(db execer, filter string, args ...interface{}) error {
	// Import the current projection during rolling upgrades. An old writer can
	// advance a known run, but cannot resurrect its terminal state. Identical
	// projections do not refresh liveness or publisher event time.
	q := `INSERT INTO conversation_task_runs
		(topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
		SELECT topic_id, source_uid, COALESCE(run_id, ''), state, summary, error, expires_at, event_updated_at, updated_at
		FROM conversation_task_status_sources WHERE ` + filter + `
		ON CONFLICT (topic_id, source_uid, run_id) DO UPDATE SET
		state = EXCLUDED.state, summary = EXCLUDED.summary, error = EXCLUDED.error,
		expires_at = EXCLUDED.expires_at, event_updated_at = EXCLUDED.event_updated_at, updated_at = EXCLUDED.updated_at
		WHERE EXCLUDED.event_updated_at > conversation_task_runs.event_updated_at
		AND NOT (conversation_task_runs.state IN ('completed','failed','cancelled','stale') AND EXCLUDED.state NOT IN ('completed','failed','cancelled','stale'))`
	if !l.Postgres {
		// MySQL assignments are evaluated left to right: keep the comparison
		// timestamp last so all preceding assignments test the original value.
		condition := `VALUES(event_updated_at) > conversation_task_runs.event_updated_at AND NOT (conversation_task_runs.state IN ('completed','failed','cancelled','stale') AND VALUES(state) NOT IN ('completed','failed','cancelled','stale'))`
		q = `INSERT INTO conversation_task_runs
		(topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
		SELECT topic_id, source_uid, COALESCE(run_id, ''), state, summary, error, expires_at, event_updated_at, updated_at
		FROM conversation_task_status_sources WHERE ` + filter + ` ON DUPLICATE KEY UPDATE `
		for _, field := range []string{"summary", "error", "expires_at", "updated_at", "state", "event_updated_at"} {
			if field != "summary" {
				q += ", "
			}
			q += field + " = IF(" + condition + ", VALUES(" + field + "), conversation_task_runs." + field + ")"
		}
	}
	_, err := db.Exec(l.query(q), args...)
	return err
}

func (l Ledger) ImportTopics(topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	args := make([]interface{}, len(topics))
	for i, topic := range topics {
		args[i] = topic
	}
	return l.importRuns(l.DB, "topic_id IN ("+strings.TrimRight(strings.Repeat("?,", len(topics)), ",")+")", args...)
}

func (l Ledger) now(tx *sql.Tx) (time.Time, error) {
	q := `SELECT CURRENT_TIMESTAMP(6)`
	if l.Postgres {
		q = `SELECT clock_timestamp()`
	}
	var now time.Time
	err := tx.QueryRow(q).Scan(&now)
	return now.UTC().Truncate(time.Microsecond), err
}

func (l Ledger) Upsert(input *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	if input == nil || input.TopicID == "" || input.SourceUID <= 0 {
		return nil, errors.New("conversation task status requires topic and source uid")
	}
	tx, err := l.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := l.lockTopic(tx, input.TopicID); err != nil {
		return nil, fmt.Errorf("lock/import task runs: %w", err)
	}
	now, err := l.now(tx)
	if err != nil {
		return nil, err
	}
	implicitEventTime := input.EventUpdatedAt.IsZero()
	s := store.PrepareConversationTaskStatusForStore(input, now)
	current, err := scanStatus(tx.QueryRow(l.query(`SELECT `+columns+` FROM conversation_task_runs WHERE topic_id = ? AND source_uid = ? AND run_id = ?`), s.TopicID, s.SourceUID, s.RunID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if err := store.ValidateConversationTaskStatusTransition(current, s, now); err != nil {
			return nil, err
		}
		// A retry of an identical receipt is a no-op: do not refresh liveness,
		// reorder the aggregate or enqueue another notification.
		if sameReceipt(current, s) && (current.EventUpdatedAt.Equal(s.EventUpdatedAt) || (implicitEventTime && types.IsTerminalConversationTaskState(s.State))) {
			aggregate, err := l.aggregate(tx, s.TopicID, now)
			if err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			*input = *current
			return aggregate, nil
		}
	}
	aggregate, err := l.write(tx, s, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	*input = *s
	return aggregate, nil
}

func sameReceipt(a, b *types.ConversationTaskStatus) bool {
	return a.State == b.State && a.Summary == b.Summary && a.Error == b.Error &&
		((a.ExpiresAt == nil && b.ExpiresAt == nil) || (a.ExpiresAt != nil && b.ExpiresAt != nil && a.ExpiresAt.Equal(*b.ExpiresAt)))
}

func (l Ledger) write(tx *sql.Tx, s *types.ConversationTaskStatus, now time.Time) (*types.ConversationTaskStatus, error) {
	q := `INSERT INTO conversation_task_runs (` + columns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (topic_id, source_uid, run_id) DO UPDATE SET state = EXCLUDED.state, summary = EXCLUDED.summary,
		error = EXCLUDED.error, updated_at = EXCLUDED.updated_at, event_updated_at = EXCLUDED.event_updated_at, expires_at = EXCLUDED.expires_at`
	if !l.Postgres {
		q = `INSERT INTO conversation_task_runs (` + columns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE state = VALUES(state), summary = VALUES(summary), error = VALUES(error),
		updated_at = VALUES(updated_at), event_updated_at = VALUES(event_updated_at), expires_at = VALUES(expires_at)`
	}
	if _, err := tx.Exec(l.query(q), s.TopicID, s.RunID, s.State, s.Summary, s.Error, s.SourceUID, s.UpdatedAt, s.EventUpdatedAt, s.ExpiresAt); err != nil {
		return nil, err
	}
	// A source projection represents its active runs first. Thus a late
	// completion of run A cannot hide a still-active run B from old clients.
	projection, err := scanStatus(tx.QueryRow(l.query(`SELECT `+columns+` FROM conversation_task_runs WHERE topic_id = ? AND source_uid = ?
		AND (expires_at IS NULL OR expires_at > ?) ORDER BY CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END, event_updated_at DESC, id DESC LIMIT 1`), s.TopicID, s.SourceUID, now))
	if errors.Is(err, sql.ErrNoRows) {
		projection = s
	} else if err != nil {
		return nil, err
	}
	q = `INSERT INTO conversation_task_status_sources (` + columns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (topic_id, source_uid) DO UPDATE SET run_id = EXCLUDED.run_id, state = EXCLUDED.state,
		summary = EXCLUDED.summary, error = EXCLUDED.error, updated_at = EXCLUDED.updated_at, event_updated_at = EXCLUDED.event_updated_at, expires_at = EXCLUDED.expires_at`
	if !l.Postgres {
		q = `INSERT INTO conversation_task_status_sources (` + columns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE run_id = VALUES(run_id), state = VALUES(state), summary = VALUES(summary), error = VALUES(error),
		updated_at = VALUES(updated_at), event_updated_at = VALUES(event_updated_at), expires_at = VALUES(expires_at)`
	}
	if _, err := tx.Exec(l.query(q), projection.TopicID, projection.RunID, projection.State, projection.Summary, projection.Error, projection.SourceUID, projection.UpdatedAt, projection.EventUpdatedAt, projection.ExpiresAt); err != nil {
		return nil, err
	}
	aggregate, err := l.aggregate(tx, s.TopicID, now)
	if err != nil {
		return nil, err
	}
	q = `UPDATE conversation_task_statuses SET run_id = ?, state = ?, summary = ?, error = ?, source_uid = NULLIF(?, 0), expires_at = ?, updated_at = ? WHERE topic_id = ?`
	if !l.Postgres {
		q = strings.Replace(q, "updated_at = ?", "updated_at = GREATEST(?, updated_at + INTERVAL 1 MICROSECOND)", 1)
	}
	if _, err := tx.Exec(l.query(q),
		aggregate.RunID, aggregate.State, aggregate.Summary, aggregate.Error, aggregate.SourceUID, aggregate.ExpiresAt, now, s.TopicID); err != nil {
		return nil, err
	}
	// Source receipt and aggregate have independent clocks. Read the stored
	// projection revision so old clients never discard a later aggregate.
	if err := tx.QueryRow(l.query(`SELECT updated_at FROM conversation_task_statuses WHERE topic_id = ?`), s.TopicID).Scan(&aggregate.UpdatedAt); err != nil {
		return nil, err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(l.query(`INSERT INTO conversation_task_status_outbox (topic_id, source_uid, status_json) VALUES (?, ?, ?)`), s.TopicID, s.SourceUID, string(data)); err != nil {
		return nil, err
	}
	return aggregate, nil
}

func (l Ledger) aggregate(tx *sql.Tx, topic string, now time.Time) (*types.ConversationTaskStatus, error) {
	s, err := scanStatus(tx.QueryRow(l.query(`SELECT `+columns+` FROM conversation_task_runs
		WHERE topic_id = ? AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END, event_updated_at DESC, source_uid DESC, id DESC LIMIT 1`), topic, now))
	if errors.Is(err, sql.ErrNoRows) {
		return &types.ConversationTaskStatus{TopicID: topic, State: "idle", UpdatedAt: now}, nil
	}
	return s, err
}

func (l Ledger) MarkStale(topic string, source int64, run string, cutoff time.Time, generation uint64) (*types.ConversationTaskStatus, bool, error) {
	if topic == "" || source <= 0 || run == "" {
		return nil, false, errors.New("conversation task stale requires topic, source uid and run id")
	}
	tx, err := l.DB.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	q := `INSERT INTO bot_connection_generations (bot_uid, generation) VALUES (?, 0) ON CONFLICT (bot_uid) DO NOTHING`
	if !l.Postgres {
		q = `INSERT IGNORE INTO bot_connection_generations (bot_uid, generation) VALUES (?, 0)`
	}
	if _, err := tx.Exec(l.query(q), source); err != nil {
		return nil, false, err
	}
	var current uint64
	if err := tx.QueryRow(l.query(`SELECT generation FROM bot_connection_generations WHERE bot_uid = ? FOR UPDATE`), source).Scan(&current); err != nil {
		return nil, false, err
	}
	if current != generation {
		return nil, false, nil
	}
	if err := l.lockTopic(tx, topic); err != nil {
		return nil, false, err
	}
	s, err := scanStatus(tx.QueryRow(l.query(`SELECT `+columns+` FROM conversation_task_runs WHERE topic_id = ? AND source_uid = ? AND run_id = ?`), topic, source, run))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if (s.State != "running" && s.State != "waiting") || s.UpdatedAt.After(cutoff) {
		return nil, false, nil
	}
	now, err := l.now(tx)
	if err != nil {
		return nil, false, err
	}
	s.State = "stale"
	s.Summary = "机器人连接中断，执行结果待确认"
	s.Error = "bot disconnected before terminal task status"
	s.ExpiresAt = nil
	s.UpdatedAt = now
	if s.EventUpdatedAt.Before(now) {
		s.EventUpdatedAt = now
	}
	aggregate, err := l.write(tx, s, now)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return aggregate, true, nil
}

func (l Ledger) ListRuns(topic string, before int64, limit int) ([]store.ConversationTaskRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	q := `SELECT id, ` + columns + ` FROM conversation_task_runs WHERE topic_id = ?`
	args := []interface{}{topic}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := l.DB.Query(l.query(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]store.ConversationTaskRun, 0)
	for rows.Next() {
		r := store.ConversationTaskRun{ConversationTaskStatus: &types.ConversationTaskStatus{}}
		err := rows.Scan(&r.ID, &r.TopicID, &r.RunID, &r.State, &r.Summary, &r.Error, &r.SourceUID, &r.UpdatedAt, &r.EventUpdatedAt, &r.ExpiresAt)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (l Ledger) ClaimEvents(limit int, lease time.Duration) ([]store.ConversationTaskStatusEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if lease < time.Second {
		lease = 30 * time.Second
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(random[:])
	tx, err := l.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now, err := l.now(tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(l.query(`SELECT id, status_json FROM conversation_task_status_outbox WHERE available_at <= ? ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED`), now, limit)
	if err != nil {
		return nil, err
	}
	var events []store.ConversationTaskStatusEvent
	for rows.Next() {
		e := store.ConversationTaskStatusEvent{Claim: token, Status: &types.ConversationTaskStatus{}}
		var data string
		if err := rows.Scan(&e.ID, &data); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), e.Status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode task outbox event %d: %w", e.ID, err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, e := range events {
		if _, err := tx.Exec(l.query(`UPDATE conversation_task_status_outbox SET claim_token = ?, available_at = ? WHERE id = ?`), token, now.Add(lease), e.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

func (l Ledger) CompleteEvent(id int64, claim string) error {
	if claim == "" {
		return errors.New("task outbox completion requires claim token")
	}
	_, err := l.DB.Exec(l.query(`DELETE FROM conversation_task_status_outbox WHERE id = ? AND claim_token = ?`), id, claim)
	return err
}
