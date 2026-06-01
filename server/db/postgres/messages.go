package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// CreateTopic creates a topic if it doesn't exist.
func (a *Adapter) CreateTopic(id, topicType string, ownerID int64) error {
	_, err := a.db.Exec(
		`INSERT INTO topics (id, type, owner_id) VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO NOTHING`,
		id, topicType, ownerID,
	)
	return err
}

// SaveMessage inserts a message and returns its ID.
func (a *Adapter) SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error) {
	var id int64
	err := a.db.QueryRow(
		`INSERT INTO messages (topic_id, from_uid, content, msg_type) VALUES ($1, $2, $3, $4)
		 RETURNING id`,
		topicID, fromUID, content, msgType,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save message: %w", err)
	}
	return id, nil
}

// SaveMessageWithBlocks inserts a message with content blocks and returns its ID.
func (a *Adapter) SaveMessageWithBlocks(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string) (int64, error) {
	var blocksJSON interface{}
	if len(blocks) > 0 {
		raw, err := json.Marshal(blocks)
		if err != nil {
			return 0, fmt.Errorf("marshal content blocks: %w", err)
		}
		blocksJSON = string(raw)
	}

	var id int64
	err := a.db.QueryRow(
		`INSERT INTO messages (topic_id, from_uid, content, content_blocks, mode, role, msg_type)
		 VALUES ($1, $2, $3, CAST($4 AS jsonb), $5, $6, $7)
		 RETURNING id`,
		topicID, fromUID, content, blocksJSON, mode, role, msgType,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save message with blocks: %w", err)
	}
	return id, nil
}

// SaveMessageWithReply inserts a message with an optional reply_to reference.
func (a *Adapter) SaveMessageWithReply(topicID string, fromUID int64, content, msgType string, replyTo int64) (int64, error) {
	var id int64
	var err error
	if replyTo > 0 {
		err = a.db.QueryRow(
			`INSERT INTO messages (topic_id, from_uid, content, msg_type, reply_to)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id`,
			topicID, fromUID, content, msgType, replyTo,
		).Scan(&id)
	} else {
		err = a.db.QueryRow(
			`INSERT INTO messages (topic_id, from_uid, content, msg_type)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id`,
			topicID, fromUID, content, msgType,
		).Scan(&id)
	}
	if err != nil {
		return 0, fmt.Errorf("save message with reply: %w", err)
	}
	return id, nil
}

// SaveMessageIdempotent inserts a message once for a client-generated id.
func (a *Adapter) SaveMessageIdempotent(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (int64, bool, error) {
	if clientMsgID == "" {
		id, err := a.saveMessageNormalized(topicID, fromUID, content, blocks, mode, role, msgType, replyTo, "")
		return id, false, err
	}

	var id int64
	err := a.db.QueryRow(
		`SELECT id FROM messages WHERE topic_id = $1 AND from_uid = $2 AND client_msg_id = $3`,
		topicID, fromUID, clientMsgID,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("lookup idempotent message: %w", err)
	}

	id, err = a.saveMessageNormalized(topicID, fromUID, content, blocks, mode, role, msgType, replyTo, clientMsgID)
	if err == nil {
		return id, false, nil
	}

	lookupErr := a.db.QueryRow(
		`SELECT id FROM messages WHERE topic_id = $1 AND from_uid = $2 AND client_msg_id = $3`,
		topicID, fromUID, clientMsgID,
	).Scan(&id)
	if lookupErr == nil {
		return id, true, nil
	}
	return 0, false, fmt.Errorf("save idempotent message: %w", err)
}

func (a *Adapter) saveMessageNormalized(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (int64, error) {
	var blocksJSON interface{}
	if len(blocks) > 0 {
		if mode == "" {
			mode = "code"
		}
		raw, err := json.Marshal(blocks)
		if err != nil {
			return 0, fmt.Errorf("marshal content blocks: %w", err)
		}
		blocksJSON = string(raw)
	} else if mode == "" {
		mode = "normal"
	}

	var id int64
	err := a.db.QueryRow(
		`INSERT INTO messages (topic_id, from_uid, content, content_blocks, mode, role, msg_type, reply_to, client_msg_id)
		 VALUES ($1, $2, $3, CAST($4 AS jsonb), $5, $6, $7, NULLIF($8, 0), NULLIF($9, ''))
		 RETURNING id`,
		topicID, fromUID, content, blocksJSON, mode, role, msgType, replyTo, clientMsgID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save normalized message: %w", err)
	}
	return id, nil
}

// GetMessagesSince returns messages after a given ID for a topic.
func (a *Adapter) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
		 FROM messages WHERE topic_id = $1 AND id > $2
		 ORDER BY id ASC LIMIT $3`,
		topicID, sinceID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages since: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan message")
}

// GetMessages returns messages for a topic, ordered by time.
func (a *Adapter) GetMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
		 FROM messages WHERE topic_id = $1
		 ORDER BY created_at ASC LIMIT $2 OFFSET $3`,
		topicID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan message")
}

// GetLatestMessages returns the newest messages for a topic, in ascending order for rendering.
func (a *Adapter) GetLatestMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
		 FROM (
		 	SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
		 	FROM messages WHERE topic_id = $1
		 	ORDER BY id DESC LIMIT $2 OFFSET $3
		 ) recent
		 ORDER BY id ASC`,
		topicID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("get latest messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan latest message")
}

// GetLatestMessagesForTopics returns the newest persisted message for each topic.
func (a *Adapter) GetLatestMessagesForTopics(topicIDs []string) (map[string]*types.Message, error) {
	if len(topicIDs) == 0 {
		return map[string]*types.Message{}, nil
	}

	placeholders := inPlaceholders(1, len(topicIDs))
	args := make([]interface{}, 0, len(topicIDs))
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}

	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT m.id, m.topic_id, m.from_uid, m.content, m.msg_type, m.created_at, m.content_blocks, m.mode, m.role
			 FROM messages m
			 JOIN (
			 	SELECT topic_id, MAX(id) AS max_id
			 	FROM messages
			 	WHERE topic_id IN (%s)
			 	GROUP BY topic_id
			 ) latest ON latest.max_id = m.id
			 WHERE m.topic_id IN (%s)`,
			placeholders,
			placeholders,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get latest messages for topics: %w", err)
	}
	defer rows.Close()

	msgs, err := scanMessages(rows, "scan latest message for topic")
	if err != nil {
		return nil, err
	}
	latest := make(map[string]*types.Message, len(topicIDs))
	for _, msg := range msgs {
		latest[msg.TopicID] = msg
	}
	return latest, nil
}

type interfaceRows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}

func scanMessages(rows interfaceRows, context string) ([]*types.Message, error) {
	var msgs []*types.Message
	for rows.Next() {
		m := &types.Message{}
		var blocksJSON []byte
		var mode, role *string
		if err := rows.Scan(&m.ID, &m.TopicID, &m.FromUID, &m.Content, &m.MsgType, &m.CreatedAt, &blocksJSON, &mode, &role); err != nil {
			return nil, fmt.Errorf("%s: %w", context, err)
		}
		if len(blocksJSON) > 0 {
			json.Unmarshal(blocksJSON, &m.ContentBlocks)
		}
		if mode != nil {
			m.Mode = *mode
		}
		if role != nil {
			m.Role = *role
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
