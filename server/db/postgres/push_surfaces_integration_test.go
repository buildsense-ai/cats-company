package postgres

import (
	"testing"

	"github.com/openchat/openchat/server/db/pushsurface/contracttest"
)

func TestPostgresPushSurfaceDurability(t *testing.T) {
	db := taskRunTestAdapter(t)
	contracttest.Run(t, db, func(uid int64) {
		if _, err := db.db.Exec(`DELETE FROM users WHERE id = $1`, uid); err != nil {
			t.Error(err)
		}
	}, func(topic string) {
		if _, err := db.db.Exec(`DELETE FROM topics WHERE id = $1`, topic); err != nil {
			t.Error(err)
		}
	})
}
