package server

import (
	"encoding/json"
	"github.com/openchat/openchat/server/store/types"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommercialInviteAutomaticallyGeneratesCode(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(`{"plan_id":1,"max_redemptions":3}`))
		req.RemoteAddr = "127.0.0.1:32000"
		rec := httptest.NewRecorder()
		handler.HandleCommercialInvites(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !commercialCodePattern.MatchString(body.Code) || len(body.Code) != 27 || seen[body.Code] {
			t.Fatal("invalid or repeated generated code")
		}
		seen[body.Code] = true
		if !store.invites[i].CreateOnly || store.invites[i].Code != body.Code {
			t.Fatal("generated code was not protected from overwrite")
		}
	}
}

func TestCommercialInviteBatchGeneration(t *testing.T) {
	store := newCommercialTestStore()
	store.plans = append(store.plans, &types.CommercialPlan{ID: 1, Slug: "batch-plan", Name: "Batch Plan"})
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)
	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(`{"plan_id":1,"max_redemptions":3,"count":5,"note":"batch"}`))
	req.RemoteAddr = "127.0.0.1:32000"
	rec := httptest.NewRecorder()
	handler.HandleCommercialInvites(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code  string   `json:"code"`
		Codes []string `json:"codes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Codes) != 5 {
		t.Fatalf("expected 5 codes, got %d", len(body.Codes))
	}
	seen := map[string]bool{}
	for _, code := range body.Codes {
		if !commercialCodePattern.MatchString(code) || len(code) != 27 || seen[code] {
			t.Fatalf("invalid or repeated generated code %q", code)
		}
		seen[code] = true
	}
	if body.Code != body.Codes[len(body.Codes)-1] {
		t.Fatalf("legacy code field should carry the last generated code: %q vs %v", body.Code, body.Codes)
	}
	if len(store.invites) != 5 {
		t.Fatalf("expected 5 stored invites, got %d", len(store.invites))
	}
	for _, invite := range store.invites {
		if !invite.CreateOnly || invite.PlanID != 1 || invite.MaxRedemptions != 3 {
			t.Fatalf("unexpected stored invite %#v", invite)
		}
	}
}

func TestCommercialInviteBatchRejectsCustomCodeAndOversizeCount(t *testing.T) {
	store := newCommercialTestStore()
	store.plans = append(store.plans, &types.CommercialPlan{ID: 1, Slug: "batch-plan", Name: "Batch Plan"})
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)
	for _, payload := range []string{
		`{"plan_id":1,"count":3,"code":"CUSTOMCODE"}`,
		`{"plan_id":1,"count":51}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(payload))
		req.RemoteAddr = "127.0.0.1:32000"
		rec := httptest.NewRecorder()
		handler.HandleCommercialInvites(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload=%s status=%d %s", payload, rec.Code, rec.Body.String())
		}
	}
	if len(store.invites) != 0 {
		t.Fatalf("no invites should be stored, got %d", len(store.invites))
	}
}
