package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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

type skillMutationGrantWSTestStore struct {
	*skillMutationGrantIssueTestStore
	mu     sync.Mutex
	apiKey string
	bodyID string
}

func (s *skillMutationGrantWSTestStore) GetBotByAPIKey(apiKey string) (int64, error) {
	if apiKey != s.apiKey {
		return 0, errors.New("not found")
	}
	return 42, nil
}

func (s *skillMutationGrantWSTestStore) EnsureBotBodyBinding(botUID int64, bodyID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if botUID != 42 {
		return "", false, errors.New("bot not found")
	}
	if s.bodyID == "" {
		s.bodyID = bodyID
	}
	return s.bodyID, s.bodyID == bodyID, nil
}

func (s *skillMutationGrantWSTestStore) SetBotBodyBinding(botUID int64, bodyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if botUID != 42 {
		return errors.New("bot not found")
	}
	s.bodyID = bodyID
	return nil
}

func (s *skillMutationGrantWSTestStore) GetBotBodyID(botUID int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if botUID != 42 {
		return "", errors.New("bot not found")
	}
	return s.bodyID, nil
}

func (s *skillMutationGrantWSTestStore) GetFriends(uid int64) ([]*types.User, error) {
	return nil, nil
}

func (s *skillMutationGrantWSTestStore) GetUserGroups(uid int64) ([]*types.Group, error) {
	return nil, nil
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
	runtimeSigner, err := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	hub.botRuntimeCredentials = runtimeSigner
	hub.bodyLeases.now = func() time.Time { return now }
	client := &Client{
		hub: hub, uid: 42, accountType: types.AccountBot, bodyID: "body-prod-1", installationID: "install-prod-1",
		connectionID: "conn-1", send: make(chan []byte, 8),
	}
	_, runtimeClaims, err := runtimeSigner.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: client.bodyID, InstallationID: client.installationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.botRuntimeCredential = runtimeClaims
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

func TestSkillMutationGrantRequiresOwnerIssuedRuntimeCredential(t *testing.T) {
	missing := newSkillMutationGrantIssueFixture(t, 7)
	missing.client.botRuntimeCredential = nil
	missing.hub.handleSkillMutationGrant(missing.client, missing.msg)
	result := readSkillMutationGrantResult(t, missing)
	if result.Error == nil || result.Error.Code != "runtime_credential_required" || result.Grant != "" {
		t.Fatalf("missing Runtime credential result: %#v", result)
	}

	mismatchedOwner := newSkillMutationGrantIssueFixture(t, 7)
	mismatchedOwner.client.botRuntimeCredential.OwnerUID = 8
	mismatchedOwner.hub.handleSkillMutationGrant(mismatchedOwner.client, mismatchedOwner.msg)
	result = readSkillMutationGrantResult(t, mismatchedOwner)
	if result.Error == nil || result.Error.Code != "runtime_credential_required" || result.Grant != "" {
		t.Fatalf("mismatched Runtime credential owner result: %#v", result)
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

func TestWebSocketAPIKeyOnlyCannotRequestSkillMutationGrant(t *testing.T) {
	fixture := newSkillMutationGrantIssueFixture(t, 7)
	apiKey := GenerateAPIKey(42)
	db := &skillMutationGrantWSTestStore{skillMutationGrantIssueTestStore: fixture.db, apiKey: apiKey}
	hub := NewHub(db, nil)
	grantSigner, _ := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return fixture.now })
	runtimeSigner, _ := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return fixture.now })
	hub.skillMutationGrants = grantSigner
	hub.botRuntimeCredentials = runtimeSigner
	hub.bodyLeases.now = func() time.Time { return fixture.now }
	go hub.Run()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWS(hub, w, r)
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	apiKeyOnly, response, err := dialSkillMutationRuntime(wsURL, apiKey, "self-reported-body", "self-reported-install", "")
	closeResponse(response)
	if err != nil {
		t.Fatalf("API-key-only websocket dial failed: %v", err)
	}
	if err := apiKeyOnly.WriteJSON(&ClientMessage{SkillMutationGrant: fixture.msg}); err != nil {
		apiKeyOnly.Close()
		t.Fatal(err)
	}
	var denied ServerMessage
	if err := apiKeyOnly.ReadJSON(&denied); err != nil {
		apiKeyOnly.Close()
		t.Fatal(err)
	}
	if denied.SkillMutationGrant == nil || denied.SkillMutationGrant.Error == nil ||
		denied.SkillMutationGrant.Error.Code != "runtime_credential_required" || denied.SkillMutationGrant.Grant != "" {
		apiKeyOnly.Close()
		t.Fatalf("API-key-only grant result: %#v", denied.SkillMutationGrant)
	}
	apiKeyOnly.Close()
	waitForClientCount(t, hub, 42, 0)

	rawCredential, _, err := runtimeSigner.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: "trusted-body", InstallationID: "trusted-install",
	})
	if err != nil {
		t.Fatal(err)
	}
	mismatched, response, err := dialSkillMutationRuntime(wsURL, apiKey, "other-body", "trusted-install", rawCredential)
	if mismatched != nil {
		mismatched.Close()
	}
	closeResponse(response)
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched Runtime credential status=%v err=%v, want 403", responseStatus(response), err)
	}
	trusted, response, err := dialSkillMutationRuntime(wsURL, apiKey, "trusted-body", "trusted-install", rawCredential)
	closeResponse(response)
	if err != nil {
		t.Fatalf("trusted Runtime websocket dial failed: %v", err)
	}
	defer trusted.Close()
	if err := trusted.WriteJSON(&ClientMessage{SkillMutationGrant: fixture.msg}); err != nil {
		t.Fatal(err)
	}
	var allowed ServerMessage
	if err := trusted.ReadJSON(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed.SkillMutationGrant == nil || allowed.SkillMutationGrant.Error != nil || allowed.SkillMutationGrant.Grant == "" {
		t.Fatalf("trusted Runtime grant result: %#v", allowed.SkillMutationGrant)
	}
}

func dialSkillMutationRuntime(wsURL, apiKey, bodyID, installationID, runtimeCredential string) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{}
	headers.Set("X-API-Key", apiKey)
	headers.Set(botBodyIDHeader, bodyID)
	headers.Set(botInstallationIDHeader, installationID)
	if runtimeCredential != "" {
		headers.Set(botRuntimeCredentialHeader, runtimeCredential)
	}
	return websocket.DefaultDialer.Dial(wsURL, headers)
}
