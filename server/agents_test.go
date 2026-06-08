package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	groupMembers  map[string]bool
	groupMuted    map[string]bool
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

func (s *agentTestStore) IsGroupMember(groupID, userID int64) (bool, error) {
	return s.groupMembers[groupMemberKey(groupID, userID)], nil
}

func (s *agentTestStore) IsMemberMuted(groupID, userID int64) (bool, error) {
	return s.groupMuted[groupMemberKey(groupID, userID)], nil
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
	if _, err := hub.bodyLeases.acquire(43, "body-review", "conn-review"); err != nil {
		t.Fatalf("acquire bot body lease: %v", err)
	}
	hub.addRegisteredClient(&Client{
		uid:          43,
		accountType:  types.AccountBot,
		bodyID:       "body-review",
		connectionID: "conn-review",
		send:         make(chan []byte, 1),
	})
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
	if body.Agents[0].IsOnline {
		t.Fatalf("owned agent without active body must be offline: %+v", body.Agents[0])
	}
	if body.Agents[1].UID != 43 || body.Agents[1].Relation != "friend" || !body.Agents[1].IsOnline {
		t.Fatalf("unexpected friend agent: %+v", body.Agents[1])
	}
}

func TestHandleListAgentsDoesNotTreatGenericBotUIDConnectionAsRuntimeOnline(t *testing.T) {
	store := &agentTestStore{
		ownerBots: []map[string]interface{}{
			{
				"id":           int64(42),
				"username":     "dev-agent",
				"display_name": "Dev Agent",
			},
		},
	}
	hub := NewHub(nil, nil)
	hub.addClient(&Client{uid: 42, send: make(chan []byte, 1)})
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
	if len(body.Agents) != 1 {
		t.Fatalf("agent count=%d, want 1: %+v", len(body.Agents), body.Agents)
	}
	if body.Agents[0].IsOnline {
		t.Fatalf("agent without active body lease must be offline: %+v", body.Agents[0])
	}
}

func TestBuildOnlineStatusListUsesBotBodyLeaseForBots(t *testing.T) {
	store := &agentTestStore{
		ownerBots: []map[string]interface{}{
			{"id": int64(42), "username": "owner-agent"},
		},
		friends: []*types.User{
			{ID: 43, Username: "friend-agent", AccountType: types.AccountBot},
			{ID: 44, Username: "human-friend", AccountType: types.AccountHuman},
		},
	}
	hub := NewHub(nil, nil)
	hub.addClient(&Client{uid: 42, send: make(chan []byte, 1)})
	hub.addClient(&Client{uid: 43, send: make(chan []byte, 1)})
	hub.addClient(&Client{uid: 44, send: make(chan []byte, 1)})
	if _, err := hub.bodyLeases.acquire(43, "body-friend", "conn-friend"); err != nil {
		t.Fatalf("acquire friend bot body lease: %v", err)
	}
	hub.addRegisteredClient(&Client{
		uid:          43,
		accountType:  types.AccountBot,
		bodyID:       "body-friend",
		connectionID: "conn-friend",
		send:         make(chan []byte, 1),
	})

	list, err := BuildOnlineStatusList(store, hub, 7)
	if err != nil {
		t.Fatalf("BuildOnlineStatusList error: %v", err)
	}
	onlineByUID := make(map[int64]bool)
	for _, item := range list {
		uid, _ := item["uid"].(int64)
		online, _ := item["online"].(bool)
		onlineByUID[uid] = online
	}

	if onlineByUID[42] {
		t.Fatalf("owned bot without body lease must be offline: %#v", onlineByUID)
	}
	if !onlineByUID[43] {
		t.Fatalf("friend bot with body lease must be online: %#v", onlineByUID)
	}
	if !onlineByUID[44] {
		t.Fatalf("human friend should still use generic online status: %#v", onlineByUID)
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

func TestHandleOpenAgentKeepsDifferentActorsOnDistinctTopics(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "school-agent", DisplayName: "School Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{
			agentPairKey(7, 43): true,
			agentPairKey(8, 43): true,
		},
	}
	handler := NewAgentHandler(store, nil)

	for _, actorUID := range []int64{7, 8} {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/open", bytes.NewBufferString(`{"agent_uid":43}`))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, actorUID))
		rec := httptest.NewRecorder()
		handler.HandleOpenAgent(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("actor %d status=%d body=%s", actorUID, rec.Code, rec.Body.String())
		}
	}

	want := []string{"p2p_7_43", "p2p_8_43"}
	if len(store.createdTopics) != len(want) {
		t.Fatalf("created topics = %#v, want %#v", store.createdTopics, want)
	}
	for i := range want {
		if store.createdTopics[i] != want[i] {
			t.Fatalf("created topics = %#v, want %#v", store.createdTopics, want)
		}
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

func TestValidateMessagePublishRejectsUnavailableAgentTopic(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "review-agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{},
	}
	hub := NewHub(store, nil)

	code, text := hub.validateMessagePublish(7, types.AccountHuman, "p2p_7_43", false)

	if code != http.StatusForbidden || text != "agent is not available to this user" {
		t.Fatalf("code=%d text=%q, want 403 agent unavailable", code, text)
	}
}

func TestValidateMessagePublishAllowsAccessibleAgentTopic(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "review-agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
			44: {ID: 44, Username: "dev-agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
			44: 7,
		},
		friendPairs: map[string]bool{
			agentPairKey(7, 43): true,
		},
	}
	hub := NewHub(store, nil)

	if code, text := hub.validateMessagePublish(7, types.AccountHuman, "p2p_7_43", false); code != 0 || text != "" {
		t.Fatalf("friend agent publish code=%d text=%q, want allowed", code, text)
	}
	if code, text := hub.validateMessagePublish(7, types.AccountHuman, "p2p_7_44", false); code != 0 || text != "" {
		t.Fatalf("owner agent publish code=%d text=%q, want allowed", code, text)
	}
}

func TestValidateMessagePublishDoesNotBlockBotReplyToHuman(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "review-agent", DisplayName: "Review Agent", AccountType: types.AccountBot},
		},
	}
	hub := NewHub(store, nil)

	if code, text := hub.validateMessagePublish(43, types.AccountBot, "p2p_7_43", false); code != 0 || text != "" {
		t.Fatalf("bot reply code=%d text=%q, want allowed", code, text)
	}
}

func TestValidateMessagePublishDoesNotBlockHumanP2P(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			7: {ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
			8: {ID: 8, Username: "bob", DisplayName: "Bob", AccountType: types.AccountHuman},
		},
	}
	hub := NewHub(store, nil)

	if code, text := hub.validateMessagePublish(7, types.AccountHuman, "p2p_7_8", false); code != 0 || text != "" {
		t.Fatalf("human p2p code=%d text=%q, want allowed", code, text)
	}
}

func TestValidateMessagePublishChecksGroupBeforeAgentAccess(t *testing.T) {
	store := &agentTestStore{}
	hub := NewHub(store, nil)

	code, text := hub.validateMessagePublish(7, types.AccountHuman, "grp_80", false)

	if code != http.StatusForbidden || text != "not a group member" {
		t.Fatalf("group publish code=%d text=%q, want group membership failure", code, text)
	}
}

func TestValidateTopicReadAccessUsesPublishIdentityWithoutMutedCheck(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
			43: {ID: 43, Username: "agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{
			43: 99,
		},
		friendPairs: map[string]bool{},
		groupMembers: map[string]bool{
			groupMemberKey(80, 7): true,
		},
		groupMuted: map[string]bool{
			groupMemberKey(80, 7): true,
		},
	}
	hub := NewHub(store, nil)

	if code, text := hub.validateTopicReadAccess(7, types.AccountHuman, "grp_80"); code != 0 || text != "" {
		t.Fatalf("muted group member read code=%d text=%q, want allowed", code, text)
	}
	if code, text := hub.validateMessagePublish(7, types.AccountHuman, "grp_80", false); code != http.StatusForbidden || text != "you are muted in this group" {
		t.Fatalf("muted group publish code=%d text=%q, want muted", code, text)
	}
	if code, text := hub.validateTopicReadAccess(8, types.AccountHuman, "grp_80"); code != http.StatusForbidden || text != "not a group member" {
		t.Fatalf("non-member read code=%d text=%q, want not member", code, text)
	}
	if code, text := hub.validateTopicReadAccess(7, types.AccountHuman, "p2p_7_43"); code != http.StatusForbidden || text != "agent is not available to this user" {
		t.Fatalf("unavailable agent read code=%d text=%q, want forbidden", code, text)
	}
}

func agentPairKey(a, b int64) string {
	if a > b {
		a, b = b, a
	}
	return p2pTopicID(a, b)
}

func groupMemberKey(groupID, userID int64) string {
	return strconv.FormatInt(groupID, 10) + ":" + strconv.FormatInt(userID, 10)
}
