package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type artifactPrivacyAccess struct {
	read func(int64, string, int64) bool
	use  func(int64, string) bool
}

func (a artifactPrivacyAccess) CanReadResource(_ context.Context, uid int64, resource string, mid int64) bool {
	return a.read != nil && a.read(uid, resource, mid)
}
func (a artifactPrivacyAccess) CanUseResource(_ context.Context, uid int64, resource string) bool {
	return a.use != nil && a.use(uid, resource)
}

func TestArtifactPrivacyPathResource(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example")
	for _, item := range []struct{ path, want string }{
		{"/artifacts/demo/v1/", "artifact:https://artifact.example/artifacts/demo"},
		{"/artifacts/demo/latest/index.html", "artifact:https://artifact.example/artifacts/demo"},
		{"/artifacts/demo/v1/nested/data.json", "artifact:https://artifact.example/artifacts/demo"},
		{"/artifacts/by-agent/42/demo/v1/app.js", "artifact:https://artifact.example/artifacts/by-agent/42/demo"},
		{"/by-agent/42/demo/v1/app.js", "artifact:https://artifact.example/by-agent/42/demo"},
		{"/artifacts/demo", "artifact:https://artifact.example/artifacts/demo"},
		{"/artifacts/", ""}, {"/artifacts/artifacts-index.json", ""},
		{"/artifacts/by-agent/42/artifacts-index.json", ""},
		{"/artifacts/by-agent/0/demo/v1/", ""},
		{"/artifacts/by-agent/042/demo/v1/", ""},
		{"/artifacts/by-agent/9223372036854775808/demo/v1/", ""},
		{"/artifacts/demo/../private/data.json", ""},
		{"/artifacts/demo/%2e%2e/private/data.json", ""},
		{"/artifacts/demo/%252e%252e/private", ""},
		{"/artifacts/demo/%5cprivate", ""},
		{"/artifacts/demo//data.json", ""},
		{"/artifacts/demo/%00", ""}, {"/artifacts/demo/%zz", ""},
		{"/uploads/files/demo.html", ""}, {"https://foreign.invalid/artifacts/demo/v1/", ""},
	} {
		t.Run(item.path, func(t *testing.T) {
			got, ok := ArtifactResourceFromPath("https://artifact.example", item.path)
			if got != item.want || ok != (item.want != "") {
				t.Fatalf("resource = %q, %v; want %q", got, ok, item.want)
			}
		})
	}
}

func TestArtifactPrivacyAuthorization(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example")
	const secret = "test-artifact-origin-secret-0123456789"
	for _, item := range []struct {
		name, method, path, messageID, secret string
		uid                                   int64
		service, readable                     bool
		want                                  int
	}{
		{"reader", "GET", "/artifacts/demo/v1/", "17", secret, 7, true, true, 204},
		{"bot reader", "GET", "/artifacts/demo/v1/app.js", "", secret, 440, true, true, 204},
		{"head", "HEAD", "/artifacts/demo/v1/data.json", "", secret, 7, true, true, 204},
		{"unrelated", "GET", "/artifacts/demo/v1/", "17", secret, 7, true, false, 404},
		{"anonymous", "GET", "/artifacts/demo/v1/", "", secret, 0, true, true, 401},
		{"missing origin", "GET", "/artifacts/demo/v1/", "", "", 7, true, true, 403},
		{"forged origin", "GET", "/artifacts/demo/v1/", "", "wrong", 7, true, true, 403},
		{"missing service", "GET", "/artifacts/demo/v1/", "", secret, 7, false, true, 503},
		{"aggregate index", "GET", "/artifacts/artifacts-index.json", "", secret, 7, true, true, 404},
		{"agent index", "HEAD", "/artifacts/by-agent/42/artifacts-index.json", "", secret, 7, true, true, 404},
		{"invalid message", "GET", "/artifacts/demo/v1/", "oops", secret, 7, true, true, 404},
		{"zero message", "GET", "/artifacts/demo/v1/", "0", secret, 7, true, true, 404},
		{"mutation", "POST", "/artifacts/demo/v1/", "", secret, 7, true, true, 405},
	} {
		t.Run(item.name, func(t *testing.T) {
			h := &CloudArtifactHandler{originSecret: secret}
			if item.service {
				h.fileAccess = artifactPrivacyAccess{read: func(uid int64, resource string, mid int64) bool {
					if uid != item.uid || resource != "artifact:https://artifact.example/artifacts/demo" {
						t.Fatalf("identity = %d %q", uid, resource)
					}
					if item.messageID == "17" && mid != 17 {
						t.Fatalf("message ID = %d", mid)
					}
					return item.readable
				}}
			}
			r := httptest.NewRequest(item.method, "/internal/artifact-file-access?origin=https://artifact.example&path="+url.QueryEscape(item.path)+"&message_id="+item.messageID, nil)
			r = r.WithContext(context.WithValue(r.Context(), uidKey, item.uid))
			r.Header.Set(artifactOriginSecretHeader, item.secret)
			w := httptest.NewRecorder()
			h.HandleFileAuthorization(w, r)
			if w.Code != item.want {
				t.Fatalf("status = %d; want %d", w.Code, item.want)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("authorization response can be cached")
			}
		})
	}
}

func TestArtifactPrivacyIgnoresVisitorResourceHeaders(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example")
	h := &CloudArtifactHandler{originSecret: "test-artifact-origin-secret-0123456789"}
	h.fileAccess = artifactPrivacyAccess{read: func(uid int64, resource string, mid int64) bool {
		return uid == 7 && resource == "artifact:https://artifact.example/artifacts/demo" && mid == 25
	}}
	r := httptest.NewRequest("GET", "/internal/artifact-file-access?path=/artifacts/private/v1/&message_id=99", nil)
	r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
	r.Header.Set(artifactOriginSecretHeader, h.originSecret)
	r.Header.Set("X-Catsco-Artifact-Path", "/artifacts/demo/v1/app.js?message_id=25")
	r.Header.Set("X-Catsco-Artifact-Origin", "https://artifact.example")
	w := httptest.NewRecorder()
	h.HandleFileAuthorization(w, r)
	if w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestArtifactPrivacyListsFilterEveryResource(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://example.test")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloudArtifactIndex{
			ContractVersion: artifactIndexContract,
			Artifacts: []cloudArtifact{
				{ID: "readable", Title: "visible", Kind: "html", URL: "https://example.test/artifacts/readable/v1/", UpdatedAt: "2026-08-12T07:00:00Z"},
				{ID: "private", Title: "hidden", Kind: "html", URL: "https://example.test/artifacts/private/v1/", UpdatedAt: "2026-08-12T07:00:00Z"},
			},
		})
	}))
	defer upstream.Close()
	h := NewCloudArtifactHandler(upstream.URL, upstream.Client())
	h.fileAccess = artifactPrivacyAccess{read: func(uid int64, resource string, mid int64) bool {
		return uid == 7 && resource == "artifact:https://example.test/artifacts/readable" && mid == 0
	}}
	r := httptest.NewRequest("GET", "/api/artifacts", nil)
	r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
	for _, configured := range []bool{true, false} {
		if !configured {
			h.SetFileAccess(nil)
		}
		w := httptest.NewRecorder()
		h.HandleList(w, r)
		if w.Code != 200 {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}
		var index cloudArtifactIndex
		if err := json.Unmarshal(w.Body.Bytes(), &index); err != nil {
			t.Fatal(err)
		}
		want := 1
		if !configured {
			want = 0
		}
		if len(index.Artifacts) != want || strings.Contains(w.Body.String(), "hidden") {
			t.Fatalf("list = %s", w.Body)
		}
	}
}

func TestArtifactPrivacyPublishRejectsSourceTheft(t *testing.T) {
	uploads, sourceURL := artifactUploadSourceForTest(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer upstream.Close()
	h := NewCloudArtifactHandler(upstream.URL, upstream.Client())
	h.SetUploadSourceValidator(uploads)
	h.fileAccess = artifactPrivacyAccess{use: func(uid int64, resource string) bool {
		if uid != 7 || resource != "upload:files/"+testArtifactUploadFileName {
			t.Fatalf("source = %d %q", uid, resource)
		}
		return false
	}}
	for _, configured := range []bool{true, false} {
		if !configured {
			h.SetFileAccess(nil)
		}
		r := httptest.NewRequest("POST", "/api/agents/440/artifacts", strings.NewReader(`{"title":"page","kind":"html","url":"`+sourceURL+`"}`))
		w := httptest.NewRecorder()
		h.handlePublish(w, r, 7, "owner", upstream.URL, "unused", "", 440)
		want := 403
		if !configured {
			want = 503
		}
		if w.Code != want || calls != 0 {
			t.Fatalf("status = %d; upstream calls = %d", w.Code, calls)
		}
	}
}

func TestArtifactPrivacyManagementRedirectNeverForwardsCredentials(t *testing.T) {
	calls := 0
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer collector.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, collector.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	h := NewCloudArtifactHandler(upstream.URL, upstream.Client())
	r := httptest.NewRequest("GET", "/api/artifacts", nil)
	r.Header.Set("Cookie", "session=private")
	r.Header.Set("Authorization", "Bearer human-secret")
	if _, err := h.requestManagement(r, "GET", upstream.URL, nil, "management-secret"); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 0 {
		t.Fatal("redirect target received credentials")
	}
}

func TestArtifactPrivacyOriginNamespaceCannotAlias(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://one.example,https://two.example")
	h := &CloudArtifactHandler{originSecret: "test-artifact-origin-secret-0123456789"}
	h.fileAccess = artifactPrivacyAccess{read: func(uid int64, resource string, mid int64) bool {
		return resource == "artifact:https://one.example/artifacts/demo"
	}}
	for _, item := range []struct {
		origin, path string
		want         int
	}{
		{"https://one.example", "/artifacts/demo/v1/", 204},
		{"https://two.example", "/artifacts/demo/v1/", 404},
		{"https://one.example", "/artifacts/by-agent/42/demo/v1/", 404},
		{"https://untrusted.example", "/artifacts/demo/v1/", 404},
		{"", "/artifacts/demo/v1/", 404},
	} {
		r := httptest.NewRequest("GET", "/authorize?origin="+url.QueryEscape(item.origin)+"&path="+url.QueryEscape(item.path), nil)
		r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
		r.Header.Set(artifactOriginSecretHeader, h.originSecret)
		w := httptest.NewRecorder()
		h.HandleFileAuthorization(w, r)
		if w.Code != item.want {
			t.Fatalf("%s%s: status %d, want %d", item.origin, item.path, w.Code, item.want)
		}
	}
}

type artifactPrivacyRecorder struct {
	artifactPrivacyAccess
	record func(int64, string) error
}

func (a artifactPrivacyRecorder) RecordUpload(_ context.Context, uid int64, resource string) error {
	return a.record(uid, resource)
}

func privatePublishedArtifact() cloudArtifact {
	return cloudArtifact{ID: "demo", Title: "page", Kind: "html",
		URL: "https://artifact.example/artifacts/by-agent/440/demo/latest/", Status: "active",
		CreatedAt: "2026-08-12T07:00:00Z", UpdatedAt: "2026-08-12T07:00:00Z",
		AgentUID: "440", UploaderUID: "7"}
}

func TestArtifactPrivacyPublishRecordsDraftAtTrustedCreation(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example")
	uploads, sourceURL := artifactUploadSourceForTest(t)
	artifact := privatePublishedArtifact()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-secret" || r.Header.Get("Cookie") != "" {
			t.Error("publish did not isolate management credentials")
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(cloudArtifactOperation{OK: true, Artifact: artifact})
	}))
	defer upstream.Close()
	db := newFileAccessMemoryStore()
	access := &FileAccessService{files: db, readable: func(uid int64, m *types.Message) bool { return uid == 8 && m.TopicID == "grp-private" }}
	if err := access.RecordUpload(context.Background(), 7, sourceURL); err != nil {
		t.Fatal(err)
	}
	h := NewCloudArtifactHandler("https://artifact.example/artifacts/artifacts-index.json", upstream.Client())
	h.SetUploadSourceValidator(uploads)
	h.SetFileAccess(access)
	r := httptest.NewRequest("POST", "/api/agents/440/artifacts", strings.NewReader(`{"title":"page","kind":"html","url":"`+sourceURL+`"}`))
	r.Header.Set("Authorization", "Bearer visitor-secret")
	r.Header.Set("Cookie", "session=visitor")
	w := httptest.NewRecorder()
	h.handlePublish(w, r, 7, "friend", upstream.URL, "management-secret", "https://artifact.example/artifacts", 440)
	if w.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", w.Code, w.Body)
	}
	key := canonicalArtifactResource(artifact.URL)
	if !access.CanUseResource(r.Context(), 7, key) || access.CanUseResource(r.Context(), 8, key) {
		t.Fatal("new draft publisher cannot attach its artifact, or an unrelated actor can")
	}
	if record := db.uploads[key]; record == nil || record.UploaderUID != 7 {
		t.Fatal("canonical ownership missing")
	}
	message := &types.Message{ID: 1, TopicID: "grp-private", MsgType: "text", Content: artifact.URL}
	db.messages[1] = message
	if err := db.BindMessageFiles(r.Context(), 1, message.TopicID, MessageFileResources(message)); err != nil {
		t.Fatal(err)
	}
	if access.CanUseResource(r.Context(), 7, key) || !access.CanUseResource(r.Context(), 8, key) {
		t.Fatal("persisted-message permissions did not replace draft ownership")
	}
}

func TestArtifactPrivacyPublishNeverClaimsUntrustedOrExistingResources(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example,https://other.example")
	for _, mode := range []string{"new", "repeat denied", "repeat allowed", "version denied", "version allowed", "wrong origin", "wrong namespace", "wrong id", "wrong uploader", "not active", "failed upstream", "malformed", "record error", "missing recorder", "missing public base"} {
		t.Run(mode, func(t *testing.T) {
			uploads, sourceURL := artifactUploadSourceForTest(t)
			artifact := privatePublishedArtifact()
			upstreamStatus, want, records, calls := 201, 201, 0, 0
			switch mode {
			case "repeat denied", "repeat allowed":
				upstreamStatus = 200
			case "version denied", "version allowed":
				version := 2
				artifact.PublishVersion = &version
			case "wrong origin":
				artifact.URL = strings.Replace(artifact.URL, "artifact.example", "other.example", 1)
			case "wrong namespace":
				artifact.URL = strings.Replace(artifact.URL, "/440/", "/441/", 1)
			case "wrong id":
				artifact.ID = "other"
			case "wrong uploader":
				artifact.UploaderUID = "8"
			case "not active":
				artifact.Status = "deleted"
				artifact.DeletedAt = artifact.UpdatedAt
			case "failed upstream":
				upstreamStatus = 500
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(upstreamStatus)
				if mode == "malformed" {
					_, _ = w.Write([]byte(`{`))
					return
				}
				_ = json.NewEncoder(w).Encode(cloudArtifactOperation{OK: true, Artifact: artifact})
			}))
			defer upstream.Close()
			h := NewCloudArtifactHandler("https://artifact.example/artifacts/artifacts-index.json", upstream.Client())
			h.SetUploadSourceValidator(uploads)
			access := artifactPrivacyAccess{use: func(uid int64, key string) bool {
				return uid == 7 && (key == "upload:files/"+testArtifactUploadFileName ||
					((mode == "repeat allowed" || mode == "version allowed") && key == "artifact:https://artifact.example/artifacts/by-agent/440/demo"))
			}}
			h.fileAccess = artifactPrivacyRecorder{artifactPrivacyAccess: access, record: func(uid int64, key string) error {
				records++
				if uid != 7 || key != "artifact:https://artifact.example/artifacts/by-agent/440/demo" {
					t.Fatal("untrusted ownership claim", uid, key)
				}
				if mode == "record error" {
					return errors.New("ownership database unavailable")
				}
				return nil
			}}
			if mode == "missing recorder" {
				h.fileAccess = access
			}
			publicBase := "https://artifact.example/artifacts"
			if mode == "missing public base" {
				h.indexURL = ""
				publicBase = ""
			}
			r := httptest.NewRequest("POST", "/publish", strings.NewReader(`{"title":"page","kind":"html","url":"`+sourceURL+`"}`))
			w := httptest.NewRecorder()
			h.handlePublish(w, r, 7, "owner", upstream.URL, "management-secret", publicBase, 440)
			switch mode {
			case "repeat denied", "version denied":
				want = 403
			case "wrong origin", "wrong namespace", "wrong id", "wrong uploader", "not active", "malformed":
				want = 502
			case "failed upstream":
				want = 502
			case "record error", "missing recorder", "missing public base":
				want = 503
			}
			if w.Code != want {
				t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body)
			}
			wantRecords := 0
			if mode == "new" || mode == "record error" {
				wantRecords = 1
			}
			if records != wantRecords {
				t.Fatalf("ownership writes = %d, want %d", records, wantRecords)
			}
			if (mode == "missing recorder" || mode == "missing public base") && calls != 0 {
				t.Fatal("mutated without required configuration")
			}
		})
	}
}

func TestArtifactPrivacyMutationRequiresCurrentResourceUseBeforeMutation(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://artifact.example,https://other.example")
	for _, mode := range []string{"denied", "allowed", "restore denied", "restore allowed", "missing service", "wrong origin", "wrong namespace", "wrong id"} {
		t.Run(mode, func(t *testing.T) {
			artifact := privatePublishedArtifact()
			suffix, method := "", http.MethodDelete
			if strings.HasPrefix(mode, "restore") {
				suffix, method = "/restore", http.MethodPost
				artifact.Status = "deleted"
				artifact.DeletedAt = artifact.UpdatedAt
			}
			if mode == "wrong origin" {
				artifact.URL = strings.Replace(artifact.URL, "artifact.example", "other.example", 1)
			}
			if mode == "wrong namespace" {
				artifact.URL = strings.Replace(artifact.URL, "/440/", "/441/", 1)
			}
			if mode == "wrong id" {
				artifact.URL = strings.Replace(artifact.URL, "/demo/", "/other/", 1)
			}
			mutations := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					_ = json.NewEncoder(w).Encode(cloudArtifactManagementList{ContractVersion: artifactManagementContract, Status: artifact.Status, Count: 1, Artifacts: []cloudArtifact{artifact}})
					return
				}
				mutations++
				if suffix == "/restore" {
					artifact.Status = "active"
					artifact.DeletedAt = ""
				} else {
					artifact.Status = "deleted"
					artifact.DeletedAt = artifact.UpdatedAt
				}
				_ = json.NewEncoder(w).Encode(cloudArtifactOperation{OK: true, Artifact: artifact})
			}))
			defer upstream.Close()
			h := NewCloudArtifactHandler("https://artifact.example/artifacts/artifacts-index.json", upstream.Client())
			if mode != "missing service" {
				h.fileAccess = artifactPrivacyAccess{use: func(uid int64, key string) bool {
					return strings.HasSuffix(mode, "allowed") && uid == 7 && key == "artifact:https://artifact.example/artifacts/by-agent/440/demo"
				}}
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/mutation", nil)
			h.handleMutation(w, r, "demo", suffix, 7, "owner", upstream.URL, "secret", "https://artifact.example/artifacts", 440)
			want, wantMutations := 404, 0
			if strings.HasSuffix(mode, "allowed") {
				want, wantMutations = 200, 1
			}
			if mode == "missing service" {
				want = 503
			}
			if strings.HasPrefix(mode, "wrong") {
				want = 502
			}
			if w.Code != want || mutations != wantMutations {
				t.Fatalf("status %d, mutations %d; want %d, %d: %s", w.Code, mutations, want, wantMutations, w.Body)
			}
		})
	}
}
