# 云虚拟员工控制面（Part B：云托管）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

## 当前状态（2026-08-07）

- **XiaoBa-CLI 侧（Part A）已完成：** worker 镜像 bake 闭环（v1.4.8 验收通过） + 镜像生命周期模块 A（`Manage-WorkerImages.ps1`：`-Action List/Latest/Prune -Keep 6`，bake 后自动清理）。详见 `E:\work\xiaoba\XiaoBa-CLI\docs\superpowers\plans\2026-08-07-worker-cloud-control-image-lifecycle.md`。
- **本文档（Part B，cats-company 侧）：** 在「AI 助手管理」对话框的**「云托管」入口**启用云虚拟员工的统一管理：创建（带配额）、版本展示、**回滚 / 重置两个动作分开**、单 worker 逐个操作。
- **关联：** 用户确认 —— 回滚（保留数据，Part A 制品切版本）与重置（丢弃数据，销毁重建到镜像）**两个都提供且 UI/文档写清楚分开**；创建配额用**环境变量控制，初始 0**；协作规则：**文档更新可直推 main，代码一律走 PR，不用 admin 合并**。

## 现状调研（已核实，2026-08-07）

### 前端
- **「AI 助手管理」对话框**：`webapp/src/widgets/agent-store-modal.jsx`（约 1700 行），三 tab：`hub`/`create`/`manage`。
  - `create` tab 的部署方式 radio：`自托管`（SELF_HOSTED，可用）与 **`云托管`（MANAGED，当前 `disabled`，文案"无需部署，创建后直接使用，即将推出"）**——**本计划要启用它**。
  - `handleCreate`：`api.createBot({username, display_name}, isManaged)`；自托管创建后强制双向加好友。
  - hub 列表区分 `我创建的 · 云托管`（有 `tenant_name`）/`我创建的 · 自托管`。
- **API 封装**：`webapp/src/api.js`（`request()`，Bearer token）；相关：`getMyBots`/`createBot(..., deployToCloud)`→`/api/bots` 或 `/api/bots/deploy`、`getAgents`→`/api/agents`、`getAgentQuota(uid)`→`/api/agents/quota`、`getCloudArtifacts` 等。
- **i18n**：`webapp/src/i18n/zh-CN.json`（`bot_*` 键）。

### 后端
- **路由**：`server/cmd/server.go`（`http.NewServeMux` + `mux.HandleFunc` + `chainHTTP(handler, mw...)`）。
- **云部署**：`server/deployer.go` `Deployer`（gauz-platform 客户端，`DEPLOY_API_URL`）：仅 `Deploy`/`Status`/`Remove` 3 个操作——**缺镜像列表/回滚/重置等管理操作**。
- **`server/botmgr.go`** `HandleDeployBot`：建 bot 账号 → `deployer.Deploy()`（失败回滚删 bot）→ `SetTenantName` → 双向加好友 → `deploymentStatus()`。
- **`server/agents.go`** `AgentHandler`：虚拟员工花名册（`/api/agents`）、`/api/agents/quota`（relay 用量，30s 缓存）、`HandleOpenAgent`。
- **版本数据**：`server/bot_definition.go` `BotDefinitionRuntime`（`DesiredRevision/AppliedRevision/LastAttempt*/LastError`）。
- **参考模板**：`server/relay_admin_proxy.go`（admin 白名单 + 短时 cookie + 反向代理 + 限流/审计）。
- **配额模型参考**：`store/types` `CommercialPlan/Entitlement/QuotaGrant/LedgerEntry`（中转移民层）。

### 关键待澄清（写实现前必须定）
1. **「云托管」员工的技术底座**：现有 `deployer.go` 走 **gauz-platform**；而用户说的"云虚拟员工 / 镜像 / 部署云机器拉最新镜像"指向 **天翼云 worker 镜像实例**（XiaoBa-CLI `ops/ctyun-worker-image` 管线）。
   - 方案 A：云托管 = **天翼云 worker 镜像实例**（新增管理面，对接 `Manage-WorkerImages.ps1` 与云 API/脚本创建、回滚、重置）。
   - 方案 B：云托管 = 现有 gauz-platform bot 部署（仅启用 MANAGED radio + 补镜像/回滚/重置能力到 deployer）。
   - **倾向方案 A**（与用户"镜像保留 6/回滚/重置/部署拉最新镜像"完全对应），但需与现有 `/api/bots/deploy` 关系理清（可能并存：gauz 部署 vs 天翼云镜像 worker）。
2. **创建/销毁/重置的云操作由谁执行**：cats-company 直接调天翼云 API，还是经 XiaoBa-CLI 脚本（`Manage-WorkerImages.ps1`/新建 `Provision-WorkerInstance.ps1`）代理？建议由 XiaoBa-CLI 侧提供脚本（凭据集中在 CI/服务侧），cats-company 经 HTTP/脚本调用。

## 模块

### 模块 B1：云托管入口启用 + 云虚拟员工列表（前端 + 后端）
- [ ] **步骤 B1-1：后端「云虚拟员工」列表 API**
  - 新增 `GET /api/cloud-workers`（jwt+owner）：返回云虚拟员工（天翼云实例或云托管 bot）列表：`name/status/version/commit/imageID/createdTime`。
  - 数据源：查询云镜像/实例（对接 XiaoBa-CLI `Manage-WorkerImages.ps1 -Action List` 或云 API）；与现有 `/api/agents`（bot 花名册）关系理清（建议云托管员工独立罗列）。
- [ ] **步骤 B1-2：前端启用「云托管」radio + 员工管理视图**
  - `agent-store-modal.jsx`：`MANAGED` radio 由 `disabled` → 可用（受配额/开关控制）；创建后进入"云托管员工"管理视图（列表：名称/状态/**版本**/镜像/创建时间）。
  - `api.js` 新增 `getCloudWorkers` / `createCloudWorker` / `rollbackCloudWorker` / `resetCloudWorker`；i18n 补 `bot_*` 键。

### 模块 B2：创建配额（环境变量，初始 0）
- [ ] **步骤 B2-1：后端配额**
  - 环境变量（例如 `CATSCO_WORKER_CREATE_QUOTA=<uid>=<n>;...`，**初始 0** 即未配置不可创建）；解析复用 `envInt64Set` 风格。
  - 创建接口校验：已创建云托管员工数 < 配额才放行；创建成功扣减（或按"已创建数 < 配额"实时判断）。
- [ ] **步骤 B2-2：前端配额展示**
  - 「可创建云虚拟员工次数」展示在创建按钮处；剩余 0 → 置灰并提示；有剩余 → 可点触发创建。

### 模块 B3：版本展示 + 「回滚」与「重置」两个动作（分开，写清楚）
- [ ] **步骤 B3-1：版本展示**：员工列表显示 `version/commit`（来自镜像 label 或运行状态）。
- [ ] **步骤 B3-2：「回滚（保留数据）」**：`POST /api/cloud-workers/{name}/rollback` —— Part A 制品切版本（`update-worker-artifact.sh --rollback`），`/srv/catsco-agent` 数据不动。UI 标注"保留数据"。
- [ ] **步骤 B3-3：「重置 / 重装（丢弃数据）」**：`POST /api/cloud-workers/{name}/reset` —— 销毁该 worker 云实例 → 从所选历史镜像（`Manage-WorkerImages.ps1 -Action List`）或最新镜像重建 → 初始化供给。UI 标注"丢弃数据，不可恢复"+ 强二次确认；**与回滚严格分开**（不同入口/警示色/确认文案）。

### 模块 B4：后端能力接入（天翼云 worker 镜像实例）
- [ ] **步骤 B4-1：XiaoBa-CLI 侧供给/重置脚本**（在 XiaoBa-CLI 仓库）
  - 新建 `Provision-WorkerInstance.ps1`（按最新/指定镜像创建实例 + 初始化供给）、`Reset-WorkerInstance.ps1`（销毁重建）；与 `Manage-WorkerImages.ps1` 配套。
- [ ] **步骤 B4-2：cats-company 对接**：新增 handler（参考 `relay_admin_proxy.go`/`cloud_artifacts.go` 模式），把云托管员工操作转发到 XiaoBa-CLI 脚本/云 API（凭据经服务端环境变量）。

## 环境变量（新增）
- `CATSCO_WORKER_CREATE_QUOTA`（创建配额，`<uid>=<n>` 分号分隔，默认空=0）
- 云操作相关（镜像 API base / 凭据 / XiaoBa-CLI 脚本路径）——实现时定名，凭据只在服务端/CI，不落前端。

## 测试与验收
- 前端：`agent-store-modal` / 相关组件测试（vitest，参考现有 `*.test.jsx`）；radio 启用、配额置灰、回滚/重置 UI 分开。
- 后端：Go 测试（配额解析/校验、列表/回滚/重置 handler 的 fake 依赖）。
- 云端验收：真实天翼云 worker 实例上验证「回滚（保数据）」与「重置（丢数据）」行为差异。

## 依赖与顺序
1. **B4-1**（XiaoBa-CLI 供给/重置脚本）先行——控制面操作依赖它。
2. **B1/B2**（列表 + 配额 + 启用云托管）→ 独立可交付。
3. **B3**（回滚/重置，依赖 B4 能力）。

## 协作规则
- 代码改动一律 PR（不 admin 合并）；文档更新可直推 main。
