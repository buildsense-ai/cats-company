package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresStoreContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_test_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open schema postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema should be idempotent: %v", err)
	}
	var migrationVersion int64
	var migrationDirty bool
	if err := db.db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&migrationVersion, &migrationDirty); err != nil {
		t.Fatalf("query schema migration baseline: %v", err)
	}
	if migrationVersion != 1 || migrationDirty {
		t.Fatalf("schema migration baseline mismatch: version=%d dirty=%v", migrationVersion, migrationDirty)
	}
	if health := db.HealthCheck(); health["status"] != "healthy" {
		t.Fatalf("expected healthy database, got %#v", health)
	}

	ownerID, err := db.CreateUser(&types.User{
		Username:    "Alice",
		Email:       "Alice@Example.com",
		DisplayName: "Alice",
		AccountType: types.AccountHuman,
		PassHash:    []byte("owner-hash"),
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	const pushSubscriptionLimit = 10
	pushResults := make(chan bool, pushSubscriptionLimit+2)
	pushErrors := make(chan error, pushSubscriptionLimit+2)
	var pushWG sync.WaitGroup
	for index := 0; index < pushSubscriptionLimit+2; index++ {
		pushWG.Add(1)
		go func(index int) {
			defer pushWG.Done()
			stored, err := db.UpsertPushSubscription(context.Background(), &types.PushSubscription{
				UID:      ownerID,
				Endpoint: fmt.Sprintf("https://push.example.test/subscription/%d", index),
				P256DH:   "p256dh",
				Auth:     "auth",
			}, pushSubscriptionLimit)
			if err != nil {
				pushErrors <- err
				return
			}
			pushResults <- stored
		}(index)
	}
	pushWG.Wait()
	close(pushErrors)
	for err := range pushErrors {
		t.Fatalf("concurrent push subscription upsert: %v", err)
	}
	close(pushResults)
	storedCount := 0
	for stored := range pushResults {
		if stored {
			storedCount++
		}
	}
	if storedCount != pushSubscriptionLimit {
		t.Fatalf("stored push subscriptions = %d, want %d", storedCount, pushSubscriptionLimit)
	}
	pushSubscriptions, err := db.ListPushSubscriptions(context.Background(), ownerID)
	if err != nil || len(pushSubscriptions) != pushSubscriptionLimit {
		t.Fatalf("list push subscriptions: count=%d err=%v", len(pushSubscriptions), err)
	}
	stored, err := db.UpsertPushSubscription(context.Background(), pushSubscriptions[0], pushSubscriptionLimit)
	if err != nil || !stored {
		t.Fatalf("refresh existing push subscription: stored=%t err=%v", stored, err)
	}
	pushSubscriptions[0].RegistrationID = "session-old"
	if stored, err = db.UpsertPushSubscription(context.Background(), pushSubscriptions[0], pushSubscriptionLimit); err != nil || !stored {
		t.Fatalf("register old push generation: stored=%t err=%v", stored, err)
	}
	pushSubscriptions[0].RegistrationID = "session-new"
	if stored, err = db.UpsertPushSubscription(context.Background(), pushSubscriptions[0], pushSubscriptionLimit); err != nil || !stored {
		t.Fatalf("register new push generation: stored=%t err=%v", stored, err)
	}
	if err := db.DeletePushSubscription(context.Background(), ownerID, pushSubscriptions[0].Endpoint, "session-old"); err != nil {
		t.Fatalf("delete old push generation: %v", err)
	}
	if err := db.DeletePushSubscription(context.Background(), ownerID, pushSubscriptions[0].Endpoint, ""); err != nil {
		t.Fatalf("delete empty push generation: %v", err)
	}
	remainingPushSubscriptions, err := db.ListPushSubscriptions(context.Background(), ownerID)
	if err != nil || len(remainingPushSubscriptions) != pushSubscriptionLimit {
		t.Fatalf("stale delete removed new push generation: count=%d err=%v", len(remainingPushSubscriptions), err)
	}
	if remainingPushSubscriptions[0].RegistrationID != "session-new" {
		t.Fatalf("new push generation changed: %+v", remainingPushSubscriptions[0])
	}
	owner, err := db.GetUserByUsername("alice")
	if err != nil || owner == nil || owner.ID != ownerID {
		t.Fatalf("case-insensitive username lookup failed: owner=%#v err=%v", owner, err)
	}
	ownerByEmail, err := db.GetUserByEmail("alice@example.com")
	if err != nil || ownerByEmail == nil || ownerByEmail.ID != ownerID {
		t.Fatalf("case-insensitive email lookup failed: owner=%#v err=%v", ownerByEmail, err)
	}
	if _, err := db.CreateUser(&types.User{
		Username:    "alice",
		Email:       "other@example.com",
		DisplayName: "Duplicate Alice",
		AccountType: types.AccountHuman,
		PassHash:    []byte("hash"),
	}); err == nil {
		t.Fatalf("expected duplicate username with different case to fail")
	}

	friendID, err := db.CreateUser(&types.User{
		Username:    "bob",
		Email:       "bob@example.com",
		DisplayName: "Bob",
		AccountType: types.AccountHuman,
		PassHash:    []byte("friend-hash"),
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	if _, err := db.CreateFriendRequest(ownerID, friendID, "hi"); err != nil {
		t.Fatalf("create friend request: %v", err)
	}
	if err := db.AcceptFriendRequest(ownerID, friendID); err != nil {
		t.Fatalf("accept friend request: %v", err)
	}
	areFriends, err := db.AreFriends(friendID, ownerID)
	if err != nil || !areFriends {
		t.Fatalf("expected reverse friendship, areFriends=%v err=%v", areFriends, err)
	}
	uidSearchResults, err := db.SearchUsers(fmt.Sprintf("%d", friendID), 10)
	if err != nil {
		t.Fatalf("search users by uid: %v", err)
	}
	if len(uidSearchResults) == 0 || uidSearchResults[0].ID != friendID {
		t.Fatalf("uid search mismatch: got=%#v want=%d", uidSearchResults, friendID)
	}

	topicID := "p2p_test"
	if err := db.CreateTopic(topicID, "p2p", ownerID); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if _, err := db.SaveMessage(topicID, ownerID, "hello", "text"); err != nil {
		t.Fatalf("save message: %v", err)
	}
	if _, err := db.SaveMessageWithBlocks(topicID, friendID, "with blocks", []types.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "file", Payload: map[string]interface{}{"name": "a.txt", "size": float64(3)}},
	}, "normal", "assistant", "text"); err != nil {
		t.Fatalf("save message with blocks: %v", err)
	}
	latest, err := db.GetLatestMessages(topicID, 10, 0)
	if err != nil || len(latest) != 2 || len(latest[1].ContentBlocks) != 2 {
		t.Fatalf("latest messages mismatch: len=%d msg=%#v err=%v", len(latest), latest, err)
	}
	perTopic, err := db.GetLatestMessagesForTopics([]string{topicID})
	if err != nil || perTopic[topicID] == nil {
		t.Fatalf("latest per topic mismatch: %#v err=%v", perTopic, err)
	}

	groupID, err := db.CreateGroup("Test Group", ownerID)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	members, err := db.GetGroupMembers(groupID)
	if err != nil || len(members) != 1 || members[0].UserID != ownerID {
		t.Fatalf("group members mismatch: %#v err=%v", members, err)
	}

	botID, err := db.CreateUser(&types.User{
		Username:    "helperbot",
		DisplayName: "Helper Bot",
		AccountType: types.AccountBot,
		PassHash:    []byte("bot-hash"),
	})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	if err := db.AddGroupMember(groupID, botID, "member"); err != nil {
		t.Fatalf("add bot group member: %v", err)
	}
	members, err = db.GetGroupMembers(groupID)
	if err != nil {
		t.Fatalf("get group members with bot: %v", err)
	}
	var botMember *types.GroupMember
	for _, member := range members {
		if member.UserID == botID {
			botMember = member
			break
		}
	}
	if botMember == nil || !botMember.IsBot {
		t.Fatalf("bot group member must disclose is_bot: %#v", botMember)
	}
	if err := db.SaveBotConfigWithOwner(botID, ownerID, "https://bot.example", "catsco-test"); err != nil {
		t.Fatalf("save bot config: %v", err)
	}
	if err := db.SaveAPIKey(botID, "cc_test_key"); err != nil {
		t.Fatalf("save api key: %v", err)
	}
	foundBotID, err := db.GetBotByAPIKey("cc_test_key")
	if err != nil || foundBotID != botID {
		t.Fatalf("get bot by api key mismatch: got=%d want=%d err=%v", foundBotID, botID, err)
	}
	if err := db.SetBotVisibility(botID, "private"); err != nil {
		t.Fatalf("set bot visibility: %v", err)
	}
	assertConversationTaskStatusAggregation(t, db, groupID, botID)
	nativeIdentity := &types.ChannelNativeGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_test", TenantKey: "tenant_test",
		ConversationID: "oc_event_order", ConversationName: "飞书｜事件顺序", OperatorChannelUserID: "ou_owner",
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(nativeIdentity, true, "evt_add", 1000); err != nil || !applied {
		t.Fatalf("first native-group add must apply: applied=%v err=%v", applied, err)
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(nativeIdentity, true, "evt_add", 1000); !errors.Is(err, store.ErrChannelNativeGroupEventBusy) || applied {
		t.Fatalf("in-flight native-group add must report busy: applied=%v err=%v", applied, err)
	}
	if _, err := db.db.Exec(
		`UPDATE channel_native_groups SET last_event_claimed_at = 0
		 WHERE channel = 'feishu' AND channel_app_id = 'cli_test' AND tenant_key = 'tenant_test' AND conversation_id = 'oc_event_order'`,
	); err != nil {
		t.Fatalf("expire native-group event claim: %v", err)
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(nativeIdentity, true, "evt_add", 1000); err != nil || !applied {
		t.Fatalf("expired pending native-group add must be retryable: applied=%v err=%v", applied, err)
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(nativeIdentity, false, "evt_delete", 1000); err != nil || !applied {
		t.Fatalf("same-time native-group delete must win: applied=%v err=%v", applied, err)
	}
	if applied, _, err := db.ApplyChannelNativeGroupMembershipEvent(nativeIdentity, true, "evt_add_late", 1000); err != nil || applied {
		t.Fatalf("same-time add must not override delete: applied=%v err=%v", applied, err)
	}
	nativeBinding, err := db.ResolveChannelNativeGroup("feishu", "cli_test", "tenant_test", "oc_event_order")
	if err != nil || nativeBinding == nil || nativeBinding.Status != types.ChannelNativeGroupDisconnected {
		t.Fatalf("native-group event order mismatch: binding=%+v err=%v", nativeBinding, err)
	}
	selectionGroup := &types.ChannelGroupBinding{
		Channel: "feishu", ChannelAppID: "cli_test", ChannelUserID: "ou_route_race", ChannelConversationType: "p2p",
		ActorUID: ownerID, CanonicalUID: ownerID, GroupID: groupID, TopicID: fmt.Sprintf("grp_%d", groupID),
	}
	selectionAgent := &types.ChannelAgentRoute{
		Channel: "feishu", ChannelAppID: "cli_test", ChannelUserID: "ou_route_race", ChannelConversationType: "p2p",
		ActorUID: ownerID, AgentUID: botID, Source: "contract_test",
	}
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel: "feishu", ChannelAppID: "cli_test", ChannelUserID: "ou_route_race", ChannelConversationType: "p2p",
		ActorUID: ownerID, CanonicalUID: ownerID, OwnerUID: ownerID, AgentUID: botID, Status: types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed channel route binding: %v", err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := db.UpsertChannelGroupBinding(selectionGroup)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := db.UpsertChannelAgentRoute(selectionAgent)
			errs <- err
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent channel selection attempt %d: %v", attempt, err)
			}
		}
		var activeGroups, agentRoutes int
		if err := db.db.QueryRow(
			`SELECT COUNT(*) FROM channel_group_bindings
			 WHERE channel = 'feishu' AND channel_app_id = 'cli_test' AND channel_user_id = 'ou_route_race'
			   AND channel_conversation_id = '' AND channel_conversation_type = 'p2p' AND status = 'active'`,
		).Scan(&activeGroups); err != nil {
			t.Fatalf("count active group selections: %v", err)
		}
		if err := db.db.QueryRow(
			`SELECT COUNT(*) FROM channel_agent_routes
			 WHERE channel = 'feishu' AND channel_app_id = 'cli_test' AND channel_user_id = 'ou_route_race'
			   AND channel_conversation_id = '' AND channel_conversation_type = 'p2p'`,
		).Scan(&agentRoutes); err != nil {
			t.Fatalf("count agent route selections: %v", err)
		}
		if activeGroups+agentRoutes != 1 {
			t.Fatalf("channel selection must stay exclusive after attempt %d: groups=%d routes=%d", attempt, activeGroups, agentRoutes)
		}
	}
	privateSelections, err := db.ListChannelPrivateSelections(ownerID, "feishu")
	if err != nil {
		t.Fatalf("list private selections: %v", err)
	}
	var currentPrivate *types.ChannelPrivateSelection
	for _, selection := range privateSelections {
		if selection.ChannelAppID == "cli_test" && selection.ChannelUserID == "ou_route_race" &&
			(currentPrivate == nil || selection.SelectedAt.After(currentPrivate.SelectedAt) ||
				(selection.SelectedAt.Equal(currentPrivate.SelectedAt) && selection.TargetKind == types.ChannelPrivateTargetGroup && currentPrivate.TargetKind == types.ChannelPrivateTargetAgent)) {
			currentPrivate = selection
		}
	}
	if currentPrivate == nil {
		t.Fatal("current private selection not found")
	}
	unbound, err := db.RevokeChannelPrivateSelection(ownerID, currentPrivate)
	if err != nil || unbound == nil || !unbound.Revoked || unbound.Changed {
		t.Fatalf("revoke private selection: result=%+v err=%v", unbound, err)
	}
	var remainingRoutes, remainingGroups, remainingBindings int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM channel_agent_routes WHERE channel = 'feishu' AND channel_app_id = 'cli_test' AND channel_user_id = 'ou_route_race' AND channel_conversation_type = 'p2p'`).Scan(&remainingRoutes); err != nil {
		t.Fatalf("count remaining private routes: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM channel_group_bindings WHERE channel = 'feishu' AND channel_app_id = 'cli_test' AND channel_user_id = 'ou_route_race' AND channel_conversation_type = 'p2p' AND status = 'active'`).Scan(&remainingGroups); err != nil {
		t.Fatalf("count remaining private groups: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM channel_agent_bindings WHERE channel = 'feishu' AND channel_app_id = 'cli_test' AND channel_user_id = 'ou_route_race' AND channel_conversation_type = 'p2p' AND status = 'active'`).Scan(&remainingBindings); err != nil {
		t.Fatalf("count remaining private bindings: %v", err)
	}
	if remainingRoutes != 0 || remainingGroups != 0 || remainingBindings != 0 {
		t.Fatalf("private selection not fully revoked: routes=%d groups=%d bindings=%d", remainingRoutes, remainingGroups, remainingBindings)
	}
	searchResults, err := db.SearchUsers("helper", 10)
	if err != nil {
		t.Fatalf("search users: %v", err)
	}
	for _, result := range searchResults {
		if result.ID == botID {
			t.Fatalf("private bot should not appear in search results: %#v", searchResults)
		}
	}

	if _, err := db.CreateFeedbackReport(&types.FeedbackReport{
		UserID:      ownerID,
		Category:    "suggestion",
		Title:       "PG test",
		Description: "test feedback",
		Attachments: []types.FeedbackAttachment{{FileKey: "file-key", URL: "/uploads/a.png", Name: "a.png"}},
	}); err != nil {
		t.Fatalf("create feedback report: %v", err)
	}
}

func assertConversationTaskStatusAggregation(t *testing.T, db *Adapter, groupID, firstBotID int64) {
	t.Helper()
	secondBotID, err := db.CreateUser(&types.User{
		Username:    "statusbot",
		DisplayName: "Status Bot",
		AccountType: types.AccountBot,
		PassHash:    []byte("status-bot-hash"),
	})
	if err != nil {
		t.Fatalf("create second task status bot: %v", err)
	}
	assertPostgresConversationTaskStatusUpgradeHandoff(t, db, firstBotID)

	topicID := fmt.Sprintf("grp_%d", groupID)
	expiry := time.Now().UTC().Add(time.Hour)
	legacyTopicID := topicID
	legacySourceUpdatedAt := time.Now().UTC().Add(-time.Hour)
	legacyAggregateUpdatedAt := legacySourceUpdatedAt.Add(30 * time.Minute)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES ($1, $2, 'older-run', 'completed', 'older completed', '', NULL, $3)`,
		legacyTopicID,
		firstBotID,
		legacySourceUpdatedAt,
	); err != nil {
		t.Fatalf("seed older conversation task source status: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES ($1, 'legacy-run', 'running', 'legacy running', '', $2, $3, $4)`,
		legacyTopicID,
		firstBotID,
		expiry,
		legacyAggregateUpdatedAt,
	); err != nil {
		t.Fatalf("seed legacy conversation task status: %v", err)
	}
	legacyAggregate, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID:   legacyTopicID,
		RunID:     "new-source-run",
		State:     "completed",
		Summary:   "new source completed",
		SourceUID: secondBotID,
	})
	if err != nil {
		t.Fatalf("upsert new source alongside legacy status: %v", err)
	}
	if legacyAggregate.State != "running" || legacyAggregate.SourceUID != firstBotID {
		t.Fatalf("new source overwrote active legacy aggregate: %+v", legacyAggregate)
	}
	legacySource, err := db.GetConversationTaskStatusForSource(legacyTopicID, firstBotID)
	if err != nil || legacySource == nil || legacySource.RunID != "legacy-run" || legacySource.State != "running" {
		t.Fatalf("legacy source was not preserved: status=%+v err=%v", legacySource, err)
	}
	if _, err := db.db.Exec(
		`DELETE FROM conversation_task_status_sources WHERE topic_id = $1`,
		legacyTopicID,
	); err != nil {
		t.Fatalf("clean up legacy task status sources: %v", err)
	}
	if _, err := db.db.Exec(
		`DELETE FROM conversation_task_statuses WHERE topic_id = $1`,
		legacyTopicID,
	); err != nil {
		t.Fatalf("clean up legacy task status aggregate: %v", err)
	}

	mixedVersionTopicID := topicID
	activeSourceUpdatedAt := time.Now().UTC().Add(-time.Hour)
	staleAggregateUpdatedAt := activeSourceUpdatedAt.Add(30 * time.Minute)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES ($1, $2, 'new-run', 'running', 'new run active', '', $3, $4)`,
		mixedVersionTopicID,
		firstBotID,
		expiry,
		activeSourceUpdatedAt,
	); err != nil {
		t.Fatalf("seed active new-run source status: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES ($1, 'old-run', 'completed', 'old run completed', '', $2, NULL, $3)`,
		mixedVersionTopicID,
		firstBotID,
		staleAggregateUpdatedAt,
	); err != nil {
		t.Fatalf("seed stale terminal aggregate: %v", err)
	}
	mixedVersionAggregate, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID:   mixedVersionTopicID,
		RunID:     "other-source-run",
		State:     "completed",
		Summary:   "other source completed",
		SourceUID: secondBotID,
	})
	if err != nil {
		t.Fatalf("upsert alongside mixed-version task status: %v", err)
	}
	if mixedVersionAggregate.State != "running" || mixedVersionAggregate.RunID != "new-run" || mixedVersionAggregate.SourceUID != firstBotID {
		t.Fatalf("stale terminal aggregate overwrote active new run: %+v", mixedVersionAggregate)
	}
	activeSource, err := db.GetConversationTaskStatusForSource(mixedVersionTopicID, firstBotID)
	if err != nil || activeSource == nil || activeSource.RunID != "new-run" || activeSource.State != "running" {
		t.Fatalf("active new-run source was not preserved: status=%+v err=%v", activeSource, err)
	}
	if _, err := db.db.Exec(
		`DELETE FROM conversation_task_status_sources WHERE topic_id = $1`,
		mixedVersionTopicID,
	); err != nil {
		t.Fatalf("clean up mixed-version task status sources: %v", err)
	}
	if _, err := db.db.Exec(
		`DELETE FROM conversation_task_statuses WHERE topic_id = $1`,
		mixedVersionTopicID,
	); err != nil {
		t.Fatalf("clean up mixed-version task status aggregate: %v", err)
	}

	readOnlyTimestamp := time.Now().UTC().Add(-time.Hour)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES ($1, $2, 'read-run', 'running', 'running before old write', '', $3, $4)`,
		topicID,
		firstBotID,
		expiry,
		readOnlyTimestamp,
	); err != nil {
		t.Fatalf("seed PostgreSQL source before legacy-only write: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES ($1, 'read-run', 'running', 'running aggregate', '', $2, $3, $4)`,
		topicID,
		firstBotID,
		expiry,
		readOnlyTimestamp,
	); err != nil {
		t.Fatalf("seed PostgreSQL legacy aggregate: %v", err)
	}
	legacyTx, err := db.db.Begin()
	if err != nil {
		t.Fatalf("begin PostgreSQL legacy writer transaction: %v", err)
	}
	defer legacyTx.Rollback()
	var legacyTransactionStartedAt time.Time
	if err := legacyTx.QueryRow(`SELECT CURRENT_TIMESTAMP`).Scan(&legacyTransactionStartedAt); err != nil {
		t.Fatalf("capture PostgreSQL legacy transaction timestamp: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := db.db.Exec(
		`UPDATE conversation_task_status_sources SET summary = 'source updated after old transaction began'
		 WHERE topic_id = $1 AND source_uid = $2`,
		topicID,
		firstBotID,
	); err != nil {
		t.Fatalf("refresh PostgreSQL source after legacy transaction began: %v", err)
	}
	if _, err := legacyTx.Exec(
		`UPDATE conversation_task_statuses SET
		   state = 'completed', summary = 'completed by old node', expires_at = NULL
		 WHERE topic_id = $1`,
		topicID,
	); err != nil {
		t.Fatalf("update PostgreSQL legacy aggregate from older transaction: %v", err)
	}
	if err := legacyTx.Commit(); err != nil {
		t.Fatalf("commit PostgreSQL legacy aggregate update: %v", err)
	}
	readOnlySource, err := db.GetConversationTaskStatusForSource(topicID, firstBotID)
	if err != nil || readOnlySource == nil || readOnlySource.State != "completed" {
		t.Fatalf("PostgreSQL legacy-only write was not visible from source read: status=%+v err=%v", readOnlySource, err)
	}
	readOnlyAggregates, err := db.GetConversationTaskStatuses([]string{topicID})
	if err != nil || readOnlyAggregates[topicID] == nil || readOnlyAggregates[topicID].State != "completed" {
		t.Fatalf("PostgreSQL legacy-only write was not visible from aggregate read: statuses=%+v err=%v", readOnlyAggregates, err)
	}
	if _, err := db.db.Exec(`DELETE FROM conversation_task_status_sources WHERE topic_id = $1`, topicID); err != nil {
		t.Fatalf("clean up legacy-only read source: %v", err)
	}
	if _, err := db.db.Exec(`DELETE FROM conversation_task_statuses WHERE topic_id = $1`, topicID); err != nil {
		t.Fatalf("clean up legacy-only read aggregate: %v", err)
	}

	upsert := func(sourceUID int64, runID, state string) *types.ConversationTaskStatus {
		t.Helper()
		status, upsertErr := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
			TopicID:   topicID,
			RunID:     runID,
			State:     state,
			Summary:   state,
			SourceUID: sourceUID,
			ExpiresAt: func() *time.Time {
				if state == "running" {
					return &expiry
				}
				return nil
			}(),
		})
		if upsertErr != nil {
			t.Fatalf("upsert task status source=%d state=%s: %v", sourceUID, state, upsertErr)
		}
		return status
	}

	upsert(firstBotID, "run-first", "running")
	upsert(secondBotID, "run-second", "running")
	aggregate := upsert(firstBotID, "run-first", "completed")
	if aggregate.State != "running" || aggregate.SourceUID != secondBotID {
		t.Fatalf("first completion must preserve second active source: %+v", aggregate)
	}
	firstSource, err := db.GetConversationTaskStatusForSource(topicID, firstBotID)
	if err != nil || firstSource == nil || firstSource.State != "completed" {
		t.Fatalf("first source status mismatch: status=%+v err=%v", firstSource, err)
	}

	aggregate = upsert(secondBotID, "run-second", "completed")
	if aggregate.State != "completed" {
		t.Fatalf("all completed aggregate mismatch: %+v", aggregate)
	}

	upsert(firstBotID, "run-first-race", "running")
	upsert(secondBotID, "run-second-race", "running")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range []struct {
		uid   int64
		runID string
	}{
		{uid: firstBotID, runID: "run-first-race"},
		{uid: secondBotID, runID: "run-second-race"},
	} {
		wg.Add(1)
		go func(uid int64, runID string) {
			defer wg.Done()
			<-start
			_, updateErr := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
				TopicID: topicID, RunID: runID, State: "completed", Summary: "completed", SourceUID: uid,
			})
			errs <- updateErr
		}(item.uid, item.runID)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent task completion: %v", err)
		}
	}

	aggregates, err := db.GetConversationTaskStatuses([]string{topicID})
	if err != nil {
		t.Fatalf("load task status aggregate: %v", err)
	}
	if aggregate = aggregates[topicID]; aggregate == nil || aggregate.State != "completed" {
		t.Fatalf("concurrent completions left stale aggregate: %+v", aggregate)
	}

	start = make(chan struct{})
	type overlappingRunResult struct {
		runID string
		err   error
	}
	overlapResults := make(chan overlappingRunResult, 2)
	for _, runID := range []string{"overlap-a", "overlap-b"} {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			<-start
			runExpiry := time.Now().UTC().Add(time.Hour)
			_, updateErr := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
				TopicID: topicID, RunID: runID, State: "running", Summary: "running",
				SourceUID: firstBotID, ExpiresAt: &runExpiry,
			})
			overlapResults <- overlappingRunResult{runID: runID, err: updateErr}
		}(runID)
	}
	close(start)
	wg.Wait()
	close(overlapResults)

	successfulRunID := ""
	rejectedRuns := 0
	for result := range overlapResults {
		if result.err == nil {
			if successfulRunID != "" {
				t.Fatalf("two overlapping runs were accepted: %s and %s", successfulRunID, result.runID)
			}
			successfulRunID = result.runID
		} else {
			rejectedRuns++
		}
	}
	if successfulRunID == "" || rejectedRuns != 1 {
		t.Fatalf("overlapping run results: successful=%q rejected=%d", successfulRunID, rejectedRuns)
	}
	currentSource, err := db.GetConversationTaskStatusForSource(topicID, firstBotID)
	if err != nil || currentSource == nil || currentSource.RunID != successfulRunID || currentSource.State != "running" {
		t.Fatalf("accepted overlapping run was not preserved: status=%+v err=%v", currentSource, err)
	}
	upsert(firstBotID, successfulRunID, "completed")
}

func assertPostgresConversationTaskStatusUpgradeHandoff(t *testing.T, db *Adapter, sourceUID int64) {
	t.Helper()
	const staleTopicID = "grp_pg_upgrade_stale"
	const protectedTopicID = "grp_pg_upgrade_protected"
	for _, topicID := range []string{staleTopicID, protectedTopicID} {
		if err := db.CreateTopic(topicID, "group", sourceUID); err != nil {
			t.Fatalf("create PostgreSQL upgrade handoff topic %s: %v", topicID, err)
		}
	}
	if _, err := db.db.Exec(`DROP TRIGGER trg_conversation_task_statuses_sync_source ON conversation_task_statuses`); err != nil {
		t.Fatalf("drop PostgreSQL task status trigger: %v", err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at)
		 VALUES ($1, $2, 'same-run', 'running', 'stale source', '', $3)`,
		staleTopicID, sourceUID, expiry,
	); err != nil {
		t.Fatalf("seed stale PostgreSQL upgrade source: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid)
		 VALUES ($1, 'same-run', 'completed', 'aggregate completed', '', $2)`,
		staleTopicID, sourceUID,
	); err != nil {
		t.Fatalf("seed completed PostgreSQL upgrade aggregate: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at)
		 VALUES ($1, $2, 'new-run', 'running', 'new run active', '', $3)`,
		protectedTopicID, sourceUID, expiry,
	); err != nil {
		t.Fatalf("seed protected PostgreSQL upgrade source: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid)
		 VALUES ($1, 'old-run', 'completed', 'old run completed', '', $2)`,
		protectedTopicID, sourceUID,
	); err != nil {
		t.Fatalf("seed protected PostgreSQL upgrade aggregate: %v", err)
	}

	migration, err := os.ReadFile("../migrations/postgres/000009_sync_legacy_conversation_task_status_sources.up.sql")
	if err != nil {
		t.Fatalf("read PostgreSQL task status upgrade migration: %v", err)
	}
	if _, err := db.db.Exec(string(migration)); err != nil {
		t.Fatalf("run PostgreSQL upgrade handoff migration: %v", err)
	}

	staleSource, err := db.GetConversationTaskStatusForSource(staleTopicID, sourceUID)
	if err != nil || staleSource == nil || staleSource.State != "completed" || staleSource.Summary != "aggregate completed" {
		t.Fatalf("PostgreSQL upgrade did not reconcile stale source: status=%+v err=%v", staleSource, err)
	}
	protectedSource, err := db.GetConversationTaskStatusForSource(protectedTopicID, sourceUID)
	if err != nil || protectedSource == nil || protectedSource.RunID != "new-run" || protectedSource.State != "running" {
		t.Fatalf("PostgreSQL upgrade overwrote protected active source: status=%+v err=%v", protectedSource, err)
	}
}

func dsnWithSearchPath(t *testing.T, rawDSN, schemaName string) string {
	t.Helper()
	parsed, err := url.Parse(rawDSN)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("CATS_PG_TEST_DSN must be a postgres URL DSN: %v", err)
	}
	q := parsed.Query()
	q.Set("search_path", schemaName)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
