// Package poolhealth evaluates database connection pool samples for readiness
// checks.
//
// Go's sql.DBStats counters (WaitCount, WaitDuration) are cumulative for the
// lifetime of the process. Comparing them against a fixed threshold makes any
// long-running process report pool pressure forever, so readiness checks must
// look at what changed since the previous sample instead. Tracker converts the
// cumulative counter into a delta for the most recent observation window and
// Evaluate turns that sample plus the instantaneous pool state into a verdict.
package poolhealth

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// MinSampleWindow is the shortest gap between two recorded samples. Calls
	// that arrive sooner report the previous verdict without consuming the
	// accumulated delta, so fast probes cannot hide a burst.
	MinSampleWindow = 30 * time.Second

	// MaxSampleWindow bounds the window a verdict may be based on. A longer gap
	// (monitor outage, maintenance) still reports the raw delta, but the sample
	// is marked stale and skipped by Evaluate so that one burst from hours ago
	// cannot raise an alarm.
	MaxSampleWindow = 10 * time.Minute

	// BurstThreshold is the number of connection waits within a sample window
	// that indicates real pool pressure rather than background noise. A healthy
	// production replica accumulates only a few waits per minute, while a stalled
	// pool queues thousands.
	BurstThreshold int64 = 300
)

// Sample is the queueing delta measured for one observation window.
type Sample struct {
	// Delta is the number of connection waits since the previous recorded sample.
	Delta int64
	// Elapsed is the length of the window the delta covers.
	Elapsed time.Duration
	// Valid reports whether a window has been measured yet.
	Valid bool
	// Stale reports a window longer than MaxSampleWindow.
	Stale bool
}

// Tracker keeps the previous (timestamp, cumulative wait count) sample. The
// zero value is ready to use.
type Tracker struct {
	mu          sync.Mutex
	lastAt      time.Time
	lastWait    int64
	verdict     Sample
	haveVerdict bool
}

// Observe records the cumulative WaitCount reported at now and returns the
// queueing sample every caller should act on.
//
// The first observation only records a baseline. A call that arrives at least
// MinSampleWindow after the previous recorded sample measures a fresh delta.
// Calls inside the window return the previous verdict instead of an empty
// sample, so concurrent or more frequent probes see the same answer and the
// accumulated delta stays reserved for the next eligible sample.
func (t *Tracker) Observe(waitCount int64, now time.Time) Sample {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.lastAt.IsZero() {
		t.lastAt, t.lastWait = now, waitCount
		return Sample{}
	}

	elapsed := now.Sub(t.lastAt)
	if elapsed >= MinSampleWindow {
		delta := waitCount - t.lastWait
		if delta < 0 {
			// The pool counter moved backwards (database reopened or adapter
			// reconnected); treat the window as fresh instead of reporting a
			// nonsensical negative burst.
			delta = 0
		}
		t.lastAt, t.lastWait = now, waitCount
		t.verdict = Sample{Delta: delta, Elapsed: elapsed, Valid: true, Stale: elapsed > MaxSampleWindow}
		t.haveVerdict = true
		return t.verdict
	}

	if t.haveVerdict {
		return t.verdict
	}
	return Sample{}
}

// Evaluate turns the instantaneous pool state and the queueing sample into a
// readiness verdict. It returns "healthy" with an empty message, or "warning"
// with every reason that fired.
func Evaluate(stats sql.DBStats, sample Sample) (status, message string) {
	var reasons []string
	if stats.MaxOpenConnections > 0 && stats.InUse >= stats.MaxOpenConnections {
		// All connections checked out right now. OpenConnections == max_open
		// with idle connections is normal pool behaviour and is not a reason.
		reasons = append(reasons, "connection pool saturated")
	}
	if sample.Valid && !sample.Stale && sample.Delta >= BurstThreshold {
		reasons = append(reasons, fmt.Sprintf("recent pool queueing: %d waits in %s", sample.Delta, sample.Elapsed.Round(time.Second)))
	}
	if len(reasons) == 0 {
		return "healthy", ""
	}
	return "warning", strings.Join(reasons, "; ")
}
