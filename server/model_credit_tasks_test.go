package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Relay 只把服务端认证出来的 uid 传进来，门禁判定本身在存储层完成。这里锁的是 HTTP
// 契约：认证、请求校验、字段映射和错误码（Relay 按这些码决定放行还是暂停任务）。
type modelCreditTaskStub struct {
	input        store.ModelCreditTaskInput
	decision     *store.ModelCreditTaskDecision
	err          error
	summary      *types.CommercialSummary
	summaryErr   error
	summaryCalls int
	existingTask *store.ModelCreditTask
}

func (s *modelCreditTaskStub) ApplyModelCreditTask(_ context.Context, in store.ModelCreditTaskInput) (*store.ModelCreditTaskDecision, error) {
	s.input = in
	return s.decision, s.err
}

func (s *modelCreditTaskStub) GetModelCreditTask(context.Context, int64, string) (*store.ModelCreditTask, error) {
	if s.existingTask != nil {
		return s.existingTask, nil
	}
	return nil, store.ErrModelCreditTaskNotFound
}

func (s *modelCreditTaskStub) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	s.summaryCalls++
	return s.summary, s.summaryErr
}

const modelCreditTaskTestSecret = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

func modelCreditTaskHTTPRequest(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/internal/relay/credit-tasks", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+modelCreditTaskTestSecret)
	return req
}

func TestModelCreditTaskHandlerMapsDecisions(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	stub := &modelCreditTaskStub{decision: &store.ModelCreditTaskDecision{
		Enabled: true, Allowed: false, TaskID: "root-1", State: store.ModelCreditTaskPaused,
		PlanTier:       2,
		RetainedTaskID: "root-2", Reason: store.ModelCreditTaskReasonQuotaExhausted,
		SpendableUnits: 0, DebtUnits: 1_500_000,
	}}
	response := httptest.NewRecorder()
	NewModelCreditTaskHandler(stub).ServeHTTP(response, modelCreditTaskHTTPRequest(`{"uid":42,"action":"status","task_id":"root-1"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if stub.input.UID != 42 || stub.input.Action != store.ModelCreditTaskActionStatus || stub.input.TaskID != "root-1" {
		t.Fatalf("unexpected store input %+v", stub.input)
	}
	var payload struct {
		Enabled        bool   `json:"enabled"`
		Allowed        bool   `json:"allowed"`
		TaskID         string `json:"task_id"`
		State          string `json:"state"`
		PlanTier       int    `json:"plan_tier"`
		RetainedTaskID string `json:"retained_task_id"`
		Reason         string `json:"reason"`
		SpendableUnits int64  `json:"spendable_units"`
		DebtUnits      int64  `json:"debt_units"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Enabled || payload.Allowed || payload.TaskID != "root-1" || payload.State != store.ModelCreditTaskPaused ||
		payload.PlanTier != 2 || payload.RetainedTaskID != "root-2" || payload.Reason != store.ModelCreditTaskReasonQuotaExhausted ||
		payload.SpendableUnits != 0 || payload.DebtUnits != 1_500_000 {
		t.Fatalf("unexpected decision payload %+v", payload)
	}

	// 老用户：enabled=false + allowed=true，字段必须齐全（Relay 不该靠缺字段判断）。
	// plan_tier 必须是 0：legacy 没有 credit 档位，Relay 不得拿它计价。
	legacy := &modelCreditTaskStub{decision: &store.ModelCreditTaskDecision{
		Enabled: false, Allowed: true, TaskID: "root-legacy", State: store.ModelCreditTaskRunning,
		Reason: store.ModelCreditTaskReasonLegacy,
	}}
	legacyResponse := httptest.NewRecorder()
	NewModelCreditTaskHandler(legacy).ServeHTTP(legacyResponse, modelCreditTaskHTTPRequest(`{"uid":7,"action":"start","task_id":"root-legacy"}`))
	if legacyResponse.Code != http.StatusOK || !strings.Contains(legacyResponse.Body.String(), `"enabled":false`) ||
		!strings.Contains(legacyResponse.Body.String(), `"allowed":true`) ||
		!strings.Contains(legacyResponse.Body.String(), `"plan_tier":0`) {
		t.Fatalf("legacy payload status=%d body=%s", legacyResponse.Code, legacyResponse.Body.String())
	}

	// 运行中任务：档位快照随响应回来（Relay 用它按原费率继续计价）。
	running := &modelCreditTaskStub{decision: &store.ModelCreditTaskDecision{
		Enabled: true, Allowed: true, TaskID: "root-running", State: store.ModelCreditTaskRunning,
		PlanTier: 3, Reason: store.ModelCreditTaskReasonOK,
	}}
	runningResponse := httptest.NewRecorder()
	NewModelCreditTaskHandler(running).ServeHTTP(runningResponse, modelCreditTaskHTTPRequest(`{"uid":8,"action":"status","task_id":"root-running"}`))
	if runningResponse.Code != http.StatusOK || !strings.Contains(runningResponse.Body.String(), `"plan_tier":3`) {
		t.Fatalf("running payload status=%d body=%s", runningResponse.Code, runningResponse.Body.String())
	}
}

func TestModelCreditTaskHandlerRejectsBadRequests(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	for _, tc := range []struct {
		name   string
		method string
		body   string
		auth   string
		status int
	}{
		{"get", http.MethodGet, ``, "Bearer " + modelCreditTaskTestSecret, http.StatusMethodNotAllowed},
		{"missing auth", http.MethodPost, `{"uid":1,"action":"start","task_id":"root"}`, "", http.StatusUnauthorized},
		{"wrong secret", http.MethodPost, `{"uid":1,"action":"start","task_id":"root"}`, "Bearer " + strings.Repeat("e", 64), http.StatusUnauthorized},
		{"bad action", http.MethodPost, `{"uid":1,"action":"explode","task_id":"root"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"missing uid", http.MethodPost, `{"action":"start","task_id":"root"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"bad task id", http.MethodPost, `{"uid":1,"action":"start","task_id":"not a token"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"unknown field", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","extra":true}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"untrusted routing hint", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","prefer_legacy":true}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"model on status", http.MethodPost, `{"uid":1,"action":"status","task_id":"root","model":"deepseek-flash"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"model on resume", http.MethodPost, `{"uid":1,"action":"resume","task_id":"root","model":"deepseek-flash"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"model on finish", http.MethodPost, `{"uid":1,"action":"finish","task_id":"root","model":"deepseek-flash"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"model on dispatch", http.MethodPost, `{"uid":1,"action":"dispatch","task_id":"root","model":"deepseek-flash"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"legacy preference on dispatch", http.MethodPost, `{"uid":1,"action":"dispatch","task_id":"root","prefer_legacy":true}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"empty model", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","model":""}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"blank model", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","model":" \t"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"null model", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","model":null}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"numeric model", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","model":1}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"long model", http.MethodPost, `{"uid":1,"action":"start","task_id":"root","model":"` + strings.Repeat("a", 129) + `"}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
		{"trailing json", http.MethodPost, `{"uid":1,"action":"start","task_id":"root"} {}`, "Bearer " + modelCreditTaskTestSecret, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &modelCreditTaskStub{}
			req := httptest.NewRequest(tc.method, "/internal/relay/credit-tasks", strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			response := httptest.NewRecorder()
			NewModelCreditTaskHandler(stub).ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if stub.input.TaskID != "" {
				t.Fatalf("invalid request reached the store: %+v", stub.input)
			}
		})
	}
}

func TestModelCreditTaskHandlerLegacyPreference(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	quota := &types.CommercialSummary{TotalsByModel: map[string]float64{"*": 10}}
	for _, tc := range []struct {
		name       string
		modelField string
		summary    *types.CommercialSummary
		wantLegacy bool
		wantReads  int
	}{
		{"old Relay", "", quota, false, 0},
		{"public model", `,"model":"deepseek-flash"`, quota, true, 1},
		{"legacy alias", `,"model":" DEEPSEEK-V4-FLASH "`, quota, true, 1},
		{"vision alias with token rights", `,"model":"deepseek-v4-flash-vision-exp"`, &types.CommercialSummary{TokenQuota: 10}, true, 1},
		{"credit only", `,"model":"deepseek-flash"`, &types.CommercialSummary{}, false, 1},
		{"free baseline", `,"model":"deepseek-flash"`, &types.CommercialSummary{
			TotalsByModel: map[string]float64{"*": 10},
			Grants:        []*types.CommercialQuotaGrant{{GrantType: "free", Model: "*", AmountCNY: 10}},
		}, false, 1},
		{"restricted GPT", `,"model":"gpt-6.2-sol"`, quota, false, 0},
		{"unrecognized spelling", `,"model":"deepseekflash"`, quota, false, 0},
		{"128-byte model", `,"model":"` + strings.Repeat("a", 128) + `"`, quota, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &modelCreditTaskStub{summary: tc.summary, decision: &store.ModelCreditTaskDecision{}}
			response := httptest.NewRecorder()
			NewModelCreditTaskHandler(stub).ServeHTTP(response, modelCreditTaskHTTPRequest(
				`{"uid":42,"action":"start","task_id":"root"`+tc.modelField+`}`))
			if response.Code != http.StatusOK || stub.input.PreferLegacy != tc.wantLegacy || stub.summaryCalls != tc.wantReads {
				t.Fatalf("status=%d input=%+v summary reads=%d body=%s", response.Code, stub.input, stub.summaryCalls, response.Body.String())
			}
		})
	}
}

func TestModelCreditTaskHandlerLegacySummaryFailure(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	stub := &modelCreditTaskStub{summaryErr: errors.New("summary unavailable"), decision: &store.ModelCreditTaskDecision{}}
	handler := NewModelCreditTaskHandler(stub)
	const body = `{"uid":42,"action":"start","task_id":"root","model":"deepseek-flash"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, modelCreditTaskHTTPRequest(body))
	if response.Code != http.StatusInternalServerError || stub.input.TaskID != "" {
		t.Fatalf("new root after summary failure: status=%d input=%+v", response.Code, stub.input)
	}
	stub.existingTask = &store.ModelCreditTask{TaskID: "root"}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, modelCreditTaskHTTPRequest(body))
	if response.Code != http.StatusOK || stub.input.TaskID != "root" || stub.input.PreferLegacy {
		t.Fatalf("existing root after summary failure: status=%d input=%+v", response.Code, stub.input)
	}
}

func TestModelCreditTaskHandlerMapsStoreErrors(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", store.ErrModelCreditInvalid, http.StatusBadRequest},
		{"unknown task", store.ErrModelCreditTaskNotFound, http.StatusNotFound},
		{"paused task", store.ErrModelCreditTaskNotRunning, http.StatusConflict},
		{"task without a trusted plan tier", store.ErrModelCreditTaskTierUnavailable, http.StatusConflict},
		{"conflict", store.ErrModelCreditConflict, http.StatusConflict},
		{"internal", errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &modelCreditTaskStub{err: tc.err}
			response := httptest.NewRecorder()
			NewModelCreditTaskHandler(stub).ServeHTTP(response, modelCreditTaskHTTPRequest(`{"uid":1,"action":"start","task_id":"root"}`))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), `"error"`) {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}

// 未接存储层时必须明确 503：不能让 Relay 把「门禁没接好」误当成「放行」。
func TestModelCreditTaskHandlerWithoutStoreIsUnavailable(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	var handler *modelCreditTaskHandler
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, modelCreditTaskHTTPRequest(`{"uid":1,"action":"start","task_id":"root"}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// dispatch 是 Relay opt-in 派发（X-CatsCo-Billing-Dispatch）专用入口：请求体只有
// uid/action/task_id，Company 侧不读 legacy 额度、也不认模型字段，判定完全交给存储层的
// 既有政策。纯 legacy 账户照旧拿到 enabled=false + allowed=true。
func TestModelCreditTaskHandlerDispatch(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", modelCreditTaskTestSecret)
	for _, action := range []string{"dispatch", "DISPATCH", " Dispatch "} {
		stub := &modelCreditTaskStub{decision: &store.ModelCreditTaskDecision{
			Enabled: true, Allowed: true, TaskID: "dispatch-root", State: store.ModelCreditTaskRunning,
			PlanTier: 2, Reason: store.ModelCreditTaskReasonOK, SpendableUnits: 4,
		}}
		response := httptest.NewRecorder()
		NewModelCreditTaskHandler(stub).ServeHTTP(response,
			modelCreditTaskHTTPRequest(`{"uid":42,"action":"`+action+`","task_id":"dispatch-root"}`))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"plan_tier":2`) {
			t.Fatalf("%q status=%d body=%s", action, response.Code, response.Body.String())
		}
		if stub.input.Action != store.ModelCreditTaskActionDispatch || stub.input.TaskID != "dispatch-root" ||
			stub.input.PreferLegacy || stub.summaryCalls != 0 {
			t.Fatalf("%q reached the store as %+v (legacy reads=%d)", action, stub.input, stub.summaryCalls)
		}
	}

	// 纯 legacy 账户：dispatch 照旧返回 legacy 判定，不会读 legacy 汇总，也不会被改道。
	legacy := &modelCreditTaskStub{decision: &store.ModelCreditTaskDecision{
		Enabled: false, Allowed: true, TaskID: "dispatch-legacy", State: store.ModelCreditTaskRunning,
		Reason: store.ModelCreditTaskReasonLegacy,
	}}
	legacyResponse := httptest.NewRecorder()
	NewModelCreditTaskHandler(legacy).ServeHTTP(legacyResponse,
		modelCreditTaskHTTPRequest(`{"uid":7,"action":"dispatch","task_id":"dispatch-legacy"}`))
	if legacyResponse.Code != http.StatusOK || legacy.summaryCalls != 0 ||
		!strings.Contains(legacyResponse.Body.String(), `"enabled":false`) ||
		!strings.Contains(legacyResponse.Body.String(), `"allowed":true`) ||
		!strings.Contains(legacyResponse.Body.String(), `"plan_tier":0`) {
		t.Fatalf("legacy dispatch status=%d body=%s", legacyResponse.Code, legacyResponse.Body.String())
	}

	// 存储层错误照旧映射成 HTTP 码：Relay 靠它决定停不停。
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"unknown root", store.ErrModelCreditTaskNotFound, http.StatusNotFound},
		{"conflict", store.ErrModelCreditConflict, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &modelCreditTaskStub{err: tc.err}
			response := httptest.NewRecorder()
			NewModelCreditTaskHandler(stub).ServeHTTP(response,
				modelCreditTaskHTTPRequest(`{"uid":1,"action":"dispatch","task_id":"dispatch-root"}`))
			if response.Code != tc.status || stub.input.Action != store.ModelCreditTaskActionDispatch {
				t.Fatalf("status=%d want=%d input=%+v body=%s", response.Code, tc.status, stub.input, response.Body.String())
			}
		})
	}
}
