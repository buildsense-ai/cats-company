package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

type repairRuntimeCapturedCandidate struct {
	EntryID            string
	RequestHash        string
	SourceSnapshotHash string
	RequestBytes       int64
	Body               []byte
}

func startRepairSourcePairedRuntime(t *testing.T, mode string) *repairSourcePairedRuntime {
	t.Helper()
	xiaoba, node := os.Getenv("XIAOBA_REPAIR_PAIRED_ROOT"), os.Getenv("XIAOBA_REPAIR_PAIRED_NODE")
	if xiaoba == "" || node == "" {
		if os.Getenv("CATSCO_REQUIRE_REPAIR_RUNTIME_CLOUD_CONTRACT") == "1" {
			t.Fatal("XIAOBA_REPAIR_PAIRED_ROOT and XIAOBA_REPAIR_PAIRED_NODE are mandatory for the combined contract")
		}
		t.Skip("requires XIAOBA_REPAIR_PAIRED_ROOT and XIAOBA_REPAIR_PAIRED_NODE; use scripts/test-publication-repair-source-paired.mjs for mandatory pinned Runtime contract")
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

func (r *repairSourcePairedRuntime) candidate(t *testing.T, entryID string) repairRuntimeCapturedCandidate {
	t.Helper()
	var response struct {
		RequestBody string `json:"requestBody"`
		Receipt     struct {
			EntryID            string `json:"entryId"`
			RequestHash        string `json:"requestHash"`
			SourceSnapshotHash string `json:"sourceSnapshotHash"`
			RequestBytes       int64  `json:"requestBytes"`
		} `json:"receipt"`
	}
	r.call(t, map[string]interface{}{"action": "candidate", "entryId": entryID}, &response)
	body, err := base64.StdEncoding.DecodeString(response.RequestBody)
	if err != nil || len(body) == 0 || response.Receipt.EntryID != entryID || response.Receipt.RequestBytes != int64(len(body)) || response.Receipt.RequestHash != publicationDescriptionSHA256Hex(string(body)) {
		t.Fatal("invalid Runtime-captured candidate export", err)
	}
	return repairRuntimeCapturedCandidate{EntryID: response.Receipt.EntryID, RequestHash: response.Receipt.RequestHash,
		SourceSnapshotHash: response.Receipt.SourceSnapshotHash, RequestBytes: response.Receipt.RequestBytes, Body: body}
}

func repairSourcePairedFixture(t *testing.T, r *repairSourcePairedRuntime, capture bool) (*publicationWorkspaceFixture, *repairSourceTestStore) {
	return repairSourcePairedFixtureEntryID(t, r, capture, publicationReadTestID)
}

func repairSourcePairedFixtureEntryID(t *testing.T, r *repairSourcePairedRuntime, capture bool, entryID string) (*publicationWorkspaceFixture, *repairSourceTestStore) {
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
			Payload: map[string]interface{}{"entry_id": entryID, "bot_uid": "42", "local_skill_id": source.LocalSkillID, "workspace_generation": source.WorkspaceGeneration}})
		if reply.Error != nil {
			t.Fatalf("initial real capture failed: %+v", reply.Error)
		}
		result, ok := reply.Result.(map[string]interface{})
		if !ok || len(result) != 6 {
			t.Fatal("wrong capture contract")
		}
		q := &s.app.Input.Receipt.Query
		q.EntryID = entryID
		q.RequestHash = result["requestHash"].(string)
		q.SourceSnapshotHash = result["sourceSnapshotHash"].(string)
		q.RequestBytes = int64(result["requestBytes"].(float64))
		s.app.Input.Draft.Evidence.CandidateHash = q.SourceSnapshotHash
		s.app = repairSourceApplicationForEntryID(t, s.app, entryID)
		s.capture.Input.RequestHash, s.capture.Input.SourceSnapshotHash, s.capture.Input.RequestBytes = q.RequestHash, q.SourceSnapshotHash, q.RequestBytes
		s.capture.Input.EntryID = entryID
		entry := s.entries["repair-source-fixture"]
		entry.ID = entryID
	}
	s.task = repairSourceTask(t, s.app)
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

func repairTaskSourcePairedCall(t *testing.T, f *publicationWorkspaceFixture, task store.SkillHubPublicationRepairTask, r *repairSourcePairedRuntime) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- f.h.publicationRepairTaskSourceVerifier(7)(ctx, snapshotPublicationRepairTask(task), task.Rounds[len(task.Rounds)-1].Lease)
	}()
	req := receiveRepairSourceCall(t, f, done)
	reply := r.rpc(t, req)
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

func TestSkillHubPublicationRepairSourcePairedTaskLease(t *testing.T) {
	r := startRepairSourcePairedRuntime(t, "normal")
	f, s := repairSourcePairedFixture(t, r, true)
	candidate := r.candidate(t, s.capture.Input.EntryID)
	if candidate.RequestHash != s.capture.Input.RequestHash || candidate.SourceSnapshotHash != s.capture.Input.SourceSnapshotHash || candidate.RequestBytes != s.capture.Input.RequestBytes {
		t.Fatal("exported Runtime candidate differs from the frozen capture receipt")
	}
	task := snapshotPublicationRepairTask(s.task)
	if err := repairTaskSourcePairedCall(t, f, task, r); err != nil {
		t.Fatal("real Runtime rejected current task/lease source proof", err)
	}
	before := s.capture
	r.action(t, "remove")
	if err := repairTaskSourcePairedCall(t, f, task, r); err == nil {
		t.Fatal("task/lease verification replayed a removed source")
	}
	if s.capture != before || s.creates != 1 {
		t.Fatal("live task source verification rewrote the original capture or entry")
	}
	t.Log("real XiaoBa Runtime RPC + current task/lease authority: live source accepted, removed original source rejected on a later task check; no capture/entry mutation, model, or file writes")
}

func TestSkillHubPublicationRepairSourcePairedDraftRecovery(t *testing.T) {
	for _, mode := range []string{"waiting-expired", "confirmed-expired", "source-removed", "authority-revoked"} {
		t.Run(mode, func(t *testing.T) {
			r := startRepairSourcePairedRuntime(t, "normal")
			f, s := repairSourcePairedFixture(t, r, true)
			reader := repairDraftSourceFixture(t, s, mode == "confirmed-expired", true)
			f.h.publicationRepairSource.drafts = reader
			task := snapshotPublicationRepairTask(s.task)
			before := s.capture
			if mode == "source-removed" {
				r.action(t, "remove")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.h.publicationRepairDescriptionSourceVerifier(7)(ctx, task) }()
			req := receiveRepairSourceCall(t, f, done)
			reply := r.rpc(t, req)
			if mode == "authority-revoked" {
				reader.denied = true
			}
			f.h.handleThinToolRPCResult(f.runtime, reply)
			err := waitRepairSource(t, done)
			status := http.StatusOK
			switch mode {
			case "source-removed":
				status = http.StatusUnprocessableEntity
				if err == nil {
					t.Fatal("removed original source authorized draft recovery")
				}
			case "authority-revoked":
				status = http.StatusConflict
				if !errors.Is(err, store.ErrSkillHubPublicationDenied) {
					t.Fatal("current draft authority loss ignored", err)
				}
			default:
				if err != nil {
					t.Fatal("expired finished draft source recovery", err)
				}
			}
			deliveryAckStatus(t, f.runtime, status)
			assertRepairSourceIdle(t, f)
			if s.capture != before || !reflect.DeepEqual(s.task, task) {
				t.Fatal("draft recovery changed task or source capture")
			}
		})
	}
	t.Log("real XiaoBa Runtime verifies original source for waiting/confirmed expired draft recovery; required CI pins Runtime; removed source and current authority loss denied; task/draft authority synthetic, no execution lease/model/write")
}

func TestPublicationRepairModelCoordinatorPairedLiveRuntimeFence(t *testing.T) {
	for _, removeAtGrant := range []bool{false, true} {
		name := "source-retained-through-dispatch"
		if removeAtGrant {
			name = "source-removed-after-grant"
		}
		t.Run(name, func(t *testing.T) {
			r := startRepairSourcePairedRuntime(t, "normal")
			f, s := repairSourcePairedFixture(t, r, true)
			task := snapshotPublicationRepairTask(s.task)
			task.Rounds[0].Lease.Until = time.Now().Add(2 * time.Minute)
			report := publicationReportFixture(task.Input.Receipt).Validation
			evidence, err := publicationRepairEvidence(report, task.Input.Evidence.CandidateHash)
			if err != nil {
				t.Fatal("paired task/report fixture mismatch", err)
			}
			// Bind the synthetic model task/lease to this exact Cloud-shaped
			// report; Runtime source authority itself is still exercised live.
			task.Input.Evidence = evidence
			task.Rounds[0].Lease.Input.Evidence = evidence
			task.IdentityHash, err = task.Input.IdentityHash()
			if err != nil {
				t.Fatal("paired task identity", err)
			}
			lease := task.Rounds[0].Lease
			s.task = task
			db := &repairModelTestStore{task: task}
			var invocations, verifications int
			ports := publicationRepairModelPorts{
				VerifySource: func(_ context.Context, got store.SkillHubPublicationRepairTask, gotLease store.SkillHubPublicationRepairLease) error {
					verifications++
					if got.ID != task.ID || got.IdentityHash != task.IdentityHash || !samePublicationRepairModelLease(gotLease, lease) {
						t.Fatal("coordinator changed the live Runtime task/lease binding")
					}
					return repairTaskSourcePairedCall(t, f, got, r)
				},
				PrepareMaterial: func(_ context.Context, got store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
					if got.ID != task.ID || got.Input.Receipt != task.Input.Receipt {
						t.Fatal("coordinator selected different synthetic material task")
					}
					return publicationRepairModelMaterial{SourceText: "untrusted synthetic source", Report: report}, nil
				},
				InvokeOnce: func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					invocations++
					return store.SkillHubPublicationRepairModelOutput{Text: "synthetic draft"}, nil
				},
			}
			db.startHook = func(_ *store.SkillHubPublicationRepairModelCall, _ *bool) {
				if removeAtGrant {
					r.action(t, "remove")
				}
			}
			result, err := runPublicationRepairModel(context.Background(), db, 7, lease,
				publicationRepairModelConfig{Provider: "synthetic-provider", Model: "synthetic-model", MaxOutputTokens: 512}, ports)
			if removeAtGrant {
				if err == nil || result != nil || invocations != 0 || db.starts != 1 || verifications != 3 {
					t.Fatal("removed live source crossed post-grant fence", result, err, invocations, db.starts, verifications)
				}
				t.Log("live Runtime source removed after grant was denied before synthetic provider invocation")
			} else {
				if err != nil || result == nil || invocations != 1 || db.starts != 1 || verifications != 5 {
					t.Fatal("coordinator did not preserve source through one synthetic dispatch and five fences", result, err, invocations, db.starts, verifications)
				}
				t.Log("live Runtime source retained through five coordinator fences; one synthetic provider invocation")
			}
		})
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
