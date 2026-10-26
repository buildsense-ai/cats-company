package mysql

import (
	"os"
	"strings"
	"testing"
)

// 自述建议（000062）：MySQL 镜像必须和 PG 语义一致，但列定义不能照抄。
//
// 实测（MySQL 8.0.46）：
//   - ALTER TABLE ... ADD COLUMN x TEXT NOT NULL DEFAULT ”
//     → ERROR 1101 BLOB, TEXT, GEOMETRY or JSON column 'x' can't have a default value
//   - ALTER TABLE ... ADD COLUMN x TEXT NOT NULL（无默认值）
//     → 省略该列的 INSERT 在 strict 模式报 ERROR 1364 Field 'x' doesn't have a default value
//
// 所以 MySQL 侧只能是可空 TEXT，读侧用 COALESCE(..., ”) 兜底。
func TestMySQLServiceAgentSelfIntroSchemaStaysNullable(t *testing.T) {
	for name, statement := range map[string]string{
		"self_intro_description":  migrateServiceAgentsAddSelfIntroDescription,
		"self_intro_input_schema": migrateServiceAgentsAddSelfIntroInputSchema,
	} {
		if strings.Contains(statement, "NOT NULL") {
			t.Fatalf("%s 不能 NOT NULL（会让省略该列的 INSERT 报 1364）: %s", name, statement)
		}
		if strings.Contains(statement, "DEFAULT") {
			t.Fatalf("%s 不能带 DEFAULT（MySQL 的 TEXT 不允许默认值，报 1101）: %s", name, statement)
		}
		if !strings.Contains(statement, "TEXT NULL") {
			t.Fatalf("%s 应该是 TEXT NULL: %s", name, statement)
		}
	}
}

func TestMySQLServiceAgentSelfIntroMigrationFilesReversible(t *testing.T) {
	up, err := os.ReadFile("../migrations/mysql/000062_service_agents_self_intro.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"self_intro_description", "self_intro_input_schema"} {
		if !strings.Contains(string(up), column) {
			t.Fatalf("up 迁移缺列 %q: %s", column, up)
		}
	}
	if strings.Contains(sqlStatementsOnly(string(up)), "DEFAULT") {
		t.Fatalf("MySQL 的 TEXT 列不能带 DEFAULT: %s", up)
	}

	down, err := os.ReadFile("../migrations/mysql/000062_service_agents_self_intro.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"self_intro_description", "self_intro_input_schema"} {
		if !strings.Contains(string(down), column) {
			t.Fatalf("down 迁移必须可逆，缺列 %q: %s", column, down)
		}
	}
}

// 注释里会提到 DEFAULT / NOT NULL（就是在解释为什么不能用），断言只看真正的 SQL 行。
func sqlStatementsOnly(contents string) string {
	var b strings.Builder
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
	return b.String()
}
