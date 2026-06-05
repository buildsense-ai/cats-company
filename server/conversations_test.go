package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// TestBuildGroupConversationSummary_FallbackToCreatedAt 测试：
// 当群组没有消息时，使用 created_at 作为排序时间
func TestBuildGroupConversationSummary_FallbackToCreatedAt(t *testing.T) {
	now := time.Now()
	createdAt := now.Add(-1 * time.Hour)

	group := &types.Group{
		ID:        1,
		Name:      "Test Group",
		OwnerID:   100,
		CreatedAt: createdAt,
	}

	summary := buildGroupConversationSummary("grp_1", group, nil)

	if summary.LastTime == nil {
		t.Fatal("LastTime should not be nil when group has no messages")
	}
	if !summary.LastTime.Equal(createdAt) {
		t.Fatalf("expected LastTime=%v, got %v", createdAt, summary.LastTime)
	}
	if summary.ID != "grp_1" {
		t.Fatalf("expected ID=grp_1, got %s", summary.ID)
	}
	if summary.Name != "Test Group" {
		t.Fatalf("expected Name=Test Group, got %s", summary.Name)
	}
	if !summary.IsGroup {
		t.Fatal("expected IsGroup=true")
	}
	if summary.GroupID != 1 {
		t.Fatalf("expected GroupID=1, got %d", summary.GroupID)
	}
}

// TestBuildGroupConversationSummary_UsesLatestMessageWhenAvailable 测试：
// 当群组有消息时，使用最新消息时间而不是 created_at
func TestBuildGroupConversationSummary_UsesLatestMessageWhenAvailable(t *testing.T) {
	groupCreatedAt := time.Now().Add(-2 * time.Hour)
	messageTime := time.Now().Add(-10 * time.Minute)

	group := &types.Group{
		ID:        2,
		Name:      "Active Group",
		OwnerID:   100,
		CreatedAt: groupCreatedAt,
	}

	latestMsg := &types.Message{
		ID:        999,
		CreatedAt: messageTime,
		Content:   "Hello",
		MsgType:   "text",
	}

	summary := buildGroupConversationSummary("grp_2", group, latestMsg)

	if summary.LastTime == nil {
		t.Fatal("LastTime should not be nil when group has messages")
	}
	if !summary.LastTime.Equal(messageTime) {
		t.Fatalf("expected LastTime=%v (message time), got %v", messageTime, summary.LastTime)
	}
	if summary.Preview != "Hello" {
		t.Fatalf("expected Preview=Hello, got %s", summary.Preview)
	}
	if summary.LatestSeq != 999 {
		t.Fatalf("expected LatestSeq=999, got %d", summary.LatestSeq)
	}
}

// TestBuildGroupConversationSummary_UsesCreatedAtWhenLatestIsOlder 测试：
// 如果群组最新消息时间早于创建时间，仍使用 created_at 作为排序时间
func TestBuildGroupConversationSummary_UsesCreatedAtWhenLatestIsOlder(t *testing.T) {
	groupCreatedAt := time.Now()
	messageTime := groupCreatedAt.Add(-10 * time.Minute)

	group := &types.Group{
		ID:        3,
		Name:      "New Topic",
		OwnerID:   100,
		CreatedAt: groupCreatedAt,
	}

	latestMsg := &types.Message{
		ID:        1000,
		CreatedAt: messageTime,
		Content:   "Older message",
		MsgType:   "text",
	}

	summary := buildGroupConversationSummary("grp_3", group, latestMsg)

	if summary.LastTime == nil {
		t.Fatal("LastTime should not be nil")
	}
	if !summary.LastTime.Equal(groupCreatedAt) {
		t.Fatalf("expected LastTime=%v (group created time), got %v", groupCreatedAt, summary.LastTime)
	}
	if summary.Preview != "Older message" {
		t.Fatalf("expected Preview=Older message, got %s", summary.Preview)
	}
	if summary.LatestSeq != 1000 {
		t.Fatalf("expected LatestSeq=1000, got %d", summary.LatestSeq)
	}
}

// TestConversationLess_NoMessagesGroupAtTop 测试：
// 验证无消息群组按创建时间降序排列（越新越靠前），复用实际排序函数
func TestConversationLess_NoMessagesGroupAtTop(t *testing.T) {
	now := time.Now()

	olderGroup := &types.Group{
		ID:        10,
		Name:      "Older Group",
		OwnerID:   100,
		CreatedAt: now.Add(-2 * time.Hour),
	}
	newerGroup := &types.Group{
		ID:        11,
		Name:      "Newer Group",
		OwnerID:   100,
		CreatedAt: now.Add(-1 * time.Hour),
	}

	olderSummary := buildGroupConversationSummary("grp_10", olderGroup, nil)
	newerSummary := buildGroupConversationSummary("grp_11", newerGroup, nil)

	if olderSummary.LastTime == nil || newerSummary.LastTime == nil {
		t.Fatal("both groups should have LastTime")
	}
	if !newerSummary.LastTime.After(*olderSummary.LastTime) {
		t.Fatal("newer group should have later LastTime")
	}

	conversations := []*types.ConversationSummary{olderSummary, newerSummary}
	sort.SliceStable(conversations, conversationLess(conversations))

	if conversations[0].GroupID != 11 {
		t.Fatalf("expected newer group first, got groupID=%d", conversations[0].GroupID)
	}
}

// TestConversationLess_SameCreatedAtUsesGroupIDDesc 测试：
// MySQL 时间戳可能只有秒级精度，同一秒创建的群应使用 ID 确定新群优先
func TestConversationLess_SameCreatedAtUsesGroupIDDesc(t *testing.T) {
	createdAt := time.Now().Truncate(time.Second)

	olderIDGroup := buildGroupConversationSummary("grp_30", &types.Group{
		ID:        30,
		Name:      "Older ID",
		OwnerID:   100,
		CreatedAt: createdAt,
	}, nil)
	newerIDGroup := buildGroupConversationSummary("grp_31", &types.Group{
		ID:        31,
		Name:      "Newer ID",
		OwnerID:   100,
		CreatedAt: createdAt,
	}, nil)

	conversations := []*types.ConversationSummary{olderIDGroup, newerIDGroup}
	sort.SliceStable(conversations, conversationLess(conversations))

	if conversations[0].GroupID != 31 {
		t.Fatalf("expected higher group ID first when timestamps tie, got GroupID=%d", conversations[0].GroupID)
	}
}

// TestConversationLess_SameTimePrefersGroup 测试：
// 同秒情况下，新话题应优先浮到会话区顶部，避免创建后看起来仍在底部
func TestConversationLess_SameTimePrefersGroup(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	groupSummary := buildGroupConversationSummary("grp_40", &types.Group{
		ID:        40,
		Name:      "New Topic",
		OwnerID:   100,
		CreatedAt: now,
	}, nil)
	friendSummary := buildFriendConversationSummary("p2p_100_200", &types.User{
		ID:          200,
		DisplayName: "Friend",
		Username:    "friend",
	}, &types.Message{
		ID:        999,
		TopicID:   "p2p_100_200",
		FromUID:   200,
		Content:   "Same second",
		MsgType:   "text",
		CreatedAt: now,
	}, nil)

	conversations := []*types.ConversationSummary{friendSummary, groupSummary}
	sort.SliceStable(conversations, conversationLess(conversations))

	if conversations[0].GroupID != 40 {
		t.Fatalf("expected group first when timestamps tie, got %#v", conversations[0])
	}
}

// TestConversationLess_MixedWithP2P 测试：
// 验证无消息群组 vs 无消息 P2P 会话的排序，复用实际排序函数
func TestConversationLess_MixedWithP2P(t *testing.T) {
	now := time.Now()

	group := &types.Group{
		ID:        20,
		Name:      "Empty Group",
		OwnerID:   100,
		CreatedAt: now.Add(-30 * time.Minute),
	}
	groupSummary := buildGroupConversationSummary("grp_20", group, nil)

	friend := &types.User{
		ID:          200,
		DisplayName: "Friend",
		Username:    "friend",
	}
	friendSummary := buildFriendConversationSummary("p2p_100_200", friend, nil, nil)

	if groupSummary.LastTime == nil {
		t.Fatal("group LastTime should use created_at")
	}
	if friendSummary.LastTime != nil {
		t.Fatal("P2P LastTime should be nil when no messages")
	}

	conversations := []*types.ConversationSummary{friendSummary, groupSummary}
	sort.SliceStable(conversations, conversationLess(conversations))

	if conversations[0].GroupID != 20 {
		t.Fatalf("expected empty group first, got GroupID=%d", conversations[0].GroupID)
	}
}

type conversationTestStore struct {
	store.Store
	friends       []*types.User
	groups        []*types.Group
	ownerBots     []map[string]interface{}
	latestByTopic map[string]*types.Message
	requestedIDs  []string
}

func (s *conversationTestStore) GetFriends(uid int64) ([]*types.User, error) {
	return s.friends, nil
}

func (s *conversationTestStore) GetUserGroups(userID int64) ([]*types.Group, error) {
	return s.groups, nil
}

func (s *conversationTestStore) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	return s.ownerBots, nil
}

func (s *conversationTestStore) GetLatestMessagesForTopics(topicIDs []string) (map[string]*types.Message, error) {
	s.requestedIDs = append([]string(nil), topicIDs...)
	if s.latestByTopic == nil {
		return map[string]*types.Message{}, nil
	}
	return s.latestByTopic, nil
}

func TestConversationsIncludeOwnedAgentsWithoutFriendRelationship(t *testing.T) {
	now := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	store := &conversationTestStore{
		ownerBots: []map[string]interface{}{
			{
				"id":           int64(42),
				"username":     "dev-agent",
				"display_name": "Dev Agent",
				"avatar_url":   "/uploads/dev.png",
			},
		},
		latestByTopic: map[string]*types.Message{
			"p2p_7_42": {ID: 9, TopicID: "p2p_7_42", FromUID: 7, Content: "hello dev", MsgType: "text", CreatedAt: now},
		},
	}
	handler := NewConversationHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversations []*types.ConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Conversations) != 1 {
		t.Fatalf("conversation count=%d, want 1: %+v", len(body.Conversations), body.Conversations)
	}
	got := body.Conversations[0]
	if got.ID != "p2p_7_42" || got.Name != "Dev Agent" || got.FriendID != 42 || !got.IsBot {
		t.Fatalf("unexpected owner agent conversation: %+v", got)
	}
	if got.Preview != "hello dev" || got.LatestSeq != 9 {
		t.Fatalf("unexpected latest message fields: %+v", got)
	}
	if len(store.requestedIDs) != 1 || store.requestedIDs[0] != "p2p_7_42" {
		t.Fatalf("requested topic ids = %#v, want p2p_7_42", store.requestedIDs)
	}
}

func TestConversationsDeduplicateOwnedAgentThatIsAlreadyFriend(t *testing.T) {
	store := &conversationTestStore{
		friends: []*types.User{
			{ID: 42, Username: "dev-agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		},
		ownerBots: []map[string]interface{}{
			{
				"id":           int64(42),
				"username":     "dev-agent",
				"display_name": "Dev Agent",
			},
		},
	}
	handler := NewConversationHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversations []*types.ConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Conversations) != 1 {
		t.Fatalf("conversation count=%d, want 1: %+v", len(body.Conversations), body.Conversations)
	}
	if !body.Conversations[0].IsBot {
		t.Fatalf("friend bot conversation should be marked is_bot: %+v", body.Conversations[0])
	}
}

func TestConversationsMarkGroupsContainingBots(t *testing.T) {
	store := &conversationTestStore{
		groups: []*types.Group{
			{
				ID:        9,
				Name:      "Bot Room",
				OwnerID:   7,
				HasBot:    true,
				CreatedAt: time.Date(2026, 6, 4, 8, 0, 0, 0, time.UTC),
			},
		},
	}
	handler := NewConversationHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversations []*types.ConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Conversations) != 1 {
		t.Fatalf("conversation count=%d, want 1: %+v", len(body.Conversations), body.Conversations)
	}
	got := body.Conversations[0]
	if got.ID != "grp_9" || !got.IsGroup || got.GroupID != 9 {
		t.Fatalf("unexpected group conversation: %+v", got)
	}
	if !got.HasBot {
		t.Fatalf("bot group conversation should be marked has_bot: %+v", got)
	}
}
