package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	deviceConnectorTokenType = "device_connector"
	devicePairingTTL         = 5 * time.Minute
	deviceConnectorTokenTTL  = 30 * 24 * time.Hour
	maxDeviceAuditEvents     = 500
	devicePairingCodeBytes   = 8
)

var defaultDeviceConnectorScopes = []string{
	"device:ws",
	"device:register",
	"device:rpc_result",
	"device:refresh",
}

// DeviceConnectorClaims is a restricted credential for one user's one local device.
// It must not be accepted by ordinary user/chat HTTP endpoints.
type DeviceConnectorClaims struct {
	TokenType      string   `json:"token_type"`
	UID            int64    `json:"userId"`
	Username       string   `json:"username,omitempty"`
	DeviceID       string   `json:"deviceId"`
	InstallationID string   `json:"installationId,omitempty"`
	DisplayName    string   `json:"displayName,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
	jwt.RegisteredClaims
}

type DeviceConnectorTokenInput struct {
	UID            int64
	Username       string
	DeviceID       string
	InstallationID string
	DisplayName    string
	Capabilities   []string
	Scopes         []string
	TTL            time.Duration
}

func GenerateDeviceConnectorToken(input DeviceConnectorTokenInput) (string, error) {
	if input.UID <= 0 {
		return "", fmt.Errorf("user id is required")
	}
	deviceID, err := normalizeUserDeviceID(input.DeviceID)
	if err != nil {
		return "", err
	}
	installationID := normalizeDeviceText(firstNonEmpty(input.InstallationID, deviceID))
	ttl := input.TTL
	if ttl <= 0 {
		ttl = deviceConnectorTokenTTL
	}
	scopes := normalizeDeviceConnectorScopes(input.Scopes)
	capabilities := normalizeDeviceConnectorCapabilityStrings(input.Capabilities)
	jti, err := randomHex(12)
	if err != nil {
		return "", err
	}
	claims := DeviceConnectorClaims{
		TokenType:      deviceConnectorTokenType,
		UID:            input.UID,
		Username:       normalizeDeviceText(input.Username),
		DeviceID:       deviceID,
		InstallationID: installationID,
		DisplayName:    normalizeDeviceText(input.DisplayName),
		Capabilities:   capabilities,
		Scopes:         scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "dev_" + jti,
			Issuer:    "catscompany",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func ParseDeviceConnectorToken(tokenStr string) (*DeviceConnectorClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &DeviceConnectorClaims{}, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*DeviceConnectorClaims)
	if !ok || !token.Valid {
		return nil, jwt.ErrSignatureInvalid
	}
	if claims.TokenType != deviceConnectorTokenType {
		return nil, fmt.Errorf("invalid device connector token type")
	}
	if claims.UID <= 0 || strings.TrimSpace(claims.DeviceID) == "" {
		return nil, fmt.Errorf("invalid device connector token")
	}
	return claims, nil
}

func extractDeviceConnectorToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	if token := strings.TrimSpace(r.Header.Get("X-CatsCo-Connector-Token")); token != "" {
		return token
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "DeviceConnector ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "DeviceConnector "))
	}
	return strings.TrimSpace(r.URL.Query().Get("connector_token"))
}

func normalizeDeviceConnectorScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return append([]string(nil), defaultDeviceConnectorScopes...)
	}
	allowed := map[string]struct{}{
		"device:ws":         {},
		"device:register":   {},
		"device:rpc_result": {},
		"device:refresh":    {},
	}
	out := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		text := strings.TrimSpace(scope)
		if _, ok := allowed[text]; !ok {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	if len(out) == 0 {
		return append([]string(nil), defaultDeviceConnectorScopes...)
	}
	return out
}

func normalizeDeviceConnectorCapabilityStrings(values []string) []string {
	if len(values) == 0 {
		values = []string{"read_file", "glob", "grep"}
	}
	ops := normalizeDeviceCapabilities(values)
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		if isAllowedDeviceRPCOperation(op) {
			out = append(out, string(op))
		}
	}
	if len(out) == 0 {
		return []string{"read_file"}
	}
	return out
}

func deviceConnectorHasScope(claims *DeviceConnectorClaims, scope string) bool {
	if claims == nil {
		return false
	}
	for _, item := range claims.Scopes {
		if item == scope {
			return true
		}
	}
	return false
}

type deviceConnectorPairing struct {
	PairingID    string
	PairingCode  string
	OwnerUID     int64
	Username     string
	DeviceName   string
	Capabilities []string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	ConsumedAt   time.Time
}

type deviceConnectorPairingStore struct {
	mu     sync.Mutex
	now    func() time.Time
	byID   map[string]*deviceConnectorPairing
	byCode map[string]*deviceConnectorPairing
	ttl    time.Duration
}

func newDeviceConnectorPairingStore(ttl time.Duration) *deviceConnectorPairingStore {
	if ttl <= 0 {
		ttl = devicePairingTTL
	}
	return &deviceConnectorPairingStore{
		now:    time.Now,
		byID:   make(map[string]*deviceConnectorPairing),
		byCode: make(map[string]*deviceConnectorPairing),
		ttl:    ttl,
	}
}

func (s *deviceConnectorPairingStore) create(ownerUID int64, username string, deviceName string, capabilities []string) (deviceConnectorPairing, error) {
	if s == nil || ownerUID <= 0 {
		return deviceConnectorPairing{}, fmt.Errorf("invalid owner")
	}
	now := s.now()
	pairing, err := newDeviceConnectorPairing(ownerUID, username, deviceName, capabilities, now, s.ttl)
	if err != nil {
		return deviceConnectorPairing{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	s.byID[pairing.PairingID] = &pairing
	s.byCode[pairing.PairingCode] = &pairing
	return pairing, nil
}

func (s *deviceConnectorPairingStore) get(pairingID string) (deviceConnectorPairing, bool) {
	if s == nil || strings.TrimSpace(pairingID) == "" {
		return deviceConnectorPairing{}, false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	pairing, ok := s.byID[strings.TrimSpace(pairingID)]
	if !ok || !now.Before(pairing.ExpiresAt) {
		return deviceConnectorPairing{}, false
	}
	return *pairing, true
}

func (s *deviceConnectorPairingStore) consume(code string) (deviceConnectorPairing, bool) {
	if s == nil || strings.TrimSpace(code) == "" {
		return deviceConnectorPairing{}, false
	}
	now := s.now()
	normalized := strings.ToUpper(strings.TrimSpace(code))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	pairing, ok := s.byCode[normalized]
	if !ok || !now.Before(pairing.ExpiresAt) || !pairing.ConsumedAt.IsZero() {
		return deviceConnectorPairing{}, false
	}
	pairing.ConsumedAt = now
	delete(s.byCode, normalized)
	return *pairing, true
}

func (s *deviceConnectorPairingStore) cleanupLocked(now time.Time) {
	for id, pairing := range s.byID {
		if now.Before(pairing.ExpiresAt) {
			continue
		}
		delete(s.byID, id)
		delete(s.byCode, pairing.PairingCode)
	}
}

func newDeviceConnectorPairing(ownerUID int64, username string, deviceName string, capabilities []string, now time.Time, ttl time.Duration) (deviceConnectorPairing, error) {
	if ownerUID <= 0 {
		return deviceConnectorPairing{}, fmt.Errorf("invalid owner")
	}
	if ttl <= 0 {
		ttl = devicePairingTTL
	}
	idSuffix, err := randomHex(10)
	if err != nil {
		return deviceConnectorPairing{}, err
	}
	codeSuffix, err := randomHex(devicePairingCodeBytes)
	if err != nil {
		return deviceConnectorPairing{}, err
	}
	return deviceConnectorPairing{
		PairingID:    "pair_" + idSuffix,
		PairingCode:  strings.ToUpper(codeSuffix),
		OwnerUID:     ownerUID,
		Username:     normalizeDeviceText(username),
		DeviceName:   normalizeDeviceText(deviceName),
		Capabilities: normalizeDeviceConnectorCapabilityStrings(capabilities),
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}, nil
}

type DeviceAuditEvent struct {
	ID          string `json:"id"`
	OwnerUserID string `json:"owner_user_id"`
	ActorUserID string `json:"actor_user_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	SessionKey  string `json:"session_key,omitempty"`
	Operation   string `json:"operation,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	Phase       string `json:"phase"`
	Result      string `json:"result,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type deviceAuditLog struct {
	mu     sync.Mutex
	now    func() time.Time
	events []DeviceAuditEvent
}

func newDeviceAuditLog() *deviceAuditLog {
	return &deviceAuditLog{now: time.Now}
}

func (l *deviceAuditLog) add(ownerUID int64, event DeviceAuditEvent) {
	if l == nil || ownerUID <= 0 {
		return
	}
	if event.ID == "" {
		if suffix, err := randomHex(8); err == nil {
			event.ID = "audit_" + suffix
		}
	}
	if event.CreatedAt == 0 {
		event.CreatedAt = unixMillis(l.now())
	}
	event.OwnerUserID = formatUID(ownerUID)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	if len(l.events) > maxDeviceAuditEvents {
		l.events = append([]DeviceAuditEvent(nil), l.events[len(l.events)-maxDeviceAuditEvents:]...)
	}
}

func (l *deviceAuditLog) list(ownerUID int64, limit int) []DeviceAuditEvent {
	if l == nil || ownerUID <= 0 {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	ownerUserID := formatUID(ownerUID)
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]DeviceAuditEvent, 0, limit)
	for i := len(l.events) - 1; i >= 0 && len(out) < limit; i-- {
		if l.events[i].OwnerUserID == ownerUserID {
			out = append(out, l.events[i])
		}
	}
	return out
}

func (h *Hub) connectorRuntime() deviceConnectorRuntimeState {
	if h == nil || h.sharedRuntime == nil {
		return nil
	}
	state, _ := h.sharedRuntime.(deviceConnectorRuntimeState)
	return state
}

func (h *Hub) addDeviceAudit(ownerUID int64, event DeviceAuditEvent) {
	if h == nil || ownerUID <= 0 {
		return
	}
	if state := h.connectorRuntime(); state != nil {
		state.appendDeviceAudit(ownerUID, event)
		return
	}
	if h.deviceAudit != nil {
		h.deviceAudit.add(ownerUID, event)
	}
}

func (h *Hub) listDeviceAudit(ownerUID int64, limit int) []DeviceAuditEvent {
	if h == nil || ownerUID <= 0 {
		return nil
	}
	if state := h.connectorRuntime(); state != nil {
		return state.listDeviceAudit(ownerUID, limit)
	}
	if h.deviceAudit == nil {
		return nil
	}
	return h.deviceAudit.list(ownerUID, limit)
}

func (h *Hub) revokeDeviceConnectorDevice(ownerUID int64, deviceID string) {
	if h == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	now := time.Now()
	if state := h.connectorRuntime(); state != nil {
		state.revokeDeviceConnectorDevice(ownerUID, deviceID, now)
		return
	}
	if h.deviceRevokes != nil {
		h.deviceRevokes.revokeDevice(ownerUID, deviceID)
	}
}

func (h *Hub) revokeDeviceConnectorToken(tokenID string, expiresAt time.Time) {
	if h == nil || strings.TrimSpace(tokenID) == "" {
		return
	}
	if state := h.connectorRuntime(); state != nil {
		state.revokeDeviceConnectorToken(tokenID, expiresAt)
		return
	}
	if h.deviceRevokes != nil {
		h.deviceRevokes.revokeToken(tokenID, expiresAt)
	}
}

func (h *Hub) isDeviceConnectorRevoked(claims *DeviceConnectorClaims) bool {
	if h == nil || claims == nil {
		return false
	}
	now := time.Now()
	if state := h.connectorRuntime(); state != nil {
		return state.isDeviceConnectorRevoked(claims, now)
	}
	return h.deviceRevokes != nil && h.deviceRevokes.isRevoked(claims)
}

type deviceConnectorRevocationList struct {
	mu      sync.Mutex
	now     func() time.Time
	devices map[int64]map[string]time.Time
	tokens  map[string]time.Time
}

func newDeviceConnectorRevocationList() *deviceConnectorRevocationList {
	return &deviceConnectorRevocationList{
		now:     time.Now,
		devices: make(map[int64]map[string]time.Time),
		tokens:  make(map[string]time.Time),
	}
}

func (r *deviceConnectorRevocationList) revokeDevice(ownerUID int64, deviceID string) {
	if r == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ownerDevices := r.devices[ownerUID]
	if ownerDevices == nil {
		ownerDevices = make(map[string]time.Time)
		r.devices[ownerUID] = ownerDevices
	}
	ownerDevices[normalizedDeviceID] = r.now()
}

func (r *deviceConnectorRevocationList) revokeToken(tokenID string, expiresAt time.Time) {
	if r == nil || strings.TrimSpace(tokenID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[strings.TrimSpace(tokenID)] = expiresAt
	r.cleanupLocked(r.now())
}

func (r *deviceConnectorRevocationList) isRevoked(claims *DeviceConnectorClaims) bool {
	if r == nil || claims == nil {
		return false
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupLocked(now)
	if expiresAt, ok := r.tokens[claims.ID]; ok && now.Before(expiresAt) {
		return true
	}
	ownerDevices := r.devices[claims.UID]
	revokedAt, ok := ownerDevices[claims.DeviceID]
	if !ok {
		return false
	}
	issuedAt := time.Time{}
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	return issuedAt.IsZero() || !issuedAt.After(revokedAt)
}

func (r *deviceConnectorRevocationList) cleanupLocked(now time.Time) {
	for tokenID, expiresAt := range r.tokens {
		if !expiresAt.IsZero() && now.After(expiresAt) {
			delete(r.tokens, tokenID)
		}
	}
}

type DeviceConnectorHandler struct {
	db       store.Store
	hub      *Hub
	pairings *deviceConnectorPairingStore
}

func NewDeviceConnectorHandler(db store.Store, hub *Hub) *DeviceConnectorHandler {
	return &DeviceConnectorHandler{
		db:       db,
		hub:      hub,
		pairings: newDeviceConnectorPairingStore(devicePairingTTL),
	}
}

func (h *DeviceConnectorHandler) connectorRuntime() deviceConnectorRuntimeState {
	if h == nil || h.hub == nil || h.hub.sharedRuntime == nil {
		return nil
	}
	state, _ := h.hub.sharedRuntime.(deviceConnectorRuntimeState)
	return state
}

func (h *DeviceConnectorHandler) createPairing(ownerUID int64, username string, deviceName string, capabilities []string) (deviceConnectorPairing, error) {
	if state := h.connectorRuntime(); state != nil {
		pairing, err := newDeviceConnectorPairing(ownerUID, username, deviceName, capabilities, time.Now(), devicePairingTTL)
		if err != nil {
			return deviceConnectorPairing{}, err
		}
		if err := state.saveDeviceConnectorPairing(pairing, devicePairingTTL); err != nil {
			return deviceConnectorPairing{}, err
		}
		return pairing, nil
	}
	return h.pairings.create(ownerUID, username, deviceName, capabilities)
}

func (h *DeviceConnectorHandler) getPairing(pairingID string) (deviceConnectorPairing, bool) {
	if state := h.connectorRuntime(); state != nil {
		return state.getDeviceConnectorPairing(pairingID, time.Now())
	}
	return h.pairings.get(pairingID)
}

func (h *DeviceConnectorHandler) consumePairing(code string) (deviceConnectorPairing, bool) {
	if state := h.connectorRuntime(); state != nil {
		return state.consumeDeviceConnectorPairing(code, time.Now())
	}
	return h.pairings.consume(code)
}

func (h *DeviceConnectorHandler) HandleCreatePairing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	user, status, msg := activeUserByID(uid, h.db.GetUser)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if user.AccountType != types.AccountHuman {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device pairing requires a human user token"})
		return
	}
	var req struct {
		DeviceName   string   `json:"device_name"`
		Capabilities []string `json:"capabilities"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	pairing, err := h.createPairing(uid, user.Username, req.DeviceName, req.Capabilities)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create pairing"})
		return
	}
	if h.hub != nil {
		h.hub.addDeviceAudit(uid, DeviceAuditEvent{
			Phase:  "pairing_created",
			Result: "ok",
			Reason: pairing.PairingID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pairing_id":   pairing.PairingID,
		"pairing_code": pairing.PairingCode,
		"expires_at":   unixMillis(pairing.ExpiresAt),
		"capabilities": pairing.Capabilities,
	})
}

func (h *DeviceConnectorHandler) HandlePairingByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	pairingID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/device-connectors/pairings/"), "/")
	pairing, ok := h.getPairing(pairingID)
	if !ok || pairing.OwnerUID != UIDFromContext(r.Context()) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pairing not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pairing_id":   pairing.PairingID,
		"status":       pairingStatus(pairing),
		"expires_at":   unixMillis(pairing.ExpiresAt),
		"capabilities": pairing.Capabilities,
	})
}

func (h *DeviceConnectorHandler) HandleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		PairingCode    string   `json:"pairing_code"`
		DeviceID       string   `json:"device_id"`
		InstallationID string   `json:"installation_id"`
		DeviceName     string   `json:"device_name"`
		Capabilities   []string `json:"capabilities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	pairing, ok := h.consumePairing(req.PairingCode)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid or expired pairing code"})
		return
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		deviceID = "device_" + strings.ToLower(randomDeviceGrantIDSuffix())
	}
	installationID := firstNonEmpty(req.InstallationID, deviceID)
	deviceName := firstNonEmpty(req.DeviceName, pairing.DeviceName, deviceID)
	var capabilities []string
	if len(req.Capabilities) == 0 {
		capabilities = pairing.Capabilities
	} else {
		capabilities = limitConnectorCapabilities(req.Capabilities, pairing.Capabilities)
	}
	device, err := h.hub.userDevices.register(pairing.OwnerUID, RegisterUserDeviceRequest{
		DeviceID:       deviceID,
		DisplayName:    deviceName,
		BodyID:         deviceID,
		InstallationID: installationID,
		Status:         "online",
		Capabilities:   capabilities,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	token, err := GenerateDeviceConnectorToken(DeviceConnectorTokenInput{
		UID:            pairing.OwnerUID,
		Username:       pairing.Username,
		DeviceID:       device.DeviceID,
		InstallationID: device.InstallationID,
		DisplayName:    device.DisplayName,
		Capabilities:   capabilities,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to issue device token"})
		return
	}
	if h.hub != nil {
		h.hub.addDeviceAudit(pairing.OwnerUID, DeviceAuditEvent{
			DeviceID: device.DeviceID,
			Phase:    "device_enrolled",
			Result:   "ok",
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"connector_token": token,
		"token_type":      deviceConnectorTokenType,
		"expires_in":      int(deviceConnectorTokenTTL.Seconds()),
		"device":          device,
	})
}

func (h *DeviceConnectorHandler) HandleRefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	claims, status, msg := h.verifyConnectorRequest(r, "device:refresh")
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	token, err := GenerateDeviceConnectorToken(DeviceConnectorTokenInput{
		UID:            claims.UID,
		Username:       claims.Username,
		DeviceID:       claims.DeviceID,
		InstallationID: claims.InstallationID,
		DisplayName:    claims.DisplayName,
		Capabilities:   claims.Capabilities,
		Scopes:         claims.Scopes,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to refresh token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"connector_token": token,
		"token_type":      deviceConnectorTokenType,
		"expires_in":      int(deviceConnectorTokenTTL.Seconds()),
	})
}

func (h *DeviceConnectorHandler) HandleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	claims, status, msg := h.verifyConnectorRequest(r, "device:register")
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	var req RegisterUserDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.DeviceID = claims.DeviceID
	req.BodyID = claims.DeviceID
	req.InstallationID = firstNonEmpty(claims.InstallationID, claims.DeviceID)
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = claims.DisplayName
	}
	if len(req.Capabilities) == 0 {
		req.Capabilities = claims.Capabilities
	} else {
		req.Capabilities = limitConnectorCapabilities(req.Capabilities, claims.Capabilities)
	}
	device, err := h.hub.userDevices.register(claims.UID, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"device": device})
}

func (h *DeviceConnectorHandler) HandleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if h.hub == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"events": []DeviceAuditEvent{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": h.hub.listDeviceAudit(uid, parseIntDefault(r.URL.Query().Get("limit"), 100)),
	})
}

func (h *DeviceConnectorHandler) HandleReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	base := strings.TrimRight(os.Getenv("CATSCO_DEVICE_CONNECTOR_RELEASE_BASE_URL"), "/")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":    "CatsCo Device Connector",
		"version": os.Getenv("CATSCO_DEVICE_CONNECTOR_VERSION"),
		"downloads": map[string]string{
			"windows": releaseURL(base, "windows"),
			"macos":   releaseURL(base, "macos"),
			"linux":   releaseURL(base, "linux"),
		},
	})
}

func (h *DeviceConnectorHandler) verifyConnectorRequest(r *http.Request, scope string) (*DeviceConnectorClaims, int, string) {
	if h == nil || h.db == nil || h.hub == nil || h.hub.userDevices == nil {
		return nil, http.StatusInternalServerError, "device connector unavailable"
	}
	token := extractDeviceConnectorToken(r)
	if token == "" {
		return nil, http.StatusUnauthorized, "missing device connector token"
	}
	claims, err := ParseDeviceConnectorToken(token)
	if err != nil {
		return nil, http.StatusUnauthorized, "invalid device connector token"
	}
	if !deviceConnectorHasScope(claims, scope) {
		return nil, http.StatusForbidden, "device connector token does not allow this action"
	}
	if h.hub.isDeviceConnectorRevoked(claims) {
		return nil, http.StatusForbidden, "device connector token has been revoked"
	}
	if _, status, msg := activeUserByID(claims.UID, h.db.GetUser); status != 0 {
		return nil, status, msg
	}
	return claims, 0, ""
}

func pairingStatus(pairing deviceConnectorPairing) string {
	if !pairing.ConsumedAt.IsZero() {
		return "consumed"
	}
	if time.Now().After(pairing.ExpiresAt) {
		return "expired"
	}
	return "pending"
}

func limitConnectorCapabilities(requested []string, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[strings.TrimSpace(value)] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, value := range normalizeDeviceConnectorCapabilityStrings(requested) {
		if _, ok := allowedSet[value]; ok {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return allowed
	}
	return out
}

func parseIntDefault(value string, fallback int) int {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &n); err != nil || n <= 0 {
		return fallback
	}
	return n
}

func releaseURL(base string, platform string) string {
	if base == "" {
		return ""
	}
	return base + "/catsco-device-connector-" + platform
}
