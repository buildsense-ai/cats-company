package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

// 这条理由是中文、且超过 8 个字符：既验证「按字符而不是按字节」的下限，也验证
// 审计行里存的就是运维真正写的调查理由。
const machineUpgradeReviewTestReason = "适配器连通性已核实并恢复，允许再跑一次"

func TestPostgresMachineUpgradeAutomaticAttemptsBoundedWithManualReview(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "upgrade-review")
	grantTopupCredits(t, db, uid, 5000, "topup:review")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-review", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-review", "bot-upgrade-review", "request-review", 2000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatal(err)
	}
	if _, proceed, err := db.RetryMachineUpgradeReview(op.OperationID); err != nil || proceed {
		t.Fatalf("manual review cannot charge a pending purchase: proceed=%v err=%v", proceed, err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("first charge: %v %v", proceed, err)
	}
	for attempt := 1; attempt < types.MachineUpgradeMaxAutomaticAttempts; attempt++ {
		if _, err := db.MarkMachineUpgradeReconciling(op.OperationID, "unknown"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`UPDATE cloud_worker_machine_upgrades SET updated_at=NOW()-INTERVAL '4 minutes' WHERE operation_id=$1`, op.OperationID); err != nil {
			t.Fatal(err)
		}
		got, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, true)
		if err != nil || !proceed || got.Attempts != attempt+1 {
			t.Fatalf("attempt %d: %#v %v %v", attempt, got, proceed, err)
		}
	}
	if _, err := db.MarkMachineUpgradeReconciling(op.OperationID, "unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE cloud_worker_machine_upgrades SET updated_at=NOW()-INTERVAL '4 minutes' WHERE operation_id=$1`, op.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, true); err != nil || proceed {
		t.Fatalf("automatic attempt exceeded cap: %v %v", proceed, err)
	}
	items, err := db.ListMachineUpgradesForReconcile(time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.OperationID == op.OperationID {
			t.Fatal("paused operation still in automatic queue")
		}
	}
	got, proceed, err := db.RetryMachineUpgradeReview(op.OperationID)
	if err != nil || !proceed || got.Attempts != types.MachineUpgradeMaxAutomaticAttempts+1 {
		t.Fatalf("manual claim: %#v %v %v", got, proceed, err)
	}
	if _, proceed, err := db.RetryMachineUpgradeReview(op.OperationID); err != nil || proceed {
		t.Fatalf("manual claim ignored lease: %v %v", proceed, err)
	}
	if balance := paidBucketUnits(t, db, uid); balance != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("retry charged twice: %d", balance)
	}
	// 不带审计的兼容入口只留给这组兼容性测试：它不能凭空写出一行「谁授权了」
	// 的审计，所以整条轨迹到这里仍然是空的。
	if audits, err := db.machineUpgradeReviewAuditsFor(op.OperationID); err != nil || len(audits) != 0 {
		t.Fatalf("audit-free compatibility claim wrote an audit row: %#v %v", audits, err)
	}
	if _, err := db.CompleteMachineUpgrade(op.OperationID, op.ToPlan, op.FromPlan, op.MachineExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, proceed, err := db.RetryMachineUpgradeReview(op.OperationID); err != nil || proceed {
		t.Fatalf("manual review revived terminal operation: %v %v", proceed, err)
	}
}

// seedMachineUpgradeReviewable holds a purchase, parks it in reconciling with a
// message, and lets the reconcile lease expire so an operator review can claim it.
func seedMachineUpgradeReviewable(t *testing.T, db *Adapter, uid int64, operationID, tenant, requestID string) *types.MachineUpgradeOperation {
	t.Helper()
	op := machineUpgradeOperationForTest(t, db, uid, operationID, tenant, requestID, 2000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade %s: %v", operationID, err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(operationID, false); err != nil || !proceed {
		t.Fatalf("charge machine upgrade %s: proceed=%v err=%v", operationID, proceed, err)
	}
	if _, err := db.MarkMachineUpgradeReconciling(operationID, "adapter timeout"); err != nil {
		t.Fatalf("park machine upgrade %s: %v", operationID, err)
	}
	if _, err := db.db.Exec(
		`UPDATE cloud_worker_machine_upgrades SET updated_at=NOW()-INTERVAL '4 minutes' WHERE operation_id=$1`,
		operationID); err != nil {
		t.Fatalf("expire reconcile lease of %s: %v", operationID, err)
	}
	stored, err := db.GetMachineUpgrade(uid, operationID)
	if err != nil || stored == nil {
		t.Fatalf("read machine upgrade %s: %#v %v", operationID, stored, err)
	}
	return stored
}

// 人工复核的授权必须和认领一起落库：谁签的字、签在哪笔操作上、什么理由，以及
// 认领前后那一行长什么样。没有这行记录，重试就只是「日志里有人说过」。
func TestPostgresMachineUpgradeReviewAuditPersistsWithLeasedClaim(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "upgrade-audit")
	grantTopupCredits(t, db, uid, 5000, "topup:audit")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-audit", time.Now().UTC().Add(10*24*time.Hour))
	before := seedMachineUpgradeReviewable(t, db, uid, "mu-audit", "bot-upgrade-audit", "request-audit")
	const actor = int64(4242)

	claimed, proceed, err := db.RetryMachineUpgradeReviewAudited(before.OperationID, actor, "  "+machineUpgradeReviewTestReason+"  ")
	if err != nil || !proceed || claimed == nil {
		t.Fatalf("audited review claim: %#v %v %v", claimed, proceed, err)
	}
	audits, err := db.machineUpgradeReviewAuditsFor(before.OperationID)
	if err != nil {
		t.Fatalf("read review audit: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1: %#v", len(audits), audits)
	}
	got := audits[0]
	if got.OperationID != before.OperationID || got.ActorUID != actor || got.Action != machineUpgradeReviewAuditAction {
		t.Fatalf("audit identity mismatch: %#v", got)
	}
	if got.Reason != machineUpgradeReviewTestReason {
		t.Fatalf("reason stored as %q, want trimmed %q", got.Reason, machineUpgradeReviewTestReason)
	}
	if got.PreviousStatus != before.Status || got.PreviousMessage != before.Message || got.PreviousAttempts != before.Attempts {
		t.Fatalf("previous row not recorded: %#v want status=%q message=%q attempts=%d",
			got, before.Status, before.Message, before.Attempts)
	}
	if got.ClaimedAttempt != before.Attempts+1 || claimed.Attempts != got.ClaimedAttempt {
		t.Fatalf("claimed attempt = %d (audit %d), want %d", claimed.Attempts, got.ClaimedAttempt, before.Attempts+1)
	}
	if delta := got.ClaimedAt.Sub(claimed.UpdatedAt); delta > time.Microsecond || delta < -time.Microsecond {
		t.Fatalf("claimed_at %s does not match the claim updated_at %s", got.ClaimedAt, claimed.UpdatedAt)
	}
	// 表只追加：授权历史不能被改写或删除。
	if _, err := db.db.Exec(`UPDATE cloud_worker_machine_upgrade_review_audits SET reason='tampered reason' WHERE audit_id=$1`, got.AuditID); err == nil {
		t.Fatal("review audit row is mutable")
	}
	if _, err := db.db.Exec(`DELETE FROM cloud_worker_machine_upgrade_review_audits WHERE audit_id=$1`, got.AuditID); err == nil {
		t.Fatal("review audit row is deletable")
	}
	// 复核不能二次扣费：钱在第一次下单时就预占过了。
	if balance := paidBucketUnits(t, db, uid); balance != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("review charged the buyer twice: %d", balance)
	}
}

// 没有拿到租约就没有授权：pending（还没付钱）、终态、以及保护期内的行都不允许
// 留下审计行，否则「审计」会变成一串没有对应重跑的假授权。
func TestPostgresMachineUpgradeReviewAuditSkippedWithoutLeasedClaim(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "upgrade-audit-noop")
	grantTopupCredits(t, db, uid, 5000, "topup:audit-noop")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-audit-noop", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-audit-noop", "bot-upgrade-audit-noop", "request-audit-noop", 2000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatal(err)
	}
	assertNoReviewAudit := func(stage string) {
		t.Helper()
		audits, err := db.machineUpgradeReviewAuditsFor(op.OperationID)
		if err != nil {
			t.Fatalf("%s: read review audit: %v", stage, err)
		}
		if len(audits) != 0 {
			t.Fatalf("%s: wrote %d audit rows for a claim that never happened: %#v", stage, len(audits), audits)
		}
	}
	// pending：复核不能替用户扣第一笔钱，也就没有授权可记。
	if _, proceed, err := db.RetryMachineUpgradeReviewAudited(op.OperationID, 4242, machineUpgradeReviewTestReason); err != nil || proceed {
		t.Fatalf("review charged a pending purchase: %v %v", proceed, err)
	}
	assertNoReviewAudit("pending")
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("first charge: %v %v", proceed, err)
	}
	if _, err := db.MarkMachineUpgradeReconciling(op.OperationID, "adapter timeout"); err != nil {
		t.Fatal(err)
	}
	// 保护期还没过：另一个进程刚认领过，这里必须放手，也不能写审计。
	if _, proceed, err := db.RetryMachineUpgradeReviewAudited(op.OperationID, 4242, machineUpgradeReviewTestReason); err != nil || proceed {
		t.Fatalf("review ignored the reconcile lease: %v %v", proceed, err)
	}
	assertNoReviewAudit("lease held")
	if _, err := db.CompleteMachineUpgrade(op.OperationID, op.ToPlan, op.FromPlan, op.MachineExpiresAt); err != nil {
		t.Fatal(err)
	}
	// 终态：钱已经结算成升级，复核不能复活它。
	if _, proceed, err := db.RetryMachineUpgradeReviewAudited(op.OperationID, 4242, machineUpgradeReviewTestReason); err != nil || proceed {
		t.Fatalf("review revived a completed operation: %v %v", proceed, err)
	}
	assertNoReviewAudit("completed")
}

// 审计写不进去，认领就必须一起回滚：否则管理端只要让审计表不可写，就能一边报错
// 一边把操作重跑出去——那还是「没有授权依据的授权」。
func TestPostgresMachineUpgradeReviewAuditFailureRollsBackClaim(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "upgrade-audit-fail")
	grantTopupCredits(t, db, uid, 5000, "topup:audit-fail")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-audit-fail", time.Now().UTC().Add(10*24*time.Hour))
	before := seedMachineUpgradeReviewable(t, db, uid, "mu-audit-fail", "bot-upgrade-audit-fail", "request-audit-fail")
	if _, err := db.db.Exec(`
		CREATE OR REPLACE FUNCTION test_reject_machine_upgrade_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'review audit store unavailable';
		END;
		$$;
		DROP TRIGGER IF EXISTS test_reject_machine_upgrade_review_audit ON cloud_worker_machine_upgrade_review_audits;
		CREATE TRIGGER test_reject_machine_upgrade_review_audit BEFORE INSERT ON cloud_worker_machine_upgrade_review_audits
		FOR EACH ROW EXECUTE FUNCTION test_reject_machine_upgrade_review_audit();`); err != nil {
		t.Fatalf("install rejecting audit trigger: %v", err)
	}
	if _, proceed, err := db.RetryMachineUpgradeReviewAudited(before.OperationID, 4242, machineUpgradeReviewTestReason); err == nil || proceed {
		t.Fatalf("claim survived an unwritable audit table: proceed=%v err=%v", proceed, err)
	}
	after, err := db.GetMachineUpgrade(uid, before.OperationID)
	if err != nil || after == nil {
		t.Fatalf("read machine upgrade after failed claim: %#v %v", after, err)
	}
	if after.Status != before.Status || after.Attempts != before.Attempts ||
		after.Message != before.Message || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("failed audit did not roll the claim back:\n before=%#v\n after =%#v", before, after)
	}
	if audits, err := db.machineUpgradeReviewAuditsFor(before.OperationID); err != nil || len(audits) != 0 {
		t.Fatalf("failed claim left an audit row behind: %#v %v", audits, err)
	}
	if _, err := db.db.Exec(`DROP TRIGGER IF EXISTS test_reject_machine_upgrade_review_audit ON cloud_worker_machine_upgrade_review_audits`); err != nil {
		t.Fatalf("drop rejecting audit trigger: %v", err)
	}
	// 回滚干净之后，同一行仍然可以被合法认领一次。
	claimed, proceed, err := db.RetryMachineUpgradeReviewAudited(before.OperationID, 4242, machineUpgradeReviewTestReason)
	if err != nil || !proceed || claimed == nil || claimed.Attempts != before.Attempts+1 {
		t.Fatalf("rollback left the row unclaimable: %#v %v %v", claimed, proceed, err)
	}
	if audits, err := db.machineUpgradeReviewAuditsFor(before.OperationID); err != nil || len(audits) != 1 {
		t.Fatalf("retry after rollback did not record its authorization: %#v %v", audits, err)
	}
}

// 授权本身不合格（没有授权人、理由太短、理由过长）必须在锁行之前就拒绝：不写
// 审计、不动状态，后面一次合法复核仍然能拿到完整的尝试次数。
func TestPostgresMachineUpgradeReviewAuditRejectsInvalidAuthorization(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "upgrade-audit-invalid")
	grantTopupCredits(t, db, uid, 5000, "topup:audit-invalid")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-audit-invalid", time.Now().UTC().Add(10*24*time.Hour))
	before := seedMachineUpgradeReviewable(t, db, uid, "mu-audit-invalid", "bot-upgrade-audit-invalid", "request-audit-invalid")
	for _, tc := range []struct {
		name  string
		actor int64
		why   string
	}{
		{"no actor", 0, machineUpgradeReviewTestReason},
		{"negative actor", -1, machineUpgradeReviewTestReason},
		{"blank reason", 4242, "        "},
		{"seven characters", 4242, "abcdefg"},
		{"seven Chinese characters", 4242, "排查适配器并恢"},
		{"one thousand and one characters", 4242, strings.Repeat("理", 1001)},
	} {
		if _, proceed, err := db.RetryMachineUpgradeReviewAudited(before.OperationID, tc.actor, tc.why); err == nil || proceed {
			t.Fatalf("%s: invalid authorization was accepted: proceed=%v err=%v", tc.name, proceed, err)
		}
	}
	after, err := db.GetMachineUpgrade(uid, before.OperationID)
	if err != nil || after == nil {
		t.Fatalf("read machine upgrade after refused authorizations: %#v %v", after, err)
	}
	if after.Status != before.Status || after.Attempts != before.Attempts || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("refused authorization touched the row:\n before=%#v\n after =%#v", before, after)
	}
	if audits, err := db.machineUpgradeReviewAuditsFor(before.OperationID); err != nil || len(audits) != 0 {
		t.Fatalf("refused authorization wrote an audit row: %#v %v", audits, err)
	}
	// 1000 个字符正好在边界上，必须被接受。
	claimed, proceed, err := db.RetryMachineUpgradeReviewAudited(before.OperationID, 4242, strings.Repeat("理", 1000))
	if err != nil || !proceed || claimed == nil || claimed.Attempts != before.Attempts+1 {
		t.Fatalf("upper bound rejected: %#v %v %v", claimed, proceed, err)
	}
}

// 回滚 000076 只允许没有复核历史的库；有历史时必须拒绝，历史一行都不能少。
func TestPostgresMachineUpgradeReviewAuditRollbackRefusesHistory(t *testing.T) {
	migration, err := os.ReadFile("../migrations/postgres/000076_machine_upgrade_review_audit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"empty", "audited"} {
		t.Run(kind, func(t *testing.T) {
			db := newMachineUpgradeTestAdapter(t)
			var operationID, tenant string
			var uid int64
			if kind == "audited" {
				uid = createCreditTopupTestUser(t, db, "upgrade-audit-rollback")
				grantTopupCredits(t, db, uid, 5000, "topup:audit-rollback")
				tenant = "bot-upgrade-audit-rollback"
				operationID = "mu-audit-rollback"
				seedMachineUpgradeLifecycle(t, db, uid, tenant, time.Now().UTC().Add(10*24*time.Hour))
				seedMachineUpgradeReviewable(t, db, uid, operationID, tenant, "request-audit-rollback")
				if _, proceed, err := db.RetryMachineUpgradeReviewAudited(operationID, 4242, machineUpgradeReviewTestReason); err != nil || !proceed {
					t.Fatalf("seed review audit: %v %v", proceed, err)
				}
			}
			conn, err := db.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, downErr := conn.ExecContext(context.Background(), string(migration))
			if downErr != nil {
				if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "empty" {
				if downErr != nil {
					t.Fatalf("empty schema should roll back: %v", downErr)
				}
				var exists bool
				if err := conn.QueryRowContext(context.Background(),
					`SELECT to_regclass('cloud_worker_machine_upgrade_review_audits') IS NOT NULL`).Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Fatal("empty down left the audit table behind")
				}
				return
			}
			if downErr == nil || !strings.Contains(downErr.Error(), "review history") {
				t.Fatalf("review history must block down: %v", downErr)
			}
			audits, err := db.machineUpgradeReviewAuditsFor(operationID)
			if err != nil || len(audits) != 1 {
				t.Fatalf("rollback dropped the authorization history: %#v %v", audits, err)
			}
		})
	}
}

var machineUpgradeAuditTestColumns = []string{
	"operation_id", "uid", "tenant_name", "lifecycle_id", "from_plan", "to_plan",
	"cost_units", "status", "held", "settled", "client_request_id", "quote_id",
	"attempts", "message", "machine_expires_at", "started_at", "finished_at",
	"created_at", "updated_at",
}

// 上面那条回滚测试需要真实 PostgreSQL；这条不需要 DSN，用 sqlmock 把「INSERT 审
// 计在 COMMIT 之前、失败就一定 ROLLBACK」这件事钉死，任何把审计搬出事务的改动都
// 会在这里断掉。
func TestMachineUpgradeReviewAuditSharesTheClaimTransaction(t *testing.T) {
	const operationID = "mu-audit-sql"
	for _, tc := range []struct {
		name      string
		insertErr error
	}{
		{"audit row is written before commit", nil},
		{"audit failure rolls the claim back", errors.New("review audit store unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			a := &Adapter{db: sqlDB}
			now := time.Now().UTC()
			locked := machineUpgradeAuditTestColumns
			rows := func(status string, attempts int, message string, updatedAt time.Time) *sqlmock.Rows {
				return sqlmock.NewRows(locked).AddRow(operationID, int64(77), "bot-worker", int64(8),
					"light", "pro", int64(100), status, true, false, "request-audit-sql", "mq-audit-sql",
					attempts, message, now.Add(24*time.Hour), nil, nil, now.Add(-time.Hour), updatedAt)
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT operation_id, uid, [\s\S]*FROM cloud_worker_machine_upgrades WHERE operation_id = \$1 FOR UPDATE`).
				WillReturnRows(rows("reconciling", 5, "adapter timeout", now.Add(-4*time.Minute)))
			mock.ExpectQuery(`UPDATE cloud_worker_machine_upgrades[\s\S]*RETURNING operation_id, uid, `).
				WillReturnRows(rows("running", 6, "", now))
			insert := mock.ExpectExec(`INSERT INTO cloud_worker_machine_upgrade_review_audits`).
				WithArgs(operationID, int64(4242), machineUpgradeReviewAuditAction, machineUpgradeReviewTestReason,
					"reconciling", "adapter timeout", 5, 6, sqlmock.AnyArg())
			if tc.insertErr != nil {
				insert.WillReturnError(tc.insertErr)
				mock.ExpectRollback()
			} else {
				insert.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			claimed, proceed, err := a.startMachineUpgradeCharge(operationID, true, true, &machineUpgradeReviewAuditInput{
				ActorUID: 4242, Reason: machineUpgradeReviewTestReason,
			})
			if tc.insertErr != nil {
				if err == nil || proceed || claimed != nil {
					t.Fatalf("unwritable audit returned a claim: %#v %v %v", claimed, proceed, err)
				}
			} else if err != nil || !proceed || claimed == nil || claimed.Attempts != 6 {
				t.Fatalf("audited claim: %#v %v %v", claimed, proceed, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// CreateSchema 里那份 const 和迁移文件必须是同一组语句：两边分开维护，任何一边
// 改了而另一边没跟上，都会让「从零建库」和「按迁移升级」产生不同的表结构。
// 这条测试不需要 DSN。
func TestMachineUpgradeReviewAuditSchemaMatchesMigration(t *testing.T) {
	raw, err := os.ReadFile("../migrations/postgres/000076_machine_upgrade_review_audit.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	modules := func(text string) string {
		var built strings.Builder
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			built.WriteString(trimmed)
			built.WriteString("\n")
		}
		return built.String()
	}
	if modules(string(raw)) != modules(createMachineUpgradeReviewAuditTable) {
		t.Fatal("000076 up migration and createMachineUpgradeReviewAuditTable drifted apart")
	}
}
