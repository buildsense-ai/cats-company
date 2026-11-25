package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mcpAuthTestServer 起一个带固定 uid 身份的 httptest server。
func mcpAuthTestServer(t *testing.T, uid int64, handler http.Handler) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), uidKey, uid)))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// postJSONRPC 发一条 JSON-RPC 并解析应答。
func postJSONRPC(t *testing.T, url string, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	status, reply, _ := postJSONRPCSession(t, url, "", body)
	return status, reply
}

// postJSONRPCSession 同 postJSONRPC，可携带会话 id，并返回响应头（取 Mcp-Session-Id）。
func postJSONRPCSession(t *testing.T, url, sessionID, body string) (int, map[string]json.RawMessage, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Streamable HTTP 要求 Accept 同时覆盖两种响应形态（JSON / SSE）。
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	// SSE 帧（event: message / data: {...}）与裸 JSON 都可能出现 —— 两种都解。
	payload := raw
	if !json.Valid(raw) {
		for _, line := range strings.Split(string(raw), "\n") {
			if data := strings.TrimPrefix(line, "data: "); data != line && json.Valid([]byte(data)) {
				payload = []byte(data)
				break
			}
		}
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(payload, &reply); err != nil {
		// 通知类方法（notifications/*）应答是 202 + 空体，合法。
		if len(bytes.TrimSpace(raw)) == 0 && resp.StatusCode == http.StatusAccepted {
			return resp.StatusCode, nil, resp.Header
		}
		t.Fatalf("decode reply: %v raw=%.200s", err, raw)
	}
	return resp.StatusCode, reply, resp.Header
}

// initMCPSession 完成 initialize + initialized 握手，返回会话 id。
func initMCPSession(t *testing.T, url string) (string, map[string]json.RawMessage) {
	t.Helper()
	_, reply, header := postJSONRPCSession(t, url, "", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	sessionID := header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatalf("no session id in initialize reply: %v", header)
	}
	postJSONRPCSession(t, url, sessionID, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	return sessionID, reply
}

func mcpTasksTestSetup(t *testing.T) (*fakeServiceAgentStore, string) {
	t.Helper()
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	layer := NewMCPTasksLayer(handler)
	ts := mcpAuthTestServer(t, 441, layer)
	return st, ts
}

func TestMCPTasksCreateGetListCancel(t *testing.T) {
	st, url := mcpTasksTestSetup(t)
	st.autoCompleteAfter = 300 * time.Millisecond // 留窗口观察 working 态

	// task 化的 tools/call：立即返回 Task 对象。
	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_hello",
		"arguments":{"input":{"topic":"x"},"client_token":"ct-task-1"},
		"_meta":{"io.modelcontextprotocol/task":{}}}}`)
	var created struct {
		Task struct {
			TaskID       string `json:"taskId"`
			Status       string `json:"status"`
			CreatedAt    string `json:"createdAt"`
			PollInterval int64  `json:"pollInterval"`
			TTL          int64  `json:"ttl"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply["result"], &created); err != nil {
		t.Fatalf("create task result: %v raw=%s", err, reply["result"])
	}
	task := created.Task
	if task.TaskID == "" || task.Status != "working" {
		t.Fatalf("task = %+v", task)
	}
	if task.TTL <= 0 || task.CreatedAt == "" {
		t.Fatalf("task shape incomplete: %+v", task)
	}

	// tasks/get：还在跑 → working。
	_, get := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":%q}}`, task.TaskID))
	var got struct {
		TaskID string `json:"taskId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(get["result"], &got); err != nil {
		t.Fatalf("tasks/get: %v", err)
	}
	if got.TaskID != task.TaskID || got.Status != "working" {
		t.Fatalf("working task = %+v", got)
	}

	// tasks/list：能看到这条任务。
	_, list := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":3,"method":"tasks/list","params":{}}`)
	var listed struct {
		Tasks []struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(list["result"], &listed); err != nil {
		t.Fatalf("tasks/list: %v", err)
	}
	found := false
	for _, entry := range listed.Tasks {
		if entry.TaskID == task.TaskID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task missing from list: %+v", listed)
	}

	// 等执行完成，再查 → completed。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		_, get2 := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"taskId":%q}}`, task.TaskID))
		var state struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(get2["result"], &state) == nil && state.Status == "completed" {
			break
		}
	}
	_, get3 := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":5,"method":"tasks/get","params":{"taskId":%q}}`, task.TaskID))
	var final struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(get3["result"], &final); err != nil || final.Status != "completed" {
		t.Fatalf("final status = %+v err=%v raw=%s", final, err, get3["result"])
	}

	// 底层 service run 确实跑过（复用 invoke 管线的证据）。
	st.mu.Lock()
	runCount := len(st.runs)
	st.mu.Unlock()
	if runCount == 0 {
		t.Fatal("no service run created for task")
	}

	// tasks/cancel：终态不可再撤销，但应答仍是 Task 对象（幂等）。
	_, cancel := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":6,"method":"tasks/cancel","params":{"taskId":%q}}`, task.TaskID))
	var cancelled struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(cancel["result"], &cancelled); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "completed" {
		t.Fatalf("terminal task cancel must be idempotent no-op, got %+v", cancelled)
	}
}

func TestMCPTasksCancelWorking(t *testing.T) {
	st, url := mcpTasksTestSetup(t)
	st.autoCompleteAfter = 3 * time.Second // 慢任务：来得及撤销

	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_hello",
		"arguments":{"input":{"topic":"x"},"client_token":"ct-cancel-1"},
		"_meta":{"io.modelcontextprotocol/task":{}}}}`)
	var created struct {
		Task struct {
			TaskID string `json:"taskId"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply["result"], &created); err != nil {
		t.Fatalf("create: %v", err)
	}
	task := created.Task
	_, cancel := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/cancel","params":{"taskId":%q}}`, task.TaskID))
	var cancelled struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(cancel["result"], &cancelled); err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel = %+v err=%v raw=%s", cancelled, err, cancel["result"])
	}
}

func TestMCPTasksCrossOwnerIsolation(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	layer := NewMCPTasksLayer(handler)
	ownerURL := mcpAuthTestServer(t, 441, layer)
	strangerURL := mcpAuthTestServer(t, 999, layer)

	_, reply := postJSONRPC(t, ownerURL, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_hello",
		"arguments":{"input":{"topic":"x"},"client_token":"ct-iso-1"},
		"_meta":{"io.modelcontextprotocol/task":{}}}}`)
	var created2 struct {
		Task struct {
			TaskID string `json:"taskId"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply["result"], &created2); err != nil {
		t.Fatalf("create: %v", err)
	}
	task := created2.Task
	// 别的账号查/撤销同一个 taskId 必须是 not found。
	_, get := postJSONRPC(t, strangerURL, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":%q}}`, task.TaskID))
	if get["error"] == nil {
		t.Fatalf("cross-owner tasks/get must error, got %s", get["result"])
	}
	_, cancel := postJSONRPC(t, strangerURL, fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tasks/cancel","params":{"taskId":%q}}`, task.TaskID))
	if cancel["error"] == nil {
		t.Fatal("cross-owner tasks/cancel must error")
	}
	// 别的账号的 tasks/list 里也没有它。
	_, list := postJSONRPC(t, strangerURL, `{"jsonrpc":"2.0","id":4,"method":"tasks/list","params":{}}`)
	var listed struct {
		Tasks []struct {
			TaskID string `json:"taskId"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(list["result"], &listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, entry := range listed.Tasks {
		if entry.TaskID == task.TaskID {
			t.Fatal("cross-owner task visible in tasks/list")
		}
	}
}

func TestMCPTasksUnknownTool(t *testing.T) {
	_, url := mcpTasksTestSetup(t)
	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_nonexistent",
		"arguments":{"input":{}},
		"_meta":{"io.modelcontextprotocol/task":{}}}}`)
	// 受理本身成功（Task 对象）；失败要在 tasks/get 的终态里看到。
	var created3 struct {
		Task struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply["result"], &created3); err != nil {
		t.Fatalf("create: %v raw=%s", err, reply["result"])
	}
	task := created3.Task
	deadline := time.Now().Add(3 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
		_, get := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":%q}}`, task.TaskID))
		var state struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(get["result"], &state) == nil && state.Status != "working" {
			status = state.Status
			break
		}
	}
	if status != "failed" {
		t.Fatalf("unknown tool task must fail, got %q", status)
	}
}

func TestMCPTasksNonTaskCallPassthrough(t *testing.T) {
	_, url := mcpTasksTestSetup(t)
	sessionID, _ := initMCPSession(t, url)
	// 不带 _meta task 的 tools/call 原样走 SDK（同步应答，不是 Task 对象）。
	_, reply, _ := postJSONRPCSession(t, url, sessionID, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_hello",
		"arguments":{"input":{"topic":"x"},"client_token":"ct-pass-1"}}}`)
	var result struct {
		StructuredContent struct {
			Status string `json:"status"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(reply["result"], &result); err != nil {
		t.Fatalf("passthrough result: %v raw=%s", err, reply["result"])
	}
	if result.StructuredContent.Status != "completed" {
		t.Fatalf("sync call must complete, got %+v", result)
	}
}

func TestMCPInitializeAdvertisesTasks(t *testing.T) {
	st := newFakeServiceAgentStore()
	handler := NewServiceAgentsMCPHandler(st)
	layer := NewMCPTasksLayer(handler)
	url := mcpAuthTestServer(t, 441, layer)
	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
		"protocolVersion":"2026-07-28",
		"capabilities":{},
		"clientInfo":{"name":"test","version":"0"}}}`)
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tasks map[string]json.RawMessage `json:"tasks"`
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(reply["result"], &result); err != nil {
		t.Fatalf("initialize reply: %v raw=%s", err, reply["result"])
	}
	// 2026-07-28 客户端走 initialize 时，SDK 按 SEP-2575 把协商封在 initialize 系
	// 最新版本（2025-11-25）——initialize 在 2026-07-28 里已废弃，版本随每请求
	// _meta 走。这里验证的是「旧握手不回错、能力面照常」。
	if result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("protocol version = %q", result.ProtocolVersion)
	}
	if result.Capabilities.Tasks == nil {
		t.Fatalf("tasks capability missing: %s", reply["result"])
	}
	if result.Capabilities.Tools == nil {
		t.Fatal("tools capability must survive injection")
	}
}

func TestMCPInitializeCompat20251125(t *testing.T) {
	st := newFakeServiceAgentStore()
	handler := NewServiceAgentsMCPHandler(st)
	layer := NewMCPTasksLayer(handler)
	url := mcpAuthTestServer(t, 441, layer)
	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
		"protocolVersion":"2025-11-25",
		"capabilities":{},
		"clientInfo":{"name":"test","version":"0"}}}`)
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(reply["result"], &result); err != nil {
		t.Fatalf("initialize reply: %v", err)
	}
	// 兼容 2025-11-25：旧客户端按它声明的版本应答，不被强行升级。
	if result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("protocol version = %q, want 2025-11-25", result.ProtocolVersion)
	}
}

func TestMCPTasksResultEndpoint(t *testing.T) {
	st, url := mcpTasksTestSetup(t)
	st.autoCompleteAfter = 5 * time.Millisecond

	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_hello",
		"arguments":{"input":{"topic":"x"},"client_token":"ct-result-1"},
		"task":{}}}`)
	var created struct {
		Task struct {
			TaskID string `json:"taskId"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply["result"], &created); err != nil {
		t.Fatalf("create: %v raw=%s", err, reply["result"])
	}
	taskID := created.Task.TaskID

	// 未完成时 tasks/result 必须报错（不是空数据冒充结果）。
	_, early := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/result","params":{"taskId":%q}}`, taskID))
	if early["error"] == nil {
		t.Fatalf("working task result must error, got %s", early["result"])
	}

	// 完成后：tasks/result 返回原 tools/call 的 CallToolResult 结构。
	deadline := time.Now().Add(5 * time.Second)
	var final map[string]json.RawMessage
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		_, got := postJSONRPC(t, url, fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tasks/result","params":{"taskId":%q}}`, taskID))
		if got["result"] != nil {
			final = got
			break
		}
	}
	if final == nil {
		t.Fatal("task never completed")
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent struct {
			Status         string `json:"status"`
			ContractVersion string `json:"contract_version"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(final["result"], &result); err != nil {
		t.Fatalf("tasks/result shape: %v raw=%s", err, final["result"])
	}
	if len(result.Content) == 0 || result.StructuredContent.Status != "completed" {
		t.Fatalf("tasks/result = %+v", result)
	}
	if result.StructuredContent.ContractVersion != "catsco.service-run-result.v1" {
		t.Fatalf("contract = %q", result.StructuredContent.ContractVersion)
	}
}

func TestMCPTasksMetaFallbackCompat(t *testing.T) {
	// 早期草案把 task 标记放在 _meta["io.modelcontextprotocol/task"]：兼容识别。
	st, url := mcpTasksTestSetup(t)
	st.autoCompleteAfter = 5 * time.Millisecond
	_, reply := postJSONRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"catsco_hello",
		"arguments":{"input":{"topic":"x"},"client_token":"ct-metacomp-1"},
		"_meta":{"io.modelcontextprotocol/task":{}}}}`)
	var created struct {
		Task struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply["result"], &created); err != nil {
		t.Fatalf("create: %v raw=%s", err, reply["result"])
	}
	if created.Task.TaskID == "" || created.Task.Status != "working" {
		t.Fatalf("meta-augmented call must create task: %+v", created)
	}
}
