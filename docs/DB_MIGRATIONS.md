# CatsCompany 数据库迁移

CatsCompany 现在采用“代码内置 PostgreSQL schema + SQL migration 基线”的过渡方案。生产环境使用腾讯云托管 PostgreSQL；MySQL 不纳入这套 migration 流程。

## 放进公开仓库的内容

- `server/db/migrations/**`：无密 SQL migration 文件。
- `scripts/db-migrate.sh`：迁移执行包装脚本，只读取环境变量。
- 本文档：流程、约定、风险说明。
- `deploy/*.example` 或 `.env.example`：只放占位示例值。

## 不能进公开仓库的内容

- 真实数据库 DSN、密码、连接串。
- 生产 `.pgpass`、`.my.cnf`、私钥、service token。
- 数据库备份、导出的 dump、含用户数据的样本 SQL。
- 带完整连接串的终端输出或截图。

## 放服务器本地的内容

当前服务器使用两个本地 env：

- `/opt/catscompany/secrets/db-migration-prod.env`
- `/opt/catscompany/secrets/db-migration-test.env`
- `/srv/cats-backups/postgres/`

`db-migration-*.env` 示例：

```bash
export CATS_DB_DRIVER=postgres
export CATS_MIGRATION_DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/DB?sslmode=require'
```

真实值只在服务器上维护，不提交。服务器本地 env 可以从部署 env 读取应用 DSN，但需要输出 migration CLI 可识别的 PostgreSQL DSN；如果应用驱动支持而 `migrate/migrate` 不支持某些参数，应在服务器本地 env 中规范化，不要把真实转换结果提交到仓库。

## 当前基线

`000001_baseline` 表示当前生产 schema 仍由 Go 代码里的 `CreateSchema()` 创建和补齐：

- `server/db/postgres/schema.go`

服务启动时会确保 `schema_migrations` 表存在，并在空表时写入版本 `1`。这只是版本标记，不会修改业务数据。

`server/db/mysql/schema.go` 仍可保留历史兼容代码，但不维护 migration 文件，也不会写入 migration 版本表。

## 之后怎么处理 schema 变更

`server/db/postgres/schema.go` 的 `CreateSchema()` 是运行时权威的幂等 schema 来源：新环境和既有环境都从同一个 schema 模块获得相同结果，服务启动时自动补齐缺失的表、列、索引、约束和 trigger。

同时，每一项 schema 变更（包括新表、新列、索引、约束、trigger）都要配套一对唯一编号的 `up` / `down` SQL migration，镜像 `CreateSchema()` 完成的同一套 DDL（含 `CREATE TABLE` 建新表）。这样外部 migration 工具（`scripts/db-migrate.sh`、`migrate` CLI）拥有稳定、可审核、可回滚的变更历史，生产如走 migration 通道也能按序执行到与 `CreateSchema()` 一致的结果。

即仓库采用“双轨”：`CreateSchema()` 负责启动时幂等补齐，migration 文件负责可编排、可审核、可回滚的变更历史。二者描述同一份 DDL，必须保持一致；若出现偏差，以 `CreateSchema()` 为准，并同步修正 migration。

但数据回填/数据转换属于 DML，无法表达为 `CreateSchema()` 的幂等 DDL，因此这类步骤只保留在 migration 的 `up`（`down`）文件中，是该数据变更的唯一权威来源（顺序遵循发布门禁第 4 条：表 -> 列 -> 数据回填 -> 约束/索引/trigger）。此时：

1. 添加一对唯一编号的 `up` / `down` 文件（沿用本仓库现有编号序列，如 `000019_agent_artifact_tags`）。
2. 在 `CreateSchema()` 中添加对应的幂等 DDL，migration 文件中的 DDL 要与之一致。
3. 生产执行前仍需备份，先记录 `version` / `dirty`，再执行 `up`。

## 发布门禁

2026-08-13 曾发生“测试环境部署成功、生产历史库启动失败”的事故。根因是新索引依赖的列只存在于全新 schema 和 SQL migration 中，而生产启动路径没有在创建索引前执行该 migration。完整复盘见：

- [`docs/incidents/2026-08-13-commercial-refund-schema-prod-502.md`](incidents/2026-08-13-commercial-refund-schema-prod-502.md)

此后所有 PostgreSQL schema 变更必须满足：

1. 默认通过 PR 合并，不直接推送 `main`。
2. 同时测试空数据库创建和生产历史 schema 升级。
3. 升级测试必须重复执行一次，验证幂等性。
4. DDL 顺序必须是：表 -> 列 -> 数据回填 -> 约束/索引/trigger。
5. 如果生产仍由 `CreateSchema()` 启动建表，仅添加 SQL migration 文件不代表生产会自动执行；必须同步保证 `CreateSchema()` 兼容旧库，或先修改部署流程让 migration 在应用启动前执行。
6. PostgreSQL 集成测试不得因为缺少测试 DSN 而在发布流水线中静默跳过。

测试环境当前 schema 已经包含目标列时，只能证明新版本可以在“较新 schema”上启动，不能证明生产历史库可升级。涉及 schema 的 PR 描述中必须写明使用的历史基线和升级验证结果。

## 服务器执行

`scripts/db-migrate.sh` 会优先使用本机 `migrate` CLI；如果没有，会回退到 Docker 镜像 `migrate/migrate`。如果希望直接安装 CLI：

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
export PATH="$PATH:$HOME/go/bin"
```

先查看版本：

```bash
cd "/srv/catscompany-prod/source/$(grep -E '^IMAGE_TAG=' /srv/catscompany-prod/env/prod.env | tail -n1 | cut -d= -f2-)"
source /opt/catscompany/secrets/db-migration-prod.env
scripts/db-migrate.sh version
```

已有生产库如果还没有版本表，可先通过应用启动自动写入版本 1，或在确认当前 schema 已经和主线一致后执行：

```bash
scripts/db-migrate.sh force 1
```

应用新迁移：

```bash
scripts/db-migrate.sh up
```

回滚最近一步：

```bash
scripts/db-migrate.sh down 1
```

生产执行前必须先做数据库备份；不要在没有备份的情况下跑破坏性迁移。
