package postgres

// Real DB contract, specified before adding owner_id to the admin roster:
// a bot must resolve to its bot_config owner, not its own UID or a friend.
import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestLegacyAgentRosterOwnerPG(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("CATS_PG_TEST_DSN required")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("legacy_owner_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`)
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, dsn, schema)); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(&types.User{Username: "legacy-owner", Email: "legacy-owner@example.test", AccountType: types.AccountHuman, PassHash: []byte("fixture")})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := db.CreateUser(&types.User{Username: "legacy-agent", Email: "legacy-agent@example.test", AccountType: types.AccountBot, PassHash: []byte("fixture")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`INSERT INTO bot_config(user_id,owner_id) VALUES($1,$2)`, bot, owner); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListBots()
	if err != nil || len(rows) != 1 {
		t.Fatalf("roster=%+v %v", rows, err)
	}
	if rows[0]["owner_id"] != owner {
		t.Fatalf("wrong owner: %+v want %v", rows, owner)
	}
}
