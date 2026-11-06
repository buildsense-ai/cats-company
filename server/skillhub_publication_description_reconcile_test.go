package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionReconcileTestStore struct {
	*terminalOutcomeTestStore
	lease     store.SkillHubPublicationDescriptionReconcileLease
	claims    atomic.Int32
	outcomes  []string
	claim     func(context.Context) (*store.SkillHubPublicationDescriptionReconcileLease, error)
	finishErr error
}

func (db *descriptionReconcileTestStore) ClaimSkillHubPublicationDescriptionReconcile(ctx context.Context, _ store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationDescriptionReconcileLease, error) {
	db.claims.Add(1)
	if db.claim != nil {
		return db.claim(ctx)
	}
	l := db.lease
	return &l, nil
}
func (db *descriptionReconcileTestStore) FinishSkillHubPublicationDescriptionReconcile(_ context.Context, l store.SkillHubPublicationDescriptionReconcileLease, outcome string) error {
	if l != db.lease {
		return store.ErrSkillHubPublicationStale
	}
	db.outcomes = append(db.outcomes, outcome)
	return db.finishErr
}
func descriptionReconcileFixture(t *testing.T, state string) (*PublicationDescriptionReconciler, *descriptionReconcileTestStore, *atomic.Int32) {
	t.Helper()
	r, s := descriptionOutcomeFixture(t)
	s.State = state
	if state == "published" {
		s.Result = &PublicationCommitCloudResult{SkillID: s.Target.SkillID, Version: s.Target.Version, PublishedAt: s.Target.ReleaseAt, PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64)}
	}
	if state == "publish_failed" {
		code := "skillhub.publication_description_release_commit_rejected"
		s.ErrorCode = &code
	}
	db := &descriptionReconcileTestStore{terminalOutcomeTestStore: &terminalOutcomeTestStore{descriptionOutcomeTestReader: r}, lease: store.SkillHubPublicationDescriptionReconcileLease{
		ActorUID: r.observation.Intent.ActorUID, EntryID: r.observation.Intent.EntryID, ApplicationID: r.observation.Intent.ApplicationID, Token: strings.Repeat("a", 32), Generation: 1, ExpiresAt: time.Now().Add(90 * time.Second)}}
	calls := &atomic.Int32{}
	w := &PublicationDescriptionReconciler{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: r.observation.Continuation.Application.Input.Receipt.Query.Issuer, Audience: r.observation.Continuation.Application.Input.Receipt.Query.Audience}, ledger: db,
		ports: publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
			calls.Add(1)
			return &s, nil
		}}}
	return w, db, calls
}

func TestSkillHubPublicationDescriptionReconcileOutcomes(t *testing.T) {
	for _, state := range []string{"queued", "publishing", "committing", "published", "publish_failed"} {
		t.Run(state, func(t *testing.T) {
			w, db, calls := descriptionReconcileFixture(t, state)
			want := "progress"
			if state == "published" || state == "publish_failed" {
				want = "recorded"
			}
			if err := w.RunOnce(context.Background()); err != nil || len(db.outcomes) != 1 || db.outcomes[0] != want || calls.Load() != 1 {
				t.Fatal("outcome", db.outcomes, err)
			}
			if want == "progress" && db.writes != 0 {
				t.Fatal("progress wrote terminal")
			}
		})
	}
}

func TestSkillHubPublicationDescriptionReconcileLostResponseRecovery(t *testing.T) {
	w, db, calls := descriptionReconcileFixture(t, "published")
	db.lose = true
	if err := w.RunOnce(context.Background()); err != nil || db.outcomes[0] != "retry" || db.receipt == nil {
		t.Fatal("lost write response", err)
	}
	// Reconstruct a worker after the database committed but the reply was lost.
	w = &PublicationDescriptionReconciler{enabled: true, scope: w.scope, ledger: db, ports: publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
		t.Fatal("recovery required Cloud again")
		return nil, nil
	}}}
	if err := w.RunOnce(context.Background()); err != nil || len(db.outcomes) != 2 || db.outcomes[1] != "recorded" || db.writes != 1 || calls.Load() != 1 {
		t.Fatal("local receipt recovery", db.outcomes, err)
	}
}

func TestSkillHubPublicationDescriptionReconcileFailsClosed(t *testing.T) {
	for _, mode := range []string{"wrong-scope", "wrong-application", "expired", "disabled", "lost-cloud", "nil-cloud", "cancel-peer", "settlement-error", "local-busy"} {
		t.Run(mode, func(t *testing.T) {
			w, db, calls := descriptionReconcileFixture(t, "published")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "local-busy":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					return nil, store.ErrSkillHubPublicationBusy
				}
			case "wrong-scope":
				w.scope.Audience = "https://other.example"
			case "wrong-application":
				db.lease.ApplicationID = "pubdesc_" + strings.Repeat("f", 64)
			case "expired":
				db.lease.ExpiresAt = time.Now().Add(-time.Second)
			case "disabled":
				db.onRead = func(int) error { return store.ErrSkillHubPublicationDenied }
			case "lost-cloud":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					return nil, context.DeadlineExceeded
				}
			case "nil-cloud":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					return nil, nil
				}
			case "cancel-peer":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					cancel()
					return nil, ctx.Err()
				}
			case "settlement-error":
				db.finishErr = store.ErrSkillHubPublicationStale
			}
			err := w.RunOnce(ctx)
			if mode == "settlement-error" {
				if !errors.Is(err, store.ErrSkillHubPublicationStale) {
					t.Fatal(err)
				}
				return
			}
			want := "retry"
			if mode == "local-busy" {
				want = "progress"
			}
			if err != nil || db.writes != 0 || len(db.outcomes) != 1 || db.outcomes[0] != want {
				t.Fatal("unsafe terminal", db.outcomes, err)
			}
			if mode == "wrong-scope" || mode == "wrong-application" || mode == "expired" || mode == "disabled" {
				if calls.Load() != 0 {
					t.Fatal("unauthorized peer call")
				}
			}
		})
	}
}

func TestSkillHubPublicationDescriptionReconcileStatusOnlyHTTPS(t *testing.T) {
	var status publicationDescriptionCloudReleaseStatus
	calls := 0
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != publicationDescriptionReleaseStatusPath {
			t.Error("reconciler invoked a non-status endpoint")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer peer.Close()
	app, intent, permit := releaseAuthorizeClientFixture(t, peer.URL)
	status = releaseStatusFixture(app, permit)
	_, db, _ := descriptionReconcileFixture(t, "queued")
	cfg := derivationClientConfig(peer)
	cfg.ReleaseStatusEnabled = true
	w, err := NewPublicationDescriptionReconciler(true, cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.ports.ReadCloud(context.Background(), app, intent, permit)
	if err != nil || got == nil || got.State != "queued" || calls != 1 {
		t.Fatal("status-only transport", got, err)
	}
}

// Existing single-call fixture counters are intentionally not concurrency-safe.
// Serialize store operations, but never peer I/O, for the real-client fanout test.
type lockedDescriptionReconcileStore struct {
	*descriptionReconcileTestStore
	mu sync.Mutex
}

func (db *lockedDescriptionReconcileStore) ClaimSkillHubPublicationDescriptionReconcile(ctx context.Context, scope store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationDescriptionReconcileLease, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.descriptionReconcileTestStore.ClaimSkillHubPublicationDescriptionReconcile(ctx, scope)
}
func (db *lockedDescriptionReconcileStore) GetSkillHubPublicationDescriptionReleaseObservation(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseObservation, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.descriptionReconcileTestStore.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
}
func (db *lockedDescriptionReconcileStore) GetSkillHubPublicationDescriptionReleaseTerminal(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.descriptionReconcileTestStore.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, id)
}
func (db *lockedDescriptionReconcileStore) FinishSkillHubPublicationDescriptionReconcile(ctx context.Context, l store.SkillHubPublicationDescriptionReconcileLease, outcome string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.descriptionReconcileTestStore.FinishSkillHubPublicationDescriptionReconcile(ctx, l, outcome)
}

func TestSkillHubPublicationDescriptionReconcileRealClientCapacity(t *testing.T) {
	for _, mode := range []string{"worker-fanout", "client-busy"} {
		t.Run(mode, func(t *testing.T) {
			base, fixture, _ := descriptionReconcileFixture(t, "queued")
			db := &lockedDescriptionReconcileStore{descriptionReconcileTestStore: fixture}
			o := fixture.observation
			replayed := true
			permit := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: o.Intent.ApplicationID,
				EntryID: o.Intent.EntryID, Target: o.Intent.Target, IntentHash: o.Intent.IdentityHash, PermitID: o.Permit.ID,
				AuthorizedAt: o.Permit.AuthorizedAt.UTC().Format(time.RFC3339Nano), Replayed: &replayed}
			status := releaseStatusFixture(o.Continuation.Application, permit)
			entered := make(chan struct{}, 4)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var requests atomic.Int32
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "POST" || r.URL.Path != publicationDescriptionReleaseStatusPath {
					t.Error("non-status request")
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				_ = json.NewEncoder(w).Encode(status)
			}))
			defer peer.Close()
			defer unblock()
			cfg := derivationClientConfig(peer)
			cfg.Issuer, cfg.Audience, cfg.ReleaseStatusEnabled = base.scope.Issuer, base.scope.Audience, true
			cfg.URL = cfg.Audience + publicationDescriptionDerivationPath
			// Preserve the immutable receipt's audience, while routing its
			// configured HTTPS origin to this test's TLS listener.
			peerURL, _ := url.Parse(peer.URL)
			transport := peer.Client().Transport
			cfg.HTTPClient = &http.Client{Transport: derivationRoundTripper(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.URL.Scheme, r.URL.Host = peerURL.Scheme, peerURL.Host
				return transport.RoundTrip(r)
			})}
			worker, err := NewPublicationDescriptionReconciler(true, cfg, db)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 2)
			if mode == "worker-fanout" {
				go func() { worker.Run(ctx); done <- nil }()
			} else {
				// Occupy both real client slots outside the worker, then ensure
				// a claimed observation gets a cost-free progress settlement.
				for i := 0; i < 2; i++ {
					go func() {
						_, err := worker.ports.ReadCloud(ctx, o.Continuation.Application, o.Intent, permit)
						done <- err
					}()
				}
			}
			for i := 0; i < 2; i++ {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("two real status requests did not start")
				}
			}
			if mode == "worker-fanout" {
				for i := 0; i < 4; i++ {
					if err := worker.RunOnce(ctx); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
						t.Fatal("excess observer was not rejected before claim", err)
					}
				}
				if fixture.claims.Load() != 2 {
					t.Fatal("excess observer acquired a lease", fixture.claims.Load())
				}
			} else if err := worker.RunOnce(ctx); err != nil {
				t.Fatal("local client Busy was not requeued", err)
			}
			unblock()
			want := 2
			if mode == "client-busy" {
				want = 1
			}
			deadline := time.After(3 * time.Second)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				db.mu.Lock()
				count := len(fixture.outcomes)
				db.mu.Unlock()
				if count == want {
					break
				}
				select {
				case <-deadline:
					t.Fatal("observations did not settle")
				case <-ticker.C:
				}
			}
			if mode == "worker-fanout" {
				cancel()
			}
			n := 1
			if mode == "client-busy" {
				n = 2
			}
			for i := 0; i < n; i++ {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("observer did not stop")
				}
			}
			for _, outcome := range fixture.outcomes {
				if outcome != "progress" {
					t.Fatal("local capacity created failure backoff", outcome)
				}
			}
			if requests.Load() != 2 || fixture.writes != 0 {
				t.Fatal("unexpected peer request or terminal write", requests.Load(), fixture.writes)
			}
		})
	}
}

func TestSkillHubPublicationDescriptionReconcileGatesAndShutdown(t *testing.T) {
	w, db, _ := descriptionReconcileFixture(t, "queued")
	off, err := NewPublicationDescriptionReconciler(false, PublicationDescriptionDerivationClientConfig{}, db)
	if err != nil || off.RunOnce(context.Background()) != nil || db.claims.Load() != 0 {
		t.Fatal("disabled worker polled")
	}
	if _, err := NewPublicationDescriptionReconciler(true, PublicationDescriptionDerivationClientConfig{}, db); err == nil {
		t.Fatal("invalid enabled config")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RECONCILE_ENABLED", "")
	StartSkillHubPublicationDescriptionReconcile(context.Background(), nil)()
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RECONCILE_ENABLED", "1")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED", "")
	StartSkillHubPublicationDescriptionReconcile(context.Background(), nil)()
	started := make(chan struct{}, 4)
	db.claim = func(ctx context.Context) (*store.SkillHubPublicationDescriptionReconcileLease, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("missing bounded concurrency")
		}
	}
	if err := w.RunOnce(ctx); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("unbounded fanout", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not wait/exit")
	}
	if w.active.Load() != 0 || w.running.Load() {
		t.Fatal("active work leaked")
	}
}
