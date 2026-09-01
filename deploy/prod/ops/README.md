# Cloud worker ops scripts (B4-1)

云托管虚拟员工的云操作脚本，跑在 cats-company 生产 server（alpine Linux 容器）内，
由控制面 `/api/cloud-workers` 通过 `runScript` 直接 `exec` 调用。

## 脚本清单

| 脚本 | 动作 | 语义 |
|---|---|---|
| `list-worker-images.sh` | 列出 bake 通道镜像 | 创建 / 重置镜像选择 |
| `list-worker-releases.sh` | 列出私有 TOS 应用发布 | 更新 / 回滚版本选择 |
| `prune-worker-releases.sh` | 清理过旧 TOS worker 发布（默认 dry-run） | 定期维护版本目录，保护当前运行版本 |
| `provision-worker.sh` | 创建实例 + 注入身份 + 写 localConfig + 启 service | 新建云托管员工 |
| `destroy-worker.sh` | 退订并永久销毁实例 + key pair + 本地 state | 内部运维清理 / 到期保留期结束（幂等） |
| `reset-worker.sh` | 原实例重装（丢数据，保留包月订单/到期时间） | 重置 / 重装 |
| `renew-worker.sh` | 延长包月或恢复 `expired/freezing` 实例，并关闭自动续费 | 套餐续费后的云员工续订/恢复 |
| `deploy-worker-version.sh` | 安装指定应用版本（保数据） | 更新 / 本地版本缺失时回滚 |
| `rollback-worker.sh` | 切换 `/opt/catsco/current`（保数据） | 版本回滚 |
| `status-worker.sh` | 批量读取实例、镜像与版本状态 | 云员工管理页状态 |
| `bootstrap-artifact-gateway.sh` | 初始化共享 HTTPS 网关 | 一次性安装 Nginx、通配符证书和路由器 |
| `ensure-artifact-gateway-network.sh` | 对齐网关公网网络 | 创建或复用 DNAT、安全组规则和通配符 DNS |
| `configure-worker-artifact.sh` | 配置单个私网 worker | 安装静态服务并注册 Agent UID 路由 |
| `sync-artifact-gateway-routes.sh` | 重建全部 Artifact 路由 | 网关恢复或路由表校正 |

## 部署配置（B4-2 对接）

在 server 进程环境里配置以下 `CATSCO_WORKER_*_SCRIPT`，指向容器内
`/opt/catsco/ops/`（Dockerfile 已 `COPY deploy/prod/ops /opt/catsco/ops` +
`chmod 0755`）：

```bash
CATSCO_WORKER_PROVISION_SCRIPT=/opt/catsco/ops/provision-worker.sh
CATSCO_WORKER_DESTROY_SCRIPT=/opt/catsco/ops/destroy-worker.sh
CATSCO_WORKER_RESET_SCRIPT=/opt/catsco/ops/reset-worker.sh
CATSCO_WORKER_RENEW_SCRIPT=/opt/catsco/ops/renew-worker.sh
CATSCO_WORKER_UPDATE_SCRIPT=/opt/catsco/ops/deploy-worker-version.sh
CATSCO_WORKER_ROLLBACK_SCRIPT=/opt/catsco/ops/rollback-worker.sh
CATSCO_WORKER_IMAGES_SCRIPT=/opt/catsco/ops/list-worker-images.sh
CATSCO_WORKER_RELEASES_SCRIPT=/opt/catsco/ops/list-worker-releases.sh
CATSCO_WORKER_RELEASE_KEEP_COUNT=10
CATSCO_WORKER_STATUS_SCRIPT=/opt/catsco/ops/status-worker.sh
CATSCO_WORKER_CREATE_QUOTA=            # 仅灰度/运维静态配额；正式公共环境留空，付费权益走 cloud_worker_credits
CTYUN_WORKER_EXT_IP=0                  # 默认内网，不申请公网 IP/带宽
```

未配置某脚本时，对应动作返回 503（fail-closed）；删除未配 destroy 脚本时
`DELETE /api/cloud-workers/{name}` 返回 503 且保留记录（无 `?force=1` 绕过
——fail-closed，运维需配置 destroy 脚本或走云控制台/DB 层处理）。

### 云凭据与环境（CTYUN_*）

所有脚本通过 `ctyun-cli` 调天翼云 API，凭据由 `CTYUN_AK` / `CTYUN_SK`
（`~/.ctyun-cli.yaml` 或环境变量）提供，**只落在服务端，不进前端/仓库**。

```bash
CTYUN_WORKER_REGION_ID=200000002530       # 华南2
CTYUN_WORKER_PROJECT_ID=0                 # 企业项目（0 = default）
CTYUN_IMAGE_PROJECT_ID=<bake-image-project-id> # 必填；与 worker 项目隔离，禁止回退到 0
CTYUN_WORKER_AZ_NAME=<worker-az-name>
CTYUN_WORKER_FLAVOR_ID=<worker-flavor-id>
CTYUN_WORKER_VPC_ID=<worker-vpc-id>
CTYUN_WORKER_SUBNET_ID=<worker-subnet-id>
CTYUN_WORKER_SECURITY_GROUP_ID=<worker-security-group-id>
CTYUN_WORKER_STATE_ROOT=/var/lib/catsco-worker  # 默认 <root>/<tenant>，见下
CTYUN_WORKER_BILLING_MODE=month          # month（默认包月）或 ondemand
CTYUN_WORKER_CYCLE_COUNT=1               # 包月购买月数，1-60
CTYUN_WORKER_AUTO_RENEW=0                # 云托管严禁自动续费；到期由天翼云保留期和控制面清理
CATSCO_WORKER_ARTIFACT_BUCKET=<dedicated-worker-artifact-bucket>
CATSCO_WORKER_ARTIFACT_PREFIX=update/worker
CATSCO_WORKER_ARTIFACT_REGION=cn-guangzhou
CATSCO_WORKER_ARTIFACT_ENDPOINT=https://tos-cn-guangzhou.volces.com
CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID=<read-only-ak>
CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY=<read-only-sk>
CATSCO_WORKER_ARTIFACT_CACHE_DIR=/var/lib/catsco-worker/.artifacts
CATSCO_WORKER_HTTP_BASE_URL=https://app.catsco.cc   # 缺省
CATSCO_WORKER_SERVER_URL=wss://app.catsco.cc/v0/channels  # 缺省
```

- `CTYUN_WORKER_*`（region/az/flavor/vpc/subnet/sg）与 XiaoBa-CLI bake 管线的
  repo vars 一致（worker 实例跑在 bake 的 worker 镜像上）。
- 上述值必须从天翼云专用 worker 项目的只读资源查询结果写入生产主机
  owner-only `prod.env`；禁止提交到仓库，且切换资源池或企业项目时不能跨项目复用。
- 私网 worker 默认通过 NAT/跳板注入。`CTYUN_JUMP_IP` 必须是生产服务器可达的
  跳板入口；云厂商控制台里显示的 jump-host 私网地址不能直接当公网入口。
- `CTYUN_WORKER_STATE_ROOT` 必须**持久化挂载**：其下每个 tenant 保存
  `id_rsa`（私钥）、`known_hosts`、`inject.env`（身份快照，reset 复用）。
  默认 `/var/lib/catsco-worker/<tenant>`。
- worker 应用包从私有 TOS 桶下载。生产凭证只授予
  `catsco-worker-release/update/worker/*` 的只读权限，不下发给浏览器或 worker。

### TOS 发布生命周期

控制面展示专用 worker artifact 桶中实际发现的全部 `manifest.json` 发布，不再截断为固定数量。
生产主机可通过 `prune-worker-releases.sh` 定期清理旧对象：脚本默认只输出候选（dry-run），
按发布时间保留最近 `CATSCO_WORKER_RELEASE_KEEP_COUNT` 个版本；启用 `--apply` 时还要求状态脚本
成功，并始终保留当前 worker 正在运行的 `app_version`。任何列表或状态读取失败都会拒绝删除。
建议先审阅 dry-run 输出，再将 `--apply` 放入受保护的 systemd timer/cron；凭据和定时任务只配置
在生产服务器，不提交仓库。`--apply` 还必须显式设置
`CATSCO_WORKER_RELEASE_DELETE_CONFIRM=I_UNDERSTAND_DELETE_WORKER_RELEASES`，并使用具备删除权限的
运维 TOS 凭据；应用下载凭据仍保持只读。发布清理不会删除 worker 本地正在使用的 `/opt/catsco/releases`。
- 云托管员工按套餐 30 天计费；不独立自动续费。套餐到期后天翼云进入 15 天冻结保留期（不收费、不可用），续费可恢复；窗口结束后控制面核对实例并调用退订/销毁接口，按量实例沿用直接删除。
  供给失败清理会短暂重试实例目录，并使用创建时记住的实例 ID 和计费模式
  兜底，避免目录最终一致性造成持续计费的孤儿实例。
- 私网 worker 默认不申请公网 IP（`CTYUN_WORKER_EXT_IP=0`），SSH 注入依赖
  `CTYUN_JUMP_IP` 跳板/NAT；只有旧的直连公网路径显式设置 `CTYUN_WORKER_EXT_IP=1`。

### 续费与到期边界

续费链路只恢复仍属于当前天翼云区域生命周期的实例，并且不会创建替代实例：

| Provider 状态 | 续费行为 |
|---|---|
| `running` / `active` / `stopped` / `shutoff` / `error` | 调用 `ResubscribeEcsInstance` 延长套餐 |
| `expired` / `freezing` / `frozen` | 仍在 15 天保留窗口内，调用 `ResubscribeEcsInstance` 恢复 |
| `unsubscribed` | 已离开可恢复生命周期，拒绝续费并要求人工核对 |
| `released` / `deleted` / `bootdiskexpired` / `nobootdisk` | 终态，拒绝续费 |

套餐支付成功后，控制面以天翼云返回的 `expires_at` 更新单个 worker 的生命周期，
并尝试关闭云侧自动续费。关闭失败不会伪造成功，日志会标记为需要运维核对。
到期后先进入 `delete_pending`，只有 `delete_after = package_expires_at + 15d`
到达后才会 claim 为 `delete_running` 并永久销毁；已进入 `delete_running` 的记录
不会被后续支付重新激活，避免和进行中的云端销毁竞态。销毁 API 使用相同
`clientToken` 做有限重试，瞬时 API 错误不会留下未清理的实例，也不会重复提交
非幂等请求。

## 私网 Artifact 转发

私网 worker 不再为每台机器申请公网 IP。页面文件仍由对应 worker 本地保存和
提供，只把公网入口集中到跳板机：

```text
https://agent-<uid>.artifacts.catsco.fun:19991/artifacts/...
  -> 通配符 DNS / DNAT
  -> 跳板机 Nginx（按 Host 查 Agent UID）
  -> worker-private-ip:19990/artifacts/...
```

启用前，在生产 `prod.env` 填写 `env.prod.example` 中的 Artifact gateway 参数。
DNS Access Key 只供网关初始化和证书续签使用，不会注入 worker。然后在 server
容器中执行一次：

```bash
/opt/catsco/ops/bootstrap-artifact-gateway.sh
```

该命令幂等地完成 DNAT、安全组、通配符 DNS、Let's Encrypt 通配符证书、
Nginx 和路由命令安装。成功后将以下开关保持为：

```env
CATSCO_ARTIFACT_GATEWAY_ENABLED=1
CATSCO_WORKER_ARTIFACT_HOST_MODE=forwarded
```

此后 `provision-worker.sh` 和 `reset-worker.sh` 会自动完成三件事：在 worker
安装 `catsco-artifact-host.service`，向 `/srv/catsco-agent/.env` 注入
`forwarded` 发布参数，以真实 Agent UID 注册网关路由。Artifact 配置失败只返回
`artifact_status=warning`，不会回滚已经可用的虚拟员工。`destroy-worker.sh` 会
尽力删除对应路由。

网关路由表丢失或从备份恢复后，运行：

```bash
/opt/catsco/ops/sync-artifact-gateway-routes.sh
```

它只从持久化 worker state 和当前运行实例重建路由，不从实例名猜 Agent UID。
排障时依次检查：

```bash
curl --fail https://gateway-probe.artifacts.catsco.fun:19991/__artifact_gateway_health
/opt/catsco/ops/artifact-gateway-route.sh status <agent-uid>
curl --fail http://<worker-private-ip>:19990/__artifact_health
curl --fail https://agent-<agent-uid>.artifacts.catsco.fun:19991/artifacts/artifacts-index.json
```

回滚时将 `CATSCO_ARTIFACT_GATEWAY_ENABLED=0` 且
`CATSCO_WORKER_ARTIFACT_HOST_MODE=`，重新创建 server 容器。旧的公网 worker
继续使用 Skill 的 `direct-https` 模式；关闭开关不会删除已有 Artifact 文件。

## 脚本依赖（Dockerfile 已装）

`bash`、`openssh-client`（ssh/ssh-keygen）、`jq`、Node.js、GNU `timeout`、`ctyun-cli`
和镜像内单独构建的 `tos-fetch`。脚本全部 `set -Eeuo pipefail` + shebang 可执行。

## 安全注意事项

- **凭据不落前端**：控制面通过 root/运行用户可读的临时 `0600` credential
  文件把 JWT/API key 传给 provision/reset；脚本仍兼容旧的 argv 选项供人工运维。
  持久化的 `inject.env` 与私钥同样必须 `chmod 600`，仅运行用户可读。
- **tenant / version 入参正则校验**：`^[a-z0-9][a-z0-9_-]{1,63}$` /
  `^[A-Za-z0-9._-]+$`，防路径/glob 注入。
- **fail-closed**：任一步失败聚合报错退出非 0；key pair 只在本次新建时才由
  失败清理删除（复用对象不动）；实例删除必须 `--clientToken` 且不带
  `--projectID`（天翼云 API 实测，2026-08-07）。
- **旧 key pair 恢复**：若同名实例不存在、云端仍有 tenant key pair，但持久化
  tenant 目录中的私钥缺失或损坏，provision 会在创建计费实例前替换该孤儿
  key pair 并生成新的 tenant 私钥，避免创建后等待 SSH 超时。

## 本地测试

需要 jq + Git Bash（Windows）。`CATSCO_JQ` 指向 jq 可执行文件：

```bash
export CATSCO_JQ=/path/to/jq
cd deploy/prod/ops && node --test *.test.mjs
```

测试覆盖 list / status / provision / update / destroy / reset / rollback，及 Artifact
网关路由、DNS、静态服务和 lifecycle 的非阻塞接入
（fake ctyun-cli + fake ssh + fake timeout），包含包月、按量、到期退订、
失败回收、版本安装与状态同步路径。
