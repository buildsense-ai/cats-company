package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type modelCreditReaderStub struct {
	account   *store.ModelCreditAccount
	events    []store.ModelCreditLedgerEvent
	uid       int64
	limit     int
	offset    int
	enabled   bool
	allowance int64
}

func (s *modelCreditReaderStub) GetModelCreditAccount(_ context.Context, uid int64) (*store.ModelCreditAccount, error) {
	s.uid = uid
	if s.account == nil {
		return nil, store.ErrModelCreditNotFound
	}
	return s.account, nil
}

func (s *modelCreditReaderStub) ListModelCreditLedgerEvents(_ context.Context, uid int64, limit, offset int) ([]store.ModelCreditLedgerEvent, error) {
	s.uid, s.limit, s.offset = uid, limit, offset
	return s.events, nil
}

// GetModelCreditEligibility 让 stub 也能回答 credit 身份：账户接口的 enabled/allowed
// 必须来自资格判定，不能用「有没有余额」自己猜。
func (s *modelCreditReaderStub) GetModelCreditEligibility(_ context.Context, uid int64) (bool, int, int64, error) {
	s.uid = uid
	tier := 0
	if s.enabled {
		tier = 3
	}
	return s.enabled, tier, s.allowance, nil
}

func modelCreditRequest(method, target string, uid int64) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if uid > 0 {
		return req.WithContext(withTestUID(uid))
	}
	return req
}

// modelCreditAccountPayload 是账户接口响应的测试视图：整数桶 + 身份字段。
type modelCreditAccountPayload struct {
	UID            int64 `json:"uid"`
	Enabled        bool  `json:"enabled"`
	Allowed        bool  `json:"allowed"`
	MonthlyUnits   int64 `json:"monthly_units"`
	GiftUnits      int64 `json:"gift_units"`
	PaidUnits      int64 `json:"paid_units"`
	DebtUnits      int64 `json:"debt_units"`
	UnitsPerCredit int64 `json:"units_per_credit"`
}

func TestModelCreditAccountRequiresOwnerAndReturnsIntegerBuckets(t *testing.T) {
	stub := &modelCreditReaderStub{
		account: &store.ModelCreditAccount{
			UID: 72, MonthlyUnits: 3_000_000, GiftUnits: 2_000_000, PaidUnits: 5_000_000, DebtUnits: 1_000_000,
		},
		enabled:   true,
		allowance: 9_000_000,
	}
	handler := NewModelCreditHandler(stub)

	unauthorized := httptest.NewRecorder()
	handler.HandleAccount(unauthorized, modelCreditRequest(http.MethodGet, "/api/relay/credits", 0))
	if unauthorized.Code != http.StatusUnauthorized || stub.uid != 0 {
		t.Fatalf("unauthenticated request status=%d uid=%d", unauthorized.Code, stub.uid)
	}

	response := httptest.NewRecorder()
	handler.HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 72))
	if response.Code != http.StatusOK || stub.uid != 72 {
		t.Fatalf("account response status=%d uid=%d body=%s", response.Code, stub.uid, response.Body.String())
	}
	var payload modelCreditAccountPayload
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MonthlyUnits != 3_000_000 || payload.GiftUnits != 2_000_000 ||
		payload.PaidUnits != 5_000_000 || payload.DebtUnits != 1_000_000 ||
		payload.UnitsPerCredit != store.ModelCreditUnitsPerCredit {
		t.Fatalf("unexpected account payload: %+v", payload)
	}
	// 身份和当期权限是显式字段：前端据此区分 credit 用户与 legacy 老用户。
	if !payload.Enabled || !payload.Allowed {
		t.Fatalf("account payload lost the credit identity fields: %+v", payload)
	}
}

// 套餐过期：enabled（身份）仍是 true，但 allowed 必须是 false —— 这不是 legacy。
func TestModelCreditAccountExpiredIdentityStaysEnabled(t *testing.T) {
	stub := &modelCreditReaderStub{enabled: true, allowance: 0}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 75))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload modelCreditAccountPayload
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Enabled || payload.Allowed {
		t.Fatalf("expired credit account = %+v, want enabled=true allowed=false", payload)
	}
}

func TestModelCreditAccountMissingIsZeroWithoutCreatingRow(t *testing.T) {
	stub := &modelCreditReaderStub{}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 73))
	if response.Code != http.StatusOK || stub.uid != 73 {
		t.Fatalf("missing account response status=%d uid=%d", response.Code, stub.uid)
	}
	var payload modelCreditAccountPayload
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MonthlyUnits != 0 || payload.GiftUnits != 0 || payload.PaidUnits != 0 {
		t.Fatalf("missing account was not represented as zero: %+v", payload)
	}
	// 从没进过 credit 的用户：身份 false，接口不会替它建账户（建账户的读路径已修）。
	if payload.Enabled || payload.Allowed {
		t.Fatalf("legacy account was reported as credit identity: %+v", payload)
	}
}

func TestModelCreditLedgerUsesAuthenticatedUIDAndBoundedPagination(t *testing.T) {
	stub := &modelCreditReaderStub{events: []store.ModelCreditLedgerEvent{{
		EventKey: "grant:private-key", Bucket: store.ModelCreditBucketPaid, EventType: store.ModelCreditEventGrant,
		Units: 2_000_000, PaidBalance: 2_000_000, CreatedAt: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}}}
	handler := NewModelCreditHandler(stub)
	response := httptest.NewRecorder()
	handler.HandleLedger(response, modelCreditRequest(http.MethodGet, "/api/relay/credits/ledger?limit=20&offset=40&uid=999", 74))
	if response.Code != http.StatusOK || stub.uid != 74 || stub.limit != 20 || stub.offset != 40 {
		t.Fatalf("ledger query status=%d uid=%d limit=%d offset=%d", response.Code, stub.uid, stub.limit, stub.offset)
	}
	if strings.Contains(response.Body.String(), "private-key") || strings.Contains(response.Body.String(), "event_key") {
		t.Fatalf("private idempotency key leaked: %s", response.Body.String())
	}
	var payload struct {
		Events []map[string]interface{} `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 1 || payload.Events[0]["bucket"] != store.ModelCreditBucketPaid {
		t.Fatalf("unexpected ledger payload: %+v", payload)
	}

	for _, target := range []string{
		"/api/relay/credits/ledger?limit=0",
		"/api/relay/credits/ledger?limit=101",
		"/api/relay/credits/ledger?offset=-1",
		"/api/relay/credits/ledger?limit=nope",
	} {
		bad := httptest.NewRecorder()
		handler.HandleLedger(bad, modelCreditRequest(http.MethodGet, target, 74))
		if bad.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", target, bad.Code, bad.Body.String())
		}
	}
}

func TestModelCreditAccountDoesNotMaskStoreFailure(t *testing.T) {
	stub := &modelCreditReaderFailureStub{}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 75))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("store failure status=%d body=%s", response.Code, response.Body.String())
	}
}

type modelCreditReaderFailureStub struct{}

func (*modelCreditReaderFailureStub) GetModelCreditAccount(context.Context, int64) (*store.ModelCreditAccount, error) {
	return nil, errors.New("database unavailable")
}

func (*modelCreditReaderFailureStub) ListModelCreditLedgerEvents(context.Context, int64, int, int) ([]store.ModelCreditLedgerEvent, error) {
	return nil, errors.New("database unavailable")
}

type modelCreditMutatorStub struct {
	reserveInput store.ModelCreditReserveInput
	settleInput  store.ModelCreditSettleInput
	releaseInput store.ModelCreditReleaseInput
	noUsageInput store.ModelCreditNoUsageConfirmation
	modelCreditReaderStub
	reservation *store.ModelCreditReservation
	account     *store.ModelCreditAccount
	err         error
	called      string
}

func (s *modelCreditMutatorStub) ReserveModelCredits(_ context.Context, input store.ModelCreditReserveInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	s.called, s.reserveInput = "reserve", input
	return s.reservation, s.account, s.err
}

func (s *modelCreditMutatorStub) SettleModelCredits(_ context.Context, input store.ModelCreditSettleInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	s.called, s.settleInput = "settle", input
	return s.reservation, s.account, s.err
}

func (s *modelCreditMutatorStub) MarkModelCreditNoUsage(_ context.Context, input store.ModelCreditNoUsageConfirmation) (*store.ModelCreditReservation, error) {
	s.called, s.noUsageInput = "confirm-no-usage", input
	return s.reservation, s.err
}

func (s *modelCreditMutatorStub) ReleaseModelCredits(_ context.Context, input store.ModelCreditReleaseInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	s.called, s.releaseInput = "release", input
	return s.reservation, s.account, s.err
}

func TestModelCreditInternalMutationDispatchesAuthenticatedOperations(t *testing.T) {
	const secret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	for _, tc := range []struct {
		path   string
		body   string
		called string
	}{
		{"reserve", `{"uid":81,"request_id":"req-1","units":1200000}`, "reserve"},
		{"settle", `{"uid":81,"request_id":"req-1","units":900000}`, "settle"},
		{"release", `{"uid":81,"request_id":"req-1","units":0}`, "release"},
		{"confirm-no-usage", `{"uid":81,"request_id":"req-1","units":0}`, "confirm-no-usage"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			stub := &modelCreditMutatorStub{reservation: &store.ModelCreditReservation{UID: 81, RequestID: "req-1", ReservedUnits: 1_200_000, Split: store.ModelCreditSplit{Monthly: 1_200_000}, State: store.ModelCreditReservationReserved}}
			handler := NewModelCreditHandler(stub)
			req := httptest.NewRequest(http.MethodPost, "/internal/relay/credits/"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+secret)
			response := httptest.NewRecorder()
			handler.HandleInternalMutation(response, req)
			if response.Code != http.StatusOK || stub.called != tc.called {
				t.Fatalf("status=%d called=%q body=%s", response.Code, stub.called, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"reservation"`) {
				t.Fatalf("reservation missing from response: %s", response.Body.String())
			}
			switch tc.called {
			case "reserve":
				if stub.reserveInput.UID != 81 || stub.reserveInput.RequestID != "req-1" || stub.reserveInput.Units != 1_200_000 {
					t.Fatalf("unexpected reserve input: %+v", stub.reserveInput)
				}
			case "settle":
				if stub.settleInput.UID != 81 || stub.settleInput.RequestID != "req-1" || stub.settleInput.ActualUnits != 900_000 {
					t.Fatalf("unexpected settle input: %+v", stub.settleInput)
				}
			case "release":
				if stub.releaseInput.UID != 81 || stub.releaseInput.RequestID != "req-1" {
					t.Fatalf("unexpected release input: %+v", stub.releaseInput)
				}
			case "confirm-no-usage":
				if stub.noUsageInput.UID != 81 || stub.noUsageInput.RequestID != "req-1" {
					t.Fatalf("unexpected no-usage input: %+v", stub.noUsageInput)
				}
			}
		})
	}
}

func TestModelCreditInternalMutationRejectsUnauthorizedAndMalformedRequests(t *testing.T) {
	const secret = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	for _, tc := range []struct {
		name   string
		path   string
		body   string
		auth   string
		status int
	}{
		{"missing auth", "reserve", `{}`, "", http.StatusUnauthorized},
		{"unknown path", "unknown", `{}`, "Bearer " + secret, http.StatusNotFound},
		{"unknown field", "reserve", `{"uid":81,"request_id":"r","units":1,"extra":true}`, "Bearer " + secret, http.StatusBadRequest},
		{"trailing json", "reserve", `{"uid":81,"request_id":"r","units":1} {}`, "Bearer " + secret, http.StatusBadRequest},
		{"nonzero no usage", "confirm-no-usage", `{"uid":81,"request_id":"r","units":1}`, "Bearer " + secret, http.StatusBadRequest},
		{"nonzero release", "release", `{"uid":81,"request_id":"r","units":1}`, "Bearer " + secret, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &modelCreditMutatorStub{}
			req := httptest.NewRequest(http.MethodPost, "/internal/relay/credits/"+tc.path, strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			response := httptest.NewRecorder()
			NewModelCreditHandler(stub).HandleInternalMutation(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if stub.called != "" {
				t.Fatalf("invalid request reached ledger mutation %q", stub.called)
			}
		})
	}
}

func TestModelCreditInternalMutationMapsLedgerErrors(t *testing.T) {
	const secret = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", store.ErrModelCreditInvalid, http.StatusBadRequest},
		{"insufficient", store.ErrModelCreditInsufficient, http.StatusPaymentRequired},
		{"conflict", store.ErrModelCreditConflict, http.StatusConflict},
		{"missing reservation", store.ErrModelCreditNotFound, http.StatusNotFound},
		{"internal failure", errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &modelCreditMutatorStub{err: tc.err}
			req := httptest.NewRequest(http.MethodPost, "/internal/relay/credits/confirm-no-usage", strings.NewReader(`{"uid":81,"request_id":"req-1","units":0}`))
			req.Header.Set("Authorization", "Bearer "+secret)
			response := httptest.NewRecorder()
			NewModelCreditHandler(stub).HandleInternalMutation(response, req)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), `"error"`) {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}
