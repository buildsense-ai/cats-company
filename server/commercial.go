package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type CommercialStore interface {
	ListCommercialPlans(includeDisabled bool) ([]*types.CommercialPlan, error)
	CreateCommercialPlan(plan *types.CommercialPlan) (int64, error)
	ListCommercialInviteCodes(limit int) ([]*types.CommercialInviteCode, error)
	CreateCommercialInviteCode(invite *types.CommercialInviteCode) (int64, error)
	GrantCommercialQuota(grant *types.CommercialQuotaGrant) (*types.CommercialQuotaGrant, error)
	RedeemCommercialInvite(uid int64, code string) (*types.CommercialSummary, error)
	GetCommercialSummary(uid int64) (*types.CommercialSummary, error)
}

type RelayCommercialHandler struct {
	store         CommercialStore
	publicEnabled bool
}

func NewRelayCommercialHandler(store CommercialStore, publicEnabled ...bool) *RelayCommercialHandler {
	enabled := true
	if len(publicEnabled) > 0 {
		enabled = publicEnabled[0]
	}
	return &RelayCommercialHandler{store: store, publicEnabled: enabled}
}

func (h *RelayCommercialHandler) available() bool {
	return h != nil && h.store != nil && h.publicEnabled
}

func (h *RelayCommercialHandler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.available() {
		writeJSON(w, http.StatusOK, commercialUnavailablePayload())
		return
	}
	uid := UIDFromContext(r.Context())
	summary, err := h.store.GetCommercialSummary(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load commercial summary"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": true,
		"summary": summary,
		"note":    "套餐额度仍在测试中；当前 relay 默认额度和重置周期继续保留。",
	})
}

func (h *RelayCommercialHandler) HandleRedeemInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.available() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial relay package is not enabled"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	summary, err := h.store.RedeemCommercialInvite(UIDFromContext(r.Context()), req.Code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "summary": summary})
}

func commercialUnavailablePayload() map[string]interface{} {
	return map[string]interface{}{
		"enabled": false,
		"summary": map[string]interface{}{
			"plans":           []interface{}{},
			"entitlements":    []interface{}{},
			"grants":          []interface{}{},
			"ledger":          []interface{}{},
			"totals_by_model": map[string]float64{},
			"total_cny":       0,
		},
		"note": "套餐额度功能尚未启用；当前 relay 默认额度和重置周期继续保留。",
	}
}

var commercialSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{1,63}$`)
var commercialCodePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{3,63}$`)

func parseCommercialBudgets(value map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for model, amount := range value {
		model = strings.TrimSpace(model)
		if model == "" || amount <= 0 {
			continue
		}
		out[model] = amount
	}
	return out
}

func (h *AccountAdminHandler) requireCommercialStore(w http.ResponseWriter, r *http.Request) (CommercialStore, bool) {
	if !h.requireLocal(w, r) {
		return nil, false
	}
	if h.commercial == nil {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial store unavailable"})
		return nil, false
	}
	return h.commercial, true
}

func (h *AccountAdminHandler) HandleCommercialPlans(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		plans, err := store.ListCommercialPlans(true)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list plans"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"plans": plans})
	case http.MethodPost:
		var req struct {
			Slug          string             `json:"slug"`
			Name          string             `json:"name"`
			Description   string             `json:"description"`
			MonthlyBudget float64            `json:"monthly_budget_cny"`
			ModelBudgets  map[string]float64 `json:"model_budgets"`
			DurationDays  int                `json:"duration_days"`
			State         int                `json:"state"`
			SortOrder     int                `json:"sort_order"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid plan request"})
			return
		}
		req.Slug = strings.TrimSpace(req.Slug)
		req.Name = strings.TrimSpace(req.Name)
		if !commercialSlugPattern.MatchString(req.Slug) {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid plan slug"})
			return
		}
		if req.Name == "" {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "plan name is required"})
			return
		}
		if req.MonthlyBudget < 0 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "monthly budget must be non-negative"})
			return
		}
		if req.State != 0 && req.State != 1 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported plan state"})
			return
		}
		id, err := store.CreateCommercialPlan(&types.CommercialPlan{
			Slug:          req.Slug,
			Name:          req.Name,
			Description:   req.Description,
			MonthlyBudget: req.MonthlyBudget,
			ModelBudgets:  parseCommercialBudgets(req.ModelBudgets),
			DurationDays:  req.DurationDays,
			State:         req.State,
			SortOrder:     req.SortOrder,
		})
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save plan"})
			return
		}
		plans, _ := store.ListCommercialPlans(true)
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "id": id, "plans": plans})
	default:
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *AccountAdminHandler) HandleCommercialInvites(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		invites, err := store.ListCommercialInviteCodes(80)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list invite codes"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"invites": invites})
	case http.MethodPost:
		var req struct {
			Code           string `json:"code"`
			PlanID         int64  `json:"plan_id"`
			MaxRedemptions int    `json:"max_redemptions"`
			ExpiresAt      string `json:"expires_at"`
			Note           string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid invite request"})
			return
		}
		code := strings.ToUpper(strings.TrimSpace(req.Code))
		if !commercialCodePattern.MatchString(code) {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid invite code"})
			return
		}
		if req.PlanID <= 0 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "plan_id is required"})
			return
		}
		var expiresAt *time.Time
		if strings.TrimSpace(req.ExpiresAt) != "" {
			parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ExpiresAt))
			if err != nil {
				writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at must be RFC3339"})
				return
			}
			expiresAt = &parsed
		}
		id, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{
			Code:           code,
			PlanID:         req.PlanID,
			MaxRedemptions: req.MaxRedemptions,
			ExpiresAt:      expiresAt,
			Note:           req.Note,
		})
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save invite code"})
			return
		}
		invites, _ := store.ListCommercialInviteCodes(80)
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "id": id, "invites": invites})
	default:
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *AccountAdminHandler) HandleCommercialGrant(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		UID           int64   `json:"uid"`
		Model         string  `json:"model"`
		AmountCNY     float64 `json:"amount_cny"`
		ResetDuration string  `json:"reset_duration"`
		Note          string  `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid grant request"})
		return
	}
	if req.UID <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "uid is required"})
		return
	}
	if req.AmountCNY <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_cny must be positive"})
		return
	}
	grant, err := store.GrantCommercialQuota(&types.CommercialQuotaGrant{
		UID:           req.UID,
		GrantType:     "manual",
		Model:         req.Model,
		AmountCNY:     req.AmountCNY,
		ResetDuration: req.ResetDuration,
		Note:          req.Note,
	})
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to grant quota"})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "grant": grant})
}

func (h *AccountAdminHandler) HandleCommercialUserSummary(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid, err := strconvParsePositiveInt64(r.URL.Query().Get("uid"))
	if err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}
	summary, err := store.GetCommercialSummary(uid)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load commercial summary"})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"summary": summary})
}

func strconvParsePositiveInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	var n int64
	if _, err := fmt.Sscan(raw, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid positive int64")
	}
	return n, nil
}
