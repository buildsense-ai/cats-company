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

> 面板已不再提供这种方式（用户不可能知道自己该填什么命令）；这里保留是因为
> API 层与旧应用还在用。把 bot 分享成工具的错误说明见「把 bot 分享成工具」一节。
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
  "visibility": "public",          // public（默认）| friends | private
  "definition": { "async_threshold_seconds": 30 },   // 可选，平台派生字段
  "bundle_base64": "…",            // 必填，agent 包原文（≤20 MiB，可用 CATSCO_SERVICE_AGENT_MAX_BUNDLE_BYTES 调整）
  "force": false
}
```

响应：`{ "slug", "version", "package_hash", "visibility", "owner_uid" }`。

### 三级可见性：公开 / 好友 / 自用

| `visibility` | 谁能在目录里找到、加进清单、调用 | 典型场景 |
|---|---|---|
| `public`（默认） | 所有人 | 做成团队/社区都能用的工具 |
| `friends` | **发布者的好友**，或**执行 bot 自己的好友**（任一成立即可） | 只给认识的人用，不想放进公共目录 |
| `private` | 只有发布者本人 | 自用、还没调教好 |

为什么 bot 的好友也算：分享出去的是「这个 bot 本人」，它交好的人本来就在它的
联系人里。只认发布者会让一支 bot 自己的社交圈白建 —— 别人是冲着这个 bot 来的，
不是冲着坐在电脑前那个人。（`private` 不受此影响：自用就是自用。）

三条规则：

1. **先判「是不是我的」再判级别** —— 自己发布的三个级别都看得到（好友级不需要
   「自己是自己的好友」这种荒谬条件）。
2. **好友按访问者求值**：`friends` 的判据是「看的人与**发布者**或**执行 bot**是不是好友」。
   所以目录、`tools/list`、调用准入、订阅准入四处共用同一个判定
   （`store.ServiceAgentVisibleTo`），不会各写一套。发布者已是好友时短路，
   不会白查一次 bot；同一对象一次请求内只查一次。
3. **看不见的应用一律 `agent_not_found`**（订阅接口返回 404）：能区分「不存在」和
   「存在但我看不到」，等于让人用这个接口探测别人的应用。

别的账号看不到、也调不动 `private` / 非好友的 `friends` 应用：
即使猜到 slug，硬调也拿 `agent_not_found`。

好友关系查不到时按**不是好友**处理（fail-closed）：这是访问控制，不是个人聚焦设置，
存储抖动不该变成「把别人的应用暴露出去」。日志会留一行，便于区分「本来就不是好友」
与「查挂了」。

**改级别立即生效（调用面）**：调用准入每次都用**当前**的归属、可见性与 definition
判定，而不是工具闭包在会话建立时捕获的那份快照。所以把应用从「好友」改成「自用」
之后，好友那条已经在线的 MCP 会话**下一次调用就会被拒**，不需要等对方重连 —— 否则
「我收回亡」只是看起来收回了。definition 也取当前值：改派给另一个 bot 后，按**新**
bot 的好友判定（旧 bot 的好友不再自动有权限）。`tools/list` 是另一回事：在线会话要靠
`list_changed` 或客户端主动重查才更新，这个差异只影响「看不看得见」，不影响「能不能调」。

> 已知限制：好友关系变化**不会**给已在线的 MCP 会话推 `list_changed`。
> 对方重开 MCP 会话（或重新 `tools/list`）后就看到了。

「分享给别人」（`public` / `friends`）必须写 `definition.description` —— 调用方是另一个
Agent，它看不到包内容、也没有主 Agent 的上下文，这段文字是它判断「要不要用」的唯一
依据。没写就 400 `description_required`。「自用」不强制。

目录接口 `GET /api/service-agents/catalog` 的条目新增 `mine` / `owner_kind` /
`visibility` 三个字段，便于前端区分「平台应用」与「我上架的」。
另外三个字段是给**看的人**判断「该不该用」的：

| 字段 | 说明 |
|---|---|
| `owner_name` | 分享者显示名。谁是作者是信任的全部线索；没有它，目录里只剩一个孤零零的名字 |
| `input_schema` | 作者声明的入参 schema 原文。光看一段散文猜不出字段名，调用方照这个填 |
| `runner_kind` | 执行方类型（`bot` / `worker` / `http` / `mcp`；空=平台内置）。决定别人的期待：要不要等、离线会不会失败、能不能动本机 |

条目另外带两个字段（编辑已有应用用）：

| 字段 | 给谁 | 说明 |
|---|---|---|
| `description` | 所有人 | 调用方 Agent 看到的工具描述（作者原文）。空 = 作者没写，别人只能靠名字猜 |
| `definition` | **仅 owner 本人** | 当前版本 definition 原文（含 `runner` / `pricing`） |

`definition` 只回给本人，原因有两个：里面可能是别人电脑上的执行命令（拿不到也用不上）；
而且**发布是全量覆盖**，编辑时必须带着原 definition 合并 —— 只改描述却丢掉 `runner`，
应用下次被调用就会 `no_command` 起不来。

## 自述（`self_intro`）：应用可以建议自己的描述，但不能替人决定

写工具描述的是谁？最理想是**应用自己** —— 只有它知道「用户在什么情况下真该找我」。
但直接给 agent 一个「发布」工具是危险的：`definition.runner.command` 是任意命令，
`visibility: public` 是对全平台放出，slug 写错还能覆盖别的应用。

所以是**建议制**：

```jsonc
// 应用（agent）在 run 结果里这样写，平台就会把这段记下来等人确认
{
  "output": {
    "text": "本次任务结果…",
    "self_intro": {
      "description": "读我（发布者）这台电脑的当前时间。用户问「几点」时用；时区换算不要用。",
      "input_schema": { "type": "object", "properties": {} }
    }
  }
}
```

平台行为：

| 时机 | 行为 |
|---|---|
| run 收尾（`complete`） | 从结果里取 `output.self_intro`（或顶层 `self_intro`），落 `service_agents.self_intro_*` |
| 目录接口 | `self_intro` **只回给 owner 本人**，且明显标为「建议」（不是已生效的描述） |
| 面板 | 行内显示「这个应用建议这样介绍自己」+ **用它 / 忽略** |
| 「用它」 | 只把文字**填进编辑表单**，人确认后照常 `POST /api/service-agents/publish` |
| 「忽略」 | `DELETE /api/service-agents/{slug}/self-intro`（幂等） |
| 任何一版发布 | 旧建议自动清掉（它就是针对上一版说的，已经过时了） |

边界（这些都是硬约束，不是约定）：

- **只认 `description` / `input_schema`**。`runner` / `pricing` / `visibility` / `slug`
  即使写在 `self_intro` 里也一个字都进不来 —— 结构体根本不认。
- **建议不生效**：它不进 `tools/list`，别人看不到，只有 owner 在面板上能看到。
- **采纳必须由人完成**：不开「采纳」写入接口，也就没有第二条改动线上文案的路。
- **失败不影响任务**：自述落库失败只记日志，run 该 completed 还是 completed。
- `input_schema` 写错只丢 schema、保留描述（描述才是主体；作者侧写错要报错，
  因为那是人主动提交的，这里是模型顺手给的）。

### 怎么让应用学会自述

平台**不会**替应用生成描述 —— 外面没有「猜」的余地，只有应用自己知道自己什么时候该被找。
所以在应用自己的提示词 / `agent.json` 里加一句就行（不用改平台任何配置）：

```text
每次跑完，在结果里额外带上 self_intro：用一两句话说明「谁、在什么情况下该找我」，
再给一个 input_schema。这只是建议，我自己会在面板上确认。
```

对应到输出就是上面那段 JSON（`output.self_intro`）。之后：

1. 应用下一次跑完 → 面板那一行出现「这个应用建议这样介绍自己」；
2. 点 **用它** → 内容填进编辑表单（**不会**直接发布）；
3. 人看过、改过 → 照常发布。发布成功后建议自动清掉。

没写描述的应用，面板会直接把这句提示显示出来（「可以让它自己写」），
免得用户以为平台会自动补一段文案。

## 外部执行后端（`runner.type=http` / `mcp`）

设计 §4.4 / P3-a。用户在 agent definition 里声明 runner，run 由**用户自己的服务**执行，平台只负责转发、签名与结果回填。

> **面板已撤掉这两个选项**（2026-10-26）。它们要求对端有**公网可达地址**，个人用户基本没有；
> 而且 `mcp` 模式平台反向连对端时不注入任何身份头，对端若有鉴权根本连不上 ——
> 摆在面板上等于把最需要暴露、最容易配错的安全选项放在最容易点的地方。
>
> 现在面板只有一种接入方式：**把 bot 分享出去**（`runner.type=bot`，见下一节）。
> 调教好的 bot 就是执行者 —— 它有自己的判断力，能在自己那台机器上干活。
>
> `http` / `mcp` / `worker` 仍保留在 API 层（内部应用、脚本直调、以及已存在的旧应用）。
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

## 把 bot 分享成工具（`runner.type=bot`）

别的东西都能靠 worker / http 接进来，但「另一个 bot」（虚拟员工）本身就是个 Agent ——
它有自己的人格、技能、上下文和判断力。这一节讲怎么把它**当成工具**给别人用。

```jsonc
{
  "slug": "jack-ops",
  "name": "杰克运维",
  "visibility": "public",
  "definition": {
    // 没写就自动取 bot_config.description（见下）
    "description": "把服务器/发布相关的排障活派给杰克。",
    "input_schema": { "type": "object", "properties": { "text": { "type": "string" } }, "required": ["text"] },
    "runner": { "type": "bot", "bot_uid": 982 }
  }
}
```

### 天生异步：调用方立刻拿到句柄

**这一点跟其它 runner 不一样，必须知道。**

bot 的一个回合是**分钟级**的：上下文压缩、多轮模型推理、它自己再去调工具。
实测一次 90s 压缩 + 20s 推理。而 `tools/call` 的同步等待上限只有 45s —— 硬等只会让
主 Agent 卡在那儿，用户看到的就是「没反应」。

所以 `runner.type=bot` 的调用**一律立即返回**：

```jsonc
{ "contract_version": "catsco.service-run-async.v1", "status": "running",
  "run_id": "run_…", "session_id": "sess_…" }
```

调用方要结果就 `catsco_run_status(run_id)`，或者等生命周期回执。
**主 Agent 零阻塞**，可以继续跟用户聊别的。

### 平台做了什么

1. 把 `input.text` 作为一条私聊消息发给目标 bot（落在两人的 p2p topic 里），
   metadata 带上 `catsco_bot_session_id`；
2. 后台轮询消息表，等 bot 回一条**属于本次调用**的 `text`；
3. 拿到就写 run 结果，`output.text` = 它的回复（多段会被拼起来）。

收尾规则：收到第一段后**再等 2 秒没有新消息**才收尾 —— bot 会把长回复切段，
收太早会把答案截断。

### 上架前提：`bot_uid` 必须是自己的 bot

发布时服务端校验「这个 bot 的 owner 就是发布者」，不是则 `400 bot_not_owned`。
没这条的话，任何人都能把别人的 bot 包成自己的工具上架：调用方以为在用那个 bot 的服务，
实际是发布者在替它接单、蹭它的算力，还会顺带继承那个 bot 的好友可见性。

执行时还会再校验一次（`bot_not_owned: …`）：definition 会热更新，内部发布通道也不经过
用户发布接口，只在发布时拦一次等于留了后门。

### 并发：`runner.max_inflight`

bot 侧的会话是串行的，并发打过去不会更快 —— 后几单会排队到超时，而超时的文案是
「bot 没回」，把「在忙」说成了「不理你」。

```jsonc
"runner": { "type": "bot", "bot_uid": 982, "max_inflight": 2 }
```

设了上限后，超出的单**立刻**以 `bot_busy: 这个应用同时已经有 N 单在跑（上限 M）…` 收尾，
并标 `retryable=true`（调用方应当稍后重试，不要同一任务反复重发）。
不写 = 不限（保持历史行为）；平台级默认可用 `CATSCO_BOT_RUNNER_MAX_INFLIGHT` 设，上限 16。
### 一次调用 = 一个 bot 会话

**这是 `runner.type=bot` 与其它 runner 最大的行为差异之一，必须知道。**

worker 每次 run 是一个干净进程，什么也不记得；bot 相反，它有长期记忆。而平台和 bot
之间只有一条私聊 topic（`p2p_调用方_bot`），如果 bot 按 topic 分会话，**所有调用会挤进
同一条会话**：上一个任务的判断会漏到下一个任务（「按 Asia/Shanghai 算」这种上游决定
会污染下一个问题的答案），上下文也会无限膨胀。

所以平台把 **run 的 `session_id` 一起下发**（`metadata.catsco_bot_session_id`），
bot 侧按它算自己的会话身份：

| 调用方式 | bot 那边 |
|---|---|
| 不传 `session_id` | 新会话 —— 互不污染 |
| 传 `session_id`（同会话追问） | 回到那个会话，接着上次说 |
| 同一 `session_id` 已有进行中的 run | `session_busy`（平台侧拦截） |

「一次调用一个会话，带会话 id 可以回原会话」—— 语义与 worker 一致。

### 回复怎么认领（为什么以前会串话）

只按「水位线之后 bot 说的都算」认领是不成立的：两条并发调用的回复落在**同一条**
消息流里，谁都可能捡到对方的。平台的认领规则现在是：

1. **`reply_to` 指回本次那条 prompt** → 明确是我的（首选路径）；
2. 回复 metadata 里 `catsco_bot_session_id` 与本次一致 → 明确表态属于哪个会话；
3. 两者都没有（旧客户端）→ **只有这个 topic 上除我之外没有别的单在等**时才认领。

第 3 条是兼容老版本用的兜底：真并发时它会让路，`bot_reply_ambiguous` 之后如实失败
——已经看见 bot 回了，就不是「没回」而是「分不清」，这两件事对调用方的处置完全不同
（前者该升级客户端，后者该重试）。

**明确属于别人的回复不算「分不清」**，只是噪音，直接跳过。这条边界看着琐碎，实际很要紧：
上一单超时（`bot_timeout`）之后 bot 并不会停手，它跑完照旧把回答发回来，那条消息带着
**上一单**的 `reply_to`；此时正在等的下一单如果把它当成「有回复但认领不了」，就会在
歧义窗口之后报 `bot_reply_ambiguous` 把一次正常调用打死。分得清就该按分得清处理。

> **客户端要求**：要拿到精确归属（也就是支持并发调用），bot 回复必须回填
> `reply_to = 收到的 prompt 的 message id`。XiaoBa-CLI 已支持。
> 不回填的旧客户端仍能工作，但同一账号**并发**调同一个 bot 时会得到 `bot_reply_ambiguous`。

### 失败怎么算

| 情况 | run 结果 |
|---|---|
| bot 不在线（没有活跃 body 租约） | **立刻** `bot_offline`，不干等 |
| 消息落库了但推不到它的连接 | `bot_deliver_failed` |
| 等了 5 分钟（默认）没回复 | `bot_timeout` |
| 已经有部分回复但超时 | 按「说到一半」交付，比丢掉有用 |
| 看见回复但判断不出是不是给我 | `bot_reply_ambiguous`（约 20s 后，不干等满 5 分钟） |
| 看见的是**别人那单**的回复 | 忽略（不算歧义，继续等自己的） |
| bot 不存在 | `bot_not_found` |

**如实失败，不编造**：bot 没回就是没回，调用方拿到的是一句能看懂的原因，
而不是一个假装完成了的空答案。

### bot 自己的「标签」

`bot_config.description` 就是 bot 在平台的自我介绍。发布 `runner.type=bot` 的应用时
**没写 `definition.description` 就自动取它** —— 标签本来就该是 bot 自己的话，
不该让发布者再手抄一遍。

这也是「分享」的关键：别人的 Agent 只能看到这段文字来决定要不要把活派过来。
没描述 = 分享了也没人知道该找它。

### 执行参数：云端可调，改完下一次调用生效

`runner.type=bot` 的四个时间参数**不用改代码、不用发客户端、不用重启平台**。
优先级从低到高：

| 层 | 在哪改 | 生效时机 | 适合 |
|---|---|---|---|
| 内置默认 | — | — | 不出声的基线 |
| 平台 env | `CATSCO_BOT_RUNNER_*` | 重启进程 | 全站基调 |
| `definition.runner` | 重新发布该应用 | **下一次调用** | 单个应用（慢 bot / 特殊场景） |

```jsonc
"runner": {
  "type": "bot",
  "bot_uid": 982,
  "timeout_seconds": 900,          // 整单等待预算，默认 300
  "settle_seconds": 3,             // 收到一段后静默多久算说完，默认 2
  "ambiguity_grace_seconds": 30,   // 看得见回复却判断不出归属时等多久，默认 20
  "poll_interval_ms": 500          // 轮询消息表间隔，默认 500
}
```

**为什么改 definition 就能实时生效**：派活时会按 `agent.id` **重读一次最新
definition**，而不是用 MCP 会话建立时快照进工具闭包的那份。所以已连接的调用方
不需要重开会话。

**这次重读放在后台**（`execute` 里），不在 `tools/call` 的返回路径上 —— 那个
路径对主 Agent 的承诺是「立刻拿到运行句柄」，任何 DB 读都会破坏它。所以最多是
「bot 晚 2 秒开始干活」，不会是「调用方多等 2 秒」。读失败不阻断派活，退回会话
快照并在日志里留一行。

热更新范围：

| 字段 | 跟随最新配置？ |
|---|---|
| `timeout_seconds` / `settle_seconds` / `ambiguity_grace_seconds` / `poll_interval_ms` | ✅ 总是 |
| `bot_uid`（改派给另一个 bot） | ✅ 仅当最新配置**仍然是 bot runner** |
| `runner.type`（bot → worker/http） | ❌ 保持调用时定下的类型 |

`runner.type` 不跟随，是因为它在 `invoke` 时就决定了本次返回句柄还是同步等；
跑到一半改类型会让调用方拿到一个语义已经变了的 run。要换类型就重新发布、让新
会话用新配置。

外部配置会**夹到允许区间**（超时 10s~30min、静默窗口 0.5s~30s、轮询 50ms~5s），
且静默/歧义窗口不会长过整单预算（否则永远不会触发，等于没配）。`settle` 的下限
是 0.5s：设成 0 会在第一段就收尾，而 bot 按 4000 字切段，答案会被默默截断。
`NaN`/`Inf`/溢出值一并挡掉。

进程内代码显式设置的值不夹区间 —— 那是写死的意图，不是手滑。

平台 env 写了但用不了（拼错、非数字、越界）会**打一行日志**再忽略：静默忽略是
最坏的情形，运维以为调了参数、实际跑的还是默认值。

### 已知限制

- **内测白名单仍然管着目录与发布**（`CATSCO_SERVICE_CATALOG_UID_WHITELIST`）：
  开关在环境变量里，没开之前只有白名单账号能用这套面；开的时候清空即可，
  使用侧（可见性判定）不需要再改
- **调用资格只看可见性**：runner 不再单独限制 bot 归属（曾经那套「bot 的 owner
  必须在白名单里」与可见性打架：应用标了 public，别人调用仍然报 `bot_not_shared`）。
  两套规则都叫授权却互不同步，所以只留一套
- 没有递归保护：A 调 B、B 又调 C 这种链，目前靠配置自觉（后续可加深度上限）
- **自己调自己直接拒**（`agent_self_call`）：把 bot 分享成工具后，那条工具会出现在
  它**自己**的工具列表里（应用是按账号可见的，tools/list 不知道是谁在调）。
  而描述写着「把话交给杰克处理」时，杰克完全有理由认为这就是它的活，
  于是调自己：一轮几分钟、烧满额度直到超时。现在调用入口会拦：
  目标 bot 等于调用方 bot（`bot_uid` 或调用账号本身就是那个 bot）时直接拒绝，
  并给模型一句可执行的话（「直接用自己的能力完成，不要转回来」）—— 不是靠它
  自己从描述里看出这是它自己。
- 只认 `msg_type=text`：bot 回的 `tool_use` / `tool_result` 卡片不算结果
- 「有几单在同一 topic 上等」是**进程内**计数：多实例部署时，另一个实例上的
  并发单本实例看不见，旧客户端的兜底认领会在跨实例并发时退化（精确归属不受影响，
  它只看 `reply_to` / 会话 id）

## 计费：谁调用谁付费、按量给

设计 §4.5。agent 在 definition 里声明单价；未声明 = 免费（金额记 0，但调用关系与用量照记，便于后续定价回溯重算）。

```jsonc
"definition": {
  "pricing": { "unit": "token",  "per_1k_tokens": 0.02, "currency": "CNY" }
  // 也支持 {"unit":"run","per_run":0.05} / {"unit":"second","per_second":0.001}
}
```

**bot 型不支持 `unit=token`**：token 用量产生在 bot 那一侧，平台拿不到（run 的 `usage_json` 为空），
按 token 计价会一直算成 0 元 —— 发布者以为在收费，实际全程免费。发布接口直接拒绝
（`400 invalid_pricing_for_bot`）。bot 型用 `unit=run`（按次）或 `unit=second`（按时长）。

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

