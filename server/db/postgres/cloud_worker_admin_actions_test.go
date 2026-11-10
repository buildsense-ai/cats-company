package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

// The admin roster must only attach an in-flight operator action to the exact
// worker that action creates. Matching by owner (or by tenant name) would label
// every other worker of that owner as provisioning, and a worker with no action
// must stay untouched so a genuinely missing one is still reported as missing.
//
// It also pins the term projection: a worker that already has a lifecycle row
// reports that row's expiry_mode/sync_pending, while a still-provisioning one
// has no row yet and must take the mode from the durable action's preview.
func TestCloudWorkerAdminRecordsAttachOnlyExactActiveAction(t *testing.T) {
	matcher := &recordingQueryMatcher{}
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &Adapter{db: db}

	const operationID = "484efdad-787e-4b2f-bc99-fcf5b22474cd"
	provisioningName := types.CommercialActionUsername(116, operationID)
	pendingName := types.CommercialActionUsername(116, "queued-op")

	mock.ExpectQuery(`FROM commercial_ops_actions`).
		WillReturnRows(sqlmock.NewRows([]string{
			"uid", "operation_id", "status", "worker_status", "runtime_plan", "worker_expiry_mode", "worker_expires_at",
		}).
			AddRow(int64(116), operationID, "running", "running", "standard", "follow_plan", "2026-12-19T06:00:00Z").
			// An action journaled before worker_expiry_mode existed projects an
			// empty mode: the historical default, not a mode somebody chose.
			AddRow(int64(116), "queued-op", "pending", "pending", "", "", ""))

	mock.ExpectQuery(`FROM users u`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "owner_id", "owner_username", "owner_display_name", "username", "display_name",
			"tenant_name", "state", "enabled", "visibility", "lifecycle_state", "package_expires_at",
			"delete_after", "last_error", "credit_state", "credit_source_ref", "credit_expires_at",
			"credit_runtime_plan", "expiry_mode", "sync_pending",
		}).
			// The worker the durable action is creating right now: no lifecycle row.
			AddRow(int64(1671), int64(116), "owner116", "Owner", provisioningName, "", provisioningName,
				0, true, "public", nil, nil, nil, "", nil, "", nil, "", "legacy", false).
			// Another worker of the same owner with no action of its own, but with
			// its own fixed term that still has to reach the provider.
			AddRow(int64(1600), int64(116), "owner116", "Owner", "bot-ops-other", "", "bot-ops-other",
				0, true, "public", nil, nil, nil, "", nil, "", nil, "", "fixed", true).
			// A second, pending action still uses its own identity.
			AddRow(int64(1700), int64(116), "owner116", "Owner", pendingName, "", pendingName,
				0, true, "public", nil, nil, nil, "", nil, "", nil, "", "legacy", false))

	mock.ExpectQuery(`FROM cloud_worker_bindings b`).
		WillReturnRows(sqlmock.NewRows([]string{
			"worker_uid", "owner_uid", "owner_username", "owner_display_name", "tenant_name", "provider",
			"region_id", "project_id", "az_name", "instance_id", "instance_name", "public_ip",
			"management_mode", "lifecycle_mode", "source", "status", "last_verified_at",
		}))

	records, err := adapter.ListCloudWorkerAdminRecords()
	if err != nil {
		t.Fatalf("list cloud worker admin records: %v", err)
	}
	byUsername := map[string]types.CloudWorkerAdminRecord{}
	for _, record := range records {
		byUsername[record.Username] = record
	}
	if len(records) != 3 {
		t.Fatalf("records: %d", len(records))
	}

	provisioning, ok := byUsername[provisioningName]
	if !ok || provisioning.OperationID != operationID || provisioning.OperationStatus != "running" || provisioning.WorkerStatus != "running" {
		t.Fatalf("active action not attached: %+v", provisioning)
	}
	if provisioning.RuntimePlan != "standard" || provisioning.PlannedExpiresAt == nil ||
		!provisioning.PlannedExpiresAt.Equal(time.Date(2026, 12, 19, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("preview facts lost: %+v", provisioning)
	}
	if provisioning.ExpiryMode != "follow_plan" {
		t.Fatalf("a provisioning worker must report the action's own term, got %q", provisioning.ExpiryMode)
	}

	other := byUsername["bot-ops-other"]
	if other.OperationID != "" || other.OperationStatus != "" || other.WorkerStatus != "" || other.PlannedExpiresAt != nil {
		t.Fatalf("owner match leaked an operation onto an unrelated worker: %+v", other)
	}
	if other.ExpiryMode != "fixed" || !other.SyncPending {
		t.Fatalf("lifecycle term/sync gap lost from the admin projection: %+v", other)
	}
	if pending := byUsername[pendingName]; pending.OperationID != "queued-op" || pending.OperationStatus != "pending" || pending.WorkerStatus != "pending" || pending.PlannedExpiresAt != nil {
		t.Fatalf("pending action: %+v", pending)
	} else if pending.ExpiryMode != "legacy" {
		t.Fatalf("an action without a mode must not invent one: %q", pending.ExpiryMode)
	}

	if len(matcher.queries) == 0 || !strings.Contains(matcher.queries[0], "WHERE status IN ('pending','running')") {
		t.Fatalf("completed/failed actions must not be matched: %q", matcher.queries)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
