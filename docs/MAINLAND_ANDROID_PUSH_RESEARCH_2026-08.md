# 中国大陆 Android 的 Web Push 与原生推送决策

更新日期：2026-08-05。本文只使用规范、浏览器源码和厂商/服务商的一手文档；它是架构判断，不替代真机验收。

## 结论

Cloudflare Relay 应保留，但它**不是**中国大陆 Android Web Push 的完整解法。它只把 CatsCo 服务器到既有 Web Push 服务 endpoint 的出站请求移到 Cloudflare；它不能让一个没有可用 Push API / Google Play services 的浏览器获得订阅，也不能把已经由浏览器创建的 FCM subscription 改投华为、小米、OPPO 或 vivo 的厂商通道。

如果产品目标是“主流中国 Android 在后台也可靠收到消息”，应把 PWA Web Push 降为可用时的补充通道，并做一个原生 Android 客户端（可复用现有 React/Vite UI 做 Capacitor 壳），以**原生 SDK + 设备 token**接入厂商推送。不要只包一层 TWA/WebView 后仍依赖 Web Push——那没有改变推送传输层。

这不意味着当前测试里的每一台国内 Android 都一定“不支持”。更严谨的判断是：观测到 iOS 与多种国内 Android 都失败后，“仅服务器到 Google 的出口故障”已不足以解释所有样本；最可能是服务端出口、浏览器能力/Google Play services、安装与权限、以及设备侧网络多因素叠加。应按 subscription endpoint、订阅结果和投递 HTTP 状态分别验证。

## 为什么 Relay 只能修一段链路

Web Push 的 `PushSubscription.endpoint` 是浏览器与它所选的 push service 建立的投递上下文；规范要求该 endpoint 是 push service 暴露给应用服务器的绝对 URL。应用服务器只能把加密消息投到这个 endpoint。VAPID 的 `aud` 也必须是该 push resource URL 的 origin，因而不能把一个已签给 FCM/Apple endpoint 的请求直接改成某个厂商服务。Relay 正确的职责是透明地代发到**原 endpoint**，不是替换 provider。  
来源：[W3C Push API §3.4](https://www.w3.org/TR/push-api/#push-subscription)、[RFC 8292 §2.1（VAPID audience）](https://www.rfc-editor.org/rfc/rfc8292.html#section-2.1)。

因此当前 Relay 的价值仍然明确：若客户端已成功生成 `fcm.googleapis.com` 或 `*.push.apple.com` subscription，而大陆 CatsCo 服务器无法连接该 endpoint，它可以修复“服务器 → push service”这一跳。它不能修复“手机/浏览器 → 自己的 push service”这一跳。

## Android：不能把 Chromium 浏览器能力当作所有国产机的系统能力

当前上游 Chromium 的 Web Push 实现会由 `PushMessagingServiceImpl` 使用 `GCMDriver`；Android 的 `GCMDriverAndroid` 明确采用 Android GCM APIs。其 Java 实现把 `com.google.android.gms` 作为 Google Play services 包名，在注册前检查该包是否已安装；缺失时返回 `Google Play Services missing`。这说明保留这条上游实现的 Android Chromium/Chrome，其 Web Push 注册路径依赖 Google Play services。Firebase 的官方 Android 文档也把 FCM 客户端支持条件写为 Android 6.0+ 且已安装 Google Play Store app 的设备（或带 Google APIs 的模拟器）。  
来源：[Chromium `PushMessagingServiceImpl`](https://chromium.googlesource.com/chromium/src/+/main/chrome/browser/push_messaging/push_messaging_service_impl.cc)、[Chromium `GCMDriverAndroid`](https://chromium.googlesource.com/chromium/src/+/main/components/gcm_driver/gcm_driver_android.h)、[Chromium `GoogleCloudMessagingV2`](https://chromium.googlesource.com/chromium/src/+/main/components/gcm_driver/android/java/src/org/chromium/components/gcm_driver/GoogleCloudMessagingV2.java)、[Firebase: Receive messages in an Android app](https://firebase.google.com/docs/cloud-messaging/android/client)。

边界也要说清：这不是“所有 Android 浏览器必然使用 FCM”的官方保证。OEM 默认浏览器和 Chromium 派生浏览器可改造、禁用或根本不实现这一能力；所以 `PushManager in window` 也不能当作“后台一定能到”的承诺。对 CatsCo 而言，只有一次真实 `subscribe()` 成功、订阅上传成功、再加一次 provider 成功投递，才能把某个“设备 + 浏览器 + 系统版本”列为支持组合。

## iOS 是另一条链路

iOS/iPadOS 16.4 起，Web Push 面向的是“已添加到主屏幕的 Web App”。WebKit 说明该实现与系统深度集成；其 Web Push 守护进程会把网页订阅转为 Apple Push Notification service（APNs）订阅。因此 iOS 收不到不能归因于 FCM/Google；应单独检查主屏幕安装、用户手势授权、`web.push.apple.com` subscription 是否已上传，以及 Apple endpoint 的投递结果。  
来源：[WebKit: Web Push on iOS and iPadOS](https://webkit.org/blog/13966/webkit-features-in-safari-16-4/)、[WebKit: Meet Web Push（APNs 实现说明）](https://webkit.org/blog/12945/meet-web-push/)。

## 厂商通道意味着什么

华为、小米、OPPO、vivo 的“厂商推送”不是把现有浏览器 `PushSubscription` 交给服务端即可使用的替代 endpoint。它们的开发者资料面向 Android 应用的 SDK、包名/签名、App 配置、回调和设备 token；即使选择聚合服务，也依旧是“Android 客户端集成 + 多厂商客户端集成”，不是服务端给 PWA 加一个 URL。

| 选择 | 能解决什么 | 代价 / 不解决什么 |
| --- | --- | --- |
| 各厂商原生 SDK（Huawei/Xiaomi/OPPO/vivo） | 让已安装的原生 App 使用相应系统厂商通道 | 多套账号、包名/签名、证书/密钥、类别/配额与回执处理；无法让任意浏览器 PWA 直接获得厂商推送 |
| 推送聚合服务 | 一个 Android SDK/服务端 API 协调多厂商通道，降低多套接入的工程量 | 仍需原生 App 与设备 token；新增供应商、数据处理与商业依赖，厂商资质/配置通常仍要完成 |
| 继续只做 PWA + Relay | 可覆盖真正生成标准 Web Push subscription 的 Chrome/桌面/iOS 主屏幕 Web App | 无法承诺无 GMS 或不支持 Push API 的国内 Android 浏览器后台到达 |

厂商一手入口（实施前需以当前控制台文档和商务条款复核）：[Huawei Push Kit Android client guide](https://developer.huawei.com/consumer/en/doc/hmscore-guides/android-client-dev-0000001050042041)、[小米开发者文档中心](https://dev.mi.com/console/doc/detail?pId=1244)、[OPPO 开放平台文档中心](https://open.oppomobile.com/new/developmentDoc/info?id=11287)、[vivo 开放平台文档中心](https://dev.vivo.com.cn/documentCenter/doc/392)。

聚合并非“第三方消息平台”或 Feishu/微信通知功能的回归；它是 Android 系统级投递 transport。其一手文档仍明确把 Android 和“多厂商”放在**客户端集成**中，例如：[个推文档：Android 客户端集成与多厂商客户端集成](https://docs.getui.com/getui/)、[JPush Android SDK 集成指南](https://docs.jiguang.cn/jpush/client/Android/android_guide/)。（是否选用聚合服务需先过隐私、成本和供应商风险评审。）

## 建议的 CatsCo 架构

```text
消息事件
  └─ 通知策略（按“接收设备”判断是否在当前会话；不要因为另一台设备在线而全局抑制）
      ├─ 应用内：WebSocket / 页面内提醒（前台）
      ├─ Web Push：现有 PushSubscription → Cloudflare Relay → 原 browser endpoint
      └─ Native Push：原生设备 token → 厂商直连或聚合服务 → 已安装 Android App
```

1. 保持 Web Push 的 endpoint、VAPID、Relay 作为一个独立 `web_push` transport；它仍服务于桌面浏览器、可用 GMS 的 Chrome 和 iOS 主屏幕 Web App。
2. 新增 `native_push` transport，而不是把厂商 token 塞进现有 Web Push subscription 表（两种凭据和失效语义不同）。这需要原生 App 决策后再做最小的新表/API，不应为了当前 Relay PR 预先扩大数据库迁移范围。
3. 首版 Android 可用 Capacitor 封装现有 React/Vite 前端，但推送必须通过原生插件/SDK 注册 token，并由 App 登录态把 token 绑定 CatsCo 用户和设备。仅 WebView/TWA 不足以解决本问题。
4. 若优先级是尽快覆盖国内机型，先评估一个聚合服务的 Android SDK；若优先级是最小化外部依赖，则直接接华为/小米/OPPO/vivo，接受更高维护成本。两条路都需要发布真实 Android App。

## 最小下一步（先验证，再扩面）

1. 部署 Relay 后保留无敏感信息的日志：endpoint host、`subscribe()` 结果、服务端投递 HTTP 状态、浏览器/OS、PWA 是否安装；绝不记录完整 subscription URL 或 token。
2. 用真实设备做矩阵：至少一台有 GMS 的 Chrome Android、一台无 GMS 的 Chrome/Chromium、华为/小米/OPPO/vivo 默认浏览器各一台，以及 iOS 主屏幕 Web App。每台分开记录“无 API / 拒绝权限 / 订阅失败 / 已注册但 provider 失败 / provider 2xx 未显示”。
3. 先把 Web Push 的实际可达覆盖率量出来，再决定原生优先的厂商顺序。不要根据品牌名或 `Notification.permission === 'granted'` 推断可达。
4. 一旦确认需要中国 Android 后台可靠性，单独立项做 Android native push spike（一个账号、一个真机、一个消息端到端），验证 token 注册、后台到达、点击深链、卸载/换机失效和通知权限关闭后再扩到全部厂商。

