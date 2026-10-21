package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// fakeServiceAgentStore 是 service 编排存储的内存假实现（MCP 面测试专用）。
type fakeServiceAgentStore struct {
	mu                   sync.Mutex
	agents               []store.ServiceAgentDescriptor
	sessions             map[string]store.ServiceSession
	runs                 map[string]store.ServiceRun
	events               map[string][]store.ServiceRunEventRecord
	tokens               map[string]string // session|token -> runID
	packages             map[string]json.RawMessage
	definitions          map[int64]json.RawMessage      // agentID -> definition（覆盖 fake 默认定义）
	agentPackageVersions map[int64][]fakePackageVersion // agentID -> 版本序列（升序，行为=真实 PG）
	definitionRevs       []int64                        // 记录 claim 请求的定义版本（会话钉定断言用）
	instanceHeartbeats   map[string]fakeInstanceHeartbeat
	receipts             map[string]*store.ServiceRunReceipt // 回执收件箱（回注测试用）
	receiptSeq           []string                            // 回执插入序（ListUndelivered 排序）
	deliveredReceipts    map[string]bool                     // 已送达标记
	hiddenAgents         map[int64]map[int64]bool            // uid → agentID → hidden（目录清单）
	inactive             map[string]bool                     // slug → 已下架（SetServiceAgentState）
	autoCompleteAfter    time.Duration
	autoAwaitAfter       time.Duration // 到点把 run 收尾为 awaiting_input（返问同步路径测试用）
	nextID               int64
}

// fakePackageVersion 模拟 service_agent_versions 的一行（内容寻址 hash + 版本号）。
type fakePackageVersion struct {
	Version int64
	Hash    string
}

func newFakeServiceAgentStore() *fakeServiceAgentStore {
	return &fakeServiceAgentStore{
		agents: []store.ServiceAgentDescriptor{
			{ID: 1, Slug: "hello", Name: "Hello 试点应用", Version: 1, AsyncThresholdSeconds: 60},
		},
		sessions:             map[string]store.ServiceSession{},
		runs:                 map[string]store.ServiceRun{},
		events:               map[string][]store.ServiceRunEventRecord{},
		tokens:               map[string]string{},
		packages:             map[string]json.RawMessage{},
		definitions:          map[int64]json.RawMessage{},
		agentPackageVersions: map[int64][]fakePackageVersion{},
		receipts:             map[string]*store.ServiceRunReceipt{},
		hiddenAgents:         map[int64]map[int64]bool{},
		deliveredReceipts:    map[string]bool{},
	}
}

// ---- 云端包分发（store.ServiceAgentStore 扩展方法）----

func (f *fakeServiceAgentStore) PublishServiceAgentPackage(_ context.Context, slug, name string, _ json.RawMessage, packageHash string, bundle json.RawMessage, force bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	agentID := int64(0)
	for _, a := range f.agents {
		if a.Slug == slug {
			agentID = a.ID
			break
		}
	}
	if agentID == 0 {
		// 自动注册（发布即上架）：与 PG/MySQL 实现同语义。
		if strings.TrimSpace(name) == "" {
			name = slug
		}
		f.nextID++
		agentID = 1000 + f.nextID
		f.agents = append(f.agents, store.ServiceAgentDescriptor{ID: agentID, Slug: slug, Name: name, Version: 1, AsyncThresholdSeconds: 60})
	}
	if _, ok := f.packages[packageHash]; !ok {
		f.packages[packageHash] = append(json.RawMessage(nil), bundle...)
	}
	// 同 hash：非 force 幂等返回既有版本；force 建新版本（回滚通道）。
	for _, pv := range f.agentPackageVersions[agentID] {
		if pv.Hash == packageHash && !force {
			return pv.Version, nil
		}
	}
	next := int64(1)
	if existing := f.agentPackageVersions[agentID]; len(existing) > 0 {
		next = existing[len(existing)-1].Version + 1
	}
	f.agentPackageVersions[agentID] = append(f.agentPackageVersions[agentID], fakePackageVersion{Version: next, Hash: packageHash})
	return next, nil
}

func (f *fakeServiceAgentStore) GetServiceAgentPackage(_ context.Context, packageHash string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bundle, ok := f.packages[packageHash]
	if !ok {
		return nil, store.ErrServicePackageNotFound
	}
	return bundle, nil
}

// ClaimServiceAgentOwnership 内存实现：不存在则建；owner 不同按 internal / 他人占用区分。
func (f *fakeServiceAgentStore) ClaimServiceAgentOwnership(_ context.Context, slug, name, ownerKind string, ownerUID int64, visibility string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slug == "" {
		return store.ErrServiceAgentNotFound
	}
	if name == "" {
		name = slug
	}
	if ownerKind == "" {
		ownerKind = store.ServiceAgentOwnerUser
	}
	if visibility == "" {
		visibility = store.ServiceAgentVisibilityPublic
	}
	for i := range f.agents {
		if f.agents[i].Slug != slug {
			continue
		}
		cur := f.agents[i]
		if cur.OwnerUID != ownerUID || (ownerKind == store.ServiceAgentOwnerUser && cur.OwnerKind != store.ServiceAgentOwnerUser) {
			if cur.OwnerKind == store.ServiceAgentOwnerInternal || cur.OwnerUID == 0 {
				return store.ErrServiceAgentInternalReserved
			}
			return store.ErrServiceAgentOwnedByOther
		}
		f.agents[i].Name = name
		f.agents[i].Visibility = visibility
		// 重新发布把下架的条目拉回 active（真实 PG 的 ClaimServiceAgentOwnership 同样如此）。
		delete(f.inactive, slug)
		return nil
	}
	f.nextID++
	f.agents = append(f.agents, store.ServiceAgentDescriptor{
		ID: 1000 + f.nextID, Slug: slug, Name: name, Version: 1, AsyncThresholdSeconds: 60,
		OwnerKind: ownerKind, OwnerUID: ownerUID, Visibility: visibility,
	})
	return nil
}

func (f *fakeServiceAgentStore) GetServiceAgentPackageHash(_ context.Context, agentID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	versions := f.agentPackageVersions[agentID]
	if len(versions) == 0 {
		return "", nil
	}
	return versions[len(versions)-1].Hash, nil
}

func (f *fakeServiceAgentStore) GetServiceAgentBySlug(_ context.Context, slug string) (int64, string, int64, int, error) {
	for _, a := range f.agents {
		if a.Slug == slug {
			if f.inactive[slug] {
				continue // 下架后等同不存在（真实 PG 用 state='active' 过滤）
			}
			return a.ID, a.Name, a.Version, a.AsyncThresholdSeconds, nil
		}
	}
	return 0, "", 0, 0, store.ErrServiceAgentNotFound
}

// SetServiceAgentState 模拟下架/上架：只有 user owner 本人能操作。
func (f *fakeServiceAgentStore) SetServiceAgentState(_ context.Context, slug string, ownerUID int64, active bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agents {
		if a.Slug != slug {
			continue
		}
		if a.OwnerKind != store.ServiceAgentOwnerUser || a.OwnerUID != ownerUID {
			return store.ErrServiceAgentOwnedByOther
		}
		if f.inactive == nil {
			f.inactive = map[string]bool{}
		}
		f.inactive[slug] = !active
		return nil
	}
	return store.ErrServiceAgentNotFound
}

func (f *fakeServiceAgentStore) ListServiceAgents(context.Context) ([]store.ServiceAgentDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.ServiceAgentDescriptor, 0, len(f.agents))
	for _, a := range f.agents {
		if f.inactive[a.Slug] {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeServiceAgentStore) CreateServiceSession(_ context.Context, agentID, definitionRev, callerUID, callerBotUID int64, assetsHash string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("sess_fake_%d", f.nextID)
	f.sessions[id] = store.ServiceSession{
		SessionID: id, AgentID: agentID, DefinitionRev: definitionRev, AssetsHash: assetsHash,
		CallerUID: callerUID, CallerBotUID: callerBotUID, State: "active", CreatedAt: time.Now(),
	}
	return id, nil
}

func (f *fakeServiceAgentStore) GetServiceSession(_ context.Context, sessionID string) (store.ServiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return store.ServiceSession{}, store.ErrServiceSessionNotFound
	}
	return s, nil
}

func (f *fakeServiceAgentStore) GetServiceRun(_ context.Context, runID string) (store.ServiceRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return store.ServiceRun{}, store.ErrServiceRunNotFound
	}
	return r, nil
}

func (f *fakeServiceAgentStore) CreateServiceRunIdempotent(_ context.Context, sessionID, clientToken string, input json.RawMessage) (store.ServiceRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sessionID + "|" + clientToken
	if clientToken != "" {
		if runID, ok := f.tokens[key]; ok {
			return f.runs[runID], true, nil
		}
	}
	for _, r := range f.runs {
		if r.SessionID == sessionID && (r.State == "submitted" || r.State == "running") {
			return store.ServiceRun{}, false, store.ErrServiceSessionBusy
		}
	}
	f.nextID++
	runID := fmt.Sprintf("run_fake_%d", f.nextID)
	run := store.ServiceRun{RunID: runID, SessionID: sessionID, State: "submitted", ClientToken: clientToken, Input: input}
	run.LeaseEpoch = 1
	f.runs[runID] = run
	f.tokens[key] = runID
	if f.autoCompleteAfter > 0 {
		delay := f.autoCompleteAfter
		go func() {
			time.Sleep(delay)
			f.completeRun(runID)
		}()
	}
	if f.autoAwaitAfter > 0 {
		delay := f.autoAwaitAfter
		go func() {
			time.Sleep(delay)
			f.awaitInputRun(runID)
		}()
	}
	return run, false, nil
}

func (f *fakeServiceAgentStore) completeRun(runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.State == "completed" {
		return
	}
	run.State = "completed"
	env := ServiceRunEnvelope{
		ContractVersion: "catsco.service-run-result.v1",
		RunID:           runID,
		SessionID:       run.SessionID,
		Status:          ServiceRunCompleted,
		Output:          json.RawMessage(`{"answer":"hello"}`),
		Usage:           &ServiceRunUsage{PromptTokens: 3, CompletionTokens: 5},
	}
	payload, _ := json.Marshal(env)
	run.Result = payload
	f.runs[runID] = run
	f.appendReceiptLocked(runID, run.SessionID, "completed", "", "")
	f.events[runID] = append(f.events[runID], store.ServiceRunEventRecord{
		RunID: runID, Seq: int64(len(f.events[runID]) + 1), Type: "result", Payload: payload, CreatedAt: time.Now(),
	})
}

// awaitInputRun 模拟实例把 run 收尾为 awaiting_input（返问；含 result 事件 + 回执）。
func (f *fakeServiceAgentStore) awaitInputRun(runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.State == "awaiting_input" || run.State == "completed" {
		return
	}
	run.State = "awaiting_input"
	env := ServiceRunEnvelope{
		ContractVersion: "catsco.service-run-result.v1",
		RunID:           runID,
		SessionID:       run.SessionID,
		Status:          ServiceRunAwaitingInput,
		Output:          json.RawMessage(`{"question":"请补充必要素材"}`),
		Usage:           &ServiceRunUsage{PromptTokens: 2, CompletionTokens: 3},
	}
	payload, _ := json.Marshal(env)
	run.Result = payload
	f.runs[runID] = run
	f.appendReceiptLocked(runID, run.SessionID, "awaiting_input", "请补充必要素材", "")
	f.events[runID] = append(f.events[runID], store.ServiceRunEventRecord{
		RunID: runID, Seq: int64(len(f.events[runID]) + 1), Type: "result", Payload: payload, CreatedAt: time.Now(),
	})
}

func (f *fakeServiceAgentStore) ClaimServiceRuns(context.Context, string, int) ([]store.ServiceRun, error) {
	return nil, nil
}

// StartServiceRun 内存实现：submitted → running（外部执行后端用）。
func (f *fakeServiceAgentStore) StartServiceRun(_ context.Context, runID, instanceID string, _ int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return false, store.ErrServiceRunNotFound
	}
	if run.State != "submitted" {
		return false, nil
	}
	run.State = "running"
	run.InstanceID = instanceID
	f.runs[runID] = run
	return true, nil
}

// ListBillableServiceRuns / RecordServiceRunLedger：默认空实现（账本用例用专属替身）。
func (f *fakeServiceAgentStore) ListBillableServiceRuns(context.Context, int) ([]store.ServiceRunBillable, error) {
	return nil, nil
}

func (f *fakeServiceAgentStore) RecordServiceRunLedger(context.Context, store.ServiceRunLedgerEntry) (bool, error) {
	return false, nil
}

func (f *fakeServiceAgentStore) SumPendingCallerChargeMicros(context.Context, int64) (int64, error) {
	return 0, nil
}

func (f *fakeServiceAgentStore) RenewServiceLease(context.Context, string, string) (bool, error) {
	return false, nil
}

func (f *fakeServiceAgentStore) AppendServiceRunEvent(_ context.Context, runID, typ string, payload json.RawMessage) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seq := int64(len(f.events[runID]) + 1)
	f.events[runID] = append(f.events[runID], store.ServiceRunEventRecord{
		RunID: runID, Seq: seq, Type: typ, Payload: payload, CreatedAt: time.Now(),
	})
	return seq, nil
}

func (f *fakeServiceAgentStore) ListServiceRunEvents(_ context.Context, runID string, afterSeq int64, limit int) ([]store.ServiceRunEventRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.ServiceRunEventRecord
	for _, ev := range f.events[runID] {
		if ev.Seq > afterSeq {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeServiceAgentStore) SaveServiceCheckpoint(context.Context, string, int64, json.RawMessage) error {
	return nil
}

func (f *fakeServiceAgentStore) LatestServiceCheckpoint(context.Context, string) (int64, json.RawMessage, error) {
	return 0, nil, nil
}

func (f *fakeServiceAgentStore) CompleteServiceRun(_ context.Context, runID, resultID string, result, usage json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return false, store.ErrServiceRunNotFound
	}
	if run.State == "completed" {
		return false, nil
	}
	run.State = "completed"
	run.Result = result
	f.runs[runID] = run
	f.appendReceiptLocked(runID, run.SessionID, "completed", "", "")
	return true, nil
}

// AwaitServiceRunInput 模拟回合制返问收尾（含回执，question 从 result 提取）。
func (f *fakeServiceAgentStore) AwaitServiceRunInput(_ context.Context, runID, resultID string, result json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return false, store.ErrServiceRunNotFound
	}
	if run.State == "awaiting_input" {
		return false, nil
	}
	if run.State != "running" && run.State != "submitted" {
		return false, fmt.Errorf("await service run state=%s: %w", run.State, store.ErrServiceRunConflict)
	}
	run.State = "awaiting_input"
	run.Result = result
	f.runs[runID] = run
	f.appendReceiptLocked(runID, run.SessionID, "awaiting_input", store.ServiceAwaitQuestionFromResult(result), "")
	return true, nil
}

func (f *fakeServiceAgentStore) FailServiceRun(_ context.Context, runID, errorText string, retryable bool) error {
	if retryable {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return store.ErrServiceRunNotFound
	}
	run.State = "failed"
	f.runs[runID] = run
	f.appendReceiptLocked(runID, run.SessionID, "failed", "", errorText)
	return nil
}

func (f *fakeServiceAgentStore) ReapExpiredServiceRuns(context.Context) (int, error) {
	return 0, nil
}

// RecordServiceInstanceHeartbeat 记录实例心跳（监控落库断言用）。
func (f *fakeServiceAgentStore) RecordServiceInstanceHeartbeat(_ context.Context, instanceID string, activeRuns int, metrics json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.instanceHeartbeats == nil {
		f.instanceHeartbeats = map[string]fakeInstanceHeartbeat{}
	}
	f.instanceHeartbeats[instanceID] = fakeInstanceHeartbeat{ActiveRuns: activeRuns, Metrics: append(json.RawMessage(nil), metrics...)}
	return nil
}

// fakeInstanceHeartbeat 是 RecordServiceInstanceHeartbeat 的落库快照（测试断言用）。
type fakeInstanceHeartbeat struct {
	ActiveRuns int
	Metrics    json.RawMessage
}

// appendReceiptLocked 写入回执（模拟 store 终态事务中的回执插入；同 run 幂等）。
func (f *fakeServiceAgentStore) appendReceiptLocked(runID, sessionID, status, question, errorText string) {
	receiptID := store.ServiceRunReceiptIDForRun(runID)
	if _, ok := f.receipts[receiptID]; ok {
		return
	}
	callerUID := int64(0)
	if s, ok := f.sessions[sessionID]; ok {
		callerUID = s.CallerUID
	}
	payload := store.BuildServiceRunReceiptPayload(receiptID, runID, sessionID, status, question, errorText)
	f.receipts[receiptID] = &store.ServiceRunReceipt{
		ReceiptID: receiptID, RunID: runID, SessionID: sessionID,
		CallerUID: callerUID, Status: status, Payload: payload,
	}
	f.receiptSeq = append(f.receiptSeq, receiptID)
}

func (f *fakeServiceAgentStore) ListUndeliveredServiceRunReceipts(_ context.Context, limit int) ([]store.ServiceRunReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.ServiceRunReceipt
	for _, id := range f.receiptSeq {
		if f.deliveredReceipts[id] {
			continue
		}
		receipt := f.receipts[id]
		if receipt == nil {
			continue
		}
		out = append(out, *receipt)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeServiceAgentStore) MarkServiceRunReceiptDelivered(_ context.Context, receiptID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveredReceipts[receiptID] = true
	return nil
}

func (f *fakeServiceAgentStore) TouchServiceRunReceiptDelivery(_ context.Context, receiptID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if receipt := f.receipts[receiptID]; receipt != nil {
		receipt.DeliveryAttempts++
	}
	return nil
}

// ---- 应用目录订阅清单（设计 §3.3-9）----

func (f *fakeServiceAgentStore) ListHiddenServiceAgentIDs(_ context.Context, uid int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for agentID, hidden := range f.hiddenAgents[uid] {
		if hidden {
			out = append(out, agentID)
		}
	}
	return out, nil
}

func (f *fakeServiceAgentStore) SetServiceAgentSubscriptionHidden(_ context.Context, uid, agentID int64, hidden bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hiddenAgents[uid] == nil {
		f.hiddenAgents[uid] = map[int64]bool{}
	}
	if hidden {
		f.hiddenAgents[uid][agentID] = true
	} else {
		delete(f.hiddenAgents[uid], agentID)
	}
	return nil
}

func mcpAuthStub(uid int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), uidKey, uid)))
	})
}

func newMCPSession(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "svc-test", Version: "0.1.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: http.DefaultClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect mcp: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func decodeServiceEnvelope(t *testing.T, structured any) ServiceRunEnvelope {
	t.Helper()
	raw, err := json.Marshal(structured)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var env ServiceRunEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope %s: %v", raw, err)
	}
	return env
}

func TestServiceAgentsMCPRequiresAuth(t *testing.T) {
	st := newFakeServiceAgentStore()
	handler := NewServiceAgentsMCPHandler(st)
	guarded := JWTAuthMiddlewareWithDB(nil)(handler.ServeHTTP)
	ts := httptest.NewServer(guarded)
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatalf("post initialize: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestServiceAgentsMCPListAndSyncCall(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	// 先注册后执行：MCP session 的 Cleanup（后注册）先关闭 SSE 流，再关 httptest server。
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var hello *mcp.Tool
	hasStatusTool := false
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "catsco_hello":
			hello = tool
		case "catsco_run_status":
			hasStatusTool = true
		}
	}
	if hello == nil {
		t.Fatalf("catsco_hello missing: %+v", tools.Tools)
	}
	if hello.Description == "" || hello.InputSchema == nil {
		t.Fatalf("hello tool shape: %+v", hello)
	}
	if !hasStatusTool {
		t.Fatal("catsco_run_status missing")
	}

	args := map[string]any{"input": map[string]any{"topic": "x"}, "client_token": "ct_mcp_1"}
	call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: args})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if call.IsError {
		t.Fatalf("unexpected tool error: %+v", call.Content)
	}
	env := decodeServiceEnvelope(t, call.StructuredContent)
	if env.ContractVersion != "catsco.service-run-result.v1" || env.Status != ServiceRunCompleted {
		t.Fatalf("sync envelope = %+v", env)
	}
	if env.SessionID == "" || env.RunID == "" {
		t.Fatalf("session/run ids missing: %+v", env)
	}

	// 同会话同 client_token 重发 → 幂等命中同一 run（幂等键作用域=会话内）
	replay, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{
		"input": map[string]any{"topic": "x"}, "client_token": "ct_mcp_1", "session_id": env.SessionID,
	}})
	if err != nil {
		t.Fatalf("replay tool: %v", err)
	}
	envReplay := decodeServiceEnvelope(t, replay.StructuredContent)
	if envReplay.RunID != env.RunID || envReplay.SessionID != env.SessionID {
		t.Fatalf("idempotency broken: first=%+v replay=%+v", env, envReplay)
	}
}

func TestServiceAgentsMCPAsyncHandleAndStatus(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 300 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	handler.syncWait = 50 * time.Millisecond
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	call, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "catsco_hello",
		Arguments: map[string]any{"input": map[string]any{"q": 1}, "client_token": "ct_mcp_async"},
	})
	if err != nil {
		t.Fatalf("async call: %v", err)
	}
	if call.IsError {
		t.Fatalf("unexpected tool error: %+v", call.Content)
	}
	handle := decodeServiceEnvelope(t, call.StructuredContent)
	if handle.ContractVersion != "catsco.service-run-async.v1" || handle.Status != ServiceRunRunning {
		t.Fatalf("async handle = %+v", handle)
	}
	if handle.RunID == "" || handle.SessionID == "" {
		t.Fatalf("handle ids missing: %+v", handle)
	}

	time.Sleep(400 * time.Millisecond)
	status, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "catsco_run_status",
		Arguments: map[string]any{"run_id": handle.RunID},
	})
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	if status.IsError {
		t.Fatalf("unexpected status error: %+v", status.Content)
	}
	final := decodeServiceEnvelope(t, status.StructuredContent)
	if final.Status != ServiceRunCompleted || final.ContractVersion != "catsco.service-run-result.v1" {
		t.Fatalf("final envelope = %+v", final)
	}
}

func TestServiceAgentsMCPSessionBusy(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 500 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	handler.syncWait = 20 * time.Millisecond
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	first, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "catsco_hello",
		Arguments: map[string]any{"input": map[string]any{"step": 1}, "client_token": "ct_busy_1"},
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	handle := decodeServiceEnvelope(t, first.StructuredContent)
	if handle.Status != ServiceRunRunning {
		t.Fatalf("first call should stay running: %+v", handle)
	}

	second, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "catsco_hello",
		Arguments: map[string]any{"input": map[string]any{"step": 2}, "client_token": "ct_busy_2", "session_id": handle.SessionID},
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !second.IsError {
		t.Fatalf("second call should be a session_busy tool error: %+v", second.StructuredContent)
	}
	busyText := ""
	for _, content := range second.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			busyText += text.Text
		}
	}
	if !strings.Contains(busyText, "session_busy") {
		t.Fatalf("busy error text = %q, want session_busy", busyText)
	}
}

// MCP 建会话时钉定当时最新包 hash（设计 A §2 会话钉快照）——之后的新发布不影响本会话。
func TestServiceAgentsMCPSessionPinsPackageHash(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	hashA := strings.Repeat("a", 64)
	if _, err := st.PublishServiceAgentPackage(context.Background(), "hello", "Hello", json.RawMessage(`{"models":["m1"]}`), hashA, json.RawMessage(`{"schema":"x"}`), false); err != nil {
		t.Fatalf("publish: %v", err)
	}
	handler := NewServiceAgentsMCPHandler(st)
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{"input": map[string]any{"topic": "x"}}})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	env := decodeServiceEnvelope(t, call.StructuredContent)
	if env.SessionID == "" {
		t.Fatalf("session id missing: %+v", env)
	}
	st.mu.Lock()
	pinned := st.sessions[env.SessionID].AssetsHash
	st.mu.Unlock()
	if pinned != hashA {
		t.Fatalf("session assets_hash = %q, want pinned %q", pinned, hashA)
	}
}

// 回合制返问（awaiting_input；设计 §3.3-8）：同步等待路径交付返问信封（question 进文本摘要）；
// 补充 = 同一会话的下一次 tools/call（await 收尾不占会话串行位）；两轮各出一份回执。
func TestServiceAgentsMCPAwaitingInput(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoAwaitAfter = 5 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{
		"input": map[string]any{"topic": "写周报"},
	}})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if call.IsError {
		t.Fatalf("awaiting call must not be a tool error: %+v", call.Content)
	}
	env := decodeServiceEnvelope(t, call.StructuredContent)
	if env.Status != ServiceRunAwaitingInput || env.SessionID == "" || env.RunID == "" {
		t.Fatalf("awaiting envelope = %+v", env)
	}
	text := ""
	for _, content := range call.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "awaiting_input") || !strings.Contains(text, "请补充必要素材") {
		t.Fatalf("awaiting text = %q（须含状态与问题文本）", text)
	}

	// run_status 对 awaiting run 同样交付返问信封（断线补取通道）
	statusCall, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_run_status", Arguments: map[string]any{"run_id": env.RunID}})
	if err != nil {
		t.Fatalf("run_status call: %v", err)
	}
	statusEnv := decodeServiceEnvelope(t, statusCall.StructuredContent)
	if statusEnv.Status != ServiceRunAwaitingInput || statusEnv.RunID != env.RunID {
		t.Fatalf("run_status envelope = %+v", statusEnv)
	}

	// 补充输入 = 同会话下一次 tools/call → 第二轮 completed
	st.mu.Lock()
	st.autoAwaitAfter = 0
	st.autoCompleteAfter = 5 * time.Millisecond
	st.mu.Unlock()
	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{
		"input":      map[string]any{"text": "起止：1 月 1 日至 1 月 7 日"},
		"session_id": env.SessionID,
	}})
	if err != nil {
		t.Fatalf("continuation call: %v", err)
	}
	if second.IsError {
		t.Fatalf("continuation tool error: %+v", second.Content)
	}
	secondEnv := decodeServiceEnvelope(t, second.StructuredContent)
	if secondEnv.Status != ServiceRunCompleted || secondEnv.SessionID != env.SessionID || secondEnv.RunID == env.RunID {
		t.Fatalf("continuation envelope = %+v", secondEnv)
	}

	// 回执收件箱：awaiting_input + completed 各一份（回注通道数据面）
	pending, err := st.ListUndeliveredServiceRunReceipts(ctx, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("receipts = %+v err=%v, want 2", pending, err)
	}
	statuses := map[string]bool{}
	for _, receipt := range pending {
		statuses[receipt.Status] = true
	}
	if !statuses["awaiting_input"] || !statuses["completed"] {
		t.Fatalf("receipt statuses = %v", statuses)
	}
}

// ---- bot 身份（设计 P1b）：tools/call 可携带 bot_uid ----

// fakeBotIdentity 是 ServiceAgentBotIdentity 的内存假实现。
type fakeBotIdentity struct {
	owners map[int64]int64
	defs   map[int64]*types.BotDefinitionRecord
}

func (f *fakeBotIdentity) GetBotOwner(botUID int64) (int64, error) {
	owner, ok := f.owners[botUID]
	if !ok {
		return 0, errors.New("bot not found")
	}
	return owner, nil
}

func (f *fakeBotIdentity) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	if def, ok := f.defs[botUID]; ok {
		return def, nil
	}
	return nil, errors.New("bot definition not found")
}

func newFakeBotIdentity(botUID, owner int64, serviceAgents []string) *fakeBotIdentity {
	return &fakeBotIdentity{
		owners: map[int64]int64{botUID: owner},
		defs: map[int64]*types.BotDefinitionRecord{
			botUID: {Definition: types.BotDefinition{Schema: "xiaoba.bot-definition.v1", ServiceAgents: serviceAgents}, Exists: true},
		},
	}
}

func mcpToolErrorText(call *mcp.CallToolResult) string {
	if call == nil {
		return ""
	}
	var sb strings.Builder
	for _, content := range call.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String()
}

// 只有主库注入 bot 身份时才允许携带 bot_uid；归属、清单、未接线三种拒绝路径都要显式。
func TestServiceAgentsMCPBotIdentityScope(t *testing.T) {
	const caller = int64(441)
	cases := []struct {
		name         string
		botUID       int64
		identity     *fakeBotIdentity
		wireIdentity bool
		wantCode     string
		wantSessionB int64
	}{
		{name: "account level without bot_uid", botUID: 0, wantSessionB: 0},
		{name: "owned bot without list", botUID: 7001, identity: newFakeBotIdentity(7001, caller, nil), wireIdentity: true, wantSessionB: 7001},
		{name: "owned bot with slug in list", botUID: 7001, identity: newFakeBotIdentity(7001, caller, []string{"hello"}), wireIdentity: true, wantSessionB: 7001},
		{name: "owned bot with slug missing from list", botUID: 7001, identity: newFakeBotIdentity(7001, caller, []string{"codex"}), wireIdentity: true, wantCode: "agent_not_enabled_for_bot"},
		{name: "owned bot with empty list", botUID: 7001, identity: newFakeBotIdentity(7001, caller, []string{}), wireIdentity: true, wantCode: "agent_not_enabled_for_bot"},
		{name: "bot owned by someone else", botUID: 7001, identity: newFakeBotIdentity(7001, 999, nil), wireIdentity: true, wantCode: "bot_not_owned"},
		{name: "unknown bot", botUID: 7002, identity: newFakeBotIdentity(7001, caller, nil), wireIdentity: true, wantCode: "bot_not_found"},
		{name: "identity not wired", botUID: 7001, wireIdentity: false, wantCode: "bot_identity_unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeServiceAgentStore()
			st.autoCompleteAfter = 5 * time.Millisecond
			handler := NewServiceAgentsMCPHandler(st)
			if tc.wireIdentity {
				handler.SetBotIdentity(tc.identity)
			}
			ts := httptest.NewServer(mcpAuthStub(caller, handler))
			t.Cleanup(ts.Close)
			session := newMCPSession(t, ts.URL)

			args := map[string]any{"input": map[string]any{"topic": "x"}}
			if tc.botUID != 0 {
				args["bot_uid"] = tc.botUID
			}
			call, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "catsco_hello", Arguments: args})
			if err != nil {
				t.Fatalf("call tool: %v", err)
			}

			if tc.wantCode != "" {
				if !call.IsError {
					t.Fatalf("want tool error %q, got %+v", tc.wantCode, call.StructuredContent)
				}
				if text := mcpToolErrorText(call); !strings.Contains(text, tc.wantCode) {
					t.Fatalf("error text = %q, want %q", text, tc.wantCode)
				}
				if len(st.sessions) != 0 {
					t.Fatalf("rejected call must not create a session: %+v", st.sessions)
				}
				return
			}

			if call.IsError {
				t.Fatalf("unexpected tool error: %s", mcpToolErrorText(call))
			}
			env := decodeServiceEnvelope(t, call.StructuredContent)
			if env.SessionID == "" {
				t.Fatalf("session id missing: %+v", env)
			}
			created, ok := st.sessions[env.SessionID]
			if !ok {
				t.Fatalf("session %s not stored", env.SessionID)
			}
			if created.CallerBotUID != tc.wantSessionB {
				t.Fatalf("caller_bot_uid = %d, want %d", created.CallerBotUID, tc.wantSessionB)
			}
		})
	}
}

// 会话与 bot 身份绑定：跨 bot 续聊、账号级会话用 bot 身份续聊，都必须拒绝。
func TestServiceAgentsMCPBotSessionIsolation(t *testing.T) {
	const caller = int64(441)
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	identity := newFakeBotIdentity(7001, caller, nil)
	identity.owners[7002] = caller
	identity.defs[7002] = identity.defs[7001]
	handler.SetBotIdentity(identity)
	ts := httptest.NewServer(mcpAuthStub(caller, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	first, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{
		"input": map[string]any{"step": 1}, "bot_uid": 7001,
	}})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first.IsError {
		t.Fatalf("first call error: %s", mcpToolErrorText(first))
	}
	env := decodeServiceEnvelope(t, first.StructuredContent)

	// 换 bot 续聊
	other, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{
		"input": map[string]any{"step": 2}, "bot_uid": 7002, "session_id": env.SessionID,
	}})
	if err != nil {
		t.Fatalf("other bot call: %v", err)
	}
	if !other.IsError || !strings.Contains(mcpToolErrorText(other), "session_not_found_or_not_owned") {
		t.Fatalf("cross-bot continuation should be rejected: %+v / %s", other.StructuredContent, mcpToolErrorText(other))
	}

	// 去掉 bot_uid 续聊（账号级）
	account, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{
		"input": map[string]any{"step": 3}, "session_id": env.SessionID,
	}})
	if err != nil {
		t.Fatalf("account call: %v", err)
	}
	if !account.IsError || !strings.Contains(mcpToolErrorText(account), "session_not_found_or_not_owned") {
		t.Fatalf("account-level continuation of a bot session should be rejected: %s", mcpToolErrorText(account))
	}
}
