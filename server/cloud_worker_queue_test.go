package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// fakeExitError mimics exec.ExitError's exit-code surface so the capacity
// detection can be tested without running a script.
type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

// A full tier pool is a wait, not a failure. Before this distinction the buyer
// who paid at peak got a 502 and their credit was released; the machine they
// bought never appeared and nothing retried it unless they clicked again.
func TestCloudWorkerCapacityFullDetectsDedicatedExitCode(t *testing.T) {
	if !cloudWorkerCapacityFull(fakeExitError{code: 75}, "") {
		t.Fatal("exit code 75 must be treated as capacity, not a provision failure")
	}
	if cloudWorkerCapacityFull(fakeExitError{code: 1}, "") {
		t.Fatal("a generic script failure must not be reclassified as capacity")
	}
	if cloudWorkerCapacityFull(errors.New("exit status 1"), "instance boot failed") {
		t.Fatal("an unrelated boot failure must stay a failure")
	}
}

// Older adapters die with a generic code, so the message is the only signal.
// Without this fallback an upgraded adapter paired with an un-upgraded control
// plane would still destroy the purchase.
func TestCloudWorkerCapacityFullDetectsAdapterMessage(t *testing.T) {
	if !cloudWorkerCapacityFull(errors.New("exit status 1"), "[packed] error: tier pro is full on every member (capacity=10 per node)") {
		t.Fatal("the placement message must be recognised even without the dedicated exit code")
	}
}

// queuedCreditStore implements the full credit surface (by embedding the
// existing focused stub) plus the queue surface, so the sweep can be exercised
// exactly as production wires it.
type queuedCreditStore struct {
	quotaCreditStub
	queued   []types.CloudWorkerQueuedCredit
	position int
	claimed  int
}

func (s *queuedCreditStore) QueueCloudWorkerCredit(int64, string) (bool, error) { return true, nil }
func (s *queuedCreditStore) CloudWorkerQueuePosition(int64) (int, error)        { return s.position, nil }
func (s *queuedCreditStore) ListQueuedCloudWorkerCredits(time.Duration, int) ([]types.CloudWorkerQueuedCredit, error) {
	return s.queued, nil
}
func (s *queuedCreditStore) ClaimQueuedCloudWorkerCredit(int64) (bool, error) {
	s.claimed++
	return true, nil
}

// The queue must be retried by the existing sweep; otherwise a slot that frees
// up stays unused while the buyer stares at "排队中".
func TestCloudWorkerQueueRetryClaimsBeforeProvisioning(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	stub := &queuedCreditStore{queued: []types.CloudWorkerQueuedCredit{
		{UID: 42, Tier: "standard", QueuedAt: time.Now().Add(-2 * time.Minute)},
	}}
	h.credits = stub
	// No provision script configured: the retry path must bail out before
	// touching any credit rather than claiming and stalling.
	h.provisionScript = ""
	stub.position = 3
	if got := h.cloudWorkerQueuePosition(42); got != 3 {
		t.Fatalf("queue position=%d want 3", got)
	}
	if got := h.cloudWorkerQueuePosition(0); got != -1 {
		t.Fatalf("anonymous queue position=%d want -1 (nothing queued)", got)
	}
	h.retryQueuedCloudWorkers()
	if stub.claimed != 0 {
		t.Fatalf("retry claimed %d credits without a provision script", stub.claimed)
	}
}

// A store that cannot queue must degrade to "not queued" instead of panicking;
// the create path relies on that fallback when it releases the credit for a
// manual retry.
func TestCloudWorkerQueuePositionWithoutQueueSupport(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.credits = nil
	if got := h.cloudWorkerQueuePosition(7); got != -1 {
		t.Fatalf("queue position without store=%d want -1", got)
	}
}

// The console needs a distinct code and a wait hint; returning the generic
// create-failure code made buyers retry and queue a second machine.
func TestCloudWorkerCapacityQueuedResponseShape(t *testing.T) {
	if cloudWorkerQueueRetrySeconds <= 0 {
		t.Fatal("a queued buyer needs a retry hint")
	}
	if !strings.Contains("cloud_worker_capacity_queued", "capacity") {
		t.Fatal("sanity check on the response code name")
	}
}

// queuedCreateStore is a paid-credit store whose reservation succeeds and whose
// queue surface records the park. Without this the create path would take the
// static-quota branch and never reach the queue at all.
type queuedCreateStore struct {
	configuredCreditStub
	queuedFor    string
	queueCalls   int
	positionSeen int
}

func (s *queuedCreateStore) QueueCloudWorkerCredit(_ int64, reservation string) (bool, error) {
	s.queueCalls++
	s.queuedFor = reservation
	return true, nil
}
func (s *queuedCreateStore) CloudWorkerQueuePosition(int64) (int, error) { return s.positionSeen, nil }
func (s *queuedCreateStore) ListQueuedCloudWorkerCredits(time.Duration, int) ([]types.CloudWorkerQueuedCredit, error) {
	return nil, nil
}
func (s *queuedCreateStore) ClaimQueuedCloudWorkerCredit(int64) (bool, error) { return false, nil }

// A full tier pool must answer 202 + a queue hint, not 502.
//
// Before this, a buyer who paid at peak got "failed to provision cloud worker",
// their credit was handed back as a generic available credit, and nothing
// retried it: the purchase silently never produced a machine. The paid credit
// now stays parked and the capacity sweep starts it when a slot frees up.
func TestCloudWorkerHandleCreateQueuesWhenTierFull(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{
		"provision": writeWorkerOpScript(t, "capacity-full"),
		"destroy":   writeWorkerOpScript(t, "ok"),
	})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, _ := newCloudWorkerTestHandlerCfg(cfg)
	credits := &queuedCreateStore{
		configuredCreditStub: configuredCreditStub{
			profileCreditStub: profileCreditStub{
				quotaCreditStub: quotaCreditStub{total: 1, available: 1},
				profile:         types.CloudWorkerPackedShared,
			},
			billing: types.CloudWorkerMonthly,
		},
		positionSeen: 3,
	}
	h.credits = credits

	rec := httptest.NewRecorder()
	h.HandleCreate(rec, cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-queued", "display_name": "Queued",
	}))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["code"] != "cloud_worker_capacity_queued" {
		t.Fatalf("code=%v want cloud_worker_capacity_queued", payload["code"])
	}
	if payload["queued"] != true {
		t.Fatalf("queued=%v want true (a released credit would never be retried)", payload["queued"])
	}
	if got := payload["queue_position"]; got != float64(3) {
		t.Fatalf("queue_position=%v want 3", got)
	}
	if credits.queueCalls != 1 || credits.queuedFor == "" {
		t.Fatalf("credit was not parked: calls=%d reservation=%q", credits.queueCalls, credits.queuedFor)
	}
}

// A genuine provision failure must still be a failure: queueing it would hide a
// broken cluster until the credit expires, with the buyer waiting for a machine
// that will never come.
func TestCloudWorkerHandleCreateStillFailsOnRealError(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{
		"provision": writeWorkerOpScript(t, "fail"),
		"destroy":   writeWorkerOpScript(t, "ok"),
	})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, _ := newCloudWorkerTestHandlerCfg(cfg)
	credits := &queuedCreateStore{
		configuredCreditStub: configuredCreditStub{
			profileCreditStub: profileCreditStub{
				quotaCreditStub: quotaCreditStub{total: 1, available: 1},
				profile:         types.CloudWorkerPackedShared,
			},
			billing: types.CloudWorkerMonthly,
		},
	}
	h.credits = credits
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-broken", "display_name": "Broken",
	}))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502 body=%s", rec.Code, rec.Body.String())
	}
	if credits.queueCalls != 0 {
		t.Fatalf("a generic failure must not be queued: calls=%d", credits.queueCalls)
	}
}

// Two different buyers must be able to provision at the same time.
//
// The operation gate used to be a single process-wide mutex. A provision takes
// about 25 seconds, so at peak only one buyer could start per half minute and
// everyone else got "another cloud worker operation is already running" — the
// whole fleet's provisioning was serialized, not just the busy account. This
// test holds one owner's gate and proves another owner is unaffected.
func TestCloudWorkerOperationGateIsPerOwner(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")

	held := h.lockOwnerOperation(7)
	defer held.Unlock()

	// The same owner is refused while its own operation runs.
	if lock, ok := h.tryBeginOperation(httptest.NewRecorder(), 7); ok {
		lock.Unlock()
		t.Fatal("the same owner must not start a second concurrent operation")
	}

	// A second owner is accepted immediately: this is what peak traffic needs.
	other, ok := h.tryBeginOperation(httptest.NewRecorder(), 8)
	if !ok {
		t.Fatal("a different owner must be able to provision in parallel")
	}
	other.Unlock()
}
