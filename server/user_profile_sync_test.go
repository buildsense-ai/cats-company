package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	synced := make(chan struct {
		token       string
		displayName string
	}, 1)
	handler.SetSkillHubProfileSync(func(_ context.Context, token string) error {
		synced <- struct {
			token       string
			displayName string
		}{token: token, displayName: db.user.DisplayName}
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
	select {
	case got := <-synced:
		if got.token != "current-user-jwt" || got.displayName != "arrowhaken" {
			t.Fatalf("token=%q displayName=%q", got.token, got.displayName)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for publisher profile sync")
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

func TestHandleUpdateMeDoesNotWaitForStalledSkillHubSync(t *testing.T) {
	db := &userProfileSyncTestStore{user: &types.User{ID: 85, Username: "arrowhaken", DisplayName: "old"}}
	handler := NewUserHandler(db)
	started := make(chan struct{})
	release := make(chan struct{})
	handler.SetSkillHubProfileSync(func(context.Context, string) error {
		close(started)
		<-release
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/me/update", bytes.NewBufferString(
		`{"display_name":"new name","avatar_url":""}`,
	))
	req.Header.Set("Authorization", "Bearer current-user-jwt")
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(85)))
	rec := httptest.NewRecorder()

	returned := make(chan struct{})
	go func() {
		handler.HandleUpdateMe(rec, req)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("HandleUpdateMe waited for stalled SkillHub sync")
	}
	if rec.Code != http.StatusOK || db.user.DisplayName != "new name" {
		close(release)
		t.Fatalf("status=%d displayName=%q body=%s", rec.Code, db.user.DisplayName, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("background publisher profile sync did not start")
	}
	close(release)
}
