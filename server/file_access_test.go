package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const testPrivateFile = "20261109_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.txt"
const testOtherFile = "20261109_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.txt"
const testPrivateURL = "/uploads/files/" + testPrivateFile
const testPrivateKey = "upload:files/" + testPrivateFile

type fileAccessMemoryStore struct {
	store.FileAccessStore
	uploads  map[string]*store.FileUploadRecord
	messages map[int64]*types.Message
	bindings map[string][]store.FileMessageBinding
	err      error
}

func newFileAccessMemoryStore() *fileAccessMemoryStore {
	return &fileAccessMemoryStore{uploads: map[string]*store.FileUploadRecord{}, messages: map[int64]*types.Message{}, bindings: map[string][]store.FileMessageBinding{}}
}
func (db *fileAccessMemoryStore) RecordFileUpload(_ context.Context, key string, uid int64) error {
	if old := db.uploads[key]; old != nil {
		if old.UploaderUID != uid {
			return errors.New("owner conflict")
		}
		return db.err
	}
	db.uploads[key] = &store.FileUploadRecord{ObjectKey: key, UploaderUID: uid}
	return db.err
}
func (db *fileAccessMemoryStore) GetFileUpload(_ context.Context, key string) (*store.FileUploadRecord, error) {
	return db.uploads[key], db.err
}
func (db *fileAccessMemoryStore) ListFileMessageBindings(_ context.Context, key string) ([]store.FileMessageBinding, error) {
	return db.bindings[key], db.err
}
func (db *fileAccessMemoryStore) GetFileAccessMessage(_ context.Context, id int64) (*types.Message, error) {
	return db.messages[id], db.err
}
func (db *fileAccessMemoryStore) BindMessageFiles(_ context.Context, id int64, topic string, keys []string) error {
	if db.err != nil {
		return db.err
	}
	message := db.messages[id]
	if message == nil || message.TopicID != topic {
		return errors.New("missing message")
	}
	for _, key := range keys {
		if upload := db.uploads[key]; upload != nil {
			upload.Bound = true
		}
		exists := false
		for _, b := range db.bindings[key] {
			if b.MessageID == id {
				exists = true
			}
		}
		if !exists {
			db.bindings[key] = append(db.bindings[key], store.FileMessageBinding{ResourceKey: key, MessageID: id, TopicID: topic})
		}
	}
	return nil
}
func (db *fileAccessMemoryStore) ListFileBackfillMessages(_ context.Context, after int64, limit int) ([]*types.Message, error) {
	out := []*types.Message{}
	for id := after + 1; id <= 20 && len(out) < limit; id++ {
		if m := db.messages[id]; m != nil {
			out = append(out, m)
		}
	}
	return out, db.err
}
func fileAccessMessage(id int64, topic string) *types.Message {
	return &types.Message{ID: id, TopicID: topic, MsgType: "file", Content: `{"url":"` + testPrivateURL + `","name":"private.txt"}`}
}
func TestFileAccessCanonicalResources(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.example")
	t.Setenv("CATSCO_FILE_TRUSTED_ORIGINS", "")
	for _, raw := range []string{testPrivateURL, "https://app.example" + testPrivateURL + "?preview=1", testPrivateKey} {
		if got := canonicalFileResource(raw); got != testPrivateKey {
			t.Fatalf("%q => %q", raw, got)
		}
	}
	for _, raw := range []string{"https://evil.example" + testPrivateURL, "//app.example" + testPrivateURL, "https://app.example@evil.example" + testPrivateURL, "/uploads/files/../files/" + testPrivateFile, "/uploads/files/%32" + testPrivateFile[1:], "/uploads/files/%2f" + testPrivateFile, "/uploads/files/a%252fb.txt", "/uploads/files/a\\b.txt", "/uploads/tutorial/a.pdf"} {
		if got := canonicalFileResource(raw); got != "" {
			t.Fatalf("unsafe %q => %q", raw, got)
		}
	}
	message := fileAccessMessage(1, "grp1")
	thumb := "/uploads/images/20261109_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"
	message.ContentBlocks = []types.ContentBlock{{Type: "file", Payload: map[string]interface{}{"url": testPrivateURL, "thumbnail": thumb}}}
	want := []string{testPrivateKey, "upload:images/20261109_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}
	if got := MessageFileResources(message); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	message = &types.Message{MsgType: "text", Content: `"see ` + testPrivateURL + `"`}
	if got := MessageFileResources(message); len(got) != 0 {
		t.Fatalf("text manufactured attachment: %v", got)
	}
	message = fileAccessMessage(1, "grp1")
	encoded, _ := json.Marshal(message.Content)
	message.Content = string(encoded)
	if got := MessageFileResources(message); len(got) != 1 {
		t.Fatalf("legacy encoded payload: %v", got)
	}
}
func TestFileAccessMessagePermissionsAndRepair(t *testing.T) {
	ctx := context.Background()
	db := newFileAccessMemoryStore()
	key := testPrivateKey
	readable := map[int64]map[string]bool{1: {"grp1": true}, 2: {"grp1": true}, 3: {"grp2": true}}
	service := &FileAccessService{files: db, readable: func(uid int64, m *types.Message) bool { return readable[uid][m.TopicID] }}
	db.messages[1] = fileAccessMessage(1, "grp1")
	db.messages[2] = fileAccessMessage(2, "grp2")
	if service.CanReadResource(ctx, 0, key, 1) || service.CanReadResource(ctx, 3, key, 1) {
		t.Fatal("anonymous or unrelated actor read historical file")
	}
	if len(db.bindings[key]) != 0 || db.uploads[key] != nil {
		t.Fatal("requester claimed ownership")
	}
	if !service.CanReadResource(ctx, 2, key, 1) || !service.CanReadResource(ctx, 1, key, 0) {
		t.Fatal("message reader denied")
	}
	readable[1]["grp1"] = false
	if service.CanReadResource(ctx, 1, key, 0) {
		t.Fatal("removed reader kept access")
	}
	db.BindMessageFiles(ctx, 2, "grp2", []string{key})
	if !service.CanReadResource(ctx, 3, key, 0) {
		t.Fatal("second real message did not grant access")
	}
	db.messages[2].Content = `{"url":"/uploads/files/` + testOtherFile + `"}`
	if service.CanReadResource(ctx, 3, key, 0) {
		t.Fatal("stale index granted access")
	}
	db.err = errors.New("database unavailable")
	if service.CanReadResource(ctx, 2, key, 1) {
		t.Fatal("database failure granted access")
	}
}
func TestFileAccessDraftAndBackfill(t *testing.T) {
	ctx := context.Background()
	db := newFileAccessMemoryStore()
	key := testPrivateKey
	service := &FileAccessService{files: db, readable: func(int64, *types.Message) bool { return false }}
	if err := service.RecordUpload(ctx, 1, key); err != nil {
		t.Fatal(err)
	}
	if !service.CanReadResource(ctx, 1, key, 0) || service.CanReadResource(ctx, 2, key, 0) {
		t.Fatal("draft ownership not enforced")
	}
	if err := service.RecordUpload(ctx, 2, key); err == nil {
		t.Fatal("ownership overwritten")
	}
	db.messages[1] = fileAccessMessage(1, "grp1")
	db.messages[3] = fileAccessMessage(3, "grp2")
	result, err := service.Backfill(ctx, 0, 1, true)
	if err != nil || result.NextAfterID != 1 || result.References != 1 || !result.HasMore || len(db.bindings[key]) != 0 {
		t.Fatalf("dry-run: %+v %v", result, err)
	}
	result, err = service.Backfill(ctx, 0, 10, false)
	if err != nil || result.NextAfterID != 3 || len(db.bindings[key]) != 2 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	if _, err = service.Backfill(ctx, 0, 10, false); err != nil || len(db.bindings[key]) != 2 {
		t.Fatal("retry was not idempotent")
	}
	if service.CanReadResource(ctx, 1, key, 0) {
		t.Fatal("uploader retained draft override after binding")
	}
	delete(db.bindings, key)
	delete(db.messages, 1)
	delete(db.messages, 3)
	if err := service.RecordUpload(ctx, 1, key); err != nil {
		t.Fatal(err)
	}
	if service.CanReadResource(ctx, 1, key, 0) {
		t.Fatal("deleting the last message resurrected draft access")
	}
}
func TestFileAccessUploadReadVariants(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", testPrivateFile), []byte("private bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	db := newFileAccessMemoryStore()
	db.messages[1] = fileAccessMessage(1, "grp1")
	db.BindMessageFiles(context.Background(), 1, "grp1", []string{testPrivateKey})
	service := &FileAccessService{files: db, readable: func(uid int64, _ *types.Message) bool { return uid == 1 }}
	handler := NewUploadHandler(dir, "/uploads")
	handler.SetFileAccess(service)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, query := range []string{"", "?preview=1", "?download=1"} {
			for _, uid := range []int64{0, 1, 2} {
				req := httptest.NewRequest(method, testPrivateURL+query, nil)
				req = req.WithContext(context.WithValue(req.Context(), uidKey, uid))
				req.Header.Set("Range", "bytes=0-2")
				res := httptest.NewRecorder()
				handler.HandleServeFile(res, req)
				if uid != 1 && res.Code != http.StatusNotFound {
					t.Fatalf("%s %s uid=%d leaked %d", method, query, uid, res.Code)
				}
				if uid == 1 && query != "?preview=1" && res.Code != http.StatusPartialContent {
					t.Fatalf("authorized %s %s: %d", method, query, res.Code)
				}
				if res.Header().Get("Cache-Control") == "public, max-age=300" {
					t.Fatal("private resource publicly cached")
				}
			}
		}
	}
}
