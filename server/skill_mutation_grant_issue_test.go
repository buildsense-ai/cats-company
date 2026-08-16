package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type skillMutationGrantIssueTestStore struct {
	store.Store
	users        map[int64]*types.User
	ownerUID     int64
	friends      map[[2]int64]bool
	mode         types.BotSkillMutationMode
	definition   *types.BotDefinitionRecord
	messages     map[string][]*types.Message
	groupMembers map[[2]int64]bool
	err          error
}

func (s *skillMutationGrantIssueTestStore) GetUser(uid int64) (*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.users[uid], nil
}

func (s *skillMutationGrantIssueTestStore) GetBotOwner(botUID int64) (int64, error) {
	if s.err != nil || botUID != 42 {
		return 0, errors.New("bot owner unavailable")
	}
	return s.ownerUID, nil
}

func (s *skillMutationGrantIssueTestStore) AreFriends(uid1, uid2 int64) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.friends[[2]int64{uid1, uid2}] || s.friends[[2]int64{uid2, uid1}], nil
}

func (s *skillMutationGrantIssueTestStore) GetBotSkillMutationMode(botUID int64) (types.BotSkillMutationMode, error) {
	if s.err != nil || botUID != 42 {
		return "", errors.New("mutation policy unavailable")
	}
	return s.mode, nil
}

func (s *skillMutationGrantIssueTestStore) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	if s.err != nil || botUID != 42 {
		return nil, errors.New("definition unavailable")
	}
	return s.definition, nil
}

func (s *skillMutationGrantIssueTestStore) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.messages[topicID], nil
}

func (s *skillMutationGrantIssueTestStore) IsGroupMember(groupID, userID int64) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.groupMembers[[2]int64{groupID, userID}], nil
}

type skillMutationGrantIssueFixture struct {
	now    time.Time
	db     *skillMutationGrantIssueTestStore
	hub    *Hub
	client *Client
	msg    *MsgSkillMutationGrant
}

func newSkillMutationGrantIssueFixture(t *testing.T, actorUID int64) *skillMutationGrantIssueFixture {
	t.Helper()
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	topicID := p2pTopicID(actorUID, 42)
	db := &skillMutationGrantIssueTestStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "owner", AccountType: types.AccountHuman},
			8:  {ID: 8, Username: "friend", AccountType: types.AccountHuman},
			42: {ID: 42, Username: "review-bot", AccountType: types.AccountBot},
		},
		ownerUID: 7,
		friends:  map[[2]int64]bool{{8, 42}: true},
		mode:     types.BotSkillMutationOwnerOnly,
		definition: &types.BotDefinitionRecord{
			Exists:  true,
			Runtime: types.BotDefinitionRuntime{DesiredRevision: 8},
			Definition: types.BotDefinition{Skills: []types.BotSkillRef{{
				Source: "skillhub", SkillID: "skill-review-pr", Version: "1.2.3", ContentHash: strings.Repeat("a", 64),
			}}},
		},
		messages: map[string][]*types.Message{
			topicID: {{ID: 99, TopicID: topicID, FromUID: actorUID, MsgType: "text", CreatedAt: now.Add(-5 * time.Minute)}},
		},
		groupMembers: make(map[[2]int64]bool),
	}
	hub := NewHub(db, nil)
	signer, err := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	hub.skillMutationGrants = signer
	hub.bodyLeases.now = func() time.Time { return now }
	client := &Client{
		hub: hub, uid: 42, accountType: types.AccountBot, bodyID: "body-prod-1", connectionID: "conn-1", send: make(chan []byte, 8),
	}
	if _, err := hub.bodyLeases.acquire(client.uid, client.bodyID, client.connectionID); err != nil {
		t.Fatal(err)
	}
	return &skillMutationGrantIssueFixture{
		now: now, db: db, hub: hub, client: client,
		msg: &MsgSkillMutationGrant{
			ID: "wire-1", Type: "request", RequestID: "grant-request-1", ClientRequestID: "mutation-001",
			SourceTopicID: topicID, SourceMessageID: 99, LocalSkillID: "review-pr", Operation: "create",
			CandidateContentHash: strings.Repeat("b", 64), CandidateSizeBytes: 4096, ExpectedDefinitionRevision: 8,
		},
	}
}

func readSkillMutationGrantResult(t *testing.T, fixture *skillMutationGrantIssueFixture) *MsgSkillMutationGrant {
	t.Helper()
	var response ServerMessage
	decodeQueuedServerMessage(t, fixture.client.send, &response)
	if response.SkillMutationGrant == nil {
		t.Fatalf("unexpected response: %#v", response)
	}
	return response.SkillMutationGrant
}

func TestSkillMutationGrantIssuedFromCanonicalOwnerMessageAndRuntime(t *testing.T) {
	fixture := newSkillMutationGrantIssueFixture(t, 7)
	fixture.hub.handleMessage(fixture.client, &ClientMessage{SkillMutationGrant: fixture.msg})
	result := readSkillMutationGrantResult(t, fixture)
	if result.Error != nil || result.Grant == "" || result.ActorUserID != "usr7" || result.AgentID != "usr42" ||
		result.RuntimeBodyID != "body-prod-1" || result.ExpiresAt <= fixture.now.UnixMilli() {
		t.Fatalf("unexpected grant result: %#v error=%+v", result, result.Error)
	}
	claims, err := fixture.hub.skillMutationGrants.verify(result.Grant)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ActorUserUID != 7 || claims.BotUID != 42 || claims.SourceMessageID != 99 ||
		claims.RuntimeBodyID != "body-prod-1" || claims.ExpectedDefinitionRevision != 8 {
		t.Fatalf("grant did not bind canonical facts: %#v", claims)
	}
}

func TestSkillMutationGrantSharedLiveAllowsFriendButOwnerOnlyDoesNot(t *testing.T) {
	ownerOnly := newSkillMutationGrantIssueFixture(t, 8)
	ownerOnly.hub.handleSkillMutationGrant(ownerOnly.client, ownerOnly.msg)
	denied := readSkillMutationGrantResult(t, ownerOnly)
	if denied.Error == nil || denied.Error.Code != "forbidden" || denied.Grant != "" {
		t.Fatalf("owner_only friend result: %#v error=%+v", denied, denied.Error)
	}

	shared := newSkillMutationGrantIssueFixture(t, 8)
	shared.db.mode = types.BotSkillMutationSharedLive
	shared.hub.handleSkillMutationGrant(shared.client, shared.msg)
	allowed := readSkillMutationGrantResult(t, shared)
	if allowed.Error != nil || allowed.Grant == "" || allowed.ActorUserID != "usr8" {
		t.Fatalf("shared_live friend result: %#v", allowed)
	}
}

func TestSkillMutationGrantRejectsInactiveOrLegacyRuntimeBody(t *testing.T) {
	fixture := newSkillMutationGrantIssueFixture(t, 7)
	fixture.client.connectionID = "not-current"
	fixture.hub.handleSkillMutationGrant(fixture.client, fixture.msg)
	result := readSkillMutationGrantResult(t, fixture)
	if result.Error == nil || result.Error.Code != "runtime_identity_invalid" {
		t.Fatalf("inactive body result: %#v", result)
	}

	legacy := newSkillMutationGrantIssueFixture(t, 7)
	legacy.client.bodyID = legacyBotBodyID(42)
	legacy.hub.handleSkillMutationGrant(legacy.client, legacy.msg)
	result = readSkillMutationGrantResult(t, legacy)
	if result.Error == nil || result.Error.Code != "runtime_identity_invalid" {
		t.Fatalf("legacy body result: %#v", result)
	}
}

func TestSkillMutationGrantRejectsNonCanonicalOrExpiredHumanMessage(t *testing.T) {
	botActor := newSkillMutationGrantIssueFixture(t, 7)
	botActor.db.users[7].AccountType = types.AccountBot
	botActor.hub.handleSkillMutationGrant(botActor.client, botActor.msg)
	result := readSkillMutationGrantResult(t, botActor)
	if result.Error == nil || result.Error.Code != "source_message_invalid" {
		t.Fatalf("bot-authored source result: %#v", result)
	}

	channelShadow := newSkillMutationGrantIssueFixture(t, 7)
	channelShadow.db.users[7].Username = "ch_feishu_unlinked"
	channelShadow.hub.handleSkillMutationGrant(channelShadow.client, channelShadow.msg)
	result = readSkillMutationGrantResult(t, channelShadow)
	if result.Error == nil || result.Error.Code != "source_message_invalid" {
		t.Fatalf("unlinked channel source result: %#v", result)
	}

	expired := newSkillMutationGrantIssueFixture(t, 7)
	expired.db.messages[expired.msg.SourceTopicID][0].CreatedAt = expired.now.Add(-skillMutationSourceMessageMaxAge - time.Second)
	expired.hub.handleSkillMutationGrant(expired.client, expired.msg)
	result = readSkillMutationGrantResult(t, expired)
	if result.Error == nil || result.Error.Code != "source_message_invalid" {
		t.Fatalf("expired source result: %#v", result)
	}
}

func TestSkillMutationGrantRejectsStaleDefinitionAndAcceptsExactReplacement(t *testing.T) {
	stale := newSkillMutationGrantIssueFixture(t, 7)
	stale.msg.ExpectedDefinitionRevision = 7
	stale.hub.handleSkillMutationGrant(stale.client, stale.msg)
	result := readSkillMutationGrantResult(t, stale)
	if result.Error == nil || result.Error.Code != "definition_stale" {
		t.Fatalf("stale definition result: %#v error=%+v", result, result.Error)
	}

	replace := newSkillMutationGrantIssueFixture(t, 7)
	replace.msg.Operation = "replace"
	replace.msg.ExpectedPreviousHash = strings.Repeat("a", 64)
	replace.msg.BeforeReference = &types.BotSkillRef{
		Source: "skillhub", SkillID: "skill-review-pr", Version: "1.2.3", ContentHash: strings.Repeat("a", 64),
	}
	replace.hub.handleSkillMutationGrant(replace.client, replace.msg)
	result = readSkillMutationGrantResult(t, replace)
	if result.Error != nil || result.Grant == "" {
		t.Fatalf("exact replacement result: %#v", result)
	}

	mismatch := newSkillMutationGrantIssueFixture(t, 7)
	mismatch.msg.Operation = "replace"
	mismatch.msg.ExpectedPreviousHash = strings.Repeat("a", 64)
	mismatch.msg.BeforeReference = &types.BotSkillRef{
		Source: "skillhub", SkillID: "skill-review-pr", Version: "1.2.2", ContentHash: strings.Repeat("a", 64),
	}
	mismatch.hub.handleSkillMutationGrant(mismatch.client, mismatch.msg)
	result = readSkillMutationGrantResult(t, mismatch)
	if result.Error == nil || result.Error.Code != "definition_stale" {
		t.Fatalf("mismatched replacement result: %#v", result)
	}
}

func TestSkillMutationGrantRequiresBothGroupMembers(t *testing.T) {
	fixture := newSkillMutationGrantIssueFixture(t, 8)
	fixture.db.mode = types.BotSkillMutationSharedLive
	fixture.msg.SourceTopicID = "grp_50"
	fixture.db.messages = map[string][]*types.Message{
		"grp_50": {{ID: 99, TopicID: "grp_50", FromUID: 8, MsgType: "text", CreatedAt: fixture.now.Add(-time.Minute)}},
	}
	fixture.db.groupMembers[[2]int64{50, 8}] = true
	fixture.hub.handleSkillMutationGrant(fixture.client, fixture.msg)
	result := readSkillMutationGrantResult(t, fixture)
	if result.Error == nil || result.Error.Code != "forbidden" {
		t.Fatalf("group without Bot membership result: %#v", result)
	}

	allowed := newSkillMutationGrantIssueFixture(t, 8)
	allowed.db.mode = types.BotSkillMutationSharedLive
	allowed.msg.SourceTopicID = "grp_50"
	allowed.db.messages = map[string][]*types.Message{
		"grp_50": {{ID: 99, TopicID: "grp_50", FromUID: 8, MsgType: "text", CreatedAt: allowed.now.Add(-time.Minute)}},
	}
	allowed.db.groupMembers[[2]int64{50, 8}] = true
	allowed.db.groupMembers[[2]int64{50, 42}] = true
	allowed.hub.handleSkillMutationGrant(allowed.client, allowed.msg)
	result = readSkillMutationGrantResult(t, allowed)
	if result.Error != nil || result.Grant == "" {
		t.Fatalf("valid group result: %#v error=%+v", result, result.Error)
	}
}

func TestDeviceConnectorCannotRequestSkillMutationGrant(t *testing.T) {
	msg := &ClientMessage{SkillMutationGrant: &MsgSkillMutationGrant{Type: "request", RequestID: "request-1"}}
	if deviceConnectorMessageAllowed(msg) {
		t.Fatal("device connector was allowed to request a Skill mutation grant")
	}
}
