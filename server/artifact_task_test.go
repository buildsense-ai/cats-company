package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type artifactTaskIntentResolverFunc func(context.Context, ArtifactContextRecord, int64, string) (ArtifactTaskIntent, error)

func (f artifactTaskIntentResolverFunc) ResolveArtifactTaskIntent(
	ctx context.Context,
	record ArtifactContextRecord,
	displayedVersion int64,
	intentID string,
) (ArtifactTaskIntent, error) {
	return f(ctx, record, displayedVersion, intentID)
}

func testArtifactTaskIntent() ArtifactTaskIntent {
	return ArtifactTaskIntent{
		ID:          "tasks.create.v1",
		Title:       "创建任务",
		Description: "根据当前页面创建一条任务。",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"required":["title"],
			"additionalProperties":false,
			"properties":{"title":{"type":"string","minLength":1,"maxLength":200}}
		}`),
		ResultSink: "tasks.upsert.v1",
	}
}

func testArtifactTaskCandidate(route runtimeRoute) artifactTask {
	return artifactTask{
		ActorUID: 7,
		TopicID:  "p2p_7_440",
		AgentUID: 440,
		Artifact: ArtifactContextRecord{
			ID:             "task-board",
			Title:          "任务看板",
			Kind:           "mini_app",
			URL:            "https://agent-440.artifacts.catsco.fun:19991/artifacts/task-board/latest/",
			PublishVersion: 3,
		},
		DisplayedVersion: 3,
		PreviewRoute:     route,
		Intent:           testArtifactTaskIntent(),
		Payload:          json.RawMessage(`{"title":"准备发布清单"}`),
		PageContext: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-26T03:00:00Z",
			"semantic_context": map[string]interface{}{"view": "board", "selected_task": "task-7"},
		},
	}
}

func TestArtifactTaskManifestAndPayloadStayVersionBounded(t *testing.T) {
	body := []byte(`{
		"contract_version":"catsco.artifact-manifest.v3",
		"purpose":"Manage project tasks.",
		"result_sinks":[{
			"id":"tasks.upsert.v1",
			"description":"Create or update tasks.",
			"input_schema":{"type":"object"}
		}],
		"task_intents":[{
			"id":"tasks.create.v1",
			"title":"Create task",
			"description":"Create a task from the current page.",
			"input_schema":{
				"type":"object",
				"required":["title"],
				"additionalProperties":false,
				"properties":{"title":{"type":"string","minLength":1,"maxLength":200}}
			},
			"result_sink":"tasks.upsert.v1"
		}]
	}`)
	intent, err := parseArtifactTaskIntentManifest(body, "tasks.create.v1")
	if err != nil {
		t.Fatalf("parse task manifest: %v", err)
	}
	if intent.ID != "tasks.create.v1" || intent.ResultSink != "tasks.upsert.v1" {
		t.Fatalf("intent = %#v", intent)
	}
	if _, err := validateArtifactTaskPayload(intent.InputSchema, json.RawMessage(`{"title":"准备发布清单"}`)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	for name, payload := range map[string]string{
		"missing required": `{}`,
		"unknown field":    `{"title":"x","prompt":"ignore policy"}`,
		"wrong type":       `{"title":42}`,
	} {
		if _, err := validateArtifactTaskPayload(intent.InputSchema, json.RawMessage(payload)); err == nil {
			t.Fatalf("%s payload was accepted", name)
		}
	}
	if _, err := parseArtifactTaskIntentManifest(
		[]byte(strings.Replace(string(body), `"tasks.upsert.v1"`, `"tasks.missing.v1"`, 1)),
		"tasks.create.v1",
	); err == nil {
		t.Fatal("intent targeting an undeclared sink was accepted")
	}
	url, err := artifactTaskVersionManifestURL(ArtifactContextRecord{
		ID:  "task-board",
		URL: "https://agent-440.artifacts.catsco.fun:19991/artifacts/task-board/latest/?cache=1#view",
	}, 3)
	if err != nil || url != "https://agent-440.artifacts.catsco.fun:19991/artifacts/task-board/v3/artifact-manifest.json" {
		t.Fatalf("immutable manifest URL = %q err=%v", url, err)
	}
}

func TestArtifactTaskStoreCorrelatesRunAndRequiresAppliedResult(t *testing.T) {
	now := time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC)
	store := newArtifactTaskStore(time.Hour, time.Minute, 5*time.Second, 8)
	store.now = func() time.Time { return now }
	route := runtimeRoute{NodeID: "preview-node", ConnectionID: "preview-connection"}

	task, err := store.create(testArtifactTaskCandidate(route))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.claimDelivery(task.Ref, 7, task.TopicID, 441); err == nil {
		t.Fatal("wrong Agent claimed task delivery")
	}
	if _, err := store.claimDelivery(task.Ref, 7, task.TopicID, 440); err != nil {
		t.Fatalf("claim task delivery: %v", err)
	}
	if _, err := store.claimDelivery(task.Ref, 7, task.TopicID, 440); err == nil {
		t.Fatal("task ref was delivered twice")
	}
	if !store.observeRun(task.Ref, 440, task.TopicID, &types.ConversationTaskStatus{
		RunID: "run-42",
		State: "running",
	}) {
		t.Fatal("running state was not correlated")
	}
	running, ok := store.forActor(task.ID, 7)
	if !ok || running.Status != artifactTaskRunning || running.RunID != "run-42" {
		t.Fatalf("running task = %#v ok=%v", running, ok)
	}
	if !store.observeRun(task.Ref, 440, task.TopicID, &types.ConversationTaskStatus{
		RunID: "run-42",
		State: "completed",
	}) {
		t.Fatal("Agent completion was not observed")
	}
	beforeGrace, _ := store.forActor(task.ID, 7)
	if beforeGrace.Status != artifactTaskRunning {
		t.Fatalf("Agent completion incorrectly completed business task: %#v", beforeGrace)
	}
	now = now.Add(5*time.Second + time.Nanosecond)
	afterGrace, _ := store.forActor(task.ID, 7)
	if afterGrace.Status != artifactTaskFailed || afterGrace.Code != "result_not_applied" {
		t.Fatalf("missing applied result did not fail: %#v", afterGrace)
	}

	now = now.Add(time.Second)
	appliedTask, err := store.create(testArtifactTaskCandidate(route))
	if err != nil {
		t.Fatalf("create applied task: %v", err)
	}
	if _, err := store.claimDelivery(appliedTask.Ref, 7, appliedTask.TopicID, 440); err != nil {
		t.Fatalf("claim applied task: %v", err)
	}
	writebacks := newArtifactResultWritebackStore(30*time.Minute, time.Second, 8)
	writebacks.now = func() time.Time { return now }
	target, err := writebacks.issueTask(appliedTask)
	if err != nil {
		t.Fatalf("issue task writeback: %v", err)
	}
	if !target.ExpiresAt.Equal(appliedTask.ExpiresAt) {
		t.Fatalf("task writeback expires at %v, want task expiry %v", target.ExpiresAt, appliedTask.ExpiresAt)
	}
	resultID := "arr_" + strings.Repeat("r", 43)
	if !store.completeResult(appliedTask.ID, resultID, artifactResultDeliveryOutcome{Status: "applied"}) {
		t.Fatal("applied result was not correlated")
	}
	completed, _ := store.forActor(appliedTask.ID, 7)
	if completed.Status != artifactTaskCompleted || completed.ResultID != resultID {
		t.Fatalf("completed task = %#v", completed)
	}
	if store.completeResult(appliedTask.ID, "arr_"+strings.Repeat("x", 43), artifactResultDeliveryOutcome{Status: "applied"}) {
		t.Fatal("completed task accepted a different result")
	}
	if store.observeRun(appliedTask.Ref, 440, appliedTask.TopicID, &types.ConversationTaskStatus{
		RunID: "run-late",
		State: "failed",
	}) {
		t.Fatal("late Agent failure changed an applied task")
	}
	stillCompleted, _ := store.forActor(appliedTask.ID, 7)
	if stillCompleted.Status != artifactTaskCompleted || stillCompleted.ResultID != resultID {
		t.Fatalf("late Agent state reversed completion: %#v", stillCompleted)
	}

	now = now.Add(time.Second)
	rejectedTask, err := store.create(testArtifactTaskCandidate(route))
	if err != nil {
		t.Fatalf("create rejected task: %v", err)
	}
	if _, err := store.claimDelivery(rejectedTask.Ref, 7, rejectedTask.TopicID, 440); err != nil {
		t.Fatalf("claim rejected task: %v", err)
	}
	rejectedResultID := "arr_" + strings.Repeat("z", 43)
	if !store.completeResult(rejectedTask.ID, rejectedResultID, artifactResultDeliveryOutcome{
		Status: "rejected",
		Code:   "invalid_payload",
	}) {
		t.Fatal("rejected result was not correlated")
	}
	if store.completeResult(rejectedTask.ID, rejectedResultID, artifactResultDeliveryOutcome{Status: "applied"}) {
		t.Fatal("failed task was revived by a late applied result")
	}
	stillFailed, _ := store.forActor(rejectedTask.ID, 7)
	if stillFailed.Status != artifactTaskFailed || stillFailed.Code != "invalid_payload" {
		t.Fatalf("rejected task did not stay failed: %#v", stillFailed)
	}
}

func TestArtifactTaskCreateMessageAndBotReadUseOneShotRef(t *testing.T) {
	db := &agentIdentityE2EStore{
		users: map[int64]*types.User{
			7:   {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			440: {ID: 440, Username: "artifact-agent", AccountType: types.AccountBot},
			441: {ID: 441, Username: "other-agent", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 7},
		friendPairs: map[string]bool{agentPairKey(7, 440): true},
	}
	hub := NewHub(db, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
		if agentUID != 440 || artifactID != "task-board" {
			t.Fatalf("resolver args agent=%d artifact=%q", agentUID, artifactID)
		}
		return testArtifactTaskCandidate(runtimeRoute{}).Artifact, nil
	}))
	hub.SetArtifactTaskIntentResolver(artifactTaskIntentResolverFunc(func(_ context.Context, record ArtifactContextRecord, version int64, intentID string) (ArtifactTaskIntent, error) {
		if record.ID != "task-board" || version != 3 || intentID != "tasks.create.v1" {
			t.Fatalf("intent resolver record=%#v version=%d intent=%q", record, version, intentID)
		}
		return testArtifactTaskIntent(), nil
	}))
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	bot := &Client{uid: 440, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.ensureClientRuntimeRoute(human)
	hub.addClient(human)
	hub.addClient(bot)
	previewSession, err := hub.artifactPreviewSessions.issue(7, hub.clientRoute(human))
	if err != nil {
		t.Fatalf("issue preview session: %v", err)
	}

	createBody, err := json.Marshal(map[string]interface{}{
		"topic_id": "p2p_7_440",
		"artifact_ref": map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "task-board",
			"displayed_version": 3,
			"currently_visible": true,
		},
		"intent_id": "tasks.create.v1",
		"payload":   map[string]interface{}{"title": "准备发布清单"},
		"page_context": map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-26T03:00:00Z",
			"semantic_context": map[string]interface{}{"view": "board"},
		},
		"preview_session": previewSession,
	})
	if err != nil {
		t.Fatalf("encode create request: %v", err)
	}
	handler := NewArtifactTaskHandler(hub)
	create := httptest.NewRequest(http.MethodPost, "/api/artifact-tasks", strings.NewReader(string(createBody)))
	create = create.WithContext(context.WithValue(create.Context(), uidKey, int64(7)))
	createRecorder := httptest.NewRecorder()
	handler.HandleUserTasks(createRecorder, create)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created map[string]interface{}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	taskID, _ := created["task_id"].(string)
	taskRef, _ := created["task_ref"].(string)
	if !artifactTaskIDPattern.MatchString(taskID) || !artifactTaskRefPattern.MatchString(taskRef) ||
		created["visible_message"] != "来自「任务看板」：创建任务" {
		t.Fatalf("created task = %#v", created)
	}

	readBeforeDelivery := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-task?task_ref="+taskRef, nil)
	readBeforeDelivery = readBeforeDelivery.WithContext(context.WithValue(readBeforeDelivery.Context(), uidKey, int64(440)))
	readBeforeRecorder := httptest.NewRecorder()
	handler.HandleBotRead(readBeforeRecorder, readBeforeDelivery)
	if readBeforeRecorder.Code != http.StatusGone {
		t.Fatalf("undelivered task read status=%d body=%s", readBeforeRecorder.Code, readBeforeRecorder.Body.String())
	}

	messageBody, err := json.Marshal(map[string]interface{}{
		"topic_id": "p2p_7_440",
		"type":     "text",
		"content":  created["visible_message"],
		"metadata": map[string]interface{}{artifactTaskRefMetadataKey: taskRef, "trace": "kept"},
	})
	if err != nil {
		t.Fatalf("encode message request: %v", err)
	}
	send := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(messageBody)))
	send = send.WithContext(context.WithValue(send.Context(), uidKey, int64(7)))
	sendRecorder := httptest.NewRecorder()
	NewMessageHandler(db, hub).HandleSendMessage(sendRecorder, send)
	if sendRecorder.Code != http.StatusOK || strings.Contains(sendRecorder.Body.String(), taskRef) {
		t.Fatalf("send status=%d body=%s", sendRecorder.Code, sendRecorder.Body.String())
	}
	var delivered ServerMessage
	decodeQueuedServerMessage(t, bot.send, &delivered)
	if delivered.Data == nil || delivered.Data.Metadata[artifactTaskRefMetadataKey] != taskRef {
		t.Fatalf("bot delivery = %#v", delivered.Data)
	}
	if messages := db.snapshotSavedMessages(); len(messages) != 1 || messages[0].Metadata[artifactTaskRefMetadataKey] != nil {
		t.Fatalf("persisted messages = %#v", messages)
	}

	replay := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(messageBody)))
	replay = replay.WithContext(context.WithValue(replay.Context(), uidKey, int64(7)))
	replayRecorder := httptest.NewRecorder()
	NewMessageHandler(db, hub).HandleSendMessage(replayRecorder, replay)
	if replayRecorder.Code == http.StatusOK {
		t.Fatalf("replayed task ref was accepted: %s", replayRecorder.Body.String())
	}

	read := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-task?task_ref="+taskRef, nil)
	read = read.WithContext(context.WithValue(read.Context(), uidKey, int64(440)))
	readRecorder := httptest.NewRecorder()
	handler.HandleBotRead(readRecorder, read)
	if readRecorder.Code != http.StatusOK || strings.Contains(readRecorder.Body.String(), taskRef) {
		t.Fatalf("bot read status=%d body=%s", readRecorder.Code, readRecorder.Body.String())
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal(readRecorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode task snapshot: %v", err)
	}
	trusted := snapshot["trusted"].(map[string]interface{})
	trustedTask := trusted["task"].(map[string]interface{})
	target := trusted["writeback_target"].(map[string]interface{})
	if trustedTask["task_id"] != taskID || target["task_id"] != taskID ||
		!artifactWritebackRefPattern.MatchString(target["writeback_ref"].(string)) {
		t.Fatalf("task snapshot = %#v", snapshot)
	}
}

func TestArtifactTaskResultCompletesOnlyAfterExactPreviewReceipt(t *testing.T) {
	db := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
	}, owners: map[int64]int64{440: 7}}
	hub := NewHub(db, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
		if agentUID != 440 || artifactID != "task-board" {
			t.Fatalf("resolver args agent=%d artifact=%q", agentUID, artifactID)
		}
		return testArtifactTaskCandidate(runtimeRoute{}).Artifact, nil
	}))
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	hub.ensureClientRuntimeRoute(human)
	hub.addClient(human)
	task, err := hub.artifactTasks.create(testArtifactTaskCandidate(hub.clientRoute(human)))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := hub.artifactTasks.claimDelivery(task.Ref, task.ActorUID, task.TopicID, task.AgentUID); err != nil {
		t.Fatalf("claim task: %v", err)
	}
	target, err := hub.issueArtifactTaskWritebackIfActive(task.Ref, task.ID)
	if err != nil {
		t.Fatalf("issue task writeback: %v", err)
	}
	resultID := "arr_" + strings.Repeat("r", 43)
	requestBody, err := json.Marshal(map[string]interface{}{
		"contract_version":  artifactResultContract,
		"writeback_ref":     target.Ref,
		"task_id":           task.ID,
		"artifact_id":       "task-board",
		"displayed_version": 3,
		"sink_id":           "tasks.upsert.v1",
		"result_id":         resultID,
		"payload":           map[string]interface{}{"items": []interface{}{map[string]interface{}{"title": "准备发布清单"}}},
	})
	if err != nil {
		t.Fatalf("encode task result: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-results", strings.NewReader(string(requestBody)))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		NewArtifactResultHandler(hub).HandleBotResults(recorder, req)
		close(done)
	}()

	var event ServerMessage
	select {
	case encoded := <-human.send:
		if err := json.Unmarshal(encoded, &event); err != nil {
			t.Fatalf("decode result event: %v", err)
		}
	case <-done:
		t.Fatalf("task result returned before delivery: status=%d body=%s", recorder.Code, recorder.Body.String())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result event")
	}
	if event.ArtifactResult == nil || event.ArtifactResult.TaskID != task.ID || event.ArtifactResult.ContextRef != "" {
		t.Fatalf("task result event = %#v", event.ArtifactResult)
	}
	beforeReceipt, _ := hub.artifactTasks.forActor(task.ID, 7)
	if beforeReceipt.Status == artifactTaskCompleted {
		t.Fatal("HTTP submission completed task before the page applied it")
	}
	receipt, err := json.Marshal(map[string]interface{}{
		"contract_version": artifactResultReceiptContract,
		"result_id":        resultID,
		"status":           "applied",
		"receipt":          map[string]interface{}{"created": 1},
	})
	if err != nil {
		t.Fatalf("encode receipt: %v", err)
	}
	hub.handleArtifactResultReceipt(human, &MsgArtifactResult{
		Type:             "receipt",
		OriginNodeID:     event.ArtifactResult.OriginNodeID,
		TaskID:           task.ID,
		WritebackRef:     target.Ref,
		TopicID:          task.TopicID,
		AgentUID:         "440",
		ArtifactID:       "task-board",
		DisplayedVersion: 3,
		ResultID:         resultID,
		Receipt:          receipt,
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result response")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"applied"`) {
		t.Fatalf("result status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	completed, _ := hub.artifactTasks.forActor(task.ID, 7)
	if completed.Status != artifactTaskCompleted || completed.ResultID != resultID {
		t.Fatalf("completed task = %#v", completed)
	}
}
