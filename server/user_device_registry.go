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
	defaultUserDeviceTTL          = 5 * time.Minute
	defaultDeviceGrantTTL         = 10 * time.Minute
	defaultDevicePreferenceTTL    = 30 * time.Minute
	maxUserDeviceIDLength         = 128
	deviceGrantIDRandomLength     = 12
	userDeviceGrantIdentitySrc    = "metadata.catsco_identity"
	channelDeviceGrantIdentitySrc = "channel_identity_link"
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
	Kind              string                 `json:"kind"`
	Source            string                 `json:"source"`
	OwnerUID          int64                  `json:"-"`
	OwnerUserID       string                 `json:"ownerUserId"`
	DeviceID          string                 `json:"deviceId"`
	DisplayName       string                 `json:"displayName,omitempty"`
	BodyID            string                 `json:"bodyId,omitempty"`
	InstallationID    string                 `json:"installationId,omitempty"`
	Status            string                 `json:"status"`
	Active            bool                   `json:"active"`
	RouteConnected    bool                   `json:"routeConnected"`
	Routable          bool                   `json:"routable"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
	Capabilities      []DeviceGrantOperation `json:"capabilities,omitempty"`
	RegisteredAt      int64                  `json:"registeredAt"`
	LastSeenAt        int64                  `json:"lastSeenAt,omitempty"`
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
	DeviceActive         bool                   `json:"deviceActive"`
	DeviceRouteConnected bool                   `json:"deviceRouteConnected"`
	DeviceRoutable       bool                   `json:"deviceRoutable"`
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

type DeviceSelectionStatus string

const (
	DeviceSelectionSelected       DeviceSelectionStatus = "selected"
	DeviceSelectionNeedsSelection DeviceSelectionStatus = "needs_selection"
	DeviceSelectionUnavailable    DeviceSelectionStatus = "unavailable"
)

type DeviceSelection struct {
	Kind            string                     `json:"kind"`
	Source          string                     `json:"source"`
	SchemaVersion   int                        `json:"schemaVersion"`
	Status          DeviceSelectionStatus      `json:"status"`
	SelectionSource string                     `json:"selectionSource,omitempty"`
	SessionKey      string                     `json:"sessionKey"`
	TopicID         string                     `json:"topicId"`
	TopicType       string                     `json:"topicType"`
	ActorUserID     string                     `json:"actorUserId"`
	OwnerUserID     string                     `json:"ownerUserId,omitempty"`
	AgentID         string                     `json:"agentId,omitempty"`
	SelectedDevice  *DeviceSelectionDevice     `json:"selectedDevice,omitempty"`
	Candidates      []DeviceSelectionCandidate `json:"candidates,omitempty"`
	CandidateCount  int                        `json:"candidateCount,omitempty"`
	CreatedAt       int64                      `json:"createdAt"`
}

type DeviceSelectionDevice struct {
	DeviceID          string                 `json:"deviceId"`
	DisplayName       string                 `json:"displayName,omitempty"`
	BodyID            string                 `json:"bodyId,omitempty"`
	InstallationID    string                 `json:"installationId,omitempty"`
	Status            string                 `json:"status,omitempty"`
	Active            bool                   `json:"active"`
	RouteConnected    bool                   `json:"routeConnected"`
	Routable          bool                   `json:"routable"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
	Operations        []DeviceGrantOperation `json:"operations,omitempty"`
	LastSeenAt        int64                  `json:"lastSeenAt,omitempty"`
}

type DeviceSelectionCandidate struct {
	DeviceID          string                 `json:"deviceId"`
	DisplayName       string                 `json:"displayName,omitempty"`
	Status            string                 `json:"status,omitempty"`
	Active            bool                   `json:"active"`
	RouteConnected    bool                   `json:"routeConnected"`
	Routable          bool                   `json:"routable"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
	Operations        []DeviceGrantOperation `json:"operations,omitempty"`
	LastSeenAt        int64                  `json:"lastSeenAt,omitempty"`
}

type DeviceTurnContext struct {
	Grants    []ScopedDeviceGrant
	Selection *DeviceSelection
}

type deviceSelectionPreference struct {
	DeviceID  string
	Source    string
	UpdatedAt int64
	ExpiresAt int64
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
	mu            sync.RWMutex
	ttl           time.Duration
	grantTT       time.Duration
	preferenceTTL time.Duration
	now           func() time.Time
	shared        sharedRuntimeState
	devices       map[int64]map[string]UserDevice
	preferences   map[int64]map[string]deviceSelectionPreference
	grants        map[string]ScopedDeviceGrant
}

func newUserDeviceRegistry(ttl time.Duration) *userDeviceRegistry {
	if ttl <= 0 {
		ttl = defaultUserDeviceTTL
	}
	return &userDeviceRegistry{
		ttl:           ttl,
		grantTT:       defaultDeviceGrantTTL,
		preferenceTTL: defaultDevicePreferenceTTL,
		now:           time.Now,
		devices:       make(map[int64]map[string]UserDevice),
		preferences:   make(map[int64]map[string]deviceSelectionPreference),
		grants:        make(map[string]ScopedDeviceGrant),
	}
}

func (r *userDeviceRegistry) withSharedRuntime(shared sharedRuntimeState) *userDeviceRegistry {
	if r == nil {
		return r
	}
	r.shared = shared
	return r
}

func (r *userDeviceRegistry) register(ownerUID int64, req RegisterUserDeviceRequest) (UserDevice, error) {
	if r == nil || ownerUID <= 0 {
		return UserDevice{}, fmt.Errorf("invalid owner")
	}
	if r.shared != nil {
		return r.shared.registerUserDevice(ownerUID, req, r.now())
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

func (r *userDeviceRegistry) unregister(ownerUID int64, deviceID string) {
	if r == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	if r.shared != nil {
		r.shared.unregisterUserDevice(ownerUID, deviceID)
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ownerDevices := r.devices[ownerUID]; ownerDevices != nil {
		delete(ownerDevices, normalizedDeviceID)
		if len(ownerDevices) == 0 {
			delete(r.devices, ownerUID)
		}
	}
}

func (r *userDeviceRegistry) list(ownerUID int64) []UserDevice {
	if r == nil || ownerUID <= 0 {
		return nil
	}
	if r.shared != nil {
		return r.shared.listUserDevices(ownerUID)
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

func (r *userDeviceRegistry) activeDevice(ownerUID int64, deviceID string) (UserDevice, bool) {
	if r == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return UserDevice{}, false
	}
	if r.shared != nil {
		return r.shared.activeUserDevice(ownerUID, deviceID, r.now(), r.ttl)
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return UserDevice{}, false
	}
	now := r.now()

	r.mu.RLock()
	defer r.mu.RUnlock()

	ownerDevices := r.devices[ownerUID]
	device, ok := ownerDevices[normalizedDeviceID]
	if !ok || !isActiveDevice(device, now, r.ttl) {
		return UserDevice{}, false
	}
	return device, true
}

func (r *userDeviceRegistry) touch(ownerUID int64, deviceID string) {
	if r == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	if r.shared != nil {
		r.shared.touchUserDevice(ownerUID, deviceID, r.now())
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	now := unixMillis(r.now())

	r.mu.Lock()
	defer r.mu.Unlock()

	ownerDevices := r.devices[ownerUID]
	device, ok := ownerDevices[normalizedDeviceID]
	if !ok {
		return
	}
	device.Status = "online"
	device.LastSeenAt = now
	ownerDevices[normalizedDeviceID] = device
}

func (r *userDeviceRegistry) grantsForTurn(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string) []ScopedDeviceGrant {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return nil
	}
	operationsByDevice := r.activeDevices(actorUID)
	if len(operationsByDevice) == 0 {
		return nil
	}
	return r.grantsForDevices(actorUID, topicID, topicType, agentUID, agentBodyID, operationsByDevice)
}

func (r *userDeviceRegistry) turnContext(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, messageText string) DeviceTurnContext {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return DeviceTurnContext{}
	}
	devices := r.activeDevices(actorUID)
	return r.turnContextForDevices(actorUID, topicID, topicType, agentUID, agentBodyID, messageText, devices, nil)
}

func (r *userDeviceRegistry) turnContextForDevices(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, messageText string, devices []UserDevice, unavailableCandidates []UserDevice) DeviceTurnContext {
	return r.turnContextForOwnerDevices(actorUID, actorUID, topicID, topicType, agentUID, agentBodyID, messageText, devices, unavailableCandidates)
}

func (r *userDeviceRegistry) turnContextForOwnerDevices(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, messageText string, devices []UserDevice, unavailableCandidates []UserDevice) DeviceTurnContext {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return DeviceTurnContext{}
	}
	if ownerUID <= 0 {
		return DeviceTurnContext{}
	}
	if len(devices) == 0 && len(unavailableCandidates) > 0 {
		return DeviceTurnContext{
			Selection: r.unavailableDeviceSelectionForOwner(actorUID, ownerUID, topicID, topicType, agentUID, "no_routable_devices", unavailableCandidates),
		}
	}
	selection, selected := r.selectDeviceForTurn(actorUID, topicID, topicType, agentUID, devices, messageText)
	if selection != nil {
		selection.OwnerUserID = formatUID(ownerUID)
	}
	var grants []ScopedDeviceGrant
	if selected != nil && selection != nil && selection.Status == DeviceSelectionSelected {
		grants = r.grantsForOwnerDevices(actorUID, ownerUID, topicID, topicType, agentUID, agentBodyID, []UserDevice{*selected})
	}
	return DeviceTurnContext{
		Grants:    grants,
		Selection: selection,
	}
}

func (r *userDeviceRegistry) unavailableDeviceSelection(actorUID int64, topicID string, topicType string, agentUID int64, reason string, candidates []UserDevice) *DeviceSelection {
	return r.unavailableDeviceSelectionForOwner(actorUID, actorUID, topicID, topicType, agentUID, reason, candidates)
}

func (r *userDeviceRegistry) unavailableDeviceSelectionForOwner(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, reason string, candidates []UserDevice) *DeviceSelection {
	createdAt := unixMillis(r.now())
	actorUserID := formatUID(actorUID)
	ownerUserID := formatUID(ownerUID)
	agentID := ""
	if agentUID > 0 {
		agentID = formatUID(agentUID)
	}
	sessionKey := buildCatsCoSessionKey(topicID, topicType, agentID)
	return &DeviceSelection{
		Kind:            "user_device_selection",
		Source:          "catscompany",
		SchemaVersion:   1,
		Status:          DeviceSelectionUnavailable,
		SelectionSource: reason,
		SessionKey:      sessionKey,
		TopicID:         topicID,
		TopicType:       topicType,
		ActorUserID:     actorUserID,
		OwnerUserID:     ownerUserID,
		AgentID:         agentID,
		Candidates:      deviceSelectionCandidates(candidates),
		CandidateCount:  len(candidates),
		CreatedAt:       createdAt,
	}
}

func (r *userDeviceRegistry) grantsForDevices(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice) []ScopedDeviceGrant {
	return r.grantsForOwnerDevices(actorUID, actorUID, topicID, topicType, agentUID, agentBodyID, devices)
}

func (r *userDeviceRegistry) grantsForOwnerDevices(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice) []ScopedDeviceGrant {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" || len(devices) == 0 {
		return nil
	}
	if ownerUID <= 0 {
		return nil
	}
	createdAt := unixMillis(r.now())
	expiresAt := unixMillis(r.now().Add(r.grantTT))
	actorUserID := formatUID(actorUID)
	ownerUserID := formatUID(ownerUID)
	identitySource := userDeviceGrantIdentitySrc
	if ownerUID != actorUID {
		identitySource = channelDeviceGrantIdentitySrc
	}
	agentID := ""
	if agentUID > 0 {
		agentID = formatUID(agentUID)
	}
	sessionKey := buildCatsCoSessionKey(topicID, topicType, agentID)

	grants := make([]ScopedDeviceGrant, 0, len(devices))
	for _, device := range devices {
		ops := deviceGrantOperations(device.Capabilities)
		if len(ops) == 0 {
			continue
		}
		grants = append(grants, ScopedDeviceGrant{
			Kind:                 "user_device_grant",
			Source:               "catscompany",
			GrantID:              "device_grant_" + randomDeviceGrantIDSuffix(),
			Status:               "active",
			IdentityTrust:        "server_canonical",
			IdentitySource:       identitySource,
			DeviceID:             device.DeviceID,
			DeviceDisplayName:    device.DisplayName,
			DeviceBodyID:         device.BodyID,
			DeviceInstallationID: device.InstallationID,
			DeviceRouteConnected: device.RouteConnected,
			DeviceRoutable:       device.Routable,
			OwnerUserID:          ownerUserID,
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
	r.rememberGrants(grants)
	return grants
}

func (r *userDeviceRegistry) rememberGrants(grants []ScopedDeviceGrant) {
	if r == nil || len(grants) == 0 {
		return
	}
	now := unixMillis(r.now())
	if r.shared != nil {
		r.shared.rememberDeviceGrants(grants, now)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupExpiredGrantsLocked(now)
	for _, grant := range grants {
		if grant.GrantID == "" || grant.ExpiresAt <= now {
			continue
		}
		r.grants[grant.GrantID] = grant
	}
}

func (r *userDeviceRegistry) lookupGrant(grantID string) (ScopedDeviceGrant, bool) {
	if r == nil || strings.TrimSpace(grantID) == "" {
		return ScopedDeviceGrant{}, false
	}
	now := unixMillis(r.now())
	if r.shared != nil {
		return r.shared.lookupDeviceGrant(strings.TrimSpace(grantID), now)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupExpiredGrantsLocked(now)
	grant, ok := r.grants[strings.TrimSpace(grantID)]
	if !ok || grant.Status != "active" || grant.ExpiresAt <= now {
		return ScopedDeviceGrant{}, false
	}
	return grant, true
}

func (r *userDeviceRegistry) cleanupExpiredGrantsLocked(now int64) {
	for grantID, grant := range r.grants {
		if grant.ExpiresAt <= now {
			delete(r.grants, grantID)
		}
	}
}

func (r *userDeviceRegistry) selectDeviceForTurn(
	actorUID int64,
	topicID string,
	topicType string,
	agentUID int64,
	devices []UserDevice,
	messageText string,
) (*DeviceSelection, *UserDevice) {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return nil, nil
	}
	createdAt := unixMillis(r.now())
	actorUserID := formatUID(actorUID)
	agentID := ""
	if agentUID > 0 {
		agentID = formatUID(agentUID)
	}
	sessionKey := buildCatsCoSessionKey(topicID, topicType, agentID)
	base := DeviceSelection{
		Kind:           "user_device_selection",
		Source:         "catscompany",
		SchemaVersion:  1,
		SessionKey:     sessionKey,
		TopicID:        topicID,
		TopicType:      topicType,
		ActorUserID:    actorUserID,
		AgentID:        agentID,
		CandidateCount: len(devices),
		CreatedAt:      createdAt,
	}
	if len(devices) == 0 {
		base.Status = DeviceSelectionUnavailable
		base.SelectionSource = "no_active_devices"
		return &base, nil
	}

	if explicitMatches := matchMentionedDevices(devices, messageText); len(explicitMatches) == 1 {
		selected := explicitMatches[0]
		base.Status = DeviceSelectionSelected
		base.SelectionSource = "explicit_mention"
		base.SelectedDevice = deviceSelectionDevice(selected)
		r.rememberDeviceSelection(actorUID, sessionKey, selected.DeviceID, "explicit_mention")
		return &base, &selected
	} else if len(explicitMatches) > 1 {
		base.Status = DeviceSelectionNeedsSelection
		base.SelectionSource = "explicit_ambiguous"
		base.Candidates = deviceSelectionCandidates(explicitMatches)
		return &base, nil
	}

	if preferredDeviceID, ok := r.deviceSelectionPreference(actorUID, sessionKey); ok {
		if selected, ok := findDeviceByID(devices, preferredDeviceID); ok {
			base.Status = DeviceSelectionSelected
			base.SelectionSource = "conversation_preference"
			base.SelectedDevice = deviceSelectionDevice(selected)
			return &base, &selected
		}
	}

	selected := devices[0]
	base.Status = DeviceSelectionSelected
	if len(devices) == 1 {
		base.SelectionSource = "single_active_device"
	} else {
		base.SelectionSource = "most_recent_online"
		base.Candidates = deviceSelectionCandidates(devices)
	}
	base.SelectedDevice = deviceSelectionDevice(selected)
	r.rememberDeviceSelection(actorUID, sessionKey, selected.DeviceID, base.SelectionSource)
	return &base, &selected
}

func (r *userDeviceRegistry) deviceSelectionPreference(actorUID int64, sessionKey string) (string, bool) {
	if r == nil || actorUID <= 0 || sessionKey == "" {
		return "", false
	}
	now := unixMillis(r.now())
	if r.shared != nil {
		return r.shared.deviceSelectionPreference(actorUID, sessionKey, now)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	actorPreferences := r.preferences[actorUID]
	if len(actorPreferences) == 0 {
		return "", false
	}
	preference, ok := actorPreferences[sessionKey]
	if !ok || preference.DeviceID == "" || preference.ExpiresAt <= now {
		return "", false
	}
	return preference.DeviceID, true
}

func (r *userDeviceRegistry) rememberDeviceSelection(actorUID int64, sessionKey string, deviceID string, source string) {
	if r == nil || actorUID <= 0 || sessionKey == "" || deviceID == "" {
		return
	}
	now := r.now()
	preference := deviceSelectionPreference{
		DeviceID:  deviceID,
		Source:    source,
		UpdatedAt: unixMillis(now),
		ExpiresAt: unixMillis(now.Add(r.preferenceTTL)),
	}
	if r.shared != nil {
		r.shared.rememberDeviceSelection(actorUID, sessionKey, preference)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actorPreferences := r.preferences[actorUID]
	if actorPreferences == nil {
		actorPreferences = make(map[string]deviceSelectionPreference)
		r.preferences[actorUID] = actorPreferences
	}
	actorPreferences[sessionKey] = preference
}

func matchMentionedDevices(devices []UserDevice, messageText string) []UserDevice {
	normalizedText := normalizeDeviceSelectionText(messageText)
	if normalizedText == "" {
		return nil
	}
	matches := make([]UserDevice, 0, 1)
	seen := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		if device.DeviceID == "" {
			continue
		}
		if deviceMentioned(device, normalizedText) {
			if _, ok := seen[device.DeviceID]; ok {
				continue
			}
			seen[device.DeviceID] = struct{}{}
			matches = append(matches, device)
		}
	}
	return matches
}

func deviceMentioned(device UserDevice, normalizedText string) bool {
	candidates := []string{device.DisplayName, device.DeviceID, device.InstallationID}
	for _, candidate := range candidates {
		normalizedCandidate := normalizeDeviceSelectionText(candidate)
		if normalizedCandidate == "" {
			continue
		}
		if strings.Contains(normalizedText, normalizedCandidate) {
			return true
		}
	}
	return false
}

func normalizeDeviceSelectionText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func findDeviceByID(devices []UserDevice, deviceID string) (UserDevice, bool) {
	for _, device := range devices {
		if device.DeviceID == deviceID {
			return device, true
		}
	}
	return UserDevice{}, false
}

func deviceSelectionDevice(device UserDevice) *DeviceSelectionDevice {
	return &DeviceSelectionDevice{
		DeviceID:          device.DeviceID,
		DisplayName:       device.DisplayName,
		BodyID:            device.BodyID,
		InstallationID:    device.InstallationID,
		Status:            device.Status,
		RouteConnected:    device.RouteConnected,
		Routable:          device.Routable,
		UnavailableReason: device.UnavailableReason,
		Operations:        deviceGrantOperations(device.Capabilities),
		LastSeenAt:        device.LastSeenAt,
	}
}

func deviceSelectionCandidates(devices []UserDevice) []DeviceSelectionCandidate {
	if len(devices) == 0 {
		return nil
	}
	out := make([]DeviceSelectionCandidate, 0, len(devices))
	for _, device := range devices {
		out = append(out, DeviceSelectionCandidate{
			DeviceID:          device.DeviceID,
			DisplayName:       device.DisplayName,
			Status:            device.Status,
			RouteConnected:    device.RouteConnected,
			Routable:          device.Routable,
			UnavailableReason: device.UnavailableReason,
			Operations:        deviceGrantOperations(device.Capabilities),
			LastSeenAt:        device.LastSeenAt,
		})
	}
	return out
}

func deviceGrantOperations(capabilities []DeviceGrantOperation) []DeviceGrantOperation {
	if len(capabilities) == 0 {
		return nil
	}
	out := make([]DeviceGrantOperation, 0, len(capabilities))
	seen := make(map[DeviceGrantOperation]struct{}, len(capabilities))
	for _, operation := range capabilities {
		if !isAllowedDeviceGrantRuntimeOperation(operation) {
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

func isAllowedDeviceGrantRuntimeOperation(operation DeviceGrantOperation) bool {
	switch operation {
	case DeviceGrantReadFile,
		DeviceGrantGlob,
		DeviceGrantGrep,
		DeviceGrantWriteFile,
		DeviceGrantEditFile,
		DeviceGrantSendFile:
		return true
	default:
		return false
	}
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
	devices := h.registry().list(ownerUID)
	if h.hub != nil {
		devices, _ = h.hub.classifyUserDevices(ownerUID, devices)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"devices":    devices,
		"checked_at": unixMillis(time.Now()),
	})
}

func (h *DeviceHandler) HandleDeviceRPCStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID, status, msg := h.resolveDeviceOwnerUID(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	pending := []DeviceRPCPendingStatus{}
	if h != nil && h.hub != nil {
		pending = h.hub.DeviceRPCStatus(ownerUID, normalizeDeviceRPCStatusAgentID(r.URL.Query().Get("agent_id")))
		if pending == nil {
			pending = []DeviceRPCPendingStatus{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"state":         "ok",
		"runtime_mode":  h.hubRuntimeMode(),
		"route_state":   h.hubRouteState(),
		"pending":       pending,
		"pending_count": len(pending),
		"checked_at":    unixMillis(time.Now()),
	})
}

func (h *DeviceHandler) HandleDeviceByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil || h.db == nil || h.registry() == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "device registry unavailable"})
		return
	}
	uid := UIDFromContext(r.Context())
	user, status, msg := activeUserByID(uid, h.db.GetUser)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if user.AccountType != types.AccountHuman {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device unlink requires a human user token"})
		return
	}
	deviceID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/devices/"), "/")
	if _, err := normalizeUserDeviceID(deviceID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid device_id"})
		return
	}
	h.registry().unregister(uid, deviceID)
	if h.hub != nil {
		h.hub.revokeDeviceConnectorDevice(uid, deviceID)
		h.hub.disconnectDeviceConnector(uid, deviceID, "device connector revoked")
		h.hub.addDeviceAudit(uid, DeviceAuditEvent{
			DeviceID: deviceID,
			Phase:    "device_unlinked",
			Result:   "ok",
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"device_id": deviceID,
	})
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

func normalizeDeviceRPCStatusAgentID(value string) string {
	agentID := strings.TrimSpace(value)
	if agentID == "" {
		return ""
	}
	if strings.HasPrefix(agentID, "usr") {
		return agentID
	}
	if uid := parseInt64(agentID); uid > 0 {
		return formatUID(uid)
	}
	return agentID
}

func (h *DeviceHandler) registry() *userDeviceRegistry {
	if h == nil || h.hub == nil {
		return nil
	}
	return h.hub.userDevices
}

func (h *DeviceHandler) hubRuntimeMode() string {
	if h == nil || h.hub == nil {
		return "unavailable"
	}
	return h.hub.RuntimeMode()
}

func (h *DeviceHandler) hubRouteState() string {
	if h == nil || h.hub == nil {
		return "unavailable"
	}
	return h.hub.RuntimeRouteState()
}
