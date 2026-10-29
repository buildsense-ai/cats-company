#!/usr/bin/env bash
# retire-legacy-plans.sh — 把旧套餐从售卖目录里摘掉（只动 sale_state）
#
# 为什么是一个手动脚本，而不是启动时的自动迁移：
#
#   下架套餐是**运营决定**，不是数据库结构。写进 CreateSchema 就会在每次重启时
#   重新断言一遍，运营反悔重新上架后，下一次部署又会把它藏起来 ——
#   2026-10-29 刚因为同样的写法修过 `cloud_worker_included` 的回填。
#
#   这里只做一件事：把指定 slug 的 `sale_state` 改成 `hidden`。
#
# 明确**不做**的事（做了会伤到存量买家）：
#   - 不改 price_fen / duration_days：历史订单与续费快照都按套餐行取值
#   - 不写 archived_at：[archive] 会把 state 置 1，续期与自动续费都会停
#   - 不删任何行：订单、权益、账本全部引用 plan_id
#
# 用法（在能连生产库的机器上执行）：
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh                # 只报告
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh --apply        # 执行
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh --apply --only catsco-plus
set -Eeuo pipefail

# 默认清单：内测/过渡套餐（价格与 slug 以线上实际行的报告为准）。
# 399/799 的 `catsco-personal` / `catsco-pro` 也一起下架，但**保留**续费能力，
# 因为还有人在用；是否需要更彻底的处置由运营按报告决定。
DEFAULT_SLUGS=(
  catsco-trial-3d
  catsco-plus-minus
  catsco-plus
  catsco-plus-plus
  catsco-personal
  catsco-pro
)

APPLY=0
ONLY=()
while (($#)); do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --only) ONLY+=("$2"); shift 2 ;;
    -h|--help)
      cat >&2 <<'USAGE'
retire-legacy-plans.sh [--apply] [--only <slug>]...

  默认只报告：列出目标套餐的价格/周期/售卖状态/在用人数/未结订单。
  --apply   把 sale_state 改成 hidden（只改这一列）
  --only    只处理指定 slug（可重复）

环境变量：DSN = 生产 PostgreSQL 连接串（只需 commercial_plans 的 SELECT + UPDATE）

不会做的事：不改价格与周期、不写 archived_at（会让续期和自动续费停掉）、不删行。
USAGE
      exit 0 ;;
    *) echo "[retire] unknown argument: $1" >&2; exit 2 ;;
  esac
done

: "${DSN:?set DSN to the production PostgreSQL URL (this script only needs SELECT + UPDATE on commercial_plans)}"

psql_q() { psql "$DSN" -X -q -v ON_ERROR_STOP=1 "$@"; }

if ((${#ONLY[@]})); then SLUGS=("${ONLY[@]}"); else SLUGS=("${DEFAULT_SLUGS[@]}"); fi

# 清单转成 SQL 数组字面量，避免拼字符串
slug_list="$(printf "'%s'," "${SLUGS[@]}")"
slug_list="${slug_list%,}"

echo "=== 当前状态（只读） ==="
psql_q -P pager=off <<SQL
SELECT p.slug,
       p.name,
       p.price_fen / 100.0                       AS price,
       p.duration_days                           AS days,
       p.sale_state,
       p.state,
       (p.archived_at IS NOT NULL)               AS archived,
       p.cloud_worker_included                   AS worker,
       p.cloud_worker_deployment_profile         AS fleet,
       COALESCE(e.active_users, 0)               AS active_users,
       COALESCE(o.open_orders, 0)                AS open_orders,
       COALESCE(pc.paid_periods, 0)              AS paid_periods
FROM commercial_plans p
LEFT JOIN (SELECT plan_id, count(*) AS active_users
             FROM commercial_entitlements
            WHERE state = 'active'
            GROUP BY plan_id) e ON e.plan_id = p.id
LEFT JOIN (SELECT plan_id, count(*) AS open_orders
             FROM commercial_orders
            WHERE status IN ('created', 'pending')
            GROUP BY plan_id) o ON o.plan_id = p.id
LEFT JOIN (SELECT plan_id, count(*) AS paid_periods
             FROM commercial_orders
            WHERE status = 'paid'
            GROUP BY plan_id) pc ON pc.plan_id = p.id
WHERE p.slug IN (${slug_list})
ORDER BY p.price_fen, p.id;
SQL

if [ "$APPLY" != "1" ]; then
  echo
  echo "只报告，未改动。加 --apply 执行下架（sale_state → hidden）。"
  echo "注意：有 active_users 或 open_orders 的套餐下架后，存量用户仍然："
  echo "  - 继续用已经买到的额度与机器"
  echo "  - 继续被自动续费 runner 识别（它只读权益到期时间，不看 sale_state）"
  echo "  - 只是在前台列表里消失"
  exit 0
fi

echo
echo "=== 执行：sale_state → hidden ==="
psql_q <<SQL
BEGIN;
WITH touched AS (
  UPDATE commercial_plans
     SET sale_state = 'hidden',
         updated_at = CURRENT_TIMESTAMP
   WHERE slug IN (${slug_list})
     AND sale_state <> 'hidden'
     AND archived_at IS NULL
  RETURNING slug
)
SELECT slug FROM touched ORDER BY slug;
COMMIT;
SQL

echo
echo "=== 结果 ==="
psql_q -P pager=off <<SQL
SELECT slug, sale_state, state, (archived_at IS NOT NULL) AS archived,
       price_fen / 100.0 AS price, duration_days AS days
FROM commercial_plans
WHERE slug IN (${slug_list})
ORDER BY price_fen, id;
SQL

echo
echo "已完成：这些套餐不再出现在前台售卖列表；价格、周期、归档状态未改动。"
echo "如某个套餐仍需售卖，把它的 sale_state 改回 public/test 即可（本脚本不会被重启覆盖）。"
