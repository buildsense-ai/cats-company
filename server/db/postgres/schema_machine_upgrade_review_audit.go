package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// createMachineUpgradeReviewAuditTable is migration 000076 applied to a database
// created from scratch: CreateSchema builds the final shape, so the statements
// here are the same idempotent CREATE the migration file carries.
//
// 人工复核云机器升配是「把钱继续花下去」的唯一人工入口，所以授权必须落库，不能
// 只写一行日志：
//   - 只有真的拿到租约认领时才有审计行，终态/pending/保护期内的空转不写；
//   - 审计写入和认领在同一个事务里，审计写不进去就连认领一起回滚；
//   - 表只追加（触发器拒绝 UPDATE/DELETE），历史不可改写；
//   - 只记事实（授权人、操作、理由、认领前后的现场），不记任何凭据。
const createMachineUpgradeReviewAuditTable = `
CREATE TABLE IF NOT EXISTS cloud_worker_machine_upgrade_review_audits (
    audit_id BIGSERIAL PRIMARY KEY,
    operation_id VARCHAR(64) NOT NULL
        REFERENCES cloud_worker_machine_upgrades(operation_id) ON DELETE CASCADE,
    actor_uid BIGINT NOT NULL CHECK (actor_uid > 0),
    action VARCHAR(32) NOT NULL
        CHECK (action IN ('retry_machine_upgrade_review')),
    reason TEXT NOT NULL
        CHECK (char_length(btrim(reason)) BETWEEN 8 AND 1000),
    previous_status VARCHAR(16) NOT NULL
        CHECK (previous_status IN ('pending','running','completed','failed','reconciling')),
    previous_message TEXT NOT NULL DEFAULT '',
    previous_attempts INT NOT NULL CHECK (previous_attempts >= 0),
    claimed_attempt INT NOT NULL CHECK (claimed_attempt = previous_attempts + 1),
    claimed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_machine_upgrade_review_audits_operation
    ON cloud_worker_machine_upgrade_review_audits (operation_id, claimed_at DESC);
CREATE INDEX IF NOT EXISTS idx_machine_upgrade_review_audits_actor
    ON cloud_worker_machine_upgrade_review_audits (actor_uid, claimed_at DESC);
CREATE OR REPLACE FUNCTION guard_machine_upgrade_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'machine upgrade review audit is append-only';
END;
$$;
DROP TRIGGER IF EXISTS guard_machine_upgrade_review_audit ON cloud_worker_machine_upgrade_review_audits;
CREATE TRIGGER guard_machine_upgrade_review_audit BEFORE UPDATE OR DELETE ON cloud_worker_machine_upgrade_review_audits
FOR EACH ROW EXECUTE FUNCTION guard_machine_upgrade_review_audit();
`

// machineUpgradeReviewAuditAction is the one operator action this table records.
const machineUpgradeReviewAuditAction = "retry_machine_upgrade_review"

// 理由的长度按「字符」算，不按字节：中文理由 8 个字符是 24 字节，按字节校验
// 等于把中文的门槛降到 3 个字。DB 的 CHECK 用的是同一组数字（8..1000 字符）。
const (
	machineUpgradeReviewReasonMinChars = 8
	machineUpgradeReviewReasonMaxChars = 1000
)

// machineUpgradeReviewAuditInput is the operator's authorization for one further
// attempt on a held machine upgrade. It carries who asked and what they said,
// never a credential.
type machineUpgradeReviewAuditInput struct {
	ActorUID int64
	Reason   string
}

// machineUpgradeReviewAuditRecord is one append-only row of that authorization.
type machineUpgradeReviewAuditRecord struct {
	AuditID          int64
	OperationID      string
	ActorUID         int64
	Action           string
	Reason           string
	PreviousStatus   string
	PreviousMessage  string
	PreviousAttempts int
	ClaimedAttempt   int
	ClaimedAt        time.Time
	CreatedAt        time.Time
}

// normalizeMachineUpgradeReviewReason trims the investigation reason and bounds
// it by Unicode characters. An empty or too long reason is refused before the row
// is locked, so a bad authorization never touches money.
func normalizeMachineUpgradeReviewReason(reason string) (string, error) {
	trimmed := strings.TrimSpace(reason)
	length := utf8.RuneCountInString(trimmed)
	if length < machineUpgradeReviewReasonMinChars || length > machineUpgradeReviewReasonMaxChars {
		return "", fmt.Errorf("machine upgrade review reason must be %d-%d characters, got %d",
			machineUpgradeReviewReasonMinChars, machineUpgradeReviewReasonMaxChars, length)
	}
	return trimmed, nil
}

// insertMachineUpgradeReviewAudit writes the authorization row inside the very
// transaction that claimed the lease. A failure here must take the claim with it:
// the caller returns the error, the deferred rollback undoes the claim, and nobody
// got an unauthorized retry.
func insertMachineUpgradeReviewAudit(tx *sql.Tx, record machineUpgradeReviewAuditRecord) error {
	if tx == nil {
		return fmt.Errorf("machine upgrade review audit needs the claim transaction")
	}
	if record.ActorUID <= 0 || strings.TrimSpace(record.OperationID) == "" {
		return fmt.Errorf("machine upgrade review audit needs an actor and an operation")
	}
	reason, err := normalizeMachineUpgradeReviewReason(record.Reason)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO cloud_worker_machine_upgrade_review_audits(
			operation_id, actor_uid, action, reason, previous_status, previous_message,
			previous_attempts, claimed_attempt, claimed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		strings.TrimSpace(record.OperationID), record.ActorUID, machineUpgradeReviewAuditAction,
		reason, record.PreviousStatus, record.PreviousMessage, record.PreviousAttempts,
		record.ClaimedAttempt, record.ClaimedAt.UTC()); err != nil {
		return fmt.Errorf("record machine upgrade review audit: %w", err)
	}
	return nil
}

// machineUpgradeReviewAuditsFor reads the review authorization trail of one
// operation, oldest first. It is the read side the admin tests assert on: who
// authorized each further attempt, and what the row looked like beforehand.
func (a *Adapter) machineUpgradeReviewAuditsFor(operationID string) ([]machineUpgradeReviewAuditRecord, error) {
	rows, err := a.db.Query(`
		SELECT audit_id, operation_id, actor_uid, action, reason, previous_status,
		       previous_message, previous_attempts, claimed_attempt, claimed_at, created_at
		FROM cloud_worker_machine_upgrade_review_audits
		WHERE operation_id = $1
		ORDER BY audit_id`, strings.TrimSpace(operationID))
	if err != nil {
		return nil, fmt.Errorf("read machine upgrade review audit: %w", err)
	}
	defer rows.Close()
	out := make([]machineUpgradeReviewAuditRecord, 0, 2)
	for rows.Next() {
		var record machineUpgradeReviewAuditRecord
		if err := rows.Scan(&record.AuditID, &record.OperationID, &record.ActorUID, &record.Action,
			&record.Reason, &record.PreviousStatus, &record.PreviousMessage, &record.PreviousAttempts,
			&record.ClaimedAttempt, &record.ClaimedAt, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan machine upgrade review audit: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate machine upgrade review audit: %w", err)
	}
	return out, nil
}
