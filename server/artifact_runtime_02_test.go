package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type artifactRuntime02MemoryStore struct {
	*artifactRuntimeMemoryStore
	runMu         sync.Mutex
	runsByTask    map[string]*store.ArtifactRuntimeRun
	taskByRef     map[string]string
	taskByRun     map[string]string
	lastPolicy    store.ArtifactRuntimeRunCreatePolicy
	messageMu     sync.Mutex
	messageIDs    map[string]int64
	nextMessageID int64
}

func (s *artifactRuntime02MemoryStore) CreateTopic(string, string, int64) error { return nil }

func (s *artifactRuntime02MemoryStore) IsMemberMuted(int64, int64) (bool, error) {
	return false, nil
}

func (s *artifactRuntime02MemoryStore) SaveMessageIdempotent(
	topicID string,
	fromUID int64,
	content string,
	blocks []types.ContentBlock,
	mode, role, msgType string,
	replyTo int64,
	clientMsgID string,
) (int64, bool, error) {
	s.messageMu.Lock()
	defer s.messageMu.Unlock()
	if s.messageIDs == nil {
		s.messageIDs = make(map[string]int64)
	}
	key := fmt.Sprintf("%s:%d:%s", topicID, fromUID, clientMsgID)
	if id := s.messageIDs[key]; id > 0 {
		return id, true, nil
	}
	s.nextMessageID++
	s.messageIDs[key] = s.nextMessageID
	if s.identityMessageStore != nil {
		s.identityMessageStore.history = append(s.identityMessageStore.history, &types.Message{
			ID: s.nextMessageID, TopicID: topicID, FromUID: fromUID, Content: content,
			ContentBlocks: blocks, Mode: mode, Role: role, MsgType: msgType,
		})
	}
	return s.nextMessageID, false, nil
}

func cloneRuntime02Run(run *store.ArtifactRuntimeRun) *store.ArtifactRuntimeRun {
	if run == nil {
		return nil
	}
	clone := *run
	clone.InputSchema = append(json.RawMessage(nil), run.InputSchema...)
	clone.Payload = append(json.RawMessage(nil), run.Payload...)
	clone.PageContext = append(json.RawMessage(nil), run.PageContext...)
	clone.AppliedEventIDs = append([]int64(nil), run.AppliedEventIDs...)
	if run.DeliveryClaimedAt != nil {
		claimedAt := *run.DeliveryClaimedAt
		clone.DeliveryClaimedAt = &claimedAt
	}
	return &clone
}

func (s *artifactRuntime02MemoryStore) CreateArtifactRuntimeRun(_ context.Context, run *store.ArtifactRuntimeRun, policy store.ArtifactRuntimeRunCreatePolicy) (*store.ArtifactRuntimeRun, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.lastPolicy = policy
	if s.runsByTask[run.TaskID] != nil || s.taskByRef[run.TaskRefHash] != "" || s.taskByRun[run.RunID] != "" {
		return nil, store.ErrArtifactRuntimeRunConflict
	}
	clone := cloneRuntime02Run(run)
	clone.Status = "submitted"
	if clone.CreatedAt.IsZero() {
		clone.CreatedAt = s.now
	}
	clone.UpdatedAt = clone.CreatedAt
	s.runsByTask[clone.TaskID] = clone
	s.taskByRef[clone.TaskRefHash] = clone.TaskID
	s.taskByRun[clone.RunID] = clone.TaskID
	return cloneRuntime02Run(clone), nil
}

func (s *artifactRuntime02MemoryStore) GetArtifactRuntimeRunByTask(_ context.Context, taskID string, actorUID int64) (*store.ArtifactRuntimeRun, bool, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	run := s.runsByTask[taskID]
	if run == nil || run.ActorUID != actorUID {
		return nil, false, nil
	}
	return cloneRuntime02Run(run), true, nil
}

func (s *artifactRuntime02MemoryStore) GetArtifactRuntimeRunByRef(_ context.Context, taskRefHash string, agentUID int64) (*store.ArtifactRuntimeRun, bool, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	run := s.runsByTask[s.taskByRef[taskRefHash]]
	if run == nil || run.AgentUID != agentUID {
		return nil, false, nil
	}
	return cloneRuntime02Run(run), true, nil
}

func (s *artifactRuntime02MemoryStore) GetArtifactRuntimeRun(_ context.Context, runID string, actorUID, agentUID int64, artifactID string) (*store.ArtifactRuntimeRun, bool, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	run := s.runsByTask[s.taskByRun[runID]]
	if run == nil || run.ActorUID != actorUID || run.AgentUID != agentUID || run.ArtifactID != artifactID {
		return nil, false, nil
	}
	return cloneRuntime02Run(run), true, nil
}

func (s *artifactRuntime02MemoryStore) ListArtifactRuntimeRuns(_ context.Context, actorUID, agentUID int64, artifactID string, limit int) ([]*store.ArtifactRuntimeRun, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	var result []*store.ArtifactRuntimeRun
	for _, run := range s.runsByTask {
		if run.ActorUID == actorUID && run.AgentUID == agentUID && run.ArtifactID == artifactID {
			result = append(result, cloneRuntime02Run(run))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *artifactRuntime02MemoryStore) ReserveArtifactRuntimeDelivery(_ context.Context, taskRefHash string, actorUID int64, topicID string, agentUID int64, clientMessageID string, claimLease time.Duration) (*store.ArtifactRuntimeDeliveryClaim, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	run := s.runsByTask[s.taskByRef[taskRefHash]]
	if run == nil || run.ActorUID != actorUID || run.TopicID != topicID || run.AgentUID != agentUID ||
		runtimeRunTerminalStatus(run.Status) || !s.now.Before(run.ExpiresAt) {
		return nil, store.ErrArtifactRuntimeRunConflict
	}
	if run.Delivered {
		if run.DeliveryClientID != clientMessageID {
			return nil, store.ErrArtifactRuntimeRunConflict
		}
		return &store.ArtifactRuntimeDeliveryClaim{Run: cloneRuntime02Run(run), AlreadyDelivered: true}, nil
	}
	if run.DeliveryClaimed {
		if run.DeliveryClientID != clientMessageID {
			return nil, store.ErrArtifactRuntimeRunConflict
		}
		if run.DeliveryClaimedAt != nil && s.now.Before(run.DeliveryClaimedAt.Add(claimLease)) {
			return nil, store.ErrArtifactRuntimeDeliveryPending
		}
	}
	recovered := run.DeliveryClaimed
	claimedAt := s.now
	run.DeliveryClaimed, run.DeliveryClientID, run.DeliveryClaimedAt = true, clientMessageID, &claimedAt
	return &store.ArtifactRuntimeDeliveryClaim{Run: cloneRuntime02Run(run), Recovered: recovered}, nil
}

func (s *artifactRuntime02MemoryStore) ConfirmArtifactRuntimeDelivery(_ context.Context, taskID, taskRefHash, clientMessageID string) (bool, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	run := s.runsByTask[taskID]
	if run == nil || run.TaskRefHash != taskRefHash || !run.DeliveryClaimed || run.DeliveryClientID != clientMessageID ||
		runtimeRunTerminalStatus(run.Status) || !s.now.Before(run.ExpiresAt) {
		return false, nil
	}
	run.DeliveryClaimed, run.DeliveryClaimedAt, run.Delivered = false, nil, true
	return true, nil
}

func (s *artifactRuntime02MemoryStore) ReleaseArtifactRuntimeDelivery(_ context.Context, taskID, taskRefHash, clientMessageID string) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	run := s.runsByTask[taskID]
	if run != nil && run.TaskRefHash == taskRefHash && run.DeliveryClientID == clientMessageID && !run.Delivered {
		run.DeliveryClaimed, run.DeliveryClientID, run.DeliveryClaimedAt = false, "", nil
	}
	return nil
}

func (s *artifactRuntime02MemoryStore) appendRunEvent(run *store.ArtifactRuntimeRun, eventType string, data map[string]interface{}) *store.ArtifactRuntimeEvent {
	s.artifactRuntimeMemoryStore.mu.Lock()
	defer s.artifactRuntimeMemoryStore.mu.Unlock()
	s.nextID++
	encoded, _ := json.Marshal(data)
	event := &store.ArtifactRuntimeEvent{
		EventID: s.nextID, EventType: eventType, AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
		Namespace: "runtime", Key: run.RunID, Revision: 1, UpdatedByUID: run.AgentUID,
		UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
		ExecutorRunID: run.ExecutorRunID, ResultID: run.ResultID, Data: encoded,
		CreatedAt: s.now.Add(time.Duration(s.nextID) * time.Second),
	}
	s.events = append(s.events, event)
	return event
}

func (s *artifactRuntime02MemoryStore) FailArtifactRuntimeRun(_ context.Context, taskID string, actorUID int64, code, message string) (*store.ArtifactRuntimeRun, *store.ArtifactRuntimeEvent, error) {
	s.runMu.Lock()
	run := s.runsByTask[taskID]
	if run == nil || run.ActorUID != actorUID {
		s.runMu.Unlock()
		return nil, nil, store.ErrArtifactRuntimeRunNotFound
	}
	if run.Status == "completed" {
		s.runMu.Unlock()
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	if run.Status == "failed" {
		clone := cloneRuntime02Run(run)
		s.runMu.Unlock()
		return clone, nil, nil
	}
	run.Status, run.Code, run.Message = "failed", code, message
	finished := s.now
	run.FinishedAt, run.UpdatedAt = &finished, finished
	clone := cloneRuntime02Run(run)
	s.runMu.Unlock()
	event := s.appendRunEvent(clone, "run.error", map[string]interface{}{"status": "failed", "code": code})
	return clone, event, nil
}

func (s *artifactRuntime02MemoryStore) ObserveArtifactRuntimeExecutor(_ context.Context, taskRefHash string, agentUID int64, topicID, executorRunID, executorState, executorError string) (*store.ArtifactRuntimeRun, *store.ArtifactRuntimeEvent, error) {
	s.runMu.Lock()
	run := s.runsByTask[s.taskByRef[taskRefHash]]
	if run == nil || run.AgentUID != agentUID || run.TopicID != topicID || !run.Delivered ||
		(run.ExecutorRunID != "" && run.ExecutorRunID != executorRunID) {
		s.runMu.Unlock()
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	if runtimeRunTerminalStatus(run.Status) {
		if !runtime02ExecutorStateTerminal(run.ExecutorState) && run.ExecutorState != executorState &&
			!(run.ExecutorState == "running" && executorState == "waiting") {
			run.ExecutorState = executorState
		}
		run.ExecutorRunID = executorRunID
		if runtime02ExecutorStateTerminal(run.ExecutorState) && run.ExecutorFinishedAt == nil {
			finished := s.now
			run.ExecutorFinishedAt = &finished
		}
		clone := cloneRuntime02Run(run)
		s.runMu.Unlock()
		return clone, nil, nil
	}
	if runtime02ExecutorStateTerminal(run.ExecutorState) || run.ExecutorState == executorState ||
		(run.ExecutorState == "running" && executorState == "waiting") {
		clone := cloneRuntime02Run(run)
		s.runMu.Unlock()
		return clone, nil, nil
	}
	run.ExecutorRunID, run.ExecutorState = executorRunID, executorState
	var eventType string
	if (executorState == "running" || executorState == "waiting") && run.Status == "submitted" {
		run.Status = "running"
		started := s.now
		run.StartedAt = &started
		eventType = "run.started"
	} else if executorState == "completed" {
		finished := s.now
		run.ExecutorFinishedAt = &finished
		if run.Status == "submitted" {
			run.Status = "running"
			run.StartedAt = &finished
			eventType = "run.started"
		}
	} else if executorState == "failed" || executorState == "cancelled" || executorState == "stale" {
		run.Status, run.Code, run.Message = "failed", "agent_"+executorState, executorError
		finished := s.now
		run.ExecutorFinishedAt, run.FinishedAt = &finished, &finished
		eventType = "run.error"
	}
	clone := cloneRuntime02Run(run)
	s.runMu.Unlock()
	if eventType == "" {
		return clone, nil, nil
	}
	return clone, s.appendRunEvent(clone, eventType, map[string]interface{}{"status": clone.Status}), nil
}

func runtime02ExecutorStateTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "stale"
}

func (s *artifactRuntime02MemoryStore) PutArtifactRuntimeStateForRun(ctx context.Context, candidate *store.ArtifactRuntimeState, baseRevision int64, taskRefHash string) (*store.ArtifactRuntimeState, *store.ArtifactRuntimeEvent, error) {
	s.runMu.Lock()
	run := cloneRuntime02Run(s.runsByTask[s.taskByRef[taskRefHash]])
	s.runMu.Unlock()
	if run == nil || run.AgentUID != candidate.AgentUID || !run.Delivered || !s.now.Before(run.ExpiresAt) || runtimeRunTerminalStatus(run.Status) {
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	state, event, err := s.artifactRuntimeMemoryStore.PutArtifactRuntimeState(ctx, candidate, baseRevision)
	if err != nil {
		return nil, nil, err
	}
	s.artifactRuntimeMemoryStore.mu.Lock()
	event.TaskID, event.RunID, event.ExecutorRunID = run.TaskID, run.RunID, run.ExecutorRunID
	event.Data, _ = json.Marshal(map[string]interface{}{
		"namespace": event.Namespace, "key": event.Key, "revision": event.Revision,
	})
	last := s.events[len(s.events)-1]
	last.TaskID, last.RunID, last.ExecutorRunID, last.Data = event.TaskID, event.RunID, event.ExecutorRunID, event.Data
	s.artifactRuntimeMemoryStore.mu.Unlock()
	return state, event, nil
}

func (s *artifactRuntime02MemoryStore) CompleteArtifactRuntimeRun(_ context.Context, taskRefHash string, agentUID int64, resultID string, appliedEventIDs []int64) (*store.ArtifactRuntimeRun, []*store.ArtifactRuntimeEvent, error) {
	s.runMu.Lock()
	run := s.runsByTask[s.taskByRef[taskRefHash]]
	if run == nil || run.AgentUID != agentUID || !run.Delivered || !s.now.Before(run.ExpiresAt) {
		s.runMu.Unlock()
		return nil, nil, store.ErrArtifactRuntimeRunNotFound
	}
	if run.Status == "completed" {
		clone := cloneRuntime02Run(run)
		s.runMu.Unlock()
		if run.ResultID == resultID {
			return clone, nil, nil
		}
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	s.artifactRuntimeMemoryStore.mu.Lock()
	valid := len(appliedEventIDs) > 0
	for _, id := range appliedEventIDs {
		matched := false
		for _, event := range s.events {
			matched = matched || (event.EventID == id && event.EventType == "state.updated" && event.RunID == run.RunID)
		}
		valid = valid && matched
	}
	s.artifactRuntimeMemoryStore.mu.Unlock()
	if !valid {
		s.runMu.Unlock()
		return nil, nil, store.ErrArtifactRuntimeEvidenceInvalid
	}
	run.Status, run.ResultID, run.AppliedEventIDs = "completed", resultID, append([]int64(nil), appliedEventIDs...)
	finished := s.now
	run.FinishedAt, run.UpdatedAt = &finished, finished
	clone := cloneRuntime02Run(run)
	s.runMu.Unlock()
	resultEvent := s.appendRunEvent(clone, "result.applied", map[string]interface{}{"applied_event_ids": appliedEventIDs})
	finishedEvent := s.appendRunEvent(clone, "run.finished", map[string]interface{}{"status": "completed"})
	return clone, []*store.ArtifactRuntimeEvent{resultEvent, finishedEvent}, nil
}

func (s *artifactRuntime02MemoryStore) ConvergeArtifactRuntimeRuns(_ context.Context, _ time.Time, _ time.Duration, _ int) (int, error) {
	return 0, nil
}

func TestArtifactRuntime02DeliveryLeaseRecoversInterruptedReservation(t *testing.T) {
	now := time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC)
	base := &artifactRuntimeMemoryStore{
		states: make(map[string]*store.ArtifactRuntimeState),
		now:    now,
	}
	db := &artifactRuntime02MemoryStore{
		artifactRuntimeMemoryStore: base,
		runsByTask:                 make(map[string]*store.ArtifactRuntimeRun),
		taskByRef:                  make(map[string]string),
		taskByRun:                  make(map[string]string),
	}
	ref, err := newArtifactTaskOpaque("atr_")
	if err != nil {
		t.Fatalf("create task ref: %v", err)
	}
	_, err = db.CreateArtifactRuntimeRun(context.Background(), &store.ArtifactRuntimeRun{
		TaskID: "task-1", TaskRefHash: artifactTaskRefHash(ref), RunID: "run-1",
		ActorUID: 7, TopicID: "p2p_7_440", AgentUID: 440,
		Status: "submitted", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, store.ArtifactRuntimeRunCreatePolicy{
		ActorActiveMax: 32, ActorRateMax: 60, ActorRateWindow: time.Minute,
		MaxEntries: 4096, Retention: 30 * 24 * time.Hour, CleanupLimit: 512,
	})
	if err != nil {
		t.Fatalf("create persistent Run: %v", err)
	}
	tasks := newArtifactTaskStore(0, 0, 0, 0).withRuntimeStore(db)
	clientMessageID := "artifact-task:task-1"
	reserved, err := tasks.reserveDelivery(ref, 7, "p2p_7_440", 440, clientMessageID)
	if err != nil || reserved == nil || reserved.AlreadyDelivered {
		t.Fatalf("first reservation: delivery=%#v err=%v", reserved, err)
	}
	if _, err := tasks.reserveDelivery(ref, 7, "p2p_7_440", 440, clientMessageID); !errors.Is(err, errArtifactTaskDeliveryPending) {
		t.Fatalf("live lease retry error=%v, want pending", err)
	}

	base.now = now.Add(artifactRuntimeDeliveryClaimLease + time.Second)
	recovered, err := tasks.reserveDelivery(ref, 7, "p2p_7_440", 440, clientMessageID)
	if err != nil || recovered == nil || recovered.AlreadyDelivered {
		t.Fatalf("expired lease recovery: delivery=%#v err=%v", recovered, err)
	}
	if !recovered.Recovered {
		t.Fatal("expired lease recovery did not preserve the recovered claim marker")
	}
	if !tasks.confirmDelivery(recovered) {
		t.Fatal("recovered reservation was not confirmed")
	}
	duplicate, err := tasks.reserveDelivery(ref, 7, "p2p_7_440", 440, clientMessageID)
	if err != nil || duplicate == nil || !duplicate.AlreadyDelivered {
		t.Fatalf("confirmed retry was not deduplicated: delivery=%#v err=%v", duplicate, err)
	}
}

type artifactRuntime02DeliveryFixture struct {
	db              *artifactRuntime02MemoryStore
	hub             *Hub
	bot             *Client
	ref             string
	taskID          string
	runID           string
	topicID         string
	clientMessageID string
}

func newArtifactRuntime02DeliveryFixture(t *testing.T, topicID string) *artifactRuntime02DeliveryFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	identity := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			440: {ID: 440, Username: "artifact-agent", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 7},
		friendPairs: map[string]bool{agentPairKey(7, 440): true},
	}
	if isGroupTopic(topicID) {
		identity.groupMembers = []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 440, IsBot: true},
		}
	}
	base := &artifactRuntimeMemoryStore{
		identityMessageStore: identity,
		states:               make(map[string]*store.ArtifactRuntimeState),
		now:                  now,
	}
	db := &artifactRuntime02MemoryStore{
		artifactRuntimeMemoryStore: base,
		runsByTask:                 make(map[string]*store.ArtifactRuntimeRun),
		taskByRef:                  make(map[string]string),
		taskByRun:                  make(map[string]string),
		messageIDs:                 make(map[string]int64),
	}
	ref, err := newArtifactTaskOpaque("atr_")
	if err != nil {
		t.Fatalf("create task ref: %v", err)
	}
	taskID, err := newArtifactTaskOpaque("atk_")
	if err != nil {
		t.Fatalf("create task id: %v", err)
	}
	runID, err := newArtifactTaskOpaque("run_")
	if err != nil {
		t.Fatalf("create run id: %v", err)
	}
	if _, err := db.CreateArtifactRuntimeRun(context.Background(), &store.ArtifactRuntimeRun{
		TaskID: taskID, TaskRefHash: artifactTaskRefHash(ref), RunID: runID,
		ActorUID: 7, TopicID: topicID, AgentUID: 440,
		Status: "submitted", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, store.ArtifactRuntimeRunCreatePolicy{}); err != nil {
		t.Fatalf("create persistent delivery Run: %v", err)
	}
	hub := NewHub(db, nil)
	bot := &Client{uid: 440, accountType: types.AccountBot, send: make(chan []byte, 8)}
	hub.addClient(bot)
	return &artifactRuntime02DeliveryFixture{
		db: db, hub: hub, bot: bot, ref: ref, taskID: taskID, runID: runID,
		topicID: topicID, clientMessageID: "artifact-task:" + taskID,
	}
}

func (f *artifactRuntime02DeliveryFixture) reserve(t *testing.T) *artifactTaskDeliveryRef {
	t.Helper()
	delivery, err := f.hub.artifactTasks.reserveDelivery(
		f.ref, 7, f.topicID, 440, f.clientMessageID,
	)
	if err != nil || delivery == nil {
		t.Fatalf("reserve delivery: delivery=%#v err=%v", delivery, err)
	}
	return delivery
}

func (f *artifactRuntime02DeliveryFixture) payload(delivery *artifactTaskDeliveryRef) *normalizedMessagePayload {
	return &normalizedMessagePayload{
		StoredContent:   `"来自「项目看板」：生成推进建议"`,
		DisplayContent:  "来自「项目看板」：生成推进建议",
		StoredType:      "text",
		DisplayType:     "text",
		ClientMsgID:     f.clientMessageID,
		Metadata:        map[string]interface{}{"trace": "runtime-02-recovery"},
		ArtifactTaskRef: delivery,
	}
}

func (f *artifactRuntime02DeliveryFixture) sendHTTP(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"topic_id": f.topicID, "type": "text", "content": "来自「项目看板」：生成推进建议",
		"client_msg_id": f.clientMessageID,
		"metadata":      map[string]interface{}{artifactTaskRefMetadataKey: f.ref, "trace": "runtime-02-recovery"},
	})
	if err != nil {
		t.Fatalf("encode recovered HTTP delivery: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
	response := httptest.NewRecorder()
	NewMessageHandler(f.db, f.hub).HandleSendMessage(response, request)
	return response
}

func drainArtifactTaskDeliveries(t *testing.T, messages <-chan []byte) []ServerMessage {
	t.Helper()
	var delivered []ServerMessage
	for {
		select {
		case raw := <-messages:
			var message ServerMessage
			if err := json.Unmarshal(raw, &message); err != nil {
				t.Fatalf("decode Artifact task delivery: %v", err)
			}
			if message.Data != nil {
				delivered = append(delivered, message)
			}
		default:
			return delivered
		}
	}
}

func assertRuntime02DeliveryCommitted(t *testing.T, fixture *artifactRuntime02DeliveryFixture) {
	t.Helper()
	run, found, err := fixture.db.GetArtifactRuntimeRun(context.Background(), fixture.runID, 7, 440, "")
	if err != nil || !found || !run.Delivered || run.DeliveryClaimed {
		t.Fatalf("recovered delivery Run=%#v found=%v err=%v", run, found, err)
	}
}

func TestArtifactRuntime02HTTPDeliveryRecoversEveryClaimCrashWindow(t *testing.T) {
	for _, crashWindow := range []string{"after_reserve", "after_save", "after_fanout"} {
		crashWindow := crashWindow
		t.Run(crashWindow, func(t *testing.T) {
			fixture := newArtifactRuntime02DeliveryFixture(t, "p2p_7_440")
			delivery := fixture.reserve(t)
			if crashWindow != "after_reserve" {
				result, err := saveNormalizedMessage(fixture.db, fixture.topicID, 7, 0, fixture.payload(delivery))
				if err != nil || result.Duplicate {
					t.Fatalf("save before simulated crash: result=%#v err=%v", result, err)
				}
				if crashWindow == "after_fanout" {
					message := fixture.hub.messageForRecipient(7, 440, fixture.topicID, 0, fixture.payload(delivery), result.ID)
					if fixture.hub.sendToUserExceptConfirmed(440, message, nil) != 1 {
						t.Fatal("simulated pre-Confirm fanout did not reach Agent queue")
					}
				}
			}

			fixture.db.now = fixture.db.now.Add(artifactRuntimeDeliveryClaimLease + time.Second)
			response := fixture.sendHTTP(t)
			if response.Code != http.StatusOK {
				t.Fatalf("recovered HTTP status=%d body=%s", response.Code, response.Body.String())
			}
			deliveries := drainArtifactTaskDeliveries(t, fixture.bot.send)
			wantDeliveries := 1
			if crashWindow == "after_fanout" {
				wantDeliveries = 2
			}
			if len(deliveries) != wantDeliveries {
				t.Fatalf("Agent deliveries=%d want=%d", len(deliveries), wantDeliveries)
			}
			executed := make(map[string]bool)
			for _, message := range deliveries {
				ref, _ := message.Data.Metadata[artifactTaskRefMetadataKey].(string)
				if ref != fixture.ref || message.Data.SeqID <= 0 {
					t.Fatalf("recovered Agent message=%#v", message.Data)
				}
				executed[ref] = true
			}
			if len(executed) != 1 {
				t.Fatalf("stable task identity produced %d executions", len(executed))
			}
			assertRuntime02DeliveryCommitted(t, fixture)
		})
	}
}

func TestArtifactRuntime02RecoveredDuplicateUsesSharedWebSocketDelivery(t *testing.T) {
	for _, topicID := range []string{"p2p_7_440", "grp_80"} {
		topicID := topicID
		t.Run(topicID, func(t *testing.T) {
			fixture := newArtifactRuntime02DeliveryFixture(t, topicID)
			delivery := fixture.reserve(t)
			result, err := saveNormalizedMessage(fixture.db, topicID, 7, 0, fixture.payload(delivery))
			if err != nil || result.Duplicate {
				t.Fatalf("save before simulated crash: result=%#v err=%v", result, err)
			}
			fixture.db.now = fixture.db.now.Add(artifactRuntimeDeliveryClaimLease + time.Second)
			sender := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
			fixture.hub.addClient(sender)
			fixture.hub.handlePub(sender, &MsgClientPub{
				ID: "recovered-pub", Topic: topicID, ClientMsgID: fixture.clientMessageID,
				Type: "text", Content: json.RawMessage(`"来自「项目看板」：生成推进建议"`),
				Metadata: map[string]interface{}{artifactTaskRefMetadataKey: fixture.ref, "trace": "runtime-02-recovery"},
			})
			var acknowledgement ServerMessage
			decodeQueuedServerMessage(t, sender.send, &acknowledgement)
			if acknowledgement.Ctrl == nil || acknowledgement.Ctrl.Code != http.StatusOK {
				t.Fatalf("recovered WebSocket acknowledgement=%#v", acknowledgement.Ctrl)
			}
			deliveries := drainArtifactTaskDeliveries(t, fixture.bot.send)
			if len(deliveries) != 1 || deliveries[0].Data.Metadata[artifactTaskRefMetadataKey] != fixture.ref {
				t.Fatalf("recovered WebSocket deliveries=%#v", deliveries)
			}
			assertRuntime02DeliveryCommitted(t, fixture)
		})
	}
}

func TestArtifactRuntime02PersistentRunSurvivesPreviewAndHubRestart(t *testing.T) {
	identity := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		owners: map[int64]int64{440: 7},
	}
	base := &artifactRuntimeMemoryStore{
		identityMessageStore: identity, states: make(map[string]*store.ArtifactRuntimeState),
		now: time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC),
	}
	db := &artifactRuntime02MemoryStore{
		artifactRuntimeMemoryStore: base, runsByTask: make(map[string]*store.ArtifactRuntimeRun),
		taskByRef: make(map[string]string), taskByRun: make(map[string]string),
	}
	record := ArtifactContextRecord{
		ID: "project-board", Title: "项目看板", Kind: "mini_app",
		URL: "https://agent-440.artifacts.catsco.fun:19991/artifacts/project-board/latest/", PublishVersion: 8,
	}
	configure := func(hub *Hub) {
		hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
			if agentUID != 440 || artifactID != record.ID {
				return ArtifactContextRecord{}, errors.New("unexpected Artifact")
			}
			return record, nil
		}))
		hub.SetArtifactRuntimeManifestResolver(artifactRuntimeManifestResolverFunc(func(_ context.Context, _ ArtifactContextRecord, _ int64) (ArtifactRuntimeManifest, error) {
			return ArtifactRuntimeManifest{
				Version:  artifactRuntimeVersion02,
				Surfaces: []ArtifactRuntimeSurface{{ID: "task-board"}},
				State:    []ArtifactRuntimeStateDeclaration{{Namespace: "project_tasks", Mode: "read-write"}},
			}, nil
		}))
		hub.SetArtifactTaskIntentResolver(artifactTaskIntentResolverFunc(func(_ context.Context, _ ArtifactContextRecord, _ int64, _ string) (ArtifactTaskIntent, error) {
			return ArtifactTaskIntent{
				ID: "tasks.plan.v1", Title: "生成推进建议", Description: "修改任务数据。",
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Completion:  &ArtifactTaskCompletion{Mode: "runtime_state"},
			}, nil
		}))
	}
	hub := NewHub(db, nil)
	configure(hub)
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	hub.ensureClientRuntimeRoute(human)
	hub.addClient(human)
	previewSession, err := hub.artifactPreviewSessions.issue(7, hub.clientRoute(human))
	if err != nil {
		t.Fatalf("issue preview session: %v", err)
	}
	createBody, _ := json.Marshal(map[string]interface{}{
		"topic_id": "p2p_7_440",
		"artifact_ref": map[string]interface{}{
			"contract_version": artifactRefContract, "id": record.ID,
			"displayed_version": 8, "currently_visible": true,
		},
		"intent_id": "tasks.plan.v1", "payload": map[string]interface{}{"scope": "week"},
		"preview_session": previewSession,
	})
	createRequest := httptest.NewRequest(http.MethodPost, "/api/artifact-tasks", strings.NewReader(string(createBody)))
	createRequest = createRequest.WithContext(context.WithValue(createRequest.Context(), uidKey, int64(7)))
	createResponse := httptest.NewRecorder()
	NewArtifactTaskHandler(hub).HandleUserTasks(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create Runtime 0.2 task status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var created struct {
		TaskID         string `json:"task_id"`
		TaskRef        string `json:"task_ref"`
		RunID          string `json:"run_id"`
		CompletionMode string `json:"completion_mode"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil ||
		!artifactTaskIDPattern.MatchString(created.TaskID) || !artifactTaskRefPattern.MatchString(created.TaskRef) ||
		!artifactRuntimeRunIDPattern.MatchString(created.RunID) || created.CompletionMode != "runtime_state" {
		t.Fatalf("created Runtime 0.2 task=%#v err=%v", created, err)
	}
	db.runMu.Lock()
	createPolicy := db.lastPolicy
	db.runMu.Unlock()
	if createPolicy.ActorActiveMax != artifactTaskActorActiveMax ||
		createPolicy.ActorRateMax != artifactTaskActorRateMax ||
		createPolicy.ActorRateWindow != artifactTaskActorRateWindow ||
		createPolicy.MaxEntries != artifactTaskStoreMaxEntries ||
		createPolicy.Retention != artifactRuntimeRunRetention ||
		createPolicy.CleanupLimit != artifactRuntimeRunCleanupBatch {
		t.Fatalf("persistent Runtime admission policy=%#v", createPolicy)
	}
	delivery, err := hub.artifactTasks.reserveDelivery(created.TaskRef, 7, "p2p_7_440", 440, "artifact-task:"+created.TaskID)
	if err != nil || !hub.artifactTasks.confirmDelivery(delivery) {
		t.Fatalf("deliver Runtime 0.2 task delivery=%#v err=%v", delivery, err)
	}
	if !hub.artifactTasks.observeRun(created.TaskRef, 440, "p2p_7_440", &types.ConversationTaskStatus{
		RunID: "xiaoba-runtime-02", State: "running",
	}) {
		t.Fatal("XiaoBa running status was not attached")
	}
	if !hub.artifactTasks.observeRun(created.TaskRef, 440, "p2p_7_440", &types.ConversationTaskStatus{
		RunID: "xiaoba-runtime-02", State: "completed",
	}) || !hub.artifactTasks.observeRun(created.TaskRef, 440, "p2p_7_440", &types.ConversationTaskStatus{
		RunID: "xiaoba-runtime-02", State: "waiting",
	}) {
		t.Fatal("XiaoBa terminal executor status was not accepted monotonically")
	}
	executorFinished, found, err := db.GetArtifactRuntimeRun(context.Background(), created.RunID, 7, 440, record.ID)
	if err != nil || !found || executorFinished.Status != "running" || executorFinished.ExecutorState != "completed" ||
		executorFinished.ExecutorFinishedAt == nil {
		t.Fatalf("late executor status regressed completed executor: run=%#v found=%v err=%v", executorFinished, found, err)
	}

	// Runtime 0.2 remains writable after the Viewer leaves.
	hub.removeClient(human)
	runtimeHandler := NewArtifactRuntimeHandler(hub, db)
	observeRequest := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-runtime?task_ref="+created.TaskRef, nil)
	observeRequest = observeRequest.WithContext(context.WithValue(observeRequest.Context(), uidKey, int64(440)))
	observeResponse := httptest.NewRecorder()
	runtimeHandler.HandleBot(observeResponse, observeRequest)
	if observeResponse.Code != http.StatusOK || !strings.Contains(observeResponse.Body.String(), `"currently_visible":false`) {
		t.Fatalf("disconnected persistent Runtime observation status=%d body=%s", observeResponse.Code, observeResponse.Body.String())
	}
	put := `{"contract_version":"catsco.artifact-runtime-request.v1","operation":"state.put","task_ref":"` + created.TaskRef + `","namespace":"project_tasks","key":"main","base_revision":0,"value":{"items":[{"id":"t1","status":"doing"}]}}`
	putRequest := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(put))
	putRequest = putRequest.WithContext(context.WithValue(putRequest.Context(), uidKey, int64(440)))
	putResponse := httptest.NewRecorder()
	runtimeHandler.HandleBot(putResponse, putRequest)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("persistent State write status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}
	var putResult struct {
		Event struct {
			EventID int64  `json:"event_id"`
			RunID   string `json:"run_id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(putResponse.Body.Bytes(), &putResult); err != nil || putResult.Event.EventID <= 0 || putResult.Event.RunID != created.RunID {
		t.Fatalf("persistent State event=%#v err=%v body=%s", putResult, err, putResponse.Body.String())
	}
	patch := `{"contract_version":"catsco.artifact-runtime-request.v1","operation":"state.patch","task_ref":"` + created.TaskRef + `","namespace":"project_tasks","key":"main","base_revision":1,"patch":[{"op":"replace","path":"/items/0/status","value":"done"}]}`
	patchRequest := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(patch))
	patchRequest = patchRequest.WithContext(context.WithValue(patchRequest.Context(), uidKey, int64(440)))
	patchResponse := httptest.NewRecorder()
	runtimeHandler.HandleBot(patchResponse, patchRequest)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("persistent State patch status=%d body=%s", patchResponse.Code, patchResponse.Body.String())
	}
	var patchResult struct {
		Event struct {
			EventID int64 `json:"event_id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(patchResponse.Body.Bytes(), &patchResult); err != nil || patchResult.Event.EventID <= putResult.Event.EventID {
		t.Fatalf("persistent State patch event=%#v err=%v body=%s", patchResult, err, patchResponse.Body.String())
	}
	resultID := "arr_" + strings.Repeat("r", 43)
	complete := fmt.Sprintf(
		`{"contract_version":"catsco.artifact-runtime-request.v1","operation":"run.complete","task_ref":"%s","result_id":"%s","applied_event_ids":[%d,%d]}`,
		created.TaskRef, resultID, patchResult.Event.EventID, putResult.Event.EventID,
	)
	completeRequest := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(complete))
	completeRequest = completeRequest.WithContext(context.WithValue(completeRequest.Context(), uidKey, int64(440)))
	completeResponse := httptest.NewRecorder()
	runtimeHandler.HandleBot(completeResponse, completeRequest)
	if completeResponse.Code != http.StatusOK || !strings.Contains(completeResponse.Body.String(), `"status":"completed"`) {
		t.Fatalf("complete Runtime Run status=%d body=%s", completeResponse.Code, completeResponse.Body.String())
	}
	duplicateComplete := fmt.Sprintf(
		`{"contract_version":"catsco.artifact-runtime-request.v1","operation":"run.complete","task_ref":"%s","result_id":"%s","applied_event_ids":[%d,%d]}`,
		created.TaskRef, resultID, putResult.Event.EventID, patchResult.Event.EventID,
	)
	duplicateCompleteRequest := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(duplicateComplete))
	duplicateCompleteRequest = duplicateCompleteRequest.WithContext(context.WithValue(duplicateCompleteRequest.Context(), uidKey, int64(440)))
	duplicateCompleteResponse := httptest.NewRecorder()
	runtimeHandler.HandleBot(duplicateCompleteResponse, duplicateCompleteRequest)
	if duplicateCompleteResponse.Code != http.StatusOK ||
		!strings.Contains(duplicateCompleteResponse.Body.String(), `"events":[]`) {
		t.Fatalf("idempotent Runtime completion status=%d body=%s", duplicateCompleteResponse.Code, duplicateCompleteResponse.Body.String())
	}
	db.mu.Lock()
	db.runsByTask[created.TaskID].ExpiresAt = time.Now().UTC().Add(-time.Second)
	db.mu.Unlock()
	if _, ok := hub.artifactTasks.forBot(created.TaskRef, 440); ok {
		t.Fatal("expired persistent Task Ref remained readable after terminal completion")
	}
	db.mu.Lock()
	db.runsByTask[created.TaskID].ExpiresAt = time.Now().UTC().Add(time.Hour)
	db.mu.Unlock()
	if !hub.artifactTasks.observeRun(created.TaskRef, 440, "p2p_7_440", &types.ConversationTaskStatus{
		RunID: "xiaoba-runtime-02", State: "waiting",
	}) {
		t.Fatal("late executor status should be accepted as a terminal no-op")
	}
	terminalRun, found, err := db.GetArtifactRuntimeRun(context.Background(), created.RunID, 7, 440, record.ID)
	if err != nil || !found || terminalRun.Status != "completed" || terminalRun.ExecutorState != "completed" {
		t.Fatalf("late executor status regressed terminal Run: run=%#v found=%v err=%v", terminalRun, found, err)
	}

	// A fresh Hub has an empty V4.1 memory store but recovers the durable Run.
	restartedHub := NewHub(db, nil)
	configure(restartedHub)
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/artifact-tasks?task_id="+created.TaskID, nil)
	statusRequest = statusRequest.WithContext(context.WithValue(statusRequest.Context(), uidKey, int64(7)))
	statusResponse := httptest.NewRecorder()
	NewArtifactTaskHandler(restartedHub).HandleUserTasks(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"status":"completed"`) ||
		!strings.Contains(statusResponse.Body.String(), created.RunID) || !strings.Contains(statusResponse.Body.String(), "xiaoba-runtime-02") {
		t.Fatalf("recovered Runtime Run status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}

	reopened := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	restartedHub.ensureClientRuntimeRoute(reopened)
	restartedHub.addClient(reopened)
	reopenedSession, err := restartedHub.artifactPreviewSessions.issue(7, restartedHub.clientRoute(reopened))
	if err != nil {
		t.Fatalf("issue reopened preview session: %v", err)
	}
	viewerRequest := func(operation string, extra map[string]interface{}) *httptest.ResponseRecorder {
		body := map[string]interface{}{
			"contract_version": artifactRuntimeRequestContract,
			"operation":        operation,
			"topic_id":         "p2p_7_440",
			"artifact_ref": map[string]interface{}{
				"contract_version": artifactRefContract, "id": record.ID,
				"displayed_version": 8, "currently_visible": true,
			},
			"preview_session": reopenedSession,
		}
		for key, value := range extra {
			body[key] = value
		}
		encoded, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPost, "/api/artifact-runtime", strings.NewReader(string(encoded)))
		request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
		response := httptest.NewRecorder()
		NewArtifactRuntimeHandler(restartedHub, db).HandleUser(response, request)
		return response
	}
	connectResponse := viewerRequest("connect", nil)
	if connectResponse.Code != http.StatusOK || !strings.Contains(connectResponse.Body.String(), created.RunID) ||
		!strings.Contains(connectResponse.Body.String(), `"status":"completed"`) {
		t.Fatalf("reopened Runtime snapshot status=%d body=%s", connectResponse.Code, connectResponse.Body.String())
	}
	eventsResponse := viewerRequest("events.list", map[string]interface{}{"after_event_id": 0, "limit": 20})
	for _, expected := range []string{
		artifactRuntimeEventContractV2, "run.started", "state.updated", "result.applied", "run.finished",
	} {
		if eventsResponse.Code != http.StatusOK || !strings.Contains(eventsResponse.Body.String(), expected) {
			t.Fatalf("replayed Runtime events missing %q status=%d body=%s", expected, eventsResponse.Code, eventsResponse.Body.String())
		}
	}
}
