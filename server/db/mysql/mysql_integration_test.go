package mysql

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestMySQLStoreContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_MYSQL_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}

	db := &Adapter{}
	if err := db.Open(rawDSN); err != nil {
		t.Fatalf("open mysql connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema should be idempotent: %v", err)
	}
	if health := db.HealthCheck(); health["status"] != "healthy" {
		t.Fatalf("expected healthy database, got %#v", health)
	}

	suffix := time.Now().UnixNano()
	ownerID, err := db.CreateUser(&types.User{
		Username:    fmt.Sprintf("mysql_owner_%d", suffix),
		Email:       fmt.Sprintf("owner_%d@example.com", suffix),
		DisplayName: "MySQL Owner",
		AccountType: types.AccountHuman,
		PassHash:    []byte("owner-hash"),
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	owner, err := db.GetUserByUsername(fmt.Sprintf("MYSQL_OWNER_%d", suffix))
	if err != nil || owner == nil || owner.ID != ownerID {
		t.Fatalf("case-insensitive username lookup failed: owner=%#v err=%v", owner, err)
	}

	friendID, err := db.CreateUser(&types.User{
		Username:    fmt.Sprintf("mysql_friend_%d", suffix),
		Email:       fmt.Sprintf("friend_%d@example.com", suffix),
		DisplayName: "MySQL Friend",
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

	topicID := fmt.Sprintf("p2p_mysql_%d", suffix)
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

	groupID, err := db.CreateGroup("MySQL Group", ownerID)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	members, err := db.GetGroupMembers(groupID)
	if err != nil || len(members) != 1 || members[0].UserID != ownerID {
		t.Fatalf("group members mismatch: %#v err=%v", members, err)
	}

	botID, err := db.CreateUser(&types.User{
		Username:    fmt.Sprintf("mysql_bot_%d", suffix),
		DisplayName: "MySQL Bot",
		AccountType: types.AccountBot,
		PassHash:    []byte("bot-hash"),
	})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	if err := db.SaveBotConfigWithOwner(botID, ownerID, "https://bot.example", "catsco-test"); err != nil {
		t.Fatalf("save bot config: %v", err)
	}
	if err := db.SaveAPIKey(botID, "cc_mysql_test_key"); err != nil {
		t.Fatalf("save api key: %v", err)
	}
	foundBotID, err := db.GetBotByAPIKey("cc_mysql_test_key")
	if err != nil || foundBotID != botID {
		t.Fatalf("get bot by api key mismatch: got=%d want=%d err=%v", foundBotID, botID, err)
	}
	if err := db.DeleteBot(botID); err != nil {
		t.Fatalf("delete bot: %v", err)
	}
	deletedBot, err := db.GetUser(botID)
	if err != nil || deletedBot == nil || deletedBot.State != 1 {
		t.Fatalf("deleted bot should be disabled, bot=%#v err=%v", deletedBot, err)
	}
}
