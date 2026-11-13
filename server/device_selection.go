package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// This endpoint is human-JWT-only. Neither a Bot's API key nor a device
// connector credential can commit a user's conversation device selection.
func (h *DeviceHandler) HandleConversationSelection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil || h.db == nil || h.registry() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device registry unavailable"})
		return
	}
	// Verify here as well as at route registration so accidental re-wrapping
	// with the broader Bot-capable auth middleware cannot widen the boundary.
	claims, status, msg := ownerClaimsFromRequest(r, h.db.GetUser)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	actor := claims.UID
	topic := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	bot := parseFormattedUID(r.URL.Query().Get("agent_id"))
	if topic == "" || len(topic) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic_id and agent_id required"})
		return
	}
	if code, message := h.hub.validateTopicReadAccess(actor, types.AccountHuman, topic); code != 0 {
		writeJSON(w, code, map[string]string{"error": message})
		return
	}
	if bot <= 0 {
		if r.Method != http.MethodGet || !isGroupTopic(topic) || r.URL.Query().Get("agent_id") != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id required"})
			return
		}
		members, err := h.db.GetGroupMembers(extractGroupID(topic))
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation members unavailable"})
			return
		}
		agents := make([]map[string]string, 0)
		for _, member := range members {
			user, err := h.db.GetUser(member.UserID)
			if err == nil && user != nil && user.AccountType == types.AccountBot && user.State == 0 {
				agents = append(agents, map[string]string{"uid": formatUID(user.ID), "display_name": displayNameOrUsername(user.DisplayName, user.Username)})
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "choose_agent", "agents": agents})
		return
	}
	agent, err := h.db.GetUser(bot)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "conversation bot unavailable"})
		return
	}
	if err := h.hub.validateSkillMutationConversationAccess(topic, actor, bot); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bot is not accessible in this conversation"})
		return
	}
	session := buildCatsCoSessionKey(topic, topicTypeForID(topic), formatUID(bot), actor)
	if r.Method == http.MethodGet {
		// A versioned empty slot prevents a very old first-selection POST from
		// becoming valid again after the preference retention window expires.
		if _, err := h.registry().ensureDeviceSelectionRevision(actor, session); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "selection store unavailable"})
			return
		}
	}
	devices, _ := h.hub.classifyUserDevices(actor, h.registry().list(actor))
	known := make([]UserDevice, 0, len(devices))
	for _, device := range devices {
		if device.RuntimeRole != "server" {
			known = append(known, device)
		}
	}
	sort.Slice(known, func(i, j int) bool { return known[i].DeviceID < known[j].DeviceID })
	if r.Method == http.MethodPost {
		var req struct {
			DeviceID         string  `json:"device_id"`
			ExpectedRevision *string `json:"expected_revision"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || req.ExpectedRevision == nil || *req.ExpectedRevision == "" || len(*req.ExpectedRevision) > maxDeviceSelectionRevisionLength || decoder.Decode(&struct{}{}) != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id and expected_revision required"})
			return
		}
		device, found := findDeviceByID(known, req.DeviceID)
		if !found {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "device is not available to this account"})
			return
		}
		if !device.Routable {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "device is offline; refresh and try again"})
			return
		}
		if _, err := h.registry().confirmDeviceSelection(actor, session, device.DeviceID, "user_confirmation", *req.ExpectedRevision); err != nil {
			code := http.StatusServiceUnavailable
			message := "selection store unavailable"
			if errors.Is(err, errDeviceSelectionConflict) {
				code, message = http.StatusConflict, err.Error()
			}
			writeJSON(w, code, map[string]string{"error": message})
			return
		}
		h.hub.addDeviceAudit(actor, DeviceAuditEvent{DeviceID: device.DeviceID, Phase: "device_selection_confirmed", Result: "ok"})
	}
	preference, err := h.registry().selectionRecord(actor, session)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "selection store unavailable"})
		return
	}
	selectedID, state := preference.DeviceID, "needs_selection"
	if preference.active(unixMillis(h.registry().now())) {
		state = "unavailable"
		if device, found := findDeviceByID(known, selectedID); found && device.Routable {
			state = "selected"
		}
	} else if preference.DeviceID == "" && len(known) == 1 && known[0].Routable {
		// Display the single-device default. GET initializes a revision only;
		// it never chooses a device or issues an execution grant.
		selectedID, state = known[0].DeviceID, "automatic"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"topic_id": topic, "agent_id": formatUID(bot), "agent_name": displayNameOrUsername(agent.DisplayName, agent.Username), "revision": preference.Revision,
		"selected_device_id": selectedID, "status": state, "devices": deviceSelectionCandidates(known),
	})
}
