# Gateway 应用批注：选区旁直接评论

本轮按用户新 UX 实施：应用 viewer 的刷新按钮旁仅一个 **批注应用** icon button。点击开启，点击元素或拖动画框，选区旁显示父页面 React 评论框；填写评论后直接发送至应用打开时绑定的原会话。无需在聊天 composer 添加批注或输入第二段正文。

## 交互与实际发送信息

- 单模式 `select`，kind 是机器字段（element/text/region），不向用户展示模式选择。SDK 的 click 与 >6px drag 区分 element/region；不声称支持触屏或自动理解 canvas 内容。
- Popover 显示目标摘要、评论 textarea、应用视口与选区及周边两张截图预览，以及发送/取消。截图失败可重新捕获，或明确选择「仅发送评论和定位，不含截图」。Enter 换行，Ctrl/Cmd+Enter 发送；Esc 取消/退出。正在捕获或发送时禁止提交。选区和批注模式保留至发送成功、取消或 Esc，避免提前退出作废截图证书。
- SDK 报告 iframe viewport 归一化 rect；父页面用实际 iframe boundingRect 定位并 clamp 屏幕边缘。选区 parent outline 不拦截指针。无 bbox 时可在 iframe 内侧角落 fallback，select host 正常要求合法 bbox。
- 父页面 resize 重定位、parent scroll 取消；iframe load、page/navigation/invalidation、app close、auth change 取消旧选区。跨域 iframe 内部滚动/resize 依赖 SDK page/invalidation 通知，UI 不读取 app DOM。
- 界面说明：发送评论、应用、页面、所选目标位置或文本，并默认附同一次捕获生成的两张 JPEG（带目标红框的应用可见视口、含 16 CSSpx 周边的选区裁图），每图不超过 2 MiB、每边不超过 2048。图片先由父页面上传，再以服务端校验的 full→crop `image` content blocks 发送；二进制不进入 `gateway_annotations` 或草稿 storage。只截图应用，不包含聊天或其他窗口，不上传完整 DOM。敏感控件在克隆中遮挡；跨域图片、背景、视频、嵌套内容或 WebGL 可能缺失，界面说明限制。

## 冻结 API 与原会话路由

使用既有 `api.sendArtifactAnnotations`，无新 endpoint。下例是明确选择仅发送评论和定位时的文本请求；默认带截图时还附 `content_blocks`（评论 text + full image + crop image），图片 schema 和上传校验见 [Gateway 标注协议](gateway-artifact-annotations.md)：

```json
{
  "open_ref": "<parent-only original binding>",
  "client_msg_id": "ga_<uuid>",
  "content": "这条评论本身",
  "gateway_annotations": {
    "contract_version": "catsco.gateway-annotations.v1",
    "agent_uid": 9,
    "app_id": "board",
    "page": {"path": "/board", "revision": "optional"},
    "annotations": [{
      "id": "selection-id",
      "kind": "element",
      "label": "SDK target label or empty",
      "body": "这条评论本身",
      "target": {"element_id":"submit-btn", "rect":{"x":0.2,"y":0.3,"width":0.1,"height":0.08},"coordinate_space":"viewport","viewport":{"width":800,"height":600,"scroll_x":0,"scroll_y":0}}
    }]
  }
}
```

content 是非空普通 string；comment 即正文。选区、page、binding、document session、auth revision/token 在父页面捕获，点击发送瞬间复核 open_ref/topic/app/agent、TTL、signal、active frame、host document token/page、当前 auth。失败保留输入，显示错误；完全相同 capture/comment 请求重试复用 client_msg_id，编辑后新 id。client id ≤128。成功关闭 editor 并 toast“已发送至原会话”，普通 server persistence/fanout/聊天历史与 Agent 回复仍沿原路径。

`open_ref` 绝不进入 iframe HTML、query、postMessage、annotation metadata、storage 或日志。新 inline capture 不写 composer 草稿条、persistent storage 或 chat capture editor。旧 saved draft 仍可恢复评论并清理，保留原历史兼容代码；旧 capability 不恢复，不能借新 open authority。

关闭与 pending 请求仍由 server persistenceGate 决定：revoke先完成则未保存请求被拒绝，保存先完成则只在原 topic 成功。迟到响应不把批注写入另一个 composer。token refresh 即使同UID必须重新打开；restart/otherreplica同样 failclosed。无普通 send/WS fallback。

## 文件与分工

UI：cloud-artifacts-panel、messages-view、popover CSS、对应测试及本主文档。SDK/sharedhost/public source 由 D 拥有，gateway vendored资源由 C export更新。UI调用 `host.setMode('select')`、消费onSelection(selection,page)以及iframe Escape的onModeChange('off')。服务端协议原样保留。

实际用户流程与本地 demo 见 [原会话绑定说明](gateway-open-binding.md)，真实 Go handlers 联调见 [服务端 fixture 说明](gateway-open-binding-server.md) 与 [截图 fixture](../server/testdata/README-artifact-screenshot-fixture.md)。UI mock demo 的 payload 记录不代表权限 E2E。核心浏览器验收使用实际 React → 真实 Gateway/Nginx capture → bound Go handlers 保存原 topic → deterministic bot 正常回执；不调用真实 LLM。
