package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Cats 算力任务的终态回执收件箱（迁移 000085）。
//
// 回执在 FinishComputeTask 的事务内写入：和结算、终态同一个提交，所以不存在
// 「扣了钱却没有回执」的中间态。receipt_id 由 quote_id 确定性派生，quote_id 上还有
// UNIQUE 约束，ON CONFLICT DO NOTHING 让 finish 重放永远只有一份回执。

var _ store.ComputeTaskReceiptStore = (*Adapter)(nil)

// insertComputeTaskReceiptTx 在 finish 的同一事务里写终态回执。
// state 是任务落盘的终态；payload 的 status 与 state 同值（回执状态 = 任务终态）。
func insertComputeTaskReceiptTx(ctx context.Context, tx *sql.Tx, quoteID string, uid int64, state string, chargedUnits int64, progress store.ComputeTaskProgress, now time.Time) error {
	receiptID := store.ComputeTaskReceiptIDForQuote(quoteID)
	payload := store.BuildComputeTaskReceiptPayload(receiptID, quoteID, state, state, chargedUnits, progress, now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO compute_task_receipts (receipt_id, quote_id, caller_uid, status, payload_json, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT DO NOTHING`,
		receiptID, quoteID, uid, state, string(payload), now); err != nil {
		return fmt.Errorf("insert compute task receipt: %w", err)
	}
	return nil
}

func (a *Adapter) ListUndeliveredComputeTaskReceipts(ctx context.Context, limit int) ([]store.ComputeTaskReceipt, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT receipt_id, quote_id, caller_uid, status, payload_json, delivery_attempts, created_at
		FROM compute_task_receipts
		WHERE delivered_at IS NULL
		ORDER BY created_at, receipt_id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list undelivered compute task receipts: %w", err)
	}
	defer rows.Close()
	var out []store.ComputeTaskReceipt
	for rows.Next() {
		var (
			receipt store.ComputeTaskReceipt
			payload []byte
		)
		if err := rows.Scan(&receipt.ReceiptID, &receipt.QuoteID, &receipt.CallerUID,
			&receipt.Status, &payload, &receipt.DeliveryAttempts, &receipt.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan compute task receipt: %w", err)
		}
		receipt.Payload = append(json.RawMessage(nil), payload...)
		out = append(out, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compute task receipts: %w", err)
	}
	return out, nil
}

func (a *Adapter) MarkComputeTaskReceiptDelivered(ctx context.Context, receiptID string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE compute_task_receipts SET delivered_at = clock_timestamp()
		WHERE receipt_id = $1 AND delivered_at IS NULL`, receiptID)
	if err != nil {
		return fmt.Errorf("mark compute task receipt delivered: %w", err)
	}
	return nil
}

func (a *Adapter) TouchComputeTaskReceiptDelivery(ctx context.Context, receiptID string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE compute_task_receipts SET delivery_attempts = delivery_attempts + 1
		WHERE receipt_id = $1 AND delivered_at IS NULL`, receiptID)
	if err != nil {
		return fmt.Errorf("touch compute task receipt delivery: %w", err)
	}
	return nil
}
