package store

import "encoding/json"

// ServiceInstancePresence 是在线 worker 实例的心跳快照（只保留「能不能接单」所需信息）。
// worker 在实例心跳的 metrics 里上报它服务的 slug 清单与本机情况，服务端据此在调用前
// 判断「有没有在线实例能接这个应用」，避免用户对着没开服务的机器白等同步窗口。
type ServiceInstancePresence struct {
	InstanceID string
	Metrics    json.RawMessage
}
