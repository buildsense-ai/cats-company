# CatsCompany 项目交接文档

> 生成日期：2026-07-16  
> 当前仓库：`E:\CodexProjects\cats-company`  
> 当前分支：`agent/chat-ui-react-migration`  
> 当前提交：`002f45c50816bd33abb4367c7b91e239a4c57bf4` (`feat(webapp): align CatsCo chat UI with local design`)  
> 当前开发状态：已暂停功能和 UI 开发，仅生成本交接文档。工作区仍有未提交改动，详见第 5、6 节。

## 1. 项目目标和当前功能说明

CatsCompany 是一个面向用户、AI Agent 与群组协作的聊天系统。当前仓库同时包含：

- React Web 前端，用于登录、聊天、好友、群聊、Agent、设备连接、Relay、反馈和文件上传等交互。
- Go 后端，用于身份认证、消息、会话、好友、群组、Agent、设备、Relay、上传等业务接口。
- WebSocket 通道，用于实时消息、在线状态、已读、输入状态和流式任务中断。
- Bot SDK、示例 Bot、协议定义、部署与运维脚本。

当前前端的主要界面结构为：

- 顶栏：模型/连接状态、当前任务标题、主题切换、客户端下载入口。
- 左侧栏：新建任务、搜索任务、协作（好友、群聊、Agent）、项目、历史任务、账户与设置。
- 主内容区：空任务欢迎页、消息列表、任务过程、输入框、Agent 选择和附件入口。
- 二级界面：登录/注册/重置密码、好友、群聊创建与管理、Agent 管理、桌面端连接、Relay、反馈、个人资料、设备绑定、移动上传等。

### 功能真实状态

| 功能域 | 当前状态 | 说明 |
| --- | --- | --- |
| 登录、注册、重置密码、个人资料 | 已有前后端代码 | 注册接口是否对当前部署开放取决于服务端配置；前端支持开发环境免登录预览。 |
| 私聊、历史消息、会话列表 | 已有前后端代码 | REST 获取历史/会话，WebSocket 实时收发；连接失败时发送会回退到 REST。 |
| 好友、申请、接受、拒绝、拉黑、删除、在线状态 | 已有前后端代码 | 在线状态通过 WebSocket `me/online` 与在线用户接口配合。 |
| 群聊创建、邀请、成员管理、公告、角色、禁言、退出/解散 | 已有前后端代码 | 前端已有创建与管理界面，仍需在真实后端数据下做完整回归。 |
| Agent 列表、创建/管理、好友关系、入口绑定 | 已有前后端代码 | 前端同时包含 Agent 管理、渠道入口、移动端绑定和桌面端唤起。 |
| 桌面端连接 | 已有前后端代码 | 前端创建连接会话并轮询状态；依赖本机 CatsCo 桌面端和后端连接服务。 |
| Relay 配置、Key、额度/用量 | 已有前后端代码 | 前端包含配置和 Key 管理界面，特殊 Bot 接口使用 `ApiKey` 鉴权。 |
| 文件上传、反馈图片、移动上传 | 已有前后端代码 | 使用 `multipart/form-data`，上传字段名为 `file`。 |
| 项目 | **仅有禁用的前端占位入口** | 当前 `sidepanel-view.jsx` 中“项目”按钮不可用，提示项目接口尚未接入；不可把它写成已完成功能。 |
| 模型选择 | 前端有选择状态，实际可用性依赖桌面端/后端 | UI 中列出多个模型；是否真正切换模型取决于已连接的 CatsCo Agent/桌面端能力。 |

## 2. 当前技术栈

### Web 前端

- React 18.2
- ReactDOM 18.2
- Vite 8.1
- Vitest 4.1 + jsdom
- Lucide React 图标
- `marked`：Markdown 解析
- `qrcode.react`：二维码
- `read-excel-file`：表格读取
- `ogl`：图形相关依赖
- 原生 `fetch` + 原生 `WebSocket`
- CSS：`openchat-theme.css` 与 `catsco-ui-system.css`
- 状态管理：React `useState`/`useEffect` 为主，没有 Redux、Zustand 等全局状态库
- 持久化：`localStorage`

### Go 后端

- Go 1.26.5
- `net/http`
- Gorilla WebSocket
- JWT v5
- PostgreSQL (`pgx`)、MySQL driver、Redis client
- gRPC / Protobuf
- 后端入口：`server/cmd/server.go`

### 其他

- Bot SDK：`bot-sdk/`
- 示例 Bot：`bots/`
- 协议：`pbx/model.proto`
- 部署：`deploy/`
- 根目录 Node 脚本目前主要用于本地 onboarding mock 和文档/演示辅助。

## 3. 项目目录结构

```text
cats-company/
├─ .github/                    # GitHub 工作流与仓库配置
├─ bot-sdk/                    # Bot SDK
├─ bots/                       # 示例或内置 Bot
├─ deploy/                     # 部署配置
├─ docs/                       # 项目文档；本文件位于此目录
├─ pbx/
│  └─ model.proto              # Protobuf 协议定义
├─ scripts/                    # 本地辅助、迁移或测试脚本
├─ server/                     # Go 后端
│  ├─ cmd/server.go            # HTTP/WS 服务入口和路由注册
│  ├─ auth.go                  # 认证
│  ├─ user.go                  # 用户资料
│  ├─ friends.go               # 好友关系
│  ├─ messages.go              # 消息
│  ├─ conversations.go         # 会话列表
│  ├─ group.go                 # 群聊
│  ├─ agents.go                # Agent
│  ├─ desktop_connect.go       # 桌面端连接
│  ├─ channel_agent_binding.go # 渠道与 Agent 绑定
│  ├─ relay_*.go               # Relay 配置、Key、用量
│  ├─ device*.go               # 设备与连接器
│  ├─ upload.go                # 上传
│  ├─ feedback.go              # 意见反馈
│  ├─ wshandler.go/wspump.go   # WebSocket
│  ├─ db/                      # 数据库相关
│  └─ store/                   # 存储层
├─ webapp/                     # React Web 前端
│  ├─ public/                  # 静态资源
│  ├─ src/
│  │  ├─ index.jsx             # React 入口
│  │  ├─ api.js                # REST、WebSocket、Token 与错误处理
│  │  ├─ views/
│  │  │  ├─ tinode-web.jsx     # 顶层页面、认证、路由与主要 UI 状态
│  │  │  ├─ sidepanel-view.jsx # 左侧栏、会话/协作列表、新建任务
│  │  │  ├─ messages-view.jsx  # 消息与输入区
│  │  │  └─ ...                # 设备绑定、移动上传等页面
│  │  ├─ widgets/              # 弹窗、消息组件、群聊、Agent、反馈等
│  │  ├─ css/
│  │  │  ├─ openchat-theme.css
│  │  │  └─ catsco-ui-system.css
│  │  ├─ i18n/                 # 国际化
│  │  └─ utils/                # 上传、Relay 等工具
│  ├─ vite.config.js           # Vite、代理和测试配置
│  ├─ package.json
│  ├─ build/                   # 生成产物，不是源代码
│  └─ node_modules/            # 安装依赖，不是源代码
├─ .env.example                # 后端环境变量示例，不含真实密钥
├─ go.mod / go.sum
└─ package.json                # 根目录辅助脚本
```

## 4. 已完成的功能

以下指“代码中已有实现”，不代表所有功能都已在当前部署环境完成端到端验收。

### 认证与账户

- 登录、注册、发送邮箱验证码、重置密码。
- Token 持久化与 Bearer 鉴权。
- 个人资料读取和更新。
- 开发环境 `VITE_DEV_BYPASS_AUTH=true` 的本地预览模式。

### 消息与实时通信

- 会话列表、历史消息读取、消息发送。
- 私聊和群聊 Topic。
- WebSocket 自动重连。
- 在线状态、输入状态、已读状态。
- 流式任务取消消息。
- WebSocket 不可用时，发送消息回退到 REST。
- 消息区支持 Markdown、内容块、代码、表格和富媒体相关组件。

### 协作

- 好友搜索、申请、接受、拒绝、删除、拉黑。
- 群聊创建、邀请、成员管理、公告、角色、禁言、退出和解散。
- Agent 列表、创建/管理、可见性、API Key、Agent 好友关系。
- Agent 渠道入口与绑定流程。

### 设备、Relay 与辅助功能

- CatsCo 桌面端连接会话和状态轮询。
- 设备列表、配对、解绑、审计信息。
- Relay 配置、商业信息、邀请码、会话、Key、用量。
- 文件上传、反馈提交、移动端上传会话。
- 教程任务、客户端下载、意见反馈、个人资料弹窗。

### 当前 UI 迁移成果

- 当前分支提交 `002f45c` 已将 Web 前端主视觉迁移到本地 CatsCo 设计方向。
- 侧栏、空任务页、消息区、输入框、设置和协作弹窗已有统一样式基础。
- 最近一次针对当前未提交改动执行的生产构建成功：

```text
vite v8.1.4
1801 modules transformed
build/assets/index-BunVasVu.css 132.38 kB
build/assets/index-Cj6ef--u.js 487.17 kB
built in 1.25s
```

## 5. 当前正在进行的任务

当前没有继续开发新功能；用户已要求暂停并交接。工作区中保留了以下尚未提交的 UI 修正：

1. **新建任务弹窗脱离侧栏裁剪**  
   `sidepanel-view.jsx` 使用 `createPortal(..., document.body)` 渲染弹窗，避免弹窗被折叠侧栏或侧栏容器裁剪。内联样式已改为公共 class。

2. **创建群聊成员筛选器布局**  
   `create-group.jsx` 将“好友/Agent”分段控件放入搜索栏右侧，配合 CSS 缩小控件并统一布局。

3. **桌面端连接弹窗布局**  
   `desktop-connect-modal.jsx` 将状态、错误、成功提示和操作按钮改为 class 驱动，处理提示出现后内容拥挤和按钮间距问题。

4. **Agent 管理错误提示布局**  
   `agent-store-modal.jsx` 将内联错误提示改为统一 class，并在错误存在时压缩表单间距，避免弹窗内容溢出。

5. **公共 UI 样式补充**  
   `catsco-ui-system.css` 新增上述弹窗、成员筛选、新建任务和交互状态样式。

### 尚未完成的验收

- 新建任务弹窗的浏览器几何和视觉回归在交接请求到来时被中断。
- 上述 5 个文件尚未提交，也没有推送。
- 生产构建已通过；本次未提交改动之后的完整 Vitest 测试套件尚未重新执行。

## 6. 修改过的重要文件及作用

### 当前未提交文件

| 文件 | 当前改动作用 |
| --- | --- |
| `webapp/src/views/sidepanel-view.jsx` | 新建任务弹窗改用 React Portal；弹窗结构与空状态改为公共样式。 |
| `webapp/src/widgets/create-group.jsx` | 成员类型切换控件移动到搜索栏内部。 |
| `webapp/src/widgets/desktop-connect-modal.jsx` | 连接状态、错误提示和操作按钮重新分组。 |
| `webapp/src/widgets/agent-store-modal.jsx` | Agent 管理错误提示和错误态间距统一。 |
| `webapp/src/css/catsco-ui-system.css` | 为上述组件补充样式，并调整若干公共悬浮/按下状态。 |

当前工作区统计：5 个文件，约 286 行新增、60 行删除。Git 同时提示这些文件下次被 Git 写入时可能从 LF 转为 CRLF。

### 已提交但后续开发必须理解的核心文件

| 文件 | 作用 |
| --- | --- |
| `webapp/src/index.jsx` | React 启动入口，只挂载 `TinodeWeb`。 |
| `webapp/src/views/tinode-web.jsx` | 顶层控制器；管理认证、路由、用户、Topic、主题、模型、WS、桌面连接和全局弹窗状态。 |
| `webapp/src/views/sidepanel-view.jsx` | 左侧栏、会话、好友、群聊、Agent、新建任务和本地置顶状态。 |
| `webapp/src/views/messages-view.jsx` | 消息列表、任务过程、输入框、发送/停止和消息操作。 |
| `webapp/src/api.js` | 前端统一 API 与 WebSocket 客户端，是前后端契约的核心。 |
| `webapp/src/css/catsco-ui-system.css` | 当前 CatsCo UI 设计系统和大量组件样式，选择器覆盖面很大。 |
| `webapp/src/css/openchat-theme.css` | 原有主题和基础布局，仍与新样式共同生效。 |
| `webapp/src/widgets/create-group.jsx` | 创建群聊、好友/Agent 成员选择。 |
| `webapp/src/widgets/group-settings.jsx` | 群聊资料、成员、公告、角色等管理。 |
| `webapp/src/widgets/agent-store-modal.jsx` | Agent 管理和创建。 |
| `webapp/src/widgets/desktop-connect-modal.jsx` | 桌面端连接与下载。 |
| `webapp/src/widgets/feedback-modal.jsx` | 意见反馈和附件。 |
| `webapp/vite.config.js` | 开发代理、WebSocket 代理、构建与测试配置。 |
| `server/cmd/server.go` | 后端启动和所有 HTTP/WS 路由注册。 |
| `server/*.go` | 各业务 Handler 和服务实现。 |
| `pbx/model.proto` | 协议源文件；改动可能要求重新生成相关代码。 |

## 7. 当前遇到的问题

1. **工作区未清洁**  
   5 个前端文件有未提交改动。当前分支也没有显示上游跟踪分支，交接后不要直接切分支、重置或覆盖。

2. **项目功能未接入**  
   左侧“项目”目前是禁用占位项，`title` 明确提示项目接口尚未接入。项目创建、把历史任务移入项目、项目展开等不能视为已实现。

3. **真实模型连接依赖外部状态**  
   前端模型选择器不等于模型已经可用。聊天能否调用模型依赖后端、CatsCo 桌面端、Agent 绑定、Topic 和连接状态。仅启动 Vite 不能保证模型调用成功。

4. **开发代理固定指向本机 6061**  
   Vite 当前把 `/api`、`/local`、`/uploads`、`/v0` 代理到 `http://localhost:6061`。如果 Go 后端或代理服务没有运行，前端会显示网络/后端异常。

5. **部分旧源码中文存在编码异常风险**  
   在 `api.js`、`sidepanel-view.jsx` 和模型说明等旧代码中可见乱码文本。PowerShell 的显示编码也可能放大该问题，因此修复前应使用 UTF-8 方式确认文件字节，并在浏览器核对实际显示，不能盲目全局替换。

6. **视觉回归未完成**  
   最新未提交弹窗改动只完成了构建验证，尚未把所有二级界面在日间/夜间、展开/折叠、窄屏/宽屏下逐一检查。

7. **状态集中度不足**  
   顶层状态主要集中在 `tinode-web.jsx`，侧栏又维护独立业务状态。`api.js` 统一了通信，但目前没有独立的全局状态层或统一错误展示层；继续添加功能时容易重复状态和样式。

8. **生成目录和日志位于前端目录**  
   `webapp/build`、`webapp/node_modules`、`.vite-*.log` 都不是源代码。不要把它们当作修改对象，也不要无意提交。

## 8. 下一步开发建议

建议严格按以下顺序继续：

1. **先保护现场**
   - 查看 `git diff`，确认 5 个未提交文件都属于本轮 UI 修正。
   - 不要执行 `git reset --hard`、`git checkout --` 或覆盖式复制。

2. **完成当前改动的浏览器验收**
   - 启动 Go 后端或明确使用开发预览模式。
   - 启动 Vite。
   - 检查新建任务弹窗是否居中、是否脱离侧栏裁剪、关闭与空状态是否正常。
   - 检查桌面连接弹窗、Agent 错误状态、创建群聊筛选器。
   - 同时检查日间/夜间和侧栏展开/折叠。

3. **运行验证**

```powershell
cd E:\CodexProjects\cats-company\webapp
npm run build
npm test
```

4. **小范围提交当前 UI 修正**
   - 提交前复查 diff，不夹带 `build`、日志或依赖目录。
   - 建议使用单独提交，不与项目功能或接口重构混在一起。

5. **再处理结构性工作**
   - 把侧栏、消息、设置弹窗、协作和项目拆分为更清晰的组件边界。
   - 建立统一的请求状态和错误类型，至少区分：网络失败、401/登录过期、403、404、409、429、5xx、WebSocket 断线、模型/Agent 不可用。
   - 结构稳定后再补“项目”后端契约和界面，不要先用 `localStorage` 模拟成已完成产品功能。

6. **项目功能建议先定义契约**
   - 项目 CRUD。
   - 项目内任务列表。
   - 把已有会话移入/移出项目。
   - 创建空项目后首次发言自动创建项目内会话。
   - 明确会话是否允许同时属于历史任务与项目，再开发 UI。

## 9. 前后端通信方式（API、接口、数据格式）

### 9.1 开发环境地址和代理

`webapp/vite.config.js`：

```js
const backendTarget = 'http://localhost:6061';

proxy: {
  '/api': backendTarget,
  '/local': backendTarget,
  '/uploads': backendTarget,
  '/v0': { target: backendTarget, ws: true },
}
```

前端还支持：

- `VITE_API_BASE`：REST 基础地址，默认空字符串，即同源。
- `VITE_WS_URL`：WebSocket 地址；默认按当前页面协议生成 `ws(s)://<host>/v0/channels`。
- `VITE_DEV_BYPASS_AUTH`：开发环境免登录预览开关。

### 9.2 鉴权

- 登录 Token 存储键：`localStorage.oc_token`。
- 常规接口头：`Authorization: Bearer <token>`。
- JSON 请求头：`Content-Type: application/json`。
- Bot 代收好友申请等特殊接口使用：`Authorization: ApiKey <key>`。
- HTTP 非 2xx 时，`api.js` 优先读取响应 JSON 的 `error` 字段，并给抛出的 Error 附加 `status` 和 `data`。

### 9.3 主要 REST 接口

以下路径来自当前 `api.js` 与 `server/cmd/server.go`，不是设计草案。

| 域 | 方法与路径 | 主要请求数据 |
| --- | --- | --- |
| 登录 | `POST /api/auth/login` | 登录表单数据 |
| 注册验证码 | `POST /api/auth/send-code` | `{ "email": "..." }` |
| 注册 | `POST /api/auth/register` | 注册表单数据 |
| 重置密码 | `POST /api/auth/reset-password` | 重置表单数据 |
| 当前用户 | `GET /api/me` | 无 |
| 更新资料 | `POST /api/me/update` | `{ "display_name", "avatar_url" }` |
| 好友列表 | `GET /api/friends` | 无 |
| 待处理申请 | `GET /api/friends/pending` | 可选 `agent_uid` 查询参数 |
| 发送好友申请 | `POST /api/friends/request` | `{ "user_id", "message" }` |
| 接受/拒绝 | `POST /api/friends/accept`、`/reject` | `{ "user_id" }`；Agent 场景附带 `agent_uid` |
| 拉黑 | `POST /api/friends/block` | `{ "user_id" }` |
| 搜索用户 | `GET /api/users/search` | 查询参数 `q`、`mode` |
| 在线用户 | `GET /api/users/online` | 无 |
| 会话列表 | `GET /api/conversations` | 无 |
| 历史消息 | `GET /api/messages` | `topic_id`、`limit`、`offset`、可选 `latest=1` |
| 发送消息 | `POST /api/messages/send` | 见下方示例 |
| 创建群聊 | `POST /api/groups/create` | `{ "name", "member_ids": [] }` |
| 群聊详情 | `GET /api/groups/info?id=...` | 查询参数 `id` |
| 更新群聊 | `POST /api/groups/update` | `{ "group_id", "name", "avatar_url" }` |
| 邀请成员 | `POST /api/groups/invite` | `{ "group_id", "user_ids": [] }` |
| 群公告 | `POST /api/groups/announcement` | 群 ID 与公告内容 |
| Agent 列表 | `GET /api/agents` | 无 |
| 打开 Agent | `POST /api/agents/open` | `{ "agent_uid" }` |
| Agent 入口 | `GET/POST /api/agent-entries` | 创建时 `{ agent_uid, channel, access_mode, channel_app_id? }` |
| 桌面连接会话 | `POST /api/desktop-connect/session` | `{}` |
| 桌面连接状态 | `GET /api/desktop-connect/status?code=...` | 查询参数 `code` |
| Relay 配置 | `GET /api/relay/config` | 无 |
| Relay Key | `/api/relay/key` 及 rotate/reveal | 按具体操作 |
| 设备 | `GET /api/devices` | 无 |
| 设备配对 | `POST /api/device-connectors/pairings` | `{ device_name, capabilities: [...] }` |
| 上传 | `POST /api/upload?type=...` | `multipart/form-data`，字段 `file` |
| 反馈 | `POST /api/feedback` | 反馈表单数据 |
| 教程任务 | `GET /api/tutorial-tasks` | 无 |
| 健康检查 | `GET /health`、`GET /ready` | 无 |

发送文本消息的典型 REST 数据：

```json
{
  "topic_id": "p2p_xxx_yyy",
  "type": "text",
  "content": "你好"
}
```

消息接口还可携带：`content_blocks`、`mode`、`role`、`metadata`、`reply_to`。

### 9.4 WebSocket

连接地址：

```text
ws://<当前主机>/v0/channels?token=<JWT>
```

握手：

```json
{
  "hi": {
    "id": "<request-id>",
    "ver": "0.1.0"
  }
}
```

订阅在线状态：

```json
{
  "get": {
    "id": "<request-id>",
    "topic": "me",
    "what": "online"
  }
}
```

发布消息：

```json
{
  "pub": {
    "id": "<request-id>",
    "topic": "<topic-id>",
    "content": "消息内容"
  }
}
```

读取断线期间历史：

```json
{
  "get": {
    "id": "<request-id>",
    "topic": "<topic-id>",
    "what": "history",
    "seq": 123
  }
}
```

停止流式任务：

```json
{
  "pub": {
    "id": "<request-id>",
    "topic": "<topic-id>",
    "type": "stream_cancel",
    "msg_type": "stream_cancel",
    "content": "",
    "metadata": {
      "stream_id": "<stream-id>",
      "stream_event": "cancel",
      "control": "interrupt"
    }
  }
}
```

其他通知：

- 正在输入：`{ "note": { "topic", "what": "kp" } }`
- 已读：`{ "note": { "topic", "what": "read", "seq" } }`
- 自动重连延迟：1、2、4、8、15、30 秒。

### 9.5 前端本地状态

已确认的重要 `localStorage` 键：

- `oc_token`：JWT。
- `oc_user`：用户资料缓存。
- `cc_app_sidebar_collapsed_v1...`：侧栏折叠状态（按用户区分）。
- `cc_pinned_groups_v1...`：置顶群聊（当前为本地状态）。
- `catsco_selected_model`：模型选择。
- `catsco_theme`：主题。
- `v3_last_topic:<uid>` / `v3_last_topic`：最后打开的 Topic。
- `catsco_desktop_connect_prompted:v1:<uid>`：桌面连接提示日期。

最后 Topic 的缓存结构包含：

```json
{
  "topicId": "...",
  "name": "...",
  "isGroup": false,
  "groupId": "...",
  "avatar_url": "...",
  "friendId": "..."
}
```

## 10. 开发注意事项（哪些文件不要随意修改）

1. **不要直接编辑生成物**
   - `webapp/build/`
   - `webapp/node_modules/`
   - `webapp/.vite-*.log`
   - Go/Protobuf 生成文件（如存在）

2. **不要在未确认契约时修改 `webapp/src/api.js` 或 `server/cmd/server.go`**  
   这两个文件分别定义前端调用和服务端路由。端点、字段、鉴权或错误格式变化必须前后端同步，并补测试。

3. **不要随意修改 `pbx/model.proto`**  
   协议变化可能需要重新生成代码，并影响服务器、客户端和 Bot。

4. **谨慎修改两个全局 CSS 文件**  
   `openchat-theme.css` 和 `catsco-ui-system.css` 同时生效，存在大量全局选择器。新增样式前先确认公共变量和现有选择器；修改后必须在浏览器检查日间/夜间、弹窗、侧栏展开/折叠和不同视口。

5. **不要把本地原型文件直接覆盖进 React 工程**  
   `E:\CodexProjects\mini-claude-code\chat-ui\index.html` 是早期本地 UI 参考，不是当前 PR 的运行入口。视觉可以参考，功能必须按 React 组件和当前 API 契约迁移。

6. **不要提交密钥或真实账号信息**  
   只提交 `.env.example` 的变量名和安全示例。真实 JWT、邮箱密码、服务器密码、Relay Key、Bot API Key、SSH 凭据都不得写入源码、文档、测试快照或 Git 历史。

7. **保留当前未提交改动**  
   交接时工作区并不干净。除非逐行确认，不要回滚、覆盖或格式化这 5 个文件。

8. **注意换行符**  
   Git 已提示当前修改文件下次写入时可能从 LF 变为 CRLF。提交前检查 diff，避免整文件换行符变化掩盖真实改动。

9. **锁文件只在依赖变化时修改**  
   不要无原因更新根目录或 `webapp` 的 `package-lock.json`。

10. **先验证再提交**
    - 至少执行 `npm run build` 和 `npm test`。
    - 涉及后端时执行对应 Go 测试。
    - 涉及视觉时必须直接在浏览器检查，不能只看代码或静态选择器。

## 交接后的第一步

新的 Codex 任务开始后，建议先执行：

```powershell
cd E:\CodexProjects\cats-company
git status --short --branch
git diff -- webapp/src/views/sidepanel-view.jsx
git diff -- webapp/src/widgets/create-group.jsx
git diff -- webapp/src/widgets/desktop-connect-modal.jsx
git diff -- webapp/src/widgets/agent-store-modal.jsx
git diff -- webapp/src/css/catsco-ui-system.css
```

确认现场无外部变化后，再继续浏览器验收或提交当前修正。
