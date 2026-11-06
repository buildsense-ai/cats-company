#!/usr/bin/env bash
# retire-legacy-plans.sh — 旧套餐处理：下架（可续费）或归档（彻底消失）
#
# 为什么是一个手动脚本，而不是启动时的自动迁移：
#
#   下架套餐是**运营决定**，不是数据库结构。写进 CreateSchema 就会在每次重启时
#   重新断言一遍，运营反悔重新上架后，下一次部署又会把它藏起来 ——
#   2026-10-29 刚因为同样的写法修过 `cloud_worker_included` 的回填。
#
# 两种动作，含义完全不同：
#
#   --apply（默认）下架：只把 sale_state 改成 hidden。
#       前台不再售卖；履约、续费、自动续费、额度同步全部照旧。
#       用于还有人在用的套餐。
#
#   --archive 归档：另外写 archived_at + state=1。
#       列表与续费都不再认这个套餐。**只能在无人使用时执行**，脚本会自己拒绝
#       有 active_users 或 open_orders 的套餐。
#
# 明确**不做**的事（做了会伤到存量买家）：
#   - 不改 price_fen / duration_days：历史订单与续费快照都按套餐行取值
#   - 不删任何行：订单、权益、账本全部引用 plan_id
#
# 用法（在能连生产库的机器上执行）：
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh                 # 只报告
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh --apply         # 下架
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh --archive       # 归档无人套餐
#   DSN='postgres://…' bash ops/retire-legacy-plans.sh --archive --only <slug>
set -Eeuo pipefail

# 下架但保留续费能力：2026-10-29 生产实测 34 个 active 权益（personal 1 / pro 33）。
# 新套餐线接管后，这些买家要么内部续费，要么换新套餐。
RETIRE_SLUGS=(
  catsco-personal
  catsco-pro
)

# 归档：2026-10-29 生产实测 0 个 active 权益、0 张未结订单。
# 这批是云员工套餐的第一版试水（9.9/7天 + 39.9/月）。
ARCHIVE_SLUGS=(
  cloud-worker-light-7d
  cloud-worker-standard-30d
)

MODE=report
ONLY=()
while (($#)); do
  case "$1" in
    --apply) MODE=retire; shift ;;
    --archive) MODE=archive; shift ;;
    --only) ONLY+=("$2"); shift 2 ;;
    -h|--help)
      cat >&2 <<'USAGE'
retire-legacy-plans.sh [--apply | --archive] [--only <slug>]...

  默认（无参数）  只报告
  --apply        下架：sale_state → hidden（有存量用户的套餐用这个）
  --archive      归档：sale_state=hidden + archived_at + state=1
                 仅当该套餐 0 个 active 权益且 0 张未结订单时执行，否则拒绝
  --only <slug>  只处理指定 slug（可重复）

环境变量：DSN = 生产 PostgreSQL 连接串
USAGE
      exit 0 ;;
    *) echo "[retire] unknown argument: $1" >&2; exit 2 ;;
  esac
done

: "${DSN:?set DSN to the production PostgreSQL URL (needs SELECT + UPDATE on commercial_plans)}"

psql_q() { psql "$DSN" -X -q -v ON_ERROR_STOP=1 "$@"; }

case "$MODE" in
  retire)  SLUGS=("${RETIRE_SLUGS[@]}") ;;
  archive) SLUGS=("${ARCHIVE_SLUGS[@]}") ;;
  *)       SLUGS=("${RETIRE_SLUGS[@]}" "${ARCHIVE_SLUGS[@]}") ;;
esac
# --only 覆盖默认清单
((${#ONLY[@]})) && SLUGS=("${ONLY[@]}")

slug_list="$(printf "'%s'," "${SLUGS[@]}")"
slug_list="${slug_list%,}"

usage_report() {
  psql_q -P pager=off <<SQL
SELECT p.slug,
       p.name,
       p.price_fen / 100.0                  AS price,
       p.duration_days                      AS days,
       p.sale_state,
       p.state,
       (p.archived_at IS NOT NULL)          AS archived,
       p.cloud_worker_deployment_profile    AS fleet,
       COALESCE(e.active_users, 0)          AS active_users,
       COALESCE(o.open_orders, 0)           AS open_orders,
       COALESCE(pc.paid_periods, 0)         AS paid_periods
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
}

echo "=== 当前状态（只读） ==="
usage_report

if [ "$MODE" = "report" ]; then
  echo
  echo "只报告，未改动。"
  echo "  --apply   下架（sale_state → hidden），用于还有用户的套餐"
  echo "  --archive 归档（+ archived_at + state=1），仅限 0 用户的套餐"
  exit 0
fi

if [ "$MODE" = "archive" ]; then
  echo
  echo "=== 归档前安全检查：必须 0 个 active 权益、0 张未结订单 ==="
  blocked="$(psql_q -tA <<SQL
SELECT string_agg(slug || '(users=' || active_users || ',orders=' || open_orders || ')', ', ')
FROM (
  SELECT p.slug,
         COALESCE(e.active_users, 0) AS active_users,
         COALESCE(o.open_orders, 0)  AS open_orders
  FROM commercial_plans p
  LEFT JOIN (SELECT plan_id, count(*) AS active_users
               FROM commercial_entitlements WHERE state = 'active'
              GROUP BY plan_id) e ON e.plan_id = p.id
  LEFT JOIN (SELECT plan_id, count(*) AS open_orders
               FROM commercial_orders WHERE status IN ('created', 'pending')
              GROUP BY plan_id) o ON o.plan_id = p.id
  WHERE p.slug IN (${slug_list})
    AND (COALESCE(e.active_users, 0) > 0 OR COALESCE(o.open_orders, 0) > 0)
) t;
SQL
)"
  if [ -n "$blocked" ]; then
    echo "[retire] 拒绝归档：以下套餐仍有存量，请改用 --apply 下架：" >&2
    echo "  $blocked" >&2
    exit 1
  fi
  echo "安全：全部 0 用户、0 未结订单。"
fi

echo
if [ "$MODE" = "archive" ]; then
  echo "=== 执行：归档（sale_state=hidden + archived_at + state=1） ==="
  psql_q <<SQL
BEGIN;
UPDATE commercial_plans
   SET sale_state  = 'hidden',
       archived_at = COALESCE(archived_at, CURRENT_TIMESTAMP),
       state       = 1,
       updated_at  = CURRENT_TIMESTAMP
 WHERE slug IN (${slug_list})
   AND archived_at IS NULL;
COMMIT;
SQL
else
  echo "=== 执行：下架（只改 sale_state） ==="
  psql_q <<SQL
BEGIN;
UPDATE commercial_plans
   SET sale_state = 'hidden',
       updated_at = CURRENT_TIMESTAMP
 WHERE slug IN (${slug_list})
   AND sale_state <> 'hidden'
   AND archived_at IS NULL;
COMMIT;
SQL
fi

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
if [ "$MODE" = "archive" ]; then
  echo "已归档：前台下架、续费不再识别。价格与周期未改动，历史订单与账本仍在。"
else
  echo "已下架：前台不再售卖；履约、续费、自动续费、额度同步照旧。"
  echo "价格、周期、归档状态未改动。运营反悔时把 sale_state 改回去即可，本脚本不会被重启覆盖。"
fi
