package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// cloud_worker_official_body_id records the runtime body minted by the
// platform when a cloud-managed worker was provisioned. A non-empty value
// marks the bot as platform-managed: the WebSocket handler refuses any other
// body, repairs the durable binding when it drifted, and takes the lease over
// from a foreign runtime (see wshandler.ServeWS). Ordinary bots leave the
// column empty and keep the legacy first-writer-wins binding behavior.

// GetCloudWorkerOfficialBodyID returns the platform-minted body for a cloud
// worker, or an empty string when the bot is not platform-managed.
func (a *Adapter) GetCloudWorkerOfficialBodyID(botUID int64) (string, error) {
	if botUID <= 0 {
		return "", fmt.Errorf("invalid bot uid")
	}
	var bodyID string
	err := a.db.QueryRow(
		`SELECT COALESCE(cloud_official_body_id, '') FROM bot_config WHERE user_id = $1`,
		botUID,
	).Scan(&bodyID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get cloud worker official body: %w", err)
	}
	return strings.TrimSpace(bodyID), nil
}

// SetCloudWorkerOfficialBodyID records (or clears, with an empty body) the
// platform-minted body for a cloud worker.
func (a *Adapter) SetCloudWorkerOfficialBodyID(botUID int64, bodyID string) error {
	if botUID <= 0 {
		return fmt.Errorf("invalid bot uid")
	}
	result, err := a.db.Exec(
		`UPDATE bot_config SET cloud_official_body_id = $1 WHERE user_id = $2`,
		strings.TrimSpace(bodyID), botUID,
	)
	if err != nil {
		return fmt.Errorf("set cloud worker official body: %w", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("cloud worker not found")
	}
	return nil
}
