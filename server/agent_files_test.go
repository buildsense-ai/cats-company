package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type agentFileTestStore struct {
	*agentTestStore
	messages         []*types.Message
	titles           map[string]string
	queriedAgentUID  int64
	queriedTopicIDs  []string
	queriedBeforeID  int64
	queriedLimit     int
	fileMessageCalls int
}

func (s *agentFileTestStore) ListAgentFileMessages(agentUID int64, topicIDs []string, beforeID int64, limit int) ([]*types.Message, error) {
	s.fileMessageCalls++
	s.queriedAgentUID = agentUID
	s.queriedTopicIDs = append([]string(nil), topicIDs...)
	s.queriedBeforeID = beforeID
	s.queriedLimit = limit
	allowed := make(map[string]struct{}, len(topicIDs))
	for _, topicID := range topicIDs {
		allowed[topicID] = struct{}{}
	}
	result := make([]*types.Message, 0, limit)
	for _, message := range s.messages {
		if message == nil || message.FromUID != agentUID {
			continue
		}
		if _, ok := allowed[message.TopicID]; !ok {
			continue
		}
		if beforeID > 0 && message.ID >= beforeID {
			continue
		}
		result = append(result, message)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *agentFileTestStore) GetConversationTitles(_ int64, topicIDs []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, topicID := range topicIDs {
		if title := s.titles[topicID]; title != "" {
			result[topicID] = title
		}
	}
	return result, nil
}

func (s *agentFileTestStore) UpdateConversationTitle(_ int64, _, _ string) (bool, error) {
	return false, nil
}

func TestAgentFilesListsStructuredAndLegacyHistoryFromAccessibleTopics(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	fileStore := newAgentFileTestStore(7, 440)
	fileStore.groupsByUser[7] = []*types.Group{{ID: 80, Name: "教研组", Kind: types.GroupKindStandard}}
	fileStore.titles = map[string]string{"p2p_7_440": "期末材料"}
	fileStore.messages = []*types.Message{
		{
			ID: 12, TopicID: "p2p_7_440", FromUID: 440, CreatedAt: now,
			ContentBlocks: []types.ContentBlock{{
				Type: "file",
				Payload: map[string]interface{}{
					"name": "学情报告.pdf", "url": "/uploads/files/report.pdf",
					"mime_type": "application/pdf", "size": float64(728341),
				},
			}},
		},
		{
			ID: 11, TopicID: "grp_80", FromUID: 440, CreatedAt: now.Add(-time.Hour),
			MsgType: "file",
			Content: `{"type":"file","payload":{"file_name":"练习题.xlsx","file_key":"files/exercise.xlsx","content_type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","file_size":2048}}`,
		},
		{
			ID: 10, TopicID: "grp_99", FromUID: 440, CreatedAt: now.Add(-2 * time.Hour),
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "不可见.pdf", "url": "/uploads/files/hidden.pdf"},
			}},
		},
	}

	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?limit=20"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		AgentUID int64             `json:"agent_uid"`
		Files    []agentFileRecord `json:"files"`
		HasMore  bool              `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AgentUID != 440 || response.HasMore || len(response.Files) != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Files[0].Name != "学情报告.pdf" || response.Files[0].TopicName != "期末材料" {
		t.Fatalf("structured file = %+v", response.Files[0])
	}
	if response.Files[1].Name != "练习题.xlsx" ||
		response.Files[1].URL != "/uploads/files/exercise.xlsx" ||
		response.Files[1].TopicName != "教研组" {
		t.Fatalf("legacy file = %+v", response.Files[1])
	}
	if fileStore.queriedAgentUID != 440 ||
		!reflect.DeepEqual(fileStore.queriedTopicIDs, []string{"p2p_7_440", "grp_80"}) ||
		fileStore.queriedLimit != 21 {
		t.Fatalf("query = agent %d topics %v limit %d", fileStore.queriedAgentUID, fileStore.queriedTopicIDs, fileStore.queriedLimit)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestAgentFilesUsesStableMessageCursor(t *testing.T) {
	fileStore := newAgentFileTestStore(7, 440)
	for _, id := range []int64{12, 11, 10, 9} {
		fileStore.messages = append(fileStore.messages, &types.Message{
			ID: id, TopicID: "p2p_7_440", FromUID: 440, CreatedAt: time.Unix(id, 0),
			ContentBlocks: []types.ContentBlock{{
				Type:    "file",
				Payload: map[string]interface{}{"name": "file.pdf", "url": "/uploads/files/" + string(rune('a'+id)) + ".pdf"},
			}},
		})
	}
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)

	first := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		first,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?limit=2"),
	)
	var firstPage struct {
		Files        []agentFileRecord `json:"files"`
		HasMore      bool              `json:"has_more"`
		NextBeforeID int64             `json:"next_before_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if first.Code != http.StatusOK || !firstPage.HasMore || firstPage.NextBeforeID != 11 || len(firstPage.Files) != 2 {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		second,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?limit=2&before_id=11"),
	)
	var secondPage struct {
		Files   []agentFileRecord `json:"files"`
		HasMore bool              `json:"has_more"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if second.Code != http.StatusOK || secondPage.HasMore || len(secondPage.Files) != 2 {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	if fileStore.queriedBeforeID != 11 {
		t.Fatalf("before id = %d", fileStore.queriedBeforeID)
	}
}

func TestAgentFilesRejectsInaccessibleAgentBeforeQuery(t *testing.T) {
	fileStore := newAgentFileTestStore(8, 440)
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files"),
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fileStore.fileMessageCalls != 0 {
		t.Fatalf("file query calls = %d", fileStore.fileMessageCalls)
	}
}

func TestAgentFilesRejectsMutationMethods(t *testing.T) {
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(newAgentFileTestStore(7, 440))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodDelete, "/api/agents/440/files"),
	)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("status = %d allow = %q body = %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
}

func newAgentFileTestStore(ownerUID, agentUID int64) *agentFileTestStore {
	base := managedArtifactAgentStore(ownerUID, agentUID, false)
	if base.groupsByUser == nil {
		base.groupsByUser = make(map[int64][]*types.Group)
	}
	return &agentFileTestStore{
		agentTestStore: base,
		titles:         make(map[string]string),
	}
}
