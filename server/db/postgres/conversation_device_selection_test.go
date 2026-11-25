package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
)

func TestConversationDeviceSelectionCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &Adapter{db: db}
	ctx := context.Background()
	const session = "session:v2:catscompany:p2p:p2p_7_42:agent:usr42"
	get := regexp.QuoteMeta(`SELECT device_id, revision, source, updated_at_ms, COALESCE((to_jsonb(conversation_device_selections)->>'owner_uid')::bigint,0) FROM conversation_device_selections WHERE actor_uid = $1 AND session_key = $2`)
	insert := regexp.QuoteMeta(`INSERT INTO conversation_device_selections (actor_uid, session_key, device_id, revision, source, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (actor_uid, session_key) DO NOTHING`)
	update := regexp.QuoteMeta(`UPDATE conversation_device_selections SET device_id = $1, revision = $2, source = $3, updated_at_ms = $4 WHERE actor_uid = $5 AND session_key = $6 AND revision = $7`)
	mock.ExpectQuery(get).WithArgs(int64(7), session).WillReturnRows(sqlmock.NewRows([]string{"device_id", "revision", "source", "updated_at_ms", "owner_uid"}))
	if record, err := a.GetConversationDeviceSelection(ctx, 7, session); err != nil || record != nil {
		t.Fatal(record, err)
	}
	next := store.ConversationDeviceSelection{DeviceID: "a", Revision: "revision-a", Source: "user_confirmation", UpdatedAt: 1}
	mock.ExpectExec(insert).WithArgs(int64(7), session, "a", "revision-a", "user_confirmation", int64(1)).WillReturnResult(sqlmock.NewResult(1, 1))
	if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "", next); err != nil || !ok {
		t.Fatal(ok, err)
	}
	mock.ExpectExec(insert).WithArgs(int64(7), session, "a", "revision-a", "user_confirmation", int64(1)).WillReturnResult(sqlmock.NewResult(0, 0))
	if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "", next); err != nil || ok {
		t.Fatal("duplicate insert won", ok, err)
	}
	mock.ExpectQuery(get).WithArgs(int64(7), session).WillReturnRows(sqlmock.NewRows([]string{"device_id", "revision", "source", "updated_at_ms", "owner_uid"}).AddRow("a", "revision-a", "user_confirmation", 1, 0))
	if record, err := a.GetConversationDeviceSelection(ctx, 7, session); err != nil || record == nil || record.DeviceID != "a" {
		t.Fatal(record, err)
	}
	next.DeviceID = "b"
	next.Revision = "revision-b"
	for _, rows := range []int64{1, 0} {
		mock.ExpectExec(update).WithArgs("b", "revision-b", "user_confirmation", int64(1), int64(7), session, "revision-a").WillReturnResult(sqlmock.NewResult(0, rows))
		if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "revision-a", next); err != nil || ok != (rows == 1) {
			t.Fatal("CAS", ok, err)
		}
	}
	mock.ExpectQuery(get).WithArgs(int64(7), session).WillReturnError(errors.New("db down"))
	if _, err := a.GetConversationDeviceSelection(ctx, 7, session); err == nil {
		t.Fatal("read error hidden")
	}
	mock.ExpectExec(update).WithArgs("b", "revision-b", "user_confirmation", int64(1), int64(7), session, "revision-a").WillReturnError(errors.New("db down"))
	if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "revision-a", next); err == nil || ok {
		t.Fatal("write error hidden")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
