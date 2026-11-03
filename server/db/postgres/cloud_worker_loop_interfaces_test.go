package postgres

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 生产排查用：确认适配器真的满足 handler 侧用来 type-assert 的每个接口。
//
// 背景（2026-11-03）：生产容器的 docker logs 里 24 分钟 0 条 [cloud-worker]，
// 而 sweepExpiredWorkersLocked 的 `lifecycle sweep scanned=` 是**无条件**打印的。
// 说明它在那之前就 return 了，而它的第一句是：
//
//	store, ok := h.credits.(interface{ ListCloudWorkerLifecycleDue(...) ... })
//	if !ok || h.destroyScript == "" { return nil }   // 两个都是裸 return，无日志
//
// 这类"接口不满足 → 整个后台循环静默停摆"的失败，编译器不会报、运行时不报、
// 监控也看不到。这里用编译期断言把它钉住：适配器一旦漏掉某个方法，
// 这个文件直接编译失败，而不是等到线上"过期套餐永不被销毁"。
var (
	_ interface {
		CloudWorkerCreditSummary(uid int64) (total, available int, err error)
		ReserveCloudWorkerCredit(uid int64, reservation string) (bool, error)
		CommitCloudWorkerCredit(uid int64, reservation string, workerUID int64, tenantName string, graceDays int) error
		ReleaseCloudWorkerCredit(uid int64, reservation string) error
		ExtendCloudWorkerLifecycles(uid int64, expiresAt time.Time, graceDays int) error
		ListCloudWorkerLifecycleDue(now time.Time, limit int) ([]types.CloudWorkerLifecycle, error)
		MarkCloudWorkerLifecyclePending(id int64, deleteAfter time.Time) error
		ClaimCloudWorkerLifecycleDeletion(id int64) (bool, error)
		MarkCloudWorkerLifecycleDeleted(id int64, errText string) error
	} = (*Adapter)(nil)

	// 容量队列（高峰排队的重试路径）
	_ interface {
		QueueCloudWorkerCredit(uid int64, reservation string) (bool, error)
		CloudWorkerQueuePosition(uid int64) (int, error)
		ListQueuedCloudWorkerCredits(minAge time.Duration, limit int) ([]types.CloudWorkerQueuedCredit, error)
		ClaimQueuedCloudWorkerCredit(uid int64) (bool, error)
	} = (*Adapter)(nil)

	// 孤儿额度兜底（支付成功但开通 goroutine 死掉）
	_ interface {
		ListOrphanedCloudWorkerCredits(minAge time.Duration, limit int) ([]int64, error)
	} = (*Adapter)(nil)
)

// 空测试只是为了让 go test 也覆盖到这个文件的断言（编译期已经生效）。
func TestCloudWorkerBackgroundLoopInterfaces(t *testing.T) {
	t.Log("adapter satisfies every interface the cloud-worker background loops type-assert")
}
