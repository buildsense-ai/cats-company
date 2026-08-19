// Package mysql - Cats Company message database operations.
package mysql

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// CreateTopic creates a topic if it doesn't exist.
func (a *Adapter) CreateTopic(id, topicType string, ownerID int64) error {
	_, err := a.db.Exec(
		`INSERT IGNORE INTO topics (id, type, owner_id) VALUES (?, ?, ?)`,
		id, topicType, ownerID,
	)
	return err
}

// SaveMessage inserts a message and returns its ID.
func (a *Adapter) SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error) {
	res, err := a.db.Exec(
		`INSERT INTO messages (topic_id, from_uid, content, msg_type) VALUES (?, ?, ?, ?)`,
		topicID, fromUID, content, msgType,
	)
	if err != nil {
		return 0, fmt.Errorf("save message: %w", err)
	}
	return res.LastInsertId()
}

// SaveMessageWithBlocks inserts a message with content blocks and returns its ID.
func (a *Adapter) SaveMessageWithBlocks(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string) (int64, error) {
	var blocksJSON []byte
	var err error
	if len(blocks) > 0 {
		blocksJSON, err = json.Marshal(blocks)
		if err != nil {
			return 0, fmt.Errorf("marshal content blocks: %w", err)
		}
	}

	res, err := a.db.Exec(
		`INSERT INTO messages (topic_id, from_uid, content, content_blocks, mode, role, msg_type) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		topicID, fromUID, content, blocksJSON, mode, role, msgType,
	)
	if err != nil {
		return 0, fmt.Errorf("save message with blocks: %w", err)
	}
	return res.LastInsertId()
}

// SaveMessageIdempotent inserts a message once for a client-generated id.
func (a *Adapter) SaveMessageIdempotent(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (int64, bool, error) {
	return a.SaveMessageWithMetadata(topicID, fromUID, content, blocks, mode, role, msgType, replyTo, clientMsgID, nil)
}

// SaveMessageWithMetadata atomically persists a normalized message and metadata.
func (a *Adapter) SaveMessageWithMetadata(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}) (int64, bool, error) {
	if clientMsgID == "" {
		id, err := a.saveMessageNormalized(topicID, fromUID, content, blocks, mode, role, msgType, replyTo, "", metadata)
		return id, false, err
	}

	var id int64
	err := a.db.QueryRow(
		`SELECT id FROM messages WHERE topic_id = ? AND from_uid = ? AND client_msg_id = ?`,
		topicID, fromUID, clientMsgID,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("lookup idempotent message: %w", err)
	}

	id, err = a.saveMessageNormalized(topicID, fromUID, content, blocks, mode, role, msgType, replyTo, clientMsgID, metadata)
	if err == nil {
		return id, false, nil
	}

	lookupErr := a.db.QueryRow(
		`SELECT id FROM messages WHERE topic_id = ? AND from_uid = ? AND client_msg_id = ?`,
		topicID, fromUID, clientMsgID,
	).Scan(&id)
	if lookupErr == nil {
		return id, true, nil
	}
	return 0, false, fmt.Errorf("save idempotent message: %w", err)
}

func (a *Adapter) saveMessageNormalized(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}) (int64, error) {
	var blocksJSON []byte
	var err error
	if len(blocks) > 0 {
		if mode == "" {
			mode = "code"
		}
		blocksJSON, err = json.Marshal(blocks)
		if err != nil {
			return 0, fmt.Errorf("marshal content blocks: %w", err)
		}
	} else if mode == "" {
		mode = "normal"
	}
	var metadataJSON []byte
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return 0, fmt.Errorf("marshal message metadata: %w", err)
		}
	}

	var res interface{ LastInsertId() (int64, error) }
	if replyTo > 0 {
		res, err = a.db.Exec(
			`INSERT INTO messages (topic_id, from_uid, content, content_blocks, mode, role, msg_type, reply_to, client_msg_id, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
			topicID, fromUID, content, blocksJSON, mode, role, msgType, replyTo, clientMsgID, metadataJSON,
		)
	} else {
		res, err = a.db.Exec(
			`INSERT INTO messages (topic_id, from_uid, content, content_blocks, mode, role, msg_type, client_msg_id, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
			topicID, fromUID, content, blocksJSON, mode, role, msgType, clientMsgID, metadataJSON,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("save normalized message: %w", err)
	}
	return res.LastInsertId()
}

// GetMessagesSince returns messages after a given ID for a topic.
func (a *Adapter) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
		 FROM messages WHERE topic_id = ? AND id > ?
		 ORDER BY id ASC LIMIT ?`,
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
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
		 FROM messages WHERE topic_id = ?
		 ORDER BY created_at ASC LIMIT ? OFFSET ?`,
		topicID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows, "scan message")
}

// GetLatestMessages returns the newest messages for a topic, but in ascending order for rendering.
func (a *Adapter) GetLatestMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
		 FROM (
		   SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
		 	FROM messages WHERE topic_id = ?
		 	ORDER BY id DESC LIMIT ? OFFSET ?
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

// GetLatestMessagesBefore returns the newest messages older than beforeID.
// Results are ordered ascending so callers can rebuild a transcript directly.
func (a *Adapter) GetLatestMessagesBefore(topicID string, beforeID int64, limit int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	if beforeID <= 0 {
		return a.GetLatestMessages(topicID, limit, 0)
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
         FROM (
           SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
           FROM messages WHERE topic_id = ? AND id < ?
           ORDER BY id DESC LIMIT ?
         ) recent
         ORDER BY id ASC`,
		topicID, beforeID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get latest messages before: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan latest message before")
}

// ListAgentFileMessages returns newest file-bearing messages authored by one agent in one conversation.
func (a *Adapter) ListAgentFileMessages(agentUID int64, topicID string, beforeID int64, limit int) ([]*types.Message, error) {
	if topicID == "" {
		return []*types.Message{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	args := []interface{}{agentUID, topicID}
	beforeClause := ""
	if beforeID > 0 {
		beforeClause = " AND id < ?"
		args = append(args, beforeID)
	}
	args = append(args, limit)
	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata
			 FROM messages
			 WHERE from_uid = ?
			   AND topic_id = ?
			   AND (
			     msg_type = 'file'
			     OR JSON_SEARCH(content_blocks, 'one', 'file', NULL, '$[*].type') IS NOT NULL
			   )%s
			 ORDER BY id DESC
			 LIMIT ?`,
			beforeClause,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list agent file messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows, "scan agent file message")
}

// ListTopicFileMessages returns newest file-bearing messages from all senders in one conversation.
func (a *Adapter) ListTopicFileMessages(topicID string, beforeID int64, limit int) ([]*types.Message, error) {
	if topicID == "" {
		return []*types.Message{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	args := []interface{}{topicID}
	beforeClause := ""
	if beforeID > 0 {
		beforeClause = " AND id < ?"
		args = append(args, beforeID)
	}
	args = append(args, limit)
	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
			 FROM messages
			 WHERE topic_id = ?
			   AND (
			     msg_type = 'file'
			     OR JSON_SEARCH(content_blocks, 'one', 'file', NULL, '$[*].type') IS NOT NULL
			   )%s
			 ORDER BY id DESC
			 LIMIT ?`,
			beforeClause,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list topic file messages: %w", err)
	}
	defer rows.Close()

	msgs := make([]*types.Message, 0)
	for rows.Next() {
		m := &types.Message{}
		var blocksJSON []byte
		var mode, role *string
		if err := rows.Scan(&m.ID, &m.TopicID, &m.FromUID, &m.Content, &m.MsgType, &m.CreatedAt, &blocksJSON, &mode, &role); err != nil {
			return nil, fmt.Errorf("scan topic file message: %w", err)
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

// GetLatestMessagesForTopics returns the newest persisted message for each topic.
func (a *Adapter) GetLatestMessagesForTopics(topicIDs []string) (map[string]*types.Message, error) {
	if len(topicIDs) == 0 {
		return map[string]*types.Message{}, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(topicIDs)), ",")
	args := make([]interface{}, 0, len(topicIDs)*2)
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}

	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT m.id, m.topic_id, m.from_uid, m.content, m.msg_type, m.created_at, m.content_blocks, m.mode, m.role, m.metadata
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
		var blocksJSON, metadataJSON []byte
		var mode, role *string
		if err := rows.Scan(&m.ID, &m.TopicID, &m.FromUID, &m.Content, &m.MsgType, &m.CreatedAt, &blocksJSON, &mode, &role, &metadataJSON); err != nil {
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
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &m.Metadata); err != nil {
				return nil, fmt.Errorf("%s metadata: %w", context, err)
			}
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GetConversationTitles returns non-empty custom P2P task titles owned by a user.
func (a *Adapter) GetConversationTitles(ownerID int64, topicIDs []string) (map[string]string, error) {
	if len(topicIDs) == 0 {
		return map[string]string{}, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(topicIDs)), ",")
	args := make([]interface{}, 0, len(topicIDs)+1)
	args = append(args, ownerID)
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}
	rows, err := a.db.Query(
		fmt.Sprintf(`SELECT topic_id, title FROM conversation_titles WHERE user_id = ? AND topic_id IN (%s)`, placeholders),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation titles: %w", err)
	}
	defer rows.Close()

	titles := make(map[string]string)
	for rows.Next() {
		var topicID, title string
		if err := rows.Scan(&topicID, &title); err != nil {
			return nil, fmt.Errorf("scan conversation title: %w", err)
		}
		titles[topicID] = title
	}
	return titles, rows.Err()
}

// UpdateConversationTitle updates a user-owned P2P task title.
func (a *Adapter) UpdateConversationTitle(ownerID int64, topicID, title string) (bool, error) {
	result, err := a.db.Exec(
		`INSERT INTO conversation_titles (user_id, topic_id, title)
		 SELECT ?, id, ? FROM topics WHERE id = ? AND type = 'p2p'
		 ON DUPLICATE KEY UPDATE title = VALUES(title), updated_at = CURRENT_TIMESTAMP`,
		ownerID, title, topicID,
	)
	if err != nil {
		return false, fmt.Errorf("update conversation title: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("conversation title rows affected: %w", err)
	}
	return updated > 0, nil
}
