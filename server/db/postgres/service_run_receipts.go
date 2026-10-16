package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// 生命周期回注收件箱（设计 §4.8-2；迁移 000033）。
// 收件箱在 run 终态事务内写入（与状态变更原子），投递 worker 扫描待投递并列。

// insertServiceRunReceiptTx 在事务内写入回执（ON CONFLICT 幂等：同 run 至多一份）。
// caller_uid 经 service_sessions join 取（receipt 跟账号）。
func insertServiceRunReceiptTx(ctx context.Context, tx *sql.Tx, runID, sessionID, status, question, errorText string) error {
	receiptID := store.ServiceRunReceiptIDForRun(runID)
	if sessionID == "" {
		err := tx.QueryRowContext(ctx, `SELECT session_id FROM service_runs WHERE run_id = $1`, runID).Scan(&sessionID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("resolve receipt session: %w", err)
		}
	}
	payload := store.BuildServiceRunReceiptPayload(receiptID, runID, sessionID, status, question, errorText)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO service_run_receipts (receipt_id, run_id, session_id, caller_uid, status, payload_json)
		SELECT $1, r.run_id, r.session_id, s.caller_uid, $2, $3
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		WHERE r.run_id = $4
		ON CONFLICT (receipt_id) DO NOTHING`,
		receiptID, status, []byte(payload), runID)
	if err != nil {
		return fmt.Errorf("insert service run receipt: %w", err)
	}
	return nil
}

func (a *Adapter) ListUndeliveredServiceRunReceipts(ctx context.Context, limit int) ([]store.ServiceRunReceipt, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT receipt_id, run_id, session_id, caller_uid, status, payload_json, delivery_attempts, created_at
		FROM service_run_receipts
		WHERE delivered_at IS NULL
		ORDER BY created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list undelivered service run receipts: %w", err)
	}
	defer rows.Close()
	var out []store.ServiceRunReceipt
	for rows.Next() {
		var (
			receipt store.ServiceRunReceipt
			payload []byte
		)
		if err := rows.Scan(&receipt.ReceiptID, &receipt.RunID, &receipt.SessionID, &receipt.CallerUID,
			&receipt.Status, &payload, &receipt.DeliveryAttempts, &receipt.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan service run receipt: %w", err)
		}
		receipt.Payload = append(json.RawMessage(nil), payload...)
		out = append(out, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service run receipts: %w", err)
	}
	return out, nil
}

func (a *Adapter) MarkServiceRunReceiptDelivered(ctx context.Context, receiptID string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE service_run_receipts SET delivered_at = clock_timestamp()
		WHERE receipt_id = $1 AND delivered_at IS NULL`, receiptID)
	if err != nil {
		return fmt.Errorf("mark service run receipt delivered: %w", err)
	}
	return nil
}

func (a *Adapter) TouchServiceRunReceiptDelivery(ctx context.Context, receiptID string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE service_run_receipts SET delivery_attempts = delivery_attempts + 1
		WHERE receipt_id = $1 AND delivered_at IS NULL`, receiptID)
	if err != nil {
		return fmt.Errorf("touch service run receipt delivery: %w", err)
	}
	return nil
}
