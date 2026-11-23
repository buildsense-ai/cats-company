package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const wikiMemoryMaxBody = 2 << 20

var wikiMemoryID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type WikiMemoryHandler struct {
	db      store.Store
	hub     *Hub
	catalog store.WikiCatalogStore
	baseURL string
	token   string
	client  *http.Client
}

func NewWikiMemoryHandler(db store.Store, hub *Hub, baseURL, token string) *WikiMemoryHandler {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") ||
		base.User != nil || base.ForceQuery || base.RawQuery != "" || base.Fragment != "" ||
		(base.EscapedPath() != "" && base.EscapedPath() != "/") {
		baseURL = ""
	} else {
		baseURL = base.Scheme + "://" + base.Host
	}
	handler := &WikiMemoryHandler{
		db: db, hub: hub, baseURL: baseURL, token: strings.TrimSpace(token),
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if provider, ok := db.(store.WikiCatalogProvider); ok {
		handler.catalog = provider.WikiCatalog()
	}
	return handler
}

func (h *WikiMemoryHandler) configured() bool {
	return h.baseURL != "" && h.token != "" && h.catalog != nil
}

func (h *WikiMemoryHandler) HandleAvailability(w http.ResponseWriter, _ *http.Request) {
	writeWikiMemoryJSON(w, http.StatusOK, map[string]bool{"enabled": h.configured()})
}

type wikiMemoryItem struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	ConversationIDs []string `json:"conversation_ids"`
	CreatedAt       string   `json:"created_at"`
	Revision        string   `json:"revision"`
}

type wikiMemoryConversation struct {
	ID      string `json:"id"`
	TopicID string `json:"topic_id"`
	Title   string `json:"title"`
}

type wikiMemoryCatalog struct {
	Items         []wikiMemoryItem         `json:"items"`
	Conversations []wikiMemoryConversation `json:"conversations"`
	NextCursor    *string                  `json:"next_cursor"`
}

type wikiMemoryDocument struct {
	ID         string   `json:"id"`
	Markdown   string   `json:"markdown"`
	SourceRefs []string `json:"source_refs"`
	Revision   string   `json:"revision"`
}

func (h *WikiMemoryHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	viewerUID, botUID, ok := h.identify(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	topic, limit := query.Get("topic_id"), 100
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			wikiMemoryError(w, http.StatusBadRequest)
			return
		}
	}
	after, valid := wikiCatalogCursor(query.Get("cursor"))
	if !valid || limit < 1 || limit > 200 || (topic != "" && !wikiMemoryBotTopic(topic, botUID)) {
		wikiMemoryError(w, http.StatusBadRequest)
		return
	}
	var topics []string
	var titles map[string]string
	if topic != "" {
		if status := h.conversationAccess(viewerUID, botUID, topic); status != http.StatusOK {
			wikiMemoryError(w, status)
			return
		}
		topics = []string{topic}
	} else {
		var err error
		topics, titles, err = h.allowedTopics(viewerUID, botUID)
		if err != nil {
			wikiMemoryError(w, http.StatusServiceUnavailable)
			return
		}
	}
	entries, err := h.catalog.ListWikiCatalog(r.Context(), botUID, topics, after, limit+1)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	catalog := wikiMemoryCatalog{Items: make([]wikiMemoryItem, 0, min(limit, len(entries)))}
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[len(entries)-1]
		data, _ := json.Marshal([2]string{last.CreatedAt, last.ID})
		cursor := base64.RawURLEncoding.EncodeToString(data)
		catalog.NextCursor = &cursor
	}
	for _, entry := range entries {
		conversations := []string{}
		if entry.TopicID != "" {
			conversations = append(conversations, entry.TopicID)
		}
		catalog.Items = append(catalog.Items, wikiMemoryItem{ID: entry.ID, Title: entry.Title,
			ConversationIDs: conversations, CreatedAt: entry.CreatedAt, Revision: entry.Revision})
	}
	catalog.Conversations = h.conversations(viewerUID, botUID, catalog.Items, titles)
	writeWikiMemoryJSON(w, http.StatusOK, catalog)
}

func (h *WikiMemoryHandler) HandleDocument(w http.ResponseWriter, r *http.Request) {
	viewerUID, botUID, ok := h.identify(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !wikiMemoryID.MatchString(id) || len(r.Header.Get("If-None-Match")) > 4096 {
		wikiMemoryError(w, http.StatusBadRequest)
		return
	}
	entry, found, err := h.catalog.GetWikiCatalogEntry(r.Context(), id)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	if !found || entry.BotUID != strconv.FormatInt(botUID, 10) {
		wikiMemoryError(w, http.StatusNotFound)
		return
	}
	if status := h.documentAccess(viewerUID, botUID, entry.TopicID); status != http.StatusOK {
		wikiMemoryError(w, status)
		return
	}
	var document wikiMemoryDocument
	etag, status := h.query(r.Context(), "wiki", wikiBodyRequest{ID: id}, r.Header.Get("If-None-Match"), &document)
	if status != http.StatusOK && status != http.StatusNotModified {
		wikiMemoryError(w, status)
		return
	}
	if etag != `"`+entry.Revision+`"` || (status == http.StatusOK && (document.ID != id || document.Revision != entry.Revision)) ||
		(status == http.StatusNotModified && r.Header.Get("If-None-Match") == "") {
		wikiMemoryError(w, http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Add("Vary", "Authorization")
	w.Header().Set("ETag", etag)
	if status == http.StatusNotModified {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(document)
}

func (h *WikiMemoryHandler) identify(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		wikiMemoryError(w, http.StatusUnauthorized)
		return 0, 0, false
	}
	botUID, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || botUID <= 0 || botUID == viewerUID {
		wikiMemoryError(w, http.StatusBadRequest)
		return 0, 0, false
	}
	if !h.configured() {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return 0, 0, false
	}
	bot, err := h.db.GetUser(botUID)
	if err != nil || bot == nil || bot.AccountType != types.AccountBot {
		wikiMemoryError(w, http.StatusNotFound)
		return 0, 0, false
	}
	return viewerUID, botUID, true
}

func (h *WikiMemoryHandler) allowedTopics(viewerUID, botUID int64) ([]string, map[string]string, error) {
	owner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		return nil, nil, err
	}
	if owner == viewerUID {
		return nil, nil, nil
	}
	topics := []string{}
	if direct := p2pTopicID(viewerUID, botUID); h.conversationAccess(viewerUID, botUID, direct) == http.StatusOK {
		topics = append(topics, direct)
	}
	groups, err := h.db.GetUserGroups(viewerUID)
	if err != nil {
		return nil, nil, err
	}
	titles := make(map[string]string)
	for _, group := range groups {
		// GetUserGroups returns standard and agent task groups of the viewer.
		if group != nil && (group.Kind == types.GroupKindStandard || group.Kind == types.GroupKindAgentTask) && slices.Contains(group.AgentIDs, botUID) {
			topic := "grp_" + strconv.FormatInt(group.ID, 10)
			topics = append(topics, topic)
			titles[topic] = group.Name
		}
	}
	return topics, titles, nil
}

func (h *WikiMemoryHandler) conversationAccess(viewerUID, botUID int64, topic string) int {
	if h.hub == nil {
		return http.StatusServiceUnavailable
	}
	if code, _ := h.hub.validateTopicReadAccess(viewerUID, types.AccountHuman, topic); code != 0 {
		if code >= 500 {
			return http.StatusServiceUnavailable
		}
		return http.StatusNotFound
	}
	if group := extractGroupID(topic); group > 0 {
		member, err := h.db.IsGroupMember(group, botUID)
		if err != nil {
			return http.StatusServiceUnavailable
		}
		if !member {
			return http.StatusNotFound
		}
	}
	return http.StatusOK
}

func (h *WikiMemoryHandler) documentAccess(viewerUID, botUID int64, topic string) int {
	owner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if owner == viewerUID {
		return http.StatusOK
	}
	if !wikiMemoryBotTopic(topic, botUID) {
		return http.StatusNotFound
	}
	return h.conversationAccess(viewerUID, botUID, topic)
}

func wikiMemoryBotTopic(topic string, botUID int64) bool {
	if group := extractGroupID(topic); group > 0 && topic == "grp_"+strconv.FormatInt(group, 10) {
		return true
	}
	peer := extractPeerUID(topic, botUID)
	return peer > 0 && peer != botUID && topic == p2pTopicID(peer, botUID)
}

func wikiCatalogCursor(cursor string) (store.WikiCatalogPosition, bool) {
	if cursor == "" {
		return store.WikiCatalogPosition{}, true
	}
	if len(cursor) > 512 {
		return store.WikiCatalogPosition{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	var fields []string
	if err != nil || json.Unmarshal(data, &fields) != nil || len(fields) != 2 ||
		!validWikiTime(fields[0]) || !wikiMemoryID.MatchString(fields[1]) {
		return store.WikiCatalogPosition{}, false
	}
	return store.WikiCatalogPosition{CreatedAt: fields[0], ID: fields[1]}, true
}

func (h *WikiMemoryHandler) conversations(viewerUID, botUID int64, items []wikiMemoryItem, groupTitles map[string]string) []wikiMemoryConversation {
	seen := make(map[string]bool)
	ids := []string{}
	for _, item := range items {
		for _, id := range item.ConversationIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	var personalTitles map[string]string
	if titleStore, ok := h.db.(conversationTitleStore); ok && len(ids) > 0 {
		personalTitles, _ = titleStore.GetConversationTitles(viewerUID, ids)
	}
	peerByTopic := make(map[string]int64)
	peers := []int64{}
	var needsGroupTitles bool
	for _, id := range ids {
		if !wikiMemoryBotTopic(id, botUID) || personalTitles[id] != "" {
			continue
		}
		if extractGroupID(id) > 0 {
			needsGroupTitles = needsGroupTitles || groupTitles[id] == ""
			continue
		}
		peer := extractPeerUID(id, viewerUID)
		if peer == 0 {
			peer = extractPeerUID(id, botUID)
		}
		peerByTopic[id] = peer
		peers = append(peers, peer)
	}
	var users map[int64]*types.User
	if userStore, ok := h.db.(usersByIDsStore); ok && len(peers) > 0 {
		users, _ = userStore.GetUsersByIDs(peers)
	}
	if needsGroupTitles {
		if groupTitles == nil {
			groupTitles = make(map[string]string)
		}
		if groups, err := h.db.GetUserGroups(botUID); err == nil {
			for _, group := range groups {
				if group != nil {
					groupTitles["grp_"+strconv.FormatInt(group.ID, 10)] = group.Name
				}
			}
		}
	}
	result := make([]wikiMemoryConversation, 0, len(ids))
	for _, id := range ids {
		if !wikiMemoryBotTopic(id, botUID) {
			result = append(result, wikiMemoryConversation{ID: id, Title: "其他来源"})
			continue
		}
		title := strings.TrimSpace(personalTitles[id])
		if title == "" {
			title = strings.TrimSpace(groupTitles[id])
		}
		if user := users[peerByTopic[id]]; title == "" && user != nil {
			title = firstNonEmpty(user.DisplayName, user.Username)
		}
		result = append(result, wikiMemoryConversation{ID: id, TopicID: id, Title: firstNonEmpty(title, "未命名会话")})
	}
	return result
}

func (h *WikiMemoryHandler) query(ctx context.Context, endpoint string, input any, ifNoneMatch string, output any) (string, int) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", http.StatusInternalServerError
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/internal/catsco/memory/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return "", http.StatusServiceUnavailable
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Authorization", "Bearer "+h.token)
	if ifNoneMatch != "" {
		upstream.Header.Set("If-None-Match", ifNoneMatch)
	}
	response, err := h.client.Do(upstream)
	if err != nil {
		return "", http.StatusBadGateway
	}
	defer response.Body.Close()
	etag := response.Header.Get("ETag")
	switch response.StatusCode {
	case http.StatusNotModified:
		if endpoint == "wiki" {
			return etag, http.StatusNotModified
		}
	case http.StatusOK:
		payload, err := io.ReadAll(io.LimitReader(response.Body, wikiMemoryMaxBody+1))
		if err == nil && len(payload) <= wikiMemoryMaxBody && json.Unmarshal(payload, output) == nil {
			return etag, http.StatusOK
		}
	case http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests:
		return "", response.StatusCode
	}
	return "", http.StatusBadGateway
}

func wikiMemoryError(w http.ResponseWriter, status int) {
	writeWikiMemoryJSON(w, status, map[string]string{"error": http.StatusText(status)})
}

func writeWikiMemoryJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
