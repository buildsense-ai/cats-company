# Chat UI 内部调研与视觉规范基线

> 调研对象：`E:\CodexProjects\mini-claude-code\chat-ui\index.html`（单文件 SPA，约 1320 行）
> 调研范围：CSS 视觉一致性、组件状态、可访问性 (a11y)、响应式、交互反馈
> 用户场景：公司内部同事，通过浏览器使用 Mini Chat 与模型对话
> 上轮上下文：已做过一轮 critique，输出 P0×1 / P1×5 / P2×3 清单；本报告在此基础上复核并扩列

---

## 一、调研方法

1. **静态走查** 完整 CSS（约 420 行）+ HTML 结构 + 关键 JS 入口
2. **Nielsen 启发式评估** 10 条原则逐一对照
3. **WCAG 2.1 AA** 关键指标人工测算（对比度、焦点可见、动效降级）
4. **响应式断点** 全部 `@media` 检查
5. **浏览器预览**（建议执行：双击 `index.html` 或通过 `启动.bat` 启动）

---

## 二、问题清单（按优先级）

### 🔴 P0 — 必须立即修（功能 / 安全）

| # | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| P0-1 | L175–179 | `.toggle-btn:hover` 被定义 **3 次**，后两次覆盖前一次，亮色主题下悬停态错误地走了 `#2a2a2c`（暗色） | 删掉重复行，亮色用 `rgba(0,0,0,0.05)`，暗色用 `#2a2a2c` |
| P0-2 | L992 | **孤儿 CSS**：`.welcome-suggestions` 闭合后残留一段无选择器的声明 `display: flex; flex-direction: column; …`，整段被浏览器忽略，且 `.progress-track` 的规则被错误拼到外层 | 删除 L992–996 之间的死代码，或补回正确的选择器 |
| P0-3 | L187 / L191 | `.sidebar:not(.open) .sidebar:not(.open) …` 出现 **嵌套选择器后缺少大括号 + 多余分号**，同样被浏览器忽略 | 校验 L185–191 整段括号闭合 |

### 🟠 P1 — 本轮必须改完

| # | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| P1-1 | L239 / L245 | 搜索框 `:focus` 有视觉反馈，但 **其它可聚焦元素**（`new-btn`、`toggle-btn`、`session`、消息 meta 按钮）全部无 `:focus-visible` 样式，键盘用户看不到焦点 | 统一加 `:focus-visible` 兜底，至少 `outline: 2px solid var(--accent); outline-offset: 2px;` |
| P1-2 | L1086 | 发送按钮 `.send-btn` 背景 `--accent` 配 `#fff` 文字 — 浅色下 `#188567` + `#fff` 对比度 **约 3.6 : 1**，未达 WCAG AA 4.5:1 | 浅色下加深的 `--accent-hover: #146a52` 给按钮 hover；或在按钮文字加粗 + 阴影 |
| P1-3 | L1124–1126 | `.toast` 在亮色下 `background: #1f1f1f; color: #fff`，对比度 17:1 ✅；但暗色下 `background: #fff; color: #1f1f1f` 也是 OK 的。**真正问题**：使用 `var(--text)` / `var(--bg)` 当 token 时，深浅反了，依赖主题切换顺序（实际表现是浅色用浅色、暗色用深色），非常脆弱 | toast 直接用独立 token：`--toast-bg` / `--toast-fg` |
| P1-4 | 全局 | **`prefers-reduced-motion` 缺失**：消息 `msgIn` 动画、侧边栏宽度过渡、按钮悬停都在动；前庭敏感同事无法降级 | 统一加 `@media (prefers-reduced-motion: reduce) { *, *::before, *::after { transition-duration: .01ms !important; animation-duration: .01ms !important; } }` |
| P1-5 | 全局 CSS | 暗色下硬编码 `#2a2a2c` 出现 **≥ 15 次**（侧边栏、菜单、按钮、悬停态），亮色下对应硬编码 `#ececec` 等散落。**token 体系不闭合** | 抽 `--surface-1`、`--surface-2`、`--surface-hover` 三档语义 token，所有硬编码替换 |

### 🟡 P2 — 下个迭代

| # | 位置 | 问题 |
|---|---|---|
| P2-1 | L953 / L984 / L1160 | `@media (max-width: 720px)` 重复定义 3 次，且断点单一 — 768 / 1024 / 1280 三个断点缺失，平板下体验拉胯 |
| P2-2 | L185–190 | `.sidebar:not(.open) …` 选择器链冗长，**建议改用属性选择器** `.sidebar[data-collapsed="true"]` 配合 JS toggle，可读性 + 性能都更好 |
| P2-3 | L857 / L877 | `!important` 出现 2 次（`.msg .meta button.regen-btn`、暗色 hover），典型的"打补丁"痕迹 — token 体系理顺后可去掉 |
| P2-4 | L868 / L887 | `.msg-more-btn` 用纯文本 `…` 三字符（letter-spacing 1px）做菜单图标 — 视觉过弱，与品牌 logo 的几何感不一致 |
| P2-5 | L909–921 | `.welcome` 在窄屏 `padding: 80px 24px 40px`，移动端上下 padding 过大，第一屏塞不下输入框 |
| P2-6 | L1138 | `.qr-box` 永远强制 `background: #fff`，暗色主题下突兀 — 跟随 `var(--panel)` |
| P2-7 | 全局 | 缺设计 token 文档（颜色 / 字号 / 圆角 / 间距 / 阴影 / 动效曲线），新人接手靠"看代码猜规范" |

---

## 三、视觉规范基线 v0.1（建议落地为 `:root` token）

### 3.1 颜色（语义层）

```
--bg            页面底色
--panel         卡片/面板
--surface-1     一级表面（输入框、菜单、悬停态）
--surface-2     二级表面（激活态、悬浮菜单）
--border        描边、分隔
--text          主文字
--muted         次要文字
--accent        品牌主色（绿色 #188567）
--accent-hover  品牌悬停（#146a52 浅 / #1a9d7a 深）
--accent-soft   品牌 12% 透明
--error         错误 / 停止
--success       成功反馈（**新增**，当前缺失）
--warning       警告（**新增**）
```

### 3.2 字号

```
--fs-xs   12px   meta / hint / toast
--fs-sm   13px   session / menu / 按钮
--fs-md   14px   （当前缺，介于 sm 与正文之间）
--fs-base 15px   正文 / 输入
--fs-lg   18px   消息标题（当前是 1.4em 不一致）
--fs-xl   34px   welcome 标题
```

### 3.3 圆角

```
--radius-sm 6px    小标签 / 标记
--radius-md 10px   按钮 / 输入
--radius-lg 14px   消息气泡 / 大面板
--radius-pill 22px 欢迎胶囊 / 输入框外层
```

### 3.4 间距

```
--space-1 4px
--space-2 8px
--space-3 12px
--space-4 16px
--space-5 20px
--space-6 24px
```

### 3.5 动效

```
--ease-out  cubic-bezier(.2, .8, .2, 1)   （当前 .15s/.2s ease 全部不规范）
--dur-fast  120ms    焦点、按钮 hover
--dur-base  180ms    卡片、消息
--dur-slow  250ms    侧边栏宽度
全部需被 prefers-reduced-motion 覆盖
```

### 3.6 阴影

```
--shadow-sm 0 2px 6px rgba(0,0,0,.04)
--shadow-md 0 4px 12px rgba(0,0,0,.08)
--shadow-lg 0 8px 28px rgba(0,0,0,.12)
暗色主题 alpha 翻倍：0.4 / 0.5 / 0.5
```

### 3.7 字体

```
--font-sans  -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif
--font-mono  "JetBrains Mono", "Consolas", "Monaco", monospace
（mono 当前是 inline 在 .msg .content pre code，未抽 token）
```

---

## 四、组件级规范（最常用 4 类）

### 4.1 按钮
- 主按钮（send）：`background: var(--accent)` / `color: #fff` / **必须同时加深 hover 和禁用态**
- 次按钮（meta 操作）：`background: var(--surface-1)` / `color: var(--text)`
- 幽灵按钮（toggle / more）：透明底 + 悬停 surface-1
- **统一聚焦环**：`outline: 2px solid var(--accent); outline-offset: 2px;`

### 4.2 消息气泡
- 用户：`background: var(--panel)` + 头像 `#6c5ce7`（紫）
- AI：`background: var(--panel)` + 头像 `var(--accent)`（绿）
- 错误：整段文字 `var(--error)` + 头像 `var(--error)`
- 三种角色**不靠背景色区分，只靠头像和 meta 文字**，更易读

### 4.3 输入框
- 默认：`border: 1px solid var(--border)` / `border-radius: var(--radius-pill)`
- 聚焦：`border-color: var(--accent)` + `box-shadow: 0 0 0 4px var(--accent-soft)`
- 字符上限 / 字数统计：当前**缺失**，建议加入右下角小字 `0 / 4000`

### 4.4 菜单/下拉
- `background: var(--surface-2)` / `border: 1px solid var(--border)` / `border-radius: var(--radius-md)`
- hover：`background: var(--surface-hover)`
- 进入动画：`opacity 0→1 + translateY(-4px)`, `--dur-base` + `--ease-out`

---

## 五、执行路线图

| 阶段 | 内容 | 工期 |
|---|---|---|
| **0. 立即** | 修 P0-1 / P0-2 / P0-3 三个明确 bug | 0.5 h |
| **1. token 化** | 把所有硬编码颜色 / 字号 / 圆角 / 间距 / 阴影 / 动效抽到 `:root`，补全 success / warning | 2 h |
| **2. a11y 兜底** | 全局 `:focus-visible` + `prefers-reduced-motion` + `aria-live="polite"` 给消息列表 | 1 h |
| **3. 响应式** | 引入 768 / 1024 / 1280 三档断点，重排 welcome / session / 消息气泡 | 2 h |
| **4. 组件打磨** | toast / qr-box / msg-more-btn / 发送按钮 hover 对比度修复 | 1 h |
| **5. 文档** | 本报告转成 `DESIGN_TOKENS.md`，代码中加注释引用 token | 0.5 h |

---

## 六、验证清单

- [ ] `index.html` 双击打开无 console error
- [ ] Chrome DevTools → Rendering → "Emulate CSS prefers-reduced-motion: reduce" 下所有过渡 ≤ 10ms
- [ ] 仅用键盘 Tab 可遍历全部交互，焦点环可见
- [ ] Chrome DevTools Lighthouse → Accessibility ≥ 95
- [ ] 浅色 / 暗色各检查 1 次 WCAG AA 对比度（重点：accent 文字、meta 按钮、toast）
- [ ] 360 / 768 / 1280 / 1920 四档断点无溢出、无错位
- [ ] `git diff --stat` 改动行 ≤ 总行数 30%

---

## 七、给后续接手人的一句话

> 当前最值钱的不是修某个具体样式，而是把 `:root` 的 token 抽完 + 加 a11y 兜底。这两步完成后，剩下的 P2 都是体力活。
