package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultDeviceRPCTTL         = 60 * time.Second
	maxDeviceRPCRequestIDLength = 128
	deviceRPCTypeRequest        = "request"
	deviceRPCTypeResult         = "result"
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
	target          *Client
	expiresAt       time.Time
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

func (r *deviceRPCRouter) add(pending deviceRPCPending) bool {
	if r == nil || pending.requestID == "" || pending.expiresAt.IsZero() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupLocked(r.now())
	if _, exists := r.pending[pending.requestID]; exists {
		return false
	}
	r.pending[pending.requestID] = pending
	return true
}

func (r *deviceRPCRouter) get(requestID string) (deviceRPCPending, bool) {
	if r == nil || requestID == "" {
		return deviceRPCPending{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.cleanupLocked(now)
	pending, ok := r.pending[requestID]
	if !ok || !now.Before(pending.expiresAt) {
		delete(r.pending, requestID)
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
		target:          target,
		expiresAt:       expiresAt,
	}
	if !h.deviceRPC.add(pending) {
		h.sendDeviceRPCAck(client, msg.ID, http.StatusConflict, "request_id is already pending", map[string]interface{}{"request_id": requestID})
		return
	}

	h.SendToClient(target, &ServerMessage{DeviceRPC: &forward})
	h.sendDeviceRPCAck(client, msg.ID, http.StatusOK, "ok", map[string]interface{}{
		"request_id":             requestID,
		"device_id":              device.DeviceID,
		"device_body_id":         forward.DeviceBodyID,
		"device_installation_id": forward.DeviceInstallationID,
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
