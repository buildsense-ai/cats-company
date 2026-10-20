package server

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCaptureDispatcher struct {
	h       *Hub
	ledger  store.SkillHubPublicationCaptureDeliveryStore
	entries store.SkillHubPublicationEntryStore
	running atomic.Bool
	sweepMu sync.Mutex
	cursor  int64 // sweepMu
}

func newPublicationCaptureDispatcherFromEnv(h *Hub) *publicationCaptureDispatcher {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_DISPATCH_ENABLED"))
	if h == nil || h.publicationCapture == nil || (v != "1" && !strings.EqualFold(v, "true")) {
		return nil
	}
	ledger, ok := h.db.(store.SkillHubPublicationCaptureDeliveryStore)
	entries, entriesOK := h.db.(store.SkillHubPublicationEntryStore)
	if !ok || !entriesOK {
		return nil
	}
	return &publicationCaptureDispatcher{h: h, ledger: ledger, entries: entries}
}

// Start only on the final Hub, never from NewHub. Cancellation joins all workers;
// no detached capture can outlive server shutdown. SQL leases survive crashes.
func StartSkillHubPublicationCaptureDispatch(parent context.Context, h *Hub) func() {
	w := newPublicationCaptureDispatcherFromEnv(h)
	if w == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() { defer close(done); w.run(ctx) }()
	return func() { cancel(); <-done }
}

func (w *publicationCaptureDispatcher) run(ctx context.Context) {
	if !w.running.CompareAndSwap(false, true) {
		return
	}
	defer w.running.Store(false)
	for ctx.Err() == nil {
		if err := w.runOnce(ctx); err != nil && ctx.Err() == nil {
			log.Print("SkillHub publication capture dispatch unavailable; retained attempts will recover.")
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type publicationCaptureJob struct {
	id      string
	binding store.SkillHubPublicationDeliveryBinding
}

func (w *publicationCaptureDispatcher) runtimeBinding(actor, bot int64, device string) (store.SkillHubPublicationDeliveryBinding, bool) {
	s, route, _, code := w.h.publicationRuntimeBinding(actor, bot, device)
	if code != "" {
		return store.SkillHubPublicationDeliveryBinding{}, false
	}
	d, ok := w.h.userDevices.activeDevice(actor, device)
	if !ok {
		return store.SkillHubPublicationDeliveryBinding{}, false
	}
	for _, capability := range d.Capabilities {
		if capability == DeviceCapabilitySkillHubPublicationCapture {
			return store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: bot, Source: s, NodeID: route.NodeID, ConnectionID: route.ConnectionID}, true
		}
	}
	return store.SkillHubPublicationDeliveryBinding{}, false
}

func (w *publicationCaptureDispatcher) runOnce(parent context.Context) error {
	if parent.Err() != nil || !w.sweepMu.TryLock() {
		return nil
	}
	defer w.sweepMu.Unlock()
	// Selection is bounded independently of slow Runtime capture calls.
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	jobs, err := w.selectJobs(ctx)
	cancel()
	if err != nil {
		return err
	}
	queue := make(chan publicationCaptureJob, len(jobs))
	for _, job := range jobs {
		queue <- job
	}
	close(queue)
	var wg sync.WaitGroup
	var errsMu sync.Mutex
	var firstErr error
	for n := 0; n < 4 && n < len(jobs); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				if parent.Err() != nil {
					return
				}
				if err := w.deliver(parent, job); err != nil {
					errsMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errsMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func (w *publicationCaptureDispatcher) selectJobs(ctx context.Context) ([]publicationCaptureJob, error) {
	jobs := []publicationCaptureJob{}
	for _, bot := range w.h.publicationLocalBots(w.cursor) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		w.cursor = bot.uid
		b, ok := w.runtimeBinding(bot.owner, bot.uid, bot.device)
		if !ok {
			continue // Offline/unsupported runtimes consume no attempt.
		}
		ids, err := w.ledger.ListSkillHubPublicationCaptureDeliveries(ctx, store.SkillHubPublicationRuntimeSelector{
			ActorUID: b.ActorUID, BotUID: b.BotUID, DeviceID: b.Source.DeviceID, BodyID: b.Source.BodyID, InstallationID: b.Source.InstallationID}, 4)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			e, err := w.entries.GetSkillHubPublicationEntry(ctx, b.ActorUID, id)
			if err != nil {
				return nil, err
			}
			if e == nil || e.ID != id || e.State != "waiting_runtime" || e.Input.Validate() != nil || e.Input.ActorUID != b.ActorUID || e.Input.BotUID != b.BotUID {
				continue
			}
			next := b
			next.Source.LocalSkillID, next.Source.WorkspaceGeneration = e.Input.Source.LocalSkillID, e.Input.Source.WorkspaceGeneration
			expected := e.Input.Source
			if next.Source.RouteGeneration < expected.RouteGeneration {
				continue
			}
			expected.RouteGeneration = next.Source.RouteGeneration
			if next.Source != expected {
				continue
			}
			jobs = append(jobs, publicationCaptureJob{id, next})
			if len(jobs) == 8 {
				return jobs, nil
			}
		}
	}
	return jobs, nil
}

func (w *publicationCaptureDispatcher) deliver(ctx context.Context, j publicationCaptureJob) error {
	b := j.binding
	fresh, ok := w.runtimeBinding(b.ActorUID, b.BotUID, b.Source.DeviceID)
	fresh.Source.LocalSkillID, fresh.Source.WorkspaceGeneration = b.Source.LocalSkillID, b.Source.WorkspaceGeneration
	if !ok || fresh != b {
		return nil
	}
	l, err := w.ledger.ClaimSkillHubPublicationCaptureDelivery(ctx, j.id, b)
	if errors.Is(err, store.ErrSkillHubPublicationBusy) || errors.Is(err, store.ErrSkillHubPublicationSettled) ||
		errors.Is(err, store.ErrSkillHubPublicationExhausted) || errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationStale) {
		return nil
	}
	if err != nil {
		return err
	}
	if l == nil || l.Validate() != nil || l.EntryID != j.id || l.Binding != b {
		return store.ErrSkillHubPublicationInvalid
	}
	workCtx, cancel := context.WithDeadline(ctx, l.ExpiresAt.Add(-5*time.Second))
	_, captureErr := w.h.CaptureSkillHubPublication(workCtx, b.ActorUID, j.id)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err() // Do not settle with detached shutdown work.
	}
	return w.ledger.FinishSkillHubPublicationCaptureDelivery(ctx, *l, publicationCaptureOutcome(captureErr))
}

func publicationCaptureOutcome(err error) string {
	switch {
	case err == nil:
		return "captured"
	case errors.Is(err, store.ErrSkillHubPublicationInvalid), errors.Is(err, store.ErrSkillHubPublicationDenied),
		errors.Is(err, store.ErrSkillHubPublicationConflict), errors.Is(err, store.ErrSkillHubPublicationSettled):
		return "rejected"
	default:
		// Timeouts, backpressure, lost routes and DB failures: bounded by SQL.
		return "retry"
	}
}
