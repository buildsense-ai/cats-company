package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// fakeFriendGraph 是可控的好友关系图（无向）。
type fakeFriendGraph struct {
	mu    sync.Mutex
	pairs map[[2]int64]bool
	err   error
	calls int
}

func newFakeFriendGraph(friendships ...[2]int64) *fakeFriendGraph {
	graph := &fakeFriendGraph{pairs: map[[2]int64]bool{}}
	for _, pair := range friendships {
		graph.pairs[serviceAgentFriendKey(pair[0], pair[1])] = true
	}
	return graph
}

func serviceAgentFriendKey(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

func (f *fakeFriendGraph) AreFriends(a, b int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.pairs[serviceAgentFriendKey(a, b)], nil
}

func (f *fakeFriendGraph) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// 三级可见性：公开 / 好友 / 自用。
//
// 这张表是**唯一**的授权口径 —— 目录、tools/list、调用准入、订阅准入都走它。
// 以前 bot-as-tool 另有一套「bot 的 owner 必须在白名单里」，于是应用标了 public
// 别人调用仍然被拒；两套规则都叫授权，却互不同步。
func TestServiceAgentVisibilityMatrix(t *testing.T) {
	const owner, friend, stranger = int64(441), int64(442), int64(900)

	cases := []struct {
		name       string
		visibility string
		viewerUID  int64
		isFriend   bool
		want       bool
	}{
		// 公开：谁都能看见
		{name: "公开-陌生人", visibility: store.ServiceAgentVisibilityPublic, viewerUID: stranger, want: true},
		{name: "公开-好友", visibility: store.ServiceAgentVisibilityPublic, viewerUID: friend, isFriend: true, want: true},
		{name: "公开-自己", visibility: store.ServiceAgentVisibilityPublic, viewerUID: owner, want: true},

		// 好友：只有发布者的好友能看见
		{name: "好友-陌生人", visibility: store.ServiceAgentVisibilityFriends, viewerUID: stranger, want: false},
		{name: "好友-好友", visibility: store.ServiceAgentVisibilityFriends, viewerUID: friend, isFriend: true, want: true},
		// 自己发布的东西三个级别都该看得到；不需要「自己是自己的好友」这种荒谬条件
		{name: "好友-自己", visibility: store.ServiceAgentVisibilityFriends, viewerUID: owner, want: true},

		// 自用：只有自己
		{name: "自用-陌生人", visibility: store.ServiceAgentVisibilityPrivate, viewerUID: stranger, want: false},
		{name: "自用-好友也不行", visibility: store.ServiceAgentVisibilityPrivate, viewerUID: friend, isFriend: true, want: false},
		{name: "自用-自己", visibility: store.ServiceAgentVisibilityPrivate, viewerUID: owner, want: true},

		// 老数据没有这个字段 → 按公开处理（历史行为，不能因为加了档位把老应用全藏起来）
		{name: "空串按公开", visibility: "", viewerUID: stranger, want: true},

		// 没见过的取值一律不给看：默认放行会让一个手滑的字符串变成公开。
		{name: "未知取值默认拒绝", visibility: "everyone", viewerUID: stranger, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := store.ServiceAgentDescriptor{
				ID: 1, Slug: "demo", OwnerUID: owner, Visibility: tc.visibility,
			}
			got := store.ServiceAgentVisibleTo(agent, store.ServiceAgentViewer{
				UID: tc.viewerUID, FriendOfOwner: tc.isFriend,
			})
			if got != tc.want {
				t.Fatalf("visibility=%q viewer=%d friend=%v → %v, want %v",
					tc.visibility, tc.viewerUID, tc.isFriend, got, tc.want)
			}
		})
	}
}

// 无主条目（owner_uid=0，平台内部发布）：只有公开档对别人可见。
// 这条防的是「0 恰好等于某个未登录 uid」之类的意外放行。
func TestServiceAgentVisibilityIgnoresZeroOwner(t *testing.T) {
	agent := store.ServiceAgentDescriptor{ID: 1, Slug: "internal", OwnerUID: 0, Visibility: store.ServiceAgentVisibilityPrivate}
	if store.ServiceAgentVisibleTo(agent, store.ServiceAgentViewer{UID: 0}) {
		t.Fatal("owner_uid=0 的自用应用不该对 uid=0 放行")
	}
}

// 好友关系只在需要时才查：公开/自用、以及「看自己的应用」都不该产生查询。
// 这不是微优化 —— 目录每次刷新都要过一遍全部应用，每条都查好友关系会变成
// 「页面越用越慢」。
func TestServiceAgentViewerResolverOnlyQueriesFriendsTier(t *testing.T) {
	graph := newFakeFriendGraph()
	resolver := newServiceAgentViewerResolver(graph, 900)

	public := store.ServiceAgentDescriptor{ID: 1, OwnerUID: 441, Visibility: store.ServiceAgentVisibilityPublic}
	private := store.ServiceAgentDescriptor{ID: 2, OwnerUID: 441, Visibility: store.ServiceAgentVisibilityPrivate}
	friendsOwn := store.ServiceAgentDescriptor{ID: 3, OwnerUID: 900, Visibility: store.ServiceAgentVisibilityFriends}

	if !resolver.allows(public) || !resolver.allows(friendsOwn) || resolver.allows(private) {
		t.Fatalf("unexpected decisions: public=%v own-friends=%v private=%v",
			resolver.allows(public), resolver.allows(friendsOwn), resolver.allows(private))
	}
	if got := graph.callCount(); got != 0 {
		t.Fatalf("不该为公开/自用/自己的应用查好友关系，实际查了 %d 次", got)
	}
}

// 同一发布者多个应用：一次请求里只查一次好友关系。
func TestServiceAgentViewerResolverCachesPerOwner(t *testing.T) {
	graph := newFakeFriendGraph([2]int64{442, 441})
	resolver := newServiceAgentViewerResolver(graph, 442)

	for _, id := range []int64{1, 2, 3} {
		agent := store.ServiceAgentDescriptor{ID: id, OwnerUID: 441, Visibility: store.ServiceAgentVisibilityFriends}
		if !resolver.allows(agent) {
			t.Fatalf("好友应该看见发布者 441 的应用 %d", id)
		}
	}
	if got := graph.callCount(); got != 1 {
		t.Fatalf("同一发布者应只查一次好友关系，实际 %d 次", got)
	}
}

// 好友查询失败 → 当作不是好友（fail-closed）。
// 这是访问控制，不是个人聚焦设置：存储抖动不该变成「把别人的应用暴露出去」。
func TestServiceAgentViewerResolverFailsClosedOnError(t *testing.T) {
	graph := newFakeFriendGraph([2]int64{442, 441})
	graph.err = errors.New("friends table unavailable")
	resolver := newServiceAgentViewerResolver(graph, 442)
	agent := store.ServiceAgentDescriptor{ID: 1, OwnerUID: 441, Visibility: store.ServiceAgentVisibilityFriends}
	if resolver.allows(agent) {
		t.Fatal("查不到好友关系时不该放行")
	}
}

// 没有注入好友源（老部署/测试替身）：好友级应用只对发布者自己可见。
// 少接一条线不能等于「所有人可见」。
func TestServiceAgentViewerResolverWithoutReader(t *testing.T) {
	resolver := newServiceAgentViewerResolver(nil, 900)
	friends := store.ServiceAgentDescriptor{ID: 1, OwnerUID: 441, Visibility: store.ServiceAgentVisibilityFriends}
	public := store.ServiceAgentDescriptor{ID: 2, OwnerUID: 441, Visibility: store.ServiceAgentVisibilityPublic}
	if resolver.allows(friends) {
		t.Fatal("没有好友源时不该放行好友级应用")
	}
	if !resolver.allows(public) {
		t.Fatal("公开应用不该受好友源影响")
	}
}

// 发布侧的档位判定：public / friends 都算「分享出去」（要对别人写清用途），
// private 不算；未知取值不合法。
func TestServiceAgentVisibilityHelpers(t *testing.T) {
	if !serviceAgentVisibilityValid(store.ServiceAgentVisibilityPublic) ||
		!serviceAgentVisibilityValid(store.ServiceAgentVisibilityFriends) ||
		!serviceAgentVisibilityValid(store.ServiceAgentVisibilityPrivate) {
		t.Fatal("三个合法档位应被接受")
	}
	if serviceAgentVisibilityValid("everyone") || serviceAgentVisibilityValid("") {
		t.Fatal("未知/空取值不该被当成合法")
	}

	if !serviceAgentVisibilityShared(store.ServiceAgentVisibilityPublic) ||
		!serviceAgentVisibilityShared(store.ServiceAgentVisibilityFriends) {
		t.Fatal("公开与好友都是分享出去")
	}
	if serviceAgentVisibilityShared(store.ServiceAgentVisibilityPrivate) {
		t.Fatal("自用不是分享")
	}

	if got := serviceAgentVisibilityLabel(store.ServiceAgentVisibilityFriends); got != "好友" {
		t.Fatalf("label = %q", got)
	}
}

// accessAwareStore 在 fake 之上补「当前可见性」能力，模拟真实 adapter。
//
// 为什么单独做一个替身而不是给 fake 加方法：现有用例大量依赖「用闭包快照判定」的
// 旧行为（它们传的 descriptor 未必在 agents 列表里）。让基础 fake 仍然不实现这个
// 接口，等于同时钉住「没有这个能力时退回快照」的兼容路径。
type accessAwareStore struct {
	*fakeServiceAgentStore
	ownerUID   int64
	visibility string
	err        error
	calls      int
}

func (s *accessAwareStore) GetServiceAgentAccess(_ context.Context, _ string) (int64, string, error) {
	s.calls++
	if s.err != nil {
		return 0, "", s.err
	}
	return s.ownerUID, s.visibility, nil
}

// 授权必须用**当前**可见性，不能用工具闭包里的会话快照。
//
// 真实场景：好友正在用 catsco_x，发布者把应用改成「自用」。如果 invoke 用快照
// 判定，好友那条已经在线的会话会继续被放行 —— 设置改了、权限没变，用户以为
// 收回了其实没有。这条用例就是钉这个。
func TestServiceAgentInvokeUsesCurrentVisibilityNotSnapshot(t *testing.T) {
	base := newFakeServiceAgentStore()
	base.autoCompleteAfter = 5 * time.Millisecond
	base.agents = []store.ServiceAgentDescriptor{{
		ID: 31, Slug: "shared", Name: "分享的应用", Version: 1,
		AsyncThresholdSeconds: 60,
		OwnerKind:             store.ServiceAgentOwnerUser,
		OwnerUID:              441,
		Visibility:            store.ServiceAgentVisibilityPrivate, // 已经改成自用
	}}

	st := &accessAwareStore{
		fakeServiceAgentStore: base,
		ownerUID:              441,
		visibility:            store.ServiceAgentVisibilityPrivate,
	}
	handler := NewServiceAgentsMCPHandler(st)
	handler.SetFriendReader(newFakeFriendGraph([2]int64{441, 442}))

	// 会话快照（工具注册时捕获的那份）仍然写着 friends —— 好友当时能看到。
	snapshot := store.ServiceAgentDescriptor{
		ID: 31, Slug: "shared", Name: "分享的应用", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
		Visibility: store.ServiceAgentVisibilityFriends,
	}

	res, _, err := handler.invoke(context.Background(), snapshot, 442, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(mcpToolErrorText(res), "agent_not_found") {
		t.Fatalf("改成自用后好友必须立刻失去调用权，实际: %s", mcpToolErrorText(res))
	}

	// 反向：快照写着自用、实际已放开成公开 —— 陌生人应该能调（不能用更严的旧快照卡住）。
	st.visibility = store.ServiceAgentVisibilityPublic
	res, _, err = handler.invoke(context.Background(), store.ServiceAgentDescriptor{
		ID: 31, Slug: "shared", Name: "分享的应用", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
		Visibility: store.ServiceAgentVisibilityPrivate,
	}, 900, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("已放开成公开，陌生人应能调: %s", mcpToolErrorText(res))
	}

	// 归属也用当前值：快照里 owner 是别人，实际是自己的应用 —— 自己当然能调。
	st.ownerUID = 900
	st.visibility = store.ServiceAgentVisibilityPrivate
	res, _, err = handler.invoke(context.Background(), store.ServiceAgentDescriptor{
		ID: 31, Slug: "shared", Name: "分享的应用", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
		Visibility: store.ServiceAgentVisibilityPrivate,
	}, 900, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("按当前归属自己就是发布者，应能调: %s", mcpToolErrorText(res))
	}

	if st.calls < 3 {
		t.Fatalf("每次调用都应读一次当前可见性，实际 %d 次", st.calls)
	}
}

// 读当前可见性失败 → 不猜，按「服务暂不可用」如实报；不能默默用快照放行。
func TestServiceAgentInvokeFailsClosedWhenAccessReadFails(t *testing.T) {
	base := newFakeServiceAgentStore()
	base.agents = []store.ServiceAgentDescriptor{{
		ID: 32, Slug: "shared", Name: "分享的应用", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
		Visibility: store.ServiceAgentVisibilityPublic,
	}}
	st := &accessAwareStore{fakeServiceAgentStore: base, err: errors.New("db down")}
	handler := NewServiceAgentsMCPHandler(st)

	res, _, err := handler.invoke(context.Background(), store.ServiceAgentDescriptor{
		ID: 32, Slug: "shared", Name: "分享的应用", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
		Visibility: store.ServiceAgentVisibilityPublic,
	}, 900, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(mcpToolErrorText(res), "service_unavailable") {
		t.Fatalf("读不到当前可见性时应如实报不可用: %s", mcpToolErrorText(res))
	}
}
