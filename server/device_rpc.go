package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultDeviceRPCTTL          = 60 * time.Second
	maxDeviceRPCRequestIDLength  = 128
	maxDeviceRPCPendingPerAgent  = 64
	maxDeviceRPCPendingPerDevice = 32
	deviceRPCTypeRequest         = "request"
	deviceRPCTypeResult          = "result"
)

type deviceRPCPending struct {
	requestID       string
	requester       *Client
	requesterRoute  runtimeRoute
	agentUID        int64
	agentID         string
	agentBodyID     string
	actorUID        int64
	actorUserID     string
	ownerUID        int64
	ownerUserID     string
	sessionKey      string
	topicID         string
	topicType       string
	grantID         string
	deviceID        string
	deviceBodyID    string
	deviceInstallID string
	operation       string
	toolName        string
	createdAt       time.Time
	target          *Client
	targetRoute     runtimeRoute
	expiresAt       time.Time
}

type DeviceRPCPendingStatus struct {
	RequestID            string `json:"request_id"`
	AgentID              string `json:"agent_id,omitempty"`
	AgentBodyID          string `json:"agent_body_id,omitempty"`
	ActorUserID          string `json:"actor_user_id,omitempty"`
	OwnerUserID          string `json:"owner_user_id,omitempty"`
	SessionKey           string `json:"session_key,omitempty"`
	TopicID              string `json:"topic_id,omitempty"`
	TopicType            string `json:"topic_type,omitempty"`
	GrantID              string `json:"grant_id,omitempty"`
	DeviceID             string `json:"device_id,omitempty"`
	DeviceBodyID         string `json:"device_body_id,omitempty"`
	DeviceInstallationID string `json:"device_installation_id,omitempty"`
	Operation            string `json:"operation,omitempty"`
	ToolName             string `json:"tool_name,omitempty"`
	CreatedAt            int64  `json:"created_at,omitempty"`
	ExpiresAt            int64  `json:"expires_at,omitempty"`
	TTLMS                int64  `json:"ttl_ms,omitempty"`
	RequesterConnected   bool   `json:"requester_connected"`
	TargetConnected      bool   `json:"target_connected"`
}

type deviceRPCRouter struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	pending map[string]deviceRPCPending
	shared  sharedRuntimeState
}

func newDeviceRPCRouter(ttl time.Duration) *deviceRPCRouter {
	if ttl <= 0 {
		ttl = defaultDeviceRPCTTL
	}
	return &deviceRPCRouter{
		ttl:     ttl,
		now:     time.Now,
		pending: make(map[string]deviceRPCPending),
	}
}

func (r *deviceRPCRouter) withSharedRuntime(shared sharedRuntimeState) *deviceRPCRouter {
	if r == nil {
		return r
	}
	r.shared = shared
	return r
}

func (r *deviceRPCRouter) add(pending deviceRPCPending) (bool, string) {
	if r == nil || pending.requestID == "" || pending.expiresAt.IsZero() {
		return false, "invalid"
	}
	if r.shared != nil {
		return r.shared.addDeviceRPCPending(deviceRPCRecordFromPending(pending), r.now())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[pending.requestID]; exists {
		return false, "duplicate"
	}
	now := r.now()
	var agentCount, deviceCount int
	for _, item := range r.pending {
		if !now.Before(item.expiresAt) {
			continue
		}
		if item.agentUID == pending.agentUID {
			agentCount++
		}
		if item.ownerUID == pending.ownerUID && item.deviceID == pending.deviceID {
			deviceCount++
		}
	}
	if agentCount >= maxDeviceRPCPendingPerAgent {
		return false, "agent_limit"
	}
	if deviceCount >= maxDeviceRPCPendingPerDevice {
		return false, "device_limit"
	}
	r.pending[pending.requestID] = pending
	return true, ""
}

func (r *deviceRPCRouter) get(requestID string) (deviceRPCPending, bool) {
	if r == nil || requestID == "" {
		return deviceRPCPending{}, false
	}
	if r.shared != nil {
		record, ok := r.shared.getDeviceRPCPending(requestID, r.now())
		if !ok {
			return deviceRPCPending{}, false
		}
		return pendingFromDeviceRPCRecord(record), true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	pending, ok := r.pending[requestID]
	if !ok || !now.Before(pending.expiresAt) {
		return deviceRPCPending{}, false
	}
	return pending, true
}

func (r *deviceRPCRouter) finish(requestID string) {
	if r == nil || requestID == "" {
		return
	}
	if r.shared != nil {
		r.shared.finishDeviceRPCPending(requestID)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, requestID)
}

func (r *deviceRPCRouter) listByOwner(ownerUID int64) []deviceRPCPending {
	if r == nil || ownerUID <= 0 {
		return nil
	}
	if r.shared != nil {
		records := r.shared.listDeviceRPCPendingByOwner(ownerUID)
		out := make([]deviceRPCPending, 0, len(records))
		for _, record := range records {
			out = append(out, pendingFromDeviceRPCRecord(record))
		}
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]deviceRPCPending, 0)
	for _, pending := range r.pending {
		if pending.ownerUID == ownerUID {
			out = append(out, pending)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].createdAt.Before(out[j].createdAt)
	})
	return out
}

func (r *deviceRPCRouter) expire(now time.Time) []deviceRPCPending {
	if r == nil {
		return nil
	}
	if r.shared != nil {
		records := r.shared.expireDeviceRPCPending(now)
		out := make([]deviceRPCPending, 0, len(records))
		for _, record := range records {
			out = append(out, pendingFromDeviceRPCRecord(record))
		}
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var expired []deviceRPCPending
	for requestID, pending := range r.pending {
		if !now.Before(pending.expiresAt) {
			expired = append(expired, pending)
			delete(r.pending, requestID)
		}
	}
	return expired
}

func (r *deviceRPCRouter) cleanupLocked(now time.Time) {
	for requestID, pending := range r.pending {
		if !now.Before(pending.expiresAt) {
			delete(r.pending, requestID)
		}
	}
}

func (h *Hub) bindClientDeviceFromHi(client *Client, msg *MsgClientHi) (map[string]interface{}, bool) {
	if h == nil || client == nil || msg == nil || msg.Device == nil {
		return nil, true
	}
	ownerUID := h.deviceOwnerUIDForClient(client)
	if ownerUID <= 0 || h.userDevices == nil {
		return nil, false
	}
	if client.deviceConnector != nil {
		if h.isDeviceConnectorRevoked(client.deviceConnector) {
			return nil, false
		}
		if !deviceConnectorHasScope(client.deviceConnector, "device:register") {
			return nil, false
		}
		if ownerUID != client.deviceConnector.UID {
			return nil, false
		}
		msg.Device.DeviceID = client.deviceConnector.DeviceID
		msg.Device.BodyID = client.deviceConnector.DeviceID
		msg.Device.InstallationID = firstNonEmpty(client.deviceConnector.InstallationID, client.deviceConnector.DeviceID)
		if strings.TrimSpace(msg.Device.DisplayName) == "" {
			msg.Device.DisplayName = client.deviceConnector.DisplayName
		}
		if len(msg.Device.Capabilities) == 0 {
			msg.Device.Capabilities = client.deviceConnector.Capabilities
		} else {
			msg.Device.Capabilities = limitConnectorCapabilities(msg.Device.Capabilities, client.deviceConnector.Capabilities)
		}
	}
	req := RegisterUserDeviceRequest{
		DeviceID:       msg.Device.DeviceID,
		DisplayName:    msg.Device.DisplayName,
		BodyID:         firstNonEmpty(msg.Device.BodyID, client.bodyID),
		InstallationID: firstNonEmpty(msg.Device.InstallationID, client.installationID),
		Status:         msg.Device.Status,
		Capabilities:   msg.Device.Capabilities,
	}
	device, err := h.userDevices.register(ownerUID, req)
	if err != nil {
		return nil, false
	}
	h.bindDeviceClient(ownerUID, device, client)
	return map[string]interface{}{
		"owner_user_id":   formatUID(ownerUID),
		"device_id":       device.DeviceID,
		"body_id":         device.BodyID,
		"installation_id": device.InstallationID,
	}, true
}

func (h *Hub) deviceOwnerUIDForClient(client *Client) int64 {
	if h == nil || client == nil || client.uid <= 0 {
		return 0
	}
	if client.accountType == types.AccountBot && h.db != nil {
		if ownerUID, err := h.db.GetBotOwner(client.uid); err == nil && ownerUID > 0 {
			return ownerUID
		}
	}
	return client.uid
}

func (h *Hub) handleDeviceRPC(client *Client, msg *MsgDeviceRPC) {
	switch strings.ToLower(strings.TrimSpace(msg.Type)) {
	case deviceRPCTypeRequest:
		h.handleDeviceRPCRequest(client, msg)
	case deviceRPCTypeResult:
		h.handleDeviceRPCResult(client, msg)
	default:
		h.sendDeviceRPCAck(client, msg.ID, http.StatusBadRequest, "unknown device_rpc type", nil)
	}
}

func (h *Hub) handleDeviceRPCRequest(client *Client, msg *MsgDeviceRPC) {
	if h == nil || h.deviceRPC == nil || h.userDevices == nil {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusServiceUnavailable, "device rpc unavailable", nil)
		return
	}
	if client == nil || client.accountType != types.AccountBot {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "device rpc requests require bot connection", nil)
		return
	}
	requestID, ok := normalizeDeviceRPCRequestID(msg.RequestID)
	if !ok {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusBadRequest, "request_id required", nil)
		return
	}
	grantID := strings.TrimSpace(msg.GrantID)
	if grantID == "" {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusBadRequest, "grant_id required", map[string]interface{}{"request_id": requestID})
		return
	}
	operation := DeviceGrantOperation(strings.TrimSpace(msg.Operation))
	if operation == "" {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusBadRequest, "operation required", map[string]interface{}{"request_id": requestID})
		return
	}
	if !isAllowedDeviceRPCOperation(operation) {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "operation is not supported by device rpc", map[string]interface{}{"request_id": requestID})
		return
	}
	grant, ok := h.userDevices.lookupGrant(grantID)
	if !ok {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "device grant is not active", map[string]interface{}{"request_id": requestID})
		return
	}
	actorUID := parseFormattedUID(grant.ActorUserID)
	if actorUID <= 0 {
		h.auditDeviceRPC(0, "rpc_rejected", "denied", "invalid actor user", msg, grant)
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "invalid actor user", map[string]interface{}{"request_id": requestID})
		return
	}
	ownerUID := parseFormattedUID(grant.OwnerUserID)
	if ownerUID <= 0 {
		ownerUID = actorUID
	}
	ownerUserID := strings.TrimSpace(grant.OwnerUserID)
	if ownerUserID == "" {
		ownerUserID = formatUID(ownerUID)
		grant.OwnerUserID = ownerUserID
	}
	if err := validateDeviceRPCGrant(client, msg, grant, operation); err != nil {
		h.auditDeviceRPC(ownerUID, "rpc_rejected", "denied", err.Error(), msg, grant)
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, err.Error(), map[string]interface{}{"request_id": requestID})
		return
	}
	device, ok := h.userDevices.activeDevice(ownerUID, grant.DeviceID)
	if !ok {
		h.auditDeviceRPC(ownerUID, "rpc_rejected", "offline", "device offline", msg, grant)
		h.sendDeviceRPCAck(client, msg.ID, http.StatusNotFound, "device offline", map[string]interface{}{"request_id": requestID, "device_id": grant.DeviceID})
		return
	}
	targetRoute, target := h.findDeviceRPCTarget(ownerUID, device)
	if !targetRoute.validAt(nowForRoute(h)) {
		h.auditDeviceRPC(ownerUID, "rpc_rejected", "unavailable", "device connection unavailable", msg, grant)
		h.sendDeviceRPCAck(client, msg.ID, http.StatusNotFound, "device connection unavailable", map[string]interface{}{"request_id": requestID, "device_id": grant.DeviceID})
		return
	}

	now := h.deviceRPC.now()
	expiresAt := now.Add(h.deviceRPC.ttl)
	if grantExpiry := time.UnixMilli(grant.ExpiresAt); grant.ExpiresAt > 0 && grantExpiry.Before(expiresAt) {
		expiresAt = grantExpiry
	}
	requesterRoute := h.clientRoute(client)
	requesterRoute.ExpiresAt = expiresAt
	forward := *msg
	forward.ID = ""
	forward.Type = deviceRPCTypeRequest
	forward.RequestID = requestID
	forward.GrantID = grant.GrantID
	forward.SessionKey = grant.SessionKey
	forward.TopicID = grant.TopicID
	forward.TopicType = grant.TopicType
	forward.ActorUserID = grant.ActorUserID
	forward.AgentID = grant.AgentID
	forward.AgentBodyID = grant.AgentBodyID
	forward.DeviceID = device.DeviceID
	forward.DeviceBodyID = firstNonEmpty(device.BodyID, grant.DeviceBodyID)
	forward.DeviceInstallationID = firstNonEmpty(device.InstallationID, grant.DeviceInstallationID)
	forward.Operation = string(operation)
	forward.CreatedAt = unixMillis(now)
	forward.ExpiresAt = unixMillis(expiresAt)

	pending := deviceRPCPending{
		requestID:       requestID,
		requester:       client,
		requesterRoute:  requesterRoute,
		agentUID:        client.uid,
		agentID:         grant.AgentID,
		agentBodyID:     grant.AgentBodyID,
		actorUID:        actorUID,
		actorUserID:     grant.ActorUserID,
		ownerUID:        ownerUID,
		ownerUserID:     ownerUserID,
		sessionKey:      grant.SessionKey,
		topicID:         grant.TopicID,
		topicType:       grant.TopicType,
		grantID:         grant.GrantID,
		deviceID:        device.DeviceID,
		deviceBodyID:    forward.DeviceBodyID,
		deviceInstallID: forward.DeviceInstallationID,
		operation:       string(operation),
		toolName:        strings.TrimSpace(msg.ToolName),
		createdAt:       now,
		target:          target,
		targetRoute:     targetRoute,
		expiresAt:       expiresAt,
	}
	if ok, reason := h.deviceRPC.add(pending); !ok {
		if reason == "agent_limit" || reason == "device_limit" {
			h.auditDeviceRPC(ownerUID, "rpc_rejected", "rate_limited", "too many pending device rpc requests", msg, grant)
			h.sendDeviceRPCAck(client, msg.ID, http.StatusTooManyRequests, "too many pending device rpc requests", map[string]interface{}{"request_id": requestID})
			return
		}
		h.auditDeviceRPC(ownerUID, "rpc_rejected", "duplicate", "request_id is already pending", msg, grant)
		h.sendDeviceRPCAck(client, msg.ID, http.StatusConflict, "request_id is already pending", map[string]interface{}{"request_id": requestID})
		return
	}

	if !h.sendDeviceRPCToRoute(targetRoute, &forward) {
		h.deviceRPC.finish(requestID)
		h.auditDeviceRPC(ownerUID, "rpc_rejected", "unavailable", "device connection unavailable", msg, grant)
		h.sendDeviceRPCAck(client, msg.ID, http.StatusNotFound, "device connection unavailable", map[string]interface{}{"request_id": requestID, "device_id": grant.DeviceID})
		return
	}
	h.auditDeviceRPC(ownerUID, "rpc_forwarded", "ok", "", &forward, grant)
	h.sendDeviceRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{
		"request_id":             requestID,
		"device_id":              device.DeviceID,
		"device_body_id":         forward.DeviceBodyID,
		"device_installation_id": forward.DeviceInstallationID,
		"operation":              string(operation),
		"tool_name":              forward.ToolName,
		"expires_at":             unixMillis(expiresAt),
	})
}

func (h *Hub) handleDeviceRPCResult(client *Client, msg *MsgDeviceRPC) {
	if h == nil || h.deviceRPC == nil {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusServiceUnavailable, "device rpc unavailable", nil)
		return
	}
	if client != nil && client.deviceConnector != nil && !deviceConnectorHasScope(client.deviceConnector, "device:rpc_result") {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "device connector token does not allow device_rpc results", nil)
		return
	}
	if client != nil && client.deviceConnector != nil && h.isDeviceConnectorRevoked(client.deviceConnector) {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "device connector token has been revoked", nil)
		return
	}
	requestID, ok := normalizeDeviceRPCRequestID(msg.RequestID)
	if !ok {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusBadRequest, "request_id required", nil)
		return
	}
	pending, ok := h.deviceRPC.get(requestID)
	if !ok {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusNotFound, "request not pending", map[string]interface{}{"request_id": requestID})
		return
	}
	if !h.pendingMatchesDeviceClient(pending, client) {
		h.auditDeviceRPC(pending.ownerUID, "rpc_result_rejected", "denied", "result source does not match target device", msg, ScopedDeviceGrant{
			GrantID:     pending.grantID,
			SessionKey:  pending.sessionKey,
			ActorUserID: pending.actorUserID,
			OwnerUserID: pending.ownerUserID,
			AgentID:     pending.agentID,
			DeviceID:    pending.deviceID,
		})
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "result source does not match target device", map[string]interface{}{"request_id": requestID})
		return
	}
	h.deviceRPC.finish(requestID)

	requesterRoute := pending.requesterRoute
	requester := pending.requester
	if !requesterRoute.validAt(nowForRoute(h)) {
		if h.isClientRegistered(requester) {
			requesterRoute = h.clientRoute(requester)
		} else {
			requester = h.findAgentRPCClient(pending.agentUID, pending.agentBodyID)
			if requester != nil {
				requesterRoute = h.clientRoute(requester)
			}
		}
	}
	if !requesterRoute.validAt(nowForRoute(h)) {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusGone, "requester offline", map[string]interface{}{"request_id": requestID})
		return
	}

	forward := *msg
	forward.ID = ""
	forward.Type = deviceRPCTypeResult
	forward.RequestID = requestID
	forward.GrantID = pending.grantID
	forward.SessionKey = pending.sessionKey
	forward.TopicID = pending.topicID
	forward.TopicType = pending.topicType
	forward.ActorUserID = pending.actorUserID
	forward.AgentID = pending.agentID
	forward.AgentBodyID = pending.agentBodyID
	forward.DeviceID = pending.deviceID
	forward.DeviceBodyID = pending.deviceBodyID
	forward.DeviceInstallationID = pending.deviceInstallID

	if !h.sendDeviceRPCToRoute(requesterRoute, &forward) {
		h.auditDeviceRPC(pending.ownerUID, "rpc_result_rejected", "gone", "requester offline", &forward, ScopedDeviceGrant{
			GrantID:     pending.grantID,
			SessionKey:  pending.sessionKey,
			ActorUserID: pending.actorUserID,
			OwnerUserID: pending.ownerUserID,
			AgentID:     pending.agentID,
			DeviceID:    pending.deviceID,
		})
		h.sendDeviceRPCAck(client, msg.ID, http.StatusGone, "requester offline", map[string]interface{}{"request_id": requestID})
		return
	}
	result := "ok"
	reason := ""
	if forward.Error != nil {
		result = "error"
		reason = forward.Error.Code
	}
	h.auditDeviceRPC(pending.ownerUID, "rpc_result", result, reason, &forward, ScopedDeviceGrant{
		GrantID:     pending.grantID,
		SessionKey:  pending.sessionKey,
		ActorUserID: pending.actorUserID,
		OwnerUserID: pending.ownerUserID,
		AgentID:     pending.agentID,
		DeviceID:    pending.deviceID,
	})
	h.sendDeviceRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
}

func (h *Hub) auditDeviceRPC(ownerUID int64, phase string, result string, reason string, msg *MsgDeviceRPC, grant ScopedDeviceGrant) {
	if h == nil || ownerUID <= 0 || msg == nil {
		return
	}
	h.addDeviceAudit(ownerUID, DeviceAuditEvent{
		ActorUserID: grant.ActorUserID,
		AgentID:     grant.AgentID,
		DeviceID:    firstNonEmpty(grant.DeviceID, msg.DeviceID),
		SessionKey:  firstNonEmpty(grant.SessionKey, msg.SessionKey),
		Operation:   firstNonEmpty(msg.Operation, string(DeviceGrantOperation(msg.Operation))),
		ToolName:    strings.TrimSpace(msg.ToolName),
		Phase:       phase,
		Result:      result,
		Reason:      reason,
	})
}

func (h *Hub) pendingMatchesDeviceClient(pending deviceRPCPending, client *Client) bool {
	if h == nil || client == nil || pending.ownerUID <= 0 || pending.deviceID == "" {
		return false
	}
	if pending.targetRoute.NodeID != "" || pending.targetRoute.ConnectionID != "" {
		if !pending.targetRoute.matches(h.clientRoute(client)) {
			return false
		}
		if current, _ := h.findDeviceRPCTarget(pending.ownerUID, UserDevice{DeviceID: pending.deviceID}); current.validAt(nowForRoute(h)) && !current.matches(h.clientRoute(client)) {
			return false
		}
		return client.deviceOwnerUID == pending.ownerUID && client.deviceID == pending.deviceID
	}
	current := h.getDeviceClient(pending.ownerUID, pending.deviceID)
	return current == client && client.deviceOwnerUID == pending.ownerUID && client.deviceID == pending.deviceID
}

func validateDeviceRPCGrant(client *Client, msg *MsgDeviceRPC, grant ScopedDeviceGrant, operation DeviceGrantOperation) error {
	if grant.Status != "active" || grant.IdentityTrust != "server_canonical" {
		return fmt.Errorf("device grant is not trusted")
	}
	if grant.AgentID != "" && parseFormattedUID(grant.AgentID) != client.uid {
		return fmt.Errorf("agent does not match grant")
	}
	if strings.TrimSpace(msg.AgentID) != "" && strings.TrimSpace(msg.AgentID) != grant.AgentID {
		return fmt.Errorf("agent_id does not match grant")
	}
	if grant.AgentBodyID != "" && client.bodyID != "" && client.bodyID != grant.AgentBodyID {
		return fmt.Errorf("agent body does not match grant")
	}
	if strings.TrimSpace(msg.AgentBodyID) != "" && strings.TrimSpace(msg.AgentBodyID) != grant.AgentBodyID {
		return fmt.Errorf("agent_body_id does not match grant")
	}
	if strings.TrimSpace(msg.ActorUserID) != "" && strings.TrimSpace(msg.ActorUserID) != grant.ActorUserID {
		return fmt.Errorf("actor_user_id does not match grant")
	}
	if strings.TrimSpace(msg.SessionKey) != "" && strings.TrimSpace(msg.SessionKey) != grant.SessionKey {
		return fmt.Errorf("session_key does not match grant")
	}
	if strings.TrimSpace(msg.TopicID) != "" && strings.TrimSpace(msg.TopicID) != grant.TopicID {
		return fmt.Errorf("topic_id does not match grant")
	}
	if strings.TrimSpace(msg.TopicType) != "" && strings.TrimSpace(msg.TopicType) != grant.TopicType {
		return fmt.Errorf("topic_type does not match grant")
	}
	deviceID := strings.TrimSpace(msg.DeviceID)
	if deviceID != "" && deviceID != grant.DeviceID {
		return fmt.Errorf("device_id does not match grant")
	}
	for _, allowed := range grant.Operations {
		if allowed == operation {
			return nil
		}
	}
	return fmt.Errorf("operation is not allowed by grant")
}

func isAllowedDeviceRPCOperation(operation DeviceGrantOperation) bool {
	switch operation {
	case DeviceGrantReadFile,
		DeviceGrantGlob,
		DeviceGrantGrep,
		DeviceGrantWriteFile,
		DeviceGrantExecuteShell:
		return true
	default:
		return false
	}
}

func (h *Hub) DeviceRPCStatus(ownerUID int64, agentIDFilter ...string) []DeviceRPCPendingStatus {
	if h == nil || h.deviceRPC == nil || ownerUID <= 0 {
		return nil
	}
	filterAgentID := ""
	if len(agentIDFilter) > 0 {
		filterAgentID = strings.TrimSpace(agentIDFilter[0])
	}
	now := time.Now()
	if h.deviceRPC.now != nil {
		now = h.deviceRPC.now()
	}
	pending := h.deviceRPC.listByOwner(ownerUID)
	out := make([]DeviceRPCPendingStatus, 0, len(pending))
	for _, item := range pending {
		if filterAgentID != "" && item.agentID != filterAgentID {
			continue
		}
		ttl := item.expiresAt.Sub(now).Milliseconds()
		if ttl < 0 {
			ttl = 0
		}
		out = append(out, DeviceRPCPendingStatus{
			RequestID:            item.requestID,
			AgentID:              item.agentID,
			AgentBodyID:          item.agentBodyID,
			ActorUserID:          item.actorUserID,
			OwnerUserID:          item.ownerUserID,
			SessionKey:           item.sessionKey,
			TopicID:              item.topicID,
			TopicType:            item.topicType,
			GrantID:              item.grantID,
			DeviceID:             item.deviceID,
			DeviceBodyID:         item.deviceBodyID,
			DeviceInstallationID: item.deviceInstallID,
			Operation:            item.operation,
			ToolName:             item.toolName,
			CreatedAt:            unixMillis(item.createdAt),
			ExpiresAt:            unixMillis(item.expiresAt),
			TTLMS:                ttl,
			RequesterConnected:   h.routeConnected(item.requesterRoute) || h.isClientRegistered(item.requester) || h.findAgentRPCClient(item.agentUID, item.agentBodyID) != nil,
			TargetConnected:      h.routeConnected(item.targetRoute) || h.pendingMatchesDeviceClient(item, item.target),
		})
	}
	return out
}

func (h *Hub) userDeviceRouteCandidates(ownerUID int64) ([]UserDevice, []UserDevice) {
	if h == nil || h.userDevices == nil || ownerUID <= 0 {
		return nil, nil
	}
	devices := h.userDevices.activeDevices(ownerUID)
	routable, unavailable := h.classifyUserDevices(ownerUID, devices)
	return routableDevices(routable), unavailableDevices(unavailable)
}

func (h *Hub) classifyUserDevices(ownerUID int64, devices []UserDevice) ([]UserDevice, []UserDevice) {
	if h == nil || ownerUID <= 0 || len(devices) == 0 {
		return devices, nil
	}
	routeNow := nowForRoute(h)
	deviceNow := routeNow
	if h.userDevices != nil && h.userDevices.now != nil {
		deviceNow = h.userDevices.now()
	}
	classified := make([]UserDevice, 0, len(devices))
	unavailable := make([]UserDevice, 0)
	for _, device := range devices {
		device.Active = isActiveDevice(device, deviceNow, h.userDevicesTTL())
		route, target := h.findDeviceRPCTarget(ownerUID, device)
		device.RouteConnected = route.validAt(routeNow) && (h.routeConnected(route) || target != nil)
		device.Routable = device.Active && device.RouteConnected
		device.UnavailableReason = ""
		if !device.Active {
			device.UnavailableReason = "not_active"
		} else if !device.RouteConnected {
			device.UnavailableReason = "route_unavailable"
		}
		classified = append(classified, device)
		if !device.Routable {
			unavailable = append(unavailable, device)
		}
	}
	return classified, unavailable
}

func routableDevices(devices []UserDevice) []UserDevice {
	if len(devices) == 0 {
		return nil
	}
	out := make([]UserDevice, 0, len(devices))
	for _, device := range devices {
		if device.Routable {
			out = append(out, device)
		}
	}
	return out
}

func unavailableDevices(devices []UserDevice) []UserDevice {
	if len(devices) == 0 {
		return nil
	}
	out := make([]UserDevice, 0, len(devices))
	for _, device := range devices {
		if !device.Routable {
			out = append(out, device)
		}
	}
	return out
}

func (h *Hub) userDevicesTTL() time.Duration {
	if h == nil || h.userDevices == nil || h.userDevices.ttl <= 0 {
		return defaultUserDeviceTTL
	}
	return h.userDevices.ttl
}

func (h *Hub) runDeviceRPCTimeouts() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		if h != nil && h.deviceRPC != nil && h.deviceRPC.now != nil {
			now = h.deviceRPC.now()
		}
		h.expireDeviceRPCRequests(now)
	}
}

func (h *Hub) expireDeviceRPCRequests(now time.Time) int {
	if h == nil || h.deviceRPC == nil {
		return 0
	}
	expired := h.deviceRPC.expire(now)
	for _, pending := range expired {
		h.notifyDeviceRPCTimeout(pending)
	}
	return len(expired)
}

func (h *Hub) notifyDeviceRPCTimeout(pending deviceRPCPending) {
	if h == nil || pending.requestID == "" {
		return
	}
	requesterRoute := pending.requesterRoute
	requester := pending.requester
	if !requesterRoute.validAt(nowForRoute(h)) {
		if h.isClientRegistered(requester) {
			requesterRoute = h.clientRoute(requester)
		} else {
			requester = h.findAgentRPCClient(pending.agentUID, pending.agentBodyID)
			if requester != nil {
				requesterRoute = h.clientRoute(requester)
			}
		}
	}
	if !requesterRoute.validAt(nowForRoute(h)) {
		return
	}
	h.sendDeviceRPCToRoute(requesterRoute, &MsgDeviceRPC{
		Type:                 deviceRPCTypeResult,
		RequestID:            pending.requestID,
		GrantID:              pending.grantID,
		SessionKey:           pending.sessionKey,
		TopicID:              pending.topicID,
		TopicType:            pending.topicType,
		ActorUserID:          pending.actorUserID,
		AgentID:              pending.agentID,
		AgentBodyID:          pending.agentBodyID,
		DeviceID:             pending.deviceID,
		DeviceBodyID:         pending.deviceBodyID,
		DeviceInstallationID: pending.deviceInstallID,
		Operation:            pending.operation,
		ToolName:             pending.toolName,
		Error: &MsgDeviceRPCError{
			Code:    "device_rpc_timeout",
			Message: "device did not return a result before the request expired",
		},
		CreatedAt: unixMillis(pending.createdAt),
		ExpiresAt: unixMillis(pending.expiresAt),
	})
}

func (h *Hub) findDeviceRPCClient(ownerUID int64, device UserDevice) *Client {
	return h.getDeviceClient(ownerUID, device.DeviceID)
}

func (h *Hub) findDeviceRPCTarget(ownerUID int64, device UserDevice) (runtimeRoute, *Client) {
	if h == nil || ownerUID <= 0 || device.DeviceID == "" {
		return runtimeRoute{}, nil
	}
	now := nowForRoute(h)
	if h.sharedRuntime != nil {
		route, ok := h.sharedRuntime.userDeviceRoute(ownerUID, device.DeviceID, now)
		if !ok {
			return runtimeRoute{}, nil
		}
		return route, h.getClientByConnectionID(route.ConnectionID)
	}
	client := h.findDeviceRPCClient(ownerUID, device)
	if client == nil {
		return runtimeRoute{}, nil
	}
	return h.clientRoute(client), client
}

func (h *Hub) sendDeviceRPCToRoute(route runtimeRoute, msg *MsgDeviceRPC) bool {
	if h == nil || !route.validAt(nowForRoute(h)) || msg == nil {
		return false
	}
	if route.NodeID == "" || route.NodeID == h.nodeID {
		return h.sendDeviceRPCToLocalRoute(route, msg)
	}
	if h.sharedRuntime != nil {
		return h.sharedRuntime.deliverDeviceRPC(route, msg, nowForRoute(h))
	}
	return false
}

func (h *Hub) routeConnected(route runtimeRoute) bool {
	if h == nil || !route.validAt(nowForRoute(h)) {
		return false
	}
	if route.NodeID == "" || route.NodeID == h.nodeID {
		return h.getClientByConnectionID(route.ConnectionID) != nil
	}
	if h.sharedRuntime != nil {
		return h.sharedRuntime.routeConnected(route, nowForRoute(h))
	}
	return false
}

func nowForRoute(h *Hub) time.Time {
	if h != nil && h.deviceRPC != nil && h.deviceRPC.now != nil {
		return h.deviceRPC.now()
	}
	return time.Now()
}

func (h *Hub) findAgentRPCClient(agentUID int64, agentBodyID string) *Client {
	if h == nil || agentUID <= 0 {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients[agentUID] {
		if client == nil {
			continue
		}
		if agentBodyID == "" || client.bodyID == agentBodyID {
			return client
		}
	}
	return nil
}

func (h *Hub) isClientRegistered(client *Client) bool {
	if h == nil || client == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	clients := h.clients[client.uid]
	_, ok := clients[client]
	return ok
}

func (h *Hub) sendDeviceRPCAck(client *Client, id string, code int, text string, params map[string]interface{}) {
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:     id,
			Code:   code,
			Text:   text,
			Params: params,
		},
	})
}

func normalizeDeviceRPCRequestID(value string) (string, bool) {
	requestID := strings.TrimSpace(value)
	if requestID == "" || len(requestID) > maxDeviceRPCRequestIDLength {
		return "", false
	}
	return requestID, true
}

func parseFormattedUID(value string) int64 {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "usr") {
		value = strings.TrimPrefix(value, "usr")
	}
	return parseInt64(value)
}
