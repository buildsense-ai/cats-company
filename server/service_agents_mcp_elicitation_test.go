package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newElicitationSession 起一个带 elicitation 客户端能力的会话。
// handler 返回固定的 accept 应答；可传 nil 关闭能力（测 out-of-band 路径）。
func newElicitationSession(t *testing.T, url string, answer map[string]any, protocolVersion string) *mcp.ClientSession {
	t.Helper()
	var options *mcp.ClientOptions
	if answer != nil {
		options = &mcp.ClientOptions{
			ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return &mcp.ElicitResult{Action: "accept", Content: answer}, nil
			},
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "elicit-test", Version: "0.1.0"}, options)
	var sessionOptions *mcp.ClientSessionOptions
	if protocolVersion != "" {
		sessionOptions = &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion}
	}
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: nil,
	}, sessionOptions)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func elicitationTestURL(t *testing.T, uid int64) string {
	t.Helper()
	st := newFakeServiceAgentStore()
	handler := NewServiceAgentsMCPHandler(st)
	ts := httptest.NewServer(mcpAuthStub(uid, handler))
	t.Cleanup(ts.Close)
	return ts.URL
}

func decodeElicitationEnvelope(t *testing.T, structured any) mcpElicitationEnvelope {
	t.Helper()
	raw, err := json.Marshal(structured)
	if err != nil {
		t.Fatalf("marshal structured: %v", err)
	}
	var env mcpElicitationEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope %s: %v", raw, err)
	}
	return env
}

// 双协议参数化：2026-07-28（input_required/mrtr）与 2025-11-25（server-initiated）走同一条 handler 路径。
func TestMCPElicitationBothProtocols(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25", ""} {
		t.Run("protocol_"+version, func(t *testing.T) {
			url := elicitationTestURL(t, 441)
			session := newElicitationSession(t, url, map[string]any{"name": "小八"}, version)
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: mcpElicitationToolName,
				Arguments: map[string]any{
					"message": "请确认收件人姓名",
					"schema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
					},
				},
			})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if result.IsError {
				t.Fatalf("tool error: %+v", result.Content)
			}
			env := decodeElicitationEnvelope(t, result.StructuredContent)
			if env.ContractVersion != mcpElicitationContractVersion {
				t.Fatalf("contract = %q", env.ContractVersion)
			}
			if env.Status != "answered" || env.Action != "accept" {
				t.Fatalf("env = %+v", env)
			}
			if env.ElicitationID == "" {
				t.Fatal("elicitation id missing")
			}
			var content map[string]any
			if err := json.Unmarshal(env.Content, &content); err != nil {
				t.Fatalf("content: %v", err)
			}
			if content["name"] != "小八" {
				t.Fatalf("content = %+v", content)
			}
		})
	}
}

func TestMCPElicitationOutOfBand(t *testing.T) {
	// 客户端不支持 elicitation（无 handler）：InputRequests 无人履行。
	// out-of-band 路径：调用方自己收答案，带 elicitation_id + response 回传。
	st := newFakeServiceAgentStore()
	handler := NewServiceAgentsMCPHandler(st)
	// 测试断言需要同一份注册表，所以不用 elicitationTestURL 的独立装配。
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	url := ts.URL
	registry := handler.elicitationRegistry()

	// 手工种一份进行中的 elicitation（模拟首回合已登记）。
	rec := &mcpElicitationRecord{
		ID: "elc_testoob", OwnerUID: 441, Message: "确认?",
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	rec.CreatedAt = registry.now()
	rec.ExpiresAt = registry.now().Add(mcpElicitationTTL)
	if !registry.put(rec) {
		t.Fatal("registry put failed")
	}

	session := newElicitationSession(t, url, nil, "")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: mcpElicitationToolName,
		Arguments: map[string]any{
			"message":        "确认?",
			"elicitation_id": "elc_testoob",
			"response": map[string]any{
				"action":  "accept",
				"content": map[string]any{"confirmed": true},
			},
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool error: %+v", result.Content)
	}
	env := decodeElicitationEnvelope(t, result.StructuredContent)
	if env.Status != "answered" || env.ElicitationID != "elc_testoob" {
		t.Fatalf("env = %+v", env)
	}
	// consume 后第二份回包必须报错（elicitation 不交付两份答案）。
	result2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: mcpElicitationToolName,
		Arguments: map[string]any{
			"message":        "确认?",
			"elicitation_id": "elc_testoob",
			"response":       map[string]any{"action": "accept"},
		},
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !result2.IsError {
		t.Fatal("consumed elicitation must error")
	}
	_ = st
}

func TestMCPElicitationValidation(t *testing.T) {
	url := elicitationTestURL(t, 441)
	session := newElicitationSession(t, url, map[string]any{}, "")

	// 空 message 拒绝。
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      mcpElicitationToolName,
		Arguments: map[string]any{"message": ""},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("empty message must be a tool error")
	}

	// url 模式缺 url 拒绝。
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      mcpElicitationToolName,
		Arguments: map[string]any{"message": "x", "mode": "url"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("url mode without url must be a tool error")
	}
}

func TestMCPElicitationInToolList(t *testing.T) {
	url := elicitationTestURL(t, 441)
	session := newElicitationSession(t, url, nil, "")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == mcpElicitationToolName {
			found = true
			if tool.Description == "" || tool.InputSchema == nil {
				t.Fatalf("elicitation tool shape: %+v", tool)
			}
		}
	}
	if !found {
		t.Fatal("mcp_elicitation_create missing from tools/list")
	}
}

func TestMCPElicitationRegistryExpiryAndOwnership(t *testing.T) {
	registry := newMCPElicitationRegistry()
	now := time.Now()
	registry.now = func() time.Time { return now }
	rec := &mcpElicitationRecord{
		ID: "elc_exp", OwnerUID: 1, Message: "m",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if !registry.put(rec) {
		t.Fatal("put failed")
	}
	if registry.get("elc_exp", 1) == nil {
		t.Fatal("record missing")
	}
	// 跨账号不可见。
	if registry.get("elc_exp", 2) != nil {
		t.Fatal("cross-owner visibility")
	}
	// 过期即清。
	now = now.Add(2 * time.Minute)
	if registry.get("elc_exp", 1) != nil {
		t.Fatal("expired record must be invisible")
	}
	// consume 即销毁。
	rec2 := &mcpElicitationRecord{
		ID: "elc_exp2", OwnerUID: 1, Message: "m",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	registry.put(rec2)
	registry.consume("elc_exp2", 1)
	if registry.get("elc_exp2", 1) != nil {
		t.Fatal("consumed record must be gone")
	}
}
