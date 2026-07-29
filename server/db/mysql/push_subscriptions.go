package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// UpsertPushSubscription atomically creates or refreshes a subscription while
// enforcing the per-user limit across all server replicas.
func (a *Adapter) UpsertPushSubscription(ctx context.Context, subscription *types.PushSubscription, maxSubscriptions int) (bool, error) {
	if subscription == nil {
		return false, fmt.Errorf("push subscription is nil")
	}
	if maxSubscriptions <= 0 {
		return false, fmt.Errorf("push subscription limit must be positive")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin push subscription upsert: %w", err)
	}
	defer tx.Rollback()

	var lockedUID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ? FOR UPDATE`, subscription.UID).Scan(&lockedUID); err != nil {
		return false, fmt.Errorf("lock push subscription user: %w", err)
	}

	var existingUID int64
	existingErr := tx.QueryRowContext(ctx,
		`SELECT uid FROM push_subscriptions WHERE endpoint = ?`,
		subscription.Endpoint,
	).Scan(&existingUID)
	if existingErr != nil && existingErr != sql.ErrNoRows {
		return false, fmt.Errorf("find push subscription endpoint: %w", existingErr)
	}
	if existingErr == sql.ErrNoRows || existingUID != subscription.UID {
		var count int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM push_subscriptions WHERE uid = ?`,
			subscription.UID,
		).Scan(&count); err != nil {
			return false, fmt.Errorf("count push subscriptions: %w", err)
		}
		if count >= maxSubscriptions {
			return false, nil
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   uid = VALUES(uid),
		   p256dh = VALUES(p256dh),
		   auth = VALUES(auth),
		   registration_id = VALUES(registration_id)`,
		subscription.UID,
		subscription.Endpoint,
		subscription.P256DH,
		subscription.Auth,
		subscription.RegistrationID,
	); err != nil {
		return false, fmt.Errorf("upsert push subscription: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit push subscription upsert: %w", err)
	}
	return true, nil
}

// ListPushSubscriptions returns all subscriptions owned by uid.
func (a *Adapter) ListPushSubscriptions(ctx context.Context, uid int64) ([]*types.PushSubscription, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, uid, endpoint, p256dh, auth, registration_id, created_at, updated_at
		 FROM push_subscriptions
		 WHERE uid = ?
		 ORDER BY id ASC`,
		uid,
	)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer rows.Close()

	subscriptions := make([]*types.PushSubscription, 0)
	for rows.Next() {
		var subscription types.PushSubscription
		if err := rows.Scan(
			&subscription.ID,
			&subscription.UID,
			&subscription.Endpoint,
			&subscription.P256DH,
			&subscription.Auth,
			&subscription.RegistrationID,
			&subscription.CreatedAt,
			&subscription.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan push subscription: %w", err)
		}
		subscriptions = append(subscriptions, &subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate push subscriptions: %w", err)
	}
	return subscriptions, nil
}

// DeletePushSubscription removes one endpoint if it belongs to uid.
func (a *Adapter) DeletePushSubscription(ctx context.Context, uid int64, endpoint, registrationID string) error {
	if _, err := a.db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE uid = ? AND endpoint = ? AND registration_id = ?`,
		uid,
		endpoint,
		registrationID,
	); err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}
