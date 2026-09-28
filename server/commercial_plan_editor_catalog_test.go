package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// TestCommercialPlansListingCarriesTheModelCatalog pins the contract the
// console's plan editor depends on. The editor renders one checkbox per
// sellable model, and it used to derive that list from a hardcoded array inside
// the page: a model onboarded on the relay only became selectable when the
// console was redeployed, and the reconcile could add the same model to a plan
// the operator could not edit. Shipping the catalog with the plan list keeps the
// editor, the save-time validator and the startup reconcile on one source.
func TestCommercialPlansListingCarriesTheModelCatalog(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)
	handler.SetCommercialModelCatalog(newStubCommercialModelCatalog(t, catalogTestModels))

	req := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/plans", nil)
	req.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	handler.HandleCommercialPlans(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plans status=%d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Plans        []map[string]interface{} `json:"plans"`
		ModelCatalog []string                 `json:"model_catalog"`
		Source       string                   `json:"model_catalog_source"`
		CatalogError string                   `json:"model_catalog_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode plans response: %v", err)
	}
	if payload.CatalogError != "" {
		t.Fatalf("catalog error=%q, want the stub catalog to answer", payload.CatalogError)
	}
	// Every relay model must reach the editor, including the ones that are not
	// in any plan yet: those are exactly the models an operator needs to add.
	offered := map[string]bool{}
	for _, model := range payload.ModelCatalog {
		offered[model] = true
	}
	for _, model := range catalogTestModels {
		if !offered[model] {
			t.Fatalf("the plan editor is not offered %s: %v", model, payload.ModelCatalog)
		}
	}
	if payload.Source == "" {
		t.Fatal("the response must say which catalog tier answered")
	}
}

// TestCommercialPlansListingSurvivesACatalogOutage keeps a relay outage from
// emptying the editor. An absent catalog is reported so the page can say so,
// and the operator still sees the plans' stored models instead of a blank
// picker that would save an empty model set.
func TestCommercialPlansListingSurvivesACatalogOutage(t *testing.T) {
	store := newCommercialTestStore()
	store.plans = append(store.plans, &types.CommercialPlan{
		ID: 1, Slug: "catsco-internal-all-models-50k", Name: "内部全模型",
		ModelBudgets: map[string]float64{"gpt-6-sol": 1000},
	})
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)
	server, _ := catalogTestServer(t, nil, http.StatusServiceUnavailable)
	handler.SetCommercialModelCatalog(newTestCatalog(t, server))

	req := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/plans", nil)
	req.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	handler.HandleCommercialPlans(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plans status=%d body=%s, want the plan list even when the catalog is down", rec.Code, rec.Body.String())
	}

	var payload struct {
		Plans        []map[string]interface{} `json:"plans"`
		ModelCatalog []string                 `json:"model_catalog"`
		CatalogError string                   `json:"model_catalog_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode plans response: %v", err)
	}
	if len(payload.Plans) != 1 {
		t.Fatalf("plans=%v, want the stored plan", payload.Plans)
	}
	if len(payload.ModelCatalog) != 0 {
		t.Fatalf("catalog=%v, want none while the relay is down", payload.ModelCatalog)
	}
	if payload.CatalogError == "" {
		t.Fatal("a failed catalog read must be reported so the page can warn instead of showing an empty picker")
	}
}
