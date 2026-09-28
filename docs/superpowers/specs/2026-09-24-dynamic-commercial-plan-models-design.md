# 商业套餐模型动态化 — 设计文档

**日期**：2026-09-24
**分支**：`feat/dynamic-commercial-plan-models`
**状态**：已批准，待实现

## 1. 问题

控制面把"官方付费套餐包含哪些模型"硬编码在多个位置，导致 relay 上线新模型后
套餐无法同步：

| 症状 | 原因 |
|---|---|
| relay 上线 `gpt-6-sol` 后，399/799 无法加入 | 白名单 `commercialOfficialPaidModels` 写死 10 个模型 |
| 已下线模型（`deepseek-v4-flash`）仍留在套餐 | 白名单与 migration 未同步 |
| 改套餐后重启被覆盖 | 7 个 migration 每次启动重放写死的 `model_budgets` JSON |
| 改一处忘另一处 | 白名单与校验函数各有 **2 份重复定义** |

### 硬编码清单

| 类别 | 位置 | 数量 | 本次处理 |
|---|---|---|---|
| **A. 模型白名单** | `server/commercial.go:808`、`server/db/postgres/commercial_plan_tier.go:21` | 2 份重复 | ✅ |
| **B. 校验函数** | `server/commercial.go:821`、`server/db/postgres/commercial_plan_tier.go:34` | 2 份重复 | ✅ |
| **C. migration 写死 JSON** | `schema.go` 中 7 个 `migrateCommercial*` | 7 个 | ✅ |
| D. Free 池历史 map | `server/commercial_relay_sync.go:579/609/622` | 3 个 | ❌ 后续评估 |
| E. 客户端模型清单 | `server/bot_model_config.go:85` | 1 个 | ❌ 后续评估 |

## 2. 目标

1. **relay 上线新模型 → 套餐自动可用**，控制面零代码改动、零发版
2. **保留误操作保护**（总额校验），但不硬编码数值
3. **套餐可选**：部分套餐只需固定模型 → 提供开关
4. **relay 不可用时有兜底**，不影响用户体验
5. **性能安全**：不新增 DB 查询，不在请求路径做重活

## 3. 数据源（实测确认）

| 来源 | 内容 | 可用性 |
|---|---|---|
| **adapter `127.0.0.1:18091/v1/models`** | ✅ **权威**（含 gpt-6-sol） | 控制面容器**无法直连**（跨容器超时） |
| relay-admin `/internal/usage/users` → `available_model_limits` | ⚠️ **滞后** | 读 DB 表 `relay_upstream_accounts.models_json`（旧值） |
| relay env `CATS_RELAY_PROVIDER_CONFIGS_JSON` | ✅ 含 gpt-6-sol | 被 DB 表优先覆盖，不可靠 |

**根因链**：

```
base_provider_configs_payload(uid)
  → UPSTREAM_ACCOUNT_STORE.provider_configs()    （DB 优先于 env）
  → 表 relay_upstream_accounts.models_json      （旧值）
  → available_model_limits                       （399/799 被拒的真正数据源）
```

**结论**：relay-admin 新增 `GET /local/model-catalog` 转发 adapter 的 `/v1/models`
（**已完成**，见 §7）。控制面通过已有的 `CATS_RELAY_ADMIN_URL` 调用。

## 4. 核心设计

### 4.1 单一事实来源

```
adapter /v1/models  →  relay-admin /local/model-catalog  →  控制面目录服务
                                                              ↓ （按套餐开关过滤）
                                                          套餐 model_budgets
                                                              ↓ （自动均分）
                                                          用户额度
```

### 4.2 额度语义（约定优于配置，不加新字段）

**规则**：
- **加模型** → 自动加入，每个模型额度 = `总额 / 模型数`（**平均分**）
- **加额度** → 后台改任意模型的值 → 按新总额重新均分
- **总额** = `Σ(model_budgets)`（保持现状，用户总额度语义不变）

**为什么保持"每模型同值"约定**：relay 侧 `commercialRelayBudgetsMatch`、
`commercialRelayUniformBudgetValue` 等已依赖"每模型同值 = 共享池"。
新增字段要改整条链路，风险大。

**示例**（399 加 gpt-6-sol）：
```
之前: 5 个文本模型 × 2100 = 10500
之后: 6 个文本模型 × 1750 = 10500   ← 总额不变，自动均分
```

### 4.3 目录服务（缓存 + 三级降级）

新增 `server/commercial_model_catalog.go`：

```go
type CommercialModelCatalog struct {
    models    []string   // 公开在役模型（有序）
    fetchedAt time.Time
    source    string     // "live" | "memory" | "snapshot"
}
```

| 层级 | 条件 | TTL | 行为 |
|---|---|---|---|
| 1. 内存缓存 | 命中且未过期 | 5 min | 直接返回（**零网络、零 DB**） |
| 2. relay live | 缓存过期时拉取 | 超时 3s | 成功 → 更新内存 + 落盘 |
| 3. 落盘快照 | relay 失败 | 24 h | 用上次成功快照 |
| 4. 超期 | 快照也过期 | — | **拒绝保存套餐**（读操作不受影响） |

**并发保护**：单飞（`sync.Mutex` + in-flight 标记）——并发请求只触发一次拉取。

**落盘路径**：`/data/commercial-model-catalog.json`

**性能保证**：
- 缓存命中 → 零网络、零 DB 查询
- **不引入任何新增 DB 查询**（用户明确要求）
- 目录读取**不在用户请求路径**（仅套餐保存 + 启动 reconcile）

### 4.4 套餐开关（新增列）

```sql
ALTER TABLE commercial_plans
  ADD COLUMN IF NOT EXISTS auto_update_models BOOLEAN NOT NULL DEFAULT TRUE;
```

| 开关 | 加新模型 | 移除下线模型 |
|---|---|---|
| `TRUE`（默认） | ✅ 自动加入 | ✅ 自动移除 |
| `FALSE` | ❌ 保持现状（有些套餐只需固定模型） | ✅ 自动移除 |

**为什么 FALSE 仍要移除下线模型**：否则套餐会留着 relay 已下线的模型，
用户选中后 404/402，体验更差。

**后台 UI**：套餐编辑器加复选框「自动跟随最新模型」。

### 4.5 校验改造（保留总额保护）

```go
func validateOfficialPaidPlanModels(
    slug string,
    budgets map[string]float64,
    catalog []string,
    currentTotal float64,
) error {
    // 1. 模型集合 ⊆ 目录（不允许自造模型）
    // 2. 每个模型额度 > 0
    // 3. 总额 == currentTotal（从 DB 当前值读，防止误操作）
}
```

**DB 性能**：`currentTotal` 从**已加载的 plan 对象**取（保存路径本来就查过 DB），
**不新增查询**。保存是低频操作（人工点击）。

### 4.6 运行时自动补齐（替代 migration）

新增 `server/db/postgres/commercial_plan_reconcile.go`：

```go
func (a *Adapter) ReconcileCommercialPlanModels(ctx context.Context, catalog []string) error {
    // 1. advisory lock（防并发启动重复执行）
    // 2. 对每个 official 套餐（catsco-personal / catsco-pro）：
    //    a. 读当前 model_budgets
    //    b. 目标集合 = catalog（若 auto_update_models=FALSE 则保持现有集合，仅移除下线模型）
    //    c. 集合无变化 → 跳过（零写操作）
    //    d. 有变化 → 总额不变，重新均分，一条 UPDATE
    // 3. 同步未完成订单的快照
    // 4. 用户 grants：仅在模型集合变化时处理（批量 SQL）
}
```

**执行时机**：启动时（`CreateSchema()` 之后）执行一次。

**性能**：
- 只在启动跑，**不在请求路径**
- 集合无变化时**零写操作**（幂等短路）
- 批量 SQL，无循环单条更新

**旧 migration 处理**：
把 7 个写死 JSON 的 migration 改成 **no-op**（保留函数名与顺序，SQL 改为
`SELECT 1`），避免启动重放覆盖 reconcile 结果。

⚠️ **关键顺序**：先加 reconcile → 再改旧 migration 为 no-op → 验证重启后值保持。

## 5. 改动文件清单

### 控制面（cats-company）

| 文件 | 改动 |
|---|---|
| `server/commercial_model_catalog.go` | **新建**：目录 + 缓存 + 三级降级 + 单飞 |
| `server/commercial_model_catalog_test.go` | **新建** |
| `server/commercial.go` | 删白名单；校验改用目录 |
| `server/db/postgres/commercial_plan_tier.go` | 删重复白名单/校验，委托上层 |
| `server/db/postgres/commercial_plan_reconcile.go` | **新建**：运行时自动补齐 |
| `server/db/postgres/commercial_plan_reconcile_test.go` | **新建** |
| `server/db/postgres/schema_commercial_model_sync.go` | **新建**：`auto_update_models` 列 |
| `server/db/postgres/schema*.go`（7 个） | 写死 JSON 改 no-op |
| `server/db/postgres/commercial.go` | 校验调用改造 |
| `server/cmd/server.go` | 启动时调用 reconcile |
| `webapp/**` | 套餐编辑器加开关 |
| 测试 | 更新现有硬编码断言 |

### relay 侧（cats-bifrost-deploy）— ✅ 已完成

| 文件 | 改动 |
|---|---|
| `relay_admin/relay_admin.py` | 新增 `GET /local/model-catalog` |
| `tests/test_relay_admin.py` | 4 个测试 |

## 6. 测试策略

| 层级 | 测试 |
|---|---|
| 目录服务 | 缓存命中/过期、relay 失败降级快照、快照过期拒绝、单飞并发 |
| 校验 | 模型不在目录 → 拒绝；总额不匹配 → 拒绝；正常 → 通过 |
| reconcile | 幂等、开关 TRUE 加模型、开关 FALSE 不加但移除下线、总额不变 |
| 集成 | 启动 → reconcile → 重启 → 值保持 |
| relay 端点 | 返回模型列表、localhost-only、adapter 不可用 503（已完成） |

## 7. relay 侧交付（已完成）

**commit**：`6ac5f27`（`feat/model-catalog-endpoint`）

```
GET /local/model-catalog
→ {"models": ["gpt-6-sol", "gpt-5.6-terra", ...], "count": 12, "source": "adapter"}

localhost-only；adapter 不可达/响应畸形 → 503（不是空列表）
env: CATS_RELAY_ADAPTER_MODEL_CATALOG_TIMEOUT（默认 3s）
```

## 8. 部署与回滚

**顺序**：
1. **先部署 relay 侧**（`/local/model-catalog`）—— 控制面依赖
2. 再部署控制面（新列 `ADD COLUMN IF NOT EXISTS`，向后兼容）

**回滚**：
- 控制面：回滚镜像（新列有默认值，旧代码忽略）
- relay：回滚端点（控制面降级到快照）

**风险**：
- migration 改 no-op 后，若 reconcile 有 bug，套餐值可能异常 → **部署前备份 `commercial_plans`**
- 已购用户（399×3、799×25）额度因均分变化 → **总额不变，用户感知为 0**

## 9. 不在本次范围

| 项 | 原因 |
|---|---|
| **D. Free 池历史 map** | 历史兼容逻辑，风险高，单独评估 |
| **E. `botModelCatalog`** | 与套餐链路独立，单独评估 |
| 5.6 系列下线 | 用户后续单独指示 |
