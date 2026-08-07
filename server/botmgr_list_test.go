package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botListTestStore struct {
	store.Store
	ownerBots []map[string]interface{}
	friends   []*types.User
	owners    map[int64]int64
}

func (s *botListTestStore) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	return s.ownerBots, nil
}

func (s *botListTestStore) GetFriends(uid int64) ([]*types.User, error) {
	return s.friends, nil
}

func (s *botListTestStore) GetBotOwner(botUID int64) (int64, error) {
	if owner, ok := s.owners[botUID]; ok {
		return owner, nil
	}
	return 0, nil
}

func TestHandleListMyBotsIncludesFriendBotsReadOnly(t *testing.T) {
	db := &botListTestStore{
		ownerBots: []map[string]interface{}{
			{
				"id":           int64(42),
				"username":     "owned-agent",
				"display_name": "Owned Agent",
				"visibility":   "public",
			},
		},
		friends: []*types.User{
			{ID: 43, Username: "friend-agent", DisplayName: "Friend Agent", AccountType: types.AccountBot},
			{ID: 44, Username: "human", DisplayName: "Human Friend", AccountType: types.AccountHuman},
		},
		owners: map[int64]int64{42: 7, 43: 9},
	}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodGet, "/api/bots", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleListMyBots(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Bots []map[string]interface{} `json:"bots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Bots) != 2 {
		t.Fatalf("bot count=%d, want 2: %+v", len(body.Bots), body.Bots)
	}
	owned := body.Bots[0]
	friend := body.Bots[1]
	if owned["relation"] != "owner" || owned["is_owner"] != true || int64(owned["owner_id"].(float64)) != 7 {
		t.Fatalf("unexpected owner bot payload: %+v", owned)
	}
	if friend["relation"] != "friend" || friend["is_owner"] != false || int64(friend["owner_id"].(float64)) != 9 {
		t.Fatalf("unexpected friend bot payload: %+v", friend)
	}
	if _, ok := friend["api_key"]; ok {
		t.Fatalf("friend bot must not expose api_key: %+v", friend)
	}
	if _, ok := friend["api_endpoint"]; ok {
		t.Fatalf("friend bot must not expose api endpoint: %+v", friend)
	}
}

func TestHandleListMyBotsDeduplicatesOwnedBotFriendRelation(t *testing.T) {
	db := &botListTestStore{
		ownerBots: []map[string]interface{}{
			{
				"id":           int64(42),
				"username":     "owned-agent",
				"display_name": "Owned Agent",
			},
		},
		friends: []*types.User{
			{ID: 42, Username: "owned-agent", DisplayName: "Owned Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{42: 7},
	}
	handler := NewBotHandler(db)
	req := httptest.NewRequest(http.MethodGet, "/api/bots", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleListMyBots(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Bots []map[string]interface{} `json:"bots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Bots) != 1 {
		t.Fatalf("bot count=%d, want 1: %+v", len(body.Bots), body.Bots)
	}
	if body.Bots[0]["relation"] != "owner" || body.Bots[0]["is_owner"] != true {
		t.Fatalf("owned bot should keep owner relation: %+v", body.Bots[0])
	}
}

