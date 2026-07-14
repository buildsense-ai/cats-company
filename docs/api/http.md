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
      "is_online": true
    }
  ]
}
```

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

### 文件上传

**POST /api/upload**
```
Content-Type: multipart/form-data

file: <binary>
type: image|file
```

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

**POST /api/bots/deploy**
部署 managed Bot

**POST /api/bots/visibility**
设置 Bot 可见性

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
