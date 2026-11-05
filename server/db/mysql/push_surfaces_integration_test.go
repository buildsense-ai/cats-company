package mysql

import (
	"os"
	"testing"

	"github.com/openchat/openchat/server/db/pushsurface/contracttest"
)

func TestMySQLPushSurfaceDurability(t *testing.T) {
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}
	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, db, func(uid int64) {
		if _, err := db.db.Exec(`DELETE FROM users WHERE id = ?`, uid); err != nil {
			t.Error(err)
		}
	}, func(topic string) {
		if _, err := db.db.Exec(`DELETE FROM topics WHERE id = ?`, topic); err != nil {
			t.Error(err)
		}
	})
}
