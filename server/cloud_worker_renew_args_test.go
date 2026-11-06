package server

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 容器没有云订阅可续，部署适配器没法从"订阅周期"推出新的到期时间。老实现
// 于是硬编码 +30 天 —— 7 天套餐续一次白送 23 天，而 30 天套餐刚好碰对，
// 所以这个错误在 30 天套餐上永远测不出来。现在平台把套餐的权威到期时间传下去。
func TestCloudWorkerRenewArgsForwardsPaidWindow(t *testing.T) {
	now := time.Now().UTC()
	paidUntil := now.Add(7 * 24 * time.Hour).Truncate(time.Second)

	store := &packageStoreTestDouble{
		deploymentTestStore: &deploymentTestStore{
			cloudWorkerTestStore: &cloudWorkerTestStore{},
			deployments:          map[string]types.CloudWorkerDeployment{},
		},
		packageExpiresAt: paidUntil,
	}
	h := NewCloudWorkerHandler(store, NewBotHandler(store), CloudWorkerConfig{})

	lifecycle := CloudWorkerLifecycle{
		ID: 1, OwnerUID: 38, TenantName: "bot-bot-a", State: "active",
		PackageExpiresAt: now.Add(-time.Hour),
	}
	args := h.cloudWorkerRenewArgs(38, lifecycle)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--name bot-bot-a") {
		t.Fatalf("租户名丢了: %v", args)
	}
	if !strings.Contains(joined, "--cycle-count 1") {
		t.Fatalf("续费增量必须钉死为 1 个月: %v", args)
	}
	if !strings.Contains(joined, "--expires-at "+paidUntil.Format(time.RFC3339)) {
		t.Fatalf("没有把套餐到期时间传给适配器（7 天套餐会被当成 30 天）: %v", args)
	}
}

// 反向保护：entitlement 比 lifecycle 还短时不能把已付费的窗口往回拉。
func TestCloudWorkerRenewArgsNeverShortensPaidWindow(t *testing.T) {
	now := time.Now().UTC()
	stale := now.Add(24 * time.Hour).Truncate(time.Second)

	store := &packageStoreTestDouble{
		deploymentTestStore: &deploymentTestStore{
			cloudWorkerTestStore: &cloudWorkerTestStore{},
			deployments:          map[string]types.CloudWorkerDeployment{},
		},
		packageExpiresAt: stale,
	}
	h := NewCloudWorkerHandler(store, NewBotHandler(store), CloudWorkerConfig{})

	lifecycle := CloudWorkerLifecycle{
		ID: 1, OwnerUID: 38, TenantName: "bot-bot-a", State: "active",
		PackageExpiresAt: now.Add(60 * 24 * time.Hour),
	}
	args := h.cloudWorkerRenewArgs(38, lifecycle)
	if strings.Contains(strings.Join(args, " "), "--expires-at") {
		t.Fatalf("更短的到期时间不能覆盖已付费窗口: %v", args)
	}
}

// 手工永久授权没有套餐到期时间：不适配器参数，让部署侧自己的兜底生效。
func TestCloudWorkerRenewArgsWithoutPackageExpiry(t *testing.T) {
	h := NewCloudWorkerHandler(&cloudWorkerTestStore{}, NewBotHandler(&cloudWorkerTestStore{}), CloudWorkerConfig{})
	args := h.cloudWorkerRenewArgs(38, CloudWorkerLifecycle{
		ID: 1, OwnerUID: 38, TenantName: "bot-bot-a", State: "active",
	})
	if strings.Contains(strings.Join(args, " "), "--expires-at") {
		t.Fatalf("没有套餐时不该编造到期时间: %v", args)
	}
	if strings.Join(args, " ") != "--name bot-bot-a --cycle-count 1" {
		t.Fatalf("基线参数不对: %v", args)
	}
}
