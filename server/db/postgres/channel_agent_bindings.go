package postgres

import (
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ChannelAgentBindingStore = (*Adapter)(nil)

func (a *Adapter) EnsureChannelAgentEntry(entry *types.ChannelAgentEntry) (*types.ChannelAgentEntry, error) {
	if entry == nil || entry.OwnerUID <= 0 || entry.AgentUID <= 0 || entry.Channel == "" || entry.SceneKey == "" {
		return nil, fmt.Errorf("invalid channel agent entry")
	}
	existing, err := a.getActiveChannelAgentEntry(entry.OwnerUID, entry.AgentUID, entry.Channel, entry.ChannelAppID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	var id int64
	if err := a.db.QueryRow(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, owner_uid, agent_uid, status)
		 VALUES ($1, $2, $3, $4, $5, 'active')
		 RETURNING id`,
		entry.SceneKey, entry.Channel, entry.ChannelAppID, entry.OwnerUID, entry.AgentUID,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("create channel agent entry: %w", err)
	}
	return a.GetChannelAgentEntryByID(id)
}

func (a *Adapter) ListChannelAgentEntries(ownerUID, agentUID int64) ([]*types.ChannelAgentEntry, error) {
	rows, err := a.db.Query(
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
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

func (a *Adapter) RegenerateChannelAgentEntry(id, ownerUID int64, sceneKey string) (*types.ChannelAgentEntry, error) {
	if id <= 0 || ownerUID <= 0 || sceneKey == "" {
		return nil, fmt.Errorf("invalid channel agent entry")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin channel agent entry regeneration: %w", err)
	}
	defer tx.Rollback()

	var channel, channelAppID string
	var agentUID int64
	if err := tx.QueryRow(
		`SELECT channel, channel_app_id, agent_uid FROM channel_agent_entries WHERE id = $1 AND owner_uid = $2 AND status = 'active'`,
		id, ownerUID,
	).Scan(&channel, &channelAppID, &agentUID); err != nil {
		return nil, fmt.Errorf("get channel agent entry: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE channel_agent_entries SET status = 'revoked' WHERE id = $1 AND owner_uid = $2`,
		id, ownerUID,
	); err != nil {
		return nil, fmt.Errorf("revoke channel agent entry: %w", err)
	}
	var newID int64
	if err := tx.QueryRow(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, owner_uid, agent_uid, status)
		 VALUES ($1, $2, $3, $4, $5, 'active')
		 RETURNING id`,
		sceneKey, channel, channelAppID, ownerUID, agentUID,
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
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
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
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
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

func (a *Adapter) UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error) {
	if binding == nil || binding.Channel == "" || binding.ChannelUserID == "" || binding.OwnerUID <= 0 || binding.AgentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent binding")
	}
	conversationType := binding.ChannelConversationType
	if conversationType == "" {
		conversationType = "p2p"
	}
	if _, err := a.db.Exec(
		`INSERT INTO channel_agent_bindings (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, canonical_uid, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
		 )
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8, $9, NULLIF($10, 0), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT (channel, channel_app_id, channel_user_id, channel_conversation_id)
		 DO UPDATE SET
		     actor_uid = COALESCE(EXCLUDED.actor_uid, channel_agent_bindings.actor_uid),
		     canonical_uid = COALESCE(EXCLUDED.canonical_uid, channel_agent_bindings.canonical_uid),
		     owner_uid = EXCLUDED.owner_uid,
		     agent_uid = EXCLUDED.agent_uid,
		     entry_id = COALESCE(EXCLUDED.entry_id, channel_agent_bindings.entry_id),
		     channel_conversation_type = EXCLUDED.channel_conversation_type,
		     status = 'active',
		     bound_at = CURRENT_TIMESTAMP,
		     last_used_at = CURRENT_TIMESTAMP`,
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
	); err != nil {
		return nil, fmt.Errorf("upsert channel agent binding: %w", err)
	}
	return a.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 binding.Channel,
		ChannelAppID:            binding.ChannelAppID,
		ChannelUserID:           binding.ChannelUserID,
		ChannelConversationID:   binding.ChannelConversationID,
		ChannelConversationType: conversationType,
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
	if query.ChannelConversationID != "" {
		binding, err = a.getChannelAgentBinding(query, "")
		if err != nil || binding != nil {
			return binding, err
		}
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
		 SET canonical_uid = $1, last_used_at = CURRENT_TIMESTAMP
		 WHERE id = $2 AND actor_uid = $3 AND agent_uid = $4 AND status = 'active'
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
			 WHERE id = $1 AND actor_uid = $2 AND agent_uid = $3 AND status = 'active'`,
			bindingID, actorUID, agentUID,
		)
		existing, lookupErr := scanChannelAgentBinding(existingRow)
		if lookupErr == nil && existing.CanonicalUID > 0 && existing.CanonicalUID != canonicalUID {
			return nil, store.ErrChannelAgentBindingAlreadyLinked
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

func (a *Adapter) getActiveChannelAgentEntry(ownerUID, agentUID int64, channel, channelAppID string) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = $1 AND agent_uid = $2 AND channel = $3 AND channel_app_id = $4 AND status = 'active'
		 ORDER BY created_at DESC LIMIT 1`,
		ownerUID, agentUID, channel, channelAppID,
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

func (a *Adapter) getChannelAgentBinding(query types.ChannelAgentBindingQuery, conversationID string) (*types.ChannelAgentBinding, error) {
	row := a.db.QueryRow(
		`SELECT id, channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		        COALESCE(actor_uid, 0), COALESCE(canonical_uid, 0), owner_uid, agent_uid, COALESCE(entry_id, 0), status, bound_at, updated_at, last_used_at
		 FROM channel_agent_bindings
		 WHERE channel = $1 AND channel_app_id = $2 AND channel_user_id = $3 AND channel_conversation_id = $4 AND status = 'active'
		 ORDER BY updated_at DESC LIMIT 1`,
		query.Channel, query.ChannelAppID, query.ChannelUserID, conversationID,
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
	return entry, nil
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
