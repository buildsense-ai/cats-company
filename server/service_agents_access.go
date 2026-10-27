package server

import (
	"log"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 应用的三级可见性：公开 / 好友 / 自用。
//
//   - public  谁都能在目录里找到、加进清单、调用；
//   - friends 只有**发布者的好友**能（按看的人求值，所以每次请求都要知道访问者是谁）；
//   - private 只有发布者自己。
//
// 「好友」判定收在这个文件里，因为目录过滤、MCP tools/list、调用准入、订阅准入
// 四处都要回答同一个问题：这个应用对我可见吗。分散实现的下场是有先例的 ——
// bot-as-tool 曾经在 runner 里另写一套「bot 的 owner 必须在白名单里」，
// 结果应用标了 public，别人调用仍然报 bot_not_shared，两套规则互相打架。
//
// 判定原则：
//   - 只有 friends 级、且访问者不是发布者时才查好友关系（其余档位零查询）；
//   - 同一发布者在一次请求里最多查一次（一个发布者可能上架多个应用）；
//   - 查不出来按「不是好友」处理（fail-closed）：这是访问控制，不是个人聚焦设置，
//     存储抖动不该变成「把别人的应用暴露出去」。打一行日志，便于区分
//     「本来就不是好友」和「查挂了」。
//
// ServiceAgentFriendReader 是好友判定源（friends 级可见性用）。
// store.Store（PG/MySQL 适配器）都已实现 AreFriends，装配层直接断言注入即可。
type ServiceAgentFriendReader interface {
	AreFriends(uid1, uid2 int64) (bool, error)
}

// serviceAgentFriendReader 是包内视角的别名：调用点写小写更清楚这是「窄能力」。
type serviceAgentFriendReader = ServiceAgentFriendReader

type serviceAgentViewerResolver struct {
	friends   serviceAgentFriendReader
	viewerUID int64
	// byOwner 缓存「访问者与该发布者是否好友」（请求期，按 owner 去重）。
	byOwner map[int64]bool
}

func newServiceAgentViewerResolver(friends serviceAgentFriendReader, viewerUID int64) *serviceAgentViewerResolver {
	return &serviceAgentViewerResolver{friends: friends, viewerUID: viewerUID}
}

// viewerFor 组出判定该 agent 可见性所需的访问者。
func (r *serviceAgentViewerResolver) viewerFor(agent store.ServiceAgentDescriptor) store.ServiceAgentViewer {
	viewer := store.ServiceAgentViewer{UID: r.viewerUID}
	if r == nil || r.friends == nil {
		return viewer
	}
	if agent.Visibility != store.ServiceAgentVisibilityFriends || agent.OwnerUID == 0 ||
		agent.OwnerUID == r.viewerUID {
		return viewer
	}
	viewer.FriendOfOwner = r.friendOfOwner(agent.OwnerUID)
	return viewer
}

// allows 是「这个应用对我可见吗」的单点判定。
func (r *serviceAgentViewerResolver) allows(agent store.ServiceAgentDescriptor) bool {
	return store.ServiceAgentVisibleTo(agent, r.viewerFor(agent))
}

func (r *serviceAgentViewerResolver) friendOfOwner(ownerUID int64) bool {
	if r.byOwner == nil {
		r.byOwner = map[int64]bool{}
	}
	if cached, ok := r.byOwner[ownerUID]; ok {
		return cached
	}
	friend, err := r.friends.AreFriends(r.viewerUID, ownerUID)
	if err != nil {
		log.Printf("service agents: friends check viewer=%d owner=%d: %v (treated as not friend)", r.viewerUID, ownerUID, err)
		friend = false
	}
	r.byOwner[ownerUID] = friend
	return friend
}

// ---- 发布侧的可见性规则（与上面的「谁能看见」成对，避免两处各写一份字符串比较）----

// serviceAgentVisibilityValid 判断可见性取值是否合法。
func serviceAgentVisibilityValid(visibility string) bool {
	switch visibility {
	case store.ServiceAgentVisibilityPublic,
		store.ServiceAgentVisibilityFriends,
		store.ServiceAgentVisibilityPrivate:
		return true
	default:
		return false
	}
}

// serviceAgentVisibilityShared 判断这个级别是否「分享给别人」。
//
// public 与 friends 都需要写清用途：调用方是另一个 Agent，它看不到包内容、
// 也没有你的对话上下文，description 是它判断「要不要用、怎么传参」的唯一依据。
// private 不强制（只有自己用，写不写都不影响别人）。
func serviceAgentVisibilityShared(visibility string) bool {
	return visibility == store.ServiceAgentVisibilityPublic ||
		visibility == store.ServiceAgentVisibilityFriends
}

// serviceAgentVisibilityLabel 给报错文案用（人话，不是枚举值）。
func serviceAgentVisibilityLabel(visibility string) string {
	switch visibility {
	case store.ServiceAgentVisibilityPublic:
		return "公开"
	case store.ServiceAgentVisibilityFriends:
		return "好友"
	case store.ServiceAgentVisibilityPrivate:
		return "自用"
	default:
		return strings.TrimSpace(visibility)
	}
}

// serviceAgentVisibilityAccepted 是「合法取值」的报错文案片段。
func serviceAgentVisibilityAccepted() string {
	return strings.Join([]string{
		store.ServiceAgentVisibilityPublic,
		store.ServiceAgentVisibilityFriends,
		store.ServiceAgentVisibilityPrivate,
	}, " / ")
}
