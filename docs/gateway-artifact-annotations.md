# Gateway 应用标注 · 设计与契约（catsco.gateway-annotations.v1）

> 状态：已实现。测试与构建验证通过；真实浏览器已验证跨域 SDK 握手和标注模式，iframe 内选择到发送的完整浏览器流程仍需补充验收。
> 范围：gateway-only 应用（无平台任务 registry 版本依赖）上的用户标注闭环：SDK 捕获 → 宿主 composer 草稿 → 消息 metadata → 服务端校验持久化 → agent 可读上下文 → 历史回看。
> 相关文档：SDK 与宿主桥接见 [gateway-annotation-sdk.md](gateway-annotation-sdk.md)；服务端授权与投递见 [gateway-annotation-backend.md](gateway-annotation-backend.md)；现有 task-host 兼容行为见 [gateway-artifact-task-compatibility.md](gateway-artifact-task-compatibility.md)。

---

## 1. 身份模型

- **应用身份** = 服务端验证的 `agent_uid + app_id`。`agent_uid` 由服务端从 topic 解析（`artifactAgentForTopic`），**绝不信任客户端/frame 里的 claims**；`app_id` 由 gateway 注册表交叉验证 `Agent == agentUID`。不存在 registry `publish_version` 依赖——gateway-only 应用（无 registry 记录）与 registry 应用共用同一标注链路。
- **宿主 frame 绑定**（messages-view）来源是 panel 侧传入的 `annotationApp:{appId,title}` + 云面板 `cloudArtifactsAgentUID`；iframe 内事件里的任何身份字段一律被宿主忽略（宿主桥接模块四重校验：exact origin / event.source / contract_version / session token）。
- **会话/文档标识**：宿主每次（重）连接铸造不可预测 session token 并注入 iframe；iframe 导航/刷新（pushState/replaceState/popstate/hashchange/pagehide，由 SDK 监听）使旧选择态失效并上报 `page.v1`，旧目标不得贴到新文档。

## 2. 消息 metadata 契约 `gateway_annotations`

```json
{
  "contract_version": "catsco.gateway-annotations.v1",
  "agent_uid": 7,
  "app_id": "saturday-demo",
  "page": { "path": "/board", "revision": "r7" },
  "annotations": [{
    "id": "a1", "kind": "element|text|region",
    "label": "可选短标签",
    "body": "写给 Agent 的批注（必填）",
    "target": {
      "element_id?": "submit-btn", "selector?": "button#submit-btn",
      "text?": "选中文本", "prefix?": "…", "suffix?": "…",
      "rect?": { "x": 0.1, "y": 0.2, "width": 0.3, "height": 0.1 },
      "coordinate_space?": "viewport",
      "viewport?": { "width": 390, "height": 700, "scroll_x": 0, "scroll_y": 120 }
    }
  }]
}
```

边界（客户端 drafts util、SDK normalizer、服务端三处同镜像校验，任一处拒绝即上/下行拒绝）：

- 最多 20 条，总量 ≤16KiB UTF-8；body ≤2000 字符、label ≤256、text ≤2000、prefix/suffix ≤256；id ≤128（且唯一）、selector ≤512、path ≤1024（拒绝 query/fragment/控制符）、revision ≤128。
- `target` 语义：element 至少 `element_id` 或 `selector`；text 至少 `text`；region 必须 `rect + coordinate_space:'viewport' + viewport` 证据。rect 为 viewport 归一化 0..1 且 `x+width ≤ 1`、`y+height ≤ 1`；element/text 可附 rect 辅助，但不作为稳定跨版本锚点。
- **revision 由 SDK 可选提供（如 `setRevision('r7')`），任何一层不得杜撰版本号**；无 revision 则字段省略。
- 后端语义：human 发送者 + 服务端 agent/app 授权 → canonical 重编码落库（`SaveMessageWithMetadata`），bot/service 带键静默剥离，transient/runtime 消息 400；agent 在实时 WS 收到与持久化一致的结构化标注 + `catsco_gateway_annotation_context` 可读摘要块（fanout-only，不入库）。普通无键消息零感知。

## 3. SDK ↔ 宿主桥（bridge）契约

消息（exact-origin + source + contract + session 四重校验，见上述）：

| 方向 | type | 载荷 |
| --- | --- | --- |
| 宿主 → frame | `catsco.gateway.annotation.connect.v1` | `session_id`, `request_id` |
| SDK → 宿主 | `catsco.gateway.annotation.ready.v1` | `capabilities:['element','text','region']`, `page` |
| 宿主 → frame | `catsco.gateway.annotation.mode.v1` | `mode: 'off'\|'element'\|'text'\|'region'` |
| SDK → 宿主 | `catsco.gateway.annotation.target.v1` | `page`, `selection:{id,kind,label,target}`（body 由宿主 UI 收集） |
| SDK → 宿主 | `catsco.gateway.annotation.page.v1` | `page`（导航/revision 变化通知） |

共享 API（`webapp/src/gateway-annotations.js`）：
`GATEWAY_ANNOTATIONS_CONTRACT`、`GATEWAY_ANNOTATION_BRIDGE_CONTRACT`、`normalizeGatewayAnnotationSelection`、`normalizeGatewayAnnotations`、`createGatewayAnnotationHost({getBinding,onReady,onSelection,onPageChange,onUnavailable})`；宿主 `getBinding() → {frame,url,agentUid,appId,signal?}`；`connect/setMode/handleWindowMessage/deactivate/dispose`，宿主每次 load 换 session_id。

应用侧 SDK（`webapp/public/catsco-annotations.js`）：
`window.CatsCoAnnotations.create({parentOrigin, revision?, getElementId?}) → {dispose, setRevision}`。3 行接入；敏感控件（password/token/secret 等类型与名字规则、contenteditable、`data-catsco-annotation-sensitive`）不可捕、不读 value；region 凭拖拽归一化 rect + viewport 证据。SDK 永不代发聊天。

## 4. 宿主 UI 行为（本模块实现）

- **入口**：侧栏「应用」tab → 打开应用 → viewer 工具栏标注（元素/文本/区域/取消）；仅当存在 `topicId` 且 agentUid>0 时渲染；工具栏状态只由宿主 host-ack 后翻转。
- **草稿**：composer 上方标注条，`key = topicId|agentUid|appId`，sessionStorage 用户隔离（`catsco_gateway_annotation_drafts:v1:<uid>`）；捕获后内联编辑（必填/超长/Escape 取消）；逐条改删、全部清除。
- **发送**：`metadata.gateway_annotations` 注入文本消息 payload；成功后清空该 bucket；失败（网络/服务端 400）整份恢复（与附件/正文同一 mutation-revision 保护域，防止并发编辑冲突）；page 漂移 ↔ 旧草稿共存时阻断新增捕捉并提示用户清理，annotation 从不贴到新页面。
- **回看**：历史消息按 contract normalizer 渲染卡片（app/page/revision/逐条 kind/label/body/target 摘要），非法或异版 metadata 整体丢弃不渲染。
- **旧 task-host 兼容**：`metadataError`（registry 缺失/版本缺失）只影响任务提交能力并如实提示「浏览与标注不受影响」；task 提交路径与标注路径互不依赖（`handleGatewayArtifactFrameChange` 内 `artifact` 判空返回原语义保留，`annotationApp` 分支独立）。

## 5. 本地联调与验收

`scripts/local-gateway-annotations-demo.mjs`（平台镜像 6063 + 本地 gateway 6066 + fixture app + 一次性 launch code + 发送记录）+ `local-gateway-annotations-fixture.html`（含敏感输入框对照样例）。运行方式见脚本头部注释。自动化验证可执行 `cd webapp && pnpm test && pnpm run build`，以及仓库根目录的 `go test ./server/...`。浏览器验收需要在跨域 fixture 中实际选择元素、文本和区域，添加评论并发送，核对消息 metadata 和回看卡片。

## 6. 已知限制（首版取舍）

1. text 捕获依赖 mouseup（键盘选词/双击不覆盖）。
2. OOPI iframe 的跨端自动化点击在 opencli 下不可用（人工可操作）；SDK↔宿主互操作以 44 用例忠实仿真覆盖。
3. 服务端 app 归属校验在每次带键消息同步查一次 gateway（低频可接受，无 singleflight）。
4. region 标注的 rect 仅 viewport 相对，滚动/缩放后仅作证据；旧目标不追溯改写。
