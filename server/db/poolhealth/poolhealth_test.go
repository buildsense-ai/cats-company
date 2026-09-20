package poolhealth

import (
	"testing"
	"time"
)

func TestTrackerSkipsFirstObservation(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	delta, elapsed, ok := tracker.Observe(1500, base)
	if ok || delta != 0 || elapsed != 0 {
		t.Fatalf("first observation must not report a window: delta=%d elapsed=%s ok=%v", delta, elapsed, ok)
	}
}

func TestTrackerReportsDeltaAfterWindow(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, _, ok := tracker.Observe(1000, base); ok {
		t.Fatal("first observation must not report a window")
	}

	delta, elapsed, ok := tracker.Observe(1450, base.Add(time.Minute))
	if !ok {
		t.Fatal("observation past the sample window must report a delta")
	}
	if delta != 450 {
		t.Fatalf("delta = %d, want 450", delta)
	}
	if elapsed != time.Minute {
		t.Fatalf("elapsed = %s, want 1m", elapsed)
	}
	if delta < BurstThreshold {
		t.Fatalf("test fixture must exceed BurstThreshold=%d, got %d", BurstThreshold, delta)
	}
}

func TestTrackerKeepsDeltaForFastProbes(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, _, ok := tracker.Observe(500, base); ok {
		t.Fatal("first observation must not report a window")
	}

	// Faster probes must not consume the accumulated delta...
	if _, _, ok := tracker.Observe(700, base.Add(5*time.Second)); ok {
		t.Fatal("observation inside MinSampleWindow must not report a window")
	}
	if _, _, ok := tracker.Observe(900, base.Add(20*time.Second)); ok {
		t.Fatal("observation inside MinSampleWindow must not report a window")
	}

	// ...so the next eligible probe still sees everything since the last
	// recorded sample.
	delta, elapsed, ok := tracker.Observe(1200, base.Add(35*time.Second))
	if !ok {
		t.Fatal("observation past the sample window must report a delta")
	}
	if delta != 700 {
		t.Fatalf("delta = %d, want 700", delta)
	}
	if elapsed != 35*time.Second {
		t.Fatalf("elapsed = %s, want 35s", elapsed)
	}
}

func TestTrackerClampsCounterReset(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, _, ok := tracker.Observe(900, base); ok {
		t.Fatal("first observation must not report a window")
	}

	delta, _, ok := tracker.Observe(3, base.Add(time.Minute))
	if !ok {
		t.Fatal("observation past the sample window must report a delta")
	}
	if delta != 0 {
		t.Fatalf("delta = %d, want 0 after counter reset", delta)
	}
}

func TestTrackerViewAdvancesAfterRecordedSample(t *testing.T) {
	var tracker Tracker
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	tracker.Observe(1000, base)
	if _, _, ok := tracker.Observe(1100, base.Add(time.Minute)); !ok {
		t.Fatal("second observation must report a delta")
	}

	// The window restarts from the recorded sample, so an immediate follow-up
	// call reports nothing even though the counter moved.
	if _, _, ok := tracker.Observe(1150, base.Add(70*time.Second)); ok {
		t.Fatal("observation inside the fresh window must not report a delta")
	}

	delta, _, ok := tracker.Observe(1300, base.Add(2*time.Minute))
	if !ok {
		t.Fatal("observation past the fresh window must report a delta")
	}
	if delta != 200 {
		t.Fatalf("delta = %d, want 200", delta)
	}
}
