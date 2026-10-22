package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// ListBillableServiceRuns 列出已终态但尚未记账的 run（设计 §4.5）。
// 回看 48h：既覆盖抖动，又不会把历史数据一次扫爆；账本写入是幂等的。
func (a *Adapter) ListBillableServiceRuns(ctx context.Context, limit int) ([]store.ServiceRunBillable, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT r.run_id, r.session_id, s.agent_id, a.slug, a.owner_kind, a.owner_uid,
		       s.caller_uid, s.caller_bot_uid,
		       COALESCE(v.definition_json->'runner'->>'type', 'codex') AS runner_type,
		       r.state,
		       COALESCE((r.usage_json->>'prompt_tokens')::bigint, 0),
		       COALESCE((r.usage_json->>'completion_tokens')::bigint, 0),
		       GREATEST(0, (EXTRACT(EPOCH FROM (r.finished_at - COALESCE(r.started_at, r.created_at))) * 1000)::bigint),
		       COALESCE(v.definition_json, '{}'::jsonb),
		       r.finished_at
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		LEFT JOIN service_agent_versions v ON v.agent_id = a.id AND v.version = s.definition_rev
		LEFT JOIN service_run_ledger l ON l.run_id = r.run_id
		WHERE r.state IN ('completed','failed','awaiting_input')
		  AND r.finished_at IS NOT NULL
		  AND r.finished_at >= now() - interval '48 hours'
		  AND l.run_id IS NULL
		ORDER BY r.finished_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list billable service runs: %w", err)
	}
	defer rows.Close()

	var out []store.ServiceRunBillable
	for rows.Next() {
		var item store.ServiceRunBillable
		var definition []byte
		if err := rows.Scan(&item.RunID, &item.SessionID, &item.AgentID, &item.AgentSlug,
			&item.OwnerKind, &item.OwnerUID, &item.CallerUID, &item.CallerBotUID,
			&item.RunnerType, &item.Status, &item.PromptTokens, &item.CompletionTokens,
			&item.DurationMs, &definition, &item.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan billable service run: %w", err)
		}
		item.DefinitionJSON = json.RawMessage(definition)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate billable service runs: %w", err)
	}
	return out, nil
}

// RecordServiceRunLedger 写入一行账本（run 已记账 → false，幂等）。
func (a *Adapter) RecordServiceRunLedger(ctx context.Context, entry store.ServiceRunLedgerEntry) (bool, error) {
	billingState := entry.BillingState
	if billingState == "" {
		billingState = store.ServiceBillingFree
	}
	result, err := a.db.ExecContext(ctx, `
		INSERT INTO service_run_ledger (
			run_id, session_id, agent_id, agent_slug, owner_kind, owner_uid,
			caller_uid, caller_bot_uid, runner_type, status,
			prompt_tokens, completion_tokens, duration_ms,
			pricing_json, currency, gross_micros, fee_micros, net_micros,
			payer_uid, payee_uid, billing_state, finished_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (run_id) DO NOTHING`,
		entry.RunID, entry.SessionID, entry.AgentID, entry.AgentSlug, entry.OwnerKind, entry.OwnerUID,
		entry.CallerUID, entry.CallerBotUID, entry.RunnerType, entry.Status,
		entry.PromptTokens, entry.CompletionTokens, entry.DurationMs,
		nullJSON(entry.PricingJSON), entry.Currency, entry.GrossMicros, entry.FeeMicros, entry.NetMicros,
		entry.PayerUID, entry.PayeeUID, billingState, entry.FinishedAt)
	if err != nil {
		return false, fmt.Errorf("record service run ledger: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record service run ledger: %w", err)
	}
	return affected > 0, nil
}

func nullJSON(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

var _ = sql.ErrNoRows

// ListPendingServiceRunLedgerCharges 列出待补扣的账本行（billing_state=pending_credit）。
// 与补账扫描同口径回看 48h：只补最近的钱，避免把历史欠账一次性推给 relay。
func (a *Adapter) ListPendingServiceRunLedgerCharges(ctx context.Context, limit int) ([]store.ServiceRunLedgerCharge, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT run_id, payer_uid, payee_uid, gross_micros, fee_micros, currency, finished_at
		FROM service_run_ledger
		WHERE billing_state = 'pending_credit'
		  AND payer_uid > 0
		  AND gross_micros > 0
		  AND finished_at >= now() - interval '48 hours'
		ORDER BY finished_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending ledger charges: %w", err)
	}
	defer rows.Close()

	var out []store.ServiceRunLedgerCharge
	for rows.Next() {
		var item store.ServiceRunLedgerCharge
		if err := rows.Scan(&item.RunID, &item.PayerUID, &item.PayeeUID,
			&item.GrossMicros, &item.FeeMicros, &item.Currency, &item.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan pending ledger charge: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending ledger charges: %w", err)
	}
	return out, nil
}

// UpdateServiceRunLedgerBillingState 结算状态迁移（from_state 条件更新，幂等）。
// run 不存在或已不在 from_state → false，不报错。
func (a *Adapter) UpdateServiceRunLedgerBillingState(ctx context.Context, runID, fromState, toState string) (bool, error) {
	if fromState == "" || toState == "" {
		return false, fmt.Errorf("update service run ledger billing state: from/to state required")
	}
	result, err := a.db.ExecContext(ctx, `
		UPDATE service_run_ledger
		SET billing_state = $3
		WHERE run_id = $1 AND billing_state = $2`, runID, fromState, toState)
	if err != nil {
		return false, fmt.Errorf("update service run ledger billing state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update service run ledger billing state: %w", err)
	}
	return affected > 0, nil
}

// SumPendingCallerChargeMicros 汇总该调用方尚未收回的金额（待扣 + 扣费失败）。
// 口径是 gross（调用方应付全额），预检时并入已用额度。
func (a *Adapter) SumPendingCallerChargeMicros(ctx context.Context, payerUID int64) (int64, error) {
	var total int64
	err := a.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(gross_micros), 0)
FROM service_run_ledger
WHERE payer_uid = $1 AND billing_state IN ('pending_credit', 'charge_failed')`, payerUID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum pending caller charge: %w", err)
	}
	return total, nil
}
