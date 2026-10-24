package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type repairSourceTestStore struct {
	*publicationWorkspaceTestStore
	guard       sync.Mutex
	app         store.SkillHubPublicationDescriptionApplication
	capture     store.SkillHubPublicationCaptureRecord
	denied      bool
	missing     bool
	gets, reads int
	onGet       func(int) error
	onRead      func(int) error
	onConfig    func(int)
	configs     int
}

func (s *repairSourceTestStore) GetBotConfig(uid int64) (*types.BotConfig, error) {
	s.guard.Lock()
	s.configs++
	n, hook, c := s.configs, s.onConfig, s.config
	s.guard.Unlock()
	if hook != nil {
		hook(n)
	}
	return &c, nil
}

func (s *repairSourceTestStore) GetSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionApplication, error) {
	s.guard.Lock()
	s.gets++
	n, hook := s.gets, s.onGet
	denied := s.denied || actor != 7 || id != s.app.Input.Receipt.Query.EntryID
	app := snapshotPublicationDescriptionApplication(s.app)
	s.guard.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if denied {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if hook != nil {
		if err := hook(n); err != nil {
			return nil, err
		}
	}
	return &app, nil
}

func (s *repairSourceTestStore) ReadSkillHubPublicationCapture(ctx context.Context, id string, _ store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationCaptureRecord, error) {
	s.guard.Lock()
	s.reads++
	n, hook, missing, capture := s.reads, s.onRead, s.missing, s.capture
	s.guard.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hook != nil {
		if err := hook(n); err != nil {
			return nil, err
		}
	}
	if missing || id != capture.Input.EntryID {
		return nil, nil
	}
	return &capture, nil
}

func (s *repairSourceTestStore) GetSkillHubPublicationEntry(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, entry := range s.entries {
		if entry.ID == id && entry.Input.ActorUID == actor {
			value := *entry
			return &value, nil
		}
	}
	return nil, nil
}

func repairSourceApplication(t *testing.T, app store.SkillHubPublicationDescriptionApplication) store.SkillHubPublicationDescriptionApplication {
	t.Helper()
	app = snapshotPublicationDescriptionApplication(app)
	app.Input.Receipt.Query.EntryID = publicationReadTestID
	task := store.SkillHubPublicationRepairInput{Receipt: app.Input.Receipt, TopicID: "grp_123", Evidence: app.Input.Draft.Evidence}
	var err error
	app.Input.Draft.TaskIdentityHash, err = task.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.Input.ConfirmationKey, err = app.Input.Draft.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.IdentityHash, err = app.Input.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.ID = "pubdesc_" + app.IdentityHash
	if app.Result != nil {
		app.Result.ApplicationID = app.ID
	}
	if !validPublicationRepairSourceApplication(app) {
		t.Fatal("invalid application fixture")
	}
	return app
}

// Construct a real store-valid current-round application. The task's initial
// receipt/evidence stay immutable; each rejected round advances private-copy
// evidence into the next lease. Nothing writes the Runtime's original source.
func repairSourceApplicationAtRound(t *testing.T, input store.SkillHubPublicationDescriptionApplication, round int) store.SkillHubPublicationDescriptionApplication {
	t.Helper()
	app := snapshotPublicationDescriptionApplication(input)
	if round < 1 || round > store.SkillHubPublicationRepairMaxRounds || app.Input.Draft.Round != 1 {
		t.Fatal("round fixture requires the original round-1 application")
	}
	initial := store.SkillHubPublicationRepairInput{Receipt: app.Input.Receipt, TopicID: "grp_123", Evidence: app.Input.Draft.Evidence}
	taskHash, err := initial.IdentityHash()
	if err != nil || taskHash != app.Input.Draft.TaskIdentityHash {
		t.Fatal("invalid initial task binding", err)
	}
	task := store.SkillHubPublicationRepairTask{ID: app.Input.Draft.TaskID, Input: initial, IdentityHash: taskHash, CreatedAt: app.CreatedAt.Add(-time.Second)}
	evidence := initial.Evidence
	var roundHash string
	for n := 1; n <= round; n++ {
		in := store.SkillHubPublicationRepairRoundInput{RequestID: fmt.Sprintf("source-check-round-%d", n), Evidence: evidence}
		roundHash, err = in.IdentityHash()
		if err != nil {
			t.Fatal(err)
		}
		lease := store.SkillHubPublicationRepairLease{EntryID: initial.Receipt.Query.EntryID, TaskID: task.ID, Round: n, Input: in,
			WorkerID: "source-check-worker", Token: strings.Repeat("e", 32), Generation: int64(n), Until: app.CreatedAt.Add(time.Minute)}
		if err := lease.Validate(); err != nil {
			t.Fatal(err)
		}
		result := &store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: evidence}
		if n < round {
			next := store.SkillHubPublicationRepairEvidence{
				CandidateHash: publicationDescriptionSHA256Hex(fmt.Sprintf("private-candidate-after-%d", n)),
				ReportHash:    publicationDescriptionSHA256Hex(fmt.Sprintf("private-report-after-%d", n)),
				IssueSetHash:  publicationDescriptionSHA256Hex(fmt.Sprintf("remaining-issues-after-%d", n))}
			result = &store.SkillHubPublicationRepairResult{Outcome: "rejected", Evidence: next}
			evidence = next
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		task.Rounds = append(task.Rounds, store.SkillHubPublicationRepairRound{Lease: lease, Result: result})
		if n < round && task.State() != "queued" {
			t.Fatal("fixture did not advance repair evidence")
		}
	}
	app.Input.Draft.Round, app.Input.Draft.RoundIdentityHash, app.Input.Draft.Evidence = round, roundHash, evidence
	app.Input.ConfirmationKey, err = app.Input.Draft.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.IdentityHash, err = app.Input.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.ID = "pubdesc_" + app.IdentityHash
	if app.Result != nil {
		app.Result.ApplicationID = app.ID
		app.Result.Derivation.OriginalCandidateHash = evidence.CandidateHash
	}
	confirmed := app.CreatedAt.Add(-time.Millisecond)
	draft := store.SkillHubPublicationDescriptionDraft{EntryID: initial.Receipt.Query.EntryID, Input: app.Input.Draft,
		ConfirmationKey: app.Input.ConfirmationKey, CreatedAt: confirmed.Add(-time.Millisecond), ConfirmedAt: &confirmed}
	if task.State() != "waiting_confirmation" {
		t.Fatal("fixture did not retain confirmation state")
	}
	if err := app.ValidateFor(task, draft); err != nil {
		t.Fatal("real store validator rejected current-round application", err)
	}
	return app
}

func newRepairSourceFixture(t *testing.T) (*publicationWorkspaceFixture, *repairSourceTestStore) {
	t.Helper()
	var s *repairSourceTestStore
	f := newPublicationWorkspaceFixtureWithStore(t, func(base *publicationWorkspaceTestStore) store.Store {
		s = &repairSourceTestStore{publicationWorkspaceTestStore: base}
		return s
	})
	// Full Runtime uses its installation ID as its device ID, matching XiaoBa's
	// reserved RPC envelope. The legacy generic workspace fixture uses aliases.
	device, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: "install-42", BodyID: "body-42", InstallationID: "install-42", RuntimeRole: "server", Status: "online",
		Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe), string(DeviceCapabilitySkillHubPublicationRepairSource)}})
	if err != nil {
		t.Fatal(err)
	}
	f.h.bindDeviceClient(7, device, f.runtime)
	f.device = device
	source := store.SkillHubPublicationSource{DeviceID: device.DeviceID, BodyID: device.BodyID, InstallationID: device.InstallationID, RouteGeneration: 1,
		LocalSkillID: "pubsrc-" + strings.Repeat("a", 48), WorkspaceGeneration: strings.Repeat("b", 64)}
	in := store.SkillHubPublicationEntryInput{ActorUID: 7, BotUID: 42, Entrypoint: "workspace", RequestID: "repair-source-fixture", Source: source}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := s.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy()); err != nil {
		t.Fatal(err)
	}
	fixture, _ := applicationTestFixture(t)
	s.app = repairSourceApplication(t, *fixture.app)
	q := s.app.Input.Receipt.Query
	s.capture = store.SkillHubPublicationCaptureRecord{CapturedAt: time.Now(), Input: store.SkillHubPublicationCaptureInput{EntryID: publicationReadTestID,
		Binding:  store.SkillHubPublicationDeliveryBinding{ActorUID: 7, BotUID: 42, Source: source, NodeID: f.h.nodeID, ConnectionID: f.runtime.connectionID},
		Revision: q.Revision, RequestHash: q.RequestHash, SourceSnapshotHash: q.SourceSnapshotHash, RequestBytes: q.RequestBytes}}
	f.h.publicationRepairSource = newPublicationRepairSourceTransport(f.h)
	if f.h.publicationRepairSource == nil {
		t.Fatal("missing repair source transport")
	}
	return f, s
}

func startRepairSourceCall(t *testing.T, f *publicationWorkspaceFixture, ctx context.Context, app store.SkillHubPublicationDescriptionApplication) (*MsgThinToolRPC, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f.h.publicationDescriptionSourceVerifier(7)(ctx, app) }()
	return receiveRepairSourceCall(t, f, done), done
}

func receiveRepairSourceCall(t *testing.T, f *publicationWorkspaceFixture, done <-chan error) *MsgThinToolRPC {
	t.Helper()
	select {
	case raw := <-f.runtime.send:
		var message ServerMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatal(err)
		}
		if message.ThinToolRPC == nil || !strings.HasPrefix(message.ThinToolRPC.RequestID, publicationRepairSourceRequestPrefix) {
			t.Fatal("wrong dispatch")
		}
		return message.ThinToolRPC
	case err := <-done:
		t.Fatalf("returned before dispatch: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("repair dispatch timeout")
	}
	return nil
}

func repairSourceReply(req *MsgThinToolRPC) *MsgThinToolRPC {
	expected := req.Payload["expected_capture"].(map[string]interface{})
	result := make(map[string]interface{}, 7)
	for key, value := range expected {
		result[key] = value
	}
	result["sourceVerified"] = true
	return &MsgThinToolRPC{ID: "repair-response", Type: thinToolRPCTypeResult, RequestID: req.RequestID, ToolName: req.ToolName,
		TargetOwnerUserID: req.TargetOwnerUserID, TargetDeviceID: req.TargetDeviceID, DeviceID: req.DeviceID, Result: result}
}

func waitRepairSource(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("repair did not settle")
		return nil
	}
}

func waitRepairSourceAck(t *testing.T, client *Client, status int) {
	t.Helper()
	select {
	case raw := <-client.send:
		var msg ServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Ctrl == nil || msg.Ctrl.Code != status {
			t.Fatalf("unexpected repair ACK: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("repair ACK timeout")
	}
}

func assertRepairSourceIdle(t *testing.T, f *publicationWorkspaceFixture) {
	t.Helper()
	w := f.h.publicationRepairSource
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != 0 || len(w.active) != 0 || len(w.slots) != 0 {
		t.Fatal("leaked repair source capacity")
	}
}

func TestSkillHubPublicationRepairSourceTransportPrivateEvidence(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	f.h.removeClient(f.owner) // Human need not keep a browser socket online.
	before, capture := snapshotPublicationDescriptionApplication(s.app), s.capture
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startRepairSourceCall(t, f, ctx, s.app)
	if req.ToolName != string(DeviceCapabilitySkillHubPublicationRepairSource) || len(req.Payload) != 5 || req.Payload["bot_uid"] != "42" ||
		req.Payload["entry_id"] != publicationReadTestID || len(req.Payload["expected_capture"].(map[string]interface{})) != 6 {
		t.Fatal("unexpected contract")
	}
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	if err := waitRepairSource(t, done); err != nil {
		t.Fatal(err)
	}
	deliveryAckStatus(t, f.runtime, http.StatusOK)
	if !reflect.DeepEqual(s.app, before) || s.capture != capture || s.creates != 1 || s.reads != 2 || s.gets < 4 || len(f.owner.send) != 0 {
		t.Fatal("mutated state or leaked receipt")
	}
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	deliveryAckStatus(t, f.runtime, http.StatusNotFound)
	assertRepairSourceIdle(t, f)
}

func TestSkillHubPublicationRepairSourceTransportLaterRounds(t *testing.T) {
	for _, round := range []int{2, 3} {
		for _, mode := range []string{"prepared", "completed", "forged-capture", "unretained-evidence"} {
			t.Run(fmt.Sprintf("round-%d/%s", round, mode), func(t *testing.T) {
				f, s := newRepairSourceFixture(t)
				base := s.app
				if mode == "completed" {
					base = repairSourceApplication(t, completedDescriptionApplication(t, "https://hub.example", []byte("retained-private-copy")))
				}
				s.app = repairSourceApplicationAtRound(t, base, round)
				q := s.app.Input.Receipt.Query
				s.capture.Input.RequestHash, s.capture.Input.SourceSnapshotHash, s.capture.Input.RequestBytes = q.RequestHash, q.SourceSnapshotHash, q.RequestBytes
				candidate := s.app.Input.Draft.Evidence.CandidateHash
				if candidate == q.SourceSnapshotHash {
					t.Fatal("fixture did not advance the private candidate")
				}
				before, capture := snapshotPublicationDescriptionApplication(s.app), s.capture
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if mode == "unretained-evidence" {
					unretained := snapshotPublicationDescriptionApplication(s.app)
					unretained.Input.Draft.Evidence.CandidateHash = publicationDescriptionSHA256Hex("unretained-private-copy")
					var err error
					unretained.Input.ConfirmationKey, err = unretained.Input.Draft.IdentityHash()
					if err != nil {
						t.Fatal(err)
					}
					unretained.IdentityHash, err = unretained.Input.IdentityHash()
					if err != nil {
						t.Fatal(err)
					}
					unretained.ID = "pubdesc_" + unretained.IdentityHash
					if !validPublicationRepairSourceApplication(unretained) {
						t.Fatal("fixture failed before authoritative application checkpoint")
					}
					if err := f.h.verifyPublicationDescriptionSource(ctx, 7, unretained); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
						t.Fatal("unretained candidate bypassed authoritative application checkpoint", err)
					}
					if len(f.runtime.send) != 0 || !reflect.DeepEqual(s.app, before) || s.capture != capture || s.creates != 1 {
						t.Fatal("unretained candidate dispatched or changed retained state")
					}
					assertRepairSourceIdle(t, f)
					return
				}
				req, done := startRepairSourceCall(t, f, ctx, s.app)
				if req.Payload["expected_capture"].(map[string]interface{})["sourceSnapshotHash"] != q.SourceSnapshotHash {
					t.Fatal("replaced original source with private-copy evidence")
				}
				reply := repairSourceReply(req)
				if mode == "forged-capture" {
					reply.Result.(map[string]interface{})["sourceSnapshotHash"] = candidate
				}
				f.h.handleThinToolRPCResult(f.runtime, reply)
				err := waitRepairSource(t, done)
				status := http.StatusOK
				if mode == "forged-capture" {
					if !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
						t.Fatal("accepted private candidate as Runtime source proof", err)
					}
					status = http.StatusUnprocessableEntity
				} else if err != nil {
					t.Fatal("rejected store-valid later round", err)
				}
				deliveryAckStatus(t, f.runtime, status)
				if !reflect.DeepEqual(s.app, before) || s.capture != capture || s.creates != 1 {
					t.Fatal("source check mutated the application, capture or budget")
				}
				assertRepairSourceIdle(t, f)
			})
		}
	}
}

func TestSkillHubPublicationRepairSourceTransportDefaultOffReserved(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	keys := []string{"CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED", "CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "CATSCO_SKILLHUB_PUBLICATION_CAPTURE_ENABLED", "CATSCO_SKILLHUB_PUBLICATION_REPAIR_SOURCE_ENABLED"}
	for _, key := range keys {
		t.Setenv(key, "true")
	}
	if newPublicationRepairSourceTransportFromEnv(f.h) == nil {
		t.Fatal("explicit enable failed")
	}
	for _, key := range keys {
		t.Setenv(key, "")
		if newPublicationRepairSourceTransportFromEnv(f.h) != nil {
			t.Fatal("gate bypass", key)
		}
		t.Setenv(key, "true")
	}
	req := publicationWorkspaceRequest()
	req.ToolName = string(DeviceCapabilitySkillHubPublicationRepairSource)
	f.h.handleThinToolRPCRequest(f.owner, req)
	if len(f.runtime.send) != 0 {
		t.Fatal("browser invoked reserved operation")
	}
	f.h.publicationRepairSource = nil
	if err := f.h.publicationDescriptionSourceVerifier(7)(context.Background(), s.app); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal(err)
	}
	f.h.handleThinToolRPCResult(f.runtime, &MsgThinToolRPC{ID: "disabled", Type: thinToolRPCTypeResult, RequestID: publicationRepairSourceRequestPrefix + "missing"})
	deliveryAckStatus(t, f.runtime, http.StatusServiceUnavailable)
}

func TestSkillHubPublicationRepairSourceTransportRejectsUntrustedFacts(t *testing.T) {
	for _, mode := range []string{"actor", "identity", "confirmation", "application", "cancelled", "missing-capture", "hash", "bytes", "capture-owner", "capture-body", "capture-connection", "capture-generation", "no-capability", "before-send"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			app := snapshotPublicationDescriptionApplication(s.app)
			actor := int64(7)
			switch mode {
			case "actor":
				actor = 8
			case "identity":
				app.IdentityHash = strings.Repeat("f", 64)
			case "confirmation":
				app.Input.ConfirmationKey = strings.Repeat("f", 64)
			case "application":
				s.app.CreatedAt = s.app.CreatedAt.Add(time.Second)
			case "cancelled":
				s.denied = true
			case "missing-capture":
				s.missing = true
			case "hash":
				s.capture.Input.RequestHash = strings.Repeat("f", 64)
			case "bytes":
				s.capture.Input.RequestBytes++
			case "capture-owner":
				s.capture.Input.Binding.ActorUID = 8
			case "capture-body":
				s.capture.Input.Binding.Source.BodyID = "other-body"
			case "capture-connection":
				s.capture.Input.Binding.ConnectionID = "other-connection"
			case "capture-generation":
				s.capture.Input.Binding.Source.RouteGeneration++
			case "no-capability":
				_, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe)}})
				if err != nil {
					t.Fatal(err)
				}
			case "before-send":
				s.onGet = func(n int) error {
					if n == 2 {
						return store.ErrSkillHubPublicationDenied
					}
					return nil
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := f.h.verifyPublicationDescriptionSource(ctx, actor, app); err == nil {
				t.Fatal("accepted untrusted facts")
			}
			if len(f.runtime.send) != 0 {
				t.Fatal("unauthorized Runtime send")
			}
			assertRepairSourceIdle(t, f)
		})
	}
}

func TestSkillHubPublicationRepairSourceTransportRejectsForgedResults(t *testing.T) {
	for _, mode := range []string{"old-capture", "extra-bytes", "hash", "size", "revision", "entry", "verified-string", "verified-false", "payload", "owner", "device", "type", "error", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, done := startRepairSourceCall(t, f, ctx, s.app)
			reply := repairSourceReply(req)
			result := reply.Result.(map[string]interface{})
			switch mode {
			case "old-capture":
				delete(result, "sourceVerified")
				reply.ToolName = string(DeviceCapabilitySkillHubPublicationCapture)
			case "extra-bytes":
				result["bytes"] = "private-source"
			case "hash":
				result["requestHash"] = strings.Repeat("f", 64)
			case "size":
				result["requestBytes"] = float64(100.5)
			case "revision":
				result["candidateRevision"] = float64(2)
			case "entry":
				result["entryId"] = "pubentry_" + strings.Repeat("f", 32)
			case "verified-string":
				result["sourceVerified"] = "true"
			case "verified-false":
				result["sourceVerified"] = false
			case "payload":
				reply.Payload = map[string]interface{}{"path": "private-path"}
			case "owner":
				reply.TargetOwnerUserID = "usr8"
			case "device":
				reply.DeviceID = "other"
			case "type":
				reply.Type = thinToolRPCTypeRequest
			case "error":
				reply.Error = &MsgDeviceRPCError{Code: "publication.source_changed"}
			case "missing":
				delete(result, "sourceSnapshotHash")
			}
			f.h.handleThinToolRPCResult(f.runtime, reply)
			if err := waitRepairSource(t, done); err == nil {
				t.Fatal("accepted forged result")
			}
			deliveryAckStatus(t, f.runtime, http.StatusUnprocessableEntity)
			assertRepairSourceIdle(t, f)
		})
	}
}

func TestSkillHubPublicationRepairSourceTransportRejectsWrongSocket(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startRepairSourceCall(t, f, ctx, s.app)
	f.h.handleThinToolRPCResult(f.owner, repairSourceReply(req))
	deliveryAckStatus(t, f.owner, http.StatusForbidden)
	clone := &Client{uid: f.runtime.uid, accountType: f.runtime.accountType, connectionID: f.runtime.connectionID,
		deviceOwnerUID: f.runtime.deviceOwnerUID, deviceID: f.runtime.deviceID, bodyID: f.runtime.bodyID,
		installationID: f.runtime.installationID, send: f.runtime.send}
	f.h.handleThinToolRPCResult(clone, repairSourceReply(req))
	deliveryAckStatus(t, f.runtime, http.StatusForbidden)
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	if err := waitRepairSource(t, done); err != nil {
		t.Fatal(err)
	}
	deliveryAckStatus(t, f.runtime, http.StatusOK)
}

func TestSkillHubPublicationRepairSourceTransportLateChanges(t *testing.T) {
	for _, mode := range []string{"cancelled", "app", "capture", "owner", "generation", "offline", "capability", "timeout", "context"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			req, done := startRepairSourceCall(t, f, ctx, s.app)
			switch mode {
			case "cancelled":
				s.guard.Lock()
				s.denied = true
				s.guard.Unlock()
			case "app":
				s.guard.Lock()
				s.app.CreatedAt = s.app.CreatedAt.Add(time.Second)
				s.guard.Unlock()
			case "capture":
				s.guard.Lock()
				s.capture.Input.RequestHash = strings.Repeat("f", 64)
				s.guard.Unlock()
			case "owner":
				s.config.OwnerID = 8
			case "generation":
				s.generation++
			case "offline":
				f.h.removeClient(f.runtime)
			case "capability":
				_, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe)}})
				if err != nil {
					t.Fatal(err)
				}
			case "timeout":
				if err := waitRepairSource(t, done); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
				deliveryAckStatus(t, f.runtime, http.StatusNotFound)
				assertRepairSourceIdle(t, f)
				return
			case "context":
				cancel()
				if err := waitRepairSource(t, done); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
				deliveryAckStatus(t, f.runtime, http.StatusNotFound)
				assertRepairSourceIdle(t, f)
				return
			}
			f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
			if err := waitRepairSource(t, done); err == nil {
				t.Fatal("accepted stale authority")
			}
			assertRepairSourceIdle(t, f)
		})
	}
}

func TestSkillHubPublicationRepairSourceTransportBusyDoesNotDisconnect(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startRepairSourceCall(t, f, ctx, s.app)
	if err := f.h.verifyPublicationDescriptionSource(ctx, 7, s.app); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal(err)
	}
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	if err := waitRepairSource(t, done); err != nil {
		t.Fatal(err)
	}
	deliveryAckStatus(t, f.runtime, http.StatusOK)
	for i := 0; i < cap(f.runtime.send); i++ {
		f.runtime.send <- []byte("occupied")
	}
	if err := f.h.verifyPublicationDescriptionSource(ctx, 7, s.app); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal(err)
	}
	if !f.h.isClientRegistered(f.runtime) {
		t.Fatal("backpressure disconnected Runtime")
	}
	assertRepairSourceIdle(t, f)
}

func TestSkillHubPublicationRepairSourceTransportDatabaseDoesNotBlockSocket(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid-first", true: "valid-first"}[valid], func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s.onRead = func(n int) error {
				if n == 2 {
					close(entered)
					<-release
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			req, done := startRepairSourceCall(t, f, ctx, s.app)
			first := repairSourceReply(req)
			first.Result.(map[string]interface{})["sourceVerified"] = valid
			returned := make(chan struct{})
			go func() { f.h.handleThinToolRPCResult(f.runtime, first); close(returned) }()
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("socket callback waited on persistence")
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("authority read did not start")
			}
			duplicate := repairSourceReply(req)
			duplicate.Result.(map[string]interface{})["sourceVerified"] = !valid
			f.h.handleThinToolRPCResult(f.runtime, duplicate)
			deliveryAckStatus(t, f.runtime, http.StatusConflict)
			once.Do(func() { close(release) })
			err := waitRepairSource(t, done)
			if (err == nil) != valid {
				t.Fatalf("duplicate replaced queued evidence: %v", err)
			}
			status := http.StatusUnprocessableEntity
			if valid {
				status = http.StatusOK
			}
			deliveryAckStatus(t, f.runtime, status)
			assertRepairSourceIdle(t, f)
		})
	}
}

func TestSkillHubPublicationRepairSourceTransportTransientErrors(t *testing.T) {
	for _, code := range []string{"publication.runtime_unavailable", "publication.runtime_timeout", "publication.source_changed"} {
		t.Run(code, func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, done := startRepairSourceCall(t, f, ctx, s.app)
			reply := repairSourceReply(req)
			reply.Result = nil
			reply.Error = &MsgDeviceRPCError{Code: code, Message: "private-path-do-not-forward"}
			f.h.handleThinToolRPCResult(f.runtime, reply)
			want := store.ErrSkillHubPublicationBusy
			if code == "publication.source_changed" {
				want = store.ErrSkillHubPublicationInvalid
			}
			if err := waitRepairSource(t, done); !errors.Is(err, want) || strings.Contains(err.Error(), "private-path") {
				t.Fatal(err)
			}
			deliveryAckStatus(t, f.runtime, http.StatusUnprocessableEntity)
			assertRepairSourceIdle(t, f)
		})
	}
}

func TestSkillHubPublicationRepairSourceTransportCancelAtFinalFence(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.onConfig = func(n int) {
		if n == 5 {
			cancel()
		}
	}
	req, done := startRepairSourceCall(t, f, ctx, s.app)
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	if err := waitRepairSource(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("final cancellation accepted: %v (config reads %d)", err, s.configs)
	}
	deliveryAckStatus(t, f.runtime, http.StatusConflict)
	assertRepairSourceIdle(t, f)
}

func TestSkillHubPublicationRepairSourceTransportSnapshotsCaller(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	app := snapshotPublicationDescriptionApplication(s.app)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startRepairSourceCall(t, f, ctx, app)
	*app.Input.Draft.Proposal.EntryIndex = 99
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	if err := waitRepairSource(t, done); err != nil {
		t.Fatal(err)
	}
	deliveryAckStatus(t, f.runtime, http.StatusOK)
	if *s.app.Input.Draft.Proposal.EntryIndex != 0 {
		t.Fatal("caller aliases authoritative snapshot")
	}
}

func TestSkillHubPublicationRepairSourceTransportReconnect(t *testing.T) {
	f, s := newRepairSourceFixture(t)
	// The same body/installation may reconnect at a newer generation. Frozen
	// entry and capture bindings are retained; only the live envelope advances.
	s.generation = 2
	f.h.mu.Lock()
	f.h.botConnectionEpochs[42] = 2
	f.h.mu.Unlock()
	f.h.removeClient(f.runtime)
	f.runtime = &Client{uid: 42, accountType: types.AccountBot, bodyID: "body-42", installationID: "install-42", send: make(chan []byte, 32)}
	f.h.addClient(f.runtime)
	if _, err := f.h.bodyLeases.acquire(42, "body-42", f.runtime.connectionID); err != nil {
		t.Fatal(err)
	}
	f.h.bindDeviceClient(7, f.device, f.runtime)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startRepairSourceCall(t, f, ctx, s.app)
	f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
	if err := waitRepairSource(t, done); err != nil {
		t.Fatal(err)
	}
	deliveryAckStatus(t, f.runtime, http.StatusOK)
	if s.capture.Input.Binding.Source.RouteGeneration != 1 {
		t.Fatal("rewrote original capture binding")
	}
}

type repairSourceApplicationStore struct {
	*repairSourceTestStore
	complete int
}

func (s *repairSourceApplicationStore) PrepareSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionApplication, error) {
	return s.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
}
func (s *repairSourceApplicationStore) CompleteSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, app store.SkillHubPublicationDescriptionApplication, result store.SkillHubPublicationDescriptionApplicationResult) (*store.SkillHubPublicationDescriptionApplication, error) {
	current, err := s.GetSkillHubPublicationDescriptionApplication(ctx, actor, app.Input.Receipt.Query.EntryID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*current, app) || result.ValidateFor(app) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	s.guard.Lock()
	s.complete++
	now := time.Now()
	s.app.Result, s.app.AppliedAt = &result, &now
	s.guard.Unlock()
	return s.GetSkillHubPublicationDescriptionApplication(ctx, actor, app.Input.Receipt.Query.EntryID)
}

func TestSkillHubPublicationRepairSourceTransportCoordinatorPorts(t *testing.T) {
	for _, mode := range []string{"apply", "copy", "source-changed"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			raw := []byte("private-independent-copy")
			completed := repairSourceApplication(t, completedDescriptionApplication(t, "https://hub.example", raw))
			s.app = snapshotPublicationDescriptionApplication(completed)
			if mode == "apply" {
				s.app.Result, s.app.AppliedAt = nil, nil
			}
			q := s.app.Input.Receipt.Query
			s.capture.Input.RequestHash, s.capture.Input.SourceSnapshotHash, s.capture.Input.RequestBytes = q.RequestHash, q.SourceSnapshotHash, q.RequestBytes
			db := &repairSourceApplicationStore{repairSourceTestStore: s}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls := 0
			done := make(chan error, 1)
			go func() {
				if mode == "apply" {
					_, err := applyPublicationDescription(ctx, db, 7, publicationReadTestID, publicationDescriptionApplicationPorts{
						VerifySource: f.h.publicationDescriptionSourceVerifier(7),
						DeriveAndRetain: func(context.Context, store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
							calls++
							return *completed.Result, nil
						},
					})
					done <- err
					return
				}
				copy, err := readConfirmedPublicationDescription(ctx, db, 7, publicationReadTestID, publicationDescriptionCopyPorts{
					VerifySource: f.h.publicationDescriptionSourceVerifier(7),
					ReadRetainedCopy: func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
						calls++
						return raw, nil
					},
				})
				if err == nil && (copy == nil || string(copy.bytes) != string(raw)) {
					err = errors.New("wrong copy")
				}
				done <- err
			}()
			checks := 2
			if mode == "source-changed" {
				checks = 1
			}
			for i := 0; i < checks; i++ {
				req := receiveRepairSourceCall(t, f, done)
				reply := repairSourceReply(req)
				status := http.StatusOK
				if mode == "source-changed" {
					reply.Result = nil
					reply.Error = &MsgDeviceRPCError{Code: "publication.source_changed"}
					status = http.StatusUnprocessableEntity
				}
				f.h.handleThinToolRPCResult(f.runtime, reply)
				waitRepairSourceAck(t, f.runtime, status)
			}
			err := waitRepairSource(t, done)
			if mode == "source-changed" {
				if err == nil || calls != 0 || db.complete != 0 {
					t.Fatal("source failure reached private copy")
				}
			} else if err != nil || calls != 1 || db.complete != map[bool]int{true: 1, false: 0}[mode == "apply"] {
				t.Fatalf("coordination: err=%v calls=%d complete=%d", err, calls, db.complete)
			}
			assertRepairSourceIdle(t, f)
		})
	}
}
