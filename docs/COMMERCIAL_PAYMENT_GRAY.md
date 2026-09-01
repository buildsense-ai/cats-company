# 商业化支付灰度

本阶段在原有套餐、邀请码、人工调额和 Relay 对账基础上，增加可售套餐、体验包、订单、灰度测试支付、支付宝电脑网站支付以及自动额度同步。

## 默认安全状态

- `CATS_RELAY_COMMERCIAL_ENABLED=0`：不向普通用户开放商业化入口。
- `CATS_COMMERCIAL_TEST_PAYMENT_ENABLED=0`：不开放测试支付。
- `CATS_ALIPAY_ENABLED=0`：不初始化支付宝客户端；回调、主动查单和新下单都会停止。
- `CATS_ALIPAY_SALES_ENABLED=0`：不开放新订单，但保留已创建订单的回调、主动查单和补单能力。停卖时应先关闭此开关，不要直接关闭 `CATS_ALIPAY_ENABLED`。
- `CATS_ALIPAY_PRODUCTION=0`：使用支付宝沙箱；生产 Compose 默认显式设为 `1`。
- 新增套餐的 `sale_state` 默认是 `hidden`，旧套餐升级后也不会自动出现在购买列表。
- 应用私钥只从容器内的 secret 文件读取，不支持写进前端或仓库配置。
- 支付回调只保存订单字段和 SHA-256 摘要，不保存原始通知正文。

## 套餐价格与当前开放状态

套餐价格自 2026-08 起与官网个人方案统一为 Free、Personal 和 Pro。Free 会把账号原有基础模型额度迁入同一个共享额度池；非标准手调额度归入“内部保留套餐”，按原值继续生效。Personal 和 Pro 通过订单履约，并与仍有效的内部保留额度共同进入账号共享池。内部 SOL 等价 token 按 Relay 当前 SOL 等价价表折成 CNY 执行额度，不返回给普通用户。

| slug | 套餐 | 价格 | 内部 SOL 等价 token | Relay 总额度 | 有效期 | 限购 | 灰度默认 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| `catsco-free` | Free / 免费版 | ¥0 | 继承默认基础额度 | 账号共享额度池 | 长期 | - | 隐藏系统套餐，不进入订单目录 |
| `catsco-personal` | Personal / 个人版 | ¥399 | 2 亿 | ¥10500 | 30 天（月套餐） | 不限 | `test` |
| `catsco-pro` | Pro / 专业版 | ¥799 | 6 亿 | ¥31500 | 30 天（月套餐） | 不限 | `test`，用户侧标记推荐 |

### Personal 升级 Pro 的时间语义

Personal → Pro 采用“立即切换”，不顺延剩余时间：支付宝确认支付成功后，旧 Personal entitlement 和其套餐额度立即标记为 `revoked`，旧套餐剩余天数不折算、不累加到新套餐。Pro entitlement 的 `starts_at` 取支付成功时间，`expires_at` 从该时刻重新计算 30 天；历史订单、撤销记录和额度账本仍永久保留供审计。用户确认购买时会看到这一规则，避免把升级误解为续期。

Personal 和 Pro 每次成功支付各赠送 1 次云托管员工创建权益。创建失败会释放该次权益；创建成功后即消耗。当前套餐可通过“续费”按钮再次购买：续费周期从当前套餐到期时间顺延 30 天，早续不会损失剩余天数；升级仍按立即切换规则处理。平台不签约支付宝自动扣款，所有续费均由用户手动发起普通支付宝支付；支付成功后，控制面才调用天翼云 `ResubscribeEcsInstance` 续订或恢复实例。套餐到期时 CatsCompany 只归档生命周期，不主动退订云主机，让实例自然进入天翼云 `expired/freezing` 冻结保留状态；保留期内续费会恢复实例并再次强制关闭云侧自动续费。`unsubscribed`、`released` 或 `deleted` 实例不可恢复，续费脚本会明确拒绝，也不会自动新建替代实例。Personal → Pro 立即升级时，旧 Personal 尚未使用的创建权益会一并撤销，Pro 只保留新套餐赠送的 1 次；已经进入创建流程的预留权益会继续完成并计为已使用。退款只撤销尚未使用/预留的创建权益和模型额度；已经创建的云员工不因退款立即销毁，继续运行至当次套餐到期，再按正常 15 天保留期和生命周期清理规则处理。用户侧不提供云托管员工永久删除能力；删除会话或从任务列表移除只影响任务入口，不影响云员工。云员工使用期限跟随当次套餐 30 天有效期；到期后天翼云进入 15 天冻结保留期，实例不可用且不收费，用户应在保留期内续费以触发恢复；超过窗口后由内部生命周期任务执行退订和永久销毁，随后清理 key pair、本地状态、机器人记录与生命周期记录。订单、支付与权益审计记录永久保留。

换算采用 `SOL 等价 token ÷ 1,000,000 × 7.5 USD × 7 CNY/USD`，即每 100 万 SOL 等价 token 对应 ¥52.5 Relay 额度。等价 token 已包含价差权重：普通输入 `1x`、输出 `6x`、缓存读取 `0.1x`、缓存写入 `0.2x`，不能再把缓存或输出重复加价。

当前新套餐开放全部六个公开模型：MiniMax M2.7、MiniMax M3、DeepSeek V4 Flash、GPT-5.6 Terra、GPT-5.6 Sol 和 GPT-5.6 Luna。六个模型共用同一个模型服务额度池；套餐表中的分模型数值只用于声明可用模型并组成共享总额，不是六份独立额度：Personal 在六个模型下各记 ¥1750，汇总为 ¥10500；Pro 各记 ¥5250，汇总为 ¥31500。任一模型的调用都按自身价格倍率从同一池中扣减，因此不同模型的 token 消耗速度可以不同，但不会把套餐总额复制六次。Pro 的内部等价 token 与共享总额均为 Personal 的 3 倍，对应官网“约 3 倍任务容量”的表达。新增公开模型时必须同步更新套餐模型集合、Relay provider scope、价格覆盖和存量权益迁移，不能只改模型目录。

用户端只显示 Free / Personal / Pro、工作强度、已用百分比和剩余百分比，不显示 SOL 等价 token、CNY 执行额度、模型预算或内部成本。用户商业化接口也不返回套餐的 `internal_quota_tokens`、`model_budgets` 和 `monthly_budget_cny`。

旧 `catsco-trial-3d`、`catsco-plus-minus`、`catsco-plus`、`catsco-plus-plus`、`catsco-team-monthly` 套餐应转为 `hidden`，停止创建新订单；已有订单和有效权益继续按原快照履约，不重写历史名称、金额或额度。`CATS_COMMERCIAL_TRIAL_PLAN_SLUG` 如需继续使用，只能指向售价为 0、状态为 `hidden` 且包含有效额度的独立内部体验计划，不能指向 Free 展示卡。

全量迁移由 Relay key 现有策略反向建立套餐基线，且必须幂等：

- 默认 `MiniMax M2.7=1000`、`MiniMax M3=500`、`DeepSeek V4 Flash=100` 归为 `catsco-free`。
- 任何非默认额度或额外模型归为 `catsco-legacy-custom`，界面显示“内部保留套餐”。
- 已有有效付费、邀请码或试用权益不重复导入；原有手调额度不归零。
- 迁移周期沿用 Relay 当前最早有效 `last_reset`，避免全量切换时无故重置已用量。
- 新注册或自助创建 Relay key 后自动初始化；全局 enforce 启动时仅扫描已配置 key 的账号。
- 单个账号迁移未完成前，模型菜单继续显示旧额度，不能先进入空共享池。

当前权益和 Relay 同步仍按单个 UID 发放。团队成员、席位和共享额度归属机制完成前，不新增团队公开套餐。

本次六模型修复随 CatsCompany 数据库 schema migration 执行：会更新 Personal/Pro 套餐与未履约订单快照，并把仍有效的旧 Terra/Sol 基础授权按原有效期、原共享总额重建为六模型授权。应用启动后的 Relay reconcile 会把每个已配置 UID 的 provider scope 一并补齐；无需手工给单个用户重复充值。

`monthly_budget_cny` 目前只作为后台账本字段保留，不能用于可售套餐或体验包。可履约套餐必须只配置明确的分模型额度；同时配置月总额度和模型额度也会被购买链路拒绝，避免 `*` 额度被记账但没有写入 Relay。

## 灰度流程

1. 在账号后台创建或更新套餐，设置价格，并将 `sale_state` 设为 `test`。
2. 配置 `CATS_RELAY_COMMERCIAL_TEST_UIDS=<uid>`，只让灰度账号看到套餐。
3. 配置 `CATS_COMMERCIAL_TEST_PAYMENT_ENABLED=1` 和 `CATS_COMMERCIAL_TEST_PAYMENT_UIDS=<uid>`。
4. 如需支付后立即写入 Relay，再配置 `CATS_RELAY_COMMERCIAL_ENFORCE_UIDS=<uid>`。
5. 用户在“模型服务”中创建订单并点击“完成灰度测试支付”。订单会幂等履约，并触发 Relay 模型额度同步。

体验包采用显式领取：把一个已启用、隐藏、售价为 0 且包含有效额度的套餐 slug 写入 `CATS_COMMERCIAL_TRIAL_PLAN_SLUG`。正式售卖套餐不能被误配成体验包；同一账号终身只能领取一次体验套餐。

## 支付宝需要准备

开通支付宝“电脑网站支付”后，需要准备：

- 支付宝开放平台应用 AppID：`CATS_ALIPAY_APP_ID`
- 收款支付宝用户 ID（seller_id/PID）：`CATS_ALIPAY_SELLER_ID`
- 应用 RSA2 私钥 `app_private_key.pem`
- 支付宝 RSA2 公钥 `alipay_public_key.pem`
- 公网 HTTPS 回调地址：`https://app.catsco.cc/api/payments/alipay/notify`
- 用户支付完成后的 HTTPS 返回地址：`https://app.catsco.cc/`

当前版本采用普通公钥模式，先不接公钥证书模式。应用公钥需要上传到支付宝开放平台，服务端保存的是与之对应的应用私钥和支付宝提供的验签公钥。应用私钥只用于请求签名，支付宝公钥只用于响应及回调验签。

生产机文件布局：

```text
${PROD_STACK_ROOT}/secrets/alipay/
  app_private_key.pem
  alipay_public_key.pem
```

建议目录权限为 `700`，私钥权限为 `600`。该目录随 secrets 根目录只读挂载到 `/run/catsco-secrets/alipay`。

生产环境变量：

```dotenv
CATS_ALIPAY_ENABLED=1
CATS_ALIPAY_SALES_ENABLED=0
CATS_ALIPAY_PRODUCTION=1
CATS_ALIPAY_APP_ID=
CATS_ALIPAY_SELLER_ID=
CATS_ALIPAY_NOTIFY_URL=https://app.catsco.cc/api/payments/alipay/notify
CATS_ALIPAY_RETURN_URL=https://app.catsco.cc/
```

沙箱环境设置 `CATS_ALIPAY_PRODUCTION=0`，并使用沙箱 AppID、私钥、公钥和买家账号。不要把正式与沙箱材料混用。

不要把真实值、应用私钥、支付宝公钥包或支付回调样本提交到 GitHub。仓库只保留变量名和示例路径。

## 订单和履约边界

- 创建订单使用 `(uid, client_request_id)` 幂等；同一用户、套餐和渠道复用未过期订单时，新请求 ID 也会持久映射到原订单，响应丢失后重试不会产生第二张可支付订单。
- 浏览器在超时、502/503/504 或响应丢失后会复用同一个 `client_request_id`；重新打开页面时会恢复最近的待支付订单。
- 下单使用 `alipay.trade.page.pay` 和产品码 `FAST_INSTANT_TRADE_PAY`，前端只跳转支付宝官方收银台，不自行伪造支付页面。
- 支付事件使用 `(channel, event_id)` 幂等，重复通知不会重复发套餐。
- 履约事务同时写入订单、支付事件、权益、额度 grant 和 ledger。
- 支付宝回调使用 RSA2 验签，并校验 AppID、seller_id、通知类型、交易状态、订单号和金额。
- 回调请求正文限制为 64 KiB，只保存 SHA-256 摘要，不保存原始表单。
- 只有 `TRADE_SUCCESS` 或 `TRADE_FINISHED` 可以履约。
- 成功处理后只返回纯文本 `success`；任何验签、订单或金额错误均返回 `failure`，让支付宝继续重试。
- 待支付页面轮询本地订单时会按 10 秒节流调用 `alipay.trade.query`；即使回调延迟或最终丢失，已支付订单仍能进入同一套幂等履约事务。
- 前端支付轮询使用单飞请求、20 秒超时和卸载取消；关闭弹窗不会留下后台轮询。过期订单在关闭后 7 天内仍可通过主动查单恢复已支付交易。
- 真实支付渠道只会对已启用 Relay enforce、已配置 Relay key 且套餐模型可映射到 Relay provider budget 的 UID 开放。下单前会预检，额度写入后会回读核验，避免 Relay 管理接口静默忽略更新。
- Relay enforce 控制新用户是否具备购买资格，不是已售权益的撤销开关。订单已履约后，即使 UID 后续移出 enforce，系统仍会在套餐有效期内同步额度并在到期后写入阻断值；退款或停权必须显式撤销权益，不能只改 allowlist。
- 权威回读依赖 Relay Admin 的 `GET /internal/users/{uid}/key/limits`。该接口直接读取 Bifrost key 的持久化 provider config，不合并默认展示配置；必须先部署对应 Relay 版本，再开启本 PR 的真实支付销售开关。
- 金额始终以整数“分”存储，调用支付宝时转换为两位小数的人民币“元”，不使用浮点数。
- 自动同步只处理 `commercial_managed_relay_budgets` 中由 CatsCompany 接管的模型额度，不改管理员手工维护的其他模型预算。
- 套餐到期后不能把 Relay 预算写成 `0`，因为 `0` 表示移除限制；系统会写入 `0.000001 CNY` 的阻断额度并保留接管记录，防止额度过期后模型意外变成无限制。
- Relay 同步失败不会回滚已经确认的支付；后台 worker 会定时重试。支付和账本始终是事实来源。

## 套餐、邀请码与人工赠送

- 邀请码只是套餐的发放方式，不创建独立的“邀请码套餐”。创建邀请码时必须绑定一个现有套餐；兑换后，用户看到的是该套餐名称，并标注来源为“邀请码兑换”。
- 同一个邀请码对同一个 UID 只能兑换一次。不同邀请码仍会形成各自可审计的套餐权益和额度记录；灰度期不要给同一用户重复发放等价邀请码，除非确实希望额度叠加。
- 人工调整分为两类：历史 Relay 管理后台里的直接额度继续保留；商业化套餐启用后，套餐所接管的模型应改在 CatsCompany 账号后台使用“赠送额度”。
- 赠送额度只能投向用户当前有效套餐包含的模型，且必须有到期时间；默认跟随包含该模型的套餐最晚到期日，显式设置也不能超过该日期。
- 赠送额度写入 `commercial_quota_grants` 和账本，并参与后续 Relay 同步。直接修改已经被商业化接管的 Relay 模型额度，可能在下一次套餐同步时被账本结果覆盖。
- 历史无期限 `manual` grant 不自动迁移或删除，避免影响已发放用户；新增管理操作统一写为有期限的 `bonus` grant。

## 上线顺序

1. 先部署带权威预算回读接口的 Relay Admin，再部署 CatsCompany；保持所有新增销售开关关闭。
2. 在账号后台把旧五档设为 `hidden`，再用预设建立 Personal / Pro 两档价格与模型额度。
3. 只给内部 UID 开测试支付与 Relay enforce，完成创建、支付、到账、重复通知和到期清退测试。
4. 准备支付宝沙箱应用和 secret 文件，开启沙箱支付，但套餐继续保持 `test`。
5. 完成沙箱收银台支付、回调重放、主动查单和金额篡改测试。
6. 更换正式应用材料，设置 `CATS_ALIPAY_PRODUCTION=1`，完成一笔真实小额支付。
7. 完成人工退款和额度回收演练后，再把套餐改为 `public` 并开启公共商业化入口。

### 云托管创建权益与内部补发

`CATSCO_WORKER_CREATE_QUOTA` 仅用于灰度/运维账号的静态开关；正式公共环境应留空。付费套餐履约时，服务会在 `cloud_worker_credits` 中为用户发放一次性创建权益，云托管创建成功后消费该权益。静态配额与套餐权益会相加，因此不能给已购买套餐的账号同时配置静态配额。

商业邀请码也支持按码配置 `cloud_worker_credits`（默认为 `0`，可配置为 `1` 或更多）。兑换邀请码时，云员工创建权益与套餐 entitlement 在同一事务中发放，且使用相同的过期时间；同一码重复兑换不会重复发放。套餐升级时，旧套餐下尚未使用的邀请码权益会一并撤销，已进入创建流程的 reserved 权益保留。

内部确需额外分配创建次数时，应使用本地管理员接口 `POST /local/account-admin/commercial/cloud-worker-credits`（或同等受保护的商业运营服务接口），提交 `uid`、`count`、幂等 `source_ref` 和可选 `expires_at`；不要直接修改生产环境静态配额。省略 `expires_at` 表示永久内部权益：创建成功后只消费 credit，不登记自动到期清理；有有效期的补发权益才会登记生命周期并按到期/宽限期清理。

商业运营后台的“云员工总览”通过 `GET /local/commercial-ops/api/cloud-workers` 读取全平台登记、生命周期、创建权益和天翼云状态。该路径只在 relay-admin 的本机路径白名单中转发；CatsCompany 端还要求本机/私网来源及 `commercial.ops.read`（或更高）service scope，不提供公网用户 JWT/API 路由。

退款自动化、发票和对账单下载不在本阶段范围内；订单状态已预留 `refunding/refunded`。灰度期退款必须按订单号在账号后台核对支付宝交易号，在支付宝商家后台人工退款，再由管理员回收对应权益和 Relay 额度并留存操作记录。完成这套演练前不得开启公共销售。
