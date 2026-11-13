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
	writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": enabled, "plan_tier": tier, "model_credit_units": allowance})
}
