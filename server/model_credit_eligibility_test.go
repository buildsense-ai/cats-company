package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
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
	if body := response.Body.String(); body != `{"allowed":true,"enabled":true,"legacy_models":[],"model_credit_units":9000000,"plan_tier":3}`+"\n" {
		t.Fatalf("unexpected eligibility payload: %s", body)
	}
}

// Credit identity and current credit permission remain independent of legacy access.
func TestModelCreditEligibilitySeparatesIdentityFromCurrentPermission(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	for _, test := range []struct {
		name     string
		stub     *modelCreditEligibilityStub
		wantBody string
	}{
		{
			name:     "expired plan keeps the identity but denies permission",
			stub:     &modelCreditEligibilityStub{enabled: true, tier: 0, allowance: 0},
			wantBody: `{"allowed":false,"enabled":true,"legacy_models":[],"model_credit_units":0,"plan_tier":0}` + "\n",
		},
		{
			name:     "never enrolled legacy user is disabled",
			stub:     &modelCreditEligibilityStub{enabled: false, tier: 0, allowance: 0},
			wantBody: `{"allowed":false,"enabled":false,"legacy_models":[],"model_credit_units":0,"plan_tier":0}` + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewModelCreditEligibilityHandler(test.stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if body := response.Body.String(); body != test.wantBody {
				t.Fatalf("payload = %s, want %s", body, test.wantBody)
			}
		})
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

func TestModelCreditEligibilityIncludesCurrentLegacyModelsEvenWithoutCreditAllowance(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &creditAwareQuotaStore{enabled: true, summary: &types.CommercialSummary{
		TotalsByModel: map[string]float64{"MiniMax-M3": 500, "gpt-5.6-sol": 100},
	}}
	response := httptest.NewRecorder()
	NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"allowed":false`) ||
		!strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"minimax-m3"`) ||
		!strings.Contains(body, `"deepseek-flash"`) || strings.Contains(body, "gpt-5.6") {
		t.Fatalf("mixed eligibility status=%d body=%s", response.Code, body)
	}
	stub.summaryErr = errors.New("legacy summary unavailable")
	response = httptest.NewRecorder()
	NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("legacy read failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestModelCreditEligibilityActiveCreditPackageDoesNotImplyLegacy(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &creditAwareQuotaStore{enabled: true, tier: 1, allowance: 10, summary: &types.CommercialSummary{
		Entitlements: []*types.CommercialEntitlement{{State: "active", PlanID: 1}},
	}}
	response := httptest.NewRecorder()
	NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"legacy_models":[]`) {
		t.Fatalf("credit-only eligibility status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestModelCreditEligibilityTokenPermissionMatchesWebsiteWithoutCreditBalance(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &creditAwareQuotaStore{enabled: true, summary: &types.CommercialSummary{TokenQuota: 1000000}}
	handler := NewBotModelConfigHandler(nil, nil)
	handler.SetCommercialQuotaSource(stub, false, nil)
	check := func(wantModels string, wantAllowed bool) {
		t.Helper()
		response := httptest.NewRecorder()
		NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"legacy_models":`+wantModels) ||
			!strings.Contains(response.Body.String(), `"allowed":false`) {
			t.Fatalf("token eligibility status=%d body=%s", response.Code, response.Body.String())
		}
		catalog, _ := handler.catalogWithUsage(context.Background(), 42, false)
		if wantAllowed && (len(catalog) != 1 || catalog[0].ID != deepSeekPublicModelID || catalog[0].BillingMode != "legacy") {
			t.Fatalf("token catalog=%#v", catalog)
		}
		if !wantAllowed && len(catalog) != 0 {
			t.Fatalf("removed token catalog=%#v", catalog)
		}
		for _, model := range []string{deepSeekPublicModelID, "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
			if allowed, err := handler.catalogModelAllowed(context.Background(), 42, model); err != nil || allowed != wantAllowed {
				t.Fatalf("token selection model=%s allowed=%v err=%v", model, allowed, err)
			}
		}
		if allowed, err := handler.catalogModelAllowed(context.Background(), 42, "gpt-6.1-sol"); err != nil || allowed {
			t.Fatalf("Token must not grant unrelated model: allowed=%v err=%v", allowed, err)
		}
	}
	check(`["deepseek-flash"]`, true)
	// Effective Token permission disappears from the trusted summary after expiry
	// or revocation. Credit identity alone cannot restore it.
	stub.summary.TokenQuota = 0
	check(`[]`, false)
}

func TestModelCreditEligibilityFreeBaselineDoesNotShadowCredits(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	stub := &creditAwareQuotaStore{enabled: true, tier: 1, allowance: 100, summary: &types.CommercialSummary{
		TotalsByModel: map[string]float64{"deepseek-flash": 100, "MiniMax-M3": 100},
		Grants: []*types.CommercialQuotaGrant{
			{GrantType: "free", Model: "deepseek-flash", AmountCNY: 100},
			{GrantType: "free", Model: "MiniMax-M3", AmountCNY: 100},
		},
	}}
	handler := NewBotModelConfigHandler(nil, nil)
	handler.SetCommercialQuotaSource(stub, true, nil)
	response := httptest.NewRecorder()
	NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"legacy_models":[]`) {
		t.Fatalf("Free baseline became legacy permission: %d %s", response.Code, response.Body.String())
	}
	catalog, errText := handler.catalogWithUsage(context.Background(), 42, false)
	if errText != "" || len(catalog) != 1 || catalog[0].ID != deepSeekPublicModelID || catalog[0].BillingMode != "credits" {
		t.Fatalf("Free baseline shadowed credits: catalog=%#v error=%s", catalog, errText)
	}
	if allowed, err := handler.catalogModelAllowed(context.Background(), 42, "minimax-m3"); err != nil || allowed {
		t.Fatalf("Free baseline expanded credit-only selection: allowed=%v err=%v", allowed, err)
	}
	// A real old package still takes precedence even alongside the Free baseline.
	stub.summary.Grants = append(stub.summary.Grants, &types.CommercialQuotaGrant{GrantType: "operator_plan", Model: "MiniMax-M3", AmountCNY: 500})
	stub.summary.TotalsByModel["MiniMax-M3"] += 500
	catalog, errText = handler.catalogWithUsage(context.Background(), 42, false)
	if errText != "" || len(catalog) != 2 {
		t.Fatalf("old package did not coexist: catalog=%#v error=%s", catalog, errText)
	}
	for _, item := range catalog {
		if item.BillingMode != "legacy" {
			t.Fatalf("old quota lost priority: %#v", item)
		}
	}
}

func TestModelCreditEligibilityLegacyDebitDoesNotRestorePermission(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	for _, tc := range []struct {
		name       string
		debit      float64
		wantLegacy bool
	}{
		{"partially_debited", 400, true},
		{"debited_to_zero", 500, false},
		{"over_debited", 600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &creditAwareQuotaStore{enabled: true, tier: 1, allowance: 100, summary: &types.CommercialSummary{
				TotalsByModel: map[string]float64{"MiniMax-M3": 500 - tc.debit, "deepseek-flash": 100},
				Grants: []*types.CommercialQuotaGrant{
					{GrantType: "operator_plan", Model: "MiniMax-M3", AmountCNY: 500},
					{GrantType: "adjustment_debit", Model: "MiniMax-M3", AmountCNY: tc.debit},
					{GrantType: "free", Model: "deepseek-flash", AmountCNY: 100},
				},
			}}
			response := httptest.NewRecorder()
			NewModelCreditEligibilityHandler(stub).ServeHTTP(response, eligibilityRequest(http.MethodPost, `{"uid":42}`, "Bearer "+secret))
			if response.Code != http.StatusOK {
				t.Fatalf("peer status=%d body=%s", response.Code, response.Body.String())
			}
			if got := strings.Contains(response.Body.String(), `"minimax-m3"`); got != tc.wantLegacy {
				t.Fatalf("legacy permission=%v want=%v body=%s", got, tc.wantLegacy, response.Body.String())
			}
			handler := NewBotModelConfigHandler(nil, nil)
			handler.SetCommercialQuotaSource(stub, true, nil)
			catalog, errText := handler.catalogWithUsage(context.Background(), 42, false)
			wantLen, wantMode := 1, "credits"
			if tc.wantLegacy {
				wantLen, wantMode = 2, "legacy"
			}
			if errText != "" || len(catalog) != wantLen {
				t.Fatalf("catalog=%#v error=%s", catalog, errText)
			}
			for _, item := range catalog {
				if item.BillingMode != wantMode {
					t.Fatalf("incorrect billing mode: %#v", item)
				}
			}
			if allowed, err := handler.catalogModelAllowed(context.Background(), 42, "minimax-m3"); err != nil || allowed != tc.wantLegacy {
				t.Fatalf("save allowed=%v error=%v", allowed, err)
			}
		})
	}
}
