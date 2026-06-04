package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type handlerCoverageStore struct {
	store.Store

	nextUID     int64
	nextGroupID int64

	users     map[int64]*types.User
	usernames map[string]*types.User

	botOwners      map[int64]int64
	botAPIKeys     map[int64]string
	botTenantNames map[int64]string
	deletedBots    []int64

	alreadyFriends bool
	blockedPairs   map[[2]int64]bool
	friendRequests []friendRequestCall

	botUsers     map[int64]bool
	groups       map[int64]*types.Group
	groupMembers map[int64][]*types.GroupMember
	addedMembers []groupMemberCall
}

type friendRequestCall struct {
	fromUID int64
	toUID   int64
	message string
}

type groupMemberCall struct {
	groupID int64
	userID  int64
	role    string
}

func newHandlerCoverageStore() *handlerCoverageStore {
	return &handlerCoverageStore{
		nextUID:        100,
		nextGroupID:    500,
		users:          map[int64]*types.User{},
		usernames:      map[string]*types.User{},
		botOwners:      map[int64]int64{},
		botAPIKeys:     map[int64]string{},
		botTenantNames: map[int64]string{},
		blockedPairs:   map[[2]int64]bool{},
		botUsers:       map[int64]bool{},
		groups:         map[int64]*types.Group{},
		groupMembers:   map[int64][]*types.GroupMember{},
	}
}

func requestWithUID(method, target, body string, uid int64) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), uidKey, uid))
}

func decodeHandlerJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return body
}

func (s *handlerCoverageStore) GetUserByUsername(username string) (*types.User, error) {
	return s.usernames[strings.ToLower(username)], nil
}

func (s *handlerCoverageStore) CreateUser(user *types.User) (int64, error) {
	s.nextUID++
	copyUser := *user
	copyUser.ID = s.nextUID
	if copyUser.DisplayName == "" {
		copyUser.DisplayName = copyUser.Username
	}
	s.users[copyUser.ID] = &copyUser
	s.usernames[strings.ToLower(copyUser.Username)] = &copyUser
	return copyUser.ID, nil
}

func (s *handlerCoverageStore) SaveBotConfigWithOwner(uid, ownerID int64, apiEndpoint, model string) error {
	s.botOwners[uid] = ownerID
	return nil
}

func (s *handlerCoverageStore) SaveAPIKey(uid int64, apiKey string) error {
	s.botAPIKeys[uid] = apiKey
	return nil
}

func (s *handlerCoverageStore) GetBotOwner(botUID int64) (int64, error) {
	owner, ok := s.botOwners[botUID]
	if !ok {
		return 0, errors.New("bot not found")
	}
	return owner, nil
}

func (s *handlerCoverageStore) GetTenantName(botUID int64) (string, error) {
	return s.botTenantNames[botUID], nil
}

func (s *handlerCoverageStore) GetBotAPIKey(botUID int64) (string, error) {
	return s.botAPIKeys[botUID], nil
}

func (s *handlerCoverageStore) DeleteBot(botUID int64) error {
	s.deletedBots = append(s.deletedBots, botUID)
	if user := s.users[botUID]; user != nil {
		user.State = 1
	}
	return nil
}

func (s *handlerCoverageStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.alreadyFriends, nil
}

func (s *handlerCoverageStore) IsBlocked(uid, blockedUID int64) (bool, error) {
	return s.blockedPairs[[2]int64{uid, blockedUID}], nil
}

func (s *handlerCoverageStore) CreateFriendRequest(fromUID, toUID int64, message string) (int64, error) {
	s.friendRequests = append(s.friendRequests, friendRequestCall{fromUID: fromUID, toUID: toUID, message: message})
	return int64(len(s.friendRequests)), nil
}

func (s *handlerCoverageStore) IsUserBot(userID int64) (bool, error) {
	return s.botUsers[userID], nil
}

func (s *handlerCoverageStore) CreateGroup(name string, ownerID int64) (int64, error) {
	s.nextGroupID++
	group := &types.Group{
		ID:         s.nextGroupID,
		Name:       name,
		OwnerID:    ownerID,
		MaxMembers: 200,
		CreatedAt:  time.Now(),
	}
	s.groups[group.ID] = group
	s.groupMembers[group.ID] = append(s.groupMembers[group.ID], &types.GroupMember{
		GroupID:  group.ID,
		UserID:   ownerID,
		Role:     "owner",
		JoinedAt: time.Now(),
	})
	return group.ID, nil
}

func (s *handlerCoverageStore) GetGroup(groupID int64) (*types.Group, error) {
	group, ok := s.groups[groupID]
	if !ok {
		return nil, errors.New("group not found")
	}
	return group, nil
}

func (s *handlerCoverageStore) AddGroupMember(groupID, userID int64, role string) error {
	s.addedMembers = append(s.addedMembers, groupMemberCall{groupID: groupID, userID: userID, role: role})
	s.groupMembers[groupID] = append(s.groupMembers[groupID], &types.GroupMember{
		GroupID:  groupID,
		UserID:   userID,
		Role:     role,
		JoinedAt: time.Now(),
	})
	return nil
}

func (s *handlerCoverageStore) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	return s.groupMembers[groupID], nil
}

func TestHandleCreateBotSavesOwnerAndAPIKey(t *testing.T) {
	db := newHandlerCoverageStore()
	handler := NewBotHandler(db, nil)

	req := requestWithUID(http.MethodPost, "/api/bots", `{"username":"helper","display_name":""}`, 7)
	rec := httptest.NewRecorder()
	handler.HandleCreateBot(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeHandlerJSON(t, rec)
	botUID := int64(body["uid"].(float64))
	if db.botOwners[botUID] != 7 {
		t.Fatalf("bot owner=%d, want 7", db.botOwners[botUID])
	}
	if db.users[botUID].DisplayName != "helper" {
		t.Fatalf("display name=%q, want helper", db.users[botUID].DisplayName)
	}
	if db.botAPIKeys[botUID] == "" {
		t.Fatal("expected generated bot api key to be stored")
	}
}

func TestHandleDeleteBotRequiresOwnerBeforeDeleting(t *testing.T) {
	db := newHandlerCoverageStore()
	db.botOwners[42] = 99
	handler := NewBotHandler(db, nil)

	req := requestWithUID(http.MethodDelete, "/api/bots?uid=42", "", 7)
	rec := httptest.NewRecorder()
	handler.HandleDeleteBot(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.deletedBots) != 0 {
		t.Fatalf("deleted bot despite owner mismatch: %+v", db.deletedBots)
	}
}

func TestHandleSendRequestRejectsBlockedRequester(t *testing.T) {
	db := newHandlerCoverageStore()
	db.blockedPairs[[2]int64{8, 7}] = true
	handler := NewFriendHandler(db)

	req := requestWithUID(http.MethodPost, "/api/friends/request", `{"user_id":8,"message":"hi"}`, 7)
	rec := httptest.NewRecorder()
	handler.HandleSendRequest(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.friendRequests) != 0 {
		t.Fatalf("created friend request despite block: %+v", db.friendRequests)
	}
}

func TestHandleCreateGroupAddsInitialMembers(t *testing.T) {
	db := newHandlerCoverageStore()
	handler := NewGroupHandler(db, NewHub(nil, nil))

	req := requestWithUID(http.MethodPost, "/api/groups/create", `{"name":"Launch","member_ids":[8,9]}`, 7)
	rec := httptest.NewRecorder()
	handler.HandleCreateGroup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeHandlerJSON(t, rec)
	groupID := int64(body["group_id"].(float64))
	if body["topic"] != "grp_501" || groupID != 501 {
		t.Fatalf("unexpected group response: %+v", body)
	}
	if len(db.addedMembers) != 2 {
		t.Fatalf("added members=%+v, want two invited members", db.addedMembers)
	}
	if len(db.groupMembers[groupID]) != 3 {
		t.Fatalf("group members=%+v, want owner plus two invited members", db.groupMembers[groupID])
	}
}

func TestHandleCreateGroupRejectsTooManyBotsBeforeCreate(t *testing.T) {
	db := newHandlerCoverageStore()
	for uid := int64(20); uid <= 30; uid++ {
		db.botUsers[uid] = true
	}
	handler := NewGroupHandler(db, NewHub(nil, nil))

	req := requestWithUID(http.MethodPost, "/api/groups/create", `{"name":"Bot Room","member_ids":[20,21,22,23,24,25,26,27,28,29,30]}`, 7)
	rec := httptest.NewRecorder()
	handler.HandleCreateGroup(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(db.groups) != 0 {
		t.Fatalf("created group despite bot limit: %+v", db.groups)
	}
}
