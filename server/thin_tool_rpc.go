package server

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	thinToolRPCTypeRequest            = "request"
	thinToolRPCTypeResult             = "result"
	defaultThinToolRPCTTL             = 120 * time.Second
	maxThinToolRPCRequestIDLength     = 128
	maxThinToolRPCPendingPerRequester = 64
	maxThinToolRPCPendingPerDevice    = 32
	thinToolRPCBotActiveOnServerCode  = "BOT_ACTIVE_ON_SERVER_RUNTIME"
)

type thinToolRPCAuthorizationError struct {
	code    string
	message string
}

func (e *thinToolRPCAuthorizationError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func thinToolRPCAuthorizationErrorCode(err error) string {
	if typed, ok := err.(*thinToolRPCAuthorizationError); ok && strings.TrimSpace(typed.code) != "" {
		return typed.code
	}
	return "permission_denied"
}

type thinToolRPCPending struct {
	requestID      string
	requester      *Client
	requesterRoute runtimeRoute
	targetRoute    runtimeRoute
	targetOwnerUID int64
	targetDeviceID string
	toolName       string
	createdAt      time.Time
	expiresAt      time.Time
}

type thinToolRPCRouter struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	pending map[string]thinToolRPCPending
}

func newThinToolRPCRouter(ttl time.Duration) *thinToolRPCRouter {
	if ttl <= 0 {
		ttl = defaultThinToolRPCTTL
	}
	return &thinToolRPCRouter{
		ttl:     ttl,
		now:     time.Now,
		pending: make(map[string]thinToolRPCPending),
	}
}

func (r *thinToolRPCRouter) add(pending thinToolRPCPending) bool {
	if r == nil || pending.requestID == "" || pending.expiresAt.IsZero() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[pending.requestID]; exists {
		return false
	}
	now := r.now()
	requesterCount := 0
	deviceCount := 0
	for _, item := range r.pending {
		if !now.Before(item.expiresAt) {
			continue
		}
		if item.requester == pending.requester {
			requesterCount++
		}
		if item.targetOwnerUID == pending.targetOwnerUID && item.targetDeviceID == pending.targetDeviceID {
			deviceCount++
		}
	}
	if requesterCount >= maxThinToolRPCPendingPerRequester {
		return false
	}
	if deviceCount >= maxThinToolRPCPendingPerDevice {
		return false
	}
	r.pending[pending.requestID] = pending
	return true
}

// finishMatching claims a pending request only when it is the same logical
// request that was previously observed. This prevents a late result from an
// old request ID from claiming a newer request that reused the ID.
func (r *thinToolRPCRouter) finishMatching(expected thinToolRPCPending) (thinToolRPCPending, bool) {
	if r == nil || expected.requestID == "" {
		return thinToolRPCPending{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[expected.requestID]
	if !ok || pending.requester != expected.requester ||
		!pending.requesterRoute.matches(expected.requesterRoute) ||
		!pending.targetRoute.matches(expected.targetRoute) ||
		!pending.createdAt.Equal(expected.createdAt) ||
		!pending.expiresAt.Equal(expected.expiresAt) {
		return thinToolRPCPending{}, false
	}
	delete(r.pending, expected.requestID)
	return pending, true
}

func (r *thinToolRPCRouter) get(requestID string) (thinToolRPCPending, bool) {
	if r == nil || requestID == "" {
		return thinToolRPCPending{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[requestID]
	if !ok || !r.now().Before(pending.expiresAt) {
		return thinToolRPCPending{}, false
	}
	return pending, true
}

func (r *thinToolRPCRouter) expire(now time.Time) []thinToolRPCPending {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var expired []thinToolRPCPending
	for requestID, pending := range r.pending {
		if !now.Before(pending.expiresAt) {
			expired = append(expired, pending)
			delete(r.pending, requestID)
		}
	}
	return expired
}

func (r *thinToolRPCRouter) cancelByRequesterRoute(route runtimeRoute) []thinToolRPCPending {
	return r.cancelByRoute(route, func(pending thinToolRPCPending) runtimeRoute {
		return pending.requesterRoute
	})
}

func (r *thinToolRPCRouter) cancelByTargetRoute(route runtimeRoute) []thinToolRPCPending {
	return r.cancelByRoute(route, func(pending thinToolRPCPending) runtimeRoute {
		return pending.targetRoute
	})
}

func (r *thinToolRPCRouter) cancelByRoute(route runtimeRoute, pendingRoute func(thinToolRPCPending) runtimeRoute) []thinToolRPCPending {
	if r == nil || route.NodeID == "" || route.ConnectionID == "" || pendingRoute == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var cancelled []thinToolRPCPending
	for requestID, pending := range r.pending {
		if pendingRoute(pending).matches(route) {
			cancelled = append(cancelled, pending)
			delete(r.pending, requestID)
		}
	}
	return cancelled
}

func (h *Hub) handleThinToolRPC(client *Client, msg *MsgThinToolRPC) {
	switch strings.ToLower(strings.TrimSpace(msg.Type)) {
	case thinToolRPCTypeRequest:
		h.handleThinToolRPCRequest(client, msg)
	case thinToolRPCTypeResult:
		h.handleThinToolRPCResult(client, msg)
	default:
		h.sendThinToolRPCAck(client, msg.ID, http.StatusBadRequest, "unknown thin_tool_rpc type", nil)
	}
}

func (h *Hub) handleThinToolRPCRequest(client *Client, msg *MsgThinToolRPC) {
	if h == nil || h.thinToolRPC == nil {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusServiceUnavailable, "thin tool rpc unavailable", nil)
		return
	}
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" || len(requestID) > maxThinToolRPCRequestIDLength {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusBadRequest, "request_id required", nil)
		return
	}
	ownerUID := parseFormattedUID(msg.TargetOwnerUserID)
	deviceID := strings.TrimSpace(msg.TargetDeviceID)
	toolName := strings.TrimSpace(msg.ToolName)
	log.Printf("[thin_tool_rpc] request received: request_id=%s msg_id=%s requester_uid=%s requester_conn=%s target_owner=%s target_device=%s tool=%s", requestID, msg.ID, formatUID(clientUID(client)), clientConnectionID(client), msg.TargetOwnerUserID, deviceID, toolName)
	if ownerUID <= 0 || deviceID == "" || toolName == "" {
		log.Printf("[thin_tool_rpc] request invalid target: request_id=%s target_owner=%s target_device=%s tool=%s", requestID, msg.TargetOwnerUserID, deviceID, toolName)
		h.sendThinToolRPCResultToRequester(client, requestID, msg, "invalid_target", "thin_tool_rpc requires target_owner_user_id, target_device_id, and tool_name")
		h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
		return
	}
	if err := h.authorizeSkillHubThinToolRPC(client, msg, ownerUID, deviceID, toolName); err != nil {
		code := thinToolRPCAuthorizationErrorCode(err)
		log.Printf("[thin_tool_rpc] skillhub request denied: request_id=%s requester_uid=%s target_owner=%s target_device=%s tool=%s code=%s reason=%s", requestID, formatUID(clientUID(client)), formatUID(ownerUID), deviceID, toolName, code, err.Error())
		h.sendThinToolRPCResultToRequester(client, requestID, msg, code, err.Error())
		h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
		return
	}

	route, _ := h.findDeviceRPCTarget(ownerUID, UserDevice{DeviceID: deviceID})
	if !route.validAt(nowForRoute(h)) || !h.routeConnected(route) {
		log.Printf("[thin_tool_rpc] target unavailable: request_id=%s target_owner=%s target_device=%s route=%s route_connected=%t", requestID, formatUID(ownerUID), deviceID, describeRuntimeRoute(route), h.routeConnected(route))
		h.sendThinToolRPCResultToRequester(client, requestID, msg, "target_device_unavailable", fmt.Sprintf("target device %s for %s is not online or has no route", deviceID, formatUID(ownerUID)))
		h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
		return
	}
	log.Printf("[thin_tool_rpc] target route selected: request_id=%s target_owner=%s target_device=%s route=%s requester_route=%s", requestID, formatUID(ownerUID), deviceID, describeRuntimeRoute(route), describeRuntimeRoute(h.clientRoute(client)))

	now := h.thinToolRPC.now()
	expiresAt := now.Add(h.thinToolRPC.ttl)
	if msg.ExpiresAt > 0 {
		if requested := time.UnixMilli(msg.ExpiresAt); requested.Before(expiresAt) {
			expiresAt = requested
		}
	}
	requesterRoute := h.clientRoute(client)
	forward := *msg
	forward.ID = ""
	forward.Type = thinToolRPCTypeRequest
	forward.RequestID = requestID
	forward.TargetOwnerUserID = formatUID(ownerUID)
	forward.TargetDeviceID = deviceID
	forward.DeviceID = deviceID
	forward.CreatedAt = unixMillis(now)
	forward.ExpiresAt = unixMillis(expiresAt)

	pending := thinToolRPCPending{
		requestID:      requestID,
		requester:      client,
		requesterRoute: requesterRoute,
		targetRoute:    route,
		targetOwnerUID: ownerUID,
		targetDeviceID: deviceID,
		toolName:       toolName,
		createdAt:      now,
		expiresAt:      expiresAt,
	}
	if !h.thinToolRPC.add(pending) {
		log.Printf("[thin_tool_rpc] request duplicate: request_id=%s target_owner=%s target_device=%s tool=%s", requestID, formatUID(ownerUID), deviceID, toolName)
		h.sendThinToolRPCResultToRequester(client, requestID, msg, "request_id_duplicate", "thin_tool_rpc request_id is already pending")
		h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
		return
	}
	if !h.isClientRegistered(client) || !h.clientRoute(client).matches(requesterRoute) {
		log.Printf("[thin_tool_rpc] requester route changed before forward: request_id=%s requester_route=%s", requestID, describeRuntimeRoute(requesterRoute))
		h.thinToolRPC.finishMatching(pending)
		return
	}
	currentRoute, _ := h.findDeviceRPCTarget(ownerUID, UserDevice{DeviceID: deviceID})
	if !currentRoute.matches(route) {
		log.Printf("[thin_tool_rpc] target route changed before forward: request_id=%s target_owner=%s target_device=%s selected_route=%s current_route=%s", requestID, formatUID(ownerUID), deviceID, describeRuntimeRoute(route), describeRuntimeRoute(currentRoute))
		if _, ok := h.thinToolRPC.finishMatching(pending); ok {
			h.sendThinToolRPCResultToRequester(client, requestID, msg, "target_device_unavailable", fmt.Sprintf("target device %s connection changed before forwarding", deviceID))
		}
		h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
		return
	}

	if !h.sendThinToolRPCToRoute(route, &forward) {
		log.Printf("[thin_tool_rpc] forward failed: request_id=%s target_owner=%s target_device=%s route=%s", requestID, formatUID(ownerUID), deviceID, describeRuntimeRoute(route))
		if _, ok := h.thinToolRPC.finishMatching(pending); ok {
			h.sendThinToolRPCResultToRequester(client, requestID, msg, "target_device_unavailable", fmt.Sprintf("target device %s route disappeared before forwarding", deviceID))
		}
		h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
		return
	}
	log.Printf("[thin_tool_rpc] forward accepted: request_id=%s target_owner=%s target_device=%s tool=%s route=%s expires_at=%d", requestID, formatUID(ownerUID), deviceID, toolName, describeRuntimeRoute(route), forward.ExpiresAt)
	h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID, "target_device_id": deviceID})
}

func (h *Hub) handleThinToolRPCResult(client *Client, msg *MsgThinToolRPC) {
	if h == nil || h.thinToolRPC == nil {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusServiceUnavailable, "thin tool rpc unavailable", nil)
		return
	}
	if client != nil && client.deviceConnector != nil && !deviceConnectorHasScope(client.deviceConnector, "device:rpc_result") {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusForbidden, "device connector token does not allow thin_tool_rpc results", nil)
		return
	}
	if client != nil && client.deviceConnector != nil && h.isDeviceConnectorRevoked(client.deviceConnector) {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusForbidden, "device connector token has been revoked", nil)
		return
	}
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusBadRequest, "request_id required", nil)
		return
	}
	log.Printf("[thin_tool_rpc] result received: request_id=%s msg_id=%s source_uid=%s source_conn=%s device_id=%s target_owner=%s target_device=%s tool=%s has_error=%t has_result=%t", requestID, msg.ID, formatUID(clientUID(client)), clientConnectionID(client), msg.DeviceID, msg.TargetOwnerUserID, msg.TargetDeviceID, msg.ToolName, msg.Error != nil, msg.Result != nil)
	pending, ok := h.thinToolRPC.get(requestID)
	if !ok {
		log.Printf("[thin_tool_rpc] result not pending: request_id=%s source_uid=%s source_conn=%s", requestID, formatUID(clientUID(client)), clientConnectionID(client))
		h.sendThinToolRPCAck(client, msg.ID, http.StatusNotFound, "request not pending", map[string]interface{}{"request_id": requestID})
		return
	}
	if !h.thinToolRPCResultMatchesTarget(pending, client) {
		log.Printf("[thin_tool_rpc] result source mismatch: request_id=%s source_uid=%s source_conn=%s expected_owner=%s expected_device=%s expected_route=%s actual_route=%s", requestID, formatUID(clientUID(client)), clientConnectionID(client), formatUID(pending.targetOwnerUID), pending.targetDeviceID, describeRuntimeRoute(pending.targetRoute), describeRuntimeRoute(h.clientRoute(client)))
		h.sendThinToolRPCAck(client, msg.ID, http.StatusForbidden, "result source does not match target device", map[string]interface{}{"request_id": requestID})
		return
	}
	pending, ok = h.thinToolRPC.finishMatching(pending)
	if !ok {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusNotFound, "request not pending", map[string]interface{}{"request_id": requestID})
		return
	}
	forward := *msg
	forward.ID = ""
	forward.Type = thinToolRPCTypeResult
	forward.RequestID = requestID
	forward.TargetOwnerUserID = formatUID(pending.targetOwnerUID)
	forward.TargetDeviceID = pending.targetDeviceID
	forward.DeviceID = pending.targetDeviceID
	forward.ToolName = pending.toolName
	if !h.sendThinToolRPCToRoute(pending.requesterRoute, &forward) {
		log.Printf("[thin_tool_rpc] result forward failed: request_id=%s requester_route=%s requester_registered=%t", requestID, describeRuntimeRoute(pending.requesterRoute), h.isClientRegistered(pending.requester))
		if h.isClientRegistered(pending.requester) {
			h.SendToClient(pending.requester, &ServerMessage{ThinToolRPC: &forward})
		} else {
			h.sendThinToolRPCAck(client, msg.ID, http.StatusGone, "requester offline", map[string]interface{}{"request_id": requestID})
			return
		}
	}
	log.Printf("[thin_tool_rpc] result forwarded: request_id=%s requester_route=%s target_owner=%s target_device=%s tool=%s", requestID, describeRuntimeRoute(pending.requesterRoute), formatUID(pending.targetOwnerUID), pending.targetDeviceID, pending.toolName)
	h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
}

func (h *Hub) authorizeSkillHubThinToolRPC(client *Client, msg *MsgThinToolRPC, ownerUID int64, deviceID string, toolName string) error {
	operation := DeviceGrantOperation(toolName)
	if !isSkillHubThinToolOperation(operation) {
		if client != nil && client.accountType == types.AccountHuman {
			return fmt.Errorf("human thin_tool_rpc requests are limited to SkillHub device operations")
		}
		return nil
	}
	if h == nil || h.db == nil || h.userDevices == nil || client == nil || client.accountType != types.AccountHuman {
		return fmt.Errorf("SkillHub device operations require an authenticated human WebApp connection")
	}
	if client.uid <= 0 || ownerUID != client.uid {
		return fmt.Errorf("target device owner does not match the authenticated user")
	}
	botUID := parseThinToolRPCBotUID(msg.Payload)
	if botUID <= 0 {
		return fmt.Errorf("bot_uid is required")
	}
	botOwnerUID, err := h.db.GetBotOwner(botUID)
	if err != nil || botOwnerUID != client.uid {
		return fmt.Errorf("bot is not owned by the authenticated user")
	}
	device, ok := h.userDevices.activeDevice(client.uid, deviceID)
	if !ok {
		return fmt.Errorf("target device is not active for the authenticated user")
	}
	if device.RuntimeRole == "server" {
		if device.BotUID <= 0 || device.BotUID != botUID {
			return fmt.Errorf("server Runtime device is not bound to the requested bot")
		}
		if operation == DeviceGrantSkillHubBotSwitch {
			return fmt.Errorf("server Runtime devices cannot switch bots through SkillHub")
		}
	}
	if operation == DeviceGrantSkillHubBotSwitch && device.RuntimeRole == "desktop" {
		if h.hasRoutableServerRuntimeForBot(client.uid, botUID, device.DeviceID) {
			return &thinToolRPCAuthorizationError{
				code:    thinToolRPCBotActiveOnServerCode,
				message: "target bot is already active on a server Runtime; desktop switch was not performed",
			}
		}
	}
	for _, capability := range device.Capabilities {
		if capability == operation {
			msg.TargetOwnerUserID = formatUID(client.uid)
			return nil
		}
	}
	return fmt.Errorf("target device does not support %s", toolName)
}

func (h *Hub) hasRoutableServerRuntimeForBot(ownerUID int64, botUID int64, excludedDeviceID string) bool {
	if h == nil || h.userDevices == nil || ownerUID <= 0 || botUID <= 0 {
		return false
	}
	devices, _ := h.classifyUserDevices(ownerUID, h.userDevices.activeDevices(ownerUID))
	for _, device := range devices {
		if device.RuntimeRole == "server" &&
			device.BotUID == botUID &&
			device.Active &&
			device.RouteConnected &&
			device.Routable &&
			device.DeviceID != excludedDeviceID {
			return true
		}
	}
	return false
}

func isSkillHubThinToolOperation(operation DeviceGrantOperation) bool {
	switch operation {
	case DeviceGrantSkillHubWorkspaceGet,
		DeviceGrantSkillHubSkillShare,
		DeviceGrantSkillHubSkillFinalize,
		DeviceGrantSkillHubSkillDelete,
		DeviceGrantSkillHubBotSwitch:
		return true
	default:
		return false
	}
}

func parseThinToolRPCBotUID(payload map[string]interface{}) int64 {
	if payload == nil {
		return 0
	}
	value, ok := payload["bot_uid"]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case string:
		uid, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil || uid <= 0 {
			return 0
		}
		return uid
	case float64:
		if typed < 1 || typed >= float64(math.MaxInt64) || math.Trunc(typed) != typed {
			return 0
		}
		return int64(typed)
	case int64:
		if typed <= 0 {
			return 0
		}
		return typed
	case int:
		if typed <= 0 {
			return 0
		}
		return int64(typed)
	default:
		return 0
	}
}

func (h *Hub) thinToolRPCResultMatchesTarget(pending thinToolRPCPending, client *Client) bool {
	if h == nil || client == nil || pending.targetOwnerUID <= 0 || pending.targetDeviceID == "" {
		return false
	}
	if pending.targetRoute.NodeID != "" || pending.targetRoute.ConnectionID != "" {
		if !pending.targetRoute.matches(h.clientRoute(client)) {
			return false
		}
	}
	if client.deviceOwnerUID == pending.targetOwnerUID && client.deviceID == pending.targetDeviceID {
		return true
	}
	return client.deviceConnector != nil &&
		client.deviceConnector.UID == pending.targetOwnerUID &&
		client.deviceConnector.DeviceID == pending.targetDeviceID
}

func (h *Hub) expireThinToolRPCRequests(now time.Time) int {
	if h == nil || h.thinToolRPC == nil {
		return 0
	}
	expired := h.thinToolRPC.expire(now)
	for _, pending := range expired {
		h.notifyThinToolRPCTimeout(pending)
	}
	return len(expired)
}

func (h *Hub) notifyThinToolRPCTimeout(pending thinToolRPCPending) {
	if h == nil || pending.requestID == "" {
		return
	}
	log.Printf("[thin_tool_rpc] pending timeout: request_id=%s target_owner=%s target_device=%s tool=%s target_route=%s requester_route=%s", pending.requestID, formatUID(pending.targetOwnerUID), pending.targetDeviceID, pending.toolName, describeRuntimeRoute(pending.targetRoute), describeRuntimeRoute(pending.requesterRoute))
	msg := &MsgThinToolRPC{
		Type:              thinToolRPCTypeResult,
		RequestID:         pending.requestID,
		TargetOwnerUserID: formatUID(pending.targetOwnerUID),
		TargetDeviceID:    pending.targetDeviceID,
		DeviceID:          pending.targetDeviceID,
		ToolName:          pending.toolName,
		Error: &MsgDeviceRPCError{
			Code:    "thin_tool_rpc_timeout",
			Message: "target device did not return a tool result before the request expired",
		},
		CreatedAt: unixMillis(pending.createdAt),
		ExpiresAt: unixMillis(pending.expiresAt),
	}
	_ = h.sendThinToolRPCToRoute(pending.requesterRoute, msg)
}

func (h *Hub) cancelThinToolRPCRequestsByRequesterRoute(route runtimeRoute) int {
	if h == nil || h.thinToolRPC == nil {
		return 0
	}
	cancelled := h.thinToolRPC.cancelByRequesterRoute(route)
	if len(cancelled) > 0 {
		log.Printf("[thin_tool_rpc] requester route disconnected: route=%s cancelled=%d", describeRuntimeRoute(route), len(cancelled))
	}
	return len(cancelled)
}

func (h *Hub) cancelThinToolRPCRequestsByTargetRoute(route runtimeRoute) int {
	if h == nil || h.thinToolRPC == nil {
		return 0
	}
	cancelled := h.thinToolRPC.cancelByTargetRoute(route)
	for _, pending := range cancelled {
		h.notifyThinToolRPCTargetReplaced(pending)
	}
	if len(cancelled) > 0 {
		log.Printf("[thin_tool_rpc] target route replaced: route=%s cancelled=%d", describeRuntimeRoute(route), len(cancelled))
	}
	return len(cancelled)
}

func (h *Hub) notifyThinToolRPCTargetReplaced(pending thinToolRPCPending) {
	if h == nil || pending.requestID == "" {
		return
	}
	msg := &MsgThinToolRPC{
		Type:              thinToolRPCTypeResult,
		RequestID:         pending.requestID,
		TargetOwnerUserID: formatUID(pending.targetOwnerUID),
		TargetDeviceID:    pending.targetDeviceID,
		DeviceID:          pending.targetDeviceID,
		ToolName:          pending.toolName,
		Error: &MsgDeviceRPCError{
			Code:    "target_device_unavailable",
			Message: "target device connection was replaced before returning a tool result",
		},
		CreatedAt: unixMillis(pending.createdAt),
		ExpiresAt: unixMillis(pending.expiresAt),
	}
	if !h.sendThinToolRPCToRoute(pending.requesterRoute, msg) {
		log.Printf("[thin_tool_rpc] target replacement notification failed: request_id=%s requester_route=%s", pending.requestID, describeRuntimeRoute(pending.requesterRoute))
	}
}

func (h *Hub) sendThinToolRPCResultToRequester(client *Client, requestID string, request *MsgThinToolRPC, code string, message string) {
	if client == nil || requestID == "" {
		return
	}
	log.Printf("[thin_tool_rpc] synthetic result to requester: request_id=%s requester_uid=%s code=%s message=%q target_owner=%s target_device=%s tool=%s", requestID, formatUID(clientUID(client)), code, message, request.TargetOwnerUserID, request.TargetDeviceID, request.ToolName)
	h.SendToClient(client, &ServerMessage{ThinToolRPC: &MsgThinToolRPC{
		Type:              thinToolRPCTypeResult,
		RequestID:         requestID,
		TargetOwnerUserID: request.TargetOwnerUserID,
		TargetDeviceID:    request.TargetDeviceID,
		DeviceID:          request.TargetDeviceID,
		ToolName:          request.ToolName,
		Error:             &MsgDeviceRPCError{Code: code, Message: message},
		CreatedAt:         unixMillis(time.Now()),
	}})
}

func (h *Hub) sendThinToolRPCToLocalRoute(route runtimeRoute, msg *MsgThinToolRPC) bool {
	if h == nil || route.ConnectionID == "" {
		log.Printf("[thin_tool_rpc] local route missing connection: request_id=%s type=%s route=%s", thinToolRPCRequestID(msg), thinToolRPCMessageType(msg), describeRuntimeRoute(route))
		return false
	}
	client := h.getClientByConnectionID(route.ConnectionID)
	if client == nil {
		log.Printf("[thin_tool_rpc] local route client not found: request_id=%s type=%s route=%s", thinToolRPCRequestID(msg), thinToolRPCMessageType(msg), describeRuntimeRoute(route))
		return false
	}
	log.Printf("[thin_tool_rpc] local forward: request_id=%s type=%s to_uid=%s to_conn=%s route=%s target_owner=%s target_device=%s tool=%s", thinToolRPCRequestID(msg), thinToolRPCMessageType(msg), formatUID(clientUID(client)), clientConnectionID(client), describeRuntimeRoute(route), msg.TargetOwnerUserID, msg.TargetDeviceID, msg.ToolName)
	h.SendToClient(client, &ServerMessage{ThinToolRPC: msg})
	return true
}

func (h *Hub) sendThinToolRPCToRoute(route runtimeRoute, msg *MsgThinToolRPC) bool {
	if h == nil || !route.validAt(nowForRoute(h)) || msg == nil {
		log.Printf("[thin_tool_rpc] route invalid: request_id=%s type=%s route=%s", thinToolRPCRequestID(msg), thinToolRPCMessageType(msg), describeRuntimeRoute(route))
		return false
	}
	if route.NodeID == "" || route.NodeID == h.nodeID {
		return h.sendThinToolRPCToLocalRoute(route, msg)
	}
	if h.sharedRuntime != nil {
		log.Printf("[thin_tool_rpc] shared forward: request_id=%s type=%s route=%s", thinToolRPCRequestID(msg), thinToolRPCMessageType(msg), describeRuntimeRoute(route))
		return h.sharedRuntime.deliverThinToolRPC(route, msg, nowForRoute(h))
	}
	log.Printf("[thin_tool_rpc] route is remote but shared runtime missing: request_id=%s type=%s route=%s hub_node=%s", thinToolRPCRequestID(msg), thinToolRPCMessageType(msg), describeRuntimeRoute(route), h.nodeID)
	return false
}

func clientUID(client *Client) int64 {
	if client == nil {
		return 0
	}
	return client.uid
}

func clientConnectionID(client *Client) string {
	if client == nil {
		return ""
	}
	return client.connectionID
}

func describeRuntimeRoute(route runtimeRoute) string {
	return fmt.Sprintf("node=%s conn=%s expires=%d", route.NodeID, route.ConnectionID, unixMillis(route.ExpiresAt))
}

func thinToolRPCRequestID(msg *MsgThinToolRPC) string {
	if msg == nil {
		return ""
	}
	return msg.RequestID
}

func thinToolRPCMessageType(msg *MsgThinToolRPC) string {
	if msg == nil {
		return ""
	}
	return msg.Type
}

func (h *Hub) sendThinToolRPCAck(client *Client, id string, code int, text string, params map[string]interface{}) {
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:     id,
			Code:   code,
			Text:   text,
			Params: params,
		},
	})
}
