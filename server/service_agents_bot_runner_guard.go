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
)
