package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type modelCreditEligibilityStub struct {
	enabled   bool
	tier      int
	allowance int64
	err       error
	uid       int64
}

func (s *modelCreditEligibilityStub) GetModelCreditEligibility(_ context.Context, uid int64) (bool, int, int64, error) {
	s.uid = uid
	return s.enabled, s.tier, s.allowance, s.err
}

func eligibilityRequest(method, body, authorization string) *http.Request {
	req := httptest.NewRequest(method, "/internal/relay/credits/eligibility", strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

func TestModelCreditEligibilityRequiresPeerSecretAndStrictUID(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &modelCreditEligibilityStub{enabled: true, tier: 3, allowance: 9000000}
	handler := NewModelCreditEligibilityHandler(stub)

	for _, test := range []struct {
		name string
		req  *http.Request
		want int
	}{
		{"unauthorized", eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer wrong"), http.StatusUnauthorized},
		{"invalid uid", eligibilityRequest(http.MethodPost, `{"uid":0}`, "Bearer "+secret), http.StatusBadRequest},
		{"unknown field", eligibilityRequest(http.MethodPost, `{"uid":42,"full_summary":true}`, "Bearer "+secret), http.StatusBadRequest},
		{"trailing json", eligibilityRequest(http.MethodPost, `{"uid":42}{}`, "Bearer "+secret), http.StatusBadRequest},
		{"wrong method", eligibilityRequest(http.MethodGet, `{}`, "Bearer "+secret), http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
	if stub.uid != 0 {
		t.Fatalf("invalid requests reached store for uid %d", stub.uid)
	}
}

func TestModelCreditEligibilityReturnsPrimaryPlanFieldsOnly(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &modelCreditEligibilityStub{enabled: true, tier: 3, allowance: 9000000}
	response := httptest.NewRecorder()
	NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
	if response.Code != http.StatusOK || stub.uid != 42 {
		t.Fatalf("status=%d uid=%d body=%s", response.Code, stub.uid, response.Body.String())
	}
	if body := response.Body.String(); body != `{"enabled":true,"model_credit_units":9000000,"plan_tier":3}`+"\n" {
		t.Fatalf("unexpected eligibility payload: %s", body)
	}
}

func TestModelCreditEligibilityDoesNotMaskStoreFailure(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &modelCreditEligibilityStub{err: errors.New("database unavailable")}
	response := httptest.NewRecorder()
	NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestModelCreditEligibilityRejectsInvalidConfiguredSecret(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "not-a-peer-secret")
	response := httptest.NewRecorder()
	NewModelCreditEligibilityHandler(&modelCreditEligibilityStub{}).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer not-a-peer-secret"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
