# Gateway Artifact 标注 — 应用侧 SDK 与宿主桥接

> 负责范围：`webapp/src/gateway-annotations.js`（共享 normalizer + 宿主 bridge）、
> `webapp/public/catsco-annotations.js`（浏览器 SDK）、两者针对性测试。
> 宿主 UI 与端到端集成见 [gateway-artifact-annotations.md](gateway-artifact-annotations.md)；
> 服务端契约见 [gateway-annotation-backend.md](gateway-annotation-backend.md)。

## 1. 架构

gateway 应用运行在宿主页面的跨域 `<iframe>` 里。标注链路只有一跳 postMessage：

```
宿主页面 (CatsCo webapp)                      gateway 应用 iframe
──────────────────────────                    ─────────────────────────
createGatewayAnnotationHost(...)              <script src="/catsco-annotations.js">
  host.connect(binding)   ── connect.v1 ──▶   SDK 记录 session_id
                          ◀── ready.v1 ────   上报 capabilities + page
  host.setMode('element') ── mode.v1 ─────▶   进入显式选择态（overlay/监听）
                          ◀── target.v1 ───   用户完成一次选择（id/kind/label/target）
                          ◀── page.v1 ─────   SPA 导航 / revision 变化
  composer 添加评论 body → normalizeGatewayAnnotations → 消息 metadata.gateway_annotations
```

两个互不混淆的 contract：

| 常量 | 值 | 用途 |
| --- | --- | --- |
| `GATEWAY_ANNOTATIONS_CONTRACT` | `catsco.gateway-annotations.v1` | 消息 metadata（随正常消息发送、服务端持久化） |
| `GATEWAY_ANNOTATION_BRIDGE_CONTRACT` | `catsco.gateway-annotation-bridge.v1` | 宿主↔iframe 的 postMessage 协议 |

SDK **不**自行发消息/提交任务；标注只有在用户于 composer 明确添加并发送后，才以 metadata 附件进入消息与 agent 输入。

## 2. 宿主侧 API（`webapp/src/gateway-annotations.js`）

```js
import {
  GATEWAY_ANNOTATIONS_CONTRACT,        // 'catsco.gateway-annotations.v1'
  GATEWAY_ANNOTATION_BRIDGE_CONTRACT,  // 'catsco.gateway-annotation-bridge.v1'
  normalizeGatewayAnnotationSelection, // bridge 单条选择 → {id,kind,label,target} | null
  normalizeGatewayAnnotations,         // metadata 值 → canonical | null
  createGatewayAnnotationHost,         // 宿主桥
} from './gateway-annotations';
```

### normalizeGatewayAnnotationSelection(value)

单条标注/选择的 schema 门（SDK→host 的 `selection` 即此形状）。规则与
`server/gateway_annotations.go` 对齐：

- 严格键集合；未知键/`__proto__` 类污染键 → `null`。
- `id` 必填，≤128 字符，无控制字符；`kind` ∈ `element|text|region`；`label` ≤256 字符（可空）。
- target 按 kind：element 需 `element_id` 或 `selector`；text 需非空白 `text`；region 需 `rect` + `coordinate_space:'viewport'` + `viewport`。
- `rect` 视口归一化 0..1（允许 1e-6 浮点容差），零面积/越界拒绝；`viewport` 的 width/height >0、scroll ≥0、上限 2^20。
- 自由文本（`text/prefix/suffix/body`）≤2000/256/256/2000，允许换行与制表符（`\n`/`\r`/`\t`），其余控制字符拒绝——与 server freetext 规则一致；`element_id/selector` 等标识类拒绝一切控制字符。

### normalizeGatewayAnnotations(value)

完整 metadata 值的门（发送前与渲染历史消息时共用）。在单条规则之上：

- `contract_version` 必须等于 `GATEWAY_ANNOTATIONS_CONTRACT`；严格顶层键。
- `agent_uid` 正整数（≤2^53）；`app_id` 匹配 gateway 应用命名规则 `^[a-z][a-z0-9_-]{0,47}$`。
- `page.path` 以 `/` 开头，拒绝 `?`/`#`/控制字符，≤1024；`revision` 可选 ≤128（缺省不杜撰）。
- `annotations` 数组 ≤20 条、每条必须有非空白 `body`（≤2000）、id 唯一；数组可为空（等价“无标注”）。
- 整体 canonical JSON ≤16 KiB UTF-8。
- 返回 canonical 形状或 `null`；**失败即拒绝整份 metadata**（发送侧保留草稿，渲染侧视为无标注卡片）。

### createGatewayAnnotationHost({getBinding, onReady, onSelection, onPageChange, onUnavailable})

- `getBinding() -> {frame, url, agentUid, appId, signal} | null`：绑定由消费方（messages-view）持有；`frame` 是 iframe 元素、`url` 是帧当前 URL（精确 origin 的唯一来源）、`signal` 是可选 AbortSignal（abort 即撤销会话）。
- `host.connect(binding?)`：**每次显式 connect 都是新文档握手**——铸造全新 `session_id` + `request_id` 并发送 `connect.v1`，不复用旧 token；iframe onLoad 重载/重绑定必须再次 connect，旧会话全部在途状态立即作废（旧帧 replay 因 token 不符被拒）。post 到帧的**精确 origin**（由 `binding.url` 解析）。
- `host.setMode(mode) -> boolean`：`off|element|text|region`；无会话时先 lazy connect，再下发 `mode.v1`。ready 到达后会自动重发当前 mode。
- `host.handleWindowMessage(event)`：供消费方挂到 `window.addEventListener('message', ...)`。每条消息先对 `getBinding()` 现值复检（绑定消失/换帧/换 app → 撤销旧会话并拒收），再校验 `event.source === frame.contentWindow`、`event.origin === binding URL origin`、`contract_version`、`session_id`。任何一项不符直接忽略；`event.data` 里的身份字段一律不采信，app/agent 身份只来自 `getBinding`。
- `onReady({capabilities, page})`：仅接受**回显本次 connect `request_id`** 的 ready（旧 connect 的 ready 不能激活新会话）；重复 ready 不重复通知。capabilities 是 SDK 自报的 `element/text/region` 子集。
- `onSelection(selection, page)`：target 接受条件 = 已 ready、当前 mode 非且等于 selection.kind、kind ∈ ready capabilities、payload.page 与宿主当前 page 快照完全一致（path+revision）。漂移/缺失/非法 → `onUnavailable(binding, 'bad-page'|'page-drift'|'capability-mismatch')` 或静默丢弃，不把旧选择重新贴到新文档。
- `onPageChange(page)`：SDK 导航/`setRevision` 通知；**page 报告是完整快照替换**（revision 被丢弃就真的丢弃，不与旧值合并）。旧目标坐标语义随 page 变化失效——UI 应丢弃/标记旧草稿（`host.page` 暴露当前页）。
- `onUnavailable(binding, reason)`：`bad-capabilities`（session 同时撤销）、`bad-selection`/`bad-page`（协议违规，会话保留）、`rebind`/`binding-changed`/`binding-gone`（绑定变更/消失）、`deactivate`。
- `host.hasSession()` / `host.sessionToken` / `host.readyCapabilities()` / `host.page`：只读状态查询。
- `host.deactivate()`：撤销会话（帧内 SDK 保留 session token，但后续消息因无会话被忽略）。
- `host.dispose()`：永久失效（后续 connect/setMode 返回 false）。

## 3. 应用侧 SDK（`webapp/public/catsco-annotations.js`）

普通 `<script>`（非 module），暴露 `window.CatsCoAnnotations`：

```html
<script src="/catsco-annotations.js"></script>
<script>
  window.CatsCoAnnotations.create({
    parentOrigin: 'https://app.catsco.cc',   // 必填，精确 origin；'*' 与缺失直接拒绝创建
    revision: 'r7',                           // 可选，应用内容版本
    getElementId: (el) => el.dataset.appId,   // 可选，稳定 app element id 来源
  });
</script>
```

返回 `{ dispose(), setRevision(next), mode() }`（`setRevision` 在 page 实际变化时走与导航相同的文档失效路径：清空 hover/拖拽/选区态并重挂 mode 提示，旧 revision 下的拖拽不得以新 revision 发布；未变化时仅上报 page）：

- 自动响应宿主 `connect.v1`（校验 `event.origin === parentOrigin` 且 `event.source === window.parent`），回 `ready.v1`；每次新 connect 重绑到最新 session。
- `mode.v1` 切换显式选择态；`off` 完全恢复普通页面交互（overlay 移除、拦截监听不消费事件）。
- 选择捕获：
  - **element**：hover 高亮（真实 getBoundingClientRect），点击上报 `element_id`（优先 `getElementId` 回调 → `data-catsco-annotation-id` → DOM `id`）+ CSS selector（id 锚定、验证过的属性锚定，或 ≤6 层**同 tag** nth-of-type 链，≤512，含 rect/viewport 证据）。每段锚点与最终 selector 都以 `document.querySelector(selector) === 元素` 验证——转义/截断导致指向其他元素时丢弃该锚点或整条 selector（`element_id` 仍是锚），**绝不发布无法 match 的 selector**。
  - **text**：mouseup 时读取 `window.getSelection()`，上报 `text`（≤2000）+ `prefix/suffix`（≤256）+ rect 证据；发送后清除选区。**整段 Range 覆盖检查**：start/end 容器、共同祖先及 Range 实际触及的每个元素（`Range.intersectsNode`）只要任一落入敏感 subtree 即整段拒绝（fail-closed；遍历超 500 节点预算同样拒绝）。
  - **region**：拖拽框选，释放点以 mouseup 事件自身坐标为准（不依赖最后一次 mousemove），两端坐标先 clamp 进 viewport 再做 <6px 误触判定与归一化；上报归一化 `rect` + `coordinate_space:'viewport'` + `viewport` 证据。
- **rect 视口交集**：element/text 的辅助 rect 一律先取 DOMRect 与 iframe viewport 的交集再归一化（部分可见大元素/文本选择产生 `x+w≤1` 的合法 rect）；交集为空（完全不可见）时省略 rect/coordinate_space/viewport，仅保留 element/text anchor；region 选择发生在 viewport 内，始终为有效正面积。
- **敏感控件排除**：`input[type=password|hidden|email|tel|number|search|file|date…]`、名字/id 命中 `password|token|secret|api-key|card|cvv|otp…` 的输入控件、contenteditable 区域、以及标了 `data-catsco-annotation-sensitive` 的子树——既不作为标注目标，也不读取任何 `.value`（SDK 从不读输入值）。
- **导航失效**：`history.pushState/replaceState` 在模块级**只 patch 一次**，由共享 dispatcher 把导航广播给所有存活实例（每个实例 dispose 只注销自己，任意 dispose 顺序下存活实例仍收到通知；最后一个实例 dispose 才恢复原生方法）。同时 `popstate`/`hashchange` → 清空拖拽/悬停态并上报 `page.v1`；宿主据此使旧页面草稿失效，旧目标不会静默贴到新文档。`pagehide` 清理 overlay。
- `dispose()`：移除全部监听与 overlay、注销 dispatcher、（最后一个实例时）还原 `history` 方法；之后不再响应 connect。多实例共用同一 document 是受支持场景，但建议应用只创建一个实例。

## 4. 发送路径（宿主 UI 组装 metadata）

SDK 上报的 selection **没有 body**（评论由宿主 composer 填写）。发送时宿主把
`{id, kind, label, body, target}` 组装进 `metadata.gateway_annotations` 并调用
`normalizeGatewayAnnotations` 校验；通过后随正常消息发出（HTTP/WS 同一路径，
服务端按 `docs/gateway-annotation-backend.md` 授权并 canonical 落库）。

## 5. 测试

| 文件 | 覆盖 |
| --- | --- |
| `webapp/src/gateway-annotations.test.js`（27 用例） | selection/metadata schema 矩阵（kind 语义、rect/viewport 边界、控制字符、原型污染、16KiB、id 唯一）；host connect/setMode/ready 去重、错 frame/origin/contract/stale session 拒绝、bad-capabilities 撤销、binding 变更失效、AbortSignal、deactivate/dispose。 |
| `webapp/src/gateway-annotation-sdk.test.js`（17 用例） | parentOrigin 校验、connect→ready、错 origin/source/contract 忽略、session 重绑、mode 生效、element 捕获与敏感控件排除、region 归一化坐标、text 捕获与敏感子树、pushState/popstate 页面通知、dispose、普通点击不拦截。 |
| 互操作用例 | 真实 host bridge ↔ 真实 SDK 脚本走完整 connect→ready→mode→target 回环（origin/session 逐跳校验）。 |

运行（复用主仓 node_modules，未安装任何依赖）：

```bash
cd webapp && ./node_modules/.bin/vitest run src/gateway-annotations.test.js src/gateway-annotation-sdk.test.js
# Test Files 2 passed, Tests 44 passed
```

## 6. 边界与已知限制

- SDK 的 hover/region 坐标是**viewport 归一化证据**，不是跨版本稳定锚点；跨 revision 重放由宿主 UI 决定（本模块只提供 page/revision 变更信号）。
- text 捕获依赖 mouseup（键盘/双击选词路径不覆盖，属首版取舍）。
- jsdom 无法跨 window postMessage，互操作测试用 source/origin 忠实回放；真实跨域行为（origin 为字符串比较、`event.source` 为 WindowProxy）语义一致，浏览器端到端由集成负责人跑。
- 不涉及旧 registry/publish_version；gateway-only 应用无需任何 registry 记录即可接入。
