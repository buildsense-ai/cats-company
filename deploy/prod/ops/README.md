# Cloud worker ops scripts (B4-1)

云托管虚拟员工的云操作脚本，跑在 cats-company 生产 server（alpine Linux 容器）内，
由控制面 `/api/cloud-workers` 通过 `runScript` 直接 `exec` 调用。

## 脚本清单

| 脚本 | 动作 | 语义 |
|---|---|---|
| `list-worker-images.sh` | 列出 bake 通道镜像 | `/api/cloud-workers/meta` 展示 + 回滚选择 |
| `provision-worker.sh` | 创建实例 + 注入身份 + 写 localConfig + 启 service | 新建云托管员工 |
| `destroy-worker.sh` | 删实例 + key pair + 本地 state | 删除（幂等） |
| `reset-worker.sh` | 销毁重建（丢数据） | 重置 / 重装 |
| `rollback-worker.sh` | 切换 `/opt/catsco/current`（保数据） | 版本回滚 |

## 部署配置（B4-2 对接）

在 server 进程环境里配置以下 `CATSCO_WORKER_*_SCRIPT`，指向容器内
`/opt/catsco/ops/`（Dockerfile 已 `COPY deploy/prod/ops /opt/catsco/ops` +
`chmod 0755`）：

```bash
CATSCO_WORKER_PROVISION_SCRIPT=/opt/catsco/ops/provision-worker.sh
CATSCO_WORKER_DESTROY_SCRIPT=/opt/catsco/ops/destroy-worker.sh
CATSCO_WORKER_RESET_SCRIPT=/opt/catsco/ops/reset-worker.sh
CATSCO_WORKER_ROLLBACK_SCRIPT=/opt/catsco/ops/rollback-worker.sh
CATSCO_WORKER_IMAGES_SCRIPT=/opt/catsco/ops/list-worker-images.sh
CATSCO_WORKER_CREATE_QUOTA=            # "<uid>=<n>;<uid>=<n>"，留空 = 未开放（0）
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
CTYUN_WORKER_AZ_NAME=cn-huanan2-2A-public-ctcloud
CTYUN_WORKER_FLAVOR_ID=<flavor-id>
CTYUN_WORKER_VPC_ID=<vpc-id>
CTYUN_WORKER_SUBNET_ID=<subnet-id>
CTYUN_WORKER_SECURITY_GROUP_ID=<sg-id>
CTYUN_WORKER_STATE_DIR=/var/lib/catsco-worker   # 默认 <dir>/<tenant>，见下
CATSCO_WORKER_HTTP_BASE_URL=https://app.catsco.cc   # 缺省
CATSCO_WORKER_SERVER_URL=wss://app.catsco.cc/v0/channels  # 缺省
```

- `CTYUN_WORKER_*`（region/az/flavor/vpc/subnet/sg）与 XiaoBa-CLI bake 管线的
  repo vars 一致（worker 实例跑在 bake 的 worker 镜像上）。
- `CTYUN_WORKER_STATE_DIR` 必须**持久化挂载**：其下每个 tenant 保存
  `id_rsa`（私钥）、`known_hosts`、`inject.env`（身份快照，reset 复用）。
  默认 `/var/lib/catsco-worker/<tenant>`。
- ⚠️ **公网 IP 配额**：provision 需要公网 IP（`extIP 1`）做 SSH 注入；若区域
  公网 IP 配额不足，`CreateEcsInstance` 会报 `Ecs.Order.ProcFailed`（实测，
  2026-08-07 华南2 配额紧张）。

## 脚本依赖（Dockerfile 已装）

`bash`、`openssh-client`（ssh/ssh-keygen）、`jq`、GNU `timeout`、`ctyun-cli`
（SHA256 校验安装）。脚本全部 `set -Eeuo pipefail` + shebang 可执行。

## 安全注意事项

- **凭据不落前端**：`--api-key` / `--login-token` 经 argv 传子脚本；`inject.env`
  与私钥 `chmod 600`，仅 root（或运行用户）可读。
- **tenant / version 入参正则校验**：`^[a-z0-9][a-z0-9_-]{1,63}$` /
  `^[A-Za-z0-9._-]+$`，防路径/glob 注入。
- **fail-closed**：任一步失败聚合报错退出非 0；key pair 只在本次新建时才由
  失败清理删除（复用对象不动）；实例删除必须 `--clientToken` 且不带
  `--projectID`（天翼云 API 实测，2026-08-07）。

## 本地测试

需要 jq + Git Bash（Windows）。`CATSCO_JQ` 指向 jq 可执行文件：

```bash
export CATSCO_JQ=/path/to/jq
cd deploy/prod/ops && node --test *.test.mjs
```

30 个测试覆盖 list / provision / destroy / reset / rollback（fake ctyun-cli +
fake ssh + fake timeout）。
