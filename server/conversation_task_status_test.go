package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestNormalizeConversationTaskStatusDefaultsActiveExpiry(t *testing.T) {
	status, err := normalizeConversationTaskStatus(42, "p2p_7_42", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{
			"state":   "running",
			"run_id":  "run-1",
			"summary": "building",
		},
	})
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.ExpiresAt == nil {
		t.Fatal("running status should receive a default expiry")
	}
	if got := status.ExpiresAt.Sub(status.UpdatedAt); got != defaultActiveTaskStatusTTL {
		t.Fatalf("default expiry=%s, want %s", got, defaultActiveTaskStatusTTL)
	}
}

func TestNormalizeConversationTaskStatusPreservesExplicitExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 7, 18, 8, 30, 0, 0, time.UTC)
	status, err := normalizeConversationTaskStatus(42, "grp_9", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{
			"state":      "waiting",
			"expires_at": expiresAt.Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.ExpiresAt == nil || !status.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at=%v, want %v", status.ExpiresAt, expiresAt)
	}
}

func TestNormalizeConversationTaskStatusLeavesTerminalExpiryOptional(t *testing.T) {
	status, err := normalizeConversationTaskStatus(42, "grp_9", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{"state": "completed"},
	})
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.ExpiresAt != nil {
		t.Fatalf("terminal expiry=%v, want nil", status.ExpiresAt)
	}
}
func TestValidateTaskStatusTransitionRejectsLateProgressForTerminalRun(t *testing.T) {
	current := taskStatusForTransition("run-1", "completed", 42)
	next := taskStatusForTransition("run-1", "running", 42)
	if err := store.ValidateConversationTaskStatusTransition(current, next, time.Now()); err == nil {
		t.Fatal("expected terminal run to reject late progress")
	}
}

func TestValidateTaskStatusTransitionAllowsAnotherActiveSource(t *testing.T) {
	current := taskStatusForTransition("run-1", "running", 42)
	next := taskStatusForTransition("run-2", "running", 43)
	if err := store.ValidateConversationTaskStatusTransition(current, next, time.Now()); err != nil {
		t.Fatalf("different source should not be rejected: %v", err)
	}
}

func taskStatusForTransition(runID, state string, sourceUID int64) *types.ConversationTaskStatus {
	return &types.ConversationTaskStatus{RunID: runID, State: state, SourceUID: sourceUID}
}

type taskRecoveryTestStore struct {
	store.Store
	active  []*types.ConversationTaskStatus
	current *types.ConversationTaskStatus
	upserts []*types.ConversationTaskStatus
}

func (s *taskRecoveryTestStore) ListActiveConversationTaskStatusesForSource(_ int64, _ time.Time) ([]*types.ConversationTaskStatus, error) {
	return s.active, nil
}

// MarkConversationTaskStatusStaleIfUnchanged models the database CAS: it only
// marks stale when the current row still matches the disconnected run and was
// not touched after the disconnection.
func (s *taskRecoveryTestStore) MarkConversationTaskStatusStaleIfUnchanged(topicID string, sourceUID int64, runID string, disconnectedAt time.Time) (*types.ConversationTaskStatus, bool, error) {
	if s.current == nil ||
		s.current.RunID != runID ||
		(s.current.State != "running" && s.current.State != "waiting") ||
		s.current.UpdatedAt.After(disconnectedAt) {
		return nil, false, nil
	}
	copyStatus := *s.current
	copyStatus.State = "stale"
	copyStatus.Summary = "机器人连接中断，任务已自动中止，可重新发送"
	copyStatus.Error = "bot disconnected before terminal task status"
	copyStatus.UpdatedAt = time.Now()
	s.current = &copyStatus
	s.upserts = append(s.upserts, &copyStatus)
	return &copyStatus, true, nil
}

func (s *taskRecoveryTestStore) GetConversationTaskStatusForSource(_ string, _ int64) (*types.ConversationTaskStatus, error) {
	return s.current, nil
}

func (s *taskRecoveryTestStore) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	copyStatus := *status
	copyStatus.UpdatedAt = time.Now()
	s.current = &copyStatus
	s.upserts = append(s.upserts, &copyStatus)
	return &copyStatus, nil
}

func (s *taskRecoveryTestStore) GetConversationTaskStatuses(_ []string) (map[string]*types.ConversationTaskStatus, error) {
	return map[string]*types.ConversationTaskStatus{}, nil
}

func TestRecoverDisconnectedBotTasksMarksOldActiveRunStale(t *testing.T) {
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hub := NewHub(db, nil)

	hub.recoverDisconnectedBotTasks(42, disconnectedAt)

	if len(db.upserts) != 1 {
		t.Fatalf("recovery upserts = %d, want 1", len(db.upserts))
	}
	if db.upserts[0].State != "stale" || db.upserts[0].RunID != "run-old" {
		t.Fatalf("recovered status = %+v", db.upserts[0])
	}
}

func TestRecoverDisconnectedBotTasksLeavesReconnectedBotRunning(t *testing.T) {
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hub := NewHub(db, nil)
	hub.clients[42] = map[*Client]struct{}{
		&Client{uid: 42, accountType: types.AccountBot}: {},
	}

	hub.recoverDisconnectedBotTasks(42, disconnectedAt)

	if len(db.upserts) != 0 {
		t.Fatalf("reconnected bot recovery upserts = %d, want 0", len(db.upserts))
	}
}

func TestRecoverDisconnectedBotTasksDoesNotOverwriteNewerRun(t *testing.T) {
	disconnectedAt := time.Now()
	candidate := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	current := &types.ConversationTaskStatus{
		TopicID:   candidate.TopicID,
		RunID:     "run-new",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(time.Second),
	}
	db := &taskRecoveryTestStore{
		active:  []*types.ConversationTaskStatus{candidate},
		current: current,
	}
	hub := NewHub(db, nil)

	hub.recoverDisconnectedBotTasks(42, disconnectedAt)

	if len(db.upserts) != 0 {
		t.Fatalf("newer run recovery upserts = %d, want 0", len(db.upserts))
	}
}

func TestRecoverDisconnectedBotTasksSkipsBotOnlineElsewhere(t *testing.T) {
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	shared := newSharedMemoryRuntimeState()
	dbA := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hubA := NewHubWithRuntime(dbA, nil, shared, "node-a")
	hubB := NewHubWithRuntime(nil, nil, shared, "node-b")

	// The bot disconnects from node A and reconnects to node B.
	if _, err := hubB.bodyLeases.acquire(42, "body-b", "conn-b"); err != nil {
		t.Fatalf("acquire body lease on node b: %v", err)
	}
	hubA.recoverDisconnectedBotTasks(42, disconnectedAt)

	if len(dbA.upserts) != 0 {
		t.Fatalf("recovery upserts while bot online elsewhere = %d, want 0", len(dbA.upserts))
	}
}

func TestRecoverDisconnectedBotTasksRunsWhenOfflineEverywhere(t *testing.T) {
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	shared := newSharedMemoryRuntimeState()
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hub := NewHubWithRuntime(db, nil, shared, "node-a")

	hub.recoverDisconnectedBotTasks(42, disconnectedAt)

	if len(db.upserts) != 1 {
		t.Fatalf("recovery upserts while offline everywhere = %d, want 1", len(db.upserts))
	}
}

func TestRecoverDisconnectedBotTasksSkipsOlderConnectionGeneration(t *testing.T) {
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hub := NewHub(db, nil)

	// Disconnect A: snapshot the generation at that moment.
	oldGeneration := hub.botConnectionEpoch(42)
	// The bot reconnects (registerClient bumps the generation) and disconnects
	// again; the old timer for disconnect A must not recover the new generation.
	hub.mu.Lock()
	hub.botConnectionEpochs[42]++
	hub.mu.Unlock()
	hub.recoverDisconnectedBotTasksIfSameGeneration(42, disconnectedAt, oldGeneration)

	if len(db.upserts) != 0 {
		t.Fatalf("old generation recovery upserts = %d, want 0", len(db.upserts))
	}
}

func TestRecoverDisconnectedBotTasksRunsForCurrentGeneration(t *testing.T) {
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-old",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hub := NewHub(db, nil)

	generation := hub.botConnectionEpoch(42)
	hub.recoverDisconnectedBotTasksIfSameGeneration(42, disconnectedAt, generation)

	if len(db.upserts) != 1 {
		t.Fatalf("current generation recovery upserts = %d, want 1", len(db.upserts))
	}
}
