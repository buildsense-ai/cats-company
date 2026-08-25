package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func artifactResultTestPreviewRoute() runtimeRoute {
	return runtimeRoute{NodeID: "preview-node", ConnectionID: "preview-connection"}
}

func readArtifactContextForWritebackTest(
	t *testing.T,
	handler *ArtifactContextSnapshotHandler,
	contextRef string,
) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/bot/artifact-context?context_ref="+contextRef,
		nil,
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	handler.HandleBotRead(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode context response: %v", err)
	}
	return response
}

func artifactResultRequestBody(t *testing.T, writebackRef, resultID string) string {
	t.Helper()
	value := map[string]interface{}{
		"contract_version":        artifactResultContract,
		"writeback_ref":           writebackRef,
		"artifact_id":             "lesson-game",
		"displayed_version":       2,
		"sink_id":                 "risk-items.upsert.v1",
		"result_id":               resultID,
		"expected_state_revision": "42",
		"payload": map[string]interface{}{
			"items": []interface{}{map[string]interface{}{"title": "延期风险"}},
		},
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode result request: %v", err)
	}
	return string(encoded)
}

func TestArtifactResultWritebackCompletesThroughExactPreviewReceipt(t *testing.T) {
	hub := newArtifactSnapshotTestHub(t)
	contextHandler := NewArtifactContextSnapshotHandler(hub)
	resultHandler := NewArtifactResultHandler(hub)
	human := &Client{
		uid:         7,
		accountType: types.AccountHuman,
		send:        make(chan []byte, 2),
	}
	otherTab := &Client{
		uid:         7,
		accountType: types.AccountHuman,
		send:        make(chan []byte, 2),
	}
	hub.addClient(otherTab)
	snapshotResponse := createArtifactSnapshotForTest(t, contextHandler, "会议纪要", human)
	contextRef, _ := snapshotResponse["context_ref"].(string)
	readResponse := readArtifactContextForWritebackTest(t, contextHandler, contextRef)
	target, ok := readResponse["writeback_target"].(map[string]interface{})
	if !ok || target["contract_version"] != artifactWritebackTargetContract {
		t.Fatalf("missing writeback target: %#v", readResponse)
	}
	writebackRef, _ := target["writeback_ref"].(string)
	if !artifactWritebackRefPattern.MatchString(writebackRef) {
		t.Fatalf("writeback_ref = %q", writebackRef)
	}

	resultID := "arr_" + strings.Repeat("r", 43)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/bot/artifact-results",
		strings.NewReader(artifactResultRequestBody(t, writebackRef, resultID)),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		resultHandler.HandleBotResults(recorder, req)
		close(done)
	}()

	var event ServerMessage
	select {
	case encoded := <-human.send:
		if err := json.Unmarshal(encoded, &event); err != nil {
			t.Fatalf("decode Artifact result event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Artifact result event")
	}
	select {
	case encoded := <-otherTab.send:
		t.Fatalf("other tab received the exact-preview request: %s", encoded)
	default:
	}
	if event.ArtifactResult == nil || event.ArtifactResult.ContextRef != contextRef ||
		event.ArtifactResult.WritebackRef != writebackRef || event.ArtifactResult.ResultID != resultID ||
		event.ArtifactResult.ArtifactID != "lesson-game" || event.ArtifactResult.DisplayedVersion != 2 {
		t.Fatalf("unexpected Artifact result event: %#v", event.ArtifactResult)
	}
	if strings.Contains(string(event.ArtifactResult.Payload), writebackRef) {
		t.Fatal("payload leaked the writeback capability")
	}

	receipt, err := json.Marshal(map[string]interface{}{
		"contract_version": artifactResultReceiptContract,
		"result_id":        resultID,
		"status":           "applied",
		"receipt": map[string]interface{}{
			"created":        1,
			"state_revision": "43",
		},
	})
	if err != nil {
		t.Fatalf("encode receipt: %v", err)
	}
	hub.handleArtifactResultReceipt(otherTab, &MsgArtifactResult{
		Type:             "receipt",
		OriginNodeID:     event.ArtifactResult.OriginNodeID,
		ContextRef:       contextRef,
		WritebackRef:     writebackRef,
		TopicID:          "p2p_7_440",
		AgentUID:         "440",
		ArtifactID:       "lesson-game",
		DisplayedVersion: 2,
		ResultID:         resultID,
		Receipt:          receipt,
	})
	select {
	case <-done:
		t.Fatal("other tab forged an applied receipt")
	default:
	}
	hub.handleArtifactResultReceipt(human, &MsgArtifactResult{
		Type:             "receipt",
		OriginNodeID:     event.ArtifactResult.OriginNodeID,
		ContextRef:       contextRef,
		WritebackRef:     writebackRef,
		TopicID:          "p2p_7_440",
		AgentUID:         "440",
		ArtifactID:       "lesson-game",
		DisplayedVersion: 2,
		ResultID:         resultID,
		Receipt:          receipt,
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Artifact result HTTP response")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode result response: %v", err)
	}
	if response["contract_version"] != artifactResultDeliveryContract || response["status"] != "applied" {
		t.Fatalf("unexpected result response: %#v", response)
	}
	application, _ := response["application_receipt"].(map[string]interface{})
	if application["result_id"] != resultID || application["status"] != "applied" {
		t.Fatalf("unexpected application receipt: %#v", application)
	}
}

func TestArtifactResultWritebackRejectsWrongActorReceiptThenTimesOut(t *testing.T) {
	store := newArtifactResultWritebackStore(time.Minute, 10*time.Millisecond, 8)
	snapshot := artifactContextSnapshot{
		Ref:              "acr_" + strings.Repeat("c", 43),
		ActorUID:         7,
		TopicID:          "p2p_7_440",
		AgentUID:         440,
		Artifact:         ArtifactContextRecord{ID: "lesson-game"},
		DisplayedVersion: 2,
		Revision:         1,
		PreviewRoute:     artifactResultTestPreviewRoute(),
	}
	target, err := store.issue(snapshot)
	if err != nil {
		t.Fatalf("issue target: %v", err)
	}
	request := artifactResultSubmitRequest{
		ContractVersion:  artifactResultContract,
		WritebackRef:     target.Ref,
		ArtifactID:       "lesson-game",
		DisplayedVersion: 2,
		SinkID:           "risk-items.upsert.v1",
		ResultID:         "arr_" + strings.Repeat("r", 43),
		Payload:          json.RawMessage(`{"items":[]}`),
	}
	delivery, created, status := store.startDelivery(request, target, hashArtifactResultRequest(request, target))
	if !created || status != "" {
		t.Fatalf("start delivery created=%v status=%q", created, status)
	}
	if _, claimed := store.claimDeliveryForSend(delivery); !claimed {
		t.Fatal("delivery was not claimed for send")
	}
	receipt := json.RawMessage(`{"contract_version":"catsco.artifact-result-receipt.v1","result_id":"` + request.ResultID + `","status":"applied"}`)
	if store.completeReceipt(&MsgArtifactResult{
		ActorUID:         "8",
		ContextRef:       target.ContextRef,
		WritebackRef:     target.Ref,
		TopicID:          target.TopicID,
		AgentUID:         "440",
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		ResultID:         request.ResultID,
	}, receipt, target.PreviewRoute) {
		t.Fatal("wrong actor completed the delivery")
	}
	time.Sleep(15 * time.Millisecond)
	store.mu.Lock()
	store.cleanupLocked(store.now().UTC())
	store.mu.Unlock()
	select {
	case <-delivery.Done:
	default:
		t.Fatal("expired delivery did not complete")
	}
	outcome, ok := store.outcome(request.ResultID)
	if !ok || outcome.Status != "delivery_timeout" {
		t.Fatalf("outcome = %#v, %v", outcome, ok)
	}
}

func TestArtifactResultReceiptReturnsToOriginRuntimeNode(t *testing.T) {
	shared := newSharedMemoryRuntimeState()
	origin := NewHubWithRuntime(nil, nil, shared, "origin-node")
	preview := NewHubWithRuntime(nil, nil, shared, "preview-node")
	previewClient := &Client{
		uid:          7,
		accountType:  types.AccountHuman,
		connectionID: "preview-connection",
	}
	snapshot := artifactContextSnapshot{
		Ref:              "acr_" + strings.Repeat("c", 43),
		ActorUID:         7,
		TopicID:          "p2p_7_440",
		AgentUID:         440,
		Artifact:         ArtifactContextRecord{ID: "lesson-game"},
		DisplayedVersion: 2,
		Revision:         1,
		PreviewRoute:     preview.clientRoute(previewClient),
	}
	target, err := origin.artifactResultWritebacks.issue(snapshot)
	if err != nil {
		t.Fatalf("issue target: %v", err)
	}
	request := artifactResultSubmitRequest{
		ContractVersion:  artifactResultContract,
		WritebackRef:     target.Ref,
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		SinkID:           "risk-items.upsert.v1",
		ResultID:         "arr_" + strings.Repeat("n", 43),
		Payload:          json.RawMessage(`{"items":[]}`),
	}
	delivery, created, status := origin.artifactResultWritebacks.startDelivery(
		request,
		target,
		hashArtifactResultRequest(request, target),
	)
	if !created || status != "" {
		t.Fatalf("start delivery created=%v status=%q", created, status)
	}
	if _, claimed := origin.artifactResultWritebacks.claimDeliveryForSend(delivery); !claimed {
		t.Fatal("delivery was not claimed for send")
	}
	receipt := json.RawMessage(`{"contract_version":"catsco.artifact-result-receipt.v1","result_id":"` + request.ResultID + `","status":"applied"}`)
	preview.handleArtifactResultReceipt(previewClient, &MsgArtifactResult{
		Type:             "receipt",
		OriginNodeID:     origin.nodeID,
		ContextRef:       target.ContextRef,
		WritebackRef:     target.Ref,
		TopicID:          target.TopicID,
		AgentUID:         "440",
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		ResultID:         request.ResultID,
		Receipt:          receipt,
	})
	select {
	case <-delivery.Done:
	case <-time.After(time.Second):
		t.Fatal("receipt did not reach the origin runtime node")
	}
	outcome, ok := origin.artifactResultWritebacks.outcome(request.ResultID)
	if !ok || outcome.Status != "applied" {
		t.Fatalf("origin outcome = %#v, %v", outcome, ok)
	}
}

func TestArtifactResultRouteTargetsOneRemoteConnection(t *testing.T) {
	shared := newSharedMemoryRuntimeState()
	origin := NewHubWithRuntime(nil, nil, shared, "origin-node")
	preview := NewHubWithRuntime(nil, nil, shared, "preview-node")
	intended := &Client{
		uid:          7,
		accountType:  types.AccountHuman,
		connectionID: "preview-intended",
		send:         make(chan []byte, 1),
	}
	other := &Client{
		uid:          7,
		accountType:  types.AccountHuman,
		connectionID: "preview-other",
		send:         make(chan []byte, 1),
	}
	preview.addClient(intended)
	preview.addClient(other)
	preview.bindClientRuntimeRoute(intended)
	preview.bindClientRuntimeRoute(other)
	message := &MsgArtifactResult{
		Type:       "request",
		ActorUID:   "7",
		ResultID:   "arr_" + strings.Repeat("r", 43),
		ArtifactID: "lesson-game",
	}
	if !origin.sendArtifactResultToRoute(preview.clientRoute(intended), message) {
		t.Fatal("exact remote delivery failed")
	}
	var delivered ServerMessage
	decodeQueuedServerMessage(t, intended.send, &delivered)
	if delivered.ArtifactResult == nil || delivered.ArtifactResult.ResultID != message.ResultID {
		t.Fatalf("delivered message = %#v", delivered.ArtifactResult)
	}
	select {
	case encoded := <-other.send:
		t.Fatalf("other remote connection received delivery: %s", encoded)
	default:
	}
}

func TestArtifactResultWritebackIsInvalidatedWhenPreviewChanges(t *testing.T) {
	hub := newArtifactSnapshotTestHub(t)
	contextHandler := NewArtifactContextSnapshotHandler(hub)
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	first := createArtifactSnapshotForTest(t, contextHandler, "first", human)
	firstRef := first["context_ref"].(string)
	read := readArtifactContextForWritebackTest(t, contextHandler, firstRef)
	target := read["writeback_target"].(map[string]interface{})
	writebackRef := target["writeback_ref"].(string)

	createArtifactSnapshotForTest(t, contextHandler, "second", human)
	if _, ok := hub.artifactResultWritebacks.target(writebackRef); ok {
		t.Fatal("replaced preview retained its writeback target")
	}
}

func TestArtifactResultSendClaimLinearizesWithPreviewInvalidation(t *testing.T) {
	for attempt := 0; attempt < 200; attempt++ {
		store := newArtifactResultWritebackStore(time.Minute, time.Second, 8)
		snapshot := artifactContextSnapshot{
			Ref:              "acr_" + strings.Repeat("c", 43),
			ActorUID:         7,
			TopicID:          "p2p_7_440",
			AgentUID:         440,
			Artifact:         ArtifactContextRecord{ID: "lesson-game"},
			DisplayedVersion: 2,
			Revision:         1,
			PreviewRoute:     artifactResultTestPreviewRoute(),
		}
		target, err := store.issue(snapshot)
		if err != nil {
			t.Fatalf("attempt %d issue target: %v", attempt, err)
		}
		request := artifactResultSubmitRequest{
			ContractVersion:  artifactResultContract,
			WritebackRef:     target.Ref,
			ArtifactID:       target.ArtifactID,
			DisplayedVersion: target.DisplayedVersion,
			SinkID:           "risk-items.upsert.v1",
			ResultID:         "arr_" + strings.Repeat("r", 43),
			Payload:          json.RawMessage(`{"items":[]}`),
		}
		delivery, created, status := store.startDelivery(request, target, hashArtifactResultRequest(request, target))
		if !created || status != "" {
			t.Fatalf("attempt %d start delivery created=%v status=%q", attempt, created, status)
		}

		start := make(chan struct{})
		var workers sync.WaitGroup
		var claimed bool
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			_, claimed = store.claimDeliveryForSend(delivery)
		}()
		go func() {
			defer workers.Done()
			<-start
			store.invalidateContext(snapshot.Ref)
		}()
		close(start)
		workers.Wait()

		receipt := json.RawMessage(`{"contract_version":"catsco.artifact-result-receipt.v1","result_id":"` + request.ResultID + `","status":"applied"}`)
		message := &MsgArtifactResult{
			ActorUID:         "7",
			ContextRef:       target.ContextRef,
			WritebackRef:     target.Ref,
			TopicID:          target.TopicID,
			AgentUID:         "440",
			ArtifactID:       target.ArtifactID,
			DisplayedVersion: target.DisplayedVersion,
			ResultID:         request.ResultID,
		}
		accepted := store.completeReceipt(message, receipt, target.PreviewRoute)
		outcome, completed := store.outcomeFor(delivery)
		if claimed {
			if !accepted || !completed || outcome.Status != "applied" {
				t.Fatalf("attempt %d claim won but receipt outcome=%#v completed=%v accepted=%v", attempt, outcome, completed, accepted)
			}
			continue
		}
		if accepted || !completed || outcome.Status != "target_mismatch" {
			t.Fatalf("attempt %d invalidation won but outcome=%#v completed=%v accepted=%v", attempt, outcome, completed, accepted)
		}
	}
}

func TestArtifactWritebackGrantSharesSnapshotCurrentBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		resultByte string
		retire     func(*Hub, artifactContextSnapshot) (string, error)
	}{
		{
			name:       "replacement",
			resultByte: "r",
			retire: func(hub *Hub, old artifactContextSnapshot) (string, error) {
				_, previousRef, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
					ActorUID: old.ActorUID,
					TopicID:  old.TopicID,
					AgentUID: old.AgentUID,
					Artifact: ArtifactContextRecord{ID: old.Artifact.ID},
				})
				return previousRef, err
			},
		},
		{
			name:       "explicit invalidation",
			resultByte: "i",
			retire: func(hub *Hub, old artifactContextSnapshot) (string, error) {
				if !hub.artifactContextSnapshots.invalidate(old.Ref, old.ActorUID) {
					return "", errors.New("snapshot invalidation failed")
				}
				return old.Ref, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newArtifactSnapshotTestHub(t)
			old, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
				ActorUID:         7,
				TopicID:          "p2p_7_440",
				AgentUID:         440,
				Artifact:         ArtifactContextRecord{ID: "lesson-game"},
				DisplayedVersion: 2,
				PreviewRoute:     artifactResultTestPreviewRoute(),
			})
			if err != nil {
				t.Fatalf("create old snapshot: %v", err)
			}
			target, status, err := hub.issueArtifactWritebackIfCurrent(old.Ref, old.Revision)
			if err != nil || status != artifactContextSnapshotActive {
				t.Fatalf("issue old target: status=%q err=%v", status, err)
			}

			// Model the old Handler path after its final snapshot lookup has passed.
			stale, status := hub.artifactContextSnapshots.lookup(old.Ref)
			if status != artifactContextSnapshotActive || stale.Revision != old.Revision {
				t.Fatalf("old current check: status=%q snapshot=%#v", status, stale)
			}

			type retireResult struct {
				previousRef string
				err         error
			}
			retired := make(chan retireResult, 1)
			allowWritebackInvalidation := make(chan struct{})
			done := make(chan struct{})
			var release sync.Once
			releaseInvalidation := func() {
				release.Do(func() { close(allowWritebackInvalidation) })
			}
			go func() {
				previousRef, retireErr := test.retire(hub, old)
				retired <- retireResult{previousRef: previousRef, err: retireErr}
				<-allowWritebackInvalidation
				if retireErr == nil {
					hub.artifactResultWritebacks.invalidateContext(previousRef)
				}
				close(done)
			}()
			defer func() {
				releaseInvalidation()
				<-done
			}()

			retirement := <-retired
			if retirement.err != nil || retirement.previousRef != old.Ref {
				t.Fatalf("retire old snapshot: previous=%q err=%v", retirement.previousRef, retirement.err)
			}
			if _, exists := hub.artifactResultWritebacks.target(target.Ref); !exists {
				t.Fatal("test did not pause before writeback invalidation")
			}
			if _, issueStatus, issueErr := hub.issueArtifactWritebackIfCurrent(stale.Ref, stale.Revision); issueErr != nil || issueStatus == artifactContextSnapshotActive {
				t.Fatalf("stale snapshot reissued target: status=%q err=%v", issueStatus, issueErr)
			}

			request := artifactResultSubmitRequest{
				ContractVersion:  artifactResultContract,
				WritebackRef:     target.Ref,
				ArtifactID:       target.ArtifactID,
				DisplayedVersion: target.DisplayedVersion,
				SinkID:           "risk-items.upsert.v1",
				ResultID:         "arr_" + strings.Repeat(test.resultByte, 43),
				Payload:          json.RawMessage(`{"items":[]}`),
			}
			delivery, created, startStatus := hub.artifactResultWritebacks.startDelivery(
				request,
				target,
				hashArtifactResultRequest(request, target),
			)
			if !created || startStatus != "" {
				t.Fatalf("start inside delayed invalidation window: created=%v status=%q", created, startStatus)
			}
			if _, claimed := hub.claimArtifactResultDeliveryForSend(delivery); claimed || delivery.SendClaimed {
				t.Fatal("stale snapshot obtained send right")
			}
			outcome, completed := hub.artifactResultWritebacks.outcomeFor(delivery)
			if !completed || outcome.Status != "target_mismatch" || outcome.Code != "artifact_preview_changed" {
				t.Fatalf("stale delivery outcome=%#v completed=%v", outcome, completed)
			}

			releaseInvalidation()
			<-done
		})
	}
}

func TestArtifactResultWritebackReusesResultIDAndRejectsConflictingPayload(t *testing.T) {
	store := newArtifactResultWritebackStore(time.Minute, time.Second, 8)
	snapshot := artifactContextSnapshot{
		Ref:              "acr_" + strings.Repeat("c", 43),
		ActorUID:         7,
		TopicID:          "p2p_7_440",
		AgentUID:         440,
		Artifact:         ArtifactContextRecord{ID: "lesson-game"},
		DisplayedVersion: 2,
		Revision:         1,
		PreviewRoute:     artifactResultTestPreviewRoute(),
	}
	target, err := store.issue(snapshot)
	if err != nil {
		t.Fatalf("issue target: %v", err)
	}
	request := artifactResultSubmitRequest{
		ContractVersion:  artifactResultContract,
		WritebackRef:     target.Ref,
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		SinkID:           "risk-items.upsert.v1",
		ResultID:         "arr_" + strings.Repeat("i", 43),
		Payload:          json.RawMessage(`{"items":[{"title":"first"}]}`),
	}

	first, created, status := store.startDelivery(request, target, hashArtifactResultRequest(request, target))
	if !created || status != "" {
		t.Fatalf("first delivery created=%v status=%q", created, status)
	}
	if _, claimed := store.claimDeliveryForSend(first); !claimed {
		t.Fatal("delivery was not claimed for send")
	}
	second, created, status := store.startDelivery(request, target, hashArtifactResultRequest(request, target))
	if created || status != "" || second != first {
		t.Fatalf("same request did not reuse delivery: created=%v status=%q", created, status)
	}

	conflict := request
	conflict.Payload = json.RawMessage(`{"items":[{"title":"different"}]}`)
	if delivery, created, status := store.startDelivery(
		conflict,
		target,
		hashArtifactResultRequest(conflict, target),
	); delivery != nil || created || status != "result_id_conflict" {
		t.Fatalf("conflicting request delivery=%#v created=%v status=%q", delivery, created, status)
	}

	receipt := json.RawMessage(`{"contract_version":"catsco.artifact-result-receipt.v1","result_id":"` + request.ResultID + `","status":"applied"}`)
	if !store.completeReceipt(&MsgArtifactResult{
		ActorUID:         "7",
		ContextRef:       target.ContextRef,
		WritebackRef:     target.Ref,
		TopicID:          target.TopicID,
		AgentUID:         "440",
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		ResultID:         request.ResultID,
	}, receipt, target.PreviewRoute) {
		t.Fatal("valid receipt did not complete the delivery")
	}
	replayed, created, status := store.startDelivery(request, target, hashArtifactResultRequest(request, target))
	if created || status != "" || replayed != first {
		t.Fatalf("completed delivery was not replayed: created=%v status=%q", created, status)
	}
	outcome, ok := store.outcome(request.ResultID)
	if !ok || outcome.Status != "applied" {
		t.Fatalf("replayed outcome = %#v, %v", outcome, ok)
	}
}

func TestArtifactResultWritebackReturnsNotConnectedWithoutPreview(t *testing.T) {
	hub := newArtifactSnapshotTestHub(t)
	contextHandler := NewArtifactContextSnapshotHandler(hub)
	resultHandler := NewArtifactResultHandler(hub)
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	snapshotResponse := createArtifactSnapshotForTest(t, contextHandler, "会议纪要", human)
	contextRef := snapshotResponse["context_ref"].(string)
	readResponse := readArtifactContextForWritebackTest(t, contextHandler, contextRef)
	target := readResponse["writeback_target"].(map[string]interface{})
	writebackRef := target["writeback_ref"].(string)
	hub.removeClient(human)
	resultID := "arr_" + strings.Repeat("o", 43)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/bot/artifact-results",
		strings.NewReader(artifactResultRequestBody(t, writebackRef, resultID)),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	resultHandler.HandleBotResults(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode result response: %v", err)
	}
	if response["status"] != "not_connected" || response["code"] != "artifact_preview_not_connected" {
		t.Fatalf("unexpected disconnected response: %#v", response)
	}
	if response["application_receipt"] != nil {
		t.Fatalf("disconnected response invented an application receipt: %#v", response)
	}
}

func TestArtifactResultWritebackRetriesUncertainDeliveryWithNewPreviewTarget(t *testing.T) {
	store := newArtifactResultWritebackStore(time.Minute, time.Second, 8)
	snapshot := artifactContextSnapshot{
		Ref:              "acr_" + strings.Repeat("a", 43),
		ActorUID:         7,
		TopicID:          "p2p_7_440",
		AgentUID:         440,
		Artifact:         ArtifactContextRecord{ID: "lesson-game"},
		DisplayedVersion: 2,
		Revision:         1,
		PreviewRoute:     artifactResultTestPreviewRoute(),
	}
	firstTarget, err := store.issue(snapshot)
	if err != nil {
		t.Fatalf("issue first target: %v", err)
	}
	request := artifactResultSubmitRequest{
		ContractVersion:       artifactResultContract,
		WritebackRef:          firstTarget.Ref,
		ArtifactID:            firstTarget.ArtifactID,
		DisplayedVersion:      firstTarget.DisplayedVersion,
		SinkID:                "risk-items.upsert.v1",
		ResultID:              "arr_" + strings.Repeat("u", 43),
		ExpectedStateRevision: "42",
		Payload:               json.RawMessage(`{"items":[{"title":"same logical operation"}]}`),
	}
	first, created, status := store.startDelivery(
		request,
		firstTarget,
		hashArtifactResultRequest(request, firstTarget),
	)
	if !created || status != "" {
		t.Fatalf("first delivery created=%v status=%q", created, status)
	}
	store.completePlatform(first, "delivery_timeout", "artifact_result_delivery_timeout", "")
	select {
	case <-first.Done:
	default:
		t.Fatal("uncertain first delivery did not complete")
	}

	snapshot.Ref = "acr_" + strings.Repeat("b", 43)
	snapshot.Revision = 2
	secondTarget, err := store.issue(snapshot)
	if err != nil {
		t.Fatalf("issue second target: %v", err)
	}
	retry := request
	retry.WritebackRef = secondTarget.Ref
	second, created, status := store.startDelivery(
		retry,
		secondTarget,
		hashArtifactResultRequest(retry, secondTarget),
	)
	if !created || status != "" || second == first {
		t.Fatalf("uncertain result was not redelivered: created=%v status=%q", created, status)
	}
	if second.Target.Ref != secondTarget.Ref || second.RequestHash != first.RequestHash {
		t.Fatalf("retry target/hash mismatch: first=%#v second=%#v", first, second)
	}
	firstOutcome, ok := store.outcomeFor(first)
	if !ok || firstOutcome.Status != "delivery_timeout" {
		t.Fatalf("first attempt lost its own outcome: %#v, %v", firstOutcome, ok)
	}
	if outcome, ok := store.outcomeFor(second); ok {
		t.Fatalf("new attempt unexpectedly inherited the old outcome: %#v", outcome)
	}
	store.completePlatform(first, "delivery_timeout", "artifact_result_delivery_timeout", "")
	if outcome, ok := store.outcomeFor(second); ok {
		t.Fatalf("late completion from the old attempt ended the retry: %#v", outcome)
	}
}

func TestArtifactResultWritebackRejectsMalformedApplicationReceipt(t *testing.T) {
	store := newArtifactResultWritebackStore(time.Minute, time.Second, 8)
	snapshot := artifactContextSnapshot{
		Ref:              "acr_" + strings.Repeat("c", 43),
		ActorUID:         7,
		TopicID:          "p2p_7_440",
		AgentUID:         440,
		Artifact:         ArtifactContextRecord{ID: "lesson-game"},
		DisplayedVersion: 2,
		Revision:         1,
		PreviewRoute:     artifactResultTestPreviewRoute(),
	}
	target, err := store.issue(snapshot)
	if err != nil {
		t.Fatalf("issue target: %v", err)
	}
	request := artifactResultSubmitRequest{
		ContractVersion:  artifactResultContract,
		WritebackRef:     target.Ref,
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		SinkID:           "risk-items.upsert.v1",
		ResultID:         "arr_" + strings.Repeat("m", 43),
		Payload:          json.RawMessage(`{"items":[]}`),
	}
	delivery, created, status := store.startDelivery(request, target, hashArtifactResultRequest(request, target))
	if !created || status != "" {
		t.Fatalf("start delivery created=%v status=%q", created, status)
	}
	if _, claimed := store.claimDeliveryForSend(delivery); !claimed {
		t.Fatal("delivery was not claimed for send")
	}
	accepted := store.completeReceipt(&MsgArtifactResult{
		ActorUID:         "7",
		ContextRef:       target.ContextRef,
		WritebackRef:     target.Ref,
		TopicID:          target.TopicID,
		AgentUID:         "440",
		ArtifactID:       target.ArtifactID,
		DisplayedVersion: target.DisplayedVersion,
		ResultID:         request.ResultID,
	}, json.RawMessage(`{"status":"applied"}`), target.PreviewRoute)
	if !accepted {
		t.Fatal("malformed receipt was not converted into a terminal failure")
	}
	outcome, ok := store.outcome(request.ResultID)
	if !ok || outcome.Status != "failed" || outcome.Code != "invalid_receipt" {
		t.Fatalf("malformed receipt outcome = %#v, %v", outcome, ok)
	}
	var receipt artifactApplicationReceipt
	if err := json.Unmarshal(outcome.ApplicationReceipt, &receipt); err != nil {
		t.Fatalf("decode normalized failure receipt: %v", err)
	}
	if receipt.Status != "failed" || receipt.Code != "invalid_receipt" || receipt.ResultID != request.ResultID {
		t.Fatalf("normalized failure receipt = %#v", receipt)
	}
}
