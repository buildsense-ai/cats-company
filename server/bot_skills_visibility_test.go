package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type botSkillsVisibilityTestStore struct {
	store.Store
	ownerUID   int64
	visibility string
}

func (s *botSkillsVisibilityTestStore) GetBotOwner(int64) (int64, error) {
	return s.ownerUID, nil
}

func (s *botSkillsVisibilityTestStore) SetBotSkillsVisibility(_ int64, visibility string) error {
	s.visibility = visibility
	return nil
}

func TestSetBotSkillsVisibility(t *testing.T) {
	db := &botSkillsVisibilityTestStore{ownerUID: 7}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodPatch, "/api/bots/skills-visibility?uid=43&v=authorized", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleSetBotSkillsVisibility(rec, req)

	if rec.Code != http.StatusOK || db.visibility != "authorized" {
		t.Fatalf("status=%d visibility=%q body=%s", rec.Code, db.visibility, rec.Body.String())
	}
}

func TestSetBotSkillsVisibilityRejectsInvalidValueAndNonOwner(t *testing.T) {
	tests := []struct {
		name       string
		ownerUID   int64
		query      string
		wantStatus int
	}{
		{name: "invalid value", ownerUID: 7, query: "uid=43&v=friends", wantStatus: http.StatusBadRequest},
		{name: "non owner", ownerUID: 9, query: "uid=43&v=public", wantStatus: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &botSkillsVisibilityTestStore{ownerUID: tc.ownerUID}
			handler := NewBotHandler(db)
			req := httptest.NewRequest(http.MethodPatch, "/api/bots/skills-visibility?"+tc.query, nil)
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
			rec := httptest.NewRecorder()
			handler.HandleSetBotSkillsVisibility(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
