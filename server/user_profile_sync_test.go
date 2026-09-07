package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type userProfileSyncTestStore struct {
	store.Store
	user *types.User
}

func (s *userProfileSyncTestStore) UpdateUser(uid int64, displayName, avatarURL string) error {
	if s.user == nil || s.user.ID != uid {
		return errors.New("user not found")
	}
	s.user.DisplayName = displayName
	s.user.AvatarURL = avatarURL
	return nil
}

func (s *userProfileSyncTestStore) GetUser(uid int64) (*types.User, error) {
	if s.user == nil || s.user.ID != uid {
		return nil, nil
	}
	copyUser := *s.user
	return &copyUser, nil
}

func TestHandleUpdateMeSynchronizesSkillHubPublisherAfterCatsCoCommit(t *testing.T) {
	db := &userProfileSyncTestStore{user: &types.User{ID: 85, Username: "arrowhaken"}}
	handler := NewUserHandler(db)
	var syncedToken string
	var syncedDisplayName string
	handler.SetSkillHubProfileSync(func(_ context.Context, token string) error {
		syncedToken = token
		syncedDisplayName = db.user.DisplayName
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/me/update", bytes.NewBufferString(
		`{"display_name":"arrowhaken","avatar_url":"https://example.test/avatar.png"}`,
	))
	req.Header.Set("Authorization", "Bearer current-user-jwt")
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(85)))
	rec := httptest.NewRecorder()

	handler.HandleUpdateMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if syncedToken != "current-user-jwt" || syncedDisplayName != "arrowhaken" {
		t.Fatalf("token=%q displayName=%q", syncedToken, syncedDisplayName)
	}
}

func TestHandleUpdateMeDoesNotRollBackWhenSkillHubSyncFails(t *testing.T) {
	db := &userProfileSyncTestStore{user: &types.User{ID: 85, Username: "arrowhaken", DisplayName: "old"}}
	handler := NewUserHandler(db)
	handler.SetSkillHubProfileSync(func(context.Context, string) error {
		return errors.New("temporary failure")
	})
	req := httptest.NewRequest(http.MethodPost, "/api/me/update", bytes.NewBufferString(
		`{"display_name":"new name","avatar_url":""}`,
	))
	req.Header.Set("Authorization", "Bearer current-user-jwt")
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(85)))
	rec := httptest.NewRecorder()

	handler.HandleUpdateMe(rec, req)

	if rec.Code != http.StatusOK || db.user.DisplayName != "new name" {
		t.Fatalf("status=%d displayName=%q body=%s", rec.Code, db.user.DisplayName, rec.Body.String())
	}
}
