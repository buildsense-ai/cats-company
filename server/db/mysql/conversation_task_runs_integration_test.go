package mysql

import (
	"os"
	"testing"

	"github.com/openchat/openchat/server/db/taskstatus/contracttest"
)

func TestMySQLConversationTaskRunDurability(t *testing.T) {
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}
	a := &Adapter{}
	if err := a.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateSchema(); err != nil {
		t.Fatalf("schema not idempotent: %v", err)
	}
	contracttest.Run(t, a.db, a, false)
}
