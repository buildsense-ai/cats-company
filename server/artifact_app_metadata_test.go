package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type appMetadataTestStore struct {
	*agentTestStore
	items    map[string]store.ArtifactAppMetadata
	messages []*types.Message
	fail     bool
}

func newAppMetadataTestStore() *appMetadataTestStore {
	return &appMetadataTestStore{
		agentTestStore: &agentTestStore{
			users:       map[int64]*types.User{7: {ID: 7}, 8: {ID: 8}, 9: {ID: 9}, 42: {ID: 42, AccountType: types.AccountBot}},
			owners:      map[int64]int64{42: 7},
			friendPairs: map[string]bool{agentPairKey(8, 42): true, agentPairKey(9, 42): true},
		},
		items: map[string]store.ArtifactAppMetadata{},
	}
}
func (s *appMetadataTestStore) ListArtifactAppMetadata(_ context.Context, agent int64) (map[string]store.ArtifactAppMetadata, error) {
	if s.fail {
		return nil, errors.New("offline")
	}
	result := map[string]store.ArtifactAppMetadata{}
	for id, item := range s.items {
		if item.AgentUID == agent {
			result[id] = item
		}
	}
	return result, nil
}
func (s *appMetadataTestStore) EnsureArtifactAppMetadata(_ context.Context, item store.ArtifactAppMetadata) error {
	if _, exists := s.items[item.AppID]; !exists {
		s.items[item.AppID] = item
	}
	return nil
}
func (s *appMetadataTestStore) UpdateArtifactAppMetadata(_ context.Context, item store.ArtifactAppMetadata) error {
	item.CreatorUID = s.items[item.AppID].CreatorUID
	s.items[item.AppID] = item
	return nil
}

func (s *appMetadataTestStore) DeleteArtifactAppMetadata(_ context.Context, uid int64, id string) error {
	if s.items[id].AgentUID == uid {
		delete(s.items, id)
	}
	return nil
}
func (s *appMetadataTestStore) GetMessagesSince(topic string, since int64, limit int) ([]*types.Message, error) {
	for _, msg := range s.messages {
		if msg.TopicID == topic && msg.ID > since {
			return []*types.Message{msg}, nil
		}
	}
	return nil, nil
}

func TestArtifactAppMetadataPermissionsAndSharedRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		uid     int64
		allowed bool
	}{
		{"agent owner", 7, true}, {"creator", 8, true}, {"friend", 9, false}, {"publishing bot", 42, false}, {"stranger", 99, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := newArtifactAppsGateway(t)
			gateway.setApps(artifactApp{ID: "board", Agent: "42", Title: "Original"})
			db := newAppMetadataTestStore()
			db.items["board"] = store.ArtifactAppMetadata{AppID: "board", AgentUID: 42, CreatorUID: 8, Title: "Original"}
			handler := gateway.handler()
			handler.SetStore(db)
			response := httptest.NewRecorder()
			handler.HandleApps(response, artifactAppsRequest(tc.uid, http.MethodPatch, "/api/artifacts/apps/board", `{"title":"Shared title","description":"Shared description","icon_url":"/uploads/app.png","creator_uid":99,"agent":"99","publicKey":"stolen"}`))
			want := http.StatusForbidden
			if tc.allowed {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if db.items["board"].CreatorUID != 8 {
				t.Fatal("metadata edit reassigned creator")
			}
			for _, call := range gateway.recorded() {
				if call.method != http.MethodGet {
					t.Fatal("presentation edit mutated tunnel")
				}
			}
			if !tc.allowed {
				return
			}
			// A different friend sees the same metadata, with no management permission.
			other := httptest.NewRecorder()
			handler.HandleApps(other, artifactAppsRequest(9, http.MethodGet, "/api/artifacts/apps?agent=42", ""))
			var result struct {
				Apps []artifactApp `json:"apps"`
			}
			json.Unmarshal(other.Body.Bytes(), &result)
			if other.Code != 200 || len(result.Apps) != 1 {
				t.Fatalf("shared list: %s", other.Body.String())
			}
			app := result.Apps[0]
			if app.Title != "Shared title" || app.Description != "Shared description" || app.IconURL != "/uploads/app.png" || app.CanManage {
				t.Fatalf("unexpected shared view: %+v", app)
			}
		})
	}
}

func TestArtifactAppContentRepublishPreservesMetadataAndCreator(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	db := newAppMetadataTestStore()
	handler.SetStore(db)
	db.messages = []*types.Message{{ID: 12, TopicID: "p2p_8_42", FromUID: 8}}
	publish := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.HandleApps(r, artifactAppsRequest(42, http.MethodPost, "/api/artifacts/apps", body))
		return r
	}
	first := publish(`{"id":"board","title":"Original","publicKey":"key","source_topic_id":"p2p_8_42","source_message_id":12,"creator_uid":99}`)
	if first.Code != 201 || db.items["board"].CreatorUID != 8 {
		t.Fatalf("first publish: %s %+v", first.Body.String(), db.items)
	}
	gateway.setApps(artifactApp{ID: "board", Agent: "42", Title: "Original"})
	edited := httptest.NewRecorder()
	handler.HandleApps(edited, artifactAppsRequest(7, http.MethodPatch, "/api/artifacts/apps/board", `{"title":"Edited","description":"Keep me","icon_url":"https://example.com/icon.png"}`))
	if edited.Code != 200 {
		t.Fatal(edited.Body.String())
	}
	retry := publish(`{"id":"board","title":"Generated replacement","creator_uid":7}`)
	if retry.Code != 201 {
		t.Fatal(retry.Body.String())
	}
	var app artifactApp
	json.Unmarshal(retry.Body.Bytes(), &app)
	if app.Title != "Edited" || app.Description != "Keep me" || app.IconURL != "https://example.com/icon.png" || app.CreatorUID != 8 {
		t.Fatalf("republish lost metadata: %+v", app)
	}
}

func TestArtifactAppRejectsUnprovenCreatorBeforePublishing(t *testing.T) {
	for _, tc := range []struct {
		name, topic     string
		messageID, from int64
	}{
		{"no source", "", 0, 8}, {"unrelated topic", "p2p_7_8", 12, 8}, {"missing message", "p2p_8_42", 13, 8}, {"bot source", "p2p_8_42", 12, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := newArtifactAppsGateway(t)
			handler := gateway.handler()
			db := newAppMetadataTestStore()
			handler.SetStore(db)
			db.messages = []*types.Message{{ID: 12, TopicID: "p2p_8_42", FromUID: tc.from}}
			body, _ := json.Marshal(map[string]any{"id": "board", "title": "title", "source_topic_id": tc.topic, "source_message_id": tc.messageID})
			r := httptest.NewRecorder()
			handler.HandleApps(r, artifactAppsRequest(42, http.MethodPost, "/api/artifacts/apps", string(body)))
			if r.Code != 400 {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
			if len(db.items) != 0 {
				t.Fatal("invalid creator was persisted")
			}
			for _, call := range gateway.recorded() {
				if call.method != http.MethodGet {
					t.Fatal("invalid source published")
				}
			}
		})
	}
}

func TestArtifactAppLegacyEditDoesNotInventCreator(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "legacy", Agent: "42", Title: "Legacy"})
	handler := gateway.handler()
	db := newAppMetadataTestStore()
	handler.SetStore(db)
	r := httptest.NewRecorder()
	handler.HandleApps(r, artifactAppsRequest(7, http.MethodPatch, "/api/artifacts/apps/legacy", `{"title":"Renamed"}`))
	if r.Code != 200 || db.items["legacy"].CreatorUID != 0 {
		t.Fatalf("legacy edit: %s %+v", r.Body.String(), db.items)
	}
	db.fail = true
	failed := httptest.NewRecorder()
	handler.HandleApps(failed, artifactAppsRequest(7, http.MethodGet, "/api/artifacts/apps?agent=42", ""))
	if failed.Code != 503 {
		t.Fatal("metadata outage silently returned stale gateway metadata")
	}
}

func TestArtifactAppRejectsInvalidMetadata(t *testing.T) {
	for _, body := range []string{`{"title":""}`, `{"title":"ok","icon_url":"javascript:alert(1)"}`, `{"title":"ok","icon_url":"//evil.example/icon"}`, `{"title":"ok","icon_url":"/uploads/../api/me"}`} {
		gateway := newArtifactAppsGateway(t)
		gateway.setApps(artifactApp{ID: "board", Agent: "42", Title: "Before"})
		handler := gateway.handler()
		handler.SetStore(newAppMetadataTestStore())
		r := httptest.NewRecorder()
		handler.HandleApps(r, artifactAppsRequest(7, http.MethodPatch, "/api/artifacts/apps/board", body))
		if r.Code != 400 {
			t.Fatalf("accepted %s: %d", body, r.Code)
		}
	}
}

func TestArtifactAppDirectHumanPublicationAndLegacyDeleteCleanup(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	db := newAppMetadataTestStore()
	handler.SetStore(db)
	r := httptest.NewRecorder()
	handler.HandleApps(r, artifactAppsRequest(8, http.MethodPost, "/api/artifacts/apps", `{"id":"human-app","title":"Mine","creator_uid":7}`))
	if r.Code != http.StatusCreated || db.items["human-app"].CreatorUID != 8 {
		t.Fatalf("human publish: %s", r.Body.String())
	}
	var app artifactApp
	json.Unmarshal(r.Body.Bytes(), &app)
	if !app.CanManage {
		t.Fatal("human creator cannot manage own app")
	}
	gateway.setApps(artifactApp{ID: "human-app", Agent: "8", Title: "Mine"})
	r = httptest.NewRecorder()
	handler.HandleApps(r, artifactAppsRequest(8, http.MethodDelete, "/api/artifacts/apps/human-app", ""))
	if r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	if _, exists := db.items["human-app"]; exists {
		t.Fatal("deleted application's provenance would leak into a new app with the same ID")
	}
}
