package mysql

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const mysqlMessageSearchQuery = `
WITH search_params AS (
  SELECT ? AS search_type, LOWER(?) AS needle
),
normalized_messages AS (
  SELECT messages.*,
         CASE
           WHEN msg_type <> 'file' OR NOT JSON_VALID(content) THEN JSON_OBJECT()
           WHEN JSON_TYPE(content) = 'STRING' AND JSON_VALID(JSON_UNQUOTE(content))
             THEN JSON_UNQUOTE(content)
           ELSE content
         END AS search_legacy_content,
         CASE
           WHEN content_blocks IS NULL THEN TRUE
           ELSE JSON_SCHEMA_VALID(
             '{"type":["array","null"],"items":{"type":["object","null"],"additionalProperties":false,"properties":{"type":{"type":["string","null"]},"text":{"type":["string","null"]},"thinking":{"type":["string","null"]},"payload":{"type":["object","null"],"properties":{"name":{"type":["string","null"]},"file_name":{"type":["string","null"]},"filename":{"type":["string","null"]},"title":{"type":["string","null"]}}},"id":{"type":["string","null"]},"name":{"type":["string","null"]},"input":{"type":["object","null"]},"tool_use_id":{"type":["string","null"]},"content":{"type":["string","null"]},"is_error":{"type":["boolean","null"]}}}}',
             content_blocks
           )
         END AS search_blocks_valid
  FROM messages
),
searchable_messages AS (
  SELECT normalized_messages.*,
         CASE
           WHEN JSON_TYPE(JSON_EXTRACT(search_legacy_content, '$.payload')) = 'OBJECT'
             THEN JSON_EXTRACT(search_legacy_content, '$.payload')
           ELSE search_legacy_content
         END AS search_legacy_fields
  FROM normalized_messages
)
SELECT m.id, m.topic_id,
       CASE WHEN t.type = 'group' THEN COALESCE(g.name, t.name, '')
            ELSE COALESCE(NULLIF(ct.title, ''), NULLIF(peer.display_name, ''), peer.username, t.name, '') END AS topic_name,
       m.from_uid, COALESCE(NULLIF(sender.display_name, ''), sender.username, ''),
       m.content, m.msg_type, m.created_at, m.content_blocks
FROM searchable_messages m
CROSS JOIN search_params search
JOIN topics t ON t.id = m.topic_id
JOIN users viewer ON viewer.id = ?
JOIN users sender ON sender.id = m.from_uid
LEFT JOIN group_members gm ON t.type = 'group'
  AND t.id = CONCAT('grp_', gm.group_id) AND gm.user_id = viewer.id
LEFT JOIN ` + "`groups`" + ` g ON g.id = gm.group_id
LEFT JOIN users peer ON t.type = 'p2p' AND peer.id <> viewer.id
  AND t.id = CONCAT('p2p_', LEAST(viewer.id, peer.id), '_', GREATEST(viewer.id, peer.id))
LEFT JOIN bot_config peer_bot ON peer_bot.user_id = peer.id
LEFT JOIN conversation_titles ct ON ct.user_id = viewer.id AND ct.topic_id = t.id
WHERE sender.account_type IN ('human', 'bot')
AND (
  (t.type = 'group' AND gm.user_id IS NOT NULL
    AND (viewer.account_type <> 'human' OR COALESCE(g.group_kind, 'standard') <> 'channel_managed'))
  OR
  (t.type = 'p2p' AND peer.id IS NOT NULL AND (
    viewer.account_type = 'bot' OR peer.account_type <> 'bot' OR peer_bot.owner_id = viewer.id OR EXISTS (
      SELECT 1 FROM friends f
      WHERE f.from_user_id = viewer.id AND f.to_user_id = peer.id AND f.status = 'accepted'
    )
  ))
)
AND (
  (search.search_type <> 'artifact' AND m.msg_type = 'text'
    AND (m.content_blocks IS NULL OR (m.search_blocks_valid AND
      JSON_SEARCH(m.content_blocks, 'one', 'thinking', NULL, '$[*].type') IS NULL
      AND JSON_SEARCH(m.content_blocks, 'one', 'tool_use', NULL, '$[*].type') IS NULL
      AND JSON_SEARCH(m.content_blocks, 'one', 'tool_result', NULL, '$[*].type') IS NULL
      AND JSON_SEARCH(m.content_blocks, 'one', 'runtime_plan', NULL, '$[*].type') IS NULL
    ))
    AND LOCATE(search.needle, LOWER(m.content)) > 0)
  OR
  (search.search_type <> 'message' AND (
    (m.content_blocks IS NOT NULL AND JSON_TYPE(m.content_blocks) = 'ARRAY'
      AND m.search_blocks_valid AND EXISTS (
      SELECT 1
      FROM JSON_TABLE(
        COALESCE(m.content_blocks, JSON_ARRAY()),
        '$[*]' COLUMNS (
          block_type VARCHAR(32) PATH '$.type',
          name VARCHAR(2048) PATH '$.name',
          payload_name VARCHAR(2048) PATH '$.payload.name',
          payload_file_name VARCHAR(2048) PATH '$.payload.file_name',
          payload_filename VARCHAR(2048) PATH '$.payload.filename',
          payload_title VARCHAR(2048) PATH '$.payload.title'
        )
      ) AS artifact
      WHERE artifact.block_type IN ('file', 'image', 'audio', 'video')
        AND (
          LOCATE(search.needle, LOWER(COALESCE(artifact.name, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_name, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_file_name, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_filename, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_title, ''))) > 0
        )
    ))
    OR (m.msg_type = 'file' AND EXISTS (
      SELECT 1
      FROM JSON_TABLE(
        JSON_EXTRACT(m.search_legacy_fields, '$'),
        '$' COLUMNS (
          name VARCHAR(2048) PATH '$.name',
          file_name VARCHAR(2048) PATH '$.file_name',
          filename VARCHAR(2048) PATH '$.filename',
          title VARCHAR(2048) PATH '$.title'
        )
      ) AS legacy_file
      WHERE (JSON_TYPE(JSON_EXTRACT(m.search_legacy_fields, '$.name')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.name, ''))) > 0)
        OR (JSON_TYPE(JSON_EXTRACT(m.search_legacy_fields, '$.file_name')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.file_name, ''))) > 0)
        OR (JSON_TYPE(JSON_EXTRACT(m.search_legacy_fields, '$.filename')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.filename, ''))) > 0)
        OR (JSON_TYPE(JSON_EXTRACT(m.search_legacy_fields, '$.title')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.title, ''))) > 0)
    ))
  ))
)
ORDER BY m.created_at DESC, m.id DESC
LIMIT ? OFFSET ?`

// SearchMessages searches only topics readable by viewerUID. Access filtering is atomic with selection.
func (a *Adapter) SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	return store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
		rows, err := a.db.Query(mysqlMessageSearchQuery, searchType, query, viewerUID, pageSize, offset)
		if err != nil {
			return nil, 0, fmt.Errorf("search messages: %w", err)
		}
		page, scanned, scanErr := scanMySQLMessageSearch(rows, query, searchType, remaining)
		closeErr := rows.Close()
		if scanErr != nil {
			return nil, scanned, scanErr
		}
		if closeErr != nil {
			return nil, scanned, fmt.Errorf("close message search rows: %w", closeErr)
		}
		return page, scanned, nil
	})
}

func scanMySQLMessageSearch(rows *sql.Rows, query, searchType string, limit int) ([]*store.MessageSearchResult, int, error) {
	results := make([]*store.MessageSearchResult, 0, limit)
	scanned := 0
	for rows.Next() {
		scanned++
		var (
			result     store.MessageSearchResult
			msgType    string
			blocksJSON []byte
		)
		if err := rows.Scan(&result.MessageID, &result.TopicID, &result.TopicName, &result.FromUID,
			&result.SenderName, &result.Content, &msgType, &result.CreatedAt, &blocksJSON); err != nil {
			return nil, scanned, fmt.Errorf("scan message search result: %w", err)
		}
		match, ok := store.MatchMessageSearchCandidate(store.MessageSearchCandidate{
			Result:        result,
			MessageType:   msgType,
			ContentBlocks: blocksJSON,
		}, query, searchType)
		if !ok {
			continue
		}
		results = append(results, match)
		if len(results) == limit {
			break
		}
	}
	return results, scanned, rows.Err()
}

// GetMessagesAround returns a bounded chronological window around a message in one topic.
func (a *Adapter) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	beforeLimit := (limit + 1) / 2
	afterLimit := limit - beforeLimit
	rows, err := a.db.Query(`
SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role FROM (
  SELECT * FROM (
    SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
    FROM messages WHERE topic_id = ? AND id <= ? ORDER BY id DESC LIMIT ?
  ) before_messages
  UNION ALL
  SELECT * FROM (
    SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
    FROM messages WHERE topic_id = ? AND id > ? ORDER BY id ASC LIMIT ?
  ) after_messages
) around_messages ORDER BY id ASC`, topicID, messageID, beforeLimit, topicID, messageID, afterLimit)
	if err != nil {
		return nil, fmt.Errorf("get messages around: %w", err)
	}
	defer rows.Close()
	return scanMySQLMessages(rows, "scan message around")
}

func scanMySQLMessages(rows *sql.Rows, context string) ([]*types.Message, error) {
	messages := make([]*types.Message, 0)
	for rows.Next() {
		message := &types.Message{}
		var blocksJSON []byte
		var mode, role *string
		if err := rows.Scan(&message.ID, &message.TopicID, &message.FromUID, &message.Content, &message.MsgType,
			&message.CreatedAt, &blocksJSON, &mode, &role); err != nil {
			return nil, fmt.Errorf("%s: %w", context, err)
		}
		if len(blocksJSON) > 0 {
			_ = json.Unmarshal(blocksJSON, &message.ContentBlocks)
		}
		if mode != nil {
			message.Mode = *mode
		}
		if role != nil {
			message.Role = *role
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
