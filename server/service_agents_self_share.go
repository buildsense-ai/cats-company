package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openchat/openchat/server/store"
)

// 自分享（`catsco_share_self`）：让 agent 在聊天里把自己的能力讲清楚并挂成工具。
//
// **为什么要给它，又为什么要收着给**：
// 描述工具的应该是应用自己 —— 只有它知道「用户在什么情况下真该找我」。但「能发布」
// 几乎等于「能对全平台放出任意定义」，所以这里把权限切成三份：
//   - 只能挂**自己的 bot**（bot_uid 取自调用者身份，不接受外部指定）；
//   - 可见性只到 friends / private —— **public 必须由本人到面板点**（那是对全平台放出）；
//   - 随时可 unpublish，且每次动作都记日志 + 在 definition 里留 self_published 痕迹。
//
// 发布通道本身仍走同一条（ClaimServiceAgentOwnership + PublishServiceAgentPackage），
// 不新增第二条写路径。
const (
	serviceShareSelfToolName = "catsco_share_self"
	// 同一个 bot 最多分享成几个应用：防止一次对话里刷出几十个条目。
	maxSelfSharedAgentsPerBot = 10
	// slug 冲突时最多试几个后缀（base-2 … base-6）。
	maxSelfShareSlugAttempts = 5
)

type serviceShareSelfArgs struct {
	Action string `json:"action" jsonschema:"publish 或 unpublish"`
	Name   string `json:"name,omitempty" jsonschema:"应用名（省略则用 slug）"`
	Slug   string `json:"slug,omitempty" jsonschema:"调用名，工具名是 catsco_<slug>；省略则按 name 生成，冲突自动加后缀"`
	// 调用方 Agent 只凭这段文字决定要不要用，所以它是必填（private 除外）。
	Description string `json:"description,omitempty" jsonschema:"能做什么、什么情况下该用（分享给好友时必填）"`
	// 用 map[string]any 而不是 json.RawMessage：SDK 会按 Go 类型推工具 schema，
	// RawMessage 推出来是字符串，客户端传对象就会被参数校验挡掉（线上撞过）。
	InputSchema map[string]any `json:"input_schema,omitempty" jsonschema:"入参 JSON Schema（对象，例如 {type:object,properties:{...}}）"`
	Visibility  string         `json:"visibility,omitempty" jsonschema:"private 或 friends（默认 friends）；public 只能本人到面板发布"`
}

type serviceShareSelfResult struct {
	Action     string `json:"action"`
	Slug       string `json:"slug,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	Version    int64  `json:"version,omitempty"`
	Active     bool   `json:"active"`
}

// addSelfShareTool 只在「调用者本身就是一个 bot、且它的 owner 在内测白名单里」时挂载。
// 人不需要这个工具（面板上有），所以不让它出现在人的工具面里。
func (h *ServiceAgentsMCPHandler) addSelfShareTool(ctx context.Context, srv *mcp.Server, callerUID int64) {
	owner, ok := h.selfShareOwner(callerUID)
	if !ok {
		return
	}
	_ = ctx
	tool := &mcp.Tool{
		Name: serviceShareSelfToolName,
		Description: "把你自己的能力分享成可被调用的 Agent 工具（catsco_<slug>）。" +
			"只在用户明确要求「把你能做的事分享出去 / 让别的 Agent 也能用」时调用；" +
			"分享前必须把 description 写清楚——调用方只凭它判断要不要找你。" +
			"可见性默认只给好友（friends），公开必须由本人到面板确认。" +
			"action=unpublish 可随时收回。owner_uid=" + fmt.Sprint(owner),
	}
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, args serviceShareSelfArgs) (*mcp.CallToolResult, any, error) {
		return h.handleShareSelf(ctx, callerUID, args)
	})
}

// selfShareOwner 判断调用者能否自分享：它是 bot、它的 owner 在白名单里。
func (h *ServiceAgentsMCPHandler) selfShareOwner(callerUID int64) (int64, bool) {
	if h.bots == nil || callerUID <= 0 {
		return 0, false
	}
	owner, err := h.bots.GetBotOwner(callerUID)
	if err != nil || owner <= 0 {
		return 0, false
	}
	if !serviceCatalogUIDAllowed(owner) {
		return 0, false
	}
	return owner, true
}

func (h *ServiceAgentsMCPHandler) handleShareSelf(ctx context.Context, callerUID int64, args serviceShareSelfArgs) (*mcp.CallToolResult, any, error) {
	owner, ok := h.selfShareOwner(callerUID)
	if !ok {
		return serviceToolError("share_self_forbidden: 只有 bot 身份（且账号在内测白名单内）才能自分享；人请在「Agent 工具」面板发布"), nil, nil
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "publish"
	}
	switch action {
	case "publish":
		return h.shareSelfPublish(ctx, callerUID, owner, args)
	case "unpublish":
		return h.shareSelfUnpublish(ctx, callerUID, owner, args)
	default:
		return serviceToolError("invalid_action: action 只能是 publish 或 unpublish"), nil, nil
	}
}

func (h *ServiceAgentsMCPHandler) shareSelfPublish(ctx context.Context, botUID, ownerUID int64, args serviceShareSelfArgs) (*mcp.CallToolResult, any, error) {
	visibility := strings.ToLower(strings.TrimSpace(args.Visibility))
	if visibility == "" {
		visibility = store.ServiceAgentVisibilityFriends
	}
	if visibility == store.ServiceAgentVisibilityPublic {
		// 公开 = 对全平台放出任意定义，只能由本人到面板确认。
		return serviceToolError("share_self_public_needs_owner: 公开分享必须由本人到「Agent 工具」面板点发布；这里只能选 friends 或 private"), nil, nil
	}
	if !serviceAgentVisibilityValid(visibility) {
		return serviceToolError("invalid_visibility: visibility 只能是 private 或 friends"), nil, nil
	}

	description := strings.TrimSpace(args.Description)
	name := strings.TrimSpace(args.Name)
	if name == "" {
		name = strings.TrimSpace(args.Slug)
	}
	if description == "" && visibility != store.ServiceAgentVisibilityPrivate {
		// 分享给好友却没有描述：对方只看到名字，等于不知道能不能用。
		return serviceToolError("share_self_description_required: 分享给好友必须写 description —— 调用你的 Agent 只凭这段文字判断要不要用"), nil, nil
	}
	if problem := validateSelfShareInputSchema(args.InputSchema); problem != "" {
		return serviceToolError(problem), nil, nil
	}

	if n, err := h.countSelfSharedAgents(ctx, ownerUID, botUID); err == nil && n >= maxSelfSharedAgentsPerBot {
		return serviceToolError(fmt.Sprintf(
			"share_self_limit: 这个 bot 已经有 %d 个分享出去的应用（上限 %d）。请先收回不用的（action=unpublish），或在面板里整理",
			n, maxSelfSharedAgentsPerBot)), nil, nil
	}

	base := normalizeSelfShareSlug(args.Slug)
	if base == "" {
		base = normalizeSelfShareSlug(name)
	}
	if base == "" {
		base = fmt.Sprintf("bot-%d", botUID)
	}

	definition, err := buildSelfShareDefinition(name, description, args.InputSchema, botUID)
	if err != nil {
		return serviceToolError("share_self_definition_failed: " + err.Error()), nil, nil
	}
	bundle := json.RawMessage(fmt.Sprintf(`{"schema":"catsco.service-agent.external.v1","slug":%q,"files":[]}`, base))
	sum := sha256.Sum256(bundle)
	packageHash := hex.EncodeToString(sum[:])

	// slug 冲突就加后缀再试：人一多，重名是常态，不该让模型反复猜名字。
	var slug string
	var claimErr error
	for attempt := 0; attempt < maxSelfShareSlugAttempts; attempt++ {
		slug = base
		if attempt > 0 {
			slug = fmt.Sprintf("%s-%d", base, attempt+1)
		}
		claimErr = h.store.ClaimServiceAgentOwnership(ctx, slug, name, store.ServiceAgentOwnerUser, ownerUID, visibility)
		if claimErr == nil {
			break
		}
		if !errors.Is(claimErr, store.ErrServiceAgentOwnedByOther) && !errors.Is(claimErr, store.ErrServiceAgentInternalReserved) {
			return serviceToolError("share_self_failed: " + claimErr.Error()), nil, nil
		}
	}
	if claimErr != nil {
		return serviceToolError(fmt.Sprintf(
			"share_self_slug_taken: 以 %q 开头的好几个调用名都被占用了，请换一个 slug 再试", base)), nil, nil
	}

	version, err := h.store.PublishServiceAgentPackage(ctx, slug, name, definition, packageHash, bundle, false)
	if err != nil {
		return serviceToolError("share_self_publish_failed: " + err.Error()), nil, nil
	}
	// 让在线会话立刻看到新工具（目录变了）。
	h.NotifyCatalogChanged()
	log.Printf("service agent self-share: bot=%d owner=%d slug=%s visibility=%s version=%d", botUID, ownerUID, slug, visibility, version)

	result := serviceShareSelfResult{Action: "publish", Slug: slug, Visibility: visibility, Version: version, Active: true}
	return serviceShareSelfText(result, fmt.Sprintf(
		"已分享为工具 `catsco_%s`（可见性：%s，版本 %d）。调用方会在下一次刷新工具清单时看到它；"+
			"用户随时可以说「收回这个分享」，你会用 action=unpublish 把它下架。",
		slug, visibilityLabel(visibility), version)), result, nil
}

func (h *ServiceAgentsMCPHandler) shareSelfUnpublish(ctx context.Context, botUID, ownerUID int64, args serviceShareSelfArgs) (*mcp.CallToolResult, any, error) {
	slug := normalizeSelfShareSlug(args.Slug)
	if slug == "" {
		slug = normalizeSelfShareSlug(args.Name)
	}
	if slug == "" {
		return serviceToolError("share_self_slug_required: 收回分享需要 slug"), nil, nil
	}
	// 只能收回自己的：先确认这个 slug 确实挂的是当前 bot。
	agents, err := h.store.ListServiceAgentsByOwner(ctx, ownerUID)
	if err != nil {
		return serviceToolError("share_self_failed: " + err.Error()), nil, nil
	}
	var found *store.ServiceAgentDescriptor
	for i := range agents {
		if agents[i].Slug == slug {
			found = &agents[i]
			break
		}
	}
	if found == nil {
		return serviceToolError("share_self_not_found: 你名下没有 slug=" + slug + " 的应用"), nil, nil
	}
	if target, ok := ParseBotRunnerBotUID(json.RawMessage(found.DefinitionJSON)); ok && target != botUID {
		return serviceToolError("share_self_not_owned: 这个应用挂的是另一个 bot，不能由你收回"), nil, nil
	}
	if err := h.store.SetServiceAgentState(ctx, slug, ownerUID, false); err != nil {
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			return serviceToolError("share_self_not_found: 应用不存在或已删除"), nil, nil
		}
		return serviceToolError("share_self_failed: " + err.Error()), nil, nil
	}
	h.NotifyCatalogChanged()
	log.Printf("service agent self-unpublish: bot=%d owner=%d slug=%s", botUID, ownerUID, slug)

	result := serviceShareSelfResult{Action: "unpublish", Slug: slug, Active: false}
	return serviceShareSelfText(result, fmt.Sprintf("已收回 `catsco_%s`：目录与工具清单里都不再出现，重新 publish 即可再上架。", slug)), result, nil
}

// countSelfSharedAgents 数这个 bot 已经被分享成几个应用（不论可见性）。
func (h *ServiceAgentsMCPHandler) countSelfSharedAgents(ctx context.Context, ownerUID, botUID int64) (int, error) {
	agents, err := h.store.ListServiceAgentsByOwner(ctx, ownerUID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, agent := range agents {
		if target, ok := ParseBotRunnerBotUID(json.RawMessage(agent.DefinitionJSON)); ok && target == botUID {
			n++
		}
	}
	return n, nil
}

func buildSelfShareDefinition(name, description string, inputSchema map[string]any, botUID int64) (json.RawMessage, error) {
	definition := map[string]any{
		"name": name,
		"runner": map[string]any{
			"type":    "bot",
			"bot_uid": botUID,
		},
		// 留下「这是它自己分享的」痕迹：出问题时能一眼看出是谁、什么时候放的。
		"self_published": map[string]any{
			"bot_uid": botUID,
			"at":      time.Now().UTC().Format(time.RFC3339),
		},
	}
	if description != "" {
		definition["description"] = description
	}
	if len(inputSchema) > 0 {
		definition["input_schema"] = inputSchema
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// normalizeSelfShareSlug 把任意文本收成一个合法 slug；收不动就返回空。
func normalizeSelfShareSlug(raw string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
		if builder.Len() >= 56 {
			break
		}
	}
	return strings.Trim(builder.String(), "-")
}

func validateSelfShareInputSchema(schema map[string]any) string {
	if len(schema) == 0 {
		return ""
	}
	if t, ok := schema["type"].(string); ok && t != "" && t != "object" {
		return "invalid_input_schema: input_schema.type 只能是 object（工具入参永远是一个对象）"
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return "invalid_input_schema: input_schema 不是合法 JSON"
	}
	if len(raw) > maxServiceAgentInputSchemaBytes {
		return "invalid_input_schema: input_schema 太大（上限 8 KiB）"
	}
	return ""
}

func visibilityLabel(visibility string) string {
	if visibility == store.ServiceAgentVisibilityPrivate {
		return "仅自己"
	}
	return "好友可见"
}

func serviceShareSelfText(result serviceShareSelfResult, text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: result,
	}
}
