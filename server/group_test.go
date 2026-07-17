package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type agentTaskMemberFailureStore struct {
	*channelAgentTestStore
	failUserID     int64
	deletedGroupID int64
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
