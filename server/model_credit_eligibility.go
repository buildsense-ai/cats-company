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
//     true。Relay 依靠它区分「过期」和「真正的 legacy 老用户」，后者才允许回落到
//     老额度路径。
//   - plan_tier：当下主套餐的档位（没有生效套餐时为 0）。
//   - model_credit_units：当下主套餐的月额度（micro units）；0 表示此刻没有可用的
//     credit 权限（过期/退订），接口据此回答 allowed=false。
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		// enabled 是身份，allowed 是当期权限：过期套餐回答 enabled=true /
		// allowed=false，真正的 legacy 老用户才是 enabled=false。Relay 只允许在
		// enabled=false 时回落到老额度路径。
		"enabled":            enabled,
		"allowed":            enabled && allowance > 0,
		"plan_tier":          tier,
		"model_credit_units": allowance,
	})
}
