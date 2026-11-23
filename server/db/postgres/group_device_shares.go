package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

const createGroupDeviceShares = `
CREATE TABLE IF NOT EXISTS group_device_share_bindings (
 owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 device_id VARCHAR(128) NOT NULL,
 installation_id VARCHAR(128) NOT NULL DEFAULT '',
 epoch BIGINT NOT NULL CHECK (epoch > 0),
 active BOOLEAN NOT NULL,
 revoked_before_ms BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(owner_uid,device_id)
);
CREATE TABLE IF NOT EXISTS group_device_shares (
 id VARCHAR(64) PRIMARY KEY,
 owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 grantee_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 topic_id VARCHAR(200) NOT NULL CHECK (topic_id ~ '^grp_[1-9][0-9]*$'),
 device_id VARCHAR(128) NOT NULL,
 device_epoch BIGINT NOT NULL CHECK (device_epoch > 0),
 operations JSONB NOT NULL CHECK (jsonb_typeof(operations) = 'array'),
 status VARCHAR(16) NOT NULL CHECK (status IN ('active','revoked')),
 revision BIGINT NOT NULL CHECK (revision > 0),
 created_at_ms BIGINT NOT NULL,
 updated_at_ms BIGINT NOT NULL,
 CHECK (owner_uid <> grantee_uid),
 UNIQUE (owner_uid, grantee_uid, topic_id, device_id)
);
CREATE INDEX IF NOT EXISTS idx_group_device_shares_grantee ON group_device_shares(topic_id, grantee_uid);
CREATE TABLE IF NOT EXISTS group_device_share_requests (
 id VARCHAR(64) PRIMARY KEY,
 requester_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 topic_id VARCHAR(200) NOT NULL CHECK (topic_id ~ '^grp_[1-9][0-9]*$'),
 device_id VARCHAR(128) NOT NULL DEFAULT '',
 description TEXT NOT NULL DEFAULT '',
 client_request_id VARCHAR(128) NOT NULL,
 status VARCHAR(16) NOT NULL CHECK (status IN ('pending','approved','rejected','invalidated')),
 revision BIGINT NOT NULL CHECK (revision > 0),
 share_id VARCHAR(64) NOT NULL DEFAULT '',
 message_id BIGINT NOT NULL DEFAULT 0,
 created_at_ms BIGINT NOT NULL,
 updated_at_ms BIGINT NOT NULL,
 CHECK (requester_uid <> owner_uid),
 UNIQUE (requester_uid, topic_id, client_request_id)
);
`

var _ store.GroupDeviceShareStore = (*Adapter)(nil)

const shareColumns = "id, owner_uid, grantee_uid, topic_id, device_id, operations, status, revision, created_at_ms, updated_at_ms, device_epoch"
const shareRequestColumns = "id, requester_uid, owner_uid, topic_id, device_id, description, client_request_id, status, revision, share_id, message_id, created_at_ms, updated_at_ms"

type shareRow interface{ Scan(...any) error }
type shareQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanDeviceShare(row shareRow) (*store.GroupDeviceShare, error) {
	var s store.GroupDeviceShare
	var operations []byte
	err := row.Scan(&s.ID, &s.OwnerUID, &s.GranteeUID, &s.TopicID, &s.DeviceID, &operations, &s.Status, &s.Revision, &s.CreatedAt, &s.UpdatedAt, &s.DeviceEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(operations, &s.Operations); err != nil {
		return nil, err
	}
	return &s, nil
}

func scanDeviceShareRequest(row shareRow) (*store.GroupDeviceShareRequest, error) {
	var r store.GroupDeviceShareRequest
	err := row.Scan(&r.ID, &r.RequesterUID, &r.OwnerUID, &r.TopicID, &r.DeviceID, &r.Description, &r.ClientRequestID, &r.Status, &r.Revision, &r.ShareID, &r.MessageID, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (a *Adapter) ListGroupDeviceShares(ctx context.Context, topic string, uid int64) ([]store.GroupDeviceShare, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT "+shareColumns+" FROM group_device_shares WHERE topic_id=$1 AND (owner_uid=$2 OR grantee_uid=$2) ORDER BY id", topic, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shares := []store.GroupDeviceShare{}
	for rows.Next() {
		s, err := scanDeviceShare(rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, *s)
	}
	return shares, rows.Err()
}

func (a *Adapter) GetGroupDeviceShare(ctx context.Context, id string) (*store.GroupDeviceShare, error) {
	return scanDeviceShare(a.db.QueryRowContext(ctx, "SELECT "+shareColumns+" FROM group_device_shares WHERE id=$1", id))
}

func saveDeviceShare(ctx context.Context, q shareQuery, s store.GroupDeviceShare, expected int64) (*store.GroupDeviceShare, error) {
	ops, err := json.Marshal(s.Operations)
	if err != nil {
		return nil, err
	}
	if s.Status == "active" {
		var epoch int64
		var active bool
		if err := q.QueryRowContext(ctx, `SELECT epoch,active FROM group_device_share_bindings WHERE owner_uid=$1 AND device_id=$2 FOR UPDATE`, s.OwnerUID, s.DeviceID).Scan(&epoch, &active); err != nil {
			return nil, err
		}
		if !active || epoch != s.DeviceEpoch {
			return nil, store.ErrDeviceShareConflict
		}
	}
	if err := lockDeviceShareMembers(ctx, q, s.TopicID, s.OwnerUID, s.GranteeUID); err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `INSERT INTO group_device_shares (`+shareColumns+`) SELECT $1::varchar(64),$2::bigint,$3::bigint,$4::varchar(200),$5::varchar(128),$6::jsonb,$7::varchar(16),1,$8::bigint,$8::bigint,$10::bigint WHERE $9::bigint=0 OR EXISTS (SELECT 1 FROM group_device_shares WHERE owner_uid=$2 AND grantee_uid=$3 AND topic_id=$4 AND device_id=$5 AND revision=$9)
 ON CONFLICT (owner_uid,grantee_uid,topic_id,device_id) DO UPDATE SET
 operations=EXCLUDED.operations,status=EXCLUDED.status,device_epoch=EXCLUDED.device_epoch,revision=group_device_shares.revision+1,updated_at_ms=EXCLUDED.updated_at_ms
 WHERE group_device_shares.revision=$9 RETURNING `+shareColumns,
		s.ID, s.OwnerUID, s.GranteeUID, s.TopicID, s.DeviceID, string(ops), s.Status, s.UpdatedAt, expected, s.DeviceEpoch)
	saved, err := scanDeviceShare(row)
	if err != nil || saved != nil {
		return saved, err
	}
	current, err := scanDeviceShare(q.QueryRowContext(ctx, "SELECT "+shareColumns+" FROM group_device_shares WHERE owner_uid=$1 AND grantee_uid=$2 AND topic_id=$3 AND device_id=$4", s.OwnerUID, s.GranteeUID, s.TopicID, s.DeviceID))
	if err != nil {
		return nil, err
	}
	// Repeating the same committed transition is idempotent. An intervening
	// opposite transition always advances the revision and invalidates this retry.
	if current != nil && current.Revision == expected+1 && current.Status == s.Status && current.DeviceEpoch == s.DeviceEpoch && reflect.DeepEqual(current.Operations, s.Operations) {
		return current, nil
	}
	return nil, store.ErrDeviceShareConflict
}

func (a *Adapter) SaveGroupDeviceShare(ctx context.Context, s store.GroupDeviceShare, expected int64) (*store.GroupDeviceShare, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err := saveDeviceShare(ctx, tx, s, expected)
	if err != nil {
		return nil, err
	}
	if saved.Status == "revoked" {
		if _, err = tx.ExecContext(ctx, `UPDATE group_device_share_requests SET status='invalidated',revision=revision+1 WHERE owner_uid=$1 AND requester_uid=$2 AND topic_id=$3 AND status IN ('pending','approved') AND (device_id='' OR device_id=$4)`, saved.OwnerUID, saved.GranteeUID, saved.TopicID, saved.DeviceID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

func (a *Adapter) CreateGroupDeviceShareRequest(ctx context.Context, r store.GroupDeviceShareRequest) (*store.GroupDeviceShareRequest, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockDeviceShareMembers(ctx, tx, r.TopicID, r.OwnerUID, r.RequesterUID); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO group_device_share_requests (`+shareRequestColumns+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',1,'',0,$8,$8)
 ON CONFLICT (requester_uid,topic_id,client_request_id) DO NOTHING`, r.ID, r.RequesterUID, r.OwnerUID, r.TopicID, r.DeviceID, r.Description, r.ClientRequestID, r.CreatedAt)
	if err != nil {
		return nil, err
	}
	saved, err := scanDeviceShareRequest(tx.QueryRowContext(ctx, "SELECT "+shareRequestColumns+" FROM group_device_share_requests WHERE requester_uid=$1 AND topic_id=$2 AND client_request_id=$3", r.RequesterUID, r.TopicID, r.ClientRequestID))
	if err != nil {
		return nil, err
	}
	if saved == nil || saved.OwnerUID != r.OwnerUID || saved.DeviceID != r.DeviceID {
		return nil, store.ErrDeviceShareConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

func (a *Adapter) GetGroupDeviceShareRequest(ctx context.Context, id string) (*store.GroupDeviceShareRequest, error) {
	return scanDeviceShareRequest(a.db.QueryRowContext(ctx, "SELECT "+shareRequestColumns+" FROM group_device_share_requests WHERE id=$1", id))
}

func (a *Adapter) ResolveGroupDeviceShareRequest(ctx context.Context, id string, expected int64, status string, s *store.GroupDeviceShare) (*store.GroupDeviceShareRequest, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := scanDeviceShareRequest(tx.QueryRowContext(ctx, "SELECT "+shareRequestColumns+" FROM group_device_share_requests WHERE id=$1", id))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, store.ErrDeviceShareConflict
	}
	if r.Status == status && r.Revision == expected+1 {
		if status == "approved" && (s == nil || r.DeviceID != s.DeviceID) {
			return nil, store.ErrDeviceShareConflict
		}
		return r, nil
	}
	if r.Status != "pending" || r.Revision != expected {
		return nil, store.ErrDeviceShareConflict
	}
	if status == "approved" {
		if s == nil || s.OwnerUID != r.OwnerUID || s.GranteeUID != r.RequesterUID || s.TopicID != r.TopicID {
			return nil, store.ErrDeviceShareConflict
		}
		current, err := scanDeviceShare(tx.QueryRowContext(ctx, "SELECT "+shareColumns+" FROM group_device_shares WHERE owner_uid=$1 AND grantee_uid=$2 AND topic_id=$3 AND device_id=$4", s.OwnerUID, s.GranteeUID, s.TopicID, s.DeviceID))
		if err != nil {
			return nil, err
		}
		revision := int64(0)
		if current != nil {
			revision = current.Revision
		}
		saved, err := saveDeviceShare(ctx, tx, *s, revision)
		if err != nil {
			return nil, err
		}
		r.ShareID = saved.ID
		r.DeviceID = saved.DeviceID
	}
	// Acquire permission/binding locks before updating the request, matching
	// revocation and unlink. CAS prevents a concurrent rejection or invalidation
	// from being overwritten and rolls back any permission created above.
	resolved, err := scanDeviceShareRequest(tx.QueryRowContext(ctx, "UPDATE group_device_share_requests SET status=$1,revision=revision+1,share_id=$2,device_id=$3,updated_at_ms=(EXTRACT(EPOCH FROM clock_timestamp())*1000)::bigint WHERE id=$4 AND status='pending' AND revision=$5 RETURNING "+shareRequestColumns, status, r.ShareID, r.DeviceID, r.ID, expected))
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		_ = tx.Rollback()
		latest, readErr := a.GetGroupDeviceShareRequest(ctx, id)
		if readErr != nil {
			return nil, readErr
		}
		if latest != nil && latest.Status == status && latest.Revision == expected+1 && (status != "approved" || (s != nil && latest.DeviceID == s.DeviceID)) {
			return latest, nil
		}
		return nil, store.ErrDeviceShareConflict
	}
	r = resolved
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func (a *Adapter) SetGroupDeviceShareRequestMessage(ctx context.Context, id string, messageID int64) error {
	_, err := a.db.ExecContext(ctx, "UPDATE group_device_share_requests SET message_id=$1 WHERE id=$2 AND message_id=0", messageID, id)
	return err
}

func (a *Adapter) GetDeviceShareBinding(ctx context.Context, owner int64, device string) (*store.GroupDeviceShareBinding, error) {
	var binding store.GroupDeviceShareBinding
	err := a.db.QueryRowContext(ctx, `SELECT epoch,active FROM group_device_share_bindings WHERE owner_uid=$1 AND device_id=$2`, owner, device).Scan(&binding.Epoch, &binding.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func (a *Adapter) RegisterDeviceShareBinding(ctx context.Context, owner int64, device, installation string, issuedAt int64) error {
	var epoch int64
	err := a.db.QueryRowContext(ctx, `INSERT INTO group_device_share_bindings(owner_uid,device_id,installation_id,epoch,active)
 VALUES($1,$2,$3,1,true) ON CONFLICT(owner_uid,device_id) DO UPDATE SET
 epoch=CASE WHEN NOT group_device_share_bindings.active OR group_device_share_bindings.installation_id<>EXCLUDED.installation_id THEN group_device_share_bindings.epoch+1 ELSE group_device_share_bindings.epoch END,
 active=true,installation_id=EXCLUDED.installation_id
 WHERE group_device_share_bindings.revoked_before_ms<$4
 RETURNING epoch`, owner, device, installation, issuedAt).Scan(&epoch)
	return err
}

func (a *Adapter) InvalidateDeviceShares(ctx context.Context, owner int64, device string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO group_device_share_bindings(owner_uid,device_id,epoch,active,revoked_before_ms)
 VALUES($1,$2,1,false,(EXTRACT(EPOCH FROM clock_timestamp())*1000)::bigint)
 ON CONFLICT(owner_uid,device_id) DO UPDATE SET epoch=group_device_share_bindings.epoch+1,active=false,
 revoked_before_ms=(EXTRACT(EPOCH FROM clock_timestamp())*1000)::bigint`, owner, device)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE group_device_shares SET status='revoked',revision=revision+1,updated_at_ms=(EXTRACT(EPOCH FROM clock_timestamp())*1000)::bigint WHERE owner_uid=$1 AND device_id=$2 AND status='active'`, owner, device)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE group_device_share_requests SET status='invalidated',revision=revision+1 WHERE owner_uid=$1 AND (status='pending' OR (status='approved' AND device_id=$2))`, owner, device)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func lockDeviceShareMembers(ctx context.Context, q shareQuery, topic string, owner, actor int64) error {
	var members int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT user_id FROM group_members WHERE group_id=split_part($1,'_',2)::bigint AND user_id IN ($2,$3) ORDER BY user_id FOR KEY SHARE) members`, topic, owner, actor).Scan(&members)
	if err != nil {
		return err
	}
	if members != 2 {
		return store.ErrDeviceShareConflict
	}
	return nil
}
