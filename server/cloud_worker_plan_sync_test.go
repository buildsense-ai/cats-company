package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// An upgrade used to be a database-only fact: fulfilment extended the paid
// window and called the renew script, which only resumes a suspended container
// and rewrites its expiry. The container kept the memory/CPU/disk of the tier it
// was created with, so a customer who bought a bigger package never received it.
// These tests pin the two halves of the fix: resolving the tier from the plan,
// and pushing it to every live worker of the owner.

func TestPlanRuntimeTier(t *testing.T) {
	plans := []*types.CommercialPlan{
		{ID: 7, CloudWorkerRuntimePlan: "  pro  "},
		{ID: 8, CloudWorkerRuntimePlan: ""},
		nil,
	}
	if got := planRuntimeTier(plans, 7); got != "pro" {
		t.Fatalf("tier=%q want pro (must be trimmed)", got)
	}
	if got := planRuntimeTier(plans, 8); got != "" {
		t.Fatalf("tier=%q want empty for a plan without a shared-fleet tier", got)
	}
	if got := planRuntimeTier(plans, 0); got != "" {
		t.Fatalf("tier=%q want empty for an unknown plan id", got)
	}
	if got := planRuntimeTier(nil, 7); got != "" {
		t.Fatalf("tier=%q want empty for no plans", got)
	}
}

// ApplyPlanToOwner must be a no-op when the adapter is not configured: an
// operator who has not deployed the script keeps the old behaviour instead of
// getting a panic or a failed order.
func TestCloudWorkerApplyPlanToOwnerWithoutScript(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	h.credits = &platformLifecycleStub{lifecycles: []CloudWorkerLifecycle{
		{ID: 1, TenantName: "worker1", State: "active"},
	}}
	h.ApplyPlanToOwner(38, "pro") // must not panic, must not call anything
	h.ApplyPlanToOwner(0, "pro")
	h.ApplyPlanToOwner(38, "  ")
}

// The adapter is called once per live worker, with --plan <tier>, and deleted or
// unnamed lifecycles are skipped.
func TestCloudWorkerApplyPlanToOwnerCallsScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script; CI runs on Linux")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "change-plan.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nprintf '{\"status\":\"changed\"}\\n'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	h, _ := newCloudWorkerTestHandler("")
	h.changePlanScript = script
	h.credits = &platformLifecycleStub{lifecycles: []CloudWorkerLifecycle{
		{ID: 1, TenantName: "worker1", State: "active"},
		{ID: 2, TenantName: "worker2", State: "deleted"},
		{ID: 3, TenantName: "", State: "active"},
		{ID: 4, TenantName: "worker4", State: "delete_running"},
	}}

	h.ApplyPlanToOwner(38, "pro")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("script was not executed: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != 1 {
		t.Fatalf("calls=%q want exactly one (only live, named workers)", calls)
	}
	if !strings.Contains(calls[0], "--name worker1") || !strings.Contains(calls[0], "--plan pro") {
		t.Fatalf("call=%q want --name worker1 --plan pro", calls[0])
	}
}
