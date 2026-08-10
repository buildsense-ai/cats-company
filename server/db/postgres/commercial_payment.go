package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

const commercialPlanColumns = `id, slug, name, description, price_fen, currency, sale_state, purchase_limit,
	monthly_budget_cny, model_budgets, internal_quota_tokens, duration_days, state, sort_order, created_at, updated_at`

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
	var expiresAt, paidAt, fulfilledAt, closedAt sql.NullTime
	if err := scanner.Scan(
		&order.ID,
		&order.OrderNo,
		&order.UID,
		&order.PlanID,
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
		&order.LastError,
		&order.CreatedAt,
		&order.UpdatedAt,
	); err != nil {
		return nil, err
	}
	order.PlanModelBudgets = decodeModelBudgets(budgets)
	order.ExpiresAt = nullableTime(expiresAt)
	order.PaidAt = nullableTime(paidAt)
	order.FulfilledAt = nullableTime(fulfilledAt)
	order.ClosedAt = nullableTime(closedAt)
	return &order, nil
}

const commercialOrderColumns = `id, order_no, uid, plan_id, plan_slug, plan_name, plan_description,
	plan_duration_days, plan_monthly_budget_cny, plan_model_budgets, amount_fen, currency, channel,
	status, provider_trade_no, checkout_url, client_request_id, expires_at, paid_at, fulfilled_at,
	closed_at, last_error, created_at, updated_at`

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
	if plan.State != 0 || plan.PriceFen <= 0 || plan.DurationDays <= 0 || plan.MonthlyBudget > 0 || !commercialPlanHasPositiveModelBudget(plan) {
		return nil, fmt.Errorf("commercial plan is not purchasable")
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
			client_request_id, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, 'created', $13, $14)
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

	expiresAt := paidAt.AddDate(0, 0, order.PlanDurationDays)
	if _, err := tx.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', $3, 'active', $4, $5)`, order.UID, order.PlanID, order.OrderNo, paidAt, expiresAt); err != nil {
		return nil, false, fmt.Errorf("create order entitlement: %w", err)
	}
	if err := createCommercialPlanGrants(tx, order.UID, order.PlanID, 0, "order", order.OrderNo, order.PlanName, order.PlanMonthlyBudget, order.PlanModelBudgets, paidAt, expiresAt); err != nil {
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
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit commercial fulfillment: %w", err)
	}
	return fulfilled, true, nil
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
			INSERT INTO commercial_quota_grants(uid, plan_id, invite_code_id, grant_type, model, amount_cny, reset_duration, effective_at, expires_at, note)
			VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, '1M', $7, $8, $9)
			RETURNING id`, uid, planID, inviteID, source, model, amount, startsAt, expiresAt, source+" "+sourceRef).Scan(&grantID); err != nil {
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
	if err := createCommercialPlanGrants(tx, uid, plan.ID, 0, "trial", plan.Slug, plan.Name, plan.MonthlyBudget, plan.ModelBudgets, startsAt, expiresAt); err != nil {
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
			WHERE effective_at <= CURRENT_TIMESTAMP AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
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
