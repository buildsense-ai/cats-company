package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// Reusing an invite code must fan out to the cloud-worker hooks the same way a
// first purchase does: resume provider-frozen instances (renewal) and
// auto-provision a first worker when the account owns none. Both hooks stay
// asynchronous so provider work never blocks the redemption response.
func TestRelayCommercialRedeemInviteTriggersCloudWorkerHooks(t *testing.T) {
	store := newCommercialTestStore()
	planID, err := store.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "catsco-personal",
		Name:         "个人版",
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 500},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code: "PERSONAL-1", PlanID: planID, MaxRedemptions: 1, CloudWorkerCredits: 1,
	}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	renewCalls := make(chan int64, 1)
	ensureCalls := make(chan int64, 1)
	handler := NewRelayCommercialHandler(store)
	handler.SetCloudWorkerRenewer(func(uid int64) *types.CloudWorkerRenewReport {
		renewCalls <- uid
		return nil
	})
	handler.SetCloudWorkerEnsure(func(uid int64) { ensureCalls <- uid })

	req := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"PERSONAL-1"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(116)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleRedeemInvite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case uid := <-renewCalls:
		if uid != 116 {
			t.Fatalf("renew hook uid=%d want 116", uid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renew hook was not called")
	}
	select {
	case uid := <-ensureCalls:
		if uid != 116 {
			t.Fatalf("ensure hook uid=%d want 116", uid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ensure hook was not called")
	}
}

// A redemption must stay local-only (and still succeed) when no hooks are
// wired, so tests and deployments without cloud workers keep working.
func TestRelayCommercialRedeemInviteWithoutCloudWorkerHooks(t *testing.T) {
	store := newCommercialTestStore()
	planID, err := store.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "catsco-personal",
		Name:         "个人版",
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 500},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code: "PERSONAL-2", PlanID: planID, MaxRedemptions: 1, CloudWorkerCredits: 1,
	}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	handler := NewRelayCommercialHandler(store)
	req := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"PERSONAL-2"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(117)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleRedeemInvite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK {
		t.Fatalf("decode response: body=%s err=%v", rec.Body.String(), err)
	}
}

// A new helper must never be invoked when the redemption itself failed.
func TestRelayCommercialRedeemInviteSkipsEnsureOnFailure(t *testing.T) {
	store := newCommercialTestStore()
	planID, err := store.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "catsco-personal",
		Name:         "个人版",
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 500},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code: "PERSONAL-3", PlanID: planID, MaxRedemptions: 1, CloudWorkerCredits: 1,
	}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	ensureCalls := make(chan int64, 1)
	handler := NewRelayCommercialHandler(store)
	handler.SetCloudWorkerEnsure(func(uid int64) { ensureCalls <- uid })

	req := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"NOPE"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(118)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleRedeemInvite(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case uid := <-ensureCalls:
		t.Fatalf("ensure hook must not run for a failed redemption (uid=%d)", uid)
	case <-time.After(200 * time.Millisecond):
	}
}

// Repeated redemption attempts against a single-redemption code must reach
// the ensure hook exactly once: only the successful redemption may start
// provisioning, so a failing retry can never open a second worker.
// (The "one worker per redemption regardless of credit count" contract is
// covered by TestRedeemWithMultipleCreditsProvisionsOneWorker.)
func TestRelayCommercialEnsureHookRunsOncePerRedemption(t *testing.T) {
	store := newCommercialTestStore()
	planID, err := store.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "catsco-personal",
		Name:         "个人版",
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 500},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	// Three credits on a single invite: only the first should auto provision.
	if _, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code: "MULTI-1", PlanID: planID, MaxRedemptions: 1, CloudWorkerCredits: 3,
	}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	var ensureInvocation int
	var ensuredUID int64
	handler := NewRelayCommercialHandler(store)
	handler.SetCloudWorkerEnsure(func(uid int64) {
		ensureInvocation++
		ensuredUID = uid
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"MULTI-1"}`))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(119)))
		req.Header.Set("Content-Type", "application/json")
		// Each redemption attempt after the first fails (single redemption
		// code); only the successful one may reach the hook.
		handler.HandleRedeemInvite(httptest.NewRecorder(), req)
	}
	// Wait for the async hook to settle.
	deadline := time.Now().Add(2 * time.Second)
	for ensureInvocation == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ensureInvocation != 1 {
		t.Fatalf("ensure hook invoked %d times, want exactly 1", ensureInvocation)
	}
	if ensuredUID != 119 {
		t.Fatalf("ensure hook uid=%d want 119", ensuredUID)
	}
}

// countingCreditStub implements the configured-credit interfaces with a real
// reservation state machine: it tracks how many credits a provisioning run
// consumes so a multi-credit redemption can be shown to open exactly one
// instance.
//
// It mirrors the production Postgres adapter, which implements
// cloudWorkerBillingCredits (ReserveCloudWorkerConfiguredCredit) - the branch
// HandleCreate picks first - and CloudWorkerCreditSummary used by the
// admission checks in AutoProvisionForOwner/HandleCreate.
type countingCreditStub struct {
	total     int
	available int
	reserved  int
	// createdWorkers records how many instances the provisioner started.
	createdWorkers int
}

func (s *countingCreditStub) CloudWorkerCreditSummary(int64) (int, int, error) {
	return s.total, s.available, nil
}

func (s *countingCreditStub) CloudWorkerConfiguredCreditSummary(int64, string, string) (int, int, error) {
	return s.total, s.available, nil
}

// ReserveCloudWorkerConfiguredCredit mirrors the production reservation: the
// first available credit is consumed and the caller learns which profile and
// billing mode it carried.
func (s *countingCreditStub) ReserveCloudWorkerConfiguredCredit(uid int64, reservation, profile, billing string) (types.CloudWorkerCreditSelection, bool, error) {
	selection := types.CloudWorkerCreditSelection{Profile: types.CloudWorkerPrivateNAT, BillingMode: types.CloudWorkerMonthly}
	if s.available <= 0 {
		return selection, false, nil
	}
	s.available--
	s.reserved++
	return selection, true, nil
}

func (s *countingCreditStub) GrantCloudWorkerConfiguredCredits(int64, int, string, *time.Time, string, string) (int, error) {
	return 0, nil
}

func (s *countingCreditStub) ReserveCloudWorkerCredit(uid int64, reservation string) (bool, error) {
	if s.available <= 0 {
		return false, nil
	}
	s.available--
	s.reserved++
	return true, nil
}

func (s *countingCreditStub) CommitCloudWorkerCredit(int64, string, int64, string, int) error {
	s.reserved--
	return nil
}

func (s *countingCreditStub) ReleaseCloudWorkerCredit(int64, string) error {
	s.reserved--
	s.available++
	return nil
}

func (s *countingCreditStub) ExtendCloudWorkerLifecycles(int64, time.Time, int) error { return nil }
func (s *countingCreditStub) ListCloudWorkerLifecycleDue(time.Time, int) ([]CloudWorkerLifecycle, error) {
	return nil, nil
}
func (s *countingCreditStub) MarkCloudWorkerLifecyclePending(int64, time.Time) error { return nil }
func (s *countingCreditStub) ClaimCloudWorkerLifecycleDeletion(int64) (bool, error) {
	return false, nil
}
func (s *countingCreditStub) MarkCloudWorkerLifecycleDeleted(int64, string) error { return nil }

// A redemption that carries three credits must auto provision exactly one
// instance and leave two credits available for the user to spend later.
func TestRedeemWithMultipleCreditsProvisionsOneWorker(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no script interpreter")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	credits := &countingCreditStub{total: 3, available: 3}
	h.credits = credits

	h.AutoProvisionForOwner(42)

	if len(ts.tenantNames) != 1 {
		t.Fatalf("tenantNames=%v want exactly one provisioned worker", ts.tenantNames)
	}
	if credits.available != 2 {
		t.Fatalf("available credits=%d want 2 left for manual creation", credits.available)
	}
	if credits.reserved != 0 {
		t.Fatalf("reserved credits=%d want 0 after provisioning settles", credits.reserved)
	}
}

// A second provisioning attempt for an account that already owns a worker is
// a renewal, not a creation: the hook must not spend another credit.
func TestAutoProvisionAfterRedeemKeepsExistingWorker(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no script interpreter")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	credits := &countingCreditStub{total: 3, available: 3}
	h.credits = credits

	h.AutoProvisionForOwner(42)
	if len(ts.tenantNames) != 1 {
		t.Fatalf("first run tenantNames=%v want one worker", ts.tenantNames)
	}
	// Simulate the now-owned instance being visible to the ownership check.
	ts.ownerBots = []map[string]interface{}{{"id": int64(2001), "tenant_name": "bot-bot-existing"}}
	h.AutoProvisionForOwner(42)

	if len(ts.tenantNames) != 1 {
		t.Fatalf("second run tenantNames=%v want the existing worker only", ts.tenantNames)
	}
	if credits.available != 2 {
		t.Fatalf("available credits=%d want 2 (no extra spend on renewal)", credits.available)
	}
}
