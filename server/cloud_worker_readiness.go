package server

import "strings"

// cloudWorkerRuntimeStatus reports observed runtime connectivity, not a model
// inference/SLA guarantee. The provider's running flag and cached device rows
// alone cannot prove that this owner's hosted Bot can receive work.
func (h *CloudWorkerHandler) cloudWorkerRuntimeStatus(ownerUID, botUID int64) string {
	if h == nil || h.bots == nil || h.bots.hub == nil || h.bots.hub.userDevices == nil {
		return "unknown"
	}
	hub := h.bots.hub
	devices, _ := hub.classifyUserDevices(ownerUID, hub.userDevices.activeDevices(ownerUID))
	for _, device := range devices {
		if device.BotUID != botUID || device.RuntimeRole != "server" || !device.Routable {
			continue
		}
		// Model preparation precedes the normal XiaoBa connection, but do not
		// treat an old/incomplete registration as a configured runtime.
		if device.ModelStatus != nil && strings.TrimSpace(device.ModelStatus.Model) != "" {
			return "connected"
		}
	}
	return "not_connected"
}
