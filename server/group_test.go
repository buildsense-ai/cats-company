package server

import (
	"bytes"
	"context"
	"errors"
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

func newGroupInviteApprovalTestStore() *groupInviteApprovalTestStore {
	return &groupInviteApprovalTestStore{
		channelAgentTestStore: newChannelAgentTestStore(),
		requests:              make(map[int64]*types.GroupInviteRequest),
		nextID:                1,
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
