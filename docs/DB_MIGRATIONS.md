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

## 之后怎么加迁移

新增 schema 变更时，不要继续只往 `schema.go` 里加 `ALTER TABLE`。新增一组 migration：

```text
server/db/migrations/postgres/000002_xxx.up.sql
server/db/migrations/postgres/000002_xxx.down.sql
```

如果某次变更需要兼容 MySQL，也在应用代码里单独说明；生产 schema 迁移以 PostgreSQL migration 为准。

## 历史 version 2 冲突处理

`000006_weixin_clawbot_tokens` 与 `000007_reconcile_commercial_relay_foundation`
是一次性修复两个独立分支曾共用 version 2 的例外。不要据此重编号或编辑任何后续已发布 migration。

生产执行这两个版本前：

1. 按本文末尾要求完成数据库备份。
2. 执行 `scripts/db-migrate.sh version`，在变更记录中保存输出的 version/dirty 状态（不要记录 DSN）。
3. 执行 `scripts/db-migrate.sh up`，并确认 version 6、7、8 均已成功应用。
4. 检查 `weixin_clawbot_tokens` 与 commercial schema 均存在，再恢复应用流量。

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
