# PPT / Word 侧边栏预览与就地编辑 · 验收报告

验收日期：2026-11-15（Asia/Shanghai）

执行身份：本次验收的执行 agent

工作根：`D:\XiaoBa_clone\cats-company`

状态值：只使用 `PASS` / `FAIL` / `NOT RUN`

## 1. 结论与边界

L0 至 L5 均已在本机重跑并通过；T-01 至 T-36 逐条结论均为 `PASS`。本次验收在真实的本地前端与生产 API、真实会话及真实 Office 桌面程序中完成，不是只引用作者自测数字。

边界必须明确：

- 这是“**本地前端代码 + 生产 API 与数据**”的验证，不是“已部署版本”的验证。
- 本次没有验证“本地后端”。因此上传接口字段、附件不可变语义、会话消息落库等结论，验证对象是生产后端，不是本地编译出的后端。
- 不能把本报告写成“整条部署链路已验”。前端由 `http://localhost:5173/` 提供，API 目标是 `https://app.catsco.cc`。
- 本次没有记录账号、密码或任何凭据明文。

## 2. 环境与前置

| 项 | 实测 |
|---|---|
| 前端 | Vite `http://localhost:5173/`，`Get-NetTCPConnection` 显示 5173 正在监听 |
| 后端 | 生产 API/数据，`VITE_BACKEND_TARGET=https://app.catsco.cc` |
| 浏览器 | Chrome，持久 profile `D:\tmp\office-verify\chrome-profile` |
| 登录 | 已用桌面凭据文件登录成功；报告不记录账号与密码 |
| Node | `C:\Program Files\nodejs\node.exe` |
| Office | 本机 PowerPoint / Word COM 自动化 |
| 证据目录 | `D:\tmp\office-verify` |

## 3. 闸门结论

| 闸门 | 状态 | 证据 |
|---|---|---|
| L0 单元测试 | PASS | `8 passed (8)`、`166 passed (166)`、退出码 0；见 `L0-acceptance-final.log` |
| L1 构建 | PASS | `✓ built in 33ms`、`EXIT=0`；见 `accept-build-final.log` |
| L2 脚本级往返 | PASS | T-01 至 T-13 的脚本判据全部为 `ok` / `WRITE OK` / `STRUCTURE OK` |
| L3 真实 Office 读回 | PASS | T-03、T-07、T-09、T-12 均为 `RESULT OPENED_CLEAN`；T-13 另用 `InlineShapes` 验真图 |
| L4 浏览器交互 | PASS | T-14 至 T-25 均在真实浏览器、真实会话附件上完成 |
| L5 五步闭环 | PASS | 在真实“24小时天选打工仔”机器人任务完成；新附件可重开，机器人基于新版本继续修改并返回可开文件 |

### 3.1 L0 数字基线说明（不静默改判据）

交接文档写的 L0 基线是 `8 文件 / 155 用例 / 0 失败`。当前代码树实际重跑为：

```text
 Test Files  8 passed (8)
      Tests  166 passed (166)
```

这是 11 条基线漂移，不是跳过或放宽：当前树比交接时的 155 条多了 11 条已有/新增回归覆盖。其中可明确归因的是 B18 的 1 条与 B19 的 1 条；其余 9 条属于当前树中已有的额外回归覆盖。没有删除、跳过或把失败改绿。8 个测试文件、166 条用例、0 失败，满足“至少 155 条且全部通过”的实质闸门；这里保留差异记录，供复核。

### 3.2 T-13 与 L3 的表述

T-13 的指定工具 `office-shape-check.ps1` 输出的是 `RESULT OPENED`，不是 `RESULT OPENED_CLEAN`。该工具用于读取 `InlineShapes`，不是 clean-open 检查；同一输出文件的可打开性与表格内容由 T-12 的 `RESULT OPENED_CLEAN` 覆盖。因此 L3 仍然通过，但报告中不把 T-13 伪写成 clean-open。

## 4. 逐条结论表（11.1）

| 编号 | 状态 | 命令/手段 | 证据（原始输出片段） |
|---|---|---|---|
| T-01 | PASS | `office-probe.mjs sample.pptx` | `slides 2`、`parts 39`、表格 `3x3`；坐标均为数值，无 `NaN`/`undefined` |
| T-02 | PASS | `office-roundtrip.mjs` | `37145 -> 35057 bytes`；`第二页标题文字 -> 验证写入成功`；`1100 leading + 3565 trailing chars identical`；`reparse OK` |
| T-03 | PASS | `office-open-check.ps1 -Expect 验证写入成功` | `RESULT OPENED_CLEAN`、`slides 2`、`EXPECT FOUND 验证写入成功` |
| T-04 | PASS | `office-style-roundtrip.mjs` + `office-open-check.ps1 -ReadFormat` | Office 读回 `size=33 bold=-1 color=#C86400`，与期望一致 |
| T-05 | PASS | `office-geometry-roundtrip.mjs` + `office-open-check.ps1 -ReadGeometry` | `left=88.8 top=54.4 width=487.2 height=44.8`；`drift=0` |
| T-06 | PASS | `office-insert-roundtrip.mjs` | `4 slides`、`44 parts`、sldIds `257,256,258,259`、group `2 children`、`ppt/media/image1.png 70 bytes` |
| T-07 | PASS | `office-open-check.ps1 -ReadGeometry` | `RESULT OPENED_CLEAN`、`slides 4`，四页几何均可读 |
| T-08 | PASS | `office-group-child-roundtrip.mjs` | 相对/绝对位移均为 `dx=365760 dy=182880`；子元素仍在组内，组子元素数 `2 -> 2` |
| T-09 | PASS | `office-group-items-probe.ps1` | `OPENED_CLEAN`、4 页；子元素 `57.6,334.8 -> 86.4,349.2`；兄弟仍 `252,334.8`；第 3 页不变 |
| T-10 | PASS | `office-docx-roundtrip.mjs` + `office-docx-open-check.ps1 -ReadFormat` | 4 段不变；`size=18 bold=-1 color=#C86400 align=1` |
| T-11 | PASS | `office-advanced-roundtrip.mjs sample-table.docx` | `Metric -> CELL-EDITED`、表格 `2x3`、`word/media/image1.png`、`rId6`、`2160000 EMU` |
| T-12 | PASS | `office-docx-open-check.ps1 -Expect CELL-EDITED` | `RESULT OPENED_CLEAN`、`table idx=1 rows=2 cols=3`、`EXPECT FOUND CELL-EDITED` |
| T-13 | PASS | `office-shape-check.ps1` | `inlineShapes 1`、`shape 1 type=3 w=170.1 h=170.1`、`table1 rows=2 cols=3 cell11=[CELL-EDITED]` |
| T-14 | PASS | 真实浏览器 + `ui-t14-current.log` | PPT/Word 卡片出现并可打开；PPT 为 `1 / 2`，Word 为 `4 块`；`.ppt` 显示 `暂不支持预览` 且下载链接保留 |
| T-15 | PASS | `ui-core2-final.json` T15 | 画布选中 `slides-slide1#1`；列表选中 `slides-slide1#0`；hover outline 存在；空白清除后 `selected=""` |
| T-16 | PASS | `ui-core2-final.json` T16 | `picked:"已选 2"`、`groupEnabled:true`；再次 Shift 后 `picked:""`，三个检查均 true |
| T-17 | PASS | `ui-t17-final.json` | `slowFollows/noDrift/panelResized/liveFactor/nwFixed/noNegative/minOnePoint/clamped` 全 true；面板拖动后 `expX=1.7943 dx=1.80` |
| T-18 | PASS | `ui-core-final.json` T18 | 文字/样式/几何即时更新；六个页面方向真实值为左 `0`、横中 `10.7`、右 `21.4`、上 `0`、纵中 `8.8`、下 `17.6` cm |
| T-19 | PASS | `ui-t19b-final.json` + `t19-only-current.pptx` 解析 | 弹窗含“只改这一页/所有使用该版式的页”；only 页 `3.17 -> 1.5`、另一页仍 `3.17`；layout-wide 两页均到 `6`；文件解析为 `x=540000` 与另一页 `x=1143000` |
| T-20 | PASS | `ui-t20-current.json` | 插入 7 个元素；媒体 `image1.png`、`image2.png`；shapeId `1..10` 唯一；表格 `gridCols=3` |
| T-21 | PASS | `ui-t21-final.json` + `t21-group-final.log` | 组框稳定；选中子元素 `slides-slide2#0/0`；子元素 `+20,+10`；兄弟/组框不动；PowerPoint `OPENED_CLEAN`，子文本框 `91.8,55.9pt` |
| T-22 | PASS | `ui-t22-current.log` | 目标 id `7` 索引 `4 -> 5` 与 `4 -> 3`；`nvBefore=true`、`grpBefore=true`；保存文件可开 |
| T-23 | PASS | `ui-core2-final.json` T23 | 复制 `4 -> 5`、下移/上移、删除 `5 -> 4`、加页 `4 -> 5` 全部成立；最后一页提示 `这是最后一页，不能再删` |
| T-24 | PASS | `ui-t24-final.json` | 撤销两次回到 `历史 B`；重做分支清除；一次拖动只一条历史；65 次修改后 `限值 4`，`limited60:true` |
| T-25 | PASS | `ui-t25-final.json` + Office 读回 | 段落/样式/缩进/字形/字号/颜色更新；单元格 `2 weeks -> T25-CELL-EDITED`；插图成功；最后一段删除被拒；Word 读到更新内容与 1 张内联图 |
| T-26 | PASS | `ui-t26-l5-one-topic.log` / `ui-t26-l5.json` / `t26-l5-bot-open.log` | 五步全 true；附件数 `1 -> 2`；保存提示为规定全文；重开有编辑；机器人 50,143 ms 后返回 `accept-t06-BOT.pptx`，`T26-L5-BOT` 可读且 `OPENED_CLEAN` |
| T-27 | PASS | `vitest -t "rejects empty bytes"` | `2 passed | 164 skipped`；拒绝空媒体部件与空上传 |
| T-28 | PASS | `vitest -t "escapes"` | `3 passed | 163 skipped`；`&`、`<`、`>` 转义用例通过 |
| T-29 | PASS | `vitest -t "xml:space=preserve"` | `1 passed | 165 skipped`；前导空格保留 |
| T-30 | PASS | `vitest -t "not a pptx|not a docx"` | `2 passed | 164 skipped`；非包类型明确拒绝 |
| T-31 | PASS | `vitest -t "mixes slide and element"` | `1 passed | 165 skipped`；混合批次拒绝 |
| T-32 | PASS | `vitest -t "two structural operations"` | `1 passed | 165 skipped`；双结构操作拒绝 |
| T-33 | PASS | `vitest -t "unknown operation"` | `1 passed | 165 skipped`；未知操作不静默跳过 |
| T-34 | PASS | `vitest -t "collapsed canvas"` | `1 passed | 165 skipped`；返回 0 而非 `NaN`/`Infinity` |
| T-35 | PASS | `ui-t35-accept.json` | 20 MB `1,631 ms`、40 MB `3,341 ms`、60 MB `4,460 ms`；三者 `loadedClean:true`、`1 / 4`、`rows:2` |
| T-36 | PASS | `ui-t36-accept.json` | 普通、长中文、无空格中文三组的 `WithinCanvas` 与 `NoHorizontalOverflow` 六项全 true |

## 5. 关键原始证据

### 5.1 L0 单元测试

命令：

```powershell
cd D:\XiaoBa_clone\cats-company\webapp
& 'C:\Program Files\nodejs\node.exe' .\node_modules\vitest\vitest.mjs run src/office
```

原始输出（`D:\tmp\office-verify\L0-acceptance-final.log`）：

```text
 RUN  v4.1.12 D:/XiaoBa_clone/cats-company/webapp

 Test Files  8 passed (8)
      Tests  166 passed (166)
   Duration  2.46s
```

### 5.2 L1 构建

原始输出末尾（`D:\tmp\office-verify\accept-build-final.log`）：

```text
computing gzip size...
build/sw.mjs  18.49 kB │ gzip: 6.26 kB
✓ built in 33ms
PWA v1.3.0
EXIT=0
```

### 5.3 T-01 至 T-06（PPT 结构、文本、样式、几何、插入）

```text
file        sample.pptx
bytes       37145
parts       39
slide 2  ppt/slides/slide2.xml
  - table           表格 3×3      table=3x3

in         sample.pptx (37145 bytes)
out        final-sample-edited.pptx (35057 bytes)
element    slides-slide2#0  "第二页标题文字"
edit       "验证写入成功"
changed    7 chars -> 6 chars
untouched  1100 leading + 3565 trailing chars identical
reparse    OK
WRITE OK

RESULT OPENED_CLEAN
slides 2
shape slide=2 idx=1 type=17 text=验证写入成功
EXPECT FOUND 验证写入成功

ok    size   3300
ok    bold   true
ok    color  "#C86400"
format slide=2 idx=1 size=33 bold=-1 italic=0 color=#C86400

ok    x   saved=1127760 expected=1127760 drift=0
ok    y   saved=690880 expected=690880 drift=0
ok    cx  saved=6187440 expected=6187440 drift=0
ok    cy  saved=568940 expected=568940 drift=0
geom slide=2 idx=1 left=88.8 top=54.4 width=487.2 height=44.8 type=17

slides     4
parts      39 -> 44
ok    sldId unique   257,256,258,259
ok    group intact   2 children
ok    picture media  ppt/media/image1.png 70 bytes
STRUCTURE OK
```

### 5.4 T-07 至 T-09（PPT 真机、组合子元素）

```text
RESULT OPENED_CLEAN
slides 4
geom slide=1 idx=1 left=60 top=40 width=480 height=41.2 type=17
geom slide=2 idx=3 left=57.6 top=334.8 width=367.2 height=54 type=6

checks
  ok    child still inside group yes
  ok    group children count     2 -> 2
  ok    relative dx              365760
  ok    relative dy              182880
  ok    absolute dx              365760 vs 365760
  ok    absolute dy              182880 vs 182880
  ok    absolute after           1097280,4434840

slides 4
group slide=2 name=组合 9 left=57.6 top=334.8 items=2
  item name=文本框 4 type=17 left=86.4 top=349.2 w=172.8 h=54
  item name=矩形 5 type=1 left=252 top=334.8 w=172.8 h=54
group slide=3 name=组合 9 left=57.6 top=334.8 items=2
  item name=文本框 4 type=17 left=57.6 top=334.8 w=172.8 h=54
  item name=矩形 5 type=1 left=252 top=334.8 w=172.8 h=54
```

### 5.5 T-10 至 T-13（Word）

```text
RESULT OPENED_CLEAN
paragraphs 4
tables 0
para idx=1 style=正文 text=Edited by round-trip harness
format idx=1 size=18 bold=-1 italic=0 color=#C86400 align=1
EXPECT FOUND Edited by round-trip harness

in         sample-table.docx (14099 bytes)
out        final-sample-table-advanced.docx (13085 bytes)
ok    cell text     "CELL-EDITED"
ok    table shape   2x3
ok    media part    word/media/image1.png
ok    picture rel   rId6 -> media/image1.png
ok    picture width 2160000 EMU
WRITE OK

RESULT OPENED_CLEAN
tables 1
table idx=1 rows=2 cols=3
EXPECT FOUND CELL-EDITED

RESULT OPENED
inlineShapes 1
shape 1 type=3 w=170.1 h=170.1
tables 1
table1 rows=2 cols=3 cell11=[CELL-EDITED]
```

### 5.6 L4 核心交互

T-14：

```text
ppt: "sample.pptx\nPPT · 36.3 KB\n预览\n下载"
word: "sample-table.docx\nWord · 13.8 KB\n预览\n下载"
legacy.previewDisabled: true
legacy.previewTitle: "暂不支持预览"
pptState: { label: "1 / 2", shapeCount: 2 }
wordState: { label: "4 块 · 0 个标题" }
checks: { pptCard:true, wordCard:true, pptPreview:true, wordPreview:true, legacyFallback:true }
```

T-15 / T-16：

```text
canvasSelects: true
listSelects: true
hoverHighlights: true
blankClears: true
addsWithoutReplacing: true
groupEnabled: true
togglesOff: true
picked: "已选 2"
```

T-17：

```text
slow: { 左:4.23, 上:2.41 }
expX: 2.1561, dx: 2.11, expY: 0.9999, dy: 1.0
panelBefore: 820 px, panelAfter: 790 px
afterResizeDrag expX: 1.7943, dx: 1.80
oppositeBefore: [19.05, 2.86], oppositeAfter: [19.05, 2.86]
visibleWidth: 1.69 cm, visibleHeight: 0.14 cm
slowFollows/noDrift/panelResized/liveFactor/nwFixed/noNegative/minOnePoint/clamped: true
```

T-18：

```text
canvasText: "画布即时更新测试"
fontSize: "41.3333px", fontWeight: "700", fontStyle: "italic"
textDecoration: "underline", color: "rgb(18, 52, 86)", textAlign: "center"
geometry after: { 左:2.5, 上:1.41, 宽:4, 高:1.45 }
alignResults: { 靠左:0, 水平居中:10.7, 靠右:21.4, 靠上:0, 垂直居中:8.8, 靠下:17.6 }
```

T-19：

```text
dialog: "这个元素来自版式 ... 取消 所有使用该版式的页 只改这一页"
only: { original:3.17, onlyP2:1.5, onlyP3:3.17 }
all: { allP2:6, allP3:6 }
checks: { onlyMovesThisPage:true, onlyLeavesOtherPage:true, allMovesBothPages:true }

parse of t19-only-current.pptx:
ppt/slides/slide1.xml {"x":540000,...}
ppt/slides/slide3.xml {"x":1143000,...}
```

T-20 至 T-23：

```text
T20 inserted: 7 elements
T20 media: ["ppt/media/image1.png","ppt/media/image2.png"]
T20 shapeIds: ["1","2","3","4","5","6","7","8","9","10"]
T20 gridCols: 3

T21 selected: "slides-slide2#0/0"
T21 childBefore: x=706.734375 y=251.71875
T21 childAfter:  x=726.71875  y=261.703125
T21 siblingBefore/After: x=706.734375 y=314.625
T21 groupBefore/After: x=706.734375 y=251.71875

T21 Office:
RESULT OPENED_CLEAN
slides 4
item name=TextBox 1 type=17 left=91.8 top=55.9

T22 up.targetIndex: 5, ids: ["2","3","9","6","8","7"]
T22 down.targetIndex: 3, ids: ["2","3","9","7","6","8"]
T22 nvBefore/grpBefore: true

T23 duplicate: 4 -> 5; moveDown/moveBack: true; delete: 5 -> 4; add: 4 -> 5
T23 last: "这是最后一页，不能再删"
```

T-24 / T-25：

```text
T24 afterUndo: "历史 B"
T24 redoDisabled: true
T24 dragBefore: { 左:2.12, 上:1.41 }
T24 dragAfter:  { 左:3.52, 上:1.97 }
T24 dragUndo:   { 左:2.12, 上:1.41 }
T24 limitValue: "限值 4"
T24 checks: { undoTwice:true, redoBranchCleared:true, dragOneHistory:true, limited60:true }

T25 afterText: "T25 段落已更新"
T25 afterAlign.textAlign: "center"
T25 afterLeftIndent.paddingLeft: "18.9333px"
T25 afterSize.runInline: "font-size: 20pt; font-weight: 700; color: rgb(51, 102, 204); font-style: italic; text-decoration: underline;"
T25 cellBefore[1.2]: "2 weeks"
T25 cellAfter[1.2]:  "T25-CELL-EDITED"
T25 afterImage.notice: "已插入图片，按页面宽度缩放。需要精确尺寸请用 Word 调整"
T25 lastReject.notice: "这是文档最后一段，不能再删"
```

### 5.7 L5 五步闭环与异常路径

决定性单主题链（`ui-t26-l5.json`、`ui-t26-l5-one-topic.log`）：

```text
beforeText: "第二页标题文字"
dirty: 1
notice: "已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版"
afterSaveCards:
  0: accept-t06-inserted.pptx (原附件)
  1: accept-t06-inserted.pptx (新附件)
canvasAfterSave: true
oldStillPresent: true
closed: true
canvasAfterReopen: true
botWaitMs: 50143
botDownload: { status:200, bytes:38654 }
checks: { step1Dirty:true, step1Canvas:true, step2Notice:true,
          step3NewAttachment:true, step4Reopen:true, step5BotCard:true }
```

机器人返回文件由独立解析器读回（`t26-l5-bot-probe.log`）：

```text
file        t26-l5-bot.pptx
bytes       38654
parts       44
slide 1  ppt/slides/slide2.xml
  - text            文本框         box 762000,508000 6096000x523220  paras=1 runs=1
      "T26-L5-BOT"
```

再由真实 PowerPoint 打开（`t26-l5-bot-open.log`）：

```text
RESULT OPENED_CLEAN
slides 4
shape slide=1 idx=1 type=17 text=T26-L5-BOT
EXPECT FOUND T26-L5-BOT
```

异常路径（`ui-t26-clean.json`、`ui-t26-validation.json`、`ui-t26-notice-final.json`）：

```text
noChange.notice: "没有可写入的改动"
noChange.countBefore: 25, countAfter: 25

uploadFailure.notice: "已写入本地，但没能存进会话，请重试保存"
uploadFailure.countBefore: 25, countAfter: 25
uploadFailure.dirtyAfterFailure: 1

retrySave.count: 26
retryReopen.text: "T26-RETRY-EDIT"
filenamePreserved: true

validation.notice: "写入失败：写入前的检查未通过：重复的形状 id 1"
validation.beforeCount: 28, afterCount: 28

notice: "已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版"
newAttachment: true
filename: true
```

### 5.8 T-27 至 T-34 单元异常防线

| 编号 | 原始输出 |
|---|---|
| T-27 | `Tests 2 passed | 164 skipped (166)`；用例 `rejects empty bytes rather than writing an empty media part`、`rejects empty bytes instead of uploading an empty file` |
| T-28 | `Tests 3 passed | 163 skipped (166)`；三条 escaping 用例通过 |
| T-29 | `Tests 1 passed | 165 skipped (166)`；`writes xml:space=preserve so leading spaces survive` |
| T-30 | `Tests 2 passed | 164 skipped (166)`；非 pptx/docx 包均明确拒绝 |
| T-31 | `Tests 1 passed | 165 skipped (166)`；混合页操作与元素操作被拒 |
| T-32 | `Tests 1 passed | 165 skipped (166)`；双结构性操作被拒 |
| T-33 | `Tests 1 passed | 165 skipped (166)`；未知操作被拒 |
| T-34 | `Tests 1 passed | 165 skipped (166)`；塌陷画布返回 0 |

### 5.9 T-35 / T-36 大文件与中文换行

```text
20 MB: testFileBytes 21071217, elapsedMs 1631, label "1 / 4", rows 2, loadedClean true
40 MB: testFileBytes 42042737, elapsedMs 3341, label "1 / 4", rows 2, loadedClean true
60 MB: testFileBytes 63014257, elapsedMs 4460, label "1 / 4", rows 2, loadedClean true
```

```text
moderateWithinCanvas: true
moderateNoHorizontalOverflow: true
longWithinCanvas: true
longNoHorizontalOverflow: true
unbrokenWithinCanvas: true
unbrokenNoHorizontalOverflow: true
```

## 6. 新发现缺陷

### B18 [PPT] 现象：继承版式几何的批量写入被接受，但没有写到正确的部件

- 复现：对两页共享同一版式的继承标题执行 `layoutGeometry`，期望两页一起移动。此前操作通过批量校验，但修改没有落到版式部件，结果表现为未生效或命中错误目标。
- 根因：`applyPartStringOp` 曾把 `layoutGeometry` 落到 slide 写入分支，没有先解析 `slide.layoutPart` 并把文本暂存到版式部件。
- 修复：在 `webapp/src/office/pptx/apply.js` 中先由 `slide` 解析 `layoutPart`，修改并校验该版式 XML，再写入 `slide.layoutPart`；slide 部件保持不变。
- 防线：`webapp/src/office/insert.test.js` 的 `writes inherited geometry to the layout for every slide that uses it`。测试同时断言 slide XML 未变、两页重新解析后的 box 都更新。本次定向重跑：`1 passed | 25 skipped (26)`，日志 `B18-accept-final.log`。

### B19 [UI/L5] 现象：保存提示与 T-26 第 2 步约定文案不一致

- 复现：编辑并保存 pptx 后读取成功 toast，旧输出为 `已保存为新版本，会话里可以直接继续改`。它没有说明“新附件就是这一版”和“小八读到的也是这一版”。
- 根因：`messages-view.jsx` 的保存回调里使用了缩写文案，未复用统一契约文案。
- 修复：在 `webapp/src/office/save.js` 增加 `OFFICE_SAVE_NOTICE`，并在 `messages-view.jsx`、`OfficePreview.jsx`、`WordPreview.jsx` 统一使用规定全文：`已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版`。
- 防线：`webapp/src/office/save.test.js` 的 `keeps the save notice explicit about the new attachment and what 小八 reads`。修复前该断言会红，修复后通过；当前 L0 为 `166/166`。浏览器证据 `ui-t26-notice-final.json` 输出规定全文，附件数 `36 -> 37`，下载 `200/38659 bytes`。

## 7. 判据异议与证据取舍

1. **L0 的 166 vs 155**：已在 3.1 记录。没有把新增用例删除来迎合旧基线，也没有把任何失败写成通过。
2. **T-19 旧脚本的两个 false**：早期的 `t19-only-current.log` 把屏幕页序映射到文件页序时断言写错，导致 `onlyLeavesOtherPageInFile=false` 与 `onlyMovesPageInFile=false`。当前只采用屏幕上的 `ui-t19b-final.json` 三项 true，以及直接解析 `t19-only-current.pptx` 的结果：目标页 `x=540000`，另一页保持 `x=1143000`。不是放宽判据，而是不再引用错误映射产生的陈旧布尔值。
3. **T-13 的 `RESULT OPENED`**：按测试清单要求看 `InlineShapes`，同时以 T-12 的 `OPENED_CLEAN` 覆盖清理状态；不把 T-13 伪写成 clean-open。

## 8. 阻塞清单

### 8.1 已绕过：标准群不是可交互机器人话题

首次尝试在标准群“Office 编辑器验收样本 20261115”（group id `7277`）完成第 5 步时，该群 `memberCount: 1`，只有当前登录用户，没有其它成员或机器人。界面证据是 `@机器人` 按钮隐藏，选择器显示 `没有匹配的机器人`；`ui-t26-step5.log` 等满 10 分钟也没有新附件。

这不是产品失败，而是测试上下文没有可用的机器人目标。随后改用真实机器人任务“24小时天选打工仔”，完整五步闭环通过并以 `t26-l5-bot-open.log` 的真实 Office 打开结果为准。因此 T-26 不记为 NOT RUN，也没有未解决的产品阻塞。

### 8.2 当前未解决事项

无会阻止 T-01 至 T-36 验收结论的阻塞。以下属于范围外的已知缺口，见第 9 节。

### 8.3 交付阻塞：分支未能推送到 ginafork

- 缺什么：本地代理 `http://127.0.0.1:7897` 未监听，GitHub 无法经代理连接。
- 怎么确认的：`Get-NetTCPConnection -State Listen -LocalPort 7897` 返回 `PORT_7897_NOT_LISTENING`。
- 原始输出：`git -c http.proxy=http://127.0.0.1:7897 push -u ginafork feat/office-editor-acceptance` 返回 `fatal: unable to access ... Failed to connect to github.com:443 over proxy 127.0.0.1 after 0 ms: Could not connect to server`。
- 解除需要做什么：启动可用代理后执行同一条 push 命令；不要改推 `origin`，不要 force push，不要修改 `main`。
- 当前状态：提交已在本地分支 `feat/office-editor-acceptance`；没有推送成功，不能声称远端已有该分支。

## 9. 已知缺口（TEST-PLAN 第十二节）

| 项 | 当前状态 | 说明 |
|---|---|---|
| G1 五步闭环 | 已解决 | 真实机器人任务完成；见 T-26 |
| G2 多元素对齐/分布未接线 | 仍成立 | 界面已验的是单元素相对页面六向对齐；多选互相左对齐与等间距分布仍只有库函数/单测，没有 UI 入口 |
| G3 PPT 表格单元格文字编辑 | 仍成立 | 按 TEST-PLAN 8.1 的既定范围，PPT 侧只改整体位置/尺寸；Word 侧单元格编辑已验 |
| G4 `/uploads/` 鉴权 | 仍成立 | 生产后端既有行为，本次未改；上线前仍需确认附件隐私策略 |
| G5 大文件性能 | 已测 | 20/40/60 MB 均能干净加载；见 T-35。60 MB 是实测边界之一，不等于所有 60 MB 文档都无条件安全 |
| G6 中文换行与 Office 差异 | 部分保留 | T-36 证明不溢出容器；与 Office 的换行断点仍可能有差异，属于接受的显示差异 |
| G7 知识库归档 | 仍成立 | 验收与实施文档已落盘，但长期知识库归档/维护流程不在本次代码验收内 |

## 10. 收尾自检（11.4）

- [x] L0 与 L1 真的重跑过，不是引用作者的数字。
- [x] T-11 用的是含表格的样本 `sample-table.docx`。
- [x] T-13 用 `office-shape-check.ps1` 看过 `InlineShapes`，不是只看 `OPENED_CLEAN`。
- [x] T-09 用 `office-group-items-probe.ps1` 看过子元素绝对位置。
- [x] 每个 NOT RUN 都写了原因；本次 T-01 至 T-36 没有 NOT RUN，每个 PASS 都有原始输出或结构化 DOM 证据。
- [x] 报告里没有凭据明文。
- [x] 提交范围已排除 `webapp/public/acceptance-sample*`、`D:\tmp` 日志/样本、构建产物与 `node_modules`；样本仍是未跟踪文件，未被提交。
- [x] PR 正文如实列出 NOT RUN（本次无；同时列出仍成立的 G2/G3/G4/G6/G7）。

## 11. 复现入口

完整命令、样本与中间文件位于 `D:\tmp\office-verify`。仓库内的验证脚本位于：

- `webapp/scripts/office-probe.mjs`
- `webapp/scripts/office-roundtrip.mjs`
- `webapp/scripts/office-style-roundtrip.mjs`
- `webapp/scripts/office-geometry-roundtrip.mjs`
- `webapp/scripts/office-insert-roundtrip.mjs`
- `webapp/scripts/office-group-child-roundtrip.mjs`
- `webapp/scripts/office-open-check.ps1`
- `webapp/scripts/office-group-items-probe.ps1`
- `webapp/scripts/office-docx-roundtrip.mjs`
- `webapp/scripts/office-advanced-roundtrip.mjs`
- `webapp/scripts/office-docx-open-check.ps1`
- `webapp/scripts/office-shape-check.ps1`
- `webapp/scripts/office-browser-driver.mjs`
