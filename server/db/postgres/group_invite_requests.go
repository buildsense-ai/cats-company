package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type groupInviteScanner interface {
	Scan(dest ...interface{}) error
}

var _ store.GroupInviteRequestStore = (*Adapter)(nil)

func scanGroupInviteRequest(row groupInviteScanner) (*types.GroupInviteRequest, error) {
	request := &types.GroupInviteRequest{}
	var resolverID sql.NullInt64
	var inviteeAvatar sql.NullString
	if err := row.Scan(
		&request.ID,
		&request.GroupID,
		&request.InviterID,
		&request.InviteeID,
		&resolverID,
		&request.Status,
		&request.CreatedAt,
		&request.UpdatedAt,
		&request.InviterUsername,
		&request.InviterDisplayName,
		&request.InviteeUsername,
		&request.InviteeDisplayName,
		&inviteeAvatar,
		&request.InviteeIsBot,
	); err != nil {
		return nil, err
	}
	if resolverID.Valid {
		request.ResolverID = resolverID.Int64
	}
	if inviteeAvatar.Valid {
		request.InviteeAvatarURL = inviteeAvatar.String
	}
	return request, nil
}

const groupInviteRequestSelect = `
SELECT r.id, r.group_id, r.inviter_id, r.invitee_id, r.resolver_id,
       r.status, r.created_at, r.updated_at,
       inviter.username, inviter.display_name,
       invitee.username, invitee.display_name, invitee.avatar_url,
       (invitee.account_type = 'bot' AND COALESCE(invitee.bot_disclose, false))
FROM group_invite_requests r
JOIN users inviter ON inviter.id = r.inviter_id
JOIN users invitee ON invitee.id = r.invitee_id`

func (a *Adapter) CreateGroupInviteRequest(groupID, inviterID, inviteeID int64) (*types.GroupInviteRequest, error) {
	var requestID int64
	err := a.db.QueryRow(
		`INSERT INTO group_invite_requests (group_id, inviter_id, invitee_id)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (group_id, invitee_id) DO UPDATE SET
		   inviter_id = CASE
		     WHEN group_invite_requests.status = 'pending' THEN group_invite_requests.inviter_id
		     ELSE EXCLUDED.inviter_id
		   END,
		   status = 'pending',
		   resolver_id = NULL,
		   created_at = CASE
		     WHEN group_invite_requests.status = 'pending' THEN group_invite_requests.created_at
		     ELSE CURRENT_TIMESTAMP
		   END,
		   updated_at = CURRENT_TIMESTAMP
		 RETURNING id`,
		groupID, inviterID, inviteeID,
	).Scan(&requestID)
	if err != nil {
		return nil, fmt.Errorf("create group invite request: %w", err)
	}
	return a.GetGroupInviteRequest(requestID)
}

func (a *Adapter) GetGroupInviteRequest(requestID int64) (*types.GroupInviteRequest, error) {
	request, err := scanGroupInviteRequest(a.db.QueryRow(groupInviteRequestSelect+` WHERE r.id = $1`, requestID))
	if err != nil {
		return nil, fmt.Errorf("get group invite request: %w", err)
	}
	return request, nil
}

func (a *Adapter) ListPendingGroupInviteRequests(groupID int64) ([]*types.GroupInviteRequest, error) {
	rows, err := a.db.Query(groupInviteRequestSelect+` WHERE r.group_id = $1 AND r.status = 'pending' ORDER BY r.created_at ASC, r.id ASC`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group invite requests: %w", err)
	}
	defer rows.Close()

	requests := make([]*types.GroupInviteRequest, 0)
	for rows.Next() {
		request, scanErr := scanGroupInviteRequest(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan group invite request: %w", scanErr)
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (a *Adapter) ApproveGroupInviteRequest(requestID, resolverID int64) (*types.GroupInviteRequest, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("approve group invite begin: %w", err)
	}
	defer tx.Rollback()

	var groupID, inviteeID int64
	var status types.GroupInviteStatus
	if err := tx.QueryRow(`SELECT group_id, invitee_id, status FROM group_invite_requests WHERE id = $1 FOR UPDATE`, requestID).Scan(&groupID, &inviteeID, &status); err != nil {
		return nil, fmt.Errorf("approve group invite lookup: %w", err)
	}
	if status != types.GroupInvitePending {
		return nil, store.ErrGroupInviteRequestNotPending
	}
	if _, err := tx.Exec(`INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT (group_id, user_id) DO NOTHING`, groupID, inviteeID); err != nil {
		return nil, fmt.Errorf("approve group invite add member: %w", err)
	}
	if _, err := tx.Exec(`UPDATE group_invite_requests SET status = 'approved', resolver_id = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, resolverID, requestID); err != nil {
		return nil, fmt.Errorf("approve group invite update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("approve group invite commit: %w", err)
	}
	return a.GetGroupInviteRequest(requestID)
}

func (a *Adapter) RejectGroupInviteRequest(requestID, resolverID int64) (*types.GroupInviteRequest, error) {
	result, err := a.db.Exec(
		`UPDATE group_invite_requests
		 SET status = 'rejected', resolver_id = $1, updated_at = CURRENT_TIMESTAMP
		 WHERE id = $2 AND status = 'pending'`,
		resolverID, requestID,
	)
	if err != nil {
		return nil, fmt.Errorf("reject group invite request: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reject group invite rows: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrGroupInviteRequestNotPending
	}
	request, err := a.GetGroupInviteRequest(requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrGroupInviteRequestNotPending
	}
	return request, err
}
