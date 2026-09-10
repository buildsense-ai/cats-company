package postgres

import (
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// SearchAdminUsers keeps UID fragments separate from names and paginates full
// account records. Exact matches lead; the UID tie-breaker makes pages stable.
func (a *Adapter) SearchAdminUsers(query, field string, limit, offset int) ([]*types.User, int, error) {
	if field != "uid" && field != "name" {
		return nil, 0, fmt.Errorf("invalid admin search field")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	pattern := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(query) + "%"
	where := "CAST(id AS TEXT) LIKE $1 ESCAPE '!'"
	exact := "CAST(id AS TEXT) = $2"
	args := []interface{}{pattern}

	if field == "name" {
		where = "(username ILIKE $1 ESCAPE '!' OR COALESCE(email, '') ILIKE $1 ESCAPE '!' OR display_name ILIKE $1 ESCAPE '!')"
		exact = "LOWER(username) = LOWER($2) OR LOWER(COALESCE(email, '')) = LOWER($2) OR LOWER(display_name) = LOWER($2)"
	}
	var total int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM users WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count admin search users: %w", err)
	}
	users := make([]*types.User, 0)
	if total == 0 || offset >= total {
		return users, total, nil
	}
	args = append(args, query, limit, offset)
	rows, err := a.db.Query(`SELECT id, username, COALESCE(email, ''), display_name,
 COALESCE(avatar_url, ''), account_type, state, created_at, updated_at
 FROM users WHERE `+where+` ORDER BY CASE WHEN `+exact+` THEN 0 ELSE 1 END, id ASC
 LIMIT $3 OFFSET $4`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search admin users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		u := &types.User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.AvatarURL, &u.AccountType, &u.State, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan admin search user: %w", err)
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}
