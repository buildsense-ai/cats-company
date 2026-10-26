package store

import "encoding/json"

// ServiceInstancePresence 是在线 worker 实例的心跳快照（只保留「能不能接单」所需信息）。
// worker 在实例心跳的 metrics 里上报它服务的 slug 清单与本机情况，服务端据此在调用前
// 判断「有没有在线实例能接这个应用」，避免用户对着没开服务的机器白等同步窗口。
type ServiceInstancePresence struct {
	InstanceID string
	// OwnerUID 是这条 worker 属于哪个账号（心跳鉴权从 cwk_/worker 券解出，不信 payload）。
	// 0 = 平台内部 worker（instance token，没有账号身份）。
	//
	// 没有它就无法按账号判断「你这边有没有 worker」：claim 用的是 per-user 凭据，
	// 别人的 worker 对本应用毫无意义，不过滤就会把「谁来都领不到」的 run 当成在线。
	OwnerUID int64
	Metrics  json.RawMessage
}
