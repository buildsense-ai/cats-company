package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// worker 券（账号登录态换的短期券）：用户不用手抄 cwk_，但权限不高于 cwk_。

func TestMintServiceWorkerSessionRoundTrip(t *testing.T) {
	SetJWTSecret("worker-session-test-secret")
	t.Cleanup(func() { SetJWTSecret("") })

	token, expires, err := MintServiceWorkerSession(442, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if token == "" || !expires.After(time.Now()) {
		t.Fatalf("minted token:%q expires:%v", token, expires)
	}
	uid, err := ParseServiceWorkerSession(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if uid != 442 {
		t.Fatalf("uid = %d, want 442", uid)
	}
}

// 账号 JWT 不能直接当 worker 券用（否则 worker 泄漏就等于整个账号泄漏）。
func TestServiceWorkerSessionRejectsUserToken(t *testing.T) {
	SetJWTSecret("worker-session-reject-secret")
	t.Cleanup(func() { SetJWTSecret("") })

	userToken, err := GenerateToken(442, "someone", "")
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}
	if _, err := ParseServiceWorkerSession(userToken); err == nil {
		t.Fatal("user token must not be accepted as a worker ticket")
	}
	// 反过来：worker 券也不能当账号 JWT 用（普通接口的最小权限）。
	workerToken, _, err := MintServiceWorkerSession(442, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := ParseToken(workerToken); err == nil {
		t.Fatal("worker ticket must not be accepted as a user token")
	}
}

// 过期与非法 uid 都要拒。
func TestMintServiceWorkerSessionValidation(t *testing.T) {
	if _, _, err := MintServiceWorkerSession(0, time.Hour); err == nil {
		t.Fatal("uid 0 must be rejected")
	}
	SetJWTSecret("worker-session-ttl-secret")
	t.Cleanup(func() { SetJWTSecret("") })
	// ttl 被截断到上限：拿到的 expires 不能超过 24h。
	_, expires, err := MintServiceWorkerSession(7, 100*time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if time.Until(expires) > serviceWorkerSessionMaxTTL+time.Minute {
		t.Fatalf("ttl not capped: %v", time.Until(expires))
	}
}

// 公网 worker 面接受 worker 券，且只能领自己名下应用的 run。
func TestWorkerAPIAcceptsSessionTicket(t *testing.T) {
	SetJWTSecret("worker-session-api-secret")
	t.Cleanup(func() { SetJWTSecret("") })

	st := newFakeOrchestratorStore()
	st.agents = []store.ServiceAgentDescriptor{
		{ID: 1, Slug: "mine-app", Name: "自己的", Version: 1, AsyncThresholdSeconds: 60,
			OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 7, Visibility: "public"},
		{ID: 2, Slug: "other-app", Name: "别人的", Version: 1, AsyncThresholdSeconds: 60,
			OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 8, Visibility: "public"},
	}
	st.sessions["sess_mine"] = store.ServiceSession{
		SessionID: "sess_mine", AgentID: 1, DefinitionRev: 1, CallerUID: 9, State: "active", CreatedAt: time.Now(),
	}
	st.sessions["sess_other"] = store.ServiceSession{
		SessionID: "sess_other", AgentID: 2, DefinitionRev: 1, CallerUID: 9, State: "active", CreatedAt: time.Now(),
	}
	st.runs["run_mine"] = store.ServiceRun{RunID: "run_mine", SessionID: "sess_mine", State: "submitted"}
	st.runs["run_other"] = store.ServiceRun{RunID: "run_other", SessionID: "sess_other", State: "submitted"}
	st.claimable = []string{"run_mine", "run_other"}

	orch := NewServiceRunOrchestrator(st, "inst-token", "secret")
	mux := http.NewServeMux()
	orch.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	ticket, _, err := MintServiceWorkerSession(7, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	status, payload := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/claim", ticket, `{"limit":10,"instance_id":"desktop-1"}`)
	if status != http.StatusOK {
		t.Fatalf("claim with ticket = %d body=%s", status, payload)
	}
	var claimed ServiceClaimResult
	if err := json.Unmarshal(payload, &claimed); err != nil {
		t.Fatalf("decode: %v (%s)", err, payload)
	}
	if len(claimed.Runs) != 1 || claimed.Runs[0].AgentSlug != "mine-app" {
		t.Fatalf("claimed = %+v, want only mine-app", claimed.Runs)
	}

	// 别人的 run 依旧不可见。
	if status, _ := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/runs/run_other/heartbeat", ticket, `{"instance_id":"desktop-1"}`); status != http.StatusNotFound {
		t.Fatalf("other run with ticket = %d, want 404", status)
	}

	// 普通账号 JWT 仍然进不来。
	userToken, err := GenerateToken(7, "owner", "")
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}
	if status, _ := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/claim", userToken, `{"limit":10,"instance_id":"desktop-1"}`); status != http.StatusUnauthorized {
		t.Fatalf("claim with user JWT = %d, want 401", status)
	}
}

// POST /api/service-worker/session 返回券；非白名单账号拒绝。
func TestServiceWorkerSessionHandler(t *testing.T) {
	SetJWTSecret("worker-session-handler-secret")
	t.Cleanup(func() { SetJWTSecret("") })

	h := NewServiceWorkerSessionHandlerFromEnv()
	h.SetAllowed(func(uid int64) bool { return uid == 38 })

	req := httptest.NewRequest(http.MethodPost, "/api/service-worker/session", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no identity = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/service-worker/session", strings.NewReader(""))
	req = req.WithContext(contextWithClaims(req.Context(), &JWTClaims{UID: 38}))
	rec = httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed uid = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token     string `json:"token"`
		UID       int64  `json:"uid"`
		ExpiresAt string `json:"expires_at"`
		Scope     string `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.UID != 38 || out.Scope != "service_worker" || out.Token == "" || out.ExpiresAt == "" {
		t.Fatalf("session payload = %+v", out)
	}
	if uid, err := ParseServiceWorkerSession(out.Token); err != nil || uid != 38 {
		t.Fatalf("returned ticket unusable: uid=%d err=%v", uid, err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/service-worker/session", strings.NewReader(""))
	req = req.WithContext(contextWithClaims(req.Context(), &JWTClaims{UID: 999}))
	rec = httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("not whitelisted = %d, want 403", rec.Code)
	}
}
