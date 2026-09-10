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

func legacyGrantFixture(now time.Time) (*commercialTestStore, commercialRelayUsageUser) {
	store := newCommercialTestStore()
	store.plans = []*types.CommercialPlan{{ID: 1, Slug: "catsco-legacy-custom", Name: "内部保留套餐"}}
	store.entitlements[61] = []*types.CommercialEntitlement{{ID: 1, UID: 61, PlanID: 1, PlanSlug: "catsco-legacy-custom", Source: "legacy", State: "active", StartsAt: now.Add(-time.Hour)}}
	store.grants[61] = []*types.CommercialQuotaGrant{{UID: 61, PlanID: 1, GrantType: "legacy", Model: "MiniMax-M3", AmountCNY: 1000, EffectiveAt: now.Add(-time.Hour)}}
	limits := []commercialRelayModelLimit{
		{Model: "MiniMax-M3", Provider: "minimax", AllowedModels: []string{"MiniMax-M3"}},
		{Model: "glm-5.3-flash", Provider: "glm-openai", AllowedModels: []string{"glm-5.3-flash"}},
		{Model: "glm-5.3-flash", Provider: "glm-anthropic", AllowedModels: []string{"glm-5.3-flash"}},
		{Model: "gpt-5.6-sol", Provider: "gpt", AllowedModels: []string{"gpt-5.6-sol"}},
	}
	return store, commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{AvailableModelLimits: limits}}
}

func TestCommercialGrantOptionsLegacyCatalogAndPaidRestrictions(t *testing.T) {
	now := time.Now().UTC()
	store, relay := legacyGrantFixture(now)
	summary, _ := store.GetCommercialSummary(61)
	relay.Limits.AvailableModelLimits = append(relay.Limits.AvailableModelLimits,
		commercialRelayModelLimit{Model: "gpt-5.6-luna", Provider: "gpt", AllowedModels: []string{"gpt-5.6-luna"}},
		commercialRelayModelLimit{Model: "broken-route"})
	options := commercialGrantModels(summary, &relay, now)
	if len(options) != 3 {
		t.Fatalf("expected three unique usable models: %+v", options)
	}
	if options[2].ID != "gpt-5.6-sol" {
		t.Fatalf("internal Sol unavailable: %+v", options)
	}
	relay.Limits.ModelLimits = relay.Limits.AvailableModelLimits
	relay.Limits.AvailableModelLimits = nil
	if len(commercialGrantModels(summary, &relay, now)) != 0 {
		t.Fatal("historical models must not become grant choices")
	}
	_, relay = legacyGrantFixture(now)
	expiry := now.Add(24 * time.Hour)
	summary.Entitlements = append(summary.Entitlements, &types.CommercialEntitlement{ID: 2, PlanID: 2, PlanSlug: "catsco-pro", Source: "invite", State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &expiry})
	summary.Plans = append(summary.Plans, &types.CommercialPlan{ID: 2, ModelBudgets: map[string]float64{"MiniMax-M3": 100}})
	options = commercialGrantModels(summary, &relay, now)
	if commercialLegacyGrantEligible(summary, now) || len(options) != 1 || options[0].ID != "MiniMax-M3" || !options[0].ExpiresAt.Equal(expiry) {
		t.Fatalf("legacy baseline bypassed paid restrictions: %+v", options)
	}
	for _, state := range []string{"revoked", "expired"} {
		summary.Entitlements[0].State = state
		summary.Entitlements = summary.Entitlements[:1]
		if commercialLegacyGrantEligible(summary, now) {
			t.Fatalf("accepted %s legacy", state)
		}
	}
}

func TestCommercialLegacyGrantHandlerAddsSharedQuotaAndRejectsStaleCatalog(t *testing.T) {
	now := time.Now().UTC()
	store, relayState := legacyGrantFixture(now)
	unavailable := false
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(relayState)
	}))
	defer relay.Close()
	h := NewAccountAdminHandler(nil, nil, nil, store)
	h.relayAdmin = &RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}
	h.commercialRelaySyncer = NewCommercialRelaySyncer(nil, h.relayAdmin, CommercialRelaySyncerOptions{EnforceEnabled: true})
	get := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/users?uid=61&grant_options=1", nil)
	get.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	h.HandleCommercialUserSummary(rec, get)
	var payload struct {
		Options commercialGrantOptions `json:"grant_options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || !payload.Options.SharedPool || !payload.Options.RequiresExpiry || len(payload.Options.Models) != 3 {
		t.Fatalf("invalid options: %s", rec.Body.String())
	}
	post := func(model, expiry string) int {
		body, _ := json.Marshal(map[string]interface{}{"uid": 61, "model": model, "amount_cny": 200, "expires_at": expiry, "note": "internal model test"})
		req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/grants", strings.NewReader(string(body)))
		req.RemoteAddr = "127.0.0.1:40200"
		r := httptest.NewRecorder()
		h.HandleCommercialGrant(r, req)
		return r.Code
	}
	expiry := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	for _, invalid := range []struct{ model, expiry string }{{"invented", expiry}, {"gpt-5.6-luna", expiry}, {"glm-5.3-flash", ""}, {"glm-5.3-flash", now.Add(-time.Hour).Format(time.RFC3339)}} {
		if post(invalid.model, invalid.expiry) != 400 {
			t.Fatalf("accepted invalid grant %+v", invalid)
		}
	}
	if post("GLM-5.3-FLASH", expiry) != 200 {
		t.Fatal("legacy GLM grant failed")
	}
	summary, _ := store.GetCommercialSummary(61)
	if summary.TotalCNY != 1200 || summary.TotalsByModel["glm-5.3-flash"] != 200 || len(summary.Entitlements) != 1 || summary.Entitlements[0].ExpiresAt != nil {
		t.Fatalf("grant changed existing package: %+v", summary)
	}
	updates, _ := commercialRelaySharedManagedPlan(61, summary, &relayState, nil)
	if len(updates) != 3 {
		t.Fatalf("missing old or new model routes: %+v", updates)
	}
	for _, update := range updates {
		if update.MaxLimit != 1200 {
			t.Fatalf("model does not share the increased pool: %+v", update)
		}
	}
	// A choice loaded earlier cannot authorize a route after it is removed.
	relayState.Limits.AvailableModelLimits = relayState.Limits.AvailableModelLimits[:1]
	if post("glm-5.3-flash", expiry) != 400 {
		t.Fatal("stale catalog accepted")
	}
	unavailable = true
	if post("MiniMax-M3", expiry) != 400 {
		t.Fatal("catalog outage accepted")
	}
	if len(store.grants[61]) != 2 {
		t.Fatal("rejected requests wrote grants")
	}
}
