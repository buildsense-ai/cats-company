package mysql

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

func TestBotSkillMutationSchemaDefaultsToOwnerOnly(t *testing.T) {
	if strings.Contains(createBotConfigTable, "skill_mutation_mode") {
		t.Fatal("fresh schema must add skill_mutation_mode through the migration instead of defining it twice")
	}
	if !strings.Contains(migrateBotConfigAddSkillMutationMode, "ADD COLUMN skill_mutation_mode") ||
		!strings.Contains(migrateBotConfigAddSkillMutationMode, "ENUM('owner_only','shared_live')") ||
		!strings.Contains(migrateBotConfigAddSkillMutationMode, "DEFAULT 'owner_only'") {
		t.Fatal("fresh and legacy schemas must use the same fail-closed skill mutation migration")
	}
	if !isIgnorableMigrationError(errors.New("Error 1060: Duplicate column name 'skill_mutation_mode'")) {
		t.Fatal("an already-upgraded schema must tolerate the repeated migration")
	}
}

func TestBotSkillMutationPolicyStore(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const botUID = int64(42)
	mock.ExpectExec(regexp.QuoteMeta(
		`UPDATE bot_config SET skill_mutation_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
	)).WithArgs("shared_live", botUID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT COALESCE(skill_mutation_mode, 'owner_only') FROM bot_config WHERE user_id = ?`,
	)).WithArgs(botUID).WillReturnRows(sqlmock.NewRows([]string{"skill_mutation_mode"}).AddRow("shared_live"))

	adapter := &Adapter{db: sqlDB}
	if err := adapter.UpdateBotSkillMutationMode(botUID, types.BotSkillMutationSharedLive); err != nil {
		t.Fatalf("update bot skill mutation mode: %v", err)
	}
	mode, err := adapter.GetBotSkillMutationMode(botUID)
	if err != nil {
		t.Fatalf("get bot skill mutation mode: %v", err)
	}
	if mode != types.BotSkillMutationSharedLive {
		t.Fatalf("mode=%q, want shared_live", mode)
	}
	if err := adapter.UpdateBotSkillMutationMode(botUID, "everyone"); err == nil {
		t.Fatal("invalid mode must be rejected before database write")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}
