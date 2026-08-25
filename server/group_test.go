package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type agentTaskMemberFailureStore struct {
	*channelAgentTestStore
	failUserID     int64
	deletedGroupID int64
}

type groupInviteApprovalTestStore struct {
	*channelAgentTestStore
	requests map[int64]*types.GroupInviteRequest
	nextID   int64
}

type groupMembershipLookupFailureStore struct {
	*channelAgentTestStore
}

func (s *groupMembershipLookupFailureStore) IsGroupMember(groupID, userID int64) (bool, error) {
	return false, errors.New("injected membership lookup failure")
}

func newGroupInviteApprovalTestStore() *groupInviteApprovalTestStore {
	return &groupInviteApprovalTestStore{
		channelAgentTestStore: newChannelAgentTestStore(),
		requests:              make(map[int64]*types.GroupInviteRequest),
		nextID:                1,
	}
}

func TestGetGroupInfoReportsMembershipLookupFailureAsServerError(t *testing.T) {
	db := &groupMembershipLookupFailureStore{channelAgentTestStore: newChannelAgentTestStore()}
	handler := NewGroupHandler(db, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/groups/info?id=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetGroupInfo(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to verify group membership") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

type groupLifecycleConversationStore struct {
	*channelAgentTestStore
}

func (s *groupLifecycleConversationStore) GetLatestMessagesForTopics([]string) (map[string]*types.Message, error) {
	return map[string]*types.Message{}, nil
}

func (s *groupLifecycleConversationStore) GetConversationTaskStatuses([]string) (map[string]*types.ConversationTaskStatus, error) {
	return map[string]*types.ConversationTaskStatus{}, nil
}

func (s *groupLifecycleConversationStore) ListMutedConversationTopics(context.Context, int64, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (s *groupLifecycleConversationStore) ListProjectTopics(int64) ([]*types.ProjectTopic, error) {
	return nil, nil
}

func TestKickMemberSendsAuthoritativeAccessRevokedOnlyToRemovedUser(t *testing.T) {
	db := &groupLifecycleConversationStore{channelAgentTestStore: newChannelAgentTestStore()}
	for _, uid := range []int64{7, 8, 42, 99} {
		db.users[uid] = &types.User{ID: uid, Username: fmt.Sprintf("user-%d", uid), AccountType: types.AccountHuman}
	}
	groupID, _ := db.CreateGroup("Lifecycle", 7)
	_ = db.AddGroupMember(groupID, 8, "member")
	_ = db.AddGroupMember(groupID, 42, "member")

	hub := NewHub(db, nil)
	clients := map[int64]*Client{}
	for _, uid := range []int64{7, 8, 42, 99} {
		clients[uid] = &Client{uid: uid, send: make(chan []byte, 4)}
		hub.addClient(clients[uid])
	}
	handler := NewGroupHandler(db, hub)
	req := httptest.NewRequest(http.MethodPost, "/api/groups/kick", bytes.NewBufferString(`{"group_id":1,"user_id":42}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleKickMember(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertPresenceWhat(t, clients[42].send, "group_access_revoked", "grp_1")
	assertPresenceWhat(t, clients[7].send, "member_kicked", "grp_1")
	assertPresenceWhat(t, clients[8].send, "member_kicked", "grp_1")
	if drainOne(clients[42].send) {
		t.Fatal("removed user received a remaining-member event")
	}
	if drainOne(clients[99].send) {
		t.Fatal("outsider received a group lifecycle event")
	}
	if member, _ := db.IsGroupMember(groupID, 42); member {
		t.Fatal("removed member still appears in authoritative membership")
	}

	// Even if the realtime presence is lost, the reconnect reconciliation API
	// is authoritative and must no longer expose the removed group.
	listReq := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), uidKey, int64(42)))
	listRec := httptest.NewRecorder()
	NewConversationHandler(db, hub).HandleList(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("conversation list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var body struct {
		Conversations []*types.ConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode conversation list: %v", err)
	}
	for _, conversation := range body.Conversations {
		if conversation.ID == "grp_1" {
			t.Fatal("removed group remained visible through GET /api/conversations")
		}
	}
}

func TestLeaveGroupSendsAccessRevokedToLeaverAndMemberLeftToRemainingUsers(t *testing.T) {
	db := newChannelAgentTestStore()
	for _, uid := range []int64{7, 8, 99} {
		db.users[uid] = &types.User{ID: uid, Username: fmt.Sprintf("user-%d", uid), AccountType: types.AccountHuman}
	}
	groupID, _ := db.CreateGroup("Lifecycle", 7)
	_ = db.AddGroupMember(groupID, 8, "member")
	hub := NewHub(db, nil)
	owner := &Client{uid: 7, send: make(chan []byte, 4)}
	leaver := &Client{uid: 8, send: make(chan []byte, 4)}
	outsider := &Client{uid: 99, send: make(chan []byte, 4)}
	hub.addClient(owner)
	hub.addClient(leaver)
	hub.addClient(outsider)
	handler := NewGroupHandler(db, hub)
	req := httptest.NewRequest(http.MethodPost, "/api/groups/leave", bytes.NewBufferString(`{"group_id":1}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(8)))
	rec := httptest.NewRecorder()

	handler.HandleLeaveGroup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertPresenceWhat(t, leaver.send, "group_access_revoked", "grp_1")
	assertPresenceWhat(t, owner.send, "member_left", "grp_1")
	if drainOne(outsider.send) {
		t.Fatal("outsider received a group lifecycle event")
	}
	if member, _ := db.IsGroupMember(groupID, 8); member {
		t.Fatal("leaver still appears in authoritative membership")
	}
}

func TestDisbandGroupUsesPreDeleteMembershipSnapshot(t *testing.T) {
	db := newChannelAgentTestStore()
	for _, uid := range []int64{7, 8} {
		db.users[uid] = &types.User{ID: uid, Username: fmt.Sprintf("user-%d", uid), AccountType: types.AccountHuman}
	}
	_, _ = db.CreateGroup("Lifecycle", 7)
	_ = db.AddGroupMember(1, 8, "member")
	hub := NewHub(db, nil)
	owner := &Client{uid: 7, send: make(chan []byte, 4)}
	member := &Client{uid: 8, send: make(chan []byte, 4)}
	hub.addClient(owner)
	hub.addClient(member)
	handler := NewGroupHandler(db, hub)
	req := httptest.NewRequest(http.MethodPost, "/api/groups/disband", bytes.NewBufferString(`{"group_id":1}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleDisbandGroup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertPresenceWhat(t, owner.send, "group_disbanded", "grp_1")
	assertPresenceWhat(t, member.send, "group_disbanded", "grp_1")
}

func assertPresenceWhat(t *testing.T, queue <-chan []byte, what, topic string) {
	t.Helper()
	var message ServerMessage
	decodeQueuedServerMessage(t, queue, &message)
	if message.Pres == nil || message.Pres.What != what || message.Pres.Topic != topic || message.Pres.Src != topic {
		t.Fatalf("presence=%#v want what=%s topic=%s", message.Pres, what, topic)
	}
}

func (s *groupInviteApprovalTestStore) CreateGroupInviteRequest(groupID, inviterID, inviteeID int64) (*types.GroupInviteRequest, error) {
	for _, request := range s.requests {
		if request.GroupID == groupID && request.InviteeID == inviteeID {
			request.Status = types.GroupInvitePending
			request.InviterID = inviterID
			request.UpdatedAt = time.Now()
			return request, nil
		}
	}
	now := time.Now()
	request := &types.GroupInviteRequest{
		ID:        s.nextID,
		GroupID:   groupID,
		InviterID: inviterID,
		InviteeID: inviteeID,
		Status:    types.GroupInvitePending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.nextID++
	if inviter := s.users[inviterID]; inviter != nil {
		request.InviterUsername = inviter.Username
		request.InviterDisplayName = inviter.DisplayName
	}
	if invitee := s.users[inviteeID]; invitee != nil {
		request.InviteeUsername = invitee.Username
		request.InviteeDisplayName = invitee.DisplayName
		request.InviteeAvatarURL = invitee.AvatarURL
		request.InviteeIsBot = invitee.AccountType == types.AccountBot && invitee.BotDisclose
	}
	s.requests[request.ID] = request
	return request, nil
}

func (s *groupInviteApprovalTestStore) GetGroupInviteRequest(requestID int64) (*types.GroupInviteRequest, error) {
	request := s.requests[requestID]
	if request == nil {
		return nil, errors.New("invite request not found")
	}
	return request, nil
}

func (s *groupInviteApprovalTestStore) ListPendingGroupInviteRequests(groupID int64) ([]*types.GroupInviteRequest, error) {
	requests := make([]*types.GroupInviteRequest, 0)
	for _, request := range s.requests {
		if request.GroupID == groupID && request.Status == types.GroupInvitePending {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func (s *groupInviteApprovalTestStore) ApproveGroupInviteRequest(requestID, resolverID int64) (*types.GroupInviteRequest, error) {
	request, err := s.GetGroupInviteRequest(requestID)
	if err != nil {
		return nil, err
	}
	if request.Status != types.GroupInvitePending {
		return nil, store.ErrGroupInviteRequestNotPending
	}
	if member, _ := s.IsGroupMember(request.GroupID, request.InviteeID); !member {
		if err := s.AddGroupMember(request.GroupID, request.InviteeID, "member"); err != nil {
			return nil, err
		}
	}
	request.Status = types.GroupInviteApproved
	request.ResolverID = resolverID
	request.UpdatedAt = time.Now()
	return request, nil
}

func (s *groupInviteApprovalTestStore) RejectGroupInviteRequest(requestID, resolverID int64) (*types.GroupInviteRequest, error) {
	request, err := s.GetGroupInviteRequest(requestID)
	if err != nil {
		return nil, err
	}
	if request.Status != types.GroupInvitePending {
		return nil, store.ErrGroupInviteRequestNotPending
	}
	request.Status = types.GroupInviteRejected
	request.ResolverID = resolverID
	request.UpdatedAt = time.Now()
	return request, nil
}

func (s *agentTaskMemberFailureStore) UpdateGroupKind(groupID int64, kind string) error {
	group := s.groups[groupID]
	if group == nil {
		return errors.New("group not found")
	}
	group.Kind = kind
	return nil
}

func (s *agentTaskMemberFailureStore) AddGroupMember(groupID, userID int64, role string) error {
	if userID == s.failUserID {
		return errors.New("injected member failure")
	}
	return s.channelAgentTestStore.AddGroupMember(groupID, userID, role)
}

func (s *agentTaskMemberFailureStore) DeleteGroup(groupID int64) error {
	s.deletedGroupID = groupID
	return s.channelAgentTestStore.DeleteGroup(groupID)
}

func TestCreateAgentTaskRollsBackWhenAgentCannotBeAdded(t *testing.T) {
	base := newChannelAgentTestStore()
	base.users[7] = &types.User{ID: 7, Username: "owner", AccountType: types.AccountHuman}
	base.users[42] = &types.User{ID: 42, Username: "agent", AccountType: types.AccountBot}
	db := &agentTaskMemberFailureStore{channelAgentTestStore: base, failUserID: 42}
	handler := NewGroupHandler(db, nil)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/groups/create",
		bytes.NewBufferString(`{"name":"Review task","member_ids":[42],"kind":"agent_task"}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleCreateGroup(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to add agent to task") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if db.deletedGroupID != 1 {
		t.Fatalf("deleted group=%d want=1", db.deletedGroupID)
	}
	if base.groups[1] != nil || base.groupMembers[1] != nil {
		t.Fatalf("partially created agent task was not removed")
	}
}

func TestCreateAgentTaskIncludesMemberMetadata(t *testing.T) {
	base := newChannelAgentTestStore()
	base.users[7] = &types.User{ID: 7, Username: "owner", AccountType: types.AccountHuman}
	base.users[42] = &types.User{ID: 42, Username: "agent", AccountType: types.AccountBot}
	db := &agentTaskMemberFailureStore{channelAgentTestStore: base, failUserID: -1}
	handler := NewGroupHandler(db, nil)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/groups/create",
		bytes.NewBufferString(`{"name":"Review task","member_ids":[42],"kind":"agent_task"}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleCreateGroup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		MemberCount int     `json:"member_count"`
		HasBot      bool    `json:"has_bot"`
		AgentIDs    []int64 `json:"agent_ids"`
		Group       struct {
			MemberCount int     `json:"member_count"`
			HasBot      bool    `json:"has_bot"`
			AgentIDs    []int64 `json:"agent_ids"`
		} `json:"group"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.MemberCount != 2 || body.Group.MemberCount != 2 {
		t.Fatalf("member count mismatch: %+v", body)
	}
	if !body.HasBot || !body.Group.HasBot {
		t.Fatalf("agent task should be marked has_bot: %+v", body)
	}
	if len(body.AgentIDs) != 1 || body.AgentIDs[0] != 42 || len(body.Group.AgentIDs) != 1 || body.Group.AgentIDs[0] != 42 {
		t.Fatalf("agent task should expose its agent IDs: %+v", body)
	}
}

func TestGroupMemberInviteRequiresOwnerOrAdminApproval(t *testing.T) {
	db := newGroupInviteApprovalTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "member", DisplayName: "Member", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "invitee", DisplayName: "Invitee", AccountType: types.AccountHuman}
	groupID, err := db.CreateGroup("Team", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.AddGroupMember(groupID, 8, "member"); err != nil {
		t.Fatalf("add proposer: %v", err)
	}
	handler := NewGroupHandler(db, nil)

	request := httptest.NewRequest(http.MethodPost, "/api/groups/invite", bytes.NewBufferString(`{"group_id":1,"user_ids":[9]}`))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(8)))
	recorder := httptest.NewRecorder()
	handler.HandleInviteMembers(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"requested":1`) {
		t.Fatalf("submit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if member, _ := db.IsGroupMember(groupID, 9); member {
		t.Fatal("regular member invite added the invitee before approval")
	}

	memberResolve := httptest.NewRequest(http.MethodPost, "/api/groups/invite/resolve", bytes.NewBufferString(`{"group_id":1,"request_id":1,"action":"approve"}`))
	memberResolve = memberResolve.WithContext(context.WithValue(memberResolve.Context(), uidKey, int64(8)))
	memberResolveRecorder := httptest.NewRecorder()
	handler.HandleResolveGroupInviteRequest(memberResolveRecorder, memberResolve)
	if memberResolveRecorder.Code != http.StatusForbidden {
		t.Fatalf("member approve status=%d body=%s", memberResolveRecorder.Code, memberResolveRecorder.Body.String())
	}
	if member, _ := db.IsGroupMember(groupID, 9); member {
		t.Fatal("regular member approved their own invite request")
	}

	resolve := httptest.NewRequest(http.MethodPost, "/api/groups/invite/resolve", bytes.NewBufferString(`{"group_id":1,"request_id":1,"action":"approve"}`))
	resolve = resolve.WithContext(context.WithValue(resolve.Context(), uidKey, int64(7)))
	resolveRecorder := httptest.NewRecorder()
	handler.HandleResolveGroupInviteRequest(resolveRecorder, resolve)

	if resolveRecorder.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", resolveRecorder.Code, resolveRecorder.Body.String())
	}
	if member, _ := db.IsGroupMember(groupID, 9); !member {
		t.Fatal("approved invite did not add the invitee")
	}
	if db.requests[1].Status != types.GroupInviteApproved || db.requests[1].ResolverID != 7 {
		t.Fatalf("unexpected resolved request: %+v", db.requests[1])
	}
}

func TestGroupAdminInviteAddsMemberImmediately(t *testing.T) {
	db := newGroupInviteApprovalTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", AccountType: types.AccountHuman}
	db.users[8] = &types.User{ID: 8, Username: "admin", AccountType: types.AccountHuman}
	db.users[9] = &types.User{ID: 9, Username: "invitee", AccountType: types.AccountHuman}
	groupID, _ := db.CreateGroup("Team", 7)
	_ = db.AddGroupMember(groupID, 8, "admin")
	handler := NewGroupHandler(db, nil)

	request := httptest.NewRequest(http.MethodPost, "/api/groups/invite", bytes.NewBufferString(`{"group_id":1,"user_ids":[9]}`))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(8)))
	recorder := httptest.NewRecorder()
	handler.HandleInviteMembers(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"added":1`) {
		t.Fatalf("invite status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if member, _ := db.IsGroupMember(groupID, 9); !member {
		t.Fatal("admin invite did not add the invitee immediately")
	}
	if len(db.requests) != 0 {
		t.Fatalf("admin invite unexpectedly created requests: %+v", db.requests)
	}
}
