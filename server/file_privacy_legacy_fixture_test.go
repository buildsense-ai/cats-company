package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const legacyUploadActorUID int64 = 7

func legacyUploadRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	return r.WithContext(context.WithValue(r.Context(), uidKey, legacyUploadActorUID))
}

// Existing format/preview fixtures create their bytes directly. Record that
// creation for the same actor used by their requests; subsequent HTTP uploads
// record their own provenance through the production handler.
func legacyUploadHandler(t *testing.T, dir, baseURL string) *UploadHandler {
	t.Helper()
	files := newFileAccessMemoryStore()
	service := &FileAccessService{files: files, readable: func(int64, *types.Message) bool { return false }}
	for _, subdir := range []string{"files", "images", "feedback"} {
		entries, err := os.ReadDir(filepath.Join(dir, subdir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				if err := service.RecordUpload(context.Background(), legacyUploadActorUID, "/uploads/"+subdir+"/"+entry.Name()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	handler := NewUploadHandler(dir, baseURL)
	handler.SetFileAccess(service)
	return handler
}

// These legacy publish tests exercise source validation without the optional
// private-source signer; transport signing has its own dedicated fixtures.
type legacyArtifactUploadSourceFixture struct {
	uploads *UploadHandler
}

func (f *legacyArtifactUploadSourceFixture) ValidateArtifactSourcePath(resource string) error {
	return f.uploads.ValidateArtifactSourcePath(resource)
}

// List/tag tests specify exactly which actor can read each upstream resource.
// Unknown actors and resources remain denied; creation can register only its
// actor/resource pair through the same recorder contract used in production.
type legacyArtifactAccessFixture struct {
	grants map[int64]map[string]bool
}

func (f *legacyArtifactAccessFixture) CanReadResource(_ context.Context, uid int64, resource string, _ int64) bool {
	return uid > 0 && f.grants[uid][canonicalFileResource(resource)]
}
func (f *legacyArtifactAccessFixture) CanUseResource(ctx context.Context, uid int64, resource string) bool {
	return f.CanReadResource(ctx, uid, resource, 0)
}
func (f *legacyArtifactAccessFixture) RecordUpload(_ context.Context, uid int64, resource string) error {
	key := canonicalFileResource(resource)
	if uid <= 0 || key == "" {
		return errors.New("invalid fixture upload")
	}
	if f.grants[uid] == nil {
		f.grants[uid] = map[string]bool{}
	}
	f.grants[uid][key] = true
	return nil
}
func grantLegacyArtifactResources(t *testing.T, h *CloudArtifactHandler, uid int64, resources ...string) {
	t.Helper()
	origins := os.Getenv("CATSCO_ARTIFACT_FILE_ORIGINS")
	for _, raw := range resources {
		if u, err := url.Parse(strings.TrimPrefix(raw, "artifact:")); err == nil && u.Scheme == "https" && u.Host != "" {
			origins += "," + u.Scheme + "://" + u.Host
		}
	}
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", origins)
	access, ok := h.fileAccess.(*legacyArtifactAccessFixture)
	if !ok {
		access = &legacyArtifactAccessFixture{grants: map[int64]map[string]bool{}}
		h.fileAccess = access
	}
	for _, resource := range resources {
		if err := access.RecordUpload(context.Background(), uid, resource); err != nil {
			t.Fatalf("authorize fixture %q: %v", resource, err)
		}
	}
}

// Use a named memory store and explicit forwarding so embedding store.Store
// cannot hide or make the FileAccessStore methods ambiguous.
type legacyChannelPrivacyStore struct {
	*channelAgentTestStore
	files *fileAccessMemoryStore
}

func newLegacyChannelPrivacyStore() *legacyChannelPrivacyStore {
	return &legacyChannelPrivacyStore{channelAgentTestStore: newChannelAgentTestStore(), files: newFileAccessMemoryStore()}
}
func (s *legacyChannelPrivacyStore) RecordFileUpload(ctx context.Context, key string, uid int64) error {
	return s.files.RecordFileUpload(ctx, key, uid)
}
func (s *legacyChannelPrivacyStore) GetFileUpload(ctx context.Context, key string) (*store.FileUploadRecord, error) {
	return s.files.GetFileUpload(ctx, key)
}
func (s *legacyChannelPrivacyStore) BindMessageFiles(ctx context.Context, id int64, topic string, keys []string) error {
	return s.files.BindMessageFiles(ctx, id, topic, keys)
}
func (s *legacyChannelPrivacyStore) ListFileMessageBindings(ctx context.Context, key string) ([]store.FileMessageBinding, error) {
	return s.files.ListFileMessageBindings(ctx, key)
}
func (s *legacyChannelPrivacyStore) GetFileAccessMessage(ctx context.Context, id int64) (*types.Message, error) {
	return s.files.GetFileAccessMessage(ctx, id)
}
func (s *legacyChannelPrivacyStore) ListFileBackfillMessages(ctx context.Context, after int64, limit int) ([]*types.Message, error) {
	return s.files.ListFileBackfillMessages(ctx, after, limit)
}
func (s *legacyChannelPrivacyStore) CreateFileMediaSession(context.Context, store.FileMediaSession) error {
	return errors.New("media sessions are not configured in this fixture")
}
func (s *legacyChannelPrivacyStore) GetFileMediaSession(context.Context, string) (*store.FileMediaSession, error) {
	return nil, nil
}
func (s *legacyChannelPrivacyStore) RevokeFileMediaSessions(context.Context, string) error {
	return nil
}
func (s *legacyChannelPrivacyStore) SaveMessageWithFileBindings(topic string, uid int64, content string, blocks []types.ContentBlock, mode, role, kind string, reply int64, clientID string, metadata map[string]interface{}, keys []string) (int64, bool, error) {
	id, duplicate, err := s.channelAgentTestStore.SaveMessageIdempotent(topic, uid, content, blocks, mode, role, kind, reply, clientID)
	if err != nil || duplicate {
		return id, duplicate, err
	}
	message := s.messages[len(s.messages)-1]
	message.Metadata = metadata
	s.files.messages[id] = message
	return id, false, s.files.BindMessageFiles(context.Background(), id, topic, keys)
}

// The screenshot fixture retains its original durable/idempotent save logic.
// The access index references those actual saved messages.
type legacyScreenshotPrivacyStore struct {
	*openBindingTestStore
	files *fileAccessMemoryStore
}

func (s *legacyScreenshotPrivacyStore) RecordFileUpload(ctx context.Context, key string, uid int64) error {
	return s.files.RecordFileUpload(ctx, key, uid)
}
func (s *legacyScreenshotPrivacyStore) GetFileUpload(ctx context.Context, key string) (*store.FileUploadRecord, error) {
	return s.files.GetFileUpload(ctx, key)
}
func (s *legacyScreenshotPrivacyStore) BindMessageFiles(ctx context.Context, id int64, topic string, keys []string) error {
	return s.files.BindMessageFiles(ctx, id, topic, keys)
}
func (s *legacyScreenshotPrivacyStore) ListFileMessageBindings(ctx context.Context, key string) ([]store.FileMessageBinding, error) {
	return s.files.ListFileMessageBindings(ctx, key)
}
func (s *legacyScreenshotPrivacyStore) GetFileAccessMessage(ctx context.Context, id int64) (*types.Message, error) {
	return s.files.GetFileAccessMessage(ctx, id)
}
func (s *legacyScreenshotPrivacyStore) ListFileBackfillMessages(ctx context.Context, after int64, limit int) ([]*types.Message, error) {
	return s.files.ListFileBackfillMessages(ctx, after, limit)
}
func (s *legacyScreenshotPrivacyStore) CreateFileMediaSession(context.Context, store.FileMediaSession) error {
	return errors.New("media sessions are not configured in this fixture")
}
func (s *legacyScreenshotPrivacyStore) GetFileMediaSession(context.Context, string) (*store.FileMediaSession, error) {
	return nil, nil
}
func (s *legacyScreenshotPrivacyStore) RevokeFileMediaSessions(context.Context, string) error {
	return nil
}
func (s *legacyScreenshotPrivacyStore) SaveMessageWithFileBindings(topic string, uid int64, content string, blocks []types.ContentBlock, mode, role, kind string, reply int64, clientID string, metadata map[string]interface{}, keys []string) (int64, bool, error) {
	id, duplicate, err := s.openBindingTestStore.SaveMessageWithMetadata(topic, uid, content, blocks, mode, role, kind, reply, clientID, metadata)
	if err != nil || duplicate {
		return id, duplicate, err
	}
	s.files.messages[id] = s.durable[len(s.durable)-1]
	return id, false, s.files.BindMessageFiles(context.Background(), id, topic, keys)
}
func newLegacyScreenshotPrivacyFixture(t *testing.T) *openBindingFixture {
	t.Helper()
	f := newOpenBindingFixture(t)
	db := &legacyScreenshotPrivacyStore{openBindingTestStore: f.db, files: newFileAccessMemoryStore()}
	f.hub.db = db
	f.bindings.messages.db = db
	f.bindings.uploads.SetFileAccess(NewFileAccessService(db, f.hub))
	// The existing fixture's file route predates authenticated attachment reads.
	f.platform.Close()
	serveFile := JWTAuthMiddlewareWithDB(db)(f.bindings.uploads.HandleServeFile)
	f.platform = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/uploads/") {
			serveFile(w, r)
			return
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.platform.Close)
	return f
}

var _ store.FileAccessStore = (*legacyChannelPrivacyStore)(nil)
var _ store.FileMessageWriter = (*legacyChannelPrivacyStore)(nil)
var _ store.FileAccessStore = (*legacyScreenshotPrivacyStore)(nil)
var _ store.FileMessageWriter = (*legacyScreenshotPrivacyStore)(nil)
