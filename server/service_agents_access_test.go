package server

import (
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 三级分享（公开 / 好友 / 自用）的判定规则。
//
// 这一层是目录过滤和调用准入的**唯一**一处规则：以前两边各判一次、
// bot runner 还另有一套 owner 白名单，三处迟早会说出不同的话。

// stubFriendship 是按 peer uid 回答的假好友表，顺带记下查询次数（缓存断言用）。
type stubFriendship struct {
	friends map[int64]bool
	err     error
	calls   []int64
}

func (s *stubFriendship) AreFriends(_, uid2 int64) (bool, error) {
	s.calls = append(s.calls, uid2)
	if s.err != nil {
		return false, s.err
	}
	return s.friends[uid2], nil
}

func accessAgent(visibility string, ownerUID int64, definition string) store.ServiceAgentDescriptor {
	return store.ServiceAgentDescriptor{
		ID: 7, Slug: "shared-app", Name: "分享的应用",
		OwnerUID: ownerUID, Visibility: visibility, DefinitionJSON: definition,
	}
}

const botRunnerDefinition = `{"runner":{"type":"bot","bot_uid":982}}`

func TestServiceAgentAccessAllows(t *testing.T) {
	const (
		viewer  = int64(38)
		owner   = int64(700)
		botUID  = int64(982)
		strange = int64(555)
	)
	cases := []struct {
		name        string
		agent       store.ServiceAgentDescriptor
		friends     map[int64]bool
		resolverErr error
		want        bool
	}{
		{
			name:  "公开：陌生人也能用",
			agent: accessAgent(store.ServiceAgentVisibilityPublic, owner, botRunnerDefinition),
			want:  true,
		},
		{
			name:  "空值按公开（老数据）",
			agent: accessAgent("", owner, botRunnerDefinition),
			want:  true,
		},
		{
			name:  "自用：陌生人不行",
			agent: accessAgent(store.ServiceAgentVisibilityPrivate, owner, botRunnerDefinition),
			want:  false,
		},
		{
			name:    "自用：连发布者的好友也不行（自用就是自用）",
			agent:   accessAgent(store.ServiceAgentVisibilityPrivate, owner, botRunnerDefinition),
			friends: map[int64]bool{owner: true, botUID: true},
			want:    false,
		},
		{
			name:  "自用：本人永远可见",
			agent: accessAgent(store.ServiceAgentVisibilityPrivate, viewer, botRunnerDefinition),
			want:  true,
		},
		{
			name:  "好友：陌生人不可以",
			agent: accessAgent(store.ServiceAgentVisibilityFriends, owner, botRunnerDefinition),
			want:  false,
		},
		{
			name:    "好友：发布者是我的好友 → 可以",
			agent:   accessAgent(store.ServiceAgentVisibilityFriends, owner, botRunnerDefinition),
			friends: map[int64]bool{owner: true},
			want:    true,
		},
		{
			name:    "好友：这个 bot 是我的好友 → 也可以（任一成立）",
			agent:   accessAgent(store.ServiceAgentVisibilityFriends, owner, botRunnerDefinition),
			friends: map[int64]bool{botUID: true},
			want:    true,
		},
		{
			name:    "好友：worker 型应用没有 bot，只能靠发布者这层",
			agent:   accessAgent(store.ServiceAgentVisibilityFriends, owner, `{"runner":{"type":"worker"}}`),
			friends: map[int64]bool{botUID: true},
			want:    false,
		},
		{
			name:  "好友：本人永远可见",
			agent: accessAgent(store.ServiceAgentVisibilityFriends, viewer, botRunnerDefinition),
			want:  true,
		},
		{
			name:    "未知可见性值一律按自用（拼错不能变成公开）",
			agent:   accessAgent("friend", owner, botRunnerDefinition),
			friends: map[int64]bool{owner: true},
			want:    false,
		},
		{
			// 查不动好友关系时宁可少显示一个应用，也不能把不该给的人放进来。
			name:        "好友查询失败 → 不放行",
			agent:       accessAgent(store.ServiceAgentVisibilityFriends, owner, botRunnerDefinition),
			resolverErr: errors.New("db down"),
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &stubFriendship{friends: tc.friends, err: tc.resolverErr}
			access := newServiceAgentAccess(viewer, resolver)
			if got := access.Allows(tc.agent); got != tc.want {
				t.Fatalf("Allows = %v, want %v", got, tc.want)
			}
		})
	}
}

// 没有注入好友能力时，好友级应用退化为不可见 —— 公开与自用不受影响。
func TestServiceAgentAccessWithoutResolver(t *testing.T) {
	access := newServiceAgentAccess(38, nil)
	if access.Allows(accessAgent(store.ServiceAgentVisibilityFriends, 700, botRunnerDefinition)) {
		t.Fatal("缺好友能力时好友级应用不该可见")
	}
	if !access.Allows(accessAgent(store.ServiceAgentVisibilityPublic, 700, botRunnerDefinition)) {
		t.Fatal("公开应用不该依赖好友能力")
	}
}

// 请求内缓存：同一个对象（发布者 / bot）只查一次库。
// 目录一次要过 N 个应用，不缓存就是 N 次往返。
func TestServiceAgentAccessCachesFriendship(t *testing.T) {
	resolver := &stubFriendship{friends: map[int64]bool{700: true}}
	access := newServiceAgentAccess(38, resolver)

	a := accessAgent(store.ServiceAgentVisibilityFriends, 700, botRunnerDefinition)
	b := accessAgent(store.ServiceAgentVisibilityFriends, 700, botRunnerDefinition)
	b.ID = 8
	access.Allows(a)
	access.Allows(b)

	if len(resolver.calls) != 1 {
		t.Fatalf("好友查询 %d 次，应该复用缓存（1 次）: %v", len(resolver.calls), resolver.calls)
	}
}

// 缓存是「一次请求」而不是进程级：好友关系可撤销，进程级缓存会让
// 「删了好友却还能用」变成真 bug。
func TestServiceAgentAccessCacheIsPerRequest(t *testing.T) {
	resolver := &stubFriendship{friends: map[int64]bool{700: true}}
	agent := accessAgent(store.ServiceAgentVisibilityFriends, 700, botRunnerDefinition)

	if !newServiceAgentAccess(38, resolver).Allows(agent) {
		t.Fatal("第一次应该可见")
	}
	delete(resolver.friends, 700) // 相当于刚被删好友
	if newServiceAgentAccess(38, resolver).Allows(agent) {
		t.Fatal("新请求必须重新查好友关系")
	}
}
