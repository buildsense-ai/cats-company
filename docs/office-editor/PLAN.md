# PPT / Word 预览与就地编辑 · 实施清单

状态：进行中
基线：cats-company `main` @ `1a0e8067`，XiaoBa-CLI `main` @ `705773b`
落地位置：`webapp/`（前端）、`server/`（版本与保存链路）
本文是唯一执行清单，完成一项勾一项，判据不通过不勾。

---

## 一、目标

让用户能在小八的侧边栏里打开 PPT 和 Word，看清内容，改内容，保存，关掉侧边栏再从会话点进来看到改后的样子，并且继续聊天时小八读到的是改后的版本。

一句话验收链：

> 侧边栏改一处 → 保存 → 关侧边栏 → 从会话重新点开看到改后 → 继续聊天让小八再改一处 → 小八改的是改后版本。

## 二、已完成拍板的决策

| # | 决策 | 结论 | 日期 |
|---|---|---|---|
| D1 | 预览方案 | 路线 A，前端直接解析 OOXML 渲染，服务端不转 PDF | 2026-11-13 |
| D2 | 编辑形态 | 路线 3 为骨架，融合路线 2 的「点选直接」：点画布元素 → 面板跳到该元素；不在画布上直接打字 | 2026-11-13 |
| D3 | 是否接 OnlyOffice | 不接。自研，避免第三方容器、AGPLv3 传导、数据出境、小八无法精确操作 | 2026-11-13 |
| D4 | 插入元素范围 | 四种加群组：文本框、图片、矩形/圆角矩形/直线、表格，加组合 | 2026-11-13 |
| D5 | 继承元素（母版/版式来源）被改 | 默认只改这一页；弹窗确认是否改为「所有使用该版式的页」 | 2026-11-13 |
| D6 | 交付节奏 | 周期估算仅作参考，按清单顺序直接推进 | 2026-11-13 |

## 三、明确不做的部分

写在这里是为了以后不被反复追问，也为了避免范围悄悄变大。

- 动画、切换效果：不做。没有播放场景。
- SmartArt、图表内部编辑：不做。可以渲染显示，不能改内部数据。
- 多人同时编辑与冲突合并：本期不做，结构上留出口。
- 100% 与 Office 视觉一致：不承诺。目标是常见商务文档 95% 的页看起来一致。
- 服务端 LibreOffice 转换：不做，与 D1 一起排除。
- 移动端编辑：不做，只做预览。

## 四、现状事实（已核验，带坐标）

### 4.1 前端类型判定

`webapp/src/widgets/chat-message.jsx`

- 第 54 行 `TEXT_FILE_EXTENSIONS` 只有文本类扩展名
- 第 55 行 `PREVIEW_FILE_EXTENSIONS = new Set(['PDF', ...TEXT_FILE_EXTENSIONS])`，**不含 DOCX / PPTX**
- 第 57 行 `SPREADSHEET_FILE_EXTENSIONS = new Set(['CSV','XLS','XLSX'])`
- 第 2319 行有 `isDocxFile`，但未被 `isPreviewableFile` 引用
- 全文无 `isPptxFile`，pptx 落进兜底「文件」分支
- 第 2325 行 `isPreviewableFile` 是唯一的预览准入判断
- 第 2333 行 `artifactMeta` 决定卡片标签，docx 显示「可下载的文档」
- 第 2875 行 `FilePreviewPanel` 是侧边栏预览容器
- 第 3596–3647 行是各类型的分支渲染

### 4.2 服务端

`server/upload.go`

- 第 39 行 `maxUploadSizeMB = 300`
- 第 97–98 行 `allowedFileExts` 已含 `.doc .docx .ppt .pptx`
- 第 255 行 `generateFileKey(storedExt)`：每次上传生成新 key，**文件不可变**
- 第 547 行 `HandleServeFile` 是 `/uploads/` 的读接口，第 1340 行挂载，**不经 JWT**
- 第 1160 行 `isUploadPreviewableExtension` 仅 pdf / md / html / video

版本相关：

- `server/file_open_binding.go` 第 170 行把描述符摘要写进 `source.Version`
- `server/cloud_artifacts.go` 第 111 行 `PublishVersion *int`，Artifact 已有版本语义，Office 文件照它设计

### 4.3 小八侧

`XiaoBa-CLI/src/catscompany/index.ts` 第 1806 行 `receiveUploadedFile`：
收到附件时下载到本地会话缓存再读。**会话指向哪个版本，下一轮就读哪个版本**，这一环不需要新建机制。

### 4.4 可复用的既有能力

- 文件批注链路：`POST /api/files/open-bindings`、`POST /api/files/annotations`（`server/cmd/server.go` 第 1030–1032 行）
- 单元格点选用 `data-file-annotation-row` 属性定位（`webapp/src/widgets/file-annotation-editor.jsx` 第 480 行）
- 表格渲染在 `webapp/src/widgets/spreadsheet-preview.jsx` 第 255 行

## 五、架构设计

### 5.1 核心原则

画面上每个元素在渲染时就带上身份，指向文件里的具体位置。保存时按记录的路径写回。**不存在「从画面反推回文件」这一步**，这是与第三方方案的根本差别。

### 5.2 数据流

```
读文件  →  解包 OOXML，得到元素树
建模型  →  每个元素分配稳定 id，记录其 XML 路径
渲染    →  画布由模型生成，DOM 节点带 id
点选    →  点到即得 id，零推断
改值    →  只改模型中该元素该字段
回写    →  按记录的 XML 路径写回，其余字节原样保留
打包    →  重新 zip 成新文件
```

### 5.3 模块划分（新增文件）

前端 `webapp/src/office/`

| 文件 | 职责 |
|---|---|
| `ooxml/zip.ts` | OOXML 包的解压与重打包，保留未改动部件 |
| `ooxml/xml.ts` | 局部可寻址的 XML 解析与写回，支持按路径替换节点文本 |
| `pptx/parse.ts` | 把 pptx 解析成演示文稿模型 |
| `pptx/model.ts` | 模型类型定义、元素 id 分配、坐标换算（EMU ↔ %） |
| `pptx/write.ts` | 模型变更写回 OOXML |
| `pptx/render.tsx` | 画布渲染，元素带 `data-office-el` |
| `docx/parse.ts` `docx/model.ts` `docx/write.ts` `docx/render.tsx` | Word 侧同构 |
| `ui/OfficePreview.tsx` | 侧边栏预览容器，接入 `FilePreviewPanel` |
| `ui/PropertyPanel.tsx` | 属性面板，点选联动与字段编辑 |
| `ui/ElementList.tsx` | 元素列表，折叠、点选跳转、悬停反向高亮 |
| `ui/InsertMenu.tsx` | 插入元素菜单 |
| `ui/InheritedConfirm.tsx` | 继承元素确认弹窗（D5） |

服务端 `server/`

| 文件 | 职责 |
|---|---|
| `office_document.go` | 文档身份与版本：`file_id` / `version` / `parent_ref` |
| `office_save.go` | 保存接口：接收新文件、登记新版本 |

### 5.4 版本数据形状

附件不可变，所以保存只能是「生成新版本」。最小三个字段：

| 字段 | 含义 |
|---|---|
| `file_id` | 同一份文档的稳定身份，跨版本不变 |
| `version` | 递增整数 |
| `parent_ref` | 上一版的附件引用，用于回退与冲突检测 |

会话侧：保存后追加一条附件消息指向新版本，标明「已更新」。不改消息协议，不做原地替换。

## 六、任务清单

每项都有判据。判据不通过就停在那一步，不往下走。

### 阶段 0 · 保真度探针

- [x] 0.1 收集真实样本：用本机 PowerPoint COM 生成含中文标题、副标题、表格、两个版式的样本
- [x] 0.2 不自研渲染库做比对，改为自研解析 + 逐元素比对（结论见下）
- [x] 0.3 用真实 PowerPoint 读回每个元素的文字、字号、粗细、颜色，与解析结果对照
- [x] 0.4 探针报告：`webapp/scripts/office-probe.mjs`

**判据达成情况**：候选渲染库（docx-preview / pptx-preview / pptx-glimpse）在探针中被否决，原因是它们把幻灯片拍平成 HTML，**丢失了渲染节点与 XML 节点的对应关系**，而点选编辑正需要这条链路。改为自研解析器，保真度用真机读回校验。

探针在真实文件上暴露并修掉了三个 bug：
1. `graphicFrame`（表格）的坐标在 `p:xfrm` 而非 `p:spPr/a:xfrm`，只读后者会让所有表格变成 0x0
2. 主题色 `sysClr` 的 `val` 是系统名（如 windowText），不是颜色，要读 `lastClr`
3. 继承占位符的 `idx` 与版式不一致时会解析不到坐标，需要按 type 兜底

### 阶段 1 · 只读预览 + 点选联动

- [x] 1.1 `webapp/package.json` 增加 `jszip`
- [x] 1.2 新增 `isPptxFile` / `isOfficePreviewFile`，接入 `isPreviewableFile`
- [x] 1.3 `artifactMeta` 补 PPT / Word 标签与副标题
- [x] 1.4 `FilePreviewPanel` 增加 office 分支
- [x] 1.5 `pptx/parse.js` 解析幻灯片、形状、文本、几何、样式、主题、继承链
- [x] 1.6 `pptx/render.jsx` 渲染单页，元素带 `data-office-el` 与稳定 id
- [x] 1.7 `ElementList.jsx` 折叠列表、点选跳转、悬停反向高亮
- [x] 1.8 `PropertyPanel.jsx` 显示选中元素字段
- [x] 1.9 多页切换

**判据达成情况**：点画布元素 → 面板定位到它 → anchor 路径可解析回同一 XML 节点，由 `canvas.test.jsx` 断言。

### 阶段 2 · 文本与样式回写

- [x] 2.1 `ooxml/xml.js` 按字符区间替换节点，不重排整份 XML
- [x] 2.2 `pptx/write.js` 文本写回（只重写改动的段落，其余段落字节不变）
- [x] 2.3 `pptx/write.js` 样式写回（字号、颜色、粗斜体、对齐，写在 run 上）
- [x] 2.4 `validateSlidePart` 保存前校验：形状 id 唯一、表格网格与单元格数匹配
- [x] 2.5 `PropertyPanel` 文字、字号、颜色、字形、对齐改为可编辑，带草稿与撤销
- [x] 2.6 未改动部件字节级不变（`office.test.js` 与 `style.test.js` 各有一条断言）

**判据达成情况**：真实文件改文字 + 改样式后，PowerPoint 打开无修复提示，且用 COM 读回字号 33pt、粗体、颜色 #C86400 全部吻合。
校验脚本：`webapp/scripts/office-style-roundtrip.mjs` + `webapp/scripts/office-open-check.ps1 -ReadFormat`。

### 阶段 3 · 几何与层级

- [x] 3.1 画布支持拖动元素，坐标按 EMU 换算回写（`pptx/geometry.js` + `ui/EditableCanvas.jsx`）
- [x] 3.2 尺寸调整手柄（八向，拖西/北边时同时移动原点）
- [x] 3.3a 对齐：相对页面的六向贴边/居中，已接进属性面板
- [ ] 3.3b 元素之间互相对齐（`alignBoxes` 多元素分支）：函数与单测都在，界面未接。
      面板收到 `onAlign` 但没有使用点，`GeometryFields` 只接 `onAlignToSlide`
- [ ] 3.3c 等间距分布（`distributeBoxes`）：函数与单测都在，**没有任何界面调用点**
- [x] 3.4 层级置顶/上移/下移/置底（移动节点，不是写属性）
- [x] 3.5 撤销重做（`ui/history.js`，快照式，上限 60 步）

**判据达成情况**：真实文件移动 0.4 英寸 × 0.2 英寸并放大后，PowerPoint 读回 left=88.8pt top=54.4pt width=487.2pt height=44.8pt，与写入值精确一致，偏差 0。

以下是实现过程中发现并必须写下来的三件事：

1. **拖动锚点必须取手势开始时的盒子**，不能取上一帧。取上一帧会让舍入误差累积，形状会逐渐偏离鼠标。
2. **EMU/像素换算因子必须每次手势现测**。侧边栏可拖宽，缓存的因子会让每次拖动都按比例短一点或长一点。
3. **层级在 OOXML 里是文档顺序，没有 z-index**。`p:spTree` 里靠后的节点画在上面，所以置顶就是移动节点。`p:nvGrpSpPr` 与 `p:grpSpPr` 不是形状槽位，必须保持在最前，否则 Office 认为结构损坏。

### 阶段 4 · 元素增删与页增删

- [x] 4.1 插入文本框（`pptx/insert.js` + `write.js::insertTextBox`）
- [x] 4.2 插入图片（媒体部件 + 关系登记 + Content-Type Default）
- [x] 4.3 插入矩形 / 圆角矩形 / 直线
- [x] 4.4 插入表格（含 tblGrid 列宽与单元格数对齐）
- [x] 4.5 组合成群组（`chOff/chExt` 等于 `off/ext`，子元素保留绝对坐标）
- [x] 4.6 删除元素
- [x] 4.7 加页、删页、复制页、调序（`pptx/slides.js`）
- [x] 4.8 继承元素识别，弹 `InheritedConfirm`，按 D5 分「只改这一页」与「改版式」两条写路径
- [x] 4.9 插入前扫已用 id，取最大值加一，防编号冲突
- [x] 4.10 统一写入口 `pptx/apply.js`，一次批次只保存一次

**判据达成情况**：真实文件上依次做五次插入（文本 / 矩形 / 圆角矩形 / 表格 / 图片）、组合两个元素、复制页、加页、调序，PowerPoint 打开无修复提示，4 页全部正常，形状 id 无冲突，图片关系可解析到 70 字节媒体。

结构校验由 `scripts/office-insert-roundtrip.mjs` 输出，判据为六项全部 ok：sldId 关系可解析、sldId 唯一、Content-Type 覆盖数与页数一致、形状 id 唯一、组合保留两个子元素、图片媒体非空。

以下是这一阶段必须写下来的四件事，全部是真实文件暴露出来的：

1. **新建部件必须单独记录**。`save()` 原来只遍历读入时的条目列表，新建的 slide 与 media 部件不会出现在输出里，而内存中一切读回正常。这个 bug 的形态是「本地全绿、PowerPoint 报缺失」。
2. **图片必须走 `r:embed` 关系 id，不能写路径**。写路径的文件能打开，但显示空白框。
3. **表格必须有 `a:tblGrid`，且列宽个数等于单元格数**，否则 PowerPoint 报内容损坏。
4. **`p:nvGrpSpPr` 与 `p:grpSpPr` 不是形状槽位**，重排层级或组合时必须保持在形状之前。

### 阶段 5 · 保存链路与会话联动

- [x] 5.1 不新增服务端表。附件本就不可变，保存 = 上传新文件拿新 `file_key`
- [x] 5.2 复用现有 `POST /api/upload?type=file` 与 `api.uploadFile`，没有新接口
- [x] 5.3 前端保存后把字节包成 File 上传（`office/save.js::officeBytesToFile`）
- [x] 5.4 会话追加一条附件消息，文本说明 + file 块（`buildOfficeUpdateMessage`）
- [x] 5.5 保存后侧边栏自动切到新附件，看到的就是刚存的那一版
- [x] 5.6 新消息落地后合并进当前列表与历史缓存，不需要刷新
- [ ] 5.7 验收链真机跑通（需要服务端与浏览器，见下）

**为什么没有 `file_id` / `version` / `parent_ref`**

原计划想新建一张表记录版本链。实际读代码后发现不需要：`generateFileKey` 每次上传都产生新
key，旧文件永远可达；会话消息本身就是版本链，顺序即历史。为同样的事再建一张表，等于把
「哪一版是最新」的判定从「最后一条消息」搬到「一个必须同步维护的字段」，多一处会不一致的
地方。所以保存只做两件事：上传，发消息。

**关于 5.7 的诚实说明**

这条判据要求「改 → 存 → 关 → 重开 → 续聊」在真实浏览器里跑一遍，需要同时起 Go 服务端与
webapp dev server，并有一个可交互的会话。本轮完成的是代码链路与 103 条单元测试，其中保存
消息的形状、文件名修复、空字节拒绝都有断言覆盖；但**五步闭环我没有亲手点过**，不能声称
通过。需要在一个有服务端的环境里验证，判据维持未勾。

**这一阶段的设计取舍**

插入和页操作如果每次都发消息，连插五个形状就是五条消息。所以分成两层：
结构操作立刻改字节但不发布，攒在 `pendingBytes`；点「写入文档」时把 draft 改动与
pending 字节合成一次上传、一条消息。代价是多一个 `hasPending` 状态，收益是会话不被刷屏。

### 阶段 6 · Word 同构实现

- [x] 6.1 `docx/parse.js` 解析段落、run、样式、表格、节属性
- [x] 6.2 `docx/render.jsx` 流式渲染，段落与 run 带定位标记
- [x] 6.3 `ui/WordPreview.jsx` Word 版属性面板：文字、对齐、缩进、字号、颜色、粗斜体下划线
- [x] 6.4 `docx/write.js` 写回：段落文字、run 样式、段落属性、插入段落、删除段落
- [x] 6.5 表格单元格文字编辑（点单元格选中，右侧面板改行列与内容）
- [x] 6.6 插入图片（media 部件 + 关系登记，按正文可用宽度等比缩放）

**判据达成情况**：用 Word 自己生成的 docx（13725 字节，3 段，其中一段粗体 14pt、一段居中），
改文字 + 改样式（18pt 粗体 #C86400 居中）后，Word 打开无修复提示，读回：

| 项 | 期望 | Word 读回 |
|---|---|---|
| 文字 | Edited by round-trip harness | 一致 |
| 字号 | 18 pt | 18 |
| 粗体 | 是 | bold=-1（COM 的 true） |
| 颜色 | #C86400 | #C86400 |
| 对齐 | 居中 | align=1（居中） |
| 段数 | 4 段不变 | 4 段 |

脚本：`scripts/office-docx-roundtrip.mjs` + `scripts/office-docx-open-check.ps1 -ReadFormat`。

**Word 与 PPT 的结构差异，以及由此产生的取舍**

Word 不存坐标，只存顺序。所以这里没有拖动、没有层级、没有对齐到边界——不是没做，是文件里
没有这个概念，硬做等于自己发明一套偏离 Word 的排版引擎。对应的能力换成段落级：对齐、缩进、
字号、颜色、字形。

**这一阶段踩到并修掉的三个坑，都是真实 Word 文件暴露的：**

1. **`w:sz` 是子元素不是属性**。按属性读会永远返回空，所有 run 的字号都显示 0，于是「字号写
   成功」在验证里表现为「字号丢失」。这个 bug 只有用 Word 生成的真实文件才暴露得出来，自造
   夹具里我恰好也写成了子元素形式，两边都错就互相掩盖。
2. **`w:rPr` 里没有 `w:sz` 时的插入位置**。必须按 `rFonts → b → i → u → color → sz` 的顺序插，
   否则同一段文字反复改样式会产生越来越长的字节差异。
3. **「设为无值」和「删除该属性」是两种意图**。一开始用一个 `null` 同时表示两者，结果是「设为
   粗体」把 `<w:b/>` 删掉了——加粗反而取消加粗。现在用 `set` / `remove` 两个显式模式。

### 阶段 7 · 收尾

- [x] 7.1 单元测试：解析、写回、坐标换算、id 分配、层级、历史、保存载荷（155 条）
- [x] 7.2 真实文件回归：PowerPoint 与 Word 各用 COM 生成样本，走完整往返
- [ ] 7.3 大文件表现：20 MB 以上文件的解析与渲染耗时（未测）
- [ ] 7.4 知识库归档（未做）

### 阶段 8 · 表格单元格、插图、组内子元素选中

这三项原来被划为「不做」，理由是各自的边界工作独立。做完之后的判据都是**用真实 Office 打开读回**，
不是自己解析回来看着对。

- [x] 8.1 Word 表格单元格文字：点单元格选中，右侧面板改行列与内容
- [x] 8.2 Word 插入图片：media 部件 + 关系登记 + Content-Type Default，按正文可用宽度等比缩放
- [x] 8.3 PPT 组合内子元素单独选中：点组内子元素拿到它自己，拖动按 `chOff/chExt` 换算回相对坐标

**判据达成情况**

Word 表格与插图，样本是 Word COM 生成的 2×3 表格文档（14099 字节）：

| 项 | 期望 | 证据 |
|---|---|---|
| 单元格文字 | CELL-EDITED | Word 读回 `tables 1, table1 rows=2 cols=3, cell11=[CELL-EDITED]` |
| 表格结构 | 2×3 不变 | 重建后仍 2 行 3 列 |
| 插入图片 | 真图不是破图占位 | Word 读回 `inlineShapes 1`，`type=3`（图片），170.1×170.1 pt |
| 结构合法 | 无修复提示 | `RESULT OPENED_CLEAN` |
| 部件 | drawing + media 都在 | `1 w:drawing`，`word/media/image1.png`，关系 rId6 |

PPT 组内子元素，样本是 4 页真实文件，组在第 2 页：

| 项 | 期望 | 证据 |
|---|---|---|
| 文件不坏 | 无修复提示 | PowerPoint 打开 `RESULT OPENED_CLEAN`，4 页 |
| 只有子元素动 | 兄弟元素不动 | 子元素 57.6,334.8 → 86.4,349.2（即 +0.4in,+0.2in）；兄弟矩形仍在 252,334.8 |
| 只改本页 | 第 3 页同名组不变 | 第 3 页仍是 57.6,334.8 |
| 组结构 | 子元素仍 2 个 | `group children count 2 -> 2` |

**这一阶段修掉的两个真实 bug，都是「自测全绿、Office 里错」的类型**

1. **`a:chExt` 读的是 `cx/cy`，原来按 `x/y` 读**。真实文件里永远读到 0，而两个几何换算函数
   都有「extent ≤ 0 就原样返回」的保护，于是组内子元素按相对坐标当绝对坐标渲染，拖动也被吞掉。
   保护本身是对的，是它把一个读错字段变成了静默失败。
2. **组内子元素的 `elId` 会撞号**。`elId` 由「部件名 + 在该层数组里的序号」拼成，子元素用它自己
   的局部序号，所以同一页上第一个顶层元素和每个组的第一个子元素都叫 `slides-slide1#0`。
   写入时按 `elId` 找目标，`findShape` 找到第一个就返回，于是「改组内子元素」实际改到了无关的
   顶层元素上。现在子元素的 id 带父级前缀，形如 `slides-slide1#2/0`。

**方法上的教训**：这两条都不是靠单测发现的。第一条是脚本在真实文件上返回 `changed: false`，
第二条是脚本报告位移落在错误的元素上。单测用自造夹具时，夹具和解析器犯了同一个错，两边互相掩盖。

**已知未完成项，不勾就是不通过**

| 项 | 原因 | 影响 |
|---|---|---|
| 五步闭环真机验证 | 需要同时起 Go 服务端与 webapp dev server，并有人点一遍 | 代码路径与 135 条单测覆盖，但端到端没亲手跑过 |
| Word 表格单元格编辑 | 单元格内多段落写回是独立一块 | 表格当前只读 |
| Word 插入图片 | 需要 media 部件与关系登记，Word 与 PPT 的关系路径不同 | 只能插入段落 |
| PPT 组内元素单独选中 | 组内子元素用相对坐标，拖动需要坐标换算 | 组合后整组编辑 |
| `/uploads/` 鉴权 | 既有问题，非本次引入 | 编辑上线后公开性被固化 |
| 大文件性能 | 未测量 | 60 MB 上限是估的 |

## 六点五、验证脚本清单

这些不是测试，是**用真实 Office 打开来判定**的验收脚本，都放在 `webapp/scripts/`。
它们的存在理由是同一条：我们自己的解析器比 Office 宽容，读回正常不代表文件没坏。

| 脚本 | 判定内容 |
|---|---|
| `office-probe.mjs` | 结构探针：页数、元素、坐标、继承、主题色 |
| `office-roundtrip.mjs` | PPT 文本写入：改动区最小化、其余字节不变 |
| `office-style-roundtrip.mjs` | PPT 样式写入：字号/粗体/颜色往返一致 |
| `office-geometry-roundtrip.mjs` | PPT 几何：位置尺寸偏差 < 0.2% |
| `office-insert-roundtrip.mjs` | PPT 插入与页操作：sldId、内容类型、形状 id、图片媒体 |
| `office-open-check.ps1` | 用真实 PowerPoint 打开，可选读回格式与几何 |
| `office-docx-roundtrip.mjs` | Word 文本与样式写入往返 |
| `office-docx-open-check.ps1` | 用真实 Word 打开，可选读回字号/粗体/颜色/对齐 |

## 七、已知风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 渲染保真度不足 | 用户觉得「不像」 | 阶段 0 先量，差异大的格式在界面上明说「预览可能有差异」 |
| 中文换行与 Office 不一致 | 文字溢出或提前折行 | 接受差异，不承诺 100%；用真实样本回归 |
| 继承元素改错页 | 别的页一起变 | D5 已定：默认只改本页并弹窗确认 |
| 元素 id 冲突 | PowerPoint 报需修复 | 4.9 插入前扫描取最大值；2.4 保存前校验 |
| `/uploads/` 无需鉴权 | 编辑上线后公开性被固化 | 待定，建议在阶段 5 之前确认附件隐私策略 |
| 大文件卡顿 | 体验差 | 7.3 测量，必要时按页懒解析 |

## 八、判据总表

| 阶段 | 判据 | 通过标准 |
|---|---|---|
| 0 | 探针报告 | 有差异占比数据 |
| 1 | 点选定位 | 控制台输出正确的 XML 路径 |
| 2 | 真机打开 | PowerPoint 无修复提示，内容样式正确 |
| 3 | 位置精度 | 偏差 < 0.2% |
| 4 | 结构完整 | 插入与页操作后无修复提示，编号无冲突 |
| 5 | 验收链 | 改 → 存 → 关 → 重开 → 续聊，五步闭环 |
| 6 | Word | Word 打开正常 |
| 7 | 回归 | 样本集全部通过 |
