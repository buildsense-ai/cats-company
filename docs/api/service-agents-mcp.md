# 定制应用调用面（MCP）

> 阶段 1（计划① Task 7-9）：调用方经 MCP（Streamable HTTP）调用 CatsCo 定制应用。
> 设计文档（内部）：agent-service-platform；本文件 = 对外接入说明。

## 端点与鉴权

- **URL**：`POST /api/mcp`（Streamable HTTP，与平台同源 app.catsco.cc）
- **鉴权**：平台登录凭证 `Authorization: Bearer <JWT>`（调用方账号；bot 身份为阶段 2）
- **协议**：MCP Streamable HTTP（`initialize` / `tools/list` / `tools/call`）

## 工具

| 工具 | 说明 |
| --- | --- |
| `catsco_<slug>` | 每个上架应用对应一个工具（如 `catsco_hello`） |
| `catsco_run_status` | 查询 run 状态/结果（`{run_id}`） |

`catsco_<slug>` 参数：

| 参数 | 说明 |
| --- | --- |
| `input` | 任意 JSON，透传给应用 |
| `session_id` | 可选；续聊会话 id（不传 = 新会话） |
| `client_token` | 幂等键（作用域：同会话内；重发返回同一 run） |
| `bot_uid` | 预留（当前须为 0；bot 身份为阶段 2） |

## 调用语义

1. **新会话**：不传 `session_id` → 新建（会话钉创建时的应用版本；新版经新会话生效）。
2. **续聊**：传上次返回的 `session_id`；同会话同一时刻仅一个进行中的 run，并发调用返回
   工具错误 `session_busy`。
3. **幂等**：同会话同 `client_token` 重发 → 同一 run。
4. **同步交付**：应用阈值内完成 → 直接返回结果 envelope（见下）。
5. **超阈值**：返回运行句柄 `catsco.service-run-async.v1`
   `{run_id, session_id, status:"running"}` → 用 `catsco_run_status` 轮询至终态
   （阶段 2 增加"回流注入"推送，主动唤醒调用方）。
6. **计费**：费用记调用方账号额度（relay 侧记账）。额度不足 → `quota_exhausted`；
   计费预检暂不可用 → `service_unavailable`（均以工具错误返回）。

## 结果 envelope（structuredContent）

```json
{
  "contract_version": "catsco.service-run-result.v1",
  "run_id": "run_...",
  "session_id": "sess_...",
  "status": "completed",
  "output": { "answer": "..." },
  "files": [],
  "usage": { "prompt_tokens": 3, "completion_tokens": 5 }
}
```

## 编排内部 API（运行时实例；非对外）

前缀 `/internal/`，`Authorization: Bearer <CATS_SERVICE_INSTANCE_TOKEN>`：

| 端点 | 请求 → 响应 |
| --- | --- |
| `POST /internal/service-runs/claim` | `{limit, instance_id}` → `{runs:[{run_id, session_id, definition_rev, threshold_seconds, input, service_token, checkpoint?}]}` |
| `POST /internal/service-runs/{run_id}/checkpoint` | `{revision, snapshot}` → `{saved}` |
| `POST /internal/service-runs/{run_id}/heartbeat` | `{instance_id}` → `{renewed}` |
| `POST /internal/service-runs/{run_id}/events` | `{type, payload}` → `{seq}` |
| `POST /internal/service-runs/{run_id}/complete` | `{result_id, result, usage?}` → `{completed}` / `{idempotent}` |
| `POST /internal/service-instances/{instance_id}/heartbeat` | `{active_runs, metrics?}` → `{ok}` |

- 租约 30s；过期由 reaper（15s 周期）置 `orphaned`，其他实例从最新 checkpoint 接管
  （`lease_epoch`/`attempts` 递增；fencing 设计 §4.8-1）。
- 同会话保串行：活跃 run 存在时新 run 拒单（`ErrServiceSessionBusy`）。
- 结果先写 `result` 事件、再 `complete`（调用方同步等待/查询都读事件）。

## 服务 token（`st1.`）

claim 现场签发：

```text
st1.<b64url(payload_json)>.<b64url(hmac_sha256(secret, "st1." + b64url_payload))>
```

claims：`caller_uid`（计费归属）、`agent`（slug）、`models`（应用定义允许的模型集）、
`run_id`、`iat`/`exp`（+4h）、`purpose=service`。实例调 relay 模型面时携带，
relay 据此将用量归因到**调用方**额度（见 relay `service_tokens.py`，金样 `catsct-interop`）。

## 配置（平台 env）

| env | 说明 |
| --- | --- |
| `CATS_SERVICE_INSTANCE_TOKEN` | 实例访问编排内部 API 的 Bearer token（未配置 → 全部 401） |
| `CATS_SERVICE_TOKEN_SECRET` | 服务 token HMAC 密钥（与 relay 同源） |
| `CATS_RELAY_ADMIN_URL` / `CATS_RELAY_ADMIN_TOKEN` | 计费预检（relay admin；未配置 → 跳过预检） |

## 用户自助上架（`POST /api/service-agents/publish`）

设计 §4.3 P2。内部发布通道（`/internal/service-agents/{slug}/publish`，实例令牌）之外，
真人账号可通过本条上架自己的 agent。

- 鉴权：JWT + 内测白名单（与 `GET /api/service-agents/catalog` 同一套
  `CATSCO_SERVICE_CATALOG_UID_WHITELIST`）。未在白名单 → 403。
- 归属：首次发布即写入 `owner_uid`；之后只有本人能继续发版；
  平台内部 slug（`owner_kind=internal`）不可抢占（409 `slug_reserved`），
  他人已占（409 `slug_taken`）。
- 版本：`package_hash = sha256(bundle)` 内容寻址，与内部通道共用版本/会话钉快照；
  同内容重发幂等返回既有版本，`force=true` 可强制建新版本（回滚通道）。

请求：

```jsonc
{
  "slug": "my-helper",            // ^[a-z0-9][a-z0-9-]{1,63}$
  "name": "我的助手",              // 可选，默认=slug，≤64 字
  "visibility": "public",          // public（默认）| private（仅自己可见可调）
  "definition": { "async_threshold_seconds": 30 },   // 可选，平台派生字段
  "bundle_base64": "…",            // 必填，agent 包原文（≤20 MiB，可用 CATSCO_SERVICE_AGENT_MAX_BUNDLE_BYTES 调整）
  "force": false
}
```

响应：`{ "slug", "version", "package_hash", "visibility", "owner_uid" }`。

可见性语义：`public` 进所有人目录并可被调用；`private` 只进 owner 自己的
`tools/list` 与目录，别人即使知道 slug 也拿不到工具、硬调返回 `agent_not_found`。

目录接口 `GET /api/service-agents/catalog` 的条目新增 `mine` / `owner_kind` /
`visibility` 三个字段，便于前端区分「平台应用」与「我上架的」。

## 外部执行后端（`runner.type=http`）

设计 §4.4 / P3-a。用户在 agent definition 里声明 runner，run 由**用户自己的服务**执行，平台只负责转发、签名与结果回填：

```jsonc
{
  "slug": "my-helper",
  "definition": {
    "async_threshold_seconds": 30,
    "runner": { "type": "http", "url": "https://user.example/catsco/run", "timeout_seconds": 60 }
  }
  // 外部 runner 不需要代码包：bundle_base64 可省略（平台生成占位包占住内容寻址）
}
```

### 转发契约 `catsco.service-run-request.v1`

```http
POST <runner.url>
Content-Type: application/json
X-Catsco-Run-Id: run_xxx
X-Catsco-Signature: sha256=<hex(hmac_sha256(SECRET, body))>

{"contract_version":"catsco.service-run-request.v1","run_id":"run_xxx","session_id":"sess_xxx",
 "agent":"my-helper","caller_uid":441,"bot_uid":0,"input":{…},"deadline_ms":60000}
```

`SECRET` = 平台 env `CATS_SERVICE_EXTERNAL_RUNNER_SECRET`（**未配置时不转发**，避免无签名裸奔）。
对端必须校验签名；`run_id` 可作为幂等键。

### 对端应答（同一次 HTTP 响应）

| `status` | 平台行为 |
|---|---|
| `completed` | 写 `result` 事件 + run → `completed`（附 `output` / `files` / `usage`） |
| `awaiting_input` | run → `awaiting_input`（`output.question` 进入回执） |
| `failed` | run → `failed`（`error` 文本落库） |
| `running` | 保持 running，调用方用 `catsco_run_status` 轮询 |

失败一律按**终态**处理：外部执行没有 worker 会接管重跑，标记可重试只会让 run 卡在 `orphaned`。对端可在自己内部重试，或让调用方重新发起 run。

### 准入限制

- 默认只接受 **https**；`CATS_SERVICE_EXTERNAL_ALLOW_HTTP=1` 放开（本地联调）
- 默认拒绝 loopback / 私网 / 链路本地 / 组播地址（DNS 解析后逐 IP 校验）；`CATS_SERVICE_EXTERNAL_ALLOW_PRIVATE=1` 放开
- 超时：`timeout_seconds`（默认 60s，封顶 10 分钟）
- 已知残余风险：DNS rebinding（MVP 接受，后续可加固定解析/IP 白名单）

### 已知限制

- 进程重启会丢失「已创建但未转发」的 run（需等 reaper 收尾）；后续可加启动扫描补齐
- 只实现 `http`；`mcp`（对端 MCP server）留待 P3-b
