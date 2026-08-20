# 云托管控制面：旧部署链路移除与迁移/回滚方案

## 背景

本仓库此前存在两套"云部署"入口：

1. **旧链路（gauz-platform）**：`POST /api/bots/deploy` + `Deployer` 客户端（`server/deployer.go`）+ `DEPLOY_API_URL` 环境变量，通过外置 gauz-platform HTTP 服务创建/查询/删除云托管 bot。
2. **新控制面**：`/api/cloud-workers`（云托管入口），通过服务端配置的可执行脚本（`CATSCO_WORKER_*_SCRIPT`）完成 provision / rollback / reset / destroy / images 操作。

旧链路在 `f635b16` 移除，原因：

- 前端（webapp）对该接口**零引用**，且前后端同仓部署、同步发版，没有存量客户端依赖它。
- 旧链路把部署凭据与调用逻辑耦合在 server 进程内（`Deployer` 直接持有 `DEPLOY_API_URL`），与新控制面"凭据留在脚本/服务端、脚本化部署"的设计冲突。
- 旧链路是 `tenant_name` 的第二个标记来源，与云托管控制面"唯一来源"的设计目标冲突。

## 升级影响

- 仍调用 `/api/bots/deploy`、`Deployer.Status`、`Deployer.Remove` 的旧客户端会收到 404/405。由于前端零引用且同仓部署，实际无存量调用方；如需兼容，见"回滚方案"。
- 数据库里已有 `tenant_name` 的 bot（旧链路托管或历史标记）会被新控制面 `/api/cloud-workers` 列表归为 cloud worker：
  - 列表 `status` 为 `unknown`（新控制面不反查旧 gauz-platform 状态；回滚/重置/删除的操作结果由脚本反馈）。
  - **删除走新的 `CATSCO_WORKER_DESTROY_SCRIPT`；未配置 destroy 脚本时 `DELETE /api/cloud-workers/{name}` 返回 503 且保留记录**（fail-closed，防止"无法销毁实例却删除唯一可定位记录"导致孤儿实例持续计费）。**无公开 force 开关**（`?force=1` 不生效——任意 owner 不能绕过保护）；运维必须先配置 destroy 脚本才能走正常删除，或直接在云控制台/DB 层处理。
  - 创建失败时的兜底：若 provision 脚本已创建实例后失败，服务端会立即按 `tenant_name` 调用 destroy 脚本清理；若销毁也失败，则**保留 bot 记录并落 `tenant_name`** 作为可重试句柄（列表仍可见，可重试删除），而不是先删唯一关联记录。**同一不变量也适用 finalize（SetTenantName）失败**：只有 destroy 确认成功才删除 bot 记录。
  - **镜像列表契约**：`CATSCO_WORKER_IMAGES_SCRIPT` 必须指向 Linux 可执行脚本（B4-1 的 `list-worker-images.sh`），输出每行一个镜像的 TSV：`imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status`。控制面 `/api/cloud-workers/meta` 解析为结构化数组。PowerShell `Manage-WorkerImages.ps1` 是 CI/开发机 bake 工具链，不能在 Linux server 上执行，不作为控制面脚本。

## 迁移路径

1. 配置新控制面所需脚本（见 `.env.example`）：`CATSCO_WORKER_PROVISION_SCRIPT`、`CATSCO_WORKER_UPDATE_SCRIPT`、`CATSCO_WORKER_RESET_SCRIPT`、`CATSCO_WORKER_ROLLBACK_SCRIPT`、`CATSCO_WORKER_DESTROY_SCRIPT`、`CATSCO_WORKER_IMAGES_SCRIPT`。
2. 新 worker 一律通过云托管入口创建（配额 `CATSCO_WORKER_CREATE_QUOTA` 控制）。
3. 存量旧托管 bot：需要保留的，在新控制面可见并可用 rollback/reset 管理；不再需要的，配置 destroy 脚本后删除。reset 会先删除旧实例和同名云 key pair，再在 tenant 独立状态目录生成新私钥；若实例已经不存在但遗留云 key pair、tenant 私钥又缺失，provision 会在创建新实例前替换该孤儿 key pair，避免进入 SSH 超时循环。
4. 旧 gauz-platform 部署服务确认无调用后下线，清理其环境变量与面向当前使用的文档、界面文案；本文档保留旧链路名称，作为迁移和回滚审计记录。

## 版本化运维契约

- **更新**：安装并切换到所选应用版本，保留 `/srv/catsco-agent`。
- **回滚**：切换到所选历史应用版本，保留 `/srv/catsco-agent`。目标版本不在
  worker 本地时，复用更新脚本下载该版本。
- **重置**：使用所选镜像销毁并重建 worker，数据会丢失。控制面必须从当前
  数据库和请求重新传入该机器人的 API key、拥有者身份及登录凭证，不信任旧
  `inject.env` 作为主要来源。
- 控制面只展示最近 6 个镜像版本。worker 已安装版本保留在
  `/opt/catsco/releases`，再次选择时不下载。
- `CATSCO_WORKER_ARTIFACT_CACHE_DIR` 是 CatsCompany 控制服务器上的共享制品
  下载缓存：它只让不同 worker 复用同一个 tar.gz，worker 仍然是一台云服务器
  对应一个虚拟员工。缓存文件每次按发布 manifest 的 SHA256 校验，损坏时重下。
- `CTYUN_WORKER_STATE_ROOT/<tenant>` 分开保存每个 worker 的 SSH key、known_hosts
  和身份快照，禁止多个 tenant 共用同一个状态目录。

## 回滚方案

若需临时恢复旧链路：

- **代码级**：`git revert f635b16`（恢复 `server/deployer.go`、`/api/bots/deploy` 路由、`HandleDeployBot`、`Deployer.Status/Remove`、`DEPLOY_API_URL`）。注意 revert 会让 `tenant_name` 重新出现双标记来源，且与云托管删除流程并存——仅作为兼容期临时手段。
- **发布级**：回退到包含旧链路的已发布 server 二进制（旧发布包内仍含 `/api/bots/deploy`）。

## 回归测试

`server/cloud_workers_test.go` 覆盖存量 `tenant_name` bot 在新控制面的行为：

- `TestCloudWorkerHandleList`：带 `tenant_name` 的存量 bot 被列表归类，自托管 bot（无 `tenant_name`）被排除。
- `TestCloudWorkerHandleDelete`：存量 `tenant_name` bot 删除——配置 destroy 时销毁实例并删记录；未配置时 503 fail-closed（记录保留）；无公开 force 开关（`?force=1` 不生效）。
- `TestCloudWorkerHandleCreateProvisionFails`：provision 失败时 destroy 兜底（销毁成功则回滚 bot 记录；无 destroy 或销毁失败则保留记录并落 `tenant_name` 作为可重试句柄）。
