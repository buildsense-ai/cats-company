package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresWorkerActionHistorySurvivesRollback(t *testing.T) {
	db := commercialPolicyTestDB(t)
	owner := createGLM53MigrationUser(t, db, "action-history-owner")
	other := createGLM53MigrationUser(t, db, "action-history-other")
	expires := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	preview, err := json.Marshal(map[string]any{"runtime_plan": "light", "deployment_profile": "packed_shared", "worker_expires_at": expires.Format(time.RFC3339), "preview_token": "private-preview"})
	if err != nil {
		t.Fatal(err)
	}
	seedAction := func(uid int64, op, status, workerStatus string) {
		t.Helper()
		_, err := db.db.Exec(`INSERT INTO commercial_ops_actions(uid, operation_id, fingerprint, request, preview, intent, status, worker_status, error, lease)
			VALUES($1,$2,'private-fingerprint',$3::jsonb,$4::jsonb,'test_worker',$5,$6,$7,'private-lease')`, uid, op,
			`{"note":"recover-me","deployment_profile":"packed_shared","runtime_plan":"light","preview_token":"private-request"}`, string(preview), status, workerStatus, strings.Repeat("failure ", 100))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		seedAction(owner, fmt.Sprintf("rollback-%d", i), "failed", "failed")
	}
	seedAction(other, "rollback-other", "failed", "failed")
	seedAction(owner, "active-operation", "running", "running")
	worker, err := db.CreateUser(&types.User{Username: types.CommercialActionUsername(owner, "active-operation"), Email: "active@example.test", DisplayName: "active", AccountType: types.AccountBot, PassHash: []byte("test-hash")})
	if err != nil {
		t.Fatal(err)
	}
	otherWorker, err := db.CreateUser(&types.User{Username: "same-owner-other-worker", Email: "other-worker@example.test", DisplayName: "other", AccountType: types.AccountBot, PassHash: []byte("test-hash")})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{worker, otherWorker} {
		if _, err := db.db.Exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name) VALUES($1,$2,$3)`, id, owner, fmt.Sprintf("tenant-%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	records, err := db.ListCloudWorkerAdminRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("worker count = %d", len(records))
	}
	for _, record := range records {
		if record.WorkerUID == worker {
			if record.OperationID != "active-operation" || record.RuntimePlan != "light" || record.PlannedExpiresAt == nil || !record.PlannedExpiresAt.Equal(expires) {
				t.Fatalf("lost active action: %+v", record)
			}
		} else if record.OperationID != "" {
			t.Fatalf("another worker inherited operation: %+v", record)
		}
	}
	// Match the rollback's persistent state: failed action remains, bot is gone.
	if _, err := db.db.Exec(`UPDATE commercial_ops_actions SET status='failed', worker_status='failed' WHERE uid=$1 AND operation_id='active-operation'`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM bot_config WHERE user_id=$1`, worker); err != nil {
		t.Fatal(err)
	}
	q := types.CommercialRecordsQuery{Kind: "actions", UID: owner, Status: "failed", Search: "rollback-", Limit: 2}
	page, err := db.ListCommercialRecords(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(page.Records, &rows); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || !page.HasMore || len(rows) != 2 {
		t.Fatalf("first page: %+v %s", page, page.Records)
	}
	for _, row := range rows {
		if row["uid"] != float64(owner) || row["runtime_plan"] != "light" || row["deployment_profile"] != "packed_shared" || len(row["error"].(string)) != 500 {
			t.Fatalf("unsafe or incomplete row: %#v", row)
		}
	}
	for _, secret := range []string{"private-preview", "private-request", "private-fingerprint", "private-lease", "preview_token", "\"request\"", "\"preview\""} {
		if strings.Contains(string(page.Records), secret) {
			t.Fatalf("history leaked %q", secret)
		}
	}
	q.Offset = 2
	page, err = db.ListCommercialRecords(context.Background(), q)
	if err != nil || page.Total != 3 || page.HasMore {
		t.Fatalf("last page: %v %+v", err, page)
	}
	q.Offset = 99
	page, err = db.ListCommercialRecords(context.Background(), q)
	if err != nil || page.Total != 3 || string(page.Records) != "[]" {
		t.Fatalf("empty page: %v %+v", err, page)
	}
	q.Search, q.Offset = "active-operation", 0
	page, err = db.ListCommercialRecords(context.Background(), q)
	if err != nil || page.Total != 1 {
		t.Fatalf("rolled-back worker lost history: %v %+v", err, page)
	}
	records, err = db.ListCloudWorkerAdminRecords()
	if err != nil || len(records) != 1 || records[0].OperationID != "" {
		t.Fatalf("rollback mixed history into machine count: %v %+v", err, records)
	}
}
