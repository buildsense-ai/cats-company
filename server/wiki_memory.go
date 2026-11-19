package server

import (
	"bytes"
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
	baseURL string
	token   string
	client  *http.Client
}

func NewWikiMemoryHandler(db store.Store, baseURL, token string) *WikiMemoryHandler {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") ||
		base.User != nil || base.ForceQuery || base.RawQuery != "" || base.Fragment != "" ||
		(base.EscapedPath() != "" && base.EscapedPath() != "/") {
		baseURL = ""
	} else {
		baseURL = base.Scheme + "://" + base.Host
	}
	return &WikiMemoryHandler{
		db: db, baseURL: baseURL, token: strings.TrimSpace(token),
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (h *WikiMemoryHandler) configured() bool {
	return h.baseURL != "" && h.token != ""
}

func (h *WikiMemoryHandler) HandleAvailability(w http.ResponseWriter, _ *http.Request) {
	writeWikiMemoryJSON(w, http.StatusOK, map[string]bool{"enabled": h.configured()})
}

type wikiMemoryScope struct {
	BotUID   string   `json:"bot_uid"`
	Kind     string   `json:"kind"`
	TopicIDs []string `json:"topic_ids,omitempty"`
}

type wikiMemoryRequest struct {
	Scope   wikiMemoryScope `json:"scope"`
	TopicID string          `json:"topic_id,omitempty"`
	Cursor  string          `json:"cursor,omitempty"`
	Limit   int             `json:"limit,omitempty"`
	ID      string          `json:"id,omitempty"`
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
	input := wikiMemoryRequest{TopicID: query.Get("topic_id"), Cursor: query.Get("cursor"), Limit: 100}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			wikiMemoryError(w, http.StatusBadRequest)
			return
		}
		input.Limit = limit
	}
	if len(input.Cursor) > 2048 || input.Limit < 1 || input.Limit > 200 || (input.TopicID != "" && !wikiMemoryBotTopic(input.TopicID, botUID)) {
		wikiMemoryError(w, http.StatusBadRequest)
		return
	}
	scope, titles, err := h.scope(viewerUID, botUID)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	if scope.Kind == "topics" && (len(scope.TopicIDs) == 0 || (input.TopicID != "" && !slices.Contains(scope.TopicIDs, input.TopicID))) {
		wikiMemoryError(w, http.StatusNotFound)
		return
	}
	input.Scope = scope
	var catalog wikiMemoryCatalog
	_, status := h.query(r, "catalog", input, &catalog)
	if status != http.StatusOK {
		wikiMemoryError(w, status)
		return
	}
	if !validWikiMemoryCatalog(catalog.Items, input) {
		wikiMemoryError(w, http.StatusBadGateway)
		return
	}
	if catalog.Items == nil {
		catalog.Items = []wikiMemoryItem{}
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
	scope, _, err := h.scope(viewerUID, botUID)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	if scope.Kind == "topics" && len(scope.TopicIDs) == 0 {
		wikiMemoryError(w, http.StatusNotFound)
		return
	}
	var document wikiMemoryDocument
	etag, status := h.query(r, "wiki", wikiMemoryRequest{Scope: scope, ID: id}, &document)
	if status != http.StatusOK && status != http.StatusNotModified {
		wikiMemoryError(w, status)
		return
	}
	if etag == "" || (status == http.StatusOK && (document.ID != id || document.Revision == "")) ||
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

func (h *WikiMemoryHandler) scope(viewerUID, botUID int64) (wikiMemoryScope, map[string]string, error) {
	scope := wikiMemoryScope{BotUID: strconv.FormatInt(botUID, 10), Kind: "topics"}
	owner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		return scope, nil, err
	}
	if owner == viewerUID {
		scope.Kind = "owner"
		return scope, nil, nil
	}
	friends, err := h.db.AreFriends(viewerUID, botUID)
	if err != nil {
		return scope, nil, err
	}
	if friends {
		scope.TopicIDs = append(scope.TopicIDs, p2pTopicID(viewerUID, botUID))
	}
	groups, err := h.db.GetUserGroups(viewerUID)
	if err != nil {
		return scope, nil, err
	}
	titles := make(map[string]string)
	for _, group := range groups {
		// GetUserGroups returns standard and agent task groups of the viewer.
		if group != nil && (group.Kind == types.GroupKindStandard || group.Kind == types.GroupKindAgentTask) && slices.Contains(group.AgentIDs, botUID) {
			topic := "grp_" + strconv.FormatInt(group.ID, 10)
			scope.TopicIDs = append(scope.TopicIDs, topic)
			titles[topic] = group.Name
		}
	}
	slices.Sort(scope.TopicIDs)
	scope.TopicIDs = slices.Compact(scope.TopicIDs)
	return scope, titles, nil
}

func wikiMemoryBotTopic(topic string, botUID int64) bool {
	if group := extractGroupID(topic); group > 0 && topic == "grp_"+strconv.FormatInt(group, 10) {
		return true
	}
	peer := extractPeerUID(topic, botUID)
	return peer > 0 && peer != botUID && topic == p2pTopicID(peer, botUID)
}

func validWikiMemoryCatalog(items []wikiMemoryItem, input wikiMemoryRequest) bool {
	if len(items) > input.Limit {
		return false
	}
	for _, item := range items {
		if !wikiMemoryID.MatchString(item.ID) || item.Revision == "" ||
			(input.TopicID != "" && !slices.Contains(item.ConversationIDs, input.TopicID)) {
			return false
		}
		if input.Scope.Kind == "topics" {
			if len(item.ConversationIDs) == 0 {
				return false
			}
			for _, topic := range item.ConversationIDs {
				if !slices.Contains(input.Scope.TopicIDs, topic) {
					return false
				}
			}
		}
	}
	return true
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

func (h *WikiMemoryHandler) query(r *http.Request, endpoint string, input wikiMemoryRequest, output any) (string, int) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", http.StatusInternalServerError
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.baseURL+"/internal/catsco/memory/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return "", http.StatusServiceUnavailable
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Authorization", "Bearer "+h.token)
	if endpoint == "wiki" {
		if etag := r.Header.Get("If-None-Match"); etag != "" {
			upstream.Header.Set("If-None-Match", etag)
		}
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
