package server

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Independent status-only reconciler. The only peer port is a read; planning,
// authorization, admission, model calls and publication execution are absent.
type PublicationDescriptionReconciler struct {
	enabled bool
	scope   store.SkillHubPublicationCancellationScope
	ledger  store.SkillHubPublicationDescriptionReconcileStore
	ports   publicationDescriptionReleaseOutcomePorts
	running atomic.Bool
	active  atomic.Int32
}

func NewPublicationDescriptionReconciler(enabled bool, cfg PublicationDescriptionDerivationClientConfig, ledger store.SkillHubPublicationDescriptionReconcileStore) (*PublicationDescriptionReconciler, error) {
	if !enabled {
		return &PublicationDescriptionReconciler{}, nil
	}
	scope := store.SkillHubPublicationCancellationScope{Issuer: cfg.Issuer, Audience: cfg.Audience}
	if ledger == nil || scope.Validate() != nil || !cfg.Enabled || !cfg.ReleaseStatusEnabled {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	// Do not grant even an accidental future caller mutation-enabled ports.
	cfg.ReadEnabled, cfg.ValidationEnabled, cfg.PlanEnabled, cfg.ReleaseAuthorizeEnabled, cfg.ReleaseCommitEnabled = false, false, false, false, false
	c, err := NewPublicationDescriptionDerivationClient(cfg)
	if err != nil {
		return nil, err
	}
	return &PublicationDescriptionReconciler{enabled: true, scope: scope, ledger: ledger,
		ports: publicationDescriptionReleaseOutcomePorts{ReadCloud: c.ReadRetainedRelease}}, nil
}

func StartSkillHubPublicationDescriptionReconcile(parent context.Context, db store.Store) func() {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RECONCILE_ENABLED"))
	if v != "1" && !strings.EqualFold(v, "true") {
		return func() {}
	}
	ledger, ok := db.(store.SkillHubPublicationDescriptionReconcileStore)
	c, err := NewPublicationDescriptionDerivationClientFromEnv()
	if !ok || err != nil || c == nil || !c.enabled || !c.releaseStatusEnabled {
		log.Print("SkillHub description reconciliation unavailable: configuration or storage unsupported.")
		return func() {}
	}
	c.readEnabled, c.validationEnabled, c.planEnabled, c.releaseAuthorizeEnabled, c.releaseCommitEnabled = false, false, false, false, false
	w := &PublicationDescriptionReconciler{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: c.issuer, Audience: c.audience}, ledger: ledger,
		ports: publicationDescriptionReleaseOutcomePorts{ReadCloud: c.ReadRetainedRelease}}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	return func() { cancel(); <-done }
}

func (w *PublicationDescriptionReconciler) Run(ctx context.Context) {
	if w == nil || !w.enabled || !w.running.CompareAndSwap(false, true) {
		return
	}
	defer w.running.Store(false)
	for ctx.Err() == nil {
		// Match the dedicated status client's budget, before acquiring leases.
		done := make(chan error, publicationDescriptionReleaseStatusConcurrency)
		for i := 0; i < publicationDescriptionReleaseStatusConcurrency; i++ {
			go func() { done <- w.RunOnce(ctx) }()
		}
		failed := false
		for i := 0; i < publicationDescriptionReleaseStatusConcurrency; i++ {
			if <-done != nil {
				failed = true
			}
		}
		if failed && ctx.Err() == nil {
			log.Print("SkillHub description status unavailable; retained observations will be recovered.")
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

func (w *PublicationDescriptionReconciler) RunOnce(ctx context.Context) error {
	if w == nil || !w.enabled {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.active.Add(1) > publicationDescriptionReleaseStatusConcurrency {
		w.active.Add(-1)
		return store.ErrSkillHubPublicationBusy
	}
	defer w.active.Add(-1)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	l, err := w.ledger.ClaimSkillHubPublicationDescriptionReconcile(ctx, w.scope)
	if err != nil || l == nil {
		return err
	}
	if l.Validate() != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	// Expired leases cannot begin peer I/O. SQL is the scheduling clock; this
	// extra deadline protects against a delayed/misbehaving store adapter.
	workCtx, workCancel := context.WithDeadline(ctx, l.ExpiresAt.Add(-5*time.Second))
	defer workCancel()
	outcome := "retry"
	if err := w.observe(workCtx, *l); err == nil {
		outcome = "recorded"
	} else if errors.Is(err, errDescriptionReconcileProgress) || errors.Is(err, store.ErrSkillHubPublicationBusy) {
		// Local capacity rejection is not an external observation failure.
		// Requeue without increasing the durable failure/backoff counter.
		outcome = "progress"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	return w.ledger.FinishSkillHubPublicationDescriptionReconcile(finishCtx, *l, outcome)
}

var errDescriptionReconcileProgress = errors.New("publication result not terminal")

// Validate every reread against the selected lease/scope, not just the initial
// lookup. This also constrains alternate terminal store implementations.
type descriptionReconcileBoundStore struct {
	store.SkillHubPublicationDescriptionReconcileStore
	lease store.SkillHubPublicationDescriptionReconcileLease
	scope store.SkillHubPublicationCancellationScope
}

func (db descriptionReconcileBoundStore) GetSkillHubPublicationDescriptionReleaseObservation(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseObservation, error) {
	if actor != db.lease.ActorUID || id != db.lease.EntryID {
		return nil, store.ErrSkillHubPublicationConflict
	}
	o, err := db.SkillHubPublicationDescriptionReconcileStore.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if o == nil || o.ValidateFor(actor, id) != nil || o.Intent.ApplicationID != db.lease.ApplicationID ||
		o.Continuation.Application.Input.Receipt.Query.Issuer != db.scope.Issuer || o.Continuation.Application.Input.Receipt.Query.Audience != db.scope.Audience {
		return nil, store.ErrSkillHubPublicationConflict
	}
	return o, nil
}

func (w *PublicationDescriptionReconciler) observe(ctx context.Context, l store.SkillHubPublicationDescriptionReconcileLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bound := descriptionReconcileBoundStore{SkillHubPublicationDescriptionReconcileStore: w.ledger, lease: l, scope: w.scope}
	// A prior worker may have committed before losing its response. Recover
	// local immutable history without requiring Cloud to be online again.
	r, err := readConfirmedPublicationDescriptionReleaseTerminal(ctx, bound, l.ActorUID, l.EntryID)
	if err != nil {
		return err
	}
	if r != nil {
		return nil
	}
	r, err = recordConfirmedPublicationDescriptionReleaseTerminal(ctx, bound, l.ActorUID, l.EntryID, w.ports)
	if err != nil {
		return err
	}
	if r == nil {
		return errDescriptionReconcileProgress
	}
	return nil
}
