package taskstatus

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func (l Ledger) ForSource(topic string, source int64) (*types.ConversationTaskStatus, error) {
	if err := l.ImportTopics([]string{topic}); err != nil {
		return nil, err
	}
	s, err := scanStatus(l.DB.QueryRow(l.query(`SELECT `+columns+` FROM conversation_task_runs
		WHERE topic_id = ? AND source_uid = ? ORDER BY
		CASE WHEN state IN ('running','waiting') AND (expires_at IS NULL OR expires_at > ?) THEN 0 ELSE 1 END,
		event_updated_at DESC, id DESC LIMIT 1`), topic, source, time.Now().UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (l Ledger) Summaries(topics []string) (map[string]*types.ConversationTaskStatus, error) {
	out := make(map[string]*types.ConversationTaskStatus)
	if len(topics) == 0 {
		return out, nil
	}
	if err := l.ImportTopics(topics); err != nil {
		return nil, err
	}
	// Limit each indexed topic lookup before combining results. Ranking every
	// historical run would make conversation-list reads grow with run history.
	now := time.Now().UTC()
	for offset := 0; offset < len(topics); offset += 100 {
		end := offset + 100
		if end > len(topics) {
			end = len(topics)
		}
		parts := make([]string, 0, end-offset)
		args := make([]interface{}, 0, 2*(end-offset))
		for _, topic := range topics[offset:end] {
			parts = append(parts, `(SELECT `+columns+` FROM conversation_task_runs WHERE topic_id = ?
			AND (expires_at IS NULL OR expires_at > ?) ORDER BY CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END,
			event_updated_at DESC, source_uid DESC, id DESC LIMIT 1)`)
			args = append(args, topic, now)
		}
		q := `SELECT c.topic_id, c.run_id, c.state, c.summary, c.error, c.source_uid,
		GREATEST(c.updated_at, COALESCE(a.updated_at, c.updated_at)), c.event_updated_at, c.expires_at
		FROM (` + strings.Join(parts, " UNION ALL ") + `) c LEFT JOIN conversation_task_statuses a ON a.topic_id = c.topic_id`
		rows, err := l.DB.Query(l.query(q), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			s, err := scanStatus(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[s.TopicID] = s
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (l Ledger) ActiveSources(topic string) ([]int64, error) {
	// Read-only hot path. The legacy projection is included for an old writer
	// that has not yet been imported by a status write or summary read.
	rows, err := l.DB.Query(l.query(`SELECT DISTINCT source_uid FROM (
		SELECT source_uid, state, expires_at FROM conversation_task_runs WHERE topic_id = ?
		UNION ALL SELECT source_uid, state, expires_at FROM conversation_task_status_sources WHERE topic_id = ?
		) candidates WHERE state IN ('running','waiting') AND (expires_at IS NULL OR expires_at > ?)`), topic, topic, time.Now().UTC())
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

func (l Ledger) ActiveBefore(source int64, cutoff time.Time) ([]*types.ConversationTaskStatus, error) {
	q := `SELECT ` + columns + ` FROM (
		SELECT ` + columns + ` FROM conversation_task_runs
		UNION ALL SELECT ` + columns + ` FROM conversation_task_status_sources legacy
		WHERE NOT EXISTS (SELECT 1 FROM conversation_task_runs r WHERE r.topic_id = legacy.topic_id AND r.source_uid = legacy.source_uid AND r.run_id = legacy.run_id)
		) candidates WHERE state IN ('running','waiting') AND updated_at <= ? AND (expires_at IS NULL OR expires_at > ?)`
	args := []interface{}{cutoff, time.Now().UTC()}
	if source > 0 {
		q += ` AND source_uid = ?`
		args = append(args, source)
	}
	q += ` ORDER BY updated_at, topic_id, source_uid, run_id`
	rows, err := l.DB.Query(l.query(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.ConversationTaskStatus
	for rows.Next() {
		s, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
