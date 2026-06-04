package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultUserDeviceTTL       = 5 * time.Minute
	defaultDeviceGrantTTL      = 10 * time.Minute
	maxUserDeviceIDLength      = 128
	deviceGrantIDRandomLength  = 12
	userDeviceGrantIdentitySrc = "metadata.catsco_identity"
)

var userDeviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type DeviceGrantOperation string

const (
	DeviceGrantReadFile       DeviceGrantOperation = "read_file"
	DeviceGrantWriteFile      DeviceGrantOperation = "write_file"
	DeviceGrantEditFile       DeviceGrantOperation = "edit_file"
	DeviceGrantSendFile       DeviceGrantOperation = "send_file"
	DeviceGrantExecuteShell   DeviceGrantOperation = "execute_shell"
	DeviceGrantGlob           DeviceGrantOperation = "glob"
	DeviceGrantGrep           DeviceGrantOperation = "grep"
	DeviceGrantBrowserControl DeviceGrantOperation = "browser_control"
	DeviceGrantDesktopControl DeviceGrantOperation = "desktop_control"
)

type UserDevice struct {
	Kind           string                 `json:"kind"`
	Source         string                 `json:"source"`
	OwnerUID       int64                  `json:"-"`
	OwnerUserID    string                 `json:"ownerUserId"`
	DeviceID       string                 `json:"deviceId"`
	DisplayName    string                 `json:"displayName,omitempty"`
	BodyID         string                 `json:"bodyId,omitempty"`
	InstallationID string                 `json:"installationId,omitempty"`
	Status         string                 `json:"status"`
	Capabilities   []DeviceGrantOperation `json:"capabilities,omitempty"`
	RegisteredAt   int64                  `json:"registeredAt"`
	LastSeenAt     int64                  `json:"lastSeenAt,omitempty"`
}

type ScopedDeviceGrant struct {
	Kind                 string                 `json:"kind"`
	Source               string                 `json:"source"`
	GrantID              string                 `json:"grantId"`
	Status               string                 `json:"status"`
	IdentityTrust        string                 `json:"identityTrust"`
	IdentitySource       string                 `json:"identitySource,omitempty"`
	DeviceID             string                 `json:"deviceId"`
	DeviceDisplayName    string                 `json:"deviceDisplayName,omitempty"`
	DeviceBodyID         string                 `json:"deviceBodyId,omitempty"`
	DeviceInstallationID string                 `json:"deviceInstallationId,omitempty"`
	OwnerUserID          string                 `json:"ownerUserId"`
	SessionKey           string                 `json:"sessionKey"`
	TopicID              string                 `json:"topicId"`
	TopicType            string                 `json:"topicType"`
	ActorUserID          string                 `json:"actorUserId"`
	AgentID              string                 `json:"agentId,omitempty"`
	AgentBodyID          string                 `json:"agentBodyId,omitempty"`
	Operations           []DeviceGrantOperation `json:"operations"`
	CreatedAt            int64                  `json:"createdAt"`
	ExpiresAt            int64                  `json:"expiresAt"`
}

type RegisterUserDeviceRequest struct {
	DeviceID       string   `json:"device_id"`
	DisplayName    string   `json:"display_name,omitempty"`
	BodyID         string   `json:"body_id,omitempty"`
	InstallationID string   `json:"installation_id,omitempty"`
	Status         string   `json:"status,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
}

type userDeviceRegistry struct {
	mu      sync.RWMutex
	ttl     time.Duration
	grantTT time.Duration
	now     func() time.Time
	devices map[int64]map[string]UserDevice
}

func newUserDeviceRegistry(ttl time.Duration) *userDeviceRegistry {
	if ttl <= 0 {
		ttl = defaultUserDeviceTTL
	}
	return &userDeviceRegistry{
		ttl:     ttl,
		grantTT: defaultDeviceGrantTTL,
		now:     time.Now,
		devices: make(map[int64]map[string]UserDevice),
	}
}

func (r *userDeviceRegistry) register(ownerUID int64, req RegisterUserDeviceRequest) (UserDevice, error) {
	if r == nil || ownerUID <= 0 {
		return UserDevice{}, fmt.Errorf("invalid owner")
	}
	deviceID, err := normalizeUserDeviceID(req.DeviceID)
	if err != nil {
		return UserDevice{}, err
	}
	now := r.now()
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

	r.mu.Lock()
	defer r.mu.Unlock()

	ownerDevices := r.devices[ownerUID]
	if ownerDevices == nil {
		ownerDevices = make(map[string]UserDevice)
		r.devices[ownerUID] = ownerDevices
	}
	if existing, ok := ownerDevices[deviceID]; ok && existing.RegisteredAt > 0 {
		device.RegisteredAt = existing.RegisteredAt
	}
	ownerDevices[deviceID] = device
	return device, nil
}

func (r *userDeviceRegistry) list(ownerUID int64) []UserDevice {
	if r == nil || ownerUID <= 0 {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	ownerDevices := r.devices[ownerUID]
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

func (r *userDeviceRegistry) activeDevices(ownerUID int64) []UserDevice {
	now := r.now()
	devices := r.list(ownerUID)
	out := make([]UserDevice, 0, len(devices))
	for _, device := range devices {
		if !isActiveDevice(device, now, r.ttl) {
			continue
		}
		out = append(out, device)
	}
	return out
}

func (r *userDeviceRegistry) grantsForTurn(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string) []ScopedDeviceGrant {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return nil
	}
	operationsByDevice := r.activeDevices(actorUID)
	if len(operationsByDevice) == 0 {
		return nil
	}

	createdAt := unixMillis(r.now())
	expiresAt := unixMillis(r.now().Add(r.grantTT))
	actorUserID := formatUID(actorUID)
	agentID := ""
	if agentUID > 0 {
		agentID = formatUID(agentUID)
	}
	sessionKey := buildCatsCoSessionKey(topicID, topicType, agentID)

	grants := make([]ScopedDeviceGrant, 0, len(operationsByDevice))
	for _, device := range operationsByDevice {
		ops := append([]DeviceGrantOperation(nil), device.Capabilities...)
		if len(ops) == 0 {
			continue
		}
		grants = append(grants, ScopedDeviceGrant{
			Kind:                 "user_device_grant",
			Source:               "catscompany",
			GrantID:              "device_grant_" + randomDeviceGrantIDSuffix(),
			Status:               "active",
			IdentityTrust:        "server_canonical",
			IdentitySource:       userDeviceGrantIdentitySrc,
			DeviceID:             device.DeviceID,
			DeviceDisplayName:    device.DisplayName,
			DeviceBodyID:         device.BodyID,
			DeviceInstallationID: device.InstallationID,
			OwnerUserID:          actorUserID,
			SessionKey:           sessionKey,
			TopicID:              topicID,
			TopicType:            topicType,
			ActorUserID:          actorUserID,
			AgentID:              agentID,
			AgentBodyID:          agentBodyID,
			Operations:           ops,
			CreatedAt:            createdAt,
			ExpiresAt:            expiresAt,
		})
	}
	return grants
}

func isActiveDevice(device UserDevice, now time.Time, ttl time.Duration) bool {
	if device.Status != "online" {
		return false
	}
	lastSeen := time.UnixMilli(device.LastSeenAt)
	return !lastSeen.IsZero() && !now.After(lastSeen.Add(ttl))
}

func normalizeUserDeviceID(value string) (string, error) {
	deviceID := strings.TrimSpace(value)
	if deviceID == "" || len(deviceID) > maxUserDeviceIDLength || !userDeviceIDPattern.MatchString(deviceID) {
		return "", fmt.Errorf("invalid device_id")
	}
	return deviceID, nil
}

func normalizeDeviceText(value string) string {
	return strings.TrimSpace(value)
}

func normalizeDeviceStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "offline":
		return "offline"
	case "online", "":
		return "online"
	default:
		return "unknown"
	}
}

func normalizeDeviceCapabilities(values []string) []DeviceGrantOperation {
	if len(values) == 0 {
		return []DeviceGrantOperation{DeviceGrantReadFile, DeviceGrantSendFile}
	}
	seen := make(map[DeviceGrantOperation]struct{}, len(values))
	out := make([]DeviceGrantOperation, 0, len(values))
	for _, value := range values {
		operation := DeviceGrantOperation(strings.TrimSpace(value))
		if !isAllowedDeviceGrantOperation(operation) {
			continue
		}
		if _, ok := seen[operation]; ok {
			continue
		}
		seen[operation] = struct{}{}
		out = append(out, operation)
	}
	return out
}

func isAllowedDeviceGrantOperation(operation DeviceGrantOperation) bool {
	switch operation {
	case DeviceGrantReadFile,
		DeviceGrantWriteFile,
		DeviceGrantEditFile,
		DeviceGrantSendFile,
		DeviceGrantExecuteShell,
		DeviceGrantGlob,
		DeviceGrantGrep,
		DeviceGrantBrowserControl,
		DeviceGrantDesktopControl:
		return true
	default:
		return false
	}
}

func buildCatsCoSessionKey(topicID string, topicType string, agentID string) string {
	parts := []string{"session", "v2", "catscompany", normalizeTopicTypeForSessionKey(topicType), topicID}
	if agentID != "" {
		parts = append(parts, "agent", agentID)
	}
	return strings.Join(parts, ":")
}

func normalizeTopicTypeForSessionKey(value string) string {
	if value == "p2p" || value == "group" {
		return value
	}
	return "unknown"
}

func unixMillis(t time.Time) int64 {
	return t.UnixNano() / int64(time.Millisecond)
}

func randomDeviceGrantIDSuffix() string {
	suffix, err := randomHex(deviceGrantIDRandomLength)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return suffix
}

type DeviceHandler struct {
	db  store.Store
	hub *Hub
}

func NewDeviceHandler(db store.Store, hub *Hub) *DeviceHandler {
	return &DeviceHandler{db: db, hub: hub}
}

func (h *DeviceHandler) HandleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID, status, msg := h.resolveDeviceOwnerUID(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	var req RegisterUserDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	device, err := h.registry().register(ownerUID, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"device": device})
}

func (h *DeviceHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID, status, msg := h.resolveDeviceOwnerUID(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"devices": h.registry().list(ownerUID)})
}

func (h *DeviceHandler) resolveDeviceOwnerUID(r *http.Request) (int64, int, string) {
	if h == nil || h.db == nil || h.registry() == nil {
		return 0, http.StatusInternalServerError, "device registry unavailable"
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		return 0, http.StatusUnauthorized, "unauthorized"
	}
	user, err := h.db.GetUser(uid)
	if err != nil || user == nil {
		return 0, http.StatusUnauthorized, "invalid user"
	}
	if user.AccountType != types.AccountBot {
		return uid, 0, ""
	}
	ownerUID, err := h.db.GetBotOwner(uid)
	if err != nil || ownerUID <= 0 {
		return uid, 0, ""
	}
	return ownerUID, 0, ""
}

func (h *DeviceHandler) registry() *userDeviceRegistry {
	if h == nil || h.hub == nil {
		return nil
	}
	return h.hub.userDevices
}
