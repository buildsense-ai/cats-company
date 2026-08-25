package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type agentFileTestStore struct {
	*agentTestStore
	messages         []*types.Message
	titles           map[string]string
	queriedAgentUID  int64
	queriedTopicID   string
	queriedBeforeID  int64
	queriedLimit     int
	fileMessageCalls int
}

func (s *agentFileTestStore) ListAgentFileMessages(agentUID int64, topicID string, beforeID int64, limit int) ([]*types.Message, error) {
	s.fileMessageCalls++
	s.queriedAgentUID = agentUID
	s.queriedTopicID = topicID
	s.queriedBeforeID = beforeID
	s.queriedLimit = limit
	result := make([]*types.Message, 0, limit)
	for _, message := range s.messages {
		if message == nil || message.FromUID != agentUID {
			continue
		}
		if message.TopicID != topicID {
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

func (s *agentFileTestStore) ListTopicFileMessages(topicID string, beforeID int64, limit int) ([]*types.Message, error) {
	s.fileMessageCalls++
	s.queriedTopicID = topicID
	s.queriedBeforeID = beforeID
	s.queriedLimit = limit
	result := make([]*types.Message, 0, limit)
	for _, message := range s.messages {
		if message == nil || message.TopicID != topicID {
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

func TestAgentFilesListsOnlyCurrentPrivateConversation(t *testing.T) {
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
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=p2p_7_440&limit=20"),
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
	if response.AgentUID != 440 || response.HasMore || len(response.Files) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Files[0].Name != "学情报告.pdf" || response.Files[0].TopicName != "期末材料" {
		t.Fatalf("structured file = %+v", response.Files[0])
	}
	if fileStore.queriedAgentUID != 440 ||
		fileStore.queriedTopicID != "p2p_7_440" ||
		fileStore.queriedLimit != 21 {
		t.Fatalf("query = agent %d topic %q limit %d", fileStore.queriedAgentUID, fileStore.queriedTopicID, fileStore.queriedLimit)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestAgentFilesListsOnlyCurrentGroupConversation(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	fileStore := newAgentFileTestStore(7, 440)
	fileStore.groupMembers = map[string]bool{groupMemberKey(80, 7): true}
	fileStore.groupsByUser[7] = []*types.Group{{ID: 80, Name: "教研组", Kind: types.GroupKindStandard}}
	fileStore.messages = []*types.Message{
		{
			ID: 12, TopicID: "p2p_7_440", FromUID: 440, CreatedAt: now,
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "私聊报告.pdf", "url": "/uploads/files/private.pdf"},
			}},
		},
		{
			ID: 11, TopicID: "grp_80", FromUID: 440, CreatedAt: now.Add(-time.Hour),
			MsgType: "file",
			Content: `{"type":"file","payload":{"file_name":"练习题.xlsx","file_key":"files/exercise.xlsx","content_type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","file_size":2048}}`,
		},
		{
			ID: 10, TopicID: "grp_81", FromUID: 440, CreatedAt: now.Add(-2 * time.Hour),
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "其他群.pdf", "url": "/uploads/files/other-group.pdf"},
			}},
		},
	}

	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=grp_80"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		TopicID string            `json:"topic_id"`
		Files   []agentFileRecord `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.TopicID != "grp_80" || len(response.Files) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Files[0].Name != "练习题.xlsx" || response.Files[0].TopicName != "教研组" {
		t.Fatalf("group file = %+v", response.Files[0])
	}
	if fileStore.queriedTopicID != "grp_80" {
		t.Fatalf("queried topic = %q", fileStore.queriedTopicID)
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
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=p2p_7_440&limit=2"),
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
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=p2p_7_440&limit=2&before_id=11"),
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
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=p2p_8_440"),
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fileStore.fileMessageCalls != 0 {
		t.Fatalf("file query calls = %d", fileStore.fileMessageCalls)
	}
}

func TestAgentFilesRequiresCurrentTopic(t *testing.T) {
	fileStore := newAgentFileTestStore(7, 440)
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files"),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fileStore.fileMessageCalls != 0 {
		t.Fatalf("file query calls = %d", fileStore.fileMessageCalls)
	}
}

func TestAgentFilesRejectsAnotherPrivateConversation(t *testing.T) {
	fileStore := newAgentFileTestStore(7, 440)
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=p2p_8_440"),
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fileStore.fileMessageCalls != 0 {
		t.Fatalf("file query calls = %d", fileStore.fileMessageCalls)
	}
}

func TestAgentFilesRejectsGroupOutsideViewerMembership(t *testing.T) {
	fileStore := newAgentFileTestStore(7, 440)
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/files?topic_id=grp_80"),
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

func TestTopicFilesListsFilesFromEverySenderInCurrentConversation(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	fileStore := newAgentFileTestStore(7, 440)
	fileStore.titles = map[string]string{"p2p_7_440": "项目材料"}
	fileStore.messages = []*types.Message{
		{
			ID: 14, TopicID: "p2p_7_440", FromUID: 7, CreatedAt: now,
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "用户说明.docx", "url": "/uploads/files/user.docx"},
			}},
		},
		{
			ID: 13, TopicID: "p2p_7_440", FromUID: 440, CreatedAt: now.Add(-time.Minute),
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "Agent 报告.pdf", "url": "/uploads/files/agent.pdf"},
			}},
		},
		{
			ID: 12, TopicID: "p2p_8_440", FromUID: 440, CreatedAt: now.Add(-2 * time.Minute),
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "其他聊天.pdf", "url": "/uploads/files/other.pdf"},
			}},
		},
	}

	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleTopicFiles(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/topics/p2p_7_440/files?limit=20"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		TopicID   string            `json:"topic_id"`
		TopicName string            `json:"topic_name"`
		Files     []agentFileRecord `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.TopicID != "p2p_7_440" || response.TopicName != "项目材料" || len(response.Files) != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Files[0].Name != "用户说明.docx" || response.Files[1].Name != "Agent 报告.pdf" {
		t.Fatalf("files = %+v", response.Files)
	}
	if fileStore.queriedTopicID != "p2p_7_440" || fileStore.queriedLimit != 21 {
		t.Fatalf("query = topic %q limit %d", fileStore.queriedTopicID, fileStore.queriedLimit)
	}
}

func TestTopicFilesListsGroupFilesFromMembers(t *testing.T) {
	fileStore := newAgentFileTestStore(7, 440)
	fileStore.groupMembers = map[string]bool{groupMemberKey(80, 7): true}
	fileStore.groupsByUser[7] = []*types.Group{{ID: 80, Name: "教研组", Kind: types.GroupKindStandard}}
	fileStore.messages = []*types.Message{
		{
			ID: 14, TopicID: "grp_80", FromUID: 9, CreatedAt: time.Now(),
			ContentBlocks: []types.ContentBlock{{
				Type: "file", Payload: map[string]interface{}{"name": "成员资料.zip", "url": "/uploads/files/member.zip"},
			}},
		},
	}
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleTopicFiles(rec, authenticatedArtifactRequestPath(http.MethodGet, "/api/topics/grp_80/files"))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "成员资料.zip") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestTopicFilesIncludesImagesAndSortsByCreatedAt(t *testing.T) {
	base := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	fileStore := newAgentFileTestStore(7, 440)
	fileStore.titles = map[string]string{"p2p_7_440": "图片资料"}
	fileStore.messages = []*types.Message{
		{
			ID: 21, TopicID: "p2p_7_440", FromUID: 7, CreatedAt: base,
			ContentBlocks: []types.ContentBlock{{
				Type: "image",
				Payload: map[string]interface{}{
					"name": "较早照片.jpg", "url": "/uploads/images/older.jpg",
					"thumbnail": "/uploads/images/older-thumb.jpg", "mime_type": "image/jpeg",
					"width": float64(1200), "height": float64(800),
				},
			}},
		},
		{
			ID: 23, TopicID: "p2p_7_440", FromUID: 440, CreatedAt: base.Add(2 * time.Hour),
			MsgType: "image",
			Content: `{"type":"image","payload":{"name":"最新照片.png","url":"/uploads/images/latest.png","mime_type":"image/png"}}`,
		},
		{
			ID: 22, TopicID: "p2p_7_440", FromUID: 7, CreatedAt: base.Add(time.Hour),
			ContentBlocks: []types.ContentBlock{{
				Type:    "file",
				Payload: map[string]interface{}{"name": "中间报告.pdf", "url": "/uploads/files/report.pdf"},
			}},
		},
	}

	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)
	rec := httptest.NewRecorder()
	handler.HandleTopicFiles(rec, authenticatedArtifactRequestPath(http.MethodGet, "/api/topics/p2p_7_440/files?limit=20"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Files []agentFileRecord `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Files) != 3 {
		t.Fatalf("files = %+v, want 3 records", response.Files)
	}
	if response.Files[0].Name != "最新照片.png" || response.Files[0].Type != "image" {
		t.Fatalf("newest file = %+v", response.Files[0])
	}
	if response.Files[1].Name != "中间报告.pdf" || response.Files[1].Type != "file" {
		t.Fatalf("middle file = %+v", response.Files[1])
	}
	image := response.Files[2]
	if image.Name != "较早照片.jpg" || image.Type != "image" || image.Thumbnail != "/uploads/images/older-thumb.jpg" || image.Width != 1200 || image.Height != 800 {
		t.Fatalf("image metadata = %+v", image)
	}
}

func TestTopicFilesRejectsConversationOutsider(t *testing.T) {
	fileStore := newAgentFileTestStore(7, 440)
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(fileStore)

	for _, target := range []string{
		"/api/topics/p2p_8_440/files",
		"/api/topics/grp_80/files",
	} {
		rec := httptest.NewRecorder()
		handler.HandleTopicFiles(rec, authenticatedArtifactRequestPath(http.MethodGet, target))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("target %s status = %d, body = %s", target, rec.Code, rec.Body.String())
		}
	}
	if fileStore.fileMessageCalls != 0 {
		t.Fatalf("file query calls = %d", fileStore.fileMessageCalls)
	}
}

func TestTopicFilesRejectsInvalidPathAndMutation(t *testing.T) {
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	handler.SetStore(newAgentFileTestStore(7, 440))

	invalid := httptest.NewRecorder()
	handler.HandleTopicFiles(invalid, authenticatedArtifactRequestPath(http.MethodGet, "/api/topics/not-a-topic/files"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, body = %s", invalid.Code, invalid.Body.String())
	}

	mutation := httptest.NewRecorder()
	handler.HandleTopicFiles(mutation, authenticatedArtifactRequestPath(http.MethodDelete, "/api/topics/p2p_7_440/files"))
	if mutation.Code != http.StatusMethodNotAllowed || mutation.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("mutation status = %d allow = %q", mutation.Code, mutation.Header().Get("Allow"))
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
