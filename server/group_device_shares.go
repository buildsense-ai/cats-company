package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const groupDeviceShareSource = "group_device_share"
const groupDeviceShareCapability = "group-device-share.v1"

type GroupDeviceShareHandler struct{ hub *Hub }

func NewGroupDeviceShareHandler(hub *Hub) *GroupDeviceShareHandler {
	return &GroupDeviceShareHandler{hub: hub}
}

func (h *Hub) validGroupShare(ctx context.Context, s *store.GroupDeviceShare, agent int64) bool {
	if s == nil || s.Status != "active" || !isGroupTopic(s.TopicID) || s.OwnerUID == s.GranteeUID {
		return false
	}
	for _, uid := range []int64{s.OwnerUID, s.GranteeUID, agent} {
		if uid <= 0 {
			continue
		}
		role, err := h.db.GetMemberRole(extractGroupID(s.TopicID), uid)
		if err != nil || role == "" {
			return false
		}
	}
	shares, ok := h.db.(store.GroupDeviceShareStore)
	if !ok {
		return false
	}
	binding, err := shares.GetDeviceShareBinding(ctx, s.OwnerUID, s.DeviceID)
	if s.DeviceID != "" && (err != nil || binding == nil || !binding.Active || binding.Epoch != s.DeviceEpoch) {
		return false
	}
	owner, err := h.db.GetUser(s.OwnerUID)
	if err != nil || owner == nil || owner.State != 0 || owner.AccountType != types.AccountHuman {
		return false
	}
	actor, err := h.db.GetUser(s.GranteeUID)
	return err == nil && actor != nil && actor.State == 0 && actor.AccountType == types.AccountHuman
}

func (h *Hub) groupDeviceGrantCurrent(g ScopedDeviceGrant) bool {
	db, ok := h.db.(store.GroupDeviceShareStore)
	if !ok || g.ShareID == "" || g.ShareRevision <= 0 || g.IdentitySource != groupDeviceShareSource || g.TopicType != "group" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	agent := parseFormattedUID(g.AgentID)
	if agent <= 0 {
		return false
	}
	bot, err := h.db.GetUser(agent)
	if err != nil || bot == nil || bot.State != 0 || bot.AccountType != types.AccountBot {
		return false
	}
	s, err := db.GetGroupDeviceShare(ctx, g.ShareID)
	if err != nil || s == nil || s.Revision != g.ShareRevision || s.TopicID != g.TopicID || s.DeviceID != g.DeviceID ||
		s.OwnerUID != parseFormattedUID(g.OwnerUserID) || s.GranteeUID != parseFormattedUID(g.ActorUserID) || !h.validGroupShare(ctx, s, agent) {
		return false
	}
	for _, op := range g.Operations {
		if !containsString(s.Operations, string(op)) {
			return false
		}
	}
	return true
}

// Only the grantee receives shared device identities. The owner's full
// registry is never returned to other group members.
func (h *Hub) sharedGroupDevices(ctx context.Context, actor int64, topic string) ([]UserDevice, error) {
	db, ok := h.db.(store.GroupDeviceShareStore)
	if !ok {
		return nil, nil
	}
	shares, err := db.ListGroupDeviceShares(ctx, topic, actor)
	if err != nil {
		return nil, err
	}
	devices := []UserDevice{}
	for _, s := range shares {
		if s.GranteeUID != actor || !h.validGroupShare(ctx, &s, 0) {
			continue
		}
		device, exists := h.userDevices.registeredDevice(s.OwnerUID, s.DeviceID)
		if !exists {
			device = UserDevice{Kind: "user_device", Source: "catscompany", OwnerUID: s.OwnerUID, OwnerUserID: formatUID(s.OwnerUID), DeviceID: s.DeviceID, DisplayName: s.DeviceID, Status: "offline"}
		}
		if device.RuntimeRole == "server" {
			continue
		}
		classified, _ := h.classifyUserDevices(s.OwnerUID, []UserDevice{device})
		if len(classified) > 0 {
			device = classified[0]
		}
		device.ShareID, device.ShareRevision = s.ID, s.Revision
		if owner, err := h.db.GetUser(s.OwnerUID); err == nil && owner != nil {
			device.OwnerDisplayName = displayNameOrUsername(owner.DisplayName, owner.Username)
		}
		permitted := []DeviceGrantOperation{}
		for _, op := range device.Capabilities {
			if containsString(s.Operations, string(op)) {
				permitted = append(permitted, op)
			}
		}
		device.Capabilities = permitted
		// Connector receives the owner's ordinary RPC after server-side sharing
		// checks. Only the requesting Bot needs to understand shared targets.
		devices = append(devices, device)
	}
	return devices, nil
}

func (h *Hub) groupDeviceTurnContext(actor int64, topic string, agent *Client, own, unavailable []UserDevice, explicitTargetsEnabled bool) DeviceTurnContext {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shared, err := h.sharedGroupDevices(ctx, actor, topic)
	if err != nil {
		session := buildCatsCoSessionKey(topic, "group", formatUID(agent.uid), actor)
		preference, readErr := h.userDevices.selectionRecord(actor, session)
		if readErr == nil && (preference.DeviceID == "" || selectionOwner(preference, actor) == actor) {
			return h.userDevices.turnContextForOwnerDevicesWithExplicitTargets(actor, actor, topic, "group", agent.uid, agent.bodyID, own, unavailable, explicitTargetsEnabled)
		}
		return DeviceTurnContext{Selection: &DeviceSelection{Status: DeviceSelectionUnavailable, SelectionSource: "sharing_store_unavailable"}}
	}
	available, missing := h.classifyUserDevices(0, shared)
	// classifyUserDevices may replace route metadata; retain the negotiated fence.
	for i := range available {
		if available[i].UnavailableReason == "group_device_share_upgrade_required" {
			available[i].Routable = false
		}
	}
	devices := append(append([]UserDevice{}, own...), routableDevices(available)...)
	candidates := append(append([]UserDevice{}, unavailable...), missing...)
	for _, d := range available {
		if !d.Routable {
			candidates = append(candidates, d)
		}
	}
	selection, selected := h.userDevices.selectDeviceForTurn(actor, topic, "group", agent.uid, devices, candidates)
	if selection == nil {
		return DeviceTurnContext{}
	}
	if _, err := h.userDevices.ensureDeviceSelectionRevision(actor, selection.SessionKey); err != nil {
		selection.Status = DeviceSelectionUnavailable
		selection.SelectionSource = "selection_store_unavailable"
		return DeviceTurnContext{Selection: selection}
	}
	selection, selected = h.userDevices.selectDeviceForTurn(actor, topic, "group", agent.uid, devices, candidates)
	result := DeviceTurnContext{Selection: selection}
	if selected != nil && selection.Status == DeviceSelectionSelected {
		selection.OwnerUserID = selected.OwnerUserID
		if selected.ShareID == "" || agent.hasRuntimeCapability(groupDeviceShareCapability) {
			result.Grants = h.userDevices.grantsForOwnerDevices(actor, selected.OwnerUID, topic, "group", agent.uid, agent.bodyID, []UserDevice{*selected}, selection.Revision)
		} else {
			selection.Status = DeviceSelectionUnavailable
			selection.SelectionSource = "group_device_share_upgrade_required"
		}
	}
	if explicitTargetsEnabled {
		for _, device := range devices {
			if device.ShareID != "" && !agent.hasRuntimeCapability(groupDeviceShareCapability) {
				continue
			}
			result.ExplicitGrants = append(result.ExplicitGrants, h.userDevices.issueDeviceGrants(actor, device.OwnerUID, topic, "group", agent.uid, agent.bodyID, []UserDevice{device}, true, selection.Revision)...)
		}
	}
	return result
}

type groupDeviceShareWrite struct {
	ExpectedOperations []string `json:"expected_operations"`
	Action             string   `json:"action"`
	TopicID            string   `json:"topic_id"`
	GranteeUID         int64    `json:"grantee_uid"`
	DeviceID           string   `json:"device_id"`
	ExpectedRevision   *int64   `json:"expected_revision"`
}

func (h *GroupDeviceShareHandler) access(w http.ResponseWriter, r *http.Request, topic string) (store.GroupDeviceShareStore, int64, bool) {
	db, ok := h.hub.db.(store.GroupDeviceShareStore)
	if !ok {
		writeJSON(w, 503, map[string]string{"error": "sharing storage unavailable"})
		return nil, 0, false
	}
	uid := UIDFromContext(r.Context())
	if !isGroupTopic(topic) || extractGroupID(topic) <= 0 {
		writeJSON(w, 400, map[string]string{"error": "group topic required"})
		return nil, 0, false
	}
	if code, msg := h.hub.validateTopicReadAccess(uid, types.AccountHuman, topic); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return nil, 0, false
	}
	return db, uid, true
}

func decodeGroupShare(w http.ResponseWriter, r *http.Request) (groupDeviceShareWrite, bool) {
	var req groupDeviceShareWrite
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid sharing request"})
		return req, false
	}
	return req, true
}

func shareError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDeviceShareConflict) {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 503, map[string]string{"error": "sharing storage unavailable"})
}

func (h *GroupDeviceShareHandler) HandleShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		db, uid, ok := h.access(w, r, r.URL.Query().Get("topic_id"))
		if !ok {
			return
		}
		shares, err := db.ListGroupDeviceShares(r.Context(), r.URL.Query().Get("topic_id"), uid)
		if err != nil {
			shareError(w, err)
			return
		}
		devices, err := h.hub.sharedGroupDevices(r.Context(), uid, r.URL.Query().Get("topic_id"))
		if err != nil {
			shareError(w, err)
			return
		}
		members, err := h.hub.db.GetGroupMembers(extractGroupID(r.URL.Query().Get("topic_id")))
		if err != nil {
			shareError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"shares": shares, "devices": deviceSelectionCandidates(devices), "members": members, "actor_uid": uid, "share_operations": groupShareOperationSnapshot(humanToolTargetDevices(h.hub.userDevices.list(uid)))})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	req, ok := decodeGroupShare(w, r)
	if !ok {
		return
	}
	db, uid, ok := h.access(w, r, req.TopicID)
	if !ok {
		return
	}
	user, err := h.hub.db.GetUser(uid)
	if err != nil || user == nil || user.AccountType != types.AccountHuman {
		writeJSON(w, 403, map[string]string{"error": "only the device owner may share or revoke"})
		return
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision < 0 || (req.Action != "share" && req.Action != "revoke") {
		writeJSON(w, 400, map[string]string{"error": "action and expected_revision required"})
		return
	}
	var s *store.GroupDeviceShare
	if req.Action == "revoke" {
		shares, readErr := db.ListGroupDeviceShares(r.Context(), req.TopicID, uid)
		if readErr != nil {
			shareError(w, readErr)
			return
		}
		for _, existing := range shares {
			if existing.OwnerUID == uid && existing.GranteeUID == req.GranteeUID && existing.DeviceID == req.DeviceID {
				copy := existing
				copy.Status = "revoked"
				copy.UpdatedAt = unixMillis(time.Now())
				s = &copy
				break
			}
		}
		if s == nil {
			writeJSON(w, 404, map[string]string{"error": "sharing relationship not found"})
			return
		}
	} else {
		s, err = h.newShare(req.TopicID, uid, req.GranteeUID, req.DeviceID)
	}
	if err != nil {
		writeJSON(w, 403, map[string]string{"error": err.Error()})
		return
	}
	if req.Action == "share" && !sameShareOperations(req.ExpectedOperations, s.Operations) {
		writeJSON(w, 409, map[string]string{"error": "device operations changed; refresh and confirm the current scope"})
		return
	}
	saved, err := db.SaveGroupDeviceShare(r.Context(), *s, *req.ExpectedRevision)
	if err != nil {
		shareError(w, err)
		return
	}
	if saved.Status == "revoked" {
		h.hub.revokeGroupShareGrants(saved.ID)
	}
	notice := h.hub.publishGroupShareNotice(saved)
	writeJSON(w, 200, struct {
		*store.GroupDeviceShare
		Notice deviceChangeNotice `json:"notice"`
	}{saved, notice})
}

func (h *GroupDeviceShareHandler) newShare(topic string, owner, actor int64, deviceID string) (*store.GroupDeviceShare, error) {
	deviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return nil, err
	}
	id, err := randomHex(24)
	if err != nil {
		return nil, err
	}
	s := &store.GroupDeviceShare{ID: id, OwnerUID: owner, GranteeUID: actor, TopicID: topic, DeviceID: deviceID, Status: "active", UpdatedAt: unixMillis(time.Now()), Operations: []string{}}
	db, ok := h.hub.db.(store.GroupDeviceShareStore)
	if !ok {
		return nil, errors.New("sharing storage unavailable")
	}
	binding, err := db.GetDeviceShareBinding(context.Background(), owner, deviceID)
	if err != nil || binding == nil || !binding.Active {
		return nil, errors.New("device must reconnect with its paired Connector")
	}
	s.DeviceEpoch = binding.Epoch
	if !h.hub.validGroupShare(context.Background(), s, 0) {
		return nil, errors.New("both users must be active members of this group")
	}
	device, ok := h.hub.userDevices.registeredDevice(owner, deviceID)
	if !ok || device.RuntimeRole == "server" {
		return nil, errors.New("device is not registered to this owner")
	}
	for _, op := range deviceGrantOperations(device.Capabilities) {
		s.Operations = append(s.Operations, string(op))
	}
	return s, nil
}

// Cleanup is best-effort; every dispatch still reads the authoritative SQL
// revision, including a grant persisted concurrently with revocation.
func (h *Hub) revokeGroupShareGrants(id string) {
	var state *RedisRuntimeState
	if auth, ok := h.deviceAuthorization.(*RedisDeviceAuthorizationStore); ok {
		state = auth.state
	} else if shared, ok := h.userDevices.shared.(*RedisRuntimeState); ok {
		state = shared
	}
	if state != nil {
		ctx, cancel := context.WithTimeout(context.Background(), redisRuntimeWriteTimeout)
		defer cancel()
		key := state.key("device_grant_share", id)
		ids, err := state.client.SMembers(ctx, key).Result()
		if err != nil {
			return
		}
		for _, grantID := range ids {
			if grantID == permanentDeviceGrantIndexMarker {
				continue
			}
			if grant, ok := state.lookupDeviceGrant(grantID, unixMillis(time.Now())); ok {
				state.revokeDeviceGrant(grant)
			}
		}
		state.client.Del(ctx, key)
		return
	}
	if shared, ok := h.userDevices.shared.(*sharedMemoryRuntimeState); ok {
		shared.mu.Lock()
		defer shared.mu.Unlock()
		for grantID, grant := range shared.grants {
			if grant.ShareID == id {
				delete(shared.grants, grantID)
			}
		}
		return
	}
	h.userDevices.mu.Lock()
	defer h.userDevices.mu.Unlock()
	for grantID, grant := range h.userDevices.grants {
		if grant.ShareID == id {
			delete(h.userDevices.grants, grantID)
		}
	}
}

// The preview and the persisted permission use the same server-side filter.
func groupShareOperationSnapshot(devices []UserDevice) map[string][]string {
	result := make(map[string][]string, len(devices))
	for _, device := range devices {
		operations := []string{}
		for _, op := range deviceGrantOperations(device.Capabilities) {
			operations = append(operations, string(op))
		}
		result[device.DeviceID] = operations
	}
	return result
}

func sameShareOperations(expected, actual []string) bool {
	if expected == nil {
		return true
	} // Existing owner clients remain compatible.
	if len(expected) != len(actual) {
		return false
	}
	remaining := make(map[string]bool, len(actual))
	for _, op := range actual {
		remaining[op] = true
	}
	for _, op := range expected {
		if !remaining[op] {
			return false
		}
		delete(remaining, op)
	}
	return len(remaining) == 0
}
