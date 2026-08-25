package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type artifactMetadataCaptureStore struct {
	*identityMessageStore
	savedMetadata map[string]interface{}
}

func (s *artifactMetadataCaptureStore) SaveMessageWithMetadata(_ string, _ int64, _ string, _ []types.ContentBlock, _, _, _ string, _ int64, _ string, metadata map[string]interface{}) (int64, bool, error) {
	s.savedMetadata = cloneArtifactPageContext(metadata)
	return 77, false, nil
}

type artifactGroupMessageStore struct {
	*artifactMetadataCaptureStore
	group   *types.Group
	members []*types.GroupMember
}

func (s *artifactGroupMessageStore) GetGroup(int64) (*types.Group, error) {
	return s.group, nil
}

func (s *artifactGroupMessageStore) GetGroupMembers(int64) ([]*types.GroupMember, error) {
	return s.members, nil
}

func (s *artifactGroupMessageStore) IsGroupMember(_ int64, userID int64) (bool, error) {
	for _, member := range s.members {
		if member != nil && member.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

func (s *artifactGroupMessageStore) IsMemberMuted(int64, int64) (bool, error) {
	return false, nil
}

func (s *artifactGroupMessageStore) IsChannelManagedGroup(int64) (bool, error) {
	return false, nil
}

func newArtifactSnapshotTestHub(t *testing.T) *Hub {
	t.Helper()
	db := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman, State: 0},
			8:   {ID: 8, AccountType: types.AccountHuman, State: 0},
			440: {ID: 440, AccountType: types.AccountBot, State: 0},
			441: {ID: 441, AccountType: types.AccountBot, State: 0},
		},
		owners: map[int64]int64{440: 7},
	}
	hub := NewHub(db, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
		if agentUID != 440 || artifactID != "lesson-game" {
			t.Fatalf("resolver arguments = agent %d artifact %q", agentUID, artifactID)
		}
		return ArtifactContextRecord{
			ID:             artifactID,
			Title:          "课堂小游戏",
			Kind:           "html",
			URL:            "https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/",
			PublishVersion: 3,
		}, nil
	}))
	return hub
}

func createArtifactSnapshotForTest(t *testing.T, handler *ArtifactContextSnapshotHandler, selectedText string, previewClients ...*Client) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{
		"topic_id": "p2p_7_440",
		"artifact_ref": map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "lesson-game",
			"displayed_version": 2,
			"currently_visible": true,
		},
		"page_context": map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-14T01:02:03Z",
			"selected_text":    selectedText,
			"controls": []interface{}{
				map[string]interface{}{"type": "checkbox", "name": "feedback", "value": "f12", "checked": true},
				map[string]interface{}{"type": "password", "name": "secret", "value": "do-not-store"},
			},
			"semantic_context": map[string]interface{}{
				"view":      "customer-comparison",
				"selection": []interface{}{"c12", "c18"},
			},
			"local_storage": map[string]interface{}{"token": "forged"},
		},
	}
	if len(previewClients) > 0 && previewClients[0] != nil {
		preview := previewClients[0]
		handler.hub.ensureClientRuntimeRoute(preview)
		if handler.hub.getClientByConnectionID(preview.connectionID) != preview {
			handler.hub.addClient(preview)
		}
		session, err := handler.hub.artifactPreviewSessions.issue(preview.uid, handler.hub.clientRoute(preview))
		if err != nil {
			t.Fatalf("issue preview session: %v", err)
		}
		body["preview_session"] = session
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode snapshot request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/artifact-context/snapshots", strings.NewReader(string(encoded)))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	recorder := httptest.NewRecorder()
	handler.HandleUserSnapshots(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return response
}

func TestArtifactContextSnapshotCreateReadAndTrustBoundary(t *testing.T) {
	hub := newArtifactSnapshotTestHub(t)
	handler := NewArtifactContextSnapshotHandler(hub)
	created := createArtifactSnapshotForTest(t, handler, "  企业客户  ")
	ref, ok := created["context_ref"].(string)
	if !ok || !artifactContextRefPattern.MatchString(ref) || strings.Contains(ref, "lesson-game") {
		t.Fatalf("context_ref = %#v", created["context_ref"])
	}

	snapshot, status := hub.artifactContextSnapshots.lookup(ref)
	if status != artifactContextSnapshotActive || snapshot.AgentUID != 440 || snapshot.DisplayedVersion != 2 {
		t.Fatalf("snapshot = %#v status=%s", snapshot, status)
	}
	if snapshot.PageContext["selected_text"] != "企业客户" {
		t.Fatalf("page context = %#v", snapshot.PageContext)
	}
	semanticContext, ok := snapshot.PageContext["semantic_context"].(map[string]interface{})
	if !ok || semanticContext["view"] != "customer-comparison" {
		t.Fatalf("semantic context = %#v", snapshot.PageContext["semantic_context"])
	}
	selection, ok := semanticContext["selection"].([]interface{})
	if !ok || len(selection) != 2 || selection[0] != "c12" || selection[1] != "c18" {
		t.Fatalf("semantic selection = %#v", semanticContext["selection"])
	}
	controls := snapshot.PageContext["controls"].([]interface{})
	if len(controls) != 1 || controls[0].(map[string]interface{})["type"] != "checkbox" {
		t.Fatalf("controls = %#v", controls)
	}
	if _, exists := snapshot.PageContext["local_storage"]; exists {
		t.Fatalf("forged storage survived: %#v", snapshot.PageContext)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+ref, nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	handler.HandleBotRead(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode read response: %v", err)
	}
	if response["contract_version"] != artifactContextSnapshotContract || response["status"] != "ok" {
		t.Fatalf("response contract = %#v", response)
	}
	if _, exists := response["writeback_target"]; exists {
		t.Fatalf("snapshot without a preview session received writeback capability: %#v", response)
	}
	if strings.Contains(recorder.Body.String(), ref) {
		t.Fatal("read response echoed the bearer context_ref")
	}
	artifact := response["artifact"].(map[string]interface{})
	if artifact["id"] != "lesson-game" || artifact["agent_uid"] != "440" || artifact["latest_version"] != float64(3) {
		t.Fatalf("trusted Artifact = %#v", artifact)
	}
	trust := response["trust"].(map[string]interface{})
	if trust["artifact"] != "server_validated" || trust["page_context"] != "untrusted_page_supplied" ||
		trust["semantic_context"] != "untrusted_page_supplied" {
		t.Fatalf("trust labels = %#v", trust)
	}
	returnedPageContext, ok := response["page_context"].(map[string]interface{})
	if !ok {
		t.Fatalf("response page context = %#v", response["page_context"])
	}
	returnedSemanticContext, ok := returnedPageContext["semantic_context"].(map[string]interface{})
	if !ok || returnedSemanticContext["view"] != "customer-comparison" {
		t.Fatalf("returned semantic context = %#v", returnedPageContext["semantic_context"])
	}

	wrongBot := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+ref, nil)
	wrongBot = wrongBot.WithContext(context.WithValue(wrongBot.Context(), uidKey, int64(441)))
	wrongRecorder := httptest.NewRecorder()
	handler.HandleBotRead(wrongRecorder, wrongBot)
	if wrongRecorder.Code != http.StatusForbidden || !strings.Contains(wrongRecorder.Body.String(), `"status":"mismatch"`) {
		t.Fatalf("wrong Bot status = %d, body = %s", wrongRecorder.Code, wrongRecorder.Body.String())
	}
}

func TestArtifactContextSnapshotReadKeepsSemanticTrustOptional(t *testing.T) {
	hub := newArtifactSnapshotTestHub(t)
	handler := NewArtifactContextSnapshotHandler(hub)
	observedAt := "2026-08-14T01:02:03Z"
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7,
		TopicID:  "p2p_7_440",
		AgentUID: 440,
		Artifact: ArtifactContextRecord{
			ID:             "lesson-game",
			Title:          "课堂小游戏",
			Kind:           "html",
			URL:            "https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/",
			PublishVersion: 3,
		},
		DisplayedVersion: 2,
		ObservedAt:       observedAt,
		PageContext: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      observedAt,
			"selected_text":    "plain HTML selection",
		},
	})
	if err != nil {
		t.Fatalf("create plain snapshot: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+snapshot.Ref, nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	handler.HandleBotRead(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode read response: %v", err)
	}
	trust, ok := response["trust"].(map[string]interface{})
	if !ok || trust["artifact"] != "server_validated" || trust["page_context"] != "untrusted_page_supplied" {
		t.Fatalf("plain trust labels = %#v", response["trust"])
	}
	if _, exists := trust["semantic_context"]; exists {
		t.Fatalf("plain response must not require semantic trust: %#v", trust)
	}
	pageContext, ok := response["page_context"].(map[string]interface{})
	if !ok || pageContext["selected_text"] != "plain HTML selection" {
		t.Fatalf("plain page context = %#v", response["page_context"])
	}
	if _, exists := pageContext["semantic_context"]; exists {
		t.Fatalf("plain page context unexpectedly has semantics: %#v", pageContext)
	}
}

func TestArtifactContextSnapshotReplacementExpiryAndInvalidation(t *testing.T) {
	hub := newArtifactSnapshotTestHub(t)
	clock := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	hub.artifactContextSnapshots = newArtifactContextSnapshotStore(time.Minute, time.Minute, 16)
	hub.artifactContextSnapshots.now = func() time.Time { return clock }
	handler := NewArtifactContextSnapshotHandler(hub)

	first := createArtifactSnapshotForTest(t, handler, "first")
	firstRef := first["context_ref"].(string)
	second := createArtifactSnapshotForTest(t, handler, "second")
	secondRef := second["context_ref"].(string)
	if firstRef == secondRef || second["revision"] != float64(2) {
		t.Fatalf("replacement response first=%#v second=%#v", first, second)
	}
	if _, status := hub.artifactContextSnapshots.lookup(firstRef); status != artifactContextSnapshotReplaced {
		t.Fatalf("first status = %s", status)
	}

	invalidateBody := `{"context_ref":"` + secondRef + `"}`
	invalidate := httptest.NewRequest(http.MethodDelete, "/api/artifact-context/snapshots", strings.NewReader(invalidateBody))
	invalidate = invalidate.WithContext(context.WithValue(invalidate.Context(), uidKey, int64(7)))
	invalidateRecorder := httptest.NewRecorder()
	handler.HandleUserSnapshots(invalidateRecorder, invalidate)
	if invalidateRecorder.Code != http.StatusOK {
		t.Fatalf("invalidate status = %d, body = %s", invalidateRecorder.Code, invalidateRecorder.Body.String())
	}
	if _, status := hub.artifactContextSnapshots.lookup(secondRef); status != artifactContextSnapshotInvalidated {
		t.Fatalf("second status = %s", status)
	}

	third := createArtifactSnapshotForTest(t, handler, "third")
	thirdRef := third["context_ref"].(string)
	clock = clock.Add(time.Minute + time.Nanosecond)
	if _, status := hub.artifactContextSnapshots.lookup(thirdRef); status != artifactContextSnapshotExpired {
		t.Fatalf("third status = %s", status)
	}
}

func TestArtifactContextBotReadReportsRetiredSnapshotStatus(t *testing.T) {
	for _, status := range []artifactContextSnapshotState{
		artifactContextSnapshotExpired,
		artifactContextSnapshotReplaced,
		artifactContextSnapshotInvalidated,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			hub := newArtifactSnapshotTestHub(t)
			clock := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
			hub.artifactContextSnapshots = newArtifactContextSnapshotStore(time.Minute, time.Minute, 16)
			hub.artifactContextSnapshots.now = func() time.Time { return clock }
			snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
				ActorUID: 7, TopicID: "p2p_7_440", AgentUID: 440,
				Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
			})
			if err != nil {
				t.Fatalf("create snapshot: %v", err)
			}
			switch status {
			case artifactContextSnapshotExpired:
				clock = clock.Add(time.Minute + time.Nanosecond)
			case artifactContextSnapshotReplaced:
				if _, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{ActorUID: 7, TopicID: "p2p_7_440", AgentUID: 440}); err != nil {
					t.Fatalf("replace snapshot: %v", err)
				}
			case artifactContextSnapshotInvalidated:
				hub.artifactContextSnapshots.invalidate(snapshot.Ref, 7)
			}

			request := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+snapshot.Ref, nil)
			request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
			recorder := httptest.NewRecorder()
			NewArtifactContextSnapshotHandler(hub).HandleBotRead(recorder, request)
			if recorder.Code != http.StatusGone || !strings.Contains(recorder.Body.String(), `"status":"`+string(status)+`"`) {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestArtifactContextRefIsNotPersistedAndIsRecipientScoped(t *testing.T) {
	base := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
			441: {ID: 441, AccountType: types.AccountBot},
		},
		owners: map[int64]int64{440: 7},
	}
	db := &artifactMetadataCaptureStore{identityMessageStore: base}
	hub := NewHub(db, nil)
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7,
		TopicID:  "p2p_7_440",
		AgentUID: 440,
		Artifact: ArtifactContextRecord{
			ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/lesson-game/latest/",
		},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	candidateMetadata := map[string]interface{}{
		"trace":                        "kept",
		artifactContextRefMetadataKey:  snapshot.Ref,
		artifactContextMetadataKey:     map[string]interface{}{"page_context": "legacy"},
		artifactPageContextMetadataKey: map[string]interface{}{"selected_text": "legacy"},
	}
	if _, wrongTopic := hub.extractArtifactContextDelivery(7, "p2p_7_441", candidateMetadata); wrongTopic != nil {
		t.Fatalf("wrong topic received delivery: %#v", wrongTopic)
	}
	if _, wrongActor := hub.extractArtifactContextDelivery(8, "p2p_7_440", candidateMetadata); wrongActor != nil {
		t.Fatalf("wrong actor received delivery: %#v", wrongActor)
	}
	metadata, delivery := hub.extractArtifactContextDelivery(7, "p2p_7_440", candidateMetadata)
	payload := &normalizedMessagePayload{
		StoredContent:      `"分析这些"`,
		DisplayContent:     "分析这些",
		StoredType:         "text",
		DisplayType:        "text",
		Metadata:           metadata,
		ArtifactContextRef: delivery,
	}
	if _, err := saveNormalizedMessage(db, "p2p_7_440", 7, 0, payload); err != nil {
		t.Fatalf("save message: %v", err)
	}
	if db.savedMetadata["trace"] != "kept" {
		t.Fatalf("saved metadata = %#v", db.savedMetadata)
	}
	for _, key := range []string{artifactContextRefMetadataKey, artifactContextMetadataKey, artifactRefMetadataKey, artifactPageContextMetadataKey} {
		if _, exists := db.savedMetadata[key]; exists {
			t.Fatalf("database metadata leaked %q: %#v", key, db.savedMetadata)
		}
	}

	matching := hub.messageForRecipient(7, 440, "p2p_7_440", 0, payload, 77)
	if matching.Data.Metadata[artifactContextRefMetadataKey] != snapshot.Ref {
		t.Fatalf("matching envelope = %#v", matching.Data.Metadata)
	}
	for _, recipient := range []int64{7, 441} {
		message := hub.messageForRecipient(7, recipient, "p2p_7_440", 0, payload, 77)
		if _, exists := message.Data.Metadata[artifactContextRefMetadataKey]; exists {
			t.Fatalf("recipient %d received ref: %#v", recipient, message.Data.Metadata)
		}
	}
}

func TestArtifactContextRefGroupFanoutFiltersEveryRecipient(t *testing.T) {
	db := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			8:  {ID: 8, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	target := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	other := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 1)}
	hub.addClient(target)
	hub.addClient(other)
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7, TopicID: "grp_80", AgentUID: 42,
		Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	_, delivery := hub.extractArtifactContextDelivery(7, "grp_80", map[string]interface{}{
		artifactContextRefMetadataKey: snapshot.Ref,
	})
	if delivery == nil {
		t.Fatal("extract delivery returned nil")
	}
	payload := &normalizedMessagePayload{
		StoredContent:      `"分析这些"`,
		DisplayContent:     "分析这些",
		StoredType:         "text",
		DisplayType:        "text",
		Mentions:           []string{structuredMentionAllBots},
		ArtifactContextRef: delivery,
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 91, nil)

	read := func(client *Client) *ServerMessage {
		t.Helper()
		select {
		case encoded := <-client.send:
			var message ServerMessage
			if err := json.Unmarshal(encoded, &message); err != nil {
				t.Fatalf("decode fanout: %v", err)
			}
			return &message
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for group fanout")
			return nil
		}
	}
	targetMessage := read(target)
	otherMessage := read(other)
	if targetMessage.Data.Metadata[artifactContextRefMetadataKey] != payload.ArtifactContextRef.Ref {
		t.Fatalf("target metadata = %#v", targetMessage.Data.Metadata)
	}
	if _, exists := otherMessage.Data.Metadata[artifactContextRefMetadataKey]; exists {
		t.Fatalf("human group member received ref: %#v", otherMessage.Data.Metadata)
	}
}

func TestArtifactContextRefRecipientRevalidationRejectsRetiredSnapshot(t *testing.T) {
	newHub := func(now *time.Time) *Hub {
		db := &identityMessageStore{
			users: map[int64]*types.User{
				7:  {ID: 7, AccountType: types.AccountHuman},
				42: {ID: 42, AccountType: types.AccountBot},
			},
			owners:      map[int64]int64{42: 7},
			friendPairs: map[string]bool{agentPairKey(7, 42): true},
		}
		hub := NewHub(db, nil)
		hub.artifactContextSnapshots = newArtifactContextSnapshotStore(time.Minute, time.Minute, 16)
		hub.artifactContextSnapshots.now = func() time.Time { return *now }
		return hub
	}

	for _, test := range []struct {
		name   string
		retire func(*testing.T, *Hub, artifactContextSnapshot, *time.Time)
	}{
		{
			name: "invalidated",
			retire: func(t *testing.T, hub *Hub, snapshot artifactContextSnapshot, _ *time.Time) {
				if !hub.artifactContextSnapshots.invalidate(snapshot.Ref, snapshot.ActorUID) {
					t.Fatal("invalidate snapshot returned false")
				}
			},
		},
		{
			name: "replaced",
			retire: func(t *testing.T, hub *Hub, snapshot artifactContextSnapshot, _ *time.Time) {
				if _, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
					ActorUID: snapshot.ActorUID, TopicID: snapshot.TopicID, AgentUID: snapshot.AgentUID,
					Artifact: ArtifactContextRecord{ID: "replacement", Title: "Replacement", Kind: "html", URL: "https://example.test/replacement/latest/"},
				}); err != nil {
					t.Fatalf("replace snapshot: %v", err)
				}
			},
		},
		{
			name: "expired",
			retire: func(_ *testing.T, _ *Hub, _ artifactContextSnapshot, now *time.Time) {
				*now = now.Add(2 * time.Minute)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
			hub := newHub(&now)
			snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
				ActorUID: 7, TopicID: "p2p_7_42", AgentUID: 42,
				Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
			})
			if err != nil {
				t.Fatalf("create snapshot: %v", err)
			}
			_, delivery := hub.extractArtifactContextDelivery(7, "p2p_7_42", map[string]interface{}{
				artifactContextRefMetadataKey: snapshot.Ref,
			})
			if delivery == nil {
				t.Fatal("extract delivery returned nil")
			}

			test.retire(t, hub, snapshot, &now)
			payload := &normalizedMessagePayload{
				StoredContent:      `"update"`,
				DisplayContent:     "update",
				StoredType:         "text",
				DisplayType:        "text",
				ArtifactContextRef: delivery,
			}
			message := hub.messageForRecipient(7, 42, "p2p_7_42", 0, payload, 1)
			if _, exists := message.Data.Metadata[artifactContextRefMetadataKey]; exists {
				t.Fatalf("retired snapshot ref reached recipient: %#v", message.Data.Metadata)
			}
		})
	}
}

func TestArtifactContextRefRESTIngressDeliversWithoutPersisting(t *testing.T) {
	db := &agentIdentityE2EStore{
		users: map[int64]*types.User{
			7:   {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			440: {ID: 440, Username: "artifact-agent", DisplayName: "Artifact Agent", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 7},
		friendPairs: map[string]bool{agentPairKey(7, 440): true},
	}
	hub := NewHub(db, nil)
	bot := &Client{uid: 440, accountType: types.AccountBot, send: make(chan []byte, 2)}
	hub.addClient(bot)
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7,
		TopicID:  "p2p_7_440",
		AgentUID: 440,
		Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"topic_id": "p2p_7_440",
		"type":     "text",
		"content":  "分析这些",
		"metadata": map[string]interface{}{
			artifactContextRefMetadataKey:  snapshot.Ref,
			artifactContextMetadataKey:     map[string]interface{}{"page_context": "legacy"},
			artifactPageContextMetadataKey: map[string]interface{}{"selected_text": "legacy"},
		},
	})
	if err != nil {
		t.Fatalf("encode send request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
	recorder := httptest.NewRecorder()
	NewMessageHandler(db, hub).HandleSendMessage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("send status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), snapshot.Ref) || strings.Contains(recorder.Body.String(), "legacy") {
		t.Fatalf("human response leaked Artifact context: %s", recorder.Body.String())
	}
	var delivered ServerMessage
	decodeQueuedServerMessage(t, bot.send, &delivered)
	if delivered.Data == nil || delivered.Data.Metadata[artifactContextRefMetadataKey] != snapshot.Ref {
		t.Fatalf("Bot delivery = %#v", delivered.Data)
	}
	if messages := db.snapshotSavedMessages(); len(messages) != 1 || messages[0].Content != "分析这些" || messages[0].Metadata != nil {
		t.Fatalf("saved messages = %#v", messages)
	}
}

func TestArtifactContextRefWebSocketIngressDeliversWithoutPersisting(t *testing.T) {
	db := &agentIdentityE2EStore{
		users: map[int64]*types.User{
			7:   {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			440: {ID: 440, Username: "artifact-agent", DisplayName: "Artifact Agent", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 7},
		friendPairs: map[string]bool{agentPairKey(7, 440): true},
	}
	hub := NewHub(db, nil)
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
	bot := &Client{uid: 440, accountType: types.AccountBot, send: make(chan []byte, 2)}
	hub.addClient(human)
	hub.addClient(bot)
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7,
		TopicID:  "p2p_7_440",
		AgentUID: 440,
		Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	hub.handlePub(human, &MsgClientPub{
		ID:      "pub-1",
		Topic:   "p2p_7_440",
		Type:    "text",
		Content: json.RawMessage(`"分析这些"`),
		Metadata: map[string]interface{}{
			artifactContextRefMetadataKey:  snapshot.Ref,
			artifactContextMetadataKey:     map[string]interface{}{"page_context": "legacy"},
			artifactPageContextMetadataKey: map[string]interface{}{"selected_text": "legacy"},
		},
	})
	var acknowledgement ServerMessage
	decodeQueuedServerMessage(t, human.send, &acknowledgement)
	if acknowledgement.Ctrl == nil || acknowledgement.Ctrl.Code != http.StatusOK {
		t.Fatalf("acknowledgement = %#v", acknowledgement.Ctrl)
	}
	var delivered ServerMessage
	decodeQueuedServerMessage(t, bot.send, &delivered)
	if delivered.Data == nil || delivered.Data.Metadata[artifactContextRefMetadataKey] != snapshot.Ref {
		t.Fatalf("Bot delivery = %#v", delivered.Data)
	}
	if messages := db.snapshotSavedMessages(); len(messages) != 1 || messages[0].Metadata != nil {
		t.Fatalf("saved messages = %#v", messages)
	}
}

func TestArtifactContextRefGroupIngressScopesRESTAndWebSocketDelivery(t *testing.T) {
	for _, ingress := range []string{"rest", "websocket"} {
		ingress := ingress
		t.Run(ingress, func(t *testing.T) {
			base := &identityMessageStore{users: map[int64]*types.User{
				7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
				8:  {ID: 8, Username: "observer", AccountType: types.AccountHuman},
				42: {ID: 42, Username: "artifact-agent", AccountType: types.AccountBot},
			}}
			capture := &artifactMetadataCaptureStore{identityMessageStore: base}
			db := &artifactGroupMessageStore{
				artifactMetadataCaptureStore: capture,
				group: &types.Group{
					ID: 80, Kind: types.GroupKindAgentTask, AgentIDs: []int64{42},
				},
				members: []*types.GroupMember{
					{GroupID: 80, UserID: 7},
					{GroupID: 80, UserID: 8},
					{GroupID: 80, UserID: 42, IsBot: true},
				},
			}
			hub := NewHub(db, nil)
			observer := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 2)}
			bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 2)}
			hub.addClient(observer)
			hub.addClient(bot)
			snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
				ActorUID: 7, TopicID: "grp_80", AgentUID: 42,
				Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
			})
			if err != nil {
				t.Fatalf("create snapshot: %v", err)
			}
			metadata := map[string]interface{}{
				"trace":                        "kept",
				artifactContextRefMetadataKey:  snapshot.Ref,
				artifactContextMetadataKey:     map[string]interface{}{"legacy": true},
				artifactPageContextMetadataKey: map[string]interface{}{"selected_text": "private"},
			}

			switch ingress {
			case "rest":
				body, err := json.Marshal(map[string]interface{}{
					"topic_id": "grp_80", "type": "text", "content": "分析这些", "metadata": metadata,
				})
				if err != nil {
					t.Fatalf("encode request: %v", err)
				}
				request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
				request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
				recorder := httptest.NewRecorder()
				NewMessageHandler(db, hub).HandleSendMessage(recorder, request)
				if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), snapshot.Ref) {
					t.Fatalf("REST response status = %d body = %s", recorder.Code, recorder.Body.String())
				}
			case "websocket":
				sender := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 2)}
				hub.addClient(sender)
				hub.handlePub(sender, &MsgClientPub{
					ID: "group-pub", Topic: "grp_80", Type: "text", Content: json.RawMessage(`"分析这些"`), Metadata: metadata,
				})
				var acknowledgement ServerMessage
				decodeQueuedServerMessage(t, sender.send, &acknowledgement)
				if acknowledgement.Ctrl == nil || acknowledgement.Ctrl.Code != http.StatusOK {
					t.Fatalf("WebSocket acknowledgement = %#v", acknowledgement.Ctrl)
				}
			}

			var botMessage ServerMessage
			decodeQueuedServerMessage(t, bot.send, &botMessage)
			if botMessage.Data == nil || botMessage.Data.Metadata[artifactContextRefMetadataKey] != snapshot.Ref {
				t.Fatalf("Bot delivery = %#v", botMessage.Data)
			}
			var observerMessage ServerMessage
			decodeQueuedServerMessage(t, observer.send, &observerMessage)
			if observerMessage.Data == nil {
				t.Fatal("human observer did not receive group message")
			}
			if _, exists := observerMessage.Data.Metadata[artifactContextRefMetadataKey]; exists {
				t.Fatalf("human observer received ref: %#v", observerMessage.Data.Metadata)
			}
			for _, key := range []string{artifactContextRefMetadataKey, artifactContextMetadataKey, artifactRefMetadataKey, artifactPageContextMetadataKey} {
				if _, exists := db.savedMetadata[key]; exists {
					t.Fatalf("database metadata leaked %q: %#v", key, db.savedMetadata)
				}
			}
			if db.savedMetadata["trace"] != "kept" {
				t.Fatalf("unrelated metadata was lost: %#v", db.savedMetadata)
			}
		})
	}
}

func TestArtifactContextSnapshotStoreDoesNotEvictActiveEntriesAtCapacity(t *testing.T) {
	store := newArtifactContextSnapshotStore(time.Minute, time.Minute, 2)
	first, _, err := store.create(artifactContextSnapshot{ActorUID: 7, TopicID: "p2p_7_42", AgentUID: 42})
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}
	second, _, err := store.create(artifactContextSnapshot{ActorUID: 8, TopicID: "p2p_8_43", AgentUID: 43})
	if err != nil {
		t.Fatalf("create second snapshot: %v", err)
	}
	if _, _, err := store.create(artifactContextSnapshot{ActorUID: 9, TopicID: "p2p_9_44", AgentUID: 44}); err == nil {
		t.Fatal("full store accepted a third active snapshot")
	}
	for name, ref := range map[string]string{"first": first.Ref, "second": second.Ref} {
		if _, status := store.lookup(ref); status != artifactContextSnapshotActive {
			t.Fatalf("%s active snapshot was evicted: %s", name, status)
		}
	}

	replacement, replacedRef, err := store.create(artifactContextSnapshot{ActorUID: 7, TopicID: "p2p_7_42", AgentUID: 42})
	if err != nil {
		t.Fatalf("replace snapshot at capacity: %v", err)
	}
	if replacement.Revision != 2 {
		t.Fatalf("replacement revision = %d, want 2", replacement.Revision)
	}
	if replacedRef != first.Ref {
		t.Fatalf("replacement reported previous ref %q, want %q", replacedRef, first.Ref)
	}
	for name, ref := range map[string]string{"replacement": replacement.Ref, "unrelated": second.Ref} {
		if _, status := store.lookup(ref); status != artifactContextSnapshotActive {
			t.Fatalf("%s active snapshot was lost after replacement: %s", name, status)
		}
	}
}

func TestArtifactContextSnapshotStoreConcurrentLifecycle(t *testing.T) {
	store := newArtifactContextSnapshotStore(time.Minute, time.Minute, 128)
	errs := make(chan error, 64)
	var workers sync.WaitGroup
	for index := 0; index < 64; index++ {
		index := index
		workers.Add(1)
		go func() {
			defer workers.Done()
			snapshot, _, err := store.create(artifactContextSnapshot{
				ActorUID: int64(index + 1),
				TopicID:  "topic-" + strconv.Itoa(index),
				AgentUID: int64(index + 1000),
			})
			if err != nil {
				errs <- err
				return
			}
			if _, status := store.lookup(snapshot.Ref); status != artifactContextSnapshotActive {
				errs <- fmt.Errorf("lookup status %s", status)
				return
			}
			if !store.invalidate(snapshot.Ref, snapshot.ActorUID) {
				errs <- errors.New("invalidate failed")
			}
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestArtifactContextConcurrentCreateInvalidatesEveryNonCurrentWriteback(t *testing.T) {
	snapshots := newArtifactContextSnapshotStore(time.Minute, time.Minute, 128)
	writebacks := newArtifactResultWritebackStore(time.Minute, time.Second, 128)
	const count = 32
	targets := make(chan artifactWritebackTarget, count)
	errs := make(chan error, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			snapshot, previousRef, err := snapshots.create(artifactContextSnapshot{
				ActorUID:         7,
				TopicID:          "p2p_7_440",
				AgentUID:         440,
				Artifact:         ArtifactContextRecord{ID: "lesson-game"},
				DisplayedVersion: 2,
				PreviewRoute: runtimeRoute{
					NodeID:       "preview-node",
					ConnectionID: fmt.Sprintf("preview-%02d", index),
				},
			})
			if err != nil {
				errs <- err
				return
			}
			target, issueErr := writebacks.issue(snapshot)
			if issueErr == nil {
				targets <- target
			}
			if previousRef != "" {
				writebacks.invalidateContext(previousRef)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(targets)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	currentRef := snapshots.currentRef(7, "p2p_7_440")
	if currentRef == "" {
		t.Fatal("concurrent creates left no current snapshot")
	}
	foundCurrent := false
	index := 0
	for target := range targets {
		request := artifactResultSubmitRequest{
			ContractVersion:  artifactResultContract,
			WritebackRef:     target.Ref,
			ArtifactID:       target.ArtifactID,
			DisplayedVersion: target.DisplayedVersion,
			SinkID:           "risk-items.upsert.v1",
			ResultID:         "arr_" + strings.Repeat("r", 39) + fmt.Sprintf("%04d", index),
			Payload:          json.RawMessage(`{"items":[]}`),
		}
		delivery, created, status := writebacks.startDelivery(
			request,
			target,
			hashArtifactResultRequest(request, target),
		)
		if target.ContextRef == currentRef {
			foundCurrent = true
			if delivery == nil || !created || status != "" {
				t.Fatalf("current target could not submit: delivery=%#v created=%v status=%q", delivery, created, status)
			}
		} else if delivery != nil || created || status != "expired" {
			t.Fatalf("stale target context=%s remained submittable: delivery=%#v created=%v status=%q", target.ContextRef, delivery, created, status)
		}
		index++
	}
	if !foundCurrent {
		t.Fatal("final current snapshot never received a writeback target")
	}
}

func TestArtifactContextSnapshotCreationRechecksP2PAccessAfterResolution(t *testing.T) {
	db := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 99},
		friendPairs: map[string]bool{agentPairKey(7, 440): true},
	}
	hub := NewHub(db, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, _ int64, artifactID string) (ArtifactContextRecord, error) {
		delete(db.friendPairs, agentPairKey(7, 440))
		return ArtifactContextRecord{ID: artifactID, Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"}, nil
	}))
	handler := NewArtifactContextSnapshotHandler(hub)
	req := httptest.NewRequest(http.MethodPost, "/api/artifact-context/snapshots", strings.NewReader(`{
		"topic_id":"p2p_7_440",
		"artifact_ref":{"contract_version":"catsco.artifact-ref.v1","id":"lesson-game","currently_visible":true}
	}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	recorder := httptest.NewRecorder()
	handler.HandleUserSnapshots(recorder, req)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"status":"mismatch"`) {
		t.Fatalf("revoked access create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestArtifactContextBotReadRejectsRevokedP2PAccess(t *testing.T) {
	db := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 99},
		friendPairs: map[string]bool{agentPairKey(7, 440): true},
	}
	hub := NewHub(db, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, _ int64, artifactID string) (ArtifactContextRecord, error) {
		return ArtifactContextRecord{ID: artifactID, Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"}, nil
	}))
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7, TopicID: "p2p_7_440", AgentUID: 440,
		Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	delete(db.friendPairs, agentPairKey(7, 440))

	request := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+snapshot.Ref, nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	recorder := httptest.NewRecorder()
	NewArtifactContextSnapshotHandler(hub).HandleBotRead(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"status":"mismatch"`) {
		t.Fatalf("revoked access read status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestArtifactContextSnapshotAndReadRequireCurrentGroupMembership(t *testing.T) {
	base := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{{GroupID: 80, UserID: 42, IsBot: true}},
	}
	db := &agentTaskGroupRoutingStore{
		identityMessageStore: base,
		group:                &types.Group{ID: 80, Kind: types.GroupKindAgentTask, AgentIDs: []int64{42}},
	}
	hub := NewHub(db, nil)
	resolverCalls := 0
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, _ int64, artifactID string) (ArtifactContextRecord, error) {
		resolverCalls++
		return ArtifactContextRecord{ID: artifactID, Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"}, nil
	}))
	handler := NewArtifactContextSnapshotHandler(hub)
	create := httptest.NewRequest(http.MethodPost, "/api/artifact-context/snapshots", strings.NewReader(`{
		"topic_id":"grp_80",
		"artifact_ref":{"contract_version":"catsco.artifact-ref.v1","id":"lesson-game","currently_visible":true}
	}`))
	create = create.WithContext(context.WithValue(create.Context(), uidKey, int64(7)))
	createRecorder := httptest.NewRecorder()
	handler.HandleUserSnapshots(createRecorder, create)
	if createRecorder.Code != http.StatusUnprocessableEntity || resolverCalls != 0 {
		t.Fatalf("non-member create status = %d calls = %d body = %s", createRecorder.Code, resolverCalls, createRecorder.Body.String())
	}

	base.groupMembers = []*types.GroupMember{
		{GroupID: 80, UserID: 7},
		{GroupID: 80, UserID: 42, IsBot: true},
	}
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7, TopicID: "grp_80", AgentUID: 42,
		Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	base.groupMembers = []*types.GroupMember{{GroupID: 80, UserID: 42, IsBot: true}}
	read := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+snapshot.Ref, nil)
	read = read.WithContext(context.WithValue(read.Context(), uidKey, int64(42)))
	readRecorder := httptest.NewRecorder()
	handler.HandleBotRead(readRecorder, read)
	if readRecorder.Code != http.StatusForbidden || resolverCalls != 0 || !strings.Contains(readRecorder.Body.String(), `"status":"mismatch"`) {
		t.Fatalf("removed member read status = %d calls = %d body = %s", readRecorder.Code, resolverCalls, readRecorder.Body.String())
	}
}

func TestArtifactContextRefAndBotReadRejectReplacedGroupAgent(t *testing.T) {
	base := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	db := &agentTaskGroupRoutingStore{
		identityMessageStore: base,
		group:                &types.Group{ID: 80, Kind: types.GroupKindAgentTask, AgentIDs: []int64{42}},
	}
	hub := NewHub(db, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, _ int64, artifactID string) (ArtifactContextRecord, error) {
		db.group.AgentIDs = []int64{43}
		base.groupMembers = []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 43, IsBot: true},
		}
		return ArtifactContextRecord{ID: artifactID, Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"}, nil
	}))
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7, TopicID: "grp_80", AgentUID: 42,
		Artifact: ArtifactContextRecord{ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/latest/"},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-context?context_ref="+snapshot.Ref, nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(42)))
	recorder := httptest.NewRecorder()
	NewArtifactContextSnapshotHandler(hub).HandleBotRead(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"status":"mismatch"`) {
		t.Fatalf("replaced Agent read status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	metadata, delivery := hub.extractArtifactContextDelivery(7, "grp_80", map[string]interface{}{artifactContextRefMetadataKey: snapshot.Ref})
	if delivery != nil || metadata != nil {
		t.Fatalf("replaced Agent received delivery: metadata=%#v delivery=%#v", metadata, delivery)
	}
	payload := &normalizedMessagePayload{
		StoredContent:      `"update"`,
		DisplayContent:     "update",
		StoredType:         "text",
		DisplayType:        "text",
		ArtifactContextRef: &artifactContextDeliveryRef{Ref: snapshot.Ref, AgentUID: 42},
	}
	message := hub.messageForRecipient(7, 42, "grp_80", 0, payload, 1)
	if _, exists := message.Data.Metadata[artifactContextRefMetadataKey]; exists {
		t.Fatalf("per-recipient revalidation leaked stale ref: %#v", message.Data.Metadata)
	}
}
