package store

import (
	"context"
	"errors"
	"time"
)

// 用户自己的 worker 凭据（per-user）。
//
// 背景：平台原本只有一个全局实例 token（CATS_SERVICE_INSTANCE_TOKEN），拿到它就能
// 认领任何人的 run；但 worker 必须部署在用户自己机器上，等于把全局秘密发给每个用户。
// 改成按用户签发后，凭据只能认领「该用户拥有的应用」的 run，泄露也只影响本人。
const ServiceWorkerTokenPrefix = "cwk_"

var (
	// ErrServiceWorkerTokenNotFound 凭据不存在或已撤销（外部一律 401，不区分）。
	ErrServiceWorkerTokenNotFound = errors.New("service worker token not found")
)

// ServiceWorkerToken 是凭据的展示信息；明文只在创建时返回一次，库里只存 sha256。
type ServiceWorkerToken struct {
	ID         int64      `json:"id"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ServiceWorkerTokenStore 管理 per-user worker 凭据。
type ServiceWorkerTokenStore interface {
	// CreateServiceWorkerToken 落一行凭据（只存 hash 与前缀），返回展示信息。
	CreateServiceWorkerToken(ctx context.Context, uid int64, tokenHash, prefix, label string) (ServiceWorkerToken, error)
	// ListServiceWorkerTokens 列出该账号未撤销的凭据。
	ListServiceWorkerTokens(ctx context.Context, uid int64) ([]ServiceWorkerToken, error)
	// RevokeServiceWorkerToken 撤销自己的凭据；已撤销视为幂等成功，他人/不存在 → ErrServiceWorkerTokenNotFound。
	RevokeServiceWorkerToken(ctx context.Context, uid, id int64) error
	// ResolveServiceWorkerToken 用 sha256(明文) 换 uid，并刷新 last_used_at。
	ResolveServiceWorkerToken(ctx context.Context, tokenHash string) (int64, error)
}
