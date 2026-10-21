# service-run-worker

把**你自己的 agent**（本地/云端的 CLI，或某个常驻进程的包装脚本）接到 CatsCo 平台上：

- 别人（或你自己）在网页「Agent 工具」里添加上架的应用后，MCP 里就能看到 `catsco_<slug>` 工具
- 调用时平台创建 run → **本 worker 认领并在这台机器上执行** → 结果回填给调用方
- 不需要公网端口、不需要反向代理；worker 主动出网轮询

## 1. 前置

平台侧需要配好实例 token（运维在 `prod.env` 里设置，容器已有）：

```
CATS_SERVICE_INSTANCE_TOKEN=<64 位随机串>
```

> 这个 token 是**实例身份**，谁拿到谁就能认领 run，务必只放在自己机器上。

## 2. 编译

```bash
go build -o service-run-worker ./cmd/service-run-worker
```

## 3. 运行

```bash
export CATS_SERVICE_INSTANCE_TOKEN=<平台给的 token>

./service-run-worker \
  -base https://app.catsco.cn \
  -instance my-mac-01 \
  -cmd 'my-agent --input {input_file}'
```

参数：

| 参数 | 说明 |
|---|---|
| `-base` | 平台地址，默认 `https://app.catsco.cn` |
| `-token` | 实例 token（也可用环境变量） |
| `-instance` | 实例 id，≤64 字符，默认 `主机名-pid`；**同一实例 id 重启会接管上次的租约** |
| `-cmd` | 执行命令模板（**可选**，应用自己声明了命令就不用给）；占位符 `{slug}` `{run_id}` `{session_id}` `{input_file}` |
| `-agents` | **只认领这些 slug**（逗号分隔）；空 = 认领所有应用的 run（多租户不推荐） |
| `-timeout` | 单次 run 超时，默认 10m |
| `-poll` | 无任务轮询间隔，默认 2s |
| `-heartbeat` | 续租/实例心跳间隔，默认 20s |
| `-once` | 只认领一轮就退出（联调用） |

### 传给命令的东西

| 位置 | 内容 |
|---|---|
| **stdin** | run 的 `input` 原文（JSON） |
| `CATSCO_INPUT_FILE` / `{input_file}` | 同上，落在临时文件里（适合只接受文件参数的命令） |
| `CATSCO_RUN_ID` / `{run_id}` | run id |
| `CATSCO_SESSION_ID` / `{session_id}` | 会话 id（续聊时同一个） |
| `CATSCO_AGENT` / `{slug}` | 应用 slug |
| `CATSCO_SERVICE_TOKEN` | 平台签的服务 token（如果被调 agent 要回调平台 API，用它做身份） |

### 多个 agent 共用一个 worker

**命令可以按应用分别指定**（推荐）：发布时在界面里给该应用填「本机执行命令」，
存在 `definition.runner.command` 里，平台在 claim 时随 run 下发，worker 按应用分派：

```json
{"runner": {"type": "worker", "command": "claude -p {input_file}"}}
```

于是同一个 worker 能同时服务多个 agent：

| 应用 | runner.command |
|---|---|
| `my-claude` | `claude -p {input_file}` |
| `my-codex` | `codex exec --input {input_file}` |

只有应用没声明 `runner.command` 时才回落到启动参数 `-cmd`；两者都没有则该 run 直接失败
（`no_command`），不会静默乱跑。所以命令全在应用侧声明时，启动**不需要** `-cmd`：

```bash
./service-run-worker -agents my-claude,my-codex
```

### 命令的返回值

- **退出码 0** → 成功。stdout 作为结果：
  - 纯文本 → `output.text = stdout`
  - 或输出 `{"output":{...},"usage":{"prompt_tokens":120,"completion_tokens":80},"files":[...]}` → 原样采用（`usage` 会进账本计费）
- **退出码非 0** → 失败（`retryable=false`），stderr 尾部作为 `error_text`
- **超时** → 失败但 `retryable=true`，别的实例/重启后可重试
- **worker 收到 SIGINT/SIGTERM** → 当前 run 标记 `retryable=true` 后退出

## 4. 上架这个 agent

用你自己的账号 JWT 发布：

```bash
curl -X POST https://app.catsco.cn/api/service-agents/publish \
  -H "Authorization: Bearer $CATSCO_USER_JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "slug": "my-agent",
    "name": "我的助手",
    "visibility": "private",
    "definition": {
      "name": "我的助手",
      "description": "跑在我自己机器上的 agent",
      "runner": { "type": "worker" }
    }
  }'
```

> ⚠️ `"runner": {"type": "worker"}` 不能省：平台对默认（codex）执行要求上传代码包，
> 而 worker 场景不需要包——写 `worker` 类型才会走占位包。

- `visibility=private` → 只有你自己可见可调（自用）
- `visibility=public` → **别人也能在 MCP 里看到 `catsco_my-agent` 并调用**（见下节）
- 想给某个 bot 单独裁剪工具清单，用网页面板的「配置范围」

## 5. 分享给别人

1. 发布时用 `visibility: "public"`
2. 别人的 MCP 里会直接出现 `catsco_<slug>`（平台默认“全部公开应用可见”，不需要对方先添加）
3. 对方一调用，run 就会被**你的 worker** 认领执行；平台上只是记账/转发

所以务必用 `-agents` 限定自己服务的应用，否则你的 worker 会把别人的 run 也领走：

```bash
./service-run-worker -cmd 'my-agent --input {input_file}' -agents my-agent
```

补充说明：

- 网页「Agent 工具」面板仍受 `CATSCO_SERVICE_CATALOG_UID_WHITELIST` 控制（管理面白名单）。
  不在白名单的账号看不到这个面板，但**不影响**他们通过 MCP 调用公开应用。
- 调用方的额度预检、账本记录都在平台侧完成（`service_run_ledger`）。

## 6. 常驻（systemd 示例）

```ini
[Unit]
Description=CatsCo service run worker
After=network-online.target

[Service]
Type=simple
Environment=CATS_SERVICE_INSTANCE_TOKEN=<token>
Environment=CATSCO_WORKER_CMD=/usr/local/bin/my-agent --input {input_file}
ExecStart=/usr/local/bin/service-run-worker -base https://app.catsco.cn -instance my-host
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

## 7. 常驻进程怎么接

如果你的 agent 不是一次性 CLI，而是一个常驻进程（比如本地 MCP server / HTTP 服务），
用一层薄包装脚本当 `-cmd` 即可（示例：把 stdin 转给常驻服务再打印结果）：

```bash
#!/usr/bin/env bash
# wrapper.sh —— 把平台转发来的 input 交给本地常驻 agent
exec curl -sS -X POST http://127.0.0.1:9xxx/run \
  -H 'Content-Type: application/json' --data-binary @-
```

```bash
./service-run-worker -cmd 'wrapper.sh' -instance my-host
```

## 8. 排查

| 现象 | 原因 |
|---|---|
| `401 unauthorized` | token 不对，或平台没配 `CATS_SERVICE_INSTANCE_TOKEN` |
| run 一直 `submitted` | worker 没在跑 / 实例 id 冲突 / 认领接口被墙 |
| run 变 `orphaned` | 租约过期（worker 卡死或网络断），会被重新派发 |
| `exit_1: …` | 你的命令非 0 退出，看 stderr 尾部 |
