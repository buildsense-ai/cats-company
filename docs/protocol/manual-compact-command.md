# XiaoBa `/compact` 传输约定

CatsCompany 对 `/compact` 保持透明，不解析、不新增专用 HTTP API，也不把它和 `/clear` 的服务端清理逻辑绑定。

## 链路

```text
用户发送 /compact
        ↓
CatsCompany 按普通文本消息转发
        ↓
XiaoBa AgentSession.handleCommand("compact")
        ↓
XiaoBa 使用 checkpoint 压缩并回复结果
```

压缩过程中的 Working 状态复用现有 `thinking` 消息类型，例如“正在压缩上下文……”。压缩完成或失败使用现有普通机器人回复返回结果。

因此 CatsCompany 不需要理解压缩、保存 checkpoint 或管理会话锁；这些职责全部由 XiaoBa 持有。这里的协议测试只保证 `/compact` 文本和 `thinking` 状态不会被消息规范化层改写，避免未来的消息协议清理误伤该命令。

