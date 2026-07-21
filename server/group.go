// Package server implements Cats Company group chat HTTP handlers.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// GroupHandler handles group-related API requests.
type GroupHandler struct {
	db  store.Store
	hub *Hub
}

// NewGroupHandler creates a new GroupHandler.
func NewGroupHandler(db store.Store, hub *Hub) *GroupHandler {
	return &GroupHandler{db: db, hub: hub}
}

func (h *GroupHandler) rejectChannelManagedGroup(w http.ResponseWriter, groupID int64) bool {
	group, err := h.db.GetGroup(groupID)
	if err != nil || group == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
		return true
	}
	if group.Kind != types.GroupKindChannelManaged {
		return false
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
	return true
}

// CreateGroupRequest is the JSON body for creating a group.
type CreateGroupRequest struct {
	Name      string  `json:"name"`
	MemberIDs []int64 `json:"member_ids"`
	Kind      string  `json:"kind,omitempty"`
}

type groupKindUpdater interface {
	UpdateGroupKind(groupID int64, kind string) error
}

// GroupActionRequest is the JSON body for group member actions.
type GroupActionRequest struct {
	GroupID int64   `json:"group_id"`
	UserIDs []int64 `json:"user_ids,omitempty"`
	UserID  int64   `json:"user_id,omitempty"`
	Role    string  `json:"role,omitempty"`
}

// ResolveGroupInviteRequest is the JSON body for approving or rejecting a
// member-proposed invitation.
type ResolveGroupInviteRequest struct {
	GroupID   int64  `json:"group_id"`
	RequestID int64  `json:"request_id"`
	Action    string `json:"action"`
}

// UpdateGroupRequest is the JSON body for updating group profile fields.
type UpdateGroupRequest struct {
	GroupID   int64  `json:"group_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// HandleCreateGroup handles POST /api/groups/create
func (h *GroupHandler) HandleCreateGroup(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req CreateGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "group name required"})
		return
	}
	if req.Kind == "" {
		req.Kind = types.GroupKindStandard
	}
	if req.Kind != types.GroupKindStandard && req.Kind != types.GroupKindAgentTask {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid group kind"})
		return
	}

	// Check total member count (creator + invited)
	if len(req.MemberIDs)+1 > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 200 members per group"})
		return
	}

	// Count bots in the member list
	botCount := 0
	requestedAgentIDs := make(map[int64]struct{})
	for _, mid := range req.MemberIDs {
		isBot, _ := h.db.IsUserBot(mid)
		if isBot {
			botCount++
			requestedAgentIDs[mid] = struct{}{}
		}
	}
	if botCount > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 10 bots per group"})
		return
	}
	if req.Kind == types.GroupKindAgentTask && (len(req.MemberIDs) != 1 || botCount != 1) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent task requires exactly one agent"})
		return
	}

	// Create the group (also creates topic and adds owner)
	groupID, err := h.db.CreateGroup(req.Name, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create group"})
		return
	}
	if req.Kind == types.GroupKindAgentTask {
		kindDB, ok := h.db.(groupKindUpdater)
		if !ok {
			_ = h.db.DeleteGroup(groupID)
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "agent tasks are unavailable"})
			return
		}
		if err := kindDB.UpdateGroupKind(groupID, req.Kind); err != nil {
			_ = h.db.DeleteGroup(groupID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create agent task"})
			return
		}
	}

	// Add initial members. Keep a local count as a fallback in case the
	// persistence count cannot be read back after creation.
	memberCount := 1
	agentIDs := make([]int64, 0, len(requestedAgentIDs))
	addedAgentIDs := make(map[int64]struct{}, len(requestedAgentIDs))
	for _, mid := range req.MemberIDs {
		if mid == uid {
			continue // owner already added
		}
		if err := h.db.AddGroupMember(groupID, mid, "member"); err != nil {
			if req.Kind == types.GroupKindAgentTask {
				_ = h.db.DeleteGroup(groupID)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to add agent to task"})
				return
			}
			continue
		}
		memberCount++
		if _, isAgent := requestedAgentIDs[mid]; isAgent {
			if _, alreadyAdded := addedAgentIDs[mid]; !alreadyAdded {
				agentIDs = append(agentIDs, mid)
				addedAgentIDs[mid] = struct{}{}
			}
		}
	}
	if persistedCount, countErr := h.db.GetGroupMemberCount(groupID); countErr == nil {
		memberCount = persistedCount
	}
	hasBot := botCount > 0
	if persistedBotCount, countErr := h.db.GetGroupBotCount(groupID); countErr == nil {
		hasBot = persistedBotCount > 0
	}

	topicID := fmt.Sprintf("grp_%d", groupID)
	group, groupErr := h.db.GetGroup(groupID)
	createdAt := time.Now()
	avatarURL := ""
	responseGroup := map[string]interface{}{
		"id":            groupID,
		"name":          req.Name,
		"owner_id":      uid,
		"avatar_url":    avatarURL,
		"created_at":    createdAt,
		"kind":          req.Kind,
		"has_bot":       hasBot,
		"is_agent_task": req.Kind == types.GroupKindAgentTask,
		"member_count":  memberCount,
		"agent_ids":     agentIDs,
	}
	if groupErr == nil && group != nil {
		createdAt = group.CreatedAt
		avatarURL = group.AvatarURL
		if group.AgentIDs != nil {
			agentIDs = append([]int64(nil), group.AgentIDs...)
		}
		responseGroup = map[string]interface{}{
			"id":            group.ID,
			"name":          group.Name,
			"owner_id":      group.OwnerID,
			"avatar_url":    group.AvatarURL,
			"max_members":   group.MaxMembers,
			"created_at":    group.CreatedAt,
			"kind":          group.Kind,
			"has_bot":       hasBot,
			"is_agent_task": group.Kind == types.GroupKindAgentTask,
			"member_count":  memberCount,
			"agent_ids":     agentIDs,
		}
	}

	// Notify all members via WebSocket
	h.notifyGroupEvent(groupID, "group_created", map[string]interface{}{
		"group_id": groupID,
		"name":     req.Name,
		"topic":    topicID,
	})

	response := map[string]interface{}{
		"group_id":     groupID,
		"topic":        topicID,
		"name":         req.Name,
		"group":        responseGroup,
		"created_at":   createdAt,
		"avatar_url":   avatarURL,
		"kind":         req.Kind,
		"has_bot":      hasBot,
		"is_agent_task": req.Kind == types.GroupKindAgentTask,
		"member_count": memberCount,
		"agent_ids":    agentIDs,
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleUpdateGroup handles POST /api/groups/update
func (h *GroupHandler) HandleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req UpdateGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if req.GroupID == 0 || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "group id and name required"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	role, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || (role != "owner" && role != "admin") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owner or admin can update group"})
		return
	}

	if err := h.db.UpdateGroupProfile(req.GroupID, req.Name, req.AvatarURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update group"})
		return
	}

	group, err := h.db.GetGroup(req.GroupID)
	if err != nil || group == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load updated group"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "group_updated", map[string]interface{}{
		"group_id":   req.GroupID,
		"name":       group.Name,
		"avatar_url": group.AvatarURL,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group": group,
	})
}

// HandleGetGroups handles GET /api/groups
func (h *GroupHandler) HandleGetGroups(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	groups, err := h.db.GetUserGroups(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get groups"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": groups})
}

// HandleGetGroupInfo handles GET /api/groups/info?id=xxx
func (h *GroupHandler) HandleGetGroupInfo(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	groupID, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid group id"})
		return
	}

	// Verify caller is a member
	isMember, err := h.db.IsGroupMember(groupID, uid)
	if err != nil || !isMember {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a group member"})
		return
	}

	group, err := h.db.GetGroup(groupID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
		return
	}
	if group.Kind == types.GroupKindChannelManaged {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
		return
	}

	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get members"})
		return
	}

	response := map[string]interface{}{
		"group":   group,
		"members": members,
	}
	role, roleErr := h.db.GetMemberRole(groupID, uid)
	if roleErr == nil && (role == "owner" || role == "admin") {
		if inviteStore, ok := h.db.(store.GroupInviteRequestStore); ok {
			requests, requestErr := inviteStore.ListPendingGroupInviteRequests(groupID)
			if requestErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get invite requests"})
				return
			}
			response["invite_requests"] = requests
		}
	}

	writeJSON(w, http.StatusOK, response)
}

// HandleInviteMembers handles POST /api/groups/invite
func (h *GroupHandler) HandleInviteMembers(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// All group members may propose an invite. Owners and admins add members
	// immediately; regular members create requests for approval.
	role, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || (role != "owner" && role != "admin" && role != "member") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only group members can invite"})
		return
	}

	seen := make(map[int64]struct{}, len(req.UserIDs))
	candidates := make([]int64, 0, len(req.UserIDs))
	for _, candidateID := range req.UserIDs {
		if candidateID <= 0 || candidateID == uid {
			continue
		}
		if _, exists := seen[candidateID]; exists {
			continue
		}
		seen[candidateID] = struct{}{}
		isMember, memberErr := h.db.IsGroupMember(req.GroupID, candidateID)
		if memberErr == nil && !isMember {
			candidates = append(candidates, candidateID)
		}
	}

	if role == "member" {
		inviteStore, ok := h.db.(store.GroupInviteRequestStore)
		if !ok {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "group invite approval is unavailable"})
			return
		}
		requested := 0
		for _, candidateID := range candidates {
			if _, requestErr := inviteStore.CreateGroupInviteRequest(req.GroupID, uid, candidateID); requestErr == nil {
				requested++
			}
		}
		if requested > 0 {
			h.notifyGroupEvent(req.GroupID, "group_invite_requests_updated", map[string]interface{}{
				"group_id": req.GroupID,
				"by":       uid,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"added": 0, "requested": requested})
		return
	}

	// Check member count limit
	currentCount, _ := h.db.GetGroupMemberCount(req.GroupID)
	if currentCount+len(candidates) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "would exceed max 200 members"})
		return
	}

	// Check bot limit
	currentBots, _ := h.db.GetGroupBotCount(req.GroupID)
	newBots := 0
	for _, mid := range candidates {
		isBot, _ := h.db.IsUserBot(mid)
		if isBot {
			newBots++
		}
	}
	if currentBots+newBots > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "would exceed max 10 bots"})
		return
	}

	added := 0
	addedIDs := make([]int64, 0, len(candidates))
	for _, mid := range candidates {
		if err := h.db.AddGroupMember(req.GroupID, mid, "member"); err == nil {
			added++
			addedIDs = append(addedIDs, mid)
		}
	}

	// Notify new members
	if added > 0 {
		h.notifyGroupEvent(req.GroupID, "members_invited", map[string]interface{}{
			"group_id": req.GroupID,
			"invited":  addedIDs,
			"by":       uid,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"added": added, "requested": 0})
}

// HandleResolveGroupInviteRequest handles POST /api/groups/invite/resolve.
func (h *GroupHandler) HandleResolveGroupInviteRequest(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	var req ResolveGroupInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GroupID <= 0 || req.RequestID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Action != "approve" && req.Action != "reject" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be approve or reject"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}
	role, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || (role != "owner" && role != "admin") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owner or admin can resolve invite requests"})
		return
	}
	inviteStore, ok := h.db.(store.GroupInviteRequestStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "group invite approval is unavailable"})
		return
	}
	inviteRequest, err := inviteStore.GetGroupInviteRequest(req.RequestID)
	if err != nil || inviteRequest.GroupID != req.GroupID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invite request not found"})
		return
	}
	if inviteRequest.Status != types.GroupInvitePending {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "invite request is no longer pending"})
		return
	}

	var resolved *types.GroupInviteRequest
	if req.Action == "approve" {
		isMember, memberErr := h.db.IsGroupMember(req.GroupID, inviteRequest.InviteeID)
		if memberErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check group membership"})
			return
		}
		if !isMember {
			memberCount, countErr := h.db.GetGroupMemberCount(req.GroupID)
			if countErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check group member limit"})
				return
			}
			if memberCount >= 200 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "would exceed max 200 members"})
				return
			}
			isBot, botErr := h.db.IsUserBot(inviteRequest.InviteeID)
			if botErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check invited user"})
				return
			}
			if isBot {
				botCount, countErr := h.db.GetGroupBotCount(req.GroupID)
				if countErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check group bot limit"})
					return
				}
				if botCount >= 10 {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "would exceed max 10 bots"})
					return
				}
			}
		}
		resolved, err = inviteStore.ApproveGroupInviteRequest(req.RequestID, uid)
	} else {
		resolved, err = inviteStore.RejectGroupInviteRequest(req.RequestID, uid)
	}
	if err != nil {
		if errors.Is(err, store.ErrGroupInviteRequestNotPending) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "invite request is no longer pending"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to resolve invite request"})
		return
	}

	if req.Action == "approve" {
		h.notifyGroupEvent(req.GroupID, "members_invited", map[string]interface{}{
			"group_id": req.GroupID,
			"invited":  []int64{inviteRequest.InviteeID},
			"by":       uid,
		})
	} else {
		h.notifyGroupEvent(req.GroupID, "group_invite_requests_updated", map[string]interface{}{
			"group_id": req.GroupID,
			"by":       uid,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"request": resolved})
}

// HandleLeaveGroup handles POST /api/groups/leave
func (h *GroupHandler) HandleLeaveGroup(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// Owner cannot leave
	role, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a group member"})
		return
	}
	if role == "owner" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner cannot leave, must disband or transfer"})
		return
	}

	if err := h.db.RemoveGroupMember(req.GroupID, uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to leave"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "member_left", map[string]interface{}{
		"group_id": req.GroupID,
		"user_id":  uid,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "left"})
}

// HandleKickMember handles POST /api/groups/kick
func (h *GroupHandler) HandleKickMember(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// Check caller is owner or admin
	callerRole, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || (callerRole != "owner" && callerRole != "admin") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owner or admin can kick"})
		return
	}

	// Cannot kick the owner
	targetRole, _ := h.db.GetMemberRole(req.GroupID, req.UserID)
	if targetRole == "owner" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot kick the owner"})
		return
	}

	// Admin cannot kick another admin
	if callerRole == "admin" && targetRole == "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin cannot kick another admin"})
		return
	}

	if err := h.db.RemoveGroupMember(req.GroupID, req.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to kick"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "member_kicked", map[string]interface{}{
		"group_id": req.GroupID,
		"user_id":  req.UserID,
		"by":       uid,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "kicked"})
}

// HandleDisbandGroup handles POST /api/groups/disband
func (h *GroupHandler) HandleDisbandGroup(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// Only owner can disband
	role, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || role != "owner" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owner can disband"})
		return
	}

	members, err := h.db.GetGroupMembers(req.GroupID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load group members"})
		return
	}

	memberIDs := make([]int64, 0, len(members))
	for _, member := range members {
		memberIDs = append(memberIDs, member.UserID)
	}

	if err := h.db.DeleteGroup(req.GroupID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to disband"})
		return
	}

	h.notifyGroupUserIDs(memberIDs, req.GroupID, "group_disbanded")

	writeJSON(w, http.StatusOK, map[string]string{"status": "disbanded"})
}

// HandleUpdateRole handles POST /api/groups/role
func (h *GroupHandler) HandleUpdateRole(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	if req.Role != "admin" && req.Role != "member" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be admin or member"})
		return
	}

	// Only owner can change roles
	callerRole, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || callerRole != "owner" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owner can change roles"})
		return
	}

	// Cannot change owner's own role
	if req.UserID == uid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot change own role"})
		return
	}

	if err := h.db.UpdateMemberRole(req.GroupID, req.UserID, req.Role); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update role"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "role_updated", map[string]interface{}{
		"group_id": req.GroupID,
		"user_id":  req.UserID,
		"role":     req.Role,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// notifyGroupEvent sends a real-time notification to all online group members.
func (h *GroupHandler) notifyGroupEvent(groupID int64, event string, data map[string]interface{}) {
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		return
	}
	userIDs := make([]int64, 0, len(members))
	for _, member := range members {
		userIDs = append(userIDs, member.UserID)
	}
	h.notifyGroupUserIDs(userIDs, groupID, event)
}

func (h *GroupHandler) notifyGroupUserIDs(userIDs []int64, groupID int64, event string) {
	if h.hub == nil {
		return
	}
	msg := &ServerMessage{
		Pres: &MsgServerPres{
			Topic: fmt.Sprintf("grp_%d", groupID),
			What:  event,
			Src:   fmt.Sprintf("grp_%d", groupID),
		},
	}
	for _, userID := range userIDs {
		h.hub.SendToUser(userID, msg)
	}
}

// HandleMuteMember handles POST /api/groups/mute
func (h *GroupHandler) HandleMuteMember(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// Check caller can manage target
	canManage, err := h.db.CanManageMember(req.GroupID, uid, req.UserID)
	if err != nil || !canManage {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no permission to mute"})
		return
	}

	if err := h.db.SetMemberMuted(req.GroupID, req.UserID, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to mute"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "member_muted", map[string]interface{}{
		"group_id": req.GroupID,
		"user_id":  req.UserID,
		"by":       uid,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "muted"})
}

// HandleUnmuteMember handles POST /api/groups/unmute
func (h *GroupHandler) HandleUnmuteMember(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req GroupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// Check caller can manage target
	canManage, err := h.db.CanManageMember(req.GroupID, uid, req.UserID)
	if err != nil || !canManage {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no permission to unmute"})
		return
	}

	if err := h.db.SetMemberMuted(req.GroupID, req.UserID, false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to unmute"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "member_unmuted", map[string]interface{}{
		"group_id": req.GroupID,
		"user_id":  req.UserID,
		"by":       uid,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "unmuted"})
}

// HandleSetAnnouncement handles POST /api/groups/announcement
func (h *GroupHandler) HandleSetAnnouncement(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req struct {
		GroupID      int64  `json:"group_id"`
		Announcement string `json:"announcement"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if h.rejectChannelManagedGroup(w, req.GroupID) {
		return
	}

	// Only owner or admin can set announcement
	role, err := h.db.GetMemberRole(req.GroupID, uid)
	if err != nil || (role != "owner" && role != "admin") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owner or admin can set announcement"})
		return
	}

	if err := h.db.SetGroupAnnouncement(req.GroupID, req.Announcement); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to set announcement"})
		return
	}

	h.notifyGroupEvent(req.GroupID, "announcement_updated", map[string]interface{}{
		"group_id":     req.GroupID,
		"announcement": req.Announcement,
		"by":           uid,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
