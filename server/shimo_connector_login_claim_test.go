package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// gatedShimoLifecycleBackend is a ShimoConnectorBackend that also implements the
// remote worker lifecycle. It can pause StartLogin so a test can interleave a
// second request with the first one.
type gatedShimoLifecycleBackend struct {
	mockShimoConnectorBackend

	mu       sync.Mutex
	started  int
	failNext bool

	entered chan struct{}
	release chan struct{}
}

func (b *gatedShimoLifecycleBackend) ConnectionStatus(context.Context, shimoActor) (shimoConnectionStatus, error) {
	return shimoConnectionStatus{State: "disconnected"}, nil
}

func (b *gatedShimoLifecycleBackend) Disconnect(context.Context, shimoActor) error { return nil }

func (b *gatedShimoLifecycleBackend) StartLogin(_ context.Context, _ shimoActor, _ string) (string, error) {
	b.mu.Lock()
	b.started++
	call := b.started
	fail := b.failNext
	b.mu.Unlock()
	// Only the first call is gated. If a regression lets a second caller open
	// a browser session, it returns immediately instead of blocking, so the
	// test fails fast instead of hanging.
	if call == 1 && b.entered != nil {
		b.entered <- struct{}{}
		<-b.release
	}
	if fail {
		return "", errors.New("worker unavailable")
	}
	return "https://app.catsco.test/shimo-login/" + strings.Repeat("c", 64) + "/", nil
}

func (b *gatedShimoLifecycleBackend) startCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

func newShimoLoginAttemptTestHandler(t *testing.T, backend ShimoConnectorBackend) (*ShimoConnectorHandler, string) {
	t.Helper()
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "https://app.catsco.test",
		Backend:     backend,
		WorkerToken: strings.Repeat("w", 40),
	})
	handler.SetLoginResumePublisher(func(ShimoLoginResume) bool { return true })
	actorToken := mustShimoActorToken(t, "usr42", "usr7", "catsco:p2p_7_42:91")
	link := callShimoJSON(t, handler.HandleConnectionLink, http.MethodPost, "/v1/shimo/connection-link", actorToken, "")
	assertShimoStatus(t, link, http.StatusOK, true)
	connectionURL, err := url.Parse(nestedString(link.body, "data", "connection_url"))
	if err != nil {
		t.Fatal(err)
	}
	return handler, connectionURL.Path
}

func (h *ShimoConnectorHandler) resumeCountForTest() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.resumes)
}

func TestShimoLoginAttemptIsClaimedAtomically(t *testing.T) {
	backend := &gatedShimoLifecycleBackend{entered: make(chan struct{}, 1), release: make(chan struct{})}
	handler, loginPath := newShimoLoginAttemptTestHandler(t, backend)

	firstCode := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.HandleLoginAttempt(recorder, httptest.NewRequest(http.MethodGet, loginPath, nil))
		firstCode <- recorder.Code
	}()

	<-backend.entered

	second := httptest.NewRecorder()
	handler.HandleLoginAttempt(second, httptest.NewRequest(http.MethodGet, loginPath, nil))
	if second.Code != http.StatusGone {
		t.Fatalf("concurrent login attempt status=%d, want %d", second.Code, http.StatusGone)
	}
	if backend.startCount() != 1 {
		t.Fatalf("concurrent login opened %d browser sessions, want 1", backend.startCount())
	}
	if got := handler.resumeCountForTest(); got != 1 {
		t.Fatalf("concurrent login registered %d resumes, want 1", got)
	}

	close(backend.release)
	if code := <-firstCode; code != http.StatusSeeOther {
		t.Fatalf("first login attempt status=%d, want %d", code, http.StatusSeeOther)
	}

	replay := httptest.NewRecorder()
	handler.HandleLoginAttempt(replay, httptest.NewRequest(http.MethodGet, loginPath, nil))
	if replay.Code != http.StatusGone {
		t.Fatalf("replayed login attempt status=%d, want %d", replay.Code, http.StatusGone)
	}
	if backend.startCount() != 1 {
		t.Fatalf("replayed login opened %d browser sessions, want 1", backend.startCount())
	}
}

func TestShimoLoginAttemptRollsBackClaimAfterFailure(t *testing.T) {
	backend := &gatedShimoLifecycleBackend{failNext: true}
	handler, loginPath := newShimoLoginAttemptTestHandler(t, backend)

	failed := httptest.NewRecorder()
	handler.HandleLoginAttempt(failed, httptest.NewRequest(http.MethodGet, loginPath, nil))
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("failed login attempt status=%d, want %d", failed.Code, http.StatusBadGateway)
	}
	if got := handler.resumeCountForTest(); got != 0 {
		t.Fatalf("failed login left %d resumes behind, want 0", got)
	}

	backend.mu.Lock()
	backend.failNext = false
	backend.mu.Unlock()

	retry := httptest.NewRecorder()
	handler.HandleLoginAttempt(retry, httptest.NewRequest(http.MethodGet, loginPath, nil))
	if retry.Code != http.StatusSeeOther {
		t.Fatalf("retry login attempt status=%d, want %d", retry.Code, http.StatusSeeOther)
	}
	if backend.startCount() != 2 {
		t.Fatalf("retry total StartLogin calls=%d, want 2", backend.startCount())
	}
}
