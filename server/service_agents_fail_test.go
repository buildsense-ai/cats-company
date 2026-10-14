package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// fail 语义（与 PG/MySQL 实现对齐）：仅 running 可 fail；
// retryable=true → orphaned（租约语义，可被重领）；false → failed（终态）。
func (f *fakeOrchestratorStore) FailServiceRun(_ context.Context, runID, _ string, retryable bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return store.ErrServiceRunNotFound
	}
	if run.State != "running" {
		return nil // 宽容：已终态不报错（与 PG 实现一致）
	}
	if retryable {
		run.State = "orphaned"
	} else {
		run.State = "failed"
	}
	f.runs[runID] = run
	return nil
}

func TestServiceOrchestratorFailEndpoint(t *testing.T) {
	st, _, runID := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)
	ctx := context.Background()

	// 未带实例 token → 401
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/fail", "", `{"error_text":"x","retryable":true}`); status != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", status)
	}

	// fail 仅对 running 生效：先 claim
	status, payload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, payload)
	}
	var claim ServiceClaimResult
	if err := json.Unmarshal(payload, &claim); err != nil || len(claim.Runs) != 1 {
		t.Fatalf("claim decode: %v %+v", err, claim)
	}

	// retryable=true → orphaned，可被其他实例重领
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/fail", "inst-token-1", `{"error_text":"transient","retryable":true}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"ok":true`) {
		t.Fatalf("fail(retryable) status=%d body=%s", status, payload)
	}
	if got, err := st.GetServiceRun(ctx, runID); err != nil || got.State != "orphaned" {
		t.Fatalf("after retryable fail = %+v err=%v, want orphaned", got, err)
	}

	// 换实例重领（orphaned 可领）→ fatal fail → failed（终态、不可再领）
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-b"}`)
	if status != http.StatusOK {
		t.Fatalf("reclaim status = %d body=%s", status, payload)
	}
	claim = ServiceClaimResult{}
	if err := json.Unmarshal(payload, &claim); err != nil || len(claim.Runs) != 1 {
		t.Fatalf("reclaim decode: %v %+v", err, claim)
	}
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/fail", "inst-token-1", `{"error_text":"fatal","retryable":false}`)
	if status != http.StatusOK {
		t.Fatalf("fail(fatal) status=%d body=%s", status, payload)
	}
	if got, err := st.GetServiceRun(ctx, runID); err != nil || got.State != "failed" {
		t.Fatalf("after fatal fail = %+v err=%v, want failed", got, err)
	}
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-c"}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"runs":[]`) {
		t.Fatalf("failed run must not be claimable: status=%d body=%s", status, payload)
	}

	// 未知 run → 404
	status, _ = doInternal(t, "POST", base+"/internal/service-runs/run_missing/fail", "inst-token-1", `{"error_text":"x"}`)
	if status != http.StatusNotFound {
		t.Fatalf("missing run status = %d, want 404", status)
	}
}
