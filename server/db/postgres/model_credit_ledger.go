package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.ModelCreditLedgerStore = (*Adapter)(nil)

// modelCreditAccountQuery reads one account row in bucket order.
const modelCreditAccountQuery = `SELECT monthly_units, gift_units, paid_units, debt_units, updated_at
	FROM model_credit_accounts WHERE uid = $1`

const modelCreditReservationQuery = `SELECT reserved_units, monthly_units, gift_units, paid_units,
	settled_units, state, created_at, lease_expires_at, no_usage_confirmed_at, settled_at, released_at
	FROM model_credit_reservations WHERE uid = $1 AND request_id = $2`

type modelCreditQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// lockModelCreditAccount creates the account row if needed and then holds its
// row lock for the rest of the transaction, which is what makes concurrent
// reserves/settles of one user serialize. Insert-then-lock instead of
// select-then-insert: the loser of ON CONFLICT DO NOTHING waits for the winner
// to commit rather than both creating a balance out of nothing.
func lockModelCreditAccount(ctx context.Context, tx *sql.Tx, uid int64) (*store.ModelCreditAccount, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO model_credit_accounts (uid) VALUES ($1) ON CONFLICT (uid) DO NOTHING`, uid); err != nil {
		return nil, fmt.Errorf("create model credit account: %w", err)
	}
	account := store.ModelCreditAccount{UID: uid}
	if err := tx.QueryRowContext(ctx, modelCreditAccountQuery+` FOR UPDATE`, uid).Scan(
		&account.MonthlyUnits, &account.GiftUnits, &account.PaidUnits, &account.DebtUnits, &account.UpdatedAt); err != nil {
		return nil, fmt.Errorf("lock model credit account: %w", err)
	}
	if err := account.Validate(); err != nil {
		return nil, fmt.Errorf("model credit account %d is inconsistent: %w", uid, err)
	}
	return &account, nil
}

func saveModelCreditAccount(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount, now time.Time) error {
	account.UpdatedAt = now.UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE model_credit_accounts
		SET monthly_units = $2, gift_units = $3, paid_units = $4, debt_units = $5, updated_at = $6
		WHERE uid = $1`,
		account.UID, account.MonthlyUnits, account.GiftUnits, account.PaidUnits, account.DebtUnits,
		account.UpdatedAt); err != nil {
		return fmt.Errorf("store model credit account: %w", err)
	}
	return nil
}

// recordModelCreditEvent appends one idempotent ledger row. The key is the
// primary key, so a repeated operation fails loudly instead of double booking;
// the balance columns are the account snapshot after the whole operation.
func recordModelCreditEvent(ctx context.Context, tx *sql.Tx, key string, account *store.ModelCreditAccount,
	eventType, bucket string, units int64, requestID *string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_credit_ledger_events
		(event_key, uid, bucket, event_type, units, request_id,
		 monthly_balance, gift_balance, paid_balance, debt_balance)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		key, account.UID, bucket, eventType, units, requestID,
		account.MonthlyUnits, account.GiftUnits, account.PaidUnits, account.DebtUnits); err != nil {
		return fmt.Errorf("record model credit event: %w", err)
	}
	return nil
}

func insertModelCreditReservation(ctx context.Context, tx *sql.Tx, reservation *store.ModelCreditReservation) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_credit_reservations
		(uid, request_id, reserved_units, monthly_units, gift_units, paid_units, state, created_at, lease_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		reservation.UID, reservation.RequestID, reservation.ReservedUnits, reservation.Split.Monthly,
		reservation.Split.Gift, reservation.Split.Paid, reservation.State, reservation.CreatedAt, reservation.LeaseExpiresAt); err != nil {
		return fmt.Errorf("insert model credit reservation: %w", err)
	}
	return nil
}

func readModelCreditReservation(ctx context.Context, q modelCreditQuerier, uid int64, requestID string, lock bool) (*store.ModelCreditReservation, error) {
	reservation := store.ModelCreditReservation{UID: uid, RequestID: requestID}
	query := modelCreditReservationQuery
	if lock {
		query += ` FOR UPDATE`
	}
	var settledUnits sql.NullInt64
	var leaseExpiresAt time.Time
	var noUsageConfirmedAt sql.NullTime
	var settledAt sql.NullTime
	var releasedAt sql.NullTime
	if err := q.QueryRowContext(ctx, query, uid, requestID).Scan(
		&reservation.ReservedUnits, &reservation.Split.Monthly, &reservation.Split.Gift, &reservation.Split.Paid,
		&settledUnits, &reservation.State, &reservation.CreatedAt, &leaseExpiresAt, &noUsageConfirmedAt,
		&settledAt, &releasedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrModelCreditNotFound
		}
		return nil, fmt.Errorf("read model credit reservation: %w", err)
	}
	reservation.LeaseExpiresAt = leaseExpiresAt
	if noUsageConfirmedAt.Valid {
		at := noUsageConfirmedAt.Time
		reservation.NoUsageConfirmedAt = &at
	}
	if settledUnits.Valid {
		units := settledUnits.Int64
		reservation.SettledUnits = &units
	}
	if settledAt.Valid {
		at := settledAt.Time
		reservation.SettledAt = &at
	}
	if releasedAt.Valid {
		at := releasedAt.Time
		reservation.ReleasedAt = &at
	}
	if err := reservation.Validate(); err != nil {
		return nil, fmt.Errorf("model credit reservation %q is inconsistent: %w", requestID, err)
	}
	return &reservation, nil
}

func settleModelCreditReservation(ctx context.Context, tx *sql.Tx, reservation *store.ModelCreditReservation, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE model_credit_reservations
		SET settled_units = $3, state = $4, settled_at = $5
		WHERE uid = $1 AND request_id = $2`,
		reservation.UID, reservation.RequestID, *reservation.SettledUnits,
		store.ModelCreditReservationSettled, now.UTC())
	if err != nil {
		return fmt.Errorf("settle model credit reservation: %w", err)
	}
	return nil
}

func releaseModelCreditReservation(ctx context.Context, tx *sql.Tx, reservation *store.ModelCreditReservation, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE model_credit_reservations
		SET state = $3, released_at = $4
		WHERE uid = $1 AND request_id = $2`,
		reservation.UID, reservation.RequestID, store.ModelCreditReservationReleased, now.UTC())
	if err != nil {
		return fmt.Errorf("release model credit reservation: %w", err)
	}
	return nil
}
func modelCreditBucketParts(split store.ModelCreditSplit) [3]struct {
	bucket string
	units  int64
} {
	return [3]struct {
		bucket string
		units  int64
	}{
		{store.ModelCreditBucketMonthly, split.Monthly},
		{store.ModelCreditBucketGift, split.Gift},
		{store.ModelCreditBucketPaid, split.Paid},
	}
}

// GrantModelCredits credits one bucket with a caller keyed, idempotent event.
// It is the only way money enters the ledger.
func (a *Adapter) GrantModelCredits(ctx context.Context, in store.ModelCreditGrantInput) (*store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	bucket, _ := store.NormalizeModelCreditGrantBucket(in.Bucket)
	key := store.ModelCreditGrantEventKey(in.IdempotencyKey)
	if len(key) > store.ModelCreditEventKeyMaxLen {
		return nil, store.ErrModelCreditInvalid
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model credit grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, err := lockModelCreditAccount(ctx, tx, in.UID)
	if err != nil {
		return nil, err
	}
	var bookedUID, bookedUnits int64
	var bookedBucket string
	switch err := tx.QueryRowContext(ctx,
		`SELECT uid, bucket, units FROM model_credit_ledger_events WHERE event_key = $1`, key).
		Scan(&bookedUID, &bookedBucket, &bookedUnits); {
	case err == nil:
		if bookedUID != in.UID || bookedBucket != bucket || bookedUnits != in.Units {
			return nil, store.ErrModelCreditConflict
		}
		// Replay: the credit is already booked, so the balance is unchanged.
		return account, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return nil, fmt.Errorf("read model credit grant event: %w", err)
	}
	var ok bool
	switch bucket {
	case store.ModelCreditBucketMonthly:
		account.MonthlyUnits, ok = store.ModelCreditAddUnits(account.MonthlyUnits, in.Units)
	case store.ModelCreditBucketGift:
		account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, in.Units)
	default:
		account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, in.Units)
	}
	if !ok {
		return nil, store.ErrModelCreditInvalid
	}
	now := time.Now().UTC()
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, err
	}
	if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventGrant, bucket, in.Units, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model credit grant: %w", err)
	}
	return account, nil
}

// ReserveModelCredits holds units for one request in the fixed order monthly ->
// gift -> paid, or reports ErrModelCreditInsufficient. The hold is keyed by
// request id, so a replay returns the stored hold and a replay with a different
// amount is rejected as a conflict.
func (a *Adapter) ReserveModelCredits(ctx context.Context, in store.ModelCreditReserveInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model credit reserve: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, err := lockModelCreditAccount(ctx, tx, in.UID)
	if err != nil {
		return nil, nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	switch {
	case err == nil:
		if reservation.State != store.ModelCreditReservationReserved || reservation.ReservedUnits != in.Units {
			return nil, nil, store.ErrModelCreditConflict
		}
		// Replay: the hold already exists, so nothing is held twice.
		return reservation, account, nil
	case errors.Is(err, store.ErrModelCreditNotFound):
	default:
		return nil, nil, err
	}
	split, ok := store.AllocateModelCreditUnits(*account, in.Units)
	if !ok {
		return nil, nil, store.ErrModelCreditInsufficient
	}
	account.MonthlyUnits -= split.Monthly
	account.GiftUnits -= split.Gift
	account.PaidUnits -= split.Paid
	now := time.Now().UTC()
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	reservation = &store.ModelCreditReservation{
		UID:            in.UID,
		RequestID:      in.RequestID,
		ReservedUnits:  in.Units,
		Split:          split,
		State:          store.ModelCreditReservationReserved,
		CreatedAt:      now,
		LeaseExpiresAt: now.Add(store.ModelCreditReservationLease),
	}
	if err := insertModelCreditReservation(ctx, tx, reservation); err != nil {
		return nil, nil, err
	}
	for _, part := range modelCreditBucketParts(split) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventReserve, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventReserve, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit model credit reserve: %w", err)
	}
	return reservation, account, nil
}

// SettleModelCredits charges what the request actually cost. The unused part of
// the hold goes back to the buckets it came from. An amount above the hold is
// only taken when the balance really covers it now; otherwise the call fails
// with ErrModelCreditInsufficient and writes nothing, so settle never
// over-draws and never creates debt.
func (a *Adapter) SettleModelCredits(ctx context.Context, in store.ModelCreditSettleInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model credit settle: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, err := lockModelCreditAccount(ctx, tx, in.UID)
	if err != nil {
		return nil, nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	if err != nil {
		return nil, nil, err
	}
	if reservation.State == store.ModelCreditReservationSettled {
		if reservation.SettledUnits == nil || *reservation.SettledUnits != in.ActualUnits {
			return nil, nil, store.ErrModelCreditConflict
		}
		// Replay: the request is already settled for this exact amount.
		return reservation, account, nil
	}
	if reservation.State != store.ModelCreditReservationReserved {
		return nil, nil, store.ErrModelCreditConflict
	}
	consumed := store.ConsumeModelCreditSplit(reservation.Split, in.ActualUnits)
	released := store.ModelCreditSplit{
		Monthly: reservation.Split.Monthly - consumed.Monthly,
		Gift:    reservation.Split.Gift - consumed.Gift,
		Paid:    reservation.Split.Paid - consumed.Paid,
	}
	var overage store.ModelCreditSplit
	if excess := in.ActualUnits - reservation.ReservedUnits; excess > 0 {
		// Taken from credit that is really available now. The order is the same
		// monthly -> gift -> paid used by reserve, and gift stays unusable while
		// the account carries debt.
		allocated, ok := store.AllocateModelCreditUnits(*account, excess)
		if !ok {
			return nil, nil, store.ErrModelCreditInsufficient
		}
		overage = allocated
	}
	var ok bool
	if account.MonthlyUnits, ok = store.ModelCreditAddUnits(account.MonthlyUnits, released.Monthly); !ok {
		return nil, nil, store.ErrModelCreditInvalid
	}
	if account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, released.Gift); !ok {
		return nil, nil, store.ErrModelCreditInvalid
	}
	if account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, released.Paid); !ok {
		return nil, nil, store.ErrModelCreditInvalid
	}
	account.MonthlyUnits -= overage.Monthly
	account.GiftUnits -= overage.Gift
	account.PaidUnits -= overage.Paid
	now := time.Now().UTC()
	settledUnits := in.ActualUnits
	reservation.SettledUnits = &settledUnits
	reservation.State = store.ModelCreditReservationSettled
	reservation.SettledAt = &now
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := settleModelCreditReservation(ctx, tx, reservation, now); err != nil {
		return nil, nil, err
	}
	charged := store.ModelCreditSplit{
		Monthly: consumed.Monthly + overage.Monthly,
		Gift:    consumed.Gift + overage.Gift,
		Paid:    consumed.Paid + overage.Paid,
	}
	for _, part := range modelCreditBucketParts(charged) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventSettle, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventSettle, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	for _, part := range modelCreditBucketParts(released) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventRelease, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRelease, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit model credit settle: %w", err)
	}
	return reservation, account, nil
}

// ReleaseModelCredits returns the complete hold only after zero usage has
// been confirmed. A settled reservation cannot be released.
func (a *Adapter) ReleaseModelCredits(ctx context.Context, in store.ModelCreditReleaseInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model credit release: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, err := lockModelCreditAccount(ctx, tx, in.UID)
	if err != nil {
		return nil, nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	if err != nil {
		return nil, nil, err
	}
	if reservation.State == store.ModelCreditReservationReleased {
		return reservation, account, nil
	}
	if reservation.State != store.ModelCreditReservationReserved {
		return nil, nil, store.ErrModelCreditConflict
	}
	if reservation.NoUsageConfirmedAt == nil {
		return nil, nil, store.ErrModelCreditConflict
	}
	for _, part := range modelCreditBucketParts(reservation.Split) {
		var ok bool
		switch part.bucket {
		case store.ModelCreditBucketMonthly:
			account.MonthlyUnits, ok = store.ModelCreditAddUnits(account.MonthlyUnits, part.units)
		case store.ModelCreditBucketGift:
			account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, part.units)
		case store.ModelCreditBucketPaid:
			account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, part.units)
		}
		if !ok {
			return nil, nil, store.ErrModelCreditInvalid
		}
	}
	now := time.Now().UTC()
	reservation.State = store.ModelCreditReservationReleased
	reservation.ReleasedAt = &now
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := releaseModelCreditReservation(ctx, tx, reservation, now); err != nil {
		return nil, nil, err
	}
	for _, part := range modelCreditBucketParts(reservation.Split) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventRelease, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRelease, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit model credit release: %w", err)
	}
	return reservation, account, nil
}

// GetModelCreditAccount reads the buckets. A missing row is
// ErrModelCreditNotFound; reading never creates credit.
func (a *Adapter) GetModelCreditAccount(ctx context.Context, uid int64) (*store.ModelCreditAccount, error) {
	if uid <= 0 {
		return nil, store.ErrModelCreditInvalid
	}
	account := store.ModelCreditAccount{UID: uid}
	if err := a.db.QueryRowContext(ctx, modelCreditAccountQuery, uid).Scan(
		&account.MonthlyUnits, &account.GiftUnits, &account.PaidUnits, &account.DebtUnits,
		&account.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrModelCreditNotFound
		}
		return nil, fmt.Errorf("read model credit account: %w", err)
	}
	if err := account.Validate(); err != nil {
		return nil, fmt.Errorf("model credit account %d is inconsistent: %w", uid, err)
	}
	return &account, nil
}

// GetModelCreditReservation reads one hold of one user.
func (a *Adapter) GetModelCreditReservation(ctx context.Context, uid int64, requestID string) (*store.ModelCreditReservation, error) {
	if uid <= 0 {
		return nil, store.ErrModelCreditInvalid
	}
	if err := store.ValidateModelCreditRequestID(requestID); err != nil {
		return nil, err
	}
	return readModelCreditReservation(ctx, a.db, uid, requestID, false)
}

// MarkModelCreditNoUsage records terminal failure evidence. It does not release
// funds; the 30-minute reservation lease remains a quarantine window for delayed
// usage delivery.
func (a *Adapter) MarkModelCreditNoUsage(ctx context.Context, in store.ModelCreditNoUsageConfirmation) (*store.ModelCreditReservation, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin no-usage confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockModelCreditAccount(ctx, tx, in.UID); err != nil {
		return nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	if err != nil {
		return nil, err
	}
	// Replaying a successful zero-usage completion after release must stay
	// idempotent, including when the peer lost the release response.
	if reservation.State == store.ModelCreditReservationReleased && reservation.NoUsageConfirmedAt != nil {
		return reservation, nil
	}
	if reservation.State != store.ModelCreditReservationReserved {
		return nil, store.ErrModelCreditConflict
	}
	if reservation.NoUsageConfirmedAt == nil {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE model_credit_reservations
			SET no_usage_confirmed_at = $3 WHERE uid = $1 AND request_id = $2`,
			in.UID, in.RequestID, now); err != nil {
			return nil, fmt.Errorf("mark model credit request no-usage: %w", err)
		}
		reservation.NoUsageConfirmedAt = &now
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit no-usage confirmation: %w", err)
	}
	return reservation, nil
}

// ReleaseExpiredModelCredits scans candidates without row locks, then releases
// each through the account-first transaction used by settle. This avoids lock
// inversion; a late settle wins or loses atomically, never both.
func (a *Adapter) ReleaseExpiredModelCredits(ctx context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit <= 0 {
		return 0, store.ErrModelCreditInvalid
	}
	rows, err := a.db.QueryContext(ctx, `SELECT uid, request_id
		FROM model_credit_reservations
		WHERE state = 'reserved' AND no_usage_confirmed_at IS NOT NULL AND lease_expires_at <= $1
		ORDER BY lease_expires_at, uid, request_id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("list expired model credit reservations: %w", err)
	}
	type candidate struct {
		uid       int64
		requestID string
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.uid, &item.requestID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired model credit reservation: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate expired model credit reservations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close expired model credit reservations: %w", err)
	}
	released := 0
	for _, item := range candidates {
		_, _, err := a.ReleaseModelCredits(ctx, store.ModelCreditReleaseInput{UID: item.uid, RequestID: item.requestID})
		if errors.Is(err, store.ErrModelCreditConflict) {
			continue
		}
		if err != nil {
			return released, err
		}
		released++
	}
	return released, nil
}

func (a *Adapter) ListModelCreditLedgerEvents(ctx context.Context, uid int64, limit, offset int) ([]store.ModelCreditLedgerEvent, error) {
	if uid <= 0 || limit <= 0 || limit > 100 || offset < 0 {
		return nil, store.ErrModelCreditInvalid
	}
	rows, err := a.db.QueryContext(ctx, `SELECT event_key, bucket, event_type, units, request_id,
		monthly_balance, gift_balance, paid_balance, debt_balance, created_at
		FROM model_credit_ledger_events WHERE uid = $1
		ORDER BY created_at DESC, event_key DESC LIMIT $2 OFFSET $3`, uid, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list model credit ledger events: %w", err)
	}
	defer rows.Close()
	events := make([]store.ModelCreditLedgerEvent, 0, limit)
	for rows.Next() {
		var event store.ModelCreditLedgerEvent
		var requestID sql.NullString
		if err := rows.Scan(&event.EventKey, &event.Bucket, &event.EventType, &event.Units, &requestID,
			&event.MonthlyBalance, &event.GiftBalance, &event.PaidBalance, &event.DebtBalance, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan model credit ledger event: %w", err)
		}
		if requestID.Valid {
			event.RequestID = &requestID.String
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model credit ledger events: %w", err)
	}
	return events, nil
}
