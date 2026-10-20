package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCaptureTestStore struct {
	*publicationWorkspaceTestStore
	status   *store.SkillHubPublicationSourceStatus
	input    store.SkillHubPublicationCaptureInput
	calls    int
	err      error
	onRecord func()
}

func (s *publicationCaptureTestStore) GetSkillHubPublicationSourceStatus(context.Context, int64, string) (*store.SkillHubPublicationSourceStatus, error) {
	return s.status, nil
}
func (s *publicationCaptureTestStore) RecordSkillHubPublicationCapture(ctx context.Context, in store.SkillHubPublicationCaptureInput) (*store.SkillHubPublicationCaptureRecord, error) {
	s.calls++
	s.input = in
	if s.onRecord != nil {
		s.onRecord()
	}
	if s.err != nil {
		return nil, s.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &store.SkillHubPublicationCaptureRecord{Input: in, CapturedAt: time.Now()}, nil
}

func newCaptureTransportFixture(t *testing.T) (*publicationWorkspaceFixture, *publicationCaptureTestStore) {
	return newCaptureTransportFixtureWithStore(t, nil)
}

func newCaptureTransportFixtureWithStore(t *testing.T, wrap func(*publicationCaptureTestStore) store.Store) (*publicationWorkspaceFixture, *publicationCaptureTestStore) {
	t.Helper()
	var s *publicationCaptureTestStore
	f := newPublicationWorkspaceFixtureWithStore(t, func(base *publicationWorkspaceTestStore) store.Store {
		s = &publicationCaptureTestStore{publicationWorkspaceTestStore: base}
		if wrap != nil {
			return wrap(s)
		}
		return s
	})
	device, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe), string(DeviceCapabilitySkillHubPublicationCapture)}})
	if err != nil {
		t.Fatal(err)
	}
	f.h.bindDeviceClient(7, device, f.runtime)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	in := store.SkillHubPublicationEntryInput{ActorUID: 7, BotUID: 42, Entrypoint: "workspace", RequestID: "capture-click",
		Source: store.SkillHubPublicationSource{DeviceID: device.DeviceID, BodyID: device.BodyID, InstallationID: device.InstallationID, RouteGeneration: 1,
			LocalSkillID: "pubsrc-" + strings.Repeat("a", 48), WorkspaceGeneration: strings.Repeat("b", 64)}}
	if _, _, err := s.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.status = &store.SkillHubPublicationSourceStatus{ActorUID: 7, EntryID: publicationReadTestID, State: "source_verified", SourceVerifiedAt: &now}
	f.h.publicationCapture = newPublicationCaptureTransport(f.h)
	if f.h.publicationCapture == nil {
		t.Fatal("missing capture transport")
	}
	return f, s
}

type captureCallResult struct {
	record *store.SkillHubPublicationCaptureRecord
	err    error
}

func startCaptureCall(t *testing.T, f *publicationWorkspaceFixture, ctx context.Context) (*MsgThinToolRPC, <-chan captureCallResult) {
	t.Helper()
	done := make(chan captureCallResult, 1)
	go func() {
		r, err := f.h.CaptureSkillHubPublication(ctx, 7, publicationReadTestID)
		done <- captureCallResult{r, err}
	}()
	var message ServerMessage
	select {
	case raw := <-f.runtime.send:
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatal(err)
		}
	case r := <-done:
		t.Fatalf("capture returned before dispatch: %v", r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("capture dispatch timeout")
	}
	if message.ThinToolRPC == nil || !strings.HasPrefix(message.ThinToolRPC.RequestID, publicationCaptureRequestPrefix) {
		t.Fatal("wrong dispatch")
	}
	return message.ThinToolRPC, done
}
func captureReply(req *MsgThinToolRPC) *MsgThinToolRPC {
	return &MsgThinToolRPC{ID: "capture-response", Type: thinToolRPCTypeResult, RequestID: req.RequestID, ToolName: req.ToolName,
		TargetOwnerUserID: req.TargetOwnerUserID, TargetDeviceID: req.TargetDeviceID, DeviceID: req.DeviceID,
		Result: map[string]interface{}{"schemaVersion": float64(1), "entryId": publicationReadTestID, "candidateRevision": float64(1), "requestHash": strings.Repeat("c", 64), "sourceSnapshotHash": strings.Repeat("d", 64), "requestBytes": float64(1234)}}
}
func captureWait(t *testing.T, done <-chan captureCallResult) captureCallResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not settle")
		return captureCallResult{}
	}
}

func TestSkillHubPublicationCaptureTransportAuthenticatedOwnerOffline(t *testing.T) {
	f, s := newCaptureTransportFixture(t)
	f.h.removeClient(f.owner)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startCaptureCall(t, f, ctx)
	if len(req.Payload) != 4 || req.Payload["entry_id"] != publicationReadTestID || req.Payload["bot_uid"] != "42" || req.ToolName != string(DeviceCapabilitySkillHubPublicationCapture) {
		t.Fatal("unexpected request")
	}
	f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
	r := captureWait(t, done)
	if r.err != nil || r.record == nil || s.calls != 1 || s.input.Binding.ActorUID != 7 || s.input.Binding.BotUID != 42 || s.input.Binding.ConnectionID != f.runtime.connectionID || s.input.RequestBytes != 1234 {
		t.Fatalf("capture: %#v %v", r, s.input)
	}
	deliveryAckStatus(t, f.runtime, 200)
	f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
	deliveryAckStatus(t, f.runtime, 404)
	if len(f.owner.send) != 0 {
		t.Fatal("private receipt sent to owner chat")
	}
}

func TestSkillHubPublicationCaptureTransportOpaqueSelectors(t *testing.T) {
	for _, selectors := range [][2]string{
		{"local-skill-1", "workspace-v1"},
		{"123e4567-e89b-12d3-a456-426614174000", "workspace:v1.2_3"},
		{strings.Repeat("a", 128), strings.Repeat("b", 128)},
	} {
		t.Run(selectors[0], func(t *testing.T) {
			f, s := newCaptureTransportFixture(t)
			in := &s.entries["capture-click"].Input
			in.Source.LocalSkillID, in.Source.WorkspaceGeneration = selectors[0], selectors[1]
			if err := in.Validate(); err != nil {
				t.Fatal("selectors must satisfy admission", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, done := startCaptureCall(t, f, ctx)
			if req.Payload["local_skill_id"] != selectors[0] || req.Payload["workspace_generation"] != selectors[1] {
				t.Fatal("admitted selectors changed in transit", req.Payload)
			}
			f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
			r := captureWait(t, done)
			if r.err != nil || r.record == nil || s.calls != 1 || s.input.Binding.Source != in.Source || r.record.Input != s.input {
				t.Fatalf("opaque selectors did not retain capture evidence: %#v", r)
			}
			deliveryAckStatus(t, f.runtime, http.StatusOK)
		})
	}
}

func TestSkillHubPublicationCaptureTransportRejectsInvalidSelectors(t *testing.T) {
	for _, field := range []string{"local_skill_id", "workspace_generation"} {
		for _, value := range []string{"", "../skill", "with space", strings.Repeat("a", 129)} {
			t.Run(field+"/"+value, func(t *testing.T) {
				f, s := newCaptureTransportFixture(t)
				in := &s.entries["capture-click"].Input
				if field == "local_skill_id" {
					in.Source.LocalSkillID = value
				} else {
					in.Source.WorkspaceGeneration = value
				}
				if in.Validate() == nil {
					t.Fatal("fixture must violate admission")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if _, err := f.h.CaptureSkillHubPublication(ctx, 7, publicationReadTestID); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
					t.Fatal("invalid stored entry was not denied", err)
				}
				if s.calls != 0 || len(f.runtime.send) != 0 {
					t.Fatal("invalid selector reached runtime or capture store")
				}
			})
		}
	}
}

func TestSkillHubPublicationCaptureTransportRejectsSpoofAndMalformed(t *testing.T) {
	for _, name := range []string{"human", "unknown", "entry", "extra", "hash", "source-hash", "uppercase-hash", "zero", "fraction", "oversize", "revision", "tool", "owner", "device", "payload", "error", "type"} {
		t.Run(name, func(t *testing.T) {
			f, s := newCaptureTransportFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, done := startCaptureCall(t, f, ctx)
			reply := captureReply(req)
			result := reply.Result.(map[string]interface{})
			switch name {
			case "human":
				f.h.handleThinToolRPCResult(f.owner, reply)
				deliveryAckStatus(t, f.owner, 403)
				f.h.handleThinToolRPCResult(f.runtime, reply)
				if r := captureWait(t, done); r.err != nil || s.calls != 1 {
					t.Fatal("spoof consumed pending request")
				}
				return
			case "unknown":
				reply.RequestID += "x"
				f.h.handleThinToolRPCResult(f.runtime, reply)
				deliveryAckStatus(t, f.runtime, 404)
				f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
				if r := captureWait(t, done); r.err != nil || s.calls != 1 {
					t.Fatal("unknown reply consumed request")
				}
				return
			case "entry":
				result["entryId"] = "pubentry_" + strings.Repeat("f", 32)
			case "extra":
				result["files"] = "must never queue raw source"
			case "hash":
				result["requestHash"] = "bad"
			case "source-hash":
				result["sourceSnapshotHash"] = "workspace-v1"
			case "uppercase-hash":
				result["requestHash"] = strings.Repeat("C", 64)
			case "zero":
				result["requestBytes"] = float64(0)
			case "fraction":
				result["requestBytes"] = 1.5
			case "oversize":
				result["requestBytes"] = float64(publicationCaptureRuntimeMaxBytes + 1)
			case "revision":
				result["candidateRevision"] = float64(2)
			case "tool":
				reply.ToolName = "skillhub.localSkill.share"
			case "owner":
				reply.TargetOwnerUserID = "usr8"
			case "device":
				reply.DeviceID = "other"
			case "payload":
				reply.Payload = map[string]interface{}{"actor": "7"}
			case "error":
				reply.Error = &MsgDeviceRPCError{Code: "internal", Message: "private runtime error"}
			case "type":
				reply.Type = thinToolRPCTypeRequest
			}
			f.h.handleThinToolRPCResult(f.runtime, reply)
			if r := captureWait(t, done); !errors.Is(r.err, store.ErrSkillHubPublicationInvalid) || s.calls != 0 {
				t.Fatalf("accepted malformed response: %v", r.err)
			}
			deliveryAckStatus(t, f.runtime, http.StatusUnprocessableEntity)
		})
	}
}

func TestSkillHubPublicationCaptureTransportAuthorityAndBackpressure(t *testing.T) {
	for _, name := range []string{"cancelled", "unverified", "wrong-status-owner", "missing-evidence", "owner-changed", "disabled", "busy", "slots-full", "expired", "full-send", "legacy"} {
		t.Run(name, func(t *testing.T) {
			f, s := newCaptureTransportFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch name {
			case "cancelled":
				s.entries["capture-click"].State = "cancelled"
			case "unverified":
				s.status.State = "pending"
			case "wrong-status-owner":
				s.status.ActorUID = 8
			case "missing-evidence":
				s.status.SourceVerifiedAt = nil
			case "owner-changed":
				s.config.OwnerID = 8
			case "disabled":
				f.h.publicationCapture = nil
			case "busy":
				f.h.publicationCapture.active[publicationReadTestID] = true
			case "slots-full":
				for i := 0; i < cap(f.h.publicationCapture.slots); i++ {
					f.h.publicationCapture.slots <- struct{}{}
				}
			case "expired":
				cancel()
			case "full-send":
				for len(f.runtime.send) < cap(f.runtime.send) {
					f.runtime.send <- []byte("occupied")
				}
			case "legacy":
				_, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe)}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.h.CaptureSkillHubPublication(ctx, 7, publicationReadTestID); err == nil || s.calls != 0 {
				t.Fatal("capture bypassed authority/backpressure")
			}
			if name != "full-send" && len(f.runtime.send) != 0 {
				t.Fatal("unexpected runtime request")
			}
		})
	}
}

func TestSkillHubPublicationCaptureTransportLateAndChangedRoutes(t *testing.T) {
	for _, name := range []string{"cancel-context", "generation", "owner", "db-denied"} {
		t.Run(name, func(t *testing.T) {
			f, s := newCaptureTransportFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, done := startCaptureCall(t, f, ctx)
			switch name {
			case "cancel-context":
				cancel()
				if r := captureWait(t, done); !errors.Is(r.err, context.Canceled) {
					t.Fatal(r.err)
				}
				f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
				deliveryAckStatus(t, f.runtime, 404)
				if s.calls != 0 {
					t.Fatal("late receipt stored")
				}
				return
			case "generation":
				s.generation++
			case "owner":
				s.config.OwnerID = 8
			case "db-denied":
				s.err = store.ErrSkillHubPublicationDenied
			}
			f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
			if r := captureWait(t, done); r.err == nil {
				t.Fatal("stale capture accepted")
			}
			deliveryAckStatus(t, f.runtime, 409)
			if name != "db-denied" && s.calls != 0 {
				t.Fatal("stale route reached store")
			}
		})
	}
}

func TestSkillHubPublicationCaptureTransportDefaultOffAndReserved(t *testing.T) {
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_ENABLED", "")
	f, _ := newCaptureTransportFixture(t)
	if newPublicationCaptureTransportFromEnv(f.h) != nil {
		t.Fatal("default enabled")
	}
	req := publicationWorkspaceRequest()
	req.ToolName = string(DeviceCapabilitySkillHubPublicationCapture)
	f.h.handleThinToolRPCRequest(f.owner, req)
	if len(f.runtime.send) != 0 {
		t.Fatal("browser invoked internal capture")
	}
}

func TestSkillHubPublicationCaptureTransportTimeoutReleasesCapacity(t *testing.T) {
	f, s := newCaptureTransportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, done := startCaptureCall(t, f, ctx)
	if r := captureWait(t, done); !errors.Is(r.err, context.DeadlineExceeded) {
		t.Fatal("timeout did not fence request", r.err)
	}
	w := f.h.publicationCapture
	if len(w.slots) != 0 || len(w.pending) != 0 || len(w.active) != 0 {
		t.Fatal("timeout retained operation capacity")
	}
	f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
	deliveryAckStatus(t, f.runtime, 404)
	if s.calls != 0 {
		t.Fatal("timeout wrote capture evidence")
	}
}

func TestSkillHubPublicationCaptureTransportDatabaseDoesNotBlockSocket(t *testing.T) {
	f, s := newCaptureTransportFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	s.onRecord = func() { close(entered); <-release }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, done := startCaptureCall(t, f, ctx)
	defer func() { close(release); captureWait(t, done) }()
	f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("DB write did not start")
	}
	// The read-loop method has already returned while storage is blocked.
	f.h.handleThinToolRPCResult(f.runtime, captureReply(req))
	deliveryAckStatus(t, f.runtime, 409)
	if _, err := f.h.CaptureSkillHubPublication(ctx, 7, publicationReadTestID); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("duplicate operation was not bounded", err)
	}
	// Release via cleanup after checking that no duplicate result settled it.
	select {
	case <-done:
		t.Fatal("blocked persistence completed early")
	default:
	}
}
