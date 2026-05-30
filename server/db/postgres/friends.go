package postgres

import (
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// CreateFriendRequest creates a new friend request.
func (a *Adapter) CreateFriendRequest(fromUID, toUID int64, message string) (int64, error) {
	var id int64
	err := a.db.QueryRow(
		`INSERT INTO friends (from_user_id, to_user_id, status, message)
		 VALUES ($1, $2, 'pending', $3)
		 ON CONFLICT (from_user_id, to_user_id)
		 DO UPDATE SET status = 'pending', message = EXCLUDED.message
		 RETURNING id`,
		fromUID, toUID, message,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create friend request: %w", err)
	}
	return id, nil
}

// AcceptFriendRequest accepts a pending friend request and creates the reverse relationship.
func (a *Adapter) AcceptFriendRequest(fromUID, toUID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`UPDATE friends SET status = 'accepted'
		 WHERE from_user_id = $1 AND to_user_id = $2 AND status = 'pending'`,
		fromUID, toUID,
	); err != nil {
		return fmt.Errorf("accept request: %w", err)
	}

	if _, err := tx.Exec(
		`INSERT INTO friends (from_user_id, to_user_id, status)
		 VALUES ($1, $2, 'accepted')
		 ON CONFLICT (from_user_id, to_user_id)
		 DO UPDATE SET status = 'accepted'`,
		toUID, fromUID,
	); err != nil {
		return fmt.Errorf("create reverse friendship: %w", err)
	}

	return tx.Commit()
}

// RejectFriendRequest rejects a pending friend request.
func (a *Adapter) RejectFriendRequest(fromUID, toUID int64) error {
	_, err := a.db.Exec(
		`UPDATE friends SET status = 'rejected'
		 WHERE from_user_id = $1 AND to_user_id = $2 AND status = 'pending'`,
		fromUID, toUID,
	)
	return err
}

// BlockUser blocks a user.
func (a *Adapter) BlockUser(uid, blockedUID int64) error {
	_, err := a.db.Exec(
		`INSERT INTO friends (from_user_id, to_user_id, status)
		 VALUES ($1, $2, 'blocked')
		 ON CONFLICT (from_user_id, to_user_id)
		 DO UPDATE SET status = 'blocked'`,
		uid, blockedUID,
	)
	return err
}

// RemoveFriend removes a friend relationship both ways.
func (a *Adapter) RemoveFriend(uid1, uid2 int64) error {
	_, err := a.db.Exec(
		`DELETE FROM friends WHERE
		 (from_user_id = $1 AND to_user_id = $2) OR (from_user_id = $3 AND to_user_id = $4)`,
		uid1, uid2, uid2, uid1,
	)
	return err
}

// GetFriends returns all accepted friends for a user.
func (a *Adapter) GetFriends(uid int64) ([]*types.User, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''),
		        u.account_type, COALESCE(u.bot_disclose, false)
		 FROM friends f JOIN users u ON f.to_user_id = u.id
		 WHERE f.from_user_id = $1 AND f.status = 'accepted'
		 ORDER BY u.display_name`,
		uid,
	)
	if err != nil {
		return nil, fmt.Errorf("get friends: %w", err)
	}
	defer rows.Close()

	var friends []*types.User
	for rows.Next() {
		u := &types.User{}
		var acctType string
		var botDisclose bool
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.AvatarURL, &acctType, &botDisclose); err != nil {
			return nil, fmt.Errorf("scan friend: %w", err)
		}
		u.AccountType = types.AccountType(acctType)
		if botDisclose && acctType == "bot" {
			u.BotDisclose = true
		}
		friends = append(friends, u)
	}
	return friends, rows.Err()
}

// GetPendingRequests returns pending friend requests sent to a user.
func (a *Adapter) GetPendingRequests(uid int64) ([]*types.FriendRequest, error) {
	rows, err := a.db.Query(
		`SELECT f.id, f.from_user_id, f.to_user_id, f.status, f.message, f.created_at,
		        u.username, COALESCE(u.display_name, '')
		 FROM friends f JOIN users u ON f.from_user_id = u.id
		 WHERE f.to_user_id = $1 AND f.status = 'pending'
		 ORDER BY f.created_at DESC`,
		uid,
	)
	if err != nil {
		return nil, fmt.Errorf("get pending requests: %w", err)
	}
	defer rows.Close()

	var requests []*types.FriendRequest
	for rows.Next() {
		r := &types.FriendRequest{}
		if err := rows.Scan(&r.ID, &r.FromUserID, &r.ToUserID, &r.Status, &r.Message, &r.CreatedAt,
			&r.FromUsername, &r.DisplayName); err != nil {
			return nil, fmt.Errorf("scan request: %w", err)
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

// AreFriends checks if two users are friends.
func (a *Adapter) AreFriends(uid1, uid2 int64) (bool, error) {
	var count int
	err := a.db.QueryRow(
		`SELECT COUNT(*) FROM friends
		 WHERE from_user_id = $1 AND to_user_id = $2 AND status = 'accepted'`,
		uid1, uid2,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// IsBlocked checks if uid has blocked blockedUID.
func (a *Adapter) IsBlocked(uid, blockedUID int64) (bool, error) {
	var count int
	err := a.db.QueryRow(
		`SELECT COUNT(*) FROM friends
		 WHERE from_user_id = $1 AND to_user_id = $2 AND status = 'blocked'`,
		uid, blockedUID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
