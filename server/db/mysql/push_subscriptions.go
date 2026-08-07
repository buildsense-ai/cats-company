package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store/types"
)

const maxPushSubscriptionUpsertAttempts = 3

// UpsertPushSubscription atomically creates or refreshes a subscription while
// enforcing the per-user limit across all server replicas. A current browser
// endpoint can move between accounts, even when the receiving account is full:
// the oldest receiving-account record is retired so the browser cannot retain
// delivery for the account that was just signed out.
func (a *Adapter) UpsertPushSubscription(ctx context.Context, subscription *types.PushSubscription, maxSubscriptions int) (bool, error) {
	var err error
	for attempt := 0; attempt < maxPushSubscriptionUpsertAttempts; attempt++ {
		var stored bool
		stored, err = a.upsertPushSubscriptionOnce(ctx, subscription, maxSubscriptions)
		if err == nil || !isRetryablePushSubscriptionTransactionError(err) {
			return stored, err
		}
	}
	return false, err
}

func (a *Adapter) upsertPushSubscriptionOnce(ctx context.Context, subscription *types.PushSubscription, maxSubscriptions int) (bool, error) {
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
		`SELECT uid FROM push_subscriptions WHERE endpoint = ? FOR UPDATE`,
		subscription.Endpoint,
	).Scan(&existingUID)
	if existingErr != nil && existingErr != sql.ErrNoRows {
		return false, fmt.Errorf("find push subscription endpoint: %w", existingErr)
	}
	endpointBelongsToAnotherUser := existingErr == nil && existingUID != subscription.UID
	if existingErr == sql.ErrNoRows || endpointBelongsToAnotherUser {
		var count int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM push_subscriptions WHERE uid = ?`,
			subscription.UID,
		).Scan(&count); err != nil {
			return false, fmt.Errorf("count push subscriptions: %w", err)
		}
		if count >= maxSubscriptions {
			if !endpointBelongsToAnotherUser {
				return false, nil
			}
			var retiredID int64
			err := tx.QueryRowContext(ctx,
				`SELECT id
				 FROM push_subscriptions
				 WHERE uid = ?
				 ORDER BY updated_at ASC, id ASC
				 LIMIT 1
				 FOR UPDATE`,
				subscription.UID,
			).Scan(&retiredID)
			if err != nil && err != sql.ErrNoRows {
				return false, fmt.Errorf("select push subscription to retire: %w", err)
			}
			if err == nil {
				if _, err := tx.ExecContext(ctx,
					`DELETE FROM push_subscriptions WHERE id = ?`,
					retiredID,
				); err != nil {
					return false, fmt.Errorf("retire push subscription: %w", err)
				}
			}
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

func isRetryablePushSubscriptionTransactionError(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
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

// DeletePushSubscriptionsByEndpoint removes the current user's endpoint
// regardless of which tab most recently registered it.
func (a *Adapter) DeletePushSubscriptionsByEndpoint(ctx context.Context, uid int64, endpoint string) error {
	if _, err := a.db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE uid = ? AND endpoint = ?`,
		uid,
		endpoint,
	); err != nil {
		return fmt.Errorf("delete push subscriptions by endpoint: %w", err)
	}
	return nil
}
