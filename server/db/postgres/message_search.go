package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const postgresMessageSearchQuery = `
SELECT m.id, m.topic_id,
       CASE WHEN t.type = 'group' THEN COALESCE(g.name, t.name, '')
            ELSE COALESCE(NULLIF(ct.title, ''), NULLIF(peer.display_name, ''), peer.username, t.name, '') END AS topic_name,
       m.from_uid, COALESCE(NULLIF(sender.display_name, ''), sender.username, ''),
       m.content, m.msg_type, m.created_at, m.content_blocks
FROM messages m
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
  ($2 <> 'artifact' AND m.msg_type <> 'file' AND STRPOS(LOWER(m.content), LOWER($3)) > 0)
  OR
  ($2 <> 'message' AND (
    (m.content_blocks IS NOT NULL AND STRPOS(LOWER(m.content_blocks::text), LOWER($3)) > 0)
    OR (m.msg_type = 'file' AND STRPOS(LOWER(m.content), LOWER($3)) > 0)
  ))
)
ORDER BY m.created_at DESC, m.id DESC
LIMIT $4 OFFSET $5`

// SearchMessages searches only topics readable by viewerUID. Access filtering is atomic with selection.
func (a *Adapter) SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	pageSize := limit * 10
	if pageSize < 200 {
		pageSize = 200
	}
	results := make([]*store.MessageSearchResult, 0, limit)
	offset := 0
	for len(results) < limit {
		rows, err := a.db.Query(postgresMessageSearchQuery, viewerUID, searchType, query, pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		page, scanned, scanErr := scanPostgresMessageSearch(rows, query, searchType, limit-len(results))
		closeErr := rows.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close message search rows: %w", closeErr)
		}
		results = append(results, page...)
		if !postgresShouldContinueSearch(len(results), limit, scanned, pageSize) {
			break
		}
		offset += scanned
	}
	return results, nil
}

func postgresMessageContentMatches(msgType, content, query string) bool {
	if msgType == "file" {
		return false
	}
	return strings.Contains(strings.ToLower(content), strings.ToLower(query))
}

func postgresShouldContinueSearch(resultCount, limit, scanned, pageSize int) bool {
	return resultCount < limit && scanned == pageSize
}

func postgresShouldIncludeSearchCandidate(searchType string, contentMatches bool, artifactName string) bool {
	switch searchType {
	case store.MessageSearchMessage:
		return contentMatches
	case store.MessageSearchArtifact:
		return artifactName != ""
	default:
		return contentMatches || artifactName != ""
	}
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
		artifactName := postgresMatchingArtifactName(jsonBytes(blocksJSON), query)
		if artifactName == "" && msgType == "file" {
			artifactName = postgresLegacyMatchingArtifactName(result.Content, query)
		}
		contentMatches := postgresMessageContentMatches(msgType, result.Content, query)
		if !postgresShouldIncludeSearchCandidate(searchType, contentMatches, artifactName) {
			continue
		}
		if artifactName != "" && (searchType == store.MessageSearchArtifact || !contentMatches) {
			result.ContentType = store.MessageSearchArtifact
			result.ArtifactName = artifactName
			result.Snippet = artifactName
		} else {
			result.ContentType = store.MessageSearchMessage
			result.Snippet = postgresMessageSearchSnippet(result.Content, query)
		}
		results = append(results, &result)
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

func postgresMatchingArtifactName(raw []byte, query string) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	needle := strings.ToLower(query)
	for _, block := range blocks {
		if block.Type != "file" && block.Type != "image" && block.Type != "audio" && block.Type != "video" {
			continue
		}
		names := []string{block.Name}
		for _, key := range []string{"name", "file_name", "filename", "title"} {
			if value, ok := block.Payload[key].(string); ok {
				names = append(names, value)
			}
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" && strings.Contains(strings.ToLower(name), needle) {
				return name
			}
		}
	}
	return ""
}

func postgresLegacyMatchingArtifactName(content, query string) string {
	var value interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &value); err != nil {
		return ""
	}
	if encoded, ok := value.(string); ok {
		if err := json.Unmarshal([]byte(strings.TrimSpace(encoded)), &value); err != nil {
			return ""
		}
	}
	rich, _ := value.(map[string]interface{})
	if rich == nil {
		return ""
	}
	payload := rich
	if nested, ok := rich["payload"].(map[string]interface{}); ok {
		payload = nested
	}
	needle := strings.ToLower(query)
	for _, key := range []string{"name", "file_name", "filename", "title"} {
		name, _ := payload[key].(string)
		name = strings.TrimSpace(name)
		if name != "" && strings.Contains(strings.ToLower(name), needle) {
			return name
		}
	}
	return ""
}

func postgresMessageSearchSnippet(content, query string) string {
	const maxRunes = 160
	text := strings.TrimSpace(content)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	lowerRunes := []rune(strings.ToLower(text))
	needle := []rune(strings.ToLower(query))
	index := 0
	if len(needle) > 0 {
		for i := 0; i+len(needle) <= len(lowerRunes); i++ {
			if string(lowerRunes[i:i+len(needle)]) == string(needle) {
				index = i
				break
			}
		}
	}
	start := index - maxRunes/3
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(runes) {
		end = len(runes)
		start = end - maxRunes
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}

// GetMessagesAround returns a bounded chronological window around a message in one topic.
func (a *Adapter) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	beforeLimit := (limit + 1) / 2
	afterLimit := limit - beforeLimit
	rows, err := a.db.Query(`
SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role FROM (
  (SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
   FROM messages WHERE topic_id = $1 AND id <= $2 ORDER BY id DESC LIMIT $3)
  UNION ALL
  (SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
   FROM messages WHERE topic_id = $1 AND id > $2 ORDER BY id ASC LIMIT $4)
) around_messages ORDER BY id ASC`, topicID, messageID, beforeLimit, afterLimit)
	if err != nil {
		return nil, fmt.Errorf("get messages around: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan message around")
}
