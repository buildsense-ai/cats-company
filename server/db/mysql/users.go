// Package mysql - Cats Company user database operations.
package mysql

import (
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// CreateUser inserts a new user into the database.
func (a *Adapter) CreateUser(u *types.User) (int64, error) {
	res, err := a.db.Exec(
		`INSERT INTO users (username, email, phone, display_name, avatar_url, account_type, pass_hash, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.Email, u.Phone, u.DisplayName, u.AvatarURL, u.AccountType, u.PassHash, u.State,
	)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	return res.LastInsertId()
}

// GetUser retrieves a user by ID.
func (a *Adapter) GetUser(id int64) (*types.User, error) {
	u := &types.User{}
	err := a.db.QueryRow(
		`SELECT id, username, COALESCE(email,''), COALESCE(phone,''), display_name, COALESCE(avatar_url,''), account_type, state, created_at, updated_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.State, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// GetUserByUsername retrieves a user by username.
func (a *Adapter) GetUserByUsername(username string) (*types.User, error) {
	u := &types.User{}
	err := a.db.QueryRow(
		`SELECT id, username, COALESCE(email,''), COALESCE(phone,''), display_name, COALESCE(avatar_url,''), account_type, pass_hash, state, created_at, updated_at
		 FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.PassHash, &u.State, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return u, nil
}

// SearchUsers searches for users by username or display name (for adding friends).
func (a *Adapter) SearchUsers(query string, limit int) ([]*types.User, error) {
	pattern := "%" + query + "%"
	rows, err := a.db.Query(
		`SELECT id, username, display_name, COALESCE(avatar_url, ''),
		        account_type, COALESCE(bot_disclose, 0)
		 FROM users WHERE (username LIKE ? OR display_name LIKE ?) AND state = 0
		 LIMIT ?`,
		pattern, pattern, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()

	var users []*types.User
	for rows.Next() {
		u := &types.User{}
		var acctType string
		var botDisclose bool
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.AvatarURL, &acctType, &botDisclose); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		// Only disclose bot status if bot_disclose is true
		if botDisclose && acctType == "bot" {
			u.BotDisclose = true
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateUser updates user profile fields.
func (a *Adapter) UpdateUser(id int64, displayName, avatarURL string) error {
	_, err := a.db.Exec(
		`UPDATE users SET display_name = ?, avatar_url = ? WHERE id = ?`,
		displayName, avatarURL, id,
	)
	return err
}
