package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 让「自己电脑上的 worker」不用手抄连接密钥（设计 §4.4 补充）。
//
// 问题：之前的流程要求用户先在网页生成 cwk_ 密钥、复制、再拼进命令行。用户既看不懂
// 「凭据」是什么，又会在 shell 历史里留下明文。
//
// 做法：客户端本来就有账号登录态，用它换一张**短期、只能接单**的 worker 券，
// 在进程内直接交给 worker。刻意不做的事：不把账号 JWT 本身交给 worker——
// 账号 JWT 能做该账号的一切，泄露代价远大于只能 claim/complete 自己 run 的券。
const (
	// serviceWorkerSessionType 是券的类型标记；普通 API 的 ParseToken 会拒绝它，
	// 所以这张券只能用在 worker 面（最小权限）。
	serviceWorkerSessionType = "service_worker"
	// serviceWorkerSessionTTL 是券的有效期：worker 会自己续，短一点更安全。
	serviceWorkerSessionTTL     = 12 * time.Hour
	serviceWorkerSessionMaxTTL  = 24 * time.Hour
	serviceWorkerSessionPathEnv = "CATS_SERVICE_WORKER_SESSION_TTL_SECONDS"
)

// ServiceWorkerSessionClaims 是 worker 券的载荷。
type ServiceWorkerSessionClaims struct {
	TokenType string `json:"token_type"`
	UID       int64  `json:"userId"`
	jwt.RegisteredClaims
}

// MintServiceWorkerSession 为 uid 签一张 worker 券。ttl<=0 用默认值，超上限被截断。
func MintServiceWorkerSession(uid int64, ttl time.Duration) (string, time.Time, error) {
	if uid <= 0 {
		return "", time.Time{}, fmt.Errorf("service worker session: invalid uid %d", uid)
	}
	if ttl <= 0 {
		ttl = serviceWorkerSessionTTL
	}
	if ttl > serviceWorkerSessionMaxTTL {
		ttl = serviceWorkerSessionMaxTTL
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(ttl)
	claims := ServiceWorkerSessionClaims{
		TokenType: serviceWorkerSessionType,
		UID:       uid,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        hex.EncodeToString(nonce[:]),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expires),
			Issuer:    "catscompany",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// ParseServiceWorkerSession 校验 worker 券并取出 uid。
// 任何非 worker 类型（含普通用户 JWT、持久登录 JWT）都会被拒绝。
func ParseServiceWorkerSession(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("service worker session: empty token")
	}
	var claims ServiceWorkerSessionClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil {
		return 0, err
	}
	if claims.TokenType != serviceWorkerSessionType {
		return 0, fmt.Errorf("service worker session: unexpected token type %q", claims.TokenType)
	}
	if claims.UID <= 0 {
		return 0, errors.New("service worker session: missing uid")
	}
	return claims.UID, nil
}

// ServiceWorkerSessionHandler 用账号登录态换 worker 券（POST /api/service-worker/session）。
//
// 客户端（桌面端或 CLI）在拉起 worker 前调用一次，把返回的 token 通过环境变量
// 交给 worker；用户界面上完全不出现这一步。
type ServiceWorkerSessionHandler struct {
	allowed func(int64) bool
}

func NewServiceWorkerSessionHandlerFromEnv() *ServiceWorkerSessionHandler {
	return &ServiceWorkerSessionHandler{allowed: serviceCatalogUIDAllowed}
}

// SetAllowed 覆盖内测白名单判定（测试注入）。
func (h *ServiceWorkerSessionHandler) SetAllowed(allowed func(int64) bool) {
	if allowed != nil {
		h.allowed = allowed
	}
}

// HandleCreate POST /api/service-worker/session
func (h *ServiceWorkerSessionHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.allowed != nil && !h.allowed(uid) {
		writeServiceJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	token, expires, err := MintServiceWorkerSession(uid, serviceWorkerSessionTTL)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "session_unavailable"})
		return
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"uid":        uid,
		"expires_at": expires.UTC().Format(time.RFC3339),
		// 提示客户端：这张券只能接单，别当账号 token 用。
		"scope": "service_worker",
	})
}
