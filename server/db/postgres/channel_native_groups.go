package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ChannelNativeGroupStore = (*Adapter)(nil)

const channelNativeGroupEventRetryLease = time.Minute

func (a *Adapter) ApplyChannelNativeGroupMembershipEvent(binding *types.ChannelNativeGroupBinding, added bool, eventID string, eventTime int64) (bool, int64, error) {
	if err := validateChannelNativeGroupBinding(binding); err != nil {
		return false, 0, err
	}
	if eventTime <= 0 {
		return false, 0, fmt.Errorf("invalid channel native group event time")
	}
	if strings.TrimSpace(eventID) == "" {
		return false, 0, fmt.Errorf("invalid channel native group event id")
	}
	status := types.ChannelNativeGroupDisconnected
	if added {
		status = types.ChannelNativeGroupPending
	}
	tx, err := a.db.Begin()
	if err != nil {
		return false, 0, fmt.Errorf("apply channel native group event begin: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`INSERT INTO channel_native_groups (
		     channel, channel_app_id, tenant_key, conversation_id, conversation_name,
		     operator_channel_user_id, status, last_event_id, last_event_time
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (channel, channel_app_id, tenant_key, conversation_id) DO NOTHING`,
		strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey),
		strings.TrimSpace(binding.ConversationID), strings.TrimSpace(binding.ConversationName), strings.TrimSpace(binding.OperatorChannelUserID),
		types.ChannelNativeGroupDisconnected, "", int64(0),
	)
	if err != nil {
		return false, 0, fmt.Errorf("apply channel native group event placeholder: %w", err)
	}
	var currentStatus, currentEventID string
	var currentEventTime, currentEventClaimedAt int64
	if err := tx.QueryRow(
		`SELECT status, last_event_id, last_event_time, last_event_claimed_at FROM channel_native_groups
		 WHERE channel = $1 AND channel_app_id = $2 AND tenant_key = $3 AND conversation_id = $4 FOR UPDATE`,
		strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey), strings.TrimSpace(binding.ConversationID),
	).Scan(&currentStatus, &currentEventID, &currentEventTime, &currentEventClaimedAt); err != nil {
		return false, 0, fmt.Errorf("apply channel native group event lock: %w", err)
	}
	claimNow := time.Now().UnixMilli()
	if strings.TrimSpace(eventID) != "" && strings.TrimSpace(eventID) == strings.TrimSpace(currentEventID) {
		if currentEventClaimedAt < 0 {
			if err := tx.Commit(); err != nil {
				return false, 0, fmt.Errorf("apply completed channel native group event commit: %w", err)
			}
			return false, 0, nil
		}
		if currentEventClaimedAt > 0 && claimNow-currentEventClaimedAt < channelNativeGroupEventRetryLease.Milliseconds() {
			return false, 0, store.ErrChannelNativeGroupEventBusy
		}
		if _, err := tx.Exec(
			`UPDATE channel_native_groups SET last_event_claimed_at = $1, updated_at = CURRENT_TIMESTAMP
			 WHERE channel = $2 AND channel_app_id = $3 AND tenant_key = $4 AND conversation_id = $5`,
			claimNow, strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey), strings.TrimSpace(binding.ConversationID),
		); err != nil {
			return false, 0, fmt.Errorf("renew channel native group event claim: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, 0, fmt.Errorf("apply channel native group duplicate commit: %w", err)
		}
		return true, claimNow, nil
	}
	if eventTime < currentEventTime || (eventTime == currentEventTime && (added || currentStatus == types.ChannelNativeGroupDisconnected)) {
		if err := tx.Commit(); err != nil {
			return false, 0, fmt.Errorf("apply stale channel native group event commit: %w", err)
		}
		return false, 0, nil
	}
	claimedAt := int64(-1)
	if added {
		claimedAt = claimNow
	}
	if _, err := tx.Exec(
		`UPDATE channel_native_groups SET status = $1, last_event_id = $2, last_event_time = $3, last_event_claimed_at = $4, updated_at = CURRENT_TIMESTAMP
		 WHERE channel = $5 AND channel_app_id = $6 AND tenant_key = $7 AND conversation_id = $8`,
		status, strings.TrimSpace(eventID), eventTime, claimedAt,
		strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey), strings.TrimSpace(binding.ConversationID),
	); err != nil {
		return false, 0, fmt.Errorf("apply channel native group event update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("apply channel native group event commit: %w", err)
	}
	return true, claimedAt, nil
}

func (a *Adapter) CompleteChannelNativeGroupMembershipEvent(binding *types.ChannelNativeGroupBinding, eventID string, claimToken int64) (bool, error) {
	if err := validateChannelNativeGroupBinding(binding); err != nil {
		return false, err
	}
	if strings.TrimSpace(eventID) == "" || claimToken <= 0 {
		return false, fmt.Errorf("invalid channel native group event completion")
	}
	result, err := a.db.Exec(
		`UPDATE channel_native_groups SET last_event_claimed_at = -1, updated_at = CURRENT_TIMESTAMP
		 WHERE channel = $1 AND channel_app_id = $2 AND tenant_key = $3 AND conversation_id = $4
		   AND last_event_id = $5 AND last_event_claimed_at = $6`,
		strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey), strings.TrimSpace(binding.ConversationID),
		strings.TrimSpace(eventID), claimToken,
	)
	if err != nil {
		return false, fmt.Errorf("complete channel native group event: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("complete channel native group event rows: %w", err)
	}
	return count == 1, nil
}

func (a *Adapter) ReleaseChannelNativeGroupMembershipEvent(binding *types.ChannelNativeGroupBinding, eventID string, claimToken int64) error {
	if err := validateChannelNativeGroupBinding(binding); err != nil {
		return err
	}
	if strings.TrimSpace(eventID) == "" || claimToken <= 0 {
		return nil
	}
	_, err := a.db.Exec(
		`UPDATE channel_native_groups SET last_event_claimed_at = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE channel = $1 AND channel_app_id = $2 AND tenant_key = $3 AND conversation_id = $4
		   AND last_event_id = $5 AND last_event_claimed_at = $6`,
		strings.TrimSpace(binding.Channel), strings.TrimSpace(binding.ChannelAppID), strings.TrimSpace(binding.TenantKey), strings.TrimSpace(binding.ConversationID),
		strings.TrimSpace(eventID), claimToken,
	)
	if err != nil {
		return fmt.Errorf("release channel native group event: %w", err)
	}
	return nil
}

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
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending')
		 ON CONFLICT (channel, channel_app_id, tenant_key, conversation_id) DO NOTHING`,
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
	if current.Status == types.ChannelNativeGroupDisconnected {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("ensure disconnected channel native group commit: %w", err)
		}
		return current, false, nil
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
		if err := tx.QueryRow(
			`INSERT INTO "groups" (name, owner_id, group_kind) VALUES ($1, $2, 'channel_managed') RETURNING id`,
			name, current.CanonicalUID,
		).Scan(&current.GroupID); err != nil {
			return nil, false, fmt.Errorf("ensure channel native group create group: %w", err)
		}
		current.TopicID = fmt.Sprintf("grp_%d", current.GroupID)
		if _, err := tx.Exec(
			`INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'owner')`,
			current.GroupID, current.CanonicalUID,
		); err != nil {
			return nil, false, fmt.Errorf("ensure channel native group add owner: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO topics (id, type, name, owner_id) VALUES ($1, 'group', $2, $3)`,
			current.TopicID, name, current.CanonicalUID,
		); err != nil {
			return nil, false, fmt.Errorf("ensure channel native group create topic: %w", err)
		}
		for _, uid := range uniqueNativeGroupMemberUIDs(memberUIDs) {
			if _, err := tx.Exec(
				`INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'member')
				 ON CONFLICT (group_id, user_id) DO NOTHING`,
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
		WHERE channel = $1 AND channel_app_id = $2 AND tenant_key = $3 AND conversation_id = $4`,
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
		`UPDATE channel_native_groups SET status = $1, updated_at = CURRENT_TIMESTAMP
		 WHERE channel = $2 AND channel_app_id = $3 AND tenant_key = $4 AND conversation_id = $5`,
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
		WHERE topic_id = $1 AND status = 'active' ORDER BY updated_at DESC, id DESC`, topicID)
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
		WHERE channel = $1 AND channel_app_id = $2 AND tenant_key = $3 AND conversation_id = $4`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanChannelNativeGroup(tx.QueryRow(query,
		strings.TrimSpace(channel), strings.TrimSpace(appID), strings.TrimSpace(tenantKey), strings.TrimSpace(conversationID)))
}

func updateChannelNativeGroupTx(tx *sql.Tx, binding *types.ChannelNativeGroupBinding) error {
	_, err := tx.Exec(
		`UPDATE channel_native_groups SET conversation_name = $1, operator_channel_user_id = $2,
		 operator_actor_uid = $3, canonical_uid = $4, group_id = $5, topic_id = $6,
		 source_kind = $7, source_group_id = $8, source_agent_uid = $9, status = $10,
		 updated_at = CURRENT_TIMESTAMP WHERE id = $11`,
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
