package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const commercialRelayBlockedLimit = 0.000001

type CommercialRelayManagedStore interface {
	GetCommercialSummary(uid int64) (*types.CommercialSummary, error)
	ListCommercialManagedRelayBudgets(uid int64) ([]*types.CommercialManagedRelayBudget, error)
	ReplaceCommercialManagedRelayBudgets(uid int64, budgets []*types.CommercialManagedRelayBudget) error
	CommercialRelaySyncRequired(uid int64) (bool, error)
	ListCommercialReconcileUIDs(afterUID int64, limit int) ([]int64, error)
}

type CommercialRelaySyncerOptions struct {
	EnforceEnabled bool
	EnforceUIDs    map[int64]bool
	Interval       time.Duration
}

type CommercialRelaySyncer struct {
	store             CommercialRelayManagedStore
	relayAdmin        *RelayAdminClient
	enforceEnabled    bool
	enforceUIDs       map[int64]bool
	interval          time.Duration
	queue             chan int64
	reconcileAfterUID int64
}

func NewCommercialRelaySyncer(store CommercialRelayManagedStore, relayAdmin *RelayAdminClient, opts CommercialRelaySyncerOptions) *CommercialRelaySyncer {
	interval := opts.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &CommercialRelaySyncer{
		store:          store,
		relayAdmin:     relayAdmin,
		enforceEnabled: opts.EnforceEnabled,
		enforceUIDs:    copyCommercialUIDSet(opts.EnforceUIDs),
		interval:       interval,
		queue:          make(chan int64, 256),
	}
}

func (s *CommercialRelaySyncer) EnforcedFor(uid int64) bool {
	return s != nil && uid > 0 && (s.enforceEnabled || s.enforceUIDs[uid])
}

func (s *CommercialRelaySyncer) Enqueue(uid int64) {
	if s == nil || uid <= 0 || s.store == nil || s.relayAdmin == nil {
		return
	}
	select {
	case s.queue <- uid:
	default:
		log.Printf("commercial relay sync queue is full; uid=%d will be retried by reconciliation", uid)
	}
}

func (s *CommercialRelaySyncer) Start(ctx context.Context) {
	if s == nil || s.store == nil || s.relayAdmin == nil {
		return
	}
	go s.run(ctx)
}

func (s *CommercialRelaySyncer) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case uid := <-s.queue:
			s.syncWithTimeout(ctx, uid)
		case <-ticker.C:
			uids, err := s.store.ListCommercialReconcileUIDs(s.reconcileAfterUID, 50)
			if err != nil {
				log.Printf("commercial relay reconciliation list failed: %v", err)
				continue
			}
			for _, uid := range uids {
				if !s.EnforcedFor(uid) {
					managed, managedErr := s.store.ListCommercialManagedRelayBudgets(uid)
					if managedErr != nil {
						log.Printf("commercial relay reconciliation state failed uid=%d: %v", uid, managedErr)
						continue
					}
					required, requiredErr := s.store.CommercialRelaySyncRequired(uid)
					if requiredErr != nil {
						log.Printf("commercial relay entitlement state failed uid=%d: %v", uid, requiredErr)
						continue
					}
					if len(managed) == 0 && !required {
						continue
					}
				}
				s.syncWithTimeout(ctx, uid)
			}
			if len(uids) < 50 {
				s.reconcileAfterUID = 0
			} else {
				s.reconcileAfterUID = uids[len(uids)-1]
			}
		}
	}
}

func (s *CommercialRelaySyncer) syncWithTimeout(parent context.Context, uid int64) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if _, err := s.SyncUID(ctx, uid); err != nil {
		log.Printf("commercial relay sync failed uid=%d: %v", uid, err)
	}
}

func (s *CommercialRelaySyncer) SyncUID(ctx context.Context, uid int64) ([]commercialRelayProviderBudgetUpdate, error) {
	if s == nil || s.store == nil || s.relayAdmin == nil {
		return nil, fmt.Errorf("commercial relay sync is not configured")
	}
	managed, err := s.store.ListCommercialManagedRelayBudgets(uid)
	if err != nil {
		return nil, fmt.Errorf("load managed relay budgets: %w", err)
	}
	required, err := s.store.CommercialRelaySyncRequired(uid)
	if err != nil {
		return nil, fmt.Errorf("load commercial relay entitlement state: %w", err)
	}
	if !s.EnforcedFor(uid) && len(managed) == 0 && !required {
		return nil, fmt.Errorf("commercial relay enforce is disabled")
	}
	summary, err := s.store.GetCommercialSummary(uid)
	if err != nil {
		return nil, fmt.Errorf("load commercial summary: %w", err)
	}
	if summary == nil {
		return nil, fmt.Errorf("commercial summary is unavailable")
	}
	relayUser, err := fetchRelayLimitsForUID(ctx, s.relayAdmin, uid)
	if err != nil {
		return nil, fmt.Errorf("load relay usage: %w", err)
	}
	if relayUser == nil || !relayUser.Configured {
		return nil, fmt.Errorf("relay key is not configured")
	}
	updates, nextManaged := commercialRelayManagedPlan(uid, summary, relayUser, managed)
	if len(updates) > 0 {
		var response map[string]interface{}
		if err := s.relayAdmin.Do(ctx, http.MethodPost, fmt.Sprintf("/internal/users/%d/key/limits", uid), map[string]interface{}{
			"provider_config_budgets": updates,
		}, &response); err != nil {
			return nil, fmt.Errorf("write relay budgets: %w", err)
		}
		verified, err := fetchRelayLimitsForUID(ctx, s.relayAdmin, uid)
		if err != nil {
			return nil, fmt.Errorf("verify relay budgets: %w", err)
		}
		if err := verifyCommercialRelayUpdates(updates, verified); err != nil {
			return nil, err
		}
	}
	if err := s.store.ReplaceCommercialManagedRelayBudgets(uid, nextManaged); err != nil {
		return nil, fmt.Errorf("save managed relay budgets: %w", err)
	}
	return updates, nil
}

func (s *CommercialRelaySyncer) ValidatePurchase(ctx context.Context, uid int64, plan *types.CommercialPlan) error {
	if s == nil || s.store == nil || s.relayAdmin == nil || !s.EnforcedFor(uid) {
		return fmt.Errorf("commercial relay sync is not enabled for uid")
	}
	if plan == nil {
		return fmt.Errorf("commercial plan is required")
	}
	if !commercialModelBudgetsConfigured(plan.ModelBudgets) {
		return fmt.Errorf("commercial plan has no relay model budgets")
	}
	relayUser, err := fetchRelayLimitsForUID(ctx, s.relayAdmin, uid)
	if err != nil {
		return fmt.Errorf("load relay usage: %w", err)
	}
	return validateCommercialRelayModels(plan.ModelBudgets, relayUser)
}

func commercialModelBudgetsConfigured(budgets map[string]float64) bool {
	for model, amount := range budgets {
		model = strings.TrimSpace(model)
		if model != "" && model != "*" && amount > 0 {
			return true
		}
	}
	return false
}

func validateCommercialRelayModels(budgets map[string]float64, relayUser *commercialRelayUsageUser) error {
	if relayUser == nil || !relayUser.Configured {
		return fmt.Errorf("relay key is not configured")
	}
	if relayUser.Key != nil && !strings.EqualFold(strings.TrimSpace(relayUser.Key.State), "active") {
		return fmt.Errorf("relay key is not active")
	}
	if strings.TrimSpace(relayUser.GovernanceError) != "" {
		return fmt.Errorf("relay governance is unavailable: %s", relayUser.GovernanceError)
	}
	for model, amount := range budgets {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" || amount <= 0 {
			continue
		}
		matched := false
		for _, limit := range relayUser.Limits.ModelLimits {
			if strings.TrimSpace(limit.Model) == model && strings.TrimSpace(limit.Provider) != "" && len(limit.AllowedModels) > 0 {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("relay model budget is not configured for %s", model)
		}
	}
	return nil
}

func verifyCommercialRelayUpdates(updates []commercialRelayProviderBudgetUpdate, relayUser *commercialRelayUsageUser) error {
	if relayUser == nil || !relayUser.Configured {
		return fmt.Errorf("relay budget verification failed: relay key is not configured")
	}
	if relayUser.Key != nil && !strings.EqualFold(strings.TrimSpace(relayUser.Key.State), "active") {
		return fmt.Errorf("relay budget verification failed: relay key is not active")
	}
	if strings.TrimSpace(relayUser.GovernanceError) != "" {
		return fmt.Errorf("relay budget verification failed: %s", relayUser.GovernanceError)
	}
	for _, update := range updates {
		matched := false
		for _, limit := range relayUser.Limits.ModelLimits {
			if commercialManagedBudgetKey(limit.Provider, limit.AllowedModels) == commercialManagedBudgetKey(update.Provider, update.AllowedModels) &&
				nearlyEqual(limit.Budget.MaxLimit, update.MaxLimit) &&
				defaultRelayResetDuration(limit.Budget.ResetDuration) == defaultRelayResetDuration(update.ResetDuration) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("relay budget verification failed for provider %s", update.Provider)
		}
	}
	return nil
}

func commercialRelayManagedPlan(uid int64, summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, managed []*types.CommercialManagedRelayBudget) ([]commercialRelayProviderBudgetUpdate, []*types.CommercialManagedRelayBudget) {
	totals := map[string]float64{}
	if summary != nil {
		for model, amount := range summary.TotalsByModel {
			model = strings.TrimSpace(model)
			if model != "" && model != "*" && amount > 0 {
				totals[model] = amount
			}
		}
	}
	relayByModel := map[string][]commercialRelayModelLimit{}
	if relayUser != nil {
		for _, limit := range relayUser.Limits.ModelLimits {
			model := strings.TrimSpace(limit.Model)
			if model != "" && model != "*" && limit.Provider != "" && len(limit.AllowedModels) > 0 {
				relayByModel[model] = append(relayByModel[model], limit)
			}
		}
	}
	managedByModel := map[string][]*types.CommercialManagedRelayBudget{}
	for _, item := range managed {
		if item != nil {
			managedByModel[item.Model] = append(managedByModel[item.Model], item)
		}
	}

	currentByKey := map[string]commercialRelayModelLimit{}
	for _, limits := range relayByModel {
		for _, limit := range limits {
			currentByKey[commercialManagedBudgetKey(limit.Provider, limit.AllowedModels)] = limit
		}
	}
	updatesByKey := map[string]commercialRelayProviderBudgetUpdate{}
	nextByKey := map[string]*types.CommercialManagedRelayBudget{}
	configByKey := map[string]*types.CommercialManagedRelayBudget{}
	desiredByKey := map[string]float64{}
	for model, amount := range totals {
		candidateByKey := map[string]*types.CommercialManagedRelayBudget{}
		for _, item := range managedByModel[model] {
			if _, found := findCommercialRelayLimit(relayByModel[model], item.Provider, item.AllowedModels); found {
				candidateByKey[commercialManagedBudgetKey(item.Provider, item.AllowedModels)] = item
			}
		}
		for _, limit := range relayByModel[model] {
			key := commercialManagedBudgetKey(limit.Provider, limit.AllowedModels)
			if candidateByKey[key] == nil {
				candidateByKey[key] = &types.CommercialManagedRelayBudget{
					UID: uid, Model: model, Provider: limit.Provider, AllowedModels: append([]string(nil), limit.AllowedModels...),
					ResetDuration: defaultRelayResetDuration(limit.Budget.ResetDuration),
				}
			}
		}
		for _, item := range candidateByKey {
			key := commercialManagedBudgetKey(item.Provider, item.AllowedModels)
			configByKey[key] = item
			desiredByKey[key] += amount
			nextByKey[model+"\x00"+key] = &types.CommercialManagedRelayBudget{
				UID: uid, Model: model, Provider: item.Provider, AllowedModels: append([]string(nil), item.AllowedModels...),
				ResetDuration: defaultRelayResetDuration(item.ResetDuration),
			}
		}
	}
	for key, amount := range desiredByKey {
		item := configByKey[key]
		resetDuration := defaultRelayResetDuration(item.ResetDuration)
		for associationKey, managedItem := range nextByKey {
			if strings.HasSuffix(associationKey, "\x00"+key) {
				managedItem.MaxLimit = amount
				managedItem.ResetDuration = resetDuration
			}
		}
		current, found := currentByKey[key]
		if !found || !nearlyEqual(current.Budget.MaxLimit, amount) || defaultRelayResetDuration(current.Budget.ResetDuration) != resetDuration {
			updatesByKey[key] = commercialRelayProviderBudgetUpdate{
				Provider: item.Provider, AllowedModels: append([]string(nil), item.AllowedModels...),
				MaxLimit: amount, ResetDuration: resetDuration,
			}
		}
	}
	for _, item := range managed {
		if item == nil {
			continue
		}
		key := commercialManagedBudgetKey(item.Provider, item.AllowedModels)
		if desiredByKey[key] > 0 {
			continue
		}
		current, found := findCommercialRelayLimit(relayByModel[item.Model], item.Provider, item.AllowedModels)
		if !found {
			continue
		}
		resetDuration := defaultRelayResetDuration(item.ResetDuration)
		nextByKey[item.Model+"\x00"+key] = &types.CommercialManagedRelayBudget{
			UID: uid, Model: item.Model, Provider: item.Provider, AllowedModels: append([]string(nil), item.AllowedModels...),
			MaxLimit: commercialRelayBlockedLimit, ResetDuration: resetDuration,
		}
		if !nearlyEqual(current.Budget.MaxLimit, commercialRelayBlockedLimit) || defaultRelayResetDuration(current.Budget.ResetDuration) != resetDuration {
			updatesByKey[key] = commercialRelayProviderBudgetUpdate{
				Provider: item.Provider, AllowedModels: append([]string(nil), item.AllowedModels...),
				MaxLimit: commercialRelayBlockedLimit, ResetDuration: resetDuration,
			}
		}
	}

	updates := make([]commercialRelayProviderBudgetUpdate, 0, len(updatesByKey))
	for _, item := range updatesByKey {
		updates = append(updates, item)
	}
	sort.Slice(updates, func(i, j int) bool {
		return commercialManagedBudgetKey(updates[i].Provider, updates[i].AllowedModels) < commercialManagedBudgetKey(updates[j].Provider, updates[j].AllowedModels)
	})
	nextManaged := make([]*types.CommercialManagedRelayBudget, 0, len(nextByKey))
	for _, item := range nextByKey {
		nextManaged = append(nextManaged, item)
	}
	sort.Slice(nextManaged, func(i, j int) bool {
		if nextManaged[i].Model != nextManaged[j].Model {
			return nextManaged[i].Model < nextManaged[j].Model
		}
		return commercialManagedBudgetKey(nextManaged[i].Provider, nextManaged[i].AllowedModels) < commercialManagedBudgetKey(nextManaged[j].Provider, nextManaged[j].AllowedModels)
	})
	return updates, nextManaged
}

func findCommercialRelayLimit(limits []commercialRelayModelLimit, provider string, allowedModels []string) (commercialRelayModelLimit, bool) {
	key := commercialManagedBudgetKey(provider, allowedModels)
	for _, limit := range limits {
		if commercialManagedBudgetKey(limit.Provider, limit.AllowedModels) == key {
			return limit, true
		}
	}
	return commercialRelayModelLimit{}, false
}

func commercialManagedBudgetKey(provider string, allowedModels []string) string {
	models := append([]string(nil), allowedModels...)
	for i := range models {
		models[i] = strings.TrimSpace(models[i])
	}
	sort.Strings(models)
	return strings.TrimSpace(provider) + "\x00" + strings.Join(models, "\x00")
}
