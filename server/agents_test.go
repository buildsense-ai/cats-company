package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type agentTestStore struct {
	store.Store
	ownerBots      []map[string]interface{}
	friends        []*types.User
	users          map[int64]*types.User
	owners         map[int64]int64
	friendPairs    map[string]bool
	groupMembers   map[string]bool
	groupsByUser   map[int64][]*types.Group
	membersByGroup map[int64][]*types.GroupMember
	groupMuted     map[string]bool
	createdTopics  []string
	botBodyIDs     map[int64]string
	modelConfigs   map[int64]*types.BotModelConfig
}

func (s *agentTestStore) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	return s.ownerBots, nil
}

func (s *agentTestStore) GetFriends(uid int64) ([]*types.User, error) {
	return s.friends, nil
}

func (s *agentTestStore) GetUserGroups(uid int64) ([]*types.Group, error) {
	return s.groupsByUser[uid], nil
}

func (s *agentTestStore) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	return s.membersByGroup[groupID], nil
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

func (s *agentTestStore) GetBotBodyID(botUID int64) (string, error) {
	bodyID, ok := s.botBodyIDs[botUID]
	if !ok {
		return "", errors.New("not found")
	}
	return bodyID, nil
}

func (s *agentTestStore) GetBotModelConfig(botUID int64) (*types.BotModelConfig, error) {
	config, ok := s.modelConfigs[botUID]
	if !ok {
		return nil, errors.New("not found")
	}
	return config, nil
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

func (s *agentTestStore) IsChannelManagedGroup(groupID int64) (bool, error) {
	return false, nil
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

func TestHandleListAgentsMarksConfiguredCloudArtifactAgents(t *testing.T) {
	t.Setenv("CATSCO_CLOUD_ARTIFACT_AGENT_UIDS", "usr42, 43; invalid 0 -1")
	store := &agentTestStore{
		ownerBots: []map[string]interface{}{
			{"id": int64(42), "username": "owner-agent"},
			{"id": int64(44), "username": "ordinary-agent"},
		},
		friends: []*types.User{
			{ID: 43, Username: "friend-agent", AccountType: types.AccountBot},
		},
	}
	handler := NewAgentHandler(store, nil)
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
	enabled := make(map[int64]bool)
	for _, agent := range body.Agents {
		enabled[agent.UID] = agent.CloudArtifactsEnabled
	}
	if !enabled[42] || !enabled[43] {
		t.Fatalf("configured agents missing capability: %+v", enabled)
	}
	if enabled[44] {
		t.Fatalf("ordinary agent unexpectedly has capability: %+v", enabled)
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
		groupsByUser: map[int64][]*types.Group{
			7: []*types.Group{{ID: 8, Name: "Shared Agent Task", AgentIDs: []int64{43, 45, 46}}},
		},
	}
	hub := NewHub(nil, nil)
	hub.addClient(&Client{uid: 42, send: make(chan []byte, 1)})
	hub.addClient(&Client{uid: 43, send: make(chan []byte, 1)})
	hub.addClient(&Client{uid: 44, send: make(chan []byte, 1)})
	if _, err := hub.bodyLeases.acquire(45, "body-group", "conn-group"); err != nil {
		t.Fatalf("acquire group bot body lease: %v", err)
	}
	hub.addRegisteredClient(&Client{
		uid:          45,
		accountType:  types.AccountBot,
		bodyID:       "body-group",
		connectionID: "conn-group",
		send:         make(chan []byte, 1),
	})
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
	if !onlineByUID[45] {
		t.Fatalf("group Agent with body lease should be online: %#v", onlineByUID)
	}
	if onlineByUID[46] {
		t.Fatalf("group Agent without body lease should be offline: %#v", onlineByUID)
	}
}

func TestBotPresenceReachesFellowGroupMembers(t *testing.T) {
	store := &agentTestStore{
		friends: []*types.User{{ID: 7, Username: "friend-member", AccountType: types.AccountHuman}},
		owners:  map[int64]int64{45: 0},
		groupsByUser: map[int64][]*types.Group{
			45: []*types.Group{{ID: 8, Name: "Shared Agent Task", AgentIDs: []int64{45}}},
		},
		membersByGroup: map[int64][]*types.GroupMember{
			8: []*types.GroupMember{
				{GroupID: 8, UserID: 7},
				{GroupID: 8, UserID: 8},
				{GroupID: 8, UserID: 45},
			},
		},
	}
	hub := NewHub(store, nil)
	memberSeven := make(chan []byte, 1)
	memberEight := make(chan []byte, 1)
	outsider := make(chan []byte, 1)
	hub.addClient(&Client{uid: 7, send: memberSeven})
	hub.addClient(&Client{uid: 8, send: memberEight})
	hub.addClient(&Client{uid: 10, send: outsider})

	for _, what := range []string{"on", "off"} {
		hub.broadcastPresence(45, what)
		for uid, messages := range map[int64]chan []byte{7: memberSeven, 8: memberEight} {
			select {
			case payload := <-messages:
				var message ServerMessage
				if err := json.Unmarshal(payload, &message); err != nil {
					t.Fatalf("decode member %d presence: %v", uid, err)
				}
				if message.Pres == nil || message.Pres.Topic != "me" || message.Pres.What != what || message.Pres.Src != "usr45" {
					t.Fatalf("member %d received unexpected presence: %+v", uid, message.Pres)
				}
			default:
				t.Fatalf("member %d did not receive group Agent %s presence", uid, what)
			}
		}
		select {
		case payload := <-outsider:
			t.Fatalf("outsider received unexpected Agent presence: %s", payload)
		default:
		}
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

func TestHandleAgentQuotaUsesOwnerBudgetAndAppliedAgentModel(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "review-agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{43: 99},
		friendPairs: map[string]bool{
			agentPairKey(7, 43): true,
		},
		modelConfigs: map[int64]*types.BotModelConfig{
			43: {AppliedKind: "catalog", AppliedModelID: "MiniMax-M3", AppliedReasoning: "high", AppliedRevision: 3},
		},
	}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); got != "99" {
			t.Fatalf("relay usage search=%q, want owner uid 99", got)
		}
		writeJSON(w, http.StatusOK, commercialRelayUsageResponse{Users: []commercialRelayUsageUser{{
			UID:        99,
			Configured: true,
			Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
				Provider: "minimax-m3-anthropic",
				Model:    "MiniMax-M3",
				Budget: commercialRelayBudget{
					MaxLimit:      1000,
					CurrentUsage:  250,
					ResetDuration: "1M",
				},
			}}},
		}}})
	}))
	defer admin.Close()

	handler := NewAgentHandler(store, nil)
	resolverCalls := 0
	handler.SetRelayUsageDependencies(&RelayAdminClient{
		baseURL: admin.URL,
		token:   "test-token",
		client:  admin.Client(),
	}, func(uid int64, bodyID string) (DeviceModelStatus, bool) {
		resolverCalls++
		return DeviceModelStatus{Source: "relay", Model: "stale-owner-device"}, uid == 99
	})
	req := httptest.NewRequest(http.MethodGet, "/api/agents/quota?uid=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleAgentQuota(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body agentQuotaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Configured || !body.Shared || body.Summary == nil {
		t.Fatalf("unexpected quota response: %+v", body)
	}
	if body.Summary.Model != "MiniMax-M3" || body.Summary.ReasoningEffort != "high" || body.Summary.RemainingPercent != 75 || body.Summary.Status != "normal" {
		t.Fatalf("unexpected sanitized summary: %+v", body.Summary)
	}
	if strings.Contains(rec.Body.String(), "used_cny") || strings.Contains(rec.Body.String(), "limit_cny") {
		t.Fatalf("friend-visible response leaked cost fields: %s", rec.Body.String())
	}
	if resolverCalls != 0 {
		t.Fatalf("applied bot model should not consult an owner device, calls=%d", resolverCalls)
	}
}

func TestHandleAgentQuotaDoesNotReuseCachedReasoningEffortAfterSwitch(t *testing.T) {
	config := &types.BotModelConfig{
		AppliedKind: "catalog", AppliedModelID: "MiniMax-M3", AppliedReasoning: "high", AppliedRevision: 3,
	}
	store := &agentTestStore{
		users:        map[int64]*types.User{43: {ID: 43, AccountType: types.AccountBot}},
		owners:       map[int64]int64{43: 99},
		friendPairs:  map[string]bool{agentPairKey(7, 43): true},
		modelConfigs: map[int64]*types.BotModelConfig{43: config},
	}
	adminCalls := 0
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminCalls++
		writeJSON(w, http.StatusOK, commercialRelayUsageResponse{Users: []commercialRelayUsageUser{{
			UID: 99, Configured: true,
			Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
				Model: "MiniMax-M3", Budget: commercialRelayBudget{MaxLimit: 100, CurrentUsage: 10},
			}}},
		}}})
	}))
	defer admin.Close()
	handler := NewAgentHandler(store, nil)
	handler.SetRelayUsageDependencies(&RelayAdminClient{baseURL: admin.URL, token: "test-token", client: admin.Client()}, nil)

	request := func() agentQuotaResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/agents/quota?uid=43", nil)
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
		rec := httptest.NewRecorder()
		handler.HandleAgentQuota(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var body agentQuotaResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return body
	}

	if got := request().Summary; got == nil || got.ReasoningEffort != "high" {
		t.Fatalf("initial summary=%+v", got)
	}
	config.AppliedReasoning = "xhigh"
	config.AppliedRevision++
	if got := request().Summary; got == nil || got.ReasoningEffort != "xhigh" {
		t.Fatalf("updated summary=%+v", got)
	}
	if adminCalls != 2 {
		t.Fatalf("relay usage calls=%d, want 2 cache entries for distinct reasoning efforts", adminCalls)
	}
}

func TestHandleAgentQuotaKeepsAppliedModelWhenQuotaBucketUsesAnotherModelName(t *testing.T) {
	store := &agentTestStore{
		users:       map[int64]*types.User{43: {ID: 43, AccountType: types.AccountBot}},
		owners:      map[int64]int64{43: 99},
		friendPairs: map[string]bool{agentPairKey(7, 43): true},
		modelConfigs: map[int64]*types.BotModelConfig{43: {
			AppliedKind:      "catalog",
			AppliedModelID:   "gpt-5.6-sol",
			AppliedReasoning: "high",
			AppliedRevision:  2,
		}},
	}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, commercialRelayUsageResponse{Users: []commercialRelayUsageUser{{
			UID: 99, Configured: true,
			Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
				Model:         "gpt-5.6-terra",
				AllowedModels: []string{"gpt-5.6-terra", "gpt-5.6-sol"},
				Budget: commercialRelayBudget{
					MaxLimit:      5000,
					CurrentUsage:  25,
					ResetDuration: "1M",
				},
			}}},
		}}})
	}))
	defer admin.Close()

	handler := NewAgentHandler(store, nil)
	handler.SetRelayUsageDependencies(&RelayAdminClient{
		baseURL: admin.URL,
		token:   "test-token",
		client:  admin.Client(),
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/agents/quota?uid=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleAgentQuota(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body agentQuotaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Summary == nil {
		t.Fatalf("missing summary: %+v", body)
	}
	if body.Summary.Model != "gpt-5.6-sol" || body.Summary.ReasoningEffort != "high" {
		t.Fatalf("summary must describe the applied bot model, got %+v", body.Summary)
	}
	if body.Summary.RemainingPercent != 99.5 || body.Summary.ResetDuration != "1M" {
		t.Fatalf("summary must retain the shared quota bucket values, got %+v", body.Summary)
	}
}

func TestHandleAgentQuotaPreservesCustomModelName(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "custom-agent", AccountType: types.AccountBot},
		},
		owners: map[int64]int64{43: 99},
		friendPairs: map[string]bool{
			agentPairKey(7, 43): true,
		},
		modelConfigs: map[int64]*types.BotModelConfig{
			43: {AppliedKind: "custom", AppliedModelID: "gpt-5.6-terra", AppliedReasoning: "xhigh", AppliedRevision: 4},
		},
	}
	handler := NewAgentHandler(store, nil)
	handler.SetRelayUsageDependencies(nil, func(uid int64, bodyID string) (DeviceModelStatus, bool) {
		return DeviceModelStatus{Source: "custom", Model: "stale-custom"}, uid == 99
	})
	req := httptest.NewRequest(http.MethodGet, "/api/agents/quota?uid=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleAgentQuota(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body agentQuotaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Configured || body.Shared || body.Summary == nil {
		t.Fatalf("unexpected custom model response: %+v", body)
	}
	if body.Summary.Source != "custom" || body.Summary.Model != "gpt-5.6-terra" || body.Summary.ReasoningEffort != "xhigh" || body.Summary.Status != "custom" {
		t.Fatalf("unexpected custom model summary: %+v", body.Summary)
	}
}

func TestResolveAgentModelStatusKeepsAppliedModelDuringPendingOrFailedSwitch(t *testing.T) {
	for _, test := range []struct {
		name   string
		config *types.BotModelConfig
	}{
		{
			name: "pending",
			config: &types.BotModelConfig{
				Kind: "catalog", ModelID: "gpt-5.6-sol", Revision: 8,
				AppliedKind: "catalog", AppliedModelID: "minimax-m3", AppliedRevision: 7,
			},
		},
		{
			name: "failed",
			config: &types.BotModelConfig{
				Kind: "catalog", ModelID: "gpt-5.6-sol", Revision: 8,
				AppliedKind: "catalog", AppliedModelID: "minimax-m3", AppliedRevision: 7,
				LastAttemptRevision: 8, LastError: "upstream unavailable",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &agentTestStore{modelConfigs: map[int64]*types.BotModelConfig{43: test.config}}
			handler := NewAgentHandler(store, nil)
			handler.SetRelayUsageDependencies(nil, func(uid int64, bodyID string) (DeviceModelStatus, bool) {
				t.Fatal("an applied cloud model must not fall back to device state")
				return DeviceModelStatus{}, false
			})

			status, ok := handler.resolveAgentModelStatus(43, 99)
			if !ok || status.Source != "relay" || status.Model != "minimax-m3" {
				t.Fatalf("resolved status=%+v ok=%v", status, ok)
			}
		})
	}
}

func TestResolveAgentModelStatusFallsBackToBoundBody(t *testing.T) {
	store := &agentTestStore{
		botBodyIDs:   map[int64]string{43: "body-agent-43"},
		modelConfigs: map[int64]*types.BotModelConfig{43: {AppliedKind: "local", AppliedModelID: "local", AppliedRevision: 2}},
	}
	handler := NewAgentHandler(store, nil)
	handler.SetRelayUsageDependencies(nil, func(uid int64, bodyID string) (DeviceModelStatus, bool) {
		if uid != 99 || bodyID != "body-agent-43" {
			t.Fatalf("resolver received uid=%d bodyID=%q", uid, bodyID)
		}
		return DeviceModelStatus{Source: "relay", Model: "gpt-5.6-terra"}, true
	})

	status, ok := handler.resolveAgentModelStatus(43, 99)
	if !ok || status.Source != "relay" || status.Model != "gpt-5.6-terra" {
		t.Fatalf("resolved status=%+v ok=%v", status, ok)
	}
}

func TestResolveAgentModelStatusKeepsBotsSeparateUnderOneOwner(t *testing.T) {
	store := &agentTestStore{modelConfigs: map[int64]*types.BotModelConfig{
		43: {AppliedKind: "catalog", AppliedModelID: "minimax-m3", AppliedRevision: 1},
		44: {AppliedKind: "catalog", AppliedModelID: "gpt-5.6-sol", AppliedRevision: 1},
	}}
	handler := NewAgentHandler(store, nil)

	first, firstOK := handler.resolveAgentModelStatus(43, 99)
	second, secondOK := handler.resolveAgentModelStatus(44, 99)
	if !firstOK || !secondOK || first.Model != "minimax-m3" || second.Model != "gpt-5.6-sol" {
		t.Fatalf("unexpected per-bot models: first=%+v second=%+v", first, second)
	}
}

func TestAppliedBotModelStatusNormalizesCustomPlaceholder(t *testing.T) {
	status, ok := appliedBotModelStatus(&types.BotModelConfig{AppliedKind: "custom", AppliedModelID: "custom"})
	if !ok || status.Source != "custom" || status.Model != "自定义模型" {
		t.Fatalf("status=%+v ok=%v", status, ok)
	}
}

func TestHandleAgentQuotaRejectsNonFriend(t *testing.T) {
	store := &agentTestStore{
		users: map[int64]*types.User{
			43: {ID: 43, Username: "private-agent", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{43: 99},
		friendPairs: map[string]bool{},
	}
	handler := NewAgentHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/agents/quota?uid=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleAgentQuota(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestSanitizeAgentQuotaPreservesZeroRemainingPercent(t *testing.T) {
	response := sanitizeAgentQuota(relayUsageResponse{
		Configured: true,
		Summary: &relayUsageSummary{
			Source:   "relay",
			Model:    "MiniMax-M3",
			Percent:  100,
			Status:   "high",
			LimitCNY: 1000,
		},
	})

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(data), `"remaining_percent":0`) {
		t.Fatalf("zero remaining percent must be explicit: %s", data)
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
