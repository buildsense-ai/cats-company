package server

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
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
	notes  map[int64]string
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

func (s *lifecycleSweepStub) MarkCloudWorkerLifecycleAwaitingRecycle(id int64, note string) error {
	row := s.rows[id]
	row.State = "delete_pending"
	s.rows[id] = row
	if s.notes == nil {
		s.notes = map[int64]string{}
	}
	s.notes[id] = note
	return nil
}

func (s *lifecycleSweepStub) MarkCloudWorkerLifecycleDeleted(id int64, errText string) error {
	row := s.rows[id]
	if errText != "" {
		row.State = "delete_failed"
	} else {
		row.State = "deleted"
	}
	s.rows[id] = row
	if s.notes == nil {
		s.notes = map[int64]string{}
	}
	s.notes[id] = errText
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

func newLifecycleSweepHandlerWithExpire(t *testing.T, expireScript string) (*CloudWorkerHandler, *cloudWorkerTestStore) {
	t.Helper()
	ok := writeWorkerOpScript(t, "ok")
	h, ts := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{
		DestroyScript:   ok,
		ProvisionScript: ok,
		ExpireScript:    expireScript,
		CreateQuota:     "7=0",
	})
	ts.listBotsErr = errRebuildStop
	return h, ts
}

// A shared container is billed by its package, not by a cloud subscription.
// The on-demand branch of the sweep exists purely to drive ECS subscription
// billing (convert the trial, or suspend it at trial expiry). Without a profile
// check a packed row that somehow carries ondemand is routed there, the billing
// script is missing on the packed path, the sweep fails and `continue`s past the
// normal expire/archive step — so a shared worker would never expire.
//
// The assertions deliberately avoid running shell scripts: the regression is the
// row's state, which differs before any script executes.
func TestSweepTreatsSharedWorkerAsPackageLifecycle(t *testing.T) {
	ts := &cloudWorkerTestStore{listBotsErr: errRebuildStop}
	store := &deploymentTestStore{
		cloudWorkerTestStore: ts,
		deployments: map[string]types.CloudWorkerDeployment{
			"bot-bot-a": {Profile: types.CloudWorkerPackedShared, Env: map[string]string{}},
		},
	}
	h := NewCloudWorkerHandler(store, NewBotHandler(store), CloudWorkerConfig{
		DestroyScript:   "/nonexistent/destroy.sh",
		ProvisionScript: "/nonexistent/provision.sh",
		CreateQuota:     "7=0",
	})

	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active",
			PackageExpiresAt: time.Now().Add(-time.Hour), DeleteAfter: time.Now().Add(3 * 24 * time.Hour),
			RetentionDays: 3, BillingMode: "ondemand", ConversionPending: true},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending: a shared worker must follow the package expiry path, not the ECS trial path", got)
	}
}

// Control case for the guard above: a dedicated ECS trial still goes through
// the billing branch, where the stub's missing billing store leaves the row
// active instead of silently expiring it.
func TestSweepKeepsDedicatedTrialOnBillingPath(t *testing.T) {
	ts := &cloudWorkerTestStore{listBotsErr: errRebuildStop}
	store := &deploymentTestStore{
		cloudWorkerTestStore: ts,
		deployments: map[string]types.CloudWorkerDeployment{
			"bot-bot-a": {Profile: types.CloudWorkerPrivateNAT, Env: map[string]string{}},
		},
	}
	h := NewCloudWorkerHandler(store, NewBotHandler(store), CloudWorkerConfig{
		DestroyScript:   "/nonexistent/destroy.sh",
		ProvisionScript: "/nonexistent/provision.sh",
		CreateQuota:     "7=0",
	})

	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active",
			PackageExpiresAt: time.Now().Add(-time.Hour), DeleteAfter: time.Now().Add(3 * 24 * time.Hour),
			RetentionDays: 3, BillingMode: "ondemand", ConversionPending: true},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "active" {
		t.Fatalf("lifecycle state=%q want active: a dedicated trial must not skip the billing branch", got)
	}
}

func TestSweepExpiredWorkersPausesPackedWorkerBeforeArchive(t *testing.T) {
	expire := writeWorkerOpScript(t, "ok")
	if expire == "" {
		t.Skip("no script interpreter available for the expire step")
	}
	h, _ := newLifecycleSweepHandlerWithExpire(t, expire)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active", PackageExpiresAt: time.Now().Add(-time.Hour), DeleteAfter: time.Now().Add(14 * 24 * time.Hour), BillingMode: "month"},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending after expire hook", got)
	}
}

func TestSweepExpiredWorkersKeepsRowActiveWhenExpireHookFails(t *testing.T) {
	expire := writeWorkerOpScript(t, "fail")
	if expire == "" {
		t.Skip("no script interpreter available for the expire step")
	}
	h, _ := newLifecycleSweepHandlerWithExpire(t, expire)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active", PackageExpiresAt: time.Now().Add(-time.Hour), DeleteAfter: time.Now().Add(14 * 24 * time.Hour), BillingMode: "month"},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "active" {
		t.Fatalf("lifecycle state=%q want active when expire hook fails", got)
	}
}

// The deployment hook keeps its own suspend deadline (suspended.json) that a
// fallback reaper reads. If the sweep does not forward the plan's retention
// window, that deadline falls back to a fixed default and the two sides disagree
// about when the instance should disappear.
func TestSweepExpiredWorkersForwardsRetentionToExpireHook(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "expire-args.txt")
	expire := filepath.Join(dir, "expire-record.sh")
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture: covered on unix runners")
	}
	if err := os.WriteFile(expire, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\n"), 0o755); err != nil {
		t.Fatalf("write expire fixture: %v", err)
	}
	h, _ := newLifecycleSweepHandlerWithExpire(t, expire)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active",
			PackageExpiresAt: time.Now().Add(-time.Hour), DeleteAfter: time.Now().Add(6 * 24 * time.Hour),
			RetentionDays: 7, BillingMode: "month"},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending after expire hook", got)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("expire hook did not run: %v", err)
	}
	gotArgs := strings.Fields(string(raw))
	want := []string{"--name", "bot-bot-a", "--grace-days", "7"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("expire hook args=%v want %v (retention must reach the deployment hook)", gotArgs, want)
	}
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

func TestSweepExpiredWorkersKeepsAwaitingRecycleRetryable(t *testing.T) {
	// An expired instance past the self-service unsubscribe window cannot be
	// deleted by our scripts: Tianyi recycles retained instances on its own
	// schedule. That outcome is a pending retry, not an operator-retry failure,
	// and nothing local may be deleted while the instance still exists.
	script := writeWorkerOpScript(t, "awaiting-recycle")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, ts := newLifecycleSweepHandler(t, script)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_failed", DeleteAfter: time.Now().Add(-time.Hour)},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending (retryable)", got)
	}
	if !strings.Contains(stub.notes[1], "awaiting-provider-recycle") {
		t.Fatalf("awaiting note=%q want the provider-recycle marker", stub.notes[1])
	}
	if len(ts.deletedBots) != 0 {
		t.Fatalf("awaiting recycle must keep the bot record: removals=%v", ts.deletedBots)
	}
	if ts.listBotsCalls != 0 {
		t.Fatalf("awaiting recycle must not rebuild a worker: roster lookups=%d", ts.listBotsCalls)
	}
}

func TestSweepExpiredWorkersRecordsDestroyOutputForOperators(t *testing.T) {
	// The stored last_error used to be the bare "exit status 1", which hid
	// the provider's refusal from operators; the script output must ride along.
	script := writeWorkerOpScript(t, "fail-detail")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, _ := newLifecycleSweepHandler(t, script)
	stub := &lifecycleSweepStub{credit: 0, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending", DeleteAfter: time.Now().Add(-time.Hour)},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_failed" {
		t.Fatalf("lifecycle state=%q want delete_failed", got)
	}
	if !strings.Contains(stub.notes[1], "provider refused the destroy") {
		t.Fatalf("stored detail=%q must include the script output", stub.notes[1])
	}
}
