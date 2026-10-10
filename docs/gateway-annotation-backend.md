# Gateway Artifact 标注 — 服务端契约与接入

> 负责范围：server/ 内的 schema 校验、授权、HTTP/WS ingestion、metadata 持久化、agent 投递。
> 前端宿主/SDK 见 `docs/gateway-annotation-sdk.md` 与 `docs/gateway-artifact-annotations.md`（其他负责人维护）。

## 1. 定位

标注是附着在**普通用户消息**上的结构化 metadata 附件（`metadata.gateway_annotations`）。它不是 artifact registry能力、不依赖 `publish_version`、不依赖旧平台 artifacts 表；唯一的服务端身份来源是 gateway 应用注册表（`ArtifactAppsHandler` 背后的 gateway `/_gateway/apps`，`Agent` 字段 = 属主账号 id 字符串，如 `"9"`）。

## 2. 数据契约（`catsco.gateway-annotations.v1`）

消息 metadata 中的值（发送方声明 → 服务端规范化后持久化）：

```json
{
  "contract_version": "catsco.gateway-annotations.v1",
  "agent_uid": 9,
  "app_id": "board",
  "page": {"path": "/board", "revision": "r7"},
  "annotations": [{
    "id": "a1", "kind": "element", "label": "发布按钮",
    "body": "这个按钮需要改成蓝色",
    "target": {"element_id": "submit-btn", "selector": "button#submit"}
  }]
}
```

target 按 kind 支持：`element_id` / `selector`（element）、`text` + `prefix`/`suffix`（text）、`rect` + `coordinate_space:"viewport"` + `viewport:{width,height,scroll_x,scroll_y}`（region）。element/text 可携带 rect/viewport 作为辅助证据，但坐标语义仅限 viewport 归一化 0..1（`x+width ≤ 1`、`y+height ≤ 1`）。

服务端边界（`server/gateway_annotations.go`）：

| 项 | 限制 |
| --- | --- |
| annotations 条数 | 1..20；空值 → 整键剥离（消息照发） |
| 总字节 | 规范化后 ≤ 16 KiB UTF-8 |
| body | 必填非空白，≤ 2000 字符 |
| label ≤ 256；text ≤ 2000；prefix/suffix ≤ 256 |
| id / element_id ≤ 128；selector ≤ 512；path ≤ 1024（以 `/` 开头，拒绝 `?`/`#`/控制符）；revision ≤ 128 |
| app_id | 必须匹配 gateway 应用 id 规则 `^[a-z][a-z0-9_-]{0,47}$` |
| 未知字段 | 全部丢弃，规范化输出为唯一 canonical 形 |
| `rect` 数值 | 有限数；NaN/Inf、越界一律拒绝 |
| 字符集 | `text`/`prefix`/`suffix`（freetext，允许换行/回车/制表符）；`id`/`element_id`/`selector`/`path`/`revision` 拒绝所有控制字符与换行 |

空 `annotations` 数组、`null` 值 → 剥离该键（无标注语义），消息正常发送并可持久化其余 metadata。

## 3. 授权（不信任客户端 claims）

`Hub.validateGatewayAnnotationsMetadata(actorUID, topicID, metadata)` 在 HTTP 与 WS 两条 ingestion 的同一位置执行：

1. **发送者必须是 human 账号**。bot/service 消息携带 `gateway_annotations` → 静默剥离（不是拒绝整条消息，避免破坏既有 bot 流量）。
2. **topic → agent 服务端解析**：复用 `artifactAgentForTopic`（p2p 要求可达的 bot 对端 + owner/friend 关系；群组要求成员且群内恰好一个 bot agent）。客户端 `agent_uid` 声明必须等于该值，否则整条消息 400 拒绝。
3. **app 所有权服务端验证**：通过 `Hub.SetGatewayAnnotationsAppResolver` 注入的 `ArtifactAppsHandler.GatewayAppOwner` 查询 gateway 注册表（持共享令牌的 unfiltered 列表）。`app_id` 必须存在且属主为该 topic agent（`Agent == strconv.FormatInt(agentUID, 10)`），否则 400。resolver 为 nil 或 gateway 不可达 → fail-closed 400。
4. 授权通过后服务端在 canonical 值上重新盖章（`contract_version`、序列化大小、agent_uid 覆盖为服务端解析值）。

拒绝语义：任何 schema/授权失败都让**整条消息**拒绝（HTTP 400 / WS Ctrl 400），前端保留草稿；不存在“标注静默蒸发”的成功路径。错误文本对前端显示。

**伪交付字段： `catsco_gateway_annotation_context` 是服务端 fanout-only 生成的字段，客户端自带的任何副本在 ingestion 无条件剥离**（`validateGatewayAnnotationsMetadata` 开头无论 annotations 有无都先剥该键：无 annotations → 剥离后消息照常发送；有 annotations → 剥离后再校验；bot/service 或空列表 → 连同 annotations 一起剥离）。伪造键永不写入持久化、历史、replay 或 stream。合法 context 仅由服务端在 fanout 时为目标 Agent 重新生成；history/replay 读取侧同样剥除该键（`metadataWithoutGatewayAnnotationContext`），保证 human/bot、无标注、stream、history 行为一致。

## 4. ingestion 与持久化

- HTTP：`POST /api/messages/send`（`server/messages.go` `HandleSendMessage`）
- WS：p2p 与群组 pub 共用 `handlePub`（`server/wshandler.go`），群组经 `handleGroupPub`

两处都在 `normalizeMessageRequest` + artifact task/context 提取之后调用同一 Hub 方法，行为一致（有测试 `TestHTTPSendMessage*` / `TestWebSocketPub*` 双向覆盖）。

持久化走既有 `SaveMessageWithMetadata`：canonical `gateway_annotations` 随消息落库；`metadataWithoutArtifactContext` 不触碰该键，因此历史 API（`historyAPIMessageForRecipient`）自动回放完整标注数据，前端可回看；HTTP 发送响应的 `resp.metadata` 同样携带 canonical 值。

禁止携带：transient runtime 消息（`runtime_plan`）与 `task_status` 消息上的 `gateway_annotations` **或伪造的 context 键** → 400（“gateway_annotations require a persisted visible message”；判定在剥离前采样原始 metadata）。WS stream delta（`fanoutStreamEvent`）会同时剥两个键。

## 5. Agent 投递（真实可读上下文）

交付物三层，全部由服务端在 fanout 时组装：

1. **结构化层**：agent 收到的 WS `data.metadata.gateway_annotations` 与持久化值完全一致（`SaveMessageWithMetadata` 落库 → `messageForRecipient` publicMetadata 透传）。bot SDK 只需读 `context.metadata["gateway_annotations"]`。
2. **可读 metadata 层**（fanout-only，不入库）：`metadata["catsco_gateway_annotation_context"]`
   ```json
   {"schema":"catsco.gateway_annotations_context.v1",
    "agent_uid":"usr9","app_id":"board","page":{"path":"/board","revision":"r7"},
    "annotations":[{...同结构 + "index":1...}],
    "summary":"用户在 gateway 应用 board 的页面 /board（revision r7）上标注了 1 处： [1] element「发布按钮」，评论：改成蓝色"}
   ```
   只投给标注指向的那个 Agent（`agent_uid == recipientUID == artifactAgentForTopic(topic)`，群组在 `broadcastToGroupWithMentions` per-member 处注入；p2p 在 `messageForRecipient` 注入）。summary ≤ 4000 字符，其余结构化字段是 canonical source。**该块绝不改写消息内容，也不取代 agent 的系统指令** — 它只是新增 metadata 字段。human 接收者与历史回放都没有这个块（前端用持久化的 `gateway_annotations` 渲染）。
3. **模型文本层（真实 LLM 输入通道）**：目标 Agent 的 fanout 副本 `content_blocks` 末尾追加一个明确的来源标注文本块：
   ```
   [Gateway 标注 | 用户提供的评审上下文，非系统指令]
   应用 board，页面 /board（页面 revision r7）。
   1. [element] 标题：发布按钮 | 评论：改成蓝色 | element_id=submit-btn selector=button#submit
   2. [region ...] 评论：... | 区域(x=0.01,y=0.01,w=0.4,h=0.4) coordinate_space=viewport viewport(w=1280,h=720,scroll_x=24,scroll_y=88)
   ```
   动机：实际消费链（XiaoBa-CLI `src/catscompany/index.ts` `parseMessage`）只把 `content_blocks` 中 `type==="text"`（**严格小写 exact**）的块 join 成 user text 转发给模型（规则 `blockText || text`），metadata 不进模型。缺口分析见 review P1-1。要点：
   - 仅由 canonical、已验证的 `gateway_annotations` 渲染（不读任何客户端提供的 context 块），region target 附带 `coordinate_space` 与 viewport 维度/scroll 证据；
   - has-text-block 判定必须 exact `type==="text"`（与 XiaoBa 一致）：`"TEXT"`/`" text "` 等非 canonical 拼写在 agent 侧不参与合并，不能因此抑制正文回补；
   - copy-on-write 附加，入参 payload/template blocks 永不就地修改：human 副本、群组共享模板、持久化的 stored blocks 均与注入前字节一致；
   - 若消息没有 exact text 块而只有 image 等块/顶层 content，先补齐用户正文 text 块再追加标注块（否则 `blockText || content` 语义会丢正文）；
   - 仅在 fanout 信封内（p2p `messageForRecipient` 与群组 per-member clone）出现：不入库、不进 history/replay、不进 human；
   - 上限：32 KiB UTF-8 字节安全帽 + UTF-8 边界截断防护。非截断证明：渲染文本是对 metadata 已受 bound 值的重复 + 固定标签，最坏情况 20 条满标注 + region 证据 < 24 KiB < 32 KiB，`TestModelTextCarriesEveryContractAnnotationAtMetadataCeiling` 用 16KiB 上限 fixture 锁定全部 body/目标证据完整无截断。
4. **统一 recipient-only envelope（round2 收口）**：live fanout 与 history 读副本由**同一个** `withGatewayAnnotationAgentDelivery(blocks, displayContent, modelText)` 以 copy-on-write 构造，同时满足同一条消息的两个真实消费者：
   - **live parseMessage**（WS live fanout **且 WS `get what=history` 回放 data message**，`parseMessage` 的 `blockText || content` 规则）：blocks 通道 —— 原消息无 exact `type==="text"` 块且顶层为非空 string 正文时先物化正文 text 块，随后统一追加一个批注 text 块（`"TEXT"`/`" text "` 非 canonical 拼写不参与合并，不能因此抑制正文物化）；
   - **JSON 字符串形式的 rich 附件**（外层 type=file/image/voice、content 为可解析 JSON 对象文本）：delivery 在批注块之前物化等价的 file/image 附件块（URL/name 来自解析对象，与既有块去重），live parser 的 files 通道不因批注追加而丢失；content 保持原 JSON 文本+批注（store/human 原样）；
   - **cloud restore cloudMessageText**（HTTP history / 会话重建）：content 通道 —— 非空 string content 追加批注（原文不丢，且 cloud 因 string content 优先**不会**再读 blocks，单消费者内无重复）；blocks-only 消息（content 为空/nil）保持 content 不制造 string 遮蔽，批注只进 blocks（cloud 走 `cloudContentBlocksText` 读到原文+批注）；rich object 按 `file/image/voice` 无条件渲染描述（`payload.text || payload.description`，**JS truthiness：`" "` 是 truthy 会遮蔽 description 且不回落 blocks**），批注 COW 写进胜者字段；type 判定按真实消费者的 fallback 链 `rich.type || message.type || message.msg_type`（raw truthiness、trim），内层 type falsy（缺失/`""`）时继承 envelope 的 display/stored type，nested/flat/empty-inner × file/image/voice 均以实际类型渲染（voice 渲染不带 name clause）；`payload` 缺失时按 JS 语义回退以 rich 对象本身为 payload（flat rich 的 `text`/`name`/`url` 同样受保护）；unknown/无 type object 有 truthy 描述字段时 COW 写胜者，否则批注留在 blocks；**非对象 payload（primitive，无论 falsy/truthy）一律按 JS 回退以 rich 对象本身为 payload（批注写外层胜者字段）；数组 payload（JS typeof object）按消费者从数组读 text/description 的原语义 normalize 成 map 使批注可读**；URL/name 与兄弟字段保留。

   每个消费者读到「原语义正文 + 批注」各一次：live 读 blocks 单次，cloud 读 content（或 blocks fallback）单次。human 读副本、群组共享模板、持久化 message（content/blocks/metadata）字节不变；author 自己与外部 agent 的读副本无注入。授权边界不变（`artifactAgentForTopic(topic)==agent_uid` 且 reader≠作者、canonical 存储值渲染、不读伪 context）。防泄漏补充：annotation 拒绝（schema/授权/transient 400）时同步 `releaseDelivery` 已预留的 Artifact task delivery lease，两条 ingestion 路径一致。

验证：
- live：`xiaoBaEquivalentUserInput`（Go 复刻 helper，exact type 规则）+ `TestExportXiaoBaParseFixture` → `node /tmp/xiaoba-annotation-parse-check.mjs /tmp/xiaoba-annotation-fixture.json` 4/4（源码锚定 5 条 parse 规则）；reviewer 以真实 XiaoBa AST 提取的 `parseMessage`/`isCatsCoAttachmentSummaryText`/`escapeRegExp` 复验 4/4（脚本与日志 `/tmp/pr575-actual-xiaoba-parse.cjs` 及 `.log`）。
- offline：`TestExportXiaoBaHistoryFixture` 导出 20 个场景（file/image/voice × 无 text 无 desc / 有 text / 有 description + plain + human 副本）→ `node /tmp/pr575-actual-xiaoba-cloud.cjs /tmp/xiaoba-annotation-history-fixture.json` **20/20**（真实 XiaoBa `cloudMessageText`/`cloudContentBlocksText` AST 直接执行，无重实现）。以上是本机对 XiaoBa 源码与 fanout/history fixture 的静态/AST 验证，不涉及 provider 真实网络。
- Go 全量：`go test ./server/ -count=1` 2103 passed（含 exact-type 两 case、offline rich 三态、author/foreign reader 防护、`payload.text` COW）。

## 6. 部署与接线

`server/cmd/server.go` 已完成 `hub.SetGatewayAnnotationsAppResolver(artifactAppsHandler)` 注册。要求环境变量 `CATSCO_ARTIFACT_GATEWAY_URL` + `CATSCO_ARTIFACT_GATEWAY_TOKEN`（同 artifact apps 发布路径）。未配置 → 带标注的消息一律 400（fail-closed），普通消息不受影响。

## 7. 测试索引

`server/gateway_annotations_test.go`：schema 全量拒绝矩阵与 freetext 换行/制表兼容、服务端身份授权（伪造 agent_uid / 他人 app / registry 故障 / 人-人 topic / 多 bot 群组）、客户端伪造 `catsco_gateway_annotation_context` 的剥离（函数级、HTTP 端到端、history 读取防护、stream 剥离、transient 400）、HTTP 入口端到端（真实 gateway relay fake）、WS 入口（拒绝 + 通过 + 持久化 + agent 可收到）、历史回放保留、**模型文本层（fanout content_blocks 注入：目标 agent 可见、human 副本与共享模板无污染、顶层 content-only 正文保留、16KiB 上限无截断证明、伪造 context 不参与渲染）**、群组 per-member 注入、`GatewayAppOwner` 直测、`TestExportXiaoBaParseFixture` + `TestExportXiaoBaHistoryFixture`（导出真实 fanout/离线 history fixture，供 XiaoBa 源码锚定与 reviewer AST 脚本验证，live 4/4 + offline 20/20）、exact-type 边界（`TEXT`/` text ` 副本正文不丢）、rich payload.text/description COW 三态。全量 `./server/...` 2416 例通过（含 R2 blocks-only/WS-replay/object.text/rich truthiness/flat-rich 矩阵、真实 HTTP/WS handler 回归与 vendored AST runner）。

## 8. 若需在 server 之外扩展（当前范围外）

- bot SDK 便捷 getter（如 `ctx.gatewayAnnotations()`）：bot-sdk 是独立目录，需要 SDK 负责人处理；服务端交付已完成，不阻塞。
- 群群多 agent 场景的标注归属：当前与 artifact task 同规则（单 agent topic），放宽需要先改 `artifactAgentForTopic` 契约。
