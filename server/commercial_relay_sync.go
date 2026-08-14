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
	RetryDelays    []time.Duration
}

type commercialRelaySyncRequest struct {
	uid     int64
	attempt int
}

type CommercialRelaySyncer struct {
	store             CommercialRelayManagedStore
	relayAdmin        *RelayAdminClient
	enforceEnabled    bool
	enforceUIDs       map[int64]bool
	interval          time.Duration
	retryDelays       []time.Duration
	queue             chan commercialRelaySyncRequest
	reconcileAfterUID int64
}

func NewCommercialRelaySyncer(store CommercialRelayManagedStore, relayAdmin *RelayAdminClient, opts CommercialRelaySyncerOptions) *CommercialRelaySyncer {
	interval := opts.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	retryDelays := opts.RetryDelays
	if retryDelays == nil {
		retryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}
	}
	retryDelays = append([]time.Duration(nil), retryDelays...)
	return &CommercialRelaySyncer{
		store:          store,
		relayAdmin:     relayAdmin,
		enforceEnabled: opts.EnforceEnabled,
		enforceUIDs:    copyCommercialUIDSet(opts.EnforceUIDs),
		interval:       interval,
		retryDelays:    retryDelays,
		queue:          make(chan commercialRelaySyncRequest, 256),
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
	case s.queue <- commercialRelaySyncRequest{uid: uid}:
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
	startupReconcile := time.NewTimer(0)
	defer startupReconcile.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-s.queue:
			if err := s.syncWithTimeout(ctx, request.uid); err != nil {
				log.Printf("commercial relay sync failed uid=%d attempt=%d: %v", request.uid, request.attempt+1, err)
				s.scheduleRetry(ctx, request)
			}
		case <-startupReconcile.C:
			s.reconcile(ctx)
		case <-ticker.C:
			s.reconcile(ctx)
		}
	}
}

func (s *CommercialRelaySyncer) scheduleRetry(ctx context.Context, request commercialRelaySyncRequest) {
	if request.attempt >= len(s.retryDelays) {
		return
	}
	delay := s.retryDelays[request.attempt]
	if delay <= 0 {
		delay = time.Millisecond
	}
	request.attempt++
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		select {
		case <-ctx.Done():
		case s.queue <- request:
		default:
			log.Printf("commercial relay retry queue is full; uid=%d will be retried by reconciliation", request.uid)
		}
	}()
}

func (s *CommercialRelaySyncer) reconcile(ctx context.Context) {
	uids, err := s.store.ListCommercialReconcileUIDs(s.reconcileAfterUID, 50)
	if err != nil {
		log.Printf("commercial relay reconciliation list failed: %v", err)
		return
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
		select {
		case <-ctx.Done():
			return
		case s.queue <- commercialRelaySyncRequest{uid: uid}:
		default:
			log.Printf("commercial relay reconciliation queue is full; uid=%d will be retried later", uid)
		}
	}
	if len(uids) < 50 {
		s.reconcileAfterUID = 0
	} else {
		s.reconcileAfterUID = uids[len(uids)-1]
	}
}

func (s *CommercialRelaySyncer) syncWithTimeout(parent context.Context, uid int64) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	_, err := s.SyncUID(ctx, uid)
	return err
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
	if err := validateCommercialRelayRequiredModels(summary, relayUser, managed); err != nil {
		return nil, err
	}
	updates, nextManaged := commercialRelayManagedPlan(uid, summary, relayUser, managed)
	modelScopes := commercialRelayModelScopes(summary, relayUser, managed)
	scopesChanged := !commercialRelayModelScopesMatch(relayUser.Limits.ModelScopes, modelScopes)
	if len(updates) > 0 || scopesChanged {
		var response map[string]interface{}
		payload := map[string]interface{}{"provider_config_budgets": updates}
		if scopesChanged {
			payload["model_scopes"] = modelScopes
		}
		if err := s.relayAdmin.Do(ctx, http.MethodPost, fmt.Sprintf("/internal/users/%d/key/limits", uid), payload, &response); err != nil {
			return nil, fmt.Errorf("write relay budgets: %w", err)
		}
		verified, err := fetchRelayLimitsForUID(ctx, s.relayAdmin, uid)
		if err != nil {
			return nil, fmt.Errorf("verify relay budgets: %w", err)
		}
		if err := verifyCommercialRelayUpdates(updates, verified); err != nil {
			return nil, err
		}
		if err := verifyCommercialRelayModelScopes(modelScopes, verified); err != nil {
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
		for _, limit := range commercialRelayCatalogLimits(relayUser) {
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

func validateCommercialRelayRequiredModels(summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, managed []*types.CommercialManagedRelayBudget) error {
	required := map[string]bool{}
	if summary != nil {
		for _, grant := range summary.Grants {
			if grant == nil || grant.AmountCNY <= 0 {
				continue
			}
			model := strings.TrimSpace(grant.Model)
			grantType := strings.ToLower(strings.TrimSpace(grant.GrantType))
			if model != "" && model != "*" && (grantType == "order" || grantType == "invite" || grantType == "trial" || grantType == "bonus") {
				required[model] = true
			}
		}
		for _, item := range managed {
			if item == nil || summary.TotalsByModel[strings.TrimSpace(item.Model)] <= 0 {
				continue
			}
			model := strings.TrimSpace(item.Model)
			if model != "" && model != "*" {
				required[model] = true
			}
		}
	}
	for model := range required {
		matched := false
		if relayUser != nil {
			for _, limit := range commercialRelayCatalogLimits(relayUser) {
				if strings.TrimSpace(limit.Model) == model && strings.TrimSpace(limit.Provider) != "" && len(limit.AllowedModels) > 0 {
					matched = true
					break
				}
			}
		}
		if !matched {
			return fmt.Errorf("commercial relay model mapping is unavailable for %s", model)
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

func verifyCommercialRelayModelScopes(expected []commercialRelayModelScope, relayUser *commercialRelayUsageUser) error {
	if relayUser == nil || !relayUser.Configured {
		return fmt.Errorf("relay model scope verification failed: relay key is not configured")
	}
	if !commercialRelayModelScopesMatch(relayUser.Limits.ModelScopes, expected) {
		return fmt.Errorf("relay model scope verification failed")
	}
	return nil
}

func commercialRelayCatalogLimits(relayUser *commercialRelayUsageUser) []commercialRelayModelLimit {
	if relayUser == nil {
		return nil
	}
	if len(relayUser.Limits.AvailableModelLimits) > 0 {
		return relayUser.Limits.AvailableModelLimits
	}
	return relayUser.Limits.ModelLimits
}

func normalizedCommercialModels(models []string) []string {
	seen := map[string]string{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" {
			continue
		}
		key := strings.ToLower(model)
		if seen[key] == "" {
			seen[key] = model
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func commercialRelayModelSetKey(models []string) string {
	normalized := normalizedCommercialModels(models)
	for index := range normalized {
		normalized[index] = strings.ToLower(normalized[index])
	}
	return strings.Join(normalized, "\x00")
}

func commercialRelayModelScopesMatch(actual, expected []commercialRelayModelScope) bool {
	if len(actual) != len(expected) {
		return false
	}
	actualByKey := map[string]string{}
	for _, scope := range actual {
		actualByKey[commercialRelayModelSetKey(scope.ManagedModels)] = commercialRelayModelSetKey(scope.AllowedModels)
	}
	for _, scope := range expected {
		if actualByKey[commercialRelayModelSetKey(scope.ManagedModels)] != commercialRelayModelSetKey(scope.AllowedModels) {
			return false
		}
	}
	return true
}

func commercialRelayModelScopes(summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, managed []*types.CommercialManagedRelayBudget) []commercialRelayModelScope {
	totals := map[string]float64{}
	governed := map[string]bool{}
	if summary != nil {
		for model, amount := range summary.TotalsByModel {
			model = strings.TrimSpace(model)
			if model != "" && model != "*" && amount > 0 {
				totals[strings.ToLower(model)] = amount
			}
		}
		for _, grant := range summary.Grants {
			if grant == nil || grant.AmountCNY <= 0 {
				continue
			}
			grantType := strings.ToLower(strings.TrimSpace(grant.GrantType))
			if grantType == "order" || grantType == "invite" || grantType == "trial" || grantType == "bonus" {
				governed[strings.ToLower(strings.TrimSpace(grant.Model))] = true
			}
		}
	}
	for _, item := range managed {
		if item != nil {
			governed[strings.ToLower(strings.TrimSpace(item.Model))] = true
		}
	}

	families := map[string][]string{}
	if relayUser != nil {
		for _, scope := range relayUser.Limits.ModelScopes {
			models := normalizedCommercialModels(scope.ManagedModels)
			if len(models) > 0 {
				families[commercialRelayModelSetKey(models)] = models
				for _, model := range models {
					governed[strings.ToLower(model)] = true
				}
			}
		}
		for _, limit := range commercialRelayCatalogLimits(relayUser) {
			models := normalizedCommercialModels(limit.AllowedModels)
			if len(models) == 0 {
				continue
			}
			owned := false
			for _, model := range models {
				if governed[strings.ToLower(model)] {
					owned = true
					break
				}
			}
			if owned {
				families[commercialRelayModelSetKey(models)] = models
			}
		}
	}

	scopes := make([]commercialRelayModelScope, 0, len(families))
	for _, models := range families {
		allowed := make([]string, 0, len(models))
		for _, model := range models {
			if totals[strings.ToLower(model)] > 0 {
				allowed = append(allowed, model)
			}
		}
		scopes = append(scopes, commercialRelayModelScope{ManagedModels: models, AllowedModels: allowed})
	}
	sort.Slice(scopes, func(i, j int) bool {
		return commercialRelayModelSetKey(scopes[i].ManagedModels) < commercialRelayModelSetKey(scopes[j].ManagedModels)
	})
	return scopes
}

func commercialRelayScopedAllowedModels(models []string, totals map[string]float64) []string {
	allowed := []string{}
	for _, model := range normalizedCommercialModels(models) {
		if totals[strings.ToLower(model)] > 0 {
			allowed = append(allowed, model)
		}
	}
	return allowed
}

func commercialRelayScopeOwnsModels(scopes []commercialRelayModelScope, models []string) bool {
	wanted := map[string]bool{}
	for _, model := range models {
		wanted[strings.ToLower(strings.TrimSpace(model))] = true
	}
	for _, scope := range scopes {
		for _, model := range scope.ManagedModels {
			if wanted[strings.ToLower(strings.TrimSpace(model))] {
				return true
			}
		}
	}
	return false
}

func commercialRelayManagedPlan(uid int64, summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, managed []*types.CommercialManagedRelayBudget) ([]commercialRelayProviderBudgetUpdate, []*types.CommercialManagedRelayBudget) {
	totals := map[string]float64{}
	normalizedTotals := map[string]float64{}
	if summary != nil {
		for model, amount := range summary.TotalsByModel {
			model = strings.TrimSpace(model)
			if model != "" && model != "*" && amount > 0 {
				totals[model] = amount
				normalizedTotals[strings.ToLower(model)] = amount
			}
		}
	}
	relayByModel := map[string][]commercialRelayModelLimit{}
	if relayUser != nil {
		for _, limit := range commercialRelayCatalogLimits(relayUser) {
			model := strings.TrimSpace(limit.Model)
			if model != "" && model != "*" && limit.Provider != "" && len(limit.AllowedModels) > 0 {
				relayByModel[model] = append(relayByModel[model], limit)
			}
		}
	}
	currentByKey := map[string]commercialRelayModelLimit{}
	if relayUser != nil {
		for _, limit := range relayUser.Limits.ModelLimits {
			currentByKey[commercialManagedBudgetKey(limit.Provider, limit.AllowedModels)] = limit
		}
	}
	modelScopes := commercialRelayModelScopes(summary, relayUser, managed)
	updatesByKey := map[string]commercialRelayProviderBudgetUpdate{}
	nextByKey := map[string]*types.CommercialManagedRelayBudget{}
	configByKey := map[string]*types.CommercialManagedRelayBudget{}
	desiredByKey := map[string]float64{}
	for model, amount := range totals {
		candidateByKey := map[string]*types.CommercialManagedRelayBudget{}
		for _, limit := range relayByModel[model] {
			allowedModels := commercialRelayScopedAllowedModels(limit.AllowedModels, normalizedTotals)
			if len(allowedModels) == 0 {
				continue
			}
			key := commercialManagedBudgetKey(limit.Provider, allowedModels)
			if candidateByKey[key] == nil {
				candidateByKey[key] = &types.CommercialManagedRelayBudget{
					UID: uid, Model: model, Provider: limit.Provider, AllowedModels: append([]string(nil), allowedModels...),
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
		if commercialRelayScopeOwnsModels(modelScopes, item.AllowedModels) {
			continue
		}
		current, found := currentByKey[key]
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
