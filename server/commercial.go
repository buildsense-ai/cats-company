package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
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
	store          CommercialStore
	publicEnabled  bool
	testUIDs       map[int64]bool
	enforceEnabled bool
	enforceUIDs    map[int64]bool
	syncer         *CommercialRelaySyncer
}

type RelayCommercialOptions struct {
	PublicEnabled  bool
	TestUIDs       map[int64]bool
	EnforceEnabled bool
	EnforceUIDs    map[int64]bool
	Syncer         *CommercialRelaySyncer
}

func NewRelayCommercialHandler(store CommercialStore, publicEnabled ...bool) *RelayCommercialHandler {
	enabled := true
	if len(publicEnabled) > 0 {
		enabled = publicEnabled[0]
	}
	return NewRelayCommercialHandlerWithOptions(store, RelayCommercialOptions{PublicEnabled: enabled})
}

func NewRelayCommercialHandlerWithOptions(store CommercialStore, opts RelayCommercialOptions) *RelayCommercialHandler {
	testUIDs := map[int64]bool{}
	for uid, enabled := range opts.TestUIDs {
		if uid > 0 && enabled {
			testUIDs[uid] = true
		}
	}
	enforceUIDs := map[int64]bool{}
	for uid, enabled := range opts.EnforceUIDs {
		if uid > 0 && enabled {
			enforceUIDs[uid] = true
		}
	}
	return &RelayCommercialHandler{
		store:          store,
		publicEnabled:  opts.PublicEnabled,
		testUIDs:       testUIDs,
		enforceEnabled: opts.EnforceEnabled,
		enforceUIDs:    enforceUIDs,
		syncer:         opts.Syncer,
	}
}

func (h *RelayCommercialHandler) available() bool {
	return h != nil && h.store != nil && h.publicEnabled
}

func (h *RelayCommercialHandler) enabledFor(uid int64) bool {
	return h != nil && h.store != nil && (h.publicEnabled || h.testUIDs[uid])
}

func (h *RelayCommercialHandler) rolloutFor(uid int64) string {
	if h == nil {
		return "disabled"
	}
	if h.publicEnabled {
		return "public"
	}
	if h.testUIDs[uid] {
		return "allowlist"
	}
	return "disabled"
}

func (h *RelayCommercialHandler) enforceFor(uid int64) bool {
	return h != nil && (h.enforceEnabled || h.enforceUIDs[uid])
}

func (h *RelayCommercialHandler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if !h.enabledFor(uid) {
		writeJSON(w, http.StatusOK, commercialUnavailablePayload())
		return
	}
	summary, err := h.store.GetCommercialSummary(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load commercial summary"})
		return
	}
	visiblePlans := make([]*types.CommercialPlan, 0, len(summary.Plans))
	for _, plan := range summary.Plans {
		if plan == nil || plan.State != 0 || plan.PriceFen <= 0 {
			continue
		}
		if plan.SaleState == "public" && (h.publicEnabled || h.testUIDs[uid]) {
			visiblePlans = append(visiblePlans, plan)
		}
		if plan.SaleState == "test" && h.testUIDs[uid] {
			visiblePlans = append(visiblePlans, plan)
		}
	}
	summary.Plans = visiblePlans
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":         true,
		"rollout":         h.rolloutFor(uid),
		"enforce_enabled": h.enforceFor(uid),
		"summary":         publicCommercialSummary(summary),
		"note":            "套餐额度内测中；未开启真实接管前，当前 relay 默认额度和重置周期继续保留。",
	})
}

func (h *RelayCommercialHandler) HandleRedeemInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if !h.enabledFor(uid) {
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
	summary, err := h.store.RedeemCommercialInvite(uid, req.Code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invite code could not be redeemed"})
		return
	}
	if h.syncer != nil {
		h.syncer.Enqueue(uid)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "summary": publicCommercialSummary(summary)})
}

type relayCommercialPublicSummary struct {
	Models       []string                           `json:"models"`
	Entitlements []relayCommercialPublicEntitlement `json:"entitlements"`
}

type relayCommercialPublicEntitlement struct {
	PlanName  string     `json:"plan_name,omitempty"`
	State     string     `json:"state"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func publicCommercialSummary(summary *types.CommercialSummary) relayCommercialPublicSummary {
	out := relayCommercialPublicSummary{
		Models:       []string{},
		Entitlements: []relayCommercialPublicEntitlement{},
	}
	if summary == nil {
		return out
	}
	for _, entitlement := range summary.Entitlements {
		if entitlement == nil {
			continue
		}
		out.Entitlements = append(out.Entitlements, relayCommercialPublicEntitlement{
			PlanName:  entitlement.PlanName,
			State:     entitlement.State,
			ExpiresAt: entitlement.ExpiresAt,
		})
	}
	for model, amount := range summary.TotalsByModel {
		model = strings.TrimSpace(model)
		if model != "" && amount > 0 {
			out.Models = append(out.Models, model)
		}
	}
	sort.Strings(out.Models)
	return out
}

func commercialUnavailablePayload() map[string]interface{} {
	return map[string]interface{}{
		"enabled": false,
		"summary": relayCommercialPublicSummary{
			Models:       []string{},
			Entitlements: []relayCommercialPublicEntitlement{},
		},
		"note": "套餐额度功能尚未启用；当前 relay 默认额度和重置周期继续保留。",
	}
}

type commercialRelayBudget struct {
	MaxLimit                float64 `json:"max_limit"`
	CurrentUsage            float64 `json:"current_usage"`
	ResetDuration           string  `json:"reset_duration"`
	LastReset               string  `json:"last_reset,omitempty"`
	PassthroughCurrentUsage float64 `json:"passthrough_current_usage,omitempty"`
	AccountCurrentUsage     float64 `json:"account_current_usage,omitempty"`
}

type commercialRelayModelLimit struct {
	Provider      string                `json:"provider"`
	Model         string                `json:"model"`
	AllowedModels []string              `json:"allowed_models"`
	SharedBudget  bool                  `json:"shared_budget"`
	Budget        commercialRelayBudget `json:"budget"`
}

type commercialRelayLimits struct {
	MonthlyBudget commercialRelayBudget       `json:"monthly_budget"`
	ModelLimits   []commercialRelayModelLimit `json:"model_limits"`
}

type commercialRelayKeySummary struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	State  string `json:"state,omitempty"`
}

type commercialRelayUsageUser struct {
	UID             int64                      `json:"uid"`
	Username        string                     `json:"username"`
	Configured      bool                       `json:"configured"`
	Key             *commercialRelayKeySummary `json:"key,omitempty"`
	Limits          commercialRelayLimits      `json:"limits"`
	GovernanceError string                     `json:"governance_error,omitempty"`
}

type commercialRelayUsageResponse struct {
	Users []commercialRelayUsageUser `json:"users"`
}

type commercialRelayBudgetComparison struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider,omitempty"`
	AllowedModels   []string `json:"allowed_models,omitempty"`
	Status          string   `json:"status"`
	CommercialLimit float64  `json:"commercial_limit_cny"`
	RelayLimit      float64  `json:"relay_limit_cny"`
	RelayUsage      float64  `json:"relay_usage_cny"`
	Remaining       float64  `json:"remaining_cny"`
	Delta           float64  `json:"delta_cny"`
	ResetDuration   string   `json:"reset_duration,omitempty"`
	Syncable        bool     `json:"syncable"`
}

type commercialRelayProviderBudgetUpdate struct {
	Provider      string   `json:"provider"`
	AllowedModels []string `json:"allowed_models"`
	MaxLimit      float64  `json:"max_limit"`
	ResetDuration string   `json:"reset_duration"`
}

type commercialRelayDryRun struct {
	UID                  int64                                 `json:"uid"`
	EnforceEnabled       bool                                  `json:"enforce_enabled"`
	RelayAdminConfigured bool                                  `json:"relay_admin_configured"`
	RelayKeyConfigured   bool                                  `json:"relay_key_configured"`
	RelayUsername        string                                `json:"relay_username,omitempty"`
	RelayKey             *commercialRelayKeySummary            `json:"relay_key,omitempty"`
	RelayGovernanceError string                                `json:"relay_governance_error,omitempty"`
	Summary              *types.CommercialSummary              `json:"summary"`
	Comparisons          []commercialRelayBudgetComparison     `json:"comparisons"`
	ProposedUpdates      []commercialRelayProviderBudgetUpdate `json:"proposed_updates"`
	CanApply             bool                                  `json:"can_apply"`
	Note                 string                                `json:"note"`
}

func (h *AccountAdminHandler) HandleCommercialRelayDryRun(w http.ResponseWriter, r *http.Request) {
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
	dryRun, err := h.buildCommercialRelayDryRun(r.Context(), store, uid)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"dry_run": dryRun})
}

func (h *AccountAdminHandler) HandleCommercialRelaySync(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		UID   int64 `json:"uid"`
		Apply bool  `json:"apply"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UID <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sync request"})
		return
	}
	dryRun, err := h.buildCommercialRelayDryRun(r.Context(), store, req.UID)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if !req.Apply {
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"applied": false, "dry_run": dryRun})
		return
	}
	if !h.commercialRelayEnforcedFor(req.UID) {
		writeAccountAdminJSON(w, http.StatusConflict, map[string]interface{}{
			"error":   "commercial relay enforce is disabled",
			"applied": false,
			"dry_run": dryRun,
		})
		return
	}
	if h.relayAdmin == nil {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "relay admin is not configured"})
		return
	}
	if h.commercialRelaySyncer != nil {
		updates, err := h.commercialRelaySyncer.SyncUID(r.Context(), req.UID)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		updated, err := h.buildCommercialRelayDryRun(r.Context(), store, req.UID)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"applied": true, "updates": updates})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"applied": true, "updates": updates, "dry_run": updated})
		return
	}
	if len(dryRun.ProposedUpdates) == 0 {
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"applied": false, "dry_run": dryRun, "note": "no syncable model budgets"})
		return
	}
	var relayResp map[string]interface{}
	err = h.relayAdmin.Do(
		r.Context(),
		http.MethodPost,
		fmt.Sprintf("/internal/users/%d/key/limits", req.UID),
		map[string]interface{}{"provider_config_budgets": dryRun.ProposedUpdates},
		&relayResp,
	)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	updated, err := h.buildCommercialRelayDryRun(r.Context(), store, req.UID)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"applied": true, "relay": relayResp})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"applied": true, "dry_run": updated})
}

func (h *AccountAdminHandler) buildCommercialRelayDryRun(ctx context.Context, store CommercialStore, uid int64) (*commercialRelayDryRun, error) {
	summary, err := store.GetCommercialSummary(uid)
	if err != nil {
		return nil, fmt.Errorf("load commercial summary: %w", err)
	}
	var relayUser *commercialRelayUsageUser
	if h.relayAdmin != nil {
		user, err := h.fetchCommercialRelayUsage(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("load relay usage: %w", err)
		}
		relayUser = user
	}
	dryRun := compareCommercialRelayBudgets(uid, summary, relayUser)
	if managedStore, ok := store.(CommercialRelayManagedStore); ok {
		managed, managedErr := managedStore.ListCommercialManagedRelayBudgets(uid)
		if managedErr == nil {
			for _, item := range managed {
				if item == nil || summary.TotalsByModel[item.Model] > 0 {
					continue
				}
				needsUpdate := true
				for index := range dryRun.Comparisons {
					row := &dryRun.Comparisons[index]
					if row.Model == item.Model && commercialManagedBudgetKey(row.Provider, row.AllowedModels) == commercialManagedBudgetKey(item.Provider, item.AllowedModels) {
						if nearlyEqual(row.RelayLimit, commercialRelayBlockedLimit) {
							row.Status = "managed_blocked"
							needsUpdate = false
						} else {
							row.Status = "managed_expired"
						}
					}
				}
				if needsUpdate {
					dryRun.ProposedUpdates = append(dryRun.ProposedUpdates, commercialRelayProviderBudgetUpdate{
						Provider: item.Provider, AllowedModels: append([]string(nil), item.AllowedModels...),
						MaxLimit: commercialRelayBlockedLimit, ResetDuration: defaultRelayResetDuration(item.ResetDuration),
					})
				}
			}
			dryRun.CanApply = len(dryRun.ProposedUpdates) > 0
		}
	}
	dryRun.EnforceEnabled = h.commercialRelayEnforcedFor(uid)
	dryRun.RelayAdminConfigured = h.relayAdmin != nil
	if h.relayAdmin == nil {
		dryRun.Note = "relay admin is not configured; only commercial ledger was loaded"
	} else if relayUser == nil {
		dryRun.Note = "relay key was not found; create the user's relay key before enforcing commercial quota"
	} else if !dryRun.EnforceEnabled {
		dryRun.Note = "dry-run only; enable CATS_RELAY_COMMERCIAL_ENFORCE_ENABLED=1 or include this uid in CATS_RELAY_COMMERCIAL_ENFORCE_UIDS before applying to relay-admin"
	} else {
		dryRun.Note = "enforce is enabled; apply will write provider_config_budgets to relay-admin"
	}
	return dryRun, nil
}

func (h *AccountAdminHandler) commercialRelayEnforcedFor(uid int64) bool {
	return h != nil && (h.commercialEnforceEnabled || h.commercialEnforceUIDs[uid])
}

func (h *AccountAdminHandler) fetchCommercialRelayUsage(ctx context.Context, uid int64) (*commercialRelayUsageUser, error) {
	return fetchRelayUsageForUID(ctx, h.relayAdmin, uid)
}

func compareCommercialRelayBudgets(uid int64, summary *types.CommercialSummary, relayUser *commercialRelayUsageUser) *commercialRelayDryRun {
	dryRun := &commercialRelayDryRun{UID: uid, Summary: summary}
	proposedByKey := map[string]commercialRelayProviderBudgetUpdate{}
	if summary == nil {
		summary = &types.CommercialSummary{UID: uid, TotalsByModel: map[string]float64{}}
		dryRun.Summary = summary
	}
	relayByModel := map[string][]commercialRelayModelLimit{}
	if relayUser != nil {
		dryRun.RelayKeyConfigured = relayUser.Configured
		dryRun.RelayUsername = relayUser.Username
		dryRun.RelayKey = relayUser.Key
		dryRun.RelayGovernanceError = relayUser.GovernanceError
		for _, limit := range relayUser.Limits.ModelLimits {
			model := strings.TrimSpace(limit.Model)
			if model == "" || model == "*" {
				continue
			}
			relayByModel[model] = append(relayByModel[model], limit)
		}
	}
	commercialModels := map[string]bool{}
	for model, amount := range summary.TotalsByModel {
		model = strings.TrimSpace(model)
		if model == "" || amount <= 0 {
			continue
		}
		commercialModels[model] = true
		limits := relayByModel[model]
		limit, ok := bestCommercialRelayLimit(limits)
		row := relayComparisonForModel(model, commercialLimitForRelayConfig(summary, amount, limit), limit, ok)

		var aliasRowsNeedingSync []commercialRelayBudgetComparison
		for _, aliasLimit := range limits {
			aliasRow := relayComparisonForModel(model, commercialLimitForRelayConfig(summary, amount, aliasLimit), aliasLimit, true)
			if !commercialRelayShouldSync(aliasRow) {
				continue
			}
			aliasRowsNeedingSync = append(aliasRowsNeedingSync, aliasRow)
			update := commercialRelayProviderBudgetUpdate{
				Provider:      aliasRow.Provider,
				AllowedModels: aliasRow.AllowedModels,
				MaxLimit:      aliasRow.CommercialLimit,
				ResetDuration: defaultRelayResetDuration(aliasRow.ResetDuration),
			}
			proposedByKey[commercialManagedBudgetKey(update.Provider, update.AllowedModels)] = update
		}
		if len(aliasRowsNeedingSync) > 0 && row.Status == "match" {
			row.Status = "mismatch"
			row.Delta = aliasRowsNeedingSync[0].Delta
		}
		dryRun.Comparisons = append(dryRun.Comparisons, row)
	}
	for model, limits := range relayByModel {
		if commercialModels[model] {
			continue
		}
		for _, limit := range limits {
			if limit.Budget.MaxLimit <= 0 {
				continue
			}
			dryRun.Comparisons = append(dryRun.Comparisons, commercialRelayBudgetComparison{
				Model:         model,
				Provider:      limit.Provider,
				AllowedModels: limit.AllowedModels,
				Status:        "relay_only",
				RelayLimit:    limit.Budget.MaxLimit,
				RelayUsage:    limit.Budget.CurrentUsage,
				Remaining:     math.Max(0, limit.Budget.MaxLimit-limit.Budget.CurrentUsage),
				Delta:         -limit.Budget.MaxLimit,
				ResetDuration: limit.Budget.ResetDuration,
				Syncable:      true,
			})
		}
	}
	for _, update := range proposedByKey {
		dryRun.ProposedUpdates = append(dryRun.ProposedUpdates, update)
	}
	sort.Slice(dryRun.ProposedUpdates, func(i, j int) bool {
		return commercialManagedBudgetKey(dryRun.ProposedUpdates[i].Provider, dryRun.ProposedUpdates[i].AllowedModels) < commercialManagedBudgetKey(dryRun.ProposedUpdates[j].Provider, dryRun.ProposedUpdates[j].AllowedModels)
	})
	dryRun.CanApply = len(dryRun.ProposedUpdates) > 0
	return dryRun
}

func commercialLimitForRelayConfig(summary *types.CommercialSummary, fallback float64, limit commercialRelayModelLimit) float64 {
	if summary == nil || (!limit.SharedBudget && len(limit.AllowedModels) <= 1) {
		return fallback
	}
	total := 0.0
	seen := map[string]bool{}
	for _, model := range limit.AllowedModels {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" || seen[model] {
			continue
		}
		seen[model] = true
		total += summary.TotalsByModel[model]
	}
	if total > 0 {
		return total
	}
	return fallback
}

func bestCommercialRelayLimit(limits []commercialRelayModelLimit) (commercialRelayModelLimit, bool) {
	if len(limits) == 0 {
		return commercialRelayModelLimit{}, false
	}
	best := limits[0]
	for _, limit := range limits[1:] {
		if limit.Budget.MaxLimit > best.Budget.MaxLimit {
			best = limit
		}
	}
	return best, true
}

func commercialRelayShouldSync(row commercialRelayBudgetComparison) bool {
	if !row.Syncable {
		return false
	}
	if row.Status == "mismatch" {
		return true
	}
	if row.Status == "over_limit" {
		return math.Abs(row.RelayLimit-row.CommercialLimit) > 0.000001
	}
	return false
}

func relayComparisonForModel(model string, amount float64, limit commercialRelayModelLimit, found bool) commercialRelayBudgetComparison {
	row := commercialRelayBudgetComparison{
		Model:           model,
		Status:          "missing_relay_budget",
		CommercialLimit: amount,
		Delta:           amount,
	}
	if !found {
		return row
	}
	row.Provider = limit.Provider
	row.AllowedModels = limit.AllowedModels
	row.RelayLimit = limit.Budget.MaxLimit
	row.RelayUsage = limit.Budget.CurrentUsage
	row.Remaining = math.Max(0, limit.Budget.MaxLimit-limit.Budget.CurrentUsage)
	row.Delta = amount - limit.Budget.MaxLimit
	row.ResetDuration = limit.Budget.ResetDuration
	row.Syncable = row.Provider != "" && len(row.AllowedModels) > 0
	if nearlyEqual(amount, limit.Budget.MaxLimit) {
		row.Status = "match"
	} else {
		row.Status = "mismatch"
	}
	if amount > 0 && limit.Budget.CurrentUsage > amount+0.000001 {
		row.Status = "over_limit"
		row.Remaining = 0
	}
	if !row.Syncable {
		row.Status = "missing_relay_budget"
	}
	return row
}

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.000001
}

func defaultRelayResetDuration(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "1M"
	}
	return value
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
			PriceFen      int64              `json:"price_fen"`
			Currency      string             `json:"currency"`
			SaleState     string             `json:"sale_state"`
			PurchaseLimit int                `json:"purchase_limit"`
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
		if req.PriceFen < 0 || req.PurchaseLimit < 0 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "price and purchase limit must be non-negative"})
			return
		}
		req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
		if req.Currency == "" {
			req.Currency = "CNY"
		}
		if req.Currency != "CNY" {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "only CNY plans are supported"})
			return
		}
		req.SaleState = strings.ToLower(strings.TrimSpace(req.SaleState))
		if req.SaleState == "" {
			req.SaleState = "hidden"
		}
		if req.SaleState != "hidden" && req.SaleState != "test" && req.SaleState != "public" {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported sale state"})
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
			PriceFen:      req.PriceFen,
			Currency:      req.Currency,
			SaleState:     req.SaleState,
			PurchaseLimit: req.PurchaseLimit,
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
	if h.commercialRelaySyncer != nil {
		h.commercialRelaySyncer.Enqueue(req.UID)
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

func (h *AccountAdminHandler) HandleCommercialOrders(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	paymentStore, ok := store.(CommercialPaymentStore)
	if !ok {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial payment store unavailable"})
		return
	}
	uid := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("uid")); raw != "" {
		parsed, err := strconvParsePositiveInt64(raw)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
			return
		}
		uid = parsed
	}
	_, _ = paymentStore.CloseExpiredCommercialOrders(100)
	orders, err := paymentStore.ListCommercialOrders(uid, 100)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list commercial orders"})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"orders": orders})
}

func strconvParsePositiveInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	var n int64
	if _, err := fmt.Sscan(raw, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid positive int64")
	}
	return n, nil
}
