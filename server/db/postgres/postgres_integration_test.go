package postgres

import (
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
	t.Run("message search decodes legacy attachment filenames", func(t *testing.T) {
		topicID := fmt.Sprintf("grp_%d", groupID)
		wantIDs := make(map[int64]bool)
		for _, content := range []string{
			`{"filename":"R\u0065port.pdf"}`,
			`{"filename":"Q1 \"Final\" Report.pdf"}`,
			`"{\"filename\":\"Escaped Report.pdf\"}"`,
		} {
			messageID, saveErr := db.SaveMessage(topicID, ownerID, content, "file")
			if saveErr != nil {
				t.Fatalf("save legacy file message: %v", saveErr)
			}
			wantIDs[messageID] = true
		}
		results, searchErr := db.SearchMessages(ownerID, "report", store.MessageSearchArtifact, 10)
		if searchErr != nil {
			t.Fatalf("search legacy file messages: %v", searchErr)
		}
		for _, result := range results {
			delete(wantIDs, result.MessageID)
		}
		if len(wantIDs) != 0 {
			t.Fatalf("legacy file search omitted message IDs: %v", wantIDs)
		}
	})
	t.Run("message search matches attachment fields independently", func(t *testing.T) {
		topicID := fmt.Sprintf("grp_%d", groupID)
		if _, saveErr := db.SaveMessageWithBlocks(topicID, ownerID, "split metadata", []types.ContentBlock{{
			Type: "file",
			Name: "Quarterly",
			Payload: map[string]interface{}{
				"title": "Report.pdf",
			},
		}}, "", "", "text"); saveErr != nil {
			t.Fatalf("save split attachment metadata: %v", saveErr)
		}
		wantID, saveErr := db.SaveMessageWithBlocks(topicID, ownerID, "real filename", []types.ContentBlock{{
			Type: "file",
			Name: "Quarterly Report.pdf",
		}}, "", "", "text")
		if saveErr != nil {
			t.Fatalf("save matching attachment: %v", saveErr)
		}

		rows, queryErr := db.db.Query(postgresMessageSearchQuery,
			ownerID, store.MessageSearchArtifact, "quarterly report", 10, 0)
		if queryErr != nil {
			t.Fatalf("query attachment candidates: %v", queryErr)
		}
		results, scanned, scanErr := scanPostgresMessageSearch(rows,
			"quarterly report", store.MessageSearchArtifact, 10)
		closeErr := rows.Close()
		if scanErr != nil {
			t.Fatalf("scan attachment candidates: %v", scanErr)
		}
		if closeErr != nil {
			t.Fatalf("close attachment candidates: %v", closeErr)
		}
		if scanned != 1 || len(results) != 1 || results[0].MessageID != wantID {
			t.Fatalf("attachment candidates scanned=%d results=%#v, want only message %d",
				scanned, results, wantID)
		}
	})
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
	t.Run("project group assignment respects membership", func(t *testing.T) {
		assertProjectGroupAssignmentAccess(t, db, groupID, friendID)
	})
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

func assertProjectGroupAssignmentAccess(t *testing.T, db *Adapter, groupID, memberID int64) {
	t.Helper()

	if err := db.AddGroupMember(groupID, memberID, "member"); err != nil {
		t.Fatalf("add project-assignment group member: %v", err)
	}
	memberProject, err := db.CreateProject(memberID, "Member group project")
	if err != nil {
		t.Fatalf("create group member project: %v", err)
	}
	groupTopicID := fmt.Sprintf("grp_%d", groupID)
	if err := db.AssignTopicToProject(memberID, memberProject.ID, groupTopicID); err != nil {
		t.Fatalf("group member must be allowed to assign group topic: %v", err)
	}
	memberAssignments, err := db.ListProjectTopics(memberID)
	if err != nil {
		t.Fatalf("list group member project assignments: %v", err)
	}
	if len(memberAssignments) != 1 ||
		memberAssignments[0].ProjectID != memberProject.ID ||
		memberAssignments[0].TopicID != groupTopicID {
		t.Fatalf("group member assignment mismatch: %#v", memberAssignments)
	}

	nonMemberID, err := db.CreateUser(&types.User{
		Username:    "project-nonmember",
		Email:       "project-nonmember@example.com",
		DisplayName: "Project Nonmember",
		AccountType: types.AccountHuman,
		PassHash:    []byte("project-nonmember-hash"),
	})
	if err != nil {
		t.Fatalf("create project-assignment nonmember: %v", err)
	}
	nonMemberProject, err := db.CreateProject(nonMemberID, "Nonmember group project")
	if err != nil {
		t.Fatalf("create group nonmember project: %v", err)
	}
	if err := db.AssignTopicToProject(nonMemberID, nonMemberProject.ID, groupTopicID); !errors.Is(err, store.ErrProjectTopicNotFound) {
		t.Fatalf("group nonmember assignment error = %v, want %v", err, store.ErrProjectTopicNotFound)
	}
	nonMemberAssignments, err := db.ListProjectTopics(nonMemberID)
	if err != nil {
		t.Fatalf("list group nonmember project assignments: %v", err)
	}
	if len(nonMemberAssignments) != 0 {
		t.Fatalf("group nonmember must not retain assignments: %#v", nonMemberAssignments)
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

	topicID := fmt.Sprintf("grp_%d", groupID)
	expiry := time.Now().UTC().Add(time.Hour)
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
