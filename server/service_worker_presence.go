package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 「本机 worker 是否在线」预检（体验修复）。
//
// 真实场景：用户在面板发布应用、拿到启动命令却没跑（或关机了），然后在 bot 里调用该工具。
// 之前的行为是：run 建好、等满同步窗口（45s）后返回 running，run 卡在 submitted 直到超时，
// 用户完全不知道「自己的电脑上根本没东西在接单」。
//
// 现在：worker 心跳里带上它服务的 slug 清单，调用前先看一眼有没有在线实例能接这个应用；
// 没有就立刻返回可读错误，不再让用户干等。
const (
	// 心跳间隔默认 20s，窗口取 3 倍留出抖动余量。
	serviceWorkerOnlineWindow = 90 * time.Second
	// 心跳里承载 slug 清单的字段名（worker 与服务端共用）。
	serviceInstanceAgentsMetric = "agents"
	// worker 没限定 -agents 时写 true，表示「所有应用都接」。
	serviceInstanceServeAllMetric = "serve_all"
)

// ServiceWorkerPresence 描述当前在线 worker 能接哪些应用。
type ServiceWorkerPresence struct {
	// Online 在线实例总数（窗口内有过心跳）。
	Online int
	// ServeAll 是否有实例声明「所有应用都接」。
	ServeAll bool
	// Agents 实例显式声明服务的 slug 集合。
	Agents map[string]struct{}
}

// Serves 判断是否有在线 worker 能接该 slug。
func (p ServiceWorkerPresence) Serves(slug string) bool {
	if p.Online == 0 {
		return false
	}
	if p.ServeAll {
		return true
	}
	_, ok := p.Agents[slug]
	return ok
}

// serviceWorkerPresenceStore 是读取在线实例的能力（主库实现；未实现=跳过预检）。
type serviceWorkerPresenceStore interface {
	ListOnlineServiceInstances(ctx context.Context, window time.Duration) ([]store.ServiceInstancePresence, error)
}

// ServiceWorkerOfflineError 表示没有在线 worker 能接这个应用。
type ServiceWorkerOfflineError struct {
	Slug      string
	NoWorkers bool // true=一个在线 worker 都没有；false=有 worker 但不服务这个应用
}

func (e *ServiceWorkerOfflineError) Error() string {
	if e.NoWorkers {
		return "发布者这边没有任何 worker 在线"
	}
	return "发布者这边虽然有人在线，但没有一个在服务这个应用"
}

// serviceWorkerOfflineToolError 是告诉调用方该怎么救的那句话（MCP tool 错误文本）。
//
// 原则：**说清是谁的问题、该去哪儿动手**。调用方是另一个 Agent，它看不到用户机器，
// 所以必须指出「这是发布者那一侧的事，不是你的参数错了」，否则它会反复重试或改参数。
//
// 桌面端已把 worker 内嵌进去，用户能做的最简单一步就是「在那台机器上登录小八」；
// 千万不要再写「命令见面板」——面板已经不展示任何启动命令，用户会找不到。
// 也不要说「改成平台直连 MCP/HTTP」——那个选项已经从面板上撤掉了（要公网可达地址，
// 且平台反向连你时对端得自己兜鉴权，普通用户用不上）。
//
// 也不能写「你的电脑」：同一个 agent 完全可能跑在用户自己的云服务器上（云员工）。
func serviceWorkerOfflineToolError(offline *ServiceWorkerOfflineError) string {
	return fmt.Sprintf(
		"worker_offline: %s。这是发布者那一侧的执行环境没就绪，与调用参数无关——重试或改成别的参数都不会变好，请把这条原因原样转告用户："+
			"在要跑这个应用的那台机器上（电脑或云服务器）启动小八并登录，它就会主动接单（不需要公网 IP，也不用开任何端口）。",
		offline.Error(),
	)
}

// workerPresence 汇总在线实例的服务范围。
//
// 兼容旧版 worker：它们的实例心跳里没有 agents/serve_all 字段，平台无从判断服务范围。
// 这种情况按「未知 = 可能服务任何应用」处理（ServeAll=true），否则升级平台会把还在正常
// 干活的旧 worker 全判成离线，把可用功能变成报错。
func workerPresence(instances []store.ServiceInstancePresence) ServiceWorkerPresence {
	presence := ServiceWorkerPresence{Agents: map[string]struct{}{}}
	for _, instance := range instances {
		presence.Online++
		if len(instance.Metrics) == 0 {
			// 连 metrics 都没有：老实现或写入失败，按未知放行。
			presence.ServeAll = true
			continue
		}
		var metrics map[string]json.RawMessage
		if err := json.Unmarshal(instance.Metrics, &metrics); err != nil {
			presence.ServeAll = true
			continue
		}
		agentsRaw, hasAgents := metrics[serviceInstanceAgentsMetric]
		serveAllRaw, hasServeAll := metrics[serviceInstanceServeAllMetric]
		if !hasAgents && !hasServeAll {
			// 旧版 worker：只报 os/arch/pid。范围未知 → 不拦。
			presence.ServeAll = true
			continue
		}
		if hasServeAll {
			var serveAll bool
			if json.Unmarshal(serveAllRaw, &serveAll) == nil && serveAll {
				presence.ServeAll = true
			}
		}
		if hasAgents {
			var agents []string
			if json.Unmarshal(agentsRaw, &agents) == nil {
				for _, slug := range agents {
					slug = strings.TrimSpace(slug)
					if slug != "" {
						presence.Agents[slug] = struct{}{}
					}
				}
			}
		}
	}
	return presence
}

// isWorkerRunner 判断该应用是否由「用户自己电脑上的 worker」执行。
// 读不到定义（版本被清理等）时按非 worker 处理：预检只是体验优化，不能因此挡住调用。
func (h *ServiceAgentsMCPHandler) isWorkerRunner(ctx context.Context, agent store.ServiceAgentDescriptor) bool {
	_, _, definitionJSON, err := h.store.GetServiceAgentDefinition(ctx, agent.ID, agent.Version)
	if err != nil {
		return false
	}
	spec := ParseServiceAgentRunnerSpec(definitionJSON)
	return spec != nil && strings.EqualFold(spec.Type, "worker")
}

// checkWorkerOnline 在创建 run 之前确认有在线 worker 能接该应用。
//
// 只算「发布者自己的」worker：claim 跑的是 per-user 凭据（只能领自己名下应用的 run），
// 别人的 worker 就算在线也接不到这一单。不过滤的后果很具体——平台上任意一个人开着
// 小八，其它人的应用都会被判成「在线」，于是 run 停在 submitted 永远没人领，
// 调用方也等不到 worker_offline（2026-10-26 生产实测）。
//
// 明确 fail-open 的两种情况：存储没实现这个能力、或查询出错 —— 宁可让用户在同步窗口里
// 等一下，也不能因为监控数据抖动把正常调用挡掉。
func (h *ServiceAgentsMCPHandler) checkWorkerOnline(ctx context.Context, agent store.ServiceAgentDescriptor) error {
	presenceStore, ok := h.store.(serviceWorkerPresenceStore)
	if !ok {
		return nil
	}
	instances, err := presenceStore.ListOnlineServiceInstances(ctx, serviceWorkerOnlineWindow)
	if err != nil {
		return nil
	}
	presence := workerPresence(instancesForOwner(instances, agent.OwnerUID))
	if presence.Serves(agent.Slug) {
		return nil
	}
	return &ServiceWorkerOfflineError{Slug: agent.Slug, NoWorkers: presence.Online == 0}
}

// instancesForOwner 只保留该 owner 名下的 worker（0=平台内部）。
//
// 升级兼容：owner_uid 是后加的列，历史行动是 0，会被当成平台内部 worker。
// 这个方向的误判是安全的（用户的 worker 下次心跳就会带回真实 owner），
// 反过来把「别人的 worker」当成「你的」会让离线预检彻底失效。
func instancesForOwner(instances []store.ServiceInstancePresence, ownerUID int64) []store.ServiceInstancePresence {
	if ownerUID < 0 {
		ownerUID = 0
	}
	filtered := make([]store.ServiceInstancePresence, 0, len(instances))
	for _, instance := range instances {
		if instance.OwnerUID == ownerUID {
			filtered = append(filtered, instance)
		}
	}
	return filtered
}
