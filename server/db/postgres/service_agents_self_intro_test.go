package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
)

// 自述建议（000062）：schema 常量 / 编号迁移文件必须说同一件事。
//
// 为什么需要这个测试：#764 只把 ALTER 写进了 schema.go 常量，没写编号迁移文件，
// 也没加进 seedHelloAgent —— 而 seedHelloAgent 走的是「编号迁移文件列表」，
// 不走 CreateSchema。结果 CI 里 ListServiceAgents 直接报
// `column a.self_intro_description does not exist (SQLSTATE 42703)`。
func TestServiceAgentSelfIntroMigrationMatchesRuntimeSchema(t *testing.T) {
	columns := []string{"self_intro_description", "self_intro_input_schema"}

	for _, column := range columns {
		if !strings.Contains(migrateServiceAgentsAddSelfIntro, column) {
			t.Fatalf("schema 常量缺列 %q: %s", column, migrateServiceAgentsAddSelfIntro)
		}
	}

	up, err := os.ReadFile("../migrations/postgres/000062_service_agents_self_intro.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if !strings.Contains(string(up), column) {
			t.Fatalf("up 迁移缺列 %q: %s", column, up)
		}
	}

	down, err := os.ReadFile("../migrations/postgres/000062_service_agents_self_intro.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if !strings.Contains(string(down), column) {
			t.Fatalf("down 迁移必须可逆，缺列 %q: %s", column, down)
		}
	}
}

// seedHelloAgent 是 PG 集成测试的建表路径；它漏跑某个编号迁移，测试库就缺列。
// 断言两列真的在，失败时能一眼看出是 seed 的问题，而不是去翻 ListServiceAgents。
func TestServiceAgentSelfIntroSeedCreatesColumns(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)

	rows, err := db.db.QueryContext(context.Background(), `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'service_agents'`)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	defer rows.Close()

	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	for _, column := range []string{"self_intro_description", "self_intro_input_schema"} {
		if !present[column] {
			t.Fatalf("seedHelloAgent 之后 service_agents 缺列 %q（000062 没进 seed 迁移列表？）现有列：%v", column, present)
		}
	}

	// 列在还不够：ListServiceAgents 得真的能跑通。
	if _, err := db.ListServiceAgents(context.Background()); err != nil {
		t.Fatalf("list agents: %v", err)
	}
}
