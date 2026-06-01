package mysql

import (
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// ListAccessibleAgents returns every agent the user can see in the workspace.
func (a *Adapter) ListAccessibleAgents(userUID int64) ([]*types.AgentRosterItem, error) {
	items := make([]*types.AgentRosterItem, 0)
	seen := make(map[int64]bool)

	owned, err := a.queryOwnedAgents(userUID)
	if err != nil {
		return nil, err
	}
	for _, item := range owned {
		seen[item.ID] = true
		items = append(items, item)
	}

	explicit, err := a.queryExplicitAgents(userUID)
	if err != nil {
		return nil, err
	}
	for _, item := range explicit {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
	}

	publicAgents, err := a.queryPublicAgents(userUID)
	if err != nil {
		return nil, err
	}
	for _, item := range publicAgents {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
	}

	return items, nil
}

// GetAccessibleAgent returns the effective access state for a user and agent.
func (a *Adapter) GetAccessibleAgent(agentUID, userUID int64) (*types.AgentRosterItem, error) {
	item, err := a.queryAgentProfile(agentUID, userUID)
	if err != nil || item == nil {
		return item, err
	}
	if item.OwnerID == userUID {
		item.Status = types.AgentAccessActive
		item.Permission = types.AgentPermissionManage
		item.Source = "owner"
		item.CanChat = true
		item.CanManage = true
		item.TopicID = agentP2PTopicID(userUID, agentUID)
		return item, nil
	}

	access, err := a.GetAgentAccess(agentUID, userUID)
	if err != nil {
		return nil, err
	}
	if access != nil && access.Status != types.AgentAccessRevoked {
		item.Status = access.Status
		item.Permission = access.Permission
		item.Source = access.Source
		item.CanChat = access.Status == types.AgentAccessActive && access.Permission != types.AgentPermissionView
		item.CanManage = access.Status == types.AgentAccessActive && access.Permission == types.AgentPermissionManage
		item.TopicID = agentP2PTopicID(userUID, agentUID)
		return item, nil
	}

	if item.Visibility == types.BotPublic {
		item.Status = types.AgentAccessActive
		item.Permission = types.AgentPermissionUse
		item.Source = "public"
		item.CanChat = true
		item.CanManage = false
		item.TopicID = agentP2PTopicID(userUID, agentUID)
		return item, nil
	}

	return nil, nil
}

func (a *Adapter) queryOwnedAgents(userUID int64) ([]*types.AgentRosterItem, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        COALESCE(b.owner_id, 0), COALESCE(b.visibility, 'public')
		   FROM users u
		   JOIN bot_config b ON b.user_id = u.id
		  WHERE u.account_type = 'bot'
		    AND u.state = 0
		    AND COALESCE(b.enabled, 1) = 1
		    AND b.owner_id = ?
		  ORDER BY u.created_at`,
		userUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list owned agents: %w", err)
	}
	defer rows.Close()

	var items []*types.AgentRosterItem
	for rows.Next() {
		item := &types.AgentRosterItem{
			Status:     types.AgentAccessActive,
			Permission: types.AgentPermissionManage,
			Source:     "owner",
			CanChat:    true,
			CanManage:  true,
		}
		var visibility string
		if err := rows.Scan(&item.ID, &item.Username, &item.DisplayName, &item.AvatarURL, &item.OwnerID, &visibility); err != nil {
			return nil, fmt.Errorf("scan owned agent: %w", err)
		}
		item.Visibility = types.BotVisibility(visibility)
		item.TopicID = agentP2PTopicID(userUID, item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *Adapter) queryExplicitAgents(userUID int64) ([]*types.AgentRosterItem, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        COALESCE(b.owner_id, 0), COALESCE(b.visibility, 'public'),
		        aa.status, aa.permission, aa.source
		   FROM agent_access aa
		   JOIN users u ON u.id = aa.agent_uid
		   JOIN bot_config b ON b.user_id = u.id
		  WHERE aa.user_uid = ?
		    AND aa.status <> 'revoked'
		    AND u.account_type = 'bot'
		    AND u.state = 0
		    AND COALESCE(b.enabled, 1) = 1
		  ORDER BY aa.updated_at DESC`,
		userUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list explicit agents: %w", err)
	}
	defer rows.Close()

	var items []*types.AgentRosterItem
	for rows.Next() {
		item := &types.AgentRosterItem{}
		var visibility, status, permission string
		if err := rows.Scan(&item.ID, &item.Username, &item.DisplayName, &item.AvatarURL, &item.OwnerID, &visibility, &status, &permission, &item.Source); err != nil {
			return nil, fmt.Errorf("scan explicit agent: %w", err)
		}
		item.Visibility = types.BotVisibility(visibility)
		item.Status = types.AgentAccessStatus(status)
		item.Permission = types.AgentPermission(permission)
		item.CanChat = item.Status == types.AgentAccessActive && item.Permission != types.AgentPermissionView
		item.CanManage = item.Status == types.AgentAccessActive && item.Permission == types.AgentPermissionManage
		item.TopicID = agentP2PTopicID(userUID, item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *Adapter) queryPublicAgents(userUID int64) ([]*types.AgentRosterItem, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        COALESCE(b.owner_id, 0), COALESCE(b.visibility, 'public')
		   FROM users u
		   JOIN bot_config b ON b.user_id = u.id
		  WHERE u.account_type = 'bot'
		    AND u.state = 0
		    AND COALESCE(b.enabled, 1) = 1
		    AND COALESCE(b.visibility, 'public') = 'public'
		    AND COALESCE(b.owner_id, 0) <> ?
		    AND NOT EXISTS (
		      SELECT 1 FROM agent_access aa
		       WHERE aa.agent_uid = u.id AND aa.user_uid = ?
		         AND aa.status IN ('pending_accept','active','blocked')
		    )
		  ORDER BY u.created_at`,
		userUID, userUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list public agents: %w", err)
	}
	defer rows.Close()

	var items []*types.AgentRosterItem
	for rows.Next() {
		item := &types.AgentRosterItem{
			Status:     types.AgentAccessActive,
			Permission: types.AgentPermissionUse,
			Source:     "public",
			CanChat:    true,
			CanManage:  false,
		}
		var visibility string
		if err := rows.Scan(&item.ID, &item.Username, &item.DisplayName, &item.AvatarURL, &item.OwnerID, &visibility); err != nil {
			return nil, fmt.Errorf("scan public agent: %w", err)
		}
		item.Visibility = types.BotVisibility(visibility)
		item.TopicID = agentP2PTopicID(userUID, item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *Adapter) queryAgentProfile(agentUID, userUID int64) (*types.AgentRosterItem, error) {
	item := &types.AgentRosterItem{}
	var visibility string
	err := a.db.QueryRow(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        COALESCE(b.owner_id, 0), COALESCE(b.visibility, 'public')
		   FROM users u
		   JOIN bot_config b ON b.user_id = u.id
		  WHERE u.id = ?
		    AND u.account_type = 'bot'
		    AND u.state = 0
		    AND COALESCE(b.enabled, 1) = 1`,
		agentUID,
	).Scan(&item.ID, &item.Username, &item.DisplayName, &item.AvatarURL, &item.OwnerID, &visibility)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent profile: %w", err)
	}
	item.Visibility = types.BotVisibility(visibility)
	item.TopicID = agentP2PTopicID(userUID, agentUID)
	return item, nil
}

// ListAgentAccess returns permission records for one agent.
func (a *Adapter) ListAgentAccess(agentUID int64) ([]*types.AgentAccess, error) {
	rows, err := a.db.Query(
		`SELECT aa.id, aa.agent_uid, aa.user_uid,
		        u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        aa.status, aa.permission, aa.source,
		        COALESCE(aa.invited_by, 0), COALESCE(inviter.username, ''),
		        aa.accepted_at, aa.created_at, aa.updated_at
		   FROM agent_access aa
		   JOIN users u ON u.id = aa.user_uid
		   LEFT JOIN users inviter ON inviter.id = aa.invited_by
		  WHERE aa.agent_uid = ?
		  ORDER BY aa.updated_at DESC`,
		agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agent access: %w", err)
	}
	defer rows.Close()
	return scanAgentAccessRows(rows, "scan agent access")
}

// GetAgentAccess returns one user's explicit permission record for an agent.
func (a *Adapter) GetAgentAccess(agentUID, userUID int64) (*types.AgentAccess, error) {
	rows, err := a.db.Query(
		`SELECT aa.id, aa.agent_uid, aa.user_uid,
		        u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        aa.status, aa.permission, aa.source,
		        COALESCE(aa.invited_by, 0), COALESCE(inviter.username, ''),
		        aa.accepted_at, aa.created_at, aa.updated_at
		   FROM agent_access aa
		   JOIN users u ON u.id = aa.user_uid
		   LEFT JOIN users inviter ON inviter.id = aa.invited_by
		  WHERE aa.agent_uid = ? AND aa.user_uid = ?
		  LIMIT 1`,
		agentUID, userUID,
	)
	if err != nil {
		return nil, fmt.Errorf("get agent access: %w", err)
	}
	defer rows.Close()
	items, err := scanAgentAccessRows(rows, "scan agent access")
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return items[0], nil
}

// UpsertAgentAccess invites or rewrites one user's permission for an agent.
func (a *Adapter) UpsertAgentAccess(agentUID, userUID, invitedBy int64, permission, status, source string) (*types.AgentAccess, error) {
	res, err := a.db.Exec(
		`INSERT INTO agent_access (agent_uid, user_uid, permission, status, source, invited_by, accepted_at)
		 VALUES (?, ?, ?, ?, ?, ?, CASE WHEN ? = 'active' THEN CURRENT_TIMESTAMP ELSE NULL END)
		 ON DUPLICATE KEY UPDATE
		   permission = VALUES(permission),
		   status = VALUES(status),
		   source = VALUES(source),
		   invited_by = VALUES(invited_by),
		   accepted_at = CASE WHEN VALUES(status) = 'active' THEN COALESCE(accepted_at, CURRENT_TIMESTAMP) ELSE NULL END`,
		agentUID, userUID, permission, status, source, invitedBy, status,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert agent access: %w", err)
	}
	id, _ := res.LastInsertId()
	if id > 0 {
		return a.getAgentAccessByID(id)
	}
	return a.GetAgentAccess(agentUID, userUID)
}

// UpdateAgentAccess updates an existing permission record.
func (a *Adapter) UpdateAgentAccess(accessID, agentUID int64, permission, status string) (*types.AgentAccess, error) {
	res, err := a.db.Exec(
		`UPDATE agent_access
		    SET permission = ?,
		        status = ?,
		        accepted_at = CASE
		          WHEN ? = 'active' THEN COALESCE(accepted_at, CURRENT_TIMESTAMP)
		          WHEN ? = 'pending_accept' THEN NULL
		          ELSE accepted_at
		        END
		  WHERE id = ? AND agent_uid = ?`,
		permission, status, status, status, accessID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("update agent access: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, nil
	}
	return a.getAgentAccessByID(accessID)
}

// RevokeAgentAccess disables one permission record without deleting the audit row.
func (a *Adapter) RevokeAgentAccess(accessID, agentUID int64) error {
	_, err := a.db.Exec(
		`UPDATE agent_access SET status = 'revoked' WHERE id = ? AND agent_uid = ?`,
		accessID, agentUID,
	)
	return err
}

// AcceptAgentInvite marks an invitation as usable by the target user.
func (a *Adapter) AcceptAgentInvite(agentUID, userUID int64) (*types.AgentAccess, error) {
	res, err := a.db.Exec(
		`UPDATE agent_access
		    SET status = 'active',
		        accepted_at = COALESCE(accepted_at, CURRENT_TIMESTAMP)
		  WHERE agent_uid = ? AND user_uid = ? AND status = 'pending_accept'`,
		agentUID, userUID,
	)
	if err != nil {
		return nil, fmt.Errorf("accept agent invite: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, nil
	}
	return a.GetAgentAccess(agentUID, userUID)
}

func (a *Adapter) getAgentAccessByID(id int64) (*types.AgentAccess, error) {
	rows, err := a.db.Query(
		`SELECT aa.id, aa.agent_uid, aa.user_uid,
		        u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        aa.status, aa.permission, aa.source,
		        COALESCE(aa.invited_by, 0), COALESCE(inviter.username, ''),
		        aa.accepted_at, aa.created_at, aa.updated_at
		   FROM agent_access aa
		   JOIN users u ON u.id = aa.user_uid
		   LEFT JOIN users inviter ON inviter.id = aa.invited_by
		  WHERE aa.id = ?
		  LIMIT 1`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("get agent access by id: %w", err)
	}
	defer rows.Close()
	items, err := scanAgentAccessRows(rows, "scan agent access")
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return items[0], nil
}

type agentAccessRows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}

func scanAgentAccessRows(rows agentAccessRows, context string) ([]*types.AgentAccess, error) {
	var items []*types.AgentAccess
	for rows.Next() {
		item := &types.AgentAccess{}
		var status, permission string
		var invitedBy int64
		var acceptedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.AgentUID, &item.UserUID,
			&item.Username, &item.DisplayName, &item.AvatarURL,
			&status, &permission, &item.Source,
			&invitedBy, &item.InvitedByName,
			&acceptedAt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("%s: %w", context, err)
		}
		item.Status = types.AgentAccessStatus(status)
		item.Permission = types.AgentPermission(permission)
		item.InvitedBy = invitedBy
		if acceptedAt.Valid {
			item.AcceptedAt = &acceptedAt.Time
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func agentP2PTopicID(uid1, uid2 int64) string {
	if uid1 > uid2 {
		uid1, uid2 = uid2, uid1
	}
	return fmt.Sprintf("p2p_%d_%d", uid1, uid2)
}
