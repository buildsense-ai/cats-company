package server

import "time"

// Server-to-server routing data. It is carried only in the shared-runtime
// envelope, never in the Connector protocol or a client-supplied RPC field.
type thinToolRPCRelay struct {
	RequesterRoute runtimeRoute `json:"requester_route"`
	ResultSource   runtimeRoute `json:"result_source"`
	TargetOwnerUID int64        `json:"owner_uid"`
	TargetDeviceID string       `json:"device_id"`
	ToolName       string       `json:"tool_name"`
	CreatedAt      time.Time    `json:"created_at"`
	ExpiresAt      time.Time    `json:"expires_at"`
}

func (h *Hub) receiveThinToolRPCFromRuntime(route runtimeRoute, msg *MsgThinToolRPC, relay *thinToolRPCRelay) bool {
	if h == nil || msg == nil || route.NodeID != h.nodeID || !h.routeConnected(route) {
		return false
	}
	if relay == nil {
		return h.sendThinToolRPCToLocalRoute(route, msg)
	}
	if msg.Type == thinToolRPCTypeRequest {
		current, _ := h.findDeviceRPCTarget(relay.TargetOwnerUID, UserDevice{DeviceID: relay.TargetDeviceID})
		if !current.matches(route) || !h.routeConnected(relay.RequesterRoute) || !relay.ExpiresAt.After(nowForRoute(h)) {
			return false
		}
		pending := thinToolRPCPending{requestID: msg.RequestID, requesterRoute: relay.RequesterRoute, targetRoute: route, targetOwnerUID: relay.TargetOwnerUID, targetDeviceID: relay.TargetDeviceID, toolName: relay.ToolName, createdAt: relay.CreatedAt, expiresAt: relay.ExpiresAt}
		if !h.thinToolRPC.add(pending) {
			return false
		}
		if !h.sendThinToolRPCToLocalRoute(route, msg) {
			h.thinToolRPC.finishMatching(pending)
			return false
		}
		return true
	}
	if msg.Type != thinToolRPCTypeResult {
		return false
	}
	pending, ok := h.thinToolRPC.get(msg.RequestID)
	// The device node authenticated the Connector and its exact connection.
	// Independently bind that proof to the original pending request here.
	if !ok || !pending.requesterRoute.matches(route) || !pending.targetRoute.matches(relay.ResultSource) {
		return false
	}
	pending, ok = h.thinToolRPC.finishMatching(pending)
	if !ok {
		return false
	}
	forward := *msg
	if pending.sharingScope != nil {
		forward = *pending.sharingScope
		forward.Result, forward.Error = msg.Result, msg.Error
	}
	forward.ID = ""
	forward.Type = thinToolRPCTypeResult
	forward.TargetOwnerUserID = formatUID(pending.targetOwnerUID)
	forward.TargetDeviceID = pending.targetDeviceID
	forward.DeviceID = pending.targetDeviceID
	forward.ToolName = pending.toolName
	result := "ok"
	if msg.Error != nil {
		result = "error"
	}
	h.auditSharedThinToolRPC(pending, "rpc_result", result)
	return h.sendThinToolRPCToLocalRoute(route, &forward)
}
