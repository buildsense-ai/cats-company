package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const commercialPlanColumns = `id, slug, name, description, price_fen, currency, sale_state, purchase_limit,
  monthly_budget_cny, real_cost_cny, model_budgets, internal_quota_tokens, duration_days, state, sort_order, auto_update_models, created_at, updated_at, cloud_worker_billing_mode, cloud_worker_deployment_profile, cloud_worker_retention_days, cloud_worker_included, plan_tier, cloud_worker_runtime_plan, model_credit_units`

// commercialRefundMinRetentionDays bounds the retention window after a refund
// for lifecycles that predate the per-plan retention column. A refund is a
// termination by agreement, not a request to erase data on the spot, so the
// worker is always parked for at least the shortest published window instead of
// being destroyed in the next sweep tick.
const commercialRefundMinRetentionDays = 3

// commercialCreditSubscriptionDurationDays is the only duration a credit
// subscription may be sold with. Its allowance is granted per natural month, so
// fulfilment always bills one month; selling a longer term would hand the buyer
// a window shorter than the one advertised. Legacy CNY/token packages keep their
// own free-form durations.
const commercialCreditSubscriptionDurationDays = 30

func (a *Adapter) GetCommercialPlan(id int64) (*types.CommercialPlan, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid commercial plan id")
	}
	plan, err := scanCommercialPlan(a.db.QueryRow(`SELECT `+commercialPlanColumns+` FROM commercial_plans WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get commercial plan: %w", err)
	}
	return plan, nil
}

func (a *Adapter) GetCommercialPlanBySlug(slug string) (*types.CommercialPlan, error) {
	plan, err := scanCommercialPlan(a.db.QueryRow(`SELECT `+commercialPlanColumns+` FROM commercial_plans WHERE slug = $1`, strings.TrimSpace(slug)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get commercial plan by slug: %w", err)
	}
	return plan, nil
}

func scanCommercialOrder(scanner interface {
	Scan(dest ...interface{}) error
}) (*types.CommercialOrder, error) {
	var order types.CommercialOrder
	var budgets []byte
	var expiresAt, paidAt, fulfilledAt, closedAt, refundedAt sql.NullTime
	if err := scanner.Scan(
		&order.ID,
		&order.OrderNo,
		&order.UID,
		&order.PlanID,
		&order.Kind,
		&order.PlanSlug,
		&order.PlanName,
		&order.PlanDescription,
		&order.PlanDurationDays,
		&order.PlanMonthlyBudget,
		&budgets,
		&order.AmountFen,
		&order.Currency,
		&order.Channel,
		&order.Status,
		&order.ProviderTradeNo,
		&order.CheckoutURL,
		&order.ClientRequestID,
		&expiresAt,
		&paidAt,
		&fulfilledAt,
		&closedAt,
		&order.RefundRequestNo,
		&refundedAt,
		&order.LastError,
		&order.CreatedAt,
		&order.UpdatedAt,
		&order.PlanModelCreditUnits,
	); err != nil {
		return nil, err
	}
	order.PlanModelBudgets = decodeModelBudgets(budgets)
	order.ExpiresAt = nullableTime(expiresAt)
	order.PaidAt = nullableTime(paidAt)
	order.FulfilledAt = nullableTime(fulfilledAt)
	order.ClosedAt = nullableTime(closedAt)
	order.RefundedAt = nullableTime(refundedAt)
	return &order, nil
}

const commercialOrderColumns = `id, order_no, uid, COALESCE(plan_id, 0), kind, plan_slug, plan_name, plan_description,
	plan_duration_days, plan_monthly_budget_cny, plan_model_budgets, amount_fen, currency, channel,
	status, provider_trade_no, checkout_url, client_request_id, expires_at, paid_at, fulfilled_at,
	closed_at, refund_request_no, refunded_at, last_error, created_at, updated_at, plan_model_credit_units`

func (a *Adapter) CreateCommercialOrder(order *types.CommercialOrder) (*types.CommercialOrder, error) {
	if order == nil || order.UID <= 0 || order.PlanID <= 0 || strings.TrimSpace(order.OrderNo) == "" || strings.TrimSpace(order.ClientRequestID) == "" {
		return nil, fmt.Errorf("invalid commercial order")
	}
	channel := strings.TrimSpace(order.Channel)
	if channel == "" {
		return nil, fmt.Errorf("invalid commercial order")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin commercial order: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("commercial_order:%d:%s", order.UID, order.ClientRequestID)); err != nil {
		return nil, fmt.Errorf("lock commercial order idempotency: %w", err)
	}

	existing, err := scanCommercialOrder(tx.QueryRow(`SELECT `+commercialOrderColumns+` FROM commercial_orders WHERE uid = $1 AND client_request_id = $2`, order.UID, order.ClientRequestID))
	if err == nil {
		if existing.PlanID != order.PlanID || existing.Channel != channel {
			return nil, fmt.Errorf("client request id is already used by another order")
		}
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("check commercial order idempotency: %w", err)
	}
	existing, err = scanCommercialOrder(tx.QueryRow(`
		SELECT `+commercialOrderColumns+` FROM commercial_orders
		WHERE order_no = (
			SELECT order_no FROM commercial_order_request_ids
			WHERE uid = $1 AND client_request_id = $2
		)`, order.UID, order.ClientRequestID))
	if err == nil {
		if existing.PlanID != order.PlanID || existing.Channel != channel {
			return nil, fmt.Errorf("client request id is already used by another order")
		}
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("check commercial order request alias: %w", err)
	}

	plan, err := scanCommercialPlan(tx.QueryRow(`SELECT `+commercialPlanColumns+` FROM commercial_plans WHERE id = $1 FOR SHARE`, order.PlanID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("commercial plan not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load commercial plan: %w", err)
	}
	// Credit-only plans sell ledger units; a new buyer has no legacy model budget.
	creditOnly := plan.ModelCreditUnits > 0 && plan.MonthlyBudget <= 0 &&
		!commercialPlanHasPositiveBudget(plan) && store.ModelCreditTaskPlanTierTrusted(plan.PlanTier)
	if plan.State != 0 || plan.PriceFen <= 0 || plan.DurationDays <= 0 || plan.MonthlyBudget > 0 || (!commercialPlanHasPositiveModelBudget(plan) && !creditOnly) {
		return nil, fmt.Errorf("commercial plan is not purchasable")
	}
	// A credit package is a monthly subscription: fulfilment bills exactly one
	// natural month, so any other duration would sell a term the buyer never
	// receives. Refuse the order here instead of silently rewriting it, and the
	// plan-management upsert below refuses to put such a package on sale at all.
	if plan.ModelCreditUnits > 0 && plan.DurationDays != commercialCreditSubscriptionDurationDays {
		return nil, fmt.Errorf("credit plans are sold as %d-day monthly subscriptions", commercialCreditSubscriptionDurationDays)
	}
	if err := validateCommercialOfficialPlanPurchase(tx, order.UID, plan.Slug, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := validateCommercialOfficialOpenOrder(tx, order.UID, plan.ID, channel, plan.Slug, time.Now().UTC()); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("commercial_open_order:%d:%d:%s", order.UID, plan.ID, channel)); err != nil {
		return nil, fmt.Errorf("lock commercial open order: %w", err)
	}
	openOrder, err := scanCommercialOrder(tx.QueryRow(`
		SELECT `+commercialOrderColumns+` FROM commercial_orders
		WHERE uid = $1 AND plan_id = $2 AND channel = $3
		  AND status IN ('created','pending')
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, order.UID, plan.ID, channel))
	if err == nil {
		if _, aliasErr := tx.Exec(`
			INSERT INTO commercial_order_request_ids(uid, client_request_id, order_no)
			VALUES ($1, $2, $3)
			ON CONFLICT(uid, client_request_id) DO NOTHING`, order.UID, order.ClientRequestID, openOrder.OrderNo); aliasErr != nil {
			return nil, fmt.Errorf("save commercial order request alias: %w", aliasErr)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit commercial order request alias: %w", err)
		}
		return openOrder, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("find commercial open order: %w", err)
	}
	if plan.PurchaseLimit > 0 {
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("commercial_purchase:%d:%d", order.UID, plan.ID)); err != nil {
			return nil, fmt.Errorf("lock commercial purchase limit: %w", err)
		}
		if _, err := tx.Exec(`
			UPDATE commercial_orders SET status = 'closed', closed_at = CURRENT_TIMESTAMP, checkout_url = ''
			WHERE uid = $1 AND plan_id = $2 AND status IN ('created','pending') AND expires_at <= CURRENT_TIMESTAMP`, order.UID, plan.ID); err != nil {
			return nil, fmt.Errorf("close expired commercial reservations: %w", err)
		}
		var reserved int
		if err := tx.QueryRow(`
			SELECT COUNT(*) FROM commercial_orders
			WHERE uid = $1 AND plan_id = $2 AND status IN ('created','pending','paid','fulfilled')`, order.UID, plan.ID).Scan(&reserved); err != nil {
			return nil, fmt.Errorf("count commercial purchases: %w", err)
		}
		if reserved >= plan.PurchaseLimit {
			return nil, fmt.Errorf("purchase limit reached")
		}
	}
	budgets, err := encodeModelBudgets(plan.ModelBudgets)
	if err != nil {
		return nil, fmt.Errorf("encode commercial order snapshot: %w", err)
	}
	expiresAt := order.ExpiresAt
	if expiresAt == nil {
		value := time.Now().UTC().Add(20 * time.Minute)
		expiresAt = &value
	}
	created, err := scanCommercialOrder(tx.QueryRow(`
		INSERT INTO commercial_orders(
			order_no, uid, plan_id, plan_slug, plan_name, plan_description, plan_duration_days,
			plan_monthly_budget_cny, plan_model_budgets, amount_fen, currency, channel, status,
			client_request_id, expires_at, plan_model_credit_units
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, 'created', $13, $14, $15)
		RETURNING `+commercialOrderColumns,
		order.OrderNo,
		order.UID,
		plan.ID,
		plan.Slug,
		plan.Name,
		plan.Description,
		plan.DurationDays,
		plan.MonthlyBudget,
		string(budgets),
		plan.PriceFen,
		normalizeCommercialCurrency(plan.Currency),
		channel,
		strings.TrimSpace(order.ClientRequestID),
		expiresAt,
		plan.ModelCreditUnits,
	))
	if err != nil {
		return nil, fmt.Errorf("create commercial order: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO commercial_order_request_ids(uid, client_request_id, order_no)
		VALUES ($1, $2, $3)`, order.UID, order.ClientRequestID, created.OrderNo); err != nil {
		return nil, fmt.Errorf("save commercial order request id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit commercial order: %w", err)
	}
	return created, nil
}

func commercialPlanHasPositiveBudget(plan *types.CommercialPlan) bool {
	if plan == nil {
		return false
	}
	if plan.MonthlyBudget > 0 {
		return true
	}
	for _, amount := range plan.ModelBudgets {
		if amount > 0 {
			return true
		}
	}
	return false
}

func commercialPlanHasPositiveModelBudget(plan *types.CommercialPlan) bool {
	if plan == nil {
		return false
	}
	for model, amount := range plan.ModelBudgets {
		if strings.TrimSpace(model) != "" && strings.TrimSpace(model) != "*" && amount > 0 {
			return true
		}
	}
	return false
}

func (a *Adapter) BeginCommercialOrderPayment(orderNo string, expiresAt time.Time) (*types.CommercialOrder, bool, error) {
	order, err := scanCommercialOrder(a.db.QueryRow(`
		UPDATE commercial_orders
		SET status = 'pending', checkout_url = '', expires_at = $2, closed_at = NULL, last_error = ''
		WHERE order_no = $1 AND (
			status IN ('created','failed') OR
			(status = 'pending' AND checkout_url = '' AND updated_at <= CURRENT_TIMESTAMP - INTERVAL '30 seconds')
		)
		RETURNING `+commercialOrderColumns, strings.TrimSpace(orderNo), expiresAt.UTC()))
	if err == nil {
		return order, true, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, fmt.Errorf("begin commercial payment: %w", err)
	}
	order, err = a.GetCommercialOrder(0, orderNo)
	if err != nil {
		return nil, false, err
	}
	if order == nil {
		return nil, false, fmt.Errorf("commercial order not found")
	}
	return order, false, nil
}

func (a *Adapter) SetCommercialOrderPaymentIntent(orderNo, checkoutURL string, expiresAt time.Time) (*types.CommercialOrder, error) {
	order, err := scanCommercialOrder(a.db.QueryRow(`
		UPDATE commercial_orders
		SET status = 'pending', checkout_url = $2, expires_at = $3, last_error = ''
		WHERE order_no = $1 AND status = 'pending'
		RETURNING `+commercialOrderColumns, strings.TrimSpace(orderNo), strings.TrimSpace(checkoutURL), expiresAt.UTC()))
	if err == sql.ErrNoRows {
		return a.GetCommercialOrder(0, orderNo)
	}
	if err != nil {
		return nil, fmt.Errorf("save commercial payment intent: %w", err)
	}
	return order, nil
}

func (a *Adapter) FailCommercialOrder(orderNo, message string) error {
	_, err := a.db.Exec(`
		UPDATE commercial_orders
		SET status = 'failed', last_error = $2
		WHERE order_no = $1 AND status IN ('created','pending')`, strings.TrimSpace(orderNo), truncateCommercialError(message))
	if err != nil {
		return fmt.Errorf("fail commercial order: %w", err)
	}
	return nil
}

func truncateCommercialError(value string) string {
	value = strings.TrimSpace(value)
	for len(value) > 500 {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return value
}

func (a *Adapter) GetCommercialOrder(uid int64, orderNo string) (*types.CommercialOrder, error) {
	query := `SELECT ` + commercialOrderColumns + ` FROM commercial_orders WHERE order_no = $1`
	args := []interface{}{strings.TrimSpace(orderNo)}
	if uid > 0 {
		query += ` AND uid = $2`
		args = append(args, uid)
	}
	order, err := scanCommercialOrder(a.db.QueryRow(query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get commercial order: %w", err)
	}
	return order, nil
}

func (a *Adapter) ListCommercialOrders(uid int64, limit int) ([]*types.CommercialOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	query := `SELECT ` + commercialOrderColumns + ` FROM commercial_orders`
	args := []interface{}{}
	if uid > 0 {
		query += ` WHERE uid = $1 ORDER BY created_at DESC, id DESC LIMIT $2`
		args = append(args, uid, limit)
	} else {
		query += ` ORDER BY created_at DESC, id DESC LIMIT $1`
		args = append(args, limit)
	}
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list commercial orders: %w", err)
	}
	defer rows.Close()
	var orders []*types.CommercialOrder
	for rows.Next() {
		order, err := scanCommercialOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan commercial order: %w", err)
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

// ListCommercialOrdersForReconciliation returns only recent orders whose
// provider may still report a completed payment. The status/age predicate is
// kept in SQL so the periodic reconciler does not scan the entire order table.
func (a *Adapter) ListCommercialOrdersForReconciliation(limit int) ([]*types.CommercialOrder, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	rows, err := a.db.Query(`
		SELECT `+commercialOrderColumns+` FROM commercial_orders
		WHERE (
			status IN ('created','pending')
			AND created_at >= $1
		) OR (
			status = 'closed' AND closed_at >= $1
		)
		ORDER BY updated_at, id
		LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list commercial orders for reconciliation: %w", err)
	}
	defer rows.Close()
	orders := make([]*types.CommercialOrder, 0, limit)
	for rows.Next() {
		order, scanErr := scanCommercialOrder(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan commercial reconciliation order: %w", scanErr)
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read commercial reconciliation orders: %w", err)
	}
	return orders, nil
}

func (a *Adapter) CancelCommercialOrder(uid int64, orderNo, reason string) (*types.CommercialOrder, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	if uid <= 0 || orderNo == "" {
		return nil, false, fmt.Errorf("invalid commercial order cancellation")
	}
	order, err := scanCommercialOrder(a.db.QueryRow(`
		UPDATE commercial_orders
		SET status = 'closed', closed_at = CURRENT_TIMESTAMP, checkout_url = '', last_error = $3
		WHERE order_no = $1 AND uid = $2 AND status IN ('created','pending','failed')
		RETURNING `+commercialOrderColumns, orderNo, uid, truncateCommercialError(reason)))
	if err == nil {
		return order, true, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, fmt.Errorf("cancel commercial order: %w", err)
	}
	order, err = a.GetCommercialOrder(uid, orderNo)
	if err != nil {
		return nil, false, err
	}
	return order, false, nil
}

func (a *Adapter) CloseExpiredCommercialOrders(limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result, err := a.db.Exec(`
		WITH expired AS (
			SELECT id FROM commercial_orders
			WHERE status IN ('created','pending') AND expires_at <= CURRENT_TIMESTAMP
			ORDER BY expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE commercial_orders o
		SET status = 'closed', closed_at = CURRENT_TIMESTAMP, checkout_url = ''
		FROM expired
		WHERE o.id = expired.id`, limit)
	if err != nil {
		return 0, fmt.Errorf("close expired commercial orders: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read expired commercial order count: %w", err)
	}
	return count, nil
}

// commercialPaidChainEnd returns the end of the continuous paid window of one
// plan tier that starts at `now`. It walks every still-live period of that tier,
// not just the one covering `now`: stacked early renewals therefore accumulate
// (period 1 → period 2 → period 3) instead of each new purchase restarting from
// the first expiry, which used to create overlapping entitlements and a second
// allowance in the same month. The tier — not the plan row or its slug — is what
// "the same product" means, so a re-listed package on the same rung renews the
// Only finite billing terms join the chain, including an audited invite
// repair whose billing end is independent of its model-access entitlement.
func commercialPaidChainEnd(tx *sql.Tx, uid int64, tier int, now time.Time) (time.Time, bool, error) {
	if tier <= 0 {
		return time.Time{}, false, nil
	}
	rows, err := tx.Query(`
		SELECT e.starts_at, `+modelCreditMonthlyTermEndSQL+`
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		LEFT JOIN commercial_invite_monthly_repairs r ON r.entitlement_id = e.id AND e.source = 'invite'
		WHERE e.uid = $1 AND e.state = 'active'
		  AND p.plan_tier = $2
		  AND `+modelCreditMonthlyTermEndSQL+` > $3
		ORDER BY e.starts_at, `+modelCreditMonthlyTermEndSQL+`, e.id
		FOR UPDATE OF e`, uid, tier, now.UTC())
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load commercial paid chain: %w", err)
	}
	defer rows.Close()
	var chainEnd time.Time
	active := false
	for rows.Next() {
		var startsAt, expiresAt time.Time
		if err := rows.Scan(&startsAt, &expiresAt); err != nil {
			return time.Time{}, false, fmt.Errorf("scan commercial paid chain: %w", err)
		}
		if !active {
			if startsAt.After(now) {
				// Periods are ordered by start, so nothing later can cover `now`.
				break
			}
			if !expiresAt.After(now) {
				continue
			}
			active, chainEnd = true, expiresAt
			continue
		}
		if startsAt.After(chainEnd) {
			// A gap ends the chain: the buyer let this tier lapse.
			break
		}
		if expiresAt.After(chainEnd) {
			chainEnd = expiresAt
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("read commercial paid chain: %w", err)
	}
	if !active {
		return time.Time{}, false, nil
	}
	return chainEnd, true, nil
}

// commercialPaidChainAnchor returns the natural-month anchor of the paid window
// the new period continues. Same-tier purchases inherit it even when they renew
// through a different plan row or slug, so the day-of-month and the once-per-
// period allowance survive a package being re-listed under a new id.
func commercialPaidChainAnchor(tx *sql.Tx, uid int64, tier int, paidAt, periodStartsAt time.Time) (time.Time, bool, error) {
	if tier <= 0 {
		return time.Time{}, false, nil
	}
	// A finite chain ends exactly at the new period's start. Without one,
	// only a perpetual entitlement can already cover the payment time.
	var tailEnd *time.Time
	if periodStartsAt.After(paidAt) {
		tailEnd = &periodStartsAt
	}
	var anchor sql.NullTime
	err := tx.QueryRow(`
		SELECT CASE WHEN e.source = 'invite' AND (e.invite_monthly_units > 0 OR r.entitlement_id IS NOT NULL)
		            THEN `+modelCreditMonthlyTermEndSQL+` ELSE COALESCE(e.period_anchor_at, e.starts_at) END
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		LEFT JOIN commercial_invite_monthly_repairs r ON r.entitlement_id = e.id AND e.source = 'invite'
		WHERE e.uid = $1 AND e.state = 'active'
		  AND p.plan_tier = $2
		  AND e.starts_at <= $3
		  AND `+modelCreditMonthlyTermEndSQL+` IS NOT DISTINCT FROM $4
		ORDER BY CASE WHEN e.source IN ('order','admin','internal') THEN 0 ELSE 1 END,
		         e.id DESC
		LIMIT 1`, uid, tier, periodStartsAt.UTC(), tailEnd).Scan(&anchor)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load credit chain period anchor: %w", err)
	}
	if !anchor.Valid {
		return time.Time{}, false, nil
	}
	return anchor.Time, true, nil
}

func (a *Adapter) FulfillCommercialOrder(orderNo string, confirmation *types.CommercialPaymentConfirmation) (*types.CommercialOrder, bool, error) {
	if confirmation == nil || strings.TrimSpace(confirmation.EventID) == "" {
		return nil, false, fmt.Errorf("invalid payment confirmation")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin commercial fulfillment: %w", err)
	}
	defer tx.Rollback()

	order, err := scanCommercialOrder(tx.QueryRow(`SELECT `+commercialOrderColumns+` FROM commercial_orders WHERE order_no = $1 FOR UPDATE`, strings.TrimSpace(orderNo)))
	if err == sql.ErrNoRows {
		return nil, false, fmt.Errorf("commercial order not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock commercial order: %w", err)
	}
	if order.Status == "fulfilled" {
		return order, false, nil
	}
	if order.Status == "refunded" || order.Status == "refunding" {
		return nil, false, fmt.Errorf("commercial order cannot be fulfilled from status %s", order.Status)
	}
	if order.Channel != strings.TrimSpace(confirmation.Channel) {
		return nil, false, fmt.Errorf("payment channel mismatch")
	}
	if order.AmountFen != confirmation.AmountFen || !strings.EqualFold(order.Currency, confirmation.Currency) {
		return nil, false, fmt.Errorf("payment amount mismatch")
	}
	paidAt := confirmation.PaidAt.UTC()
	if paidAt.IsZero() {
		paidAt = time.Now().UTC()
	}
	// Match PostgreSQL timestamp precision before deriving a credit period key.
	if order.PlanModelCreditUnits > 0 && order.Kind != types.CommercialOrderKindCreditTopup {
		paidAt = paidAt.Truncate(time.Microsecond)
	}
	// A credit subscription is only ever sold as a 30-day monthly product, and the
	// order snapshot decides — not the live plan row — so an order taken before
	// this rule existed (or before an operator edited the plan) fails here instead
	// of being fulfilled as a shorter term than it advertised.
	if order.Kind != types.CommercialOrderKindCreditTopup && order.PlanModelCreditUnits > 0 && order.PlanDurationDays != commercialCreditSubscriptionDurationDays {
		return nil, false, fmt.Errorf("credit subscription order %s was sold for %d days, want a %d-day monthly product",
			order.OrderNo, order.PlanDurationDays, commercialCreditSubscriptionDurationDays)
	}
	result, err := tx.Exec(`
		INSERT INTO commercial_payment_events(channel, event_id, order_no, provider_trade_no, event_type, payload_hash, status)
		VALUES ($1, $2, $3, $4, 'payment_success', $5, 'processed')
		ON CONFLICT(channel, event_id) DO NOTHING`,
		confirmation.Channel,
		confirmation.EventID,
		order.OrderNo,
		strings.TrimSpace(confirmation.ProviderTradeNo),
		strings.TrimSpace(confirmation.PayloadHash),
	)
	if err != nil {
		return nil, false, fmt.Errorf("record payment event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read payment event result: %w", err)
	}
	if affected == 0 {
		return nil, false, fmt.Errorf("payment event was already used")
	}
	// A top-up sells ledger units, not a package: the whole fulfilment is one
	// idempotent booking into the paid bucket. No entitlement, no quota grant,
	// no trial, no machine, and nothing that could move an existing package
	// window — that separation is the product, not an optimisation.
	if order.Kind == types.CommercialOrderKindCreditTopup {
		return a.fulfillCommercialTopupOrder(tx, order, paidAt)
	}
	// Serialise the same buyer's fulfilments before reading the paid chain.
	// CommercialPaidChainEnd walks the tier's live periods, and two orders of one
	// uid could otherwise read the same chain tail: a first purchase has no
	// entitlement row to lock at all, and even with rows present the
	// SELECT ... FOR UPDATE inside the chain query works from a snapshot taken
	// before the other transaction committed its next segment, so each order
	// would append its month on top of the *previously* stored expiry. Taking the
	// tier advisory lock here (transaction-scoped, released on commit, and
	// re-entrant so activateCommercialOfficialPlan may take it again) makes the
	// waiting transaction run its chain query in a fresh statement snapshot that
	// sees the committed segment.
	if err := lockCommercialOfficialPlanTier(tx, order.UID); err != nil {
		return nil, false, err
	}
	// A purchase of the tier the buyer already holds is a renewal: the new period
	// is appended after that tier's whole continuous paid chain (stacked early
	// renewals accumulate instead of overlapping each other), and that holds when
	// the buyer renews through a different plan row or slug on the same rung — the
	// rung, not the slug or the plan id, is what "the same product" means.
	// Upgrades still start immediately at payment time; a lapsed tier has no
	// active period and also starts now.
	periodStartsAt := paidAt
	orderTier, tierErr := planTierForSlug(tx, order.PlanSlug)
	if tierErr != nil {
		return nil, false, tierErr
	}
	if orderTier > 0 {
		if chainEnd, active, chainErr := commercialPaidChainEnd(tx, order.UID, orderTier, paidAt); chainErr != nil {
			return nil, false, chainErr
		} else if active && chainEnd.After(periodStartsAt) {
			periodStartsAt = chainEnd
		}
	}
	if err := activateCommercialOfficialPlan(tx, order.UID, order.PlanSlug, paidAt); err != nil {
		return nil, false, err
	}

	// A credit package bills in anchored natural months (docs/credit-plan-rules
	// P0-5): the paid window ends at the next month boundary of the subscription's
	// original anchor, so an early renewal only extends it (the next period's
	// allowance is granted when that boundary is reached, never on payment) and
	// 1/31 → 2/28 → 3/31 never drifts to the 28th. Legacy budget packages keep the
	// fixed duration they were sold with.
	expiresAt := periodStartsAt.AddDate(0, 0, order.PlanDurationDays)
	var entitlementAnchor any
	if order.PlanModelCreditUnits > 0 {
		// The natural-month anchor follows the paid window this period continues,
		// including a renewal through a different plan row on the same rung: that
		// is what keeps one allowance per period and 1/31 → 2/28 → 3/31 intact.
		// Re-anchoring on the payment day is exactly what shortened the window and
		// granted a second allowance inside the month that was already paid for.
		// An invited first term has its own finite expiry (including any
		// machine-aligned boundary). The first paid term anchors at its start,
		// not at the original redemption, so it always buys a full month.
		anchor := paidAt
		if orderTier > 0 {
			if existing, ok, err := commercialPaidChainAnchor(tx, order.UID, orderTier, paidAt, periodStartsAt); err != nil {
				return nil, false, err
			} else if ok {
				anchor = existing
			}
		} else if existing, ok, err := activeCommercialCreditPackageAnchor(tx, order.UID, order.PlanID, paidAt); err != nil {
			return nil, false, err
		} else if ok {
			anchor = existing
		}
		if boundary, ok := store.ModelCreditMonthlyNextBoundary(anchor, periodStartsAt); ok {
			expiresAt = boundary
		}
		entitlementAnchor = anchor
	}
	if _, err := tx.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at, period_anchor_at)
		VALUES ($1, $2, 'order', $3, 'active', $4, $5, $6)`, order.UID, order.PlanID, order.OrderNo, periodStartsAt, expiresAt, entitlementAnchor); err != nil {
		return nil, false, fmt.Errorf("create order entitlement: %w", err)
	}
	budgets := order.PlanModelBudgets
	if budgets[types.GPT62Sol] > 0 && types.GPT62SolLegacyPlan(order.PlanSlug) && order.PlanModelCreditUnits == 0 {
		var active bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM commercial_plans WHERE id = $1 AND slug = $2
			AND state = 0 AND archived_at IS NULL AND model_credit_units = 0 AND model_budgets ? $3)`, order.PlanID, order.PlanSlug, types.GPT62Sol).Scan(&active); err != nil {
			return nil, false, fmt.Errorf("check restricted order model: %w", err)
		}
		budgets = a.gpt62GrantBudgets(budgets, order.PlanSlug, active)
	} else {
		budgets = filterGPT62GrantBudgets(budgets)
	}
	if err := createCommercialPlanGrants(tx, order.UID, order.PlanID, 0, "order", order.OrderNo, order.PlanName, order.PlanMonthlyBudget, budgets, periodStartsAt, expiresAt); err != nil {
		return nil, false, err
	}
	// A credit package sells a monthly allowance, not a lump of spendable paid
	// balance: the period that starts at payment is booked here, in the very
	// same transaction, by the reconciler the sweeper and the read paths use.
	// That is what holds a subscription to one grant per natural month
	// (P0-2/P0-5). Booking the amount into the paid bucket as well gave every
	// new buyer the same units twice, and turned an early renewal into an
	// immediate top-up. A renewal period starts at the previous expiry (see
	// above), so reconciling at payment time grants nothing for it; the next
	// period's allowance is granted when that period begins. Legacy budget
	// packages snapshot 0 units and stay off the ledger entirely.
	if order.PlanModelCreditUnits > 0 {
		if _, _, err := reconcileModelCreditMonthlyPeriod(context.Background(), tx, order.UID, paidAt); err != nil {
			return nil, false, err
		}
	}
	// Whether this package buys a machine is a plan attribute, not a slug list.
	// Keeping the allow-list here is what made every new package token-only until
	// someone edited Go.
	includesWorker, err := planIncludesCloudWorker(tx, order.PlanID)
	if err != nil {
		return nil, false, err
	}
	if includesWorker {
		profile, err := planCloudWorkerDeploymentProfile(tx, order.PlanID)
		if err != nil {
			return nil, false, err
		}
		// Retention follows the purchased package (7d → 3 days, longer → 7).
		retention, err := planCloudWorkerRetentionDays(tx, order.PlanID, order.PlanDurationDays)
		if err != nil {
			return nil, false, err
		}
		// A renewal must not mint a second provisioning credit while the buyer
		// still owns something it can extend or spend: an existing instance (any
		// lifecycle that has not been destroyed yet keeps the single machine the
		// package covers) is renewed further down, and an unspent credit already
		// is the ticket for the next creation. Only a buyer with neither — a
		// first purchase, or a worker whose destroy already started and cannot
		// be recovered — receives a fresh credit, which the sweep rebuild hook
		// then spends on a replacement instance.
		survives, err := cloudWorkerSurvivesRenewal(tx, order.UID, profile)
		if err != nil {
			return nil, false, err
		}
		if !survives {
			// The slot size travels with the credit, not with the create call: a
			// credit can outlive the plan row it came from (a retired package is
			// archived while paid credits stay spendable).
			runtimePlan, err := planCloudWorkerRuntimePlan(tx, order.PlanID)
			if err != nil {
				return nil, false, err
			}
			// A shared worker bought by a package follows that package: the term is
			// not a separate product an operator has to remember to set, and a
			// contract that outlives the package is exactly the drift this column
			// removes. Dedicated ECS keeps the historical derivation (empty mode).
			expiryMode := ""
			if profile == types.CloudWorkerPackedShared {
				expiryMode = types.CloudWorkerExpiryFollowPlan
			}
			// The retention intent comes from the order's own snapshot, never from
			// the current plan row: a plan can be edited or retired while paid
			// orders and their credits stay live. plan_model_credit_units > 0 is
			// exactly "the buyer paid with model credit", so the environment this
			// credit provisions keeps its files and history after the window ends
			// (approved credit rules 2026-11-09, P1-2). A legacy budget package
			// snapshots 0 and keeps its historical cleanup schedule.
			retainData := order.PlanModelCreditUnits > 0
			if err := grantCloudWorkerCredit(tx, order.UID, "order:"+order.OrderNo, expiresAt, profile, retention, runtimePlan, expiryMode, retainData); err != nil {
				return nil, false, err
			}
		}
		// If the user already has a cloud worker, a renewal/upgrade extends the
		// retention window atomically with the paid entitlement. The worker is
		// not deleted at the old expiry while a new monthly package is active.
		if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, order.UID, &expiresAt, retention, profile); err != nil {
			return nil, false, err
		}
	}
	// Whatever this purchase did - renew the same package, upgrade from a lower
	// tier, or buy a token-only package that revoked a machine tier - a
	// follow_plan worker now follows the package the buyer really holds. The
	// extend above keeps a longer remaining window when the package only grows;
	// this settles it against what actually survived, so a revoked or superseded
	// window cannot outlive it.
	if err := settleSharedCloudWorkerFollowPlanTerms(tx, order.UID, paidAt); err != nil {
		return nil, false, err
	}
	fulfilled, err := scanCommercialOrder(tx.QueryRow(`
		UPDATE commercial_orders
		SET status = 'fulfilled', provider_trade_no = $2, paid_at = $3, fulfilled_at = CURRENT_TIMESTAMP,
		    checkout_url = '', last_error = ''
		WHERE order_no = $1
		RETURNING `+commercialOrderColumns, order.OrderNo, strings.TrimSpace(confirmation.ProviderTradeNo), paidAt))
	if err != nil {
		return nil, false, fmt.Errorf("complete commercial order: %w", err)
	}
	// Hand the plan's "includes a machine" decision to the caller. The payment
	// handler cannot look the plan up itself (it holds no plan query), and
	// re-deriving it from the slug is exactly the coupling this column removed.
	// It is a one-shot signal on the returned struct, not a stored fact.
	fulfilled.PlanIncludesWorker = includesWorker
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit commercial fulfillment: %w", err)
	}
	return fulfilled, true, nil
}

// cloudWorkerSurvivesRenewal reports whether a paid order already has a cloud
// worker in the same deployment class to renew, or a matching provisioning
// credit to spend. Dedicated and shared workers are separate entitlements.
func cloudWorkerSurvivesRenewal(tx *sql.Tx, uid int64, profile string) (bool, error) {
	profile, ok := types.NormalizeCloudWorkerProfile(profile)
	if !ok {
		return false, fmt.Errorf("invalid cloud worker deployment profile %q", profile)
	}
	var survives bool
	if err := tx.QueryRow(`
        SELECT EXISTS (
            SELECT 1 FROM cloud_worker_lifecycles l
            WHERE l.owner_uid = $1 AND l.state NOT IN ('deleted', 'delete_running')
              AND (($2 = 'packed_shared' AND `+cloudWorkerSharedProfileFilter+`)
                   OR ($2 <> 'packed_shared' AND NOT (`+cloudWorkerSharedProfileFilter+`)))
        ) OR EXISTS (
            SELECT 1 FROM cloud_worker_credits
            WHERE uid = $1 AND state IN ('available', 'reserved')
              AND source_ref NOT LIKE 'ops:%'
              AND (($2 = 'packed_shared' AND deployment_profile = 'packed_shared')
                   OR ($2 <> 'packed_shared' AND deployment_profile <> 'packed_shared'))
              AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
        )`, uid, profile).Scan(&survives); err != nil {
		return false, fmt.Errorf("check cloud worker renewal target: %w", err)
	}
	return survives, nil
}

// BeginCommercialOrderRefund claims one order for a refund and freezes the paid
// credit that claim may have to reverse, in one transaction that holds both the
// order row and the buyer's credit account row.
//
// The order of operations is the whole point. The claim used to flip the status
// to 'refunding' and call the provider, and only then tried to revoke the credit;
// when that revocation refused (credit already spent, still held by a model call,
// ...), the failure path put the order back to 'fulfilled' while the money had
// already left. Now nothing can reach the provider until the credit is provably
// reversible:
//   - the paid units the order sold are moved out of the spendable bucket into
//     commercial_refund_holds, with a reserve event in the paid ledger;
//   - any consumption, open model-call reservation, open machine-upgrade hold or
//     arrears repayment of that credit refuses the claim here, before the call;
//   - the claim is deterministic (CCRF-<orderNo>) and the hold is created once,
//     so a stale retry reuses it instead of freezing a second time.
//
// An order that is already 'refunding' without a hold was claimed by a build that
// refunded first. The provider may have paid the money out, so that state fails
// closed and is left to an operator instead of being replayed.
func (a *Adapter) BeginCommercialOrderRefund(orderNo, refundRequestNo string, staleAfter time.Duration) (*types.CommercialOrder, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	refundRequestNo = strings.TrimSpace(refundRequestNo)
	if orderNo == "" || refundRequestNo == "" {
		return nil, false, fmt.Errorf("invalid commercial refund claim")
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	ctx := context.Background()
	now := time.Now().UTC()
	staleBefore := now.Add(-staleAfter)
	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin commercial refund claim: %w", err)
	}
	defer tx.Rollback()

	order, err := scanCommercialOrder(tx.QueryRow(`SELECT `+commercialOrderColumns+`
		FROM commercial_orders WHERE order_no = $1 FOR UPDATE`, orderNo))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock commercial refund order: %w", err)
	}
	switch order.Status {
	case "refunded":
		return order, false, nil
	case "fulfilled":
		if _, err := freezeCommercialOrderRefundHold(ctx, tx, order, refundRequestNo, now); err != nil {
			return order, false, err
		}
		claimed, err := scanCommercialOrder(tx.QueryRow(`
			UPDATE commercial_orders
			SET status = 'refunding', refund_request_no = $2, last_error = ''
			WHERE order_no = $1
			RETURNING `+commercialOrderColumns, orderNo, refundRequestNo))
		if err != nil {
			return nil, false, fmt.Errorf("claim commercial refund: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit commercial refund claim: %w", err)
		}
		return claimed, true, nil
	case "refunding":
		hold, err := readCommercialRefundHold(ctx, tx, order.OrderNo, true)
		if err != nil {
			return order, false, err
		}
		if hold == nil {
			return order, false, fmt.Errorf("%w: order %s is already refunding without a local hold; check the payment channel before retrying",
				errCommercialRefundHoldMissing, order.OrderNo)
		}
		if hold.RefundRequestNo != refundRequestNo || hold.State != commercialRefundHoldHeld {
			return order, false, fmt.Errorf("%w: order %s holds claim %q in state %q",
				errCommercialRefundHoldMismatch, order.OrderNo, hold.RefundRequestNo, hold.State)
		}
		if !order.UpdatedAt.Before(staleBefore) {
			// Another attempt claimed the order within the stale window; keep
			// its claim and its frozen credit untouched.
			return order, false, nil
		}
		claimed, err := scanCommercialOrder(tx.QueryRow(`
			UPDATE commercial_orders
			SET refund_request_no = $2, last_error = '', updated_at = $3
			WHERE order_no = $1
			RETURNING `+commercialOrderColumns, orderNo, refundRequestNo, now))
		if err != nil {
			return nil, false, fmt.Errorf("refresh commercial refund claim: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit commercial refund claim: %w", err)
		}
		return claimed, true, nil
	default:
		return order, false, nil
	}
}

// FailCommercialOrderRefund records why a refund attempt could not finish and
// deliberately changes nothing else.
//
// It used to put the order back to 'fulfilled', which is exactly the bug: a
// provider timeout, an HTTP 5xx, a crash between the call and the local commit
// and a provider that really refunded all look identical from here, so restoring
// 'fulfilled' sells the buyer credit that the platform already paid back. The
// claim, the 'refunding' status and the frozen credit stay until the refund is
// completed by a retry (the provider request number is deterministic) or by an
// operator who checked the channel.
func (a *Adapter) FailCommercialOrderRefund(orderNo, refundRequestNo, message string) error {
	if _, err := a.db.Exec(`
		UPDATE commercial_orders
		SET last_error = $3
		WHERE order_no = $1 AND status = 'refunding' AND refund_request_no = $2`,
		strings.TrimSpace(orderNo), strings.TrimSpace(refundRequestNo),
		truncateCommercialError(message)); err != nil {
		return fmt.Errorf("record failed commercial refund: %w", err)
	}
	return nil
}

func (a *Adapter) CompleteCommercialOrderRefund(orderNo string, confirmation *types.CommercialRefundConfirmation) (*types.CommercialOrder, bool, error) {
	if confirmation == nil || strings.TrimSpace(confirmation.EventID) == "" || strings.TrimSpace(confirmation.RefundRequestNo) == "" {
		return nil, false, fmt.Errorf("invalid refund confirmation")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin commercial refund completion: %w", err)
	}
	defer tx.Rollback()

	order, err := scanCommercialOrder(tx.QueryRow(`SELECT `+commercialOrderColumns+` FROM commercial_orders WHERE order_no = $1 FOR UPDATE`, strings.TrimSpace(orderNo)))
	if err == sql.ErrNoRows {
		return nil, false, fmt.Errorf("commercial order not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock commercial refund order: %w", err)
	}
	if order.Status == "refunded" {
		return order, false, nil
	}
	if order.Status != "refunding" || order.RefundRequestNo != strings.TrimSpace(confirmation.RefundRequestNo) {
		return nil, false, fmt.Errorf("commercial refund claim mismatch")
	}
	if order.Channel != strings.TrimSpace(confirmation.Channel) || order.AmountFen != confirmation.AmountFen || !strings.EqualFold(order.Currency, confirmation.Currency) {
		return nil, false, fmt.Errorf("commercial refund payment mismatch")
	}
	if strings.TrimSpace(order.ProviderTradeNo) != "" && strings.TrimSpace(order.ProviderTradeNo) != strings.TrimSpace(confirmation.ProviderTradeNo) {
		return nil, false, fmt.Errorf("commercial refund trade mismatch")
	}
	// The hold is read before anything is written: a 'refunding' order without
	// one has an unknown provider outcome, so no local refund event may be
	// recorded for it, and a consumed hold means the reversal is already booked.
	ctx := context.Background()
	hold, err := readCommercialRefundHold(ctx, tx, order.OrderNo, true)
	if err != nil {
		return nil, false, err
	}
	if hold == nil {
		return nil, false, fmt.Errorf("%w: order %s is refunding without a local hold; check the payment channel before completing",
			errCommercialRefundHoldMissing, order.OrderNo)
	}
	if hold.State != commercialRefundHoldHeld || hold.RefundRequestNo != strings.TrimSpace(confirmation.RefundRequestNo) {
		return nil, false, fmt.Errorf("%w: order %s holds claim %q in state %q",
			errCommercialRefundHoldMismatch, order.OrderNo, hold.RefundRequestNo, hold.State)
	}
	refundedAt := confirmation.RefundedAt.UTC()
	if refundedAt.IsZero() {
		refundedAt = time.Now().UTC()
	}
	result, err := tx.Exec(`
		INSERT INTO commercial_payment_events(channel, event_id, order_no, provider_trade_no, event_type, payload_hash, status)
		VALUES ($1, $2, $3, $4, 'refund_success', $5, 'processed')
		ON CONFLICT(channel, event_id) DO NOTHING`,
		confirmation.Channel,
		confirmation.EventID,
		order.OrderNo,
		strings.TrimSpace(confirmation.ProviderTradeNo),
		strings.TrimSpace(confirmation.PayloadHash),
	)
	if err != nil {
		return nil, false, fmt.Errorf("record refund event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read refund event result: %w", err)
	}
	if affected == 0 {
		return nil, false, fmt.Errorf("refund event was already used")
	}
	// The frozen credit is consumed here, not re-checked. Its units already left
	// the spendable paid bucket when the claim was taken, so a model call that ran
	// while the provider was being called can only have spent other units: the
	// refund finishes instead of getting stuck on a balance it should no longer
	// care about, and the buyer cannot be paid out twice for the same units.
	bookedPaidCredit, err := commercialOrderCreditGrantBooked(ctx, tx, order.OrderNo)
	if err != nil {
		return nil, false, err
	}
	if bookedPaidCredit && order.PlanModelCreditUnits > 0 && hold.Units != order.PlanModelCreditUnits {
		return nil, false, fmt.Errorf("%w: order %s froze %d units but booked %d",
			errCommercialRefundHoldMismatch, order.OrderNo, hold.Units, order.PlanModelCreditUnits)
	}
	if err := consumeCommercialRefundHold(ctx, tx, order, hold, refundedAt); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`
		UPDATE commercial_entitlements
		SET state = 'revoked'
		WHERE uid = $1 AND source = 'order' AND source_ref = $2 AND state <> 'revoked'`, order.UID, order.OrderNo); err != nil {
		return nil, false, fmt.Errorf("revoke refunded entitlement: %w", err)
	}
	if order.PlanModelCreditUnits > 0 {
		// The monthly balance can contain multiple orders. Revoke only this
		// order's lots, including frozen units and any outstanding holds.
		if err := revokeOrderModelCreditMonthlyLots(ctx, tx, order.UID, order.OrderNo, refundedAt); err != nil {
			return nil, false, err
		}
		if _, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, order.UID, refundedAt); err != nil {
			return nil, false, err
		}
	}
	grantRows, err := tx.Query(`
		SELECT id, model, amount_cny
		FROM commercial_quota_grants
		WHERE uid = $1 AND grant_type = 'order' AND source_ref = $2 AND revoked_at IS NULL
		FOR UPDATE`, order.UID, order.OrderNo)
	if err != nil {
		return nil, false, fmt.Errorf("lock refunded quota grants: %w", err)
	}
	type refundGrant struct {
		id     int64
		model  string
		amount float64
	}
	var grants []refundGrant
	for grantRows.Next() {
		var grant refundGrant
		if err := grantRows.Scan(&grant.id, &grant.model, &grant.amount); err != nil {
			grantRows.Close()
			return nil, false, fmt.Errorf("scan refunded quota grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := grantRows.Close(); err != nil {
		return nil, false, fmt.Errorf("close refunded quota grants: %w", err)
	}
	for _, grant := range grants {
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'revoke', 'refund', $4, $5)
			ON CONFLICT DO NOTHING`, order.UID, grant.model, -grant.amount, grant.id, "refund "+order.OrderNo); err != nil {
			return nil, false, fmt.Errorf("record refunded quota reversal: %w", err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE commercial_quota_grants
		SET revoked_at = $3, expires_at = LEAST(COALESCE(expires_at, $3), $3)
		WHERE uid = $1 AND grant_type = 'order' AND source_ref = $2 AND revoked_at IS NULL`, order.UID, order.OrderNo, refundedAt); err != nil {
		return nil, false, fmt.Errorf("revoke refunded quota grants: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'revoked', reservation_ref = '', reserved_at = NULL
		WHERE uid = $1 AND source_ref = $2 AND state IN ('available','reserved','consumed')`, order.UID, "order:"+order.OrderNo); err != nil {
		return nil, false, fmt.Errorf("revoke refunded cloud worker credit: %w", err)
	}
	// A top-up refund is a credit reversal, nothing more: it must not reschedule
	// a machine the buyer also owns, because no package window moved for it.
	if order.Kind != types.CommercialOrderKindCreditTopup {
		// Cloud workers follow the paid window: after a refund the owner is rolled
		// back to whatever paid time survives, or the worker is scheduled for
		// cleanup when nothing remains. A refunded trial must not auto-convert to
		// a monthly subscription the platform would pay for.
		var remainingPaid sql.NullTime
		if err := tx.QueryRow(`
		SELECT MAX(expires_at) FROM commercial_entitlements
		WHERE uid = $1 AND state = 'active' AND expires_at > CURRENT_TIMESTAMP`, order.UID).Scan(&remainingPaid); err != nil {
			return nil, false, fmt.Errorf("check remaining paid time after refund: %w", err)
		}
		rollbackUntil := refundedAt
		if remainingPaid.Valid {
			rollbackUntil = remainingPaid.Time
		}
		// The retention window is the plan's own (7-day package → 3 days, 30-day →
		// 7 days), not a fixed 15. A refund that lands the worker past its paid
		// window used to set delete_after = refundedAt, so the sweeper destroyed
		// and purged the instance within a couple of minutes of the refund — no
		// window at all for a mistaken refund or a support follow-up. Rows created
		// before the column existed carry 0, so fall back to the shortest published
		// window instead of zero.
		if _, err := tx.Exec(`
		UPDATE cloud_worker_lifecycles l
		SET package_expires_at = $2::timestamptz,
		    delete_after = $2::timestamptz + (GREATEST(NULLIF(l.retention_days, 0), $3::int) * INTERVAL '1 day'),
		    conversion_pending = false,
		    -- The remaining package window is what the worker now follows, so the
		    -- provider has to be told before another sweep may destroy it.
		    sync_pending = CASE WHEN l.expiry_mode IN ('legacy','follow_plan') AND `+cloudWorkerSharedProfileFilter+` THEN TRUE ELSE l.sync_pending END,
		    updated_at = CURRENT_TIMESTAMP
		-- A refund rolls the owner back to the paid time that survives it (or to
		-- a finite window ending now). A shared worker on its own fixed term, or a
		-- permanent grant, is not part of any package: refunding a package must not
		-- reschedule it, and a permanent grant must not acquire an expiry because
		-- somebody else's order was refunded. A follow_plan worker is resolved from
		-- the surviving package below instead of from this blanket rollback: it may
		-- still be following a perpetual package, which this one update cannot
		-- express without inventing a date.
		WHERE l.owner_uid = $1 AND l.state IN ('active','delete_pending','delete_failed')
		  AND NOT (`+cloudWorkerSharedProfileFilter+` AND l.expiry_mode IN ('fixed','permanent','follow_plan'))`,
			order.UID, rollbackUntil, commercialRefundMinRetentionDays); err != nil {
			return nil, false, fmt.Errorf("roll back cloud worker lifecycles after refund: %w", err)
		}
	}
	// The refunded entitlement is already revoked at this point, so this settles
	// follow_plan workers on what the buyer really still holds: the surviving
	// package's window, a perpetual one (no deadline), or — when nothing is left —
	// expiry now plus a pending sync, so the worker is paused and collected
	// instead of staying open-ended on an entitlement that no longer exists.
	if err := settleSharedCloudWorkerFollowPlanTerms(tx, order.UID, refundedAt); err != nil {
		return nil, false, err
	}
	refunded, err := scanCommercialOrder(tx.QueryRow(`
		UPDATE commercial_orders
		SET status = 'refunded', refunded_at = $2, checkout_url = '', last_error = ''
		WHERE order_no = $1
		RETURNING `+commercialOrderColumns, order.OrderNo, refundedAt))
	if err != nil {
		return nil, false, fmt.Errorf("complete refunded commercial order: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit commercial refund: %w", err)
	}
	return refunded, true, nil
}

func createCommercialPlanGrants(tx *sql.Tx, uid, planID, inviteID int64, source, sourceRef, planName string, monthlyBudget float64, budgets map[string]float64, startsAt, expiresAt time.Time) error {
	modelBudgets := map[string]float64{}
	for model, amount := range budgets {
		modelBudgets[model] = amount
	}
	if monthlyBudget > 0 {
		modelBudgets["*"] = monthlyBudget
	}
	for model, amount := range modelBudgets {
		model = strings.TrimSpace(model)
		if model == "" || amount <= 0 {
			continue
		}
		var grantID int64
		if err := tx.QueryRow(`
			INSERT INTO commercial_quota_grants(uid, plan_id, invite_code_id, grant_type, model, amount_cny, reset_duration, effective_at, expires_at, source_ref, note)
			VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, '1M', $7, $8, $9, $10)
			RETURNING id`, uid, planID, inviteID, source, model, amount, startsAt, expiresAt, sourceRef, source+" "+sourceRef).Scan(&grantID); err != nil {
			return fmt.Errorf("create %s quota grant: %w", source, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'grant', $4, $5, $6)`, uid, model, amount, source, grantID, planName); err != nil {
			return fmt.Errorf("create %s quota ledger: %w", source, err)
		}
	}
	return nil
}

func (a *Adapter) ClaimCommercialTrial(uid int64, planSlug string) (*types.CommercialSummary, error) {
	if uid <= 0 || strings.TrimSpace(planSlug) == "" {
		return nil, fmt.Errorf("invalid commercial trial")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin commercial trial: %w", err)
	}
	defer tx.Rollback()
	plan, err := scanCommercialPlan(tx.QueryRow(`SELECT `+commercialPlanColumns+` FROM commercial_plans WHERE slug = $1 AND state = 0 FOR SHARE`, strings.TrimSpace(planSlug)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("trial plan is unavailable")
	}
	if err != nil {
		return nil, fmt.Errorf("load trial plan: %w", err)
	}
	if plan.PriceFen != 0 || plan.SaleState != "hidden" || plan.DurationDays <= 0 || !commercialPlanHasQuota(plan) {
		return nil, fmt.Errorf("trial plan is unavailable")
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid = $1 AND source = 'trial'`, uid).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check commercial trial: %w", err)
	}
	if exists > 0 {
		return nil, fmt.Errorf("trial package already claimed")
	}
	startsAt := time.Now().UTC()
	expiresAt := startsAt.AddDate(0, 0, plan.DurationDays)
	result, err := tx.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'trial', $3, 'active', $4, $5)
		ON CONFLICT DO NOTHING`, uid, plan.ID, plan.Slug, startsAt, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("create trial entitlement: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read trial entitlement result: %w", err)
	}
	if affected == 0 {
		return nil, fmt.Errorf("trial package already claimed")
	}
	if err := createCommercialPlanGrants(tx, uid, plan.ID, 0, "trial", plan.Slug, plan.Name, plan.MonthlyBudget, filterGPT62GrantBudgets(plan.ModelBudgets), startsAt, expiresAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit commercial trial: %w", err)
	}
	return a.GetCommercialSummary(uid)
}

func commercialPlanHasQuota(plan *types.CommercialPlan) bool {
	if plan == nil {
		return false
	}
	if plan.MonthlyBudget > 0 {
		return true
	}
	for _, amount := range plan.ModelBudgets {
		if amount > 0 {
			return true
		}
	}
	return false
}

func (a *Adapter) HasCommercialTrial(uid int64, _ string) (bool, error) {
	var exists bool
	if err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM commercial_entitlements WHERE uid = $1 AND source = 'trial')`, uid).Scan(&exists); err != nil {
		return false, fmt.Errorf("check commercial trial: %w", err)
	}
	return exists, nil
}

func (a *Adapter) ListCommercialManagedRelayBudgets(uid int64) ([]*types.CommercialManagedRelayBudget, error) {
	rows, err := a.db.Query(`
		SELECT uid, model, provider, allowed_models, max_limit, reset_duration, updated_at
		FROM commercial_managed_relay_budgets WHERE uid = $1
		ORDER BY model, provider`, uid)
	if err != nil {
		return nil, fmt.Errorf("list managed relay budgets: %w", err)
	}
	defer rows.Close()
	var budgets []*types.CommercialManagedRelayBudget
	for rows.Next() {
		var item types.CommercialManagedRelayBudget
		var allowedRaw []byte
		if err := rows.Scan(&item.UID, &item.Model, &item.Provider, &allowedRaw, &item.MaxLimit, &item.ResetDuration, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan managed relay budget: %w", err)
		}
		_ = json.Unmarshal(allowedRaw, &item.AllowedModels)
		budgets = append(budgets, &item)
	}
	return budgets, rows.Err()
}

func (a *Adapter) ReplaceCommercialManagedRelayBudgets(uid int64, budgets []*types.CommercialManagedRelayBudget) error {
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("begin managed relay budget update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM commercial_managed_relay_budgets WHERE uid = $1`, uid); err != nil {
		return fmt.Errorf("clear managed relay budgets: %w", err)
	}
	for _, item := range budgets {
		if item == nil || strings.TrimSpace(item.Model) == "" || strings.TrimSpace(item.Provider) == "" || item.MaxLimit <= 0 {
			continue
		}
		allowed, err := json.Marshal(item.AllowedModels)
		if err != nil {
			return fmt.Errorf("encode managed relay models: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_managed_relay_budgets(uid, model, provider, allowed_models, max_limit, reset_duration)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6)`, uid, item.Model, item.Provider, string(allowed), item.MaxLimit, defaultString(item.ResetDuration, "1M")); err != nil {
			return fmt.Errorf("save managed relay budget: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed relay budget update: %w", err)
	}
	return nil
}

func (a *Adapter) CommercialRelaySyncRequired(uid int64) (bool, error) {
	var required bool
	if err := a.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM commercial_entitlements
			WHERE uid = $1 AND source IN ('order','trial') AND state = 'active'
			  AND starts_at <= CURRENT_TIMESTAMP
			  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		)`, uid).Scan(&required); err != nil {
		return false, fmt.Errorf("check commercial relay entitlement: %w", err)
	}
	return required, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (a *Adapter) ListCommercialReconcileUIDs(afterUID int64, limit int) ([]int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := a.db.Query(`
		SELECT uid FROM (
			SELECT DISTINCT uid FROM commercial_managed_relay_budgets
			UNION
			SELECT DISTINCT uid FROM commercial_quota_grants
			WHERE revoked_at IS NULL AND effective_at <= CURRENT_TIMESTAMP AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		) candidates
		WHERE uid > $1
		ORDER BY uid
		LIMIT $2`, afterUID, limit)
	if err != nil {
		return nil, fmt.Errorf("list commercial reconcile users: %w", err)
	}
	defer rows.Close()
	var uids []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("scan commercial reconcile user: %w", err)
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}
