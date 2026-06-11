package mysql

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

	res, err := a.db.Exec(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, owner_uid, agent_uid, status)
		 VALUES (?, ?, ?, ?, ?, 'active')`,
		entry.SceneKey, entry.Channel, entry.ChannelAppID, entry.OwnerUID, entry.AgentUID,
	)
	if err != nil {
		return nil, fmt.Errorf("create channel agent entry: %w", err)
	}
	id, _ := res.LastInsertId()
	return a.GetChannelAgentEntryByID(id)
}

func (a *Adapter) ListChannelAgentEntries(ownerUID, agentUID int64) ([]*types.ChannelAgentEntry, error) {
	rows, err := a.db.Query(
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
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
		`SELECT channel, channel_app_id, agent_uid FROM channel_agent_entries WHERE id = ? AND owner_uid = ? AND status = 'active'`,
		id, ownerUID,
	).Scan(&channel, &channelAppID, &agentUID); err != nil {
		return nil, fmt.Errorf("get channel agent entry: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE channel_agent_entries SET status = 'revoked', updated_at = CURRENT_TIMESTAMP WHERE id = ? AND owner_uid = ?`,
		id, ownerUID,
	); err != nil {
		return nil, fmt.Errorf("revoke channel agent entry: %w", err)
	}
	res, err := tx.Exec(
		`INSERT INTO channel_agent_entries (scene_key, channel, channel_app_id, owner_uid, agent_uid, status)
		 VALUES (?, ?, ?, ?, ?, 'active')`,
		sceneKey, channel, channelAppID, ownerUID, agentUID,
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
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
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
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
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

func (a *Adapter) UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error) {
	if binding == nil || binding.Channel == "" || binding.ChannelUserID == "" || binding.OwnerUID <= 0 || binding.AgentUID <= 0 {
		return nil, fmt.Errorf("invalid channel agent binding")
	}
	conversationType := binding.ChannelConversationType
	if conversationType == "" {
		conversationType = "p2p"
	}
	_, err := a.db.Exec(
		`INSERT INTO channel_agent_bindings (
		     channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type,
		     actor_uid, canonical_uid, owner_uid, agent_uid, entry_id, status, bound_at, last_used_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		     actor_uid = COALESCE(VALUES(actor_uid), actor_uid),
		     canonical_uid = COALESCE(VALUES(canonical_uid), canonical_uid),
		     owner_uid = VALUES(owner_uid),
		     agent_uid = VALUES(agent_uid),
		     entry_id = COALESCE(VALUES(entry_id), entry_id),
		     channel_conversation_type = VALUES(channel_conversation_type),
		     status = 'active',
		     bound_at = CURRENT_TIMESTAMP,
		     last_used_at = CURRENT_TIMESTAMP,
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
		 SET canonical_uid = ?, last_used_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status = 'active'
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
			 WHERE id = ? AND actor_uid = ? AND agent_uid = ? AND status = 'active'`,
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

func (a *Adapter) getActiveChannelAgentEntry(ownerUID, agentUID int64, channel, channelAppID string) (*types.ChannelAgentEntry, error) {
	row := a.db.QueryRow(
		`SELECT id, scene_key, channel, channel_app_id, owner_uid, agent_uid, status, created_at, updated_at, last_used_at
		 FROM channel_agent_entries
		 WHERE owner_uid = ? AND agent_uid = ? AND channel = ? AND channel_app_id = ? AND status = 'active'
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
		 WHERE channel = ? AND channel_app_id = ? AND channel_user_id = ? AND channel_conversation_id = ? AND status = 'active'
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
	if _, err := a.db.Exec(`UPDATE channel_agent_bindings SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, binding.ID); err != nil {
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

func nullableInt64(value int64) interface{} {
	if value <= 0 {
		return nil
	}
	return value
}
