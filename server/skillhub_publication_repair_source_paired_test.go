package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Real XiaoBa handler/storage; synthetic CatsCo authority/store and socket.
// CI invokes the strict runner, which pins Runtime and rejects every skip.
type repairSourcePairedRuntime struct {
	in     io.WriteCloser
	out    *json.Decoder
	source map[string]interface{}
}

func startRepairSourcePairedRuntime(t *testing.T, mode string) *repairSourcePairedRuntime {
	t.Helper()
	xiaoba, node := os.Getenv("XIAOBA_REPAIR_PAIRED_ROOT"), os.Getenv("XIAOBA_REPAIR_PAIRED_NODE")
	if xiaoba == "" || node == "" {
		t.Skip("use scripts/test-publication-repair-source-paired.mjs for mandatory pinned Runtime contract")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	cmd := exec.CommandContext(ctx, node, filepath.Join("..", "scripts", "fixtures", "publication-repair-source-runtime.mjs"), xiaoba, t.TempDir(), mode)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Runtime failed: %v\n%s", err, stderr.String())
			}
		case <-time.After(5 * time.Second):
			cancel()
			<-done
			t.Errorf("Runtime did not exit\n%s", stderr.String())
		}
		cancel()
	})
	r := &repairSourcePairedRuntime{in: in, out: json.NewDecoder(out)}
	var ready struct {
		Source map[string]interface{} `json:"source"`
	}
	if err := r.out.Decode(&ready); err != nil {
		t.Fatal("Runtime readiness:", err)
	}
	if len(ready.Source) != 2 {
		t.Fatal("invalid Runtime descriptor")
	}
	r.source = ready.Source
	return r
}

func (r *repairSourcePairedRuntime) call(t *testing.T, input, output interface{}) {
	t.Helper()
	if err := json.NewEncoder(r.in).Encode(input); err != nil {
		t.Fatal(err)
	}
	if err := r.out.Decode(output); err != nil {
		t.Fatal(err)
	}
}

func (r *repairSourcePairedRuntime) rpc(t *testing.T, req *MsgThinToolRPC) *MsgThinToolRPC {
	t.Helper()
	var result struct {
		Reply *MsgThinToolRPC `json:"reply"`
	}
	r.call(t, map[string]interface{}{"action": "rpc", "request": req}, &result)
	if result.Reply == nil {
		t.Fatal("missing Runtime reply")
	}
	return result.Reply
}

func (r *repairSourcePairedRuntime) action(t *testing.T, action string) {
	t.Helper()
	var result struct {
		OK bool `json:"ok"`
	}
	r.call(t, map[string]interface{}{"action": action}, &result)
	if !result.OK {
		t.Fatal("Runtime action failed:", action)
	}
}

func repairSourcePairedFixture(t *testing.T, r *repairSourcePairedRuntime, capture bool) (*publicationWorkspaceFixture, *repairSourceTestStore) {
	t.Helper()
	f, s := newRepairSourceFixture(t)
	source := s.entries["repair-source-fixture"].Input.Source
	source.LocalSkillID = r.source["local_skill_id"].(string)
	source.WorkspaceGeneration = r.source["workspace_generation"].(string)
	s.entries["repair-source-fixture"].Input.Source = source
	s.capture.Input.Binding.Source = source
	if capture {
		reply := r.rpc(t, &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: "pubcapture_paired_fixture_12345678", ToolName: string(DeviceCapabilitySkillHubPublicationCapture),
			TargetOwnerUserID: "usr7", TargetDeviceID: "install-42", DeviceID: "install-42", ExpiresAt: unixMillis(time.Now().Add(20 * time.Second)),
			Payload: map[string]interface{}{"entry_id": publicationReadTestID, "bot_uid": "42", "local_skill_id": source.LocalSkillID, "workspace_generation": source.WorkspaceGeneration}})
		if reply.Error != nil {
			t.Fatalf("initial real capture failed: %+v", reply.Error)
		}
		result, ok := reply.Result.(map[string]interface{})
		if !ok || len(result) != 6 {
			t.Fatal("wrong capture contract")
		}
		q := &s.app.Input.Receipt.Query
		q.RequestHash = result["requestHash"].(string)
		q.SourceSnapshotHash = result["sourceSnapshotHash"].(string)
		q.RequestBytes = int64(result["requestBytes"].(float64))
		s.app.Input.Draft.Evidence.CandidateHash = q.SourceSnapshotHash
		s.app = repairSourceApplication(t, s.app)
		s.capture.Input.RequestHash, s.capture.Input.SourceSnapshotHash, s.capture.Input.RequestBytes = q.RequestHash, q.SourceSnapshotHash, q.RequestBytes
	}
	return f, s
}

func repairSourcePairedCall(t *testing.T, f *publicationWorkspaceFixture, s *repairSourceTestStore, r *repairSourcePairedRuntime) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, done := startRepairSourceCall(t, f, ctx, s.app)
	reply := r.rpc(t, req) // unchanged CatsCo envelope; no test-side field repair
	f.h.handleThinToolRPCResult(f.runtime, reply)
	err := waitRepairSource(t, done)
	status := http.StatusUnprocessableEntity
	if err == nil {
		status = http.StatusOK
	}
	deliveryAckStatus(t, f.runtime, status)
	assertRepairSourceIdle(t, f)
	return err
}

func TestSkillHubPublicationRepairSourcePairedReplay(t *testing.T) {
	for _, mode := range []string{"normal", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			r := startRepairSourcePairedRuntime(t, mode)
			f, s := repairSourcePairedFixture(t, r, true)
			f.h.removeClient(f.owner)
			before := s.capture
			for i := 0; i < 2; i++ {
				if i > 0 {
					r.action(t, "restart")
					f.h.publicationRepairSource = newPublicationRepairSourceTransport(f.h)
				}
				if err := repairSourcePairedCall(t, f, s, r); err != nil {
					t.Fatal(err)
				}
			}
			// Ordinary capture replays frozen bytes after source removal. This
			// lane must instead re-read the source on EVERY invocation.
			r.action(t, "remove")
			if err := repairSourcePairedCall(t, f, s, r); err == nil {
				t.Fatal("replay bypassed live source fence")
			}
			if s.capture != before || s.creates != 1 {
				t.Fatal("verification rewrote capture/entry")
			}
		})
	}
}

func TestSkillHubPublicationRepairSourcePairedLaterRounds(t *testing.T) {
	for _, round := range []int{2, 3} {
		for _, changed := range []bool{false, true} {
			name := fmt.Sprintf("round-%d", round)
			if changed {
				name += "-source-changed"
			}
			t.Run(name, func(t *testing.T) {
				r := startRepairSourcePairedRuntime(t, "normal")
				f, s := repairSourcePairedFixture(t, r, true)
				s.app = repairSourceApplicationAtRound(t, s.app, round)
				original := s.capture
				if s.app.Input.Draft.Evidence.CandidateHash == original.Input.SourceSnapshotHash {
					t.Fatal("fixture has no private-copy progress")
				}
				if changed {
					r.action(t, "change")
				}
				err := repairSourcePairedCall(t, f, s, r)
				if changed {
					if !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
						t.Fatal("changed original source accepted in later round", err)
					}
				} else if err != nil {
					t.Fatal("unchanged original source rejected in later round", err)
				}
				if s.capture != original || s.creates != 1 {
					t.Fatal("later round replaced original capture/entry")
				}
			})
		}
	}
}

func TestSkillHubPublicationRepairSourcePairedFailures(t *testing.T) {
	for _, action := range []string{"change", "remove", "disable", "owner", "switching", "missing-capture", "mismatch"} {
		t.Run(action, func(t *testing.T) {
			r := startRepairSourcePairedRuntime(t, "normal")
			f, s := repairSourcePairedFixture(t, r, action != "missing-capture")
			switch action {
			case "mismatch":
				s.app.Input.Receipt.Query.RequestHash = strings.Repeat("f", 64)
				s.app = repairSourceApplication(t, s.app)
				s.capture.Input.RequestHash = s.app.Input.Receipt.Query.RequestHash
			case "missing-capture":
			default:
				r.action(t, action)
			}
			want := store.ErrSkillHubPublicationInvalid
			if action == "switching" {
				want = store.ErrSkillHubPublicationBusy
			}
			if err := repairSourcePairedCall(t, f, s, r); !errors.Is(err, want) {
				t.Fatalf("accepted unsafe source or wrong retry policy: %v", err)
			}
			if action == "switching" {
				r.action(t, "recover")
				if err := repairSourcePairedCall(t, f, s, r); err != nil {
					t.Fatal("same receipt failed after recovery:", err)
				}
			}
		})
	}
}
