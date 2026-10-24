package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultUserDeviceTTL             = 5 * time.Minute
	defaultDeviceGrantTTL            = 10 * time.Minute
	defaultDevicePreferenceTTL       = 30 * time.Minute
	maxUserDeviceIDLength            = 128
	maxDeviceSelectionRevisionLength = 64
	deviceGrantIDRandomLength        = 12
	userDeviceGrantIdentitySrc       = "metadata.catsco_identity"
	channelDeviceGrantIdentitySrc    = "channel_identity_link"
)

var userDeviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type DeviceGrantOperation string

const (
	DeviceGrantReadFile                             DeviceGrantOperation = "read_file"
	DeviceGrantResolveDir                           DeviceGrantOperation = "resolve_common_directory"
	DeviceGrantWriteFile                            DeviceGrantOperation = "write_file"
	DeviceGrantEditFile                             DeviceGrantOperation = "edit_file"
	DeviceGrantSendFile                             DeviceGrantOperation = "send_file"
	DeviceGrantExecuteShell                         DeviceGrantOperation = "execute_shell"
	DeviceGrantGlob                                 DeviceGrantOperation = "glob"
	DeviceGrantGrep                                 DeviceGrantOperation = "grep"
	DeviceGrantBrowserControl                       DeviceGrantOperation = "browser_control"
	DeviceGrantDesktopControl                       DeviceGrantOperation = "desktop_control"
	DeviceGrantSkillHubWorkspaceGet                 DeviceGrantOperation = "skillhub.localWorkspace.get"
	DeviceGrantSkillHubWorkspaceApplyDefinition     DeviceGrantOperation = "skillhub.localWorkspace.applyDefinition"
	DeviceCapabilitySkillHubWorkspacePagination     DeviceGrantOperation = "skillhub.localWorkspace.pagination.v1"
	DeviceGrantSkillHubWorkspaceSync                DeviceGrantOperation = "skillhub.localWorkspace.syncToAgent"
	DeviceGrantSkillHubSkillShare                   DeviceGrantOperation = "skillhub.localSkill.share"
	DeviceCapabilitySkillHubPublicationProbe        DeviceGrantOperation = "skillhub.publication.sourceProbe.v1"
	DeviceCapabilitySkillHubPublicationCapture      DeviceGrantOperation = "skillhub.publication.capture.v1"
	DeviceCapabilitySkillHubPublicationRepairSource DeviceGrantOperation = "skillhub.publication.verifyRepairSource.v1"
	DeviceCapabilitySkillHubPublicationUpload       DeviceGrantOperation = "skillhub.publication.upload.v1"
	DeviceGrantSkillHubSkillFinalize                DeviceGrantOperation = "skillhub.localSkill.finalize"
	DeviceGrantSkillHubSkillDelete                  DeviceGrantOperation = "skillhub.localSkill.delete"
	DeviceGrantSkillHubBotSwitch                    DeviceGrantOperation = "skillhub.localBot.switch"
	DeviceGrantKnowledgeDocumentList                DeviceGrantOperation = "knowledge.document.list"
	DeviceGrantKnowledgeDocumentRead                DeviceGrantOperation = "knowledge.document.read"
)

type UserDevice struct {
	Kind              string                 `json:"kind"`
	Source            string                 `json:"source"`
	OwnerUID          int64                  `json:"-"`
	OwnerUserID       string                 `json:"ownerUserId"`
	BotUID            int64                  `json:"botUid,omitempty"`
	DeviceID          string                 `json:"deviceId"`
	DisplayName       string                 `json:"displayName,omitempty"`
	OS                string                 `json:"os"`
	BodyID            string                 `json:"bodyId,omitempty"`
	InstallationID    string                 `json:"installationId,omitempty"`
	RuntimeRole       string                 `json:"runtimeRole,omitempty"`
	Status            string                 `json:"status"`
	Active            bool                   `json:"active"`
	RouteConnected    bool                   `json:"routeConnected"`
	Routable          bool                   `json:"routable"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
	Capabilities      []DeviceGrantOperation `json:"capabilities,omitempty"`
	ModelStatus       *DeviceModelStatus     `json:"modelStatus,omitempty"`
	RegisteredAt      int64                  `json:"registeredAt"`
	LastSeenAt        int64                  `json:"lastSeenAt,omitempty"`
}

type DeviceModelStatus struct {
	Source          string `json:"source,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	UpdatedAt       int64  `json:"updatedAt,omitempty"`
}

type ScopedDeviceGrant struct {
	// Explicit targets use their own grant; selection remains only the default.
	TargetScope          string                 `json:"targetScope,omitempty"`
	SelectionRevision    string                 `json:"selectionRevision,omitempty"`
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
	Revision        string                     `json:"revision"`
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
	OS                string                 `json:"os,omitempty"`
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
	Grants         []ScopedDeviceGrant
	ExplicitGrants []ScopedDeviceGrant
	Selection      *DeviceSelection
}

type deviceSelectionPreference struct {
	Durable   bool
	Revision  string
	DeviceID  string
	Source    string
	UpdatedAt int64
	ExpiresAt int64
}

type RegisterUserDeviceRequest struct {
	BotUID         int64              `json:"bot_uid,omitempty"`
	DeviceID       string             `json:"device_id"`
	DisplayName    string             `json:"display_name,omitempty"`
	OS             string             `json:"os,omitempty"`
	BodyID         string             `json:"body_id,omitempty"`
	InstallationID string             `json:"installation_id,omitempty"`
	RuntimeRole    string             `json:"runtime_role,omitempty"`
	Status         string             `json:"status,omitempty"`
	Capabilities   []string           `json:"capabilities,omitempty"`
	ModelStatus    *DeviceModelStatus `json:"model_status,omitempty"`
}

type userDeviceRegistry struct {
	selectionStore store.ConversationDeviceSelectionStore
	mu             sync.RWMutex
	ttl            time.Duration
	grantTT        time.Duration
	preferenceTTL  time.Duration
	now            func() time.Time
	shared         sharedRuntimeState
	devices        map[int64]map[string]UserDevice
	preferences    map[int64]map[string]deviceSelectionPreference
	grants         map[string]ScopedDeviceGrant
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
		BotUID:         normalizeDeviceBotUID(req.BotUID),
		DeviceID:       deviceID,
		DisplayName:    normalizeDeviceText(req.DisplayName),
		OS:             normalizeDeviceOS(req.OS),
		BodyID:         normalizeDeviceText(req.BodyID),
		InstallationID: normalizeDeviceText(req.InstallationID),
		RuntimeRole:    normalizeDeviceRuntimeRole(req.RuntimeRole),
		Status:         normalizeDeviceStatus(req.Status),
		Capabilities:   normalizeDeviceCapabilities(req.Capabilities),
		ModelStatus:    normalizeDeviceModelStatus(req.ModelStatus, now),
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

func (r *userDeviceRegistry) registeredDevice(ownerUID int64, deviceID string) (UserDevice, bool) {
	if r == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return UserDevice{}, false
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return UserDevice{}, false
	}
	for _, device := range r.list(ownerUID) {
		if device.DeviceID == normalizedDeviceID {
			return device, true
		}
	}
	return UserDevice{}, false
}

func normalizeDeviceModelStatus(input *DeviceModelStatus, now time.Time) *DeviceModelStatus {
	if input == nil {
		return nil
	}
	source := strings.ToLower(strings.TrimSpace(input.Source))
	model := normalizeDeviceText(input.Model)
	switch source {
	case "relay":
		if model == "" {
			return nil
		}
	case "custom":
		if model == "" {
			model = "自定义模型"
		}
	default:
		return nil
	}
	updatedAt := input.UpdatedAt
	if updatedAt <= 0 {
		updatedAt = unixMillis(now)
	}
	return &DeviceModelStatus{
		Source:          source,
		Model:           model,
		ReasoningEffort: normalizeDeviceText(input.ReasoningEffort),
		UpdatedAt:       updatedAt,
	}
}

func LatestDeviceModelStatus(hub *Hub, ownerUID int64) (DeviceModelStatus, bool) {
	if hub == nil || hub.userDevices == nil || ownerUID <= 0 {
		return DeviceModelStatus{}, false
	}
	return latestDeviceModelStatus(hub.userDevices.activeDevices(ownerUID), nil)
}

// DeviceModelStatusForBody returns the current model reported by the device
// bound to one bot, without allowing another device owned by the same account
// to replace it.
func DeviceModelStatusForBody(hub *Hub, ownerUID int64, bodyID string) (DeviceModelStatus, bool) {
	bodyID = strings.TrimSpace(bodyID)
	if hub == nil || hub.userDevices == nil || ownerUID <= 0 || bodyID == "" {
		return DeviceModelStatus{}, false
	}
	return latestDeviceModelStatus(hub.userDevices.activeDevices(ownerUID), func(device UserDevice) bool {
		return strings.TrimSpace(device.BodyID) == bodyID || strings.TrimSpace(device.DeviceID) == bodyID
	})
}

func latestDeviceModelStatus(devices []UserDevice, include func(UserDevice) bool) (DeviceModelStatus, bool) {
	var best DeviceModelStatus
	bestSeenAt := int64(0)
	for _, device := range devices {
		if include != nil && !include(device) {
			continue
		}
		if device.ModelStatus == nil {
			continue
		}
		status := *device.ModelStatus
		if status.Source != "relay" && status.Source != "custom" {
			continue
		}
		if status.Source == "relay" && strings.TrimSpace(status.Model) == "" {
			continue
		}
		seenAt := status.UpdatedAt
		if seenAt <= 0 {
			seenAt = device.LastSeenAt
		}
		if seenAt <= 0 {
			continue
		}
		if seenAt >= bestSeenAt {
			best = status
			bestSeenAt = seenAt
		}
	}
	return best, bestSeenAt > 0
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

func (r *userDeviceRegistry) turnContext(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string) DeviceTurnContext {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return DeviceTurnContext{}
	}
	devices := r.activeDevices(actorUID)
	return r.turnContextForDevices(actorUID, topicID, topicType, agentUID, agentBodyID, devices, nil)
}

func (r *userDeviceRegistry) turnContextForDevices(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice, unavailableCandidates []UserDevice) DeviceTurnContext {
	return r.turnContextForOwnerDevices(actorUID, actorUID, topicID, topicType, agentUID, agentBodyID, devices, unavailableCandidates)
}

func (r *userDeviceRegistry) turnContextForOwnerDevices(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice, unavailableCandidates []UserDevice) DeviceTurnContext {
	return r.turnContextForOwnerDevicesWithExplicitTargets(actorUID, ownerUID, topicID, topicType, agentUID, agentBodyID, devices, unavailableCandidates, xiaobaRuntimeV2EnabledForBot(agentUID))
}

// turnContextForOwnerDevicesWithExplicitTargets keeps the server-side rollout
// decision separate from client negotiation. Callers that have the authenticated
// Bot connection must pass its negotiated capability; the historical helper
// above remains for registry-only tests and callers without a live peer.
func (r *userDeviceRegistry) turnContextForOwnerDevicesWithExplicitTargets(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice, unavailableCandidates []UserDevice, explicitTargetsEnabled bool) DeviceTurnContext {
	if r == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return DeviceTurnContext{}
	}
	if ownerUID <= 0 {
		return DeviceTurnContext{}
	}
	devices = devicesForOwner(ownerUID, devices)
	unavailableCandidates = devicesForOwner(ownerUID, unavailableCandidates)
	selection, selected := r.selectDeviceForTurn(actorUID, topicID, topicType, agentUID, devices, unavailableCandidates)
	if selection != nil {
		selection.OwnerUserID = formatUID(ownerUID)
	}
	var grants, explicitGrants []ScopedDeviceGrant
	if selected != nil && selection != nil && selection.Status == DeviceSelectionSelected {
		grants = r.grantsForOwnerDevices(actorUID, ownerUID, topicID, topicType, agentUID, agentBodyID, []UserDevice{*selected}, selection.Revision)
	}
	// The legacy grant list remains selected-only even during a v2 rollout.
	// Explicit capabilities live in a separate envelope ignored by old Bots,
	// and cannot execute through either RPC without an exact explicit target.
	if ownerUID == actorUID && explicitTargetsEnabled && selection != nil && selection.SelectionSource != "selection_store_unavailable" && selection.SelectionSource != "selection_changed" {
		explicitGrants = r.grantsForExplicitDevices(actorUID, topicID, topicType, agentUID, agentBodyID, devices, selection.Revision)
	}
	return DeviceTurnContext{
		Grants:         grants,
		ExplicitGrants: explicitGrants,
		Selection:      selection,
	}
}

func (r *userDeviceRegistry) grantsForDevices(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice) []ScopedDeviceGrant {
	return r.grantsForOwnerDevices(actorUID, actorUID, topicID, topicType, agentUID, agentBodyID, devices)
}

func (r *userDeviceRegistry) grantsForOwnerDevices(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice, selectedRevision ...string) []ScopedDeviceGrant {
	return r.issueDeviceGrants(actorUID, ownerUID, topicID, topicType, agentUID, agentBodyID, devices, false, selectedRevision...)
}

func (r *userDeviceRegistry) grantsForExplicitDevices(actorUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice, revision string) []ScopedDeviceGrant {
	return r.issueDeviceGrants(actorUID, actorUID, topicID, topicType, agentUID, agentBodyID, devices, true, revision)
}

func (r *userDeviceRegistry) issueDeviceGrants(actorUID int64, ownerUID int64, topicID string, topicType string, agentUID int64, agentBodyID string, devices []UserDevice, explicit bool, selectedRevision ...string) []ScopedDeviceGrant {
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
	sessionKey := buildCatsCoSessionKey(topicID, topicType, agentID, actorUID)

	preference, err := r.selectionRecord(actorUID, sessionKey)
	if err != nil {
		return nil
	}
	revision := preference.Revision
	if len(selectedRevision) > 0 && selectedRevision[0] != revision {
		return nil
	}
	grants := make([]ScopedDeviceGrant, 0, len(devices))
	for _, device := range devices {
		if device.OwnerUID != ownerUID {
			continue
		}
		if !explicit && preference.DeviceID != "" && (preference.DeviceID != device.DeviceID || !preference.active(createdAt)) {
			continue
		}
		ops := deviceGrantOperations(device.Capabilities)
		if len(ops) == 0 {
			continue
		}
		targetScope := ""
		if explicit {
			targetScope = "explicit_device"
		}
		grants = append(grants, ScopedDeviceGrant{
			TargetScope:          targetScope,
			SelectionRevision:    revision,
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

func devicesForOwner(ownerUID int64, devices []UserDevice) []UserDevice {
	if ownerUID <= 0 || len(devices) == 0 {
		return nil
	}
	filtered := make([]UserDevice, 0, len(devices))
	for _, device := range devices {
		if device.OwnerUID == ownerUID {
			filtered = append(filtered, device)
		}
	}
	return filtered
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
		grant, ok := r.shared.lookupDeviceGrant(strings.TrimSpace(grantID), now)
		return grant, ok && r.grantSelectionCurrent(grant)
	}
	r.mu.Lock()
	r.cleanupExpiredGrantsLocked(now)
	grant, ok := r.grants[strings.TrimSpace(grantID)]
	r.mu.Unlock()
	if !ok || grant.Status != "active" || grant.ExpiresAt <= now {
		return ScopedDeviceGrant{}, false
	}
	return grant, r.grantSelectionCurrent(grant)
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
	unavailableSets ...[]UserDevice,
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
	sessionKey := buildCatsCoSessionKey(topicID, topicType, agentID, actorUID)
	known := append([]UserDevice(nil), devices...)
	if len(unavailableSets) > 0 {
		known = append(known, unavailableSets[0]...)
	}
	base := DeviceSelection{
		Kind:           "user_device_selection",
		Source:         "catscompany",
		SchemaVersion:  1,
		SessionKey:     sessionKey,
		TopicID:        topicID,
		TopicType:      topicType,
		ActorUserID:    actorUserID,
		AgentID:        agentID,
		CandidateCount: len(known),
		Candidates:     deviceSelectionCandidates(known),
		CreatedAt:      createdAt,
	}
	preference, err := r.selectionRecord(actorUID, sessionKey)
	if err != nil {
		base.Status, base.SelectionSource = DeviceSelectionUnavailable, "selection_store_unavailable"
		return &base, nil
	}
	base.Revision = preference.Revision
	if len(devices) == 0 {
		base.Status = DeviceSelectionUnavailable
		base.SelectionSource = "no_routable_devices"
		if preferred, ok := r.deviceSelectionPreference(actorUID, sessionKey); ok {
			base.SelectedDevice = &DeviceSelectionDevice{DeviceID: preferred, UnavailableReason: "route_unavailable"}
			if device, found := findDeviceByID(known, preferred); found {
				base.SelectedDevice = deviceSelectionDevice(device)
			}
		}
		return &base, nil
	}

	preferredDeviceID, hasPreference := preference.DeviceID, preference.active(createdAt)
	// Conversation selection is a default, independent of task text. Naming
	// another device (including in a quotation or negation) must never rewrite
	// or invalidate it. Explicit tool targets use separate per-device grants.

	if hasPreference {
		if selected, ok := findDeviceByID(devices, preferredDeviceID); ok {
			base.Status = DeviceSelectionSelected
			base.SelectionSource = "conversation_preference"
			base.SelectedDevice = deviceSelectionDevice(selected)
			r.refreshDeviceSelection(actorUID, sessionKey, preference)
			return &base, &selected
		}
		base.Status, base.SelectionSource = DeviceSelectionUnavailable, "selected_device_unavailable"
		base.SelectedDevice = &DeviceSelectionDevice{DeviceID: preferredDeviceID, UnavailableReason: "route_unavailable"}
		if device, found := findDeviceByID(known, preferredDeviceID); found {
			base.SelectedDevice = deviceSelectionDevice(device)
		}
		return &base, nil
	}

	if len(known) > 1 {
		base.Status, base.SelectionSource = DeviceSelectionNeedsSelection, "multiple_devices"
		return &base, nil
	}
	selected := devices[0]
	base.Status = DeviceSelectionSelected
	base.SelectionSource = "single_active_device"
	base.SelectedDevice = deviceSelectionDevice(selected)
	r.rememberDeviceSelection(actorUID, sessionKey, selected.DeviceID, base.SelectionSource)
	current, err := r.selectionRecord(actorUID, sessionKey)
	if err != nil || current.DeviceID != selected.DeviceID || !current.active(createdAt) {
		base.Status, base.SelectionSource = DeviceSelectionNeedsSelection, "selection_changed"
		base.SelectedDevice = nil
		return &base, nil
	}
	base.Revision = current.Revision
	return &base, &selected
}

func (r *userDeviceRegistry) deviceSelectionPreference(actorUID int64, sessionKey string) (string, bool) {
	if r == nil || actorUID <= 0 || sessionKey == "" {
		return "", false
	}
	preference, err := r.selectionRecord(actorUID, sessionKey)
	if err != nil || !preference.active(unixMillis(r.now())) {
		return "", false
	}
	return preference.DeviceID, true
}

func (r *userDeviceRegistry) rememberDeviceSelection(actorUID int64, sessionKey string, deviceID string, source string) {
	if r == nil || actorUID <= 0 || sessionKey == "" || deviceID == "" {
		return
	}
	current, err := r.selectionRecord(actorUID, sessionKey)
	if err != nil {
		return
	}
	// Automatic selection and preference renewal must never overwrite a
	// concurrent explicit confirmation or revive an expired different target.
	if current.DeviceID != "" && current.DeviceID != deviceID {
		return
	}
	if current.DeviceID == deviceID && current.active(unixMillis(r.now())) {
		r.refreshDeviceSelection(actorUID, sessionKey, current)
		return
	}
	_, _ = r.confirmDeviceSelection(actorUID, sessionKey, deviceID, source, current.Revision)
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
			OS:                device.OS,
			Active:            device.Active,
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
		DeviceGrantResolveDir,
		DeviceGrantGlob,
		DeviceGrantGrep,
		DeviceGrantWriteFile,
		DeviceGrantEditFile,
		DeviceGrantSendFile,
		DeviceGrantExecuteShell,
		DeviceGrantSkillHubWorkspaceGet,
		DeviceGrantSkillHubWorkspaceSync,
		DeviceGrantSkillHubSkillShare,
		DeviceGrantSkillHubSkillFinalize,
		DeviceGrantSkillHubSkillDelete,
		DeviceGrantSkillHubBotSwitch,
		DeviceGrantKnowledgeDocumentList,
		DeviceGrantKnowledgeDocumentRead:
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

func normalizeDeviceRuntimeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "desktop":
		return "desktop"
	case "server":
		return "server"
	default:
		return ""
	}
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

func normalizeDeviceOS(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "windows":
		return "windows"
	case "macos":
		return "macos"
	case "linux":
		return "linux"
	case "win32":
		return "windows"
	case "darwin":
		return "macos"
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
		DeviceGrantResolveDir,
		DeviceGrantWriteFile,
		DeviceGrantEditFile,
		DeviceGrantSendFile,
		DeviceGrantExecuteShell,
		DeviceGrantGlob,
		DeviceGrantGrep,
		DeviceGrantBrowserControl,
		DeviceGrantDesktopControl,
		DeviceGrantSkillHubWorkspaceGet,
		DeviceGrantSkillHubWorkspaceApplyDefinition,
		DeviceCapabilitySkillHubWorkspacePagination,
		DeviceCapabilitySkillHubPublicationProbe,
		DeviceCapabilitySkillHubPublicationCapture,
		DeviceCapabilitySkillHubPublicationRepairSource,
		DeviceCapabilitySkillHubPublicationUpload,
		DeviceGrantSkillHubWorkspaceSync,
		DeviceGrantSkillHubSkillShare,
		DeviceGrantSkillHubSkillFinalize,
		DeviceGrantSkillHubSkillDelete,
		DeviceGrantSkillHubBotSwitch,
		DeviceGrantKnowledgeDocumentList,
		DeviceGrantKnowledgeDocumentRead:
		return true
	default:
		return false
	}
}

func buildCatsCoSessionKey(topicID string, topicType string, agentID string, actorUID int64) string {
	normalizedTopicType := normalizeTopicTypeForSessionKey(topicType)
	sessionTopicID := strings.TrimSpace(topicID)
	if normalizedTopicType == "group" && actorUID > 0 {
		sessionTopicID = sessionTopicID + ":actor:" + formatUID(actorUID)
	}
	parts := []string{
		"session",
		"v2",
		"catscompany",
		normalizedTopicType,
		encodeSessionKeyPart(sessionTopicID),
	}
	if agentID != "" {
		parts = append(parts, "agent", encodeSessionKeyPart(agentID))
	}
	return strings.Join(parts, ":")
}

func encodeSessionKeyPart(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return "unknown_topic"
	}
	return strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
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
	// Never trust a caller-supplied Bot binding. The authenticated Bot API key
	// is the only authority for associating a Runtime device with one Bot.
	req.BotUID = 0
	actorUID := UIDFromContext(r.Context())
	if actor, err := h.db.GetUser(actorUID); err == nil && actor != nil && actor.AccountType == types.AccountBot {
		req.BotUID = actorUID
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

func normalizeDeviceBotUID(value int64) int64 {
	if value > 0 {
		return value
	}
	return 0
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
