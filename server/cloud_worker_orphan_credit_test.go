package server

import (
	"errors"
	"testing"
	"time"
)

// orphanCreditStore adds the orphan-credit surface to the shared credit stub so
// the periodic sweep can be exercised the way production wires it.
type orphanCreditStore struct {
	quotaCreditStub
	orphans []int64
	err     error
	lists   int
}

func (s *orphanCreditStore) ListOrphanedCloudWorkerCredits(time.Duration, int) ([]int64, error) {
	s.lists++
	return s.orphans, s.err
}

// A purchase writes its credit inside the payment transaction and then starts
// provisioning on a bare goroutine. If that goroutine never runs (deploy, panic,
// OOM in the window), the credit stayed 'available' forever and the buyer — who
// had paid in full — never received a machine. Nothing retried it; the only path
// back was the customer noticing and pressing create.
//
// The sweep is that retry. It must actually provision, not merely notice.
func TestSweepRecoversOrphanedPaidCredit(t *testing.T) {
	cfg := workerScriptCfg(t, "42=1", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	h.credits = &orphanCreditStore{
		quotaCreditStub: quotaCreditStub{total: 1, available: 1},
		orphans:         []int64{42},
	}

	h.SweepExpiredWorkers(time.Now().UTC())

	if len(ts.tenantNames) != 1 {
		t.Fatalf("tenantNames=%v want exactly one recovered worker", ts.tenantNames)
	}
}

// An operator grant is not an instruction to build a machine now, so the sweep
// must never look at it. The store query is the scope guard; this pins the
// contract at the handler boundary: no store support, no sweep attempt.
func TestOrphanSweepRequiresStoreSupport(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("")
	h.provisionScript = "/bin/true"
	h.credits = quotaCreditStub{}

	// Must not panic and must not provision anything.
	h.retryOrphanedCloudWorkerCredits()

	if len(ts.tenantNames) != 0 {
		t.Fatalf("tenantNames=%v want none without orphan support", ts.tenantNames)
	}
}

// A permanently broken account (bad image, revoked entitlement, unreachable
// provider) would otherwise spend a create attempt and log a failure on every
// one-minute tick. A fruitless attempt must arm the backoff.
func TestOrphanSweepBacksOffAfterNoProgress(t *testing.T) {
	cfg := workerScriptCfg(t, "42=1", map[string]string{"provision": writeWorkerOpScript(t, "fail")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, _ := newCloudWorkerTestHandlerCfg(cfg)
	h.credits = &orphanCreditStore{
		quotaCreditStub: quotaCreditStub{total: 1, available: 1},
		orphans:         []int64{42},
	}

	h.retryOrphanedCloudWorkerCredits()

	h.orphanMu.Lock()
	until, blocked := h.orphanRetryBlocked[42]
	h.orphanMu.Unlock()
	if !blocked || !until.After(time.Now()) {
		t.Fatalf("fruitless attempt must back off, blocked=%v until=%v", blocked, until)
	}
	// Note: a failed provision deliberately keeps the bot record for support
	// cleanup (cloud_worker_provision_failed_pending_cleanup), so the assertion
	// here is only about the backoff, not about the roster.
}

// While the backoff is armed the sweep must not touch the provider at all, even
// though the credit is still there and would otherwise look recoverable.
func TestOrphanSweepHonoursBackoff(t *testing.T) {
	cfg := workerScriptCfg(t, "42=1", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	h.credits = &orphanCreditStore{
		quotaCreditStub: quotaCreditStub{total: 1, available: 1},
		orphans:         []int64{42},
	}
	h.orphanMu.Lock()
	h.orphanRetryBlocked = map[int64]time.Time{42: time.Now().Add(10 * time.Minute)}
	h.orphanMu.Unlock()

	h.retryOrphanedCloudWorkerCredits()

	if len(ts.tenantNames) != 0 {
		t.Fatalf("tenantNames=%v want none while backing off", ts.tenantNames)
	}
}

// The sweep must tolerate a failing store: one bad query cannot take down the
// expiry sweep that shares the same tick.
func TestOrphanSweepSurvivesStoreError(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.provisionScript = "/bin/true"
	h.credits = &orphanCreditStore{err: errors.New("db down")}

	h.SweepExpiredWorkers(time.Now().UTC())

	if h.orphanRetryBlocked != nil {
		t.Fatalf("a failed list must not mark any owner blocked: %v", h.orphanRetryBlocked)
	}
}
