package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// —— 包体必须是 JSON ——
//
// 包体会原样写进 service_agent_versions.package_json（jsonb）。不校验就由数据库报错，
// 调用方拿到 500 publish_unavailable，看不出是自己传错了（线上真的撞到过）。
func TestServiceAgentPublishRejectsNonJSONBundle(t *testing.T) {
	st := newFakeServiceAgentStore()
	ts := newPublishTestServer(t, st, 441, true)

	status, body := postPublish(t, ts.URL, map[string]any{
		"slug":          "bad-bundle",
		"visibility":    "private",
		"bundle_base64": base64.StdEncoding.EncodeToString([]byte("pkg")),
		"definition": map[string]any{
			"description": "x",
			"runner":      map[string]any{"type": "worker", "command": "node x.js"},
		},
	})
	if status != http.StatusBadRequest || body["error"] != "invalid_bundle" {
		t.Fatalf("status=%d body=%v, want 400 invalid_bundle", status, body)
	}
}

// —— query token 只给 WebSocket 升级 ——
func TestExtractTokenRejectsQueryTokenOutsideWebSocket(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/api/friends?token=leaky", nil)
	if got := extractToken(plain); got != "" {
		t.Fatalf("HTTP 接口不该接受 query token，got %q", got)
	}

	ws := httptest.NewRequest(http.MethodGet, "/v0/channels?token=ws-token", nil)
	ws.Header.Set("Upgrade", "websocket")
	ws.Header.Set("Connection", "Upgrade")
	if got := extractToken(ws); got != "ws-token" {
		t.Fatalf("WebSocket 升级必须接受 query token（浏览器带不了头），got %q", got)
	}

	// 别的路径挂了 Upgrade 头也不算数：只认 /v0/channels。
	other := httptest.NewRequest(http.MethodGet, "/api/friends?token=leaky", nil)
	other.Header.Set("Upgrade", "websocket")
	if got := extractToken(other); got != "" {
		t.Fatalf("非 WS 路径不该接受 query token，got %q", got)
	}

	hdr := httptest.NewRequest(http.MethodGet, "/api/friends?token=leaky", nil)
	hdr.Header.Set("Authorization", "Bearer header-token")
	if got := extractToken(hdr); got != "header-token" {
		t.Fatalf("Authorization 头优先，got %q", got)
	}
}

// —— 好友关系变化要推 list_changed ——
type countingCatalogNotifier struct{ calls int }

func (c *countingCatalogNotifier) NotifyCatalogChanged() { c.calls++ }

func TestFriendHandlerCatalogNotifierIsOptional(t *testing.T) {
	handler := NewFriendHandler(nil)
	handler.notifyServiceCatalogChanged() // 未注入时不能 panic

	notifier := &countingCatalogNotifier{}
	handler.SetCatalogNotifier(notifier)
	handler.notifyServiceCatalogChanged()
	if notifier.calls != 1 {
		t.Fatalf("calls = %d, want 1", notifier.calls)
	}
}

// 拉黑 = 解除关系：「好友可见」的应用要立刻从对方的工具清单里消失。
func TestFriendBlockNotifiesServiceCatalog(t *testing.T) {
	const (
		actorUID int64 = 9
		agentUID int64 = 43
	)
	db := &realtimeFriendEventStore{
		users:  map[int64]*types.User{agentUID: {ID: agentUID, AccountType: types.AccountBot}},
		owners: map[int64]int64{agentUID: 7},
	}
	handler := NewFriendHandler(db)
	notifier := &countingCatalogNotifier{}
	handler.SetCatalogNotifier(notifier)

	req := httptest.NewRequest(http.MethodPost, "/api/friends/block", bytes.NewBufferString(`{"user_id":43}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, actorUID))
	rec := httptest.NewRecorder()

	handler.HandleBlock(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if notifier.calls != 1 {
		t.Fatalf("catalog notify calls = %d, want 1（不推的话对方的工具清单要重连才更新）", notifier.calls)
	}
}
