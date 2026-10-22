package mysql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// ListBillableServiceRuns 列出已终态但尚未记账的 run（PG 同语义镜像；设计 §4.5）。
func (a *Adapter) ListBillableServiceRuns(ctx context.Context, limit int) ([]store.ServiceRunBillable, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT r.run_id, r.session_id, s.agent_id, a.slug, a.owner_kind, a.owner_uid,
		       s.caller_uid, s.caller_bot_uid,
		       COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.definition_json, '$.runner.type')), 'codex') AS runner_type,
		       r.state,
		       COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(r.usage_json, '$.prompt_tokens')) AS SIGNED), 0),
		       COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(r.usage_json, '$.completion_tokens')) AS SIGNED), 0),
		       GREATEST(0, CAST(TIMESTAMPDIFF(MICROSECOND, COALESCE(r.started_at, r.created_at), r.finished_at) / 1000 AS SIGNED)),
		       COALESCE(v.definition_json, JSON_OBJECT()),
		       r.finished_at
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		LEFT JOIN service_agent_versions v ON v.agent_id = a.id AND v.version = s.definition_rev
		LEFT JOIN service_run_ledger l ON l.run_id = r.run_id
		WHERE r.state IN ('completed','failed','awaiting_input')
		  AND r.finished_at IS NOT NULL
		  AND r.finished_at >= DATE_SUB(NOW(6), INTERVAL 48 HOUR)
		  AND l.run_id IS NULL
		ORDER BY r.finished_at
		LIMIT ?`, limit)
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

// RecordServiceRunLedger 写入一行账本（INSERT IGNORE = 幂等）。
func (a *Adapter) RecordServiceRunLedger(ctx context.Context, entry store.ServiceRunLedgerEntry) (bool, error) {
	billingState := entry.BillingState
	if billingState == "" {
		billingState = store.ServiceBillingFree
	}
	var pricing interface{}
	if len(entry.PricingJSON) > 0 {
		pricing = []byte(entry.PricingJSON)
	}
	result, err := a.db.ExecContext(ctx, `
		INSERT IGNORE INTO service_run_ledger (
			run_id, session_id, agent_id, agent_slug, owner_kind, owner_uid,
			caller_uid, caller_bot_uid, runner_type, status,
			prompt_tokens, completion_tokens, duration_ms,
			pricing_json, currency, gross_micros, fee_micros, net_micros,
			payer_uid, payee_uid, billing_state, finished_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entry.RunID, entry.SessionID, entry.AgentID, entry.AgentSlug, entry.OwnerKind, entry.OwnerUID,
		entry.CallerUID, entry.CallerBotUID, entry.RunnerType, entry.Status,
		entry.PromptTokens, entry.CompletionTokens, entry.DurationMs,
		pricing, entry.Currency, entry.GrossMicros, entry.FeeMicros, entry.NetMicros,
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

// ListPendingServiceRunLedgerCharges 列出待补扣的账本行（PG 同语义镜像）。
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
		  AND finished_at >= DATE_SUB(NOW(6), INTERVAL 48 HOUR)
		ORDER BY finished_at
		LIMIT ?`, limit)
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

// UpdateServiceRunLedgerBillingState 结算状态迁移（PG 同语义镜像）。
func (a *Adapter) UpdateServiceRunLedgerBillingState(ctx context.Context, runID, fromState, toState string) (bool, error) {
	if fromState == "" || toState == "" {
		return false, fmt.Errorf("update service run ledger billing state: from/to state required")
	}
	result, err := a.db.ExecContext(ctx, `
		UPDATE service_run_ledger
		SET billing_state = ?
		WHERE run_id = ? AND billing_state = ?`, toState, runID, fromState)
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
func (a *Adapter) SumPendingCallerChargeMicros(ctx context.Context, payerUID int64) (int64, error) {
	var total int64
	err := a.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(gross_micros), 0)
FROM service_run_ledger
WHERE payer_uid = ? AND billing_state IN ('pending_credit', 'charge_failed')`, payerUID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum pending caller charge: %w", err)
	}
	return total, nil
}
