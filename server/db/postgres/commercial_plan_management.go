package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// Only selected page IDs are aggregated; individual UIDs are fetched separately.
const commercialPlanUsageSQL = `
WITH selected AS (
 SELECT id, slug, state, sale_state FROM commercial_plans
 WHERE archived_at IS NULL AND id = ANY($1::bigint[])
), assignments AS (
 SELECT e.plan_id, e.uid, e.starts_at <= CURRENT_TIMESTAMP AS started
 FROM commercial_entitlements e JOIN selected p ON p.id=e.plan_id
 WHERE e.state='active' AND (e.expires_at IS NULL OR e.expires_at>CURRENT_TIMESTAMP)
 UNION ALL
 SELECT g.plan_id, g.uid, g.effective_at <= CURRENT_TIMESTAMP
 FROM commercial_quota_grants g JOIN selected p ON p.id=g.plan_id
 WHERE g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at>CURRENT_TIMESTAMP)
), counts AS (
 SELECT plan_id, COUNT(DISTINCT uid) FILTER (WHERE started) active_users,
 COUNT(DISTINCT uid) FILTER (WHERE NOT started) scheduled_users
 FROM assignments GROUP BY plan_id
)
SELECT p.id, p.slug, p.state, p.sale_state, COALESCE(c.active_users,0), COALESCE(c.scheduled_users,0),
 (SELECT count(*) FROM commercial_invite_codes i WHERE i.plan_id=p.id AND i.state=0
  AND i.redeemed_count<i.max_redemptions AND (i.expires_at IS NULL OR i.expires_at>CURRENT_TIMESTAMP)),
 (SELECT count(*) FROM commercial_orders o WHERE o.plan_id=p.id AND o.status IN ('created','pending','paid','refunding'))
FROM selected p LEFT JOIN counts c ON c.plan_id=p.id ORDER BY p.id`

type commercialPlanQueryer interface {
	Query(string, ...interface{}) (*sql.Rows, error)
}

func commercialPlanUsages(db commercialPlanQueryer, ids []int64, trialSlug string) ([]*types.CommercialPlanUsage, error) {
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = strconv.FormatInt(id, 10)
	}
	rows, err := db.Query(commercialPlanUsageSQL, "{"+strings.Join(values, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("load plan usage: %w", err)
	}
	defer rows.Close()
	out := []*types.CommercialPlanUsage{}
	for rows.Next() {
		item := &types.CommercialPlanUsage{}
		var slug, sale string
		var state int
		if err := rows.Scan(&item.PlanID, &slug, &state, &sale, &item.ActiveUsers, &item.ScheduledUsers, &item.ActiveInvites, &item.OpenOrders); err != nil {
			return nil, err
		}
		switch {
		case slug == "catsco-free" || slug == "catsco-personal" || slug == "catsco-pro" || slug == "catsco-legacy-custom" || (trialSlug != "" && slug == trialSlug):
			item.DeleteBlockedBy = "系统或新人试用套餐"
		case state == 0 && sale != "hidden":
			item.DeleteBlockedBy = "前台公开或灰度展示中"
		case item.ActiveUsers > 0:
			item.DeleteBlockedBy = "仍有用户在使用"
		case item.ScheduledUsers > 0:
			item.DeleteBlockedBy = "仍有待生效权益或额度"
		case item.ActiveInvites > 0:
			item.DeleteBlockedBy = "仍有可用邀请码"
		case item.OpenOrders > 0:
			item.DeleteBlockedBy = "仍有未完成订单"
		default:
			item.CanDelete = true
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *Adapter) ListCommercialPlanUsage(ids []int64, trialSlug string) ([]*types.CommercialPlanUsage, error) {
	return commercialPlanUsages(a.db, ids, trialSlug)
}

func (a *Adapter) ListCommercialPlanUsers(planID, afterUID int64, limit int) (*types.CommercialPlanUsers, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := a.db.Query(`
 SELECT uid FROM (
  SELECT uid FROM commercial_entitlements WHERE plan_id=$1 AND state='active'
   AND starts_at<=CURRENT_TIMESTAMP AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP) AND uid>$2
  UNION
  SELECT uid FROM commercial_quota_grants WHERE plan_id=$1 AND revoked_at IS NULL
   AND effective_at<=CURRENT_TIMESTAMP AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP) AND uid>$2
 ) users ORDER BY uid LIMIT $3`, planID, afterUID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &types.CommercialPlanUsers{UIDs: []int64{}}
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out.UIDs = append(out.UIDs, uid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.UIDs) > limit {
		out.HasMore = true
		out.UIDs = out.UIDs[:limit]
	}
	if len(out.UIDs) > 0 {
		out.NextUID = out.UIDs[len(out.UIDs)-1]
	}
	return out, nil
}

// Archiving retains order and entitlement history. The row lock serializes
// this check with plan edits and the reference guards installed by the schema.
func (a *Adapter) DeleteCommercialPlan(id int64, slug, trialSlug string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int64
	err = tx.QueryRow(`SELECT id FROM commercial_plans WHERE id=$1 AND slug=$2 AND archived_at IS NULL FOR UPDATE`, id, slug).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrCommercialPlanNotFound
	}
	if err != nil {
		return err
	}
	usage, err := commercialPlanUsages(tx, []int64{id}, trialSlug)
	if err != nil {
		return err
	}
	if len(usage) != 1 || !usage[0].CanDelete {
		return types.ErrCommercialPlanDeleteConflict
	}
	if _, err = tx.Exec(`UPDATE commercial_plans SET archived_at=CURRENT_TIMESTAMP,state=1,sale_state='hidden' WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}
