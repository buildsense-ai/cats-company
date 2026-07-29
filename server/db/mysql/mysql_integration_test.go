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
	assertMySQLConversationTaskStatusGeneration(t, db, firstBotID, secondBotID)
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
}
