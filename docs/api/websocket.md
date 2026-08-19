# CatsCompany WebSocket 协议

## 连接

**端点：** `ws://your-server:6061/v0/channels`

**认证方式：**
- JWT Token: `?token=<jwt_token>`
- API Key (Bot): Header `X-API-Key: <api_key>`

> 兼容说明：服务端仍兼容 `?api_key=<api_key>`，但不建议在新代码里使用。
> URL 可能进入浏览器历史、代理日志或监控日志，Bot SDK 默认使用 Header 传递。

## 消息格式

所有消息都是 JSON 格式。

### 客户端 → 服务器

#### 1. 握手 (hi)
```json
{
  "hi": {
    "id": "1",
    "ver": "0.1.0"
  }
}
```

#### 2. 发送消息 (pub)
```json
{
  "pub": {
    "id": "2",
    "topic": "p2p_3_5",
    "content": "Hello!",
    "reply_to": 123
  }
}
```

**content 支持：**
- 纯文本: `"Hello"`
- 富文本: `{"type": "image", "payload": {...}}`

##### 取消群聊 Agent 流 (stream_cancel)

`stream_cancel` 是 `pub` 的控制型变体，用于停止群聊中一个正在执行的 Agent：

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

- `metadata.stream_id` 必填。
- `metadata.target_bot_uid` 是目标 Agent 的数字 UID。多人群聊必须提供，且目标必须是本群 Bot；仅在群里恰好只有一个人类成员和一个 Bot 时可省略，由服务端推断唯一 Bot。
- 发起者必须是未被禁言的群成员。多人群聊还要求发起者是目标 Agent 当前活跃轮次的发起者；其他成员、其他轮次发起者和 Bot 无权中止该轮次。
- 成功返回 `ctrl.code: 200`，取消事件只会发给目标 Agent 和群内人类观察者，不会中止同群其他 Agent。
- 以下情况返回 `ctrl.code: 403`，且不会执行取消：非群成员或已被禁言；缺少、伪造或指向非本群 Agent 的 `target_bot_uid`；非当前轮次发起者；Bot 尝试中止其他 Agent。客户端收到 `403` 后不得显示为“已停止”。

XiaoBa-CLI 等 Agent runtime 在多人群中接收、处理或转发取消事件时，必须保留 `target_bot_uid`，不能只根据 `stream_id` 推断目标 Agent。

#### 3. 订阅 (sub)
```json
{
  "sub": {
    "id": "3",
    "topic": "p2p_3_5"
  }
}
```

#### 4. 获取历史 (get)
```json
{
  "get": {
    "id": "4",
    "topic": "p2p_3_5",
    "what": "history",
    "seq": 100
  }
}
```

#### 5. 通知 (note)
```json
{
  "note": {
    "topic": "p2p_3_5",
    "what": "kp",
    "seq": 123
  }
}
```

**what 类型：**
- `kp`: 正在输入
- `read`: 已读回执
- `visibility`: 浏览器页面可见性同步；此时省略 `topic`，并携带 `visibility: "visible"` 或 `"hidden"`。服务端据此决定是否需要额外发送 Web Push，不会把该状态转发给其他用户。

#### 6. 设备 RPC (device_rpc)

`device_rpc` 用于 bot 将被授权的工具请求路由到用户当前选定的本地设备。服务端只接受 bot 连接发起的 `request`，并要求请求绑定有效的 `grant_id`、会话、用户、设备和 operation。

当前 Device RPC operation：
- 普通文件任务：`read_file`、`resolve_common_directory`、`glob`、`grep`、`write_file`、`edit_file`
- 高风险命令任务：`execute_shell`

`execute_shell` 只有在目标设备声明了该 capability、服务端为当前会话下发的 grant 包含 `execute_shell`、并且请求通过 Device RPC grant 校验时才会被转发。服务端会记录设备审计事件，包括操作者、agent、目标设备、session、operation、tool、阶段、结果；`execute_shell` 还会记录本次 shell 命令文本。

### 服务器 → 客户端

#### 1. 控制消息 (ctrl)
```json
{
  "ctrl": {
    "id": "1",
    "code": 200,
    "text": "ok",
    "params": {
      "uid": "usr3",
      "name": "张三"
    }
  }
}
```

#### 2. 数据消息 (data)
```json
{
  "data": {
    "topic": "p2p_3_5",
    "from": "usr5",
    "seq": 456,
    "content": "Hi there!",
    "reply_to": 123
  }
}
```

#### 3. 在线状态 (pres)
```json
{
  "pres": {
    "topic": "me",
    "what": "on",
    "src": "usr5"
  }
}
```

#### 4. 信息通知 (info)
```json
{
  "info": {
    "topic": "p2p_3_5",
    "from": "usr5",
    "what": "kp"
  }
}
```

#### 5. 应用通知 (notification)

成员成功把成果共享到 Agent 云端后，Agent 所有者的在线客户端会收到：

```json
{
  "notification": {
    "type": "cloud_artifact_shared",
    "message": "有新文件在云端共享"
  }
}
```

该事件只表示共享已完成，不包含成果标题、文件 URL、任务名、上传者或其他成果详情。

## Topic 格式

- **P2P:** `p2p_{smaller_uid}_{larger_uid}`
- **群组:** `grp_{group_id}`

## 错误码

- `200`: 成功
- `400`: 请求错误
- `401`: 未授权
- `403`: 禁止访问
- `429`: 频率限制
- `500`: 服务器错误
