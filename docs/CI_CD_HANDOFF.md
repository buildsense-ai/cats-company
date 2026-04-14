# CI/CD 交接说明

本文档说明 `chore/pr-ready-test-deploy` 分支当前的 CI/CD 方案，以及主仓库合并后需要执行的操作。

## 当前状态

这条分支现在已经可以发起 PR。

已经完成的验证：

- `go test ./server/...`
- `docker compose -f deploy/test/docker-compose.yml --env-file deploy/test/env.test.example config`
- `docker compose -f deploy/prod/docker-compose.yml --env-file deploy/prod/env.prod.example config`
- fork 仓库里的 test 自动部署链路已经在真实服务器上跑通过：
  - GitHub Actions 构建镜像
  - 推送到 GHCR
  - 服务器拉取镜像并启动

目前仍属于草案的部分：

- `deploy/prod` 和 `deploy-prod.yml`
- 生产流量切换
- 生产 nginx 切换

## 这次 PR 包含的内容

1. CI 工作流：
   - `.github/workflows/ci.yml`
2. 测试环境部署骨架：
   - `deploy/test/*`
   - `.github/workflows/deploy-test.yml`
3. 生产环境部署骨架：
   - `deploy/prod/*`
   - `.github/workflows/deploy-prod.yml`

## 部署模型

目标流程如下：

1. 代码进入 `main`
2. 自动运行 `Deploy Docker Test`
3. GitHub Actions 使用提交 SHA 构建并推送 GHCR 镜像
4. 测试服务器拉取并运行这一个 SHA
5. 如果 test workflow 成功完成，则触发 `Deploy Docker Prod`
6. 生产环境拉取并运行同一个 SHA

几个关键原则：

- test 和 prod 必须使用同一个镜像 SHA
- prod 不能在服务器上重新构建源码
- prod 复用现有 MySQL 容器和数据

## 服务器目录结构建议

主仓库合并后，推荐使用如下目录结构：

```text
/srv/
  catscompany-test/
    compose/
    env/
      test.env
      env.test.example
    data/
      mysql/
      uploads/
    logs/
    CURRENT_REVISION
    releases/

  catscompany-prod/
    compose/
    env/
      prod.env
      env.prod.example
    data/
      uploads/
    logs/
    CURRENT_REVISION
    PREVIOUS_REVISION
```

在新的自动化流程稳定之前，以下旧目录建议保留，作为回滚保险：

- `/root/catscompany`
- `/root/catscompany-test`
- `/root/text/catscompany-docker-test`

## GitHub Environments 和 Secrets

主仓库中建议创建两个 environment：

- `test`
- `prod`

### `test` environment 需要的 secrets

- `SSH_HOST`
- `SSH_USER`
- `SSH_PRIVATE_KEY`
- `GHCR_USERNAME`
- `GHCR_TOKEN`

### `prod` environment 需要的 secrets

- `SSH_HOST`
- `SSH_USER`
- `SSH_PRIVATE_KEY`
- `GHCR_USERNAME`
- `GHCR_TOKEN`

说明：

- 如果希望做到全自动，不要给 `prod` 配置人工审批
- `GHCR_TOKEN` 在服务器侧只需要有包拉取权限即可

## 服务器侧需要准备的文件

### Test 环境

创建：

- `/srv/catscompany-test/env/test.env`

建议从下面这个文件复制：

- `deploy/test/env.test.example`

至少要填写：

- `GHCR_OWNER`
- `MYSQL_ROOT_PASSWORD`
- `MYSQL_PASSWORD`
- `BOT_ASSISTANT_PASSWORD`
- `OC_JWT_SECRET`

### Prod 环境

创建：

- `/srv/catscompany-prod/env/prod.env`

建议从下面这个文件复制：

- `deploy/prod/env.prod.example`

至少要填写：

- `GHCR_OWNER`
- `OC_JWT_SECRET`
- `OC_DB_DSN`

## 生产数据库说明

生产环境明确设计为复用现有 MySQL，而不是新建一套数据库。

默认的 `prod.env` 示例假设当前生产 MySQL 容器已经映射到宿主机：

```text
OC_DB_DSN=openchat_shadow:<密码>@tcp(host.docker.internal:3306)/openchat?parseTime=true&charset=utf8mb4
```

这意味着：

- 生产环境只有应用层容器化
- MySQL 继续作为现有的持久化基础设施存在
- 数据保持连续，不会新建一套生产库

关于 `3306`：

- 现有生产 MySQL 继续占用宿主机 `3306` 是没问题的
- 新 prod 容器只是作为客户端连接 `host.docker.internal:3306`
- 不会再额外启动一个新的 `3306`

但要注意：

- 不建议让“旧 prod 应用”和“新 prod 容器”长期同时连接并写入同一套生产库
- 影子 prod 阶段不要和旧 prod 共用同一个数据库账号；建议单独创建例如 `openchat_shadow`
- 本次实际排查中，旧 prod 与影子 prod 共用了 `openchat` 账号，导致 host grant 和密码调整互相踩踏，已经通过拆分账号修复
- 第一次上线建议采用：
  - 影子启动
  - 健康检查与少量验证
  - 切流
  - 停掉旧 prod 应用

也就是说，问题不在端口，而在于是否允许两套生产应用长期同时读写同一套数据库。

## 端口说明

### Test 默认端口

- MySQL: `13306`
- API: `16061`
- gRPC: `16062`
- Web: `18080`

### Prod 默认端口

- API: `26061`
- gRPC: `26062`
- Web: `28080`

prod 目前默认使用影子端口，也就是说这次 PR 不会自动把公网流量切到新的生产栈。

## 主仓库第一次上线建议步骤

1. 合并这条 PR
2. 配置 `test` environment secrets
3. 准备 `/srv/catscompany-test/env/test.env`
4. 让 `main` 自动触发 `Deploy Docker Test`
5. 验证：
   - GHCR 包是否在主仓库所属账号下生成
   - test 栈是否健康
6. 配置 `prod` environment secrets
7. 准备 `/srv/catscompany-prod/env/prod.env`
8. 确认 prod 能通过 `OC_DB_DSN` 连到现有 MySQL
9. 为影子 prod 单独准备数据库账号，例如 `openchat_shadow@172.17.0.1`
10. 不要让影子 prod 直接复用旧 prod 正在使用的数据库账号
11. 允许 `Deploy Docker Prod` 运行
12. 验证 prod 影子栈的 `26061` / `28080`
13. 单独决定 nginx 的切流方式

## 回滚

生产环境骨架包含：

- `deploy/prod/remote-rollback.sh`

它会通过恢复 `PREVIOUS_REVISION` 中记录的旧镜像 SHA，并重新执行 compose，实现快速回滚。

## 当前限制

- prod 还没有在 fork 的服务器环境里做过真实验证
- nginx 切流不在本次 PR 自动化范围内
- 数据库迁移仍依赖 `server/cmd/server.go` 中的应用启动逻辑
- 这次 PR 主要聚焦镜像化自动部署，不涉及整套基础设施迁移
