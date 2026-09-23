package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type fakeAutoRenewStore struct {
	due     []*types.CommercialAutoRenewDue
	runs    []*types.CommercialAutoRenewRun
	config  *types.CommercialAutoRenewConfig
	listErr error
}

func (s *fakeAutoRenewStore) GetCommercialAutoRenewConfig(uid int64) (*types.CommercialAutoRenewConfig, error) {
	return s.config, nil
}

func (s *fakeAutoRenewStore) SetCommercialAutoRenewConfig(uid int64, enabled bool, note string) (*types.CommercialAutoRenewConfig, error) {
	s.config = &types.CommercialAutoRenewConfig{UID: uid, Enabled: enabled, Note: note}
	return s.config, nil
}

func (s *fakeAutoRenewStore) ListDueCommercialAutoRenew(now time.Time, within time.Duration) ([]*types.CommercialAutoRenewDue, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.due, nil
}

func (s *fakeAutoRenewStore) RecordCommercialAutoRenewRun(run *types.CommercialAutoRenewRun) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *fakeAutoRenewStore) ListCommercialAutoRenewRuns(uid int64, limit int) ([]*types.CommercialAutoRenewRun, error) {
	return s.runs, nil
}

type fakeAutoRenewExecutor struct {
	operations []*types.CommercialAccountAdjustment
	err        error
	failUID    int64
	result     *types.CommercialAccountAdjustmentResult
}

func (f *fakeAutoRenewExecutor) ApplyCommercialAutoRenewExtend(ctx context.Context, uid int64, expectedExpiry time.Time, operationID, note string) (*types.CommercialAccountAdjustmentResult, error) {
	expiry := expectedExpiry
	f.operations = append(f.operations, &types.CommercialAccountAdjustment{UID: uid, ExpectedExpiresAt: &expiry, OperationID: operationID, Note: note})
	if f.failUID != 0 && uid == f.failUID {
		return nil, errors.New("relay key is not configured")
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestCommercialAutoRenewRunnerExtendsDueAccounts(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(3 * 24 * time.Hour)
	newExpiry := expiry.AddDate(0, 0, 30)
	store := &fakeAutoRenewStore{due: []*types.CommercialAutoRenewDue{{UID: 42, ExpiresAt: expiry}}}
	executor := &fakeAutoRenewExecutor{result: &types.CommercialAccountAdjustmentResult{
		Action: commercialAutoRenewActionExtend, Applied: true, ExpiresAt: &newExpiry,
	}}
	runner := NewCommercialAutoRenewRunner(store, executor, 7*24*time.Hour)

	applied, failed := runner.RunOnce(context.Background())
	if applied != 1 || failed != 0 {
		t.Fatalf("run counts applied=%d failed=%d want 1/0", applied, failed)
	}
	if len(executor.operations) != 1 {
		t.Fatalf("executor calls: %#v", executor.operations)
	}
	operation := executor.operations[0]
	wantOperationID := fmt.Sprintf("auto-renew-42-%d", expiry.Unix())
	if operation.UID != 42 {
		t.Fatalf("operation uid=%d want 42", operation.UID)
	}
	if operation.OperationID != wantOperationID {
		t.Fatalf("operation id=%q want %q", operation.OperationID, wantOperationID)
	}
	if operation.ExpectedExpiresAt == nil || !operation.ExpectedExpiresAt.Equal(expiry) {
		t.Fatalf("expected expiry=%v want %v", operation.ExpectedExpiresAt, expiry)
	}
	if len(store.runs) != 1 {
		t.Fatalf("run log: %#v", store.runs)
	}
	run := store.runs[0]
	if run.Status != commercialAutoRenewRunApplied || run.Action != commercialAutoRenewActionExtend || run.OperationID != wantOperationID {
		t.Fatalf("run record: %#v", run)
	}
	if run.NewExpiry == nil || !run.NewExpiry.Equal(newExpiry) {
		t.Fatalf("run new expiry: %#v want %v", run.NewExpiry, newExpiry)
	}
}

func TestCommercialAutoRenewRunnerRecordsFailuresAndContinues(t *testing.T) {
	expiry := time.Now().UTC().Add(2 * 24 * time.Hour)
	newExpiry := expiry.AddDate(0, 0, 30)
	store := &fakeAutoRenewStore{due: []*types.CommercialAutoRenewDue{{UID: 7, ExpiresAt: expiry}, {UID: 42, ExpiresAt: expiry}}}
	executor := &fakeAutoRenewExecutor{failUID: 7, result: &types.CommercialAccountAdjustmentResult{Applied: true, ExpiresAt: &newExpiry}}
	runner := NewCommercialAutoRenewRunner(store, executor, 7*24*time.Hour)

	applied, failed := runner.RunOnce(context.Background())
	if applied != 1 || failed != 1 {
		t.Fatalf("run counts applied=%d failed=%d want 1/1", applied, failed)
	}
	if len(store.runs) != 2 {
		t.Fatalf("run log: %#v", store.runs)
	}
	statuses := map[int64]string{}
	for _, run := range store.runs {
		statuses[run.UID] = run.Status
	}
	if statuses[7] != commercialAutoRenewRunFailed || statuses[42] != commercialAutoRenewRunApplied {
		t.Fatalf("per-account statuses: %#v", statuses)
	}
	if !strings.Contains(store.runs[0].Message, "relay key") {
		t.Fatalf("failure message lost: %#v", store.runs[0])
	}
}

func TestCommercialAutoRenewRunnerTreatsReplayAsAppliedWithoutDoubleBilling(t *testing.T) {
	expiry := time.Now().UTC().Add(3 * 24 * time.Hour)
	newExpiry := expiry.AddDate(0, 0, 30)
	store := &fakeAutoRenewStore{due: []*types.CommercialAutoRenewDue{{UID: 5, ExpiresAt: expiry}}}
	executor := &fakeAutoRenewExecutor{result: &types.CommercialAccountAdjustmentResult{Applied: false, ExpiresAt: &newExpiry}}
	runner := NewCommercialAutoRenewRunner(store, executor, 7*24*time.Hour)

	applied, failed := runner.RunOnce(context.Background())
	if applied != 1 || failed != 0 {
		t.Fatalf("replay counts applied=%d failed=%d want 1/0", applied, failed)
	}
	if len(store.runs) != 1 || store.runs[0].Status != commercialAutoRenewRunApplied {
		t.Fatalf("replay run record: %#v", store.runs)
	}
	if !strings.Contains(store.runs[0].Message, "已顺延") {
		t.Fatalf("replay message: %#v", store.runs[0])
	}
	if len(executor.operations) != 1 {
		t.Fatalf("replay must call the executor exactly once: %#v", executor.operations)
	}
}

func TestCommercialAutoRenewRunnerHandlesListErrors(t *testing.T) {
	store := &fakeAutoRenewStore{listErr: errors.New("database is down")}
	runner := NewCommercialAutoRenewRunner(store, &fakeAutoRenewExecutor{}, 7*24*time.Hour)
	if applied, failed := runner.RunOnce(context.Background()); applied != 0 || failed != 0 {
		t.Fatalf("list errors must not mutate state: %d/%d", applied, failed)
	}
	if len(store.runs) != 0 {
		t.Fatalf("unexpected run records: %#v", store.runs)
	}
}

type autoRenewHandlerStore struct {
	*commercialTestStore
	config *types.CommercialAutoRenewConfig
	runs   []*types.CommercialAutoRenewRun
}

func (s *autoRenewHandlerStore) GetCommercialAutoRenewConfig(uid int64) (*types.CommercialAutoRenewConfig, error) {
	return s.config, nil
}

func (s *autoRenewHandlerStore) SetCommercialAutoRenewConfig(uid int64, enabled bool, note string) (*types.CommercialAutoRenewConfig, error) {
	s.config = &types.CommercialAutoRenewConfig{UID: uid, Enabled: enabled, Note: note}
	return s.config, nil
}

func (s *autoRenewHandlerStore) ListDueCommercialAutoRenew(now time.Time, within time.Duration) ([]*types.CommercialAutoRenewDue, error) {
	return nil, nil
}

func (s *autoRenewHandlerStore) RecordCommercialAutoRenewRun(run *types.CommercialAutoRenewRun) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *autoRenewHandlerStore) ListCommercialAutoRenewRuns(uid int64, limit int) ([]*types.CommercialAutoRenewRun, error) {
	return s.runs, nil
}

func newAutoRenewRequest(method, target, body string) *http.Request {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	request.RemoteAddr = "127.0.0.1:22345"
	return request
}

func TestCommercialAutoRenewHandlerTogglesConfig(t *testing.T) {
	store := &autoRenewHandlerStore{commercialTestStore: newCommercialTestStore()}
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)

	recorder := httptest.NewRecorder()
	handler.HandleCommercialAutoRenew(recorder, newAutoRenewRequest(http.MethodGet, "/local/account-admin/commercial/auto-renew?uid=38", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var getBody struct {
		Config *types.CommercialAutoRenewConfig `json:"config"`
		Runs   []*types.CommercialAutoRenewRun  `json:"runs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &getBody); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getBody.Config == nil || getBody.Config.UID != 38 || getBody.Config.Enabled {
		t.Fatalf("default config: %#v", getBody.Config)
	}

	recorder = httptest.NewRecorder()
	handler.HandleCommercialAutoRenew(recorder, newAutoRenewRequest(http.MethodPost, "/local/account-admin/commercial/auto-renew", `{"uid":38,"enabled":true,"note":"内部车队"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.config == nil || !store.config.Enabled || store.config.Note != "内部车队" {
		t.Fatalf("config not saved: %#v", store.config)
	}

	recorder = httptest.NewRecorder()
	handler.HandleCommercialAutoRenew(recorder, newAutoRenewRequest(http.MethodPost, "/local/account-admin/commercial/auto-renew", `{"uid":0}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing uid must be rejected: %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.HandleCommercialAutoRenew(recorder, newAutoRenewRequest(http.MethodPost, "/local/account-admin/commercial/auto-renew", `{"uid":38,"note":"`+strings.Repeat("x", 257)+`"}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("overlong note must be rejected: %d", recorder.Code)
	}
}

type autoRenewExtendStore struct {
	CommercialStore
	summary *types.CommercialSummary
	plans   []*types.CommercialPlan
	applied *types.CommercialAccountAdjustment
	result  *types.CommercialAccountAdjustmentResult
}

func (s *autoRenewExtendStore) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return s.summary, nil
}

func (s *autoRenewExtendStore) ListCommercialPlans(bool) ([]*types.CommercialPlan, error) {
	return s.plans, nil
}

func (s *autoRenewExtendStore) ApplyCommercialAccountAdjustment(adjustment *types.CommercialAccountAdjustment) (*types.CommercialAccountAdjustmentResult, error) {
	s.applied = adjustment
	return s.result, nil
}

func (s *autoRenewExtendStore) RecordCommercialCycleReset(uid int64, operationID, note string, effectiveAt time.Time) (bool, time.Time, error) {
	return true, effectiveAt, nil
}

func TestCommercialAutoRenewExtendAppliesDeterministicExtension(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(5 * 24 * time.Hour)
	newExpiry := expiry.AddDate(0, 0, 30)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", ModelBudgets: map[string]float64{"gpt-5.6-terra": 500}, DurationDays: 30}
	store := &autoRenewExtendStore{
		summary: &types.CommercialSummary{
			UID: 38, TotalCNY: 600,
			Entitlements: []*types.CommercialEntitlement{{UID: 38, PlanID: 7, PlanSlug: "catsco-personal", Source: "operator", State: "active", StartsAt: now.Add(-25 * 24 * time.Hour), ExpiresAt: &expiry}},
		},
		plans:  []*types.CommercialPlan{personal},
		result: &types.CommercialAccountAdjustmentResult{Action: commercialAdjustmentExtend, Applied: true, ExpiresAt: &newExpiry},
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": true,
			"limits": map[string]interface{}{
				"monthly_budget": map[string]interface{}{"max_limit": 600, "current_usage": 10, "reset_duration": "1M"},
				"model_limits":   []interface{}{},
			},
		})
	}))
	defer relay.Close()
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)
	handler.SetCommercialRelayAdmin(&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}, true)

	result, err := handler.ApplyCommercialAutoRenewExtend(context.Background(), 38, expiry, "auto-renew-38-1", "内部自动续费")
	if err != nil {
		t.Fatalf("auto renew extend failed: %v", err)
	}
	if store.applied == nil || store.applied.Action != commercialAdjustmentExtend || store.applied.OperationID != "auto-renew-38-1" {
		t.Fatalf("adjustment not applied with deterministic id: %#v", store.applied)
	}
	if store.applied.ExpectedExpiresAt == nil || !store.applied.ExpectedExpiresAt.Equal(expiry) {
		t.Fatalf("expected expiry guard missing: %#v", store.applied.ExpectedExpiresAt)
	}
	if result == nil || result.ExpiresAt == nil || !result.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("result expiry: %#v want %v", result, newExpiry)
	}

	if _, err := handler.ApplyCommercialAutoRenewExtend(context.Background(), 38, expiry.Add(time.Hour), "auto-renew-38-2", "内部自动续费"); err == nil {
		t.Fatal("a changed package expiry must abort the renewal instead of double extending")
	}
}

func TestCommercialAutoRenewExtendRequiresRelayEnforcement(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(5 * 24 * time.Hour)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", ModelBudgets: map[string]float64{"gpt-5.6-terra": 500}, DurationDays: 30}
	store := &autoRenewExtendStore{
		summary: &types.CommercialSummary{
			UID: 38, TotalCNY: 600,
			Entitlements: []*types.CommercialEntitlement{{UID: 38, PlanID: 7, PlanSlug: "catsco-personal", Source: "operator", State: "active", StartsAt: now.Add(-25 * 24 * time.Hour), ExpiresAt: &expiry}},
		},
		plans:  []*types.CommercialPlan{personal},
		result: &types.CommercialAccountAdjustmentResult{Action: commercialAdjustmentExtend, Applied: true},
	}
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)
	if _, err := handler.ApplyCommercialAutoRenewExtend(context.Background(), 38, expiry, "auto-renew-38-9", "内部自动续费"); err == nil {
		t.Fatal("renewal without relay enforcement must not apply")
	}
	if store.applied != nil {
		t.Fatalf("ledger must stay untouched: %#v", store.applied)
	}
}

func TestCommercialAutoRenewHandlerRejectsForeignSources(t *testing.T) {
	store := &autoRenewHandlerStore{commercialTestStore: newCommercialTestStore()}
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)

	foreign := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/auto-renew?uid=38", nil)
	foreign.RemoteAddr = "203.0.113.9:4444"
	recorder := httptest.NewRecorder()
	handler.HandleCommercialAutoRenew(recorder, foreign)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign source must be rejected: %d", recorder.Code)
	}

	method := httptest.NewRequest(http.MethodDelete, "/local/account-admin/commercial/auto-renew?uid=38", nil)
	method.RemoteAddr = "127.0.0.1:22345"
	recorder = httptest.NewRecorder()
	handler.HandleCommercialAutoRenew(recorder, method)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method must be rejected: %d", recorder.Code)
	}
}
