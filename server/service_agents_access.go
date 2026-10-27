package server

import (
	"encoding/json"
	"log"

	"github.com/openchat/openchat/server/store"
)

// 应用工具的分享范围是三级：公开 / 好友 / 自用。
//
// 为什么判定放在调用面（这里）而不是 store 层：
//  1. 「好友」要按**访问者**求值，纯函数做不到 —— 旧实现只能表达「公开 or 本人」；
//  2. 好友的对方可能是**发布者**，也可能是**这个 bot**（bot 在这个产品里是能加好友的实体），
//     而「这个应用背后是哪个 bot」属于 runner 解析的知识，不属于存储层。
//
// store 只保留常量（适配器写库、发布校验要用）。

// ServiceAgentFriendResolver 是「两个人是不是好友」的窄能力。
// 单独定义而不是塞进 store.ServiceAgentStore：缺这个能力时好友级应用退化成
// 不可见（fail-closed），其余级别照常工作，不会因为一个可选能力缺席就报错。
//
// 导出是因为装配层（cmd/server.go）要把它传进来 —— 参数类型不必可命名，
// 但跨包传接口时导出更好读。
type ServiceAgentFriendResolver interface {
	AreFriends(uid1, uid2 int64) (bool, error)
}

// serviceAgentAccess 是一次请求内使用的可见性判定器。
//
// 好友查询带缓存：一次目录请求里同一个对象（发布者 / bot）只查一次库。
// 缓存的生命周期刻意做成「一次请求」而不是进程级 —— 好友关系是可撤销的，
// 进程级缓存会让「删了好友却还能用」变成真 bug。
type serviceAgentAccess struct {
	viewerUID int64
	resolver  ServiceAgentFriendResolver
	friends   map[int64]bool
}

func newServiceAgentAccess(viewerUID int64, resolver ServiceAgentFriendResolver) *serviceAgentAccess {
	return &serviceAgentAccess{viewerUID: viewerUID, resolver: resolver, friends: map[int64]bool{}}
}

// isFriend 回答「访问者和 uid 是不是好友」。
//
// 查询失败按「不是好友」处理：宁可少显示一个应用，也不要把不该给的人放进来。
// 真正查不动时应当报错，而不是靠放宽权限来「让页面看起来正常」。
func (a *serviceAgentAccess) isFriend(uid int64) bool {
	if uid <= 0 || a.viewerUID <= 0 || a.resolver == nil {
		return false
	}
	if cached, ok := a.friends[uid]; ok {
		return cached
	}
	friends, err := a.resolver.AreFriends(a.viewerUID, uid)
	if err != nil {
		log.Printf("service agent access: check friendship viewer=%d peer=%d: %v", a.viewerUID, uid, err)
		friends = false
	}
	a.friends[uid] = friends
	return friends
}

// Allows 判断 agent 对访问者是否可见可调（目录过滤与调用准入共用这一处规则）。
func (a *serviceAgentAccess) Allows(agent store.ServiceAgentDescriptor) bool {
	// 自己的应用永远可见：自用级别的全部意义就是「只有我看得见」。
	if agent.OwnerUID != 0 && agent.OwnerUID == a.viewerUID {
		return true
	}
	switch agent.Visibility {
	case "", store.ServiceAgentVisibilityPublic:
		// 空值按公开：老数据/未标注都当默认分享出去，与历史行为一致。
		return true
	case store.ServiceAgentVisibilityFriends:
		// 好友这一级取**任一**：发布者是我的好友，或者这个 bot 是我的好友。
		// 两条都算，是因为「好友」在这个产品里同时存在于人和 bot 之间 ——
		// 一个是「我认识作者」，一个是「我一直在用这个 bot」，两者都该算分享对象。
		if a.isFriend(agent.OwnerUID) {
			return true
		}
		if botUID, ok := ParseBotRunnerBotUID(json.RawMessage(agent.DefinitionJSON)); ok {
			return a.isFriend(botUID)
		}
		return false
	default:
		// 未知值一律按自用：一个拼写错误不该把应用变成公开。
		return false
	}
}
