package mysql

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestCreateBotDefinitionUsesLockedMySQLJSONDocument(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	adapter := &Adapter{db: database}
	raw := []byte(`{"channel":"feishu","cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":4}}`)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT COALESCE(config, JSON_OBJECT()) FROM bot_config WHERE user_id = ? FOR UPDATE`,
	)).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectExec(regexp.QuoteMeta(
		`UPDATE bot_config SET config = ? WHERE user_id = ?`,
	)).WithArgs(sqlmock.AnyArg(), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	snapshot, err := adapter.CreateBotDefinition(42, []types.BotSkillRef{
		{SkillID: "lin/agent-browser", Version: "1.0.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Model.Revision != 4 || snapshot.Skills.Revision != 1 || len(snapshot.Skills.Skills) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateBotDefinitionRejectsStaleMySQLModelRevision(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	adapter := &Adapter{db: database}
	raw := []byte(`{
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":5},
		"bot_definition":{"schema":"xiaoba.bot-definition.v1","skills":[],"revision":2,"updatedAt":"2026-07-24T00:00:00Z"}
	}`)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT COALESCE(config, JSON_OBJECT()) FROM bot_config WHERE user_id = ? FOR UPDATE`,
	)).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectRollback()

	_, err = adapter.UpdateBotDefinition(42, 4, 2, []types.BotSkillRef{})
	if !errors.Is(err, store.ErrStaleBotDefinitionRevision) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
