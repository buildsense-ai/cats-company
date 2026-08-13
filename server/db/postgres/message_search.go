package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const postgresMessageSearchQuery = `
WITH normalized_messages AS (
  SELECT messages.*,
         CASE
           WHEN msg_type <> 'file' OR NOT pg_input_is_valid(content, 'jsonb') THEN '{}'::jsonb
           WHEN jsonb_typeof(content::jsonb) = 'string'
             AND pg_input_is_valid(content::jsonb #>> '{}', 'jsonb')
             THEN (content::jsonb #>> '{}')::jsonb
           ELSE content::jsonb
         END AS search_legacy_content,
         CASE
           WHEN content_blocks IS NULL THEN TRUE
           WHEN jsonb_typeof(content_blocks) = 'null' THEN TRUE
           WHEN jsonb_typeof(content_blocks) <> 'array' THEN FALSE
           ELSE NOT EXISTS (
             SELECT 1
             FROM jsonb_array_elements(content_blocks) AS typed_block
             WHERE jsonb_typeof(typed_block) NOT IN ('object', 'null')
               OR (
                 jsonb_typeof(typed_block) = 'object'
                 AND (
                   EXISTS (
                     SELECT 1
                     FROM jsonb_object_keys(typed_block) AS block_key
                     WHERE lower(block_key) IN (
                       'type', 'text', 'thinking', 'payload', 'id',
                       'name', 'input', 'tool_use_id', 'content', 'is_error'
                     )
                     AND block_key NOT IN (
                       'type', 'text', 'thinking', 'payload', 'id',
                       'name', 'input', 'tool_use_id', 'content', 'is_error'
                     )
                   )
                   OR
                   (typed_block ? 'type' AND jsonb_typeof(typed_block->'type') NOT IN ('string', 'null'))
                   OR (typed_block ? 'text' AND jsonb_typeof(typed_block->'text') NOT IN ('string', 'null'))
                   OR (typed_block ? 'thinking' AND jsonb_typeof(typed_block->'thinking') NOT IN ('string', 'null'))
                   OR (typed_block ? 'payload' AND jsonb_typeof(typed_block->'payload') NOT IN ('object', 'null'))
                   OR (typed_block ? 'id' AND jsonb_typeof(typed_block->'id') NOT IN ('string', 'null'))
                   OR (typed_block ? 'name' AND jsonb_typeof(typed_block->'name') NOT IN ('string', 'null'))
                   OR (typed_block ? 'input' AND jsonb_typeof(typed_block->'input') NOT IN ('object', 'null'))
                   OR (typed_block ? 'tool_use_id' AND jsonb_typeof(typed_block->'tool_use_id') NOT IN ('string', 'null'))
                   OR (typed_block ? 'content' AND jsonb_typeof(typed_block->'content') NOT IN ('string', 'null'))
                   OR (typed_block ? 'is_error' AND jsonb_typeof(typed_block->'is_error') NOT IN ('boolean', 'null'))
                   OR (
                     jsonb_typeof(typed_block->'payload') = 'object'
                     AND (
                       (typed_block->'payload' ? 'name' AND jsonb_typeof(typed_block->'payload'->'name') NOT IN ('string', 'null'))
                       OR (typed_block->'payload' ? 'file_name' AND jsonb_typeof(typed_block->'payload'->'file_name') NOT IN ('string', 'null'))
                       OR (typed_block->'payload' ? 'filename' AND jsonb_typeof(typed_block->'payload'->'filename') NOT IN ('string', 'null'))
                       OR (typed_block->'payload' ? 'title' AND jsonb_typeof(typed_block->'payload'->'title') NOT IN ('string', 'null'))
                     )
                   )
                 )
               )
           )
         END AS search_blocks_valid
  FROM messages
)
SELECT m.id, m.topic_id,
       CASE WHEN t.type = 'group' THEN COALESCE(g.name, t.name, '')
            ELSE COALESCE(NULLIF(ct.title, ''), NULLIF(peer.display_name, ''), peer.username, t.name, '') END AS topic_name,
       m.from_uid, COALESCE(NULLIF(sender.display_name, ''), sender.username, ''),
       m.content, m.msg_type, m.created_at, m.content_blocks
FROM normalized_messages m
JOIN topics t ON t.id = m.topic_id
JOIN users viewer ON viewer.id = $1
JOIN users sender ON sender.id = m.from_uid
LEFT JOIN group_members gm ON t.type = 'group'
  AND t.id = 'grp_' || gm.group_id::text AND gm.user_id = viewer.id
LEFT JOIN "groups" g ON g.id = gm.group_id
LEFT JOIN users peer ON t.type = 'p2p' AND peer.id <> viewer.id
  AND t.id = 'p2p_' || LEAST(viewer.id, peer.id)::text || '_' || GREATEST(viewer.id, peer.id)::text
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
  ($2 <> 'artifact' AND m.msg_type = 'text'
    AND (m.content_blocks IS NULL OR jsonb_typeof(m.content_blocks) = 'null'
      OR (m.search_blocks_valid
      AND NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(m.content_blocks) AS block
        WHERE block->>'type' IN ('thinking', 'tool_use', 'tool_result', 'runtime_plan')
      )
    ))
    AND STRPOS(LOWER(m.content), LOWER($3)) > 0)
  OR
  ($2 <> 'message' AND (
    (m.content_blocks IS NOT NULL AND jsonb_typeof(m.content_blocks) = 'array'
      AND m.search_blocks_valid AND EXISTS (
      SELECT 1 FROM jsonb_array_elements(m.content_blocks) AS artifact
      WHERE artifact->>'type' IN ('file', 'image', 'audio', 'video')
        AND (
          STRPOS(LOWER(COALESCE(artifact->>'name', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'name', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'file_name', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'filename', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'title', '')), LOWER($3)) > 0
        )
    ))
    OR (m.msg_type = 'file' AND EXISTS (
      SELECT 1
      FROM LATERAL (
        SELECT CASE
          WHEN jsonb_typeof(m.search_legacy_content->'payload') = 'object'
            THEN m.search_legacy_content->'payload'
          ELSE m.search_legacy_content
        END AS content
      ) AS legacy_file
      WHERE (jsonb_typeof(legacy_file.content->'name') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'name'), LOWER($3)) > 0)
        OR (jsonb_typeof(legacy_file.content->'file_name') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'file_name'), LOWER($3)) > 0)
        OR (jsonb_typeof(legacy_file.content->'filename') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'filename'), LOWER($3)) > 0)
        OR (jsonb_typeof(legacy_file.content->'title') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'title'), LOWER($3)) > 0)
    ))
  ))
)
ORDER BY m.created_at DESC, m.id DESC
LIMIT $4 OFFSET $5`

// SearchMessages searches only topics readable by viewerUID. Access filtering is atomic with selection.
func (a *Adapter) SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	return store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
		rows, err := a.db.Query(postgresMessageSearchQuery, viewerUID, searchType, query, pageSize, offset)
		if err != nil {
			return nil, 0, fmt.Errorf("search messages: %w", err)
		}
		page, scanned, scanErr := scanPostgresMessageSearch(rows, query, searchType, remaining)
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

func scanPostgresMessageSearch(rows *sql.Rows, query, searchType string, limit int) ([]*store.MessageSearchResult, int, error) {
	results := make([]*store.MessageSearchResult, 0, limit)
	scanned := 0
	for rows.Next() {
		scanned++
		var (
			result     store.MessageSearchResult
			msgType    string
			blocksJSON interface{}
		)
		if err := rows.Scan(&result.MessageID, &result.TopicID, &result.TopicName, &result.FromUID,
			&result.SenderName, &result.Content, &msgType, &result.CreatedAt, &blocksJSON); err != nil {
			return nil, scanned, fmt.Errorf("scan message search result: %w", err)
		}
		blocks := jsonBytes(blocksJSON)
		match, ok := store.MatchMessageSearchCandidate(store.MessageSearchCandidate{
			Result:        result,
			MessageType:   msgType,
			ContentBlocks: blocks,
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

func jsonBytes(value interface{}) []byte {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return typed
	case string:
		return []byte(typed)
	default:
		raw, _ := json.Marshal(typed)
		return raw
	}
}

// GetMessagesAround returns a bounded chronological window around a message in one topic.
func (a *Adapter) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	beforeLimit := (limit + 1) / 2
	afterLimit := limit - beforeLimit
	rows, err := a.db.Query(`
SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, client_msg_id, metadata FROM (
  (SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, client_msg_id, metadata
   FROM messages WHERE topic_id = $1 AND id <= $2 ORDER BY id DESC LIMIT $3)
  UNION ALL
  (SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, client_msg_id, metadata
   FROM messages WHERE topic_id = $1 AND id > $2 ORDER BY id ASC LIMIT $4)
) around_messages ORDER BY id ASC`, topicID, messageID, beforeLimit, afterLimit)
	if err != nil {
		return nil, fmt.Errorf("get messages around: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan message around")
}
