package server

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// The post-purchase hook provisions the paid instance without the buyer
// clicking create. It must reuse HandleCreate (quota reservation, tenant
// persistence, provision script, auto-friend) and stay idempotent: renewals
// and repeated fulfillments of an account that already owns a worker skip.
func TestAutoProvisionForOwnerCreatesWorker(t *testing.T) {
	cfg := workerScriptCfg(t, "42=1", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)

	h.AutoProvisionForOwner(42)

	if len(ts.tenantNames) != 1 {
		t.Fatalf("tenantNames=%v want exactly one provisioned worker", ts.tenantNames)
	}
	var botUID int64
	var tenant string
	for uid, name := range ts.tenantNames {
		botUID, tenant = uid, name
	}
	if !strings.HasPrefix(tenant, "bot-bot-42-") {
		t.Fatalf("tenant=%q want bot-bot-42-* prefix", tenant)
	}
	if !ts.friendPairs[agentPairKey(42, botUID)] {
		t.Fatalf("friend was not auto added")
	}
	if len(ts.deletedBots) != 0 {
		t.Fatalf("bot must not be rolled back on success: %v", ts.deletedBots)
	}
}

func TestAutoProvisionForOwnerSkipsWhenWorkerExists(t *testing.T) {
	cfg := workerScriptCfg(t, "42=5", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	// One existing cloud worker (bot with a tenant name) makes the account a
	// renewal/upgrade case: the hook must not provision a second instance.
	ts.ownerBots = []map[string]interface{}{{"id": int64(1001), "tenant_name": "bot-bot-existing"}}

	h.AutoProvisionForOwner(42)

	if len(ts.tenantNames) != 0 {
		t.Fatalf("tenantNames=%v want none when a worker already exists", ts.tenantNames)
	}
}

func TestAutoProvisionForOwnerSkipsWithoutEntitlement(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)

	h.AutoProvisionForOwner(7)

	if len(ts.tenantNames) != 0 {
		t.Fatalf("tenantNames=%v want none without quota or credit", ts.tenantNames)
	}
}

func TestAutoProvisionForOwnerSkipsWithoutProvisionScript(t *testing.T) {
	h, ts := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{CreateQuota: "42=1"})

	h.AutoProvisionForOwner(42)

	if len(ts.tenantNames) != 0 {
		t.Fatalf("tenantNames=%v want none when provision script is missing", ts.tenantNames)
	}
}

func TestAutoCloudWorkerUsernameShape(t *testing.T) {
	username, err := autoCloudWorkerUsername(38)
	if err != nil {
		t.Fatalf("generate username: %v", err)
	}
	if !workerUsernameRe.MatchString(username) {
		t.Fatalf("username=%q violates workerUsernameRe", username)
	}
	if !strings.HasPrefix(username, "bot-38-") {
		t.Fatalf("username=%q want bot-38-* prefix", username)
	}
	other, err := autoCloudWorkerUsername(38)
	if err != nil {
		t.Fatalf("generate second username: %v", err)
	}
	if other == username {
		t.Fatalf("usernames must be unique per call")
	}
}

func TestAutoCloudWorkerDisplayName(t *testing.T) {
	store := &cloudWorkerTestStore{}
	if got := autoCloudWorkerDisplayName(store, 42); got != "Creator的云员工" {
		t.Fatalf("display_name=%q want %q", got, "Creator的云员工")
	}
	long := &cloudWorkerTestStore{creatorUser: &types.User{Username: "creator", DisplayName: strings.Repeat("很", 60)}}
	if got := autoCloudWorkerDisplayName(long, 42); len([]rune(got)) > 40 {
		t.Fatalf("display_name rune length=%d must be bounded to 40", len([]rune(got)))
	}
}

// A concurrent cloud operation holds the per-owner operation lock; the auto
// provision must back off and retry instead of leaving a paid account without
// its worker. The conflict returns before any credit is reserved, so the
// retry cannot double-provision.
func TestAutoProvisionForOwnerRetriesBusyOperation(t *testing.T) {
	cfg := workerScriptCfg(t, "42=1", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	previousDelay := autoProvisionRetryDelay
	autoProvisionRetryDelay = func(int) time.Duration { return 30 * time.Millisecond }
	t.Cleanup(func() { autoProvisionRetryDelay = previousDelay })

	h.opMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.AutoProvisionForOwner(42)
	}()
	time.Sleep(15 * time.Millisecond)
	h.opMu.Unlock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("auto provisioning did not finish")
	}
	if len(ts.tenantNames) != 1 {
		t.Fatalf("tenantNames=%v want exactly one worker after retry", ts.tenantNames)
	}
}
