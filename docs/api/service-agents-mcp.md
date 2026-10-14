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
