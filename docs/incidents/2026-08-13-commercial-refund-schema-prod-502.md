# 2026-08-13 商业化退款 schema 导致生产 502

## 摘要

2026-08-13，商业化退款与模型权益变更在测试环境部署成功，但生产环境启动失败。新版本在历史 PostgreSQL schema 上创建退款相关索引时，依赖的 `commercial_quota_grants.source_ref` 列尚不存在，导致 CatsCompany server 容器退出，网页短时间返回 502。

发现后立即将生产 `server` 和 `web` 回滚到上一健康版本。随后补充旧库兼容迁移与真实 PostgreSQL 回归测试，修复版本通过 CI、测试环境和生产部署，服务恢复并完成业务数据核验。

## 影响

- 影响服务：`https://app.catsco.cc/`
- 用户表现：网页返回 502，聊天及账号相关 API 暂时不可用。
- Relay 本身未中断；`https://relay.catsco.cc/` 保持可访问。
- 数据库没有发现业务数据丢失或重复扣款。
- 影响窗口：生产失败版本切换后至回滚完成，持续数分钟。

## 时间线

以下时间均为 Asia/Shanghai：

- 15:58：提交 `04e77ba` 的 CI 与测试环境部署开始，随后均成功。
- 16:15：生产部署流水线开始部署 `04e77ba`。
- 16:22：生产 server 启动失败，流水线失败，网页出现 502。
- 发现后立即将生产 `server/web` 恢复到上一健康版本 `8146d3d`，外网恢复 200。
- 16:35：推送热修 `04e4e25`，增加旧 schema 升级逻辑和回归测试。
- 16:48：热修测试环境部署完成，server/web healthy。
- 16:55：热修生产部署完成，三个生产容器 healthy，外网恢复并保持 200。

相关流水线：

- 失败生产部署：<https://github.com/buildsense-ai/cats-company/actions/runs/31681272024>
- 热修测试部署：<https://github.com/buildsense-ai/cats-company/actions/runs/31682740233>
- 热修生产部署：<https://github.com/buildsense-ai/cats-company/actions/runs/31683751589>

## 直接原因

生产启动日志：

```text
schema initialization failed: postgres schema statement failed:
ERROR: column "source_ref" does not exist (SQLSTATE 42703)
```

`CreateSchema()` 会直接执行 `createCommercialIndexes`，其中新索引引用了 `commercial_quota_grants.source_ref`。该列虽然出现在 SQL migration 文件和全新建表定义中，但生产启动路径没有在 `CreateSchema()` 之前执行对应 migration。历史生产表因此缺列，索引创建失败。

正确顺序应为：

1. 确保表存在。
2. 幂等补充新列。
3. 回填历史数据。
4. 创建依赖新列的约束和索引。

## 为什么 Test 通过而 Prod 失败

测试环境已经运行过较新的 schema，或由当前建表定义创建，目标列已经存在。因此测试部署验证了“当前 schema 启动”，没有验证“生产历史 schema 升级”。

原有测试也主要覆盖：

- 全新数据库创建。
- 退款业务逻辑、幂等和权限。
- 当前测试库上的容器启动与健康检查。

缺失的场景是：从不包含 `source_ref`、`revoked_at`、`refund_request_no` 和 `refunded_at` 的历史表结构启动当前版本。

## 促成因素

- 数据库存在 `CreateSchema()` 与 SQL migration 两条路径，但生产部署没有明确保证 migration 先于应用启动。
- 测试库结构与生产历史库存在差异，测试成功被误当成生产升级成功的充分条件。
- schema 变更没有附带旧结构升级测试。
- 本次变更直接推送到 `main`，绕过了正常 PR review 和合并门禁。
- 生产部署会替换运行容器；新容器启动失败期间，入口短暂失去健康 upstream。

## 恢复与修复

1. 手动恢复生产 `server/web` 到 `8146d3d`，确认容器 healthy 和外网 HTTP 200。
2. 在 `CreateSchema()` 中将兼容迁移放在商业化索引创建之前：
   - 幂等补充四个退款字段。
   - 从历史 `note` 回填 `source_ref`。
   - 再创建退款相关索引。
3. 新增 PostgreSQL 集成测试，主动删除新列和索引模拟历史 schema，并验证：
   - 第一次升级成功。
   - 历史订单引用正确回填。
   - 第二次启动保持幂等。
4. 使用真实 PostgreSQL 18 执行旧库升级测试，而不是接受因缺少 DSN 被跳过的结果。
5. 热修依次通过 CI、测试部署和生产部署。
6. 生产核验：
   - 四个新列与两个索引均存在。
   - `app.catsco.cc` 与 `relay.catsco.cc` 均返回 200。
   - UID 38 的 0.01 元测试订单为 `fulfilled`。
   - Terra/Sol 两笔额度授权合计 10500，Relay 模型范围已排除 Luna。

## 发布门禁

从本事故起执行以下规则：

- 所有正常改动默认必须开 PR，经 CI 和 review 后合并；拥有管理员权限不等于允许直推 `main`。
- 只有负责人明确说明“本次允许直接推 main”的紧急操作才可例外，并需在事后补事故或变更记录。
- 任何 PostgreSQL schema PR 必须同时验证：
  - 空数据库创建。
  - 至少一个生产历史 schema 升级场景。
  - 重复执行的幂等性。
- 仅存在于 SQL migration 文件的变更，如果部署流程没有在应用启动前执行 migration，不能视为已生效。
- 新索引、约束和 trigger 必须在依赖的表、列和回填完成后创建。
- 测试环境部署成功不能替代历史 schema 升级验证。

## 后续动作

- [ ] 为 CI 增加固定的 legacy-schema-upgrade job，确保 PostgreSQL 集成测试不会静默跳过。
- [ ] 在生产部署前增加 schema preflight，检查本次索引依赖的列是否存在。
- [ ] 定期从生产 schema-only 导出生成脱敏升级基线，不包含任何用户数据。
- [ ] 评估将 SQL migration 设为生产部署的唯一 schema 入口，逐步移除双路径语义。
- [ ] 优化生产切换方式：新 server 未健康前保留旧 upstream，减少启动失败造成的 502 窗口。

