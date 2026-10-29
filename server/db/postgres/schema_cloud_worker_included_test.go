package postgres

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// 「套餐含不含机器」的标记由谁掌握。
//
// 2026-10-29 现场：`cloud_worker_included` 的回填写在 ALTER 旁边但**没有真的加
// guard** —— 注释说"与 ADD COLUMN 同一 guard 内"，SQL 却是无条件的 UPDATE，
// 每次启动都执行。后果：
//
//   - 运营在后台把某个旧套餐的机器关掉，下次重启又被打开，而且没有任何日志；
//   - 我们正在把 399/799 换成新的套餐线，这两个 slug 恰好就是回填名单里的，
//     运营一旦把它们设成 false，重启即被撤销。
//
// 所以这条测试锁的是"回填只发生一次"，不是"回填发生了"。
func TestPostgresCloudWorkerIncludedBackfillRunsOnce(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_worker_included_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create worker-included test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open worker-included schema: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create worker-included schema objects: %v", err)
	}

	// 首次建表：两个官方付费套餐应当被回填成"含机器"。
	for _, slug := range []string{"catsco-personal", "catsco-pro"} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_plans(slug, name, price_fen, sale_state, duration_days, model_budgets)
			VALUES ($1, $1, 1, 'hidden', 30, '{"MiniMax-M3":1}'::jsonb)
			ON CONFLICT (slug) DO NOTHING`, slug); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}

	// 模拟"运营手动把旧套餐的机器关掉"。
	if _, err := db.db.Exec(`UPDATE commercial_plans SET cloud_worker_included = FALSE WHERE slug = 'catsco-pro'`); err != nil {
		t.Fatalf("operator turns the machine off: %v", err)
	}

	included := func(slug string) bool {
		t.Helper()
		var got bool
		if err := db.db.QueryRow(`SELECT cloud_worker_included FROM commercial_plans WHERE slug = $1`, slug).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", slug, err)
		}
		return got
	}

	// 重启：CreateSchema 走的是 CREATE TABLE IF NOT EXISTS + 一次性回填。
	// 已经存在的列不能再被 UPDATE 覆盖。
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("re-run schema (simulated restart): %v", err)
	}
	if included("catsco-pro") {
		t.Fatal("restart re-enabled the machine for catsco-pro; the backfill must run only when the column is added")
	}
	if !included("catsco-personal") {
		t.Fatal("catsco-personal lost its machine: the flag must stay operator-controlled after the backfill")
	}

	// 新建的套餐默认不含机器 —— 误发机器很贵，误不发只是张工单。
	if _, err := db.db.Exec(`
		INSERT INTO commercial_plans(slug, name, price_fen, sale_state, duration_days, model_budgets)
		VALUES ('catsco-standard', 'standard', 9900, 'hidden', 30, '{"MiniMax-M3":1}'::jsonb)`); err != nil {
		t.Fatalf("seed new plan: %v", err)
	}
	if included("catsco-standard") {
		t.Fatal("a brand new plan must default to token-only")
	}
}
