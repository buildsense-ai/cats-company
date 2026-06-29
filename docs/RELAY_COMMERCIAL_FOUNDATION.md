# Relay 商业化基础设施

本文说明 CatsCompany 内置 relay 商业化测试版的链路、灰度方式和当前边界。文档不包含服务器 IP、真实密钥或后台密码，可随仓库保存。

## 目标

- 先保留现有 relay 默认额度、Bifrost virtual key 和模型调用链路。
- 新增一层可灰度的商业化账本，用来测试套餐、邀请码和人工发放。
- 用户在 CatsCo 中转站页面能看到套餐额度，并用邀请码兑换。
- 管理员在本地账号后台能创建套餐、创建邀请码、手动给用户发额度、查询用户额度汇总。

## 链路

1. 管理员在账号后台创建套餐。
2. 管理员创建邀请码，绑定到某个套餐。
3. 用户在 CatsCompany 的 CatsCo 中转站弹窗输入邀请码。
4. 后端校验邀请码、计划状态、过期时间、兑换次数和用户是否已兑换过。
5. 后端写入 entitlement、quota grant 和 quota ledger。
6. 用户页面重新读取 commercial summary，按模型展示额度。

这条链路目前只负责商业化额度展示和账本记录，不直接替换 Bifrost 的 virtual key，也不直接改变当前 relay 调用扣费逻辑。

## 灰度开关

公开用户接口默认关闭：

```bash
CATS_RELAY_COMMERCIAL_ENABLED=1
```

开启后，认证用户可以访问：

- `GET /api/relay/commercial`
- `POST /api/relay/invite/redeem`

关闭时，用户页面只看到“套餐额度功能尚未启用；当前 relay 默认额度和重置周期继续保留”的安全空态。

本地账号后台接口不受该灰度开关影响，但仍要求只能通过本地/SSH 隧道访问。

## 数据表

- `commercial_plans`
  套餐定义，包含总额度、模型额度 JSON、有效天数和上下架状态。

- `commercial_invite_codes`
  邀请码定义，绑定套餐，记录最大兑换次数、已兑换次数、过期时间和状态。

- `commercial_entitlements`
  用户获得某个套餐的权益记录。邀请码来源会通过唯一索引限制同一用户同一码只能兑换一次。

- `commercial_quota_grants`
  额度发放记录。邀请码兑换和后台人工发放都会落到这里。

- `commercial_quota_ledger`
  审计账本，记录额度变动来源，后续接真实消耗、退款、补偿时继续扩展。

## 管理后台

账号后台新增“中转商业化测试”区域：

- 创建/更新套餐。
- 创建/更新邀请码。
- 按 UID、模型、金额手动发放额度。
- 查询某个 UID 的商业化额度汇总。

建议早期运营流程：

1. 先创建学校/老师试用套餐。
2. 给每个学校一批邀请码，控制 `max_redemptions`。
3. 对重点用户用后台手动发放额外额度。
4. 每天抽查用户 summary 和 relay 实际用量是否一致。

## 当前未接入的部分

- 尚未把 commercial quota 作为 relay 强制限额来源。
- 尚未把 Bifrost 实际调用成本写入 `commercial_quota_ledger` 的消耗项。
- 尚未做支付、订单、发票、退款。
- 尚未做组织/学校维度预算池。

这些应作为后续迭代，避免一次性替换现有稳定 relay 链路。

## 验证清单

代码合并或部署前建议跑：

```bash
go test ./...
npm run build --prefix webapp
git diff --check
```

浏览器验证：

1. 使用测试用户登录 CatsCompany。
2. 打开头像菜单里的“CatsCo 中转站”。
3. 确认套餐额度区域出现安全空态或灰度数据。
4. 输入邀请码并兑换。
5. 确认额度按模型更新，重复兑换同一个码会被拒绝。

上线验证：

1. 先不开 `CATS_RELAY_COMMERCIAL_ENABLED`，确认现有 relay 页面和调用不受影响。
2. 通过本地账号后台创建测试套餐和邀请码。
3. 开启灰度环境变量，只给内部用户兑换测试。
4. 对比用户页面额度、后台 summary、数据库账本。
