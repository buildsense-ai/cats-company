# 云员工控制面（Part B: cats-company 侧）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 webapp/服务端提供"云员工"控制面：①新开云员工（拉初始化镜像创建 ECS + 首次引导）；②检查已有云员工版本（心跳上报 vs 最新版本）；③用户确认后服务端执行应用层更新（调用 Part A 的分发/更新能力）。

**架构：** 服务端持有天翼云 AK/SK（`CTYUN_AK`/`CTYUN_SK`，新增环境变量，参考 `server/artifact_runtime_config.go` 的 env 读取模式），封装天翼云 client（ECS 建实例、IMS 查镜像）。worker（XiaoBa agent）通过心跳上报应用版本（`worker-release.json` 的 version/commit）与镜像版本（`/etc/catsco-image.json`）；服务端存储并暴露版本状态。webapp 在机器人/云员工页面加"新开云员工 / 检查更新 / 更新确认"入口；更新执行时服务端触发 Part A 的分发通道（`deploy-worker-artifact` 的脚本契约），worker 侧脚本保证校验/冒烟/回滚。

**技术栈：** Go（server）、React/Vite（webapp）、天翼云 OpenAPI（ECS/IMS）、复用 XiaoBa-CLI Part A 的 `update-worker-artifact.sh` / `deploy-worker-artifact.mjs` 契约。

**关联计划：** Part A（XiaoBa-CLI 应用制品更新）见 `E:\work\xiaoba\XiaoBa-CLI\docs\superpowers\plans\2026-08-04-worker-artifact-update.md`。本计划依赖 Part A 交付的脚本契约；若 Part A 未完成，Part B 的"更新执行"任务阻塞。

**关键事实（已核实）：**
- 服务端当前**无**天翼云 AK/SK；仅有火山 DNS 的 `VOLC_ACCESSKEY`/`VOLC_SECRETKEY`（`server/artifact_runtime_config.go`，可作 env 读取/配置模式参考）。
- 本地有 `ctyun-cli` + `~/.ctyun-cli.yaml`（天翼云 AK/SK 在本地，部署时迁移为服务端环境变量）。
- 现有机制：`AgentHandler`（`server/agents.go`，`/api/agents`、`/api/agents/quota`）、`BotHandler`（`/api/bots/*`，含 body-status）、设备模型状态 `DeviceModelStatus`（`userDeviceRegistry`）；webapp 机器人选择器在 `webapp/src/widgets/bot-model-selector.jsx`、挂载于 `webapp/src/views/tinode-web.jsx`。
- worker 上线后通过 WebSocket/HTTP 上报设备能力与模型状态；应用/镜像版本上报为**新增字段**（Part B 任务 2/3）。

---

## 文件结构

**创建（server）：**
- `server/ctyun_config.go` — 天翼云 AK/SK/region 等 env 配置读取（模式仿 `artifact_runtime_config.go`）
- `server/ctyun_client.go` — 天翼云 OpenAPI client：创建 ECS、查询/列表镜像、停止实例等（有界超时、错误归一化）
- `server/worker_versions.go` — 云员工版本状态模型与存储（镜像版本 + 应用版本 + 上报时间）
- `server/worker_control.go` — 控制面 handler：创建云员工、查询版本、发起更新（复用 Part A 脚本）
- 测试：`server/ctyun_client_test.go`、`server/worker_versions_test.go`、`server/worker_control_test.go`

**创建（webapp）：**
- `webapp/src/widgets/worker-control-panel.jsx` — 云员工控制面板（新开/版本/更新按钮）
- `webapp/src/api.js` — 新增控制面 API 封装（worker 创建/版本/更新）

**修改：**
- `server/cmd/server.go` — 注册新 handler/路由
- `server/agents.go` — 列表响应增加云员工版本状态字段（可选）
- `webapp/src/views/tinode-web.jsx` — 挂载控制面板
- `.env.example` / 部署说明 — 新增 `CTYUN_*` 环境变量文档

---

### 任务 1：天翼云配置读取 + client 骨架

**文件：**
- 创建：`server/ctyun_config.go`、`server/ctyun_client.go`
- 测试：`server/ctyun_client_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
func TestCtyunConfigFromEnv(t *testing.T) {
	t.Setenv("CTYUN_AK", "ak-1")
	t.Setenv("CTYUN_SK", "sk-1")
	t.Setenv("CTYUN_REGION", "cn-huabei2")
	t.Setenv("CTYUN_PROJECT_ID", "0")
	cfg := CtyunConfigFromEnv()
	if cfg.AccessKey != "ak-1" || cfg.SecretKey != "sk-1" || cfg.RegionID != "cn-huabei2" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestCtyunClientRequiresCredentials(t *testing.T) {
	_, err := NewCtyunClient(CtyunConfig{})
	if err == nil {
		t.Fatal("expected error when credentials are missing")
	}
}
```

- [ ] **步骤 2：运行确认失败**

运行：`go test ./server/ -run 'Ctyun' -v`；预期 FAIL。

- [ ] **步骤 3：实现 `ctyun_config.go`**（仿 `artifact_runtime_config.go`：`firstNonEmpty` 读取 `CTYUN_AK`/`CTYUN_SK`/`CTYUN_REGION`/`CTYUN_PROJECT_ID`，未配置时显式错误；**绝不打印密钥**）。

- [ ] **步骤 4：实现 `ctyun_client.go` 骨架**：`CtyunClient` 结构（ak/sk/region/projectID + httpClient + 超时）；方法签名先给 `CreateWorkerInstance`、`ListWorkerImages`、`StopInstance`（返回结构见步骤 5/6）；`NewCtyunClient(cfg)` 校验必填。

- [ ] **步骤 5：实现签名与请求封装（先空实现 + 测试驱动）**

```go
type WorkerImage struct {
	ImageID   string `json:"imageID"`
	ImageName string `json:"imageName"`
	Status    string `json:"imageStatus"`
	Version   string // 从 labels version 解析
	Commit    string // 从 labels commit 解析
	UpdatedAt time.Time
}
func (c *CtyunClient) ListWorkerImages(ctx context.Context) ([]WorkerImage, error) { ... }
```

**天翼云 API 映射（命令名已用本地 ctyun-cli --help 核实）：**
- 查镜像：`ims ListImage`（结果含 `imageName`/`imageStatus`/`labels`/`sourceServerID`）与 `ims GetImageDetail`；`ListWorkerImages` 用 `product=catsco-worker` label 过滤，返回按 `version` 排序的最新列表
- 建实例：`ecs CreateEcsInstance`（参数：`--instanceName catsco-worker-*`、`--imageID`（最新初始化镜像）、`--flavorID`、`--vpcID`/`--subnetID`/`--secGroupList`、`--keyPairID`、`--onDemand true`、`--extIP`、`--bootDiskType`/`--bootDiskSize`，参照 `ops/ctyun-worker-image/New-CatsCoWorkerImage.ps1` 的构建机参数模式）
- 停/删实例：`ecs StopEcsInstance`、`ecs BatchDeleteEcsInstances`（回滚/释放时用，需确认实例归属 `catsco-worker-*` 命名，防误删——复用 Part A 的 `Assert-TemporaryBuilder` 思路）

用带 `httptest` 的 fake 天翼云 API 服务器测试响应解析与错误处理（参考 `server/relay_keys_test.go` 的 httptest 模式）。

- [ ] **步骤 6：测试通过**

运行：`go test ./server/ -run 'Ctyun' -v`；预期 PASS。

- [ ] **步骤 7：Commit**

```bash
git add server/ctyun_config.go server/ctyun_client.go server/ctyun_client_test.go
git commit -m "feat(worker): add Tianyi Cloud config and client skeleton"
```

---

### 任务 2：worker 应用/镜像版本上报与存储

**文件：**
- 创建：`server/worker_versions.go`
- 测试：`server/worker_versions_test.go`

- [ ] **步骤 1：编写失败的测试**（版本状态 upsert + 按 bot 查询 + 最新版本比较）

```go
func TestWorkerVersionUpsertAndQuery(t *testing.T) {
	// 存入 bot 110: {appVersion: "1.4.8", appCommit: "abc", imageVersion: "1.4.7"}
	// 断言：查询返回；compare 函数对 appVersion 排序给出"有更新"提示
}
```

- [ ] **步骤 2：运行确认失败**

- [ ] **步骤 3：实现版本模型**

```go
type WorkerVersionState struct {
	BotUID       int64
	AppVersion   string // worker-release.json version
	AppCommit    string // worker-release.json commit
	ImageVersion string // /etc/catsco-image.json version（未上报则空）
	ReportedAt   time.Time
}
```

存储：新增表 `worker_version_states`（bot_uid 主键）或扩展现有 bot 状态 JSON（参照 `bot_definition` 的 `bot_config.config` 节点模式）。给出迁移/初始化路径（postgres 与 mysql 同步）。

- [ ] **步骤 4：实现上报入口 handler**：`POST /api/bot/worker-version`（bot API key 鉴权，与现有 `/api/bot/definition` 同鉴权模式），body `{app_version, app_commit, image_version}`。

- [ ] **步骤 5：补测试**（上报 → 查询 → 覆盖更新；非法输入拒绝）。

- [ ] **步骤 6：Commit**

```bash
git add server/worker_versions.go server/worker_versions_test.go
git commit -m "feat(worker): persist worker application and image versions reported by heartbeat"
```

---

### 任务 3：XiaoBa 心跳上报版本（跨仓库协作，属于 Part A/Part B 交界）

**说明：** 本任务在 XiaoBa-CLI 实现"上报版本"，接口契约在 Part B 任务 2 定义。若 Part A 已实现 `worker-release.json`，本任务只加"上报动作"。

**文件（XiaoBa-CLI）：**
- 修改：`src/catscompany/client.ts` — 上报点已定位：
  - `CatsDeviceRegistration` 接口（约 23-33 行，含 `capabilities`/`model_status`）新增可选字段 `app_version`/`image_version`
  - `registerDevice(registration)`（约 801 行）与 `startHeartbeat()`（约 940 行）携带版本字段
- 新增：`src/catscompany/version-report.ts` — 读取 `/opt/catsco/current/worker-release.json` 与 `/etc/catsco-image.json` 的辅助函数（文件不存在返回 undefined）
- 测试：`tests/catscompany-version-report.test.ts`（mock 上报端点断言字段出现；无 manifest 时不发送）

- [ ] **步骤 1：实现 `version-report.ts` 读取辅助**

```ts
export function readWorkerAppVersion(): { version: string; commit: string } | undefined {
  // 读 /opt/catsco/current/worker-release.json；缺失/非法返回 undefined
}
export function readWorkerImageVersion(): string | undefined {
  // 读 /etc/catsco-image.json 的 version；缺失返回 undefined
}
```

- [ ] **步骤 2：在 `client.ts` 上报中携带版本**

在 `CatsDeviceRegistration` 增加 `app_version?: string`、`image_version?: string`；`registerDevice` 与 heartbeat payload 中附加（取 `version-report.ts` 返回值；本地桌面端/无 manifest 时省略）。用 mock 上报端点测试字段出现/缺失。

- [ ] **步骤 2：测试**（mock 上报端点断言字段出现；无 manifest 时不发送）。

- [ ] **步骤 3：Commit（XiaoBa-CLI）** + 在 Part B 服务端联调冒烟（本地起 server + 模拟上报）。

---

### 任务 4：控制面 handler（创建云员工 / 查版本 / 发起更新）

**文件：**
- 创建：`server/worker_control.go`
- 测试：`server/worker_control_test.go`

- [ ] **步骤 1：编写失败的测试**（handler 行为）

```go
// POST /api/workers/create  {image_id, bot_name, region...} -> 调用 CtyunClient.CreateWorkerInstance + 首次引导
// GET  /api/workers/versions?uid=<bot> -> 返回 worker 版本 + 最新可用版本 + hasUpdate
// POST /api/workers/update {uid, version, commit} -> 触发更新执行（Part A 脚本）
```

- [ ] **步骤 2：运行确认失败**

- [ ] **步骤 3：实现创建云员工**

`CreateWorkerInstance`：用最新初始化镜像（`ListWorkerImages` 取最新 active `product=catsco-worker`）创建按量 ECS；随后**首次引导**（README first-boot 契约）：注入一次性 bootstrap 凭据到 `/srv/catsco-agent`（经 cloud-init/启动脚本，凭据短生命周期）、enable `catsco-agent.service`、等待注册心跳成功。

- [ ] **步骤 4：实现版本查询**：合并 `worker_version_states`（心跳上报）+ `ListWorkerImages`（最新镜像/制品版本），计算 `has_update`；对已有 worker，更新源用"最新应用制品版本"（Part A）。

- [ ] **步骤 5：实现更新执行**：`POST /api/workers/update` —— 用户已确认；服务端调用 Part A 分发通道（子进程调用 `deploy-worker-artifact.mjs` 或通过已部署的通道），带 `--expected-*` 校验；返回逐台结果。**安全**：仅 owner 可触发；更新前 `draining`（可选，首版先做"任务完成后更新"的等待提示）；失败回滚由 Part A 脚本保证。

- [ ] **步骤 6：补测试 + 全量 Go 测试**

运行：`go test ./...`；预期全绿（webapp 部分见任务 5）。

- [ ] **步骤 7：Commit**

```bash
git add server/worker_control.go server/worker_control_test.go server/cmd/server.go
git commit -m "feat(worker): cloud worker control plane (create/version/update endpoints)"
```

---

### 任务 5：webapp 云员工控制面板

**文件：**
- 创建：`webapp/src/widgets/worker-control-panel.jsx`
- 修改：`webapp/src/api.js`、`webapp/src/views/tinode-web.jsx`

- [ ] **步骤 1：编写失败测试**（webapp 测试运行器已确认：**vitest**，`npm test` = `vitest run`；仿照 `bot-model-selector` 现有组件测试写面板渲染/交互测试）

```js
// 渲染面板：显示"新开云员工 / 检查版本 / 更新"按钮
// 点击检查版本 -> 调 api.getWorkerVersions -> 展示 has_update 与"确认更新"确认框
// 确认 -> 调 api.updateWorker -> 展示逐台结果
```

- [ ] **步骤 2：运行确认失败**

- [ ] **步骤 3：实现 `api.js` 封装**：`createWorker`、`getWorkerVersions`、`updateWorker`。

- [ ] **步骤 4：实现 `worker-control-panel.jsx`**：在机器人详情/云员工区域渲染（挂载点仿 `bot-model-selector` 在 `tinode-web.jsx`）；按钮状态机（idle/checking/confirming/updating/done）、错误提示、结果列表（逐台 success/失败+回滚）。

- [ ] **步骤 5：webapp 测试 + 构建**

运行：`npm test`（webapp 目录，确认现有运行器）、`npm run build`；预期全绿。

- [ ] **步骤 6：Commit**

```bash
git add webapp/src/widgets/worker-control-panel.jsx webapp/src/api.js webapp/src/views/tinode-web.jsx
git commit -m "feat(webapp): worker control panel for create/version/update"
```

---

### 任务 6：部署配置、文档与端到端联调

**文件：**
- 修改：`.env.example`、部署说明（新增 `CTYUN_AK`/`CTYUN_SK`/`CTYUN_REGION`/`CTYUN_PROJECT_ID`，注明来自本地 `~/.ctyun-cli.yaml`，**密钥不入库**）
- 创建：`docs/worker-control-plane.md`

- [ ] **步骤 1：写环境变量文档**（哪些变量、从哪来、最小权限建议）。
- [ ] **步骤 2：本地端到端冒烟**：本地起 server（postgres）+ 模拟 XiaoBa 上报版本 + 调 `GET /api/workers/versions` 验证 has_update 计算；`create`/`update` 用 `--dry-run` 模式或 fake ctyun 客户端验证流程（**不真实创建云资源**，与 Part A 验收一致）。
- [ ] **步骤 3：全量测试**：`go test ./...`（server）、webapp `npm test` + build；确认无回归。
- [ ] **步骤 4：Commit**

```bash
git add .env.example docs/worker-control-plane.md
git commit -m "docs(worker): document control plane env, safety boundary, and local smoke flow"
```

---

## 验收清单

- [ ] 服务端可读天翼云 AK/SK（env，不入库不打印）；client 有界超时、错误归一化
- [ ] worker 心跳上报应用/镜像版本并持久化；查询接口返回版本与 `has_update`
- [ ] webapp：新开云员工（拉最新初始化镜像建 ECS + 首次引导）、检查版本、确认更新、逐台结果
- [ ] 更新执行复用 Part A 脚本契约（校验/冒烟/回滚），服务端仅触发与展示，不持有 worker 凭据
- [ ] 全量测试 0 失败；本地端到端冒烟通过（不真实创建云资源）
- [ ] 环境变量文档就绪；无密钥入库

---

## 依赖与风险

- 依赖 Part A（XiaoBa-CLI）脚本契约；Part B 任务 5 可先行（UI + 查询），更新执行任务需 Part A 就绪。
- 天翼云 API 具体端点/返回结构以 `ctyun-cli` 帮助与天翼云 OpenAPI 文档为准（实现任务 1 时用 `ctyun-cli --help` / `ctyun-cli ims --help` 核实字段）。
- 新开云员工的首次引导涉及生产 ECS 创建，联调一律走 fake/dry-run；真实创建需用户在控制面确认后执行。
