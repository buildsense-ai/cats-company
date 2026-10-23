package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store/types"
)

func TestConversationTaskStatusPostgresPublisherRestartAndFanout(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for publisher/database/fanout integration")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("cats_task_fanout_%d", time.Now().UnixNano())
	if _, err := base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	db := &postgres.Adapter{}
	if err := db.Open(parsed.String()); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	uids := make([]int64, 3)
	for i := range uids {
		kind := types.AccountBot
		if i == 0 {
			kind = types.AccountHuman
		}
		uid, err := db.CreateUser(&types.User{Username: fmt.Sprintf("task-fanout-%d", i), AccountType: kind, PassHash: []byte("test")})
		if err != nil {
			t.Fatal(err)
		}
		uids[i] = uid
	}
	group, err := db.CreateGroup("task fanout", uids[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range uids[1:] {
		if err := db.AddGroupMember(group, uid, "member"); err != nil {
			t.Fatal(err)
		}
	}
	topic := fmt.Sprintf("grp_%d", group)
	beforeCrash := newTaskRecoveryTestHub(db)
	beforeCrash.taskStatusDelivery = make(chan struct{}, 1) // Pause after commit, before delivery.
	publish := func(h *Hub, uid int64, run, state string) {
		t.Helper()
		client := &Client{hub: h, uid: uid, accountType: types.AccountBot, send: make(chan []byte, 32)}
		body, _ := json.Marshal(map[string]string{"run_id": run, "state": state})
		h.handlePub(client, &MsgClientPub{ID: run + state, Topic: topic, Type: "task_status", Content: body})
		var ack ServerMessage
		decodeQueuedServerMessage(t, client.send, &ack)
		if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
			t.Fatalf("publisher ack=%+v", ack)
		}
	}
	publish(beforeCrash, uids[1], "a", "running")
	publish(beforeCrash, uids[2], "b", "running")
	publish(beforeCrash, uids[2], "b", "completed")

	// A fresh Hub has no publisher memory. It must recover the committed B
	// terminal without replacing the legacy aggregate (A is still running).
	afterRestart := newTaskRecoveryTestHub(db)
	user := taskDeliveryClient(afterRestart, uids[0])
	afterRestart.dispatchConversationTaskStatusEvents()
	foundTerminal := false
	for i := 0; i < 3; i++ {
		var msg ServerMessage
		decodeQueuedServerMessage(t, user.send, &msg)
		if msg.TaskStatus == nil || msg.TaskStatus.RunID != "a" || msg.TaskStatus.State != "running" {
			t.Fatalf("stale aggregate replayed: %+v", msg)
		}
		if msg.TaskStatusSource != nil && msg.TaskStatusSource.RunID == "b" && msg.TaskStatusSource.State == "completed" {
			foundTerminal = true
		}
	}
	if !foundTerminal {
		t.Fatal("B terminal was hidden by A aggregate")
	}
	publish(afterRestart, uids[1], "a", "completed")
	var final ServerMessage
	decodeQueuedServerMessage(t, user.send, &final)
	if final.TaskStatus == nil || final.TaskStatus.State != "completed" {
		t.Fatalf("final aggregate=%+v", final)
	}
	afterRestart.handleGet(user, &MsgClientGet{ID: "restore", Topic: topic, What: "task_statuses"})
	var history ServerMessage
	decodeQueuedServerMessage(t, user.send, &history)
	if history.Ctrl == nil || history.Ctrl.Code != 200 {
		t.Fatalf("restore=%+v", history)
	}
	params, ok := history.Ctrl.Params.(map[string]interface{})
	if !ok {
		t.Fatalf("history params=%+v", history.Ctrl.Params)
	}
	rows, ok := params["task_statuses"].([]interface{})
	if !ok || len(rows) != 2 {
		t.Fatalf("lost run history: %+v", history)
	}
	messages, err := db.GetMessages(topic, 100, 0)
	if err != nil || len(messages) != 0 {
		t.Fatalf("task status polluted visible history: %v %v", messages, err)
	}
}
