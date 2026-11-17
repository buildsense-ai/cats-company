package postgres

import (
	"github.com/openchat/openchat/server/store/types"
	"testing"
	"time"
)

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
	if _, err := db.CompleteMachineUpgrade(op.OperationID, op.ToPlan, op.FromPlan, op.MachineExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, proceed, err := db.RetryMachineUpgradeReview(op.OperationID); err != nil || proceed {
		t.Fatalf("manual review revived terminal operation: %v %v", proceed, err)
	}
}
