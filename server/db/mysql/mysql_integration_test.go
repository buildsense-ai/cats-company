package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store/types"
)

func TestMySQLStoreContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_MYSQL_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}

	config, err := mysqldriver.ParseDSN(rawDSN)
	if err != nil {
		t.Fatalf("parse MySQL test DSN: %v", err)
	}
	schemaName := fmt.Sprintf("cats_test_%d", time.Now().UnixNano())
	adminConfig := *config
	adminConfig.DBName = ""
	adminDB, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatalf("open MySQL admin connection: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec("CREATE DATABASE `" + schemaName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatalf("create MySQL test database: %v", err)
	}
	defer adminDB.Exec("DROP DATABASE `" + schemaName + "`")

	testConfig := *config
	testConfig.DBName = schemaName
	db := &Adapter{}
	if err := db.Open(testConfig.FormatDSN()); err != nil {
		t.Fatalf("open MySQL test database: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create MySQL schema: %v", err)
	}

	firstBotID, err := db.CreateUser(&types.User{
		Username:    "mysqlstatusbot1",
		DisplayName: "MySQL Status Bot 1",
		AccountType: types.AccountBot,
		PassHash:    []byte("mysql-status-bot-1"),
	})
	if err != nil {
		t.Fatalf("create first MySQL task status bot: %v", err)
	}
	secondBotID, err := db.CreateUser(&types.User{
		Username:    "mysqlstatusbot2",
		DisplayName: "MySQL Status Bot 2",
		AccountType: types.AccountBot,
		PassHash:    []byte("mysql-status-bot-2"),
	})
	if err != nil {
		t.Fatalf("create second MySQL task status bot: %v", err)
	}

	assertMySQLPushSubscriptionGenerations(t, db, firstBotID)
	assertMySQLConversationTaskStatusUpgradeHandoff(t, db, firstBotID)
	assertMySQLConversationTaskStatusGeneration(t, db, firstBotID, secondBotID)
}

func assertMySQLConversationTaskStatusUpgradeHandoff(t *testing.T, db *Adapter, sourceUID int64) {
	t.Helper()
	const staleTopicID = "grp_mysql_upgrade_stale"
	const protectedTopicID = "grp_mysql_upgrade_protected"
	for _, topicID := range []string{staleTopicID, protectedTopicID} {
		if err := db.CreateTopic(topicID, "group", sourceUID); err != nil {
			t.Fatalf("create MySQL upgrade handoff topic %s: %v", topicID, err)
		}
	}
	for _, triggerName := range []string{
		"trg_conversation_task_statuses_sync_source_insert",
		"trg_conversation_task_statuses_sync_source_update",
	} {
		if _, err := db.db.Exec("DROP TRIGGER " + triggerName); err != nil {
			t.Fatalf("drop MySQL task status trigger %s: %v", triggerName, err)
		}
	}
	expiry := time.Now().UTC().Add(time.Hour)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at)
		 VALUES (?, ?, 'same-run', 'running', 'stale source', '', ?)`,
		staleTopicID, sourceUID, expiry,
	); err != nil {
		t.Fatalf("seed stale MySQL upgrade source: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid)
		 VALUES (?, 'same-run', 'completed', 'aggregate completed', '', ?)`,
		staleTopicID, sourceUID,
	); err != nil {
		t.Fatalf("seed completed MySQL upgrade aggregate: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at)
		 VALUES (?, ?, 'new-run', 'running', 'new run active', '', ?)`,
		protectedTopicID, sourceUID, expiry,
	); err != nil {
		t.Fatalf("seed protected MySQL upgrade source: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid)
		 VALUES (?, 'old-run', 'completed', 'old run completed', '', ?)`,
		protectedTopicID, sourceUID,
	); err != nil {
		t.Fatalf("seed protected MySQL upgrade aggregate: %v", err)
	}

	if err := db.CreateSchema(); err != nil {
		t.Fatalf("run MySQL upgrade handoff: %v", err)
	}

	staleSource, err := db.GetConversationTaskStatusForSource(staleTopicID, sourceUID)
	if err != nil || staleSource == nil || staleSource.State != "completed" || staleSource.Summary != "aggregate completed" {
		t.Fatalf("MySQL upgrade did not reconcile stale source: status=%+v err=%v", staleSource, err)
	}
	protectedSource, err := db.GetConversationTaskStatusForSource(protectedTopicID, sourceUID)
	if err != nil || protectedSource == nil || protectedSource.RunID != "new-run" || protectedSource.State != "running" {
		t.Fatalf("MySQL upgrade overwrote protected active source: status=%+v err=%v", protectedSource, err)
	}
}

func assertMySQLPushSubscriptionGenerations(t *testing.T, db *Adapter, ownerID int64) {
	t.Helper()
	const endpoint = "https://push.example.test/mysql-generation"
	subscription := &types.PushSubscription{
		UID:      ownerID,
		Endpoint: endpoint,
		P256DH:   "p256dh",
		Auth:     "auth",
	}
	for _, registrationID := range []string{"", "Session-ABC"} {
		subscription.RegistrationID = registrationID
		stored, err := db.UpsertPushSubscription(context.Background(), subscription, 10)
		if err != nil || !stored {
			t.Fatalf("upsert MySQL push generation %q: stored=%t err=%v", registrationID, stored, err)
		}
	}
	for _, staleRegistrationID := range []string{"", "session-abc"} {
		if err := db.DeletePushSubscription(context.Background(), ownerID, endpoint, staleRegistrationID); err != nil {
			t.Fatalf("delete stale MySQL push generation %q: %v", staleRegistrationID, err)
		}
	}
	subscriptions, err := db.ListPushSubscriptions(context.Background(), ownerID)
	if err != nil || len(subscriptions) != 1 || subscriptions[0].RegistrationID != "Session-ABC" {
		t.Fatalf("stale MySQL delete removed current push generation: subscriptions=%+v err=%v", subscriptions, err)
	}
}

func assertMySQLConversationTaskStatusGeneration(t *testing.T, db *Adapter, firstBotID, secondBotID int64) {
	t.Helper()
	const topicID = "grp_mysql_mixed_version"
	if err := db.CreateTopic(topicID, "group", firstBotID); err != nil {
		t.Fatalf("create MySQL task status topic: %v", err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	activeSourceUpdatedAt := time.Now().UTC().Add(-time.Hour)
	staleAggregateUpdatedAt := activeSourceUpdatedAt.Add(30 * time.Minute)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES (?, ?, 'new-run', 'running', 'new run active', '', ?, ?)`,
		topicID,
		firstBotID,
		expiry,
		activeSourceUpdatedAt,
	); err != nil {
		t.Fatalf("seed MySQL active new-run source status: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES (?, 'old-run', 'completed', 'old run completed', '', ?, NULL, ?)`,
		topicID,
		firstBotID,
		staleAggregateUpdatedAt,
	); err != nil {
		t.Fatalf("seed MySQL stale terminal aggregate: %v", err)
	}

	aggregate, err := db.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID:   topicID,
		RunID:     "other-source-run",
		State:     "completed",
		Summary:   "other source completed",
		SourceUID: secondBotID,
	})
	if err != nil {
		t.Fatalf("upsert alongside MySQL mixed-version task status: %v", err)
	}
	if aggregate.State != "running" || aggregate.RunID != "new-run" || aggregate.SourceUID != firstBotID {
		t.Fatalf("MySQL stale terminal aggregate overwrote active new run: %+v", aggregate)
	}
	activeSource, err := db.GetConversationTaskStatusForSource(topicID, firstBotID)
	if err != nil || activeSource == nil || activeSource.RunID != "new-run" || activeSource.State != "running" {
		t.Fatalf("MySQL active new-run source was not preserved: status=%+v err=%v", activeSource, err)
	}

	const readOnlyTopicID = "grp_mysql_legacy_read"
	if err := db.CreateTopic(readOnlyTopicID, "group", firstBotID); err != nil {
		t.Fatalf("create MySQL legacy read topic: %v", err)
	}
	sameSecond := time.Now().UTC().Truncate(time.Second)
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES (?, ?, 'read-run', 'running', 'running before old write', '', ?, ?)`,
		readOnlyTopicID,
		firstBotID,
		expiry,
		sameSecond,
	); err != nil {
		t.Fatalf("seed MySQL source before legacy-only write: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES (?, 'read-run', 'running', 'running aggregate', '', ?, ?, ?)`,
		readOnlyTopicID,
		firstBotID,
		expiry,
		sameSecond,
	); err != nil {
		t.Fatalf("seed MySQL legacy aggregate: %v", err)
	}
	if _, err := db.db.Exec(
		`UPDATE conversation_task_statuses SET
		   state = 'completed', summary = 'completed by old node', expires_at = NULL, updated_at = ?
		 WHERE topic_id = ?`,
		sameSecond,
		readOnlyTopicID,
	); err != nil {
		t.Fatalf("update MySQL legacy aggregate without new-node upsert: %v", err)
	}
	readOnlySource, err := db.GetConversationTaskStatusForSource(readOnlyTopicID, firstBotID)
	if err != nil || readOnlySource == nil || readOnlySource.State != "completed" {
		t.Fatalf("MySQL legacy-only write was not visible from source read: status=%+v err=%v", readOnlySource, err)
	}
	readOnlyAggregates, err := db.GetConversationTaskStatuses([]string{readOnlyTopicID})
	if err != nil || readOnlyAggregates[readOnlyTopicID] == nil || readOnlyAggregates[readOnlyTopicID].State != "completed" {
		t.Fatalf("MySQL legacy-only write was not visible from aggregate read: statuses=%+v err=%v", readOnlyAggregates, err)
	}
}
