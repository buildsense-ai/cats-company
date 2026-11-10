package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestArtifactPrivateSourcePublishIntegration(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://chat.example")
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example")
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "signed source", true: "signing unavailable"}[unavailable], func(t *testing.T) {
			db, _, parent, _ := newFileMediaTest(t)
			files := &artifactSourceTestStore{fileAccessMemoryStore: newFileAccessMemoryStore(), sessions: db}
			access := &FileAccessService{files: files, readable: func(int64, *types.Message) bool { return false }}
			if err := access.RecordUpload(context.Background(), 7, testPrivateKey); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "files"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "files", testPrivateFile), []byte("<html>private</html>"), 0600); err != nil {
				t.Fatal(err)
			}
			uploads := NewUploadHandler(dir, "/uploads")
			if !unavailable {
				uploads.SetFileAccess(access)
			}
			calls := 0
			artifact := privatePublishedArtifact()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]string
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				source, err := url.Parse(payload["url"])
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				token := source.Query().Get("publish_token")
				if source.Scheme != "https" || source.Host != "chat.example" || source.Path != testPrivateURL || !strings.HasPrefix(token, "p.") {
					t.Errorf("management did not receive the scoped private source")
				}
				if record, ok := db.records[fileMediaHash(token)]; !ok || record.ActorUID != 7 || record.ParentHash != fileMediaHash(parent) {
					t.Error("source session does not match the publisher")
				}
				if r.Header.Get("Authorization") != "Bearer management-secret" || r.Header.Get("Cookie") != "" {
					t.Error("management credential isolation failed")
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(cloudArtifactOperation{OK: true, Artifact: artifact})
			}))
			defer upstream.Close()
			h := NewCloudArtifactHandler("https://artifact.example/artifacts/artifacts-index.json", upstream.Client())
			h.SetUploadSourceValidator(uploads)
			h.SetFileAccess(access)
			r := httptest.NewRequest("POST", "/publish", strings.NewReader(`{"title":"private","kind":"html","url":"https://chat.example`+testPrivateURL+`"}`))
			r.Header.Set("Authorization", "Bearer "+parent)
			w := httptest.NewRecorder()
			h.handlePublish(w, r, 7, "owner", upstream.URL, "management-secret", "https://artifact.example/artifacts", 440)
			if unavailable {
				if w.Code != 503 || calls != 0 {
					t.Fatalf("failed signer: status=%d calls=%d", w.Code, calls)
				}
				return
			}
			if w.Code != 201 || calls != 1 {
				t.Fatalf("publish: status=%d calls=%d body=%s", w.Code, calls, w.Body)
			}
			if strings.Contains(w.Body.String(), "publish_token") || !strings.Contains(w.Body.String(), artifact.URL) {
				t.Fatal("source transport leaked into the artifact response")
			}
		})
	}
}
