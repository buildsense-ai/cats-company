package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

type jsonArgument map[string]interface{}

func (want jsonArgument) Match(value driver.Value) bool {
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return false
	}
	var got map[string]interface{}
	return json.Unmarshal(raw, &got) == nil && reflect.DeepEqual(map[string]interface{}(want), got)
}

func TestSaveMessageWithMetadataIsAtomicAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	adapter := &Adapter{db: db}
	metadata := map[string]interface{}{"source_channel": "feishu", "attempt": float64(2)}

	mock.ExpectQuery(`SELECT id FROM messages`).
		WithArgs("p2p_1_2", int64(1), "client-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`(?s)INSERT INTO messages .*metadata.*RETURNING id`).
		WithArgs("p2p_1_2", int64(1), "hello", nil, "normal", "user", "text", int64(9), "client-1", jsonArgument(metadata)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)))

	id, duplicate, err := adapter.SaveMessageWithMetadata("p2p_1_2", 1, "hello", nil, "normal", "user", "text", 9, "client-1", metadata)
	if err != nil || id != 41 || duplicate {
		t.Fatalf("first save: id=%d duplicate=%v err=%v", id, duplicate, err)
	}

	mock.ExpectQuery(`SELECT id FROM messages`).
		WithArgs("p2p_1_2", int64(1), "client-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)))
	id, duplicate, err = adapter.SaveMessageWithMetadata("p2p_1_2", 1, "changed", nil, "normal", "user", "text", 0, "client-1", map[string]interface{}{"changed": true})
	if err != nil || id != 41 || !duplicate {
		t.Fatalf("duplicate save: id=%d duplicate=%v err=%v", id, duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMessageHistoryReadsMetadataAndLegacyNull(t *testing.T) {
	tests := []struct {
		name string
		args []driver.Value
		call func(*Adapter) ([]*types.Message, error)
	}{
		{"since", []driver.Value{"topic", int64(3), 10}, func(a *Adapter) ([]*types.Message, error) { return a.GetMessagesSince("topic", 3, 10) }},
		{"messages", []driver.Value{"topic", 10, 2}, func(a *Adapter) ([]*types.Message, error) { return a.GetMessages("topic", 10, 2) }},
		{"latest", []driver.Value{"topic", 10, 2}, func(a *Adapter) ([]*types.Message, error) { return a.GetLatestMessages("topic", 10, 2) }},
		{"before", []driver.Value{"topic", int64(20), 10}, func(a *Adapter) ([]*types.Message, error) { return a.GetLatestMessagesBefore("topic", 20, 10) }},
		{"agent files", []driver.Value{int64(7), "topic", 10}, func(a *Adapter) ([]*types.Message, error) { return a.ListAgentFileMessages(7, "topic", 0, 10) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			createdAt := time.Date(2026, time.August, 7, 0, 0, 0, 0, time.UTC)
			rows := sqlmock.NewRows([]string{"id", "topic_id", "from_uid", "content", "msg_type", "created_at", "content_blocks", "mode", "role", "metadata"}).
				AddRow(int64(1), "topic", int64(7), "hello", "text", createdAt, nil, "normal", "user", []byte(`{"source_channel":"feishu"}`)).
				AddRow(int64(2), "topic", int64(7), "legacy", "text", createdAt, nil, nil, nil, nil)
			mock.ExpectQuery(`(?s)SELECT .*metadata.*FROM`).WithArgs(test.args...).WillReturnRows(rows)
			adapter := &Adapter{db: db}
			messages, err := test.call(adapter)
			if err != nil {
				t.Fatalf("history read: %v", err)
			}
			if len(messages) != 2 || messages[0].Metadata["source_channel"] != "feishu" || messages[1].Metadata != nil {
				t.Fatalf("history metadata = %#v", messages)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestListTopicFileMessagesReadsMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	createdAt := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{"id", "topic_id", "from_uid", "content", "msg_type", "created_at", "content_blocks", "mode", "role", "metadata"}).
		AddRow(
			int64(14), "grp_1686", int64(7), "", "file", createdAt,
			[]byte(`[{"type":"file","payload":{"name":"example.pdf"}}]`), "code", "user", []byte(`{"source_channel":"web"}`),
		)
	mock.ExpectQuery(`(?s)SELECT .*metadata.*FROM messages.*WHERE topic_id =`).
		WithArgs("grp_1686", 41).
		WillReturnRows(rows)

	messages, err := (&Adapter{db: db}).ListTopicFileMessages("grp_1686", 0, 41)
	if err != nil {
		t.Fatalf("list topic file messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Metadata["source_channel"] != "web" {
		t.Fatalf("messages = %#v", messages)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMessagesMetadataSchemaMigrationIsIdempotent(t *testing.T) {
	if !strings.Contains(createMessagesTable, "metadata JSONB DEFAULT NULL") {
		t.Fatal("new messages table must include nullable metadata")
	}
	if !strings.Contains(migrateMessagesAddMetadata, "ADD COLUMN IF NOT EXISTS metadata JSONB") {
		t.Fatal("legacy metadata migration must be idempotent")
	}
}
