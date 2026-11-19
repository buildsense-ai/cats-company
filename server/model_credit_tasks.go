package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// Relay 公共接口 POST /v1/credit-tasks 由 Relay 自己用同模型 Bearer key 认证、在
// 服务端解析出 UID，然后带着 peer secret 调这里的内部接口。所以下面的 uid 永远来自
// Relay 的服务端认证，客户端无法伪造。
//
//	POST /internal/relay/credit-tasks   {uid, action, task_id, model? (start only)}
//	-> {enabled, allowed, task_id, state, plan_tier, retained_task_id?, reason?, spendable_units, debt_units}
//
// action 有五个：start / status / resume / finish，以及 dispatch。dispatch 是 Relay
// opt-in 派发（X-CatsCo-Billing-Dispatch）专用：它只推进计费状态（root 缺失等同
// start，running/completed 等同 status，paused 等同 resume），不会重放应用请求。
// 纯 legacy 账户照旧返回 enabled=false + allowed=true 且什么都不落库；请求体里没有
// 档位字段，也拒绝未知字段 —— 客户端无法自报 tier；model 只有 start 带。
//
// 为什么任务要单独一个接口：额度是否够、有没有欠款、套餐权限是否还有效、以及「额度
// 耗尽只保留最近开始的那个任务」这些判定必须在服务端一次事务里做完，Relay 不能自己
// 拼（docs/credit-plan-rules-20261109.md P0-3）。
//
// plan_tier 是这个 root 任务的套餐档位快照（1..3，legacy/无快照为 0）：任务运行中套餐
// 到期时 Relay 仍按它计价（按到期前费率跑到结束），所以不要改用 eligibility 的现读档位。
type modelCreditTaskHandler struct {
	tasks store.ModelCreditTaskStore
}

// NewModelCreditTaskHandler 注入任务门禁的存储面；存储层未实现时接口返回 503，
// 由 Relay 决定怎么处理「门禁不可用」（默认应当拒绝放行）。
func NewModelCreditTaskHandler(tasks store.ModelCreditTaskStore) *modelCreditTaskHandler {
	return &modelCreditTaskHandler{tasks: tasks}
}

type modelCreditTaskRequest struct {
	UID    int64           `json:"uid"`
	Action string          `json:"action"`
	TaskID string          `json:"task_id"`
	Model  json.RawMessage `json:"model"`
}

type modelCreditTaskResponse struct {
	Enabled bool   `json:"enabled"`
	Allowed bool   `json:"allowed"`
	TaskID  string `json:"task_id"`
	State   string `json:"state"`
	// PlanTier 是这个 root 任务 start 时锁定的套餐档位快照（1..3）；0 表示没有可信
	// 快照（legacy 老用户，或旧行）。任务运行中套餐到期，Relay 就按这个档位继续计价，
	// 不要再去 eligibility 现读。
	PlanTier       int    `json:"plan_tier"`
	RetainedTaskID string `json:"retained_task_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
	SpendableUnits int64  `json:"spendable_units"`
	DebtUnits      int64  `json:"debt_units"`
}

var modelCreditTaskPeerSecretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const modelCreditTaskModelMaxLen = 128

func (h *modelCreditTaskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	secret := strings.TrimSpace(os.Getenv("CATSCO_RELAY_CREDIT_PEER_SECRET"))
	auth := r.Header.Values("Authorization")
	if !modelCreditTaskPeerSecretPattern.MatchString(secret) || len(auth) != 1 ||
		subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.tasks == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credit task gate unavailable"})
		return
	}
	var input modelCreditTaskRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	taskID := strings.TrimSpace(input.TaskID)
	action, ok := store.NormalizeModelCreditTaskAction(input.Action)
	if !ok || input.UID <= 0 || store.ValidateModelCreditTaskID(taskID) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var preferLegacy bool
	if input.Model != nil {
		var model string
		if action != store.ModelCreditTaskActionStart || json.Unmarshal(input.Model, &model) != nil ||
			len(model) > modelCreditTaskModelMaxLen || strings.TrimSpace(model) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		// Only DeepSeek Flash and its catalog aliases overlap with credit billing.
		if strings.EqualFold(resolveLegacyCatalogModelID(strings.TrimSpace(model)), deepSeekPublicModelID) {
			summary, err := readLegacyModelSummary(h.tasks, input.UID)
			if err != nil {
				// Replays still use the credit gate if legacy eligibility is unavailable.
				if task, taskErr := h.tasks.GetModelCreditTask(r.Context(), input.UID, taskID); taskErr != nil || task == nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load legacy eligibility"})
					return
				}
			} else {
				preferLegacy = legacyQuotaModelAllowed(summary, model)
			}
		}
	}
	decision, err := h.tasks.ApplyModelCreditTask(r.Context(), store.ModelCreditTaskInput{
		UID: input.UID, Action: action, TaskID: taskID, PreferLegacy: preferLegacy,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrModelCreditInvalid):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		case errors.Is(err, store.ErrModelCreditTaskRequired):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root task id required"})
		case errors.Is(err, store.ErrModelCreditTaskNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "root task not found"})
		case errors.Is(err, store.ErrModelCreditTaskNotRunning):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "root task is not running"})
		case errors.Is(err, store.ErrModelCreditTaskTierUnavailable):
			// 档位快照不可信（旧行）：算不出费率就不放行，Relay 收到即停。
			writeJSON(w, http.StatusConflict, map[string]string{"error": "root task plan tier unavailable"})
		case errors.Is(err, store.ErrModelCreditConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "request conflict"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credit task gate failed"})
		}
		return
	}
	if decision == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credit task gate failed"})
		return
	}
	writeJSON(w, http.StatusOK, modelCreditTaskResponse{
		Enabled:        decision.Enabled,
		Allowed:        decision.Allowed,
		TaskID:         decision.TaskID,
		State:          decision.State,
		PlanTier:       decision.PlanTier,
		RetainedTaskID: decision.RetainedTaskID,
		Reason:         decision.Reason,
		SpendableUnits: decision.SpendableUnits,
		DebtUnits:      decision.DebtUnits,
	})
}
