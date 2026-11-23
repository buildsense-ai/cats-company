package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type wikiMemoryTestStore struct {
	store.Store
	groups map[int64][]*types.Group
	memory []store.WikiCatalogEntry
}

func (s *wikiMemoryTestStore) GetUser(id int64) (*types.User, error) {
	if id == 50 || id == 51 {
		return &types.User{ID: id, DisplayName: "Helper", AccountType: types.AccountBot}, nil
	}
	return &types.User{ID: id, DisplayName: "Human", AccountType: types.AccountHuman}, nil
}
func (s *wikiMemoryTestStore) GetBotOwner(int64) (int64, error) { return 1, nil }
func (s *wikiMemoryTestStore) AreFriends(user, bot int64) (bool, error) {
	return user == 2 && bot == 50, nil
}
func (s *wikiMemoryTestStore) GetUserGroups(uid int64) ([]*types.Group, error) {
	return s.groups[uid], nil
}
func (s *wikiMemoryTestStore) IsChannelManagedGroup(id int64) (bool, error) { return id == 9, nil }
func (s *wikiMemoryTestStore) IsGroupMember(id, uid int64) (bool, error) {
	if uid == 50 {
		return id == 7 || id == 9, nil
	}
	for _, group := range s.groups[uid] {
		if group.ID == id {
			return true, nil
		}
	}
	return false, nil
}
func (s *wikiMemoryTestStore) WikiCatalog() store.WikiCatalogStore { return s }
func (s *wikiMemoryTestStore) ReplaceWikiCatalog(_ context.Context, entries []store.WikiCatalogEntry) error {
	s.memory = entries
	return nil
}
func (s *wikiMemoryTestStore) ListWikiCatalog(_ context.Context, botUID int64, topics []string, _ store.WikiCatalogPosition, _ int) ([]store.WikiCatalogEntry, error) {
	var out []store.WikiCatalogEntry
	for _, entry := range s.memory {
		if botUID != 50 || entry.BotUID != "50" {
			continue
		}
		if topics == nil {
			out = append(out, entry)
			continue
		}
		for _, topic := range topics {
			if entry.TopicID == topic {
				out = append(out, entry)
				break
			}
		}
	}
	return out, nil
}
func (s *wikiMemoryTestStore) GetWikiCatalogEntry(_ context.Context, id string) (store.WikiCatalogEntry, bool, error) {
	for _, entry := range s.memory {
		if entry.ID == id {
			return entry, true, nil
		}
	}
	return store.WikiCatalogEntry{}, false, nil
}

func wikiFixture(id, topic string) store.WikiCatalogEntry {
	return store.WikiCatalogEntry{ID: id, BotUID: "50", TopicID: topic, Title: id, CreatedAt: "2026-11-19T00:00:00.000001Z", Revision: "v1"}
}

func wikiEndpoint(t *testing.T, db *wikiMemoryTestStore, upstream http.HandlerFunc) (*WikiMemoryHandler, *http.ServeMux) {
	t.Helper()
	service := httptest.NewServer(upstream)
	t.Cleanup(service.Close)
	handler := NewWikiMemoryHandler(db, &Hub{db: db}, service.URL, "service-key")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents/{uid}/memories", handler.HandleCatalog)
	mux.HandleFunc("GET /api/agents/{uid}/memories/{id}", handler.HandleDocument)
	return handler, mux
}

func wikiMemoryTestRequest(mux *http.ServeMux, uid int64, path, ifNoneMatch string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, uid))
	request.Header.Set("If-None-Match", ifNoneMatch)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestWikiMemoryAvailability(t *testing.T) {
	h := NewWikiMemoryHandler(&wikiMemoryTestStore{}, nil, "https://memory.internal:8460/", "test-key")
	response := httptest.NewRecorder()
	h.HandleAvailability(response, httptest.NewRequest(http.MethodGet, "/api/memories/availability", nil))
	var result map[string]bool
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || !result["enabled"] {
		t.Fatal(response.Body)
	}
}

func TestWikiMemoryCatalogIsLocalAndUsesLiveTopicPermissions(t *testing.T) {
	db := &wikiMemoryTestStore{groups: map[int64][]*types.Group{
		2: {{ID: 7, Name: "会话标题", Kind: types.GroupKindStandard, AgentIDs: []int64{50}},
			{ID: 8, Kind: types.GroupKindStandard, AgentIDs: []int64{51}},
			{ID: 9, Kind: types.GroupKindChannelManaged, AgentIDs: []int64{50}}},
	}, memory: []store.WikiCatalogEntry{wikiFixture("group", "grp_7"), wikiFixture("private", "p2p_2_50"), wikiFixture("unknown", "")}}
	_, mux := wikiEndpoint(t, db, func(w http.ResponseWriter, r *http.Request) {
		t.Error("directory request reached upstream")
		w.WriteHeader(http.StatusBadGateway)
	})
	const path = "/api/agents/50/memories"
	for _, tc := range []struct {
		viewer int64
		suffix string
		ids    []string
	}{
		{2, "?topic_id=grp_7", []string{"group"}},
		{2, "", []string{"group", "private"}},
		{3, "", []string{}},
		{1, "", []string{"group", "private", "unknown"}},
	} {
		res := wikiMemoryTestRequest(mux, tc.viewer, path+tc.suffix, "")
		var page wikiMemoryCatalog
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &page) != nil {
			t.Fatalf("catalog: %d %s", res.Code, res.Body)
		}
		ids := []string{}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		if !reflect.DeepEqual(ids, tc.ids) {
			t.Fatalf("viewer %d: got %v want %v", tc.viewer, ids, tc.ids)
		}
	}
	for _, topic := range []string{"grp_8", "grp_9", "p2p_3_50"} {
		if res := wikiMemoryTestRequest(mux, 2, path+"?topic_id="+topic, ""); res.Code != http.StatusNotFound {
			t.Errorf("inaccessible topic %s: %d", topic, res.Code)
		}
	}
}

func TestWikiMemoryConditionalReadRechecksGroupMembership(t *testing.T) {
	db := &wikiMemoryTestStore{groups: map[int64][]*types.Group{
		3: {{ID: 7, Kind: types.GroupKindAgentTask, AgentIDs: []int64{50}}},
	}, memory: []store.WikiCatalogEntry{wikiFixture("wiki-1", "grp_7"), wikiFixture("owner-only", "")}}
	proxied := make(chan string, 4)
	_, mux := wikiEndpoint(t, db, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request) != 1 || request["id"] != "wiki-1" || r.Header.Get("Authorization") != "Bearer service-key" {
			t.Errorf("bad service request: %v", request)
		}
		tag := r.Header.Get("If-None-Match")
		proxied <- tag
		w.Header().Set("ETag", `"v1"`)
		if tag == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(wikiMemoryDocument{ID: "wiki-1", Revision: "v1", Markdown: "# Bot memory"})
	})
	const path = "/api/agents/50/memories/wiki-1"
	res := wikiMemoryTestRequest(mux, 3, path, "")
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("body: %d %s", res.Code, res.Body)
	}
	if cached := wikiMemoryTestRequest(mux, 3, path, `"v1"`); cached.Code != http.StatusNotModified {
		t.Fatalf("conditional: %d", cached.Code)
	}
	delete(db.groups, 3)
	for _, denied := range []string{path, "/api/agents/51/memories/wiki-1", "/api/agents/50/memories/owner-only"} {
		if res := wikiMemoryTestRequest(mux, 3, denied, `"v1"`); res.Code != http.StatusNotFound || res.Header().Get("ETag") != "" {
			t.Errorf("denied: %d %s", res.Code, res.Body)
		}
	}
	if len(proxied) != 2 {
		t.Fatalf("upstream requests: %d", len(proxied))
	}
}

func TestWikiMemorySyncKeepsPreviousSnapshotUntilAllPagesSucceed(t *testing.T) {
	old := wikiFixture("old", "grp_7")
	db := &wikiMemoryTestStore{memory: []store.WikiCatalogEntry{old}}
	fail := true
	requests := 0
	h, _ := wikiEndpoint(t, db, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/internal/catsco/memory/catalog" {
			t.Error(r.URL.Path)
		}
		var request wikiCatalogRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Cursor == "" {
			cursor := "next"
			_ = json.NewEncoder(w).Encode(wikiCatalogPage{Items: []store.WikiCatalogEntry{wikiFixture("new", "grp_7")}, NextCursor: &cursor})
			return
		}
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(wikiCatalogPage{Items: []store.WikiCatalogEntry{wikiFixture("other", "")}})
	})
	if err := h.syncWikiCatalog(context.Background()); err == nil || !reflect.DeepEqual(db.memory, []store.WikiCatalogEntry{old}) {
		t.Fatalf("failed sync replaced snapshot: %v %v", err, db.memory)
	}
	fail = false
	if err := h.syncWikiCatalog(context.Background()); err != nil || len(db.memory) != 2 || db.memory[0].ID != "new" || requests != 4 {
		t.Fatalf("successful sync: %v %+v (%d requests)", err, db.memory, requests)
	}
}
