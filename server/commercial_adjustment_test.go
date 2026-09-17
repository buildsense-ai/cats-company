package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialAdjustmentPreviewStore struct {
	*commercialTestStore
	summary *types.CommercialSummary
}

func (s *commercialAdjustmentPreviewStore) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return s.summary, nil
}

func TestCommercialAdjustmentPreviewProtectsUsedSharedQuota(t *testing.T) {
	now := time.Now().UTC()
	expiresAt := now.Add(20 * 24 * time.Hour)
	currentPlan := &types.CommercialPlan{ID: 1, Slug: "current", Name: "当前套餐", ModelBudgets: map[string]float64{"gpt-5.6-terra": 80}, DurationDays: 30}
	targetPlan := &types.CommercialPlan{ID: 2, Slug: "target", Name: "目标套餐", ModelBudgets: map[string]float64{"gpt-5.6-terra": 200}, DurationDays: 30}
	store := &commercialAdjustmentPreviewStore{
		commercialTestStore: newCommercialTestStore(),
		summary: &types.CommercialSummary{
			UID: 38, Plans: []*types.CommercialPlan{currentPlan, targetPlan}, TotalCNY: 100,
			Entitlements: []*types.CommercialEntitlement{{UID: 38, PlanID: 1, State: "active", StartsAt: now.Add(-24 * time.Hour), ExpiresAt: &expiresAt}},
			Grants: []*types.CommercialQuotaGrant{
				{UID: 38, PlanID: 1, GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 80},
				{UID: 38, GrantType: "manual", Model: "gpt-5.6-terra", AmountCNY: 20},
			},
			TotalsByModel: map[string]float64{"gpt-5.6-terra": 100},
		},
	}
	store.plans = []*types.CommercialPlan{currentPlan, targetPlan}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/38/key/limits" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": true,
			"limits": map[string]interface{}{
				"monthly_budget": map[string]interface{}{"max_limit": 100, "current_usage": 70, "reset_duration": "1M"},
				"model_limits":   []interface{}{},
			},
		})
	}))
	defer relay.Close()
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)
	handler.SetCommercialRelayAdmin(&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}, true)

	decrease, err := handler.buildCommercialAdjustmentPreview(context.Background(), store, &commercialAdjustmentRequest{
		UID: 38, Action: commercialAdjustmentDecrease, AmountCNY: 40, Preview: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decrease.NextTotalCNY != 60 || decrease.RelayUsageCNY != 70 || decrease.CanApply {
		t.Fatalf("unsafe decrease was allowed: %#v", decrease)
	}

	change, err := handler.buildCommercialAdjustmentPreview(context.Background(), store, &commercialAdjustmentRequest{
		UID: 38, Action: commercialAdjustmentChangePlan, PlanID: 2, Preview: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if change.NextTotalCNY != 220 || !change.CanApply || change.TargetPlan == nil || change.TargetPlan.ID != 2 {
		t.Fatalf("plan preview did not preserve manual quota: %#v", change)
	}
	if !change.UsageWillReset || change.NextRemainingCNY != 220 {
		t.Fatalf("plan preview did not account for the cycle reset: %#v", change)
	}

	reset, err := handler.buildCommercialAdjustmentPreview(context.Background(), store, &commercialAdjustmentRequest{
		UID: 38, Action: commercialAdjustmentResetCycle, Preview: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reset.UsageWillReset || reset.RelayUsageCNY != 70 || reset.NextRemainingCNY != 100 {
		t.Fatalf("cycle reset preview did not show the post-reset balance: %#v", reset)
	}
}

func TestCommercialAdjustmentExtendPreviewAppendsOnePeriod(t *testing.T) {
	now := time.Now().UTC()
	currentExpiry := now.Add(10 * 24 * time.Hour)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", ModelBudgets: map[string]float64{"gpt-5.6-terra": 500}, DurationDays: 30}
	s := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: &types.CommercialSummary{
		UID: 38, TotalCNY: 600,
		Entitlements: []*types.CommercialEntitlement{{UID: 38, PlanID: 7, PlanSlug: "catsco-personal", Source: "order", State: "active", StartsAt: now.Add(-20 * 24 * time.Hour), ExpiresAt: &currentExpiry}},
	}}
	s.plans = []*types.CommercialPlan{personal}
	h := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	preview, err := h.buildCommercialAdjustmentPreview(context.Background(), s, &commercialAdjustmentRequest{UID: 38, Action: commercialAdjustmentExtend, Preview: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := currentExpiry.AddDate(0, 0, 30)
	if preview.ExpiresAt == nil || !preview.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("extension expiry mismatch: %#v want %v", preview.ExpiresAt, wantExpiry)
	}
	if preview.PreviousExpiresAt == nil || !preview.PreviousExpiresAt.Equal(currentExpiry) {
		t.Fatalf("previous expiry missing: %#v", preview.PreviousExpiresAt)
	}
	if preview.NextTotalCNY != 1100 || preview.UsageWillReset || preview.CurrentPlan == nil || preview.CurrentPlan.ID != 7 {
		t.Fatalf("extension preview: %#v", preview)
	}
	reset, err := h.buildCommercialAdjustmentPreview(context.Background(), s, &commercialAdjustmentRequest{UID: 38, Action: commercialAdjustmentExtend, ResetCycle: true, Preview: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reset.UsageWillReset {
		t.Fatalf("combined reset preview did not flag the usage reset: %#v", reset)
	}
}

func TestCommercialAdjustmentExtendPreviewRejectsExpiredAndUnsupportedPlans(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", ModelBudgets: map[string]float64{"gpt-5.6-terra": 500}, DurationDays: 30}
	free := &types.CommercialPlan{ID: 1, Slug: "catsco-free", Name: "Free"}
	s := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: &types.CommercialSummary{UID: 38, TotalCNY: 100, Entitlements: []*types.CommercialEntitlement{
		{UID: 38, PlanID: 7, PlanSlug: "catsco-personal", Source: "order", State: "active", StartsAt: now.Add(-40 * 24 * time.Hour), ExpiresAt: &expired},
	}}}
	s.plans = []*types.CommercialPlan{free, personal}
	h := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	_, err := h.buildCommercialAdjustmentPreview(context.Background(), s, &commercialAdjustmentRequest{UID: 38, Action: commercialAdjustmentExtend, Preview: true}, now)
	var adjustmentErr *types.CommercialAdjustmentError
	if !errors.As(err, &adjustmentErr) || adjustmentErr.Code != "no_active_plan" {
		t.Fatalf("expired package should be rejected: %v", err)
	}

	freeSummary := &types.CommercialSummary{UID: 39, TotalCNY: 100, Entitlements: []*types.CommercialEntitlement{
		{UID: 39, PlanID: 1, PlanSlug: "catsco-free", Source: "free", State: "active", StartsAt: now.Add(-24 * time.Hour)},
	}}
	s2 := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: freeSummary}
	s2.plans = []*types.CommercialPlan{free, personal}
	h2 := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s2)
	_, err = h2.buildCommercialAdjustmentPreview(context.Background(), s2, &commercialAdjustmentRequest{UID: 39, Action: commercialAdjustmentExtend, Preview: true}, now)
	if !errors.As(err, &adjustmentErr) || adjustmentErr.Code != "no_active_plan" {
		t.Fatalf("free package should be rejected: %v", err)
	}
}

func TestCommercialAdjustmentExtendPreviewChainsOnNewestSegment(t *testing.T) {
	now := time.Now().UTC()
	currentExpiry := now.Add(10 * 24 * time.Hour)
	extensionExpiry := currentExpiry.AddDate(0, 0, 30)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", ModelBudgets: map[string]float64{"gpt-5.6-terra": 500}, DurationDays: 30}
	s := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: &types.CommercialSummary{
		UID: 40, TotalCNY: 600,
		Entitlements: []*types.CommercialEntitlement{
			{UID: 40, PlanID: 7, PlanSlug: "catsco-personal", Source: "order", State: "active", StartsAt: now.Add(-20 * 24 * time.Hour), ExpiresAt: &currentExpiry},
			{UID: 40, PlanID: 7, PlanSlug: "catsco-personal", Source: "operator", State: "active", StartsAt: currentExpiry, ExpiresAt: &extensionExpiry},
		},
	}}
	s.plans = []*types.CommercialPlan{personal}
	h := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	preview, err := h.buildCommercialAdjustmentPreview(context.Background(), s, &commercialAdjustmentRequest{UID: 40, Action: commercialAdjustmentExtend, Preview: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.PreviousExpiresAt == nil || !preview.PreviousExpiresAt.Equal(extensionExpiry) {
		t.Fatalf("preview did not chain on the newest segment: %#v", preview.PreviousExpiresAt)
	}
	if preview.ExpiresAt == nil || !preview.ExpiresAt.Equal(extensionExpiry.AddDate(0, 0, 30)) {
		t.Fatalf("chained expiry mismatch: %#v", preview.ExpiresAt)
	}
}

func TestCommercialAdjustmentExtendPreviewRejectsZeroDurationPlan(t *testing.T) {
	now := time.Now().UTC()
	currentExpiry := now.Add(10 * 24 * time.Hour)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", DurationDays: -1}
	s := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: &types.CommercialSummary{
		UID: 41, TotalCNY: 100,
		Entitlements: []*types.CommercialEntitlement{{UID: 41, PlanID: 7, PlanSlug: "catsco-personal", Source: "operator", State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &currentExpiry}},
	}}
	s.plans = []*types.CommercialPlan{personal}
	h := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	_, err := h.buildCommercialAdjustmentPreview(context.Background(), s, &commercialAdjustmentRequest{UID: 41, Action: commercialAdjustmentExtend, Preview: true}, now)
	var adjustmentErr *types.CommercialAdjustmentError
	if !errors.As(err, &adjustmentErr) || adjustmentErr.Code != "unsupported_plan" {
		t.Fatalf("zero-duration plan should be rejected: %v", err)
	}
}

func TestCommercialAdjustmentExtendApplyRequiresExpectedExpiry(t *testing.T) {
	now := time.Now().UTC()
	currentExpiry := now.Add(10 * 24 * time.Hour)
	personal := &types.CommercialPlan{ID: 7, Slug: "catsco-personal", Name: "个人版", ModelBudgets: map[string]float64{"gpt-5.6-terra": 500}, DurationDays: 30}
	s := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: &types.CommercialSummary{
		UID: 42, TotalCNY: 600,
		Entitlements: []*types.CommercialEntitlement{{UID: 42, PlanID: 7, PlanSlug: "catsco-personal", Source: "order", State: "active", StartsAt: now.Add(-20 * 24 * time.Hour), ExpiresAt: &currentExpiry}},
	}}
	s.plans = []*types.CommercialPlan{personal}
	h := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	body, _ := json.Marshal(map[string]interface{}{
		"uid": 42, "action": "extend", "note": "合同续费", "operation_id": "extend-op-test", "expected_total_cny": 600.0,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/account-admin/commercial/adjustments", bytes.NewReader(body))
	req = withCommercialOpsService(req, AccountService{Slug: "test-ops", Scopes: []string{"commercial.ops.write"}})
	rec := httptest.NewRecorder()
	h.HandleCommercialAdjustment(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "expected_expiry is required") {
		t.Fatalf("extend apply without expected_expiry: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCommercialAdjustmentRequestRequiresPreviewVersion(t *testing.T) {
	req := &commercialAdjustmentRequest{UID: 38, Action: commercialAdjustmentIncrease, AmountCNY: 10, Note: "合同增购", OperationID: "op-1"}
	if err := normalizeCommercialAdjustmentRequest(req); err != nil {
		t.Fatal(err)
	}
	if req.Action != commercialAdjustmentIncrease {
		t.Fatalf("unexpected action: %s", req.Action)
	}
}

func TestCommercialAdjustmentPreviewPrefersPackageOverFreeAndSupportsPermanent(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	free := &types.CommercialPlan{ID: 1, Slug: "catsco-free", Name: "Free"}
	paid := &types.CommercialPlan{ID: 2, Slug: "catsco-pro", Name: "Pro"}
	permanent := &types.CommercialPlan{ID: 3, Slug: "internal-permanent", DurationDays: -1, ModelBudgets: map[string]float64{"gpt-5.6-sol": 50000}}
	s := &commercialAdjustmentPreviewStore{commercialTestStore: newCommercialTestStore(), summary: &types.CommercialSummary{UID: 826, TotalCNY: 100, Entitlements: []*types.CommercialEntitlement{
		{PlanID: 1, Source: "free", State: "active", StartsAt: now.Add(-time.Minute)},
		{PlanID: 2, Source: "operator", State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &expiry},
	}}}
	s.plans = []*types.CommercialPlan{free, paid, permanent}
	h := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	preview, err := h.buildCommercialAdjustmentPreview(context.Background(), s, &commercialAdjustmentRequest{UID: 826, Action: commercialAdjustmentChangePlan, PlanID: 3, Preview: true}, now)
	if err != nil || preview.CurrentPlan == nil || preview.CurrentPlan.ID != paid.ID || preview.ExpiresAt != nil || preview.NextTotalCNY != 50000 {
		t.Fatalf("primary/permanent preview: %+v %v", preview, err)
	}
}

func TestCommercialAdjustmentDoesNotReplayOlderRelayCycle(t *testing.T) {
	currentAt := time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC)
	requestedAt := currentAt.Add(-time.Hour)
	posts := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/38/key/limits" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			posts++
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"configured":         true,
			"usage_window_start": currentAt.Format(time.RFC3339Nano),
			"limits":             map[string]interface{}{},
		})
	}))
	defer relay.Close()
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, newCommercialTestStore())
	handler.SetCommercialRelayAdmin(&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}, true)

	if err := handler.resetCommercialRelayCycle(context.Background(), 38, requestedAt); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("older idempotent retry reset Relay usage %d times", posts)
	}
}

func TestCommercialAdjustmentAcceptsRelaySecondPrecisionReadback(t *testing.T) {
	requestedAt := time.Date(2026, 8, 21, 8, 14, 58, 859123000, time.UTC)
	usageWindowStart := ""
	posts := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/38/key/limits" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			posts++
			var payload struct {
				UsageWindowStart string `json:"usage_window_start"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			parsed, err := time.Parse(time.RFC3339Nano, payload.UsageWindowStart)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			usageWindowStart = parsed.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"configured":         true,
			"usage_window_start": usageWindowStart,
			"limits":             map[string]interface{}{},
		})
	}))
	defer relay.Close()
	handler := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, newCommercialTestStore())
	handler.SetCommercialRelayAdmin(&RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}, true)

	if err := handler.resetCommercialRelayCycle(context.Background(), 38, requestedAt); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("Relay cycle reset posts=%d, want 1", posts)
	}
	if usageWindowStart != "2026-08-21T08:14:58Z" {
		t.Fatalf("Relay did not normalize the cycle to second precision: %q", usageWindowStart)
	}
	if sameCommercialRelayTimestamp(usageWindowStart, requestedAt.Add(time.Second).Format(time.RFC3339Nano)) {
		t.Fatal("timestamps in different seconds were accepted")
	}
}
