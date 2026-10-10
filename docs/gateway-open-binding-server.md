# Gateway app-open binding：服务端契约与边界

仅用于 gateway registry 应用，不涉及平台旧版 versioned Artifact。应用从 topic A 打开后，发送永远由该次 open 的服务端记录解析回 A；浏览器切到 B、iframe 刷新、多 tab、gateway viewer cookie 覆盖均不能重新绑定。

## API

所有路由要求现有有效、active 的用户 JWT。topic launch / annotations / revoke 进一步要求 **human account**，并 fingerprint 实际已验签 token（SHA-256），不只绑定 UID。新签发 JWT 含随机 JTI，避免同一秒两次登录产生相同 token。旧 JWT 仍兼容；legacy token 若本身字节相同，服务端无法辨别两个副本。

### `POST /api/artifacts/launch`

输入保持 `{ app, topic_id? }`。不提供 topic 的 standalone identity launch 兼容旧行为，不返回 open binding，没有 annotation 发送能力。

带 topic 时，在请求 gateway code **之前**验证：

- 精确 canonical `p2p_<小UID>_<大UID>` 或 `grp_<ID>`；p2p 包含 actor，不接受反序别名、前导零或任意 hint。
- 复用正常消息发布权限（群成员、mute、channel-managed group 隔离、p2p Agent owner/friend 权限）。
- human actor、active bot，topic 唯一 Agent。群还重新检查实际 Agent membership；stale `Group.AgentIDs` 不能隐藏其他 bot。
- gateway 全量 registry 的唯一 exact app ID、owner == Agent UID。
- registry 主 URL 与全部 alternate URLs 必须是配置允许 origin 上的精确 `/<app>/`；拒绝 userinfo、query、fragment、编码 path 或别的 app/origin。

冻结 API 未携带 `selected_agent`；因此多 bot 群 **fail closed**，不从 app owner 猜一个 Agent。

正常 launch response 原有字段加：

```json
{
  "open_binding": {
    "contract_version": "catsco.artifact-open-binding.v1",
    "open_ref": "aob_<32-byte-random-base64url>",
    "topic_id": "p2p_7_9",
    "agent_uid": 9,
    "app_id": "board",
    "app_origin": "https://artifact.catsco.cc",
    "expires_at": "2026-10-07T02:00:00Z"
  }
}
```

gateway 返回 app ID、launch path `/_launch/<code>`、唯一 `next=/<app>/` 与 origin 必须精确匹配 canonical registry。验证失败不 mint 引用；容量/随机数等失败则 fail closed，gateway 已签发但未交付的 code 自然过期。

`open_ref` 仅在 CatsCo 父页面 response 出现，**不发送 gateway、不进 app URL/HTML/cookie、不写消息 metadata、不派生 Agent sessionKey**。launch 与绑定 API responses 使用 `Cache-Control: no-store`。

### `POST /api/artifacts/annotations`

```json
{
  "open_ref": "aob_...",
  "client_msg_id": "stable-message-id",
  "content": "普通用户文字",
  "reply_to": 3,
  "content_blocks": [],
  "gateway_annotations": {
    "contract_version": "catsco.gateway-annotations.v1",
    "agent_uid": 9,
    "app_id": "board",
    "page": {"path": "/board/"},
    "annotations": []
  }
}
```

示例 annotations 要替换为现有 v1 有效非空列表。body 最大 64 KiB，client_msg_id 必填且 ≤128 bytes，无控制字符；content 必须非空 string；reply_to 非负。annotation 继续执行现有 v1 16 KiB/20 条/target 规范。body 多余 `topic_id`/UID/metadata 不是 authority，不进入普通消息 envelope。

每次重新验证：当前 JWT active human + exact fingerprint/actor + binding TTL/status + 原 topic 权限/唯一 active Agent + app owner/current URLs + annotation app/Agent claim 一致。旧 origin 不再被 registry 列出即失败。必须具备 metadata-aware message store，不能降级为只保存文字。

之后调用普通 `MessageHandler.sendMessage`：相同 normalize、rate limit、message publish access、annotation 校验、`SaveMessageWithMetadata`、idempotency、live fanout 与 HTTP/WS history replay。响应与普通消息相同，包含 canonical `topic_id`、`seq_id`、`client_msg_id`、`duplicate`、metadata。群发送沿用正常结构化 mention，且移除 client block mentions，精确目标唯一 bound Agent。

原始用户文字与结构化 annotation durable 保存；只有目标 Agent 的 delivery/history copy 附加服务端可读文本；人类 copy 与持久化原文字不被修改。重试遵循普通消息 `(topic, actor, client_msg_id)` 幂等语义，duplicate 不重复 live fanout；不要为不同内容复用 ID。Agent offline 时沿用普通持久化/history 重建，不要求在线、不开新 provider/session。

### `DELETE /api/artifacts/open-bindings/{open_ref}`

要求相同 actor + 实际 auth token。204；在 tombstone TTL 内同会话反复撤销返回 204。wrong actor、same UID different token、unknown、expired 都返回 403 `artifact_open_binding_invalid`，不能撤销别人的引用。已过期/重启丢失返回 403，宿主 close/logout 最佳努力可以忽略；不当成成功发送。

保存前（普通 message ingestion 的第二次 gateway lookup 之后）再次验证 auth/account/topic；等待 store 锁后再次以新时间检查 TTL/JWT expiry。DB save 开始后，不倒退撤销已开始的原子保存。

撤销与持久化 gate 以 mutex 线性化：已完成 revoke 不会允许仍等 gateway 校验的请求保存；已经进入保存 gate 的发送先完成保存，再完成 revoke。网络响应晚到不等于撤销可以倒退取消已经保存的用户消息。

## 生命周期与部署限制

- 每次 open 独立 CSPRNG 256-bit opaque 引用；只服务端存储 actor/topic/agent/app/origin/token fingerprint/expiry/revoked。
- TTL 固定 30 分钟，普通 JWT 更早到期时缩短；不滑动续期、不猜 topic、不默默降级普通发送。错误必须提示重新打开。
- 当前是 bounded process-local store（4096 records）。expired records 在 lookup/mint 懒清理；revoked tombstone 留到 TTL 保证同会话 revoke 幂等。满时拒绝，不驱逐其他活跃 binding。
- 项目 sharedRuntimeState 只有专属 routing/device 等接口，无通用 bounded capability storage；本轮没有扩 Redis/数据库 schema。**不同 replica 与 restart 无法 resolve 均 fail closed**。多 replica 需 sticky routing 才可使用，或后续显式实现共享可撤销 TTL store；不能当分布式可用能力已完成。
- 全局 store mutex 跨正常 DB `CreateTopic`/`SaveMessageWithMetadata` 持有，串行化所有 bound submit 的保存，也使慢 DB 暂时阻塞 binding lookup/mint/revoke。这是低频 MVP 的简单线性化取舍，不适合高吞吐。锁内 Store 实现/回调 **不得重入 issue/lookup/revoke/persistenceGate**，否则死锁；权限、auth DB、外部 registry 查询全部在拿锁之前，正常 fanout 在解锁之后。后续可实现 per-record serialization，而不是削弱 revoke/save 顺序。
- token refresh/替换产生不同 token 必须 reopen；服务端没有全局 logout/revoke JWT session registry，登出仍依赖宿主明确 revoke + 本地 token invalidation。persistent JWT 同样最多 30 分钟 open TTL。
- 权限与 gateway registry revalidation 是正常同步 lookup，不是跨 DB/gateway 事务。对同步检查之后发生的外部权限变更遵循既有普通消息检查边界；revoke 对本地 binding 保存有明确 gate。
- gateway code TTL 与 open TTL 不同：one-time code 用于 identity；app open 实例有效期由 open binding 决定。

## 验证

`server/artifact_open_binding_test.go` 启动 Go HTTP platform + gateway registry/code fixture，运行真实 auth/launch/bound message/history handlers，并用 metadata-aware durable test store验证实际保存与普通幂等路径。覆盖 A/B/多 open、错误 auth/claims/ACL/URLs、TTL/revoke/restart、群 mute/Agent 离群/歧义、正常 Agent 可读内容与人类隔离、HTTP history/WS replay、容量与 revoke-save gate。数据库适配器的已有 SQL-mock 幂等/metadata tests 也包含在全量 server tests。

另有 opt-in `TestArtifactOpenBindingActualGatewayHTTP`：`server/testdata/open-binding-real-gateway.mjs` 动态 import 指定真实 gateway repo 的 `createControlPlane`/`ViewerStore`，真正运行 `/_gateway/apps`、`/_gateway/codes`，Go HTTP 发送验证 A/B 原 topic、幂等/错误 token/revoke/recipient text；不改 gateway source。它仍不是生产 DB、Nginx HTML 注入或浏览器自动 runtime 联合 E2E；不以测试通过宣称部署。

集成 reviewer 可直接启动 **test-only actual Go httptest fixture**，无需 production backdoor：先启动真实 gateway（含 board owner9、other owner11；public origin 与下面一致），运行：

```sh
CATSCO_OPEN_BINDING_DEV_FIXTURE=/tmp/catsco-open-binding-http.json \
CATSCO_OPEN_BINDING_GATEWAY_CONTROL_URL=http://127.0.0.1:22445 \
CATSCO_OPEN_BINDING_GATEWAY_PUBLIC_ORIGIN=https://artifact.example.test \
CATSCO_OPEN_BINDING_GATEWAY_CONTROL_TOKEN='<test-only >=32-char token>' \
CATSCO_OPEN_BINDING_DEV_LISTEN=127.0.0.1:22446 \
CATSCO_OPEN_BINDING_DEV_DURATION=5m \
go test ./server -run '^TestArtifactOpenBindingDevHTTPFixture$' -count=1 -v
```

输出 JSON mode0600 包含 platform_url、fixture JWT、同 actor 不同 session JWT、错误 actor JWT、topics/apps；测试结束清理 httptest，不修改正常配置。固定 listen 可用于 runtime parent origin allowlist；默认 loopback random port。普通 GET `/api/messages?topic_id=...` 返回 test durable store 的真实保存结果。dev duration 最大15m。

```sh
CATSCO_GATEWAY_TEST_ROOT=/path/to/catsco-artifact-gateway \
go test ./server -run '^TestArtifactOpenBindingActualGatewayHTTP$' -count=1 -v
```

```sh
go test ./server -run 'TestArtifactOpenBinding|TestArtifactLaunch|TestArtifactAppsGatewayAppOwner' -count=1
go test ./server/...
go vet ./server/...
go test -race ./server -run 'TestArtifactOpenBinding' -count=1
```
