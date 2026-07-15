package mysql

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ChannelNativeGroupStore = (*Adapter)(nil)

// EnsureChannelNativeGroup transactionally creates the CatsCo group backing a
// native channel conversation. The identity row serializes concurrent calls.
func (a *Adapter) EnsureChannelNativeGroup(binding *types.ChannelNativeGroupBinding, groupName string, memberUIDs []int64) (*types.ChannelNativeGroupBinding, bool, error) {
	if err := validateChannelNativeGroupBinding(binding); err != nil {
		return nil, false, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("ensure channel native group begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO channel_native_groups (
		     channel, channel_app_id, tenant_key, conversation_id, conversation_name,
		     operator_channel_user_id, operator_actor_uid, canonical_uid, source_kind,
		     source_group_id, source_agent_uid, status
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')
		 ON DUPLICATE KEY UPDATE id = id`,
		strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey),
		strings.TrimSpace(binding.ConversationID), strings.TrimSpace(binding.ConversationName), strings.TrimSpace(binding.OperatorChannelUserID),
		nullableNativeGroupID(binding.OperatorActorUID), nullableNativeGroupID(binding.CanonicalUID), strings.TrimSpace(binding.SourceKind),
		nullableNativeGroupID(binding.SourceGroupID), nullableNativeGroupID(binding.SourceAgentUID),
	); err != nil {
		return nil, false, fmt.Errorf("ensure channel native group placeholder: %w", err)
	}

	current, err := selectChannelNativeGroupTx(tx, binding.Channel, binding.ChannelAppID, binding.TenantKey, binding.ConversationID, true)
	if err != nil {
		return nil, false, fmt.Errorf("ensure channel native group lock: %w", err)
	}
	mergeChannelNativeGroupBinding(current, binding)

	created := false
	switch {
	case current.GroupID > 0:
		current.Status = types.ChannelNativeGroupActive
	case current.CanonicalUID <= 0:
		current.Status = types.ChannelNativeGroupPending
	default:
		name := channelNativeGroupName(groupName, current)
		result, err := tx.Exec("INSERT INTO `groups` (name, owner_id, group_kind) VALUES (?, ?, 'channel_managed')", name, current.CanonicalUID)
		if err != nil {
			return nil, false, fmt.Errorf("ensure channel native group create group: %w", err)
		}
		current.GroupID, err = result.LastInsertId()
		if err != nil {
			return nil, false, fmt.Errorf("ensure channel native group group id: %w", err)
		}
		current.TopicID = fmt.Sprintf("grp_%d", current.GroupID)
		if _, err := tx.Exec(
			"INSERT INTO group_members (group_id, user_id, role) VALUES (?, ?, 'owner')",
			current.GroupID, current.CanonicalUID,
		); err != nil {
			return nil, false, fmt.Errorf("ensure channel native group add owner: %w", err)
		}
		if _, err := tx.Exec(
			"INSERT INTO topics (id, type, name, owner_id) VALUES (?, 'group', ?, ?)",
			current.TopicID, name, current.CanonicalUID,
		); err != nil {
			return nil, false, fmt.Errorf("ensure channel native group create topic: %w", err)
		}
		for _, uid := range uniqueNativeGroupMemberUIDs(memberUIDs) {
			if _, err := tx.Exec(
				`INSERT INTO group_members (group_id, user_id, role) VALUES (?, ?, 'member')
				 ON DUPLICATE KEY UPDATE user_id = VALUES(user_id)`,
				current.GroupID, uid,
			); err != nil {
				return nil, false, fmt.Errorf("ensure channel native group add member %d: %w", uid, err)
			}
		}
		current.Status = types.ChannelNativeGroupActive
		created = true
	}

	if err := updateChannelNativeGroupTx(tx, current); err != nil {
		return nil, false, err
	}
	current, err = selectChannelNativeGroupTx(tx, current.Channel, current.ChannelAppID, current.TenantKey, current.ConversationID, false)
	if err != nil {
		return nil, false, fmt.Errorf("ensure channel native group reload: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("ensure channel native group commit: %w", err)
	}
	return current, created, nil
}

func (a *Adapter) ResolveChannelNativeGroup(channel, appID, tenantKey, conversationID string) (*types.ChannelNativeGroupBinding, error) {
	if strings.TrimSpace(channel) == "" || strings.TrimSpace(conversationID) == "" {
		return nil, fmt.Errorf("invalid channel native group identity")
	}
	binding, err := scanChannelNativeGroup(a.db.QueryRow(channelNativeGroupSelect+`
		WHERE channel = ? AND channel_app_id = ? AND tenant_key = ? AND conversation_id = ?`,
		strings.TrimSpace(channel), strings.TrimSpace(appID), strings.TrimSpace(tenantKey), strings.TrimSpace(conversationID)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel native group: %w", err)
	}
	return binding, nil
}

func (a *Adapter) SetChannelNativeGroupStatus(channel, appID, tenantKey, conversationID, status string) error {
	status = strings.TrimSpace(status)
	if strings.TrimSpace(channel) == "" || strings.TrimSpace(conversationID) == "" || !validChannelNativeGroupStatus(status) {
		return fmt.Errorf("invalid channel native group status update")
	}
	_, err := a.db.Exec(
		`UPDATE channel_native_groups SET status = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE channel = ? AND channel_app_id = ? AND tenant_key = ? AND conversation_id = ?`,
		status, strings.TrimSpace(channel), strings.TrimSpace(appID), strings.TrimSpace(tenantKey), strings.TrimSpace(conversationID),
	)
	if err != nil {
		return fmt.Errorf("set channel native group status: %w", err)
	}
	return nil
}

func (a *Adapter) ListChannelNativeGroupsForTopic(topicID string) ([]*types.ChannelNativeGroupBinding, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil, fmt.Errorf("invalid channel native group topic")
	}
	rows, err := a.db.Query(channelNativeGroupSelect+`
		WHERE topic_id = ? AND status = 'active' ORDER BY updated_at DESC, id DESC`, topicID)
	if err != nil {
		return nil, fmt.Errorf("list channel native groups for topic: %w", err)
	}
	defer rows.Close()

	var bindings []*types.ChannelNativeGroupBinding
	for rows.Next() {
		binding, err := scanChannelNativeGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel native group: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channel native groups for topic: %w", err)
	}
	return bindings, nil
}

const channelNativeGroupSelect = `SELECT id, channel, channel_app_id, tenant_key, conversation_id,
	conversation_name, operator_channel_user_id, COALESCE(operator_actor_uid, 0),
	COALESCE(canonical_uid, 0), COALESCE(group_id, 0), topic_id, source_kind,
	COALESCE(source_group_id, 0), COALESCE(source_agent_uid, 0), status, created_at, updated_at
	FROM channel_native_groups `

func selectChannelNativeGroupTx(tx *sql.Tx, channel, appID, tenantKey, conversationID string, forUpdate bool) (*types.ChannelNativeGroupBinding, error) {
	query := channelNativeGroupSelect + `
		WHERE channel = ? AND channel_app_id = ? AND tenant_key = ? AND conversation_id = ?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanChannelNativeGroup(tx.QueryRow(query,
		strings.TrimSpace(channel), strings.TrimSpace(appID), strings.TrimSpace(tenantKey), strings.TrimSpace(conversationID)))
}

func updateChannelNativeGroupTx(tx *sql.Tx, binding *types.ChannelNativeGroupBinding) error {
	_, err := tx.Exec(
		`UPDATE channel_native_groups SET conversation_name = ?, operator_channel_user_id = ?,
		 operator_actor_uid = ?, canonical_uid = ?, group_id = ?, topic_id = ?,
		 source_kind = ?, source_group_id = ?, source_agent_uid = ?, status = ?,
		 updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		binding.ConversationName, binding.OperatorChannelUserID, nullableNativeGroupID(binding.OperatorActorUID),
		nullableNativeGroupID(binding.CanonicalUID), nullableNativeGroupID(binding.GroupID), binding.TopicID,
		binding.SourceKind, nullableNativeGroupID(binding.SourceGroupID), nullableNativeGroupID(binding.SourceAgentUID),
		binding.Status, binding.ID,
	)
	if err != nil {
		return fmt.Errorf("ensure channel native group update binding: %w", err)
	}
	return nil
}

type channelNativeGroupScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelNativeGroup(row channelNativeGroupScanner) (*types.ChannelNativeGroupBinding, error) {
	binding := &types.ChannelNativeGroupBinding{}
	if err := row.Scan(
		&binding.ID, &binding.Channel, &binding.ChannelAppID, &binding.TenantKey, &binding.ConversationID,
		&binding.ConversationName, &binding.OperatorChannelUserID, &binding.OperatorActorUID,
		&binding.CanonicalUID, &binding.GroupID, &binding.TopicID, &binding.SourceKind,
		&binding.SourceGroupID, &binding.SourceAgentUID, &binding.Status, &binding.CreatedAt, &binding.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return binding, nil
}

func validateChannelNativeGroupBinding(binding *types.ChannelNativeGroupBinding) error {
	if binding == nil || strings.TrimSpace(binding.Channel) == "" || strings.TrimSpace(binding.ConversationID) == "" {
		return fmt.Errorf("invalid channel native group binding")
	}
	return nil
}

func mergeChannelNativeGroupBinding(current, incoming *types.ChannelNativeGroupBinding) {
	if value := strings.TrimSpace(incoming.ConversationName); value != "" {
		current.ConversationName = value
	}
	if value := strings.TrimSpace(incoming.OperatorChannelUserID); value != "" {
		current.OperatorChannelUserID = value
	}
	if incoming.OperatorActorUID > 0 {
		current.OperatorActorUID = incoming.OperatorActorUID
	}
	if incoming.CanonicalUID > 0 {
		current.CanonicalUID = incoming.CanonicalUID
	}
	if value := strings.TrimSpace(incoming.SourceKind); value != "" {
		current.SourceKind = value
	}
	if incoming.SourceGroupID > 0 {
		current.SourceGroupID = incoming.SourceGroupID
	}
	if incoming.SourceAgentUID > 0 {
		current.SourceAgentUID = incoming.SourceAgentUID
	}
}

func channelNativeGroupName(groupName string, binding *types.ChannelNativeGroupBinding) string {
	if name := strings.TrimSpace(groupName); name != "" {
		return name
	}
	if name := strings.TrimSpace(binding.ConversationName); name != "" {
		return name
	}
	return strings.TrimSpace(binding.ConversationID)
}

func uniqueNativeGroupMemberUIDs(memberUIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(memberUIDs))
	result := make([]int64, 0, len(memberUIDs))
	for _, uid := range memberUIDs {
		if uid <= 0 {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		result = append(result, uid)
	}
	return result
}

func validChannelNativeGroupStatus(status string) bool {
	return status == types.ChannelNativeGroupPending || status == types.ChannelNativeGroupActive || status == types.ChannelNativeGroupDisconnected
}

func nullableNativeGroupID(value int64) interface{} {
	if value <= 0 {
		return nil
	}
	return value
}
