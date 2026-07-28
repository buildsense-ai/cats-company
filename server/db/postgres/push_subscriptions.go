package postgres

import (
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// UpsertPushSubscription creates or refreshes the subscription identified by
// its globally unique endpoint.
func (a *Adapter) UpsertPushSubscription(subscription *types.PushSubscription) error {
	if subscription == nil {
		return fmt.Errorf("push subscription is nil")
	}
	_, err := a.db.Exec(
		`INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (endpoint) DO UPDATE SET
		   uid = EXCLUDED.uid,
		   p256dh = EXCLUDED.p256dh,
		   auth = EXCLUDED.auth`,
		subscription.UID,
		subscription.Endpoint,
		subscription.P256DH,
		subscription.Auth,
	)
	if err != nil {
		return fmt.Errorf("upsert push subscription: %w", err)
	}
	return nil
}

// ListPushSubscriptions returns all subscriptions owned by uid.
func (a *Adapter) ListPushSubscriptions(uid int64) ([]*types.PushSubscription, error) {
	rows, err := a.db.Query(
		`SELECT id, uid, endpoint, p256dh, auth, created_at, updated_at
		 FROM push_subscriptions
		 WHERE uid = $1
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
func (a *Adapter) DeletePushSubscription(uid int64, endpoint string) error {
	if _, err := a.db.Exec(
		`DELETE FROM push_subscriptions WHERE uid = $1 AND endpoint = $2`,
		uid,
		endpoint,
	); err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}

// DeletePushSubscriptionByEndpoint removes an endpoint regardless of owner.
func (a *Adapter) DeletePushSubscriptionByEndpoint(endpoint string) error {
	if _, err := a.db.Exec(
		`DELETE FROM push_subscriptions WHERE endpoint = $1`,
		endpoint,
	); err != nil {
		return fmt.Errorf("delete push subscription by endpoint: %w", err)
	}
	return nil
}
