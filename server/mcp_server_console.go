package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// MCPServerConsoleHandler 是控制台「MCP Server 配置」的数据面：
//   - CRUD：GET/POST/PUT/DELETE /api/mcp-servers[/{id}]
//   - OAuth 2.1：POST /api/mcp-servers/{id}/oauth/start 发起授权（返回跳转地址），
//     GET /api/mcp/oauth/callback 接收回调、用 PKCE 换 token、加密托管进库。
//
// 凭据纪律（为什么是密文而不是明文配置）：
// client_secret 与 access/refresh token 经过 mcpCredentialVault 加密后才落库，
// 回显一律掩码 —— 即使库被拖走，没有密钥也只是些解不开的噪声。
type MCPServerConsoleHandler struct {
	store    store.MCPServerConfigStore
	vault    *mcpCredentialVault
	http     *http.Client
	// externalBaseURL 是回调地址的外部可达前缀（例 https://chat.example.com），
	// 用于发起授权时拼 redirect_uri。留空则从请求反推 Host。
	externalBaseURL string
	now             func() time.Time // 测试注入；nil=time.Now
}

// NewMCPServerConsoleHandler 装配 console 数据面。vault 为 nil 时：
// CRUD 仍可用（除 secret 写入），OAuth 授权/回调不可用 —— 宁可关闭托管也不落明文。
func NewMCPServerConsoleHandler(st store.MCPServerConfigStore, vault *mcpCredentialVault) *MCPServerConsoleHandler {
	return &MCPServerConsoleHandler{store: st, vault: vault, http: &http.Client{Timeout: 30 * time.Second}}
}

// NewMCPServerConsoleHandlerFromEnv 从环境装配（密钥缺失 → 托管功能降级）。
func NewMCPServerConsoleHandlerFromEnv(st store.MCPServerConfigStore) *MCPServerConsoleHandler {
	vault, err := NewMCPCredentialVaultFromEnv()
	if err != nil {
		log.Printf("mcp server console: credential vault disabled: %v", err)
	}
	h := NewMCPServerConsoleHandler(st, vault)
	h.externalBaseURL = strings.TrimRight(strings.TrimSpace(envOr("CATSCO_MCP_OAUTH_BASE_URL", "")), "/")
	return h
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (h *MCPServerConsoleHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// ---- CRUD ----

type mcpServerConfigView struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Endpoint        string `json:"endpoint"`
	Transport       string `json:"transport"`
	Enabled         bool   `json:"enabled"`
	AuthType        string `json:"auth_type"`
	ClientID        string `json:"client_id"`
	AuthorizeURL    string `json:"authorize_url"`
	TokenURL        string `json:"token_url"`
	Scopes          string `json:"scopes"`
	HasClientSecret bool   `json:"has_client_secret"`
	// Credential 摘要只给「有没有、什么类型、何时过期」—— token 本体永不出库。
	Credential *mcpCredentialView `json:"credential,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

type mcpCredentialView struct {
	TokenType string    `json:"token_type"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (h *MCPServerConsoleHandler) viewOf(cfg *store.MCPServerConfig) mcpServerConfigView {
	view := mcpServerConfigView{
		ID: cfg.ID, Name: cfg.Name, Endpoint: cfg.Endpoint, Transport: cfg.Transport,
		Enabled: cfg.Enabled, AuthType: cfg.AuthType, ClientID: cfg.ClientID,
		AuthorizeURL: cfg.AuthorizeURL, TokenURL: cfg.TokenURL, Scopes: cfg.Scopes,
		HasClientSecret: cfg.ClientSecretEnc != "",
		CreatedAt:       cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt,
	}
	if h.store != nil {
		if cred, err := h.store.GetMCPOAuthCredential(context.Background(), cfg.OwnerUID, cfg.ID); err == nil {
			view.Credential = &mcpCredentialView{
				TokenType: cred.TokenType, Scope: cred.Scope, ExpiresAt: cred.ExpiresAt, UpdatedAt: cred.UpdatedAt,
			}
		}
	}
	return view
}

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSONResponse(w, status, map[string]string{"error": message})
}

type mcpServerConfigWrite struct {
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint"`
	Transport    string `json:"transport"`
	Enabled      bool   `json:"enabled"`
	AuthType     string `json:"auth_type"`
	ClientID     string `json:"client_id"`
	AuthorizeURL string `json:"authorize_url"`
	TokenURL     string `json:"token_url"`
	Scopes       string `json:"scopes"`
	ClientSecret string `json:"client_secret,omitempty"`
	// ClearSecret=true 时显式清除已托管的 client_secret（与「不传不改」区分）。
	ClearSecret bool `json:"clear_secret,omitempty"`
}

// validateMCPServerWrite 校验写入请求；返回规范化后的拷贝。
func validateMCPServerWrite(in mcpServerConfigWrite) (mcpServerConfigWrite, string) {
	in.Name = strings.TrimSpace(in.Name)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.Transport = strings.TrimSpace(in.Transport)
	in.AuthType = strings.TrimSpace(in.AuthType)
	if in.Name == "" || len(in.Name) > 128 {
		return in, "name_required"
	}
	if in.Endpoint == "" || len(in.Endpoint) > 2048 {
		return in, "endpoint_required"
	}
	if parsed, err := url.Parse(in.Endpoint); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return in, "endpoint_must_be_http_url"
	}
	if in.Transport == "" {
		in.Transport = "streamable_http"
	}
	switch in.Transport {
	case "streamable_http", "sse":
	default:
		return in, "transport_unsupported"
	}
	if in.AuthType == "" {
		in.AuthType = "none"
	}
	switch in.AuthType {
	case "none", "oauth21", "bearer":
	default:
		return in, "auth_type_unsupported"
	}
	if in.AuthType == "oauth21" {
		for _, u := range []string{in.AuthorizeURL, in.TokenURL} {
			if parsed, err := url.Parse(strings.TrimSpace(u)); err != nil || parsed.Scheme != "https" {
				// OAuth 2.1 §3.1: 授权与 token 端点必须 TLS（localhost 是唯一的开发例外）。
				if err == nil && parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
					continue
				}
				return in, "oauth_urls_must_be_https"
			}
		}
		if strings.TrimSpace(in.ClientID) == "" {
			return in, "client_id_required_for_oauth21"
		}
	}
	return in, ""
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

func (h *MCPServerConsoleHandler) uid(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	return uid, true
}

// HandleCollection 处理 /api/mcp-servers（GET 列表 / POST 创建）。
func (h *MCPServerConsoleHandler) HandleCollection(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.uid(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		configs, err := h.store.ListMCPServerConfigs(r.Context(), uid)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "list_failed")
			return
		}
		out := make([]mcpServerConfigView, 0, len(configs))
		for i := range configs {
			out = append(out, h.viewOf(&configs[i]))
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{"servers": out})
	case http.MethodPost:
		var in mcpServerConfigWrite
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		norm, problem := validateMCPServerWrite(in)
		if problem != "" {
			writeJSONError(w, http.StatusBadRequest, problem)
			return
		}
		in = norm
		cfg := &store.MCPServerConfig{
			OwnerUID: uid, Name: in.Name, Endpoint: in.Endpoint, Transport: in.Transport,
			Enabled: in.Enabled, AuthType: in.AuthType, ClientID: strings.TrimSpace(in.ClientID),
			AuthorizeURL: strings.TrimSpace(in.AuthorizeURL), TokenURL: strings.TrimSpace(in.TokenURL),
			Scopes: strings.TrimSpace(in.Scopes), ConfigJSON: json.RawMessage("{}"),
		}
		// 先建行拿 id，再加密写 secret —— AAD 绑定 (owner, id)，顺序不能反。
		if h.store == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "store_unavailable")
			return
		}
		id, err := h.store.CreateMCPServerConfig(r.Context(), cfg)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "create_failed")
			return
		}
		cfg.ID = id
		if secret := strings.TrimSpace(in.ClientSecret); secret != "" {
			if err := h.encryptSecretInto(cfg, uid, secret); err != nil {
				writeJSONError(w, http.StatusServiceUnavailable, "vault_unavailable")
				return
			}
			if err := h.store.UpdateMCPServerConfig(r.Context(), cfg); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "create_failed")
				return
			}
		}
		writeJSONResponse(w, http.StatusCreated, h.viewOf(cfg))
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h *MCPServerConsoleHandler) encryptSecretInto(cfg *store.MCPServerConfig, ownerUID int64, secret string) error {
	if h.vault == nil {
		return errors.New("vault unavailable")
	}
	enc, err := h.vault.Encrypt(ownerUID, cfg.ID, "client_secret", []byte(secret))
	if err != nil {
		return err
	}
	cfg.ClientSecretEnc = enc
	return nil
}

func parsePathID(path, prefix string) (int64, string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" || strings.Contains(rest, "//") {
		return 0, "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}
	return id, sub, true
}

// HandleItem 处理 /api/mcp-servers/{id} 及子资源（GET/PUT/DELETE、oauth/start）。
func (h *MCPServerConsoleHandler) HandleItem(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.uid(w, r)
	if !ok {
		return
	}
	id, sub, valid := parsePathID(r.URL.Path, "/api/mcp-servers/")
	if !valid {
		writeJSONError(w, http.StatusNotFound, "not_found")
		return
	}
	if sub == "oauth/start" {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		h.handleOAuthStart(w, r, uid, id)
		return
	}
	if sub != "" {
		writeJSONError(w, http.StatusNotFound, "not_found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := h.store.GetMCPServerConfig(r.Context(), uid, id)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSONResponse(w, http.StatusOK, h.viewOf(cfg))
	case http.MethodPut:
		h.handleUpdate(w, r, uid, id)
	case http.MethodDelete:
		if err := h.store.DeleteMCPOAuthCredential(r.Context(), uid, id); err != nil {
			log.Printf("mcp server delete credential uid=%d id=%d: %v", uid, id, err)
		}
		if err := h.store.DeleteMCPServerConfig(r.Context(), uid, id); err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h *MCPServerConsoleHandler) handleUpdate(w http.ResponseWriter, r *http.Request, uid, id int64) {
	cfg, err := h.store.GetMCPServerConfig(r.Context(), uid, id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found")
		return
	}
	var in mcpServerConfigWrite
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	norm, problem := validateMCPServerWrite(in)
	if problem != "" {
		writeJSONError(w, http.StatusBadRequest, problem)
		return
	}
	in = norm
	cfg.Name = in.Name
	cfg.Endpoint = in.Endpoint
	cfg.Transport = in.Transport
	cfg.Enabled = in.Enabled
	cfg.AuthType = in.AuthType
	cfg.ClientID = strings.TrimSpace(in.ClientID)
	cfg.AuthorizeURL = strings.TrimSpace(in.AuthorizeURL)
	cfg.TokenURL = strings.TrimSpace(in.TokenURL)
	cfg.Scopes = strings.TrimSpace(in.Scopes)
	// secret 三态：不传 = 不动；clear_secret = 清除；给了值 = 重新加密托管。
	switch {
	case in.ClearSecret:
		cfg.ClientSecretEnc = ""
	case strings.TrimSpace(in.ClientSecret) != "":
		if err := h.encryptSecretInto(cfg, uid, strings.TrimSpace(in.ClientSecret)); err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "vault_unavailable")
			return
		}
	}
	if err := h.store.UpdateMCPServerConfig(r.Context(), cfg); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "update_failed")
		return
	}
	writeJSONResponse(w, http.StatusOK, h.viewOf(cfg))
}

// ---- OAuth 2.1（授权码 + PKCE S256）----

const mcpOAuthStateTTL = 10 * time.Minute

func randomURLSafe(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// handleOAuthStart 生成 state + PKCE verifier，回给前端授权跳转地址。
// verifier 不落浏览器、不落日志，只进 mcp_oauth_states —— 拦截到授权码的攻击者
// 没有 verifier 就换不出 token。
func (h *MCPServerConsoleHandler) handleOAuthStart(w http.ResponseWriter, r *http.Request, uid, id int64) {
	cfg, err := h.store.GetMCPServerConfig(r.Context(), uid, id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found")
		return
	}
	if cfg.AuthType != "oauth21" {
		writeJSONError(w, http.StatusBadRequest, "server_not_oauth21")
		return
	}
	state, err := randomURLSafe(32)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "state_generation_failed")
		return
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "state_generation_failed")
		return
	}
	redirectURI := h.redirectURI(r)
	if err := h.store.CreateMCPOAuthState(r.Context(), &store.MCPOAuthState{
		State: state, ServerID: cfg.ID, OwnerUID: uid,
		CodeVerifier: verifier, RedirectURI: redirectURI,
		ExpiresAt: h.clock().Add(mcpOAuthStateTTL),
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "state_store_failed")
		return
	}
	authorize, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "authorize_url_invalid")
		return
	}
	query := authorize.Query()
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	if cfg.Scopes != "" {
		query.Set("scope", cfg.Scopes)
	}
	// PKCE（OAuth 2.1 强制）：S256 挑战。
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	authorize.RawQuery = query.Encode()
	writeJSONResponse(w, http.StatusOK, map[string]string{
		"authorize_url": authorize.String(),
		"state":         state,
		"expires_at":    h.clock().Add(mcpOAuthStateTTL).UTC().Format(time.RFC3339),
	})
}

func (h *MCPServerConsoleHandler) redirectURI(r *http.Request) string {
	base := h.externalBaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = strings.Split(proto, ",")[0]
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimRight(base, "/") + "/api/mcp/oauth/callback"
}

// oauthTokenResponse 是 token 端点的响应子集（RFC 6749 §5.1）。
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// tokenExchanger 让测试换掉真正的 HTTP 交换。
type tokenExchanger func(h *MCPServerConsoleHandler, r *http.Request, cfg *store.MCPServerConfig, clientSecret, code, verifier, redirectURI string) (*oauthTokenResponse, error)

// exchangeToken 是真实实现：POST application/x-www-form-urlencoded 到 token 端点。
func exchangeToken(h *MCPServerConsoleHandler, r *http.Request, cfg *store.MCPServerConfig, clientSecret, code, verifier, redirectURI string) (*oauthTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := h.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var token oauthTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("token response invalid: %w", err)
	}
	if token.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}
	return &token, nil
}

// HandleOAuthCallback 接收 OAuth 2.1 回调（GET /api/mcp/oauth/callback?code&state）。
//
// 这条路径是浏览器重定向，不带 JWT —— 身份完全由一次性 state 承载。
// 安全链：state 一次性消费 + 过期校验 + PKCE verifier 只存在服务端 +
// token 加密落库，任何一环断裂都拿不到可用凭据。
func (h *MCPServerConsoleHandler) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	h.handleOAuthCallbackWith(w, r, exchangeToken)
}

func (h *MCPServerConsoleHandler) handleOAuthCallbackWith(w http.ResponseWriter, r *http.Request, exchange tokenExchanger) {
	fail := func(status int, message string) {
		writeJSONError(w, status, message)
	}
	if h.vault == nil {
		fail(http.StatusServiceUnavailable, "credential_vault_unavailable")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	stateParam := strings.TrimSpace(r.URL.Query().Get("state"))
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		// 用户拒绝授权是正常分支：如实报告，不当作系统错误。
		fail(http.StatusBadRequest, "authorization_denied: "+errParam)
		return
	}
	if code == "" || stateParam == "" {
		fail(http.StatusBadRequest, "code_and_state_required")
		return
	}
	st, err := h.store.TakeMCPOAuthState(r.Context(), stateParam)
	if err != nil {
		fail(http.StatusNotFound, "state_not_found_or_consumed")
		return
	}
	if h.clock().After(st.ExpiresAt) {
		fail(http.StatusBadRequest, "state_expired")
		return
	}
	cfg, err := h.store.GetMCPServerConfig(r.Context(), st.OwnerUID, st.ServerID)
	if err != nil {
		fail(http.StatusNotFound, "server_not_found")
		return
	}
	// 解 client_secret 参与交换（机密客户端）；公开客户端没有 secret 也行（PKCE 兜底）。
	clientSecret := ""
	if cfg.ClientSecretEnc != "" {
		plain, err := h.vault.Decrypt(st.OwnerUID, cfg.ID, "client_secret", cfg.ClientSecretEnc)
		if err != nil {
			fail(http.StatusInternalServerError, "secret_decrypt_failed")
			return
		}
		clientSecret = string(plain)
	}
	token, err := exchange(h, r, cfg, clientSecret, code, st.CodeVerifier, st.RedirectURI)
	if err != nil {
		log.Printf("mcp oauth callback token exchange server=%d: %v", cfg.ID, err)
		fail(http.StatusBadGateway, "token_exchange_failed")
		return
	}
	accessEnc, err := h.vault.Encrypt(st.OwnerUID, cfg.ID, "access_token", []byte(token.AccessToken))
	if err != nil {
		fail(http.StatusInternalServerError, "credential_encrypt_failed")
		return
	}
	refreshEnc := ""
	if token.RefreshToken != "" {
		refreshEnc, err = h.vault.Encrypt(st.OwnerUID, cfg.ID, "refresh_token", []byte(token.RefreshToken))
		if err != nil {
			fail(http.StatusInternalServerError, "credential_encrypt_failed")
			return
		}
	}
	expiresAt := time.Time{}
	if token.ExpiresIn > 0 {
		expiresAt = h.clock().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	if err := h.store.UpsertMCPOAuthCredential(r.Context(), &store.MCPOAuthCredential{
		ServerID: cfg.ID, OwnerUID: st.OwnerUID,
		AccessTokenEnc: accessEnc, RefreshTokenEnc: refreshEnc,
		TokenType: token.TokenType, Scope: token.Scope, ExpiresAt: expiresAt,
	}); err != nil {
		fail(http.StatusInternalServerError, "credential_store_failed")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"connected": true,
		"server_id": cfg.ID,
		"token_type": token.TokenType,
		"scope":      token.Scope,
		"expires_at": expiresAt,
	})
}
