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

普通的新表、新列、索引、约束和 trigger 由 `server/db/postgres/schema.go` 的 `CreateSchema()` 统一管理，必须保持幂等。这让新环境和既有环境都从同一个 schema 模块获得相同结果，不再为同一项 DDL 维护两套真相。

`push_subscriptions` 的用户外键升级也绝不在启动时删除历史行。若旧库存在没有对应用户的订阅，MySQL 启动会报告数量并停止升级；先备份、审计并通过受控运维操作修复这些行，再重新启动。

只有在需要单独编排、审核或回滚的数据转换中，才添加新的 PostgreSQL SQL migration。此时：

1. 添加一对唯一编号的 `up` / `down` 文件。
2. 不要在 `CreateSchema()` 重复同一个数据转换。
3. 生产执行前仍需备份，先记录 `version` / `dirty`，再执行 `up`。

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
