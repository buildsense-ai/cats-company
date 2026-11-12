# 附件隐私实现回退（2026-11-12）

## 范围与原因

本分支依次对合并提交执行第一父提交反向变更：

1. #930 `4b83ef215bf51d525348814aff8eeda4b0ebcf81`：移除 uploads 鉴权路由安装器和部署接线。
2. #922 `cc9a8ef6822c86abd1b540ba14e4d9abb9c52c91`：移除持久化消息附件访问控制、媒体 Cookie 引导、artifact 授权回调及前端接线。

配套 XiaoBa-CLI 分支 `revert/private-attachment-download-630` 反向变更 #630。不是整版降级；后续会话转录、连接稳定性、Daily Notes、handoff、worker 包缓存（#932）等无关功能不回退。

已确认普通鉴权 GET 在实际 API 请求发出前等待无超时的 `ensureFileMediaSession()` / 浏览器锁，可能阻塞模型配额、会话状态与消息请求。本回退直接移除这一全局依赖。没有据此认定某次生产故障的唯一触发原因。

## 安全降级（必须明确接受）

**这不是等价的隐私方案。已知旧附件 URL 可能再次允许匿名读取；消息成员关系、上传者身份和 `message_id` 不再限制附件读取。** Artifact 静态入口和索引也恢复旧行为。URL 不应被当作授权凭证，不要将其公开发布。现有后台管理/消息 API 鉴权不因本回退被移除。

## 有意保留的例外

- 保留 PostgreSQL `000067_file_access.up.sql` 和 `.down.sql`，以及 MySQL/PostgreSQL 三张附加表的幂等 schema 定义和 `CreateSchema` 集成。既有数据留存，运行时不再读取/写入隐私表。**不执行 down、不删表、不改迁移版本、不删消息或文件。**
- 保留日志脱敏，不恢复源 URL、签名 query、Authorization/Cookie 或凭证日志。
- `.cc` 和 `.cn` 的旧 `/uploads/` ZOS 路由增加共存例外：非空 Authorization 或 `message_id` 直接进入应用读取器（418 内部转发），绝不先送 ZOS。`preview` / `download` 继续走应用。应用响应不再回落 ZOS。
- 不恢复 #930 安装器。这两个仓库文件的修改**不会自动修改已部署 Nginx**，也不会自动回退 worker、网关或存储策略。

## 共存矩阵

| 客户端/请求 | 回退后的仓库目标行为 |
| --- | --- |
| 回退 XiaoBa：旧匿名相对/绝对附件 URL | ZOS 旧路径；未命中可回应用 |
| 已部署 #630 XiaoBa：Authorization + message_id | 直接应用读取；上下文 query 不再参与附件授权 |
| 旧页面媒体 Cookie / 浏览器原生媒体 | 普通链接；不要求媒体会话初始化 |
| preview / download、HEAD、Range | 保持旧应用读取语义；合成数据回归测试覆盖 |
| 普通鉴权 GET | 无媒体会话/浏览器锁前置等待，原 API 鉴权仍生效 |
| 仍运行隐私版 artifact worker/gateway | **不保证可用**；若应用授权回调先消失，可能全部拒绝 |

测试仅使用本地合成数据，不读取生产附件或会话。静态契约不等于生产端到端验收。

## 部署顺序与独立审批门槛

本 PR 仅提交代码，**不得据此合并、开启 auto-merge、发布、执行迁移、重载 Nginx 或改线上 ACL/环境变量。** main 的 CI 可能触发部署，因此合并同样需要另行授权。

若未来获批部署，负责人必须先核查并协调：

1. 盘点两个线上 vhost、已安装 `uploads-zos.conf`、独立 gateway / artifact worker、鉴权回调和 ZOS/存储策略的实际版本；保留配置与软件回滚点。
2. 制定 worker/gateway 与应用授权回调的切换窗口：不能留下“旧受保护 worker 依赖已移除回调”的混合状态。不要凭应用 PR 假定独立 worker 已回退。
3. 将两个线上 uploads 路由更新为本分支共存行为，备份、`nginx -t` 后才能另行获批 reload；不要运行已删除的安装器或只恢复 #930 之前的原始路由（会把 #630 鉴权请求送入 ZOS）。
4. 先恢复服务端和独立静态服务的旧 URL 可读能力，再发布匿名下载的回退客户端。对合成附件验证 `.cc` / `.cn`、旧/新客户端、GET/HEAD/Range、preview/download、artifact 主文档和子资源、未命中/异常分支。
5. 验证聊天、模型配额、fork 状态恢复，且没有 `/api/files/media-session` 前置等待。检查脱敏日志、缓存与跨域策略。若入口仍被外部策略阻断，不能宣称仓库回退已修复生产。

## 本地检查入口

- `python deploy/attachment-privacy-rollback_test.py`
- `python deploy/attachment-privacy-rollback-nginx_test.py`（需要 nginx；仅本地假上游）
- `npm test` / `npm run build`（webapp）
- `go test ./server/... ./cmd/tos-fetch`
- `node --test deploy/prod/ops/artifact-static-host.test.mjs`

实际测试结果及未覆盖的真实数据库/容器/生产验收，见 PR 描述与 CI；不要将未运行项记为通过。
