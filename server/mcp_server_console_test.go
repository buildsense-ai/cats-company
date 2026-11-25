package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// fakeMCPServerConfigStore 是 MCPServerConfigStore 的内存实现（测试用）。
type fakeMCPServerConfigStore struct {
	mu          sync.Mutex
	configs     map[int64]*store.MCPServerConfig
	states      map[string]*store.MCPOAuthState
	credentials map[string]*store.MCPOAuthCredential // serverID|owner
	nextID      int64
}

func newFakeMCPServerConfigStore() *fakeMCPServerConfigStore {
	return &fakeMCPServerConfigStore{
		configs:     map[int64]*store.MCPServerConfig{},
		states:      map[string]*store.MCPOAuthState{},
		credentials: map[string]*store.MCPOAuthCredential{},
	}
}

func (f *fakeMCPServerConfigStore) CreateMCPServerConfig(_ context.Context, cfg *store.MCPServerConfig) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	copied := *cfg
	copied.ID = f.nextID
	copied.CreatedAt = time.Now()
	copied.UpdatedAt = copied.CreatedAt
	f.configs[copied.ID] = &copied
	return copied.ID, nil
}

func (f *fakeMCPServerConfigStore) GetMCPServerConfig(_ context.Context, ownerUID, id int64) (*store.MCPServerConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.configs[id]
	if !ok || cfg.OwnerUID != ownerUID {
		return nil, store.ErrMCPServerNotFound
	}
	copied := *cfg
	return &copied, nil
}

func (f *fakeMCPServerConfigStore) ListMCPServerConfigs(_ context.Context, ownerUID int64) ([]store.MCPServerConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.MCPServerConfig, 0)
	for _, cfg := range f.configs {
		if cfg.OwnerUID == ownerUID {
			out = append(out, *cfg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeMCPServerConfigStore) UpdateMCPServerConfig(_ context.Context, cfg *store.MCPServerConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.configs[cfg.ID]
	if !ok || existing.OwnerUID != cfg.OwnerUID {
		return store.ErrMCPServerNotFound
	}
	copied := *cfg
	copied.UpdatedAt = time.Now()
	f.configs[cfg.ID] = &copied
	return nil
}

func (f *fakeMCPServerConfigStore) DeleteMCPServerConfig(_ context.Context, ownerUID, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.configs[id]
	if !ok || cfg.OwnerUID != ownerUID {
		return store.ErrMCPServerNotFound
	}
	delete(f.configs, id)
	return nil
}

func (f *fakeMCPServerConfigStore) CreateMCPOAuthState(_ context.Context, st *store.MCPOAuthState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *st
	f.states[st.State] = &copied
	return nil
}

func (f *fakeMCPServerConfigStore) TakeMCPOAuthState(_ context.Context, state string) (*store.MCPOAuthState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[state]
	if !ok {
		return nil, store.ErrMCPOAuthStateNotFound
	}
	delete(f.states, state)
	copied := *st
	return &copied, nil
}

func credentialKey(serverID, ownerUID int64) string {
	return fmt.Sprintf("%d|%d", serverID, ownerUID)
}

func (f *fakeMCPServerConfigStore) UpsertMCPOAuthCredential(_ context.Context, cred *store.MCPOAuthCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *cred
	copied.UpdatedAt = time.Now()
	f.credentials[credentialKey(cred.ServerID, cred.OwnerUID)] = &copied
	return nil
}

func (f *fakeMCPServerConfigStore) GetMCPOAuthCredential(_ context.Context, ownerUID, serverID int64) (*store.MCPOAuthCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cred, ok := f.credentials[credentialKey(serverID, ownerUID)]
	if !ok {
		return nil, store.ErrMCPCredentialNotFound
	}
	copied := *cred
	return &copied, nil
}

func (f *fakeMCPServerConfigStore) DeleteMCPOAuthCredential(_ context.Context, ownerUID, serverID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.credentials, credentialKey(serverID, ownerUID))
	return nil
}

func testVault(t *testing.T) *mcpCredentialVault {
	t.Helper()
	vault, err := NewMCPCredentialVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	return vault
}

func consoleTestServer(t *testing.T, uid int64, h *MCPServerConsoleHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mcp-servers", h.HandleCollection)
	mux.HandleFunc("/api/mcp-servers/", h.HandleItem)
	mux.HandleFunc("/api/mcp/oauth/callback", h.HandleOAuthCallback)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), uidKey, uid)))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, client *http.Client, method, url string, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp.StatusCode, parsed
}

func TestMCPCredentialVaultRoundTrip(t *testing.T) {
	vault := testVault(t)
	enc, err := vault.Encrypt(7, 42, "access_token", []byte("secret-token-value"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, "v1:") {
		t.Fatalf("ciphertext format: %q", enc)
	}
	if strings.Contains(enc, "secret-token-value") {
		t.Fatal("ciphertext leaks plaintext")
	}
	plain, err := vault.Decrypt(7, 42, "access_token", enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plain) != "secret-token-value" {
		t.Fatalf("roundtrip = %q", plain)
	}
	// AAD 绑定：换 owner / server / field 任何一个都必须解不开。
	for _, tamper := range []struct {
		owner, server int64
		field         string
	}{{8, 42, "access_token"}, {7, 43, "access_token"}, {7, 42, "refresh_token"}} {
		if _, err := vault.Decrypt(tamper.owner, tamper.server, tamper.field, enc); err == nil {
			t.Fatalf("tampered AAD %+v must fail decryption", tamper)
		}
	}
}

func TestMCPServerConsoleCRUD(t *testing.T) {
	st := newFakeMCPServerConfigStore()
	h := NewMCPServerConsoleHandler(st, testVault(t))
	ts := consoleTestServer(t, 101, h)
	client := ts.Client()

	// 创建（含 client_secret：加密托管）。
	status, created := postJSON(t, client, http.MethodPost, ts.URL+"/api/mcp-servers", `{
		"name": "日历 MCP", "endpoint": "https://mcp.example.com/mcp",
		"auth_type": "oauth21", "client_id": "catsco-console",
		"authorize_url": "https://auth.example.com/authorize", "token_url": "https://auth.example.com/token",
		"scopes": "calendar.read", "client_secret": "topsecret", "enabled": true
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, created)
	}
	id := int64(created["id"].(float64))
	if created["has_client_secret"] != true {
		t.Fatalf("secret flag missing: %v", created)
	}
	// 密钥必须是密文且可还原。
	raw := st.configs[id]
	if raw.ClientSecretEnc == "" || strings.Contains(raw.ClientSecretEnc, "topsecret") {
		t.Fatalf("secret not encrypted: %q", raw.ClientSecretEnc)
	}
	plain, err := h.vault.Decrypt(101, id, "client_secret", raw.ClientSecretEnc)
	if err != nil || string(plain) != "topsecret" {
		t.Fatalf("secret roundtrip: %v %q", err, plain)
	}
	// 响应里绝不出现明文 secret。
	rawJSON, _ := json.Marshal(created)
	if strings.Contains(string(rawJSON), "topsecret") {
		t.Fatal("response leaks client secret")
	}

	// 列表 + 读取。
	status, list := postJSON(t, client, http.MethodGet, ts.URL+"/api/mcp-servers", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	servers := list["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("list = %v", servers)
	}

	// 更新（不改 secret：不传字段 = 不动）。
	status, updated := postJSON(t, client, http.MethodPut, fmt.Sprintf("%s/api/mcp-servers/%d", ts.URL, id), `{
		"name": "日历 MCP v2", "endpoint": "https://mcp.example.com/mcp",
		"auth_type": "oauth21", "client_id": "catsco-console",
		"authorize_url": "https://auth.example.com/authorize", "token_url": "https://auth.example.com/token",
		"enabled": false
	}`)
	if status != http.StatusOK || updated["name"] != "日历 MCP v2" || updated["enabled"] != false {
		t.Fatalf("update: %d %v", status, updated)
	}
	if st.configs[id].ClientSecretEnc == "" {
		t.Fatal("secret must survive update without secret fields")
	}

	// 校验失败路径：非 https 的 oauth url。
	status, _ = postJSON(t, client, http.MethodPost, ts.URL+"/api/mcp-servers", `{
		"name": "x", "endpoint": "https://mcp.example.com", "auth_type": "oauth21", "client_id": "a",
		"authorize_url": "http://insecure.example.com/authorize", "token_url": "https://auth.example.com/token"
	}`)
	if status != http.StatusBadRequest {
		t.Fatalf("http oauth url must be rejected, got %d", status)
	}

	// 删除。
	status, deleted := postJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/mcp-servers/%d", ts.URL, id), "")
	if status != http.StatusOK || deleted["deleted"] != true {
		t.Fatalf("delete: %d %v", status, deleted)
	}
	status, _ = postJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/mcp-servers/%d", ts.URL, id), "")
	if status != http.StatusNotFound {
		t.Fatalf("deleted server must 404, got %d", status)
	}
}

func TestMCPServerConsoleCrossOwnerIsolation(t *testing.T) {
	st := newFakeMCPServerConfigStore()
	h := NewMCPServerConsoleHandler(st, testVault(t))
	// 账号 101 创建；账号 102 视角必须全部 404。
	ts101 := consoleTestServer(t, 101, h)
	ts102 := consoleTestServer(t, 102, h)
	_, created := postJSON(t, ts101.Client(), http.MethodPost, ts101.URL+"/api/mcp-servers", `{"name":"x","endpoint":"https://mcp.example.com","enabled":true}`)
	id := int64(created["id"].(float64))
	status, _ := postJSON(t, ts102.Client(), http.MethodGet, fmt.Sprintf("%s/api/mcp-servers/%d", ts102.URL, id), "")
	if status != http.StatusNotFound {
		t.Fatalf("cross-owner read must 404, got %d", status)
	}
	status, list := postJSON(t, ts102.Client(), http.MethodGet, ts102.URL+"/api/mcp-servers", "")
	if status != http.StatusOK || len(list["servers"].([]any)) != 0 {
		t.Fatalf("cross-owner list must be empty: %v", list)
	}
}

func TestMCPOAuth21Flow(t *testing.T) {
	st := newFakeMCPServerConfigStore()
	h := NewMCPServerConsoleHandler(st, testVault(t))
	ts := consoleTestServer(t, 101, h)
	client := ts.Client()

	// 配置一个 oauth21 server。
	_, created := postJSON(t, client, http.MethodPost, ts.URL+"/api/mcp-servers", `{
		"name": "日历 MCP", "endpoint": "https://mcp.example.com/mcp",
		"auth_type": "oauth21", "client_id": "catsco-console",
		"authorize_url": "https://auth.example.com/authorize", "token_url": "https://auth.example.com/token",
		"client_secret": "topsecret", "enabled": true
	}`)
	id := int64(created["id"].(float64))

	// 发起授权：必须给出含 PKCE 的跳转地址。
	status, start := postJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/mcp-servers/%d/oauth/start", ts.URL, id), "")
	if status != http.StatusOK {
		t.Fatalf("oauth start = %d %v", status, start)
	}
	authorizeURL, err := url.Parse(start["authorize_url"].(string))
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	query := authorizeURL.Query()
	state := query.Get("state")
	if state == "" || query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("pkce params missing: %v", query)
	}
	if query.Get("response_type") != "code" || query.Get("client_id") != "catsco-console" {
		t.Fatalf("authorize params: %v", query)
	}
	if strings.Contains(authorizeURL.String(), "topsecret") {
		t.Fatal("client secret must never appear in authorize url")
	}
	// verifier 只存在服务端 state 里。
	stored := st.states[state]
	if stored == nil || stored.CodeVerifier == "" {
		t.Fatal("verifier must be stored server-side")
	}
	if stored.RedirectURI != query.Get("redirect_uri") {
		t.Fatal("redirect uri mismatch")
	}

	// 回调：换一个假 token 端点（exchange 注入），验证加密托管落库。
	var capturedForm url.Values
	fakeExchange := func(_ *MCPServerConsoleHandler, _ *http.Request, cfg *store.MCPServerConfig, clientSecret, code, verifier, redirectURI string) (*oauthTokenResponse, error) {
		capturedForm = url.Values{
			"client_secret": {clientSecret}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI},
		}
		return &oauthTokenResponse{
			AccessToken: "access-token-abc", RefreshToken: "refresh-token-xyz",
			TokenType: "Bearer", ExpiresIn: 3600, Scope: "calendar.read",
		}, nil
	}
	callbackReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/mcp/oauth/callback?code=authcode1&state="+url.QueryEscape(state), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	h.handleOAuthCallbackWith(recorder, callbackReq, fakeExchange)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 交换用的是存起来的 verifier 与解密后的 secret。
	if capturedForm.Get("code_verifier") != stored.CodeVerifier {
		t.Fatalf("verifier mismatch: %q", capturedForm.Get("code_verifier"))
	}
	if capturedForm.Get("client_secret") != "topsecret" {
		t.Fatalf("client secret: %q", capturedForm.Get("client_secret"))
	}
	if capturedForm.Get("code") != "authcode1" {
		t.Fatalf("code: %q", capturedForm.Get("code"))
	}
	// 凭据加密落库，密文不含明文，AAD 绑定可还原。
	cred, err := st.GetMCPOAuthCredential(context.Background(), 101, id)
	if err != nil {
		t.Fatalf("credential missing: %v", err)
	}
	if strings.Contains(cred.AccessTokenEnc, "access-token-abc") {
		t.Fatal("access token stored in plaintext")
	}
	plainAccess, err := h.vault.Decrypt(101, id, "access_token", cred.AccessTokenEnc)
	if err != nil || string(plainAccess) != "access-token-abc" {
		t.Fatalf("access token roundtrip: %v %q", err, plainAccess)
	}
	plainRefresh, err := h.vault.Decrypt(101, id, "refresh_token", cred.RefreshTokenEnc)
	if err != nil || string(plainRefresh) != "refresh-token-xyz" {
		t.Fatalf("refresh token roundtrip: %v", err)
	}
	if cred.ExpiresAt.IsZero() {
		t.Fatal("expires_at must be computed from expires_in")
	}
	// 凭据摘要出现在配置视图里，但 token 本体不出现。
	_, view := postJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/mcp-servers/%d", ts.URL, id), "")
	viewJSON, _ := json.Marshal(view)
	if strings.Contains(string(viewJSON), "access-token-abc") {
		t.Fatal("view leaks token")
	}
	if view["credential"] == nil {
		t.Fatalf("credential summary missing: %v", view)
	}

	// state 一次性消费：重放同一个回调必须 404。
	recorder2 := httptest.NewRecorder()
	h.handleOAuthCallbackWith(recorder2, callbackReq, fakeExchange)
	if recorder2.Code != http.StatusNotFound {
		t.Fatalf("replayed callback must be 404, got %d", recorder2.Code)
	}
}

func TestMCPOAuthCallbackRejectsExpiredState(t *testing.T) {
	st := newFakeMCPServerConfigStore()
	h := NewMCPServerConsoleHandler(st, testVault(t))
	now := time.Now()
	h.now = func() time.Time { return now }
	// 手工种一份过期的 state + 配置。
	id, err := st.CreateMCPServerConfig(context.Background(), &store.MCPServerConfig{
		OwnerUID: 101, Name: "x", Endpoint: "https://mcp.example.com", AuthType: "oauth21",
		ClientID: "c", AuthorizeURL: "https://auth.example.com/a", TokenURL: "https://auth.example.com/t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateMCPOAuthState(context.Background(), &store.MCPOAuthState{
		State: "expiredstate", ServerID: id, OwnerUID: 101, CodeVerifier: "v", RedirectURI: "https://x/cb",
		ExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "/api/mcp/oauth/callback?code=c&state=expiredstate", nil)
	recorder := httptest.NewRecorder()
	called := false
	h.handleOAuthCallbackWith(recorder, req, func(_ *MCPServerConsoleHandler, _ *http.Request, _ *store.MCPServerConfig, _, _, _, _ string) (*oauthTokenResponse, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	if called {
		t.Fatal("token exchange must not run for expired state")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expired state = %d", recorder.Code)
	}
}

func TestMCPOAuthCallbackUnavailableWithoutVault(t *testing.T) {
	st := newFakeMCPServerConfigStore()
	h := NewMCPServerConsoleHandler(st, nil) // 密钥未配置 → 托管不可用
	req, _ := http.NewRequest(http.MethodGet, "/api/mcp/oauth/callback?code=c&state=s", nil)
	recorder := httptest.NewRecorder()
	h.handleOAuthCallbackWith(recorder, req, exchangeToken)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-vault callback = %d, want 503", recorder.Code)
	}
}
