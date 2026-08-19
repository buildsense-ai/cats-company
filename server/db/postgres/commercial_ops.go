package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) GetCommercialOperationsOverview(now time.Time) (*types.CommercialOperationsOverview, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	overview := &types.CommercialOperationsOverview{
		GeneratedAt:    now.UTC(),
		OrdersByStatus: map[string]int64{},
	}
	var createdOrders, pendingOrders, paidOrders, fulfilledOrders int64
	var closedOrders, failedOrders, refundingOrders, refundedOrders int64
	err := a.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM commercial_plans),
			(SELECT COUNT(*) FROM commercial_plans WHERE state = 0 AND sale_state IN ('test','public')),
			(SELECT COUNT(*) FROM commercial_invite_codes
			 WHERE state = 0 AND redeemed_count < max_redemptions AND (expires_at IS NULL OR expires_at > $1)),
			(SELECT COALESCE(SUM(redeemed_count), 0) FROM commercial_invite_codes),
			(SELECT COALESCE(SUM(GREATEST(max_redemptions - redeemed_count, 0)), 0)
			 FROM commercial_invite_codes
			 WHERE state = 0 AND (expires_at IS NULL OR expires_at > $1)),
			(SELECT COUNT(*) FROM commercial_orders),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'created'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'pending'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'paid'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'fulfilled'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'closed'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'failed'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'refunding'),
			(SELECT COUNT(*) FROM commercial_orders WHERE status = 'refunded'),
			(SELECT COALESCE(SUM(amount_fen), 0) FROM commercial_orders
			 WHERE status IN ('paid','fulfilled') AND paid_at >= date_trunc('month', $1::timestamptz)),
			(SELECT COUNT(*) FROM commercial_entitlements
			 WHERE state = 'active' AND starts_at <= $1 AND (expires_at IS NULL OR expires_at > $1)),
			(SELECT COUNT(*) FROM commercial_entitlements
			 WHERE state = 'active' AND starts_at <= $1 AND expires_at > $1 AND expires_at <= $1 + INTERVAL '7 days'),
			(SELECT COUNT(*) FROM commercial_quota_grants
			 WHERE revoked_at IS NULL AND effective_at <= $1 AND (expires_at IS NULL OR expires_at > $1)),
			(SELECT COUNT(DISTINCT uid) FROM commercial_managed_relay_budgets),
			(SELECT COUNT(*) FROM commercial_payment_events
			 WHERE status = 'rejected' AND created_at >= $1 - INTERVAL '24 hours')`, now.UTC()).Scan(
		&overview.PlansTotal,
		&overview.PlansOnSale,
		&overview.InvitesActive,
		&overview.InviteRedemptions,
		&overview.InviteRedemptionsRemaining,
		&overview.OrdersTotal,
		&createdOrders,
		&pendingOrders,
		&paidOrders,
		&fulfilledOrders,
		&closedOrders,
		&failedOrders,
		&refundingOrders,
		&refundedOrders,
		&overview.RevenueMonthFen,
		&overview.ActiveEntitlements,
		&overview.EntitlementsExpiring7D,
		&overview.ActiveGrants,
		&overview.ManagedUsers,
		&overview.RejectedPaymentEvents24H,
	)
	if err != nil {
		return nil, fmt.Errorf("load commercial operations overview: %w", err)
	}
	overview.OrdersByStatus["created"] = createdOrders
	overview.OrdersByStatus["pending"] = pendingOrders
	overview.OrdersByStatus["paid"] = paidOrders
	overview.OrdersByStatus["fulfilled"] = fulfilledOrders
	overview.OrdersByStatus["closed"] = closedOrders
	overview.OrdersByStatus["failed"] = failedOrders
	overview.OrdersByStatus["refunding"] = refundingOrders
	overview.OrdersByStatus["refunded"] = refundedOrders
	events, err := a.ListCommercialOperatorEvents(30)
	if err != nil {
		return nil, err
	}
	overview.RecentOperatorEvents = events
	paymentEvents, err := a.ListCommercialPaymentEvents(50)
	if err != nil {
		return nil, err
	}
	overview.RecentPaymentEvents = paymentEvents
	entitlements, err := a.ListRecentCommercialEntitlements(50)
	if err != nil {
		return nil, err
	}
	overview.RecentEntitlements = entitlements
	return overview, nil
}

func (a *Adapter) ListRecentCommercialEntitlements(limit int) ([]*types.CommercialEntitlement, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.Query(`
		SELECT e.id, e.uid, e.plan_id, p.slug, p.name, e.source, e.source_ref,
		       e.state, e.starts_at, e.expires_at, e.created_at, e.updated_at
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent commercial entitlements: %w", err)
	}
	defer rows.Close()
	entitlements := make([]*types.CommercialEntitlement, 0, limit)
	for rows.Next() {
		item, err := scanCommercialEntitlement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recent commercial entitlement: %w", err)
		}
		entitlements = append(entitlements, item)
	}
	return entitlements, rows.Err()
}

func (a *Adapter) ListCommercialPaymentEvents(limit int) ([]*types.CommercialPaymentEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.Query(`
		SELECT id, channel, event_id, order_no, provider_trade_no, event_type,
		       status, LEFT(error_message, 500), created_at
		FROM commercial_payment_events
		ORDER BY created_at DESC, id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list commercial payment events: %w", err)
	}
	defer rows.Close()
	events := make([]*types.CommercialPaymentEvent, 0, limit)
	for rows.Next() {
		var event types.CommercialPaymentEvent
		if err := rows.Scan(
			&event.ID,
			&event.Channel,
			&event.EventID,
			&event.OrderNo,
			&event.ProviderTradeNo,
			&event.EventType,
			&event.Status,
			&event.ErrorMessage,
			&event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan commercial payment event: %w", err)
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}

func (a *Adapter) RecordCommercialOperatorEvent(event *types.CommercialOperatorEvent) error {
	if event == nil {
		return fmt.Errorf("commercial operator event is nil")
	}
	_, err := a.db.Exec(`
		INSERT INTO commercial_operator_events(service, action, target_type, target_ref, status_code)
		VALUES ($1, $2, $3, $4, $5)`,
		truncateCommercialOperatorValue(event.Service, 128),
		truncateCommercialOperatorValue(event.Action, 128),
		truncateCommercialOperatorValue(event.TargetType, 64),
		truncateCommercialOperatorValue(event.TargetRef, 160),
		event.StatusCode,
	)
	if err != nil {
		return fmt.Errorf("record commercial operator event: %w", err)
	}
	return nil
}

func (a *Adapter) ListCommercialOperatorEvents(limit int) ([]*types.CommercialOperatorEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.Query(`
		SELECT id, service, action, target_type, target_ref, status_code, created_at
		FROM commercial_operator_events
		ORDER BY created_at DESC, id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list commercial operator events: %w", err)
	}
	defer rows.Close()
	events := make([]*types.CommercialOperatorEvent, 0, limit)
	for rows.Next() {
		var event types.CommercialOperatorEvent
		if err := rows.Scan(
			&event.ID,
			&event.Service,
			&event.Action,
			&event.TargetType,
			&event.TargetRef,
			&event.StatusCode,
			&event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan commercial operator event: %w", err)
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}

func truncateCommercialOperatorValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
