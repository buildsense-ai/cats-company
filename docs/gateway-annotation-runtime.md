# Gateway 自动标注 runtime（公共 SDK / 导出）

范围仅 gateway-only app。SDK 唯一可编辑源为 `webapp/public/catsco-annotations.js`；不处理平台旧 versioned artifact，不负责 open binding API 或宿主发送 UI。

## 注入契约（与真实 gateway 对齐）

Gateway 对 opt-in app 的 HTML 响应注入固定 **同 origin 外部 classic script**：

```html
<script src="/_catsco/runtime/annotations-v1.js"
  data-catsco-parent-origins="[&quot;https://app.catsco.cc&quot;]"></script>
```

- 属性名称固定 `data-catsco-parent-origins`。值为 JSON string array，HTML attribute escaping 由 gateway renderer 做；SDK 通过执行时 `document.currentScript.getAttribute` 获得浏览器解码后的 JSON。
- origin 必须是规范化精确 HTTP(S) origin（`new URL(value).origin === value`），例如 `https://app.catsco.cc`、本地 `http://127.0.0.1:3000`。拒绝 `*`、`null`、路径、尾 `/`、query/hash、userinfo、opaque scheme。配置为空、非法或混入非法项则整个自动 bootstrap fail closed；最多 32 项，属性最多 16 KiB。
- 不从 URL、referrer、cookie、viewer identity、topic 推导信任。已配置 allowlist 不允许后续重复脚本扩大或替换。
- 外部加载不需要 inline JavaScript 或远程 dynamic import；script 必须由 gateway 的固定本地资源 route serve，不能代理用户提供 URL。
- 无 `open_ref`、JWT、控制 secret 或聊天路由进入 script/attribute/frame。SDK 不读取这些数据，不发 chat/network 请求。bridge `session_id` 只是文档握手 token，**不是 Agent session**；Agent/open-binding 由父页面和服务端负责。
- `</head>` + `</body>` 双点注入允许。第二次执行保留原 `window.CatsCoAnnotations`、实例、导航 dispatcher 与监听；既不重置会话，也不重复 target/page。可能仍有两个浏览器资源请求，SDK singleton 不能消除 HTTP 请求。

### CSP 与降级

保持 upstream CSP，不增 `unsafe-inline` / `unsafe-eval`、不添加 nonce、blob URL 或绕过限制。`script-src 'self'` 允许资源才可执行；禁止时宿主应显示 SDK unavailable，不改用其他发送路径。SDK 的既有 overlay 使用动态 style/style properties；严格 style CSP 可能使视觉提示受限，SDK 不放宽策略。Nginx 的 HTML/content-type/gzip/cache/download/stream/WS 行为与固定 alias route 测试归 gateway-runtime C 所有，本模块不声称完成这些集成验收。

## bootstrap / explicit 优先级

1. 属性配置生效后，只安装一个等待 connect 的 window message listener，不开启标注、不创建 overlay。
2. 首个有效 `connect.v1` 需 `event.source === window.parent`、非 standalone、origin 在 allowlist、正确 bridge contract、session/request 非空且不超过 128 字符，无控制字符。
3. 从该事件 origin 固定创建 singleton，移除等待 listener，**直接把同一首次事件交给实例**并回 ready（原样 echo session/request），不要求 host 第二次 connect。
4. 手工 `create({ parentOrigin, revision?, getElementId? })` 在首次 connect 前执行则优先建立同一 singleton；自动已建立后同 origin create 返回该对象并原位更新显式提供的 revision/getElementId，不新增监听，也不重握手。冲突 origin（即使另一个 allowlisted origin）拒绝返回 `null`。
5. 手工 SDK 已在之前无配置的 script 下建立单个实例，后来 gateway 重复加载时可收养该实例（必须在 allowlist 内）；已有多实例或 origin 冲突则拒绝自动配置，绝不另加自动实例。
6. **无注入属性的纯手工模式仍保留既有多实例 API**与共享 history dispatcher，兼容原高级 app。注入模式不再并行多实例。
7. `instance.dispose()` 移除监听/overlay 并注销导航 dispatcher；最后一实例恢复原 history。不会因晚到 connect 或重复 script 自动复活。应用可明确再次 create，或新文档重新 bootstrap。
8. `window.CatsCoAnnotations.dispose()` 同时取消尚在等待的 bootstrap 与全部存活实例。公共 `bootstrapAttribute(jsonString)` 用于重复加载内部配置，不应由 app 从用户数据推导调用；同文档配置不可变。

高级配置应放在 app 自己已被 CSP 允许的外部 JS 中：

```js
const sdk = window.CatsCoAnnotations.create({
  parentOrigin: 'https://app.catsco.cc',
  revision: 'r7',
  getElementId: (element) => element.dataset.appId,
});
// 与自动实例同对象；没有 revision 时不杜撰。
sdk?.setRevision('r8');
```

## 单按钮 inline 批注：`select`

新版父页面调用 `host.setMode('select')`，用户无需选择 kind。SDK 以一个 mouse gesture 解析：

- 短点击（最大轴位移 ≤6px）捕获起点 element，要求仍在当前 document 且非敏感 subtree。
- 最大轴位移 >6px 解析为 region，使用 **mouseup 实际坐标**；先与 viewport 求交。正面积的窄框有效，零面积拖动丢弃，不能退回 element。
- mouseup 只发布一次，浏览器随后 click 被消费，不追加 element；拦截 document capture 的 pointer down/up、mouse down/move/up、click/dblclick/auxclick，避免 app 元素普通事件/默认操作。SDK 无法撤回在安装前已经运行的 window capture 监听，应用全局监听时序仍需真实浏览器验证。
- `select` 必须包含合法正面积 `target.rect`、`coordinate_space:'viewport'` 和 `viewport`。element 无 bbox/完全不可见时 fail closed，不捏造点坐标。发布后保留高亮，直到父页面 off/new mode、导航、revision/session 更换或下一次选择。
- SDK 不创建评论输入框；父页面以 bbox 定位自己的 popover，再用 parent-only open binding 调 annotations API 直接发送。SDK 不接收 open_ref，不读父页面 textarea，也不负责消息正文或发送。
- capabilities 和机器 kind 仍为 `element/text/region`，没有新的 `kind:'select'`。host 在 select 接受已声明三种 kind，照常验证 frame/origin/session/page，额外要求 bbox。旧 explicit element/text/region 仍精确 kind gate；旧 normalizer 接受无 bbox 锚点的历史文档不变。
- 本次 select 默认只发 element/region。已有 text Selection 的自动优先级属 optional，未增加，以免 stale Selection 与 click/drag 混淆；旧 explicit text 捕获保留。
- 选中过程记录 page/revision/session/request，导航/revision/session/off/Escape 取消旧拖动和尾随 click。相同 revision 不取消有效拖动。

### Escape 同步父按钮

`select` 内 Escape 设 off、清选区，并沿现有 `mode.v1` 发送 **只含 `mode:'off'` + session + 当前 page** 的退出通知。shared host 只接受已 ready select 的 exact frame/origin/session/page；不会允许 SDK任意开启模式。

```js
const host = createGatewayAnnotationHost({
  getBinding,
  onSelection,
  onModeChange: (mode) => setAnnotationMode(mode), // iframe Esc → off
});
host.setMode('select');
```

旧 explicit modes 的 Escape 保留旧行为，避免高级应用兼容回退。parent 自身 textarea 的 Escape/取消由 UI 自己处理并调用 host off。

## 双截图

宿主使用 `captureScreenshot({selectionId,page,signal})` 请求截图，并可调用 `cancelScreenshot()`；`screenshotSupported()` 读取当前文档能力。SDK 的 ready 声明截图支持，请求和结果绑定当前 session、selection 和 page。

- 固定同源资源 `/_catsco/runtime/html2canvas-pro-1.6.7.min.js`（html2canvas-pro 1.6.7）按需加载并校验 SRI。支持浏览器计算后的 `color()`、`oklab()`、`oklch()`，包括 `color-mix()` 的计算值和伪元素。保留应用原有 global，捕获使用验证后的 renderer；不使用 CDN。
- 1.6.7 的浏览器包在 UMD 初始化后将 default export 转为 `window.html2canvas` 函数；加载器冻结这个最终函数，而不是根据包头或无 window 的 VM 推断导出形态。
- 同一 viewport bitmap 生成 full（红框标注目标）和 crop（16 CSSpx 周边）两张 JPEG。DPR 最多2，每边最多2048，每图最多2 MiB。
- 捕获克隆中排除批注 overlay、遮挡敏感控件并保留布局；保留的 Shadow DOM 根整体遮挡，展开为普通 DOM 的节点继续按控件/敏感 subtree 遮挡。Shadow DOM 节点纳入 10,000 节点预算。风险提示说明跨域图、背景、视频、嵌套内容或 WebGL 可能缺失。
- 不支持的颜色/图片函数解析错误仅返回 `unsupported-style`，不传递原始 CSS 或错误正文。宿主显示样式不支持提示；普通 `capture-failed` 不再误归因为资源读取失败。
- scroll、resize、page/revision/session 变化、off、Escape 或 dispose 作废选区及在途截图；即使已捕获，滚动和尺寸变化也通知宿主撤销旧目标。
- 图片结果经父页面上传并用 `image` content blocks 发送；不进入 annotation metadata、草稿 storage 或 SDK 的聊天请求。

## 文档 / session 防御

保留 element/text/region 捕获、敏感 subtree fail-closed、exact source/origin、revision/page 上报、session mode 控制等既有功能。

- 有效 **新 session 或新 request** connect：先把 mode 归 off，取消 hover/region drag、清真实 DOM Selection，再写新 session 并回 ready。清理发生在 ready **之前**，避免同步 host ready callback 的 mode 被覆盖。重复同 session/request connect 可重发 ready，保持当前 mode。
- malformed connect 忽略，不截断/改写原 session；旧 session mode 不生效。
- SPA navigation / 实际 revision 变化清 hover/drag/Selection 并通知 page；相同 revision 的既有 drag 不受影响。
- mode 转换与 Escape 清旧 Selection；pagehide 清捕获并移除会话，需新 connect；dispose 后 setRevision 不会复活。

## 唯一源导出（C 仅导出，不手改 vendor）

脚本不改 gateway，不安装依赖、不生成时间戳；逐字节输出源 SDK 和确定性 manifest：

```bash
# 在 CatsCo worktree；out-dir 由 C 选择其拥有的 vendor/public 目录。
node scripts/export-gateway-annotation-runtime.mjs --out-dir /path/to/gateway/vendor

# 同时 pin 主会话冻结的 source SHA-256，源变更则拒绝写入。
node scripts/export-gateway-annotation-runtime.mjs \
  --out-dir /path/to/gateway/vendor --expected-sha256 <64位小写hex>

# CI / 联调反漂移：必须 exact SDK bytes + exact deterministic manifest。
node scripts/export-gateway-annotation-runtime.mjs \
  --out-dir /path/to/gateway/vendor --check --expected-sha256 <64位小写hex>
```

产物：`annotations-v1.js`、`html2canvas-pro-1.6.7.min.js`、`html2canvas-pro-1.6.7.LICENSE`、兼容已打开页面的 `html2canvas-1.4.1.min.js` / `.LICENSE`、`annotations-v1.manifest.json`。manifest 保留 SDK source/SHA-256/bytes/固定 runtime URL/配置属性，并列出五项资源的尺寸、MIME 和 SHA。新 renderer 从 npm 1.6.7 artifact 原样取出并验证 npm SHA-512；许可文件保留 fork 与 upstream 的 MIT 通知。renderer 与许可证有固定内容校验；SDK hash 应取当前仓库源文件。改 SDK 后重新测试并导出，gateway 不手改第二份。`annotations-v1` 为协议 URL，**不是 immutable content hash URL**，缓存应 revalidate。

上线前须先部署完整资源目录，再加载包含新 renderer exact alias 的 Gateway 配置。旧 alias 保留以服务已经打开的旧 SDK 文档；重新加载应用 iframe 后采用新 SDK，旧文档不会自动替换内存中的 renderer。

## 现代 CSS 真实浏览器回归（#598）

```bash
node scripts/gateway-annotation-screenshot-browser.mjs
opencli browser screenshot-regression open http://127.0.0.1:18762/
# 使用 open 返回的真实 target ID，激活标签页；后台标签页会节流 readiness/timer。
opencli browser screenshot-regression tab select <targetId>
opencli browser screenshot-regression click '#run'
opencli browser screenshot-regression wait text 'PASS:' --timeout 10000
opencli browser screenshot-regression eval 'window.result'
# 再打开 http://127.0.0.1:18762/?plain 验证普通 RGB 页面。
```

fixture 使用 127.0.0.1 宿主与 localhost iframe 的真实跨 origin 通信、完整 SDK / host 和 SRI bundle；捕获真实 JPEG，再断言现代颜色、伪元素、遮罩区域像素、full 红框、crop 无红框、尺寸，以及原应用的输入和 renderer global 未变。包括保留和展开的 Shadow DOM。fixture 不上传图片，不发送 Agent 消息。

## 实际测试

复用 B 管理的已有 `webapp/node_modules`，无需新增依赖：

```bash
cd webapp
pnpm exec vitest run src/gateway-annotation-screenshot-sdk.test.js \
  src/gateway-annotation-sdk.test.js \
  src/gateway-annotation-sdk-bootstrap.test.js src/gateway-annotations.test.js \
  src/artifact-open-binding.test.js --reporter=default
cd ..
node --test scripts/export-gateway-annotation-runtime.test.mjs
node --check webapp/public/catsco-annotations.js
```

当前专项结果：SDK58 + screenshot30 + host54 + bootstrap32 + open-binding3 = **177 个通过**；连同截图 UI 工具和消息流程共 **184 个通过**；export Node tests **4 个通过**。测试执行公共源完整 production bytes + 真实 SDK / host 方法。覆盖首次 ready、双注入、explicit 前后升级、错误 allowlist/source/origin/session、真实 Selection/drag stale 防御、dispose、renderer 来源和遮罩、截图取消及裁图几何，以及 export 真 CLI/hash/byte/manifest 漂移。另有本地 HTTP fixture 经 jsdom 外部资源加载器实际请求两次 SDK，验证 HTML escaped 属性解码、document.currentScript 自动配置、首次 ready 和单个 element target。jsdom 回放跨 realm postMessage（模拟 structured clone），**不等价真实浏览器跨 origin / Nginx / CSP / 服务端落库 E2E**。本地核心浏览器链路另用真实 React、Gateway/Nginx 和 Go handlers 验证了 click、Esc、drag、双 JPEG 预览、发送及原会话回执；完整浏览器负向矩阵和生产环境仍须单独验证。
