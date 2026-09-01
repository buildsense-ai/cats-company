package postgres

import (
	"strings"
	"testing"
)

func TestAgentArtifactTagsAreScopedPerAgentAndCascadeWithAgent(t *testing.T) {
	if !strings.Contains(createAgentArtifactTagsTable, "PRIMARY KEY (agent_uid, artifact_id, tag)") {
		t.Fatalf("agent artifact tags must be namespaced per agent and artifact; schema=%s", createAgentArtifactTagsTable)
	}
	if !strings.Contains(createAgentArtifactTagsTable, "agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE") {
		t.Fatalf("agent artifact tags must be deleted with their agent; schema=%s", createAgentArtifactTagsTable)
	}
	if !strings.Contains(createAgentArtifactTagsTable, "CREATE INDEX IF NOT EXISTS idx_agent_artifact_tags_agent_tag") {
		t.Fatalf("agent artifact tags need the (agent_uid, tag) index for tag-count aggregation; schema=%s", createAgentArtifactTagsTable)
	}
}
