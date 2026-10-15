package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/store"
)

// fakeServiceAgentStore 是 service 编排存储的内存假实现（MCP 面测试专用）。
type fakeServiceAgentStore struct {
	mu                sync.Mutex
	agents            []store.ServiceAgentDescriptor
	sessions          map[string]store.ServiceSession
	runs              map[string]store.ServiceRun
	events            map[string][]store.ServiceRunEventRecord
	tokens            map[string]string // session|token -> runID
	packages          map[string]json.RawMessage
	packageHashes     map[int64]string // agentID -> 最新版本内容 hash
	autoCompleteAfter time.Duration
	nextID            int64
}

func newFakeServiceAgentStore() *fakeServiceAgentStore {
	return &fakeServiceAgentStore{
		agents: []store.ServiceAgentDescriptor{
			{ID: 1, Slug: "hello", Name: "Hello 试点应用", Version: 1, AsyncThresholdSeconds: 60},
		},
		sessions:      map[string]store.ServiceSession{},
		runs:          map[string]store.ServiceRun{},
		events:        map[string][]store.ServiceRunEventRecord{},
		tokens:        map[string]string{},
		packages:      map[string]json.RawMessage{},
		packageHashes: map[int64]string{},
	}
}

// ---- 云端包分发（store.ServiceAgentStore 扩展方法）----

func (f *fakeServiceAgentStore) PublishServiceAgentPackage(_ context.Context, slug string, _ json.RawMessage, packageHash string, bundle json.RawMessage) (int64, error) {
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
		return 0, store.ErrServiceAgentNotFound
	}
	if _, ok := f.packages[packageHash]; !ok {
		f.packages[packageHash] = append(json.RawMessage(nil), bundle...)
		f.nextID++
	}
	f.packageHashes[agentID] = packageHash
	return f.nextID, nil
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

func (f *fakeServiceAgentStore) GetServiceAgentPackageHash(_ context.Context, agentID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.packageHashes[agentID], nil
}

func (f *fakeServiceAgentStore) GetServiceAgentBySlug(_ context.Context, slug string) (int64, string, int64, int, error) {
	for _, a := range f.agents {
		if a.Slug == slug {
			return a.ID, a.Name, a.Version, a.AsyncThresholdSeconds, nil
		}
	}
	return 0, "", 0, 0, store.ErrServiceAgentNotFound
}

func (f *fakeServiceAgentStore) ListServiceAgents(context.Context) ([]store.ServiceAgentDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.ServiceAgentDescriptor(nil), f.agents...), nil
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
	f.events[runID] = append(f.events[runID], store.ServiceRunEventRecord{
		RunID: runID, Seq: int64(len(f.events[runID]) + 1), Type: "result", Payload: payload, CreatedAt: time.Now(),
	})
}

func (f *fakeServiceAgentStore) ClaimServiceRuns(context.Context, string, int) ([]store.ServiceRun, error) {
	return nil, nil
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
	return true, nil
}

func (f *fakeServiceAgentStore) FailServiceRun(context.Context, string, string, bool) error {
	return nil
}

func (f *fakeServiceAgentStore) ReapExpiredServiceRuns(context.Context) (int, error) {
	return 0, nil
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
