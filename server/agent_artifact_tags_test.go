package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type artifactTagTestStore struct {
	*agentTestStore
	tags      map[int64]map[string][]string
	storedBy  map[int64]int64
	countsErr error
}

func newArtifactTagTestStore() *artifactTagTestStore {
	fixture := &artifactTagTestStore{
		agentTestStore: managedArtifactAgentStore(8, 440, true),
		tags:           map[int64]map[string][]string{},
		storedBy:       map[int64]int64{},
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
	handler := tagTestHandler(t, tagStore)

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
}

func TestAgentArtifactTagDeleteAllowsFriendWrite(t *testing.T) {
	tagStore := newArtifactTagTestStore()
	tagStore.set(440, "alpha", []string{"游戏", "演示"})
	handler := tagTestHandler(t, tagStore)

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
	handler := tagTestHandler(t, tagStore)

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
	handler := tagTestHandler(t, tagStore)

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
	handler := tagTestHandler(t, tagStore)

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
