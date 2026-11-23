package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type fileVersionTestStore struct {
	*fileAnnotationBrowserStore
	mu       sync.Mutex
	bindings map[string]*store.FileVersionBinding
	fail     bool
}

func fileVersionBindingKey(topicID string, messageID, attachmentIndex int64) string {
	return topicID + ":" + strconv.FormatInt(messageID, 10) + ":" + strconv.FormatInt(attachmentIndex, 10)
}

func (s *fileVersionTestStore) UpsertFileVersionBinding(_ context.Context, binding *store.FileVersionBinding) (*store.FileVersionBinding, error) {
	if s.fail {
		return nil, errors.New("persistence unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = make(map[string]*store.FileVersionBinding)
	}
	key := fileVersionBindingKey(binding.TopicID, binding.SourceMessageID, binding.SourceAttachmentIndex)
	now := time.Now().UTC()
	copy := *binding
	if existing := s.bindings[key]; existing != nil {
		copy.ID = existing.ID
		copy.CreatedAt = existing.CreatedAt
	} else {
		copy.ID = int64(len(s.bindings) + 1)
		copy.CreatedAt = now
	}
	copy.UpdatedAt = now
	s.bindings[key] = &copy
	result := copy
	return &result, nil
}

func (s *fileVersionTestStore) GetFileVersionBinding(_ context.Context, topicID string, messageID, attachmentIndex int64) (*store.FileVersionBinding, error) {
	if s.fail {
		return nil, errors.New("persistence unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[fileVersionBindingKey(topicID, messageID, attachmentIndex)]
	if binding == nil {
		return nil, nil
	}
	copy := *binding
	return &copy, nil
}

type fileVersionFixture struct {
	parent *openBindingFixture
	db     *fileVersionTestStore
	server *httptest.Server
	token  string
	other  string
	source fileAnnotationSource
}

func newFileVersionFixture(t *testing.T) *fileVersionFixture {
	t.Helper()
	parent := newOpenBindingFixture(t)
	db := &fileVersionTestStore{
		fileAnnotationBrowserStore: &fileAnnotationBrowserStore{parent.db},
		bindings:                   make(map[string]*store.FileVersionBinding),
	}
	payload := map[string]interface{}{
		"name": "source.pptx", "url": "/uploads/files/source.pptx", "file_key": "source.pptx",
		"mime_type": "application/vnd.openxmlformats-officedocument.presentationml.presentation", "size": 4096,
	}
	message := &types.Message{
		ID: 1, TopicID: "p2p_7_9", FromUID: 9, MsgType: "file", Content: "source",
		ContentBlocks: []types.ContentBlock{{Type: "file", Payload: payload}},
		CreatedAt:     time.Now().UTC(),
	}
	parent.db.durable = append(parent.db.durable, message)
	source, ok := canonicalFileAnnotationSource(message, 0)
	if !ok {
		t.Fatal("fixture source attachment is not canonical")
	}
	hub := NewHub(db, nil)
	handler := NewFileVersionBindingHandler(hub)
	mux := http.NewServeMux()
	auth := JWTAuthMiddlewareWithDB(db)
	mux.HandleFunc("POST /api/files/version-bindings", auth(handler.Handle))
	mux.HandleFunc("GET /api/files/version-bindings", auth(handler.Handle))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &fileVersionFixture{
		parent: parent, db: db, server: server, token: parent.token,
		other: parent.otherActor, source: source,
	}
}

func fileVersionRequestBody(source fileAnnotationSource) map[string]interface{} {
	return map[string]interface{}{
		"topic_id": source.TopicID, "source_message_id": source.MessageID,
		"source_attachment_index": source.AttachmentIndex, "source_file_key": source.FileKey,
		"latest_file_key": "latest.pptx", "latest_url": "/uploads/files/latest.pptx",
		"latest_name": "latest.pptx", "latest_size": 8192,
		"latest_mime_type": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	}
}

func (f *fileVersionFixture) request(t *testing.T, method, path, token string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]interface{}
	_ = json.NewDecoder(response.Body).Decode(&result)
	return response.StatusCode, result
}

func TestFileVersionBindingValidUpsert(t *testing.T) {
	f := newFileVersionFixture(t)
	status, body := f.request(t, http.MethodPost, "/api/files/version-bindings", f.token, fileVersionRequestBody(f.source))
	if status != http.StatusOK || body["latest_file_key"] != "latest.pptx" || body["source_file_key"] != "source.pptx" {
		t.Fatalf("valid upsert status=%d body=%#v", status, body)
	}
}

func TestFileVersionBindingGETHit(t *testing.T) {
	f := newFileVersionFixture(t)
	if status, body := f.request(t, http.MethodPost, "/api/files/version-bindings", f.token, fileVersionRequestBody(f.source)); status != http.StatusOK {
		t.Fatalf("seed upsert status=%d body=%#v", status, body)
	}
	path := "/api/files/version-bindings?topic_id=p2p_7_9&source_message_id=1&source_attachment_index=0"
	status, body := f.request(t, http.MethodGet, path, f.token, nil)
	if status != http.StatusOK || body["latest_url"] != "/uploads/files/latest.pptx" || body["latest_name"] != "latest.pptx" {
		t.Fatalf("GET hit status=%d body=%#v", status, body)
	}
}

func TestFileVersionBindingRejectsSourceMismatch(t *testing.T) {
	f := newFileVersionFixture(t)
	request := fileVersionRequestBody(f.source)
	request["source_file_key"] = "different.pptx"
	status, body := f.request(t, http.MethodPost, "/api/files/version-bindings", f.token, request)
	if status != http.StatusConflict || body["error"] != "file_source_key_mismatch" {
		t.Fatalf("source mismatch status=%d body=%#v", status, body)
	}
	if len(f.db.bindings) != 0 {
		t.Fatalf("source mismatch wrote %d bindings", len(f.db.bindings))
	}
}

func TestFileVersionBindingRejectsUnauthorized(t *testing.T) {
	f := newFileVersionFixture(t)
	status, body := f.request(t, http.MethodPost, "/api/files/version-bindings", f.other, fileVersionRequestBody(f.source))
	if status != http.StatusBadRequest && status != http.StatusForbidden {
		t.Fatalf("unauthorized status=%d body=%#v", status, body)
	}
	if len(f.db.bindings) != 0 {
		t.Fatalf("unauthorized write created %d bindings", len(f.db.bindings))
	}
}

func TestFileVersionBindingRejectsNonOfficeURL(t *testing.T) {
	f := newFileVersionFixture(t)
	request := fileVersionRequestBody(f.source)
	request["latest_file_key"] = "latest.pdf"
	request["latest_url"] = "/uploads/files/latest.pdf"
	request["latest_name"] = "latest.pdf"
	request["latest_mime_type"] = "application/pdf"
	status, body := f.request(t, http.MethodPost, "/api/files/version-bindings", f.token, request)
	if status != http.StatusBadRequest || body["error"] != "file_version_fields_invalid" {
		t.Fatalf("non-office status=%d body=%#v", status, body)
	}
}

func TestFileVersionBindingStoreUnavailable(t *testing.T) {
	parent := newOpenBindingFixture(t)
	hub := NewHub(parent.db, nil)
	handler := NewFileVersionBindingHandler(hub)
	server := httptest.NewServer(http.HandlerFunc(JWTAuthMiddlewareWithDB(parent.db)(handler.Handle)))
	defer server.Close()
	raw, err := json.Marshal(fileVersionRequestBody(fileAnnotationSource{TopicID: "p2p_7_9", MessageID: 1, AttachmentIndex: 0, FileKey: "source.pptx"}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+parent.token)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]interface{}
	_ = json.NewDecoder(response.Body).Decode(&body)
	if response.StatusCode != http.StatusServiceUnavailable || body["error"] != "file_version_binding_unavailable" {
		t.Fatalf("store unavailable status=%d body=%#v", response.StatusCode, body)
	}
}
