# CatsCompany HTTP API

## 认证

**JWT Token:**
```
Authorization: Bearer <token>
```

**API Key (Bot):**
```
Authorization: ApiKey <api_key>
```

## 端点

### 认证

**POST /api/auth/register**
```json
{
  "username": "user123",
  "password": "pass123"
}
```

**POST /api/auth/login**
```json
{
  "username": "user123",
  "password": "pass123"
}
```

返回：
```json
{
  "token": "jwt_token",
  "uid": 3,
  "username": "user123"
}
```

### 用户

**GET /api/me**
获取当前用户信息

**POST /api/me/update**
更新用户资料

**GET /api/users/search?q=keyword**
搜索用户

### 好友

**GET /api/friends**
获取好友列表

**POST /api/friends/request**
发送好友请求

**POST /api/friends/accept**
接受好友请求

**POST /api/friends/reject**
拒绝好友请求

### 会话

**GET /api/conversations**
获取会话列表（包含最新消息）

返回：
```json
{
  "conversations": [
    {
      "id": "p2p_3_5",
      "name": "张三",
      "is_group": false,
      "preview": "最后一条消息",
      "latest_seq": 123,
      "last_time": "2026-03-05T10:00:00Z",
      "is_online": true,
      "notifications_muted": false
    }
  ]
}
```

`notifications_muted` 是当前账户针对该会话的浏览器通知屏蔽状态，未设置时为 `false`。

**PUT /api/conversations/notification-preferences**

设置或取消屏蔽当前账户对某个会话的浏览器通知。当前用户必须能访问该 P2P 会话或属于
该群组。

请求：
```json
{
  "topic_id": "p2p_3_5",
  "muted": true
}
```

返回 200：
```json
{
  "topic_id": "p2p_3_5",
  "notifications_muted": true
}
```

缺少字段返回 400，会话不可访问返回 403，存储操作失败返回 500；服务未实现会话通知偏好
能力时返回 501。

### 消息

**GET /api/messages?topic=p2p_3_5&limit=50**
获取消息历史

**POST /api/messages/send**
发送消息（HTTP 方式，推荐用 WebSocket）

### 图片生成

**POST /v1/images/generations**

使用现有 CatsCo 用户登录凭证（`Authorization: Bearer ...`）或 Bot API Key
（`Authorization: ApiKey ...`）调用服务端配置的图片生成服务。请求和响应遵循
OpenAI-compatible images generations 格式；服务端固定使用配置的模型并强制
`n=1`，不会向客户端暴露上游 API Key。

此接口仅在服务端配置 `CATSCO_IMAGE_UPSTREAM_URL` 和上游 API Key 后可用。
生产环境将这些值只保存在服务器的
`/srv/catscompany-prod/env/prod.env`；常规部署会保留该文件，不要把上游 API Key
写入仓库或 GitHub Actions 配置。

XiaoBa Skill 从现有 `CATSCO_HTTP_BASE_URL` 自动推导 `/v1/images/generations`，
并复用当前 CatsCo 登录或 Bot 凭证，无需任何生图专用客户端配置。

**POST /v1/images/edits**

使用与图片生成相同的 CatsCo 用户或 Bot 鉴权、限流、服务端模型和上游密钥，
用于带参考图的图片生成。请求使用 JSON，不接受 multipart、遮罩或远程图片 URL：

```json
{
  "prompt": "保持参考图中的角色身份，生成新的夜间城市场景",
  "images": [
    {"image_url": "data:image/png;base64,..."}
  ],
  "size": "1024x1024",
  "quality": "medium",
  "output_format": "png"
}
```

服务端接受 1-3 张不重复的 PNG、JPEG 或 WebP。单张解码后最多 8 MiB，
合计最多 16 MiB；只在内存中验证并转发，不下载远程图片、不持久化参考图，
也不会把 base64、CatsCo 凭证或上游 API Key 写入日志。客户端传入的 `model`
和 `n` 会被服务端配置覆盖，其中 `n` 固定为 1。第一版只支持同步响应，
`async=true` 会被明确拒绝，避免返回当前网关无法继续查询的任务 ID。

### 文件上传

**POST /api/upload**

Multipart（兼容现有 API 客户端）：

```
Content-Type: multipart/form-data

file: <binary>
type: image|file
```

Raw body（WebApp 使用）：

```http
POST /api/upload?type=image&raw=1
Content-Type: image/jpeg
X-CatsCo-File-Name: photo.jpg
X-CatsCo-File-Size: 12345

<raw file bytes>
```

`X-CatsCo-File-Name` 使用 URL 编码。`upload_incomplete` 表示服务端确认未保存完整文件，可安全重试；`upload_metadata_invalid` 表示声明的大小与实际 body 冲突，不应自动重试；`upload_invalid_request` 表示请求格式错误，不应自动重试；应用层只有在实际读取字节超过限制时才返回 `upload_too_large`。上游代理的请求体限制仍独立生效。

返回：
```json
{
  "url": "/uploads/xxx.jpg",
  "name": "image.jpg",
  "size": 12345
}
```

### Bot 管理

**GET /api/bots**
获取我的 Bot 列表

**POST /api/bots**
创建 Bot

**PATCH /api/bots?uid={uid}**
更新 Agent 基本信息与协作设置。`artifact_upload_enabled` 用于控制普通成员能否直接发布共享成果；默认开启，关闭后所有者仍可上传和管理。

**PATCH /api/bots/visibility**
设置 Bot 可见性

**PATCH /api/bots/skills-visibility?uid={uid}&v=owner|authorized|public**
设置 Agent 技能列表的可见范围。仅 Agent 所有者可调用；未设置时默认为 `owner`。

**GET /api/agents/skills?uid={uid}**
按技能可见范围返回脱敏技能列表。需要用户 JWT；响应不包含内容哈希或完整 Agent 配置。

**GET /api/agents/skills/runtime?uid={uid}**
按同一技能可见范围返回服务器 Agent 最近一次实际上报的运行时 Skills 清单。需要用户 JWT；响应只包含脱敏元数据（名称、描述、相对路径、调用标记和可选哈希），不包含服务器绝对路径、`SKILL.md` 内容或凭据。`runtime_status` 为 `unreported`、`reported` 或 `stale`；超过 15 分钟未收到服务端回执时为 `stale`。

**POST /api/bot/skills/inventory**
服务器 Agent 使用 Bot API Key 上报当前已加载的 Skills。服务端只接受 `xiaoba.bot-runtime-skills.v1` 清单，并在服务端写入接收时间；旧版 CatsCo 服务可返回 `404`、`405` 或 `501`，Agent 应继续运行并稍后重试。

### 管理员 API

需要 admin 权限（OC_ADMIN_USERNAMES）

**GET /api/admin/bots**
所有 Bot 列表

**POST /api/admin/bots/register**
注册 Bot

**POST /api/admin/bots/toggle**
启用/禁用 Bot

**POST /api/admin/bots/rotate-key**
轮换 API Key

**GET /api/admin/bots/stats**
Bot 统计信息
