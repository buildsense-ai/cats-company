# PPT / Word 侧边栏预览与就地编辑 · 完整测试清单

给接手验证的 agent 用。读完这一份就能知道：要验什么、怎么验、什么算通过、哪些还没做。

来源：`docs/office-editor/PLAN.md`（实施清单）+ 本轮对话里用户追加的需求 + 代码实际接线情况。
凡是我核实过代码与文档不一致的地方，都标了「⚠ 与清单不符」，不粉饰。

基线仓库：`D:\XiaoBa_clone\cats-company`，分支 `feat/artifact-panel-topic-binding`，基线提交 `c0f6ae1e`。

---

## 一、执行约定（先读这一节，否则会做出无效结论）

### 1.1 判据必须来自被测对象本身

这个项目里，判据分四种强度，从弱到强：

| 级别 | 手段 | 能证明什么 | 不能证明什么 |
|---|---|---|---|
| A | 单元测试 | 内存里逻辑对 | 文件能否被 Office 打开 |
| B | 自己解析回来再读 | 字节写进去了 | 结构是否符合 OOXML 规范 |
| C | 用真实 PowerPoint / Word 打开 | 文件没坏，Office 认 | 用户界面能不能操作 |
| D | 真实浏览器点一遍 | 交互链路通 | 大文件、并发、异常路径 |

**这个项目已经多次出现「A 全绿、C 失败」**。原因是自造夹具和解析器犯同一个错，两边互相掩盖。
所以类和 Word 文件相关的结论，**必须至少到 C 级**才算通过。

### 1.2 不许用这几句话代替证据

- 「测试通过」不说多少条、哪个文件 —— 不算证据
- 「构建成功」—— 只证明能编译
- 「应该可以」「理论上没问题」—— 不算结论
- 「已修复」不说用什么输入复现、修复后哪条断言会变红 —— 不算证据

### 1.3 每条结论要带的东西

编号（下文 T-xx）、实际执行的命令、原始输出片段、判定 PASS / FAIL / NOT RUN。
NOT RUN 就写 NOT RUN，不要写成 PASS。

---

## 二、需求清单与编号

### 2.1 用户原始需求（第一轮提出）

| 编号 | 需求原文意思 | 落地形态 | 状态 |
|---|---|---|---|
| R1 | 侧边栏能预览 PPT | 前端解析 OOXML 渲染，不转 PDF | 已实现 |
| R2 | 侧边栏能预览 Word | 同上 | 已实现 |
| R3 | 能像 PowerPoint / Word 一样就地编辑 | 点画布元素 → 右侧面板改属性 | 已实现 |
| R4 | 改完能保存 | 上传新文件 + 追加附件消息 | 已实现 |
| R5 | 关掉侧边栏后从会话重新点开，看到改后结果 | 新附件消息指向新文件 | 已实现 |
| R6 | 继续聊天时小八基于改后的文件继续改 | 会话消息即版本链，小八读最新附件 | 已实现（未端到端验证） |

一句话验收链（唯一的端到端判据）：

> 侧边栏改一处 → 保存 → 关侧边栏 → 从会话重新点开看到改后 → 继续聊天让小八再改一处 → 小八改的是改后版本。

### 2.2 开发过程中新增的需求

| 编号 | 新增需求 | 来源 | 状态 |
|---|---|---|---|
| R7 | Word 表格的单元格能逐个编辑 | 第二轮追加 | 已实现，真机已验证 |
| R8 | Word 能插入图片 | 第二轮追加 | 已实现，真机已验证 |
| R9 | PPT 组合里的子元素能单独选中 | 第二轮追加 | 已实现，真机已验证 |
| R10 | 代码全部打包到 E 盘、与 gina 文件夹同级 | 第三轮追加 | 已完成（`E:\office-editor-code` + 同名 zip） |
| R11 | 梳理完整测试清单给其他 agent 执行 | 本轮 | 本文档 |

### 2.3 已拍板的范围决策（测试时不要当成缺陷）

| 编号 | 决策 | 测试含义 |
|---|---|---|
| D1 | 预览走前端解析 OOXML，服务端不转 PDF | 不要测 PDF 中转链路，不存在 |
| D2 | 编辑是「点元素 → 右侧面板改」，不在画布上直接打字 | 不要测 canvas 内联输入框，设计上就没有 |
| D3 | 不接 OnlyOffice | 不要测第三方容器、协同、AGPL 相关 |
| D4 | 插入只做四种：文本框、图片、矩形族/直线、表格，加组合 | 不要测插入图表 / SmartArt / 公式 |
| D5 | 继承元素（母版/版式来源）被改，默认只改本页，弹窗问是否改为所有用该版式的页 | 必须测「只改本页」与「改版式」两条路径 |
| D6 | 周期估算只作参考，按清单推进 | 不涉及测试 |

### 2.4 明确不做（不在测试范围）

- 动画、切换效果
- SmartArt、图表内部数据编辑
- 多人同时编辑与冲突合并
- 100% 与 Office 视觉一致（目标只是常见商务文档「看起来一致」）
- 移动端编辑（只做预览）

---

## 三、环境与前置

### 3.1 设备与路径

| 项 | 值 |
|---|---|
| 目标设备 | `zjz`（Windows，device_id `device_b71f6f1a6f60`） |
| 仓库 | `D:\XiaoBa_clone\cats-company` |
| 前端目录 | `D:\XiaoBa_clone\cats-company\webapp` |
| Node | `C:\Program Files\nodejs\node.exe`（v26.7.0）。默认 `node` 是 v20.12.2，跑 Vite 8 会 `ERR_INVALID_ARG_VALUE` |
| 样本目录 | `D:\tmp\office-verify` |
| 代码包 | `E:\office-editor-code`（与 `E:\gina` 同级） |

### 3.2 已知环境坑（会浪费大量时间的几个）

1. **PowerShell 内联长命令会被吞掉 `$`、引号和 `foreach` 变量**。所有多行脚本写成 `.ps1` 再用 `powershell -File` 跑。
2. **单条远程命令同步等待上限 5 分钟**。`vite build` 要写成 `.bat` 后 `Start-Process` 后台跑，再读 log 尾部。
3. **直连 GitHub 超时**，需要 `git -c http.proxy=http://127.0.0.1:7897`。
4. **Word / PowerPoint COM 会挂起并留下进程**（`WINWORD`、`POWERPNT`）。跑之前先 `Get-Process` 检查残留，跑之后确认退出，有残留就 `Stop-Process -Force`。
5. **COM 里 `SaveAs([ref]...)` 报类型转换错误**，改用 `SaveAs2($path, 16)` 字符串路径，并设 `DisplayAlerts=0`、`AutomationSecurity=3`。

### 3.3 必需的样本文件

没有这些文件，C 级判据无法执行。首次执行要先造样本。

| 样本 | 造法 | 校验 |
|---|---|---|
| `sample.pptx` | PowerPoint COM 生成：中文标题、副标题、表格、两个版式 | 37145 字节，2 页，39 parts |
| `sample-table.docx` | Word COM 生成：一段标题 + 2×3 表格 + 一段结尾 | 14099 字节，含 1 个表格 |
| `sample-inserted.pptx` | 对 `sample.pptx` 跑 `office-insert-roundtrip.mjs` | 38677 字节，4 页，含 1 个组合 |

造 docx 的可复用脚本骨架（`make-table.ps1`）：

```powershell
$word = New-Object -ComObject Word.Application
$word.Visible = $false
$word.DisplayAlerts = 0
$doc = $word.Documents.Add()
$table = $doc.Tables.Add($doc.Paragraphs.Add().Range, 2, 3)
$table.Cell(1,1).Range.Text = 'Metric'
# …填满单元格…
$doc.SaveAs2('D:\tmp\office-verify\sample-table.docx', 16)
$doc.Close(0)
$word.Quit()
```

---

## 四、测试分层与执行顺序

按这个顺序跑，前面不过就不要往后跑。

| 层 | 内容 | 耗时 | 前置 |
|---|---|---|---|
| L0 | 单元测试 155 条 | 约 3 秒 | 无 |
| L1 | 构建 | 约 40 秒 | 无 |
| L2 | 脚本级往返（PPT 文本/样式/几何/插入/组合，Word 文本/表格/插图） | 每个约 1 秒 | 样本文件 |
| L3 | 真实 Office 打开读回 | 每个约 4–15 秒 | 样本 + Office 已装 |
| L4 | 真实浏览器交互 | 人工，较慢 | Go 服务端 + dev server |
| L5 | 五步闭环 | 人工，最慢 | 同上 + 可交互会话 |

---

## 五、L0 · 单元测试

### 命令

```cmd
cd /d D:\XiaoBa_clone\cats-company\webapp
"C:\Program Files\nodejs\node.exe" .\node_modules\vitest\vitest.mjs run src/office
```

### 判据

| 项 | 期望 |
|---|---|
| 测试文件数 | 8 |
| 用例数 | 155 |
| 失败数 | 0 |

### 覆盖矩阵（用来判断某条需求有没有单测兜底）

| 测试文件 | 条数 | 覆盖的编号 |
|---|---|---|
| `office.test.js` | 17 | R1、XML 定位、包读写、解析、写回 |
| `style.test.js` | 14 | R3 样式、保存前校验 |
| `geometry.test.js` | 27 | R3 拖动/缩放/吸附/对齐/分布/层级/撤销 |
| `insert.test.js` | 24 | R3 插入、组合、页操作、批量写 |
| `docx.test.js` | 32 | R2 解析、段落写回、样式、增删段 |
| `save.test.js` | 14 | R4、R5 文件名与消息形状 |
| `canvas.test.jsx` | 7 | R3 点选联动、元素列表、继承标记 |
| `advanced.test.js` | 20 | R7、R8、R9 |

⚠ 注意：`geometry.test.js` 里有 `distributeBoxes` 的用例，但界面没有接线（见 8.1）。**单测通过不代表功能可用**。

---

## 六、L1 · 构建

### 命令

写成 `.bat` 后台跑，再读 log：

```bat
cd /d D:\XiaoBa_clone\cats-company\webapp
"C:\Program Files\nodejs\node.exe" .\node_modules\vite\bin\vite.js build > D:\tmp\office-verify\build.log 2>&1
echo EXIT=%ERRORLEVEL% >> D:\tmp\office-verify\build.log
```

### 判据

`build.log` 末尾出现 `EXIT=0`，且无 `error during build`。

出现 `WARN inlineDynamicImports is deprecated` 属已知噪音，不算失败。

---

## 七、L2 / L3 · 脚本级往返与真实 Office 判据

每个用例格式：**要验什么 → 命令 → 判据 → 当前状态**。

### T-01 PPT 结构探针（R1）

```powershell
cd D:\XiaoBa_clone\cats-company\webapp
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-probe.mjs D:\tmp\office-verify\sample.pptx
```

判据：输出页数、元素数、每个元素坐标、继承标记、主题色，且没有 `undefined` / `NaN`。
状态：已通过（2 页 39 parts）。

### T-02 PPT 文本写回（R3）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-roundtrip.mjs D:\tmp\office-verify\sample.pptx D:\tmp\office-verify\sample-edited.pptx
```

判据三条同时成立：
1. 改动区长度变小（5 → 4 字符）
2. 前 1100 字符与后 3567 字符**逐字节不变**
3. 重新解析后文字正确

状态：已通过。输出 35053 字节。

### T-03 PPT 文本真机判据（R3）

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-open-check.ps1 -Path D:\tmp\office-verify\sample-edited.pptx -Expect '改写后的文字' -ReadFormat
```

判据：`RESULT OPENED_CLEAN` + `EXPECT FOUND`。
状态：已通过。

### T-04 PPT 样式写回与真机判据（R3）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-style-roundtrip.mjs D:\tmp\office-verify\sample.pptx D:\tmp\office-verify\sample-styled.pptx
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-open-check.ps1 -Path D:\tmp\office-verify\sample-styled.pptx -ReadFormat
```

判据：真机读回 33pt、bold、`#C86400` 三项全部一致。
状态：已通过。

### T-05 PPT 几何写回与真机判据（R3）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-geometry-roundtrip.mjs D:\tmp\office-verify\sample.pptx D:\tmp\office-verify\sample-moved.pptx
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-open-check.ps1 -Path D:\tmp\office-verify\sample-moved.pptx -ReadGeometry
```

判据：位移 0.4in × 0.2in，真机读回偏差 **< 0.2% 页宽**。
实测：left=88.8pt top=54.4pt width=487.2pt height=44.8pt，偏差 0。
状态：已通过。

### T-06 PPT 插入与页操作（R3）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-insert-roundtrip.mjs D:\tmp\office-verify\sample.pptx D:\tmp\office-verify\sample-inserted.pptx
```

判据六项全 `ok`：

| # | 判据 |
|---|---|
| 1 | sldId 关系可解析 |
| 2 | sldId 唯一（257,256,258,259） |
| 3 | Content-Type 里 slide override 数 = 页数（4） |
| 4 | 形状 id 每页唯一 |
| 5 | 组合保留 2 个子元素 |
| 6 | 图片媒体非空（`ppt/media/image1.png`，70 字节） |

状态：已通过，输出 38677 字节，4 页 44 parts。

### T-07 PPT 插入真机判据（R3）

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-open-check.ps1 -Path D:\tmp\office-verify\sample-inserted.pptx -ReadGeometry
```

判据：`RESULT OPENED_CLEAN`，4 页全部正常，无修复提示。
状态：已通过。

### T-08 组合内子元素单独移动（R9）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-group-child-roundtrip.mjs D:\tmp\office-verify\sample-inserted.pptx D:\tmp\office-verify\sample-group-child.pptx
```

判据四项全 `ok`：

| # | 判据 | 实测 |
|---|---|---|
| 1 | 子元素仍在组内 | yes |
| 2 | 组子元素数不变 | 2 → 2 |
| 3 | 相对坐标位移精确 | dx=365760 dy=182880 |
| 4 | 换算回绝对坐标位移精确 | dx=365760 dy=182880，abs=1097280,4434840 |

状态：已通过，输出 38694 字节。

### T-09 组合内子元素真机判据（R9）

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-group-items-probe.ps1 D:\tmp\office-verify\sample-group-child.pptx
```

判据四条同时成立：

| 项 | 期望 | 实测 |
|---|---|---|
| 文件不坏 | `RESULT OPENED_CLEAN`，4 页 | 一致 |
| 只有目标子元素动 | 57.6,334.8 → 86.4,349.2 | 一致（+0.4in,+0.2in） |
| 兄弟元素不动 | 矩形仍在 252,334.8 | 一致 |
| 其他页不受影响 | 第 3 页同名组仍 57.6,334.8 | 一致 |

状态：已通过。

⚠ 这一条是最容易被假通过骗过的地方。必须用 `office-group-items-probe.ps1` 读**每个子元素**的绝对位置，
不能只看 `office-open-check.ps1` 的顶层 shape 列表 —— 后者不展开组合，看不出子元素动没动。

### T-10 Word 文本与样式写回（R2、R3）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-docx-roundtrip.mjs D:\tmp\office-verify\sample.docx D:\tmp\office-verify\sample-docx-edited.docx
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-docx-open-check.ps1 -Path D:\tmp\office-verify\sample-docx-edited.docx -Expect 'Edited by round-trip harness' -ReadFormat
```

判据：

| 项 | 期望 | 实测 |
|---|---|---|
| 文字 | Edited by round-trip harness | 一致 |
| 字号 | 18 pt | 18 |
| 粗体 | 是 | bold=-1 |
| 颜色 | #C86400 | #C86400 |
| 对齐 | 居中 | align=1 |
| 段数 | 4 段不变 | 4 |

状态：已通过。

### T-11 Word 表格单元格编辑（R7）

```powershell
& 'C:\Program Files\nodejs\node.exe' .\scripts\office-advanced-roundtrip.mjs D:\tmp\office-verify\sample-table.docx D:\tmp\office-verify\sample-table-advanced.docx
```

判据七项全 `ok`：

| # | 判据 | 实测 |
|---|---|---|
| 1 | block 数因插图增加 | 4 → 5 |
| 2 | 单元格文字已改 | `"Metric"` → `"CELL-EDITED"` |
| 3 | 表格形状保持 | 2×3 |
| 4 | drawing 存在 | 1 个 `w:drawing` |
| 5 | media 部件存在 | `word/media/image1.png` |
| 6 | 文档结构合法 | valid clean |
| 7 | 关系登记 | rId6，2160000 EMU 宽 |

状态：已通过，输出 13085 字节。

⚠ 用 `sample.docx`（无表格）跑这条会输出 `cell skipped (no table in this file)`，并"通过"。
那不是通过，是没测到。必须用 `sample-table.docx`。

### T-12 Word 表格与插图真机判据（R7、R8）

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-docx-open-check.ps1 -Path D:\tmp\office-verify\sample-table-advanced.docx -Expect 'CELL-EDITED' -ReadFormat
```

判据：

| 项 | 期望 | 实测 |
|---|---|---|
| 文件不坏 | `RESULT OPENED_CLEAN` | 一致 |
| 单元格文字 | CELL-EDITED | 一致 |
| 表格结构 | 2 行 3 列 | 一致 |

状态：已通过。

### T-13 Word 插图是真图不是破图占位（R8）

**这一条必须单独跑。** Word 里图片解析失败会显示破图占位，**不一定报错**，
所以 `OPENED_CLEAN` 不能证明图片可用。

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\office-shape-check.ps1 D:\tmp\office-verify\sample-table-advanced.docx
```

判据：

| 项 | 期望 | 实测 |
|---|---|---|
| InlineShapes 数 | 1 | 1 |
| 形状类型 | `type=3`（图片） | 3 |
| 实际尺寸 | 非 0，成比例 | 170.1 × 170.1 pt |

状态：已通过。

⚠ 若脚本不存在，用 `shape-check.ps1` 的思路自己写：`$doc.InlineShapes.Count` 与逐项 `Type/Width/Height`。

---

## 八、L4 · 界面交互（真实浏览器）

这一层全部 **NOT RUN**，需要 Go 服务端 + webapp dev server。以下是执行清单。

### 8.1 ⚠ 先说两个核实到的缺口

跑之前必须知道这两条，否则会以为是测试方法错：

**缺口一：多元素对齐与分布没有接线。**

- `pptx/geometry.js` 有 `alignBoxes` / `distributeBoxes`，单测 27 条里都覆盖了
- `OfficePreview.jsx` 的 `handleAlign` 调用 `alignBoxes([单个元素])`，而 `alignBoxes` 在长度 < 2 时**直接原样返回**
- `PropertyPanel` 收到了 `onAlign` 这个 prop，但内部没有使用它；`GeometryFields` 只接收 `onAlignToSlide`
- `distributeBoxes` **没有任何界面调用点**

结论：界面上只有「相对页面六向对齐」（即 `onAlignToSlide`，把单个元素贴到页边或页中）可用。
「多选后互相左对齐」「等间距分布」这两件事**代码在库里、界面没接**。
PLAN 3.3 标了 `[x]`，与代码不符，已在本文档记录。

**缺口二：PPT 表格的单元格文字不可编辑。**

`PropertyPanel` 的 `TableFields` 明说「单元格内容编辑在表格阶段开放，当前只能改整体位置与尺寸」。
表格单元格编辑只做在 **Word** 侧（R7），PPT 侧没做。测试时不要把这条算成 R7 失败。

### 8.2 T-14 预览准入（R1、R2）

| 步骤 | 判据 |
|---|---|
| 会话里贴一个 `.pptx` 附件，点预览 | 侧边栏出现演示文稿画布，不是「只能下载」卡片 |
| 贴一个 `.docx` 附件，点预览 | 侧边栏出现文档流式渲染 |
| 贴一个 `.ppt`（旧二进制格式） | 回落到下载卡片，不尝试预览 |
| 卡片标签 | PPT 显示 `PPT` / `可预览与编辑的演示文稿`；Word 显示 `Word` / `可预览与编辑的文档` |

状态：NOT RUN。代码位置 `chat-message.jsx` L63–L64、L2332–L2346、L2376–L2387。

### 8.3 T-15 点选联动（R3）

| 步骤 | 判据 |
|---|---|
| 点画布上一个元素 | 右侧面板切换到该元素的字段；画布出现选择框 |
| 点左侧元素列表里另一项 | 画布选择框跟到该元素 |
| 悬停列表项 | 画布上对应元素高亮 |
| 点空白 | 清除选择 |

状态：NOT RUN。单测 `canvas.test.jsx` 断言了 `data-office-el` 与 anchor 路径可解析回同一节点。

### 8.4 T-16 多选（R3）

| 步骤 | 判据 |
|---|---|
| 按住 Shift 点第二个元素 | 选择集增加，不替换 |
| 再次 Shift 点已选元素 | 从选择集移除 |
| 选中 ≥ 2 个元素 | 「组合」按钮可用 |

状态：NOT RUN。单测断言了 shift-click 走多选分支。

### 8.5 T-17 拖动与缩放（R3）

| 步骤 | 判据 |
|---|---|
| 拖动元素 | 跟随鼠标，松手落到新位置 |
| 缓慢拖 20 次然后松手 | **不累积漂移**（锚点取手势开始时的盒子） |
| 拖侧边栏改宽度后再拖元素 | 位移与鼠标一致（换算因子每次现测，不是缓存常量） |
| 拖西北角手柄 | 原点跟着移动，对角固定 |
| 把手柄拖过对边 | 不反转成负尺寸 |
| 缩到极小 | 不小于 1pt（12700 EMU） |
| 拖出页面 | 至少留一丝在页内，仍可抓回 |

状态：NOT RUN。单测逐条覆盖（`geometry.test.js`），界面未手验。

### 8.6 T-18 属性面板编辑（R3）

| 类别 | 字段 | 判据 |
|---|---|---|
| 文本 | 段落文字 | 改完画布即时更新 |
| 样式 | 字号 / 颜色 / 粗体 / 斜体 / 下划线 / 对齐 | 改完画布即时更新 |
| 几何 | 位置（cm）、尺寸（cm） | 输入后元素移动/缩放；尺寸下限 0.1cm |
| 几何 | 相对页面的左/中/右/上/中/下六个按钮 | 元素贴到对应页边或页中 |

状态：NOT RUN。

### 8.7 T-19 继承元素与 D5 弹窗（R3）

| 步骤 | 判据 |
|---|---|
| 点一个继承自版式的元素（列表里有「继承」标记） | 面板提示位置继承自版式 |
| 改它的位置 | 弹 `InheritedConfirm`，两个选项：只改这一页 / 改所有使用该版式的页 |
| 选「只改这一页」 | 保存后**其他页不变**，本页脱离版式定位 |
| 选「改版式」 | 保存后**所有用该版式的页一起变** |
| 点取消 | 不改任何东西 |

状态：NOT RUN。**这是风险最高的一条**：改错范围会静默改掉别的页。
单测覆盖了「拒绝在继承元素上直接写几何」，弹窗两条写路径没有端到端验证。

### 8.8 T-20 插入元素（R3、D4）

| 插入项 | 判据 |
|---|---|
| 文本框 | 出现可编辑文本框，初始有占位文字 |
| 矩形 | 实心矩形 |
| 圆角矩形 | 带圆角 |
| 直线 | 水平细线，可拖动 |
| 表格 | 默认 3×3，含 `a:tblGrid` 且列宽个数 = 单元格数 |
| 图片 | 从本地选图，插入后画布显示真图不是空框 |
| 连续插两次图片 | 两个独立媒体部件（`image1.png`、`image2.png`），不互相覆盖 |
| 插入后形状 id | 与已有 id 不冲突（扫最大值加一） |

状态：NOT RUN（脚本级已验 6 项结构判据，界面未点）。菜单项见 `InsertMenu.jsx` 的 `PRIMITIVES`。

### 8.9 T-21 组合与组内子元素（R9）

| 步骤 | 判据 |
|---|---|
| 选两个元素点「组合」 | 变成一个组，画布位置不变 |
| **点组内的子元素** | 选中**子元素本身**，不是整个组 |
| 拖动组内子元素 | 只有它动，兄弟元素不动 |
| 保存后用 PowerPoint 打开 | 无修复提示，位置准确 |

状态：NOT RUN（脚本级 T-08/T-09 已验）。界面点选是这一条的核心，必须在浏览器里点。

### 8.10 T-22 层级（R3）

| 操作 | 判据 |
|---|---|
| 置顶 / 置底 / 上移 / 下移 | 叠放顺序变化，且不是写属性而是移动节点 |
| 操作后保存 | PowerPoint 打开无修复提示 |
| 置顶时 `p:nvGrpSpPr`、`p:grpSpPr` 位置 | 仍在所有形状之前 |

状态：NOT RUN（单测覆盖节点重排与「非形状子节点保持原位」）。

### 8.11 T-23 页操作（R3）

| 操作 | 判据 |
|---|---|
| 加页 | 新页有独立关系、Content-Type 与 sldId |
| 删页 | 部件、关系、Content-Type 一起删干净 |
| 删最后一页 | 拒绝，并给出提示（不允许 0 页） |
| 复制页 | 内容一致 |
| 页上移 / 下移 | 只改 `sldIdLst` 顺序，重新解析后顺序正确 |
| 移到原位 | 无操作，不产生变更 |

状态：NOT RUN（脚本级 T-06 已验结构）。

### 8.12 T-24 撤销重做（R3）

| 步骤 | 判据 |
|---|---|
| 改三次，撤销两次 | 回到第一次改完的状态 |
| 撤销后重新编辑 | 重做分支被丢弃，不能再前进 |
| 连续拖 60 次以上 | 历史不无限增长（上限 60） |
| 拖动过程 | 一次拖动只产生**一条**历史（松手才记），不是每一帧一条 |

状态：NOT RUN。上限常量 `UNDO_LIMIT = 60`，见 `ui/history.js` L11。

### 8.13 T-25 Word 侧面板（R2、R3）

| 类别 | 字段 | 判据 |
|---|---|---|
| 段落 | 段落内容 | 改完即时更新 |
| 样式 | 对齐 / 左缩进 cm / 首行缩进 cm / 字形 / 字号 pt / 颜色 / 下划线 | 改完即时更新 |
| 结构 | 插入段落 / 删除段落 | 增删生效；删最后一段被拒绝 |
| 表格 | 点单元格 → 面板出现「单元格 第x行第y列」→ 改「内容」 | 只改该单元格，其他单元格不动 |
| 图片 | 「插入图片」选本地图 | 插入到正文，等比缩放到正文可用宽度 |

状态：NOT RUN。单元格与插图脚本级已验（T-11、T-12、T-13）。

Word 侧**不应有**拖动、层级、对齐到边界 —— Word 文件不存坐标，只存顺序。测试时不要把这些列为缺失。

---

## 九、L5 · 五步闭环（最高优先级的未完成项）

这是用户原始需求 R4、R5、R6 的唯一端到端判据，**当前 NOT RUN**。

### 前置

1. 起 Go 服务端
2. 起 webapp dev server（后端代理 `http://localhost:6061`，小八 `http://127.0.0.1:3800`）
3. 有一个可交互的会话，会话里有一个 pptx 附件

### T-26 五步闭环

| 步 | 动作 | 判据 |
|---|---|---|
| 1 | 侧边栏打开 pptx，改一处文字 | 画布显示改后内容，出现「已修改」标记 |
| 2 | 点保存 | 提示「已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版」 |
| 3 | 会话列表 | **新增一条附件消息**，原消息与旧附件**没有被改动** |
| 4 | 关掉侧边栏，从会话重新点开新附件 | 看到改后内容 |
| 5 | 继续聊天，让小八再改一处 | 小八读到的是**改后版本**，不是原始版本 |

### 附带必须验的异常路径

| 场景 | 判据 |
|---|---|
| 没有任何改动就点保存 | 提示「没有可写入的改动」，不上传空文件 |
| 上传失败 | 提示「已写入本地，但没能存进会话，请重试保存」，**不能声称已保存** |
| 写入前校验不通过 | 报错 `写入前的检查未通过：…`，不产出坏文件 |
| 连续多次保存 | 每次都是新附件消息，旧消息全部可达 |
| 文件名 | 保持原名，不变成 `.pptx.pptx` 或 `.doc` 名装 pptx 字节 |

### 为什么这条一直没跑

需要同时起两个服务并有人点。代码链路与 155 条单测覆盖了消息形状、文件名修复、空字节拒绝，
但**端到端没有亲手跑过**，所以不能声称通过。

---

## 十、回归防线（已修 bug，每条都要有对应用例）

下面每一条都是真实踩过、并且**只有真实 Office 或真实文件才暴露出来**的。测试时必须防回退。

### 10.1 PPT 侧

| # | bug 形态 | 判据用例 | 防线 |
|---|---|---|---|
| B1 | 只读 `p:spPr/a:xfrm`，所有表格变成 0×0 | T-01 输出表格坐标非 0 | `office-probe.mjs` |
| B2 | `sysClr` 的 `val` 是 `windowText` 这种系统名，被当颜色 | T-01 主题色是 `#RRGGBB` | 单测 + 探针 |
| B3 | 继承占位符 `idx` 与版式不一致时解析不到坐标 | T-19 继承元素能定位 | 单测 |
| B4 | `save()` 只遍历读入时的条目，新建 slide / media 不出现在输出里 | T-06 第 1、3 项 + 真机能开 | `office-insert-roundtrip.mjs` |
| B5 | 图片写路径而不是 `r:embed` 关系 id，显示空框 | T-06 第 6 项 + T-20 插图 | 插入往返 + 真机 |
| B6 | 表格缺 `a:tblGrid` 或列宽数 ≠ 单元格数 | T-06 + T-20 表格 | `validateSlidePart` |
| B7 | `p:nvGrpSpPr` / `p:grpSpPr` 被当成形状槽位，重排后结构损坏 | T-22 真机无修复提示 | 单测 `keeps the non-shape children in place` |
| B8 | 拖动锚点取上一帧，舍入误差累积，元素逐渐偏离鼠标 | T-17 缓慢拖 20 次 | 单测 `accumulates from the gesture anchor` |
| B9 | EMU/像素换算因子被缓存，侧边栏拖宽后每次拖动都偏 | T-17 拖侧边栏后再拖 | 单测 `derives EMU per pixel from the live rect` |
| B10 | **`a:chExt` 读 `x/y` 而不是 `cx/cy`**，extent 恒为 0，几何换算函数走保护分支原样返回，组内子元素拖动被静默吞掉 | T-08 换算精确 + T-09 真机位移 | `office-group-child-roundtrip.mjs` |
| B11 | **组内子元素 `elId` 与顶层元素撞号**（都叫 `slides-slide1#0`），写入改到无关元素 | T-08 判据 3、4 + 代码里子元素 id 形如 `slides-slide1#2/0` | 同上 |
| B12 | `nextMediaNumber` 只匹配 `ppt/media/`，Word 第二张图覆盖第一张 | T-20 连续插两张图 + 单测 `gives a second picture its own media part` | 单测 |

B10、B11 是本轮新修的，也是最容易再犯的：它们都是「单测全绿、Office 里错」，
因为自造夹具和解析器犯了同一个错。

### 10.2 Word 侧

| # | bug 形态 | 判据用例 | 防线 |
|---|---|---|---|
| B13 | `w:sz` 按属性读（实际是子元素），所有字号显示 0 | T-10 字号读回 18 | `office-docx-open-check.ps1 -ReadFormat` |
| B14 | `w:rPr` 内插入顺序错，反复改样式导致字节差异越来越长 | T-10 多次改样式后字节稳定 | 单测 |
| B15 | 「设为无值」与「删除属性」共用 `null` 语义，加粗反而删掉 `<w:b/>` | T-10 粗体读回 -1 + 单测 `clears bold by removing the attribute` | 单测 |
| B16 | 表格单元格写回时把其他单元格也改了 | T-11 判据 2 + 单测 `leaves the others alone` | 单测 |
| B17 | 插图未登记关系 / 未声明扩展名，Word 显示破图 | T-12 + T-13 | `office-advanced-roundtrip.mjs` |

---

## 十一、异常与边界（容易漏，但都会在真实使用中出现）

| 编号 | 场景 | 判据 |
|---|---|---|
| T-27 | 上传空字节 | 拒绝，不产出空媒体部件（单测已有） |
| T-28 | 用户输入含 `&` `<` `>` | XML 转义正确，文档不被破坏（单测已有） |
| T-29 | 用户输入含前导空格 | `xml:space="preserve"`，空格保留（单测已有） |
| T-30 | 传入非 docx / 非 pptx 包 | 明确报错，不当作空文档渲染（单测已有） |
| T-31 | 批量操作里混入页操作与元素操作 | 拒绝（单测已有） |
| T-32 | 批量里两条结构性操作 | 拒绝（单测已有） |
| T-33 | 未知操作类型 | 明确拒绝，不静默跳过（单测已有） |
| T-34 | 侧边栏宽度为 0 / 画布塌陷 | 返回 0 而不是 NaN 或 Infinity（单测已有） |
| T-35 | 大文件（20 MB 以上） | **NOT RUN，未测量**。60 MB 上限是估的 |
| T-36 | 中文换行 | 与 Office 不一致可接受，但不能溢出容器。**NOT RUN，未系统测** |

---

## 十二、缺陷 / 缺口总表（当前已知，全部保持未勾）

| # | 项 | 影响 | 建议 |
|---|---|---|---|
| G1 | 五步闭环未端到端跑 | R4/R5/R6 全部只有代码级证据 | 最高优先级，见第九节 |
| G2 | 多元素对齐与分布未接线 | 单测通过但界面没有入口；PLAN 3.3 标 `[x]` 与代码不符 | 要么接线，要么把 3.3 拆成「相对页面」与「元素间」两项分别标注 |
| G3 | PPT 表格单元格文字不可编辑 | 只有 Word 侧做了 | 若需要，按 Word 侧同构补 |
| G4 | `/uploads/` 无需鉴权 | 既有问题，非本次引入；编辑上线会把它固化 | 上线前确认附件隐私策略 |
| G5 | 大文件性能未测 | 60 MB 上限无实测支撑 | 造 20 / 40 / 60 MB 样本各测一次解析与首屏渲染耗时 |
| G6 | 中文换行与 Office 不一致 | 可能提前折行或溢出 | 接受差异，界面上明说；用真实样本回归 |
| G7 | 知识库未归档 | 下次接手要重读文档 | 归档决策与真实 bug 清单 |

---

## 十三、给执行 agent 的收尾要求

交报告时逐条给编号与状态，格式：

    T-08 PASS  命令：…  证据：dx=365760 dy=182880，WRITE OK
    T-19 NOT RUN  原因：需要起 Go 服务端 + dev server

不要这样写：

    ✅ 全部通过
    ✅ 测试完成，没有发现问题
    ⚠️ 大体上可用

如果真的发现了新 bug，按这个格式补进第十节：

    B18 [PPT / Word] 现象：…  复现：…  根因：…  修复后哪条断言会变红：…

判据不通过就停在那一步，不要往下走，也不要把「跳过」写成「通过」。
