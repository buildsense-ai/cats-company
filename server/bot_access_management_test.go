package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botAccessManagementStore struct {
	store.Store
	users         map[int64]*types.User
	owners        map[int64]int64
	configs       map[int64]*types.BotConfig
	configErr     error
	friendPairs   map[string]bool
	friends       []*types.User
	createdFriend bool
	removedA      int64
	removedB      int64
}

func (s *botAccessManagementStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
}

func (s *botAccessManagementStore) GetBotOwner(botUID int64) (int64, error) {
	return s.owners[botUID], nil
}

func (s *botAccessManagementStore) GetBotConfig(uid int64) (*types.BotConfig, error) {
	if s.configErr != nil {
		return nil, s.configErr
	}
	if config := s.configs[uid]; config != nil {
		return config, nil
	}
	return &types.BotConfig{UserID: uid, Visibility: types.BotPublic}, nil
}

func (s *botAccessManagementStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.friendPairs[agentPairKey(uid1, uid2)], nil
}

func (s *botAccessManagementStore) IsBlocked(uid, blockedUID int64) (bool, error) {
	return false, nil
}

func (s *botAccessManagementStore) CreateFriendRequest(fromUID, toUID int64, message string) (int64, error) {
	s.createdFriend = true
	return 1, nil
}

func (s *botAccessManagementStore) GetFriends(uid int64) ([]*types.User, error) {
	return s.friends, nil
}

func (s *botAccessManagementStore) RemoveFriend(uid1, uid2 int64) error {
	s.removedA = uid1
	s.removedB = uid2
	return nil
}

func TestPrivateBotRejectsNewFriendRequest(t *testing.T) {
	db := &botAccessManagementStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "private-agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{43: 99},
		configs: map[int64]*types.BotConfig{
			43: {UserID: 43, OwnerID: 99, Visibility: types.BotPrivate},
		},
		friendPairs: map[string]bool{},
	}
	handler := NewFriendHandler(db)
	req := httptest.NewRequest(http.MethodPost, "/api/friends/request", strings.NewReader(`{"user_id":43}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleSendRequest(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if db.createdFriend {
		t.Fatal("private bot request should not create a friend request")
	}
	if !strings.Contains(rec.Body.String(), "agent is private") {
		t.Fatalf("body=%s, want private error", rec.Body.String())
	}
}

func TestBotConfigErrorRejectsFriendRequest(t *testing.T) {
	db := &botAccessManagementStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "agent", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{43: 99},
		configErr:   errors.New("db unavailable"),
		friendPairs: map[string]bool{},
	}
	handler := NewFriendHandler(db)
	req := httptest.NewRequest(http.MethodPost, "/api/friends/request", strings.NewReader(`{"user_id":43}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleSendRequest(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
	if db.createdFriend {
		t.Fatal("bot config error should not create a friend request")
	}
}

func TestOwnerCanRemoveBotFriend(t *testing.T) {
	db := &botAccessManagementStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "agent", AccountType: types.AccountBot},
			99: {ID: 99, Username: "owner", AccountType: types.AccountHuman},
		},
		owners:      map[int64]int64{43: 99},
		configs:     map[int64]*types.BotConfig{},
		friendPairs: map[string]bool{agentPairKey(7, 43): true},
	}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodDelete, "/api/bots/friends?uid=43&user_id=7", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(99)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotFriends(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if db.removedA != 43 || db.removedB != 7 {
		t.Fatalf("removed=(%d,%d), want bot/user pair (43,7)", db.removedA, db.removedB)
	}
}

func TestOwnerCannotRemoveOwnBotAccess(t *testing.T) {
	db := &botAccessManagementStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "agent", AccountType: types.AccountBot},
			99: {ID: 99, Username: "owner", AccountType: types.AccountHuman},
		},
		owners: map[int64]int64{43: 99},
	}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodDelete, "/api/bots/friends?uid=43&user_id=99", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(99)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotFriends(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if db.removedA != 0 || db.removedB != 0 {
		t.Fatalf("owner self removal should not call RemoveFriend, got (%d,%d)", db.removedA, db.removedB)
	}
}

func TestGetBotFriendsFiltersOwner(t *testing.T) {
	db := &botAccessManagementStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "agent", AccountType: types.AccountBot},
			99: {ID: 99, Username: "owner", AccountType: types.AccountHuman},
		},
		owners: map[int64]int64{43: 99},
		friends: []*types.User{
			{ID: 99, Username: "owner", AccountType: types.AccountHuman},
			{ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
		},
	}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodGet, "/api/bots/friends?uid=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(99)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotFriends(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Friends []*types.User `json:"friends"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Friends) != 1 || body.Friends[0].ID != 7 {
		t.Fatalf("friends=%+v, want only non-owner user 7", body.Friends)
	}
}

