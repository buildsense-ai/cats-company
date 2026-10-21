package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// ---- per-user worker 凭据 ----

type fakeWorkerTokenStore struct {
	mu     sync.Mutex
	nextID int64
	owners map[string]int64 // tokenHash -> uid（撤销即删）
	byID   map[int64]string // tokenID -> tokenHash
	list   map[int64][]store.ServiceWorkerToken
}

func newFakeWorkerTokenStore() *fakeWorkerTokenStore {
	return &fakeWorkerTokenStore{
		owners: map[string]int64{},
		byID:   map[int64]string{},
		list:   map[int64][]store.ServiceWorkerToken{},
	}
}

func (f *fakeWorkerTokenStore) CreateServiceWorkerToken(_ context.Context, uid int64, hash, prefix, label string) (store.ServiceWorkerToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	token := store.ServiceWorkerToken{ID: f.nextID, Prefix: prefix, Label: label, CreatedAt: time.Now()}
	f.owners[hash] = uid
	f.byID[token.ID] = hash
	f.list[uid] = append(f.list[uid], token)
	return token, nil
}

func (f *fakeWorkerTokenStore) ListServiceWorkerTokens(_ context.Context, uid int64) ([]store.ServiceWorkerToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.ServiceWorkerToken(nil), f.list[uid]...), nil
}

func (f *fakeWorkerTokenStore) RevokeServiceWorkerToken(_ context.Context, uid, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash, ok := f.byID[id]
	if !ok {
		return store.ErrServiceWorkerTokenNotFound
	}
	if f.owners[hash] != uid {
		return store.ErrServiceWorkerTokenNotFound
	}
	delete(f.owners, hash)
	return nil
}

func (f *fakeWorkerTokenStore) ResolveServiceWorkerToken(_ context.Context, hash string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	uid, ok := f.owners[hash]
	if !ok {
		return 0, store.ErrServiceWorkerTokenNotFound
	}
	return uid, nil
}

func TestServiceWorkerCredentialLifecycle(t *testing.T) {
	st := newFakeWorkerTokenStore()
	handler := NewServiceWorkerCredentialHandlerFromEnv(st)
	handler.SetAllowed(func(int64) bool { return true })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/service-worker/credentials", handler.HandleList)
	mux.HandleFunc("POST /api/service-worker/credentials", handler.HandleCreate)
	mux.HandleFunc("DELETE /api/service-worker/credentials/{id}", handler.HandleRevoke)
	ts := httptest.NewServer(mcpAuthStub(7, mux))
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/service-worker/credentials", "application/json",
		strings.NewReader(`{"label":"mac"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	raw, _ := created["token"].(string)
	if !strings.HasPrefix(raw, store.ServiceWorkerTokenPrefix) || len(raw) != len(store.ServiceWorkerTokenPrefix)+64 {
		t.Fatalf("token = %q, want cwk_ + 64 hex", raw)
	}
	if created["shown_once"] != true {
		t.Fatalf("shown_once = %v, want true（明文只返回一次）", created["shown_once"])
	}

	sum := sha256.Sum256([]byte(raw))
	if uid, err := st.ResolveServiceWorkerToken(context.Background(), hex.EncodeToString(sum[:])); err != nil || uid != 7 {
		t.Fatalf("resolve = %d %v, want uid 7", uid, err)
	}

	// 列表只给短前缀，绝不含明文。
	listResp, err := http.Get(ts.URL + "/api/service-worker/credentials")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listResp.Body.Close()
	var listed struct {
		Credentials []map[string]any `json:"credentials"`
		Prefix      string           `json:"prefix"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Prefix != store.ServiceWorkerTokenPrefix {
		t.Fatalf("prefix = %q", listed.Prefix)
	}
	if len(listed.Credentials) != 1 {
		t.Fatalf("credentials = %+v", listed.Credentials)
	}
	view := listed.Credentials[0]
	short, _ := view["prefix"].(string)
	if !strings.HasPrefix(raw, short) || len(short) >= len(raw) {
		t.Fatalf("view prefix = %q，应是明文的短前缀", short)
	}
	if _, leaked := view["token"]; leaked {
		t.Fatal("列表不应返回明文凭据")
	}

	// 撤销后立即失效。
	id := int64(created["id"].(float64))
	req, err := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/api/service-worker/credentials/%d", ts.URL, id), nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", delResp.StatusCode)
	}
	if _, err := st.ResolveServiceWorkerToken(context.Background(), hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("撤销后凭据应失效")
	}
}

func postWorkerAPI(t *testing.T, url, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, payload
}

// 公网 worker 面只认 cwk_ 凭据，且凭据只能认领/操作自己名下应用的 run。
func TestPublicWorkerScopedToOwnApplications(t *testing.T) {
	st := newFakeOrchestratorStore()
	st.agents = []store.ServiceAgentDescriptor{
		{ID: 1, Slug: "mine-app", Name: "我的应用", Version: 1, AsyncThresholdSeconds: 60,
			OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 7, Visibility: "public"},
		{ID: 2, Slug: "other-app", Name: "别人的应用", Version: 1, AsyncThresholdSeconds: 60,
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

	tokens := newFakeWorkerTokenStore()
	raw := store.ServiceWorkerTokenPrefix + strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(raw))
	if _, err := tokens.CreateServiceWorkerToken(context.Background(), 7, hex.EncodeToString(sum[:]), "cwk_aaaaaa", "test"); err != nil {
		t.Fatalf("create token: %v", err)
	}

	orch := NewServiceRunOrchestrator(st, "inst-token", "secret")
	orch.SetWorkerTokenStore(tokens)
	mux := http.NewServeMux()
	orch.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// 全局实例 token 不再能用公网面（只在 /internal 内网通道有效）。
	if status, _ := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/claim", "inst-token", `{"limit":10,"instance_id":"w1"}`); status != http.StatusUnauthorized {
		t.Fatalf("instance token on public surface = %d, want 401", status)
	}

	// cwk_ 凭据：只领到自己名下应用的 run。
	status, payload := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/claim", raw, `{"limit":10,"instance_id":"w1"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, payload)
	}
	var claimed ServiceClaimResult
	if err := json.Unmarshal(payload, &claimed); err != nil {
		t.Fatalf("decode claim: %v (%s)", err, payload)
	}
	if len(claimed.Runs) != 1 || claimed.Runs[0].AgentSlug != "mine-app" {
		t.Fatalf("claimed = %+v, want only mine-app", claimed.Runs)
	}

	// 别人的 run：续租/回填都当不存在。
	for _, action := range []string{"heartbeat", "complete", "fail"} {
		body := `{"instance_id":"w1"}`
		if action == "complete" {
			body = `{"result_id":"res_1","result":{"status":"completed"}}`
		}
		status, _ := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/runs/run_other/"+action, raw, body)
		if status != http.StatusNotFound {
			t.Fatalf("%s other run = %d, want 404", action, status)
		}
	}

	// 自己的 run 可以正常续租。
	if status, body := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/runs/run_mine/heartbeat", raw, `{"instance_id":"w1"}`); status != http.StatusOK {
		t.Fatalf("heartbeat own run = %d body=%s", status, body)
	}

	// 撤销凭据后立刻 401。
	if err := tokens.RevokeServiceWorkerToken(context.Background(), 7, 1); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if status, _ := postWorkerAPI(t, ts.URL+ServiceWorkerAPIPrefix+"/claim", raw, `{"limit":10,"instance_id":"w1"}`); status != http.StatusUnauthorized {
		t.Fatalf("revoked credential = %d, want 401", status)
	}
}
