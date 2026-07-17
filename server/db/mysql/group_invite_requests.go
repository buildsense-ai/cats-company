package mysql

import (
	"database/sql"
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
       (invitee.account_type = 'bot' AND COALESCE(invitee.bot_disclose, 0) = 1)
FROM group_invite_requests r
JOIN users inviter ON inviter.id = r.inviter_id
JOIN users invitee ON invitee.id = r.invitee_id`

func (a *Adapter) CreateGroupInviteRequest(groupID, inviterID, inviteeID int64) (*types.GroupInviteRequest, error) {
	result, err := a.db.Exec(
		`INSERT INTO group_invite_requests (group_id, inviter_id, invitee_id)
		 VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   id = LAST_INSERT_ID(id),
		   inviter_id = IF(status = 'pending', inviter_id, VALUES(inviter_id)),
		   created_at = IF(status = 'pending', created_at, CURRENT_TIMESTAMP),
		   status = 'pending',
		   resolver_id = NULL,
		   updated_at = CURRENT_TIMESTAMP`,
		groupID, inviterID, inviteeID,
	)
	if err != nil {
		return nil, fmt.Errorf("create group invite request: %w", err)
	}
	requestID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create group invite request id: %w", err)
	}
	return a.GetGroupInviteRequest(requestID)
}

func (a *Adapter) GetGroupInviteRequest(requestID int64) (*types.GroupInviteRequest, error) {
	request, err := scanGroupInviteRequest(a.db.QueryRow(groupInviteRequestSelect+` WHERE r.id = ?`, requestID))
	if err != nil {
		return nil, fmt.Errorf("get group invite request: %w", err)
	}
	return request, nil
}

func (a *Adapter) ListPendingGroupInviteRequests(groupID int64) ([]*types.GroupInviteRequest, error) {
	rows, err := a.db.Query(groupInviteRequestSelect+` WHERE r.group_id = ? AND r.status = 'pending' ORDER BY r.created_at ASC, r.id ASC`, groupID)
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
	if err := tx.QueryRow(`SELECT group_id, invitee_id, status FROM group_invite_requests WHERE id = ? FOR UPDATE`, requestID).Scan(&groupID, &inviteeID, &status); err != nil {
		return nil, fmt.Errorf("approve group invite lookup: %w", err)
	}
	if status != types.GroupInvitePending {
		return nil, store.ErrGroupInviteRequestNotPending
	}
	if _, err := tx.Exec(`INSERT IGNORE INTO group_members (group_id, user_id, role) VALUES (?, ?, 'member')`, groupID, inviteeID); err != nil {
		return nil, fmt.Errorf("approve group invite add member: %w", err)
	}
	if _, err := tx.Exec(`UPDATE group_invite_requests SET status = 'approved', resolver_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, resolverID, requestID); err != nil {
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
		 SET status = 'rejected', resolver_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'pending'`,
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
	return a.GetGroupInviteRequest(requestID)
}
