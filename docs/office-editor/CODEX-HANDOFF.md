# PPT / Word 编辑器验收 · Codex 执行手册

本文件是给 Codex 的执行文档。读完这一份就该知道：你是谁、要做什么、边界在哪、什么算通过、怎么交报告。

配套判据文档：`docs/office-editor/TEST-PLAN.md`（下称「测试清单」）。
实施记录：`docs/office-editor/PLAN.md`（下称「实施清单」）。

---

## 一、身份

你是本次验收的**执行 agent**，不是需求方，不是代码作者。

你的职责有三条，按顺序：

1. **如实执行**测试清单里的每一项，产出可复现的证据。
2. **修复**执行中发现的真实缺陷，每修一条都要能指出「哪条断言会因此变红」。
3. **交付**一份按编号逐条给结论的报告，并在闸门全过后推送 PR。

你不是来让报告好看的。判据不过就写不过。跑不了的写 NOT RUN 并说明卡在哪。
把「跳过」写成「通过」是本任务里最严重的错误。

作者已完成的部分（不要重复劳动，也不要当作已通过采信）：155 条单元测试、构建、
以及 T-02 到 T-13 的脚本级与真实 Office 判据。这些**必须由你重跑一遍确认**，
因为设备状态可能已变，且作者的自测有「夹具与解析器犯同一个错」的历史。

---

## 二、执行口令

用户会把下面这一句发给你。收到即开始，不要反问确认。

```
读取 D:\XiaoBa_clone\cats-company\docs\office-editor\CODEX-HANDOFF.md，
按第二节之后的全部要求执行。你现在是本次验收的执行 agent，
身份、范围、闸门、禁止事项、报告格式都以该文档为准。
不要向我提问，遇到阻塞按文档第八节的方式记录并继续能做的部分。
```

---

## 三、工作根与路径清单

工作根：`D:\XiaoBa_clone\cats-company`

| 用途 | 路径 |
|---|---|
| 仓库根 | `D:\XiaoBa_clone\cats-company` |
| 前端目录 | `D:\XiaoBa_clone\cats-company\webapp` |
| 待测代码 | `D:\XiaoBa_clone\cats-company\webapp\src\office\` |
| 样式 | `D:\XiaoBa_clone\cats-company\webapp\src\styles\office-preview.css` |
| 接入点（已改） | `webapp\src\views\messages-view.jsx`、`webapp\src\widgets\chat-message.jsx` |
| 验证脚本 | `D:\XiaoBa_clone\cats-company\webapp\scripts\office-*.mjs` 与 `office-*.ps1` |
| 判据文档 | `D:\XiaoBa_clone\cats-company\docs\office-editor\TEST-PLAN.md` |
| 实施记录 | `D:\XiaoBa_clone\cats-company\docs\office-editor\PLAN.md` |
| 代码快照包 | 原 `E:\office-editor-code` 已删除，以仓库内 `webapp/src/office/` 为准 |
| 样本与工作目录 | `D:\tmp\office-verify` |
| 登录凭据 | `C:\Users\17499\Desktop\catsco账号密码.txt` |

### 3.1 运行时

| 项 | 值 |
|---|---|
| Node（可用） | `C:\Program Files\nodejs\node.exe`（v26.7.0） |
| Node（默认，**不可用**） | `node` v20.12.2，跑 Vite 8 报 `ERR_INVALID_ARG_VALUE` |
| Go | `C:\Program Files\Go\bin\go.exe`（go1.27.0） |
| Chrome | `C:\Program Files\Google\Chrome\Application\chrome.exe` |
| codex | `codex-cli 0.154.0`，模型 `deepseek-flash`，provider `catsco-relay` |
| 浏览器驱动 | `webapp\scripts\office-browser-driver.mjs`（已实测可用，见 6.2） |

**所有涉及 vite / vitest 的命令，都必须显式用 `C:\Program Files\nodejs\node.exe`。**
用默认 `node` 会得到与代码无关的报错，别在那上面浪费时间。

### 3.2 Git

| remote | URL |
|---|---|
| `origin` | `https://github.com/buildsense-ai/cats-company.git` |
| `ginafork` | `https://github.com/GinovaCode/cats-company.git` |

当前分支：`feat/artifact-panel-topic-binding`（**没有配置上游分支**）
已配置：`credential.helper=manager`、`user.name=GinovaCode`、`user.email=wangjj297@mail2.sysu.edu.cn`

**推送目标固定为 `ginafork`，不要推 `origin`。** `origin` 是上游组织仓库。

---

## 四、执行顺序与闸门

按 L0 → L1 → L2 → L3 → L4 → L5 顺序跑。**前一层不过，不要往后跑。**

| 层 | 内容 | 闸门（全过才进下一层） |
|---|---|---|
| L0 | 单元测试 | 8 文件 / 155 用例 / 0 失败 |
| L1 | 构建 | `vite build` 退出码 0 |
| L2 | 脚本级往返 | T-01 至 T-13 各自的 `ok` 全绿 |
| L3 | 真实 Office 读回 | 每条 `RESULT OPENED_CLEAN`，且内容/几何/形状数与期望一致 |
| L4 | 浏览器交互 | 本地前端 + 生产后端代理可跑，见第六节 |
| L5 | 五步闭环 | 见第七节 |

发现缺陷时：先按测试清单第十节的 bug 编号对照，确认是不是已知回归；
是新缺陷就按「复现 → 根因 → 修复 → 哪条断言会变红」四段记录，再修。

---

## 五、L0–L3 的具体命令

以下命令全部可原样执行。输出要贴进报告。

### L0 单元测试

```cmd
cd /d D:\XiaoBa_clone\cats-company\webapp
"C:\Program Files\nodejs\node.exe" .\node_modules\vitest\vitest.mjs run src/office
```

判据：`Test Files 8 passed (8)`、`Tests 155 passed (155)`、退出码 0。

### L1 构建

`vite build` 可能超过单次命令的同步等待上限。写成 `.bat` 后台跑，再读 log：

```bat
@echo off
cd /d D:\XiaoBa_clone\cats-company\webapp
"C:\Program Files\nodejs\node.exe" .\node_modules\vite\bin\vite.js build > D:\tmp\office-verify\build.log 2>&1
echo EXIT=%ERRORLEVEL% >> D:\tmp\office-verify\build.log
```

```powershell
Start-Process -FilePath 'cmd.exe' -ArgumentList '/c','D:\tmp\office-verify\build.bat' -WindowStyle Hidden
Start-Sleep -Seconds 50
Get-Content 'D:\tmp\office-verify\build.log' -Tail 8
```

判据：末尾 `EXIT=0`。`WARN inlineDynamicImports is deprecated` 是已知噪音，不算失败。

### L2 / L3 脚本与真机判据

样本若不存在，先按测试清单第 3.3 节造。已有样本：

| 样本 | 字节 | 内容 |
|---|---|---|
| `D:\tmp\office-verify\sample.pptx` | 37145 | 2 页，含中文标题、表格 |
| `D:\tmp\office-verify\sample-inserted.pptx` | 38677 | 4 页，含 1 个组合、1 张图片 |
| `D:\tmp\office-verify\sample.docx` | 13725 | 4 段，含粗体与居中 |
| `D:\tmp\office-verify\sample-table.docx` | 14099 | 含 2×3 表格 |

逐条执行 T-01 到 T-13。**每一条都要记：命令原文、原始输出、判定。**

两个最容易假通过的地方，重点盯：

1. **T-11 表格单元格**：用 `sample.docx`（无表格）跑会输出
   `cell skipped (no table in this file)` 并「通过」。那不是通过，是没测到。
   必须用 `sample-table.docx`。
2. **T-13 插图是真图**：Word 里图片关系写错会显示破图占位而**不报错**，
   所以 `OPENED_CLEAN` 完全不能证明图片可用。必须用
   `office-shape-check.ps1` 读 `InlineShapes` 数量与 `type=3`。

同理 **T-09 组合内子元素**：必须用 `office-group-items-probe.ps1` 读每个子元素的
绝对位置，不能只看顶层 shape 列表 —— 后者不展开组合，子元素动没动都看不出来。

---

## 六、L4 浏览器端到端：环境已就绪，按 6.4 执行

这一层的前置**已经解决**。原来的两个阻塞（浏览器工具、后端数据库）都有可用的替代路径，
下面写清结论和原因，你直接照做即可，不要再去装 MySQL。

### 6.1 浏览器能力：用仓库内的驱动，不要用 browser_* 工具

**结论**：浏览器工具实测可用，入口是 `webapp\scripts\office-browser-driver.mjs`。

**codex 自带的 `browser_*` 工具在这个环境里不可用，原因是撞名，不是没装。**

| 事实 | 证据 |
|---|---|
| codex 0.154.0 注册了原生 `browser_navigate` 等工具 | `codex features list` 里 `browser_use`、`in_app_browser` 均为 `stable true` |
| 但每次调用都返回 `unsupported call` | 实测日志：`ERROR codex_core::tools::router: error=unsupported call: browser_navigate` |
| 原因是这些工具走 OpenAI 托管工具通道 | 本机 provider 是第三方 relay（`catsco-relay`），托管工具通道不转发 |
| playwright MCP 服务本身是健康的 | 手工 stdio 握手拿到 25 个工具：`browser_navigate,browser_click,browser_snapshot,...` |
| 但 MCP 工具名与原生 `browser_*` 重名，被原生那份遮蔽 | 注册后仍报同一个 `unsupported call` |

所以不要再花时间在 MCP 上。已经装过又移除了，`codex mcp list` 现在为空是刻意为之。

**驱动已实测通过的项目（2026-11-14）**：

| 命令 | 实测输出 |
|---|---|
| `open http://localhost:5173/` | `URL http://localhost:5173/login` / `TITLE CatsCo` |
| `login` | `LOGGED_IN yes` |
| `url`（跨进程再查） | `URL http://localhost:5173/` |
| `text` | 完整应用壳，含全部会话列表、项目列表、输入框 |

profile 是持久化的（`D:\tmp\office-verify\chrome-profile`），登录一次后续命令都带着。

### 6.2 后端与数据：不需要 MySQL，也不需要 Docker

**结论**：用**本地前端 + 生产 API**。这条路径已实测跑通登录并加载真实会话。

为什么这是正确的做法，而不是"拿线上糊弄"：

- 本次要验的功能（Office 预览与就地编辑）**是纯前端**。`webapp/src/office/` 全部代码在浏览器里跑，
  本次改动没有一行后端代码：`git diff HEAD` 里 `server/`、`cmd/` 都没有文件。
- 真正要验的是「**本地这份前端代码**在真实会话、真实附件上能不能工作」。
  前端是本地 dev server 提供的，所以验的是改过的代码。
- 只有 API 与数据来自生产。这恰好把「后端是不是本地编译的」这个无关变量固定住了。

**边界要求（必须写进报告）**：

这测的是「本地前端代码 + 生产 API 与数据」，不是「已部署版本」，也不是「本地后端」。
如果某个结论依赖后端行为（例如上传接口的字段、附件不可变语义），要说清它验证的是生产后端，
不是你本地编译出来的那份。**不许**把它写成"端到端全链路已验"。

**启动命令**：

```bat
@echo off
cd /d D:\XiaoBa_clone\cats-company\webapp
set VITE_BACKEND_TARGET=https://app.catsco.cc
"C:\Program Files\nodejs\node.exe" .\node_modules\vite\bin\vite.js --port 5173 --strictPort > D:\tmp\office-verify\vite-prod.log 2>&1
```

后台起（`vite` 是常驻进程，不要同步等）：

```powershell
Start-Process -FilePath 'cmd.exe' -ArgumentList '/c','D:\tmp\office-verify\vite-prod.bat' -WindowStyle Hidden
Start-Sleep -Seconds 22
Get-Content 'D:\tmp\office-verify\vite-prod.log' -Tail 8
Get-NetTCPConnection -State Listen | Where-Object { $_.LocalPort -eq 5173 }
```

判据：日志出现 `ready in`，且 5173 有监听。

**为什么不在我的服务器上装 MySQL**（已被排除，不要重试）：

| 项 | 实测 |
|---|---|
| 磁盘 | 50G 用了 49G，**只剩 1.9G**，装 MariaDB 会把系统盘压垮 |
| Go | 未安装 |
| Docker | 未安装 |
| 从该机下载 MariaDB 包 | 约 13 KB/s，90 MB 要数小时 |
| 即使装上 | 你的电脑要跨公网连 3306，等于把数据库暴露在公网，不能这么做 |

结论：这条路不通，也不该走。6.2 的方案不需要数据库。

### 6.3 登录：驱动已内置，不要手写

凭据文件：`C:\Users\17499\Desktop\catsco账号密码.txt`
已确认结构（**不含明文**）：2 行，第 1 行是账号，第 2 行是密码。

直接用驱动的 `login` 命令，它已经处理了三件事：

1. 按**当前打开的 origin** 登录。写死生产域名曾经让"本地代码测试"实际测的是线上，
   这个坑已经修掉了。
2. 点 `button[type="submit"]`。登录表单在提交按钮**之前**还有一个普通 button（密码显隐切换），
   用 `form button` 这种联合选择器会点到切换按钮，表单不提交，症状就是"一直停在 /login"。
3. 已经登录时输出 `LOGGED_IN already`，不报错。

```powershell
$env:BROWSER_HEADLESS='1'
& 'C:\Program Files\nodejs\node.exe' 'webapp\scripts\office-browser-driver.mjs' open 'http://localhost:5173/'
& 'C:\Program Files\nodejs\node.exe' 'webapp\scripts\office-browser-driver.mjs' login
```

判据：`LOGGED_IN yes` 或 `LOGGED_IN already`，且 `URL` 不再是 `/login`。

**限定条件**：驱动只在登录快结束时不加密地读一次凭据文件，不写回、不打印、不进 profile 之外的地方。
报告里只写「已用桌面凭据文件登录成功」或「登录失败，现象是 X」，**不写账号与密码**。

### 6.4 驱动的完整命令表

工作目录无所谓，脚本按绝对路径找依赖，仓库根和 `webapp/` 下都能跑。

| 命令 | 输出 | 用途 |
|---|---|---|
| `open <url>` | `URL` `TITLE` | 打开页面，记住 origin 供后续命令使用 |
| `login` | `URL` `LOGGED_IN yes\|already\|no` | 用桌面凭据登录当前 origin |
| `text` | `TEXT "<页面全文>"` | 断言界面文案、确认登录后壳渲染出来 |
| `inputs` | `FIELDS [...]` | 列出所有 input/button 的 type、name、placeholder、文本 |
| `click <selector>` | `URL` | 点元素 |
| `fill <selector> <value>` | `FILLED <selector>` | 填输入框 |
| `eval "<表达式>"` | `VALUE <json>` | 在页面里求值，用来断言 DOM 状态 |
| `shot <path>` | `SHOT <path>` | 截图，供人工复核 |
| `url` | `URL` | 查当前地址 |

注意三点：

1. **每条命令是独立进程**，页面状态靠持久 profile 与 `D:\tmp\office-verify\browser-state.json`
   里的 URL 衔接。所以不要在命令之间手改这个 json。
2. 默认**有头**（能看到浏览器窗口），因为验证码、授权弹窗需要人看一眼。
   无人值守时设 `BROWSER_HEADLESS=1`。
3. 判据看 `OK 1` 与具体字段，不要看退出码就完事 —— 失败会打印 `FAIL <原因>` 且退出码非 0。

### 6.5 L4 的推进方式

用驱动按测试清单 8.2 到 8.13 逐项操作，每条都要留下 `text` 或 `eval` 的输出作为证据。
截图只作辅助，不作为唯一判据 —— 截图能骗人，`text` 与 `eval` 的返回值更难。

做不到的项照第八节写 NOT RUN，写清是哪一步卡住。

---

## 七、L5 五步闭环

这是用户原始需求 R4 / R5 / R6 的唯一端到端判据。做法见测试清单第九节 T-26。

| 步 | 动作 | 判据 |
|---|---|---|
| 1 | 侧边栏打开 pptx，改一处文字 | 画布更新，出现「已修改」 |
| 2 | 点保存 | 提示「已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版」 |
| 3 | 看会话 | 新增附件消息，**原消息与旧附件没被改** |
| 4 | 关侧边栏，从会话重开新附件 | 看到改后内容 |
| 5 | 继续聊天让小八再改 | 小八读到的是改后版本 |

异常路径必须一起验：

| 场景 | 判据 |
|---|---|
| 无改动点保存 | 「没有可写入的改动」，不上传空文件 |
| 上传失败 | 「已写入本地，但没能存进会话，请重试保存」，**不能声称已保存** |
| 校验不通过 | `写入前的检查未通过：…`，不产出坏文件 |
| 文件名 | 保持原名，不出现 `.pptx.pptx` |

L5 依赖 L4 的前置。L4 做不了，L5 就是 NOT RUN，不要用单测代替。

---

## 八、L4 / L5 跑不了时怎么办

**不要伪造。** 按这个顺序处理，然后把结论写进报告。

1. 记录阻塞点：具体是哪一步、你用什么命令确认的、原始输出是什么
2. 明确写出哪些编号因此是 NOT RUN
3. 继续完成不依赖该前置的部分（L0–L3 全部、以及代码层面的静态核查）
4. 在报告里写清「要跑通这一项，还需要补什么」。例如某个交互元素选择器定位不到，
   就写清页面当时的 `inputs` 输出与 `text` 片段，让下一个人能接着定位

已知不该再出现的情况：不要再写「后端因缺少 MySQL 无法启动」。
6.2 已经给出不用数据库的路径，且实测能登录、能加载真实会话。如果你仍然卡住，
说明卡点在别处，要如实写出实际卡点。

---

## 九、PR 流程

### 9.1 前置判断

先跑：

```powershell
Get-Command gh -ErrorAction SilentlyContinue
git remote -v
git status --short
```

已知：`gh` **未安装**。

### 9.2 分支与提交

从当前分支 `feat/artifact-panel-topic-binding` 出发，**新建**一个分支再提交：

```
feat/office-editor-<一句话主题>
```

提交前必须：

1. `git status --short` 看清每个文件，确认没有把凭据、样本、日志、`node_modules` 带进去
2. `git diff --cached --stat` 复核将要提交的范围
3. 提交信息写清「改了什么 + 为什么」，不写「update」「fix」

推送到 fork：

```
git -c http.proxy=http://127.0.0.1:7897 push -u ginafork <分支名>
```

直连 GitHub 会超时，**必须带代理**。若代理不可用，如实报「推送失败，原因是网络」，
不要反复重试浪费时间。

### 9.3 创建 PR

| 情况 | 做法 |
|---|---|
| `gh` 可用且已登录 | `gh pr create --repo GinovaCode/cats-company --base main --head <分支> --title … --body …` |
| `gh` 不可用 | **不要**改用 GitHub API 猜 token。改为在仓库根写 `PR-DESCRIPTION.md`，把标题、摘要、测试结论表、未完成项写好，交给用户手动创建 |

PR 正文必须包含：

1. 这份改动做了什么（一段）
2. 测试结论表：每个 T-xx 的编号、状态、证据一行
3. **NOT RUN 的项与原因**，不能被省略
4. 已知缺口（测试清单第十二节的 G1–G7 里仍然成立的部分）

**PR 不许声称「全部验收通过」**，除非 L5 真的跑通了。L5 没过就写「核心链路未端到端验证」。

### 9.4 红线

- 不推 `origin`（上游组织仓库）
- 不 `push --force`
- 不改 `main`
- 不提交凭据文件、`D:\tmp` 里的样本、构建产物
- 不在没有通过闸门的情况下创建 PR 并宣称可合并

---

## 十、禁止事项

1. **不许把 NOT RUN 写成 PASS。** 跑不了就写跑不了，写清卡点。
2. **不许用「测试通过」「构建成功」代替证据。** 必须给条数、文件名、原始输出片段。
3. **不许把线上环境的验证结果算作本地改动的通过依据。**
4. **不许把凭据明文写进任何文件、日志、提交或报告。**
5. **不许为通过判据去改判据。** 判据是 TEST-PLAN.md，不是你写的。
   若你认为判据本身有误，单独列一节「判据异议」说明理由，不要私自放宽。
6. **不许为了让某个脚本「通过」而删掉它的断言。**
7. 不许推 `origin`、不许 force push、不许改 `main`。

---

## 十一、报告格式

最终交一份 `docs/office-editor/ACCEPTANCE-REPORT.md`，并在对话里给出同样内容的摘要。

### 11.1 逐条结论表（必须有）

```
| 编号 | 状态 | 命令/手段 | 证据（原始输出片段） |
|---|---|---|---|
| T-01 | PASS | office-probe.mjs sample.pptx | 2 页 39 parts，坐标无 NaN |
| T-09 | PASS | office-group-items-probe.ps1 | 子元素 57.6,334.8 → 86.4,349.2；兄弟 252,334.8 不变 |
| T-19 | NOT RUN | 需 Go 服务端 + dev server | 3306 未监听，无 MySQL，无 docker |
```

状态只允许三种：`PASS` / `FAIL` / `NOT RUN`。

### 11.2 新发现缺陷（如有）

```
### B18 [PPT] 现象：…
- 复现：<命令或步骤>
- 根因：<一句话>
- 修复：<改了什么>
- 防线：<新增/修改的断言名，修复前会红，修复后绿>
```

### 11.3 阻塞清单（如有）

逐条写：缺什么、怎么确认的、原始输出、解除需要做什么。

### 11.4 收尾自检

交之前逐条过：

- [ ] L0 与 L1 真的重跑过，不是引用作者的数字
- [ ] T-11 用的是含表格的样本
- [ ] T-13 用 `office-shape-check.ps1` 看过 `InlineShapes`，不是只看 `OPENED_CLEAN`
- [ ] T-09 用 `office-group-items-probe.ps1` 看过子元素绝对位置
- [ ] 每个 NOT RUN 都写了原因，每个 PASS 都有原始输出
- [ ] 报告里没有凭据明文
- [ ] 提交范围里没有样本、日志、构建产物
- [ ] PR 正文如实列出 NOT RUN
