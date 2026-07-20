package mysql

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ChannelAgentBindingStore = (*Adapter)(nil)
var _ store.ChannelPrivateBindingStore = (*Adapter)(nil)

func (a *Adapter) ListChannelPrivateSelections(canonicalUID int64, channel string) ([]*types.ChannelPrivateSelection, error) {
	if canonicalUID <= 0 || strings.TrimSpace(channel) == "" {
		return nil, fmt.Errorf("invalid channel private selection scope")
	}
	var selections []*types.ChannelPrivateSelection
	routeRows, err := a.db.Query(
		`SELECT DISTINCT r.channel, r.channel_app_id, r.channel_user_id, COALESCE(r.actor_uid, b.actor_uid, 0),
		        r.agent_uid, r.selected_at
		 FROM channel_agent_routes r
		 JOIN channel_agent_bindings b
		   ON b.channel = r.channel AND b.channel_app_id = r.channel_app_id
		  AND b.channel_user_id = r.channel_user_id AND b.agent_uid = r.agent_uid
		  AND b.canonical_uid = ? AND b.status = 'active'
		 WHERE r.channel = ? AND r.channel_conversation_type = 'p2p' AND r.channel_conversation_id = ''`,
		canonicalUID, strings.TrimSpace(channel),
	)
	if err != nil {
		return nil, fmt.Errorf("list channel private agent selections: %w", err)
	}
	for routeRows.Next() {
		selection := &types.ChannelPrivateSelection{TargetKind: types.ChannelPrivateTargetAgent}
		if err := routeRows.Scan(&selection.Channel, &selection.ChannelAppID, &selection.ChannelUserID, &selection.ActorUID, &selection.AgentUID, &selection.SelectedAt); err != nil {
			routeRows.Close()
			return nil, err
		}
		selections = append(selections, selection)
	}
	if err := routeRows.Close(); err != nil {
		return nil, err
	}
	if err := routeRows.Err(); err != nil {
		return nil, err
	}

	groupRows, err := a.db.Query(
		`SELECT channel, channel_app_id, channel_user_id, COALESCE(actor_uid, 0), group_id, topic_id, selected_at
		 FROM channel_group_bindings
		 WHERE canonical_uid = ? AND channel = ? AND channel_conversation_type = 'p2p'
		   AND channel_conversation_id = '' AND status = 'active'`,
		canonicalUID, strings.TrimSpace(channel),
	)
	if err != nil {
		return nil, fmt.Errorf("list channel private group selections: %w", err)
	}
	defer groupRows.Close()
	for groupRows.Next() {
		selection := &types.ChannelPrivateSelection{TargetKind: types.ChannelPrivateTargetGroup}
		if err := groupRows.Scan(&selection.Channel, &selection.ChannelAppID, &selection.ChannelUserID, &selection.ActorUID, &selection.GroupID, &selection.TopicID, &selection.SelectedAt); err != nil {
			return nil, err
		}
		selections = append(selections, selection)
	}
	if err := groupRows.Err(); err != nil {
		return nil, err
	}
	return selections, nil
}

func (a *Adapter) RevokeChannelPrivateSelection(canonicalUID int64, expected *types.ChannelPrivateSelection) (*types.ChannelPrivateUnbindResult, error) {
	if canonicalUID <= 0 || expected == nil || expected.Channel == "" || expected.ChannelUserID == "" || expected.SelectedAt.IsZero() {
		return nil, fmt.Errorf("invalid channel private identity")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockChannelRouteSelectionTx(tx, expected.Channel, expected.ChannelAppID, expected.ChannelUserID, "", "p2p"); err != nil {
		return nil, err
	}
	current, err := currentChannelPrivateSelectionMySQLTx(tx, canonicalUID, expected)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return &types.ChannelPrivateUnbindResult{}, nil
	}
	if !sameChannelPrivateSelection(current, expected) {
		return &types.ChannelPrivateUnbindResult{Changed: true}, nil
	}
	if _, err := tx.Exec(
		`UPDATE channel_agent_bindings
		 SET status = 'revoked', device_access_enabled = FALSE, updated_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = ? AND channel = ? AND channel_app_id = ? AND channel_user_id = ?
		   AND channel_conversation_type = 'p2p' AND status <> 'revoked'`,
		canonicalUID, expected.Channel, expected.ChannelAppID, expected.ChannelUserID,
	); err != nil {
		return nil, fmt.Errorf("revoke channel private agent bindings: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM channel_agent_routes
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND channel_conversation_type = 'p2p'`,
		expected.Channel, expected.ChannelAppID, expected.ChannelUserID,
	); err != nil {
		return nil, fmt.Errorf("clear channel private agent routes: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE channel_group_bindings SET status = 'revoked', updated_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = ? AND channel = ? AND channel_app_id = ? AND channel_user_id = ?
		   AND channel_conversation_type = 'p2p' AND status = 'active'`,
		canonicalUID, expected.Channel, expected.ChannelAppID, expected.ChannelUserID,
	); err != nil {
		return nil, fmt.Errorf("revoke channel private group bindings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &types.ChannelPrivateUnbindResult{Revoked: true}, nil
}

func currentChannelPrivateSelectionMySQLTx(tx *sql.Tx, canonicalUID int64, identity *types.ChannelPrivateSelection) (*types.ChannelPrivateSelection, error) {
	var route *types.ChannelPrivateSelection
	row := tx.QueryRow(
		`SELECT r.channel, r.channel_app_id, r.channel_user_id, COALESCE(r.actor_uid, 0), r.agent_uid, r.selected_at
		 FROM channel_agent_routes r
		 WHERE r.channel = ? AND r.channel_app_id = ? AND r.channel_user_id = ?
		   AND r.channel_conversation_id = '' AND r.channel_conversation_type = 'p2p'
		   AND EXISTS (SELECT 1 FROM channel_agent_bindings b
		       WHERE b.channel = r.channel AND b.channel_app_id = r.channel_app_id AND b.channel_user_id = r.channel_user_id
		         AND b.agent_uid = r.agent_uid AND b.canonical_uid = ? AND b.status = 'active')
		 ORDER BY r.selected_at DESC LIMIT 1`,
		identity.Channel, identity.ChannelAppID, identity.ChannelUserID, canonicalUID,
	)
	candidate := &types.ChannelPrivateSelection{TargetKind: types.ChannelPrivateTargetAgent}
	if err := row.Scan(&candidate.Channel, &candidate.ChannelAppID, &candidate.ChannelUserID, &candidate.ActorUID, &candidate.AgentUID, &candidate.SelectedAt); err == nil {
		route = candidate
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("read current channel private agent selection: %w", err)
	}

	var group *types.ChannelPrivateSelection
	row = tx.QueryRow(
		`SELECT channel, channel_app_id, channel_user_id, COALESCE(actor_uid, 0), group_id, topic_id, selected_at
		 FROM channel_group_bindings
		 WHERE canonical_uid = ? AND channel = ? AND channel_app_id = ? AND channel_user_id = ?
		   AND channel_conversation_id = '' AND channel_conversation_type = 'p2p' AND status = 'active'
		 ORDER BY selected_at DESC LIMIT 1`,
		canonicalUID, identity.Channel, identity.ChannelAppID, identity.ChannelUserID,
	)
	candidate = &types.ChannelPrivateSelection{TargetKind: types.ChannelPrivateTargetGroup}
	if err := row.Scan(&candidate.Channel, &candidate.ChannelAppID, &candidate.ChannelUserID, &candidate.ActorUID, &candidate.GroupID, &candidate.TopicID, &candidate.SelectedAt); err == nil {
		group = candidate
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("read current channel private group selection: %w", err)
	}
	if group != nil && (route == nil || !route.SelectedAt.After(group.SelectedAt)) {
		return group, nil
	}
	return route, nil
}

func sameChannelPrivateSelection(current, expected *types.ChannelPrivateSelection) bool {
	if current == nil || expected == nil || current.TargetKind != expected.TargetKind || !current.SelectedAt.Equal(expected.SelectedAt) {
		return false
	}
	if current.TargetKind == types.ChannelPrivateTargetAgent {
		return current.AgentUID == expected.AgentUID
	}
	return current.GroupID == expected.GroupID && current.TopicID == expected.TopicID
}

func (a *Adapter) EnsureChannelAgentEntry(entry *types.ChannelAgentEntry) (*types.ChannelAgentEntry, error) {
	if entry == nil || entry.OwnerUID <= 0 || entry.AgentUID <= 0 || entry.Channel == "" || entry.SceneKey == "" {
		return nil, fmt.Errorf("invalid channel agent entry")
	}
	accessMode := types.NormalizeChannelAgentAccessMode(entry.AccessMode)
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel agent entry ensure: %w", err)
	}
	defer tx.Rollback()

	var lockedAgentID int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = ? FOR UPDATE`, entry.AgentUID).Scan(&lockedAgentID); err != nil {
		return nil, fmt.Errorf("lock channel agent entry scope: %w", err)
	}

	existing, err := getActiveChannelAgentEntryTx(tx, entry.OwnerUID, entry.AgentUID, entry.Channel, entry.ChannelAppID, accessMode)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	}

	res, err := tx.Exec(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		entry.SceneKey, entry.Channel, entry.ChannelAppID, accessMode, entry.OwnerUID, entry.AgentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("create channel agent entry: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.GetChannelAgentEntryByID(id)
}

func (a *Adapter) ListChannelAgentEntries(ownerUID, agentUID int64) ([]*types.ChannelAgentEntry, error) {
	rows, err := a.db.Query(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = ? AND agent_uid = ? AND status = 'active'
		 ORDER BY created_at DESC`,
		ownerUID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel agent entries: %w", err)
	}
	defer rows.Close()

	var entries []*types.ChannelAgentEntry
	for rows.Next() {
		entry, err := scanChannelAgentEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (a *Adapter) ListChannelAgentEntriesByChannelApp(channel, channelAppID string) ([]*types.ChannelAgentEntry, error) {
	rows, err := a.db.Query(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE channel = ? AND channel_app_id = ? AND status = 'active'
		 ORDER BY updated_at DESC, id DESC`,
		channel, channelAppID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel app agent entries: %w", err)
	}
	defer rows.Close()

	var entries []*types.ChannelAgentEntry
	for rows.Next() {
		entry, err := scanChannelAgentEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (a *Adapter) RegenerateChannelAgentEntry(id, ownerUID int64, sceneKey, nextChannelAppID string) (*types.ChannelAgentEntry, error) {
	if id <= 0 || ownerUID <= 0 || sceneKey == "" {
		return nil, fmt.Errorf("invalid channel agent entry")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel agent entry regeneration: %w", err)
	}
	defer tx.Rollback()

	var channel, channelAppID, accessMode string
	var agentUID int64
	if err := tx.QueryRow(
		`SELECT channel, channel_app_id, access_mode, agent_uid FROM channel_agent_entries WHERE id = ? AND owner_uid = ? AND status = 'active'`,
		id, ownerUID,
	).Scan(&channel, &channelAppID, &accessMode, &agentUID); err != nil {
		return nil, fmt.Errorf("get channel agent entry: %w", err)
	}
	if strings.TrimSpace(nextChannelAppID) != "" {
		channelAppID = strings.TrimSpace(nextChannelAppID)
	}
	if _, err := tx.Exec(
		`UPDATE channel_agent_entries SET status = 'revoked', updated_at = CURRENT_TIMESTAMP WHERE id = ? AND owner_uid = ?`,
		id, ownerUID,
	); err != nil {
		return nil, fmt.Errorf("revoke channel agent entry: %w", err)
	}
	res, err := tx.Exec(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		sceneKey, channel, channelAppID, types.NormalizeChannelAgentAccessMode(accessMode), ownerUID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("create regenerated channel agent entry: %w", err)
	}
	newID, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.GetChannelAgentEntryByID(newID)
}

func (a *Adapter) GetChannelAgentEntryByID(id int64) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries WHERE id = ?`,
		id,
	)
	entry, err := scanChannelAgentEntry(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel agent entry: %w", err)
	}
	return entry, nil
}

func (a *Adapter) GetChannelAgentEntryBySceneKey(sceneKey string) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries WHERE scene_key = ?`,
		sceneKey,
	)
	entry, err := scanChannelAgentEntry(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel agent entry by scene: %w", err)
	}
	return entry, nil
}

func (a *Adapter) ListChannelAgentBindingsForAgent(ownerUID, agentUID int64) ([]*types.ChannelAgentBinding, error) {
	if ownerUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent binding list")
	}
	rows, err := a.db.Query(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE owner_uid = ? AND agent_uid = ? AND status IN ('active', 'pending_login', 'pending_approval', 'rejected')
		 ORDER BY updated_at DESC`,
		ownerUID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel agent bindings: %w", err)
	}
	defer rows.Close()
	var bindings []*types.ChannelAgentBinding
	for rows.Next() {
		binding, scanErr := scanChannelAgentBinding(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bindings, nil
}

func (a *Adapter) RequestChannelAgentAccess(request *types.ChannelAgentAccessRequest) (*types.ChannelAgentAccessRequest, error) {
	if request == nil || request.EntryID <= 0 || request.Channel == "" || request.ChannelUserID == "" || request.ActorUID <= 0 || request.OwnerUID <= 0 || request.AgentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent access request")
	}
	conversationType := request.ChannelConversationType
	if conversationType == "" {
		conversationType = "p2p"
	}
	res, err := a.db.Exec(
		`INSERT INTO channel_agent_access_requests (
		     entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, owner_uid, agent_uid, status, requested_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		     channel_conversation_type = VALUES(channel_conversation_type),
		     actor_uid = VALUES(actor_uid),
		     owner_uid = VALUES(owner_uid),
		     agent_uid = VALUES(agent_uid),
		     status = IF(status = 'approved', 'approved', 'pending'),
		     requested_at = IF(status = 'approved', requested_at, CURRENT_TIMESTAMP),
		     reviewed_by_uid = IF(status = 'approved', reviewed_by_uid, NULL),
		     reviewed_at = IF(status = 'approved', reviewed_at, NULL),
		     updated_at = CURRENT_TIMESTAMP`,
		request.EntryID,
		request.Channel,
		request.ChannelAppID,
		request.ChannelUserID,
		request.ChannelConversationID,
		conversationType,
		request.ActorUID,
		request.OwnerUID,
		request.AgentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("request channel agent access: %w", err)
	}
	id, _ := res.LastInsertId()
	if id > 0 {
		return a.getChannelAgentAccessRequestByID(id)
	}
	return a.getChannelAgentAccessRequest(types.ChannelAgentBindingQuery{
		Channel:                 request.Channel,
		ChannelAppID:            request.ChannelAppID,
		ChannelUserID:           request.ChannelUserID,
		ChannelConversationID:   request.ChannelConversationID,
		ChannelConversationType: conversationType,
	}, request.ChannelConversationID)
}

func (a *Adapter) ResolveChannelAgentAccessRequest(query types.ChannelAgentBindingQuery) (*types.ChannelAgentAccessRequest, error) {
	if query.Channel == "" || query.ChannelUserID == "" {
		return nil, fmt.Errorf("invalid channel agent access query")
	}
	request, err := a.getChannelAgentAccessRequest(query, query.ChannelConversationID)
	if err != nil || request != nil {
		return request, err
	}
	return nil, nil
}

func (a *Adapter) ApproveChannelAgentAccessRequestsForActor(actorUID, agentUID, reviewerUID int64) ([]*types.ChannelAgentBinding, error) {
	if actorUID <= 0 || agentUID <= 0 || reviewerUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent access approval")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel agent access approval: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT id, entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        actor_uid, owner_uid, agent_uid, status, COALESCE(reviewed_by_uid, 0), requested_at, updated_at, reviewed_at
		 FROM channel_agent_access_requests
		 WHERE actor_uid = ? AND agent_uid = ? AND status = 'pending'
		 ORDER BY requested_at ASC`,
		actorUID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending channel agent access: %w", err)
	}
	var requests []*types.ChannelAgentAccessRequest
	for rows.Next() {
		request, scanErr := scanChannelAgentAccessRequest(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		requests = append(requests, request)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var bindings []*types.ChannelAgentBinding
	for _, request := range requests {
		if _, err := tx.Exec(
			`UPDATE channel_agent_access_requests
			 SET status = 'approved', reviewed_by_uid = ?, reviewed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ? AND status = 'pending'`,
			reviewerUID, request.ID,
		); err != nil {
			return nil, fmt.Errorf("approve channel agent access: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO channel_agent_bindings (
			     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			     actor_uid, owner_uid, agent_uid, entry_id, status, device_access_enabled, bound_at, last_used_at
			 )
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			 ON DUPLICATE KEY UPDATE
			     actor_uid = VALUES(actor_uid),
			     owner_uid = VALUES(owner_uid),
			     entry_id = COALESCE(VALUES(entry_id), entry_id),
			     channel_conversation_type = VALUES(channel_conversation_type),
			     status = 'active',
			     device_access_enabled = TRUE,
			     bound_at = CURRENT_TIMESTAMP,
			     last_used_at = CURRENT_TIMESTAMP,
			     updated_at = CURRENT_TIMESTAMP`,
			request.Channel,
			request.ChannelAppID,
			request.ChannelUserID,
			request.ChannelConversationID,
			request.ChannelConversationType,
			request.ActorUID,
			request.OwnerUID,
			request.AgentUID,
			nullableInt64(request.EntryID),
		); err != nil {
			return nil, fmt.Errorf("create approved channel agent binding: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, request := range requests {
		binding, err := a.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
			Channel:                 request.Channel,
			ChannelAppID:            request.ChannelAppID,
			ChannelUserID:           request.ChannelUserID,
			ChannelConversationID:   request.ChannelConversationID,
			ChannelConversationType: request.ChannelConversationType,
			AgentUID:                request.AgentUID,
		})
		if err != nil {
			return nil, err
		}
		if binding != nil {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}

func (a *Adapter) RejectChannelAgentAccessRequestsForActor(actorUID, agentUID, reviewerUID int64) error {
	if actorUID <= 0 || agentUID <= 0 || reviewerUID <= 0 {
		return fmt.Errorf("invalid channel agent access rejection")
	}
	if _, err := a.db.Exec(
		`UPDATE channel_agent_access_requests
		 SET status = 'rejected', reviewed_by_uid = ?, reviewed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE actor_uid = ? AND agent_uid = ? AND status = 'pending'`,
		reviewerUID, actorUID, agentUID,
	); err != nil {
		return fmt.Errorf("reject channel agent access: %w", err)
	}
	return nil
}

func (a *Adapter) ActivateChannelAgentBindingsForCanonicalUser(canonicalUID, agentUID, reviewerUID int64) ([]*types.ChannelAgentBinding, error) {
	if canonicalUID <= 0 || agentUID <= 0 || reviewerUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent canonical approval")
	}
	if _, err := a.db.Exec(
		`UPDATE channel_agent_bindings
		 SET status = 'active', device_access_enabled = TRUE, bound_at = CURRENT_TIMESTAMP, last_used_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = ? AND agent_uid = ? AND status IN ('pending_approval', 'active')`,
		canonicalUID, agentUID,
	); err != nil {
		return nil, fmt.Errorf("activate channel agent binding: %w", err)
	}
	rows, err := a.db.Query(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE canonical_uid = ? AND agent_uid = ? AND status = 'active'
		 ORDER BY updated_at DESC`,
		canonicalUID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list activated channel agent bindings: %w", err)
	}
	defer rows.Close()
	var bindings []*types.ChannelAgentBinding
	for rows.Next() {
		binding, scanErr := scanChannelAgentBinding(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bindings, nil
}

func (a *Adapter) RejectChannelAgentBindingsForCanonicalUser(canonicalUID, agentUID, reviewerUID int64) error {
	if canonicalUID <= 0 || agentUID <= 0 || reviewerUID <= 0 {
		return fmt.Errorf("invalid channel agent canonical rejection")
	}
	if _, err := a.db.Exec(
		`UPDATE channel_agent_bindings
		 SET status = 'rejected', updated_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = ? AND agent_uid = ? AND status IN ('pending_approval', 'active', 'pending_login')`,
		canonicalUID, agentUID,
	); err != nil {
		return fmt.Errorf("reject channel agent binding: %w", err)
	}
	return nil
}

func (a *Adapter) UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error) {
	if binding == nil || binding.Channel == "" || binding.ChannelUserID == "" || binding.OwnerUID <= 0 || binding.AgentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent binding")
	}
	conversationType := binding.ChannelConversationType
	if conversationType == "" {
		conversationType = "p2p"
	}
	status := strings.TrimSpace(binding.Status)
	if status == "" {
		status = types.ChannelAgentBindingActive
	}
	_, err := a.db.Exec(
		`INSERT INTO channel_agent_bindings (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, canonical_uid, device_access_enabled, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		     actor_uid = COALESCE(VALUES(actor_uid), actor_uid),
		     canonical_uid = COALESCE(VALUES(canonical_uid), canonical_uid),
		     device_access_enabled = IF(VALUES(device_access_enabled), TRUE, device_access_enabled),
		     owner_uid = VALUES(owner_uid),
		     entry_id = COALESCE(VALUES(entry_id), entry_id),
		     channel_conversation_type = VALUES(channel_conversation_type),
		     status = VALUES(status),
		     bound_at = IF(VALUES(status) = 'active', CURRENT_TIMESTAMP, bound_at),
		     last_used_at = IF(VALUES(status) = 'active', CURRENT_TIMESTAMP, last_used_at),
		     updated_at = CURRENT_TIMESTAMP`,
		binding.Channel,
		binding.ChannelAppID,
		binding.ChannelUserID,
		binding.ChannelConversationID,
		conversationType,
		nullableInt64(binding.ActorUID),
		nullableInt64(binding.CanonicalUID),
		binding.DeviceAccessEnabled,
		binding.OwnerUID,
		binding.AgentUID,
		nullableInt64(binding.EntryID),
		status,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert channel agent binding: %w", err)
	}
	resolved, err := a.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 binding.Channel,
		ChannelAppID:            binding.ChannelAppID,
		ChannelUserID:           binding.ChannelUserID,
		ChannelConversationID:   binding.ChannelConversationID,
		ChannelConversationType: conversationType,
		AgentUID:                binding.AgentUID,
	})
	if resolved != nil && binding.DeviceAccessEnabled {
		resolved.DeviceAccessEnabled = true
	}
	return resolved, err
}

func (a *Adapter) ResolveChannelAgentBinding(query types.ChannelAgentBindingQuery) (*types.ChannelAgentBinding, error) {
	if query.Channel == "" || query.ChannelUserID == "" {
		return nil, fmt.Errorf("invalid channel agent binding query")
	}
	binding, err := a.getChannelAgentBinding(query, query.ChannelConversationID)
	if err != nil || binding != nil {
		return binding, err
	}
	return nil, nil
}

func (a *Adapter) ResolveChannelAgentBindingForActor(channel, channelAppID string, actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if channel == "" || actorUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent actor query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE channel = ? AND channel_app_id = ? AND actor_uid = ? AND agent_uid = ? AND status = 'active'
		 ORDER BY updated_at DESC LIMIT 1`,
		channel, channelAppID, actorUID, agentUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent actor binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent actor binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) ResolveChannelAgentBindingForCanonical(channel, channelAppID string, canonicalUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if channel == "" || canonicalUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent canonical query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE channel = ? AND channel_app_id = ? AND canonical_uid = ? AND agent_uid = ? AND status = 'active'
		 ORDER BY updated_at DESC LIMIT 1`,
		channel, channelAppID, canonicalUID, agentUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent canonical binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent canonical binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) ResolveChannelAgentBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if actorUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent actor query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE actor_uid = ? AND agent_uid = ? AND status = 'active'
		 ORDER BY updated_at DESC LIMIT 1`,
		actorUID, agentUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent actor binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent actor binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) ResolveChannelAgentBindingForChannelUser(channel, channelAppID, channelUserID string) (*types.ChannelAgentBinding, error) {
	channel = strings.TrimSpace(channel)
	channelAppID = strings.TrimSpace(channelAppID)
	channelUserID = strings.TrimSpace(channelUserID)
	if channel == "" || channelUserID == "" {
		return nil, fmt.Errorf("invalid channel agent identity query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND canonical_uid IS NOT NULL
		 ORDER BY updated_at DESC LIMIT 1`,
		channel, channelAppID, channelUserID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent identity binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) ResolveChannelAgentDeviceAccessBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if actorUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent device access query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE actor_uid = ? AND agent_uid = ? AND status = 'active' AND canonical_uid IS NOT NULL AND device_access_enabled = TRUE
		 ORDER BY updated_at DESC LIMIT 1`,
		actorUID, agentUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent device access binding: %w", err)
	}
	binding.DeviceAccessEnabled = true
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent device access binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64, enableDeviceAccess bool) (*types.ChannelAgentBinding, error) {
	if bindingID <= 0 || actorUID <= 0 || agentUID <= 0 || canonicalUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent link")
	}
	var channel, channelAppID, channelUserID string
	if err := a.db.QueryRow(
		`SELECT channel, channel_app_id, channel_user_id
		 FROM channel_agent_bindings
		 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status IN ('pending_login', 'pending_approval', 'active', 'rejected')`,
		bindingID, actorUID, agentUID,
	).Scan(&channel, &channelAppID, &channelUserID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get channel agent binding link target: %w", err)
	}
	var conflictingCanonicalUID int64
	err := a.db.QueryRow(
		`SELECT COALESCE(canonical_uid, 0)
		 FROM channel_agent_bindings
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ?
		   AND canonical_uid IS NOT NULL AND canonical_uid <> ?
		 ORDER BY updated_at DESC LIMIT 1`,
		channel, channelAppID, channelUserID, canonicalUID,
	).Scan(&conflictingCanonicalUID)
	if err == nil && conflictingCanonicalUID > 0 {
		return nil, store.ErrChannelAgentBindingAlreadyLinked
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("check channel identity link conflict: %w", err)
	}
	result, err := a.db.Exec(
		`UPDATE channel_agent_bindings
		 SET canonical_uid = ?,
		     device_access_enabled = IF(?, TRUE, device_access_enabled),
		     status = CASE WHEN status = 'active' THEN 'active' ELSE 'pending_approval' END,
		     last_used_at = CASE WHEN status = 'active' THEN CURRENT_TIMESTAMP ELSE last_used_at END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status IN ('pending_login', 'pending_approval', 'active')
		   AND (canonical_uid IS NULL OR canonical_uid = 0 OR canonical_uid = ?)`,
		canonicalUID, enableDeviceAccess, bindingID, actorUID, agentUID, canonicalUID,
	)
	if err != nil {
		return nil, fmt.Errorf("link channel agent binding: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		row := a.db.QueryRow(
			`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
			 FROM channel_agent_bindings
			 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status IN ('pending_login', 'pending_approval', 'active', 'rejected')`,
			bindingID, actorUID, agentUID,
		)
		existing, lookupErr := scanChannelAgentBinding(row)
		if lookupErr == nil && existing.CanonicalUID > 0 && existing.CanonicalUID != canonicalUID {
			return nil, store.ErrChannelAgentBindingAlreadyLinked
		}
		if lookupErr == nil && existing.CanonicalUID == canonicalUID {
			if enableDeviceAccess && !existing.DeviceAccessEnabled {
				if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET device_access_enabled = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, existing.ID); err != nil {
					return nil, fmt.Errorf("enable channel agent device access: %w", err)
				}
				existing.DeviceAccessEnabled = true
			}
			return existing, nil
		}
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return nil, fmt.Errorf("check channel agent binding link conflict: %w", lookupErr)
		}
		return nil, nil
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE id = ?`,
		bindingID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get linked channel agent binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) CreateChannelIdentityMobileLink(link *types.ChannelIdentityMobileLink) (*types.ChannelIdentityMobileLink, error) {
	if link == nil || link.SceneKey == "" || link.EntryID <= 0 || link.Channel == "" || link.CanonicalUID <= 0 || link.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("invalid channel identity mobile link")
	}
	res, err := a.db.Exec(
		`INSERT INTO channel_identity_mobile_links (
		     scene_key, entry_id, channel, channel_app_id, canonical_uid, status, expires_at
		 )
		 VALUES (?, ?, ?, ?, ?, 'active', ?)`,
		link.SceneKey, link.EntryID, link.Channel, link.ChannelAppID, link.CanonicalUID, link.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create channel identity mobile link: %w", err)
	}
	id, _ := res.LastInsertId()
	row := a.db.QueryRow(
		`SELECT id, scene_key, entry_id, channel, channel_app_id, canonical_uid, status, expires_at, consumed_at, created_at, updated_at
		 FROM channel_identity_mobile_links WHERE id = ?`,
		id,
	)
	created, err := scanChannelIdentityMobileLink(row)
	if err != nil {
		return nil, fmt.Errorf("get channel identity mobile link: %w", err)
	}
	return created, nil
}

func (a *Adapter) GetChannelIdentityMobileLink(sceneKey string) (*types.ChannelIdentityMobileLink, error) {
	sceneKey = strings.TrimSpace(sceneKey)
	if sceneKey == "" {
		return nil, fmt.Errorf("invalid channel identity mobile link")
	}
	row := a.db.QueryRow(
		`SELECT id, scene_key, entry_id, channel, channel_app_id, canonical_uid, status, expires_at, consumed_at, created_at, updated_at
		 FROM channel_identity_mobile_links
		 WHERE scene_key = ?`,
		sceneKey,
	)
	link, err := scanChannelIdentityMobileLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel identity mobile link: %w", err)
	}
	return link, nil
}

func (a *Adapter) ConsumeChannelIdentityMobileLink(sceneKey, channel, channelAppID string) (*types.ChannelIdentityMobileLink, error) {
	sceneKey = strings.TrimSpace(sceneKey)
	channel = strings.TrimSpace(channel)
	channelAppID = strings.TrimSpace(channelAppID)
	if sceneKey == "" || channel == "" {
		return nil, fmt.Errorf("invalid channel identity mobile link")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel identity mobile link consume: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRow(
		`SELECT id, scene_key, entry_id, channel, channel_app_id, canonical_uid, status, expires_at, consumed_at, created_at, updated_at
		 FROM channel_identity_mobile_links
		 WHERE scene_key = ?
		 FOR UPDATE`,
		sceneKey,
	)
	link, err := scanChannelIdentityMobileLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel identity mobile link: %w", err)
	}
	if link.Status != "active" || !link.ExpiresAt.After(time.Now()) || link.Channel != channel || (channelAppID != "" && link.ChannelAppID != channelAppID) {
		return nil, nil
	}
	if _, err := tx.Exec(
		`UPDATE channel_identity_mobile_links
		 SET status = 'consumed', consumed_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'active'`,
		link.ID,
	); err != nil {
		return nil, fmt.Errorf("consume channel identity mobile link: %w", err)
	}
	consumedAt := time.Now()
	link.Status = "consumed"
	link.ConsumedAt = &consumedAt
	link.UpdatedAt = consumedAt
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return link, nil
}

func (a *Adapter) CreateChannelGroupMobileLink(link *types.ChannelGroupMobileLink) (*types.ChannelGroupMobileLink, error) {
	if link == nil || link.SceneKey == "" || link.Channel == "" || link.CanonicalUID <= 0 || link.GroupID <= 0 || link.TopicID == "" || link.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("invalid channel group mobile link")
	}
	res, err := a.db.Exec(
		`INSERT INTO channel_group_mobile_links (
		     scene_key, channel, channel_app_id, canonical_uid, group_id, topic_id, status, expires_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`,
		link.SceneKey, link.Channel, link.ChannelAppID, link.CanonicalUID, link.GroupID, link.TopicID, link.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create channel group mobile link: %w", err)
	}
	id, _ := res.LastInsertId()
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, canonical_uid, group_id, topic_id, status, expires_at, consumed_at, created_at, updated_at
		 FROM channel_group_mobile_links WHERE id = ?`,
		id,
	)
	created, err := scanChannelGroupMobileLink(row)
	if err != nil {
		return nil, fmt.Errorf("get channel group mobile link: %w", err)
	}
	return created, nil
}

func (a *Adapter) GetChannelGroupMobileLink(sceneKey string) (*types.ChannelGroupMobileLink, error) {
	sceneKey = strings.TrimSpace(sceneKey)
	if sceneKey == "" {
		return nil, fmt.Errorf("invalid channel group mobile link")
	}
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, canonical_uid, group_id, topic_id, status, expires_at, consumed_at, created_at, updated_at
		 FROM channel_group_mobile_links
		 WHERE scene_key = ?`,
		sceneKey,
	)
	link, err := scanChannelGroupMobileLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel group mobile link: %w", err)
	}
	return link, nil
}

func (a *Adapter) ConsumeChannelGroupMobileLink(sceneKey, channel, channelAppID string) (*types.ChannelGroupMobileLink, error) {
	sceneKey = strings.TrimSpace(sceneKey)
	channel = strings.TrimSpace(channel)
	channelAppID = strings.TrimSpace(channelAppID)
	if sceneKey == "" || channel == "" {
		return nil, fmt.Errorf("invalid channel group mobile link")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel group mobile link consume: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, canonical_uid, group_id, topic_id, status, expires_at, consumed_at, created_at, updated_at
		 FROM channel_group_mobile_links
		 WHERE scene_key = ?
		 FOR UPDATE`,
		sceneKey,
	)
	link, err := scanChannelGroupMobileLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel group mobile link: %w", err)
	}
	if link.Status != "active" || !link.ExpiresAt.After(time.Now()) || link.Channel != channel || (channelAppID != "" && link.ChannelAppID != channelAppID) {
		return nil, nil
	}
	if _, err := tx.Exec(
		`UPDATE channel_group_mobile_links
		 SET status = 'consumed', consumed_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'active'`,
		link.ID,
	); err != nil {
		return nil, fmt.Errorf("consume channel group mobile link: %w", err)
	}
	consumedAt := time.Now()
	link.Status = "consumed"
	link.ConsumedAt = &consumedAt
	link.UpdatedAt = consumedAt
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return link, nil
}

func (a *Adapter) UpsertChannelGroupBinding(binding *types.ChannelGroupBinding) (*types.ChannelGroupBinding, error) {
	if binding == nil || binding.Channel == "" || binding.ChannelUserID == "" || binding.CanonicalUID <= 0 || binding.GroupID <= 0 || binding.TopicID == "" {
		return nil, fmt.Errorf("invalid channel group binding")
	}
	conversationType := normalizeGroupBindingConversationType(binding.ChannelConversationType)
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel group selection: %w", err)
	}
	defer tx.Rollback()
	if err := lockChannelRouteSelectionTx(tx, binding.Channel, binding.ChannelAppID, binding.ChannelUserID, binding.ChannelConversationID, conversationType); err != nil {
		return nil, err
	}
	_, err = tx.Exec(
		`INSERT INTO channel_group_bindings (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, canonical_uid, group_id, topic_id, status, bound_at, selected_at, last_used_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		     actor_uid = COALESCE(VALUES(actor_uid), actor_uid),
		     canonical_uid = VALUES(canonical_uid),
		     group_id = VALUES(group_id),
		     topic_id = VALUES(topic_id),
		     status = 'active',
		     selected_at = CURRENT_TIMESTAMP,
		     last_used_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP`,
		binding.Channel,
		binding.ChannelAppID,
		binding.ChannelUserID,
		binding.ChannelConversationID,
		conversationType,
		nullableInt64(binding.ActorUID),
		binding.CanonicalUID,
		binding.GroupID,
		binding.TopicID,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert channel group binding: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM channel_agent_routes
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ?
		   AND channel_conversation_id = ? AND channel_conversation_type = ?`,
		binding.Channel,
		binding.ChannelAppID,
		binding.ChannelUserID,
		binding.ChannelConversationID,
		conversationType,
	); err != nil {
		return nil, fmt.Errorf("clear superseded channel agent route: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit channel group selection: %w", err)
	}
	return a.ResolveChannelGroupBinding(types.ChannelGroupBindingQuery{
		Channel:                 binding.Channel,
		ChannelAppID:            binding.ChannelAppID,
		ChannelUserID:           binding.ChannelUserID,
		ChannelConversationID:   binding.ChannelConversationID,
		ChannelConversationType: conversationType,
	})
}

func (a *Adapter) ResolveChannelGroupBinding(query types.ChannelGroupBindingQuery) (*types.ChannelGroupBinding, error) {
	if query.Channel == "" || query.ChannelUserID == "" {
		return nil, fmt.Errorf("invalid channel group binding query")
	}
	conversationType := normalizeGroupBindingConversationType(query.ChannelConversationType)
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), canonical_uid, group_id, topic_id, status, bound_at, selected_at, updated_at, last_used_at
		 FROM channel_group_bindings
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND channel_conversation_id = ?
		   AND channel_conversation_type = ?
		   AND (? = 0 OR actor_uid IS NULL OR actor_uid = ?)
		   AND (? = 0 OR group_id = ?)
		   AND (? = '' OR topic_id = ?)
		   AND status IN ('active', 'revoked')
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID, conversationType,
		query.ActorUID, query.ActorUID, query.GroupID, query.GroupID, strings.TrimSpace(query.TopicID), strings.TrimSpace(query.TopicID),
	)
	binding, err := scanChannelGroupBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel group binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_group_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
		return nil, fmt.Errorf("touch channel group binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) ListChannelGroupBindingsForTopic(topicID string) ([]*types.ChannelGroupBinding, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil, fmt.Errorf("invalid channel group topic")
	}
	rows, err := a.db.Query(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), canonical_uid, group_id, topic_id, status, bound_at, selected_at, updated_at, last_used_at
		 FROM channel_group_bindings
		 WHERE topic_id = ? AND status = 'active'
		 ORDER BY updated_at DESC`,
		topicID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel group bindings: %w", err)
	}
	defer rows.Close()

	var bindings []*types.ChannelGroupBinding
	for rows.Next() {
		binding, err := scanChannelGroupBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bindings, nil
}

func (a *Adapter) UpsertChannelAgentRoute(route *types.ChannelAgentRoute) (*types.ChannelAgentRoute, error) {
	if route == nil || route.Channel == "" || route.ChannelUserID == "" || route.AgentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent route")
	}
	conversationType := route.ChannelConversationType
	if conversationType == "" {
		conversationType = "p2p"
	}
	source := strings.TrimSpace(route.Source)
	if source == "" {
		source = "manual"
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel agent route selection: %w", err)
	}
	defer tx.Rollback()
	if route.Channel == "feishu" && conversationType == "p2p" && strings.TrimSpace(route.ChannelConversationID) != "" {
		if err := lockChannelRouteSelectionTx(tx, route.Channel, route.ChannelAppID, route.ChannelUserID, "", "p2p"); err != nil {
			return nil, err
		}
	}
	if err := lockChannelRouteSelectionTx(tx, route.Channel, route.ChannelAppID, route.ChannelUserID, route.ChannelConversationID, conversationType); err != nil {
		return nil, err
	}
	if route.Channel == "feishu" && conversationType == "p2p" {
		var active int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM channel_agent_bindings
			 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND agent_uid = ? AND status = 'active'`,
			route.Channel, route.ChannelAppID, route.ChannelUserID, route.AgentUID,
		).Scan(&active); err != nil {
			return nil, fmt.Errorf("verify active channel agent binding: %w", err)
		}
		if active == 0 {
			return nil, fmt.Errorf("channel agent binding is not active")
		}
	}
	if _, err := tx.Exec(
		`UPDATE channel_group_bindings
		 SET status = 'revoked', updated_at = CURRENT_TIMESTAMP
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ?
		   AND channel_conversation_id = ? AND channel_conversation_type = ?
		   AND status = 'active'`,
		route.Channel,
		route.ChannelAppID,
		route.ChannelUserID,
		route.ChannelConversationID,
		conversationType,
	); err != nil {
		return nil, fmt.Errorf("revoke superseded channel group binding: %w", err)
	}
	_, err = tx.Exec(
		`INSERT INTO channel_agent_routes (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, agent_uid, source, selected_at, last_used_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		     actor_uid = COALESCE(VALUES(actor_uid), actor_uid),
		     agent_uid = VALUES(agent_uid),
		     source = VALUES(source),
		     selected_at = CURRENT_TIMESTAMP,
		     last_used_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP`,
		route.Channel,
		route.ChannelAppID,
		route.ChannelUserID,
		route.ChannelConversationID,
		conversationType,
		nullableInt64(route.ActorUID),
		route.AgentUID,
		source,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert channel agent route: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit channel agent route selection: %w", err)
	}
	return a.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 route.Channel,
		ChannelAppID:            route.ChannelAppID,
		ChannelUserID:           route.ChannelUserID,
		ChannelConversationID:   route.ChannelConversationID,
		ChannelConversationType: conversationType,
	})
}

func lockChannelRouteSelectionTx(tx *sql.Tx, channel, appID, userID, conversationID, conversationType string) error {
	if _, err := tx.Exec(
		`INSERT INTO channel_route_selection_locks (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type
		 ) VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE updated_at = updated_at`,
		channel, appID, userID, conversationID, conversationType,
	); err != nil {
		return fmt.Errorf("lock channel route selection: %w", err)
	}
	return nil
}

func (a *Adapter) ResolveChannelAgentRoute(query types.ChannelAgentRouteQuery) (*types.ChannelAgentRoute, error) {
	if query.Channel == "" || query.ChannelUserID == "" {
		return nil, fmt.Errorf("invalid channel agent route query")
	}
	conversationType := query.ChannelConversationType
	if conversationType == "" {
		conversationType = "p2p"
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), agent_uid, source, selected_at, updated_at, last_used_at
		 FROM channel_agent_routes
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND channel_conversation_id = ?
		   AND channel_conversation_type = ?
		   AND (? = 0 OR actor_uid IS NULL OR actor_uid = ?)
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID, conversationType, query.ActorUID, query.ActorUID,
	)
	route, err := scanChannelAgentRoute(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent route: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_routes SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, route.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent route: %w", err)
	}
	return route, nil
}

func (a *Adapter) getActiveChannelAgentEntry(ownerUID, agentUID int64, channel, channelAppID, accessMode string) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = ? AND agent_uid = ? AND channel = ? AND channel_app_id = ? AND access_mode = ? AND status = 'active'
		 ORDER BY created_at DESC LIMIT 1`,
		ownerUID, agentUID, channel, channelAppID, types.NormalizeChannelAgentAccessMode(accessMode),
	)
	entry, err := scanChannelAgentEntry(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active channel agent entry: %w", err)
	}
	return entry, nil
}

func getActiveChannelAgentEntryTx(tx *sql.Tx, ownerUID, agentUID int64, channel, channelAppID, accessMode string) (*types.ChannelAgentEntry, error) {
	row := tx.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = ? AND agent_uid = ? AND channel = ? AND channel_app_id = ? AND access_mode = ? AND status = 'active'
		 ORDER BY created_at DESC LIMIT 1`,
		ownerUID, agentUID, channel, channelAppID, types.NormalizeChannelAgentAccessMode(accessMode),
	)
	entry, err := scanChannelAgentEntry(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active channel agent entry: %w", err)
	}
	return entry, nil
}

func (a *Adapter) getChannelAgentAccessRequestByID(id int64) (*types.ChannelAgentAccessRequest, error) {
	row := a.db.QueryRow(
		`SELECT id, entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        actor_uid, owner_uid, agent_uid, status, COALESCE(reviewed_by_uid, 0), requested_at, updated_at, reviewed_at
		 FROM channel_agent_access_requests WHERE id = ?`,
		id,
	)
	request, err := scanChannelAgentAccessRequest(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel agent access request: %w", err)
	}
	return request, nil
}

func (a *Adapter) getChannelAgentAccessRequest(query types.ChannelAgentBindingQuery, conversationID string) (*types.ChannelAgentAccessRequest, error) {
	row := a.db.QueryRow(
		`SELECT id, entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        actor_uid, owner_uid, agent_uid, status, COALESCE(reviewed_by_uid, 0), requested_at, updated_at, reviewed_at
		 FROM channel_agent_access_requests
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND channel_conversation_id = ?
		   AND (? = 0 OR agent_uid = ?)
		   AND (? = 0 OR actor_uid = ?)
		   AND status IN ('pending', 'rejected')
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, conversationID, query.AgentUID, query.AgentUID, query.ActorUID, query.ActorUID,
	)
	request, err := scanChannelAgentAccessRequest(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent access request: %w", err)
	}
	return request, nil
}

func (a *Adapter) getChannelAgentBinding(query types.ChannelAgentBindingQuery, conversationID string) (*types.ChannelAgentBinding, error) {
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND channel_conversation_id = ?
		   AND (? = 0 OR agent_uid = ?)
		   AND (? = 0 OR actor_uid = ?)
		   AND status IN ('active', 'pending_login', 'pending_approval', 'rejected')
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, conversationID, query.AgentUID, query.AgentUID, query.ActorUID, query.ActorUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent binding: %w", err)
	}
	return binding, nil
}

type channelAgentRouteScanner interface {
	Scan(dest ...interface{}) error
}

type channelIdentityMobileLinkScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelIdentityMobileLink(row channelIdentityMobileLinkScanner) (*types.ChannelIdentityMobileLink, error) {
	link := &types.ChannelIdentityMobileLink{}
	var consumedAt sql.NullTime
	if err := row.Scan(
		&link.ID,
		&link.SceneKey,
		&link.EntryID,
		&link.Channel,
		&link.ChannelAppID,
		&link.CanonicalUID,
		&link.Status,
		&link.ExpiresAt,
		&consumedAt,
		&link.CreatedAt,
		&link.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if consumedAt.Valid {
		link.ConsumedAt = &consumedAt.Time
	}
	return link, nil
}

type channelGroupMobileLinkScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelGroupMobileLink(row channelGroupMobileLinkScanner) (*types.ChannelGroupMobileLink, error) {
	link := &types.ChannelGroupMobileLink{}
	var consumedAt sql.NullTime
	if err := row.Scan(
		&link.ID,
		&link.SceneKey,
		&link.Channel,
		&link.ChannelAppID,
		&link.CanonicalUID,
		&link.GroupID,
		&link.TopicID,
		&link.Status,
		&link.ExpiresAt,
		&consumedAt,
		&link.CreatedAt,
		&link.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if consumedAt.Valid {
		link.ConsumedAt = &consumedAt.Time
	}
	return link, nil
}

type channelGroupBindingScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelGroupBinding(row channelGroupBindingScanner) (*types.ChannelGroupBinding, error) {
	binding := &types.ChannelGroupBinding{}
	var lastUsedAt sql.NullTime
	if err := row.Scan(
		&binding.ID,
		&binding.Channel,
		&binding.ChannelAppID,
		&binding.ChannelUserID,
		&binding.ChannelConversationID,
		&binding.ChannelConversationType,
		&binding.ActorUID,
		&binding.CanonicalUID,
		&binding.GroupID,
		&binding.TopicID,
		&binding.Status,
		&binding.BoundAt,
		&binding.SelectedAt,
		&binding.UpdatedAt,
		&lastUsedAt,
	); err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		binding.LastUsedAt = &lastUsedAt.Time
	}
	return binding, nil
}

func scanChannelAgentRoute(row channelAgentRouteScanner) (*types.ChannelAgentRoute, error) {
	route := &types.ChannelAgentRoute{}
	var lastUsedAt sql.NullTime
	if err := row.Scan(
		&route.ID,
		&route.Channel,
		&route.ChannelAppID,
		&route.ChannelUserID,
		&route.ChannelConversationID,
		&route.ChannelConversationType,
		&route.ActorUID,
		&route.AgentUID,
		&route.Source,
		&route.SelectedAt,
		&route.UpdatedAt,
		&lastUsedAt,
	); err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		route.LastUsedAt = &lastUsedAt.Time
	}
	return route, nil
}

type channelAgentEntryScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelAgentEntry(row channelAgentEntryScanner) (*types.ChannelAgentEntry, error) {
	entry := &types.ChannelAgentEntry{}
	var lastUsedAt sql.NullTime
	if err := row.Scan(
		&entry.ID,
		&entry.SceneKey,
		&entry.Channel,
		&entry.ChannelAppID,
		&entry.AccessMode,
		&entry.OwnerUID,
		&entry.AgentUID,
		&entry.Status,
		&entry.CreatedAt,
		&entry.UpdatedAt,
		&lastUsedAt,
	); err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		entry.LastUsedAt = &lastUsedAt.Time
	}
	entry.AccessMode = types.NormalizeChannelAgentAccessMode(entry.AccessMode)
	return entry, nil
}

type channelAgentAccessRequestScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelAgentAccessRequest(row channelAgentAccessRequestScanner) (*types.ChannelAgentAccessRequest, error) {
	request := &types.ChannelAgentAccessRequest{}
	var reviewedAt sql.NullTime
	if err := row.Scan(
		&request.ID,
		&request.EntryID,
		&request.Channel,
		&request.ChannelAppID,
		&request.ChannelUserID,
		&request.ChannelConversationID,
		&request.ChannelConversationType,
		&request.ActorUID,
		&request.OwnerUID,
		&request.AgentUID,
		&request.Status,
		&request.ReviewedByUID,
		&request.RequestedAt,
		&request.UpdatedAt,
		&reviewedAt,
	); err != nil {
		return nil, err
	}
	if reviewedAt.Valid {
		request.ReviewedAt = &reviewedAt.Time
	}
	return request, nil
}

type channelAgentBindingScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelAgentBinding(row channelAgentBindingScanner) (*types.ChannelAgentBinding, error) {
	binding := &types.ChannelAgentBinding{}
	var lastUsedAt sql.NullTime
	if err := row.Scan(
		&binding.ID,
		&binding.Channel,
		&binding.ChannelAppID,
		&binding.ChannelUserID,
		&binding.ChannelConversationID,
		&binding.ChannelConversationType,
		&binding.ActorUID,
		&binding.CanonicalUID,
		&binding.DeviceAccessEnabled,
		&binding.OwnerUID,
		&binding.AgentUID,
		&binding.EntryID,
		&binding.Status,
		&binding.BoundAt,
		&binding.UpdatedAt,
		&lastUsedAt,
	); err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		binding.LastUsedAt = &lastUsedAt.Time
	}
	return binding, nil
}

func normalizeGroupBindingConversationType(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "group") {
		return "group"
	}
	return "p2p"
}

func nullableInt64(value int64) interface{} {
	if value <= 0 {
		return nil
	}
	return value
}
