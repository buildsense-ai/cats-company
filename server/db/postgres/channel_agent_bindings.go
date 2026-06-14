package postgres

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

	var id int64
	if err := a.db.QueryRow(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, access_mode, owner_uid, agent_uid, status)
		 VALUES ($1, $2, $3, $4, $5, $6, 'active')
		 RETURNING id`,
		entry.SceneKey, entry.Channel, entry.ChannelAppID, accessMode, entry.OwnerUID, entry.AgentUID,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("create channel agent entry: %w", err)
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
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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
			     actor_uid, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
			 )
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, 0), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid)
			 DO UPDATE SET
			     actor_uid = EXCLUDED.actor_uid,
			     owner_uid = EXCLUDED.owner_uid,
			     entry_id = COALESCE(EXCLUDED.entry_id, channel_agent_bindings.entry_id),
			     channel_conversation_type = EXCLUDED.channel_conversation_type,
			     status = 'active',
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
		 SET status = 'active', bound_at = CURRENT_TIMESTAMP, last_used_at = CURRENT_TIMESTAMP
		 WHERE canonical_uid = $1 AND agent_uid = $2 AND status IN ('pending_approval', 'active')
		 RETURNING id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		           COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at`,
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
		     actor_uid, canonical_uid, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
		 )
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8, $9, NULLIF($10, 0), $11, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid)
		 DO UPDATE SET
		     actor_uid = COALESCE(EXCLUDED.actor_uid, channel_agent_bindings.actor_uid),
		     canonical_uid = COALESCE(EXCLUDED.canonical_uid, channel_agent_bindings.canonical_uid),
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
		binding.OwnerUID,
		binding.AgentUID,
		binding.EntryID,
		status,
	); err != nil {
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

func (a *Adapter) ResolveChannelAgentBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if actorUID <= 0 || agentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent actor query")
	}
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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

func (a *Adapter) LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64) (*types.ChannelAgentBinding, error) {
	if bindingID <= 0 || actorUID <= 0 || agentUID <= 0 || canonicalUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent link")
	}
	row := a.db.QueryRow(
		`UPDATE channel_agent_bindings
		 SET canonical_uid = $1,
		     status = CASE WHEN status = 'active' THEN 'active' ELSE 'pending_approval' END,
		     last_used_at = CASE WHEN status = 'active' THEN CURRENT_TIMESTAMP ELSE last_used_at END
		 WHERE id = $2 AND actor_uid = $3 AND agent_uid = $4 AND status IN ('pending_login', 'pending_approval', 'active')
		   AND (canonical_uid IS NULL OR canonical_uid = 0 OR canonical_uid = $1)
		 RETURNING id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		           COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at`,
		canonicalUID, bindingID, actorUID, agentUID,
	)
	binding, err := scanChannelAgentBinding(row)
	if err == sql.ErrNoRows {
		existingRow := a.db.QueryRow(
			`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
			        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
			 FROM channel_agent_bindings
			 WHERE id = $1 AND actor_uid = $2 AND agent_uid = $3 AND status IN ('pending_login', 'pending_approval', 'active', 'rejected')`,
			bindingID, actorUID, agentUID,
		)
		existing, lookupErr := scanChannelAgentBinding(existingRow)
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
	if err != nil {
		return nil, fmt.Errorf("link channel agent binding: %w", err)
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
	if _, err := a.db.Exec(
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
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
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
