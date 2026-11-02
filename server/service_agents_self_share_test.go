package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openchat/openchat/server/store"
)

// —— 自分享（catsco_share_self）——
//
// 权限切成三份：只能挂自己的 bot；可见性只到 friends/private；随时可收回。
// 这几条都是「模型能自己决定」与「人能决定」的分界线，不能靠提示词，必须靠代码。

func newSelfShareHandler(t *testing.T, botUID, ownerUID int64) (*ServiceAgentsMCPHandler, *fakeServiceAgentStore) {
	t.Helper()
	st := newFakeServiceAgentStore()
	h := NewServiceAgentsMCPHandler(st)
	identity := newFakeBotIdentity(botUID, ownerUID, nil)
	h.SetBotIdentity(identity)
	return h, st
}

func shareText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	t.Fatal("工具结果里没有文本")
	return ""
}

func TestSelfShareToolOnlyForWhitelistedBots(t *testing.T) {
	const (
		botUID   int64 = 982
		ownerUID int64 = 38
	)
	h, _ := newSelfShareHandler(t, botUID, ownerUID)

	// 人（不是 bot）：没有这个工具。
	if h.canSelfShare(ownerUID) {
		t.Fatal("主人的账号不该拿到自分享工具")
	}
	// bot 在，但 owner 不在白名单：也不给。
	t.Setenv(serviceCatalogWhitelistEnv, "")
	if h.canSelfShare(botUID) {
		t.Fatal("owner 不在白名单时不该拿到自分享工具")
	}
	// 白名单命中：给。
	t.Setenv(serviceCatalogWhitelistEnv, "38")
	if !h.canSelfShare(botUID) {
		t.Fatal("白名单账号的 bot 应该拿到自分享工具")
	}

	// 即使工具没挂出来，直接调用也必须被拒（纵深防御）。
	_, _, _ = h.handleShareSelf(context.Background(), ownerUID, serviceShareSelfArgs{Action: "publish"})
	result, _, err := h.handleShareSelf(context.Background(), ownerUID, serviceShareSelfArgs{Action: "publish"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(shareText(t, result), "share_self_forbidden") {
		t.Fatalf("人的调用应被拒绝，got %q", shareText(t, result))
	}
}

func TestSelfSharePublishCreatesBotAgent(t *testing.T) {
	const (
		botUID   int64 = 982
		ownerUID int64 = 38
	)
	t.Setenv(serviceCatalogWhitelistEnv, "38")
	h, st := newSelfShareHandler(t, botUID, ownerUID)

	result, structured, err := h.handleShareSelf(context.Background(), botUID, serviceShareSelfArgs{
		Action:      "publish",
		Name:        "杰克运维",
		Slug:        "jack-ops",
		Description: "把服务器排障的活派给杰克，用户问「为什么 502」时用。",
		Visibility:  "friends",
	})
	if err != nil {
		t.Fatalf("publish err: %v", err)
	}
	view, ok := structured.(serviceShareSelfResult)
	if !ok || view.Slug != "jack-ops" || view.Visibility != store.ServiceAgentVisibilityFriends || !view.Active {
		t.Fatalf("structured = %+v", structured)
	}
	if !strings.Contains(shareText(t, result), "catsco_jack-ops") {
		t.Fatalf("text = %q", shareText(t, result))
	}

	agents, err := st.ListServiceAgentsByOwner(context.Background(), ownerUID)
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents = %d err=%v", len(agents), err)
	}
	if agents[0].Slug != "jack-ops" || agents[0].Name != "杰克运维" {
		t.Fatalf("agent = %+v", agents[0])
	}
}

// definition 是这次发布的真正内容，单独钉住：它决定「谁在干活、凭什么能被调用」。
func TestSelfShareDefinitionShape(t *testing.T) {
	raw, err := buildSelfShareDefinition("杰克运维", "把服务器排障的活派给杰克。", map[string]any{"type": "object"}, 982)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var definition map[string]any
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	runner, _ := definition["runner"].(map[string]any)
	if runner["type"] != "bot" || runner["bot_uid"].(float64) != 982 {
		t.Fatalf("runner = %v，必须挂在自己的 bot 上", runner)
	}
	if definition["description"] != "把服务器排障的活派给杰克。" {
		t.Fatalf("description = %v", definition["description"])
	}
	if _, ok := definition["input_schema"]; !ok {
		t.Fatal("input_schema 必须原样带进去（调用方靠它知道怎么传参）")
	}
	selfPublished, ok := definition["self_published"].(map[string]any)
	if !ok || selfPublished["bot_uid"].(float64) != 982 || selfPublished["at"] == "" {
		t.Fatalf("self_published = %v，要留「谁、什么时候放的」", definition["self_published"])
	}
}

func TestSelfShareRejectsPublicAndMissingDescription(t *testing.T) {
	const (
		botUID   int64 = 982
		ownerUID int64 = 38
	)
	t.Setenv(serviceCatalogWhitelistEnv, "38")
	h, _ := newSelfShareHandler(t, botUID, ownerUID)

	cases := []struct {
		name string
		args serviceShareSelfArgs
		want string
	}{
		{
			name: "public 必须人来点",
			args: serviceShareSelfArgs{Action: "publish", Name: "x", Description: "d", Visibility: "public"},
			want: "share_self_public_needs_owner",
		},
		{
			name: "friends 必须写描述",
			args: serviceShareSelfArgs{Action: "publish", Name: "x", Visibility: "friends"},
			want: "share_self_description_required",
		},
		{
			name: "非法可见性",
			args: serviceShareSelfArgs{Action: "publish", Name: "x", Description: "d", Visibility: "secret"},
			want: "invalid_visibility",
		},
		{
			name: "非法 action",
			args: serviceShareSelfArgs{Action: "rename"},
			want: "invalid_action",
		},
		{
			name: "input_schema 不是对象",
			args: serviceShareSelfArgs{Action: "publish", Name: "x", Description: "d", Visibility: "private", InputSchema: map[string]any{"type": "array"}},
			want: "invalid_input_schema",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := h.handleShareSelf(context.Background(), botUID, tc.args)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(shareText(t, result), tc.want) {
				t.Fatalf("result = %q, want %s", shareText(t, result), tc.want)
			}
		})
	}

	// private 不要求描述（只有自己用）。
	result, structured, _ := h.handleShareSelf(context.Background(), botUID, serviceShareSelfArgs{
		Action: "publish", Name: "只有我自己", Visibility: "private"})
	if view, ok := structured.(serviceShareSelfResult); !ok || view.Visibility != store.ServiceAgentVisibilityPrivate {
		t.Fatalf("private 应该允许无描述上架，got %q / %+v", shareText(t, result), structured)
	}
}

func TestSelfShareSlugCollisionAddsSuffix(t *testing.T) {
	const (
		botUID   int64 = 982
		ownerUID int64 = 38
	)
	t.Setenv(serviceCatalogWhitelistEnv, "38")
	h, st := newSelfShareHandler(t, botUID, ownerUID)

	// 别人已经占了 jack-ops 这个名字。
	st.agents = append(st.agents, store.ServiceAgentDescriptor{
		Slug: "jack-ops", Name: "别人的", OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
		DefinitionJSON: `{"runner":{"type":"bot","bot_uid":1619}}`,
	})

	_, structured, _ := h.handleShareSelf(context.Background(), botUID, serviceShareSelfArgs{
		Action: "publish", Name: "杰克运维", Slug: "jack-ops", Description: "d", Visibility: "friends"})
	view, ok := structured.(serviceShareSelfResult)
	if !ok || view.Slug != "jack-ops-2" {
		t.Fatalf("slug = %+v，冲突时应自动加后缀（人一多，重名是常态）", structured)
	}
}

func TestSelfShareUnpublishOnlyOwnBot(t *testing.T) {
	const (
		botUID   int64 = 982
		ownerUID int64 = 38
	)
	t.Setenv(serviceCatalogWhitelistEnv, "38")
	h, st := newSelfShareHandler(t, botUID, ownerUID)

	st.agents = append(st.agents,
		store.ServiceAgentDescriptor{Slug: "mine", Name: "我的", OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: ownerUID,
			DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`},
		store.ServiceAgentDescriptor{Slug: "other-bot", Name: "别人的 bot", OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: ownerUID,
			DefinitionJSON: `{"runner":{"type":"bot","bot_uid":1619}}`},
	)

	// 同主人、但挂的是另一个 bot：不能替你收。
	result, _, _ := h.handleShareSelf(context.Background(), botUID, serviceShareSelfArgs{Action: "unpublish", Slug: "other-bot"})
	if !strings.Contains(shareText(t, result), "share_self_not_owned") {
		t.Fatalf("result = %q", shareText(t, result))
	}

	// 自己挂的：可以收。
	unpublished, structured, _ := h.handleShareSelf(context.Background(), botUID, serviceShareSelfArgs{Action: "unpublish", Slug: "mine"})
	view, ok := structured.(serviceShareSelfResult)
	if !ok || view.Active || view.Action != "unpublish" {
		t.Fatalf("structured = %+v (%q)", structured, shareText(t, unpublished))
	}
	if !st.inactive["mine"] {
		t.Fatal("收回后必须落 inactive（目录与工具清单都不再出现）")
	}
}

func TestSelfShareLimitPerBot(t *testing.T) {
	const (
		botUID   int64 = 982
		ownerUID int64 = 38
	)
	t.Setenv(serviceCatalogWhitelistEnv, "38")
	h, st := newSelfShareHandler(t, botUID, ownerUID)

	for i := 0; i < maxSelfSharedAgentsPerBot; i++ {
		st.agents = append(st.agents, store.ServiceAgentDescriptor{
			Slug: "shared-" + string(rune('a'+i)), Name: "x",
			OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: ownerUID,
			DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`,
		})
	}
	result, _, _ := h.handleShareSelf(context.Background(), botUID, serviceShareSelfArgs{
		Action: "publish", Name: "第 N 个", Description: "d", Visibility: "friends"})
	if !strings.Contains(shareText(t, result), "share_self_limit") {
		t.Fatalf("result = %q，超过上限应拒绝", shareText(t, result))
	}
}
