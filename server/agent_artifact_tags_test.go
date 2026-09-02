package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type artifactTagTestStore struct {
	*agentTestStore
	tags          map[int64]map[string][]string
	storedBy      map[int64]int64
	countsErr     error
	purgeFailures map[int64]int
}

func newArtifactTagTestStore() *artifactTagTestStore {
	fixture := &artifactTagTestStore{
		agentTestStore: managedArtifactAgentStore(8, 440, true),
		tags:           map[int64]map[string][]string{},
		storedBy:       map[int64]int64{},
		purgeFailures:  map[int64]int{},
	}
	fixture.friendPairs[agentPairKey(7, 440)] = true
	return fixture
}

func (s *artifactTagTestStore) set(agentUID int64, artifactID string, tags []string) {
	if s.tags[agentUID] == nil {
		s.tags[agentUID] = map[string][]string{}
	}
	s.tags[agentUID][artifactID] = tags
}

func (s *artifactTagTestStore) ListAgentArtifactTags(agentUID int64, artifactIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	for _, id := range artifactIDs {
		if tags := s.tags[agentUID][id]; len(tags) > 0 {
			result[id] = append([]string{}, tags...)
		}
	}
	return result, nil
}

func (s *artifactTagTestStore) ListAgentArtifactTagCounts(agentUID int64) ([]store.AgentArtifactTagCount, error) {
	if s.countsErr != nil {
		return nil, s.countsErr
	}
	totals := map[string]int{}
	for _, artifacts := range s.tags[agentUID] {
		for _, tag := range artifacts {
			totals[tag]++
		}
	}
	counts := make([]store.AgentArtifactTagCount, 0, len(totals))
	for tag, count := range totals {
		counts = append(counts, store.AgentArtifactTagCount{Tag: tag, Count: count})
	}
	// Mirror the production adapter's ordering (count DESC, tag ASC) so the
	// fixture does not inherit Go map iteration randomness.
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count != counts[j].Count {
			return counts[i].Count > counts[j].Count
		}
		return counts[i].Tag < counts[j].Tag
	})
	return counts, nil
}

func (s *artifactTagTestStore) ReplaceAgentArtifactTags(agentUID int64, artifactID string, tags []string, createdBy int64) ([]string, error) {
	s.set(agentUID, artifactID, append([]string{}, tags...))
	s.storedBy[agentUID] = createdBy
	return append([]string{}, tags...), nil
}

func (s *artifactTagTestStore) DeleteAgentArtifactTag(agentUID int64, artifactID, tag string) (bool, error) {
	artifacts := s.tags[agentUID]
	current := artifacts[artifactID]
	for i, value := range current {
		if value == tag {
			next := append(append([]string{}, current[:i]...), current[i+1:]...)
			if len(next) == 0 {
				delete(artifacts, artifactID)
			} else {
				artifacts[artifactID] = next
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *artifactTagTestStore) PurgeAgentArtifactTags(agentUID int64, artifactID string) error {
	if n := s.purgeFailures[agentUID]; n > 0 {
		s.purgeFailures[agentUID] = n - 1
		return fmt.Errorf("transient purge failure (%d attempts remaining)", n)
	}
	delete(s.tags[agentUID], artifactID)
	return nil
}

func (s *artifactTagTestStore) ListAgentArtifactTagArtifactIDs(agentUID int64) ([]string, error) {
	ids := make([]string, 0, len(s.tags[agentUID]))
	for id := range s.tags[agentUID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func overTagLimitFixture() []string {
	values := make([]string, 0, maxAgentArtifactTagsPerArtifact+1)
	for i := 0; i <= maxAgentArtifactTagsPerArtifact; i++ {
		values = append(values, fmt.Sprintf("标签-%02d", i))
	}
	return values
}

func TestNormalizeAgentArtifactTags(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr error
	}{
		{
			name:  "trims collapses and dedupes while preserving order",
			input: []string{"  游戏 ", "演示", "游戏", "D  D", "\t网页\t"},
			want:  []string{"游戏", "演示", "D D", "网页"},
		},
		{
			name:  "drops empty entries",
			input: []string{"", "   ", "标签"},
			want:  []string{"标签"},
		},
		{
			name:  "accepts cjk letters digits and separators",
			input: []string{"阶段-1", "V_2", "demo.3", "中文标签"},
			want:  []string{"阶段-1", "V_2", "demo.3", "中文标签"},
		},
		{
			name:    "rejects path separators",
			input:   []string{"a/b"},
			wantErr: errAgentArtifactTagInvalid,
		},
		{
			name:    "rejects control characters",
			input:   []string{"tag\x07"},
			wantErr: errAgentArtifactTagInvalid,
		},
		{
			name:    "rejects overlong tags",
			input:   []string{strings.Repeat("长", maxAgentArtifactTagRunes+1)},
			wantErr: errAgentArtifactTagInvalid,
		},
		{
			name:    "rejects more tags than allowed",
			input:   overTagLimitFixture(),
			wantErr: errAgentArtifactTagLimit,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAgentArtifactTags(test.input)
			if test.wantErr != nil {
				if err != test.wantErr {
					t.Fatalf("err = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("tags = %#v, want %#v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("tags = %#v, want %#v", got, test.want)
				}
			}
		})
	}
}

func tagTestHandler(t *testing.T, tagStore *artifactTagTestStore) *CloudArtifactHandler {
	t.Helper()
	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		"https://upstream.test/internal/artifacts",
		"test-management-token-abcdefghijklmnopqrstuvwxyz",
		nil,
	)
	handler.SetStore(tagStore)
	return handler
}

// tagActiveArtifactsJSON builds a management-list response the same way
// managedAgentListJSONWithUploader does, but with caller-chosen artifact IDs,
// so tag-write tests can serve exactly the artifacts that should resolve.
func tagActiveArtifactsJSON(ids ...string) string {
	artifacts := make([]cloudArtifact, 0, len(ids))
	for _, id := range ids {
		artifacts = append(artifacts, cloudArtifact{
			ID:        id,
			Title:     "Artifact " + id,
			Kind:      "html",
			URL:       "https://example.test/by-agent/440/" + id + "/latest/",
			Status:    "active",
			CreatedAt: "2026-07-22T05:00:00.000Z",
			UpdatedAt: "2026-07-22T07:00:00.000Z",
			AgentUID:  "440",
		})
	}
	payload := cloudArtifactManagementList{
		ContractVersion: artifactManagementContract,
		Status:          "active",
		Count:           len(artifacts),
		Artifacts:       artifacts,
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

// newTagUpstream stubs the artifact management upstream with an active list
// containing exactly ids, so tag-write routing can resolve its targets.
func newTagUpstream(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tagActiveArtifactsJSON(ids...)))
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func tagTestHandlerWithUpstream(t *testing.T, tagStore *artifactTagTestStore, upstream *httptest.Server) *CloudArtifactHandler {
	t.Helper()
	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		"test-management-token-abcdefghijklmnopqrstuvwxyz",
		upstream.Client(),
	)
	handler.SetStore(tagStore)
	return handler
}

func ownerArtifactRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(context.WithValue(req.Context(), uidKey, int64(8)))
}

func TestAgentArtifactTagCollectionReturnsCounts(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "alpha", []string{"游戏", "演示"})
	tagStore.set(440, "beta", []string{"游戏"})
	handler := tagTestHandler(t, tagStore)

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, ownerArtifactRequest(http.MethodGet, "/api/agents/440/artifacts/tags"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response cloudArtifactTagCountsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Tags) != 2 {
		t.Fatalf("tags = %#v", response.Tags)
	}
	if response.Tags[0].Tag != "游戏" || response.Tags[0].Count != 2 {
		t.Fatalf("most used tag = %#v", response.Tags[0])
	}
}

func TestAgentArtifactTagCollectionAllowsMemberRead(t *testing.T) {
	handler := tagTestHandler(t, newArtifactTagTestStore())
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts/tags"))
	if rec.Code != http.StatusOK {
		t.Fatalf("member status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentArtifactTagCollectionRequiresGet(t *testing.T) {
	handler := tagTestHandler(t, newArtifactTagTestStore())
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, authenticatedArtifactRequestPath(http.MethodPost, "/api/agents/440/artifacts/tags"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentArtifactTagsReplaceAllowsFriendWrite(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := authenticatedArtifactRequestPath(http.MethodPut, "/api/agents/440/artifacts/alpha/tags")
	req.Body = io.NopCloser(strings.NewReader(`{"tags":["游戏"]}`))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("friend status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(tagStore.tags[440]["alpha"]) != 1 || tagStore.tags[440]["alpha"][0] != "游戏" {
		t.Fatalf("friend write tags = %#v", tagStore.tags[440])
	}
	if tagStore.storedBy[440] != 7 {
		t.Fatalf("created_by = %d, want friend uid 7", tagStore.storedBy[440])
	}
}

func TestAgentArtifactTagsReplaceRejectsUnknownArtifact(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := ownerArtifactRequest(http.MethodPut, "/api/agents/440/artifacts/does-not-exist/tags")
	req.Body = io.NopCloser(strings.NewReader(`{"tags":["游戏"]}`))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "artifact_not_found") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(tagStore.tags[440]) != 0 || tagStore.storedBy[440] != 0 {
		t.Fatalf("unknown artifact write changed store: tags=%#v storedBy=%d", tagStore.tags[440], tagStore.storedBy[440])
	}
}

func TestAgentArtifactTagDeleteRejectsUnknownArtifact(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := ownerArtifactRequest(http.MethodDelete, "/api/agents/440/artifacts/does-not-exist/tags/%E6%B8%B8%E6%88%8F")
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "artifact_not_found") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentArtifactTagDeleteAllowsFriendWrite(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "alpha", []string{"游戏", "演示"})
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := authenticatedArtifactRequestPath(http.MethodDelete, "/api/agents/440/artifacts/alpha/tags/%E6%B8%B8%E6%88%8F")
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("friend status = %d, body = %s", rec.Code, rec.Body.String())
	}
	remaining := tagStore.tags[440]["alpha"]
	if len(remaining) != 1 || remaining[0] != "演示" {
		t.Fatalf("remaining tags = %#v", remaining)
	}
}

func TestAgentArtifactTagsReplaceStoresNormalizedSet(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := ownerArtifactRequest(http.MethodPut, "/api/agents/440/artifacts/alpha/tags")
	req.Body = io.NopCloser(strings.NewReader(`{"tags":[" 游戏 ","演示","游戏"]}`))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response cloudArtifactTagsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Tags) != 2 || response.Tags[0] != "游戏" || response.Tags[1] != "演示" {
		t.Fatalf("tags = %#v", response.Tags)
	}
	if tagStore.storedBy[440] != 8 {
		t.Fatalf("created_by = %d", tagStore.storedBy[440])
	}
}

func TestAgentArtifactTagsReplaceRejectsInvalidPayload(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := ownerArtifactRequest(http.MethodPut, "/api/agents/440/artifacts/alpha/tags")
	req.Body = io.NopCloser(strings.NewReader(`{"tags":["a/b"]}`))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "artifact_tag_invalid") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentArtifactTagDeleteRemovesOneTag(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "alpha", []string{"游戏", "演示"})
	upstream := newTagUpstream(t, "alpha")
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	req := ownerArtifactRequest(http.MethodDelete, "/api/agents/440/artifacts/alpha/tags/%E6%B8%B8%E6%88%8F")
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	remaining := tagStore.tags[440]["alpha"]
	if len(remaining) != 1 || remaining[0] != "演示" {
		t.Fatalf("remaining tags = %#v", remaining)
	}
}

func TestAgentArtifactTagDeleteRejectsInvalidTagPath(t *testing.T) {
	handler := tagTestHandler(t, newArtifactTagTestStore())
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, authenticatedArtifactRequestPath(http.MethodDelete, "/api/agents/440/artifacts/alpha/tags/a%2Fb"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentArtifactDeletePurgesArtifactTags(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("status") == "active":
			_, _ = w.Write([]byte(tagActiveArtifactsJSON("alpha")))
		case r.Method == http.MethodDelete && r.URL.Path == "/internal/agents/440/artifacts/alpha":
			_, _ = w.Write([]byte(managedAgentOperationJSON("440", "alpha", "deleted")))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "alpha", []string{"游戏", "演示"})
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, ownerArtifactRequest(http.MethodDelete, "/api/agents/440/artifacts/alpha"))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(tagStore.tags[440]["alpha"]) != 0 {
		t.Fatalf("tags survived artifact delete: %#v", tagStore.tags[440]["alpha"])
	}

	// Purging an artifact without tags is an idempotent no-op.
	if err := tagStore.PurgeAgentArtifactTags(440, "beta"); err != nil {
		t.Fatalf("purge without tags: %v", err)
	}
}

func TestAgentArtifactListMergesAgentTags(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(managedAgentListJSON("440", "active")))
	}))
	defer upstream.Close()

	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "shared-game", []string{"游戏", "演示"})
	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.SetStore(tagStore)

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, ownerArtifactRequest(http.MethodGet, "/api/agents/440/artifacts?status=active"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response cloudArtifactManagementList
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v", response.Artifacts)
	}
	artifact := response.Artifacts[0]
	if len(artifact.Tags) != 2 || artifact.Tags[0] != "游戏" || artifact.Tags[1] != "演示" {
		t.Fatalf("merged tags = %#v", artifact.Tags)
	}
}

func TestParseAgentArtifactAPIPathTagRoutes(t *testing.T) {
	route, ok := parseAgentArtifactAPIPath("/api/agents/440/artifacts/tags")
	if !ok || route.action != "tag-collection" || route.agentUID != 440 {
		t.Fatalf("collection route = %#v ok=%v", route, ok)
	}
	route, ok = parseAgentArtifactAPIPath("/api/agents/440/artifacts/alpha/tags")
	if !ok || route.action != "tags" || route.artifactID != "alpha" {
		t.Fatalf("artifact tags route = %#v ok=%v", route, ok)
	}
	route, ok = parseAgentArtifactAPIPath("/api/agents/440/artifacts/alpha/tags/%E6%B8%B8%E6%88%8F")
	if !ok || route.action != "tag-delete" || route.tag != "游戏" {
		t.Fatalf("tag delete route = %#v ok=%v", route, ok)
	}
}

func TestAgentArtifactDeletePurgeRetriesTransientFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("status") == "active":
			_, _ = w.Write([]byte(tagActiveArtifactsJSON("alpha")))
		case r.Method == http.MethodDelete && r.URL.Path == "/internal/agents/440/artifacts/alpha":
			_, _ = w.Write([]byte(managedAgentOperationJSON("440", "alpha", "deleted")))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	tagStore := newArtifactTagTestStore()
	tagStore.purgeFailures[440] = 1
	tagStore.set(440, "alpha", []string{"游戏"})
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, ownerArtifactRequest(http.MethodDelete, "/api/agents/440/artifacts/alpha"))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, exists := tagStore.tags[440]["alpha"]; exists {
		t.Fatalf("tags survived purge after retry: %#v", tagStore.tags[440])
	}
}

func TestAgentArtifactManagedListReconcilesOrphanTags(t *testing.T) {
	upstream := newTagUpstream(t, "alpha")
	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "alpha", []string{"游戏"})
	tagStore.set(440, "ghost", []string{"过期"})
	handler := tagTestHandlerWithUpstream(t, tagStore, upstream)

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(rec, ownerArtifactRequest(http.MethodGet, "/api/agents/440/artifacts?status=active"))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, exists := tagStore.tags[440]["ghost"]; exists {
		t.Fatalf("orphan tags survived reconciliation: %#v", tagStore.tags[440])
	}
	remaining := tagStore.tags[440]["alpha"]
	if len(remaining) != 1 || remaining[0] != "游戏" {
		t.Fatalf("active artifact tags = %#v", remaining)
	}
}
