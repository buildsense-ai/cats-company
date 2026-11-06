package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memoryUploadStorage struct {
	objects map[string][]byte
}

func newMemoryUploadStorage() *memoryUploadStorage {
	return &memoryUploadStorage{objects: map[string][]byte{}}
}

func (s *memoryUploadStorage) Remote() bool { return true }

func (s *memoryUploadStorage) Put(_ context.Context, key, sourcePath string, size int64, wantSHA256, _ string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("unexpected object size")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != wantSHA256 {
		return errors.New("unexpected object digest")
	}
	s.objects[key] = data
	return nil
}

func (s *memoryUploadStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *memoryUploadStorage) Stat(_ context.Context, key string) (UploadObjectInfo, error) {
	data, ok := s.objects[key]
	if !ok {
		return UploadObjectInfo{}, os.ErrNotExist
	}
	return UploadObjectInfo{Size: int64(len(data))}, nil
}

func TestUploadStorageConfigurationDefaultsLocalAndRequiresZOSValues(t *testing.T) {
	for _, name := range []string{
		"CATSCO_UPLOAD_ZOS_ENDPOINT",
		"CATSCO_UPLOAD_ZOS_REGION",
		"CATSCO_UPLOAD_ZOS_BUCKET",
		"CATSCO_UPLOAD_ZOS_ACCESS_KEY_ID",
		"CATSCO_UPLOAD_ZOS_SECRET_ACCESS_KEY",
		"CATSCO_UPLOAD_ZOS_SECURITY_TOKEN",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("CATSCO_UPLOAD_STORAGE", "")
	storage, err := NewUploadStorageFromEnv(t.TempDir())
	if err != nil {
		t.Fatalf("default storage: %v", err)
	}
	if storage.Remote() {
		t.Fatal("default storage unexpectedly uses remote backend")
	}

	t.Setenv("CATSCO_UPLOAD_STORAGE", "invalid")
	if _, err := NewUploadStorageFromEnv(t.TempDir()); err == nil {
		t.Fatal("expected unsupported backend to fail")
	}
	t.Setenv("CATSCO_UPLOAD_STORAGE", "zos")
	if _, err := NewUploadStorageFromEnv(t.TempDir()); err == nil {
		t.Fatal("expected incomplete ZOS configuration to fail")
	}
}

func TestLocalUploadStorageRejectsParentDirectorySymlink(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "file.txt"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(baseDir, "files")); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}

	storage := NewLocalUploadStorage(baseDir)
	if _, err := storage.Open(context.Background(), "uploads/files/file.txt"); err == nil {
		t.Fatal("opened upload through a parent directory symlink")
	}
	if _, err := storage.Stat(context.Background(), "uploads/files/file.txt"); err == nil {
		t.Fatal("statted upload through a parent directory symlink")
	}
}

func TestTutorialUploadUsesSharedStorageAndRemainsReadableAtUploadsURL(t *testing.T) {
	baseDir := t.TempDir()
	storage := newMemoryUploadStorage()
	handler := NewTutorialTaskHandler(baseDir, "/uploads")
	handler.SetUploadStorage(storage)
	body, contentType := tutorialMultipartBody(t, "guide.pdf", []byte("tutorial attachment"))
	req := httptest.NewRequest(http.MethodPost, "/local/tutorial-admin/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	handler.HandleAdminUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		File tutorialTaskFile `json:"file"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !strings.HasPrefix(payload.File.URL, "/uploads/tutorial/") {
		t.Fatalf("tutorial URL = %q", payload.File.URL)
	}
	key := strings.TrimPrefix(payload.File.URL, "/")
	if got := string(storage.objects[key]); got != "tutorial attachment" {
		t.Fatalf("stored tutorial content = %q", got)
	}

	uploads := NewUploadHandler(baseDir, "/uploads")
	uploads.SetStorage(storage)
	getReq := httptest.NewRequest(http.MethodGet, payload.File.URL, nil)
	getRec := httptest.NewRecorder()
	uploads.HandleServeFile(getRec, getReq)
	if getRec.Code != http.StatusOK || getRec.Body.String() != "tutorial attachment" {
		t.Fatalf("tutorial read status=%d body=%q", getRec.Code, getRec.Body.String())
	}
	localPath := filepath.Join(baseDir, "tutorial", filepath.Base(payload.File.URL))
	localCopy, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read retained local tutorial copy: %v", err)
	}
	if string(localCopy) != string(storage.objects[key]) {
		t.Fatalf("local tutorial copy = %q, remote object = %q", localCopy, storage.objects[key])
	}
}

func TestMaterializeChannelOutboundAttachmentReadsRemoteAndCleansTemporaryFile(t *testing.T) {
	t.Setenv("CATSCO_UPLOAD_STAGING_DIR", t.TempDir())
	storage := newMemoryUploadStorage()
	key := "uploads/files/20261106_0123456789abcdef0123456789abcdef.txt"
	storage.objects[key] = []byte("outbound attachment")
	media, cleanup, err := materializeChannelOutboundAttachment(context.Background(), storage, t.TempDir(), channelOutboundAttachment{
		Type:     "file",
		Name:     "report.txt",
		URL:      "/" + key,
		Size:     int64(len(storage.objects[key])),
		MimeType: "text/plain",
	}, 1<<20)
	if err != nil {
		t.Fatalf("materialize outbound attachment: %v", err)
	}
	data, err := os.ReadFile(media.Path)
	if err != nil {
		t.Fatalf("read materialized attachment: %v", err)
	}
	if string(data) != "outbound attachment" || media.Name != "report.txt" {
		t.Fatalf("materialized media=%+v data=%q", media, data)
	}
	path := media.Path
	cleanup()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary attachment remains after cleanup: %v", err)
	}
}

func TestChannelOutboundUploadObjectKeyRejectsForeignAndMismatchedURLs(t *testing.T) {
	for _, tc := range []struct {
		url  string
		kind string
	}{
		{url: "https://example.invalid/uploads/files/20261106_0123456789abcdef0123456789abcdef.txt", kind: "file"},
		{url: "/uploads/files/20261106_0123456789abcdef0123456789abcdef.txt", kind: "image"},
		{url: "/uploads/tutorial/20261106_0123456789abcdef0123456789abcdef.pdf", kind: "file"},
	} {
		if key, err := weixinClawBotUploadObjectKey(tc.url, tc.kind); err == nil {
			t.Fatalf("accepted invalid outbound url %q as %q", tc.url, key)
		}
	}
}
