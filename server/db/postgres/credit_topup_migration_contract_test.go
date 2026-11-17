package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresCreditTopupRollbackPreservesTransactionHistory(t *testing.T) {
	migration, err := os.ReadFile("../migrations/postgres/000074_credit_topup_machine_upgrade.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"empty", "topup", "upgrade"} {
		t.Run(kind, func(t *testing.T) {
			db := newMachineUpgradeTestAdapter(t)
			var orderNo string
			var uid int64
			if kind != "empty" {
				uid = createCreditTopupTestUser(t, db, "rollback-history")
			}
			if kind == "topup" {
				order, err := db.CreateCommercialTopupOrder(uid, 3000, "alipay_page", "rollback-order", nil)
				if err != nil {
					t.Fatal(err)
				}
				orderNo = order.OrderNo
			}
			if kind == "upgrade" {
				seedMachineUpgradeLifecycle(t, db, uid, "bot-rollback-history", time.Now().UTC().Add(24*time.Hour))
				op := machineUpgradeOperationForTest(t, db, uid, "mu-rollback-history", "bot-rollback-history", "rollback-request", 100)
				if _, _, err := db.OpenMachineUpgrade(op); err != nil {
					t.Fatal(err)
				}
			}
			conn, err := db.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Match the migration runner: remove newer empty schemas before 074.
			for _, name := range []string{"000076_machine_upgrade_review_audit.down.sql", "000075_commercial_refund_holds.down.sql"} {
				newer, err := os.ReadFile("../migrations/postgres/" + name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(context.Background(), string(newer)); err != nil {
					t.Fatalf("roll back newer empty migration %s: %v", name, err)
				}
			}
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
				if err := conn.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='commercial_orders' AND column_name='kind')`).Scan(&exists); err != nil || exists {
					t.Fatalf("empty down did not restore old orders shape: exists=%v err=%v", exists, err)
				}
				return
			}
			if downErr == nil || !strings.Contains(downErr.Error(), "transaction history") {
				t.Fatalf("history must block down: %v", downErr)
			}
			if kind == "topup" {
				got, err := db.GetCommercialOrder(uid, orderNo)
				if err != nil || got == nil || got.OrderNo != orderNo {
					t.Fatalf("topup was deleted: %#v %v", got, err)
				}
			} else {
				got, err := db.GetMachineUpgrade(uid, "mu-rollback-history")
				if err != nil || got == nil {
					t.Fatalf("upgrade was deleted: %#v %v", got, err)
				}
			}
		})
	}
}
