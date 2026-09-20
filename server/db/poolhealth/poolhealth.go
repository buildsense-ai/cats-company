// Package poolhealth evaluates database connection pool samples for readiness
// checks.
//
// Go's sql.DBStats counters (WaitCount, WaitDuration) are cumulative for the
// lifetime of the process. Comparing them against a fixed threshold makes any
// long-running process report pool pressure forever, so readiness checks must
// look at what changed since the previous sample instead. Tracker converts the
// cumulative counter into a delta for the most recent observation window.
package poolhealth

import (
	"sync"
	"time"
)

const (
	// MinSampleWindow is the shortest gap between two recorded samples. Calls
	// that arrive sooner only read the state without consuming the accumulated
	// delta, so fast probes (or repeated readiness hits) cannot hide a burst.
	MinSampleWindow = 30 * time.Second

	// BurstThreshold is the number of connection waits within a sample window
	// that indicates real pool pressure rather than background noise. A healthy
	// production replica accumulates only a few waits per minute, while a stalled
	// pool queues thousands.
	BurstThreshold = int64(300)
)

// Tracker keeps the previous (timestamp, cumulative wait count) sample. The
// zero value is ready to use.
type Tracker struct {
	mu       sync.Mutex
	lastAt   time.Time
	lastWait int64
}

// Observe records the cumulative WaitCount reported at now and reports the
// delta since the previous recorded sample.
//
// It returns ok=false for the first observation and for calls that arrive
// inside MinSampleWindow; in both cases the stored sample is left untouched so
// the next eligible call still sees the full accumulated delta.
func (t *Tracker) Observe(waitCount int64, now time.Time) (delta int64, elapsed time.Duration, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.lastAt.IsZero() {
		t.lastAt, t.lastWait = now, waitCount
		return 0, 0, false
	}

	elapsed = now.Sub(t.lastAt)
	if elapsed < MinSampleWindow {
		return 0, 0, false
	}

	delta = waitCount - t.lastWait
	if delta < 0 {
		// The pool counter moved backwards (database reopened or adapter
		// reconnected); treat the window as fresh instead of reporting a
		// nonsensical negative burst.
		delta = 0
	}
	t.lastAt, t.lastWait = now, waitCount
	return delta, elapsed, true
}
