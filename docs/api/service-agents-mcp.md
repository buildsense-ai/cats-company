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
| `input` | 应用入参，透传；**形状由应用作者的 `input_schema` 声明**（见「工具契约」） |
| `session_id` | 可选；续聊会话 id（不传 = 新会话） |
| `client_token` | 幂等键（作用域：同会话内；重发返回同一 run） |
| `bot_uid` | 可选；以 bot 身份调用时带 bot 的 uid（校验 bot 归属：owner 必须是调用者；若该 bot 定义里显式设了 `serviceAgents`，被调 agent 必须在其中 → 否则 `agent_not_enabled_for_bot`）。不影响 `tools/list`（工具清单跟账号） |

## 调用失败怎么说清楚（`worker_offline` / `not_picked_up`）

「我的环境主动接入」是 pull 模式：发布者的 worker 不来领，run 就没法执行。
对调用方 Agent 来说，这类失败**不是它的参数有问题**，所以返回必须写清三件事：

1. **是谁的问题**（发布者那一侧的执行环境）
2. **不要重试**（重试或换参数都不会变好，只会浪费额度）
3. **该去哪儿动手**（在跑这个应用的那台机器上启动小八并登录）

实际返回：

```text
worker_offline: 发布者这边没有任何 worker 在线。这是发布者那一侧的执行环境没就绪，
与调用参数无关——重试或改成别的参数都不会变好，请把这条原因原样转告用户：
在要跑这个应用的那台机器上（电脑或云服务器）启动小八并登录，它就会主动接单
（不需要公网 IP，也不用开任何端口）。
```

在线判断**按账号过滤**：claim 用的是 per-user 凭据（`cwk_` / worker 券），
只能领自己名下应用的 run，所以别人的 worker 对你这单毫无意义。

竞态兜底（预检通过后 worker 立刻掉线）：run 停在 `submitted` 超过 **2 分钟**
会被 reaper 置为 `failed` / `error_text=not_picked_up`，`catsco_run_status` 会返回：

```text
not_picked_up: 发布者那边一直没有 worker 认领这个任务（执行环境不在线）。…
```

`error_text` 里已知短码都会翻译成人话（`not_picked_up` / `no_command` /
`worker_offline`），未知短码原样透出（不吞信息）。

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
条目另外带两个字段（编辑已有应用用）：

| 字段 | 给谁 | 说明 |
|---|---|---|
| `description` | 所有人 | 调用方 Agent 看到的工具描述（作者原文）。空 = 作者没写，别人只能靠名字猜 |
| `definition` | **仅 owner 本人** | 当前版本 definition 原文（含 `runner` / `pricing`） |

`definition` 只回给本人，原因有两个：里面可能是别人电脑上的执行命令（拿不到也用不上）；
而且**发布是全量覆盖**，编辑时必须带着原 definition 合并 —— 只改描述却丢掉 `runner`，
应用下次被调用就会 `no_command` 起不来。

## 外部执行后端（`runner.type=http` / `mcp`）

设计 §4.4 / P3-a。用户在 agent definition 里声明 runner，run 由**用户自己的服务**执行，平台只负责转发、签名与结果回填。

> **面板已撤掉这两个选项**（2026-10-26）。它们要求对端有**公网可达地址**，个人用户基本没有；
> 而且 `mcp` 模式平台反向连对端时不注入任何身份头，对端若有鉴权根本连不上 ——
> 摆在面板上等于把最需要暴露、最容易配错的安全选项放在最容易点的地方。
>
> 现在面板只有一种接入方式：**你的环境主动连平台领任务**（`runner.type=worker`，pull）。
> 电脑、云服务器都行，不需要公网 IP、不开任何入站端口。
>
> `http` / `mcp` 仍保留在 API 层（内部应用、脚本直调、以及已存在的旧应用）。
> 旧应用在面板里编辑时，`runner` 会**原样保留**，只改名称/说明/价格。

`http` 的转发契约如下：

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

## 计费：谁调用谁付费、按量给

设计 §4.5。agent 在 definition 里声明单价；未声明 = 免费（金额记 0，但调用关系与用量照记，便于后续定价回溯重算）。

```jsonc
"definition": {
  "pricing": { "unit": "token",  "per_1k_tokens": 0.02, "currency": "CNY" }
  // 也支持 {"unit":"run","per_run":0.05} / {"unit":"second","per_second":0.001}
}
```

### 账本表 `service_run_ledger`

每个终态 run 一行（`run_id` 主键 → 重扫幂等）：

| 列 | 含义 |
|---|---|
| `payer_uid` | **调用者**（谁调用谁给钱） |
| `payee_uid` | agent owner（`owner_kind=internal` → 0 = 平台自营） |
| `gross_micros` / `fee_micros` / `net_micros` | 应付 / 平台抽成 / owner 应得，单位**微元**（1e-6 货币单位，整数避免浮点误差） |
| `pricing_json` | 计价快照（与当时 definition 绑定，后续改价不影响历史账） |
| `prompt_tokens` / `completion_tokens` / `duration_ms` | 实际用量（来自 run 的 `usage_json` 与 `started_at→finished_at`） |
| `runner_type` | `codex` / `http`（外部执行后端） |

### 怎么记

**补账扫描**（`ServiceRunLedgerWriter`，30s 一轮），不埋点：扫「已终态 + 48h 内 + 无账本行」的 run，按会话钉定版本的 `pricing` 算钱后落行。原因：run 有 4 条终态路径（worker complete / awaiting_input / fail / 外部 runner），逐条埋点容易漏。

### 平台抽成

`CATS_SERVICE_PLATFORM_FEE_BPS`（万分比，默认 **0** = 全额归 agent owner）。
`fee_micros = round(gross * bps / 10000)`，`net_micros = gross - fee`。

### 边界

- **只记账不动钱**：本表是结算的输入，真正扣费/入账读它另做
- 未定价 agent：`gross=0`，但 `payer/payee/用量` 仍记录 → 后续定价可回溯重算
- 扫描窗口 48h：更早的历史 run 不会自动补账（需要时用同一条 SQL 手工回填）

### 结算状态 `billing_state`（按会话请求实时结算）

| 状态 | 含义 | 预检是否计入 |
|---|---|---|
| `free` | 未定价（默认免费分享） | 否（金额 0） |
| `settled_platform` | 平台自带执行（codex）：调用方额度已由 **relay 在模型调用时实时扣除** | relay 已算，不重复计 |
| `pending_credit` | 外部执行后端（`runner.type=http`）：对端自报用量，平台侧暂无扣费通道 | **是** —— 金额并入调用方已用额度 |

`pending_credit` 的设计意图：外部 agent 的模型调用不经过我们的 relay，平台扣不到；
但这笔账不能白算 —— 预检（`ServiceBalanceChecker`）会把这部分「待扣」加到
relay 的 `current_usage` 上再比 `max_limit`，所以**碰额度上限一样会被拒单**，
不会出现"免费无限调外部 agent"。

## 工具契约：description / input_schema（作者自己写）

调用方是**另一个 Agent**，它看不到包内容、也没有主 Agent 的上下文 —— 它能看见的只有
`tools/list` 里的 `description` 和 `inputSchema`。所以这两段文字就是分享出去的**全部契约**，
由作者（往往就是 Agent 自己在写自己的 `agent.json`）填写，平台只做透传与上限校验。

```jsonc
// agent.json
{
  "schema": "catsco.service-agent.v1",
  "slug": "zh-polish",
  "name": "中文润色",

  // 做什么 / 什么时候用 / 什么时候不要用。公开应用必填（≤1200 字）。
  "description": "把中文技术文档润色成对外可发布的英文稿，保留代码块与专有名词。
                  用户要发英文稿、英文邮件时使用；纯翻译或写作建议不要用。",

  // 描述工具的 input 字段（不是整个参数对象）。缺省 = 通用写法（不声明 required）。
  "input_schema": {
    "type": "object",
    "properties": {
      "text":     {"type": "string", "description": "要处理的正文"},
      "audience": {"type": "string", "enum": ["老板", "全员"], "description": "给谁看"}
    },
    "required": ["text"]
  }
}
```

平台派生后在 `tools/list` 里是这样：

```jsonc
{
  "name": "catsco_zh-polish",
  "description": "把中文技术文档润色成对外可发布的英文稿…",   // 作者原文，逐字透出
  "inputSchema": {
    "type": "object",
    "properties": {
      "input":       { /* ← 作者写的 input_schema 原样嵌在这里 */ },
      "session_id":  {"type": "string"},
      "client_token":{"type": "string"},
      "bot_uid":     {"type": "integer"}
    },
    "required": ["input"]
  }
}
```

规则：

- **description 逐字透出**，平台不加「CatsCo 定制应用 / slug= / 版本 v」之类实现细节 ——
  对模型是噪音，还容易让它以为要在这里操作版本。
- **没写 description**：工具描述退回「作者未说明这个应用能做什么…建议只在用户明确点名时调用」，
  公开上架则直接 400 `description_required`（写了也没人会用，不如不发）。
- **input_schema 校验**：必须是 JSON 对象、顶层 `type` 若出现只能为 `"object"`、≤8 KiB；
  不合法 → 发布失败（静默丢弃等于让工具变哑巴，宁可当场报错）。空/缺失走通用写法。
  **两条通道同一套规则**：包通道读 `agent.json`，用户上架通道读 `definition.input_schema`
  （400 `invalid_input_schema`）。"
- 平台字段（`session_id` / `client_token` / `bot_uid`）始终可用，与作者的 `input` 平级。

> 自发布闭环：Agent 只需在自己的 `agent.json` 里写好这两段，然后调
> `POST /internal/service-agents/{slug}/publish`（实例令牌）即可，平台自动派生并持久化 ——
> 不需要另做「发布工具」。

