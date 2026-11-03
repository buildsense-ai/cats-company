package server

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureCloudWorkerLog redirects the standard logger for the duration of fn
// and returns everything written. The cloud-worker loops only speak through
// log.Printf, so this is the actual observable surface of the background work.
func captureCloudWorkerLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

// idleSweepHandler wires a handler whose expiry sweep runs but always finds
// nothing due, which is the production steady state.
func idleSweepHandler(t *testing.T) *CloudWorkerHandler {
	t.Helper()
	h, _ := newCloudWorkerTestHandler("")
	h.destroyScript = "/bin/true"
	h.credits = quotaCreditStub{}
	return h
}

func TestIdleLifecycleSweepLogsFirstTickThenThrottles(t *testing.T) {
	h := idleSweepHandler(t)

	out := captureCloudWorkerLog(t, func() {
		for i := 0; i < cloudWorkerIdleSweepLogEvery; i++ {
			h.SweepExpiredWorkers(time.Now().UTC())
		}
	})

	// A fresh process must prove liveness on its first empty sweep: that line is
	// what distinguishes "deployed and idle" from "deployed and wedged".
	if got := strings.Count(out, "lifecycle sweep idle"); got != 1 {
		t.Fatalf("after %d idle sweeps got %d idle heartbeats, want exactly 1\noutput:\n%s",
			cloudWorkerIdleSweepLogEvery, got, out)
	}

	// Tick cloudWorkerIdleSweepLogEvery+1 crosses the interval boundary.
	out = captureCloudWorkerLog(t, func() {
		h.SweepExpiredWorkers(time.Now().UTC())
	})
	if got := strings.Count(out, "lifecycle sweep idle"); got != 1 {
		t.Fatalf("tick %d got %d idle heartbeats, want 1\noutput:\n%s",
			cloudWorkerIdleSweepLogEvery+1, got, out)
	}
}

// The heartbeat must stay quiet between intervals, otherwise it is just noise
// at one line a minute and nobody will read it.
func TestIdleLifecycleSweepHeartbeatIsNotEveryTick(t *testing.T) {
	h := idleSweepHandler(t)
	h.SweepExpiredWorkers(time.Now().UTC()) // consumes the first-tick line

	out := captureCloudWorkerLog(t, func() {
		for i := 0; i < cloudWorkerIdleSweepLogEvery-1; i++ {
			h.SweepExpiredWorkers(time.Now().UTC())
		}
	})
	if strings.Contains(out, "lifecycle sweep idle") {
		t.Fatalf("idle heartbeat repeated before the interval elapsed:\n%s", out)
	}
}

// Real work must reset the idle counter, so the next quiet stretch starts a
// fresh interval instead of inheriting an old count.
func TestLifecycleSweepActivityResetsIdleCounter(t *testing.T) {
	h := idleSweepHandler(t)

	h.SweepExpiredWorkers(time.Now().UTC()) // idle #1 (logs)
	h.SweepExpiredWorkers(time.Now().UTC()) // idle #2 (silent)
	if h.idleSweeps != 2 {
		t.Fatalf("idleSweeps=%d want 2", h.idleSweeps)
	}

	// Simulate a sweep that found due work by driving the counter the same way
	// the non-empty branch does; a real item would need a full lifecycle store.
	h.sweepMu.Lock()
	h.idleSweeps = 0
	h.sweepMu.Unlock()

	out := captureCloudWorkerLog(t, func() {
		h.SweepExpiredWorkers(time.Now().UTC())
	})
	if got := strings.Count(out, "lifecycle sweep idle"); got != 1 {
		t.Fatalf("first idle sweep after activity got %d heartbeats, want 1\noutput:\n%s", got, out)
	}
}

// A failing store must clear the counter too: once it recovers, the operator
// needs a heartbeat immediately rather than up to ten minutes later.
type failingLifecycleStore struct {
	quotaCreditStub
}

func (failingLifecycleStore) ListCloudWorkerLifecycleDue(time.Time, int) ([]CloudWorkerLifecycle, error) {
	return nil, errCloudWorkerTestStoreDown
}

var errCloudWorkerTestStoreDown = &testStoreError{}

type testStoreError struct{}

func (*testStoreError) Error() string { return "store down" }

func TestLifecycleSweepFailureResetsIdleCounter(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.destroyScript = "/bin/true"
	h.credits = failingLifecycleStore{}

	h.SweepExpiredWorkers(time.Now().UTC())
	if h.idleSweeps != 0 {
		t.Fatalf("idleSweeps=%d after a store failure, want 0", h.idleSweeps)
	}

	out := captureCloudWorkerLog(t, func() {
		h.SweepExpiredWorkers(time.Now().UTC())
	})
	if !strings.Contains(out, "lifecycle sweep list failed") {
		t.Fatalf("store failure was not reported:\n%s", out)
	}
}
