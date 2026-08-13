# Cats Company API 接入文档

> 版本：v0.5.0-alpha | 更新：2026-02-26

## 概述

Cats Company 是一个独立的即时通讯平台，提供：
- REST API（HTTP）— 用户管理、好友、群组、文件上传
- WebSocket — 实时消息收发
- Bot SDK（Go / Python / TypeScript）— 快速开发 Bot

### 系统边界

```
┌─────────────────────────────────────────────────┐
│              Cats Company Server                 │
│                                                  │
│  ┌──────────┐  ┌───────────┐  ┌──────────────┐  │
│  │ REST API │  │ WebSocket │  │ File Storage │  │
│  └──────────┘  └───────────┘  └──────────────┘  │
│                                                  │
│  认证方式：                                       │
│  • 用户：JWT Token（登录获取）                     │
│  • Bot：API Key（管理员注册获取）                   │
└──────────────────────────────────────────────────┘
        ▲              ▲              ▲
        │              │              │
   ┌────────────┐   ┌──────────────┐
   │ Web Client │   │  第三方 Bot   │
   │  (React)   │   │ (OpenCrawl 等)│
   └──────┬─────┘   └──────┬───────┘
          └──────────┬─────┘
                     │
```

### 服务地址

| 环境 | HTTP API | WebSocket |
|------|----------|-----------|
| 生产 | `https://app.catsco.cc` | `wss://app.catsco.cc/v0/channels` |
| 本地 | `http://localhost:6061` | `ws://localhost:6061/v0/channels` |

---

## 1. 认证

### 1.1 用户认证（JWT）

通过登录接口获取 JWT Token，有效期 7 天。

```
Authorization: Bearer <token>
```

或 query 参数：`?token=<token>`

### 1.2 Bot 认证（API Key）

Bot 通过管理员注册获取 API Key，格式：`cc_{hex_uid}_{random_32_bytes}`

```
Authorization: ApiKey <api_key>
```

或 query 参数：`?api_key=<api_key>`

---

## 2. REST API

所有请求 Content-Type 为 `application/json`。需要鉴权的接口在 Header 中携带 Token 或 API Key。

### 2.1 健康检查（无需鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查 |
| GET | `/ready` | 就绪检查（含 DB 状态） |

### 2.2 认证接口（无需鉴权）

#### POST /api/auth/register

```json
// Request
{ "username": "alice", "password": "mypassword", "display_name": "Alice" }

// Response 200
{ "uid": 1, "username": "alice" }
```

#### POST /api/auth/login

```json
// Request
{ "username": "alice", "password": "mypassword" }

// Response 200
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "uid": 1,
  "username": "alice",
  "display_name": "Alice"
}
```

### 2.3 用户接口

#### GET /api/users/search?q={query}

搜索用户（最少 2 字符）。无需鉴权。

#### GET /api/users/online

获取好友在线状态。需要鉴权。返回 `{ "online_uids": [2, 5, 8] }`

### 2.4 好友接口（需要鉴权）

| 方法 | 路径 | 说明 | 请求体 |
|------|------|------|--------|
| GET | `/api/friends` | 好友列表 | - |
| GET | `/api/friends/pending` | 待处理请求 | - |
| POST | `/api/friends/request` | 发送好友请求 | `{ "to_uid": 2, "message": "Hi!" }` |
| POST | `/api/friends/accept` | 接受请求 | `{ "request_id": 1 }` |
| POST | `/api/friends/reject` | 拒绝请求 | `{ "request_id": 1 }` |
| POST | `/api/friends/block` | 屏蔽用户 | `{ "user_id": 2 }` |
| DELETE | `/api/friends/remove?user_id={id}` | 删除好友 | - |

### 2.5 消息接口（需要鉴权）

#### POST /api/messages/send

REST 备用通道（推荐使用 WebSocket）。

```json
// Request
{ "topic_id": "p2p_1_2", "content": "Hello!" }

// Response 200
{ "seq": 42 }
```

#### GET /api/messages?topic_id={id}&limit={n}&offset={n}

获取消息历史。

#### GET /api/topics/{topicId}/files?limit={n}&before_id={messageId}

读取当前聊天内的文件历史，结果包含该聊天中所有发送者产生的文件，不按 Agent 或发送者过滤。

- 私聊仅允许双方读取。
- 群聊仅允许当前群成员读取。
- `before_id` 使用消息 ID 作为稳定的向前分页游标。

```json
{
  "topic_id": "p2p_7_440",
  "topic_name": "项目材料",
  "count": 1,
  "files": [
    {
      "id": "820:0",
      "name": "需求说明.pdf",
      "url": "/uploads/files/requirements.pdf",
      "message_id": 820,
      "topic_id": "p2p_7_440",
      "created_at": "2026-08-12T02:20:00Z",
      "block_index": 0
    }
  ],
  "has_more": false,
  "next_before_id": 0
}
```

### 2.6 群组接口（需要鉴权）

| 方法 | 路径 | 说明 | 请求体 |
|------|------|------|--------|
| GET | `/api/groups` | 我的群组列表 | - |
| POST | `/api/groups/create` | 创建群组 | `{ "name": "群名", "member_ids": [2,3] }` |
| GET | `/api/groups/info?id={groupId}` | 群详情 | - |
| POST | `/api/groups/invite` | 邀请成员 | `{ "group_id": 1, "user_ids": [4,5] }` |
| POST | `/api/groups/leave` | 退出群组 | `{ "group_id": 1 }` |
| POST | `/api/groups/kick` | 踢出成员 | `{ "group_id": 1, "user_id": 4 }` |
| POST | `/api/groups/mute` | 禁言 | `{ "group_id": 1, "user_id": 4 }` |
| POST | `/api/groups/unmute` | 解禁 | `{ "group_id": 1, "user_id": 4 }` |
| POST | `/api/groups/announcement` | 设置公告 | `{ "group_id": 1, "content": "..." }` |
| POST | `/api/groups/disband` | 解散群组 | `{ "group_id": 1 }` |
| POST | `/api/groups/role` | 修改角色 | `{ "group_id": 1, "user_id": 4, "role": "admin" }` |

### 2.7 文件上传（需要鉴权，支持 JWT 和 API Key）

#### POST /api/upload?type={image|file}

兼容两种上传方式：

- 通用客户端可继续使用 `multipart/form-data`，字段名为 `file`。
- WebApp 使用 `POST /api/upload?type={image|file}&raw=1`，请求体直接发送文件字节，并携带：
  - `X-CatsCo-File-Name`：经过 URL 编码的原始文件名。
  - `X-CatsCo-File-Size`：原始文件字节数。
  - `Content-Type`：文件 MIME 类型（已知时）。

Raw 上传在应用层只在实际读取超过限制时返回 `upload_too_large`；上游代理的请求体限制仍独立生效。请求体少于声明字节数时返回可安全重试的 `upload_incomplete`；元数据与实际字节数冲突时返回不可重试的 `upload_metadata_invalid`；非法 multipart 请求返回不可重试的 `upload_invalid_request`。

**上传限制**：
- 单文件最大：1GB（图片和文件均为1GB）
- 实际限制取决于服务器配置（磁盘空间、内存、网络带宽等）
- 支持的文件类型：图片（jpg/png/gif/webp）、文档（pdf/doc/docx/xls/xlsx/ppt/pptx）、压缩包（zip/rar/7z）、音视频（mp3/mp4/wav）、代码文件（md/go/py/js/json/xml/csv/txt）

```json
// Response 200
{
  "file_key": "abc123",
  "url": "/uploads/2026/02/abc123.png",
  "name": "photo.png",
  "size": 102400,
  "type": "image"
}
```

#### GET /uploads/{path}

访问已上传的文件（无需鉴权）。

### 2.8 Bot 管理接口

#### 2.8.1 用户端 Bot 管理（需要用户 JWT 鉴权）

用户可以自行创建、管理自己的 Bot。

| 方法 | 路径 | 说明 | 请求体 |
|------|------|------|--------|
| GET | `/api/bots` | 列出我创建的 Bot | - |
| POST | `/api/bots` | 创建新 Bot | `{ "username": "my_bot", "display_name": "My Bot" }` |
| DELETE | `/api/bots?uid={uid}` | 删除我的 Bot | - |
| PATCH | `/api/bots/visibility` | 设置 Bot 可见性 | `{ "uid": 10, "visibility": "public" }` |
| PATCH | `/api/bots/skills-visibility?uid={uid}&v={scope}` | 设置技能列表可见范围 | - |
| GET | `/api/agents/skills?uid={uid}` | 按所有者权限读取脱敏技能列表 | - |

#### POST /api/bots — 创建 Bot

```json
// Request (Authorization: Bearer <user_token>)
{ "username": "my_assistant", "display_name": "我的助手" }

// Response 200
{
  "uid": 10,
  "username": "my_assistant",
  "display_name": "我的助手",
  "api_key": "cc_0a_a1b2c3d4e5f6...",
  "visibility": "public"
}
```

创建成功后返回 `api_key`，Bot 即可用此 key 通过 WebSocket 接入。

#### PATCH /api/bots/visibility — 设置可见性

```json
// Request
{ "uid": 10, "visibility": "private" }

// Response 200
{ "ok": true }
```

`visibility` 可选值：`"public"`（所有人可见）、`"private"`（仅创建者可见）。

#### PATCH /api/bots?uid={uid} — 更新 Agent 设置

仅 Agent 所有者可调用。除了名称、头像、定位模板和用途说明外，可通过
`artifact_upload_enabled` 控制普通成员是否能直接发布共享成果。该字段默认为
`true`；关闭后所有者仍可上传和管理成果，普通成员只能查看已有成果。

```json
{
  "artifact_upload_enabled": false
}
```

#### PATCH /api/bots/skills-visibility — 设置技能列表可见范围

仅 Agent 所有者可调用。`v` 可选值：

- `owner`：仅所有者可查看。
- `authorized`：所有者和已添加该 Agent 的用户可查看。
- `public`：所有已登录用户可查看。

未设置的已有 Agent 默认按 `owner` 处理。

```json
// Response 200
{ "uid": 10, "skills_visibility": "authorized" }
```

#### GET /api/agents/skills — 读取 Agent 技能列表

需要用户 JWT 鉴权。Agent 所有者和好友可读取脱敏技能元数据；`skills_visibility=public` 还允许非好友用户读取。响应只包含已经同步到 BotDefinition 的技能标识、来源和版本，不返回内容哈希、Bot definition、模型、提示词、设备路径、技能源码或密钥。

```json
// Response 200
{
  "botId": "10",
  "skills_visibility": "authorized",
  "skills": [
    { "source": "skillhub", "skillId": "catsco/example", "version": "1.0.0" }
  ]
}
```

无权查看时返回 `403`：

```json
{ "error": "Agent 所有者未公开技能列表" }
```

#### GET /api/agents/{agentUid}/artifacts?status={active|deleted} — 读取 Agent 成果

读取当前用户有权访问的 Agent 成果。成果可携带稳定的来源任务标识 `source_topic_id`，前端可据此在“当前任务”和“全部成果”之间筛选；当旧成果列表完全没有该字段时，界面会回退到“全部”以避免误导或隐藏已有成果。

```json
{
  "status": "active",
  "count": 1,
  "viewer_relation": "owner",
  "visibility": "agent_users",
  "can_publish": true,
  "publish_mode": "immediate",
  "artifacts": [
    {
      "id": "lesson-game",
      "title": "课堂小游戏",
      "kind": "html",
      "url": "https://example.test/lesson-game/latest/",
      "status": "active",
      "source_title": "课堂任务",
      "source_topic_id": "p2p_7_440",
      "creator_type": "user",
      "creator_uid": "7",
      "creator_name": "成员甲",
      "uploader_uid": "7",
      "uploader_name": "成员甲",
      "uploaded_by_me": true,
      "can_delete": true,
      "updated_at": "2026-08-12T06:00:00Z"
    }
  ]
}
```

成果使用“可信成员直接发布”模型：

- Agent 所有者和已添加该 Agent 的成员都可直接发布，不进入待审核状态。
- Agent 所有者可下架任意成员发布的成果。
- 普通成员只能下架自己发布的成果。
- 回收站及恢复操作暂仅向 Agent 所有者开放。
- 旧成果缺少 `uploader_uid` 时，普通成员不能管理，避免错误授权。
- 只有成果管理服务返回 `can_publish: true` 且 `publish_mode: "immediate"` 时，客户端才展示发布入口。

成果来源统一使用 `creator_type`、`creator_uid` 和 `creator_name`：

- `creator_type: "user"` 表示成员上传，界面显示“我上传”或成员名称。
- `creator_type: "agent"` 表示 Agent 生成并发布，界面显示具体 Agent 名称和“生成”。
- `creator_type: "unknown"` 表示历史记录无法证明创建者，界面显示“来源未知”，不得自动归属给 Agent 所有者。
- `uploader_uid`、`uploader_name` 暂时保留用于成员下架权限和旧版成果服务兼容；CatsCo 会把它们规范化为用户创建者信息。

#### POST /api/agents/{agentUid}/artifacts — 直接发布 Agent 成果

发布一个已上传到当前 CatsCo 实例的 HTML 网页或 ZIP 小应用。`url` 必须与当前 API 请求同源并位于 `/uploads/files/` 下，避免把任意外部地址注册为可信成果。CatsCo 根据 Bearer 登录态注入 `actor_uid`、`creator_type: "user"`、`creator_uid`、`uploader_uid` 和 `publish_mode: "immediate"`；客户端不得提交或覆盖这些身份字段。

成员上传默认无需审批并直接展示。若 Agent 所有者关闭了成员上传权限，普通成员调用此接口返回 `403`；所有者不受该开关影响。

```json
{
  "title": "课堂网页",
  "kind": "html",
  "url": "https://catsco.example/uploads/files/result.html",
  "source_title": "课堂任务",
  "source_topic_id": "p2p_7_440"
}
```

成功返回 `201`，成果状态立即为 `active`：

```json
{
  "ok": true,
  "artifact": {
    "id": "member-result",
    "title": "课堂网页",
    "kind": "html",
    "url": "https://artifacts.example.com/by-agent/440/member-result/latest/",
    "status": "active",
    "agent_uid": "440",
    "creator_type": "user",
    "creator_uid": "7",
    "creator_name": "成员甲",
    "uploader_uid": "7",
    "uploader_name": "成员甲",
    "uploaded_by_me": true,
    "can_delete": true,
    "created_at": "2026-08-12T07:00:00Z",
    "updated_at": "2026-08-12T07:00:00Z"
  }
}
```

成果管理服务需要负责导入或注册 CatsCo 上传 URL，并返回成果节点下的最终 URL。未实现该能力时应在列表中省略 `can_publish`，CatsCo 前端不会显示失效入口。

#### DELETE /api/agents/{agentUid}/artifacts/{artifactId} — 下架成果

Agent 所有者可以下架任意活跃成果；普通成员仅可下架 `uploader_uid` 与当前登录用户一致的成果。服务端会再次读取并验证成果归属，不依赖客户端的 `can_delete` 字段。

#### 2.8.2 管理员 Bot 管理（需要管理员鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/admin/bots` | 列出所有 Bot |
| POST | `/api/admin/bots/register` | 注册新 Bot |
| POST | `/api/admin/bots/toggle?uid={uid}` | 启用/禁用 Bot |
| POST | `/api/admin/bots/rotate-key?uid={uid}` | 轮换 API Key |
| GET | `/api/admin/bots/stats?uid={uid}` | Bot 统计 |
| GET | `/api/admin/bots/debug?uid={uid}&limit={n}` | Bot 调试日志 |

#### POST /api/admin/bots/register — 管理员注册 Bot

```json
// Request
{
  "username": "opencrawl_bot",
  "password": "bot_password",
  "display_name": "OpenCrawl Bot",
  "model": "custom",
  "api_endpoint": "https://opencrawl.example.com/webhook"
}

// Response 200
{
  "uid": 10,
  "username": "opencrawl_bot",
  "type": "bot",
  "api_key": "cc_0a_a1b2c3d4e5f6..."
}
```

拿到 `api_key` 后，Bot 即可通过 WebSocket 或 REST API 接入。

---

## 3. WebSocket 协议

### 3.1 连接

```
wss://app.catsco.cc/v0/channels?api_key=<api_key>
```

用户连接使用 `?token=<jwt>`，Bot 连接使用 `?api_key=<key>`。

### 3.2 握手

连接建立后，客户端必须发送握手消息：

```json
// Client → Server
{ "hi": { "id": "1", "ver": "0.1.0" } }

// Server → Client (成功)
{ "ctrl": { "id": "1", "code": 200, "text": "ok", "params": { "uid": "10", "build": "catscompany" } } }
```

收到 `code: 200` 且 `build: "catscompany"` 即握手成功。

### 3.3 消息类型 — Client → Server

#### 发送消息 (pub)

```json
{
  "pub": {
    "id": "2",
    "topic": "p2p_1_10",
    "content": "Hello from bot!",
    "reply_to": 5
  }
}
```

- `topic`: P2P 格式 `p2p_{小uid}_{大uid}`，群组格式 `grp_{groupId}`
- `content`: 字符串（纯文本）或富媒体对象（见 3.5）
- `reply_to`: 可选，引用的消息 seq

#### 拉取历史 (get)

```json
{ "get": { "id": "3", "topic": "p2p_1_10", "what": "history", "seq": 0 } }
```

#### 输入中提示 (note)

```json
{ "note": { "topic": "p2p_1_10", "what": "kp" } }
```

#### 已读回执 (note)

```json
{ "note": { "topic": "p2p_1_10", "what": "read", "seq": 42 } }
```

#### 取消群聊 Agent 流 (pub / stream_cancel)

群聊中的客户端可用 `stream_cancel` 请求停止一个正在执行的 Agent：

```json
{
  "pub": {
    "id": "cancel-1",
    "topic": "grp_5",
    "type": "stream_cancel",
    "metadata": {
      "stream_id": "run-42",
      "stream_event": "cancel",
      "target_bot_uid": 42
    }
  }
}
```

- `stream_id` 必填，用于关联本次取消事件。
- `target_bot_uid` 是目标 Agent 的数字 UID。多人群聊必须显式提供，且目标必须是该群的 Bot 成员；只有“一个人类成员 + 一个 Bot”的双人群可省略，服务端会推断唯一 Bot。
- 发起者必须是未被禁言的群成员。在多人群聊中，还必须是当前目标 Agent 活跃轮次的发起者；普通群成员、其他轮次的发起者及 Bot 都不能中止该轮次。
- 成功时服务端返回 `ctrl.code: 200`，并仅把取消事件投递给目标 Agent 和群内人类观察者，不会中止同群其他 Agent。
- 权限或目标校验失败统一返回 `ctrl.code: 403`。包括：发起者不是群成员或已被禁言、缺少/伪造 `target_bot_uid`、目标不是本群 Agent、发起者不是当前轮次发起者，或 Bot 尝试中止其他 Agent。`403` 表示取消未被执行，客户端不得将其当作已停止。

XiaoBa-CLI 等 Agent runtime 在多人群中接收、处理或转发取消事件时，必须保留 `target_bot_uid`；不能仅依赖 `stream_id` 推断目标 Agent。

### 3.4 消息类型 — Server → Client

#### 控制消息 (ctrl)

```json
{ "ctrl": { "id": "2", "code": 200, "text": "ok", "params": { "seq": 43 } } }
```

常见 code：`200` 成功，`404` 未找到，`429` 限流，`500` 服务器错误。

#### 数据消息 (data)

```json
{
  "data": {
    "topic": "p2p_1_10",
    "from": "1",
    "seq": 43,
    "content": "Hello!",
    "reply_to": 5
  }
}
```

#### 在线状态 (pres)

```json
{ "pres": { "topic": "me", "what": "on", "src": "2" } }
```

`what` 值：`on`（上线）、`off`（下线）、`msg`（新消息）、`upd`（更新）

#### 通知 (info)

```json
// 输入中
{ "info": { "topic": "p2p_1_10", "from": "1", "what": "kp" } }

// 已读
{ "info": { "topic": "p2p_1_10", "from": "1", "what": "read", "seq": 42 } }
```

### 3.5 富媒体消息格式

`content` 字段除了纯文本字符串外，还支持以下结构化类型：

#### 图片

```json
{
  "type": "image",
  "payload": {
    "url": "/uploads/2026/02/abc.png",
    "name": "photo.png",
    "size": 102400,
    "width": 800,
    "height": 600
  }
}
```

#### 文件

```json
{
  "type": "file",
  "payload": {
    "url": "/uploads/2026/02/doc.pdf",
    "name": "report.pdf",
    "size": 204800,
    "mime_type": "application/pdf"
  }
}
```

#### 链接预览

```json
{
  "type": "link_preview",
  "payload": {
    "url": "https://example.com/article",
    "title": "Article Title",
    "description": "A brief summary...",
    "image_url": "https://example.com/thumb.jpg"
  }
}
```

#### 卡片

```json
{
  "type": "card",
  "payload": {
    "title": "Task Complete",
    "description": "Crawl finished: 128 pages",
    "image_url": "/uploads/preview.png",
    "actions": [
      { "label": "View Report", "url": "https://example.com/report" },
      { "label": "Re-run", "action": "rerun_crawl" }
    ]
  }
}
```

### 3.6 Topic 命名规则

| 类型 | 格式 | 示例 | 说明 |
|------|------|------|------|
| P2P | `p2p_{小uid}_{大uid}` | `p2p_1_10` | UID 排序，确定性生成 |
| 群组 | `grp_{groupId}` | `grp_5` | 创建群组时获得 |
| 系统 | `me` | `me` | 在线状态通知 |

### 3.7 群聊 Bot 行为

- 群聊中 Bot 默认只在被 `@` 时收到消息
- P2P 中 Bot 收到所有消息
- Bot 之间互相不投递消息（防止循环）

---

## 4. Bot SDK 快速接入

### 4.1 接入流程

```
1. 用户创建 Bot（POST /api/bots）或管理员注册 Bot → 获得 api_key
2. 用户添加 Bot 为好友
3. Bot 通过 WebSocket 连接 → 握手 → 收发消息
```

### 4.2 TypeScript SDK

```bash
npm install @catscompany/bot-sdk
```

#### SDK 方法参考

| 方法 | 说明 |
|------|------|
| `new CatsBot(config)` | 初始化，传入 `serverUrl`、`apiKey`，可选 `connectTimeout` / `handshakeTimeout` |
| `connect()` | 建立 WebSocket 连接并完成握手 |
| `run()` | connect + 保持连接（含自动重连） |
| `disconnect()` | 断开连接 |
| `sendMessage(topic, content, replyTo?)` | 发送消息，返回 seq |
| `sendTaskStatus(topic, status)` | 发布带 `run_id` 的任务状态，用于标记 agent turn 生命周期 |
| `sendImage(topic, upload, opts?)` | 发送图片（需先 upload） |
| `sendFile(topic, upload, mimeType?)` | 发送文件（需先 upload） |
| `sendLinkPreview(topic, payload)` | 发送链接预览卡片 |
| `sendCard(topic, payload)` | 发送富卡片 |
| `sendTyping(topic)` | 发送输入中提示 |
| `sendReadReceipt(topic, seq)` | 发送已读回执 |
| `getHistory(topic, sinceSeq?)` | 拉取消息历史 |
| `uploadFile(filePath, type?)` | 从磁盘上传文件 |
| `uploadBuffer(buffer, filename, type?)` | 从 Buffer 上传 |
| `on(event, listener)` | 监听事件 |

#### 事件列表

| 事件 | 回调参数 | 说明 |
|------|----------|------|
| `ready` | `(uid: string)` | 连接成功，握手完成 |
| `message` | `(ctx: MessageContext)` | 收到消息 |
| `ctrl` | `(ctrl: MsgServerCtrl)` | 收到控制消息 |
| `disconnect` | `(code, reason)` | 连接断开 |
| `reconnecting` | `(attempt: number)` | 正在重连 |
| `error` | `(err: Error)` | 连接错误 |

#### MessageContext 属性和方法

| 属性/方法 | 说明 |
|-----------|------|
| `text` | 提取纯文本内容 |
| `topic` | 消息所在 topic |
| `from` | 发送者 uid |
| `seq` | 消息序号 |
| `content` | 原始 content（string 或 rich object） |
| `isP2P` | 是否 P2P 消息 |
| `isGroup` | 是否群组消息 |
| `reply(content)` | 回复到同一 topic |
| `replyWithTyping(content, delay?)` | 先 typing 再回复 |
| `sendTyping()` | 发送输入中提示 |
| `markRead()` | 标记已读 |

```typescript
import { CatsBot } from '@catscompany/bot-sdk';

const bot = new CatsBot({
  serverUrl: 'wss://app.catsco.cc/v0/channels',
  apiKey: 'cc_0a_your_api_key_here',
  connectTimeout: 15000,
  handshakeTimeout: 10000,
});

bot.on('ready', (uid) => {
  console.log(`Bot online, uid: ${uid}`);
});

bot.on('message', async (ctx) => {
  console.log(`[${ctx.topic}] ${ctx.from}: ${ctx.text}`);

  // 回复消息
  await ctx.reply('Got it!');

  // 带 typing 效果的回复
  await ctx.replyWithTyping('Processing...');

  // 发送卡片
  await bot.sendCard(ctx.topic, {
    title: 'Crawl Result',
    description: '128 pages crawled',
    actions: [{ label: 'View', url: 'https://example.com' }],
  });
});

bot.run();
```

说明：

- 外部 Bot 建议统一连接 nginx 暴露的入口：`wss://app.catsco.cc/v0/channels`
- 需要 Web Push 的 agent 应在开始生成前发布同一 `run_id` 的 `running`，发送完最终用户可见消息后再发布终态；服务端会在匹配终态到达后为该 turn 通知一次。
- `connectTimeout` 控制 TCP/WebSocket 建连阶段超时
- `handshakeTimeout` 控制 `hi -> ctrl` 握手阶段超时
- 如果升级请求在握手前被 HTTP 拒绝，SDK 会直接抛出带 `statusCode` 的 `HandshakeError`

### 4.3 Go SDK

```go
package main

import (
    "fmt"
    bot "github.com/catscompany/bot-sdk-go"
)

func main() {
    b := bot.New(bot.Config{
        ServerURL: "wss://app.catsco.cc/v0/channels",
        APIKey:    "cc_0a_your_api_key_here",
    })

    b.OnReady(func(uid string) {
        fmt.Println("Bot online, uid:", uid)
    })

    b.OnMessage(func(ctx *bot.Context) {
        fmt.Printf("[%s] %s: %s\n", ctx.Topic, ctx.From, ctx.Text())
        ctx.Reply("Got it!")
    })

    b.Connect()
}
```

### 4.4 不使用 SDK — 裸 WebSocket 接入

如果你的语言没有 SDK，可以直接用 WebSocket 接入。以下是 Python 示例：

```python
import json
import websocket

API_KEY = "cc_0a_your_api_key_here"
WS_URL = f"wss://app.catsco.cc/v0/channels?api_key={API_KEY}"

ws = websocket.create_connection(WS_URL)

# 1. 握手
ws.send(json.dumps({"hi": {"id": "1", "ver": "0.1.0"}}))
resp = json.loads(ws.recv())
assert resp["ctrl"]["code"] == 200
uid = resp["ctrl"]["params"]["uid"]
print(f"Connected as uid={uid}")

# 2. 收消息循环
while True:
    msg = json.loads(ws.recv())
    if "data" in msg:
        topic = msg["data"]["topic"]
        text = msg["data"]["content"]
        # 3. 回复
        ws.send(json.dumps({
            "pub": {"id": "2", "topic": topic, "content": f"Echo: {text}"}
        }))
```

---

## 5. 第三方接入指南（如 OpenCrawl）

### 5.1 接入步骤

```
步骤 1: 联系管理员注册 Bot 账号
        → 管理员调用 POST /api/admin/bots/register
        → 你拿到 api_key

步骤 2: 用户添加你的 Bot 为好友
        → 用户在 App/Web 中搜索你的 Bot username 并添加

步骤 3: 你的服务连接 WebSocket
        → wss://app.catsco.cc/v0/channels?api_key=<your_key>
        → 完成握手

步骤 4: 收发消息
        → 监听 data 消息，处理用户请求
        → 通过 pub 回复结果
```

### 5.2 Bot 能力清单

作为 Bot 接入后，你可以：

| 能力 | 方式 | 说明 |
|------|------|------|
| 收发文本消息 | WebSocket pub/data | 核心能力 |
| 发送图片 | 先 POST /api/upload，再 pub | 需要先上传 |
| 发送文件 | 先 POST /api/upload，再 pub | 需要先上传 |
| 发送链接预览 | WebSocket pub | 直接发送 |
| 发送卡片消息 | WebSocket pub | 直接发送 |
| 输入中提示 | WebSocket note | 提升体验 |
| 已读回执 | WebSocket note | 提升体验 |
| 拉取历史消息 | WebSocket get | 上下文恢复 |

### 5.3 Bot 限制

- Bot 有发送频率限制（rate limit），超限返回 `429`
- Bot 之间不互相投递消息
- 群聊中 Bot 只在被 `@` 时收到消息
- Bot 不能调用好友管理、群组管理等用户接口

---

## 6. 错误码

| Code | 含义 |
|------|------|
| 200 | 成功 |
| 400 | 请求参数错误 |
| 401 | 未认证 / Token 无效 |
| 403 | 无权限 |
| 404 | 资源不存在 |
| 409 | 冲突（如用户名已存在） |
| 429 | 请求过于频繁（限流） |
| 500 | 服务器内部错误 |
