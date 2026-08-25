package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestCommercialAdjustmentRequestRequiresPreviewVersion(t *testing.T) {
	req := &commercialAdjustmentRequest{UID: 38, Action: commercialAdjustmentIncrease, AmountCNY: 10, Note: "合同增购", OperationID: "op-1"}
	if err := normalizeCommercialAdjustmentRequest(req); err != nil {
		t.Fatal(err)
	}
	if req.Action != commercialAdjustmentIncrease {
		t.Fatalf("unexpected action: %s", req.Action)
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
