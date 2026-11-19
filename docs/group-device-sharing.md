# 群聊设备共享

设备所有者可以把一台电脑的使用权授予同群真人。许可持续到撤销，适用于该群当前的 Bot；默认设备仍按使用者、群、Bot 分别保存。物理 owner 与实际 actor 始终分开。

## 运行链路

1. 网页在群聊的「设备与授权」面板显示本人电脑及已共享给自己的电脑。所有者勾选其他真人，主动授予或撤销；使用者在对应 Bot 列选择默认设备。
2. 用户也可以要求 Bot 申请。XiaoBa 的 `request_device_access` 工具提交当前可信消息 ID、设备所有者名字和用途。后端从原始消息读取实际发起人，验证该消息确实提到了调用 Bot，再在当前群成员中唯一匹配所有者。
3. 后端保存申请，生成带类型引用的群消息。数据库仍使用既有 `msg_type=text`，卡片放在 `content_blocks` 的 `card` 块中。公开内容只有申请说明与请求引用。只有所有者查询到自己的私有电脑列表并能批准、改选或拒绝；Bot 不能批准。
4. 批准在 PostgreSQL 事务内写入许可并更新申请。Web 根据服务端版本轮询同步。卡片处理按钮还要求引用与原始群和消息 ID 一致；复制的卡片不能作为审批入口。
5. A 下一轮消息发送给群内 Bot 时，后端签发 actor=A、owner=B 的短期执行 grant。Bot 经既有 WSS 的 Device RPC / Thin Tool RPC 转到 B 的 Connector，结果通过原请求返回 Bot，再按群聊规则发送回复。

批准不会自动续跑原消息中的任务；申请工具暂停当前轮，等待用户下一条指令。

## 数据及接口

新增迁移 `server/db/migrations/postgres/000080_group_device_sharing.up.sql`，与启动时 CreateSchema 的新增 DDL 保持一致：

- `group_device_shares`：owner、grantee、group、device 的唯一关系，允许操作、状态、版本及时间。
- `group_device_share_requests`：申请人、所有者、说明、幂等键、状态、版本、所选设备、关联许可和卡片消息。
- `group_device_share_bindings`：配对 Connector 的安装身份、绑定 epoch、解除绑定时间。旧配对 Token 和旧 epoch 不能恢复已解绑许可。
- `conversation_device_selections.owner_uid`：可空，旧记录按 owner=actor 解释。设备的完整身份是 owner+device，不能只凭 device ID 选中另一人的同名目标。

HTTP 入口均使用现有认证与群成员访问校验：

| 路径 | 能力 |
| --- | --- |
| `GET /api/groups/device-shares?topic_id=…` | 本人发出/收到的许可、已获共享的设备及群成员 |
| `POST /api/groups/device-shares` | 所有者 `share` / `revoke`，要求 `expected_revision` |
| `GET /api/groups/device-share-requests?topic_id=…&request_id=…` | 卡片最新状态；私有设备列表只返回所有者 |
| `POST /api/groups/device-share-requests` | `create` / `approve` / `reject`；审批要求所有者真人和版本 |
| `/api/conversations/device-selection` | 既有选择接口增加 `owner_uid`，旧客户端省略时解释为本人 |

JSON 不接受额外 actor/requester 字段。创建申请不能把模型给出的设备名称当作已验证设备 ID，首版统一由所有者在私有列表中确认实际电脑。

## 执行与撤销边界

新协议能力名为 `group-device-share.v1`，Bot 和目标 Connector 都须在握手中声明。未升级的接收端显示需要升级，不能获得可执行的共享路由。本人设备和既有 `channel_identity_link` 委托保持原协议。

执行 grant 使用 `identitySource=group_device_share`、`shareId`、`shareRevision`，并绑定 actor、owner、会话、群、Bot/运行体、设备及操作。消息元数据字段使用 camelCase；RPC 转发使用 `identity_source`、`share_id`、`share_revision` 等 snake_case 字段，由后端从已验证 grant 填入。

共享 grant 为 10 分钟短期授权，Redis 保存执行凭证及按许可建立的清理索引。PostgreSQL 是共享许可的权威来源：查询 grant、续期、最终 RPC 派发均复核当前许可版本、双方和 Bot 的群成员关系、账号状态、绑定 epoch 及操作交集。SQL 不可用时拒绝跨用户执行，Redis 中的旧 grant 或并发重新写入不能恢复旧许可。

撤销、任一授权双方退群、群解散、设备解绑会使后续调用失效。失效/离线的默认目标保留为不可用，不自动换到自己的电脑或 Cloud。重复请求和状态变更使用幂等键与 CAS；批准、撤销、退群和解绑通过事务与锁协调。

撤销不终止已经在设备上启动的命令。本功能不增加设备级任务锁；多人同时操作同一设备仍可能产生文件或桌面冲突。执行结果可能被发送到群内。

## 发布与回滚

先在目标租户 schema 验证/应用 080，随后发布 CatsCompany 后端与 Web，再发布带新能力的 XiaoBa Bot 和 Connector。运行时沿用既有 XiaoBa 编译与 Worker 分发流程，无需增加服务。旧 Connector 允许继续用于本人的任务，共享调用等待升级。

回滚旧后端前，先撤销共享并显式把所有非本人默认选择改回本人的设备，避免旧二进制忽略 owner 字段。保留许可/申请表作为记录；down 迁移在有记录或跨用户选择时主动拒绝删除，不能强制清空业务证据。

## 本地验证（2026-11-19）

后端基线 `6e273585`，搭配 XiaoBa `af1494c9` 的 feature worktree。PostgreSQL 17.10、Redis 7.4、进程内设备注册表，两个 Cloud 与两个独立 Connector。

已验证自然语言申请、仅所有者批准、私有目录隔离、重复批准、默认共享读取、显式共享读取且不改变默认设备。读取的随机标记仅存在于 pcB，Cloud B 没有同名文件。

两个真实 Chrome 会话验证卡片同步、默认选择、服务端重启后保留共享目标、UI 撤销后不换机。固定发布版 XiaoBa `c3facde9` 作为旧 pcB，验证共享选择返回 409 / 需要升级，同时所有者经真实模型仍能读取本机文件。

自动检查覆盖 Go 授权/设备/RPC、PostgreSQL 事务与旧选择升级、XiaoBa 委托及路由、前端卡片及设备面板。TypeScript 类型检查和 Web 发布构建通过。PostgreSQL 全包检查还发现现存 `TestPostgresCommercialImage26ModelsFillPlansAndRollBack` 的套餐断言失败，已在未修改 main 上复现相同结果。扩展运行旧 `tool-gateway-catsco.test.ts` 有 23 项失败；相同未修改 main 上也失败 23 项，属于现存旧测试与路由实现的差异。

尚未覆盖真实 macOS/Windows Connector 的系统操作、完整生产历史数据库快照升级，以及两套 HTTP 租户实例之间的隔离验收；本地隔离 schema 和旧表升级测试不能替代这些发布前检查。
