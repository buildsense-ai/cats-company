package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func imageGrantFreeFixture(now time.Time) *commercialTestStore {
	store := newCommercialTestStore()
	store.plans = []*types.CommercialPlan{{ID: 1, Slug: "catsco-free", Name: "Free", ModelBudgets: map[string]float64{"MiniMax-M2.7": 1000}}}
	store.entitlements[61] = []*types.CommercialEntitlement{{
		ID: 1, UID: 61, PlanID: 1, PlanSlug: "catsco-free", Source: "free",
		State: "active", StartsAt: now.Add(-time.Hour),
	}}
	store.grants[61] = []*types.CommercialQuotaGrant{{
		UID: 61, PlanID: 1, GrantType: "free", Model: "MiniMax-M2.7",
		AmountCNY: 1000, EffectiveAt: now.Add(-time.Hour),
	}}
	return store
}

func TestCommercialSummaryHasActiveEntitlementRespectsDates(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	cases := []struct {
		name        string
		entitlement *types.CommercialEntitlement
		want        bool
	}{
		{"no expiry", &types.CommercialEntitlement{State: "active", StartsAt: past}, true},
		{"current", &types.CommercialEntitlement{State: "active", StartsAt: past, ExpiresAt: &future}, true},
		{"expired", &types.CommercialEntitlement{State: "active", StartsAt: now.Add(-2 * time.Hour), ExpiresAt: &past}, false},
		{"future start", &types.CommercialEntitlement{State: "active", StartsAt: future, ExpiresAt: &future}, false},
		{"revoked", &types.CommercialEntitlement{State: "revoked", StartsAt: past}, false},
	}
	for _, tc := range cases {
		summary := &types.CommercialSummary{Entitlements: []*types.CommercialEntitlement{tc.entitlement}}
		if got := commercialSummaryHasActiveEntitlement(summary, now); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if commercialSummaryHasActiveEntitlement(nil, now) {
		t.Fatal("nil summary must not qualify")
	}
	if commercialSummaryHasActiveEntitlement(&types.CommercialSummary{Entitlements: []*types.CommercialEntitlement{nil}}, now) {
		t.Fatal("nil entitlement must not qualify")
	}
}

func TestResolveCommercialBonusGrantImageLaneOutsidePackage(t *testing.T) {
	now := time.Now().UTC()
	store := imageGrantFreeFixture(now)
	summary, err := store.GetCommercialSummary(61)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveCommercialBonusGrant(summary, "gpt-image-2.5", "", now); err == nil {
		t.Fatal("image grant without an explicit expiry was accepted")
	}
	expiresAt := now.Add(30 * 24 * time.Hour).Truncate(time.Second)
	model, resolved, err := resolveCommercialBonusGrant(summary, "gpt-image-2.5", expiresAt.Format(time.RFC3339), now)
	if err != nil || model != "gpt-image-2.5" || !resolved.Equal(expiresAt) {
		t.Fatalf("image add-on rejected: %v %q %v", err, model, resolved)
	}
	// Chat models stay tied to the package's model set.
	if _, _, err := resolveCommercialBonusGrant(summary, "gpt-5.6-terra", expiresAt.Format(time.RFC3339), now); err == nil {
		t.Fatal("chat model outside the package was accepted")
	}
}

func TestResolveCommercialBonusGrantImageLaneWithExpiringPackage(t *testing.T) {
	now := time.Now().UTC()
	store := newCommercialTestStore()
	store.plans = []*types.CommercialPlan{{ID: 1, Slug: "internal-image-test", Name: "Internal", ModelBudgets: map[string]float64{"MiniMax-M2.7": 100}}}
	packageExpiry := now.Add(30 * 24 * time.Hour).Truncate(time.Second)
	store.entitlements[62] = []*types.CommercialEntitlement{{
		ID: 1, UID: 62, PlanID: 1, PlanSlug: "internal-image-test", Source: "operator_plan",
		State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &packageExpiry,
	}}
	store.grants[62] = []*types.CommercialQuotaGrant{{
		UID: 62, PlanID: 1, GrantType: "operator_plan", Model: "MiniMax-M2.7",
		AmountCNY: 100, EffectiveAt: now.Add(-time.Hour),
	}}
	summary, err := store.GetCommercialSummary(62)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveCommercialBonusGrant(summary, "gpt-image-2.5", "", now); err == nil {
		t.Fatal("image grant without an explicit expiry was accepted")
	}
	expiresAt := now.Add(10 * 24 * time.Hour).Truncate(time.Second)
	model, resolved, err := resolveCommercialBonusGrant(summary, "gpt-image-2.5", expiresAt.Format(time.RFC3339), now)
	if err != nil || model != "gpt-image-2.5" || !resolved.Equal(expiresAt) {
		t.Fatalf("image add-on outside an expiring package was rejected: %v %q %v", err, model, resolved)
	}
	// The operator decides how long the add-on lives: it may outlive the
	// package, which is why the expiry must be explicit.
	beyondPackage := packageExpiry.Add(24 * time.Hour)
	if _, resolved, err = resolveCommercialBonusGrant(summary, "gpt-image-2.5", beyondPackage.Format(time.RFC3339), now); err != nil || !resolved.Equal(beyondPackage) {
		t.Fatalf("uncapped image add-on expiry was rejected: %v %v", err, resolved)
	}
}

func TestResolveCommercialBonusGrantImageLaneRequiresActiveEntitlement(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	empty, err := newCommercialTestStore().GetCommercialSummary(63)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveCommercialBonusGrant(empty, "gpt-image-2.5", expiry, now); err == nil || !strings.Contains(err.Error(), "active package") {
		t.Fatalf("image grant without an entitlement was accepted: %v", err)
	}
	past := now.Add(-time.Hour)
	expired := &types.CommercialSummary{Entitlements: []*types.CommercialEntitlement{{
		State: "active", StartsAt: now.Add(-2 * time.Hour), ExpiresAt: &past,
	}}}
	if _, _, err := resolveCommercialBonusGrant(expired, "gpt-image-2.5", expiry, now); err == nil || !strings.Contains(err.Error(), "active package") {
		t.Fatalf("expired entitlement qualified for an image add-on: %v", err)
	}
}

func TestCommercialGrantOptionsOfferImageLaneForFreePackage(t *testing.T) {
	now := time.Now().UTC()
	store := imageGrantFreeFixture(now)
	summary, err := store.GetCommercialSummary(61)
	if err != nil {
		t.Fatal(err)
	}
	relay := commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{AvailableModelLimits: []commercialRelayModelLimit{
		{Model: "MiniMax-M2.7", Provider: "minimax", AllowedModels: []string{"MiniMax-M2.7"}},
		{Model: "gpt-image-2.5", Provider: "pptoken-image-search", AllowedModels: []string{"gpt-image-2.5", "gpt-image-2"}},
		{Model: "gpt-image-2", Provider: "pptoken-image-search", AllowedModels: []string{"gpt-image-2.5", "gpt-image-2"}},
	}}}
	options := commercialGrantModels(summary, &relay, now)
	if len(options) != 2 || options[0].ID != "gpt-image-2" || options[1].ID != "gpt-image-2.5" {
		t.Fatalf("image add-ons missing from grant choices: %+v", options)
	}
	if options[0].ExpiresAt != nil || options[1].ExpiresAt != nil {
		t.Fatalf("image add-ons must not borrow a package expiry: %+v", options)
	}
	if !options[0].RequiresExpiry || !options[1].RequiresExpiry {
		t.Fatalf("image add-ons must ask for an explicit expiry: %+v", options)
	}
}

func TestCommercialImageGrantHandlerExtendsFreePackage(t *testing.T) {
	now := time.Now().UTC()
	store := imageGrantFreeFixture(now)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": true,
			"limits": map[string]interface{}{
				"available_model_limits": []map[string]interface{}{
					{"model": "gpt-image-2.5", "provider": "pptoken-image-search", "allowed_models": []string{"gpt-image-2.5"}},
				},
			},
		})
	}))
	defer relay.Close()
	handler := NewAccountAdminHandler(nil, nil, nil, store)
	handler.relayAdmin = &RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}
	post := func(model, expiry string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{"uid": 61, "model": model, "amount_cny": 50, "expires_at": expiry, "note": "image add-on test"})
		req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/grants", strings.NewReader(string(body)))
		req.RemoteAddr = "127.0.0.1:40200"
		rec := httptest.NewRecorder()
		handler.HandleCommercialGrant(rec, req)
		return rec
	}
	if rec := post("gpt-image-2.5", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("image grant without expiry accepted: %d %s", rec.Code, rec.Body.String())
	}
	expiry := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	if rec := post("gpt-image-9", expiry); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "目录") {
		t.Fatalf("uncatalogued image model accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("gpt-image-2.5", expiry); rec.Code != http.StatusOK {
		t.Fatalf("image grant rejected: %d %s", rec.Code, rec.Body.String())
	}
	summary, err := store.GetCommercialSummary(61)
	if err != nil || summary.TotalsByModel["gpt-image-2.5"] != 50 {
		t.Fatalf("image add-on missing from summary: %v %+v", err, summary.TotalsByModel)
	}
	if rec := post("gpt-5.6-terra", expiry); rec.Code != http.StatusBadRequest {
		t.Fatalf("chat model outside the package accepted: %d %s", rec.Code, rec.Body.String())
	}
	relay.Close()
	if rec := post("gpt-image-2.5", expiry); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "目录") {
		t.Fatalf("image grant survived a catalog outage: %d %s", rec.Code, rec.Body.String())
	}
}
