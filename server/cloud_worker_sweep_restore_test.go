package server

import (
	"errors"
	"testing"
	"time"
)

// lifecycleSweepStub drives the hourly lifecycle sweep in tests: rows move
// through the same state machine as the PostgreSQL store and the credit
// summary models the buyer's provisioning ticket. The untouched credit-store
// methods are inherited from quotaCreditStub.
type lifecycleSweepStub struct {
	quotaCreditStub
	credit int
	rows   map[int64]CloudWorkerLifecycle
	due    []int64
}

func (s *lifecycleSweepStub) CloudWorkerCreditSummary(int64) (int, int, error) {
	return s.credit, s.credit, nil
}

func (s *lifecycleSweepStub) ListCloudWorkerLifecycleDue(time.Time, int) ([]CloudWorkerLifecycle, error) {
	var items []CloudWorkerLifecycle
	for _, id := range s.due {
		if row, ok := s.rows[id]; ok {
			items = append(items, row)
		}
	}
	return items, nil
}

func (s *lifecycleSweepStub) MarkCloudWorkerLifecyclePending(id int64, deleteAfter time.Time) error {
	row := s.rows[id]
	row.State = "delete_pending"
	row.DeleteAfter = deleteAfter
	s.rows[id] = row
	return nil
}

func (s *lifecycleSweepStub) ClaimCloudWorkerLifecycleDeletion(id int64) (bool, error) {
	row := s.rows[id]
	row.State = "delete_running"
	s.rows[id] = row
	return true, nil
}

func (s *lifecycleSweepStub) MarkCloudWorkerLifecycleDeleted(id int64, errText string) error {
	row := s.rows[id]
	if errText != "" {
		row.State = "delete_failed"
	} else {
		row.State = "deleted"
	}
	s.rows[id] = row
	return nil
}

func (s *lifecycleSweepStub) ListCloudWorkerLifecycles(uid int64) ([]CloudWorkerLifecycle, error) {
	var items []CloudWorkerLifecycle
	for _, row := range s.rows {
		if row.OwnerUID == uid && row.State != "deleted" {
			items = append(items, row)
		}
	}
	return items, nil
}

// errRebuildStop ends AutoProvisionForOwner right after its roster lookup so
// the sweep tests observe the rebuild decision without provisioning anything.
var errRebuildStop = errors.New("stop after rebuild roster lookup")

func newLifecycleSweepHandler(t *testing.T, destroyScript string) (*CloudWorkerHandler, *cloudWorkerTestStore) {
	t.Helper()
	h, ts := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{
		DestroyScript:   destroyScript,
		ProvisionScript: destroyScript,
		CreateQuota:     "7=0",
	})
	// The rebuild path must reach the roster lookup, which then stops it.
	ts.listBotsErr = errRebuildStop
	return h, ts
}

func TestSweepExpiredWorkersRebuildsPaidWorkerAfterDeletion(t *testing.T) {
	script := writeWorkerOpScript(t, "ok")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, ts := newLifecycleSweepHandler(t, script)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending", DeleteAfter: time.Now().Add(-time.Hour)},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "deleted" {
		t.Fatalf("lifecycle state=%q want deleted", got)
	}
	if len(ts.deletedBots) != 1 || ts.deletedBots[0] != 10 {
		t.Fatalf("bot record removals=%v want [10]", ts.deletedBots)
	}
	if ts.listBotsCalls == 0 {
		t.Fatal("worker with a paid provisioning credit was not rebuilt after deletion")
	}
}

func TestSweepExpiredWorkersSkipsRebuildWithoutCredit(t *testing.T) {
	script := writeWorkerOpScript(t, "ok")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, ts := newLifecycleSweepHandler(t, script)
	stub := &lifecycleSweepStub{credit: 0, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending", DeleteAfter: time.Now().Add(-time.Hour)},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "deleted" {
		t.Fatalf("lifecycle state=%q want deleted", got)
	}
	if ts.listBotsCalls != 0 {
		t.Fatalf("unpaid deletion must not rebuild a worker: roster lookups=%d", ts.listBotsCalls)
	}
}

func TestSweepExpiredWorkersSkipsRebuildWhileAnotherWorkerRemains(t *testing.T) {
	script := writeWorkerOpScript(t, "ok")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, ts := newLifecycleSweepHandler(t, script)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending", DeleteAfter: time.Now().Add(-time.Hour)},
		2: {ID: 2, WorkerUID: 11, OwnerUID: 7, TenantName: "bot-bot-b", State: "active"},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "deleted" {
		t.Fatalf("lifecycle state=%q want deleted", got)
	}
	if ts.listBotsCalls != 0 {
		t.Fatalf("a surviving second worker must suppress the rebuild: roster lookups=%d", ts.listBotsCalls)
	}
}

func TestSweepExpiredWorkersSkipsRebuildWhenDestroyFails(t *testing.T) {
	script := writeWorkerOpScript(t, "fail")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, ts := newLifecycleSweepHandler(t, script)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending", DeleteAfter: time.Now().Add(-time.Hour)},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_failed" {
		t.Fatalf("lifecycle state=%q want delete_failed", got)
	}
	if ts.listBotsCalls != 0 {
		t.Fatalf("a failed destroy must not rebuild a worker before operator retry: roster lookups=%d", ts.listBotsCalls)
	}
}
