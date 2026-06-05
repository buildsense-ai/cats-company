package postgres

import (
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// CreateGroup creates a new group, adds the owner as a member, and creates the group topic.
func (a *Adapter) CreateGroup(name string, ownerID int64) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("create group begin tx: %w", err)
	}
	defer tx.Rollback()

	var groupID int64
	if err := tx.QueryRow(
		`INSERT INTO "groups" (name, owner_id) VALUES ($1, $2) RETURNING id`,
		name, ownerID,
	).Scan(&groupID); err != nil {
		return 0, fmt.Errorf("create group insert: %w", err)
	}

	if _, err := tx.Exec(
		`INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'owner')`,
		groupID, ownerID,
	); err != nil {
		return 0, fmt.Errorf("create group add owner: %w", err)
	}

	topicID := fmt.Sprintf("grp_%d", groupID)
	if _, err := tx.Exec(
		`INSERT INTO topics (id, type, name, owner_id) VALUES ($1, 'group', $2, $3)
		 ON CONFLICT (id) DO NOTHING`,
		topicID, name, ownerID,
	); err != nil {
		return 0, fmt.Errorf("create group topic: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("create group commit: %w", err)
	}
	return groupID, nil
}

// GetGroup returns a group by ID.
func (a *Adapter) GetGroup(groupID int64) (*types.Group, error) {
	g := &types.Group{}
	var avatarURL *string
	var announcement *string
	err := a.db.QueryRow(
		`SELECT id, name, owner_id, avatar_url, announcement, max_members, created_at FROM "groups" WHERE id = $1`,
		groupID,
	).Scan(&g.ID, &g.Name, &g.OwnerID, &avatarURL, &announcement, &g.MaxMembers, &g.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get group: %w", err)
	}
	if avatarURL != nil {
		g.AvatarURL = *avatarURL
	}
	if announcement != nil {
		g.Announcement = *announcement
	}
	return g, nil
}

// AddGroupMember adds a user to a group with the given role.
func (a *Adapter) AddGroupMember(groupID, userID int64, role string) error {
	_, err := a.db.Exec(
		`INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, $3)`,
		groupID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("add group member: %w", err)
	}
	return nil
}

// RemoveGroupMember removes a user from a group.
func (a *Adapter) RemoveGroupMember(groupID, userID int64) error {
	_, err := a.db.Exec(
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`,
		groupID, userID,
	)
	if err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	return nil
}

// GetGroupMembers returns all members of a group with user info.
func (a *Adapter) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	rows, err := a.db.Query(
		`SELECT gm.id, gm.group_id, gm.user_id, gm.role, COALESCE(gm.muted, false), gm.joined_at,
		        u.username, u.display_name, u.avatar_url,
		        u.account_type, COALESCE(u.bot_disclose, false)
		 FROM group_members gm
		 JOIN users u ON u.id = gm.user_id
		 WHERE gm.group_id = $1
		 ORDER BY gm.joined_at ASC`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("get group members: %w", err)
	}
	defer rows.Close()

	var members []*types.GroupMember
	for rows.Next() {
		m := &types.GroupMember{}
		var avatarURL *string
		var acctType string
		var botDisclose bool
		if err := rows.Scan(&m.ID, &m.GroupID, &m.UserID, &m.Role, &m.Muted, &m.JoinedAt,
			&m.Username, &m.DisplayName, &avatarURL, &acctType, &botDisclose); err != nil {
			return nil, fmt.Errorf("scan group member: %w", err)
		}
		if avatarURL != nil {
			m.AvatarURL = *avatarURL
		}
		if botDisclose && acctType == "bot" {
			m.IsBot = true
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// GetUserGroups returns all groups a user belongs to.
func (a *Adapter) GetUserGroups(userID int64) ([]*types.Group, error) {
	rows, err := a.db.Query(
		`SELECT g.id, g.name, g.owner_id, g.avatar_url, g.max_members, g.created_at,
		        EXISTS(
		          SELECT 1
		          FROM group_members gm_bot
		          JOIN users u_bot ON u_bot.id = gm_bot.user_id
		          WHERE gm_bot.group_id = g.id AND u_bot.account_type = 'bot'
		        ) AS has_bot
		 FROM "groups" g
		 JOIN group_members gm ON gm.group_id = g.id
		 WHERE gm.user_id = $1
		 ORDER BY g.created_at DESC, g.id DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get user groups: %w", err)
	}
	defer rows.Close()

	var groups []*types.Group
	for rows.Next() {
		g := &types.Group{}
		var avatarURL *string
		if err := rows.Scan(&g.ID, &g.Name, &g.OwnerID, &avatarURL, &g.MaxMembers, &g.CreatedAt, &g.HasBot); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		if avatarURL != nil {
			g.AvatarURL = *avatarURL
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// IsGroupMember checks if a user is a member of a group.
func (a *Adapter) IsGroupMember(groupID, userID int64) (bool, error) {
	var count int
	err := a.db.QueryRow(
		`SELECT COUNT(*) FROM group_members WHERE group_id = $1 AND user_id = $2`,
		groupID, userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("is group member: %w", err)
	}
	return count > 0, nil
}

// GetGroupMemberCount returns the number of members in a group.
func (a *Adapter) GetGroupMemberCount(groupID int64) (int, error) {
	var count int
	err := a.db.QueryRow(
		`SELECT COUNT(*) FROM group_members WHERE group_id = $1`,
		groupID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get group member count: %w", err)
	}
	return count, nil
}

// GetGroupBotCount returns the number of bot members in a group.
func (a *Adapter) GetGroupBotCount(groupID int64) (int, error) {
	var count int
	err := a.db.QueryRow(
		`SELECT COUNT(*) FROM group_members gm
		 JOIN users u ON u.id = gm.user_id
		 WHERE gm.group_id = $1 AND u.account_type = 'bot'`,
		groupID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get group bot count: %w", err)
	}
	return count, nil
}

// UpdateMemberRole updates a member's role in a group.
func (a *Adapter) UpdateMemberRole(groupID, userID int64, role string) error {
	_, err := a.db.Exec(
		`UPDATE group_members SET role = $1 WHERE group_id = $2 AND user_id = $3`,
		role, groupID, userID,
	)
	if err != nil {
		return fmt.Errorf("update member role: %w", err)
	}
	return nil
}

// DeleteGroup deletes a group and all its members.
func (a *Adapter) DeleteGroup(groupID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("delete group begin tx: %w", err)
	}
	defer tx.Rollback()

	topicID := fmt.Sprintf("grp_%d", groupID)
	if _, err := tx.Exec(`DELETE FROM messages WHERE topic_id = $1`, topicID); err != nil {
		return fmt.Errorf("delete group messages: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM topics WHERE id = $1`, topicID); err != nil {
		return fmt.Errorf("delete group topic: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM "groups" WHERE id = $1`, groupID); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	return tx.Commit()
}

// GetMemberRole returns the role of a user in a group.
func (a *Adapter) GetMemberRole(groupID, userID int64) (string, error) {
	var role string
	err := a.db.QueryRow(
		`SELECT role FROM group_members WHERE group_id = $1 AND user_id = $2`,
		groupID, userID,
	).Scan(&role)
	if err != nil {
		return "", err
	}
	return role, nil
}

// IsMemberMuted checks if a member is muted in a group.
func (a *Adapter) IsMemberMuted(groupID, userID int64) (bool, error) {
	var muted bool
	err := a.db.QueryRow(
		`SELECT COALESCE(muted, false) FROM group_members WHERE group_id = $1 AND user_id = $2`,
		groupID, userID,
	).Scan(&muted)
	if err != nil {
		return false, err
	}
	return muted, nil
}

// SetMemberMuted sets the muted status for a group member.
func (a *Adapter) SetMemberMuted(groupID, userID int64, muted bool) error {
	_, err := a.db.Exec(
		`UPDATE group_members SET muted = $1 WHERE group_id = $2 AND user_id = $3`,
		muted, groupID, userID,
	)
	if err != nil {
		return fmt.Errorf("set member muted: %w", err)
	}
	return nil
}

// CanManageMember checks if actor can manage target in a group.
func (a *Adapter) CanManageMember(groupID, actorID, targetID int64) (bool, error) {
	actorRole, err := a.GetMemberRole(groupID, actorID)
	if err != nil {
		return false, err
	}
	targetRole, err := a.GetMemberRole(groupID, targetID)
	if err != nil {
		return false, err
	}
	if actorRole == "owner" {
		return true, nil
	}
	if actorRole == "admin" && targetRole == "member" {
		return true, nil
	}
	return false, nil
}

// SetGroupAnnouncement sets the announcement text for a group.
func (a *Adapter) SetGroupAnnouncement(groupID int64, announcement string) error {
	_, err := a.db.Exec(
		`UPDATE "groups" SET announcement = $1 WHERE id = $2`,
		announcement, groupID,
	)
	if err != nil {
		return fmt.Errorf("set group announcement: %w", err)
	}
	return nil
}

// UpdateGroupProfile updates mutable group profile fields and keeps the topic name in sync.
func (a *Adapter) UpdateGroupProfile(groupID int64, name, avatarURL string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("update group begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`UPDATE "groups" SET name = $1, avatar_url = $2 WHERE id = $3`,
		name, avatarURL, groupID,
	); err != nil {
		return fmt.Errorf("update group profile: %w", err)
	}

	topicID := fmt.Sprintf("grp_%d", groupID)
	if _, err := tx.Exec(
		`UPDATE topics SET name = $1 WHERE id = $2`,
		name, topicID,
	); err != nil {
		return fmt.Errorf("update group topic: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update group commit: %w", err)
	}
	return nil
}

// IsUserBot checks if a user has account_type = 'bot'.
func (a *Adapter) IsUserBot(userID int64) (bool, error) {
	var acctType string
	err := a.db.QueryRow(
		`SELECT account_type FROM users WHERE id = $1`,
		userID,
	).Scan(&acctType)
	if err != nil {
		return false, err
	}
	return acctType == "bot", nil
}
