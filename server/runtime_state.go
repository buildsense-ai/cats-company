package server

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type runtimeRoute struct {
	NodeID       string
	ConnectionID string
	ExpiresAt    time.Time
}

func (r runtimeRoute) validAt(now time.Time) bool {
	return r.NodeID != "" && r.ConnectionID != "" && (r.ExpiresAt.IsZero() || now.Before(r.ExpiresAt))
}

func (r runtimeRoute) matches(other runtimeRoute) bool {
	return r.NodeID != "" &&
		r.ConnectionID != "" &&
		r.NodeID == other.NodeID &&
		r.ConnectionID == other.ConnectionID
}

type deviceRPCPendingRecord struct {
	requestID       string
	requesterRoute  runtimeRoute
	targetRoute     runtimeRoute
	agentUID        int64
	agentID         string
	agentBodyID     string
	actorUID        int64
	actorUserID     string
	ownerUID        int64
	ownerUserID     string
	identitySource  string
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
	expiresAt       time.Time
}

type sharedRuntimeState interface {
	runtimeMode() string
	runtimeRouteState() string
	registerRuntimeNode(nodeID string, hub *Hub)
	bindRuntimeRoute(route runtimeRoute, now time.Time)
	clearRuntimeRoute(route runtimeRoute)
	deliverDeviceRPC(route runtimeRoute, msg *MsgDeviceRPC, now time.Time) bool
	routeConnected(route runtimeRoute, now time.Time) bool

	acquireBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) (botBodyLeaseResult, error)
	botBodyLeaseConflict(botUID int64, bodyID string, now time.Time) (botBodyLease, bool)
	botBodyLeaseStatus(botUID int64, now time.Time) (botBodyLease, bool)
	botBodyLeaseIsCurrent(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time) bool
	releaseBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string) bool
	renewBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) bool

	registerUserDevice(ownerUID int64, req RegisterUserDeviceRequest, now time.Time) (UserDevice, error)
	unregisterUserDevice(ownerUID int64, deviceID string)
	listUserDevices(ownerUID int64) []UserDevice
	activeUserDevice(ownerUID int64, deviceID string, now time.Time, ttl time.Duration) (UserDevice, bool)
	touchUserDevice(ownerUID int64, deviceID string, now time.Time)
	bindUserDeviceRoute(ownerUID int64, device UserDevice, route runtimeRoute, now time.Time)
	clearUserDeviceRoute(ownerUID int64, deviceID string, route runtimeRoute)
	userDeviceRoute(ownerUID int64, deviceID string, now time.Time) (runtimeRoute, bool)

	rememberDeviceGrants(grants []ScopedDeviceGrant, now int64)
	lookupDeviceGrant(grantID string, now int64) (ScopedDeviceGrant, bool)
	deviceSelectionPreference(actorUID int64, sessionKey string, now int64) (string, bool)
	rememberDeviceSelection(actorUID int64, sessionKey string, preference deviceSelectionPreference)

	addDeviceRPCPending(pending deviceRPCPendingRecord, now time.Time) (bool, string)
	getDeviceRPCPending(requestID string, now time.Time) (deviceRPCPendingRecord, bool)
	finishDeviceRPCPending(requestID string)
	listDeviceRPCPendingByOwner(ownerUID int64) []deviceRPCPendingRecord
	expireDeviceRPCPending(now time.Time) []deviceRPCPendingRecord
}

type deviceConnectorRuntimeState interface {
	saveDeviceConnectorPairing(pairing deviceConnectorPairing, ttl time.Duration) error
	getDeviceConnectorPairing(pairingID string, now time.Time) (deviceConnectorPairing, bool)
	consumeDeviceConnectorPairing(code string, now time.Time) (deviceConnectorPairing, bool)
	appendDeviceAudit(ownerUID int64, event DeviceAuditEvent)
	listDeviceAudit(ownerUID int64, limit int) []DeviceAuditEvent
	revokeDeviceConnectorDevice(ownerUID int64, deviceID string, revokedAt time.Time)
	revokeDeviceConnectorToken(tokenID string, expiresAt time.Time)
	isDeviceConnectorRevoked(claims *DeviceConnectorClaims, now time.Time) bool
}

type sharedMemoryRuntimeState struct {
	mu sync.Mutex

	nodes map[string]*Hub

	botLeases map[int64]botBodyLease

	devices      map[int64]map[string]UserDevice
	deviceRoutes map[int64]map[string]runtimeRoute
	grants       map[string]ScopedDeviceGrant
	preferences  map[int64]map[string]deviceSelectionPreference

	deviceRPC map[string]deviceRPCPendingRecord

	connectorPairingsByID   map[string]deviceConnectorPairing
	connectorPairingCodeIDs map[string]string
	deviceAuditEvents       []DeviceAuditEvent
	revokedConnectorDevices map[int64]map[string]time.Time
	revokedConnectorTokens  map[string]time.Time
}

func newSharedMemoryRuntimeState() *sharedMemoryRuntimeState {
	return &sharedMemoryRuntimeState{
		nodes:                   make(map[string]*Hub),
		botLeases:               make(map[int64]botBodyLease),
		devices:                 make(map[int64]map[string]UserDevice),
		deviceRoutes:            make(map[int64]map[string]runtimeRoute),
		grants:                  make(map[string]ScopedDeviceGrant),
		preferences:             make(map[int64]map[string]deviceSelectionPreference),
		deviceRPC:               make(map[string]deviceRPCPendingRecord),
		connectorPairingsByID:   make(map[string]deviceConnectorPairing),
		connectorPairingCodeIDs: make(map[string]string),
		revokedConnectorDevices: make(map[int64]map[string]time.Time),
		revokedConnectorTokens:  make(map[string]time.Time),
	}
}

func (s *sharedMemoryRuntimeState) runtimeMode() string {
	return "shared_memory"
}

func (s *sharedMemoryRuntimeState) runtimeRouteState() string {
	if s == nil {
		return "unavailable"
	}
	return "ready"
}

func (s *sharedMemoryRuntimeState) registerRuntimeNode(nodeID string, hub *Hub) {
	if s == nil || nodeID == "" || hub == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[nodeID] = hub
}

func (s *sharedMemoryRuntimeState) bindRuntimeRoute(route runtimeRoute, now time.Time) {
}

func (s *sharedMemoryRuntimeState) clearRuntimeRoute(route runtimeRoute) {
}

func (s *sharedMemoryRuntimeState) deliverDeviceRPC(route runtimeRoute, msg *MsgDeviceRPC, now time.Time) bool {
	if s == nil || !route.validAt(now) {
		return false
	}
	s.mu.Lock()
	hub := s.nodes[route.NodeID]
	s.mu.Unlock()
	if hub == nil {
		return false
	}
	return hub.sendDeviceRPCToLocalRoute(route, msg)
}

func (s *sharedMemoryRuntimeState) routeConnected(route runtimeRoute, now time.Time) bool {
	if s == nil || !route.validAt(now) {
		return false
	}
	s.mu.Lock()
	hub := s.nodes[route.NodeID]
	s.mu.Unlock()
	return hub != nil && hub.getClientByConnectionID(route.ConnectionID) != nil
}

func (s *sharedMemoryRuntimeState) acquireBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) (botBodyLeaseResult, error) {
	if s == nil {
		return botBodyLeaseResult{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.botLeases[botUID]; ok && isSharedLeaseActive(existing, now) {
		if existing.bodyID != bodyID {
			if isLegacyBotBodyID(existing.bodyID) && !isLegacyBotBodyID(bodyID) {
				next := buildSharedBotBodyLease(botUID, bodyID, connectionID, nodeID, now, ttl)
				s.botLeases[botUID] = next
				return botBodyLeaseResult{Lease: next, Replaced: true}, nil
			}
			return botBodyLeaseResult{Lease: existing}, errBotBodyLeaseConflict
		}
		next := buildSharedBotBodyLease(botUID, bodyID, connectionID, nodeID, now, ttl)
		s.botLeases[botUID] = next
		return botBodyLeaseResult{Lease: next, Replaced: true}, nil
	}

	next := buildSharedBotBodyLease(botUID, bodyID, connectionID, nodeID, now, ttl)
	s.botLeases[botUID] = next
	return botBodyLeaseResult{Lease: next}, nil
}

func (s *sharedMemoryRuntimeState) botBodyLeaseConflict(botUID int64, bodyID string, now time.Time) (botBodyLease, bool) {
	if s == nil || botUID <= 0 || bodyID == "" {
		return botBodyLease{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.botLeases[botUID]
	if !ok || !isSharedLeaseActive(existing, now) || existing.bodyID == bodyID {
		return botBodyLease{}, false
	}
	if isLegacyBotBodyID(existing.bodyID) && !isLegacyBotBodyID(bodyID) {
		return botBodyLease{}, false
	}
	return existing, true
}

func (s *sharedMemoryRuntimeState) botBodyLeaseStatus(botUID int64, now time.Time) (botBodyLease, bool) {
	if s == nil || botUID <= 0 {
		return botBodyLease{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.botLeases[botUID]
	if !ok || !isSharedLeaseActive(lease, now) {
		return botBodyLease{}, false
	}
	return lease, true
}

func (s *sharedMemoryRuntimeState) botBodyLeaseIsCurrent(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time) bool {
	if s == nil || botUID <= 0 || bodyID == "" || connectionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.botLeases[botUID]
	return ok &&
		isSharedLeaseActive(lease, now) &&
		lease.bodyID == bodyID &&
		lease.connectionID == connectionID &&
		lease.nodeID == nodeID
}

func (s *sharedMemoryRuntimeState) releaseBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string) bool {
	if s == nil || botUID <= 0 || bodyID == "" || connectionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.botLeases[botUID]
	if !ok || lease.bodyID != bodyID || lease.connectionID != connectionID || lease.nodeID != nodeID {
		return false
	}
	delete(s.botLeases, botUID)
	return true
}

func (s *sharedMemoryRuntimeState) renewBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) bool {
	if s == nil || botUID <= 0 || bodyID == "" || connectionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.botLeases[botUID]
	if !ok || lease.bodyID != bodyID || lease.connectionID != connectionID || lease.nodeID != nodeID {
		return false
	}
	lease.expiresAt = now.Add(ttl)
	s.botLeases[botUID] = lease
	return true
}

func (s *sharedMemoryRuntimeState) registerUserDevice(ownerUID int64, req RegisterUserDeviceRequest, now time.Time) (UserDevice, error) {
	if s == nil || ownerUID <= 0 {
		return UserDevice{}, fmt.Errorf("invalid owner")
	}
	deviceID, err := normalizeUserDeviceID(req.DeviceID)
	if err != nil {
		return UserDevice{}, err
	}
	device := UserDevice{
		Kind:           "user_device",
		Source:         "catscompany",
		OwnerUID:       ownerUID,
		OwnerUserID:    formatUID(ownerUID),
		DeviceID:       deviceID,
		DisplayName:    normalizeDeviceText(req.DisplayName),
		BodyID:         normalizeDeviceText(req.BodyID),
		InstallationID: normalizeDeviceText(req.InstallationID),
		Status:         normalizeDeviceStatus(req.Status),
		Capabilities:   normalizeDeviceCapabilities(req.Capabilities),
		RegisteredAt:   unixMillis(now),
		LastSeenAt:     unixMillis(now),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ownerDevices := s.devices[ownerUID]
	if ownerDevices == nil {
		ownerDevices = make(map[string]UserDevice)
		s.devices[ownerUID] = ownerDevices
	}
	if existing, ok := ownerDevices[deviceID]; ok && existing.RegisteredAt > 0 {
		device.RegisteredAt = existing.RegisteredAt
	}
	ownerDevices[deviceID] = device
	return device, nil
}

func (s *sharedMemoryRuntimeState) unregisterUserDevice(ownerUID int64, deviceID string) {
	if s == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ownerDevices := s.devices[ownerUID]; ownerDevices != nil {
		delete(ownerDevices, normalizedDeviceID)
		if len(ownerDevices) == 0 {
			delete(s.devices, ownerUID)
		}
	}
	if ownerRoutes := s.deviceRoutes[ownerUID]; ownerRoutes != nil {
		delete(ownerRoutes, normalizedDeviceID)
		if len(ownerRoutes) == 0 {
			delete(s.deviceRoutes, ownerUID)
		}
	}
}

func (s *sharedMemoryRuntimeState) listUserDevices(ownerUID int64) []UserDevice {
	if s == nil || ownerUID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ownerDevices := s.devices[ownerUID]
	if len(ownerDevices) == 0 {
		return nil
	}
	out := make([]UserDevice, 0, len(ownerDevices))
	for _, device := range ownerDevices {
		out = append(out, device)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenAt != out[j].LastSeenAt {
			return out[i].LastSeenAt > out[j].LastSeenAt
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

func (s *sharedMemoryRuntimeState) activeUserDevice(ownerUID int64, deviceID string, now time.Time, ttl time.Duration) (UserDevice, bool) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return UserDevice{}, false
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return UserDevice{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[ownerUID][normalizedDeviceID]
	if !ok || !isActiveDevice(device, now, ttl) {
		return UserDevice{}, false
	}
	return device, true
}

func (s *sharedMemoryRuntimeState) touchUserDevice(ownerUID int64, deviceID string, now time.Time) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ownerDevices := s.devices[ownerUID]
	device, ok := ownerDevices[normalizedDeviceID]
	if !ok {
		return
	}
	device.Status = "online"
	device.LastSeenAt = unixMillis(now)
	ownerDevices[normalizedDeviceID] = device
}

func (s *sharedMemoryRuntimeState) bindUserDeviceRoute(ownerUID int64, device UserDevice, route runtimeRoute, now time.Time) {
	if s == nil || ownerUID <= 0 || device.DeviceID == "" || !route.validAt(now) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ownerRoutes := s.deviceRoutes[ownerUID]
	if ownerRoutes == nil {
		ownerRoutes = make(map[string]runtimeRoute)
		s.deviceRoutes[ownerUID] = ownerRoutes
	}
	ownerRoutes[device.DeviceID] = route
}

func (s *sharedMemoryRuntimeState) clearUserDeviceRoute(ownerUID int64, deviceID string, route runtimeRoute) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.deviceRoutes[ownerUID][deviceID]
	if !ok || !current.matches(route) {
		return
	}
	delete(s.deviceRoutes[ownerUID], deviceID)
	if len(s.deviceRoutes[ownerUID]) == 0 {
		delete(s.deviceRoutes, ownerUID)
	}
}

func (s *sharedMemoryRuntimeState) userDeviceRoute(ownerUID int64, deviceID string, now time.Time) (runtimeRoute, bool) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return runtimeRoute{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	route, ok := s.deviceRoutes[ownerUID][deviceID]
	if !ok || !route.validAt(now) {
		return runtimeRoute{}, false
	}
	return route, true
}

func (s *sharedMemoryRuntimeState) rememberDeviceGrants(grants []ScopedDeviceGrant, now int64) {
	if s == nil || len(grants) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for grantID, grant := range s.grants {
		if grant.ExpiresAt <= now {
			delete(s.grants, grantID)
		}
	}
	for _, grant := range grants {
		if grant.GrantID == "" || grant.ExpiresAt <= now {
			continue
		}
		s.grants[grant.GrantID] = grant
	}
}

func (s *sharedMemoryRuntimeState) lookupDeviceGrant(grantID string, now int64) (ScopedDeviceGrant, bool) {
	if s == nil || grantID == "" {
		return ScopedDeviceGrant{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, grant := range s.grants {
		if grant.ExpiresAt <= now {
			delete(s.grants, id)
		}
	}
	grant, ok := s.grants[grantID]
	if !ok || grant.Status != "active" || grant.ExpiresAt <= now {
		return ScopedDeviceGrant{}, false
	}
	return grant, true
}

func (s *sharedMemoryRuntimeState) deviceSelectionPreference(actorUID int64, sessionKey string, now int64) (string, bool) {
	if s == nil || actorUID <= 0 || sessionKey == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preference, ok := s.preferences[actorUID][sessionKey]
	if !ok || preference.DeviceID == "" || preference.ExpiresAt <= now {
		return "", false
	}
	return preference.DeviceID, true
}

func (s *sharedMemoryRuntimeState) rememberDeviceSelection(actorUID int64, sessionKey string, preference deviceSelectionPreference) {
	if s == nil || actorUID <= 0 || sessionKey == "" || preference.DeviceID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actorPreferences := s.preferences[actorUID]
	if actorPreferences == nil {
		actorPreferences = make(map[string]deviceSelectionPreference)
		s.preferences[actorUID] = actorPreferences
	}
	actorPreferences[sessionKey] = preference
}

func (s *sharedMemoryRuntimeState) addDeviceRPCPending(pending deviceRPCPendingRecord, now time.Time) (bool, string) {
	if s == nil || pending.requestID == "" || pending.expiresAt.IsZero() {
		return false, "invalid"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.deviceRPC[pending.requestID]; exists {
		return false, "duplicate"
	}
	var agentCount, deviceCount int
	for _, item := range s.deviceRPC {
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
	s.deviceRPC[pending.requestID] = pending
	return true, ""
}

func (s *sharedMemoryRuntimeState) getDeviceRPCPending(requestID string, now time.Time) (deviceRPCPendingRecord, bool) {
	if s == nil || requestID == "" {
		return deviceRPCPendingRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.deviceRPC[requestID]
	if !ok || !now.Before(pending.expiresAt) {
		return deviceRPCPendingRecord{}, false
	}
	return pending, true
}

func (s *sharedMemoryRuntimeState) finishDeviceRPCPending(requestID string) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deviceRPC, requestID)
}

func (s *sharedMemoryRuntimeState) listDeviceRPCPendingByOwner(ownerUID int64) []deviceRPCPendingRecord {
	if s == nil || ownerUID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]deviceRPCPendingRecord, 0)
	for _, pending := range s.deviceRPC {
		if pending.ownerUID == ownerUID {
			out = append(out, pending)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].createdAt.Before(out[j].createdAt)
	})
	return out
}

func (s *sharedMemoryRuntimeState) expireDeviceRPCPending(now time.Time) []deviceRPCPendingRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var expired []deviceRPCPendingRecord
	for requestID, pending := range s.deviceRPC {
		if !now.Before(pending.expiresAt) {
			expired = append(expired, pending)
			delete(s.deviceRPC, requestID)
		}
	}
	return expired
}

func (s *sharedMemoryRuntimeState) saveDeviceConnectorPairing(pairing deviceConnectorPairing, ttl time.Duration) error {
	if s == nil || pairing.PairingID == "" || pairing.PairingCode == "" {
		return fmt.Errorf("invalid pairing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupConnectorPairingsLocked(time.Now())
	s.connectorPairingsByID[pairing.PairingID] = pairing
	s.connectorPairingCodeIDs[pairing.PairingCode] = pairing.PairingID
	return nil
}

func (s *sharedMemoryRuntimeState) getDeviceConnectorPairing(pairingID string, now time.Time) (deviceConnectorPairing, bool) {
	if s == nil || pairingID == "" {
		return deviceConnectorPairing{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupConnectorPairingsLocked(now)
	pairing, ok := s.connectorPairingsByID[pairingID]
	if !ok || !now.Before(pairing.ExpiresAt) {
		return deviceConnectorPairing{}, false
	}
	return pairing, true
}

func (s *sharedMemoryRuntimeState) consumeDeviceConnectorPairing(code string, now time.Time) (deviceConnectorPairing, bool) {
	if s == nil || code == "" {
		return deviceConnectorPairing{}, false
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupConnectorPairingsLocked(now)
	pairingID := s.connectorPairingCodeIDs[normalized]
	pairing, ok := s.connectorPairingsByID[pairingID]
	if !ok || !now.Before(pairing.ExpiresAt) || !pairing.ConsumedAt.IsZero() {
		return deviceConnectorPairing{}, false
	}
	pairing.ConsumedAt = now
	s.connectorPairingsByID[pairing.PairingID] = pairing
	delete(s.connectorPairingCodeIDs, normalized)
	return pairing, true
}

func (s *sharedMemoryRuntimeState) cleanupConnectorPairingsLocked(now time.Time) {
	for pairingID, pairing := range s.connectorPairingsByID {
		if now.Before(pairing.ExpiresAt) {
			continue
		}
		delete(s.connectorPairingsByID, pairingID)
		delete(s.connectorPairingCodeIDs, pairing.PairingCode)
	}
}

func (s *sharedMemoryRuntimeState) appendDeviceAudit(ownerUID int64, event DeviceAuditEvent) {
	if s == nil || ownerUID <= 0 {
		return
	}
	event.OwnerUserID = formatUID(ownerUID)
	if event.CreatedAt == 0 {
		event.CreatedAt = unixMillis(time.Now())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceAuditEvents = append(s.deviceAuditEvents, event)
	if len(s.deviceAuditEvents) > maxDeviceAuditEvents {
		s.deviceAuditEvents = append([]DeviceAuditEvent(nil), s.deviceAuditEvents[len(s.deviceAuditEvents)-maxDeviceAuditEvents:]...)
	}
}

func (s *sharedMemoryRuntimeState) listDeviceAudit(ownerUID int64, limit int) []DeviceAuditEvent {
	if s == nil || ownerUID <= 0 {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	ownerUserID := formatUID(ownerUID)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeviceAuditEvent, 0, limit)
	for i := len(s.deviceAuditEvents) - 1; i >= 0 && len(out) < limit; i-- {
		if s.deviceAuditEvents[i].OwnerUserID == ownerUserID {
			out = append(out, s.deviceAuditEvents[i])
		}
	}
	return out
}

func (s *sharedMemoryRuntimeState) revokeDeviceConnectorDevice(ownerUID int64, deviceID string, revokedAt time.Time) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ownerDevices := s.revokedConnectorDevices[ownerUID]
	if ownerDevices == nil {
		ownerDevices = make(map[string]time.Time)
		s.revokedConnectorDevices[ownerUID] = ownerDevices
	}
	ownerDevices[deviceID] = revokedAt
}

func (s *sharedMemoryRuntimeState) revokeDeviceConnectorToken(tokenID string, expiresAt time.Time) {
	if s == nil || tokenID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokedConnectorTokens[tokenID] = expiresAt
	s.cleanupConnectorTokenRevokesLocked(time.Now())
}

func (s *sharedMemoryRuntimeState) isDeviceConnectorRevoked(claims *DeviceConnectorClaims, now time.Time) bool {
	if s == nil || claims == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupConnectorTokenRevokesLocked(now)
	if expiresAt, ok := s.revokedConnectorTokens[claims.ID]; ok && now.Before(expiresAt) {
		return true
	}
	revokedAt, ok := s.revokedConnectorDevices[claims.UID][claims.DeviceID]
	if !ok {
		return false
	}
	if claims.IssuedAt == nil {
		return true
	}
	return !claims.IssuedAt.Time.After(revokedAt)
}

func (s *sharedMemoryRuntimeState) cleanupConnectorTokenRevokesLocked(now time.Time) {
	for tokenID, expiresAt := range s.revokedConnectorTokens {
		if !expiresAt.IsZero() && now.After(expiresAt) {
			delete(s.revokedConnectorTokens, tokenID)
		}
	}
}

func buildSharedBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) botBodyLease {
	return botBodyLease{
		botUID:       botUID,
		bodyID:       bodyID,
		connectionID: connectionID,
		nodeID:       nodeID,
		acquiredAt:   now,
		expiresAt:    now.Add(ttl),
	}
}

func isSharedLeaseActive(lease botBodyLease, now time.Time) bool {
	return lease.bodyID != "" && (lease.expiresAt.IsZero() || now.Before(lease.expiresAt))
}

func newRuntimeNodeID() string {
	if nodeID := strings.TrimSpace(os.Getenv("CATSCO_NODE_ID")); nodeID != "" {
		return nodeID
	}
	if suffix, err := randomHex(6); err == nil && suffix != "" {
		return "node-" + suffix
	}
	return fmt.Sprintf("node-%d", time.Now().UnixNano())
}

func deviceRPCRecordFromPending(pending deviceRPCPending) deviceRPCPendingRecord {
	return deviceRPCPendingRecord{
		requestID:       pending.requestID,
		requesterRoute:  pending.requesterRoute,
		targetRoute:     pending.targetRoute,
		agentUID:        pending.agentUID,
		agentID:         pending.agentID,
		agentBodyID:     pending.agentBodyID,
		actorUID:        pending.actorUID,
		actorUserID:     pending.actorUserID,
		ownerUID:        pending.ownerUID,
		ownerUserID:     pending.ownerUserID,
		identitySource:  pending.identitySource,
		sessionKey:      pending.sessionKey,
		topicID:         pending.topicID,
		topicType:       pending.topicType,
		grantID:         pending.grantID,
		deviceID:        pending.deviceID,
		deviceBodyID:    pending.deviceBodyID,
		deviceInstallID: pending.deviceInstallID,
		operation:       pending.operation,
		toolName:        pending.toolName,
		createdAt:       pending.createdAt,
		expiresAt:       pending.expiresAt,
	}
}

func pendingFromDeviceRPCRecord(record deviceRPCPendingRecord) deviceRPCPending {
	return deviceRPCPending{
		requestID:       record.requestID,
		requesterRoute:  record.requesterRoute,
		targetRoute:     record.targetRoute,
		agentUID:        record.agentUID,
		agentID:         record.agentID,
		agentBodyID:     record.agentBodyID,
		actorUID:        record.actorUID,
		actorUserID:     record.actorUserID,
		ownerUID:        record.ownerUID,
		ownerUserID:     record.ownerUserID,
		identitySource:  record.identitySource,
		sessionKey:      record.sessionKey,
		topicID:         record.topicID,
		topicType:       record.topicType,
		grantID:         record.grantID,
		deviceID:        record.deviceID,
		deviceBodyID:    record.deviceBodyID,
		deviceInstallID: record.deviceInstallID,
		operation:       record.operation,
		toolName:        record.toolName,
		createdAt:       record.createdAt,
		expiresAt:       record.expiresAt,
	}
}
