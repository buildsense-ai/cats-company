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
}

func (s *wikiMemoryTestStore) GetUser(id int64) (*types.User, error) {
	if id == 50 {
		return &types.User{ID: id, DisplayName: "Helper", AccountType: types.AccountBot}, nil
	}
	return &types.User{ID: id, DisplayName: "Human", AccountType: types.AccountHuman}, nil
}

func (s *wikiMemoryTestStore) GetBotOwner(int64) (int64, error) { return 1, nil }

func (s *wikiMemoryTestStore) AreFriends(viewerUID, botUID int64) (bool, error) {
	return viewerUID == 2 && botUID == 50, nil
}

func (s *wikiMemoryTestStore) GetUserGroups(uid int64) ([]*types.Group, error) {
	return s.groups[uid], nil
}

func (s *wikiMemoryTestStore) GetGroup(id int64) (*types.Group, error) {
	return &types.Group{ID: id, Name: "会话标题"}, nil
}

func newWikiMemoryEndpoint(t *testing.T, db store.Store, upstream http.HandlerFunc) *http.ServeMux {
	t.Helper()
	service := httptest.NewServer(upstream)
	t.Cleanup(service.Close)
	handler := NewWikiMemoryHandler(db, service.URL, "service-key")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents/{uid}/memories", handler.HandleCatalog)
	mux.HandleFunc("GET /api/agents/{uid}/memories/{id}", handler.HandleDocument)
	return mux
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

func TestWikiMemoryCatalogUsesLiveTopicPermissions(t *testing.T) {
	db := &wikiMemoryTestStore{groups: map[int64][]*types.Group{
		2: {{ID: 7, Name: "会话标题", Kind: types.GroupKindStandard, AgentIDs: []int64{50}},
			{ID: 8, Kind: types.GroupKindStandard, AgentIDs: []int64{51}},
			{ID: 9, Kind: types.GroupKindChannelManaged, AgentIDs: []int64{50}}},
	}}
	proxied := make(chan wikiMemoryRequest, 4)
	mux := newWikiMemoryEndpoint(t, db, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/catsco/memory/catalog" || r.Header.Get("Authorization") != "Bearer service-key" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var request wikiMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		proxied <- request
		_ = json.NewEncoder(w).Encode(wikiMemoryCatalog{Items: []wikiMemoryItem{{ID: "wiki-1", Title: "Title", ConversationIDs: []string{"grp_7"}, Revision: "v1"}}})
	})
	const path = "/api/agents/50/memories"
	read := wikiMemoryTestRequest(mux, 2, path+"?topic_id=grp_7", "")
	if read.Code != http.StatusOK {
		t.Fatalf("allowed catalog: %d %s", read.Code, read.Body)
	}
	proceed := <-proxied
	if proceed.Scope.Kind != "topics" || !reflect.DeepEqual(proceed.Scope.TopicIDs, []string{"grp_7", "p2p_2_50"}) {
		t.Errorf("bad scope: %+v", proceed.Scope)
	}
	var page wikiMemoryCatalog
	if err := json.NewDecoder(read.Body).Decode(&page); err != nil || len(page.Conversations) != 1 || page.Conversations[0].Title != "会话标题" {
		t.Fatalf("bad conversations: %+v (%v)", page.Conversations, err)
	}
	for _, topic := range []string{"grp_8", "grp_9", "p2p_3_50"} {
		if response := wikiMemoryTestRequest(mux, 2, path+"?topic_id="+topic, ""); response.Code != http.StatusNotFound {
			t.Errorf("inaccessible topic %s: %d", topic, response.Code)
		}
	}
	if response := wikiMemoryTestRequest(mux, 3, path, ""); response.Code != http.StatusNotFound {
		t.Errorf("viewer without accessible topics: %d", response.Code)
	}
	if len(proxied) != 0 {
		t.Fatal("denied request reached upstream")
	}
	if response := wikiMemoryTestRequest(mux, 1, path, ""); response.Code != http.StatusOK {
		t.Fatalf("owner catalog: %d %s", response.Code, response.Body)
	}
	if scope := (<-proxied).Scope; scope.Kind != "owner" || len(scope.TopicIDs) != 0 {
		t.Errorf("bad owner scope: %+v", scope)
	}
}

func TestWikiMemoryConditionalReadRechecksGroupMembership(t *testing.T) {
	db := &wikiMemoryTestStore{groups: map[int64][]*types.Group{
		3: {{ID: 7, Name: "会话标题", Kind: types.GroupKindAgentTask, AgentIDs: []int64{50}}},
	}}
	proxied := make(chan string, 4)
	mux := newWikiMemoryEndpoint(t, db, func(w http.ResponseWriter, r *http.Request) {
		var request wikiMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.ID != "wiki-1" || !reflect.DeepEqual(request.Scope.TopicIDs, []string{"grp_7"}) {
			t.Errorf("bad wiki request: %+v", request)
		}
		tag := r.Header.Get("If-None-Match")
		proxied <- tag
		w.Header().Set("ETag", `"v1"`)
		if tag == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(wikiMemoryDocument{ID: "wiki-1", Revision: "v1", Markdown: "## Bot memory", SourceRefs: []string{"ep_1"}})
	})
	const path = "/api/agents/50/memories/wiki-1"
	first := wikiMemoryTestRequest(mux, 3, path, "")
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "private, no-cache" || first.Header().Get("Vary") != "Authorization" {
		t.Fatalf("initial wiki body: %d %+v %s", first.Code, first.Header(), first.Body)
	}
	second := wikiMemoryTestRequest(mux, 3, path, first.Header().Get("ETag"))
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 || second.Header().Get("ETag") != `"v1"` {
		t.Fatalf("conditional wiki response: %d %+v %s", second.Code, second.Header(), second.Body)
	}
	delete(db.groups, 3)
	revoked := wikiMemoryTestRequest(mux, 3, path, first.Header().Get("ETag"))
	if revoked.Code != http.StatusNotFound || revoked.Header().Get("ETag") != "" {
		t.Fatalf("revoked request disclosed cached document: %d %+v", revoked.Code, revoked.Header())
	}
	if len(proxied) != 2 || <-proxied != "" || <-proxied != `"v1"` {
		t.Fatal("unexpected upstream validators")
	}
}
