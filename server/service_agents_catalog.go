package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 应用目录管理 API（设计 §3.3-9 目录与订阅；网页 cats 的目录/管理界面数据面）。
//
//	GET    /api/service-agents/catalog              全部 active agent + 本账号清单状态
//	GET    /api/service-agents/catalog/access       本账号是否可进入应用目录（内测门）
//	PUT    /api/service-agents/{slug}/subscription  添加（恢复默认可见）
//	DELETE /api/service-agents/{slug}/subscription  删除（屏蔽：tools/list 隐去、调用拒绝）
//
// 语义=模式二（默认开放 + 个人清单）：无行=默认可见；删除写屏蔽行；添加=删行。
// 清单跟账号（uid）；发布权仅内部（本 API 不提供创建/发布）。
//
// 内测门（2026-10-16）：目录管理面暂只对白名单 uid 开放（env
// CATSCO_SERVICE_CATALOG_UID_WHITELIST，逗号分隔；未配置=禁用）。与 relay 用量
// 面板同一模式：非白名单用户静默看不到（access 返回 allowed=false），管理 API 403。
type ServiceAgentCatalogHandler struct {
	store       store.ServiceAgentStore
	notifier    serviceSubscriptionNotifier
	allowedUIDs []int64
}

// serviceSubscriptionNotifier 清单变更后的在线 MCP 会话同步器（MCP handler 实现）。
type serviceSubscriptionNotifier interface {
	NotifySubscriptionChanged(callerUID int64)
}

func NewServiceAgentCatalogHandler(st store.ServiceAgentStore) *ServiceAgentCatalogHandler {
	return &ServiceAgentCatalogHandler{store: st}
}

// NewServiceAgentCatalogHandlerFromEnv 按环境白名单构造（生产接线用）。
func NewServiceAgentCatalogHandlerFromEnv(st store.ServiceAgentStore) *ServiceAgentCatalogHandler {
	h := NewServiceAgentCatalogHandler(st)
	h.allowedUIDs = serviceCatalogAllowedUIDs()
	return h
}

const serviceCatalogWhitelistEnv = "CATSCO_SERVICE_CATALOG_UID_WHITELIST"

// serviceCatalogAllowedUIDs 解析白名单（逗号分隔；未配置/非法项忽略）。
func serviceCatalogAllowedUIDs() []int64 {
	var allowed []int64
	for _, raw := range strings.Split(os.Getenv(serviceCatalogWhitelistEnv), ",") {
		uid, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && uid > 0 {
			allowed = append(allowed, uid)
		}
	}
	return allowed
}

// serviceCatalogUIDAllowed 当前账号是否在 Agent 工具内测白名单内。
func serviceCatalogUIDAllowed(uid int64) bool {
	for _, id := range serviceCatalogAllowedUIDs() {
		if id == uid {
			return true
		}
	}
	return false
}

func (h *ServiceAgentCatalogHandler) allows(uid int64) bool {
	for _, id := range h.allowedUIDs {
		if id == uid {
			return true
		}
	}
	return false
}

// SetSubscriptionNotifier 注入在线会话同步器（未注入=仅数据面生效，在线会话等客户端重查）。
func (h *ServiceAgentCatalogHandler) SetSubscriptionNotifier(n serviceSubscriptionNotifier) {
	h.notifier = n
}

// HandleAccess 汇报本账号是否可进入应用目录（内测门；不解释原因）。
func (h *ServiceAgentCatalogHandler) HandleAccess(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeServiceJSON(w, http.StatusOK, map[string]bool{"allowed": h.allows(uid)})
}

type serviceCatalogEntry struct {
	Slug                  string `json:"slug"`
	Name                  string `json:"name"`
	Version               int64  `json:"version"`
	AsyncThresholdSeconds int    `json:"async_threshold_seconds,omitempty"`
	Hidden                bool   `json:"hidden"`
	// 归属与可见性（设计 P2）：mine=true 表示当前账号是发布者。
	OwnerKind  string `json:"owner_kind,omitempty"`
	Mine       bool   `json:"mine"`
	Visibility string `json:"visibility,omitempty"`
	// Description：调用方 Agent 看到的工具描述（作者写的原文）。
	// 空 = 作者没写，别人（另一个 Agent）只能靠名字猜，基本等于用不起来。
	Description string `json:"description,omitempty"`
	// Definition：完整 definition 原文，只给 owner 本人（编辑时回填 runner/pricing）。
	// 发布是全量覆盖，别人拿不到也用不上这里面的本机命令。
	Definition json.RawMessage `json:"definition,omitempty"`
}

// ensureAllowed 内测门：非白名单账号管理 API 一律 403（静默功能不存在）。
func (h *ServiceAgentCatalogHandler) ensureAllowed(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return 0, false
	}
	if !h.allows(uid) {
		writeServiceJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return 0, false
	}
	return uid, true
}

func (h *ServiceAgentCatalogHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.ensureAllowed(w, r)
	if !ok {
		return
	}
	agents, err := h.store.ListServiceAgents(r.Context())
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "catalog_unavailable"})
		return
	}
	hiddenIDs, err := h.store.ListHiddenServiceAgentIDs(r.Context(), uid)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "catalog_unavailable"})
		return
	}
	hidden := make(map[int64]struct{}, len(hiddenIDs))
	for _, id := range hiddenIDs {
		hidden[id] = struct{}{}
	}
	entries := make([]serviceCatalogEntry, 0, len(agents))
	for _, agent := range agents {
		// 可见性（设计 L2）：private 只进 owner 自己的目录。
		if !store.ServiceAgentVisibleTo(agent, uid) {
			continue
		}
		_, blocked := hidden[agent.ID]
		mine := agent.OwnerUID != 0 && agent.OwnerUID == uid
		entry := serviceCatalogEntry{
			Slug:                  agent.Slug,
			Name:                  agent.Name,
			Version:               agent.Version,
			AsyncThresholdSeconds: agent.AsyncThresholdSeconds,
			Hidden:                blocked,
			OwnerKind:             agent.OwnerKind,
			Mine:                  mine,
			Visibility:            agent.Visibility,
			Description:           agent.Description,
		}
		if mine && strings.TrimSpace(agent.DefinitionJSON) != "" {
			entry.Definition = json.RawMessage(agent.DefinitionJSON)
		}
		entries = append(entries, entry)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"agents": entries})
}

// HandleAdd 添加（恢复默认可见）：PUT /api/service-agents/{slug}/subscription。幂等。
func (h *ServiceAgentCatalogHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	h.setHidden(w, r, false)
}

// HandleRemove 删除（屏蔽）：DELETE /api/service-agents/{slug}/subscription。幂等。
func (h *ServiceAgentCatalogHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	h.setHidden(w, r, true)
}

func (h *ServiceAgentCatalogHandler) setHidden(w http.ResponseWriter, r *http.Request, hidden bool) {
	uid, ok := h.ensureAllowed(w, r)
	if !ok {
		return
	}
	slug := strings.TrimSpace(r.PathValue("slug"))
	if slug == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "slug_required"})
		return
	}
	agentID, _, _, _, err := h.store.GetServiceAgentBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "agent_not_found"})
			return
		}
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
		return
	}
	if err := h.store.SetServiceAgentSubscriptionHidden(r.Context(), uid, agentID, hidden); err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
		return
	}
	if h.notifier != nil {
		// 在线 MCP 会话增量同步（list_changed 通知）；离线会话下次连接即生效。
		h.notifier.NotifySubscriptionChanged(uid)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"slug": slug, "hidden": hidden})
}
