package server

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func prepareTestConversationTaskStatus(status *types.ConversationTaskStatus) *types.ConversationTaskStatus {
	prepared := store.PrepareConversationTaskStatusForStore(status, time.Now().UTC())
	if status != nil && prepared != nil {
		status.EventUpdatedAt = prepared.EventUpdatedAt
	}
	return prepared
}

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
	if !status.EventUpdatedAt.IsZero() {
		t.Fatalf("event_updated_at=%v without publisher timestamp, want zero for post-lock ordering", status.EventUpdatedAt)
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

func TestNormalizeConversationTaskStatusSeparatesPublisherEventTimeFromServerLiveness(t *testing.T) {
	eventUpdatedAt := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	before := time.Now().UTC()
	status, err := normalizeConversationTaskStatus(42, "p2p_7_42", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{
			"run_id":     "run-1",
			"state":      "running",
			"updated_at": eventUpdatedAt.Format(time.RFC3339),
		},
	})
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.UpdatedAt.Before(before) || status.UpdatedAt.After(after) {
		t.Fatalf("server updated_at=%v, want within [%v, %v]", status.UpdatedAt, before, after)
	}
	if !status.EventUpdatedAt.Equal(eventUpdatedAt) {
		t.Fatalf("event_updated_at=%v, want %v", status.EventUpdatedAt, eventUpdatedAt)
	}
}

func TestNormalizeConversationTaskStatusBoundsExtremeFuturePublisherTime(t *testing.T) {
	before := time.Now().UTC()
	status, err := normalizeConversationTaskStatus(42, "p2p_7_42", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{
			"run_id": "run-1", "state": "running",
			"updated_at": before.Add(24 * time.Hour).Format(time.RFC3339),
		},
	})
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.EventUpdatedAt.Before(before) || status.EventUpdatedAt.After(after) {
		t.Fatalf("bounded event_updated_at=%v, want within [%v, %v]", status.EventUpdatedAt, before, after)
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
	active     []*types.ConversationTaskStatus
	current    *types.ConversationTaskStatus
	upserts    []*types.ConversationTaskStatus
	generation uint64
	bumpErr    error
	listErr    error
}

func newTaskRecoveryTestHub(db store.Store) *Hub {
	return newTaskRecoveryTestHubWithRuntime(db, nil, "")
}

func newTaskRecoveryTestHubWithRuntime(db store.Store, shared sharedRuntimeState, nodeID string) *Hub {
	if nodeID == "" {
		nodeID = newRuntimeNodeID()
	}
	hub := &Hub{
		clients:             make(map[int64]map[*Client]struct{}),
		db:                  db,
		nodeID:              nodeID,
		sharedRuntime:       shared,
		bodyLeases:          newBotBodyLeaseManager(defaultBotBodyLeaseTTL).withSharedRuntime(shared, nodeID),
		groupTurns:          newGroupAgentTurnTracker(defaultGroupAgentTurnTTL),
		agentPush:           newAgentPushTurnCoordinator(),
		botConnectionEpochs: make(map[int64]uint64),
	}
	if shared != nil {
		shared.registerRuntimeNode(nodeID, hub)
	}
	return hub
}

func (s *taskRecoveryTestStore) GetFriends(_ int64) ([]*types.User, error) {
	return nil, nil
}

func (s *taskRecoveryTestStore) GetBotOwner(_ int64) (int64, error) {
	return 0, fmt.Errorf("no owner in test store")
}

func (s *taskRecoveryTestStore) ListActiveConversationTaskStatusesForSource(_ int64, _ time.Time) ([]*types.ConversationTaskStatus, error) {
	return s.active, nil
}

// ListAllActiveConversationTaskStatusesBefore returns the subset of active
// rows whose last update is at or before the cutoff, mirroring the durable
// reaper query.
func (s *taskRecoveryTestStore) ListAllActiveConversationTaskStatusesBefore(cutoff time.Time) ([]*types.ConversationTaskStatus, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []*types.ConversationTaskStatus
	for _, st := range s.active {
		if (st.State == "running" || st.State == "waiting") && !st.UpdatedAt.After(cutoff) {
			out = append(out, st)
		}
	}
	return out, nil
}

// MarkConversationTaskStatusStaleIfUnchanged models the database CAS: it only
// marks stale when the current row still matches the disconnected run, was not
// touched after the disconnection, and the cluster-wide bot connection
// generation still equals the snapshot the recovery timer took.
func (s *taskRecoveryTestStore) MarkConversationTaskStatusStaleIfUnchanged(topicID string, sourceUID int64, runID string, disconnectedAt time.Time, generation uint64) (*types.ConversationTaskStatus, bool, error) {
	if s.current == nil ||
		s.current.RunID != runID ||
		(s.current.State != "running" && s.current.State != "waiting") ||
		s.current.UpdatedAt.After(disconnectedAt) ||
		s.generation != generation {
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

func (s *taskRecoveryTestStore) BumpBotConnectionGeneration(botUID int64) (uint64, error) {
	if s.bumpErr != nil {
		return 0, s.bumpErr
	}
	s.generation++
	return s.generation, nil
}

func (s *taskRecoveryTestStore) BotConnectionGeneration(botUID int64) (uint64, error) {
	return s.generation, nil
}

func (s *taskRecoveryTestStore) GetConversationTaskStatusForSource(_ string, _ int64) (*types.ConversationTaskStatus, error) {
	return s.current, nil
}

func (s *taskRecoveryTestStore) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	prepared := prepareTestConversationTaskStatus(status)
	s.current = prepared
	s.upserts = append(s.upserts, prepared)
	return prepared, nil
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
	hub := newTaskRecoveryTestHub(db)
	deliveries := 0
	hub.observeAgentPushTaskStatus(active)
	hub.agentPush.observeVisibleMessage(7, 42, &ServerMessage{Data: &MsgServerData{
		Topic: active.TopicID, SeqID: 1, Type: "text", Content: "partial answer",
	}}, func() bool { deliveries++; return true })

	hub.recoverDisconnectedBotTasks(42, disconnectedAt, hub.botConnectionEpoch(42))

	if len(db.upserts) != 1 {
		t.Fatalf("recovery upserts = %d, want 1", len(db.upserts))
	}
	if db.upserts[0].State != "stale" || db.upserts[0].RunID != "run-old" {
		t.Fatalf("recovered status = %+v", db.upserts[0])
	}
	if deliveries != 1 {
		t.Fatalf("recovery terminal push deliveries = %d, want 1", deliveries)
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
	hub := newTaskRecoveryTestHub(db)
	hub.mu.Lock()
	hub.clients[42] = map[*Client]struct{}{
		&Client{uid: 42, accountType: types.AccountBot}: {},
	}
	hub.mu.Unlock()

	hub.recoverDisconnectedBotTasks(42, disconnectedAt, hub.botConnectionEpoch(42))

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
	hub := newTaskRecoveryTestHub(db)

	hub.recoverDisconnectedBotTasks(42, disconnectedAt, hub.botConnectionEpoch(42))

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
	hubA := newTaskRecoveryTestHubWithRuntime(dbA, shared, "node-a")
	hubB := newTaskRecoveryTestHubWithRuntime(nil, shared, "node-b")

	// The bot disconnects from node A and reconnects to node B.
	if _, err := hubB.bodyLeases.acquire(42, "body-b", "conn-b"); err != nil {
		t.Fatalf("acquire body lease on node b: %v", err)
	}
	hubA.recoverDisconnectedBotTasks(42, disconnectedAt, hubA.botConnectionEpoch(42))

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
	hub := newTaskRecoveryTestHubWithRuntime(db, shared, "node-a")

	hub.recoverDisconnectedBotTasks(42, disconnectedAt, hub.botConnectionEpoch(42))

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
	// Both nodes share the same generation store, so a bump on node B is visible
	// to node A (cluster-wide fencing), exactly like a shared database.
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hubA := newTaskRecoveryTestHub(db)
	hubB := newTaskRecoveryTestHub(db)

	// Disconnect A: snapshot the generation at that moment.
	oldGeneration := hubA.botConnectionEpoch(42)
	// The bot reconnects on node B (registerClient bumps the cluster-wide
	// generation) and disconnects again; the old timer for disconnect A must not
	// recover the new generation, even though A sees no local client.
	if _, err := db.BumpBotConnectionGeneration(42); err != nil {
		t.Fatalf("bump generation on node b: %v", err)
	}
	hubB.mu.Lock()
	hubB.botConnectionEpochs[42] = db.generation
	hubB.mu.Unlock()
	hubA.recoverDisconnectedBotTasksIfSameGeneration(42, disconnectedAt, oldGeneration)

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
	hub := newTaskRecoveryTestHub(db)

	generation := hub.botConnectionEpoch(42)
	hub.recoverDisconnectedBotTasksIfSameGeneration(42, disconnectedAt, generation)

	if len(db.upserts) != 1 {
		t.Fatalf("current generation recovery upserts = %d, want 1", len(db.upserts))
	}
}

func TestRecoverDisconnectedBotTasksCrossNodeGenerationBumpedElsewhere(t *testing.T) {
	// Regression for the cross-node race: node A schedules a timer for disconnect
	// 1, the bot reconnects on node B (bumping the cluster-wide generation), then
	// disconnects from B. Timer A fires while the bot is offline everywhere, but
	// the generation it snapshotted is stale, so it must NOT recover the new
	// generation's work.
	disconnectedAt := time.Now()
	active := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-new-gen",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: disconnectedAt.Add(-time.Minute),
	}
	shared := newSharedMemoryRuntimeState()
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{active}, current: active}
	hubA := newTaskRecoveryTestHubWithRuntime(db, shared, "node-a")
	hubB := newTaskRecoveryTestHubWithRuntime(db, shared, "node-b")

	// Disconnect 1 on node A: snapshot generation 0.
	oldGeneration := hubA.botConnectionEpoch(42)
	if oldGeneration != 0 {
		t.Fatalf("initial generation = %d, want 0", oldGeneration)
	}
	// Bot reconnects on node B (bump) ...
	if _, err := hubB.bodyLeases.acquire(42, "body-b", "conn-b"); err != nil {
		t.Fatalf("acquire body lease on node b: %v", err)
	}
	if _, err := db.BumpBotConnectionGeneration(42); err != nil {
		t.Fatalf("bump generation on node b: %v", err)
	}
	// ... and disconnects from B: lease released, bot offline everywhere.
	if !hubB.bodyLeases.release(42, "body-b", "conn-b") {
		t.Fatalf("release body lease on node b failed")
	}
	// Timer A fires with its stale snapshot: must not recover.
	hubA.recoverDisconnectedBotTasksIfSameGeneration(42, disconnectedAt, oldGeneration)

	if len(db.upserts) != 0 {
		t.Fatalf("cross-node stale generation recovery upserts = %d, want 0", len(db.upserts))
	}
}

func TestRegisterClientRejectsBotWhenGenerationBumpFails(t *testing.T) {
	// Fail-closed: when a cluster-wide generation store is configured but the
	// durable bump fails, the connection must be rejected. Accepting it with
	// only a process-local bump would leave the new generation invisible to
	// other nodes (botConnectionEpoch reads the persisted value), so an old
	// recovery timer could still mark the fresh connection's work stale.
	db := &taskRecoveryTestStore{bumpErr: errors.New("db hiccup")}
	hub := NewHub(db, nil)
	if _, err := hub.bodyLeases.acquire(42, "body-fail", "conn-fail"); err != nil {
		t.Fatalf("acquire body lease: %v", err)
	}
	client := &Client{
		uid:          42,
		accountType:  types.AccountBot,
		bodyID:       "body-fail",
		connectionID: "conn-fail",
		send:         make(chan []byte, 256),
	}

	accepted := hub.registerClient(client)

	if accepted {
		t.Fatalf("bot connection accepted despite durable generation bump failure")
	}
	if len(hub.getClients(42)) != 0 {
		t.Fatalf("rejected bot left a registered client")
	}
	if db.generation != 0 {
		t.Fatalf("rejected bot still advanced generation = %d", db.generation)
	}
}

func TestRegisterClientBumpsDurableGenerationBeforeAccept(t *testing.T) {
	// The durable bump must complete before the bot is accepted so every node
	// observes the new generation the moment the bot can carry tasks.
	db := &taskRecoveryTestStore{}
	hub := NewHub(db, nil)
	if _, err := hub.bodyLeases.acquire(42, "body-ok", "conn-ok"); err != nil {
		t.Fatalf("acquire body lease: %v", err)
	}
	client := &Client{
		uid:          42,
		accountType:  types.AccountBot,
		bodyID:       "body-ok",
		connectionID: "conn-ok",
		send:         make(chan []byte, 256),
	}

	accepted := hub.registerClient(client)

	if !accepted {
		t.Fatalf("bot connection rejected when generation bump succeeds")
	}
	if db.generation != 1 {
		t.Fatalf("generation after accept = %d, want 1", db.generation)
	}
	if hub.botConnectionEpoch(42) != 1 {
		t.Fatalf("botConnectionEpoch after accept = %d, want 1", hub.botConnectionEpoch(42))
	}
	if len(hub.getClients(42)) != 1 {
		t.Fatalf("accepted bot not registered")
	}
}

func TestConversationTaskReaperMarksExpiredOfflineRunStale(t *testing.T) {
	now := time.Now()
	candidate := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-expired",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: now.Add(-10 * time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{candidate}, current: candidate}
	hub := newTaskRecoveryTestHub(db)
	deliveries := 0
	hub.observeAgentPushTaskStatus(candidate)
	hub.agentPush.observeVisibleMessage(7, 42, &ServerMessage{Data: &MsgServerData{
		Topic: candidate.TopicID, SeqID: 1, Type: "text", Content: "partial answer",
	}}, func() bool { deliveries++; return true })

	hub.recoverStaleDisconnectedBotTasks(now)

	if len(db.upserts) != 1 {
		t.Fatalf("reaper upserts = %d, want 1", len(db.upserts))
	}
	if db.upserts[0].State != "stale" || db.upserts[0].RunID != "run-expired" {
		t.Fatalf("reaped status = %+v", db.upserts[0])
	}
	if deliveries != 1 {
		t.Fatalf("reaper terminal push deliveries = %d, want 1", deliveries)
	}
}

func TestConversationTaskReaperSkipsBotStillOnlineLocally(t *testing.T) {
	now := time.Now()
	candidate := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-expired",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: now.Add(-10 * time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{candidate}, current: candidate}
	hub := newTaskRecoveryTestHub(db)
	hub.mu.Lock()
	hub.clients[42] = map[*Client]struct{}{
		&Client{uid: 42, accountType: types.AccountBot}: {},
	}
	hub.mu.Unlock()

	hub.recoverStaleDisconnectedBotTasks(now)

	if len(db.upserts) != 0 {
		t.Fatalf("reaper upserts for online bot = %d, want 0", len(db.upserts))
	}
}

func TestConversationTaskReaperSkipsRunNewerThanCutoff(t *testing.T) {
	now := time.Now()
	// The row was updated after the cutoff (e.g. the bot sent a fresh status
	// update), so it must not be reaped even though it is older than the grace.
	candidate := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-fresh",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: now.Add(30 * time.Second),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{candidate}, current: candidate}
	hub := newTaskRecoveryTestHub(db)

	hub.recoverStaleDisconnectedBotTasks(now)

	if len(db.upserts) != 0 {
		t.Fatalf("reaper upserts for fresh run = %d, want 0", len(db.upserts))
	}
}

func TestConversationTaskReaperSkipsBotOnlineElsewhere(t *testing.T) {
	now := time.Now()
	candidate := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-expired",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: now.Add(-10 * time.Minute),
	}
	shared := newSharedMemoryRuntimeState()
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{candidate}, current: candidate}
	hubA := newTaskRecoveryTestHubWithRuntime(db, shared, "node-a")
	hubB := newTaskRecoveryTestHubWithRuntime(nil, shared, "node-b")

	// The bot reconnects to node B while node A's reaper sweeps.
	if _, err := hubB.bodyLeases.acquire(42, "body-b", "conn-b"); err != nil {
		t.Fatalf("acquire body lease on node b: %v", err)
	}
	hubA.recoverStaleDisconnectedBotTasks(now)

	if len(db.upserts) != 0 {
		t.Fatalf("reaper upserts while bot online elsewhere = %d, want 0", len(db.upserts))
	}
}

func TestConversationTaskReaperRetriesAfterListError(t *testing.T) {
	now := time.Now()
	candidate := &types.ConversationTaskStatus{
		TopicID:   "p2p_7_42",
		RunID:     "run-expired",
		State:     "running",
		SourceUID: 42,
		UpdatedAt: now.Add(-10 * time.Minute),
	}
	db := &taskRecoveryTestStore{active: []*types.ConversationTaskStatus{candidate}, current: candidate}
	hub := newTaskRecoveryTestHub(db)

	// First sweep fails transiently (e.g. DB hiccup): nothing reaped.
	db.listErr = fmt.Errorf("transient db error")
	hub.recoverStaleDisconnectedBotTasks(now)
	if len(db.upserts) != 0 {
		t.Fatalf("reaper upserts on list error = %d, want 0", len(db.upserts))
	}

	// Next sweep succeeds and converges.
	db.listErr = nil
	hub.recoverStaleDisconnectedBotTasks(now)
	if len(db.upserts) != 1 {
		t.Fatalf("reaper upserts after retry = %d, want 1", len(db.upserts))
	}
	if db.upserts[0].State != "stale" {
		t.Fatalf("reaped status = %+v", db.upserts[0])
	}
}
