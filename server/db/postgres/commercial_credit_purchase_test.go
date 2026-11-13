package postgres

import (
	"fmt"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresCommercialCreditOnlyPurchase(t *testing.T) {
	db := modelCreditDatabase(t)
	uid, err := db.CreateUser(&types.User{Username: "credit-buyer", Email: "credit-buyer@example.test", DisplayName: "Credit buyer", AccountType: types.AccountHuman, PassHash: []byte("test-hash")})
	if err != nil {
		t.Fatal(err)
	}
	for i, offer := range []struct {
		price int64
		units int64
	}{{3900, 4_000_000_000}, {7900, 12_000_000_000}, {15900, 22_500_000_000}} {
		t.Run(fmt.Sprintf("tier-%d", i+1), func(t *testing.T) {
			id, err := db.CreateCommercialPlan(&types.CommercialPlan{Slug: fmt.Sprintf("credit-purchase-%d", i+1), Name: "Credit", PriceFen: offer.price, Currency: "CNY", SaleState: "public", DurationDays: 30, PlanTier: i + 1, ModelCreditUnits: offer.units})
			if err != nil {
				t.Fatal(err)
			}
			req := &types.CommercialOrder{OrderNo: fmt.Sprintf("CCCREDIT%d", i+1), UID: uid, PlanID: id, Channel: "alipay_page", ClientRequestID: fmt.Sprintf("credit-purchase-%d", i+1)}
			order, err := db.CreateCommercialOrder(req)
			if err != nil {
				t.Fatalf("new credit buyer order: %v", err)
			}
			if order.AmountFen != offer.price || order.Currency != "CNY" || order.PlanModelCreditUnits != offer.units || len(order.PlanModelBudgets) != 0 || order.PlanMonthlyBudget != 0 {
				t.Fatalf("bad snapshot: %#v", order)
			}
			replay, err := db.CreateCommercialOrder(req)
			if err != nil || replay.OrderNo != order.OrderNo {
				t.Fatalf("idempotency: %#v %v", replay, err)
			}
			closed, changed, err := db.CancelCommercialOrder(uid, order.OrderNo, "unpaid acceptance")
			if err != nil || !changed || closed.Status != "closed" || closed.PaidAt != nil || closed.FulfilledAt != nil {
				t.Fatalf("cancel: %#v %v", closed, err)
			}
		})
	}
	var grants, entitlements int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid=$1`, uid).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid=$1`, uid).Scan(&entitlements); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || entitlements != 0 {
		t.Fatalf("unpaid order granted benefits: ledger=%d entitlements=%d", grants, entitlements)
	}
	for _, tier := range []int{0, 4} {
		id, err := db.CreateCommercialPlan(&types.CommercialPlan{Slug: fmt.Sprintf("credit-bad-%d", tier), Name: "Bad credit", PriceFen: 3900, Currency: "CNY", DurationDays: 30, PlanTier: tier, ModelCreditUnits: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.CreateCommercialOrder(&types.CommercialOrder{OrderNo: fmt.Sprintf("CCBAD%d", tier), UID: uid, PlanID: id, Channel: "alipay_page", ClientRequestID: fmt.Sprintf("bad-%d", tier)}); err == nil {
			t.Fatalf("bad tier %d accepted", tier)
		}
	}
}
