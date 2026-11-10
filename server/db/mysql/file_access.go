package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.FileAccessStore = (*Adapter)(nil)
var _ store.FileMessageWriter = (*Adapter)(nil)

const fileAccessTimeout = 15 * time.Second
const fileAccessMessageColumns = `id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to`

func validFileAccessString(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validFileAccessHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func fileAccessKeys(keys []string) ([]string, error) {
	if len(keys) > 256 {
		return nil, fmt.Errorf("too many file resource keys (maximum 256)")
	}
	seen := make(map[string]bool, len(keys))
	unique := make([]string, 0, len(keys))
	for _, key := range keys {
		if !validFileAccessString(key, 16384) {
			return nil, fmt.Errorf("invalid file resource key")
		}
		if !seen[key] {
			unique = append(unique, key)
			seen[key] = true
		}
	}
	return unique, nil
}

func (a *Adapter) RecordFileUpload(ctx context.Context, objectKey string, uploaderUID int64) error {
	if !validFileAccessString(objectKey, 1024) || uploaderUID <= 0 {
		return fmt.Errorf("invalid file upload")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	// The duplicate branch preserves ownership, bound state and the original timestamp.
	_, err := a.db.ExecContext(ctx, `INSERT INTO file_uploads (object_key, uploader_uid) VALUES (?, ?) ON DUPLICATE KEY UPDATE object_key = object_key`, objectKey, uploaderUID)
	if err != nil {
		return fmt.Errorf("record file upload: %w", err)
	}
	var owner int64
	if err := a.db.QueryRowContext(ctx, `SELECT uploader_uid FROM file_uploads WHERE object_key = ?`, objectKey).Scan(&owner); err != nil {
		return fmt.Errorf("read file upload owner: %w", err)
	}
	if owner != uploaderUID {
		return fmt.Errorf("file upload already belongs to another uploader")
	}
	return nil
}

func (a *Adapter) GetFileUpload(ctx context.Context, objectKey string) (*store.FileUploadRecord, error) {
	if !validFileAccessString(objectKey, 1024) {
		return nil, fmt.Errorf("invalid file object key")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	record := &store.FileUploadRecord{}
	err := a.db.QueryRowContext(ctx, `SELECT object_key, uploader_uid, bound, created_at FROM file_uploads WHERE object_key = ?`, objectKey).Scan(&record.ObjectKey, &record.UploaderUID, &record.Bound, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get file upload: %w", err)
	}
	return record, nil
}

func (a *Adapter) BindMessageFiles(ctx context.Context, messageID int64, topicID string, resourceKeys []string) error {
	if messageID <= 0 || !validFileAccessString(topicID, 64) {
		return fmt.Errorf("invalid file message binding")
	}
	keys, err := fileAccessKeys(resourceKeys)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin file bindings: %w", err)
	}
	defer tx.Rollback()
	var actualTopic string
	if err := tx.QueryRowContext(ctx, `SELECT topic_id FROM messages WHERE id = ? FOR UPDATE`, messageID).Scan(&actualTopic); err != nil {
		return fmt.Errorf("verify file binding message: %w", err)
	}
	if actualTopic != topicID {
		return fmt.Errorf("file binding topic does not match message")
	}
	if err := insertFileMessageBindings(ctx, tx, messageID, actualTopic, keys); err != nil {
		return err
	}
	return tx.Commit()
}

func insertFileMessageBindings(ctx context.Context, tx *sql.Tx, messageID int64, topicID string, keys []string) error {
	for _, key := range keys {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
		_, err := tx.ExecContext(ctx, `INSERT INTO file_message_bindings (resource_hash, resource_key, message_id, topic_id) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE resource_hash = resource_hash`, hash, key, messageID, topicID)
		if err != nil {
			return fmt.Errorf("bind message file: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE file_uploads SET bound = true WHERE object_key = ?`, key); err != nil {
			return fmt.Errorf("mark file upload bound: %w", err)
		}
	}
	return nil
}

func (a *Adapter) ListFileMessageBindings(ctx context.Context, resourceKey string) ([]store.FileMessageBinding, error) {
	if !validFileAccessString(resourceKey, 16384) {
		return nil, fmt.Errorf("invalid file resource key")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(resourceKey)))
	// No LIMIT: every referencing message must remain available to authorization.
	rows, err := a.db.QueryContext(ctx, `SELECT resource_key, message_id, topic_id FROM file_message_bindings WHERE resource_hash = ? AND BINARY resource_key = BINARY ? ORDER BY message_id`, hash, resourceKey)
	if err != nil {
		return nil, fmt.Errorf("list file message bindings: %w", err)
	}
	defer rows.Close()
	bindings := make([]store.FileMessageBinding, 0)
	for rows.Next() {
		var binding store.FileMessageBinding
		if err := rows.Scan(&binding.ResourceKey, &binding.MessageID, &binding.TopicID); err != nil {
			return nil, fmt.Errorf("scan file binding: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bindings, nil
}

func (a *Adapter) GetFileAccessMessage(ctx context.Context, messageID int64) (*types.Message, error) {
	if messageID <= 0 {
		return nil, fmt.Errorf("invalid file access message ID")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT `+fileAccessMessageColumns+` FROM messages WHERE id = ?`, messageID)
	if err != nil {
		return nil, fmt.Errorf("get file access message: %w", err)
	}
	defer rows.Close()
	messages, err := scanMessages(rows, "scan file access message")
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}
	return messages[0], nil
}

func (a *Adapter) ListFileBackfillMessages(ctx context.Context, afterID int64, limit int) ([]*types.Message, error) {
	if afterID < 0 {
		return nil, fmt.Errorf("invalid file backfill cursor")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT `+fileAccessMessageColumns+` FROM messages WHERE id > ? ORDER BY id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list file backfill messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan file backfill message")
}

func (a *Adapter) CreateFileMediaSession(ctx context.Context, session store.FileMediaSession) error {
	if !validFileAccessHash(session.TokenHash) || session.ActorUID <= 0 || (session.ParentHash != "" && !validFileAccessHash(session.ParentHash)) || !session.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("invalid file media session")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	_, err := a.db.ExecContext(ctx, `INSERT INTO file_media_sessions (token_hash, actor_uid, parent_hash, expires_at) VALUES (?, ?, ?, ?)`, session.TokenHash, session.ActorUID, session.ParentHash, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create file media session: %w", err)
	}
	return nil
}

func (a *Adapter) GetFileMediaSession(ctx context.Context, tokenHash string) (*store.FileMediaSession, error) {
	if !validFileAccessHash(tokenHash) {
		return nil, fmt.Errorf("invalid file media token hash")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	session := &store.FileMediaSession{}
	err := a.db.QueryRowContext(ctx, `SELECT token_hash, actor_uid, parent_hash, expires_at FROM file_media_sessions WHERE token_hash = ? AND expires_at > CURRENT_TIMESTAMP`, tokenHash).Scan(&session.TokenHash, &session.ActorUID, &session.ParentHash, &session.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get file media session: %w", err)
	}
	if !session.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	return session, nil
}

func (a *Adapter) RevokeFileMediaSessions(ctx context.Context, parentHash string) error {
	if !validFileAccessHash(parentHash) {
		return fmt.Errorf("invalid file media parent hash")
	}
	ctx, cancel := context.WithTimeout(ctx, fileAccessTimeout)
	defer cancel()
	_, err := a.db.ExecContext(ctx, `DELETE FROM file_media_sessions WHERE parent_hash = ?`, parentHash)
	if err != nil {
		return fmt.Errorf("revoke file media sessions: %w", err)
	}
	return nil
}

// SaveMessageWithFileBindings mirrors normalized message defaults without changing
// the legacy writer. Only a successfully inserted message may acquire new keys.
func (a *Adapter) SaveMessageWithFileBindings(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}, resourceKeys []string) (int64, bool, error) {
	if !validFileAccessString(topicID, 64) || fromUID <= 0 || len(clientMsgID) > 128 || !utf8.ValidString(clientMsgID) || strings.ContainsRune(clientMsgID, 0) || replyTo < 0 || len(content) > 65535 || !utf8.ValidString(content) || len(mode) > 20 || len(role) > 20 {
		return 0, false, fmt.Errorf("invalid file message parameters")
	}
	switch msgType {
	case "text", "image", "voice", "file":
	default:
		return 0, false, fmt.Errorf("invalid file message type")
	}
	keys, err := fileAccessKeys(resourceKeys)
	if err != nil {
		return 0, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileAccessTimeout)
	defer cancel()
	lookup := func() (int64, error) {
		var id int64
		err := a.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE topic_id = ? AND from_uid = ? AND client_msg_id = ?`, topicID, fromUID, clientMsgID).Scan(&id)
		return id, err
	}
	if clientMsgID != "" {
		id, err := lookup()
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("lookup file message: %w", err)
		}
	}
	var blocksJSON, metadataJSON []byte
	if len(blocks) > 0 {
		if mode == "" {
			mode = "code"
		}
		blocksJSON, err = json.Marshal(blocks)
		if err != nil {
			return 0, false, fmt.Errorf("marshal content blocks: %w", err)
		}
	} else if mode == "" {
		mode = "normal"
	}
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return 0, false, fmt.Errorf("marshal message metadata: %w", err)
		}
	}
	if len(blocksJSON) > 4<<20 || len(metadataJSON) > 4<<20 {
		return 0, false, fmt.Errorf("file message JSON exceeds 4 MiB")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("begin file message: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages (topic_id, from_uid, content, content_blocks, mode, role, msg_type, reply_to, client_msg_id, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), NULLIF(?, ''), ?)`, topicID, fromUID, content, blocksJSON, mode, role, msgType, replyTo, clientMsgID, metadataJSON)
	if err != nil {
		// Roll back before looking up a concurrent winner (also releases locks).
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return 0, false, fmt.Errorf("rollback file message after %v: %w", err, rollbackErr)
		}
		var duplicate *mysqlDriver.MySQLError
		if clientMsgID != "" && errors.As(err, &duplicate) && duplicate.Number == 1062 {
			if id, lookupErr := lookup(); lookupErr == nil {
				return id, true, nil
			}
		}
		return 0, false, fmt.Errorf("insert file message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("get file message ID: %w", err)
	}
	if err := insertFileMessageBindings(ctx, tx, id, topicID, keys); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit file message: %w", err)
	}
	return id, false, nil
}
