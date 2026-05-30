package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type agentTestStore struct {
	store.Store
	ownerBots     []map[string]interface{}
	friends       []*types.User
	users         map[int64]*types.User
	owners        map[int64]int64
	friendPairs   map[string]bool
	createdTopics []string
}

func (s *agentTestStore) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	return s.ownerBots, nil
}

func (s *agentTestStore) GetFriends(uid int64) ([]*types.User, error) {
	return s.friends, nil
}

func (s *agentTestStore) GetUser(id int64) (*types.User, error) {
	if s.users == nil {
		return nil, errors.New("not found")
	}
	user := s.users[id]
	if user == nil {
		return nil, errors.New("not found")
	}
	return user, nil
}

func (s *agentTestStore) GetBotOwner(botUID int64) (int64, error) {
	if s.owners == nil {
		return 0, errors.New("not found")
	}
	owner, ok := s.owners[botUID]
	if !ok {
		return 0, errors.New("not found")
	}
	return owner, nil
}

func (s *agentTestStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.friendPairs[agentPairKey(uid1, uid2)], nil
}

func (s *agentTestStore) CreateTopic(id, topicType string, ownerID int64) error {
	s.createdTopics = append(s.createdTopics, id)
	return nil
}

func TestHandleListAgentsIncludesOwnedAndFriendBots(t *testing.T) {
	store := &agentTestStore{
		ownerBots: []map[string]interface{}{
			{
				"id":           int64(42),
				"username":     "dev-agent",
				"display_name": "Dev Agent",
				"visibility":   "private",
			},
		},
		friends: []*types.User{
			{ID: 43, Username: "review-agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
			{ID: 44, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
		},
	}
	hub := NewHub(nil, nil)
	hub.addClient(&Client{uid: 43, send: make(chan []byte, 1)})
	handler := NewAgentHandler(store, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleListAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Agents []AgentSummary `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Agents) != 2 {
		t.Fatalf("agent count=%d, want 2: %+v", len(body.Agents), body.Agents)
	}
	if body.Agents[0].UID != 42 || body.Agents[0].Relation != "owner" || body.Agents[0].TopicID != "p2p_7_42" {
		t.Fatalf("unexpected owned agent: %+v", body.Agents[0])
	}
	if body.Agents[1].UID != 43 || body.Agents[1].Relation != "friend" || !body.Agents[1].IsOnline {
		t.Fatalf("unexpected friend agent: %+v", body.Agents[1])
	}
}

func TestHandleOpenAgentCreatesP2PTopicForAccessibleAgent(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "review-agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{
			agentPairKey(7, 43): true,
		},
	}
	handler := NewAgentHandler(store, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/open", bytes.NewBufferString(`{"agent_uid":43}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleOpenAgent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.createdTopics) != 1 || store.createdTopics[0] != "p2p_7_43" {
		t.Fatalf("created topics = %#v, want p2p_7_43", store.createdTopics)
	}
}

func TestHandleOpenAgentRejectsUnavailableAgent(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "review-agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{},
	}
	handler := NewAgentHandler(store, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/open", bytes.NewBufferString(`{"agent_uid":43}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleOpenAgent(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.createdTopics) != 0 {
		t.Fatalf("created topics = %#v, want none", store.createdTopics)
	}
}

func agentPairKey(a, b int64) string {
	if a > b {
		a, b = b, a
	}
	return p2pTopicID(a, b)
}
