package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	MessageSearchAll      = "all"
	MessageSearchMessage  = "message"
	MessageSearchArtifact = "artifact"

	defaultMessageSearchCandidatePageSize = 200
	maxMessageSearchCandidateRows         = 2000
)

type messageSearchPager struct {
	resultLimit int
	pageSize    int
	scanned     int
}

type MessageSearchPageLoader func(pageSize, offset, remaining int) ([]*MessageSearchResult, int, error)

func CollectMessageSearchResults(limit int, loadPage MessageSearchPageLoader) ([]*MessageSearchResult, error) {
	pager := newMessageSearchPager(limit)
	results := make([]*MessageSearchResult, 0, limit)
	offset := 0
	for pageSize := pager.nextPageLimit(); pageSize > 0; pageSize = pager.nextPageLimit() {
		page, scanned, err := loadPage(pageSize, offset, limit-len(results))
		if err != nil {
			return nil, err
		}
		results = append(results, page...)
		if !pager.recordPage(len(results), scanned) {
			break
		}
		offset += scanned
	}
	return results, nil
}

func newMessageSearchPager(resultLimit int) *messageSearchPager {
	pageSize := resultLimit * 10
	if pageSize < defaultMessageSearchCandidatePageSize {
		pageSize = defaultMessageSearchCandidatePageSize
	}
	return &messageSearchPager{resultLimit: resultLimit, pageSize: pageSize}
}

func (p *messageSearchPager) nextPageLimit() int {
	remaining := maxMessageSearchCandidateRows - p.scanned
	if remaining <= 0 {
		return 0
	}
	if remaining < p.pageSize {
		return remaining
	}
	return p.pageSize
}

func (p *messageSearchPager) recordPage(resultCount, scanned int) bool {
	requested := p.nextPageLimit()
	p.scanned += scanned
	return resultCount < p.resultLimit &&
		requested > 0 &&
		scanned == requested &&
		p.scanned < maxMessageSearchCandidateRows
}

func MessageSearchContentMatches(msgType, content, query string) bool {
	return msgType != "file" && strings.Contains(strings.ToLower(content), strings.ToLower(query))
}

func ShouldIncludeMessageSearchCandidate(searchType string, contentMatches bool, artifactName string) bool {
	switch searchType {
	case MessageSearchMessage:
		return contentMatches
	case MessageSearchArtifact:
		return artifactName != ""
	default:
		return contentMatches || artifactName != ""
	}
}

func MatchingArtifactName(raw []byte, query string) string {
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

func LegacyMatchingArtifactName(content, query string) string {
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

func MessageSearchSnippet(content, query string) string {
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
	for i := 0; len(needle) > 0 && i+len(needle) <= len(lowerRunes); i++ {
		if string(lowerRunes[i:i+len(needle)]) == string(needle) {
			index = i
			break
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

// MessageSearchResult is a permission-scoped global message search hit.
type MessageSearchResult struct {
	MessageID    int64     `json:"message_id"`
	TopicID      string    `json:"topic_id"`
	TopicName    string    `json:"topic_name"`
	FromUID      int64     `json:"from_uid"`
	SenderName   string    `json:"sender_name"`
	Content      string    `json:"content"`
	Snippet      string    `json:"snippet"`
	ContentType  string    `json:"content_type"`
	ArtifactName string    `json:"artifact_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// MessageSearchStore is optional so narrow fake stores are not forced to implement search.
// Implementations must apply topic read access in the same query that selects results.
type MessageSearchStore interface {
	SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*MessageSearchResult, error)
}

// MessageAroundStore is optional support for locating an old message in topic history.
type MessageAroundStore interface {
	GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error)
}
