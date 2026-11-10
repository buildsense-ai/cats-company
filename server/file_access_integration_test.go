package server

import (
	"context"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type fileAccessPolicyStore struct {
	store.Store
	*fileAccessMemoryStore
	users   map[int64]*types.User
	members map[int64]bool
	written int
}

func (db *fileAccessPolicyStore) GetUser(uid int64) (*types.User, error) { return db.users[uid], nil }
func (db *fileAccessPolicyStore) IsGroupMember(_ int64, uid int64) (bool, error) {
	return db.members[uid], nil
}
func (db *fileAccessPolicyStore) IsChannelManagedGroup(int64) (bool, error) { return false, nil }
func (db *fileAccessPolicyStore) SaveMessageWithFileBindings(topic string, uid int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientID string, metadata map[string]interface{}, keys []string) (int64, bool, error) {
	db.written++
	id := int64(10 + db.written)
	db.messages[id] = &types.Message{ID: id, TopicID: topic, FromUID: uid, Content: content, ContentBlocks: blocks, MsgType: msgType}
	return id, false, db.BindMessageFiles(context.Background(), id, topic, keys)
}
func newFileAccessPolicyStore() *fileAccessPolicyStore {
	return &fileAccessPolicyStore{fileAccessMemoryStore: newFileAccessMemoryStore(), users: map[int64]*types.User{1: {ID: 1, AccountType: types.AccountHuman}, 2: {ID: 2, AccountType: types.AccountBot}, 3: {ID: 3, AccountType: types.AccountHuman}}, members: map[int64]bool{1: true, 2: true}}
}
func TestFileAccessUsesHumanAndBotMessagePermissions(t *testing.T) {
	db := newFileAccessPolicyStore()
	ctx := context.Background()
	key := testPrivateKey
	db.messages[1] = fileAccessMessage(1, "grp_10")
	db.BindMessageFiles(ctx, 1, "grp_10", []string{key})
	access := NewFileAccessService(db, nil)
	for _, uid := range []int64{1, 2} {
		if !access.CanReadResource(ctx, uid, key, 0) {
			t.Fatalf("message reader %d denied", uid)
		}
	}
	if access.CanReadResource(ctx, 3, key, 1) {
		t.Fatal("non-member was granted access")
	}
	db.members[2] = false
	if access.CanReadResource(ctx, 2, key, 0) {
		t.Fatal("removed bot retained access")
	}
	db.users[1].State = 1
	if access.CanReadResource(ctx, 1, key, 0) {
		t.Fatal("disabled human retained access")
	}
}
func TestFileAccessMessageIngestionRejectsForgedReferences(t *testing.T) {
	db := newFileAccessPolicyStore()
	key := testPrivateKey
	db.RecordFileUpload(context.Background(), key, 1)
	payload := &normalizedMessagePayload{StoredType: "file", StoredContent: `{"url":"` + testPrivateURL + `"}`}
	if _, err := saveNormalizedMessage(db, "grp_10", 2, 0, payload); err == nil || db.written != 0 {
		t.Fatal("forged attachment saved")
	}
	result, err := saveNormalizedMessage(db, "grp_10", 1, 0, payload)
	if err != nil || result.ID == 0 || db.written != 1 {
		t.Fatalf("verified upload not saved: %+v %v", result, err)
	}
	if len(db.bindings[key]) != 1 {
		t.Fatal("new message missing binding")
	}
	if _, err := saveNormalizedMessage(db, "grp_10", 2, 0, payload); err != nil {
		t.Fatalf("authorized message reader could not reuse attachment: %v", err)
	}
}
func TestFileAccessArtifactOriginAndNamespaceIsolation(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://static.example,https://other.example")
	t.Setenv("CATSCO_ARTIFACT_NODES_JSON", "")
	t.Setenv("CATSCO_ARTIFACT_NODES_FILE", "")
	t.Setenv("CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE", "")
	raw := "https://static.example/artifacts/by-agent/2/report/latest/index.html"
	key := canonicalFileResource(raw)
	if key != "artifact:https://static.example/artifacts/by-agent/2/report" {
		t.Fatalf("key: %q", key)
	}
	if canonicalFileResource("https://static.example/artifacts/by-agent/3/report/latest/") == key || canonicalFileResource("https://other.example/artifacts/by-agent/2/report/latest/") == key {
		t.Fatal("artifact namespaces collapsed")
	}
	for _, raw := range []string{"https://evil.example/artifacts/by-agent/2/report/latest/", "https://static.example/artifacts/by-agent/2/report/../secret/", "https://static.example/artifacts/artifacts-index.json", "artifact:report"} {
		if got := canonicalFileResource(raw); got != "" {
			t.Fatalf("unsafe artifact %q: %q", raw, got)
		}
	}
	message := &types.Message{MsgType: "text", Content: `"Published: https://static.example/artifacts/by-agent/2/report/latest/"`}
	keys := MessageFileResources(message)
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("legacy artifact URL: %v", keys)
	}
	db := newFileAccessMemoryStore()
	db.messages[1] = message
	message.ID = 1
	message.TopicID = "grp_10"
	access := &FileAccessService{files: db, readable: func(uid int64, _ *types.Message) bool { return uid == 1 }}
	if !access.CanReadResource(context.Background(), 1, raw, 1) {
		t.Fatal("real artifact message failed repair")
	}
	if access.CanReadResource(context.Background(), 1, "https://static.example/artifacts/by-agent/3/report/latest/", 1) {
		t.Fatal("same ID from another namespace granted")
	}
}
