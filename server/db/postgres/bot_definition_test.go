package postgres

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

func TestUpdateBotDefinitionUsesLockedPostgresJSONDocument(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	adapter := &Adapter{db: database}
	raw := []byte(`{
		"channel":"feishu",
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":4},
		"bot_definition":{
			"schema":"xiaoba.bot-definition.v1",
			"skills":[{"skillId":"lin/agent-browser","version":"1.0.3"}],
			"revision":2,
			"updatedAt":"2026-07-24T00:00:00Z"
		}
	}`)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE`,
	)).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectExec(regexp.QuoteMeta(
		`UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2`,
	)).WithArgs(sqlmock.AnyArg(), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	snapshot, err := adapter.UpdateBotDefinition(42, 4, 2, []types.BotSkillRef{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Model.Revision != 4 || snapshot.Skills.Revision != 3 ||
		snapshot.Skills.Skills == nil || len(snapshot.Skills.Skills) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetBotDefinitionReturnsLegacySnapshotForMissingPostgresNode(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	adapter := &Adapter{db: database}

	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1`,
	)).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(
		[]byte(`{"cloud_model":{"revision":4}}`),
	))

	snapshot, err := adapter.GetBotDefinition(42)
	if err != nil || snapshot == nil || snapshot.Model == nil || snapshot.Skills != nil {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
