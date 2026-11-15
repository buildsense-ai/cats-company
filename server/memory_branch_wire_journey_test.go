package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// The input fixture is an actual Branch/controller capture, not a handcrafted
// message. Failure modes were listed in memory_source_journey_test.go before
// implementation. This path checks HTTP ingestion → typed content blocks →
// storage JSON → history blocks, including snapshots beyond the old 2KB clip.
func TestMemoryBranchWireReplayJourney(t *testing.T) {
	fixturePath, err := filepath.Abs("testdata/memory-branch.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Events []json.RawMessage `json:"events"`
	}
	if json.Unmarshal(data, &fixture) != nil || len(fixture.Events) < 3 {
		t.Fatal("missing real runtime capture")
	}
	var replay []map[string]interface{}
	var wire []json.RawMessage
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		var req SendMessageRequest
		if json.Unmarshal(raw, &req) != nil {
			w.WriteHeader(400)
			return
		}
		payload, e := normalizeMessageRequest(&req)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		if len(payload.ContentBlocks) != 1 || payload.ContentBlocks[0].Type != "memory_branch" || !isInternalAgentWorkingMessage(payload.DisplayType, payload.DisplayContent, payload.ContentBlocks) {
			t.Error("not a durable UI-only source block")
		}
		encoded, _ := json.Marshal(payload.ContentBlocks)
		var stored []types.ContentBlock
		if json.Unmarshal(encoded, &stored) != nil {
			t.Error("storage JSON failed")
		}
		snapshot := stored[0].Payload
		replay = append(replay, snapshot)
		wire = append(wire, append(json.RawMessage(nil), raw...))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"seq_id": len(replay)})
	}))
	defer handler.Close()
	if root := os.Getenv("XIAOBA_BRANCH_WORKTREE"); root != "" {
		command := exec.Command("node", "--import", "tsx", filepath.Join(root, "scripts/memory-branch-wire-journey.ts"), handler.URL, fixturePath)
		command.Dir = root
		if output, e := command.CombinedOutput(); e != nil {
			t.Fatalf("actual MessageSender HTTP journey: %v\n%s", e, output)
		}
	} else {
		for _, event := range fixture.Events {
			body, _ := json.Marshal(map[string]interface{}{"topic_id": "p2p_usr275_usr574", "type": "memory_branch", "content": event})
			res, e := http.Post(handler.URL+"/api/messages/send", "application/json", bytes.NewReader(body))
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatal("runtime snapshot refused", res.StatusCode)
			}
		}
	}
	if len(replay) != len(fixture.Events) {
		t.Fatal("transport lost a snapshot")
	}
	if len(fixture.Events[len(fixture.Events)-1]) <= 2000 {
		t.Fatal("fixture does not exercise old clipping boundary")
	}
	for i, event := range fixture.Events {
		var expected map[string]interface{}
		json.Unmarshal(event, &expected)
		for _, field := range []string{"nodes", "edges", "read_refs", "injected_refs", "answer_refs", "sequence", "origin_turn_id", "consumed_turn_id"} {
			a, _ := json.Marshal(expected[field])
			b, _ := json.Marshal(replay[i][field])
			if !bytes.Equal(a, b) {
				t.Fatalf("history changed %s at event %d", field, i)
			}
		}
	}
	last := replay[len(replay)-1]
	if last["status"] != "answered" || len(last["answer_refs"].([]interface{})) != 1 {
		t.Fatal("answer reference update lost")
	}
	bad := map[string]interface{}{}
	json.Unmarshal(fixture.Events[len(fixture.Events)-1], &bad)
	bad["token"] = "PRIVATE_CAPABILITY_MUST_NOT_PERSIST"
	if _, ok := normalizedMemoryBranch(bad); ok {
		t.Fatal("private extension admitted")
	}
	delete(bad, "token")
	bad["schema"] = "catsco.memory_branch.v99"
	if _, ok := normalizedMemoryBranch(bad); ok {
		t.Fatal("unknown schema admitted")
	}
	encoded, _ := json.Marshal(replay)
	if strings.Contains(string(encoded), "Only audited production releases") || strings.Contains(string(encoded), "External rollback behavior") {
		t.Fatal("source bodies leaked into public source blocks")
	}
	os.MkdirAll("/tmp/catsco-branch-sources", 0755)
	artifact, _ := json.MarshalIndent(map[string]interface{}{"wire": wire, "history_blocks": replay, "actual_message_sender": os.Getenv("XIAOBA_BRANCH_WORKTREE") != "", "external_provider_calls": 0}, "", "  ")
	if e := os.WriteFile("/tmp/catsco-branch-sources/wire-replay.json", artifact, 0644); e != nil {
		t.Fatal(e)
	}
}
