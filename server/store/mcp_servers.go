package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ---- console MCP server 配置 + OAuth 2.1 凭据托管 ----
//
// 这三张表共同支撑「控制台 MCP Server 配置 CRUD + OAuth 2.1 回调 + 加密凭据托管」：
//   - mcp_server_configs    每个账号名下登记的 MCP server（含 OAuth 配置；client_secret 密文）
//   - mcp_oauth_states      OAuth 进行中的一次性 state（10 分钟过期；回调消费后立即作废）
//   - mcp_oauth_credentials 回调交换出的 access/refresh token（AES-256-GCM 密文落库）

var (
	ErrMCPServerNotFound     = errors.New("mcp server config not found")
	ErrMCPServerOwnedByOther = errors.New("mcp server config owned by other account")
	ErrMCPOAuthStateNotFound = errors.New("mcp oauth state not found")
	ErrMCPCredentialNotFound = errors.New("mcp oauth credential not found")
)

// MCPServerConfig 是控制台登记的一个外部 MCP server 配置。
//
// ClientSecretEnc / 凭据字段都是密文 —— 读侧要密钥才能还原；
// 控制台回显永远只给掩码，不给明文。
type MCPServerConfig struct {
	ID              int64
	OwnerUID        int64
	Name            string
	Endpoint        string
	Transport       string // streamable_http / sse
	Enabled         bool
	AuthType        string // none / oauth21 / bearer
	ClientID        string
	AuthorizeURL    string
	TokenURL        string
	Scopes          string
	ClientSecretEnc string
	ConfigJSON      json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// MCPOAuthState 是一次进行中的 OAuth 2.1 授权流程。
// state 一次性消费：回调成功后该行被删除，重放回调直接 404。
type MCPOAuthState struct {
	State        string
	ServerID     int64
	OwnerUID     int64
	CodeVerifier string // PKCE S256 verifier（回调交换 token 时用）
	RedirectURI  string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// MCPOAuthCredential 是一次授权完成的 token 托管记录（密文）。
type MCPOAuthCredential struct {
	ServerID        int64
	OwnerUID        int64
	AccessTokenEnc  string
	RefreshTokenEnc string
	TokenType       string
	Scope           string
	ExpiresAt       time.Time // 零值 = access token 不声明过期
	UpdatedAt       time.Time
}

// MCPServerConfigStore 是 console CRUD + OAuth 回调 + 凭据托管的存储面。
type MCPServerConfigStore interface {
	// 配置 CRUD（owner 归属过滤内建于每个方法；查别人 id 一律 ErrMCPServerNotFound）。
	CreateMCPServerConfig(ctx context.Context, cfg *MCPServerConfig) (int64, error)
	GetMCPServerConfig(ctx context.Context, ownerUID, id int64) (*MCPServerConfig, error)
	ListMCPServerConfigs(ctx context.Context, ownerUID int64) ([]MCPServerConfig, error)
	UpdateMCPServerConfig(ctx context.Context, cfg *MCPServerConfig) error
	DeleteMCPServerConfig(ctx context.Context, ownerUID, id int64) error

	// OAuth state：发起授权时写入，回调时消费（Take = 读 + 删，一次性）。
	CreateMCPOAuthState(ctx context.Context, st *MCPOAuthState) error
	TakeMCPOAuthState(ctx context.Context, state string) (*MCPOAuthState, error)

	// 凭据托管：upsert / 读 / 删（删配置时级联清掉）。
	UpsertMCPOAuthCredential(ctx context.Context, cred *MCPOAuthCredential) error
	GetMCPOAuthCredential(ctx context.Context, ownerUID, serverID int64) (*MCPOAuthCredential, error)
	DeleteMCPOAuthCredential(ctx context.Context, ownerUID, serverID int64) error
}
