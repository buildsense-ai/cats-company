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
	agentUID        int64
	agentID         string
	agentBodyID     string
	actorUID        int64
	actorUserID     string
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
	expiresAt       time.Time
}

type DeviceRPCPendingStatus struct {
	RequestID            string `json:"request_id"`
	AgentID              string `json:"agent_id,omitempty"`
	AgentBodyID          string `json:"agent_body_id,omitempty"`
	ActorUserID          string `json:"actor_user_id,omitempty"`
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

func (r *deviceRPCRouter) add(pending deviceRPCPending) (bool, string) {
	if r == nil || pending.requestID == "" || pending.expiresAt.IsZero() {
		return false, "invalid"
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
		if item.actorUID == pending.actorUID && item.deviceID == pending.deviceID {
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
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, requestID)
}

func (r *deviceRPCRouter) listByActor(actorUID int64) []deviceRPCPending {
	if r == nil || actorUID <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]deviceRPCPending, 0)
	for _, pending := range r.pending {
		if pending.actorUID == actorUID {
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
	if err := validateDeviceRPCGrant(client, msg, grant, operation); err != nil {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, err.Error(), map[string]interface{}{"request_id": requestID})
		return
	}
	actorUID := parseFormattedUID(grant.ActorUserID)
	if actorUID <= 0 {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "invalid actor user", map[string]interface{}{"request_id": requestID})
		return
	}
	device, ok := h.userDevices.activeDevice(actorUID, grant.DeviceID)
	if !ok {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusNotFound, "device offline", map[string]interface{}{"request_id": requestID, "device_id": grant.DeviceID})
		return
	}
	target := h.findDeviceRPCClient(actorUID, device)
	if target == nil {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusNotFound, "device connection unavailable", map[string]interface{}{"request_id": requestID, "device_id": grant.DeviceID})
		return
	}

	now := h.deviceRPC.now()
	expiresAt := now.Add(h.deviceRPC.ttl)
	if grantExpiry := time.UnixMilli(grant.ExpiresAt); grant.ExpiresAt > 0 && grantExpiry.Before(expiresAt) {
		expiresAt = grantExpiry
	}
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
		agentUID:        client.uid,
		agentID:         grant.AgentID,
		agentBodyID:     grant.AgentBodyID,
		actorUID:        actorUID,
		actorUserID:     grant.ActorUserID,
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
		expiresAt:       expiresAt,
	}
	if ok, reason := h.deviceRPC.add(pending); !ok {
		if reason == "agent_limit" || reason == "device_limit" {
			h.sendDeviceRPCAck(client, msg.ID, http.StatusTooManyRequests, "too many pending device rpc requests", map[string]interface{}{"request_id": requestID})
			return
		}
		h.sendDeviceRPCAck(client, msg.ID, http.StatusConflict, "request_id is already pending", map[string]interface{}{"request_id": requestID})
		return
	}

	h.SendToClient(target, &ServerMessage{DeviceRPC: &forward})
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
		h.sendDeviceRPCAck(client, msg.ID, http.StatusForbidden, "result source does not match target device", map[string]interface{}{"request_id": requestID})
		return
	}
	h.deviceRPC.finish(requestID)

	requester := pending.requester
	if !h.isClientRegistered(requester) {
		requester = h.findAgentRPCClient(pending.agentUID, pending.agentBodyID)
	}
	if requester == nil {
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

	h.SendToClient(requester, &ServerMessage{DeviceRPC: &forward})
	h.sendDeviceRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{"request_id": requestID})
}

func (h *Hub) pendingMatchesDeviceClient(pending deviceRPCPending, client *Client) bool {
	if h == nil || client == nil || pending.actorUID <= 0 || pending.deviceID == "" {
		return false
	}
	current := h.getDeviceClient(pending.actorUID, pending.deviceID)
	return current == client && client.deviceOwnerUID == pending.actorUID && client.deviceID == pending.deviceID
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
	case DeviceGrantReadFile, DeviceGrantGlob, DeviceGrantGrep:
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
	pending := h.deviceRPC.listByActor(ownerUID)
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
			RequesterConnected:   h.isClientRegistered(item.requester) || h.findAgentRPCClient(item.agentUID, item.agentBodyID) != nil,
			TargetConnected:      h.pendingMatchesDeviceClient(item, item.target),
		})
	}
	return out
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
	requester := pending.requester
	if !h.isClientRegistered(requester) {
		requester = h.findAgentRPCClient(pending.agentUID, pending.agentBodyID)
	}
	if requester == nil {
		return
	}
	h.SendToClient(requester, &ServerMessage{
		DeviceRPC: &MsgDeviceRPC{
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
		},
	})
}

func (h *Hub) findDeviceRPCClient(ownerUID int64, device UserDevice) *Client {
	return h.getDeviceClient(ownerUID, device.DeviceID)
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
