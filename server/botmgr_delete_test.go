package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type botDeleteTestStore struct {
	store.Store
	ownerUID    int64
	tenantName  string
	tenantError error
	deletedUIDs []int64
}

func (s *botDeleteTestStore) GetBotOwner(int64) (int64, error) {
	return s.ownerUID, nil
}

func (s *botDeleteTestStore) GetTenantName(int64) (string, error) {
	return s.tenantName, s.tenantError
}

func (s *botDeleteTestStore) DeleteBot(botUID int64) error {
	s.deletedUIDs = append(s.deletedUIDs, botUID)
	return nil
}

func runBotDeleteRequest(t *testing.T, db *botDeleteTestStore) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodDelete, "/api/bots?uid=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleDeleteBot(rec, req)
	return rec
}

func TestHandleDeleteBotAllowsSelfHostedBot(t *testing.T) {
	db := &botDeleteTestStore{ownerUID: 7}
	rec := runBotDeleteRequest(t, db)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if len(db.deletedUIDs) != 1 || db.deletedUIDs[0] != 42 {
		t.Fatalf("deletedUIDs=%v want [42]", db.deletedUIDs)
	}
}

func TestHandleDeleteBotRejectsCloudWorker(t *testing.T) {
	db := &botDeleteTestStore{ownerUID: 7, tenantName: "worker-bot-bot-a"}
	rec := runBotDeleteRequest(t, db)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if len(db.deletedUIDs) != 0 {
		t.Fatalf("cloud worker must not be deleted: %v", db.deletedUIDs)
	}
}

func TestHandleDeleteBotFailsClosedWhenHostingTypeUnavailable(t *testing.T) {
	db := &botDeleteTestStore{ownerUID: 7, tenantError: errors.New("database unavailable")}
	rec := runBotDeleteRequest(t, db)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
	if len(db.deletedUIDs) != 0 {
		t.Fatalf("bot must be kept when hosting type cannot be verified: %v", db.deletedUIDs)
	}
}
