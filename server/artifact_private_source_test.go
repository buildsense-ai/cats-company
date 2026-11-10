package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type artifactSourceTestStore struct {
	*fileAccessMemoryStore
	sessions *fileMediaTestStore
}

func (s *artifactSourceTestStore) CreateFileMediaSession(ctx context.Context, r store.FileMediaSession) error {
	return s.sessions.CreateFileMediaSession(ctx, r)
}
func TestArtifactPrivateSourceTransport(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://chat.example")
	db, a, parent, _ := newFileMediaTest(t)
	files := &artifactSourceTestStore{fileAccessMemoryStore: newFileAccessMemoryStore(), sessions: db}
	if err := files.RecordFileUpload(context.Background(), testPrivateKey, 7); err != nil {
		t.Fatal(err)
	}
	access := &FileAccessService{files: files, readable: func(uid int64, _ *types.Message) bool { return uid == 7 }}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "files"), 0700)
	os.WriteFile(filepath.Join(dir, "files", testPrivateFile), []byte("source bytes"), 0600)
	uploads := NewUploadHandler(dir, "/uploads")
	uploads.SetFileAccess(access)
	req := httptest.NewRequest("POST", "https://chat.example/api/agents/9/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+parent)
	signed, err := uploads.PrivateArtifactSourceURL(req, 7, "https://chat.example"+testPrivateURL)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(signed)
	token := target.Query().Get("publish_token")
	if token == "" {
		t.Fatal("missing scoped source token")
	}
	record := db.records[fileMediaHash(token)]
	if record.ActorUID != 7 || record.ParentHash != fileMediaHash(parent) || record.ExpiresAt.After(time.Now().Add(artifactSourceTTL)) {
		t.Fatalf("record: %+v", record)
	}
	read := func(method, path, auth string) int {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", auth)
		r.Header.Set("Range", "bytes=0-2")
		res := httptest.NewRecorder()
		a.UploadMiddleware(access, uploads.HandleServeFile)(res, r)
		return res.Code
	}
	for _, method := range []string{"GET", "HEAD"} {
		if status := read(method, signed, ""); status != 206 {
			t.Fatalf("source %s=%d", method, status)
		}
	}
	if status := read("GET", "https://chat.example/uploads/files/"+testOtherFile+"?publish_token="+url.QueryEscape(token), ""); status != 404 {
		t.Fatalf("cross-file=%d", status)
	}
	if status := read("GET", signed, "Bearer invalid"); status != 401 {
		t.Fatalf("invalid header downgraded=%d", status)
	}
	if status := read("POST", signed, ""); status != 405 {
		t.Fatalf("token mutation=%d", status)
	}
	if status, _ := fileMediaRead(a, "", &http.Cookie{Name: fileMediaCookieName, Value: token}); status != 401 {
		t.Fatalf("source token exchanged for broad cookie=%d", status)
	}
	record.ExpiresAt = time.Now().Add(-time.Second)
	db.records[record.TokenHash] = record
	if status := read("GET", signed, ""); status != 401 {
		t.Fatalf("expired=%d", status)
	}
	record.ExpiresAt = time.Now().Add(time.Minute)
	db.records[record.TokenHash] = record
	if rec := fileMediaMutate(a, "DELETE", "Bearer "+parent, "https://chat.example", nil); rec.Code != 204 {
		t.Fatalf("logout=%d", rec.Code)
	}
	if status := read("GET", signed, ""); status != 401 {
		t.Fatalf("revoked source=%d", status)
	}
	if _, err := uploads.PrivateArtifactSourceURL(req, 8, "https://chat.example"+testPrivateURL); err == nil {
		t.Fatal("foreign actor minted source token")
	}
	if _, err := uploads.PrivateArtifactSourceURL(req, 7, "https://evil.example"+testPrivateURL); err == nil {
		t.Fatal("external source accepted")
	}
}
