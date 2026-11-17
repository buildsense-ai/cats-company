package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// ModelCreditEligibilityReader 是资格查询的存储面。
//
// GetModelCreditEligibility 的三个返回值分别是：
//   - enabled：credit 身份。这个用户是否曾经进入 credit 体系；套餐过期后仍然是
//     true。有效旧模型权限通过 legacy_models 独立返回。
//   - plan_tier：当下主套餐的档位；没有生效套餐但还有付费余额（独立充值）时是
//     Relay 梯度的第 1 档，其余无权限情况为 0。
//   - model_credit_units：当下可用的 credit 额度（micro units）—— 主套餐的月额度，
//     或者没有套餐时的付费余额；0 表示此刻没有可用的 credit 权限（过期/退订/余额
//     为零），接口据此回答 allowed=false。
//
// 机器目录、保存和 peer 的 legacy_models 共用有效商业摘要，不恢复历史权益。
type ModelCreditEligibilityReader interface {
	GetModelCreditEligibility(context.Context, int64) (bool, int, int64, error)
}

type ModelCreditEligibilityHandler struct {
	reader ModelCreditEligibilityReader
}

func NewModelCreditEligibilityHandler(reader ModelCreditEligibilityReader) *ModelCreditEligibilityHandler {
	return &ModelCreditEligibilityHandler{reader: reader}
}

var modelCreditEligibilityPeerSecretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (h *ModelCreditEligibilityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	secret := strings.TrimSpace(os.Getenv("CATSCO_RELAY_CREDIT_PEER_SECRET"))
	auth := r.Header.Values("Authorization")
	if !modelCreditEligibilityPeerSecretPattern.MatchString(secret) || len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.reader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credit eligibility unavailable"})
		return
	}
	var input struct {
		UID int64 `json:"uid"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.UID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	enabled, tier, allowance, err := h.reader.GetModelCreditEligibility(r.Context(), input.UID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load credit eligibility"})
		return
	}
	summary, err := readLegacyModelSummary(h.reader, input.UID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load legacy eligibility"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":            enabled,
		"allowed":            enabled && allowance > 0,
		"plan_tier":          tier,
		"model_credit_units": allowance,
		"legacy_models":      legacyModelsForSummary(summary),
	})
}
