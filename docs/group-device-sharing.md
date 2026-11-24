# 群聊设备共享

设备所有者可以把一台电脑的使用权授予同群真人。许可持续到撤销，适用于该群当前的 Bot；默认设备仍按使用者、群、Bot 分别保存。物理 owner 与实际 actor 始终分开。

## 运行链路

1. 网页在群聊的「设备与授权」面板显示本人电脑及已共享给自己的电脑。所有者勾选其他真人，使用者在对应 Bot 列选择默认设备；这些改动先暂存，必须点击面板右上方「确认」才提交。单 Bot 私聊也使用显式确认。
2. 用户也可以要求 Bot 申请。XiaoBa 的 `request_device_access` 工具提交当前可信消息 ID、设备所有者名字和用途。后端从原始消息读取实际发起人，验证该消息确实提到了调用 Bot，再在当前群成员中唯一匹配所有者。
3. 后端保存申请，生成带类型引用的群消息。数据库仍使用既有 `msg_type=text`，卡片放在 `content_blocks` 的 `card` 块中。公开内容只有申请说明与请求引用。只有所有者查询到自己的私有电脑列表并能批准、改选或拒绝；Bot 不能批准。
4. 批准在 PostgreSQL 事务内写入许可并更新申请。Web 根据服务端版本轮询同步。卡片处理按钮还要求引用与原始群和消息 ID 一致；复制的卡片不能作为审批入口。
5. 设备所有者 A 授权使用者 B 后，后端为 B 的群消息提供 actor=B、owner=A 的无固定期限权限引用，每次调用仍检查当前许可。转发到 pcA 时转换为 actor=A、owner=A 的普通 Device RPC / Thin Tool RPC；服务端 pending 和审计保留 B，返回 Bot 时恢复 B 的调用上下文。Connector 无需理解群共享。

批准不会自动续跑原消息中的任务；申请工具暂停当前轮，等待用户下一条指令。

## 数据及接口

新增迁移 `server/db/migrations/postgres/000082_group_device_sharing.up.sql`，与启动时 CreateSchema 的新增 DDL 保持一致：

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

`group-device-share.v1` 只作为 Bot 支持共享目标元数据的能力标记，Connector 不再需要声明。旧 Connector 使用既有所有者 RPC 协议；未支持共享目标的 Bot 仍需升级。本人设备和既有 `channel_identity_link` 委托保持原协议。

执行 grant 使用 `identitySource=group_device_share`、`shareId`、`shareRevision`，并绑定 actor、owner、会话、群、Bot/运行体、设备及操作。Bot 的消息元数据保留 camelCase 共享字段。后端校验后，发给 Connector 的 RPC 使用所有者身份和既有 `device_rpc_forward` 来源，不携带共享许可字段；原实际使用者及许可范围仅留在服务端并在响应中恢复。

共享权限引用复用现有无固定期限机制：服务端 permanent=true，兼容旧客户端的有限数值 expiresAt 使用 9999 年哨兵值，不签发十分钟租约，也无需心跳续期。Redis 中该记录与索引不设 TTL，撤销/选择切换/解绑时清理。PostgreSQL 仍是共享许可的权威来源：查询和每次派发都复核当前许可版本、双方和 Bot 的群成员关系、账号状态、绑定 epoch 及操作范围。SQL 不可用时拒绝执行，缓存不能恢复已撤销许可。每一条 RPC 的命令/传输超时仍保留，与用户授权期限无关。

撤销、任一授权双方退群、群解散、设备解绑会使后续调用失效。失效/离线的默认目标保留为不可用，不自动换到自己的电脑或 Cloud。重复请求和状态变更使用幂等键与 CAS；批准、撤销、退群和解绑通过事务与锁协调。

撤销不终止已经在设备上启动的命令。本功能不增加设备级任务锁；多人同时操作同一设备仍可能产生文件或桌面冲突。执行结果可能被发送到群内。

## 发布与回滚

先准备包含本功能的 XiaoBa 稳定 Worker 包，再发布 CatsCompany 后端与 Web，最后灰度安装 Worker 包。生产 `remote-deploy.sh` 在替换 API 前运行候选镜像的 `--prepare-postgres-schema`，由 `CreateSchema()` 在目标租户 schema 创建共享表并补充 owner 列；显式 082 SQL 与启动 DDL 对齐，正常部署不自动执行完整版本迁移链。发布前备份并确认各租户 search_path，失败可能保留部分已执行 DDL。运行时沿用既有 XiaoBa 编译与 Worker 分发流程，无需增加服务。旧 Connector 可用于本人及后端已批准的共享调用，无需因本次转换额外升级。

回滚旧后端前，先撤销共享并显式把所有非本人默认选择改回本人的设备，避免旧二进制忽略 owner 字段。保留许可/申请表作为记录；down 迁移在有记录或跨用户选择时主动拒绝删除，不能强制清空业务证据。

## 本地验证（2026-11-19）

后端基线 `6e273585`，搭配 XiaoBa `af1494c9` 的 feature worktree。PostgreSQL 17.10、Redis 7.4、进程内设备注册表，两个 Cloud 与两个独立 Connector。

已验证自然语言申请、仅所有者批准、私有目录隔离、重复批准、默认共享读取、显式共享读取且不改变默认设备。读取的随机标记仅存在于 pcB，Cloud B 没有同名文件。

两个真实 Chrome 会话验证卡片同步、默认选择、服务端重启后保留共享目标、UI 撤销后不换机。早期曾用固定发布版 XiaoBa `c3facde9` 验证共享选择返回 409；该行为已被后续的服务端所有者代执行方案替代，不再作为当前验收标准。

自动检查覆盖 Go 授权/设备/RPC、PostgreSQL 事务与旧选择升级、XiaoBa 委托及路由、前端卡片及设备面板。TypeScript 类型检查和 Web 发布构建通过。PostgreSQL 全包检查还发现现存 `TestPostgresCommercialImage26ModelsFillPlansAndRollBack` 的套餐断言失败，已在未修改 main 上复现相同结果。扩展运行旧 `tool-gateway-catsco.test.ts` 有 23 项失败；相同未修改 main 上也失败 23 项，属于现存旧测试与路由实现的差异。

尚未覆盖真实 macOS/Windows Connector 的系统操作、完整生产历史数据库快照升级，以及两套 HTTP 租户实例之间的隔离验收；本地隔离 schema 和旧表升级测试不能替代这些发布前检查。


## 确认与对话回执（2026-11-20）

确认接口先保存实际设置，再保存一条普通文本消息，以操作用户为发言人。
授权、撤销和默认切换分别给出具体设备、对象或 Bot；消息 metadata 标记
`catsco_device_notice=true`，持久化后仅通过真人 WebSocket 推送，跳过 Bot 和配对 Connector，
不会将提示作为新的 Agent 执行请求。新私聊会先创建 topic，允许确认成为第一条消息。
重复的同一许可版本不会生成第二条提示。默认切换依原选择 revision 校验。

响应附带 `notice.message_id`，若回执写入失败则是 `notice.pending=true`；这时实际已保存的
设置保持有效，界面明确提示聊天回执待核对。不能把回执失败解释成授权未保存，也不自动重放授权。
群聊多项修改依次提交，部分失败会显示已确认数量并要求刷新重新选择；它不是全表原子事务。
取消更改不产生服务端写入。切换登录账号会丢弃旧账号草稿并停止余下批量请求。

额外兼容修正：某个停用 Bot 目录失败不隐藏其它 Bot；共享存储失败时本人设备仍可用，
已有共享默认保持不可用而不换机。跨用户 grant 续期按物理 owner 查设备，并复核许可和能力；
客户端验证续期保留 shareId/shareRevision。本人设备沿用原来的默认/显式授权行为。


## 默认设备可为空（2026-11-20）

群聊每个 Bot 列及私聊设备列表均提供「未指定」。点击确认后，POST device-selection 显式提交空 device_id；缺少该字段不能当作清空。后端仍使用 CAS，保存 device_id 为空、source=user_unspecified 的持久记录并更新 revision，清理旧执行 grant。GET 返回 status=unspecified，不自动选回唯一设备。此状态只清除 actor/topic/Bot 的默认偏好，不撤销设备共享。

消息中的设备选择保持兼容状态 needs_selection，但无默认目标；运行时提示改为根据任务判断已授权的准确设备目标，不再要求必须先设置默认设备。确有歧义时仍询问；已选目标不可用时继续阻断，不因清空功能绕过授权。未设置默认时不签发默认别名 grant，原有逐设备显式 grant 仍按实际权限签发，任务完成也不写回默认。

在独立 grp_4 验收：清空后刷新和 server 重启仍为空，真实模型按「我自己的本地设备」成功读取 pcA，默认未被写回；共享许可未随清空撤销，剩余唯一设备也未自动选中，可再次选择后重新清空。前端 173 项、XiaoBa 扩展 84 项及 Go Device/ThinTool/Group 回归通过。证据位于 /Users/kuaifang/work/tmp/kuai-unset-device-20261120。本次更新服务端、Web 和 Cloud Bot 提示/目标解析；没有改变 Connector 协议，PC 可继续使用原安装包。


## 最终审阅修正

解散群组先按 user_id 顺序锁定成员，再更新许可，与审批的加锁顺序一致，避免权限行和成员行之间的循环等待。旧版无凭证 Thin Tool RPC 兼容请求保持可用；携带新的共享/会话来源字段则必须提供完整有效 grant，不能借兼容路径转发未经校验的来源。XiaoBa 续期时将 owner 与 device_id 一起用于替代判断，同 ID 的另一所有者授权不会使原授权停续期。上述场景均有修复前失败、修复后通过的定向测试；SQL 并发测试使用真实 PostgreSQL 隔离 schema。


## 外部审查加固（2026-11-23）

- 旧无 grant 的普通 Thin Tool RPC 仅允许 Bot 访问数据库确认的 Bot 所有者设备；目标属于其他用户、归属未知或读取失败均要求 grant。新 Bot 仍须携带完整 grant。该限制主动收紧旧版跨用户兼容旁路；旧 Bot 服务其他用户需升级。
- 缺少可信 deviceSelection 时，不从 deviceGrants 第一项推断 speaker_default，运行时快照也不把它显示为就绪。默认 grant 与当前选择 revision 不符时在客户端派发前拒绝；服务端原有的选择版本及许可实时复核继续保留。
- 首次只有一台共享设备，也必须确认后才能成为默认设备；本人唯一设备和既有可信身份委托保留原自动选择行为。显式选择「未指定」继续保持为空，独立显式授权不受默认偏好影响。
- 两种 RPC 接收端对 group_device_share 要求完整 owner、actor、grant、session、群、Bot、许可版本及未过期截止时间，owner 必须匹配本机绑定，目标必须匹配本机设备；共享 owner 不能回退为 actor。旧的非共享 RPC 兼容不变。
- Bot 代发的申请卡片以认证 Bot 为消息作者，申请记录中的 requester 仍是提到该 Bot 的原始真人；卡片保存并显示 Bot、代申请用户和原消息编号。相同 Bot / 原消息 / 所有者沿用幂等键，不新增重复卡片。历史申请卡片不改写。
- 客户端保留旧 grant 不等于撤销后还能执行：服务端查询、续期和派发仍按当前许可及选择版本拒绝。撤销不终止已启动命令的边界不变。

此前接收端的直接共享 RPC 校验保留作为兼容防御；当前共享调用由后端转为所有者 RPC，不要求升级 Connector。


## 所有者代执行与无期限许可（2026-11-23）

按最终约定，pcA 只处理所有者 A 的普通请求，后端负责检查群内使用者 B 的真实权限。B 的群消息、申请人和后端审计不能改成 A；两种 RPC 的返回值恢复原 B 会话，防止所有者身份泄漏到调用方的消息范围。Thin Tool pending 仅保存必要身份元数据，不保留工具 payload；服务端现有内存/Redis 审计记录真实 actor。群消息及共享许可已在 PostgreSQL；本轮没有新增长期逐工具审计表。

Bot 仍须支持共享设备元数据；`group-device-share.v1` 不是新网络服务或 Connector 协议。Connector 原有的绑定、设备身份和 RPC 超时校验照常执行。新共享权限不依赖 10 分钟 / 24 小时续期链；旧版有期限记录的续期兼容接口保留，不影响其他既有行为。


## 永久引用的派发回归约束（2026-11-23）

TestGroupSharedRPCRevalidatesPermanentGrantBeforeDispatch 从真实 handleMessage 入口覆盖 Device RPC / Thin Tool RPC，组合内存 / Redis 缓存与有效、撤销、许可版本变化、退群、绑定变化、操作移除、权威存储故障七种状态，共 28 组场景。测试不清理旧 grant，断言请求派发读取当前许可；失效时设备收不到请求，不残留 pending，调用者收到拒绝；有效对照仍能按所有者协议到达设备。

这约束实际的授权结果，而非某个辅助函数的调用次数。lookupGrant 已统一复核，独立路径上的额外检查是防御层；未来新增设备 RPC 入口必须加入同一测试矩阵，现有测试不可能自动覆盖未知的新入口。pending 的 deviceGrants 合并逻辑本轮未改。


## 当前完整授权列表更新（2026-11-23）

实时用户消息为 Bot 携带当前会话的完整设备授权列表：没有可执行权限时，device_grants 明确发送 []；V2 显式设备列表也发送 []。历史回放继续省略这些字段，不能重新签发或覆盖正在执行的授权上下文。

XiaoBa 保留「未提供更新」与「明确为空」的区别：undefined 不更新，[] 清空；非空列表经过可信来源和会话范围校验后替换旧列表。一次消费多条排队消息时只取最新提供的完整列表，不将历史授权做并集。runner 同样替换列表，并重建设备运行时上下文；重新授权后不会被旧目标失败状态卡住。附件和 Skill connector 授权仍沿用各自原合并规则。

这是收到服务端新上下文时的数据更新，不是数据库对象与 Bot 内存自动绑定；本轮没有新增数据库变更推送或轮询服务。新上下文到达前，后续 RPC 仍由服务器实时复核和拒绝已撤销权限。通用 permission_denied、网络超时和存储故障不能被客户端直接解释成「所有者已经撤销」，因此本轮不据此篡改本地授权状态。

本轮需要更新 server 和 Bot 运行时代码；Connector 不使用此 pending React loop，不需要因此重装。Bot 侧 group-device-share.v1 兼容门控仍保留，删除它未包含在本次列表同步改动中。

## 跨节点与授权范围确认

共享调用按有效设备路由判断在线状态，不要求 Connector 与 Bot 连到同一个后端进程。Device RPC 沿用共享 pending；Thin Tool RPC 在设备节点建立仅含路由的接收记录，经服务端 Redis envelope 将结果送回原节点，按原 pending 校验来源与接收者连接并恢复实际 actor。该 envelope 不传给 Connector，不新增客户端设备协议。原节点与设备节点都会清理完成的 pending；重复、过期和错误连接的结果不能消费原请求。

Thin Tool RPC 按节点和连接分别计算每个请求者的 64 条额度，同一所有者的同一台设备仍共用 32 条额度。设备节点拒绝超额请求时，通过 Redis 将繁忙结果返回原节点，原节点确认是同一次请求后结束等待。

解绑只使指向该 owner+device 的许可及申请失效，尚未选设备或指向其它设备的 pending 申请保留。后续审批仍按当前绑定 epoch 校验，保留申请不恢复旧设备权限。

所有者审批卡及直接共享的待确认区域展示服务端 `share_operations`。界面把用户看到的操作列表作为 `expected_operations` 提交；列表与待保存权限不一致时返回 409，要求刷新确认。此字段为兼容旧所有者客户端而可省略；新界面始终携带已显示的列表，未知范围不允许确认。范围提示只在授权操作时出现，不新增设备表。

多节点升级应先完成全部 server 节点更新，再开放新版本共享功能。新旧后端混跑期间，旧节点不理解新的服务端 Thin RPC relay，跨节点调用可能失败；Connector 不需要因此升级。

## 验证覆盖与剩余边界

| 场景 | 当前证据 | 尚未证明的范围 |
| --- | --- | --- |
| 共享 RPC 跨节点 | `TestGroupSharedRPCAcrossProcesses` 启动两个独立 OS 测试进程，Bot/设备连接记录分别只在各自进程；CI 使用真实 Redis，验证默认已选和默认留空两种情况的两条 RPC 请求/结果、真实 actor、pending 清理、续授权与撤销。 | 客户端收件箱和权限库为 fixture，没有声称是完整浏览器/原生 Connector E2E。 |
| 路由、结果来源与并发额度 | `TestThinToolRPCRelay*` 验证错误来源、错误接收者、重复结果、过期请求、设备不匹配及不同 Bot 的额度隔离。双进程用例验证设备达到上限时，实际通过 Redis 向 Bot 返回繁忙结果。 | 进程崩溃、网络分区、滚动升级的全部排列尚未验收；Pub/Sub 与内存 pending 不保证结果必达或恰好一次执行。 |
| SQL 与迁移 | 独立 PostgreSQL 17.10 验证 082、旧 owner 空值、CAS、审批/撤销/删群并发，以及解绑不影响无关申请；CI 的 `group_device_sharing` 任务明确选中这些测试。 | 使用最小历史 schema 与一次性测试 schema，不等于完整生产历史快照升级或 HTTP 多租户隔离验收。 |
| 授权范围 | API 与 Web 回归验证实际操作预览、确认范围不匹配拒绝和未知范围阻止确认。 | 原生桌面系统操作、所有平台安装与更新没有在 Linux 容器中得到证明。 |
| 完整业务链 | 本地已有两人、两 Cloud、两独立 PC 及真实安装包分发环境；此前完成过真实模型与旧 Connector 的授权读取验收。 | 尚无自动化覆盖全部浏览器→server→SQL/Redis→真实 Bot→真实 Connector→文件效果的组合矩阵。独立组件通过不能替代这项联合验收。 |

普通聊天入口/群广播对远端 Bot 的投递也需要单独的多后端 E2E 验证；本次设备 RPC 的跨节点测试不能证明任意负载均衡下的聊天投递正确。

撤销只阻止后续派发，已启动命令不被强制终止。网络中断后不能盲目重放写文件或命令请求；本功能不提供设备互斥或操作级恰好一次保证。跨多项设置的确认仍逐项提交，回执与设置不是同一个事务。
