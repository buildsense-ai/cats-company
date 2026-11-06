package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 生产实测（2026-11-03）：relay 侧 8 个 key 对应的账号在平台 users 表里不存在
// —— 7 个已删的测试账号，加上平台自己的服务 key（cats-company-server，
// uid 9900020699，带的是平台外的合成 uid）。bootstrap 会给这些 key 逐个建商业
// 基线，而建基线的第一步是锁 users 行，注定失败；结果每次进程启动都要打
// 8×4 = 32 条 error、发 32 次无效的 relay 请求。
func TestBootstrapSkipsRelayKeysWithoutPlatformAccount(t *testing.T) {
	store := &commercialRelaySyncTestStore{users: map[int64]*types.User{
		11: {ID: 11, Username: "alice"},
	}}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/internal/usage/users") {
			_ = json.NewEncoder(w).Encode(commercialRelayUsageResponse{
				Users: []commercialRelayUsageUser{
					{UID: 11, Configured: true},
					{UID: 12, Configured: true},         // 账号已删除
					{UID: 9900020699, Configured: true}, // 平台外的合成 uid
				},
				TotalCount: 3,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(relay.Close)

	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{
		baseURL: relay.URL, token: "test", client: relay.Client(),
	}, CommercialRelaySyncerOptions{})

	missing, err := syncer.queueConfiguredRelayUsers(context.Background())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(missing) != 2 || missing[0] != 12 || missing[1] != 9900020699 {
		t.Fatalf("missing = %v, want [12 9900020699]", missing)
	}
	select {
	case request := <-syncer.queue:
		if request.uid != 11 {
			t.Fatalf("queued uid = %d, want 11", request.uid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an existing account must still be queued for sync")
	}
}

// missingAccountSyncStore 模拟「账号不存在」这一终态错误，落在同步的第一步。
type missingAccountSyncStore struct {
	*commercialRelaySyncTestStore
	attempts int32
}

func (s *missingAccountSyncStore) ListCommercialManagedRelayBudgets(int64) ([]*types.CommercialManagedRelayBudget, error) {
	atomic.AddInt32(&s.attempts, 1)
	return nil, fmt.Errorf("load managed relay budgets: %w", store.ErrCommercialRelayUserMissing)
}

func TestSyncStopsRetryingWhenAccountIsGone(t *testing.T) {
	st := &missingAccountSyncStore{commercialRelaySyncTestStore: &commercialRelaySyncTestStore{
		users:   map[int64]*types.User{7: {ID: 7}},
		summary: &types.CommercialSummary{},
	}}
	syncer := NewCommercialRelaySyncer(st, &RelayAdminClient{
		baseURL: "http://127.0.0.1:1", token: "test", client: http.DefaultClient,
	}, CommercialRelaySyncerOptions{
		EnforceEnabled: true,
		// 真出事的时候这几个延迟是 2s/5s/15s；这里压到 1ms 只是为了让「没有重试」
		// 这件事在毫秒级就能断言出来。
		RetryDelays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go syncer.run(ctx)

	syncer.Enqueue(7)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		syncer.pendingMu.Lock()
		_, pending := syncer.pendingUIDs[7]
		syncer.pendingMu.Unlock()
		if !pending {
			if got := atomic.LoadInt32(&st.attempts); got != 1 {
				t.Fatalf("sync attempts = %d, want 1 (a missing account is terminal)", got)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending entry survived; attempts=%d", atomic.LoadInt32(&st.attempts))
}
