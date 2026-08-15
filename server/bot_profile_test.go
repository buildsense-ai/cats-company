package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type botProfileTestStore struct {
	store.Store
	ownerID               int64
	role                  *string
	description           *string
	artifactUploadEnabled *bool
}

func (s *botProfileTestStore) GetBotArtifactUploadPolicy(int64) (bool, error) {
	return s.artifactUploadEnabled == nil || *s.artifactUploadEnabled, nil
}

func (s *botProfileTestStore) UpdateBotArtifactUploadPolicy(_ int64, enabled bool) error {
	s.artifactUploadEnabled = &enabled
	return nil
}

func (s *botProfileTestStore) GetBotOwner(int64) (int64, error) {
	return s.ownerID, nil
}

func TestHandleUpdateBotPersistsArtifactUploadPolicy(t *testing.T) {
	db := &botProfileTestStore{ownerID: 7}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/bots?uid=42",
		strings.NewReader(`{"artifact_upload_enabled":false}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleUpdateBot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.artifactUploadEnabled == nil || *db.artifactUploadEnabled {
		t.Fatalf("artifact upload policy=%v, want false", db.artifactUploadEnabled)
	}
}

func (s *botProfileTestStore) UpdateBotProfile(_ int64, role, description *string) error {
	s.role = role
	s.description = description
	return nil
}

func TestHandleUpdateBotPersistsAssistantProfile(t *testing.T) {
	db := &botProfileTestStore{ownerID: 7}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/bots?uid=42",
		strings.NewReader(`{"role":"research","description":"  检索资料并形成结论。  "}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleUpdateBot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.role == nil || *db.role != "research" {
		t.Fatalf("role=%v, want research", db.role)
	}
	if db.description == nil || *db.description != "检索资料并形成结论。" {
		t.Fatalf("description=%v, want trimmed profile description", db.description)
	}
}

func TestHandleUpdateBotRejectsUnknownAssistantRole(t *testing.T) {
	db := &botProfileTestStore{ownerID: 7}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodPatch, "/api/bots?uid=42", strings.NewReader(`{"role":"unknown"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleUpdateBot(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.role != nil || db.description != nil {
		t.Fatalf("invalid profile must not be persisted: role=%v description=%v", db.role, db.description)
	}
}
