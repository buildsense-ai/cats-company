package mysql

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) CreateUserWithBotInvite(user *types.User, code string) (int64, int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var botUID, ownerUID int64
	if err := tx.QueryRow(`SELECT bot_uid, owner_uid FROM bot_invite_codes WHERE code = ? AND revoked_at IS NULL FOR UPDATE`, code).Scan(&botUID, &ownerUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, store.ErrBotInviteUnavailable
		}
		return 0, 0, err
	}
	var state int
	var accountType string
	if err := tx.QueryRow(`SELECT state, account_type FROM users WHERE id = ?`, botUID).Scan(&state, &accountType); err != nil || state != 0 || accountType != "bot" {
		return 0, 0, store.ErrBotInviteUnavailable
	}
	if ownerUID <= 0 {
		return 0, 0, store.ErrBotInviteUnavailable
	}
	res, err := tx.Exec(`INSERT INTO users (username, email, phone, display_name, avatar_url, account_type, pass_hash, state) VALUES (?,?,?,?,?,?,?,?)`, user.Username, user.Email, user.Phone, user.DisplayName, user.AvatarURL, user.AccountType, user.PassHash, user.State)
	if err != nil {
		return 0, 0, fmt.Errorf("create user: %w", err)
	}
	userUID, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`UPDATE bot_invite_codes SET used_count = used_count + 1, updated_at = CURRENT_TIMESTAMP WHERE bot_uid = ?`, botUID); err != nil {
		return 0, 0, err
	}
	for _, pair := range [][2]int64{{userUID, botUID}, {botUID, userUID}} {
		if _, err := tx.Exec(`INSERT INTO friends (from_user_id, to_user_id, status, message) VALUES (?,?,'accepted','bot invite')`, pair[0], pair[1]); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit user bot invite: %w", err)
	}
	return userUID, botUID, nil
}

func (a *Adapter) CreateBotInviteCode(botUID, ownerUID int64, code string) error {
	var existing int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM bot_invite_codes WHERE bot_uid = ?`, botUID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		_, err := a.db.Exec(`UPDATE bot_invite_codes
SET owner_uid = ?, code = ?, revoked_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE bot_uid = ?`, ownerUID, code, botUID)
		return err
	}
	_, err := a.db.Exec(`INSERT INTO bot_invite_codes (bot_uid, owner_uid, code, created_at, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, botUID, ownerUID, code)
	return err
}

func (a *Adapter) GetBotInviteCode(botUID, ownerUID int64) (string, error) {
	var code string
	err := a.db.QueryRow(`SELECT code FROM bot_invite_codes WHERE bot_uid = ? AND owner_uid = ? AND revoked_at IS NULL`, botUID, ownerUID).Scan(&code)
	return code, err
}

func (a *Adapter) RevokeBotInviteCode(botUID, ownerUID int64) error {
	_, err := a.db.Exec(`UPDATE bot_invite_codes SET revoked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE bot_uid = ? AND owner_uid = ?`, botUID, ownerUID)
	return err
}

func (a *Adapter) BotInviteCodeExists(code string) (bool, error) {
	var count int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM bot_invite_codes WHERE code = ? AND revoked_at IS NULL`, code).Scan(&count)
	return count > 0, err
}

func (a *Adapter) RedeemBotInviteCode(code string, userUID int64) (int64, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var botUID, ownerUID int64
	err = tx.QueryRow(`SELECT bot_uid, owner_uid FROM bot_invite_codes WHERE code = ? AND revoked_at IS NULL FOR UPDATE`, code).Scan(&botUID, &ownerUID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrBotInviteUnavailable
	}
	if err != nil {
		return 0, err
	}
	var state int
	var accountType string
	if err := tx.QueryRow(`SELECT state, account_type FROM users WHERE id = ?`, botUID).Scan(&state, &accountType); err != nil || state != 0 || accountType != "bot" {
		return 0, store.ErrBotInviteUnavailable
	}
	var userState int
	var userType string
	if err := tx.QueryRow(`SELECT state, account_type FROM users WHERE id = ?`, userUID).Scan(&userState, &userType); err != nil || userState != 0 || userType != "human" || userUID == ownerUID {
		return 0, store.ErrBotInviteUnavailable
	}
	var blocked int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM friends WHERE status = 'blocked' AND ((from_user_id = ? AND to_user_id = ?) OR (from_user_id = ? AND to_user_id = ?))`, userUID, botUID, botUID, userUID).Scan(&blocked); err != nil {
		return 0, err
	}
	if blocked > 0 {
		return 0, store.ErrBotInviteUnavailable
	}
	var accepted int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM friends WHERE from_user_id = ? AND to_user_id = ? AND status = 'accepted'`, userUID, botUID).Scan(&accepted); err != nil {
		return 0, err
	}
	if accepted == 0 {
		if _, err := tx.Exec(`UPDATE bot_invite_codes SET used_count = used_count + 1, updated_at = CURRENT_TIMESTAMP WHERE bot_uid = ?`, botUID); err != nil {
			return 0, err
		}
	}
	for _, pair := range [][2]int64{{userUID, botUID}, {botUID, userUID}} {
		if _, err := tx.Exec(`INSERT INTO friends (from_user_id, to_user_id, status, message) VALUES (?, ?, 'accepted', 'bot invite') ON DUPLICATE KEY UPDATE status = 'accepted'`, pair[0], pair[1]); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit bot invite: %w", err)
	}
	return botUID, nil
}
