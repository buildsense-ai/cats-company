package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 云端包分发（000029）：schema 常量 / 编号迁移镜像 / 运行时 CreateSchema / 发布往返。

func TestServiceAgentPackageSchemaConstants(t *testing.T) {
	if !strings.Contains(createServiceAgentVersionsTable, "package_json JSONB") {
		t.Fatal("versions table missing package_json (fresh DB)")
	}
	if !strings.Contains(migrateServiceAgentVersionsAddPackageJson, "ADD COLUMN IF NOT EXISTS package_json") {
		t.Fatal("additive migration missing package_json")
	}
	if !strings.Contains(createServiceAgentVersionsAssetsHashIndex, "idx_service_agent_versions_assets_hash") {
		t.Fatal("missing assets_hash index")
	}
}

func TestServiceAgentPackageMigrationMatchesRuntimeSchema(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000029_service_agent_versions_package.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"package_json JSONB", "idx_service_agent_versions_assets_hash"} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("migration missing %q: %s", want, up)
		}
	}
	down, err := os.ReadFile("../migrations/postgres/000029_service_agent_versions_package.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP COLUMN IF EXISTS package_json") {
		t.Fatalf("down migration must be reversible: %s", down)
	}
}

// 000029 在 000027 之上幂等执行，且可回滚再上。
func TestServiceAgentPackageMigrationFilesExecute(t *testing.T) {
	db := pgTestDB(t)
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	execRetireV4MigrationFile(t, db, "000029_service_agent_versions_package.up.sql")
	execRetireV4MigrationFile(t, db, "000029_service_agent_versions_package.up.sql") // 幂等重放
	execRetireV4MigrationFile(t, db, "000029_service_agent_versions_package.down.sql")
	execRetireV4MigrationFile(t, db, "000029_service_agent_versions_package.up.sql")
}

// CreateSchema 是运行时权威：空库直接具备 package_json；发布/读取/最新 hash 全链路。
func TestServiceAgentPackagePublishRoundtrip(t *testing.T) {
	db := pgTestDB(t)
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	ctx := context.Background()
	id := agentID(t, db, "hello")

	hash := strings.Repeat("ab", 32)
	bundle := json.RawMessage(`{"schema":"catsco.service-agent-bundle.v1","slug":"hello","files":[]}`)
	// 模拟事故现场：首次发布派生值缺模型白名单，重发同内容时应自愈。
	definition := json.RawMessage(`{"async_threshold_seconds": 60, "models": []}`)

	version, err := db.PublishServiceAgentPackage(ctx, "hello", definition, hash, bundle, false)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if version <= 0 {
		t.Fatalf("version = %d", version)
	}
	replayed, err := db.PublishServiceAgentPackage(ctx, "hello", definition, hash, bundle, false)
	if err != nil || replayed != version {
		t.Fatalf("republish: version=%d err=%v, want idempotent %d", replayed, err, version)
	}

	// 同 hash 重发新派生值 → definition_json 覆盖（修正历史派生 bug 的自愈路径）。
	fixed := json.RawMessage(`{"async_threshold_seconds": 60, "models": ["MiniMax-M3"]}`)
	if _, err := db.PublishServiceAgentPackage(ctx, "hello", fixed, hash, bundle, false); err != nil {
		t.Fatalf("republish with fixed definition: %v", err)
	}
	_, _, latestDefinition, err := db.GetServiceAgentDefinition(ctx, id, 0)
	if err != nil {
		t.Fatalf("definition after republish: %v", err)
	}
	if !strings.Contains(string(latestDefinition), "MiniMax-M3") {
		t.Fatalf("definition not updated by republish: %s", latestDefinition)
	}

	// 会话钉定语义：版本级定义查询精确命中（v1=种子定义，v2=发布定义）；缺失版本回退最新。
	_, _, v1Definition, err := db.GetServiceAgentDefinition(ctx, id, 1)
	if err != nil || !strings.Contains(string(v1Definition), "gpt-6.1-sol") {
		t.Fatalf("v1 definition = %s err=%v, want seeded gpt-6.1-sol", v1Definition, err)
	}
	_, _, v2Definition, err := db.GetServiceAgentDefinition(ctx, id, version)
	if err != nil || !strings.Contains(string(v2Definition), "MiniMax-M3") {
		t.Fatalf("v%d definition = %s err=%v, want MiniMax-M3", version, v2Definition, err)
	}
	_, _, fallbackDefinition, err := db.GetServiceAgentDefinition(ctx, id, 9999)
	if err != nil || !strings.Contains(string(fallbackDefinition), "MiniMax-M3") {
		t.Fatalf("missing version fallback = %s err=%v, want latest", fallbackDefinition, err)
	}

	// force 重发：同 hash 也建新版本（回滚通道：重发旧包 → 新版本指向旧内容）。
	forced, err := db.PublishServiceAgentPackage(ctx, "hello", fixed, hash, bundle, true)
	if err != nil {
		t.Fatalf("force publish: %v", err)
	}
	if forced != version+1 {
		t.Fatalf("force version = %d, want %d", forced, version+1)
	}
	if latestAfterForce, err := db.GetServiceAgentPackageHash(ctx, id); err != nil || latestAfterForce != hash {
		t.Fatalf("latest after force = %q err=%v, want %s", latestAfterForce, err, hash)
	}

	got, err := db.GetServiceAgentPackage(ctx, hash)
	if err != nil {
		t.Fatalf("get package: %v", err)
	}
	var gotBundle, wantBundle any
	if err := json.Unmarshal(got, &gotBundle); err != nil {
		t.Fatalf("get package json: %v", err)
	}
	if err := json.Unmarshal(bundle, &wantBundle); err != nil {
		t.Fatalf("fixture json: %v", err)
	}
	if gotRaw, _ := json.Marshal(gotBundle); string(gotRaw) != string(mustJSON(t, wantBundle)) {
		t.Fatalf("bundle mismatch: %s vs %s", gotRaw, bundle)
	}

	latest, err := db.GetServiceAgentPackageHash(ctx, id)
	if err != nil || latest != hash {
		t.Fatalf("latest hash = %q err=%v, want %s", latest, err, hash)
	}

	if _, err := db.GetServiceAgentPackage(ctx, strings.Repeat("cd", 32)); !errors.Is(err, store.ErrServicePackageNotFound) {
		t.Fatalf("unknown hash err = %v, want ErrServicePackageNotFound", err)
	}
	if _, err := db.PublishServiceAgentPackage(ctx, "ghost", definition, hash, bundle, false); !errors.Is(err, store.ErrServiceAgentNotFound) {
		t.Fatalf("unknown slug err = %v, want ErrServiceAgentNotFound", err)
	}

	// 未发布过包的 agent（无 package_json 行）最新 hash 为空（claim 下发空 → 实例回退内置包）。
	if _, err := db.db.ExecContext(ctx, `INSERT INTO service_agents (slug, name) VALUES ('bare', 'Bare')`); err != nil {
		t.Fatalf("seed bare agent: %v", err)
	}
	var bareID int64
	if err := db.db.QueryRowContext(ctx, `SELECT id FROM service_agents WHERE slug = 'bare'`).Scan(&bareID); err != nil {
		t.Fatalf("select bare: %v", err)
	}
	if emptyHash, err := db.GetServiceAgentPackageHash(ctx, bareID); err != nil || emptyHash != "" {
		t.Fatalf("bare hash = %q err=%v, want empty", emptyHash, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
