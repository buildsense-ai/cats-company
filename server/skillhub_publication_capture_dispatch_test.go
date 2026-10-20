package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type captureDispatchTestStore struct {
	*publicationCaptureTestStore
	mu      sync.Mutex
	claims  int
	outcome string
	lease   *store.SkillHubPublicationCaptureLease
}

func (s *captureDispatchTestStore) ListSkillHubPublicationCaptureDeliveries(context.Context, store.SkillHubPublicationRuntimeSelector, int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome != "" || s.lease != nil {
		return nil, nil
	}
	return []string{publicationReadTestID}, nil
}
func (s *captureDispatchTestStore) ClaimSkillHubPublicationCaptureDelivery(_ context.Context, id string, b store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationCaptureLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lease != nil {
		return nil, store.ErrSkillHubPublicationBusy
	}
	s.claims++
	s.lease = &store.SkillHubPublicationCaptureLease{EntryID: id, Binding: b, Token: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Attempt: 1, ExpiresAt: time.Now().Add(40 * time.Second)}
	copied := *s.lease
	return &copied, nil
}
func (s *captureDispatchTestStore) FinishSkillHubPublicationCaptureDelivery(_ context.Context, _ store.SkillHubPublicationCaptureLease, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome = outcome
	return nil
}

func newCaptureDispatcherFixture(t *testing.T) (*publicationWorkspaceFixture, *captureDispatchTestStore, *publicationCaptureDispatcher) {
	t.Helper()
	var s *captureDispatchTestStore
	f, _ := newCaptureTransportFixtureWithStore(t, func(capture *publicationCaptureTestStore) store.Store {
		s = &captureDispatchTestStore{publicationCaptureTestStore: capture}
		return s
	})
	w := &publicationCaptureDispatcher{h: f.h, ledger: s, entries: s}
	return f, s, w
}

func TestSkillHubPublicationCaptureDispatcherGatesAndOffline(t *testing.T) {
	f, s, w := newCaptureDispatcherFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_DISPATCH_ENABLED", "")
	if newPublicationCaptureDispatcherFromEnv(f.h) != nil {
		t.Fatal("default enabled")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_DISPATCH_ENABLED", "true")
	if newPublicationCaptureDispatcherFromEnv(f.h) == nil {
		t.Fatal("enabled unavailable")
	}
	f.h.publicationCapture = nil
	if newPublicationCaptureDispatcherFromEnv(f.h) != nil {
		t.Fatal("bypassed transport gate")
	}
	f.h.removeClient(f.runtime)
	if err := w.runOnce(context.Background()); err != nil || s.claims != 0 {
		t.Fatal("offline consumed attempts", err)
	}
}

func TestSkillHubPublicationCaptureDispatcherOutcomes(t *testing.T) {
	for _, kind := range []string{"success", "busy", "source", "malformed_busy", "shutdown"} {
		t.Run(kind, func(t *testing.T) {
			f, s, w := newCaptureDispatcherFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- w.runOnce(ctx) }()
			var msg ServerMessage
			select {
			case raw := <-f.runtime.send:
				if err := json.Unmarshal(raw, &msg); err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no dispatch")
			}
			reply := captureReply(msg.ThinToolRPC)
			want := "captured"
			switch kind {
			case "busy", "malformed_busy":
				reply.Result = nil
				reply.Error = &MsgDeviceRPCError{Code: "publication.runtime_unavailable", Message: "do not persist"}
				want = "retry"
				if kind == "malformed_busy" {
					reply.TargetDeviceID = "other"
					want = "rejected"
				}
			case "source":
				reply.Result = nil
				reply.Error = &MsgDeviceRPCError{Code: "publication.source_unsafe"}
				want = "rejected"
			case "shutdown":
				cancel()
				want = ""
			}
			if kind != "shutdown" {
				f.h.handleThinToolRPCResult(f.runtime, reply)
			}
			select {
			case err := <-done:
				if err != nil && !(kind == "shutdown" && errors.Is(err, context.Canceled)) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not join")
			}
			if s.outcome != want || s.claims != 1 || (kind != "success" && s.calls != 0) {
				t.Fatal("outcome", s.outcome, s.claims, s.calls)
			}
			f.h.publicationCapture.mu.Lock()
			active := len(f.h.publicationCapture.pending) + len(f.h.publicationCapture.active)
			f.h.publicationCapture.mu.Unlock()
			if active != 0 {
				t.Fatal("worker leaked pending capture")
			}
		})
	}
}

func TestSkillHubPublicationCaptureDispatcherStop(t *testing.T) {
	f, s, _ := newCaptureDispatcherFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_DISPATCH_ENABLED", "true")
	stop := StartSkillHubPublicationCaptureDispatch(context.Background(), f.h)
	select {
	case <-f.runtime.send:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join")
	}
	if s.outcome != "" {
		t.Fatal("shutdown settled a lease")
	}
}
