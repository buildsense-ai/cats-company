package postgres

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

func TestGrantCloudWorkerRuntimeCreditsIdempotency(t *testing.T) {
	for _, tc := range []struct {
		name, stored, requested string
		conflict                bool
	}{
		{"same tier", "pro", " Pro ", false},
		{"changed tier", "pro", "heavy", true},
		{"legacy default is standard", "", "standard", false},
		{"standard replay as default", "standard", "", false},
		{"default cannot replay pro", "pro", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &Adapter{db: db}
			plan, _ := types.NormalizeCloudWorkerRuntimePlan(tc.requested)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO cloud_worker_credits\(uid, source_ref, expires_at, deployment_profile, billing_mode, runtime_plan\)`).
				WithArgs(int64(7), "manual:runtime-replay:1", nil, "packed_shared", "month", plan).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT uid, deployment_profile, billing_mode, runtime_plan FROM cloud_worker_credits`).
				WithArgs("manual:runtime-replay:1").
				WillReturnRows(sqlmock.NewRows([]string{"uid", "deployment_profile", "billing_mode", "runtime_plan"}).AddRow(7, "packed_shared", "month", tc.stored))
			if tc.conflict {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			count, err := a.GrantCloudWorkerRuntimeCredits(7, 1, "runtime-replay", nil, "packed_shared", "month", tc.requested)
			if (err != nil) != tc.conflict || count != 0 {
				t.Fatalf("count=%d err=%v conflict=%v", count, err, tc.conflict)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReserveCloudWorkerRuntimeCreditFiltersTier(t *testing.T) {
	for _, tc := range []struct {
		requested, stored string
		found             bool
	}{
		{"pro", "pro", true}, {"standard", "", true}, {"heavy", "", false},
	} {
		t.Run(tc.requested, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &Adapter{db: db}
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE cloud_worker_credits c\s+SET state = CASE`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
			rows := sqlmock.NewRows([]string{"id", "deployment_profile", "billing_mode", "expires_at", "retention_days", "runtime_plan", "expiry_mode", "retain_data"})
			if tc.found {
				rows.AddRow(1, "packed_shared", "month", nil, 7, tc.stored, "follow_plan", false)
			}
			mock.ExpectQuery(`SELECT c\.id, .*COALESCE\(NULLIF\(c.runtime_plan, ''\), 'standard'\) = \$4`).
				WithArgs(int64(7), "packed_shared", "month", tc.requested).WillReturnRows(rows)
			if tc.found {
				mock.ExpectExec(`UPDATE cloud_worker_credits\s+SET state = 'reserved'`).WithArgs(int64(1), "runtime-reservation").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			selected, reserved, err := a.ReserveCloudWorkerRuntimeCredit(7, "runtime-reservation", "packed_shared", "month", tc.requested)
			if err != nil || reserved != tc.found || selected.RuntimePlan != tc.stored {
				t.Fatalf("selection=%+v reserved=%v err=%v", selected, reserved, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// This contract uses only an explicitly supplied test database and a disposable
// schema. The known production database is rejected before opening a connection.
func TestPostgresCloudWorkerRuntimeCreditsContract(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to an isolated PostgreSQL test database")
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("CATS_PG_TEST_DSN must be a PostgreSQL URL for an isolated test database")
	}
	if strings.EqualFold(strings.Trim(parsed.Path, "/"), "catsco_app_test") || strings.EqualFold(parsed.Query().Get("dbname"), "catsco_app_test") {
		t.Fatal("catsco_app_test is production and must never be used for this test")
	}
	schema := fmt.Sprintf("cats_admin_runtime_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open isolated test database: %v", err)
	}
	defer base.Close()
	var searchPath string
	if err := base.db.QueryRow(`SHOW search_path`).Scan(&searchPath); err != nil {
		t.Fatal(err)
	}
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`)
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schema)); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.db.QueryRow(`SHOW search_path`).Scan(&searchPath); err != nil || !strings.Contains(searchPath, schema) {
		t.Fatalf("test schema search_path=%q err=%v", searchPath, err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(&types.User{Username: "admin-runtime-owner", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(&types.User{Username: "admin-runtime-other", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(24 * time.Hour)
	for i, plan := range []string{"", "standard", "light", "pro", "heavy"} {
		count, err := db.GrantCloudWorkerRuntimeCredits(owner, 1, fmt.Sprintf("runtime-%d", i), &expiry, "packed_shared", "month", plan)
		if err != nil || count != 1 {
			t.Fatalf("grant %q: count=%d err=%v", plan, count, err)
		}
	}
	for _, tc := range []struct {
		plan      string
		available int
	}{{"standard", 2}, {"pro", 1}, {"heavy", 1}, {"light", 1}, {"", 5}} {
		total, available, err := db.CloudWorkerRuntimeCreditSummary(owner, "packed_shared", "month", tc.plan)
		if err != nil || total != tc.available || available != tc.available {
			t.Fatalf("summary %q: total=%d available=%d err=%v", tc.plan, total, available, err)
		}
	}
	if count, err := db.GrantCloudWorkerRuntimeCredits(owner, 1, "runtime-3", &expiry, "packed_shared", "month", "pro"); err != nil || count != 0 {
		t.Fatalf("replay: %d %v", count, err)
	}
	for _, tc := range []struct {
		uid           int64
		billing, plan string
	}{{owner, "month", "heavy"}, {owner, "month", ""}, {other, "month", "pro"}, {owner, "ondemand", "pro"}} {
		if _, err := db.GrantCloudWorkerRuntimeCredits(tc.uid, 1, "runtime-3", &expiry, "packed_shared", tc.billing, tc.plan); err == nil {
			t.Fatalf("conflicting replay accepted: %+v", tc)
		}
	}
	if count, err := db.GrantCloudWorkerRuntimeCredits(owner, 1, "runtime-0", &expiry, "packed_shared", "month", "standard"); err != nil || count != 0 {
		t.Fatalf("default replay as standard: %d %v", count, err)
	}
	if count, err := db.GrantCloudWorkerConfiguredCredits(owner, 1, "runtime-1", &expiry, "packed_shared", "month"); err != nil || count != 0 {
		t.Fatalf("standard replay as default: %d %v", count, err)
	}
	for _, profile := range []string{"private_nat", "public_ip"} {
		if _, err := db.GrantCloudWorkerRuntimeCredits(owner, 1, "runtime-ecs", &expiry, profile, "month", "pro"); err == nil {
			t.Fatalf("ECS runtime accepted for %s", profile)
		}
	}
	selected, reserved, err := db.ReserveCloudWorkerRuntimeCredit(owner, "reserve-pro", "packed_shared", "month", "pro")
	if err != nil || !reserved || selected.RuntimePlan != "pro" || selected.ExpiresAt == nil {
		t.Fatalf("pro reservation: %+v %v %v", selected, reserved, err)
	}
	if _, reserved, err := db.ReserveCloudWorkerRuntimeCredit(owner, "no-more-pro", "packed_shared", "month", "pro"); err != nil || reserved {
		t.Fatalf("another tier spent for pro: reserved=%v err=%v", reserved, err)
	}
	if queued, err := db.QueueCloudWorkerCredit(owner, "reserve-pro"); err != nil || !queued {
		t.Fatalf("queue: %v %v", queued, err)
	}
	queued, err := db.ListQueuedCloudWorkerCredits(0, 10)
	if err != nil || len(queued) != 1 || queued[0].Tier != "pro" {
		t.Fatalf("queued tier: %+v %v", queued, err)
	}
	if _, reserved, err := db.ReserveCloudWorkerRuntimeCredit(owner, "reserve-heavy", "packed_shared", "month", "heavy"); err != nil || !reserved {
		t.Fatalf("reserve heavy: %v %v", reserved, err)
	}
	if queued, err := db.QueueCloudWorkerCredit(owner, "reserve-heavy"); err != nil || !queued {
		t.Fatalf("queue heavy: %v %v", queued, err)
	}
	if claimed, err := db.ClaimQueuedCloudWorkerRuntimeCredit(owner, "pro"); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	selected, reserved, err = db.ReserveCloudWorkerRuntimeCredit(owner, "retry-pro", "packed_shared", "month", "pro")
	if err != nil || !reserved || selected.RuntimePlan != "pro" {
		t.Fatalf("retry tier: %+v %v %v", selected, reserved, err)
	}
	queued, err = db.ListQueuedCloudWorkerCredits(0, 10)
	if err != nil || len(queued) != 1 || queued[0].Tier != "heavy" {
		t.Fatalf("claim released another tier: %+v %v", queued, err)
	}
	for i, want := range []string{"", "standard"} {
		selected, reserved, err = db.ReserveCloudWorkerRuntimeCredit(owner, fmt.Sprintf("reserve-standard-%d", i), "packed_shared", "month", "standard")
		if err != nil || !reserved || selected.RuntimePlan != want {
			t.Fatalf("standard compatibility: selection=%+v want=%q reserved=%v err=%v", selected, want, reserved, err)
		}
	}
	if _, reserved, err := db.ReserveCloudWorkerRuntimeCredit(owner, "no-more-standard", "packed_shared", "month", "standard"); err != nil || reserved {
		t.Fatalf("another tier spent for standard: reserved=%v err=%v", reserved, err)
	}
	var stored string
	if err := db.db.QueryRow(`SELECT runtime_plan FROM cloud_worker_credits WHERE source_ref='manual:runtime-0:1'`).Scan(&stored); err != nil || stored != "" {
		t.Fatalf("legacy runtime was rewritten: %q %v", stored, err)
	}
}

func TestClaimQueuedCloudWorkerRuntimeCreditFiltersOneTier(t *testing.T) {
	for _, plan := range []string{"pro", "standard"} {
		t.Run(plan, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &Adapter{db: db}
			mock.ExpectExec(`UPDATE cloud_worker_credits .*WHERE id = \( SELECT id FROM cloud_worker_credits .*COALESCE\(NULLIF\(runtime_plan, ''\), 'standard'\) = \$2 .*FOR UPDATE SKIP LOCKED LIMIT 1`).
				WithArgs(int64(7), plan).WillReturnResult(sqlmock.NewResult(0, 1))
			claimed, err := a.ClaimQueuedCloudWorkerRuntimeCredit(7, " "+strings.ToUpper(plan)+" ")
			if err != nil || !claimed {
				t.Fatalf("claimed=%v err=%v", claimed, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
