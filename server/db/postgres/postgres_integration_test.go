package postgres

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

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
