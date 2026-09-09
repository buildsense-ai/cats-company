package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestCommercialPermanentReplacementKeepsSharedTotal(t *testing.T) {
	now := time.Now().UTC()
	summary := &types.CommercialSummary{TotalCNY: 50000, Plans: []*types.CommercialPlan{{ID: 1, DurationDays: -1}}, Entitlements: []*types.CommercialEntitlement{{PlanID: 1, Source: "operator", State: "active", StartsAt: now.Add(-time.Hour)}}, TotalsByModel: map[string]float64{"gpt-5.6-terra": 20000, "gpt-5.6-sol": 30000}}
	if !commercialRelayHasPermanentPackage(summary, now) || commercialRelaySharedLimit(summary) != 50000 {
		t.Fatal("permanent shared pool would be topped up with Free baseline")
	}
	summary.Entitlements[0].State = "revoked"
	if commercialRelayHasPermanentPackage(summary, now) {
		t.Fatal("revoked permanent plan must allow Free restoration")
	}
}

type commercialPlanManagementTestStore struct {
	*commercialOpsTestStore
	calls int
	err   error
}

func (s *commercialPlanManagementTestStore) ListCommercialPlanUsage(ids []int64, trial string) ([]*types.CommercialPlanUsage, error) {
	s.calls++
	return []*types.CommercialPlanUsage{{PlanID: ids[0], ActiveUsers: 2}}, s.err
}
func (s *commercialPlanManagementTestStore) ListCommercialPlanUsers(id, after int64, limit int) (*types.CommercialPlanUsers, error) {
	s.calls++
	return &types.CommercialPlanUsers{UIDs: []int64{826}}, s.err
}
func (s *commercialPlanManagementTestStore) DeleteCommercialPlan(id int64, slug, trial string) error {
	s.calls++
	return s.err
}

func TestCommercialPlanManagementAuthorizationAndValidation(t *testing.T) {
	for _, tc := range []struct {
		method, query, body, scope string
		status                     int
		calls                      int
	}{
		{"GET", "?view=usage&ids=1,2", "", commercialOpsReadScope, 200, 1},
		{"GET", "?view=users&plan_id=1&limit=25&after_uid=38", "", commercialOpsReadScope, 200, 1},
		{"GET", "?view=users&plan_id=1&limit=101", "", commercialOpsReadScope, 400, 0},
		{"GET", "?view=users&plan_id=1&after_uid=-1", "", commercialOpsReadScope, 400, 0},
		{"GET", "?view=usage&ids=" + strings.Repeat("1,", 50) + "1", "", commercialOpsReadScope, 400, 0},
		{"GET", "?view=usage&ids=1;select", "", commercialOpsReadScope, 400, 0},
		{"POST", "?action=delete", `{"id":2,"slug":"fixture"}`, commercialOpsReadScope, 403, 0},
		{"POST", "?action=delete", `{"id":2,"slug":"fixture"}`, commercialOpsWriteScope, 200, 1},
		{"POST", "?action=delete", `{"id":0,"slug":"fixture"}`, commercialOpsWriteScope, 400, 0},
		{"GET", "?action=delete&ids=2", "", commercialOpsReadScope, 400, 0},
	} {
		t.Run(tc.method+tc.query+tc.scope, func(t *testing.T) {
			s := &commercialPlanManagementTestStore{commercialOpsTestStore: newCommercialOpsTestStore()}
			admin := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
			h := NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{Slug: "fixture", Scopes: []string{tc.scope}}}, s)
			rec := httptest.NewRecorder()
			h.HandlePlans(rec, commercialOpsRequest(tc.method, "/api/account/commercial-ops/plans"+tc.query, tc.body))
			if rec.Code != tc.status || s.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, s.calls, rec.Body.String())
			}
			if tc.method == http.MethodPost && tc.status == 200 && (len(s.events) != 1 || s.events[0].Action != "plans.delete" || s.events[0].TargetRef != "fixture") {
				t.Fatalf("missing delete audit: %+v", s.events)
			}
		})
	}
}

func TestCommercialPlanDeleteConflictAndMissing(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{types.ErrCommercialPlanDeleteConflict, 409}, {types.ErrCommercialPlanNotFound, 404}} {
		s := &commercialPlanManagementTestStore{commercialOpsTestStore: newCommercialOpsTestStore(), err: tc.err}
		admin := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
		h := NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{Slug: "fixture", Scopes: []string{commercialOpsWriteScope}}}, s)
		rec := httptest.NewRecorder()
		h.HandlePlans(rec, commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/plans?action=delete", `{"id":2,"slug":"fixture"}`))
		if rec.Code != tc.status {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
