package postgres

import (
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// CreateUser inserts a new user into the database.
func (a *Adapter) CreateUser(u *types.User) (int64, error) {
	var id int64
	err := a.db.QueryRow(
		`INSERT INTO users (username, email, phone, display_name, avatar_url, account_type, pass_hash, state)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id`,
		u.Username, u.Email, u.Phone, u.DisplayName, u.AvatarURL, u.AccountType, u.PassHash, u.State,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

// GetUser retrieves a user by ID.
func (a *Adapter) GetUser(id int64) (*types.User, error) {
	u := &types.User{}
	err := a.db.QueryRow(
		`SELECT id, username, COALESCE(email,''), COALESCE(phone,''), display_name, COALESCE(avatar_url,''), account_type, state, created_at, updated_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.State, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// ListAdminUsers returns users for local account administration.
func (a *Adapter) ListAdminUsers(query string, limit, offset int) ([]*types.User, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	pattern := "%" + query + "%"
	rows, err := a.db.Query(
		`SELECT id, username, COALESCE(email,''), COALESCE(phone,''), display_name, COALESCE(avatar_url,''), account_type, state, created_at, updated_at
		 FROM users
		 WHERE ($1 = '' OR CAST(id AS TEXT) = $1 OR username ILIKE $2 OR COALESCE(email, '') ILIKE $2 OR display_name ILIKE $2)
		 ORDER BY id DESC
		 LIMIT $3 OFFSET $4`,
		query, pattern, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list admin users: %w", err)
	}
	defer rows.Close()

	var users []*types.User
	for rows.Next() {
		u := &types.User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.State, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan admin user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountAdminUsers returns the number of users matching the local admin query.
func (a *Adapter) CountAdminUsers(query string) (int, error) {
	pattern := "%" + query + "%"
	var count int
	err := a.db.QueryRow(
		`SELECT COUNT(*)
		 FROM users
		 WHERE ($1 = '' OR CAST(id AS TEXT) = $1 OR username ILIKE $2 OR COALESCE(email, '') ILIKE $2 OR display_name ILIKE $2)`,
		query, pattern,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count admin users: %w", err)
	}
	return count, nil
}

// UpdateUserState enables or disables a user account.
func (a *Adapter) UpdateUserState(uid int64, state int) error {
	_, err := a.db.Exec(`UPDATE users SET state = $1 WHERE id = $2`, state, uid)
	return err
}

// GetUserByUsername retrieves a user by username.
func (a *Adapter) GetUserByUsername(username string) (*types.User, error) {
	u := &types.User{}
	err := a.db.QueryRow(
		`SELECT id, username, COALESCE(email,''), COALESCE(phone,''), display_name, COALESCE(avatar_url,''), account_type, pass_hash, state, created_at, updated_at
		 FROM users WHERE lower(username) = lower($1)`, username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.PassHash, &u.State, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return u, nil
}

// GetUserByEmail retrieves a user by email.
func (a *Adapter) GetUserByEmail(email string) (*types.User, error) {
	u := &types.User{}
	err := a.db.QueryRow(
		`SELECT id, username, COALESCE(email,''), COALESCE(phone,''), display_name, COALESCE(avatar_url,''), account_type, pass_hash, state, created_at, updated_at
		 FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Phone, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.PassHash, &u.State, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// UpdateUserDisplayName updates a user's display name.
func (a *Adapter) UpdateUserDisplayName(uid int64, displayName string) error {
	_, err := a.db.Exec(`UPDATE users SET display_name = $1 WHERE id = $2`, displayName, uid)
	return err
}

// UpdateUserPasswordHash updates a user's password hash.
func (a *Adapter) UpdateUserPasswordHash(uid int64, passHash []byte) error {
	_, err := a.db.Exec(`UPDATE users SET pass_hash = $1 WHERE id = $2`, passHash, uid)
	return err
}

// SearchUsers searches for users by username or display name.
func (a *Adapter) SearchUsers(query string, limit int) ([]*types.User, error) {
	pattern := "%" + query + "%"
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        u.account_type, COALESCE(u.bot_disclose, false)
		 FROM users u
		 LEFT JOIN bot_config b ON u.id = b.user_id AND u.account_type = 'bot'
		 WHERE (u.username ILIKE $1 OR u.display_name ILIKE $2) AND u.state = 0
		   AND (u.account_type != 'bot' OR COALESCE(b.visibility, 'public') = 'public')
		 LIMIT $3`,
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
		`UPDATE users SET display_name = $1, avatar_url = $2 WHERE id = $3`,
		displayName, avatarURL, id,
	)
	return err
}

// UpdateUserAvatar updates only the avatar URL for a user.
func (a *Adapter) UpdateUserAvatar(id int64, avatarURL string) error {
	_, err := a.db.Exec(
		`UPDATE users SET avatar_url = $1 WHERE id = $2`,
		avatarURL, id,
	)
	return err
}
