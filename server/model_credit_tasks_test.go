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
)

// Relay 只把服务端认证出来的 uid 传进来，门禁判定本身在存储层完成。这里锁的是 HTTP
// 契约：认证、请求校验、字段映射和错误码（Relay 按这些码决定放行还是暂停任务）。
type modelCreditTaskStub struct {
	input    store.ModelCreditTaskInput
	decision *store.ModelCreditTaskDecision
	err      error
}

func (s *modelCreditTaskStub) ApplyModelCreditTask(_ context.Context, in store.ModelCreditTaskInput) (*store.ModelCreditTaskDecision, error) {
	s.input = in
	return s.decision, s.err
}

func (s *modelCreditTaskStub) GetModelCreditTask(context.Context, int64, string) (*store.ModelCreditTask, error) {
	return nil, store.ErrModelCreditTaskNotFound
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
