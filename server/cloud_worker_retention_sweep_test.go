package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The hook invocation is where the rule becomes observable: a retained credit
// environment must ask for a pause with no due date, while a legacy row keeps
// receiving the plan's grace window. The distinction is the stored retain_data
// flag, never the term mode: a legacy shared package also follows its plan.
func TestCloudWorkerExpireArgsCarryNoDeadlineForRetainedRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		item    CloudWorkerLifecycle
		want    string
		comment string
	}{
		{
			name:    "retained credit environment",
			item:    CloudWorkerLifecycle{TenantName: "bot-credit", RetentionDays: 7, RetainData: true},
			want:    "--name bot-credit --retain",
			comment: "a credit environment is paused, never given a deletion deadline",
		},
		{
			name:    "retained credit environment that still records its plan window",
			item:    CloudWorkerLifecycle{TenantName: "bot-credit-window", RetentionDays: 3, RetainData: true},
			want:    "--name bot-credit-window --retain",
			comment: "retention_days stays on the row for the record, but the hook must not act on it",
		},
		{
			name:    "legacy follow_plan row",
			item:    CloudWorkerLifecycle{TenantName: "bot-legacy-follow", RetentionDays: 7, DeleteAfter: time.Now().Add(7 * 24 * time.Hour)},
			want:    "--name bot-legacy-follow --grace-days 7",
			comment: "follow_plan is not the marker: an old shared package keeps its sold retention window",
		},
		{
			name:    "legacy schedule",
			item:    CloudWorkerLifecycle{TenantName: "bot-legacy", RetentionDays: 7, DeleteAfter: time.Now().Add(7 * 24 * time.Hour)},
			want:    "--name bot-legacy --grace-days 7",
			comment: "the legacy retention window must still be forwarded unchanged",
		},
		{
			name:    "legacy without a configured window",
			item:    CloudWorkerLifecycle{TenantName: "bot-old", DeleteAfter: time.Now().Add(time.Hour)},
			want:    "--name bot-old",
			comment: "an unset window keeps the hook's own default, exactly as before",
		},
		{
			name:    "row with no deadline at all",
			item:    CloudWorkerLifecycle{TenantName: "bot-nodate", RetentionDays: 7},
			want:    "--name bot-nodate --retain",
			comment: "with no delete_after there is no deadline to hand over, so nothing may be invented",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(cloudWorkerExpireArgs(tc.item), " "); got != tc.want {
				t.Fatalf("args=%q want %q (%s)", got, tc.want, tc.comment)
			}
		})
	}
}

// P1-2 of the approved credit rules keeps a credit environment (its files and
// its history) with no cleanup deadline, so nothing may purge it at expiry. The
// database side is pinned by TestPostgresCreditEnvironmentKeepsDataOnExpiry;
// these tests pin what the sweep does with such a row: it may pause the machine,
// it must never hand the deployment hook a deletion deadline, and it must never
// destroy. Legacy rows keep their window and their destroy step.
func TestSweepRetainedEnvironmentPausesWithoutDeletionDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture: covered on unix runners")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "expire-args.txt")
	expire := filepath.Join(dir, "expire-record.sh")
	if err := os.WriteFile(expire, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\n"), 0o755); err != nil {
		t.Fatalf("write expire fixture: %v", err)
	}
	destroyMarker := filepath.Join(dir, "destroyed")
	destroy := filepath.Join(dir, "destroy-record.sh")
	if err := os.WriteFile(destroy, []byte("#!/bin/sh\n: > \""+destroyMarker+"\"\n"), 0o755); err != nil {
		t.Fatalf("write destroy fixture: %v", err)
	}
	h, ts := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{
		DestroyScript:   destroy,
		ProvisionScript: destroy,
		ExpireScript:    expire,
		CreateQuota:     "7=0",
	})
	ts.listBotsErr = errRebuildStop
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active",
			PackageExpiresAt: time.Now().Add(-time.Hour), RetentionDays: 7, BillingMode: "month", RetainData: true},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("expire hook did not run: %v", err)
	}
	gotArgs := strings.Fields(string(raw))
	want := []string{"--name", "bot-bot-a", "--retain"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("expire hook args=%v want %v: a credit environment must be paused without a data-deletion deadline", gotArgs, want)
	}
	if _, err := os.Stat(destroyMarker); err == nil {
		t.Fatal("the sweep destroyed a retained environment")
	}
	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending (archived, not deleted)", got)
	}
	if !stub.rows[1].DeleteAfter.IsZero() {
		t.Fatalf("delete_after=%v: the sweep scheduled a cleanup for a retained environment", stub.rows[1].DeleteAfter)
	}
}

// A retained row that some earlier pass already archived must never be claimed
// for deletion, however the clock looks: with no deadline there is nothing that
// could make a destroy legitimate. The destroy script is a missing file, so any
// attempt to run it would show up as delete_failed.
func TestSweepNeverDestroysRetentionOnlyRow(t *testing.T) {
	h, ts := newLifecycleSweepHandler(t, "/nonexistent/destroy.sh")
	ts.listBotsErr = errRebuildStop
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending",
			PackageExpiresAt: time.Now().Add(-90 * 24 * time.Hour), BillingMode: "month", RetainData: true},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending: a row without a cleanup deadline must not be claimed", got)
	}
	if len(ts.deletedBots) != 0 {
		t.Fatalf("bot record removals=%v want none", ts.deletedBots)
	}
}

// Control case: the legacy dedicated lifecycle still expires, is archived and
// then claimed for deletion, so the retention rule cannot silently switch off
// the whole cleanup path.
func TestSweepStillDestroysRowsThatCarryADeadline(t *testing.T) {
	script := writeWorkerOpScript(t, "ok")
	if script == "" {
		t.Skip("no script interpreter available for the destroy step")
	}
	h, ts := newLifecycleSweepHandler(t, script)
	ts.listBotsErr = errRebuildStop
	stub := &lifecycleSweepStub{credit: 0, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "delete_pending",
			DeleteAfter: time.Now().Add(-time.Hour), BillingMode: "month"},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(time.Now().UTC())

	if got := stub.rows[1].State; got != "deleted" {
		t.Fatalf("lifecycle state=%q want deleted: a row whose deadline passed must still be destroyed", got)
	}
}

// The other half of the rule: a legacy shared worker that follows its plan is
// not a credit environment. Its expiry must still hand the cluster the plan's
// grace window and keep the deadline the database already scheduled, otherwise
// P1-2 would silently turn every old shared package into a permanent machine.
func TestSweepLegacyFollowPlanRowKeepsItsGraceWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture: covered on unix runners")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "expire-args.txt")
	expire := filepath.Join(dir, "expire-record.sh")
	if err := os.WriteFile(expire, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\n"), 0o755); err != nil {
		t.Fatalf("write expire fixture: %v", err)
	}
	h, ts := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{
		DestroyScript:   expire,
		ProvisionScript: expire,
		ExpireScript:    expire,
		CreateQuota:     "7=0",
	})
	ts.listBotsErr = errRebuildStop
	now := time.Now().UTC()
	legacyDeadline := now.Add(7 * 24 * time.Hour)
	stub := &lifecycleSweepStub{credit: 1, rows: map[int64]CloudWorkerLifecycle{
		1: {ID: 1, WorkerUID: 10, OwnerUID: 7, TenantName: "bot-bot-a", State: "active",
			PackageExpiresAt: now.Add(-time.Hour), DeleteAfter: legacyDeadline,
			RetentionDays: 7, BillingMode: "month"},
	}, due: []int64{1}}
	h.credits = stub

	h.SweepExpiredWorkers(now)

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("expire hook did not run: %v", err)
	}
	gotArgs := strings.Fields(string(raw))
	want := []string{"--name", "bot-bot-a", "--grace-days", "7"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("expire hook args=%v want %v: a legacy follow_plan row keeps its grace window", gotArgs, want)
	}
	if got := stub.rows[1].State; got != "delete_pending" {
		t.Fatalf("lifecycle state=%q want delete_pending", got)
	}
}
