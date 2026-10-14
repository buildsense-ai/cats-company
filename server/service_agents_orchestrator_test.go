package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// ---- fake 扩展（同包共享 fakeServiceAgentStore；计划① Task 8）----

// GetServiceAgentDefinition 让 fakeServiceAgentStore 满足带定义查询的扩后接口。
func (f *fakeServiceAgentStore) GetServiceAgentDefinition(_ context.Context, agentID int64) (string, int, json.RawMessage, error) {
	for _, a := range f.agents {
		if a.ID == agentID {
			definition := json.RawMessage(`{"async_threshold_seconds": 60, "models": ["gpt-6.1-sol"]}`)
			return a.Slug, a.AsyncThresholdSeconds, definition, nil
		}
	}
	return "", 0, nil, store.ErrServiceAgentNotFound
}

// fakeOrchestratorStore 在 mcp fake 之上覆盖编排相关语义（claim/lease/reap/checkpoint/complete）。
type fakeOrchestratorStore struct {
	*fakeServiceAgentStore
	claimable   []string
	checkpoints map[string]map[int64]json.RawMessage
	resultIDs   map[string]string
	usage       map[string]json.RawMessage
}

func (f *fakeOrchestratorStore) ClaimServiceRuns(_ context.Context, instanceID string, limit int) ([]store.ServiceRun, error) {
	if limit <= 0 {
		limit = 10
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var claimed []store.ServiceRun
	for _, runID := range f.claimable {
		if len(claimed) >= limit {
			break
		}
		run, ok := f.runs[runID]
		if !ok || (run.State != "submitted" && run.State != "orphaned") {
			continue
		}
		run.State = "running"
		run.InstanceID = instanceID
		run.LeaseEpoch++
		run.Attempts++
		run.LeaseExpiresAt = time.Now().Add(30 * time.Second)
		f.runs[runID] = run
		claimed = append(claimed, run)
	}
	return claimed, nil
}

func (f *fakeOrchestratorStore) RenewServiceLease(_ context.Context, runID, instanceID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.State != "running" || run.InstanceID != instanceID {
		return false, nil
	}
	run.LeaseExpiresAt = time.Now().Add(30 * time.Second)
	f.runs[runID] = run
	return true, nil
}

func (f *fakeOrchestratorStore) SaveServiceCheckpoint(_ context.Context, sessionID string, revision int64, snapshot json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkpoints[sessionID] == nil {
		f.checkpoints[sessionID] = map[int64]json.RawMessage{}
	}
	f.checkpoints[sessionID][revision] = append(json.RawMessage(nil), snapshot...)
	return nil
}

func (f *fakeOrchestratorStore) LatestServiceCheckpoint(_ context.Context, sessionID string) (int64, json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var latest int64
	var snapshot json.RawMessage
	for revision, snap := range f.checkpoints[sessionID] {
		if revision >= latest {
			latest = revision
			snapshot = snap
		}
	}
	return latest, snapshot, nil
}

func (f *fakeOrchestratorStore) CompleteServiceRun(_ context.Context, runID, resultID string, result, usage json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return false, store.ErrServiceRunNotFound
	}
	if run.State == "completed" && f.resultIDs[runID] == resultID {
		return false, nil // 幂等重放
	}
	if run.State != "running" {
		return false, fmt.Errorf("complete service run state=%s: %w", run.State, store.ErrServiceRunConflict)
	}
	run.State = "completed"
	run.Result = result
	f.runs[runID] = run
	f.resultIDs[runID] = resultID
	if len(usage) > 0 {
		f.usage[runID] = usage
	}
	return true, nil
}

func (f *fakeOrchestratorStore) ReapExpiredServiceRuns(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reaped := 0
	now := time.Now()
	for runID, run := range f.runs {
		if run.State == "running" && !run.LeaseExpiresAt.IsZero() && run.LeaseExpiresAt.Before(now) {
			run.State = "orphaned"
			f.runs[runID] = run
			reaped++
		}
	}
	return reaped, nil
}

func newFakeOrchestratorStore() *fakeOrchestratorStore {
	return &fakeOrchestratorStore{
		fakeServiceAgentStore: newFakeServiceAgentStore(),
		checkpoints:           map[string]map[int64]json.RawMessage{},
		resultIDs:             map[string]string{},
		usage:                 map[string]json.RawMessage{},
	}
}

func seedOrchestratorFixture(t *testing.T) (*fakeOrchestratorStore, string, string) {
	t.Helper()
	st := newFakeOrchestratorStore()
	sessionID, err := st.CreateServiceSession(context.Background(), 1, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	runID := "run_orch_1"
	st.mu.Lock()
	st.runs[runID] = store.ServiceRun{RunID: runID, SessionID: sessionID, State: "submitted", Input: json.RawMessage(`{"topic":"x"}`)}
	st.mu.Unlock()
	st.claimable = append(st.claimable, runID)
	return st, sessionID, runID
}

func newOrchestratorServer(t *testing.T, st *fakeOrchestratorStore) (*ServiceRunOrchestrator, string) {
	t.Helper()
	orchestrator := NewServiceRunOrchestrator(st, "inst-token-1", "test-secret")
	mux := http.NewServeMux()
	orchestrator.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return orchestrator, ts.URL
}

func doInternal(t *testing.T, method, url, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, payload
}

// ---- 测试 ----

func TestServiceOrchestratorClaimAuth(t *testing.T) {
	st, _, _ := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)

	body := `{"limit":5,"instance_id":"inst-a"}`
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/claim", "", body); status != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", status)
	}
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/claim", "wrong-token", body); status != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", status)
	}
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", body); status != http.StatusOK {
		t.Fatalf("valid token status = %d, want 200", status)
	}
}

func TestServiceOrchestratorClaimLifecycle(t *testing.T) {
	st, sessionID, runID := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)

	// claim：校验下发字段 + 现场签发的 service token
	status, payload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, payload)
	}
	var claim ServiceClaimResult
	if err := json.Unmarshal(payload, &claim); err != nil {
		t.Fatalf("decode claim: %v body=%s", err, payload)
	}
	if len(claim.Runs) != 1 {
		t.Fatalf("claim runs = %+v", claim.Runs)
	}
	entry := claim.Runs[0]
	if entry.RunID != runID || entry.SessionID != sessionID {
		t.Fatalf("claim ids = %+v", entry)
	}
	if entry.DefinitionRev != 1 || entry.ThresholdSeconds != 60 {
		t.Fatalf("claim rev/threshold = %+v", entry)
	}
	if string(entry.Input) != `{"topic":"x"}` {
		t.Fatalf("claim input = %s", entry.Input)
	}
	if entry.Checkpoint != nil {
		t.Fatalf("checkpoint should be empty before upload: %+v", entry.Checkpoint)
	}
	claims, err := VerifyServiceToken("test-secret", entry.ServiceToken)
	if err != nil {
		t.Fatalf("verify service token: %v", err)
	}
	if claims.CallerUID != 441 || claims.RunID != runID || claims.Agent != "hello" || claims.Purpose != "service" {
		t.Fatalf("service token claims = %+v", claims)
	}
	if len(claims.Models) != 1 || claims.Models[0] != "gpt-6.1-sol" {
		t.Fatalf("service token models = %+v", claims.Models)
	}
	if remaining := time.Until(claims.ExpiresAt); remaining < 3*time.Hour+55*time.Minute || remaining > 4*time.Hour+5*time.Minute {
		t.Fatalf("service token ttl = %v, want ~4h", remaining)
	}
	if _, err := VerifyServiceToken("other-secret", entry.ServiceToken); err == nil {
		t.Fatal("token must not verify with a different secret")
	}

	// checkpoint 上传
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/checkpoint", "inst-token-1", `{"revision":2,"snapshot":{"cursor":9}}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"saved":true`) {
		t.Fatalf("checkpoint status=%d body=%s", status, payload)
	}

	// runs heartbeat：实例匹配才续租
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/heartbeat", "inst-token-1", `{"instance_id":"inst-b"}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"renewed":false`) {
		t.Fatalf("wrong instance renew status=%d body=%s", status, payload)
	}
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/heartbeat", "inst-token-1", `{"instance_id":"inst-a"}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"renewed":true`) {
		t.Fatalf("renew status=%d body=%s", status, payload)
	}

	// 事件
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/events", "inst-token-1", `{"type":"progress","payload":{"pct":50}}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"seq":1`) {
		t.Fatalf("events status=%d body=%s", status, payload)
	}

	// 完成 + 幂等重放（同 result_id）
	completeBody := `{"result_id":"res_1","result":{"contract_version":"catsco.service-run-result.v1","output":{"answer":"hi"}},"usage":{"prompt_tokens":3}}`
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/complete", "inst-token-1", completeBody)
	if status != http.StatusOK || !strings.Contains(string(payload), `"completed":true`) {
		t.Fatalf("complete status=%d body=%s", status, payload)
	}
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/complete", "inst-token-1", completeBody)
	if status != http.StatusOK || !strings.Contains(string(payload), `"idempotent":true`) {
		t.Fatalf("complete replay status=%d body=%s", status, payload)
	}
	finished, err := st.GetServiceRun(context.Background(), runID)
	if err != nil || finished.State != "completed" {
		t.Fatalf("run after complete = %+v err=%v", finished, err)
	}
	if string(finished.Result) != `{"contract_version":"catsco.service-run-result.v1","output":{"answer":"hi"}}` {
		t.Fatalf("stored result = %s", finished.Result)
	}
	if len(st.usage[runID]) == 0 {
		t.Fatal("usage should be forwarded to the store")
	}

	// 实例心跳：只回执
	status, payload = doInternal(t, "POST", base+"/internal/service-instances/inst-a/heartbeat", "inst-token-1", `{"active_runs":1,"metrics":{"cpu":0.5}}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"ok":true`) {
		t.Fatalf("instance heartbeat status=%d body=%s", status, payload)
	}
}

func TestServiceOrchestratorReapAndReclaim(t *testing.T) {
	st, sessionID, runID := seedOrchestratorFixture(t)
	orchestrator, base := newOrchestratorServer(t, st)

	// 第一次领取（inst-a）+ 上传 checkpoint
	status, payload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, payload)
	}
	var claim ServiceClaimResult
	if err := json.Unmarshal(payload, &claim); err != nil || len(claim.Runs) != 1 {
		t.Fatalf("claim decode: %v %+v", err, claim)
	}
	claimedRun, err := st.GetServiceRun(context.Background(), runID)
	if err != nil || claimedRun.LeaseEpoch != 1 || claimedRun.Attempts != 1 {
		t.Fatalf("first claim should bump epoch/attempts in store: %+v err=%v", claimedRun, err)
	}
	status, _ = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/checkpoint", "inst-token-1", `{"revision":3,"snapshot":{"cursor":"p3"}}`)
	if status != http.StatusOK {
		t.Fatalf("checkpoint status = %d", status)
	}

	// 强制租约过期 → ReapOnce → orphaned
	st.mu.Lock()
	run := st.runs[runID]
	run.LeaseExpiresAt = time.Now().Add(-time.Minute)
	st.runs[runID] = run
	st.mu.Unlock()
	reaped, err := orchestrator.ReapOnce(context.Background())
	if err != nil || reaped != 1 {
		t.Fatalf("reap = %d err=%v, want 1", reaped, err)
	}
	if got, _ := st.GetServiceRun(context.Background(), runID); got.State != "orphaned" {
		t.Fatalf("run after reap = %+v", got)
	}

	// 换实例再领：attempts/epoch 递增 + 下发 checkpoint 快照
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-b"}`)
	if status != http.StatusOK {
		t.Fatalf("reclaim status = %d body=%s", status, payload)
	}
	claim = ServiceClaimResult{}
	if err := json.Unmarshal(payload, &claim); err != nil || len(claim.Runs) != 1 {
		t.Fatalf("reclaim decode: %v %+v", err, claim)
	}
	resumed := claim.Runs[0]
	if resumed.RunID != runID {
		t.Fatalf("resumed claim = %+v", resumed)
	}
	if resumed.Checkpoint == nil || resumed.Checkpoint.Revision != 3 || string(resumed.Checkpoint.Snapshot) != `{"cursor":"p3"}` {
		t.Fatalf("resumed checkpoint = %+v", resumed.Checkpoint)
	}
	if final, _ := st.GetServiceRun(context.Background(), runID); final.InstanceID != "inst-b" || final.State != "running" || final.LeaseEpoch != 2 || final.Attempts != 2 {
		t.Fatalf("run after reclaim = %+v", final)
	}
	_ = sessionID
}
