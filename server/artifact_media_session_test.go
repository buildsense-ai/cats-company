package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func artifactMediaFixture(t *testing.T) (*fileMediaTestStore, *FileMediaAuth, *FileAccessService, string, string) {
	t.Helper()
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://static.example")
	t.Setenv("CATSCO_ARTIFACT_ORIGIN_SECRET", strings.Repeat("s", 32))
	db, auth, parent, _ := newFileMediaTest(t)
	files := newFileAccessMemoryStore()
	raw := "https://static.example/artifacts/by-agent/9/report/latest/index.html?version=2#slide"
	files.messages[1] = &types.Message{ID: 1, TopicID: "grp_1", MsgType: "text", Content: `"` + raw + `"`}
	access := &FileAccessService{files: files, readable: func(uid int64, _ *types.Message) bool { return uid == 7 }}
	return db, auth, access, parent, raw
}
func artifactMediaIssue(t *testing.T, a *FileMediaAuth, access *FileAccessService, parent, raw, origin string, messageID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"url": raw, "message_id": messageID})
	req := httptest.NewRequest("POST", "https://chat.example/api/files/artifact-session", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+parent)
	req.Header.Set("Origin", origin)
	res := httptest.NewRecorder()
	a.HandleArtifactSession(access)(res, req)
	return res
}
func artifactMediaIssued(t *testing.T, res *httptest.ResponseRecorder) (string, *url.URL) {
	t.Helper()
	if res.Code != 200 {
		t.Fatalf("issue=%d %s", res.Code, res.Body.String())
	}
	var result struct {
		URL     string `json:"url"`
		Expires int    `json:"expires_in"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(result.URL)
	if err != nil || result.Expires != 900 {
		t.Fatalf("response=%s %v", res.Body.String(), err)
	}
	return target.Query().Get("media_token"), target
}
func artifactMediaRead(a *FileMediaAuth, access *FileAccessService, token, origin, path, secret, auth string) int {
	req := httptest.NewRequest("GET", "https://chat.example/api/artifacts/file-authorization?"+url.Values{"origin": {origin}, "path": {path}}.Encode(), nil)
	req.Header.Set(artifactMediaHeader, token)
	req.Header.Set(artifactOriginSecretHeader, secret)
	req.Header.Set("Authorization", auth)
	res := httptest.NewRecorder()
	a.ArtifactMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if !access.CanReadResource(r.Context(), UIDFromContext(r.Context()), origin+path, 0) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(204)
	})(res, req)
	return res.Code
}
func TestArtifactMediaSessionScopeAndIdentity(t *testing.T) {
	db, a, access, parent, raw := artifactMediaFixture(t)
	token, target := artifactMediaIssued(t, artifactMediaIssue(t, a, access, parent, raw, "https://chat.example", 1))
	if token == "" || target.Fragment != "slide" || target.Query().Get("version") != "2" {
		t.Fatalf("transport URL changed: %s", target)
	}
	record, ok := db.records[fileMediaHash(token)]
	if !ok || record.ActorUID != 7 || record.ParentHash != fileMediaHash(parent) {
		t.Fatal("session identity/hash lost")
	}
	secret := strings.Repeat("s", 32)
	for _, p := range []string{target.Path, "/artifacts/by-agent/9/report/v2/assets/app.js", "/artifacts/by-agent/9/report/manifest.json"} {
		if status := artifactMediaRead(a, access, token, "https://static.example", p, secret, ""); status != 204 {
			t.Fatalf("resource %s=%d", p, status)
		}
	}
	for _, p := range []string{"/artifacts/by-agent/8/report/latest/", "/artifacts/by-agent/9/other/latest/", "/artifacts/artifacts-index.json"} {
		if status := artifactMediaRead(a, access, token, "https://static.example", p, secret, ""); status != 404 {
			t.Fatalf("cross-resource=%d", status)
		}
	}
	if status := artifactMediaRead(a, access, token, "https://other.example", target.Path, secret, ""); status != 404 {
		t.Fatalf("cross-origin=%d", status)
	}
	if status := artifactMediaRead(a, access, token, "https://static.example", target.Path, "", ""); status != 403 {
		t.Fatalf("missing worker secret=%d", status)
	}
	if status := artifactMediaRead(a, access, token, "https://static.example", target.Path, secret, "Bearer invalid"); status != 401 {
		t.Fatalf("invalid header downgraded=%d", status)
	}
	if status, _ := fileMediaRead(a, "", &http.Cookie{Name: fileMediaCookieName, Value: token}); status != 401 {
		t.Fatalf("artifact token used as upload cookie=%d", status)
	}
	tampered := token[:len(token)-1] + "X"
	if status := artifactMediaRead(a, access, tampered, "https://static.example", target.Path, secret, ""); status != 401 {
		t.Fatalf("tampered token=%d", status)
	}
	access.readable = func(int64, *types.Message) bool { return false }
	if status := artifactMediaRead(a, access, token, "https://static.example", target.Path, secret, ""); status != 404 {
		t.Fatalf("removed member retained access=%d", status)
	}
}
func TestArtifactMediaSessionExpiryDisableAndLogout(t *testing.T) {
	for _, scenario := range []string{"expiry", "disabled", "logout", "offline"} {
		t.Run(scenario, func(t *testing.T) {
			db, a, access, parent, raw := artifactMediaFixture(t)
			token, target := artifactMediaIssued(t, artifactMediaIssue(t, a, access, parent, raw, "https://chat.example", 1))
			want := 401
			switch scenario {
			case "expiry":
				record := db.records[fileMediaHash(token)]
				record.ExpiresAt = time.Now().Add(-time.Second)
				db.records[record.TokenHash] = record
			case "disabled":
				db.users[7].State = 1
				want = 403
			case "logout":
				if res := fileMediaMutate(a, "DELETE", "Bearer "+parent, "https://chat.example", nil); res.Code != 204 {
					t.Fatalf("logout=%d", res.Code)
				}
			case "offline":
				db.unavailable = true
				want = 503
			}
			if status := artifactMediaRead(a, access, token, "https://static.example", target.Path, strings.Repeat("s", 32), ""); status != want {
				t.Fatalf("%s=%d want %d", scenario, status, want)
			}
		})
	}
}
func TestArtifactMediaSessionMintingRequiresVerifiedMessageAndOrigin(t *testing.T) {
	_, a, access, parent, raw := artifactMediaFixture(t)
	for _, tc := range []struct {
		raw, origin string
		id          int64
		want        int
	}{
		{raw, "", 1, 403}, {raw, "https://evil.example", 1, 403}, {raw, "https://chat.example", 2, 404},
		{"https://evil.example/artifacts/report/latest/", "https://chat.example", 1, 404},
		{"https://static.example/artifacts/by-agent/9/other/latest/", "https://chat.example", 1, 404},
	} {
		if res := artifactMediaIssue(t, a, access, parent, tc.raw, tc.origin, tc.id); res.Code != tc.want {
			t.Fatalf("mint %s=%d want %d", tc.raw, res.Code, tc.want)
		}
	}
}
