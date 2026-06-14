package mysql

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ChannelAgentBindingStore = (*Adapter)(nil)

func (a *Adapter) EnsureChannelAgentEntry(entry *types.ChannelAgentEntry) (*types.ChannelAgentEntry, error) {
	if entry == nil || entry.OwnerUID <= 0 || entry.AgentUID <= 0 || entry.Channel == "" || entry.SceneKey == "" {
		return nil, fmt.Errorf("invalid channel agent entry")
	}
	accessMode := types.NormalizeChannelAgentAccessMode(entry.AccessMode)
	existing, err := a.getActiveChannelAgentEntry(entry.OwnerUID, entry.AgentUID, entry.Channel, entry.ChannelAppID, accessMode)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	res, err := a.db.Exec(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		entry.SceneKey, entry.Channel, entry.ChannelAppID, accessMode, entry.OwnerUID, entry.AgentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("create channel agent entry: %w", err)
	}
	id, _ := res.LastInsertId()
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
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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
			     actor_uid, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
			 )
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			 ON DUPLICATE KEY UPDATE
			     actor_uid = VALUES(actor_uid),
			     owner_uid = VALUES(owner_uid),
			     entry_id = COALESCE(VALUES(entry_id), entry_id),
			     channel_conversation_type = VALUES(channel_conversation_type),
			     status = 'active',
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
		 SET status = 'active', bound_at = CURRENT_TIMESTAMP, last_used_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = ? AND agent_uid = ? AND status IN ('pending_approval', 'active')`,
		canonicalUID, agentUID,
	); err != nil {
		return nil, fmt.Errorf("activate channel agent binding: %w", err)
	}
	rows, err := a.db.Query(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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
		     actor_uid, canonical_uid, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		     actor_uid = COALESCE(VALUES(actor_uid), actor_uid),
		     canonical_uid = COALESCE(VALUES(canonical_uid), canonical_uid),
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
		binding.OwnerUID,
		binding.AgentUID,
		nullableInt64(binding.EntryID),
		status,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert channel agent binding: %w", err)
	}
	return a.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 binding.Channel,
		ChannelAppID:            binding.ChannelAppID,
		ChannelUserID:           binding.ChannelUserID,
		ChannelConversationID:   binding.ChannelConversationID,
		ChannelConversationType: conversationType,
		AgentUID:                binding.AgentUID,
	})
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
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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

func (a *Adapter) ResolveChannelAgentBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if actorUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent actor query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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

func (a *Adapter) LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64) (*types.ChannelAgentBinding, error) {
	if bindingID <= 0 || actorUID <= 0 || agentUID <= 0 || canonicalUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent link")
	}
	result, err := a.db.Exec(
		`UPDATE channel_agent_bindings
		 SET canonical_uid = ?,
		     status = CASE WHEN status = 'active' THEN 'active' ELSE 'pending_approval' END,
		     last_used_at = CASE WHEN status = 'active' THEN CURRENT_TIMESTAMP ELSE last_used_at END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status IN ('pending_login', 'pending_approval', 'active')
		   AND (canonical_uid IS NULL OR canonical_uid = 0 OR canonical_uid = ?)`,
		canonicalUID, bindingID, actorUID, agentUID, canonicalUID,
	)
	if err != nil {
		return nil, fmt.Errorf("link channel agent binding: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		row := a.db.QueryRow(
			`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
			 FROM channel_agent_bindings
			 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status IN ('pending_login', 'pending_approval', 'active', 'rejected')`,
			bindingID, actorUID, agentUID,
		)
		existing, lookupErr := scanChannelAgentBinding(row)
		if lookupErr == nil && existing.CanonicalUID > 0 && existing.CanonicalUID != canonicalUID {
			return nil, store.ErrChannelAgentBindingAlreadyLinked
		}
		if lookupErr == nil && existing.CanonicalUID == canonicalUID {
			return existing, nil
		}
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return nil, fmt.Errorf("check channel agent binding link conflict: %w", lookupErr)
		}
		return nil, nil
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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
	_, err := a.db.Exec(
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
	return a.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 route.Channel,
		ChannelAppID:            route.ChannelAppID,
		ChannelUserID:           route.ChannelUserID,
		ChannelConversationID:   route.ChannelConversationID,
		ChannelConversationType: conversationType,
	})
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
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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

func nullableInt64(value int64) interface{} {
	if value <= 0 {
		return nil
	}
	return value
}
