package server

import (
	"github.com/openchat/openchat/server/db/mysql"
	"github.com/openchat/openchat/server/db/postgres"
)

// 装配守卫：两种 adapter 都必须满足 botRunnerStore，否则 NewServiceBotRunner
// 的类型断言会失败、runner 静默变 nil，bot-as-tool 能力悄悄消失 ——
// 线上表现是「分享出去的 bot 工具调用永远没人接」，排查起来毫无线索。
//
// 编译期断言，不跑任何东西。
var (
	_ botRunnerStore = (*postgres.Adapter)(nil)
	_ botRunnerStore = (*mysql.Adapter)(nil)

	// 热更新能力（definition 每次派活重读）。缺了不会崩 —— 会退化成「会话快照」，
	// 也就是改配置要重开 MCP 会话才生效。这个断言保证主 adapter 不会静默退化。
	_ botDefinitionReader = (*postgres.Adapter)(nil)
	_ botDefinitionReader = (*mysql.Adapter)(nil)

	// 好友判定（friends 级可见性）。缺了同样不崩：好友级应用会对**所有人**不可见，
	// 表现为「我明明分享给好友了，他看不到」—— 静默失败最难查，所以编译期钉住。
	_ ServiceAgentFriendReader = (*postgres.Adapter)(nil)
	_ ServiceAgentFriendReader = (*mysql.Adapter)(nil)

	// 调用准入读「当前可见性」（而不是工具闭包里的会话快照）。缺了会退化成
	// 「改成自用后，已在线的调用方还能继续调」—— 用户以为收回了，实际没有。
	_ serviceAgentAccessReader = (*postgres.Adapter)(nil)
	_ serviceAgentAccessReader = (*mysql.Adapter)(nil)
)
