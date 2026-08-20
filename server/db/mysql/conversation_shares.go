package mysql

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.ConversationShareStore = (*Adapter)(nil)
var _ store.MessagesByIDStore = (*Adapter)(nil)

// GetMessagesByIDs resolves an explicit selection inside one topic. Callers
// still verify all requested IDs are present so a partial database result can
// never become a partial, surprising share.
func (a *Adapter) GetMessagesByIDs(topicID string, ids []int64) ([]*types.Message, error) {
	if len(ids) == 0 {
		return []*types.Message{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, topicID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, client_msg_id, metadata
			 FROM messages WHERE topic_id = ? AND id IN (%s)
			 ORDER BY created_at ASC, id ASC`,
			placeholders,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages by ids: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan selected message")
}

func (a *Adapter) CreateConversationShare(share *store.ConversationShare, items []*store.ConversationShareItem, assets []*store.ConversationShareAsset) error {
	if share == nil {
		return fmt.Errorf("conversation share is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("begin conversation share: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO conversation_shares (id, owner_uid, topic_id, token_hash, title, state, expires_at, created_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		share.ID, share.OwnerUID, share.TopicID, share.TokenHash, share.Title, string(share.State), share.ExpiresAt, share.CreatedAt, share.RevokedAt,
	); err != nil {
		return fmt.Errorf("insert conversation share: %w", err)
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO conversation_share_items (id, share_id, position, source_message_id, speaker, snapshot)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			item.ID, item.ShareID, item.Position, item.SourceMessageID, item.Speaker, item.Snapshot,
		); err != nil {
			return fmt.Errorf("insert conversation share item: %w", err)
		}
	}
	for _, asset := range assets {
		if asset == nil {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO conversation_share_assets (id, share_id, item_id, storage_key, name, mime_type, size, kind)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			asset.ID, asset.ShareID, asset.ItemID, asset.StorageKey, asset.Name, asset.MimeType, asset.Size, asset.Kind,
		); err != nil {
			return fmt.Errorf("insert conversation share asset: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation share: %w", err)
	}
	return nil
}

func (a *Adapter) GetConversationShareByTokenHash(tokenHash string) (*store.ConversationShare, error) {
	row := a.db.QueryRow(
		`SELECT id, owner_uid, topic_id, token_hash, title, state, expires_at, created_at, revoked_at
		 FROM conversation_shares WHERE token_hash = ?`,
		tokenHash,
	)
	return scanMySQLConversationShare(row)
}

func (a *Adapter) GetConversationShareByID(shareID string) (*store.ConversationShare, error) {
	row := a.db.QueryRow(
		`SELECT id, owner_uid, topic_id, token_hash, title, state, expires_at, created_at, revoked_at
		 FROM conversation_shares WHERE id = ?`,
		shareID,
	)
	return scanMySQLConversationShare(row)
}

func (a *Adapter) ListConversationShares(ownerUID int64, topicID string) ([]*store.ConversationShare, error) {
	rows, err := a.db.Query(
		`SELECT id, owner_uid, topic_id, token_hash, title, state, expires_at, created_at, revoked_at
			 FROM conversation_shares
			 WHERE owner_uid = ? AND topic_id = ?
			 ORDER BY created_at DESC`,
		ownerUID, topicID,
	)
	if err != nil {
		return nil, fmt.Errorf("list conversation shares: %w", err)
	}
	defer rows.Close()

	shares := make([]*store.ConversationShare, 0)
	for rows.Next() {
		share, err := scanMySQLConversationShare(rows)
		if err != nil {
			return nil, err
		}
		if share != nil {
			shares = append(shares, share)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conversation shares: %w", err)
	}
	return shares, nil
}

func (a *Adapter) GetConversationShareItems(shareID string) ([]*store.ConversationShareItem, error) {
	rows, err := a.db.Query(
		`SELECT id, share_id, position, source_message_id, speaker, snapshot
		 FROM conversation_share_items WHERE share_id = ? ORDER BY position ASC`,
		shareID,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation share items: %w", err)
	}
	defer rows.Close()
	items := make([]*store.ConversationShareItem, 0)
	for rows.Next() {
		item := &store.ConversationShareItem{}
		if err := rows.Scan(&item.ID, &item.ShareID, &item.Position, &item.SourceMessageID, &item.Speaker, &item.Snapshot); err != nil {
			return nil, fmt.Errorf("scan conversation share item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *Adapter) GetConversationShareAsset(shareID, assetID string) (*store.ConversationShareAsset, error) {
	row := a.db.QueryRow(
		`SELECT id, share_id, item_id, storage_key, name, mime_type, size, kind
		 FROM conversation_share_assets WHERE share_id = ? AND id = ?`,
		shareID, assetID,
	)
	asset := &store.ConversationShareAsset{}
	if err := row.Scan(&asset.ID, &asset.ShareID, &asset.ItemID, &asset.StorageKey, &asset.Name, &asset.MimeType, &asset.Size, &asset.Kind); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get conversation share asset: %w", err)
	}
	return asset, nil
}

func (a *Adapter) RevokeConversationShare(ownerUID int64, shareID string) (bool, error) {
	result, err := a.db.Exec(
		`UPDATE conversation_shares SET state = ?, revoked_at = CURRENT_TIMESTAMP(6)
		 WHERE id = ? AND owner_uid = ? AND state = ?`,
		string(store.ConversationShareStateRevoked), shareID, ownerUID, string(store.ConversationShareStateActive),
	)
	if err != nil {
		return false, fmt.Errorf("revoke conversation share: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("conversation share rows affected: %w", err)
	}
	return updated > 0, nil
}

type mysqlConversationShareScanner interface {
	Scan(dest ...interface{}) error
}

func scanMySQLConversationShare(row mysqlConversationShareScanner) (*store.ConversationShare, error) {
	share := &store.ConversationShare{}
	var expiresAt, revokedAt sql.NullTime
	var state string
	if err := row.Scan(&share.ID, &share.OwnerUID, &share.TopicID, &share.TokenHash, &share.Title, &state, &expiresAt, &share.CreatedAt, &revokedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan conversation share: %w", err)
	}
	share.State = store.ConversationShareState(state)
	if expiresAt.Valid {
		value := expiresAt.Time.UTC()
		share.ExpiresAt = &value
	}
	if revokedAt.Valid {
		value := revokedAt.Time.UTC()
		share.RevokedAt = &value
	}
	return share, nil
}
