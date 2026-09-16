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
}

func TestCommercialImageGrantHandlerExtendsFreePackage(t *testing.T) {
	now := time.Now().UTC()
	store := imageGrantFreeFixture(now)
	handler := NewAccountAdminHandler(nil, nil, nil, store)
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
}
