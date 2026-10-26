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
	"github.com/openchat/openchat/server/store/types"
)

// ---- fake 扩展（同包共享 fakeServiceAgentStore；计划① Task 8）----

// GetServiceAgentDefinition 让 fakeServiceAgentStore 满足带定义查询的扩后接口。
// 记录请求的版本（会话钉定断言用）；定义内容固定（现有用例依赖 gpt-6.1-sol）。
func (f *fakeServiceAgentStore) GetServiceAgentDefinition(_ context.Context, agentID int64, definitionRev int64) (string, int, json.RawMessage, error) {
	for _, a := range f.agents {
		if a.ID == agentID {
			f.mu.Lock()
			f.definitionRevs = append(f.definitionRevs, definitionRev)
			f.mu.Unlock()
			definition := json.RawMessage(`{"async_threshold_seconds": 60, "models": ["gpt-6.1-sol"]}`)
			if custom, ok := f.definitions[agentID]; ok {
				definition = custom
			}
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

// ClaimServiceRunsOwnedBy 模拟 per-user 凭据的认领作用域：只领 ownerUID 名下应用的 run。
func (f *fakeOrchestratorStore) ClaimServiceRunsOwnedBy(_ context.Context, instanceID string, limit int, ownerUID int64) ([]store.ServiceRun, error) {
	if limit <= 0 {
		limit = 10
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := map[int64]bool{}
	for _, agent := range f.agents {
		if agent.OwnerKind == store.ServiceAgentOwnerUser && agent.OwnerUID == ownerUID {
			allowed[agent.ID] = true
		}
	}
	var claimed []store.ServiceRun
	for _, runID := range f.claimable {
		if len(claimed) >= limit {
			break
		}
		run, ok := f.runs[runID]
		if !ok || (run.State != "submitted" && run.State != "orphaned") {
			continue
		}
		session, ok := f.sessions[run.SessionID]
		if !ok || !allowed[session.AgentID] {
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

// ServiceRunOwnerUID 返回 run 所属应用的 owner_uid（internal 应用返回 0）。
func (f *fakeOrchestratorStore) ServiceRunOwnerUID(_ context.Context, runID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return 0, store.ErrServiceRunNotFound
	}
	session, ok := f.sessions[run.SessionID]
	if !ok {
		return 0, store.ErrServiceRunNotFound
	}
	for _, agent := range f.agents {
		if agent.ID != session.AgentID {
			continue
		}
		if agent.OwnerKind != store.ServiceAgentOwnerUser {
			return 0, nil
		}
		return agent.OwnerUID, nil
	}
	return 0, store.ErrServiceAgentNotFound
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

// 公网 worker 心跳必须把**鉴权解出的账号**落进 owner_uid。
//
// 离线预检按 owner 过滤（claim 是 per-user 的），owner 落错等于预检失效：
// 别人的 worker 会被当成「你的」，run 停在 submitted 永远没人领。
// 所以这条盯两件事：uid 正确落库、payload 里伪造 owner 一律无视。
func TestServiceOrchestratorWorkerHeartbeatRecordsOwner(t *testing.T) {
	st, _, _ := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)

	workerToken, _, err := MintServiceWorkerSession(441, time.Hour)
	if err != nil {
		t.Fatalf("mint worker session: %v", err)
	}

	// payload 里塞一个假的 owner_uid：必须被忽略（owner 只能来自凭证）。
	body := `{"owner_uid":999,"active_runs":2,"metrics":{"serve_all":true}}`
	status, payload := doInternal(t, "POST", base+ServiceWorkerAPIPrefix+"/instances/inst-u441/heartbeat", workerToken, body)
	if status != http.StatusOK {
		t.Fatalf("worker heartbeat status=%d body=%s", status, payload)
	}
	st.mu.Lock()
	recorded := st.instanceHeartbeats["inst-u441"]
	st.mu.Unlock()
	if recorded.OwnerUID != 441 {
		t.Fatalf("owner = %d, want 441（必须来自凭证，不能来自 payload）", recorded.OwnerUID)
	}
	if recorded.ActiveRuns != 2 {
		t.Fatalf("active_runs = %d, want 2", recorded.ActiveRuns)
	}
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

// 用户自己电脑上的 worker 只能走公网前缀：生产 nginx 对 /internal/ 一律 404。
// 这条测试盯住公网这组路由不被重构时丢掉，同时确认内部专属端点没有被顺带导出。
func TestServiceOrchestratorPublicWorkerSurface(t *testing.T) {
	st, _, _ := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)

	exposed := []string{
		ServiceWorkerAPIPrefix + "/claim",
		ServiceWorkerAPIPrefix + "/runs/run_x/heartbeat",
		ServiceWorkerAPIPrefix + "/runs/run_x/complete",
		ServiceWorkerAPIPrefix + "/runs/run_x/fail",
		ServiceWorkerAPIPrefix + "/instances/inst-a/heartbeat",
	}
	body := `{"limit":1,"instance_id":"inst-a"}`
	for _, path := range exposed {
		if status, _ := doInternal(t, "POST", base+path, "", body); status != http.StatusUnauthorized {
			t.Fatalf("POST %s 无 token = %d, want 401", path, status)
		}
		if status, _ := doInternal(t, "POST", base+path, "wrong-token", body); status != http.StatusUnauthorized {
			t.Fatalf("POST %s 错 token = %d, want 401", path, status)
		}
	}

	// 内部专属：包分发、checkpoint、events、await-input 不应在公网前缀下活着。
	for _, path := range []string{
		ServiceWorkerAPIPrefix + "/runs/run_x/checkpoint",
		ServiceWorkerAPIPrefix + "/runs/run_x/events",
		ServiceWorkerAPIPrefix + "/runs/run_x/await-input",
		ServiceWorkerAPIPrefix + "/service-agents/demo/publish",
		ServiceWorkerAPIPrefix + "/service-agent-packages/deadbeef",
	} {
		status, _ := doInternal(t, "POST", base+path, "inst-token-1", body)
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s = %d，内部端点不应暴露到公网前缀", path, status)
		}
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
	if entry.AgentSlug != "hello" {
		t.Fatalf("claim agent_slug = %q, want hello", entry.AgentSlug)
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

	// 实例心跳：回执 + 落库（监控数据源）
	status, payload = doInternal(t, "POST", base+"/internal/service-instances/inst-a/heartbeat", "inst-token-1", `{"active_runs":1,"metrics":{"cpu":0.5}}`)
	if status != http.StatusOK || !strings.Contains(string(payload), `"ok":true`) {
		t.Fatalf("instance heartbeat status=%d body=%s", status, payload)
	}
	st.mu.Lock()
	recorded, ok := st.instanceHeartbeats["inst-a"]
	st.mu.Unlock()
	if !ok {
		t.Fatal("instance heartbeat must be persisted")
	}
	if recorded.ActiveRuns != 1 || !strings.Contains(string(recorded.Metrics), `"cpu":0.5`) {
		t.Fatalf("persisted heartbeat = %+v", recorded)
	}
	// 实例 token 是平台内部身份：owner 必须落 0，否则内部 worker 会被当成某个用户的。
	if recorded.OwnerUID != 0 {
		t.Fatalf("internal instance heartbeat owner = %d, want 0", recorded.OwnerUID)
	}
	// 坏体 → 400（不再静默回执）
	if status, _ := doInternal(t, "POST", base+"/internal/service-instances/inst-a/heartbeat", "inst-token-1", `{oops`); status != http.StatusBadRequest {
		t.Fatalf("invalid heartbeat body status = %d, want 400", status)
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

// 会话钉快照（设计 A §2）：claim 按会话创建时固化的 package hash 与 definition_rev 下发；
// 旧式会话（无钉定）回退最新版本 hash——发布新包不影响进行中会话（防"半新半旧"）。
func TestServiceClaimPinsSessionSnapshot(t *testing.T) {
	st := newFakeOrchestratorStore()
	_, base := newOrchestratorServer(t, st)
	ctx := context.Background()
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)

	// 发布 A（v1）→ 建会话（钉定 A；MCP 语义：建会话时固化当时最新）
	if _, err := st.PublishServiceAgentPackage(ctx, "hello", "Hello", json.RawMessage(`{"models":["m1"]}`), hashA, json.RawMessage(`{"schema":"x"}`), false); err != nil {
		t.Fatalf("publish A: %v", err)
	}
	pinnedSession, err := st.CreateServiceSession(ctx, 1, 1, 441, 0, hashA)
	if err != nil {
		t.Fatalf("create pinned session: %v", err)
	}

	// 发布 B（v2；最新变为 B）
	if _, err := st.PublishServiceAgentPackage(ctx, "hello", "Hello", json.RawMessage(`{"models":["m2"]}`), hashB, json.RawMessage(`{"schema":"y"}`), false); err != nil {
		t.Fatalf("publish B: %v", err)
	}

	// 钉定会话 + 旧式会话（钉定上线前创建，definition_rev=0 且无 hash）各挂一个 run
	legacySession, err := st.CreateServiceSession(ctx, 1, 0, 442, 0, "")
	if err != nil {
		t.Fatalf("create legacy session: %v", err)
	}
	st.mu.Lock()
	st.runs["run_pin_1"] = store.ServiceRun{RunID: "run_pin_1", SessionID: pinnedSession, State: "submitted", Input: json.RawMessage(`{}`)}
	st.runs["run_pin_2"] = store.ServiceRun{RunID: "run_pin_2", SessionID: legacySession, State: "submitted", Input: json.RawMessage(`{}`)}
	st.mu.Unlock()
	st.claimable = append(st.claimable, "run_pin_1", "run_pin_2")

	status, payload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, payload)
	}
	var claim ServiceClaimResult
	if err := json.Unmarshal(payload, &claim); err != nil || len(claim.Runs) != 2 {
		t.Fatalf("claim decode: %v %+v", err, claim)
	}
	byRunID := map[string]ServiceClaimRun{}
	for _, entry := range claim.Runs {
		byRunID[entry.RunID] = entry
	}
	if got := byRunID["run_pin_1"].PackageHash; got != hashA {
		t.Fatalf("pinned session package_hash = %q, want %q (不受新发布影响)", got, hashA)
	}
	if got := byRunID["run_pin_2"].PackageHash; got != hashB {
		t.Fatalf("legacy session package_hash = %q, want latest %q (回退语义)", got, hashB)
	}

	// 定义版本透传：钉定会话=其 definition_rev（1）；旧式会话=0（存储层回退最新）
	st.mu.Lock()
	revs := append([]int64(nil), st.definitionRevs...)
	st.mu.Unlock()
	if len(revs) != 2 || revs[0] != 1 || revs[1] != 0 {
		t.Fatalf("definition revs = %v, want [1 0]（按 claim 顺序）", revs)
	}
}

// ---- 回合制返问（awaiting_input）+ 生命周期回注 ----

// AwaitServiceRunInput 覆盖基类 fake：resultIDs 幂等 + 回执写入（与真实 store 同语义）。
func (f *fakeOrchestratorStore) AwaitServiceRunInput(_ context.Context, runID, resultID string, result json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return false, store.ErrServiceRunNotFound
	}
	if run.State == "awaiting_input" && f.resultIDs[runID] == resultID {
		return false, nil // 幂等重放
	}
	if run.State != "running" {
		return false, fmt.Errorf("await service run state=%s: %w", run.State, store.ErrServiceRunConflict)
	}
	run.State = "awaiting_input"
	run.Result = result
	f.runs[runID] = run
	f.resultIDs[runID] = resultID
	f.appendReceiptLocked(runID, run.SessionID, "awaiting_input", store.ServiceAwaitQuestionFromResult(result), "")
	return true, nil
}

type fakeReceiptKicker struct {
	kicks int
}

func (f *fakeReceiptKicker) Kick() { f.kicks++ }

func TestServiceOrchestratorAwaitInput(t *testing.T) {
	st, _, runID := seedOrchestratorFixture(t)
	orchestrator, base := newOrchestratorServer(t, st)
	kicker := &fakeReceiptKicker{}
	orchestrator.SetReceiptKicker(kicker)

	// claim → running
	if status, payload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`); status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, payload)
	}

	awaitBody := `{"result_id":"awt_` + runID + `","result":{"contract_version":"catsco.service-run-result.v1","run_id":"` + runID + `","session_id":"sess_orch_1","status":"awaiting_input","output":{"question":"请提供周报起止日期"}}}`
	// 鉴权：无 token → 401
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/await-input", "", awaitBody); status != http.StatusUnauthorized {
		t.Fatalf("await-input no token = %d, want 401", status)
	}
	// 坏体 → 400
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/await-input", "inst-token-1", `{}`); status != http.StatusBadRequest {
		t.Fatalf("await-input bad body = %d, want 400", status)
	}
	// 正常收尾 → 200 awaited
	status, payload := doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/await-input", "inst-token-1", awaitBody)
	if status != http.StatusOK {
		t.Fatalf("await-input status = %d body=%s", status, payload)
	}
	var resp struct {
		AwaitingInput bool `json:"awaiting_input"`
		Idempotent    bool `json:"idempotent"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil || !resp.AwaitingInput || resp.Idempotent {
		t.Fatalf("await-input resp = %s (err=%v)", payload, err)
	}
	// 投递唤醒：终态后 Kick 一次
	if kicker.kicks != 1 {
		t.Fatalf("kicker kicks = %d, want 1", kicker.kicks)
	}
	// store 状态 + 回执
	run, err := st.GetServiceRun(context.Background(), runID)
	if err != nil || run.State != "awaiting_input" {
		t.Fatalf("run after await-input = %+v err=%v", run, err)
	}
	receipts, err := st.ListUndeliveredServiceRunReceipts(context.Background(), 10)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts = %+v err=%v", receipts, err)
	}
	if receipts[0].Status != "awaiting_input" || !strings.Contains(string(receipts[0].Payload), "请提供周报起止日期") {
		t.Fatalf("receipt payload = %s", receipts[0].Payload)
	}
	if !strings.Contains(string(receipts[0].Payload), `"contract_version":"catsco.service-run-receipt.v1"`) {
		t.Fatalf("receipt contract = %s", receipts[0].Payload)
	}
	// 幂等重放（同 result_id）→ 200 idempotent，不再 Kick
	status, payload = doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/await-input", "inst-token-1", awaitBody)
	if status != http.StatusOK {
		t.Fatalf("await-input replay status = %d body=%s", status, payload)
	}
	resp = struct {
		AwaitingInput bool `json:"awaiting_input"`
		Idempotent    bool `json:"idempotent"`
	}{}
	if err := json.Unmarshal(payload, &resp); err != nil || resp.AwaitingInput || !resp.Idempotent {
		t.Fatalf("await-input replay resp = %s (err=%v)", payload, err)
	}
	if kicker.kicks != 1 {
		t.Fatalf("replay must not kick, kicks = %d", kicker.kicks)
	}
	// 错态（已 awaiting 且不同 result_id）→ 409
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/"+runID+"/await-input", "inst-token-1", `{"result_id":"other","result":{}}`); status != http.StatusConflict {
		t.Fatalf("await-input conflict = %d, want 409", status)
	}
	// 未知 run → 404
	if status, _ := doInternal(t, "POST", base+"/internal/service-runs/run_missing/await-input", "inst-token-1", `{"result_id":"x","result":{}}`); status != http.StatusNotFound {
		t.Fatalf("await-input unknown run = %d, want 404", status)
	}
}

// 回注投递 worker：在线 → 推送并标记送达；离线 → 留存 attempts 累计（离线待取）。
func TestServiceRunReceiptDeliverer(t *testing.T) {
	st := newFakeOrchestratorStore()
	ctx := context.Background()
	sessionID, err := st.CreateServiceSession(ctx, 1, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	st.mu.Lock()
	st.runs["run_rcp_1"] = store.ServiceRun{RunID: "run_rcp_1", SessionID: sessionID, State: "running"}
	st.appendReceiptLocked("run_rcp_1", sessionID, "completed", "", "")
	st.mu.Unlock()

	hub := &Hub{clients: map[int64]map[*Client]struct{}{}}
	deliverer := NewServiceRunReceiptDeliverer(st, hub)

	// 离线：无在线连接 → 不送达，attempts 累计
	if delivered, err := deliverer.SweepOnce(ctx); err != nil || delivered != 0 {
		t.Fatalf("offline sweep delivered=%d err=%v, want 0", delivered, err)
	}
	receipts, _ := st.ListUndeliveredServiceRunReceipts(ctx, 10)
	if len(receipts) != 1 || receipts[0].DeliveryAttempts != 1 {
		t.Fatalf("after offline sweep receipts = %+v", receipts)
	}

	// 上线：注册一个调用方连接 → 下一轮补投 + 标记送达
	clientSend := make(chan []byte, 8)
	client := &Client{uid: 441, accountType: types.AccountHuman, send: clientSend}
	hub.mu.Lock()
	hub.clients[441] = map[*Client]struct{}{client: {}}
	hub.mu.Unlock()
	if delivered, err := deliverer.SweepOnce(ctx); err != nil || delivered != 1 {
		t.Fatalf("online sweep delivered=%d err=%v, want 1", delivered, err)
	}
	receipts, _ = st.ListUndeliveredServiceRunReceipts(ctx, 10)
	if len(receipts) != 0 {
		t.Fatalf("receipt must be marked delivered, pending = %+v", receipts)
	}
	select {
	case raw := <-clientSend:
		var msg ServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil || msg.ServiceRunReceipt == nil {
			t.Fatalf("pushed message = %s (err=%v)", raw, err)
		}
		if msg.ServiceRunReceipt.ReceiptID != "rcp_rcp_1" || msg.ServiceRunReceipt.RunID != "run_rcp_1" ||
			msg.ServiceRunReceipt.Status != "completed" || msg.ServiceRunReceipt.ContractVersion != store.ServiceRunReceiptContract {
			t.Fatalf("receipt message = %+v", msg.ServiceRunReceipt)
		}
	default:
		t.Fatal("expected a pushed receipt on the client channel")
	}
}
