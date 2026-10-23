package server

import (
	"errors"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type taskDeliveryStore struct {
	store.Store
	pending       []store.ConversationTaskStatusEvent
	aggregate     *types.ConversationTaskStatus
	members       []*types.GroupMember
	visibilityErr error
	managed       bool
	completeErr   error
	completed     int
	runs          []store.ConversationTaskRun
	historyCalls  int
	before        int64
}

func (s *taskDeliveryStore) ClaimConversationTaskStatusEvents(int, time.Duration) ([]store.ConversationTaskStatusEvent, error) {
	return s.pending, nil
}
func (s *taskDeliveryStore) CompleteConversationTaskStatusEvent(id int64, claim string) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.completed++
	for i, e := range s.pending {
		if e.ID == id && e.Claim == claim {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			break
		}
	}
	return nil
}
func (s *taskDeliveryStore) GetConversationTaskStatuses([]string) (map[string]*types.ConversationTaskStatus, error) {
	out := map[string]*types.ConversationTaskStatus{}
	if s.aggregate != nil {
		out[s.aggregate.TopicID] = s.aggregate
	}
	return out, nil
}
func (s *taskDeliveryStore) GetConversationTaskStatusForSource(string, int64) (*types.ConversationTaskStatus, error) {
	return s.aggregate, nil
}
func (s *taskDeliveryStore) UpsertConversationTaskStatus(*types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	return s.aggregate, nil
}
func (s *taskDeliveryStore) GetGroupMembers(int64) ([]*types.GroupMember, error) {
	return s.members, nil
}
func (s *taskDeliveryStore) IsChannelManagedGroup(int64) (bool, error) {
	return s.managed, s.visibilityErr
}
func (s *taskDeliveryStore) IsUserBot(uid int64) (bool, error) { return uid != 7, nil }
func (s *taskDeliveryStore) IsGroupMember(_ int64, uid int64) (bool, error) {
	for _, member := range s.members {
		if member.UserID == uid {
			return true, nil
		}
	}
	return false, nil
}
func (s *taskDeliveryStore) ListConversationTaskRuns(topic string, before int64, limit int) ([]store.ConversationTaskRun, error) {
	s.historyCalls++
	s.before = before
	return s.runs, nil
}

func taskDeliveryFixture() *taskDeliveryStore {
	now := time.Now().UTC()
	return &taskDeliveryStore{
		aggregate: &types.ConversationTaskStatus{TopicID: "grp_80", SourceUID: 42, RunID: "a", State: "running", UpdatedAt: now},
		pending:   []store.ConversationTaskStatusEvent{{ID: 9, Claim: "claim", Status: &types.ConversationTaskStatus{TopicID: "grp_80", SourceUID: 43, RunID: "b", State: "failed", UpdatedAt: now}}},
		members:   []*types.GroupMember{{UserID: 7}, {UserID: 42}, {UserID: 43}},
	}
}
func taskDeliveryClient(h *Hub, uid int64) *Client {
	c := &Client{hub: h, uid: uid, accountType: types.AccountHuman, send: make(chan []byte, 64)}
	h.clients[uid] = map[*Client]struct{}{c: {}}
	return c
}

func TestConversationTaskStatusDeliveryKeepsSourceTerminalAndLegacyAggregate(t *testing.T) {
	db := taskDeliveryFixture()
	h := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(h, 7)
	h.dispatchConversationTaskStatusEvents()
	var msg ServerMessage
	decodeQueuedServerMessage(t, user.send, &msg)
	if msg.TaskStatus == nil || msg.TaskStatus.State != "running" || msg.TaskStatus.SourceUID != 42 {
		t.Fatalf("legacy aggregate changed meaning: %+v", msg)
	}
	if msg.TaskStatusSource == nil || msg.TaskStatusSource.RunID != "b" || msg.TaskStatusSource.State != "failed" || msg.TaskStatusEventID != 9 {
		t.Fatalf("terminal receipt hidden: %+v", msg)
	}
	if db.completed != 1 || len(db.pending) != 0 {
		t.Fatal("event was not completed")
	}
}

func TestConversationTaskStatusDeliveryRetriesVisibilityFailureWithoutLeaking(t *testing.T) {
	db := taskDeliveryFixture()
	db.visibilityErr = errors.New("database temporarily unavailable")
	h := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(h, 7)
	h.dispatchConversationTaskStatusEvents()
	if db.completed != 0 || len(db.pending) != 1 || len(user.send) != 0 {
		t.Fatal("visibility failure leaked or discarded event")
	}
	db.visibilityErr = nil
	h.dispatchConversationTaskStatusEvents()
	if db.completed != 1 || len(user.send) != 1 {
		t.Fatal("pending event not retried")
	}
}

func TestConversationTaskStatusDeliveryFiltersChannelManagedHumans(t *testing.T) {
	db := taskDeliveryFixture()
	db.managed = true
	h := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(h, 7)
	bot := taskDeliveryClient(h, 42)
	h.dispatchConversationTaskStatusEvents()
	if len(user.send) != 0 || len(bot.send) != 1 {
		t.Fatal("channel-managed visibility changed")
	}
}

func TestConversationTaskStatusDeliveryCrossesRuntimeNodes(t *testing.T) {
	db := taskDeliveryFixture()
	shared := newSharedMemoryRuntimeState()
	origin := newTaskRecoveryTestHubWithRuntime(db, shared, "origin")
	remote := newTaskRecoveryTestHubWithRuntime(db, shared, "remote")
	user := taskDeliveryClient(remote, 7)
	origin.dispatchConversationTaskStatusEvents()
	var msg ServerMessage
	decodeQueuedServerMessage(t, user.send, &msg)
	if msg.TaskStatusSource == nil || msg.TaskStatusSource.RunID != "b" {
		t.Fatal("source event did not reach remote node")
	}
}

func TestConversationTaskStatusDeliveryReplaysAfterCompletionFailure(t *testing.T) {
	db := taskDeliveryFixture()
	db.completeErr = errors.New("commit unavailable")
	h := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(h, 7)
	h.dispatchConversationTaskStatusEvents()
	if len(db.pending) != 1 {
		t.Fatal("unacknowledged event discarded")
	}
	db.completeErr = nil
	h.dispatchConversationTaskStatusEvents()
	if len(user.send) != 2 || len(db.pending) != 0 {
		t.Fatal("at-least-once replay failed")
	}
}

func TestConversationTaskStatusDeliveryDoesNotResurrectExpiredAggregate(t *testing.T) {
	db := taskDeliveryFixture()
	db.aggregate = nil
	h := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(h, 7)
	h.dispatchConversationTaskStatusEvents()
	var msg ServerMessage
	decodeQueuedServerMessage(t, user.send, &msg)
	if msg.TaskStatus == nil || msg.TaskStatus.State != "idle" || msg.TaskStatusSource == nil {
		t.Fatalf("expired aggregate resurrected: %+v", msg)
	}
}

func TestConversationTaskStatusHistoryChecksMembershipAndVisibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		uid     int64
		managed bool
		code    int
	}{
		{"nonmember", 8, false, 403}, {"managed human", 7, true, 404}, {"member", 7, false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := taskDeliveryFixture()
			db.managed = tc.managed
			h := newTaskRecoveryTestHub(db)
			user := taskDeliveryClient(h, tc.uid)
			h.handleGet(user, &MsgClientGet{ID: "history", Topic: "grp_80", What: "task_statuses", BeforeID: 123})
			var msg ServerMessage
			decodeQueuedServerMessage(t, user.send, &msg)
			if msg.Ctrl == nil || msg.Ctrl.Code != tc.code {
				t.Fatalf("response=%+v", msg)
			}
			if tc.code != 200 && db.historyCalls != 0 {
				t.Fatal("unauthorized request queried runs")
			}
			if tc.code == 200 && db.before != 123 {
				t.Fatal("pagination cursor not forwarded")
			}
		})
	}
}

func TestConversationTaskStatusHistoryRejectsNegativeCursor(t *testing.T) {
	db := taskDeliveryFixture()
	h := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(h, 7)
	h.handleGet(user, &MsgClientGet{Topic: "grp_80", What: "task_statuses", BeforeID: -1})
	var msg ServerMessage
	decodeQueuedServerMessage(t, user.send, &msg)
	if msg.Ctrl == nil || msg.Ctrl.Code != 400 || db.historyCalls != 0 {
		t.Fatalf("negative cursor response=%+v", msg)
	}
}
