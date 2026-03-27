package server

import (
	"github.com/openchat/openchat/server/db/mysql"
)

func validateInvitationCode(db *mysql.Adapter, code string) (bool, error) {
	if code == "" {
		return false, nil
	}

	query := "SELECT id, used FROM invitations WHERE code = ? AND (expires_at IS NULL OR expires_at > NOW())"
	var id int64
	var used bool
	err := db.DB().QueryRow(query, code).Scan(&id, &used)
	if err != nil {
		return false, err
	}

	return !used, nil
}

func markInvitationUsed(db *mysql.Adapter, code string, userId int64) error {
	query := "UPDATE invitations SET used = 1, used_by = ?, used_at = NOW() WHERE code = ?"
	_, err := db.DB().Exec(query, userId, code)
	return err
}
