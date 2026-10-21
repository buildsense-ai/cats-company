package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 用户自己的 worker 凭据（per-user）。
//
// 背景：原来只有一个全局实例 token（CATS_SERVICE_INSTANCE_TOKEN），拿到就能认领
// 任何人的 run；但 worker 必须跑在用户自己机器上。改成按用户签发后，凭据只能
// 认领「自己名下应用」的 run，泄露也只影响本人。
//
// 明文只在创建响应里出现一次，库里只存 sha256。
type ServiceWorkerCredentialHandler struct {
	store   store.ServiceWorkerTokenStore
	allowed func(int64) bool
}

func NewServiceWorkerCredentialHandlerFromEnv(st store.ServiceWorkerTokenStore) *ServiceWorkerCredentialHandler {
	return &ServiceWorkerCredentialHandler{store: st, allowed: serviceCatalogUIDAllowed}
}

// SetAllowed 覆盖内测白名单判定（测试注入）。
func (h *ServiceWorkerCredentialHandler) SetAllowed(allowed func(int64) bool) {
	if allowed != nil {
		h.allowed = allowed
	}
}

type serviceWorkerCredentialView struct {
	ID         int64  `json:"id"`
	Prefix     string `json:"prefix"`
	Label      string `json:"label"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// HandleList GET /api/service-worker/credentials
func (h *ServiceWorkerCredentialHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.authorize(w, r)
	if !ok {
		return
	}
	tokens, err := h.store.ListServiceWorkerTokens(r.Context(), uid)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "credentials_unavailable"})
		return
	}
	views := make([]serviceWorkerCredentialView, 0, len(tokens))
	for _, token := range tokens {
		view := serviceWorkerCredentialView{
			ID: token.ID, Prefix: token.Prefix, Label: token.Label,
			CreatedAt: token.CreatedAt.UTC().Format(time.RFC3339),
		}
		if token.LastUsedAt != nil {
			view.LastUsedAt = token.LastUsedAt.UTC().Format(time.RFC3339)
		}
		views = append(views, view)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{
		"credentials": views,
		"prefix":      store.ServiceWorkerTokenPrefix,
	})
}

// HandleCreate POST /api/service-worker/credentials
//
// 返回明文（只此一次）。label 可选，仅用于让用户区分多台机器。
func (h *ServiceWorkerCredentialHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
			return
		}
	}
	label := strings.TrimSpace(req.Label)
	if len([]rune(label)) > 64 {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_label"})
		return
	}

	raw, err := newServiceWorkerToken()
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "credentials_unavailable"})
		return
	}
	sum := sha256.Sum256([]byte(raw))
	token, err := h.store.CreateServiceWorkerToken(r.Context(), uid, hex.EncodeToString(sum[:]), displayTokenPrefix(raw), label)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "credentials_unavailable"})
		return
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{
		"id":     token.ID,
		"token":  raw,
		"prefix": token.Prefix,
		"label":  token.Label,
		// 明文只在这里出现，客户端必须提示用户立即保存。
		"shown_once": true,
		"created_at": token.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// HandleRevoke DELETE /api/service-worker/credentials/{id}
func (h *ServiceWorkerCredentialHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_id"})
		return
	}
	err = h.store.RevokeServiceWorkerToken(r.Context(), uid, id)
	if errors.Is(err, store.ErrServiceWorkerTokenNotFound) {
		writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "credential_not_found"})
		return
	}
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "revoke_unavailable"})
		return
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"id": id, "revoked": true})
}

func (h *ServiceWorkerCredentialHandler) authorize(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return 0, false
	}
	if h.allowed != nil && !h.allowed(uid) {
		writeServiceJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return 0, false
	}
	return uid, true
}

// newServiceWorkerToken 生成 cwk_ + 32 字节随机（hex）。
func newServiceWorkerToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return store.ServiceWorkerTokenPrefix + hex.EncodeToString(buf[:]), nil
}

// displayTokenPrefix 给 UI 看的短前缀（cwk_ + 前 8 位），不足以还原明文。
func displayTokenPrefix(raw string) string {
	const keep = len(store.ServiceWorkerTokenPrefix) + 8
	if len(raw) <= keep {
		return raw
	}
	return raw[:keep]
}
