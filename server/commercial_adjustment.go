package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialAdjustmentIncrease   = "increase"
	commercialAdjustmentDecrease   = "decrease"
	commercialAdjustmentChangePlan = "change_plan"
	commercialAdjustmentResetCycle = "reset_cycle"
)

type commercialAccountAdjustmentStore interface {
	ApplyCommercialAccountAdjustment(*types.CommercialAccountAdjustment) (*types.CommercialAccountAdjustmentResult, error)
	RecordCommercialCycleReset(uid int64, operationID, note string, effectiveAt time.Time) (bool, time.Time, error)
}

type commercialAdjustmentRequest struct {
	UID              int64    `json:"uid"`
	Action           string   `json:"action"`
	AmountCNY        float64  `json:"amount_cny"`
	PlanID           int64    `json:"plan_id"`
	ExpectedTotalCNY *float64 `json:"expected_total_cny"`
	OperationID      string   `json:"operation_id"`
	Note             string   `json:"note"`
	Preview          bool     `json:"preview"`
}

type commercialAdjustmentPreview struct {
	UID              int64                 `json:"uid"`
	Action           string                `json:"action"`
	CurrentTotalCNY  float64               `json:"current_total_cny"`
	NextTotalCNY     float64               `json:"next_total_cny"`
	RelayUsageCNY    float64               `json:"relay_usage_cny"`
	NextRemainingCNY float64               `json:"next_remaining_cny"`
	UsageWillReset   bool                  `json:"usage_will_reset"`
	ExpiresAt        *time.Time            `json:"expires_at,omitempty"`
	CurrentPlan      *types.CommercialPlan `json:"current_plan,omitempty"`
	TargetPlan       *types.CommercialPlan `json:"target_plan,omitempty"`
	RelayConfigured  bool                  `json:"relay_configured"`
	EnforceEnabled   bool                  `json:"enforce_enabled"`
	CanApply         bool                  `json:"can_apply"`
	Warnings         []string              `json:"warnings,omitempty"`
}

func (h *AccountAdminHandler) HandleCommercialAdjustment(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req commercialAdjustmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid adjustment request"})
		return
	}
	if err := normalizeCommercialAdjustmentRequest(&req); err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now().UTC()
	preview, err := h.buildCommercialAdjustmentPreview(r.Context(), store, &req, now)
	if err != nil {
		writeCommercialAdjustmentError(w, err)
		return
	}
	if req.Preview {
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"preview": preview})
		return
	}
	if req.ExpectedTotalCNY == nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "expected_total_cny is required after preview"})
		return
	}
	if !nearlyEqual(preview.CurrentTotalCNY, *req.ExpectedTotalCNY) {
		writeAccountAdminJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "共享额度在预览后发生变化，请刷新后重试", "code": "stale_total", "preview": preview,
		})
		return
	}
	if !preview.CanApply {
		writeAccountAdminJSON(w, http.StatusConflict, map[string]interface{}{
			"error":   "adjustment cannot be applied",
			"preview": preview,
		})
		return
	}
	adjustmentStore, ok := store.(commercialAccountAdjustmentStore)
	if !ok {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial adjustment store unavailable"})
		return
	}

	var result *types.CommercialAccountAdjustmentResult
	if req.Action == commercialAdjustmentResetCycle {
		applied, resetAt, err := adjustmentStore.RecordCommercialCycleReset(req.UID, req.OperationID, req.Note, now)
		if err != nil {
			writeCommercialAdjustmentError(w, err)
			return
		}
		result = &types.CommercialAccountAdjustmentResult{
			Action: req.Action, OperationID: req.OperationID, Applied: applied,
			PreviousTotalCNY: preview.CurrentTotalCNY, NextTotalCNY: preview.NextTotalCNY,
			CycleStartedAt: &resetAt, ExpiresAt: preview.ExpiresAt,
		}
	} else {
		result, err = adjustmentStore.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{
			UID: req.UID, Action: req.Action, AmountCNY: req.AmountCNY, PlanID: req.PlanID,
			ExpectedTotalCNY: req.ExpectedTotalCNY, OperationID: req.OperationID,
			Note: req.Note, EffectiveAt: now,
		})
		if err != nil {
			writeCommercialAdjustmentError(w, err)
			return
		}
	}

	if err := h.applyCommercialAdjustmentToRelay(r.Context(), req.UID, req.Action, result); err != nil {
		if h.commercialRelaySyncer != nil {
			h.commercialRelaySyncer.Enqueue(req.UID)
		}
		writeAccountAdminJSON(w, http.StatusAccepted, map[string]interface{}{
			"ok": true, "applied": true, "synced": false, "result": result,
			"error": "账本已更新，执行层同步失败，系统会继续重试：" + err.Error(),
		})
		return
	}
	verifyReq := commercialAdjustmentRequest{UID: req.UID, Action: commercialAdjustmentResetCycle, Preview: true}
	verified, verifyErr := h.buildCommercialAdjustmentPreview(r.Context(), store, &verifyReq, time.Now().UTC())
	response := map[string]interface{}{"ok": true, "applied": true, "synced": true, "result": result}
	if verifyErr == nil {
		response["verified"] = verified
	}
	writeAccountAdminJSON(w, http.StatusOK, response)
}

func normalizeCommercialAdjustmentRequest(req *commercialAdjustmentRequest) error {
	if req == nil || req.UID <= 0 {
		return fmt.Errorf("uid is required")
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	switch req.Action {
	case commercialAdjustmentIncrease, commercialAdjustmentDecrease:
		if req.AmountCNY <= 0 || req.AmountCNY > 99999999 || math.IsNaN(req.AmountCNY) || math.IsInf(req.AmountCNY, 0) {
			return fmt.Errorf("amount_cny must be a positive supported amount")
		}
	case commercialAdjustmentChangePlan:
		if req.PlanID <= 0 {
			return fmt.Errorf("plan_id is required")
		}
	case commercialAdjustmentResetCycle:
	default:
		return fmt.Errorf("unsupported adjustment action")
	}
	req.Note = strings.TrimSpace(req.Note)
	if !req.Preview && req.Note == "" {
		return fmt.Errorf("adjustment reason is required")
	}
	req.OperationID = strings.TrimSpace(req.OperationID)
	if !req.Preview && (req.OperationID == "" || len(req.OperationID) > 128) {
		return fmt.Errorf("operation_id is required")
	}
	return nil
}

func (h *AccountAdminHandler) buildCommercialAdjustmentPreview(ctx context.Context, store CommercialStore, req *commercialAdjustmentRequest, now time.Time) (*commercialAdjustmentPreview, error) {
	summary, err := store.GetCommercialSummary(req.UID)
	if err != nil {
		return nil, fmt.Errorf("load commercial summary: %w", err)
	}
	if summary == nil || len(summary.Entitlements) == 0 {
		return nil, &types.CommercialAdjustmentError{Code: "no_package", Message: "用户当前没有有效套餐"}
	}
	plans, err := store.ListCommercialPlans(true)
	if err != nil {
		return nil, fmt.Errorf("load commercial plans: %w", err)
	}
	planByID := make(map[int64]*types.CommercialPlan, len(plans))
	for _, plan := range plans {
		if plan != nil {
			planByID[plan.ID] = plan
		}
	}
	preview := &commercialAdjustmentPreview{
		UID: req.UID, Action: req.Action, CurrentTotalCNY: summary.TotalCNY,
		NextTotalCNY: summary.TotalCNY, EnforceEnabled: h.commercialRelayEnforcedFor(req.UID),
	}
	for _, entitlement := range summary.Entitlements {
		if entitlement == nil || entitlement.State != "active" {
			continue
		}
		if preview.CurrentPlan == nil {
			preview.CurrentPlan = planByID[entitlement.PlanID]
			preview.ExpiresAt = entitlement.ExpiresAt
		}
		if entitlement.ExpiresAt != nil && (preview.ExpiresAt == nil || entitlement.ExpiresAt.After(*preview.ExpiresAt)) {
			preview.ExpiresAt = entitlement.ExpiresAt
		}
	}

	switch req.Action {
	case commercialAdjustmentIncrease:
		preview.NextTotalCNY += req.AmountCNY
	case commercialAdjustmentDecrease:
		preview.NextTotalCNY -= req.AmountCNY
		if preview.NextTotalCNY < -0.0000005 {
			preview.Warnings = append(preview.Warnings, "共享额度不能减到零以下")
		}
	case commercialAdjustmentChangePlan:
		target := planByID[req.PlanID]
		if target == nil || target.State != 0 {
			return nil, &types.CommercialAdjustmentError{Code: "invalid_plan", Message: "所选套餐不可用"}
		}
		if commercialPlanQuotaTotal(target) <= 0 {
			return nil, &types.CommercialAdjustmentError{Code: "invalid_plan", Message: "所选套餐没有可用额度"}
		}
		if preview.CurrentPlan != nil && preview.CurrentPlan.ID == target.ID {
			return nil, &types.CommercialAdjustmentError{Code: "same_plan", Message: "用户已经在使用该套餐；如需刷新用量请选择重置周期"}
		}
		preview.TargetPlan = target
		preview.NextTotalCNY = commercialPreservedQuota(summary) + commercialPlanQuotaTotal(target)
		preview.ExpiresAt = commercialPreviewPlanExpiry(target, now)
		preview.UsageWillReset = true
	case commercialAdjustmentResetCycle:
		preview.UsageWillReset = true
	}

	if h.relayAdmin != nil {
		relayUser, relayErr := fetchRelayLimitsForUID(ctx, h.relayAdmin, req.UID)
		if relayErr != nil {
			preview.Warnings = append(preview.Warnings, "执行层读取失败："+relayErr.Error())
		} else if relayUser != nil {
			preview.RelayConfigured = relayUser.Configured
			preview.RelayUsageCNY = relayUser.Limits.MonthlyBudget.CurrentUsage
		}
	}
	nextUsage := preview.RelayUsageCNY
	if preview.UsageWillReset {
		nextUsage = 0
	}
	preview.NextRemainingCNY = math.Max(0, preview.NextTotalCNY-nextUsage)
	if req.Action == commercialAdjustmentDecrease && preview.NextTotalCNY+0.0000005 < preview.RelayUsageCNY {
		preview.Warnings = append(preview.Warnings, "调整后的共享额度低于当前已用额度")
	}
	if !preview.EnforceEnabled {
		preview.Warnings = append(preview.Warnings, "该用户尚未启用商业共享额度执行")
	}
	if !preview.RelayConfigured {
		preview.Warnings = append(preview.Warnings, "该用户尚未配置 Relay Key")
	}
	preview.CanApply = len(preview.Warnings) == 0
	return preview, nil
}

func commercialPlanQuotaTotal(plan *types.CommercialPlan) float64 {
	if plan == nil {
		return 0
	}
	total := plan.MonthlyBudget
	for _, amount := range plan.ModelBudgets {
		if amount > 0 {
			total += amount
		}
	}
	return total
}

func commercialPreservedQuota(summary *types.CommercialSummary) float64 {
	total := 0.0
	if summary == nil {
		return total
	}
	for _, grant := range summary.Grants {
		if grant == nil || grant.PlanID != 0 {
			continue
		}
		amount := grant.AmountCNY
		if strings.EqualFold(strings.TrimSpace(grant.GrantType), "adjustment_debit") {
			amount = -amount
		}
		total += amount
	}
	return total
}

func commercialPreviewPlanExpiry(plan *types.CommercialPlan, now time.Time) *time.Time {
	if plan == nil || plan.Slug == "catsco-free" || plan.Slug == "catsco-legacy-custom" {
		return nil
	}
	days := plan.DurationDays
	if days <= 0 {
		days = 30
	}
	expiresAt := now.AddDate(0, 0, days)
	return &expiresAt
}

func (h *AccountAdminHandler) applyCommercialAdjustmentToRelay(ctx context.Context, uid int64, action string, result *types.CommercialAccountAdjustmentResult) error {
	if h.relayAdmin == nil || h.commercialRelaySyncer == nil {
		return fmt.Errorf("commercial Relay sync is not configured")
	}
	if action == commercialAdjustmentResetCycle || action == commercialAdjustmentChangePlan {
		if result == nil || result.CycleStartedAt == nil {
			return fmt.Errorf("commercial cycle start is unavailable")
		}
		if err := h.resetCommercialRelayCycle(ctx, uid, *result.CycleStartedAt); err != nil {
			return err
		}
	}
	_, err := h.commercialRelaySyncer.SyncUID(ctx, uid)
	return err
}

func (h *AccountAdminHandler) resetCommercialRelayCycle(ctx context.Context, uid int64, resetAt time.Time) error {
	resetAt = resetAt.UTC()
	current, err := fetchRelayLimitsForUID(ctx, h.relayAdmin, uid)
	if err != nil {
		return fmt.Errorf("load Relay cycle: %w", err)
	}
	stamp := resetAt.Format(time.RFC3339Nano)
	if current != nil {
		if currentAt, ok := parseCommercialRelayTime(current.UsageWindowStart); ok && !currentAt.Before(resetAt) {
			return nil
		}
	}
	var response map[string]interface{}
	if err := h.relayAdmin.Do(ctx, http.MethodPost, fmt.Sprintf("/internal/users/%d/key/limits", uid), map[string]interface{}{
		"reset_budget_usage": true,
		"usage_window_start": stamp,
	}, &response); err != nil {
		return fmt.Errorf("reset Relay cycle: %w", err)
	}
	verified, err := fetchRelayLimitsForUID(ctx, h.relayAdmin, uid)
	if err != nil {
		return fmt.Errorf("verify Relay cycle: %w", err)
	}
	if verified == nil || !verified.Configured || !sameCommercialRelayTimestamp(verified.UsageWindowStart, stamp) {
		return fmt.Errorf("Relay cycle readback did not match")
	}
	return nil
}

func writeCommercialAdjustmentError(w http.ResponseWriter, err error) {
	var adjustmentErr *types.CommercialAdjustmentError
	if errors.As(err, &adjustmentErr) {
		status := http.StatusBadRequest
		if adjustmentErr.Code == "not_found" {
			status = http.StatusNotFound
		} else if adjustmentErr.Code == "stale_total" || adjustmentErr.Code == "insufficient_quota" || adjustmentErr.Code == "same_plan" {
			status = http.StatusConflict
		}
		writeAccountAdminJSON(w, status, map[string]string{"error": adjustmentErr.Message, "code": adjustmentErr.Code})
		return
	}
	writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "commercial adjustment failed"})
}
