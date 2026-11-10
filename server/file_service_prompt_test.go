package server

import (
	"context"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type filePromptPolicyStore struct{ *fileAccessPolicyStore }

func (db *filePromptPolicyStore) SaveMessageWithMetadata(string, int64, string, []types.ContentBlock, string, string, string, int64, string, map[string]interface{}) (int64, bool, error) {
	return 0, false, errors.New("unvalidated message write")
}
func TestFileAccessServicePromptCannotManufactureArtifactReference(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "https://static.example")
	db := &filePromptPolicyStore{newFileAccessPolicyStore()}
	raw := "https://static.example/artifacts/by-agent/2/report/latest/"
	if _, _, err := saveServiceBotPrompt(db, "grp_10", 2, "read "+raw, nil); err == nil || db.written != 0 {
		t.Fatal("service prompt manufactured access")
	}
	if err := db.RecordFileUpload(context.Background(), canonicalArtifactResource(raw), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := saveServiceBotPrompt(db, "grp_10", 1, "read "+raw, nil); err != nil || db.written != 1 {
		t.Fatalf("verified source prompt: %v", err)
	}
	if len(db.bindings[canonicalArtifactResource(raw)]) != 1 {
		t.Fatal("service prompt not bound atomically")
	}
}
