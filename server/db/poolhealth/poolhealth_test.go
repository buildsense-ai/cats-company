package poolhealth

import (
	"database/sql"
	"sync"
	"testing"
	"time"
)

func TestTrackerSkipsFirstObservation(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	sample := tracker.Observe(1500, base)
	if sample.Valid {
		t.Fatalf("first observation must not report a window: %+v", sample)
	}
}

func TestTrackerReportsDeltaAfterWindow(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if sample := tracker.Observe(1000, base); sample.Valid {
		t.Fatal("first observation must not report a window")
	}

	sample := tracker.Observe(1450, base.Add(time.Minute))
	if !sample.Valid {
		t.Fatal("observation past the sample window must report a delta")
	}
	if sample.Delta != 450 {
		t.Fatalf("delta = %d, want 450", sample.Delta)
	}
	if sample.Elapsed != time.Minute {
		t.Fatalf("elapsed = %s, want 1m", sample.Elapsed)
	}
	if sample.Stale {
		t.Fatal("a one minute window must not be stale")
	}
	if sample.Delta < BurstThreshold {
		t.Fatalf("test fixture must exceed BurstThreshold=%d, got %d", BurstThreshold, sample.Delta)
	}
}

func TestTrackerKeepsDeltaForFastProbes(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if sample := tracker.Observe(500, base); sample.Valid {
		t.Fatal("first observation must not report a window")
	}

	// Faster probes must not consume the accumulated delta...
	if sample := tracker.Observe(700, base.Add(5*time.Second)); sample.Valid {
		t.Fatal("observation inside MinSampleWindow must not report a window")
	}
	if sample := tracker.Observe(900, base.Add(20*time.Second)); sample.Valid {
		t.Fatal("observation inside MinSampleWindow must not report a window")
	}

	// ...so the next eligible probe still sees everything since the last
	// recorded sample.
	sample := tracker.Observe(1200, base.Add(35*time.Second))
	if !sample.Valid {
		t.Fatal("observation past the sample window must report a delta")
	}
	if sample.Delta != 700 {
		t.Fatalf("delta = %d, want 700", sample.Delta)
	}
	if sample.Elapsed != 35*time.Second {
		t.Fatalf("elapsed = %s, want 35s", sample.Elapsed)
	}
}

func TestTrackerSharesVerdictWithFastProbes(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	tracker.Observe(1000, base)
	recorded := tracker.Observe(1500, base.Add(time.Minute))
	if !recorded.Valid || recorded.Delta != 500 {
		t.Fatalf("recorded sample = %+v, want delta 500", recorded)
	}

	// Probes inside the fresh window must repeat the recorded verdict so a
	// second prober cannot silently consume the burst signal.
	repeat := tracker.Observe(1510, base.Add(70*time.Second))
	if repeat != recorded {
		t.Fatalf("fast probe sample = %+v, want recorded verdict %+v", repeat, recorded)
	}
}

func TestTrackerMarksStaleWindow(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	tracker.Observe(0, base)
	sample := tracker.Observe(5000, base.Add(2*time.Hour))
	if !sample.Valid {
		t.Fatal("a long window still reports its delta")
	}
	if !sample.Stale {
		t.Fatalf("window of %s must be stale (max %s)", sample.Elapsed, MaxSampleWindow)
	}
}

func TestTrackerClampsCounterReset(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if sample := tracker.Observe(900, base); sample.Valid {
		t.Fatal("first observation must not report a window")
	}

	sample := tracker.Observe(3, base.Add(time.Minute))
	if !sample.Valid {
		t.Fatal("observation past the sample window must report a delta")
	}
	if sample.Delta != 0 {
		t.Fatalf("delta = %d, want 0 after counter reset", sample.Delta)
	}

	// The reset sample becomes the new baseline.
	next := tracker.Observe(203, base.Add(2*time.Minute))
	if !next.Valid || next.Delta != 200 {
		t.Fatalf("delta after reset = %+v, want 200", next)
	}
}

func TestTrackerViewAdvancesAfterRecordedSample(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	tracker.Observe(1000, base)
	if sample := tracker.Observe(1100, base.Add(time.Minute)); !sample.Valid || sample.Delta != 100 {
		t.Fatalf("second observation must report delta 100, got %+v", sample)
	}

	// The window restarts from the recorded sample, so an immediate follow-up
	// call repeats the recorded verdict instead of measuring again.
	if sample := tracker.Observe(1150, base.Add(70*time.Second)); !sample.Valid || sample.Delta != 100 || sample.Elapsed != time.Minute {
		t.Fatalf("observation inside the fresh window must repeat the verdict, got %+v", sample)
	}

	sample := tracker.Observe(1300, base.Add(2*time.Minute))
	if !sample.Valid || sample.Delta != 200 {
		t.Fatalf("delta = %+v, want 200", sample)
	}
}

func TestTrackerConcurrentObserve(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			offset := time.Duration(step) * 7 * time.Second
			tracker.Observe(100+int64(step), base.Add(offset))
		}(i)
	}
	wg.Wait()
}

func TestEvaluate(t *testing.T) {
	idle := sql.DBStats{MaxOpenConnections: 15}
	saturated := sql.DBStats{MaxOpenConnections: 15, OpenConnections: 15, InUse: 15}

	tests := []struct {
		name    string
		stats   sql.DBStats
		sample  Sample
		status  string
		message string
	}{
		{
			name:   "idle pool without samples is healthy",
			stats:  idle,
			sample: Sample{},
			status: "healthy",
		},
		{
			name:   "idle pool with a quiet window is healthy",
			stats:  idle,
			sample: Sample{Delta: 12, Elapsed: time.Minute, Valid: true},
			status: "healthy",
		},
		{
			name:    "saturated pool warns",
			stats:   saturated,
			sample:  Sample{},
			status:  "warning",
			message: "connection pool saturated",
		},
		{
			name:   "open connections at capacity with idle connections stays healthy",
			stats:  sql.DBStats{MaxOpenConnections: 15, OpenConnections: 15, InUse: 3},
			sample: Sample{},
			status: "healthy",
		},
		{
			name:    "queueing burst warns",
			stats:   idle,
			sample:  Sample{Delta: BurstThreshold, Elapsed: 90 * time.Second, Valid: true},
			status:  "warning",
			message: "recent pool queueing: 300 waits in 1m30s",
		},
		{
			name:   "burst below threshold stays healthy",
			stats:  idle,
			sample: Sample{Delta: BurstThreshold - 1, Elapsed: time.Minute, Valid: true},
			status: "healthy",
		},
		{
			name:   "stale burst is not judged",
			stats:  idle,
			sample: Sample{Delta: 9000, Elapsed: 2 * time.Hour, Valid: true, Stale: true},
			status: "healthy",
		},
		{
			name:    "saturation and burst combine",
			stats:   saturated,
			sample:  Sample{Delta: 800, Elapsed: time.Minute, Valid: true},
			status:  "warning",
			message: "connection pool saturated; recent pool queueing: 800 waits in 1m0s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, message := Evaluate(tc.stats, tc.sample)
			if status != tc.status {
				t.Fatalf("status = %q, want %q (message %q)", status, tc.status, message)
			}
			if tc.message != "" && message != tc.message {
				t.Fatalf("message = %q, want %q", message, tc.message)
			}
			if tc.message == "" && tc.status == "healthy" && message != "" {
				t.Fatalf("healthy verdict must not carry a message, got %q", message)
			}
		})
	}
}
