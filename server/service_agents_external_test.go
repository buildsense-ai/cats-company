package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- 外部执行后端（runner.type=http；设计 §4.4 P3-a）----

func TestParseServiceAgentRunnerSpec(t *testing.T) {
	cases := []struct {
		name       string
		definition string
		wantNil    bool
		wantType   string
		wantURL    string
	}{
		{name: "no runner", definition: `{"async_threshold_seconds":60}`, wantNil: true},
		{name: "invalid json", definition: `{`, wantNil: true},
		{name: "empty type", definition: `{"runner":{"url":"https://x"}}`, wantNil: true},
		{name: "http runner", definition: `{"runner":{"type":"HTTP","url":" https://user.example/run ","timeout_seconds":30}}`, wantType: "http", wantURL: "https://user.example/run"},
		{name: "codex runner", definition: `{"runner":{"type":"codex"}}`, wantType: "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := ParseServiceAgentRunnerSpec(json.RawMessage(tc.definition))
			if tc.wantNil {
				if spec != nil {
					t.Fatalf("spec = %+v, want nil", spec)
				}
				return
			}
			if spec == nil || spec.Type != tc.wantType || spec.URL != tc.wantURL {
				t.Fatalf("spec = %+v, want type=%s url=%s", spec, tc.wantType, tc.wantURL)
			}
			if spec.IsExternal() != (tc.wantType == "http") {
				t.Fatalf("IsExternal mismatch for %s", tc.wantType)
			}
		})
	}
}

// endpoint 准入：默认只允许 https + 公网；本地联调开关可放开。
func TestExternalRunnerValidateTarget(t *testing.T) {
	runner := NewServiceExternalRunnerFromEnv(nil)

	if _, err := runner.validateTarget("http://203.0.113.10/run"); err == nil {
		t.Fatal("plain http should be rejected by default")
	}
	if _, err := runner.validateTarget("https://localhost/run"); err == nil {
		t.Fatal("localhost should be rejected by default")
	}
	if _, err := runner.validateTarget("https://127.0.0.1/run"); err == nil {
		t.Fatal("loopback should be rejected by default")
	}
	if _, err := runner.validateTarget("ftp://example.com/run"); err == nil {
		t.Fatal("non-http scheme should be rejected")
	}

	runner.AllowInsecureTargets(true)
	for _, raw := range []string{"http://127.0.0.1:8080/run", "https://localhost/run"} {
		if _, err := runner.validateTarget(raw); err != nil {
			t.Fatalf("allowInsecure should accept %s: %v", raw, err)
		}
	}
}

func newExternalTestRun(t *testing.T, st *fakeServiceAgentStore) (string, string) {
	t.Helper()
	ctx := context.Background()
	sessionID, err := st.CreateServiceSession(ctx, 1, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := st.CreateServiceRunIdempotent(ctx, sessionID, "ct_ext_1", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return sessionID, run.RunID
}

func externalRunnerForTest(t *testing.T, st *fakeServiceAgentStore, secret string) *ServiceExternalRunner {
	t.Helper()
	runner := NewServiceExternalRunnerFromEnv(st)
	runner.SetSecret(secret)
	runner.AllowInsecureTargets(true)
	runner.client = &http.Client{Timeout: 5 * time.Second}
	return runner
}

// 回归：转发条件曾经写成 `if created`，而 CreateServiceRunIdempotent 的布尔值
// 语义是 reused（true=幂等命中、false=本次新建），于是新建 run 永远不会被转发到
// 外部执行器，调用方只能拿到一个永远 submitted 的句柄。
func TestInvokeDispatchesExternalRunnerOnlyForFreshRun(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"status":"completed","output":{"text":"ok"},"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	t.Cleanup(ts.Close)

	st := newFakeServiceAgentStore()
	st.definitions[1] = json.RawMessage(`{"async_threshold_seconds":1,"runner":{"type":"http","url":"` + ts.URL + `","timeout_seconds":5}}`)

	handler := NewServiceAgentsMCPHandler(st)
	handler.SetExternalRunner(externalRunnerForTest(t, st, "test-secret"))
	handler.syncWait = 20 * time.Millisecond // 不等终态，直接拿句柄

	agent := st.agents[0]
	args := serviceInvokeToolArgs{Input: map[string]any{"text": "hi"}, ClientToken: "ct-dispatch-1"}
	if _, _, err := handler.invoke(context.Background(), agent, 441, args); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	waitHits := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if atomic.LoadInt32(&hits) == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("dispatch hits = %d, want %d", atomic.LoadInt32(&hits), want)
	}
	waitHits(1)

	// 同会话同 client_token 重发 = 幂等命中既有 run，不能再转一次（对端只应收到一次）。
	st.mu.Lock()
	var sessionID string
	for id := range st.sessions {
		sessionID = id
	}
	st.mu.Unlock()
	if sessionID == "" {
		t.Fatal("expected one session after fresh invoke")
	}
	args.SessionID = sessionID
	if _, _, err := handler.invoke(context.Background(), agent, 441, args); err != nil {
		t.Fatalf("invoke reuse: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("idempotent reuse dispatched again: hits=%d", got)
	}
}

// 成功路径：签名正确、请求体契约、结果写成 result 事件并完成 run。
func TestExternalRunnerCompleted(t *testing.T) {
	st := newFakeServiceAgentStore()
	sessionID, runID := newExternalTestRun(t, st)
	const secret = "test-secret"

	var gotBody []byte
	var gotSig, gotRunID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Catsco-Signature")
		gotRunID = r.Header.Get("X-Catsco-Run-Id")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"status":"completed","output":{"text":"外部 agent 回答"},"files":["https://f/1"],"usage":{"total_tokens":12}}`))
	}))
	t.Cleanup(ts.Close)

	runner := externalRunnerForTest(t, st, secret)
	runner.execute(ServiceExternalRunRequest{
		RunID:     runID,
		SessionID: sessionID,
		AgentSlug: "my-helper",
		CallerUID: 441,
		Input:     json.RawMessage(`{"text":"hi"}`),
		Spec:      &ServiceAgentRunnerSpec{Type: "http", URL: ts.URL, TimeoutSeconds: 5},
	})

	// 签名：hmac-sha256(secret, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Fatalf("signature = %q, want %q", gotSig, want)
	}
	if gotRunID != runID {
		t.Fatalf("run header = %q, want %q", gotRunID, runID)
	}
	var req map[string]any
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if req["contract_version"] != serviceRunRequestContractVersion || req["run_id"] != runID ||
		req["agent"] != "my-helper" || req["caller_uid"].(float64) != 441 {
		t.Fatalf("request body = %v", req)
	}

	run := st.runs[runID]
	if run.State != "completed" {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	var envelope ServiceRunEnvelope
	if err := json.Unmarshal(run.Result, &envelope); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if envelope.Status != ServiceRunCompleted || !strings.Contains(string(envelope.Output), "外部 agent 回答") {
		t.Fatalf("result envelope = %+v (%s)", envelope, run.Result)
	}
	events := st.events[runID]
	if len(events) != 1 || events[0].Type != "result" {
		t.Fatalf("events = %+v, want one result event", events)
	}
}

// 返问路径：awaiting_input 收尾，不写 completed。
func TestExternalRunnerAwaitingInput(t *testing.T) {
	st := newFakeServiceAgentStore()
	sessionID, runID := newExternalTestRun(t, st)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"awaiting_input","output":{"question":"要按哪个模板？"}}`))
	}))
	t.Cleanup(ts.Close)

	runner := externalRunnerForTest(t, st, "s")
	runner.execute(ServiceExternalRunRequest{
		RunID: runID, SessionID: sessionID, AgentSlug: "my-helper",
		CallerUID: 441, Input: json.RawMessage(`{}`),
		Spec: &ServiceAgentRunnerSpec{Type: "http", URL: ts.URL},
	})

	if st.runs[runID].State != "awaiting_input" {
		t.Fatalf("run state = %s, want awaiting_input", st.runs[runID].State)
	}
}

// 失败路径：5xx → run 置 failed（可重试）。
func TestExternalRunnerFailure(t *testing.T) {
	st := newFakeServiceAgentStore()
	sessionID, runID := newExternalTestRun(t, st)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	runner := externalRunnerForTest(t, st, "s")
	runner.execute(ServiceExternalRunRequest{
		RunID: runID, SessionID: sessionID, AgentSlug: "my-helper",
		CallerUID: 441, Input: json.RawMessage(`{}`),
		Spec: &ServiceAgentRunnerSpec{Type: "http", URL: ts.URL},
	})

	run := st.runs[runID]
	if run.State != "failed" {
		t.Fatalf("run state = %s, want failed", run.State)
	}
}

// 「running」应答保持不动；未知状态直接判失败。
func TestExternalRunnerStatusHandling(t *testing.T) {
	st := newFakeServiceAgentStore()
	sessionID, runID := newExternalTestRun(t, st)

	t.Run("running keeps state", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"running"}`))
		}))
		defer ts.Close()
		runner := externalRunnerForTest(t, st, "s")
		runner.execute(ServiceExternalRunRequest{
			RunID: runID, SessionID: sessionID, AgentSlug: "my-helper",
			CallerUID: 441, Input: json.RawMessage(`{}`),
			Spec: &ServiceAgentRunnerSpec{Type: "http", URL: ts.URL},
		})
		if st.runs[runID].State != "running" {
			t.Fatalf("state = %s, want running", st.runs[runID].State)
		}
	})

	t.Run("unknown status fails", func(t *testing.T) {
		st2 := newFakeServiceAgentStore()
		session2, run2 := newExternalTestRun(t, st2)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"weird"}`))
		}))
		defer ts.Close()
		runner := externalRunnerForTest(t, st2, "s")
		runner.execute(ServiceExternalRunRequest{
			RunID: run2, SessionID: session2, AgentSlug: "my-helper",
			CallerUID: 441, Input: json.RawMessage(`{}`),
			Spec: &ServiceAgentRunnerSpec{Type: "http", URL: ts.URL},
		})
		if st2.runs[run2].State != "failed" {
			t.Fatalf("state = %s, want failed", st2.runs[run2].State)
		}
	})
}

// codex 型 agent 不会被平台转发（SpecFor 返回 nil → MaybeDispatch 直接跳过）。
func TestExternalRunnerSkipsPlatformRunner(t *testing.T) {
	st := newFakeServiceAgentStore()
	runner := externalRunnerForTest(t, st, "s")
	agent := st.agents[0] // hello：fake 定义里没有 runner
	if spec := runner.SpecFor(context.Background(), agent); spec.IsExternal() {
		t.Fatalf("platform agent must not be dispatched externally: %+v", spec)
	}
}
