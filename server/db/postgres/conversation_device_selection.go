package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openchat/openchat/server/store"
)

var _ store.ConversationDeviceSelectionStore = (*Adapter)(nil)

func (a *Adapter) GetConversationDeviceSelection(ctx context.Context, actor int64, session string) (*store.ConversationDeviceSelection, error) {
	if err := store.ValidateDeviceSelectionScope(actor, session); err != nil {
		return nil, err
	}
	var record store.ConversationDeviceSelection
	err := a.db.QueryRowContext(ctx, `SELECT device_id, revision, source, updated_at_ms, COALESCE((to_jsonb(conversation_device_selections)->>'owner_uid')::bigint,0) FROM conversation_device_selections WHERE actor_uid = $1 AND session_key = $2`, actor, session).
		Scan(&record.DeviceID, &record.Revision, &record.Source, &record.UpdatedAt, &record.OwnerUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (a *Adapter) CompareConversationDeviceSelection(ctx context.Context, actor int64, session, expected string, next store.ConversationDeviceSelection) (bool, error) {
	if err := store.ValidateDeviceSelectionScope(actor, session); err != nil {
		return false, err
	}
	if err := store.ValidateDeviceSelectionRecord(expected, next); err != nil {
		return false, err
	}
	var result sql.Result
	var err error
	if next.OwnerUID > 0 {
		if expected == "" {
			result, err = a.db.ExecContext(ctx, `INSERT INTO conversation_device_selections (actor_uid,session_key,device_id,revision,source,updated_at_ms,owner_uid) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (actor_uid,session_key) DO NOTHING`, actor, session, next.DeviceID, next.Revision, next.Source, next.UpdatedAt, next.OwnerUID)
		} else {
			result, err = a.db.ExecContext(ctx, `UPDATE conversation_device_selections SET device_id=$1,revision=$2,source=$3,updated_at_ms=$4,owner_uid=$5 WHERE actor_uid=$6 AND session_key=$7 AND revision=$8`, next.DeviceID, next.Revision, next.Source, next.UpdatedAt, next.OwnerUID, actor, session, expected)
		}
	} else if expected == "" {
		result, err = a.db.ExecContext(ctx, `INSERT INTO conversation_device_selections (actor_uid, session_key, device_id, revision, source, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (actor_uid, session_key) DO NOTHING`, actor, session, next.DeviceID, next.Revision, next.Source, next.UpdatedAt)
	} else {
		result, err = a.db.ExecContext(ctx, `UPDATE conversation_device_selections SET device_id = $1, revision = $2, source = $3, updated_at_ms = $4 WHERE actor_uid = $5 AND session_key = $6 AND revision = $7`, next.DeviceID, next.Revision, next.Source, next.UpdatedAt, actor, session, expected)
	}
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

const createConversationDeviceSelectionsTable = `
CREATE TABLE IF NOT EXISTS conversation_device_selections (
    actor_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_key VARCHAR(768) COLLATE "C" NOT NULL,
    device_id VARCHAR(128) NOT NULL,
    revision VARCHAR(64) COLLATE "C" NOT NULL,
    source VARCHAR(64) NOT NULL,
    updated_at_ms BIGINT NOT NULL,
    PRIMARY KEY (actor_uid, session_key)
);
`

const upgradeConversationDeviceSelectionOwner = `ALTER TABLE conversation_device_selections ADD COLUMN IF NOT EXISTS owner_uid BIGINT;`
