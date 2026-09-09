package postgres

import (
	"context"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) ListCommercialRecords(ctx context.Context, q types.CommercialRecordsQuery) (*types.CommercialRecordsPage, error) {
	// Projections are intentionally explicit. Orders must never expose checkout
	// URLs, request credentials, or payment callback payloads in a list response.
	var projection, from, search, state, uid string
	switch q.Kind {
	case "orders":
		projection = `id, order_no, uid, plan_slug, plan_name, amount_fen, channel, status,
			provider_trade_no, paid_at, fulfilled_at, expires_at, closed_at,
			refund_request_no, refunded_at, LEFT(last_error, 500) AS last_error, created_at`
		from, search, state, uid = "commercial_orders", "order_no || ' ' || plan_name || ' ' || COALESCE(provider_trade_no,'')", "status", "uid"
	case "invites":
		projection = `i.id, i.code, i.plan_id, p.slug AS plan_slug, p.name AS plan_name, i.max_redemptions, i.redeemed_count,
			i.expires_at, i.state, i.note, i.created_at, i.updated_at, i.cloud_worker_credits`
		from, search, state, uid = "commercial_invite_codes i JOIN commercial_plans p ON p.id = i.plan_id", "i.code || ' ' || p.name", "i.state::text", "NULL::bigint"
	case "payment-events":
		projection = `e.id, e.channel, e.event_id, e.order_no, e.provider_trade_no, e.event_type,
			e.status, LEFT(e.error_message, 500) AS error_message, e.created_at`
		from, search, state, uid = "commercial_payment_events e LEFT JOIN commercial_orders o ON o.order_no = e.order_no", "e.order_no || ' ' || e.event_id", "e.status", "o.uid"
	case "entitlements":
		projection = `e.id, e.uid, e.plan_id, p.slug AS plan_slug, p.name AS plan_name,
			e.source, e.source_ref, e.state, e.starts_at, e.expires_at, e.created_at, e.updated_at`
		from, search, state, uid = "commercial_entitlements e JOIN commercial_plans p ON p.id = e.plan_id", "p.name || ' ' || e.source_ref", "e.state", "e.uid"
	case "operator-events":
		projection = `id, service, action, target_type, target_ref, status_code, created_at`
		from, search, state, uid = "commercial_operator_events", "service || ' ' || action || ' ' || target_ref", "status_code::text", "NULL::bigint"
	default:
		return nil, fmt.Errorf("invalid commercial records kind")
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 1000000 {
		return nil, fmt.Errorf("invalid pagination")
	}
	// Count and page share one statement/snapshot, including an empty last page.
	sql := fmt.Sprintf(`WITH filtered AS NOT MATERIALIZED (
		SELECT %s FROM %s
		WHERE ($1::bigint = 0 OR %s = $1) AND ($2 = '' OR (%s) ILIKE '%%' || $2 || '%%')
		  AND ($3 = '' OR %s = $3)
	), page AS (SELECT * FROM filtered ORDER BY created_at DESC, id DESC LIMIT $4 OFFSET $5)
	SELECT (SELECT COUNT(*) FROM filtered),
	       COALESCE(jsonb_agg(to_jsonb(page) ORDER BY created_at DESC, id DESC), '[]'::jsonb)
	FROM page`, projection, from, uid, search, state)
	page := &types.CommercialRecordsPage{Limit: q.Limit, Offset: q.Offset}
	if err := a.db.QueryRowContext(ctx, sql, q.UID, q.Search, q.Status, q.Limit, q.Offset).Scan(&page.Total, &page.Records); err != nil {
		return nil, fmt.Errorf("list commercial records: %w", err)
	}
	page.HasMore = int64(q.Offset+q.Limit) < page.Total
	return page, nil
}
