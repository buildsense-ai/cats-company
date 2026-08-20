package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialOpsTestVerifier struct {
	service AccountService
}

func (v commercialOpsTestVerifier) Configured() bool { return true }

func (v commercialOpsTestVerifier) Verify(token string) (AccountService, bool) {
	return v.service, token == "service-secret"
}

type commercialOpsTestStore struct {
	*commercialTestStore
	events []*types.CommercialOperatorEvent
}

func newCommercialOpsTestStore() *commercialOpsTestStore {
	return &commercialOpsTestStore{commercialTestStore: newCommercialTestStore()}
}

func (s *commercialOpsTestStore) GetCommercialOperationsOverview(now time.Time) (*types.CommercialOperationsOverview, error) {
	return &types.CommercialOperationsOverview{
		GeneratedAt:          now,
		PlansTotal:           int64(len(s.plans)),
		OrdersByStatus:       map[string]int64{"pending": 2},
		RecentOperatorEvents: s.events,
	}, nil
}

func (s *commercialOpsTestStore) RecordCommercialOperatorEvent(event *types.CommercialOperatorEvent) error {
	s.events = append(s.events, event)
	return nil
}

func commercialOpsRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:32000"
	req.Header.Set("Authorization", "Service service-secret")
	return req
}

func TestCommercialOpsRequiresExplicitScopesAndInternalSource(t *testing.T) {
	store := newCommercialOpsTestStore()
	admin := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)

	unscoped := NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{Slug: "legacy"}}, store)
	rec := httptest.NewRecorder()
	unscoped.HandleOverview(rec, commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/overview", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unscoped status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	readOnly := NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{Slug: "dashboard", Scopes: []string{commercialOpsReadScope}}}, store)
	rec = httptest.NewRecorder()
	readOnly.HandlePlans(rec, commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/plans", `{"slug":"test-plan"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only write status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	external := commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/overview", "")
	external.RemoteAddr = "203.0.113.8:443"
	rec = httptest.NewRecorder()
	readOnly.HandleOverview(rec, external)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("external source status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = httptest.NewRecorder()
	readOnly.HandleOverview(rec, commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/overview", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("read overview status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCommercialOpsWriteIsAuditedWithoutRequestBody(t *testing.T) {
	store := newCommercialOpsTestStore()
	admin := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)
	handler := NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{
		Slug:   "cats-relay-admin",
		Scopes: []string{commercialOpsWriteScope},
	}}, store)

	rec := httptest.NewRecorder()
	handler.HandlePlans(rec, commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/plans", `{
		"slug":"catsco-personal",
		"name":"个人版",
		"price_fen":39900,
		"currency":"CNY",
		"sale_state":"test",
		"internal_quota_tokens":200000000,
		"model_budgets":{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"gpt-5.6-terra":1750,"gpt-5.6-sol":1750,"gpt-5.6-luna":1750},
		"duration_days":30,
		"state":0
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("write status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(store.events))
	}
	event := store.events[0]
	if event.Service != "cats-relay-admin" || event.Action != "plans.upsert" || event.TargetRef != "catsco-personal" || event.StatusCode != http.StatusOK {
		t.Fatalf("unexpected audit event: %+v", event)
	}
}

func TestCommercialOpsTargetRefPreservesOversizedBody(t *testing.T) {
	body := `{"slug":"catsco-personal","padding":"` + strings.Repeat("x", commercialOpsBodyLimit) + `"}`
	req := commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/plans", body)
	if got := commercialOpsTargetRef(req); got != "oversized" {
		t.Fatalf("target ref = %q, want oversized", got)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restored) != body {
		t.Fatalf("restored body changed: got %d bytes, want %d", len(restored), len(body))
	}
}
