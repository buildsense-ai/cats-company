package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// A package that includes a machine must fan out to the cloud worker hooks
// exactly once per fulfillment: renewal for existing instances and the
// post-purchase auto provisioning for first purchases. Both stay asynchronous
// so provider work never blocks the payment callback.
//
// Which packages include a machine is a plan attribute (cloud_worker_included),
// not a slug allow-list: the slug check used to live here and in the fulfilment
// transaction, so a new package silently sold tokens with no machine.
func TestFulfillCommercialOrderTriggersCloudWorkerHooks(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-HOOKS"] = &types.CommercialOrder{
		OrderNo: "CC-HOOKS", UID: 38, AmountFen: 5900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
		PlanSlug: "catsco-basic", PlanName: "基础版",
		// Filled in by the real fulfilment from the plan row.
		PlanIncludesWorker: true,
	}
	renewCalls := make(chan int64, 1)
	ensureCalls := make(chan int64, 1)
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		RenewCloudWorkers: func(uid int64) *types.CloudWorkerRenewReport { renewCalls <- uid; return nil },
		EnsureCloudWorker: func(uid int64) { ensureCalls <- uid },
	})
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-HOOKS",
		AmountFen: 5900, Currency: "CNY",
	}
	fulfilled, changed, err := handler.fulfillCommercialOrder("CC-HOOKS", confirmation)
	if err != nil || !changed || fulfilled == nil {
		t.Fatalf("fulfill: changed=%v err=%v", changed, err)
	}
	select {
	case uid := <-renewCalls:
		if uid != 38 {
			t.Fatalf("renew hook uid=%d want 38", uid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renew hook was not called")
	}
	select {
	case uid := <-ensureCalls:
		if uid != 38 {
			t.Fatalf("ensure hook uid=%d want 38", uid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ensure hook was not called")
	}
}

func TestFulfillCommercialOrderSkipsCloudWorkerHooksForOtherPlans(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-HOOKS-SKIP"] = &types.CommercialOrder{
		OrderNo: "CC-HOOKS-SKIP", UID: 38, AmountFen: 10000, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
		// A token-only package: selling quota must never mint a machine. This is
		// the path that would otherwise create a worker out of the static
		// rollout quota for a buyer that holds no provisioning credit.
		PlanSlug: "catsco-legacy-custom", PlanName: "定制",
		PlanIncludesWorker: false,
	}
	called := make(chan int64, 2)
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		RenewCloudWorkers: func(uid int64) *types.CloudWorkerRenewReport { called <- uid; return nil },
		EnsureCloudWorker: func(uid int64) { called <- uid },
	})
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-SKIP",
		AmountFen: 10000, Currency: "CNY",
	}
	_, changed, err := handler.fulfillCommercialOrder("CC-HOOKS-SKIP", confirmation)
	if err != nil || !changed {
		t.Fatalf("fulfill: changed=%v err=%v", changed, err)
	}
	select {
	case uid := <-called:
		t.Fatalf("cloud worker hooks must not run for non-official plans (uid=%d)", uid)
	case <-time.After(200 * time.Millisecond):
	}
}

// Alipay callbacks and the reconciliation loop share fulfillCommercialOrder;
// a duplicate confirmation of an already fulfilled order must stay a no-op so
// the hooks never provision twice.
func TestFulfillCommercialOrderHooksRunOncePerOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-HOOKS-ONCE"] = &types.CommercialOrder{
		OrderNo: "CC-HOOKS-ONCE", UID: 38, AmountFen: 5900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
		PlanSlug: "catsco-personal", PlanName: "个人版",
		PlanIncludesWorker: true,
	}
	ensureCalls := make(chan int64, 4)
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		EnsureCloudWorker: func(uid int64) { ensureCalls <- uid },
	})
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-ONCE",
		AmountFen: 5900, Currency: "CNY",
	}
	if _, changed, err := handler.fulfillCommercialOrder("CC-HOOKS-ONCE", confirmation); err != nil || !changed {
		t.Fatalf("first fulfill: changed=%v err=%v", changed, err)
	}
	if _, changed, err := handler.fulfillCommercialOrder("CC-HOOKS-ONCE", confirmation); err != nil || changed {
		t.Fatalf("second fulfill must be a no-op: changed=%v err=%v", changed, err)
	}
	select {
	case <-ensureCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("ensure hook was not called for the first fulfillment")
	}
	select {
	case uid := <-ensureCalls:
		t.Fatalf("ensure hook ran twice for one order (uid=%d)", uid)
	case <-time.After(200 * time.Millisecond):
	}
}
