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
| `catsco_compute` | 统一算力入口，查询报价目录、比较资源、正式报价和查询任务 |

`catsco_<slug>` 参数：

| 参数 | 说明 |
| --- | --- |
| `input` | 应用入参，透传；**形状由应用作者的 `input_schema` 声明**（见「工具契约」） |
| `session_id` | 可选；续聊会话 id（不传 = 新会话） |
| `client_token` | 幂等键（作用域：同会话内；重发返回同一 run） |
| `bot_uid` | 可选；以 bot 身份调用时带 bot 的 uid（校验 bot 归属：owner 必须是调用者；若该 bot 定义里显式设了 `serviceAgents`，被调 agent 必须在其中 → 否则 `agent_not_enabled_for_bot`）。不影响 `tools/list`（工具清单跟账号） |

## 统一算力入口

`catsco_compute` 通过 `action` 查询报价目录、比较资源、创建正式报价和查询任务。当前托管执行使用固定 Slurm CPU 验收作业。

| action | 用途 | 必要参数 |
| --- | --- | --- |
| `catalog` | 只读查询当前有效的单节点报价规格、按节点费率和单任务节点上限 | 无 |
| `preview` | 比较当前 Agent 设备和 Cats 托管 Slurm，只读返回容量、适配性、费率和预览估算 | `task`，可选 `agent_runtime` |
| `quote` | 创建需要用户本人确认的正式报价 | `task.nodes`、`logical_cpus`、`memory_mib`、`wall_seconds` |
| `status` | 查询已创建任务的状态、云操作号、Slurm 作业号、资源回收和实际扣费 | `quote_id` |

本机资源采样可以由 XiaoBa 的 `local_compute_resources` 提供。它属于本机采样能力，不再作为算力执行入口展示。采样结果放入 `agent_runtime`，并标明来源为 `agent_reported`。没有采样结果时，预览保留未知状态。

`preview` 的 `task.topology` 为 `independent_shards` 或 `distributed`，分别填写正整数 `shards` 或 `nodes`。`logical_cpus`、`memory_mib`、可选 `gpu_count` 和 `memory_mib_per_gpu` 是每分片或每节点需求。可选的 `estimated_billable_s` 是预览假设时长，独立任务按每分片填写，分布式任务按每节点填写。它只用于预览估算，不能作为正式费用上限。

`preview` 返回 `catsco.compute-preview.v1` 结构化结果，包含各候选的 `fit`、`capacity`、`reason_code`、来源、采样时间和可用采样字段。文本第一段是聊天 Markdown 表格，第二段是结构化 JSON。预计完工 `eta_s` 当前为 `null`，供应商库存和排队时长未知时必须保留未知。

`catalog` 返回 `skus`、`max_nodes_per_task` 和 `units_per_credit`。规格条目包含每节点的逻辑 CPU、内存、按节点费率、计价周期、取整粒度和费率有效期。正式报价选用满足每节点 CPU 和内存要求的最低价有效规格。多个节点使用同一规格，费用上限按节点数线性增加，CPU 和内存要求仍按每节点校验。目录提供报价条件，实时库存和空闲容量需另外查询。

`wall_seconds` 由 Agent 按任务填写，单位为秒，必须为正数且是整分钟。运营可通过 `CATSCOMPANY_COMPUTE_MAX_WALL_SECONDS` 按队列能力设置报价时限，设置后 `catalog` 返回 `max_wall_seconds`。留空或填 `0` 时 Cats 无额外运营时限，Slurm 仍执行其队列策略。部署时应先升级 worker，再放开原有的 300 秒限制。

`quote` 只需要作业形状：节点数、每节点逻辑 CPU、每节点内存和 `wall_seconds`。每节点计费上限就等于用户填写的作业时限，开机、提交、恢复和释放的时间由运营承担，不计入用户账单；调用方额外填写的上限字段不会生效。`wall_seconds` 只表示 Slurm 作业时限，排队和资源开通等待不计入用户资源时长。

报价返回的 `max_node_seconds` 等于节点数乘以用户填写的作业时限。`max_units` 按该时长取整及发布费率计算，用于确认时预留额度。报价不会自动开通资源。任务结束时按资源实际被观察到的时间起点至资源回收时间结算，实际扣费不超过用户填写的作业时限，作业超出该时限会被 Slurm 终止并进入回收。

旧报价数据可能没有 `max_resource_seconds_per_node`。worker 会从旧报价的 `max_node_seconds / nodes` 回退读取资源预算，旧任务继续按原报价语义结算。

预览不依赖容量快照。未配置 `CATSCOMPANY_COMPUTE_SLURM_SNAPSHOT_FILE` 时，`preview` 直接使用 `catalog` 的已发布规格与费率：返回 `source=platform_catalog`、`catalog.sku_id`、`catalog.on_demand=true`，容量 `capacity=unknown`、`eta_s=null`、`reason_code=slurm_capacity_on_demand`。这对应按需开通的零常驻部署——没有空闲节点可报，库存容量在开通时确认，开通与排队时间不计入用户账单。规格不足时返回 `fit=does_not_fit` 与 `no_published_shape`（无匹配 CPU 规格或含 GPU 需求）或 `nodes_exceed_catalog_limit`（节点数超过报价上限）；`catalog` 为空时返回 `slurm_catalog_unavailable`。

部署方也可以配置 `CATSCOMPANY_COMPUTE_SLURM_SNAPSHOT_FILE` 指向一个绝对路径，提供实测容量，此时 `preview` 优先使用该快照并返回 `source=platform_slurm_snapshot`。由平台采集器原子更新该路径的本地普通 JSON 文件。采样对应一组节点规格一致的 Slurm 队列，包含 UTC RFC 3339 `sampled_at`、已配置且可调度的 `nodes`、其中完全空闲的 `idle_nodes`、`logical_cpus_per_node`、`memory_mib_per_node`、可选 `gpus_per_node` 和 `memory_mib_per_gpu`。未知的 GPU 字段留空，已确认没有 GPU 时 `gpus_per_node` 为 0。文件缺失、过期或格式异常时云集群状态为未知，此时预览回落到 `platform_catalog` 口径。

仓库的 `deploy/collect-slurm-snapshot.py` 可在已配置 Slurm 客户端的 Linux 主机上用 Python 3.8 或更新版本运行。为采集器准备平台配置文件，指定一个分区及预期的同规格节点资源。以下示例对应一组 2 CPU、3663 MiB、无 GPU 的节点。

```json
{"partition":"batch","logical_cpus_per_node":2,"memory_mib_per_node":3663,"gpus_per_node":0}
```

```bash
python3 deploy/collect-slurm-snapshot.py --config /etc/catsco/slurm-preview.json \
  --output /var/lib/catsco-compute/slurm.json
```

可用现有定时器每 30 秒运行一次。采集器执行只读 `sinfo`，核对分区和节点规格。启用的分区中，`idle`、`allocated`、`mixed`、`completing` 计入可调度节点，纯 `idle` 计入空闲节点。带状态标志的节点保守排除。已存在的空分区报告零节点。采集失败或遇到混合规格时保留旧文件，旧采样超过两分钟后自然失效。

采集器原子替换快照，文件模式为 `0640`。让采集器可写父目录，并为 Cats 进程配置快照读取权限。两者使用不同用户时，可让父目录以共同可读组设置 setgid，使新文件继承组。容器部署应挂载快照父目录，并将 `CATSCOMPANY_COMPUTE_SLURM_SNAPSHOT_FILE` 指向容器内文件路径。省略 `--output` 时输出一份 JSON 到标准输出，供隔离网络中的部署方通过已有受控通道传递。

采集文件可附带同一规格的 `pricing` 对象，字段为 `cny_micros_per_node_period`（一个节点在一个计价周期内的人民币金额，单位为百万分之一元）、`rate_period_s`、`billing_increment_s` 和 RFC 3339 `valid_until`。它是平台发布给用户的费率，应由已核实的成本口径和运营定价生成。费率未发布、过期、无有效周期或格式异常时云端仍显示待报价。节点采样异常时不会试算。Agent 不提供或覆盖费率。

换算沿用云机器账本，1 元 = 100 积分，1 积分 = 1,000,000 账本单位。每分片或每节点的预计计费时长先向上取整到 `billing_increment_s`，再用 `ceil(节点周期价格的账本单位 × 取整后秒数 × 分片数或节点数 ÷ rate_period_s)` 计算。这里计的是按任务分配的节点时间。供应商账单仍按资源实际生命周期核对。冷启动、盘、传输、闲置与固定费用只有纳入发布费率或相应计费时长才会进入这次试算。

例如，试算费率为 300000 微元／节点／3600 秒，粒度为 60 秒时，两个节点各计费 1800 秒，预计 30 积分。该数值仅用于演示换算。

## Slurm 文件直传试点

这是 Cats 托管 Slurm 的一种可选执行方法。实际任务可根据执行环境、数据所在位置、网络可达性和授权采用已有文件引用、存储直传、集群接收入口或其他工具。

此试点复用私有 `runner.type=worker` 应用和 `catsco_run_status`。集群里的 `service-run-worker` 主动调用 `/api/service-worker` 认领请求。Cats 只收到任务参数、文件引用和状态。文件所在设备直接向对象存储上传，集群 worker 从对象存储取输入并上传产物，下载设备直接向对象存储取产物。

| `input.action` | 其他参数 | `output` |
|---|---|---|
| `prepare` | `size_bytes`，`sha256`（小写十六进制） | `input_ref` 和包含 `method`、`url`、`fields`、`expires_at` 的 `upload` |
| `submit` | `input_ref` | `slurm_job_id`、`input_sha256`、`artifact_ref` |
| `download` | `artifact_ref` | `download_url`、产物的 `sha256` 和 `size_bytes` |

调用 `catsco_slurm-file-pilot` 时把上述参数放在 `input`。收到异步句柄时，用 `catsco_run_status` 获取最终 `output`。文件所在设备先算大小和 SHA-256，再用 `prepare` 返回的 URL 发 `multipart/form-data` POST。将所有 `upload.fields` 原样作为文本表单字段，最后添加名为 `file` 的二进制字段。收到 2xx 后再提交。不要把文件正文放入 MCP，也不要把临时签名地址或表单字段贴进聊天。`download` 可在产物引用的有效期内获得新地址。

本轮验收模板固定为单节点、单 CPU 的 SHA-256 校验，单文件 1 字节至 256 MiB。上传凭证 30 分钟，仅授权一个指定大小的对象。输入引用与产物引用绑定 Cats 调用者 UID，worker 暂存时再次核对大小和哈希。产物引用 24 小时，下载地址最多 5 分钟。同一 Cats `run_id` 的完成回执会复用，已有领取记录而终态不明时应先对账。重试 MCP 提交时沿用原 `session_id` 和 `client_token`。当前交付范围为私有试点，分片续传、用户取消、算力付费与扩容后续接入。

### 试点部署

1. 用运营方的专用账号发布仅该账号可调用的 `slurm-file-pilot`，`visibility=private`，`definition.runner.type=worker`，不设远端 `runner.command`。描述说明三个 `action`、参数及直传方式。此账号只用于此应用，因为公网 worker 凭证按应用所有者划分认领范围。
2. 在可访问 Slurm 的 Linux 主机上分别构建 `./cmd/service-run-worker` 和 `./cmd/slurm-file-pilot`。由有权使用指定分区和账户的专用 Slurm 身份运行，准备同一绝对路径、提交主机和计算节点都可访问且仅该身份可写的 `CATSCO_SLURM_SHARED_DIR`。
3. 配置 `CATSCO_SLURM_PARTITION`、`CATSCO_SLURM_ACCOUNT`、`CATSCO_SLURM_S3_ENDPOINT`（设备可达的 HTTPS origin，且支持 S3 签名 POST policy）、`CATSCO_SLURM_S3_REGION`、`CATSCO_SLURM_BUCKET`。注入仅限试点桶下 `slurm-file-pilot/` 前缀的 `CATSCO_SLURM_S3_ACCESS_KEY`、`CATSCO_SLURM_S3_SECRET_KEY`、可选 `CATSCO_SLURM_S3_SESSION_TOKEN`，并设置至少 32 字节随机值作为 `CATSCO_SLURM_REF_SECRET`。为对象前缀设生命周期，保留期覆盖 24 小时产物引用期。共享目录只保留用于幂等的领取记录和回执，在 Cats run 已终态且确认无重试后清理。
4. 注入同一专用账号的 `CATS_WORKER_TOKEN`，启动 `service-run-worker -agents slurm-file-pilot -timeout 4m -cmd /绝对路径/slurm-file-pilot`。部署 Cats 后端及 worker 时使用包含 `caller_uid` 认领字段的版本。集群 worker 只需出站访问 Cats 与对象存储。

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
> **worker 券不受内测白名单限制**：`POST /api/service-worker/session` 用账号登录态换券，
> 默认对任何已登录账号开放（权限边界在 run 级：只能领自己名下应用的 run）。
> 2026-11-05 曾误把 Agent 工具内测白名单（`CATSCO_SERVICE_CATALOG_UID_WHITELIST`）
> 套在这个接口上，结果不在名单里的账号全部 403、换不到券 —— 表现为「bot 在线但一直不干活」，
> 线上累计 366,006 次 403 / 7 次 200。需要临时收紧时用独立开关
> `CATSCO_SERVICE_WORKER_UID_WHITELIST`（空=不限制）。


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


## 自分享（`catsco_share_self`）：agent 可以把自己挂成工具

上面那句「直接给 agent 一个发布工具是危险的」仍然成立 —— 所以给的不是发布工具，而是把
那三个危险面逐个切掉之后剩下的那一小块：

| 危险面 | 面板发布 | `catsco_share_self` |
|---|---|---|
| `runner.command` 写任意命令 | ✅ 可写 | ❌ 只生成 `runner:{type:"bot",bot_uid:<自己>}` |
| `visibility: public` 对全平台放出 | ✅ 可写 | ❌ 只能 `private`/`friends`；传 `public` 直接报 `share_self_public_needs_owner` |
| slug 写错覆盖别人的应用 | 409 报错 | 409 → 自动加 `-2`/`-3` 后缀，**不会覆盖** |
| 挂别人的 bot | 归属校验 | ❌ `bot_uid` 取自调用者身份，参数里根本没有这个字段 |

**可见性范围**：这个工具只在「调用者本身就是 bot、且它的 owner 在
`CATSCO_SERVICE_CATALOG_UID_WHITELIST` 里」的 MCP 会话挂出来。人的工具面里没有它。

**参数**

```jsonc
{ "action": "publish",            // publish（默认）| unpublish
  "name": "杰克运维",
  "slug": "jack-ops",             // 省略则按 name 生成
  "description": "服务器排障派给杰克；用户问“为什么 502”时用",  // friends 必填
  "input_schema": { "type": "object", "properties": { "text": { "type": "string" } } },
  "visibility": "friends" }       // private | friends（默认 friends）
```

**边界（硬约束）**

- 同一个 bot 最多分享成 `10` 个应用；超了报 `share_self_limit`。
- `unpublish` 只能收回**自己挂的**（按 `definition.runner.bot_uid` 比对），收回即 `inactive`。
- `definition.self_published = { bot_uid, at }` 留痕，出问题能看出「谁、什么时候放的」。
- 每次 publish/unpublish 都记日志；目录变更会推 `list_changed`。
- 与「加 bot 为好友」互不覆盖：自分享只写 `service_agents` 一条目录记录，
  不触碰 channel agent binding；`friends` 可见性判定的仍然是**这个 bot 自己的好友**。
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
2. 消息一落库就唤醒（进程内事件通知），**不再每 500ms 轮询消息表**：等 bot 回一条
   **属于本次调用**的 `text`。查库退化成安全网 —— 通知没送到时（多实例、别的进程
   直接写库）按 500ms 起步、翻倍退避到 10s 兜底；
3. 拿到就写 run 结果，`output.text` = 它的回复（多段会被拼起来）。

收尾规则：收到第一段后**再等 5 秒没有新消息**才收尾 —— bot 会把长回复切段，
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

### 按调用方配额：一个调用方不能把总闸占满

`max_inflight` 是「整个应用」的闸。应用一旦设为 `public`，16 个名额可能被先到的调用方
吃光 —— 这时真正的用户拿到 `bot_busy`，看不出是「谁在刷」。

所以平台在每个应用上还有一层**按调用方**的配额：同一个调用方对同一个应用最多同时占
**2** 单（平台默认；`CATSCO_BOT_RUNNER_MAX_CALLER_INFLIGHT` 可调，夹到 `[0,8]`，
`0` = 不限）。超出的单立刻以
`caller_busy: 你在这个应用上同时已经有 N 单在跑（每个调用方上限 M）…` 收尾，同样
`retryable=true`。

两个实现细节（改之前踩过）：

- **闸排在占位之前**：`StartServiceRun` 会把 run 推到 `running`，而闸是「数 `running` 的单」。
  如果先占位再查，同时到达的几单会互相看见对方 —— 实测并发 3 单时 3 单全被拒、一单没跑。
  现在同一次准入（查 + 占位）在每应用一把进程内锁里串行，结果是确定的「放行到上限、其余拒掉」。
- **被拒的单直接落终态**（`retryable=true` → `orphaned`），不会在 `submitted` 上滞留 2 分钟
  再被 reaper 当成「没人领」收走。

判据是 `sessions.caller_uid`（谁发起的）。两个码要分清：`caller_busy` = 你自己并行
太多；`bot_busy` = 整个应用满了（别人也在用）。
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
4. 但若这个会话里**除目标 bot 之外有人说过话**（调用方，或调用方代言的 bot），此后目标 bot
   的无标记消息一律不认 —— 调用方也是 bot 时，它的运行时会把目标 bot 的进度消息当成新任务
   回话，两边互刷，那些消息是对那句回话的回应，不是对我们 prompt 的答复。
   平台自己写的 prompt（`metadata.catsco_bot_call`）不算「有人说话」；附件不在此列
   （老客户端发附件天生没标记）。

   第 4 条只**停止兜底认领**，不做快速失败：带 `reply_to` 的真答案晚到 40 秒也照收；
   一直没等到才在超时那一刻报 `bot_reply_ambiguous`（而不是 `bot_timeout`）。
5. 若这个 bot 在最近的会话记录里**有过带 `reply_to` 的真实回复**（说明它的客户端会回填），
   本次就只认领带归属的回复：不回填的一律当成「对着会话上下文随口回话」拒掉，
   超时如实报 `bot_reply_ambiguous`（原因写「会回填却没回填」）。
   没有任何回填证据的 bot（老客户端、人工代答的 bot）继续走第 3 条兜底，行为不变；
   能力探测本身失败时按「不会回填」处理（fail-open）。

   为什么需要第 5 条：调用方 bot 自激互刷时，目标 bot 的会话上下文被带偏，**第一条**
   答复就是「回环信息，没有新任务」这类话 —— 实测在 prompt 之后 1 秒就到，早于任何人
   插话，第 4 条根本管不到。而它真正的答复一定带 `reply_to`（同一个 bot 15 分钟内
   既回过带 `reply_to` 的 `OK`，也回过一堆不带的闲话）。


第 3 条是兼容老版本用的兜底：真并发时它会让路，`bot_reply_ambiguous` 之后如实失败
——已经看见 bot 回了，就不是「没回」而是「分不清」，这两件事对调用方的处置完全不同
（前者该升级客户端，后者该重试）。

**明确属于别人的回复不算「分不清」**，只是噪音，直接跳过。这条边界看着琐碎，实际很要紧：
上一单超时（`bot_timeout`）之后 bot 并不会停手，它跑完照旧把回答发回来，那条消息带着
**上一单**的 `reply_to`；此时正在等的下一单如果把它当成「有回复但认领不了」，就会在
歧义窗口之后报 `bot_reply_ambiguous` 把一次正常调用打死。分得清就该按分得清处理。
**调用方 bot 会「自激」**：调用方本身是个 agent bot 时，目标 bot 的进度消息（`mode=code`）
落进同一个 topic，调用方的运行时把它当成一条新任务来回应；目标 bot 再把那句回应当成新
任务 —— 两边互刷（实测一次 18 条 / 70 秒，单子 27.5s 就「completed」，交出去的是句
「回环碎片，没有新任务」）。平台侧靠上面的第 4 条把这类消息拒掉、如实报
`bot_reply_ambiguous`；**根治在客户端**：等工具结果的这段时间，不要对这个会话回话。


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
| 会话里除目标 bot 之外有人说过话（bot↔bot 互刷） | 无标记消息不认领；等满超时报 `bot_reply_ambiguous`（不拿回环碎片当答案） |
| 这个 bot 会回填 `reply_to`，这次却没回填 | 不认领；等满超时报 `bot_reply_ambiguous`（多半是会话上下文被别的消息带偏） |
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
  "settle_seconds": 3,             // 收到一段后静默多久算说完，默认 5
  "ambiguity_grace_seconds": 30,   // 看得见回复却判断不出归属时等多久，默认 20
  "poll_interval_ms": 500          // 安全网查库的起始间隔（主路径是落库即通知），默认 500
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


## 现场自检（一条命令）

验「bot 当工具」这条链路以前靠临时探针，而探针本身坑过三次：HTTP 分片把 SSE 的
`data:` 行切断被当成「0 个工具」、只看 `content[0].text` 就说「结果拿不到」（正文其实在
`structuredContent.output.text`）、把某个配置组合的失败说成整个功能不可用。

现在固化在 `scripts/verify-agent-tools.mjs`：

```bash
# 只看这个身份能看到哪些工具（只读）
node scripts/verify-agent-tools.mjs --uid 1619 --secret "$CATSCO_JWT_SECRET"

# 真调一次并等到终态（会真的起一单）
node scripts/verify-agent-tools.mjs --uid 1619 --secret "$CATSCO_JWT_SECRET" \
  --call catsco_my_tool --text "只回复 OK"
```

输出示例（真实一次快调）：

```
身份 uid=1619 可见工具 4 个：
  - catsco_run_status
  - catsco_share_self
  - catsco_my_tool
调用 catsco_my_tool ...
  [3s] status=running
  [9s] status=completed
run=run_92fd... status=completed
output: OK
```

要点：

- `--json` 给机器读；退出码 0=成功、1=结果不符预期、2=参数问题、3=没等到终态；
- 判成功看 **`structuredContent.status === 'completed'`**，不要看正文长得像不像答案；
- 想复现「别人能不能用我的工具」，换 `--uid` 即可（可见性按身份判定）；
- 解析层只按 SSE 事件边界切（空行），`fetch` 已经解掉传输层分片 —— 换成裸 socket
  时要先自己去分片。

单元测试 `scripts/verify-agent-tools.test.mjs` 跟 CI 一起跑，锁住分片、多行 data、
半截 JSON 留痕、token 签名与参数解析这些点。
