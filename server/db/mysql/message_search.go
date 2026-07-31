package mysql

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const mysqlMessageSearchQuery = `
SELECT m.id, m.topic_id,
       CASE WHEN t.type = 'group' THEN COALESCE(g.name, t.name, '')
            ELSE COALESCE(NULLIF(ct.title, ''), NULLIF(peer.display_name, ''), peer.username, t.name, '') END AS topic_name,
       m.from_uid, COALESCE(NULLIF(sender.display_name, ''), sender.username, ''),
       m.content, m.msg_type, m.created_at, m.content_blocks
FROM messages m
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
WHERE (
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
  (? <> 'artifact' AND m.msg_type <> 'file' AND LOCATE(LOWER(?), LOWER(m.content)) > 0)
  OR
  (? <> 'message' AND (
    (m.content_blocks IS NOT NULL AND LOCATE(LOWER(?), LOWER(CAST(m.content_blocks AS CHAR))) > 0)
    OR (m.msg_type = 'file' AND LOCATE(LOWER(?), LOWER(m.content)) > 0)
  ))
)
ORDER BY m.created_at DESC, m.id DESC
LIMIT ? OFFSET ?`

// SearchMessages searches only topics readable by viewerUID. Access filtering is atomic with selection.
func (a *Adapter) SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	return store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
		rows, err := a.db.Query(mysqlMessageSearchQuery, viewerUID, searchType, query, searchType, query, query, pageSize, offset)
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
		artifactName := store.MatchingArtifactName(blocksJSON, query)
		if artifactName == "" && msgType == "file" {
			artifactName = store.LegacyMatchingArtifactName(result.Content, query)
		}
		contentMatches := store.MessageSearchContentMatches(msgType, result.Content, query)
		if !store.ShouldIncludeMessageSearchCandidate(searchType, contentMatches, artifactName) {
			continue
		}
		if artifactName != "" && (searchType == store.MessageSearchArtifact || !contentMatches) {
			result.ContentType = store.MessageSearchArtifact
			result.ArtifactName = artifactName
			result.Snippet = artifactName
		} else {
			result.ContentType = store.MessageSearchMessage
			result.Snippet = store.MessageSearchSnippet(result.Content, query)
		}
		results = append(results, &result)
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
