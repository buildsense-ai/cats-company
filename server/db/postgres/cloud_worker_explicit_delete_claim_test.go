package postgres

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 显式删除（运维/管理员）把 delete_after 设在当下，而套餐往往还在有效期内。认领条件
// 如果还要求 package_expires_at<=now()，这类行会一直停在 delete_pending，直到套餐自然
// 到期——期间 cloud_worker_cleanup_stuck 每两小时重提醒一次，实例也不会被回收。
// delete_after 才是唯一的删除时钟；过期驱动那条路径写入时就已是过期，因此不受影响。
func TestClaimCloudWorkerLifecycleDeletionHonorsExplicitDelete(t *testing.T) {
	db := commercialPolicyTestDB(t)
	owner := createGLM53MigrationUser(t, db, "explicit-delete")
	expires := pgMicro(time.Now().UTC().Add(30 * 24 * time.Hour))
	due := pgMicro(time.Now().UTC().Add(-time.Hour))
	_, id := seedSharedLifecycleWorker(t, db, owner, "bot-ops-explicit-delete", types.CloudWorkerExpiryLegacy,
		&expires, &due, "delete_pending", "")

	ok, err := db.ClaimCloudWorkerLifecycleDeletion(id)
	if err != nil {
		t.Fatalf("claim explicit delete: %v", err)
	}
	if !ok {
		t.Fatal("a worker whose delete_after has passed must be claimable even while its paid package is still valid")
	}
	if err := db.MarkCloudWorkerLifecycleDeleted(id, ""); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	var state string
	if err := db.db.QueryRow(`SELECT state FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatalf("read lifecycle: %v", err)
	}
	if state != "deleted" {
		t.Fatalf("state = %q, want deleted", state)
	}
}
