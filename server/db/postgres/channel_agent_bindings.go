package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ChannelAgentBindingStore = (*Adapter)(nil)

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
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = $1 FOR UPDATE`, entry.AgentUID).Scan(&lockedAgentID); err != nil {
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

	var id int64
	if err := tx.QueryRow(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status)
		 VALUES ($1, $2, $3, $4, $5, $6, 'active')
		 RETURNING id`,
		entry.SceneKey, entry.Channel, entry.ChannelAppID, accessMode, entry.OwnerUID, entry.AgentUID,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("create channel agent entry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.GetChannelAgentEntryByID(id)
}

func (a *Adapter) ListChannelAgentEntries(ownerUID, agentUID int64) ([]*types.ChannelAgentEntry, error) {
	rows, err := a.db.Query(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = $1 AND agent_uid = $2 AND status = 'active'
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
		 WHERE channel = $1 AND channel_app_id = $2 AND status = 'active'
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
		`SELECT channel, channel_app_id, access_mode, agent_uid FROM channel_agent_entries WHERE id = $1 AND owner_uid = $2 AND status = 'active'`,
		id, ownerUID,
	).Scan(&channel, &channelAppID, &accessMode, &agentUID); err != nil {
		return nil, fmt.Errorf("get channel agent entry: %w", err)
	}
	if strings.TrimSpace(nextChannelAppID) != "" {
		channelAppID = strings.TrimSpace(nextChannelAppID)
	}
	if _, err := tx.Exec(
		`UPDATE channel_agent_entries SET status = 'revoked' WHERE id = $1 AND owner_uid = $2`,
		id, ownerUID,
	); err != nil {
		return nil, fmt.Errorf("revoke channel agent entry: %w", err)
	}
	var newID int64
	if err := tx.QueryRow(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status)
		 VALUES ($1, $2, $3, $4, $5, $6, 'active')
		 RETURNING id`,
		sceneKey, channel, channelAppID, types.NormalizeChannelAgentAccessMode(accessMode), ownerUID, agentUID,
	).Scan(&newID); err != nil {
		return nil, fmt.Errorf("create regenerated channel agent entry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.GetChannelAgentEntryByID(newID)
}

func (a *Adapter) GetChannelAgentEntryByID(id int64) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries WHERE id = $1`,
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
		 FROM channel_agent_entries WHERE scene_key = $1`,
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
		 WHERE owner_uid = $1 AND agent_uid = $2 AND status IN ('active', 'pending_login', 'pending_approval', 'rejected')
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
	var id int64
	if err := a.db.QueryRow(
		`INSERT INTO channel_agent_access_requests (
		     entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, owner_uid, agent_uid, status, requested_at
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', CURRENT_TIMESTAMP)
		 ON CONFLICT (entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id)
		 DO UPDATE SET
		     channel_conversation_type = EXCLUDED.channel_conversation_type,
		     actor_uid = EXCLUDED.actor_uid,
		     owner_uid = EXCLUDED.owner_uid,
		     agent_uid = EXCLUDED.agent_uid,
		     status = CASE WHEN channel_agent_access_requests.status = 'approved' THEN 'approved' ELSE 'pending' END,
		     requested_at = CASE WHEN channel_agent_access_requests.status = 'approved' THEN channel_agent_access_requests.requested_at ELSE CURRENT_TIMESTAMP END,
		     reviewed_by_uid = CASE WHEN channel_agent_access_requests.status = 'approved' THEN channel_agent_access_requests.reviewed_by_uid ELSE NULL END,
		     reviewed_at = CASE WHEN channel_agent_access_requests.status = 'approved' THEN channel_agent_access_requests.reviewed_at ELSE NULL END
		 RETURNING id`,
		request.EntryID,
		request.Channel,
		request.ChannelAppID,
		request.ChannelUserID,
		request.ChannelConversationID,
		conversationType,
		request.ActorUID,
		request.OwnerUID,
		request.AgentUID,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("request channel agent access: %w", err)
	}
	return a.getChannelAgentAccessRequestByID(id)
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
		 WHERE actor_uid = $1 AND agent_uid = $2 AND status = 'pending'
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
			 SET status = 'approved', reviewed_by_uid = $1, reviewed_at = CURRENT_TIMESTAMP
			 WHERE id = $2 AND status = 'pending'`,
			reviewerUID, request.ID,
		); err != nil {
			return nil, fmt.Errorf("approve channel agent access: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO channel_agent_bindings (
			     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			     actor_uid, owner_uid, agent_uid, entry_id, status, device_access_enabled, bound_at, last_used_at
			 )
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, 0), 'active', TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid)
			 DO UPDATE SET
			     actor_uid = EXCLUDED.actor_uid,
			     owner_uid = EXCLUDED.owner_uid,
			     entry_id = COALESCE(EXCLUDED.entry_id, channel_agent_bindings.entry_id),
			     channel_conversation_type = EXCLUDED.channel_conversation_type,
			     status = 'active',
			     device_access_enabled = TRUE,
			     bound_at = CURRENT_TIMESTAMP,
			     last_used_at = CURRENT_TIMESTAMP`,
			request.Channel,
			request.ChannelAppID,
			request.ChannelUserID,
			request.ChannelConversationID,
			request.ChannelConversationType,
			request.ActorUID,
			request.OwnerUID,
			request.AgentUID,
			request.EntryID,
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
		 SET status = 'rejected', reviewed_by_uid = $1, reviewed_at = CURRENT_TIMESTAMP
		 WHERE actor_uid = $2 AND agent_uid = $3 AND status = 'pending'`,
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
	rows, err := a.db.Query(
		`UPDATE channel_agent_bindings
		 SET status = 'active', device_access_enabled = TRUE, bound_at = CURRENT_TIMESTAMP, last_used_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = $1 AND agent_uid = $2 AND status IN ('pending_approval', 'active')
		 RETURNING id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		           COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at`,
		canonicalUID, agentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("activate channel agent binding: %w", err)
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
		 SET status = 'rejected'
		 WHERE canonical_uid = $1 AND agent_uid = $2 AND status IN ('pending_approval', 'active', 'pending_login')`,
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
	if _, err := a.db.Exec(
		`INSERT INTO channel_agent_bindings (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, canonical_uid, device_access_enabled, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
		 )
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8, $9, $10, NULLIF($11, 0), $12, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid)
		 DO UPDATE SET
		     actor_uid = COALESCE(EXCLUDED.actor_uid, channel_agent_bindings.actor_uid),
		     canonical_uid = COALESCE(EXCLUDED.canonical_uid, channel_agent_bindings.canonical_uid),
		     device_access_enabled = CASE WHEN EXCLUDED.device_access_enabled THEN TRUE ELSE channel_agent_bindings.device_access_enabled END,
		     owner_uid = EXCLUDED.owner_uid,
		     entry_id = COALESCE(EXCLUDED.entry_id, channel_agent_bindings.entry_id),
		     channel_conversation_type = EXCLUDED.channel_conversation_type,
		     status = EXCLUDED.status,
		     bound_at = CASE WHEN EXCLUDED.status = 'active' THEN CURRENT_TIMESTAMP ELSE channel_agent_bindings.bound_at END,
		     last_used_at = CASE WHEN EXCLUDED.status = 'active' THEN CURRENT_TIMESTAMP ELSE channel_agent_bindings.last_used_at END`,
		binding.Channel,
		binding.ChannelAppID,
		binding.ChannelUserID,
		binding.ChannelConversationID,
		conversationType,
		binding.ActorUID,
		binding.CanonicalUID,
		binding.DeviceAccessEnabled,
		binding.OwnerUID,
		binding.AgentUID,
		binding.EntryID,
		status,
	); err != nil {
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
		 WHERE channel = $1 AND channel_app_id = $2 AND actor_uid = $3 AND agent_uid = $4 AND status = 'active'
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
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, binding.ID); err != nil {
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
		 WHERE channel = $1 AND channel_app_id = $2 AND canonical_uid = $3 AND agent_uid = $4 AND status = 'active'
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
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, binding.ID); err != nil {
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
		 WHERE actor_uid = $1 AND agent_uid = $2 AND status = 'active'
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
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, binding.ID); err != nil {
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
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3 AND canonical_uid IS NOT NULL
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
		 WHERE actor_uid = $1 AND agent_uid = $2 AND status = 'active' AND canonical_uid IS NOT NULL AND device_access_enabled = TRUE
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
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, binding.ID); err != nil {
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
		 WHERE id = $1 AND actor_uid = $2 AND agent_uid = $3 AND status IN ('pending_login', 'pending_approval', 'active', 'rejected')`,
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
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3
		   AND canonical_uid IS NOT NULL AND canonical_uid <> $4
		 ORDER BY updated_at DESC LIMIT 1`,
		channel, channelAppID, channelUserID, canonicalUID,
	).Scan(&conflictingCanonicalUID)
	if err == nil && conflictingCanonicalUID > 0 {
		return nil, store.ErrChannelAgentBindingAlreadyLinked
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("check channel identity link conflict: %w", err)
	}
	row := a.db.QueryRow(
		`UPDATE channel_agent_bindings
		 SET canonical_uid = $1,
		     device_access_enabled = CASE WHEN $5 THEN TRUE ELSE device_access_enabled END,
		     status = CASE WHEN status = 'active' THEN 'active' ELSE 'pending_approval' END,
		     last_used_at = CASE WHEN status = 'active' THEN CURRENT_TIMESTAMP ELSE last_used_at END
		 WHERE id = $2 AND actor_uid = $3 AND agent_uid = $4 AND status IN ('pending_login', 'pending_approval', 'active')
		   AND (canonical_uid IS NULL OR canonical_uid = 0 OR canonical_uid = $1)
		 RETURNING id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		           COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at`,
		canonicalUID, bindingID, actorUID, agentUID, enableDeviceAccess,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		existingRow := a.db.QueryRow(
			`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), COALESCE(device_access_enabled, FALSE), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
			 FROM channel_agent_bindings
			 WHERE id = $1 AND actor_uid = $2 AND agent_uid = $3 AND status IN ('pending_login', 'pending_approval', 'active', 'rejected')`,
			bindingID, actorUID, agentUID,
		)
		existing, lookupErr := scanChannelAgentBinding(existingRow)
		if lookupErr == nil && existing.CanonicalUID > 0 && existing.CanonicalUID != canonicalUID {
			return nil, store.ErrChannelAgentBindingAlreadyLinked
		}
		if lookupErr == nil && existing.CanonicalUID == canonicalUID {
			if enableDeviceAccess && !existing.DeviceAccessEnabled {
				if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET device_access_enabled = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, existing.ID); err != nil {
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
	if err != nil {
		return nil, fmt.Errorf("link channel agent binding: %w", err)
	}
	return binding, nil
}

func (a *Adapter) CreateChannelIdentityMobileLink(link *types.ChannelIdentityMobileLink) (*types.ChannelIdentityMobileLink, error) {
	if link == nil || link.SceneKey == "" || link.EntryID <= 0 || link.Channel == "" || link.CanonicalUID <= 0 || link.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("invalid channel identity mobile link")
	}
	row := a.db.QueryRow(
		`INSERT INTO channel_identity_mobile_links (
		     scene_key, entry_id, channel, channel_app_id, canonical_uid, status, expires_at
		 )
		 VALUES ($1, $2, $3, $4, $5, 'active', $6)
		 RETURNING id, scene_key, entry_id, channel, channel_app_id, canonical_uid, status, expires_at, consumed_at, created_at, updated_at`,
		link.SceneKey, link.EntryID, link.Channel, link.ChannelAppID, link.CanonicalUID, link.ExpiresAt,
	)
	created, err := scanChannelIdentityMobileLink(row)
	if err != nil {
		return nil, fmt.Errorf("create channel identity mobile link: %w", err)
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
		 WHERE scene_key = $1`,
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
		 WHERE scene_key = $1
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
	var consumedAt time.Time
	if err := tx.QueryRow(
		`UPDATE channel_identity_mobile_links
		 SET status = 'consumed', consumed_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND status = 'active'
		 RETURNING consumed_at`,
		link.ID,
	).Scan(&consumedAt); err != nil {
		return nil, fmt.Errorf("consume channel identity mobile link: %w", err)
	}
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
	row := a.db.QueryRow(
		`INSERT INTO channel_group_mobile_links (
		     scene_key, channel, channel_app_id, canonical_uid, group_id, topic_id, status, expires_at
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, 'active', $7)
		 RETURNING id, scene_key, channel, channel_app_id, canonical_uid, group_id, topic_id, status, expires_at, consumed_at, created_at, updated_at`,
		link.SceneKey, link.Channel, link.ChannelAppID, link.CanonicalUID, link.GroupID, link.TopicID, link.ExpiresAt,
	)
	created, err := scanChannelGroupMobileLink(row)
	if err != nil {
		return nil, fmt.Errorf("create channel group mobile link: %w", err)
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
		 WHERE scene_key = $1`,
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
		 WHERE scene_key = $1
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
	var consumedAt time.Time
	if err := tx.QueryRow(
		`UPDATE channel_group_mobile_links
		 SET status = 'consumed', consumed_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND status = 'active'
		 RETURNING consumed_at`,
		link.ID,
	).Scan(&consumedAt); err != nil {
		return nil, fmt.Errorf("consume channel group mobile link: %w", err)
	}
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
	if _, err := tx.Exec(
		`INSERT INTO channel_group_bindings (
			     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			     actor_uid, canonical_uid, group_id, topic_id, status, bound_at, selected_at, last_used_at
			 )
			 VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), $7, $8, $9, 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
			 DO UPDATE SET
			     actor_uid = COALESCE(EXCLUDED.actor_uid, channel_group_bindings.actor_uid),
			     canonical_uid = EXCLUDED.canonical_uid,
			     group_id = EXCLUDED.group_id,
			     topic_id = EXCLUDED.topic_id,
			     status = 'active',
			     selected_at = CURRENT_TIMESTAMP,
			     last_used_at = CURRENT_TIMESTAMP`,
		binding.Channel,
		binding.ChannelAppID,
		binding.ChannelUserID,
		binding.ChannelConversationID,
		conversationType,
		binding.ActorUID,
		binding.CanonicalUID,
		binding.GroupID,
		binding.TopicID,
	); err != nil {
		return nil, fmt.Errorf("upsert channel group binding: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM channel_agent_routes
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3
		   AND channel_conversation_id = $4 AND channel_conversation_type = $5`,
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
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3 AND channel_conversation_id = $4
		   AND channel_conversation_type = $5
		   AND ($6 = 0 OR actor_uid IS NULL OR actor_uid = $6)
		   AND ($7 = 0 OR group_id = $7)
		   AND ($8 = '' OR topic_id = $8)
		   AND status IN ('active', 'revoked')
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID, conversationType, query.ActorUID, query.GroupID, strings.TrimSpace(query.TopicID),
	)
	binding, err := scanChannelGroupBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel group binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_group_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, binding.ID); err != nil {
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
		 WHERE topic_id = $1 AND status = 'active'
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
	if err := lockChannelRouteSelectionTx(tx, route.Channel, route.ChannelAppID, route.ChannelUserID, route.ChannelConversationID, conversationType); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE channel_group_bindings
		 SET status = 'revoked', updated_at = CURRENT_TIMESTAMP
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3
		   AND channel_conversation_id = $4 AND channel_conversation_type = $5
		   AND status = 'active'`,
		route.Channel,
		route.ChannelAppID,
		route.ChannelUserID,
		route.ChannelConversationID,
		conversationType,
	); err != nil {
		return nil, fmt.Errorf("revoke superseded channel group binding: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO channel_agent_routes (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, agent_uid, source, selected_at, last_used_at
		 )
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), $7, $8, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
		 DO UPDATE SET
		     actor_uid = COALESCE(EXCLUDED.actor_uid, channel_agent_routes.actor_uid),
		     agent_uid = EXCLUDED.agent_uid,
		     source = EXCLUDED.source,
		     selected_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP,
		     last_used_at = CURRENT_TIMESTAMP`,
		route.Channel,
		route.ChannelAppID,
		route.ChannelUserID,
		route.ChannelConversationID,
		conversationType,
		route.ActorUID,
		route.AgentUID,
		source,
	); err != nil {
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
		 ) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
		 DO UPDATE SET updated_at = channel_route_selection_locks.updated_at`,
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
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3 AND channel_conversation_id = $4
		   AND channel_conversation_type = $5
		   AND ($6 = 0 OR actor_uid IS NULL OR actor_uid = $6)
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, query.ChannelConversationID, conversationType, query.ActorUID,
	)
	route, err := scanChannelAgentRoute(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent route: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_routes SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, route.ID); err != nil {
		return nil, fmt.Errorf("touch channel agent route: %w", err)
	}
	return route, nil
}

func (a *Adapter) getActiveChannelAgentEntry(ownerUID, agentUID int64, channel, channelAppID, accessMode string) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = $1 AND agent_uid = $2 AND channel = $3 AND channel_app_id = $4 AND access_mode = $5 AND status = 'active'
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
		 WHERE owner_uid = $1 AND agent_uid = $2 AND channel = $3 AND channel_app_id = $4 AND access_mode = $5 AND status = 'active'
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
		 FROM channel_agent_access_requests WHERE id = $1`,
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
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3 AND channel_conversation_id = $4
		   AND ($5 = 0 OR agent_uid = $5)
		   AND ($6 = 0 OR actor_uid = $6)
		   AND status IN ('pending', 'rejected')
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, conversationID, query.AgentUID, query.ActorUID,
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
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3 AND channel_conversation_id = $4
		   AND ($5 = 0 OR agent_uid = $5)
		   AND ($6 = 0 OR actor_uid = $6)
		   AND status IN ('active', 'pending_login', 'pending_approval', 'rejected')
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, conversationID, query.AgentUID, query.ActorUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve channel agent binding: %w", err)
	}
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1`, binding.ID); err != nil {
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
