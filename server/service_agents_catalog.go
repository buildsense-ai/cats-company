package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
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
	// friends 是「好友」级可见性所需的判定源；未注入时 friends 级应用
	// 只对发布者自己可见（fail-closed，不会因为少接一条线就变成公开）。
	friends serviceAgentFriendReader
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

// ServiceCatalogAllowedUIDs 返回内测白名单（逗号分隔的 CATSCO_SERVICE_CATALOG_UID_WHITELIST；
// 未配置/非法项忽略）。其他需要「先只在白名单内上线」的能力（例如 bot-as-tool）复用它。
func ServiceCatalogAllowedUIDs() []int64 { return serviceCatalogAllowedUIDs() }

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

// SetFriendReader 注入好友判定源（friends 级可见性用）。
func (h *ServiceAgentCatalogHandler) SetFriendReader(friends serviceAgentFriendReader) {
	h.friends = friends
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
	// State 只有「我的应用」接口（/api/service-agents/mine）会填：active / inactive。
	// 目录接口恒为空 —— 它只返回 active，填了也没意义。
	State string `json:"state,omitempty"`
	// Description：调用方 Agent 看到的工具描述（作者写的原文）。
	// 空 = 作者没写，别人（另一个 Agent）只能靠名字猜，基本等于用不起来。
	Description string `json:"description,omitempty"`
	// OwnerName 是分享者显示名。目录里「谁分享的」是信任的全部线索：
	// 熟人名字直接决定要不要试，陌生作者也能据此判断是不是自己想找的那一类。
	OwnerName string `json:"owner_name,omitempty"`
	// InputSchema 是作者声明的入参 schema（原文）。给所有人看：
	// 调用方需要知道该传什么字段，光看一段散文是猜不出来的。
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	// RunnerKind 是执行方类型（bot / worker / http / 空=平台内置）。
	// 人需要知道「这是另一个 bot 在干」还是「在某人自己电脑上跑」—— 这决定了
	// 它能干什么、要不要等、离线时会不会失败。
	RunnerKind string `json:"runner_kind,omitempty"`
	// Definition：完整 definition 原文，只给 owner 本人（编辑时回填 runner/pricing）。
	// 发布是全量覆盖，别人拿不到也用不上这里面的本机命令。
	Definition json.RawMessage `json:"definition,omitempty"`
	// SelfIntro：应用跑完后「建议」的自我描述，只给 owner 本人。
	// 它不是当前生效的描述（生效的看 description）—— 等人在面板确认。
	SelfIntro *serviceCatalogSelfIntro `json:"self_intro,omitempty"`
}

// serviceCatalogSelfIntro 是待确认的自述建议。
type serviceCatalogSelfIntro struct {
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// serviceAgentRunnerKind 取执行方类型（bot / worker / http / mcp / codex；空=平台内置）。
//
// 只从 definition 读，不额外查库：目录一次要列几十个应用，
// 每个再查一次执行方在线状态会把列表拖慢，而且那个数据随时在变。
func serviceAgentRunnerKind(definition string) string {
	trimmed := strings.TrimSpace(definition)
	if trimmed == "" {
		return ""
	}
	var parsed struct {
		Runner *struct {
			Type string `json:"type"`
		} `json:"runner"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil || parsed.Runner == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Runner.Type))
}

// serviceAgentNameReader 是「批量取用户显示名」的窄能力。
//
// 为什么不在 ListServiceAgents 里 JOIN users：那个查询属于 service_agents 域，
// 让它依赖 users 表会把两个域绑死 —— 已有的集成测试只 seed 最小 schema
// （service_agents + versions），一个 JOIN 就让它们全挂。这里一次批量取回，
// 也没有 N+1。
type serviceAgentNameReader interface {
	GetUsersByIDs(ids []int64) (map[int64]*types.User, error)
}

// serviceAgentOwnerNames 批量取发布者显示名（失败/缺能力都返回空表，不影响目录）。
func serviceAgentOwnerNames(store any, agents []store.ServiceAgentDescriptor) map[int64]string {
	reader, ok := store.(serviceAgentNameReader)
	if !ok {
		return nil
	}
	ids := make([]int64, 0, len(agents))
	seen := make(map[int64]struct{}, len(agents))
	for _, agent := range agents {
		if agent.OwnerUID <= 0 {
			continue
		}
		if _, dup := seen[agent.OwnerUID]; dup {
			continue
		}
		seen[agent.OwnerUID] = struct{}{}
		ids = append(ids, agent.OwnerUID)
	}
	if len(ids) == 0 {
		return nil
	}
	users, err := reader.GetUsersByIDs(ids)
	if err != nil {
		log.Printf("service catalog owner names: %v", err)
		return nil
	}
	names := make(map[int64]string, len(users))
	for uid, user := range users {
		if user == nil {
			continue
		}
		// 显示名优先，没设就用用户名 —— 界面上总要有个能称呼的东西。
		name := strings.TrimSpace(user.DisplayName)
		if name == "" {
			name = strings.TrimSpace(user.Username)
		}
		if name != "" {
			names[uid] = name
		}
	}
	return names
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
	// 发布者显示名：一次批量取回（不在查询里 JOIN users —— 那会让 store 依赖另一个域）。
	ownerNames := serviceAgentOwnerNames(h.store, agents)
	// 可见性按访问者求值（friends 级需要好友关系）：一次请求里同一发布者最多查一次。
	resolver := newServiceAgentViewerResolver(h.friends, uid)
	for _, agent := range agents {
		// 可见性（设计 L2）：自用只进自己的目录，好友级只给发布者的好友。
		if !resolver.allows(agent) {
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
			OwnerName:             ownerNames[agent.OwnerUID],
			RunnerKind:            serviceAgentRunnerKind(agent.DefinitionJSON),
		}
		if schema := strings.TrimSpace(agent.InputSchema); schema != "" {
			entry.InputSchema = json.RawMessage(schema)
		}
		if mine && strings.TrimSpace(agent.DefinitionJSON) != "" {
			entry.Definition = json.RawMessage(agent.DefinitionJSON)
		}
		// 自述建议只给 owner：这是「它建议你怎么介绍自己」，别人看到只会困惑。
		if mine {
			if suggestion := strings.TrimSpace(agent.SelfIntroDescription); suggestion != "" {
				entry.SelfIntro = &serviceCatalogSelfIntro{Description: suggestion}
				if schema := strings.TrimSpace(agent.SelfIntroInputSchema); schema != "" {
					entry.SelfIntro.InputSchema = json.RawMessage(schema)
				}
			}
		}
		entries = append(entries, entry)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"agents": entries})
}

// HandleMine GET /api/service-agents/mine
//
// 自己的全部应用（含已下架）。目录接口（HandleCatalog）只列 active，这是对的：
// 别人的目录不该出现停用应用。但 owner 自己必须还能看到 —— 否则「收回」之后就
// 既不能重新启用、也没法彻底删除，等于失联。这也是面板要显示可操作按钮的前提。
func (h *ServiceAgentCatalogHandler) HandleMine(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.ensureAllowed(w, r)
	if !ok {
		return
	}
	agents, err := h.store.ListServiceAgentsByOwner(r.Context(), uid)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "catalog_unavailable"})
		return
	}
	entries := make([]serviceCatalogEntry, 0, len(agents))
	for _, agent := range agents {
		entry := serviceCatalogEntry{
			Slug:                  agent.Slug,
			Name:                  agent.Name,
			Version:               agent.Version,
			AsyncThresholdSeconds: agent.AsyncThresholdSeconds,
			OwnerKind:             agent.OwnerKind,
			Mine:                  true,
			Visibility:            agent.Visibility,
			State:                 agent.State,
			Description:           agent.Description,
			RunnerKind:            serviceAgentRunnerKind(agent.DefinitionJSON),
		}
		if schema := strings.TrimSpace(agent.InputSchema); schema != "" {
			entry.InputSchema = json.RawMessage(schema)
		}
		if definition := strings.TrimSpace(agent.DefinitionJSON); definition != "" {
			entry.Definition = json.RawMessage(definition)
		}
		if suggestion := strings.TrimSpace(agent.SelfIntroDescription); suggestion != "" {
			entry.SelfIntro = &serviceCatalogSelfIntro{Description: suggestion}
			if schema := strings.TrimSpace(agent.SelfIntroInputSchema); schema != "" {
				entry.SelfIntro.InputSchema = json.RawMessage(schema)
			}
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
	agents, err := h.store.ListServiceAgents(r.Context())
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
		return
	}
	// 看不见的应用一律 404，跟「不存在」同一个回答：能订阅一个自用/别人家好友级应用，
	// 等于让人用「加/删成功与否」探测别人的应用是否存在、叫什么。
	resolver := newServiceAgentViewerResolver(h.friends, uid)
	var target *store.ServiceAgentDescriptor
	for i := range agents {
		if agents[i].Slug != slug {
			continue
		}
		if !resolver.allows(agents[i]) {
			break
		}
		target = &agents[i]
		break
	}
	if target == nil {
		writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "agent_not_found"})
		return
	}
	if err := h.store.SetServiceAgentSubscriptionHidden(r.Context(), uid, target.ID, hidden); err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
		return
	}
	if h.notifier != nil {
		// 在线 MCP 会话增量同步（list_changed 通知）；离线会话下次连接即生效。
		h.notifier.NotifySubscriptionChanged(uid)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"slug": slug, "hidden": hidden})
}

// HandleDismissSelfIntro DELETE /api/service-agents/{slug}/self-intro
//
// 丢掉待确认的自述建议（主人不想采纳）。幂等：没有建议时也返回 200。
//
// 为什么只有「丢弃」没有「采纳」：采纳 = 把这段文字变成对外描述，那是一次**发布**
// ——必须由人带着完整 definition 走 POST /api/service-agents/publish（面板是预填
// 编辑表单，人再点发布）。不开单独的「采纳」写入路径，就不会有第二条改动线上文案的路。
func (h *ServiceAgentCatalogHandler) HandleDismissSelfIntro(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.ensureAllowed(w, r)
	if !ok {
		return
	}
	slug := strings.TrimSpace(r.PathValue("slug"))
	if slug == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "slug_required"})
		return
	}
	selfIntro, ok := h.store.(ServiceAgentSelfIntroStore)
	if !ok {
		writeServiceJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "self_intro_unavailable"})
		return
	}
	// 只有 owner 本人能动：别人的建议与你无关。
	agents, err := h.store.ListServiceAgents(r.Context())
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "catalog_unavailable"})
		return
	}
	for _, agent := range agents {
		if agent.Slug != slug {
			continue
		}
		if agent.OwnerUID == 0 || agent.OwnerUID != uid {
			writeServiceJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
			return
		}
		if err := selfIntro.ClearServiceAgentSelfIntro(r.Context(), slug); err != nil {
			writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
			return
		}
		writeServiceJSON(w, http.StatusOK, map[string]any{"slug": slug, "dismissed": true})
		return
	}
	writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "agent_not_found"})
}
