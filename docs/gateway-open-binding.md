# Gateway 标注：绑定原会话的打开实例

本页是 UI/API/host 绑定说明。当前用户交互已改为[选区旁直接评论](gateway-inline-annotation.md)：刷新旁单按钮，评论直接提交；以下 composer draft 内容仅说明旧 saved draft 的兼容与隔离行为。

只覆盖 gateway-only 应用；旧平台 versioned artifact、task/runtime/context-ref 路径保持兼容，不重建 Agent session，不传 XiaoBa sessionKey，不直接调用 provider。

## 冻结接口

### 打开

`POST /api/artifacts/launch`：`{app, topic_id?}`。服务端验证 actor、原 topic 权限、唯一可用 agent、canonical app registry owner/URL 后签发 launch code，并对 conversation open 返回：

```json
{
  "open_binding": {
    "contract_version": "catsco.artifact-open-binding.v1",
    "open_ref": "aob_<random-base64url>",
    "topic_id": "<canonical-original-topic>",
    "agent_uid": 201,
    "app_id": "saturday-demo",
    "app_origin": "https://artifact.catsco.cc",
    "expires_at": "<RFC3339>"
  }
}
```

TTL 为 30 分钟或 JWT 到期时间（二者取更早）。绑定使用已验签 JWT 的 fingerprint；token refresh 后即使 UID 相同也属于新 auth session，必须重新打开，不能迁移原 open。identity-only/standalone launch 可无 binding；访客或绑定失败仍能浏览，但标注捕获/发送禁用。多 bot group 无 selected-agent 字段时严格拒绝歧义，不猜目标。

### 提交

```http
POST /api/artifacts/annotations
Authorization: Bearer <current-platform-auth>
Content-Type: application/json
```

```json
{
  "open_ref": "aob_<original-open>",
  "client_msg_id": "ga_<uuid>",
  "content": "请按标注调整",
  "reply_to": 123,
  "gateway_annotations": {
    "contract_version": "catsco.gateway-annotations.v1",
    "agent_uid": 201,
    "app_id": "saturday-demo",
    "page": {"path": "/board"},
    "annotations": [{"id":"a1","kind":"element","label":"发布按钮","body":"改成蓝色","target":{"element_id":"submit-btn"}}]
  }
}
```

`content` 是普通 string；`client_msg_id` 必填、最长 128；`reply_to`、`content_blocks` 可选。subject/recipient 由服务端解析 open，不接受当前 UI topic 作为路由 authority。走已有正常消息持久化、idempotency、fanout 和 recipient-only readable text injection。成功响应沿用正常 send 的 `seq_id/id/metadata`，UI 在发送成功后关闭评论框并刷新消息数据；新批注仅在原 topic 出现。失败绝不 fallback `/api/messages/send` 或 WS。绑定 invalid/mismatch 明确提示原会话重新打开并重新标注。

### 撤销

`DELETE /api/artifacts/open-bindings/{open_ref}` 使用原 auth session，204 幂等；wrong actor/session 无权撤销。关闭 iframe/面板、切 app/tab/topic、logout 会先本地 abort/invalidate，再最佳努力 HTTP revoke。API 层暂存原 auth token 仅供 logout revoke；不传给 app。`pagehide` 使用 keepalive DELETE。网络中断时本地仍 failclosed，服务端 TTL 最终回收。关闭期间的 pending 提交不保证成功：A 的 persistenceGate 将 revoke/save 线性化；revoke 完成早于保存则拒绝，已完成保存则正常成功，两者都只能作用于 original record.TopicID。UI 对失败恢复原草稿/正文，拒绝旧 revoked capability 的重试，提示重新打开。

## 父页面生命周期与隔离

- 每次实际打开创建独立 open。父页面持有 `open_binding`，不放 URL/query/cookie/HTML，不发给 iframe。gateway cookie 只表示身份，不决定 conversation。
- 同 app 两个 tab/topic 各自保存自己的 open；host binding key 包含 open_ref + origin，不能把旧 document session 重用于另一次 open。
- iframe 刷新仅更换 bridge `session_id/request_id` 并重新 connect-ready；panel 原 open_binding 不变。首次 connect 与自动 runtime 使用原冻结 bridge 协议，D/C 不需要读取 open_ref。
- topic 切换采用最小可行的禁 capture/关闭旧 viewer；已进入 HTTP 的 A 请求携带冻结 A open_ref，晚到响应不 append B。尚在异步 preparation、旧 viewer 已关闭时取消发送，不降级普通消息。
- user-scoped sessionStorage 的 draft 只保留评论、捕获 page/revision 和 mutation revision，绝不写 open_ref/open_binding。能力关联由每个 MessagesView mount 自己的内存 Map 持有，按 user/bucket/row ID/内容与版本关联原 binding；没有模块级共享能力 Map。新 open/mount/login 不借旧 ref，旧 storage 中即使带 binding 字段也不接受它为 authority。
- 关闭 open 的 abort 按 open_ref detach 对应内存关联，不删评论；失败恢复不会重新挂回已关闭 capability。unmount/auth change 清空该 mount Map。整页 refresh/remount 后评论可恢复，但缺内存能力必须明确提示重新打开并重新捕获，发送 failclosed。同 document iframe reload 不清 mount Map，仍关联原 open，只更新 document handshake。
- snapshot 在首个 await 前深复制 rows/targets/binding；每个异步边界与提交前保留 auth revision/token、mount、abort、TTL guards。失败恢复只合并原 bucket 版本，不覆盖新编辑/ABA/recovery 行，不因 late logout failure resurrect 草稿。
- 相同 auth/open/envelope/row revisions 的重试复用 client_msg_id，避免持久化成功但响应丢失产生重复消息；修改内容/版本则生成新 id。重试缓存在 mount 内有界（100）。
- page drift、缺 capture page、混合 revision、16KiB/20 行 envelope、恢复容量及 capture confirm 的 document/session guards 保留。父页面内存能力证书不进入 storage 或发送的 gateway_annotations metadata；open_ref 仅作为绑定 endpoint 顶层字段和 revoke path 使用，不进 app URL、HTML、postMessage 或日志。
- runtime 不响应/被 CSP 阻止时显示不可用，不绕过 CSP、不跨域读取 DOM、不猜选择。

## Runtime 对齐

D 的唯一源为 `webapp/public/catsco-annotations.js`，C 用导出脚本 vendoring：

```sh
node scripts/export-gateway-annotation-runtime.mjs --out-dir <gateway-runtime-dir>
node scripts/export-gateway-annotation-runtime.mjs --out-dir <gateway-runtime-dir> --check
```

Gateway 注入外部 `/_catsco/runtime/annotations-v1.js`，配置 `data-catsco-parent-origins` 为明确 allowlist 的 JSON array，HTML 属性转义。无 inline secret/config/open_ref；SDK 自动 bootstrap 接受首次 host connect；app 无需手工 script/create。高级 explicit SDK 的优先级、singleton/dispose 见 `gateway-annotation-runtime.md`；实际 Nginx 注入和非 HTML/gzip/CSP 限制见 gateway repo 文档。上游 CSP 禁止 self script 时自然 blocked，UI 显示 unavailable。

## 本地 UI demo（不是实际 backend 验收证据）

```sh
MOCK_CATS_SCENARIO=showcase MOCK_CATS_PORT=6062 node scripts/local-onboarding-mock-server.mjs
GATEWAY_ANNOTATIONS_LOG_PATH=/tmp/catsco-ui-demo.jsonl node scripts/local-gateway-annotations-demo.mjs
cd webapp
VITE_BACKEND_TARGET=http://127.0.0.1:6063 VITE_ARTIFACT_GATEWAY_BASE=http://127.0.0.1:6066 pnpm start --host localhost --port 5173 --strictPort
```

fixture 不手工 SDK create；demo gateway 注入外部 runtime（无 revision 时不杜撰）。mock-mode binding store 仅供 UI 演示，记录 canonical topic 并转发 onboarding mock，**不代表生产权限校验、真实 gateway Nginx、Go 正常 ingestion/persistence 已完成 E2E**。

真服务联调可用 `GATEWAY_ANNOTATIONS_REAL_BACKEND=http://127.0.0.1:<Go-port>`，此时所有 platform artifact endpoints 原样转发 A 的真实 server，不由 demo mint/resolve/revoke。A 已提供 test-only actual Go httptest fixture（可选 loopback `127.0.0.1:22446`），详细启动环境见 [server 联调说明](gateway-open-binding-server.md)。A 的真实 gateway control-plane apps/codes + Go durable test store、idempotency、recipient route 联调已通过；Nginx HTML/runtime/bootstrap/CSP/浏览器跨域捕获全链路仍由主会话独立验收，不能用 UI mock demo 代替。

## 验证与部署边界

UI targeted、完整 Vitest/build 和 demo node 测试由 B 执行；server tests/vet、真实 gateway/renderconfig 与联合 HTTP fixture 由 A/C/主会话提供。构建通过不等于真实 E2E 或上线。

当前后端 open store process-local：重启、其他 replica 无法 resolve 时 failclosed，必须重新打开；多 replica 部署需要 sticky routing，或实现共享的可撤销 TTL store。图片与 owned receipt 目录也须成套共享。
