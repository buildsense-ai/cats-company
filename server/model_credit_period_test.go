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

// modelCreditPeriodStub 同时实现可选的月额度窗口能力，用来验证
// GET /api/relay/credits 的周期字段（而不是推算）。
type modelCreditPeriodStub struct {
	account     *store.ModelCreditAccount
	enabled     bool
	allowance   int64
	identityErr error
	period      *store.ModelCreditPeriod
	periodErr   error
	calledUID   int64
	calledNow   time.Time
}

func (s *modelCreditPeriodStub) GetModelCreditAccount(_ context.Context, uid int64) (*store.ModelCreditAccount, error) {
	if s.account == nil {
		return nil, store.ErrModelCreditNotFound
	}
	return s.account, nil
}

func (s *modelCreditPeriodStub) ListModelCreditLedgerEvents(context.Context, int64, int, int) ([]store.ModelCreditLedgerEvent, error) {
	return nil, nil
}

func (s *modelCreditPeriodStub) GetModelCreditEligibility(context.Context, int64) (bool, int, int64, error) {
	return s.enabled, 0, s.allowance, s.identityErr
}

func (s *modelCreditPeriodStub) GetModelCreditPeriod(_ context.Context, uid int64, now time.Time) (*store.ModelCreditPeriod, error) {
	s.calledUID, s.calledNow = uid, now
	return s.period, s.periodErr
}

// modelCreditReaderOnlyStub 只提供基础读取能力（没有月额度窗口），用来证明新字段
// 是可选能力：旧存储照样能回答，只是不带周期。
type modelCreditReaderOnlyStub struct {
	account *store.ModelCreditAccount
}

func (s *modelCreditReaderOnlyStub) GetModelCreditAccount(context.Context, int64) (*store.ModelCreditAccount, error) {
	return s.account, nil
}

func (s *modelCreditReaderOnlyStub) ListModelCreditLedgerEvents(context.Context, int64, int, int) ([]store.ModelCreditLedgerEvent, error) {
	return nil, nil
}

func modelCreditPeriodPayload(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode account payload %s: %v", body, err)
	}
	return payload
}

func modelCreditPeriodTimestamp(t *testing.T, payload map[string]json.RawMessage, key string) time.Time {
	t.Helper()
	raw, ok := payload[key]
	if !ok {
		t.Fatalf("payload is missing %q: %v", key, payload)
	}
	var at time.Time
	if err := json.Unmarshal(raw, &at); err != nil {
		t.Fatalf("%q is not a timestamp (%s): %v", key, raw, err)
	}
	return at.UTC()
}

// 有生效月订阅且续费已覆盖下一期：四个字段都给，值来自存储（订阅自己的锚点），
// 原有 enabled/allowed/ledger 字段一个都不变。
func TestModelCreditAccountReportsPaidMonthlyPeriod(t *testing.T) {
	periodStart := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
	subscriptionExpires := time.Date(2027, time.March, 31, 10, 0, 0, 0, time.UTC)
	stub := &modelCreditPeriodStub{
		account:   &store.ModelCreditAccount{UID: 75, MonthlyUnits: 12_000 * store.ModelCreditUnitsPerCredit},
		enabled:   true,
		allowance: 12_000 * store.ModelCreditUnitsPerCredit,
		period: &store.ModelCreditPeriod{
			MonthlyPeriodStartsAt: periodStart,
			MonthlyPeriodEndsAt:   periodEnd,
			SubscriptionExpiresAt: &subscriptionExpires,
			NextRefreshAt:         &periodEnd,
		},
	}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 75))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if stub.calledUID != 75 || stub.calledNow.IsZero() {
		t.Fatalf("period reader called with uid=%d now=%v", stub.calledUID, stub.calledNow)
	}
	payload := modelCreditPeriodPayload(t, response.Body.Bytes())
	if got := modelCreditPeriodTimestamp(t, payload, "monthly_period_starts_at"); !got.Equal(periodStart) {
		t.Fatalf("monthly_period_starts_at=%s want %s", got, periodStart)
	}
	if got := modelCreditPeriodTimestamp(t, payload, "monthly_period_ends_at"); !got.Equal(periodEnd) {
		t.Fatalf("monthly_period_ends_at=%s want %s", got, periodEnd)
	}
	if got := modelCreditPeriodTimestamp(t, payload, "subscription_expires_at"); !got.Equal(subscriptionExpires) {
		t.Fatalf("subscription_expires_at=%s want %s", got, subscriptionExpires)
	}
	if got := modelCreditPeriodTimestamp(t, payload, "next_refresh_at"); !got.Equal(periodEnd) {
		t.Fatalf("next_refresh_at=%s want the next period boundary %s", got, periodEnd)
	}
	// 时间必须是 RFC3339 字符串，而不是 Unix 时间戳或对象。
	if raw := string(payload["next_refresh_at"]); !strings.HasPrefix(raw, `"2027-02-28T10:00:00`) {
		t.Fatalf("next_refresh_at is not an RFC3339 string: %s", raw)
	}
	var buckets struct {
		Enabled      bool  `json:"enabled"`
		Allowed      bool  `json:"allowed"`
		MonthlyUnits int64 `json:"monthly_units"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &buckets); err != nil {
		t.Fatal(err)
	}
	if !buckets.Enabled || !buckets.Allowed || buckets.MonthlyUnits != 12_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("existing account fields changed: %+v", buckets)
	}
}

// 订阅在本期边界到期（没有续费）：周期和到期时间照实给，next_refresh_at 必须缺省，
// 让前端提示「续费后才刷新」而不是自己推算。
func TestModelCreditAccountOmitsRefreshWhenRenewalMissing(t *testing.T) {
	periodStart := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
	stub := &modelCreditPeriodStub{
		enabled:   true,
		allowance: 1,
		period: &store.ModelCreditPeriod{
			MonthlyPeriodStartsAt: periodStart,
			MonthlyPeriodEndsAt:   periodEnd,
			SubscriptionExpiresAt: &periodEnd,
		},
	}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 75))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	payload := modelCreditPeriodPayload(t, response.Body.Bytes())
	if _, ok := payload["next_refresh_at"]; ok {
		t.Fatalf("next_refresh_at must be absent when nothing covers the next period: %s", response.Body.String())
	}
	if got := modelCreditPeriodTimestamp(t, payload, "subscription_expires_at"); !got.Equal(periodEnd) {
		t.Fatalf("subscription_expires_at=%s want %s", got, periodEnd)
	}
}

// legacy / 邀请：没有月订阅，存储返回空。四个字段整体缺省，不能虚构周期或刷新时间。
func TestModelCreditAccountWithoutMonthlySubscriptionOmitsPeriod(t *testing.T) {
	stub := &modelCreditPeriodStub{enabled: true, allowance: 0}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 75))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	payload := modelCreditPeriodPayload(t, response.Body.Bytes())
	for _, key := range []string{"monthly_period_starts_at", "monthly_period_ends_at", "subscription_expires_at", "next_refresh_at"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("legacy/invite account must not invent %q: %s", key, response.Body.String())
		}
	}
}

// 周期读取失败：必须 503（可重试），不能拿零值假装「没有订阅」。
func TestModelCreditAccountPeriodFailureIsRetryable(t *testing.T) {
	stub := &modelCreditPeriodStub{enabled: true, allowance: 1, periodErr: errors.New("period read failed")}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 75))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "monthly_units") || strings.Contains(response.Body.String(), "next_refresh_at") {
		t.Fatalf("period failure must not answer with fabricated buckets: %s", response.Body.String())
	}
}

// 没有该可选能力的存储：照常有响应，只是不带周期字段（可选接口，不是新依赖）。
func TestModelCreditAccountWithoutPeriodReaderOmitsPeriod(t *testing.T) {
	stub := &modelCreditReaderOnlyStub{account: &store.ModelCreditAccount{UID: 71, GiftUnits: 5}}
	response := httptest.NewRecorder()
	NewModelCreditHandler(stub).HandleAccount(response, modelCreditRequest(http.MethodGet, "/api/relay/credits", 71))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	payload := modelCreditPeriodPayload(t, response.Body.Bytes())
	if _, ok := payload["next_refresh_at"]; ok {
		t.Fatalf("a store without the period reader must not report a refresh time: %s", response.Body.String())
	}
}
