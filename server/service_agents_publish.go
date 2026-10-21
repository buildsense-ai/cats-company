package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 用户自助上架（设计 §4.3 P2）。
//
// 与内部发布通道（/internal/service-agents/{slug}/publish，实例令牌）的区别：
//   - 鉴权：真人账号 JWT + 内测白名单（与目录接口同一套门禁）；
//   - 归属：首次发布即占位 owner_uid，之后只有本人能继续发版；
//   - 平台内部 slug（owner_kind=internal）用户不可抢占，避免冒充官方应用。
//
// 包体由调用方以 base64 上传，服务端按内容算 sha256 作为 packageHash（内容寻址），
// 与内部通道的版本/会话钉快照机制完全一致。
const (
	serviceAgentPublishMaxBundleEnv = "CATSCO_SERVICE_AGENT_MAX_BUNDLE_BYTES"
	serviceAgentPublishDefaultMax   = 20 << 20 // 20 MiB
	serviceAgentSlugPattern         = `^[a-z0-9][a-z0-9-]{1,63}$`
)

var serviceAgentSlugRe = regexp.MustCompile(serviceAgentSlugPattern)

type ServiceAgentPublishHandler struct {
	store     store.ServiceAgentStore
	allowed   func(int64) bool
	maxBundle int64
	notifier  serviceAgentCatalogNotifier
}

// serviceAgentCatalogNotifier 在目录/可见性变化后同步在线 MCP 会话的工具集（list_changed）。
// 由 ServiceAgentsMCPHandler 实现；未注入时仅影响下次 tools/list（兼容测试）。
type serviceAgentCatalogNotifier interface {
	NotifyCatalogChanged()
}

// SetCatalogNotifier 注入目录变更通知器（线上由 MCP handler 实现）。
func (h *ServiceAgentPublishHandler) SetCatalogNotifier(notifier serviceAgentCatalogNotifier) {
	h.notifier = notifier
}

// NewServiceAgentPublishHandlerFromEnv 构造上架接口（白名单与目录接口共用）。
func NewServiceAgentPublishHandlerFromEnv(st store.ServiceAgentStore) *ServiceAgentPublishHandler {
	maxBundle := int64(serviceAgentPublishDefaultMax)
	if raw := strings.TrimSpace(os.Getenv(serviceAgentPublishMaxBundleEnv)); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			maxBundle = n
		}
	}
	return &ServiceAgentPublishHandler{
		store:     st,
		allowed:   serviceCatalogUIDAllowed,
		maxBundle: maxBundle,
	}
}

// SetAllowed 覆盖白名单判定（测试注入）。
func (h *ServiceAgentPublishHandler) SetAllowed(allowed func(int64) bool) {
	if allowed != nil {
		h.allowed = allowed
	}
}

type serviceAgentPublishRequest struct {
	Slug       string          `json:"slug"`
	Name       string          `json:"name"`
	Visibility string          `json:"visibility,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
	// BundleBase64 是 agent 包原文（base64）；也接受 data URL 前缀。
	BundleBase64 string `json:"bundle_base64,omitempty"`
	Force        bool   `json:"force,omitempty"`
}

// HandlePublish POST /api/service-agents/publish
func (h *ServiceAgentPublishHandler) HandlePublish(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.allowed != nil && !h.allowed(uid) {
		writeServiceJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}

	var req serviceAgentPublishRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBundle*2+4096))
	if err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}

	slug := strings.TrimSpace(req.Slug)
	if !serviceAgentSlugRe.MatchString(slug) {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_slug", "detail": "slug 需匹配 " + serviceAgentSlugPattern,
		})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = slug
	}
	if len([]rune(name)) > 64 {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_name"})
		return
	}
	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		visibility = store.ServiceAgentVisibilityPublic
	}
	if visibility != store.ServiceAgentVisibilityPublic && visibility != store.ServiceAgentVisibilityPrivate {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_visibility", "detail": "visibility 只能是 public / private",
		})
		return
	}
	if len(req.Definition) == 0 || string(req.Definition) == "null" {
		req.Definition = json.RawMessage(`{}`)
	}

	bundle, err := decodeServiceAgentBundle(req.BundleBase64)
	if err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_bundle", "detail": err.Error()})
		return
	}
	// 外部执行后端（runner.type=http/mcp）没有代码包：用确定性占位包占住内容寻址。
	runnerSpec := ParseServiceAgentRunnerSpec(req.Definition)
	if len(bundle) == 0 && runnerSpec != nil && runnerSpec.Type != "" && runnerSpec.Type != "codex" {
		bundle = []byte(fmt.Sprintf(`{"schema":"catsco.service-agent.external.v1","slug":%q,"files":[]}`, slug))
	}
	if int64(len(bundle)) > h.maxBundle {
		writeServiceJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": "bundle_too_large", "limit_bytes": h.maxBundle,
		})
		return
	}
	if len(bundle) == 0 {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "bundle_required"})
		return
	}

	ctx := r.Context()
	if err := h.store.ClaimServiceAgentOwnership(ctx, slug, name, store.ServiceAgentOwnerUser, uid, visibility); err != nil {
		switch {
		case errors.Is(err, store.ErrServiceAgentInternalReserved):
			writeServiceJSON(w, http.StatusConflict, map[string]any{"error": "slug_reserved", "detail": "该 slug 属于平台内部应用"})
		case errors.Is(err, store.ErrServiceAgentOwnedByOther):
			writeServiceJSON(w, http.StatusConflict, map[string]any{"error": "slug_taken", "detail": "该 slug 已被其他账号占用"})
		default:
			writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "publish_unavailable"})
		}
		return
	}

	sum := sha256.Sum256(bundle)
	packageHash := hex.EncodeToString(sum[:])
	version, err := h.store.PublishServiceAgentPackage(ctx, slug, name, req.Definition, packageHash, bundle, req.Force)
	if err != nil {
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			writeServiceJSON(w, http.StatusConflict, map[string]any{"error": "agent_inactive"})
			return
		}
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "publish_unavailable"})
		return
	}

	writeServiceJSON(w, http.StatusOK, map[string]any{
		"slug":         slug,
		"version":      version,
		"package_hash": packageHash,
		"visibility":   visibility,
		"owner_uid":    uid,
	})
}

// HandleUnpublish DELETE /api/service-agents/{slug}
//
// 收回分享：把已上架的应用置为 inactive。效果：
//   - 目录（自己的和别人的）立即不再列出；
//   - 已缓存工具名的 MCP 会话收到 list_changed，工具消失；
//   - 直接调用旧工具名 → agent_not_found（invoke 里做实时状态校验）；
//   - 重新发布同一 slug 即重新上架（ClaimServiceAgentOwnership 会把 state 拉回 active）。
//
// 只有发布者本人能收回（平台 internal 条目不可）。幂等：重复收回返回同样的 200。
func (h *ServiceAgentPublishHandler) HandleUnpublish(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.allowed != nil && !h.allowed(uid) {
		writeServiceJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	slug := strings.TrimSpace(r.PathValue("slug"))
	if !serviceAgentSlugRe.MatchString(slug) {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_slug", "detail": "slug 需匹配 " + serviceAgentSlugPattern,
		})
		return
	}

	err := h.store.SetServiceAgentState(r.Context(), slug, uid, false)
	switch {
	case errors.Is(err, store.ErrServiceAgentNotFound):
		writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "agent_not_found"})
		return
	case errors.Is(err, store.ErrServiceAgentOwnedByOther), errors.Is(err, store.ErrServiceAgentInternalReserved):
		writeServiceJSON(w, http.StatusForbidden, map[string]any{
			"error": "not_owner", "detail": "只有发布者本人能收回该应用",
		})
		return
	case err != nil:
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "unpublish_unavailable"})
		return
	}

	if h.notifier != nil {
		h.notifier.NotifyCatalogChanged()
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"slug": slug, "state": "inactive"})
}

// decodeServiceAgentBundle 解 base64（兼容 data URL 前缀），并做基本大小/格式校验。
func decodeServiceAgentBundle(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if idx := strings.Index(trimmed, ";base64,"); idx >= 0 {
		trimmed = trimmed[idx+len(";base64,"):]
	}
	trimmed = strings.ReplaceAll(trimmed, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, errors.New("bundle_base64 不是合法 base64")
	}
	return decoded, nil
}

// 保持接口可测：允许注入 store（默认走构造参数）。
var _ = context.Background
