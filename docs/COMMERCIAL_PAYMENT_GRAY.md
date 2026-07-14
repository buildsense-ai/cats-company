# 商业化支付灰度

本阶段在原有套餐、邀请码、人工调额和 Relay 对账基础上，增加可售套餐、体验包、订单、灰度测试支付、微信 Native 支付以及自动额度同步。

## 默认安全状态

- `CATS_RELAY_COMMERCIAL_ENABLED=0`：不向普通用户开放商业化入口。
- `CATS_COMMERCIAL_TEST_PAYMENT_ENABLED=0`：不开放测试支付。
- `CATS_WECHAT_PAY_ENABLED=0`：不初始化微信支付。
- 新增套餐的 `sale_state` 默认是 `hidden`，旧套餐升级后也不会自动出现在购买列表。
- 商户私钥和 APIv3 Key 只从容器内的 secret 文件读取，不支持写进前端或仓库配置。
- 支付回调只保存订单字段和 SHA-256 摘要，不保存原始解密报文。

## 灰度流程

1. 在账号后台创建或更新套餐，设置价格，并将 `sale_state` 设为 `test`。
2. 配置 `CATS_RELAY_COMMERCIAL_TEST_UIDS=<uid>`，只让灰度账号看到套餐。
3. 配置 `CATS_COMMERCIAL_TEST_PAYMENT_ENABLED=1` 和 `CATS_COMMERCIAL_TEST_PAYMENT_UIDS=<uid>`。
4. 如需支付后立即写入 Relay，再配置 `CATS_RELAY_COMMERCIAL_ENFORCE_UIDS=<uid>`。
5. 用户在“模型服务”中创建订单并点击“完成灰度测试支付”。订单会幂等履约，并触发 Relay 模型额度同步。

体验包采用显式领取：把一个已启用套餐的 slug 写入 `CATS_COMMERCIAL_TRIAL_PLAN_SLUG`。同一账号终身只能领取一次体验套餐。

## 微信支付需要准备

开通微信支付 Native 支付后，需要准备：

- 关联商户号的 AppID：`CATS_WECHAT_PAY_APP_ID`
- 微信支付商户号：`CATS_WECHAT_PAY_MCH_ID`
- 商户 API 证书序列号：`CATS_WECHAT_PAY_MCH_CERT_SERIAL`
- 商户 API 私钥 `apiclient_key.pem`
- 32 字节 APIv3 Key
- 若新商户采用微信支付公钥模式：微信支付公钥 ID 和 `wechatpay_pub.pem`
- 公网 HTTPS 回调地址：`https://app.catsco.cc/api/payments/wechat/notify`

运行时不需要把 `apiclient_cert.pem` 放进应用容器；证书用于确认序列号，SDK 签名使用 `apiclient_key.pem`。默认由官方 SDK 自动下载微信支付平台证书并用于回调验签。若商户采用微信支付公钥模式，则同时配置 `CATS_WECHAT_PAY_PUBLIC_KEY_ID` 和公钥文件，应用会改用公钥验签。

生产机文件布局：

```text
${PROD_STACK_ROOT}/secrets/wechat-pay/
  apiclient_key.pem
  api_v3_key
  wechatpay_pub.pem  # 仅公钥模式需要
```

建议目录权限为 `700`，其中的 secret 文件权限为 `600`。`api_v3_key` 文件只放 APIv3 Key 本身。该目录随 secrets 根目录通过 compose 只读挂载到 `/run/catsco-secrets/wechat-pay`。

生产环境变量：

```dotenv
CATS_WECHAT_PAY_ENABLED=1
CATS_WECHAT_PAY_APP_ID=
CATS_WECHAT_PAY_MCH_ID=
CATS_WECHAT_PAY_MCH_CERT_SERIAL=
CATS_WECHAT_PAY_PUBLIC_KEY_ID=  # 平台证书模式留空
CATS_WECHAT_PAY_NOTIFY_URL=https://app.catsco.cc/api/payments/wechat/notify
```

不要把真实值、私钥、APIv3 Key、商户证书压缩包或支付回调样本提交到 GitHub。仓库只保留变量名和示例路径。

## 订单和履约边界

- 创建订单使用 `(uid, client_request_id)` 幂等，重复点击不会产生重复订单。
- 支付事件使用 `(channel, event_id)` 幂等，重复通知不会重复发套餐。
- 履约事务同时写入订单、支付事件、权益、额度 grant 和 ledger。
- 回调必须通过官方 SDK 验签和解密，并校验 AppID、商户号、订单号、金额和币种。
- 自动同步只处理 `commercial_managed_relay_budgets` 中由 CatsCompany 接管的模型额度，不改管理员手工维护的其他模型预算。
- 套餐到期后不能把 Relay 预算写成 `0`，因为 `0` 表示移除限制；系统会写入 `0.000001 CNY` 的阻断额度并保留接管记录，防止额度过期后模型意外变成无限制。
- Relay 同步失败不会回滚已经确认的支付；后台 worker 会定时重试。支付和账本始终是事实来源。

## 上线顺序

1. 先部署代码，保持所有新增开关关闭。
2. 建立测试套餐和体验套餐。
3. 只给内部 UID 开测试支付与 Relay enforce，完成创建、支付、到账、重复通知和到期清退测试。
4. 准备微信商户材料和 secret 文件，开启微信支付，但套餐继续保持 `test`。
5. 完成真实小额支付与退款人工流程演练后，再把套餐改为 `public` 并开启公共商业化入口。

退款自动化、发票和对账单下载不在本阶段范围内；订单状态已预留 `refunding/refunded`，上线真实支付前需明确人工退款 SOP。
