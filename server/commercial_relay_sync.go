package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
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

type commercialRelayBaselineStore interface {
	EnsureCommercialRelayBaseline(uid int64, profile string, budgets map[string]float64, startsAt time.Time) (bool, error)
}

type CommercialRelaySyncerOptions struct {
	EnforceEnabled bool
	EnforceUIDs    map[int64]bool
	Interval       time.Duration
	RetryDelays    []time.Duration
}

type commercialRelaySyncRequest struct {
	uid        int64
	generation uint64
	attempt    int
}

type commercialRelayPendingState struct {
	generation   uint64
	queued       bool
	inFlight     bool
	retryWaiting bool
}

type CommercialRelaySyncer struct {
	store          CommercialRelayManagedStore
	relayAdmin     *RelayAdminClient
	enforceEnabled bool
	enforceUIDs    map[int64]bool
	interval       time.Duration
	retryDelays    []time.Duration
	queue          chan commercialRelaySyncRequest
	reconcileQueue chan struct{}
	pendingMu      sync.Mutex
	pendingUIDs    map[int64]*commercialRelayPendingState
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
		reconcileQueue: make(chan struct{}, 1),
		pendingUIDs:    map[int64]*commercialRelayPendingState{},
	}
}

func (s *CommercialRelaySyncer) EnforcedFor(uid int64) bool {
	return s != nil && uid > 0 && (s.enforceEnabled || s.enforceUIDs[uid])
}

func (s *CommercialRelaySyncer) Enqueue(uid int64) {
	if s == nil || uid <= 0 || s.store == nil || s.relayAdmin == nil {
		return
	}
	if !s.notifyAndQueue(context.Background(), uid, false) {
		log.Printf("commercial relay sync queue is full; uid=%d will be retried by reconciliation", uid)
		s.requestReconciliation()
	}
}

func (s *CommercialRelaySyncer) Start(ctx context.Context) {
	if s == nil || s.store == nil || s.relayAdmin == nil {
		return
	}
	go s.run(ctx)
	go s.runReconciliation(ctx)
	if s.enforceEnabled {
		go s.bootstrapConfiguredRelayUsers(ctx)
	}
	s.requestReconciliation()
}

func (s *CommercialRelaySyncer) bootstrapConfiguredRelayUsers(ctx context.Context) {
	for {
		if err := s.bootstrapConfiguredRelayUsersOnce(ctx); err == nil {
			return
		} else {
			log.Printf("commercial relay baseline bootstrap failed: %v", err)
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *CommercialRelaySyncer) bootstrapConfiguredRelayUsersOnce(ctx context.Context) error {
	// Relay usage rows include the full provider/model catalog. Keep bootstrap
	// pages comfortably below RelayAdminClient's bounded response size.
	const pageSize = 10
	for offset := 0; ; offset += pageSize {
		var page commercialRelayUsageResponse
		path := fmt.Sprintf("/internal/usage/users?offset=%d&limit=%d&include_governance=1", offset, pageSize)
		if err := s.relayAdmin.Do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return fmt.Errorf("offset %d: %w", offset, err)
		}
		for index := range page.Users {
			user := &page.Users[index]
			if user.Configured && user.UID > 0 {
				if !s.notifyAndQueue(ctx, user.UID, true) {
					if err := ctx.Err(); err != nil {
						return fmt.Errorf("queue uid %d: %w", user.UID, err)
					}
					return fmt.Errorf("queue uid %d", user.UID)
				}
			}
		}
		if len(page.Users) < pageSize || (page.TotalCount > 0 && offset+len(page.Users) >= page.TotalCount) {
			return nil
		}
	}
}

func (s *CommercialRelaySyncer) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-s.queue:
			generation, attempt, ok := s.beginSync(request)
			if !ok {
				continue
			}
			if err := s.syncWithTimeout(ctx, request.uid); err != nil {
				log.Printf("commercial relay sync failed uid=%d attempt=%d: %v", request.uid, attempt+1, err)
				s.finishFailedSync(ctx, request.uid, generation, attempt)
			} else {
				s.finishSuccessfulSync(ctx, request.uid, generation)
			}
		case <-ticker.C:
			s.requestReconciliation()
		}
	}
}

func (s *CommercialRelaySyncer) notifyAndQueue(ctx context.Context, uid int64, wait bool) bool {
	s.pendingMu.Lock()
	pending := s.pendingUIDs[uid]
	if pending == nil {
		pending = &commercialRelayPendingState{}
		s.pendingUIDs[uid] = pending
	}
	pending.generation++
	if pending.queued || pending.inFlight || pending.retryWaiting {
		s.pendingMu.Unlock()
		return true
	}
	pending.queued = true
	request := commercialRelaySyncRequest{uid: uid, generation: pending.generation}
	s.pendingMu.Unlock()
	return s.dispatchRequest(ctx, request, wait)
}

func (s *CommercialRelaySyncer) ensureQueued(ctx context.Context, uid int64, wait bool) bool {
	s.pendingMu.Lock()
	pending := s.pendingUIDs[uid]
	if pending == nil {
		pending = &commercialRelayPendingState{generation: 1}
		s.pendingUIDs[uid] = pending
	}
	if pending.queued || pending.inFlight || pending.retryWaiting {
		s.pendingMu.Unlock()
		return true
	}
	pending.queued = true
	request := commercialRelaySyncRequest{uid: uid, generation: pending.generation}
	s.pendingMu.Unlock()
	return s.dispatchRequest(ctx, request, wait)
}

func (s *CommercialRelaySyncer) dispatchRequest(ctx context.Context, request commercialRelaySyncRequest, wait bool) bool {
	if wait {
		select {
		case <-ctx.Done():
			s.markDispatchFailed(request.uid)
			return false
		case s.queue <- request:
			return true
		}
	}
	select {
	case s.queue <- request:
		return true
	default:
		s.markDispatchFailed(request.uid)
		return false
	}
}

func (s *CommercialRelaySyncer) markDispatchFailed(uid int64) {
	s.pendingMu.Lock()
	if pending := s.pendingUIDs[uid]; pending != nil {
		pending.queued = false
	}
	s.pendingMu.Unlock()
}

func (s *CommercialRelaySyncer) beginSync(request commercialRelaySyncRequest) (uint64, int, bool) {
	s.pendingMu.Lock()
	pending := s.pendingUIDs[request.uid]
	if pending == nil {
		s.pendingMu.Unlock()
		return 0, 0, false
	}
	pending.queued = false
	pending.inFlight = true
	generation := pending.generation
	attempt := request.attempt
	if request.generation != generation {
		attempt = 0
	}
	s.pendingMu.Unlock()
	return generation, attempt, true
}

func (s *CommercialRelaySyncer) finishSuccessfulSync(ctx context.Context, uid int64, generation uint64) {
	s.pendingMu.Lock()
	pending := s.pendingUIDs[uid]
	if pending == nil {
		s.pendingMu.Unlock()
		return
	}
	pending.inFlight = false
	if pending.generation == generation {
		delete(s.pendingUIDs, uid)
		s.pendingMu.Unlock()
		return
	}
	pending.queued = true
	request := commercialRelaySyncRequest{uid: uid, generation: pending.generation}
	s.pendingMu.Unlock()

	if !s.dispatchRequest(ctx, request, false) {
		log.Printf("commercial relay follow-up queue is full; uid=%d will be retried by reconciliation", uid)
		s.requestReconciliation()
	}
}

func (s *CommercialRelaySyncer) finishFailedSync(ctx context.Context, uid int64, generation uint64, attempt int) {
	s.pendingMu.Lock()
	pending := s.pendingUIDs[uid]
	if pending == nil {
		s.pendingMu.Unlock()
		return
	}
	pending.inFlight = false
	if pending.generation != generation {
		pending.queued = true
		request := commercialRelaySyncRequest{uid: uid, generation: pending.generation}
		s.pendingMu.Unlock()
		if !s.dispatchRequest(ctx, request, false) {
			log.Printf("commercial relay fresh-update queue is full; uid=%d will be retried by reconciliation", uid)
			s.requestReconciliation()
		}
		return
	}
	if attempt >= len(s.retryDelays) {
		delete(s.pendingUIDs, uid)
		s.pendingMu.Unlock()
		return
	}
	pending.retryWaiting = true
	delay := s.retryDelays[attempt]
	if delay <= 0 {
		delay = time.Millisecond
	}
	nextAttempt := attempt + 1
	s.pendingMu.Unlock()
	s.scheduleRetry(ctx, uid, generation, nextAttempt, delay)
}

func (s *CommercialRelaySyncer) scheduleRetry(ctx context.Context, uid int64, generation uint64, attempt int, delay time.Duration) {
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			s.clearRetryWaiting(uid)
			return
		case <-timer.C:
		}

		s.pendingMu.Lock()
		pending := s.pendingUIDs[uid]
		if pending == nil || !pending.retryWaiting {
			s.pendingMu.Unlock()
			return
		}
		pending.retryWaiting = false
		if pending.generation != generation {
			attempt = 0
		}
		pending.queued = true
		request := commercialRelaySyncRequest{uid: uid, generation: pending.generation, attempt: attempt}
		s.pendingMu.Unlock()
		if !s.dispatchRequest(ctx, request, true) && ctx.Err() == nil {
			s.requestReconciliation()
		}
	}()
}

func (s *CommercialRelaySyncer) clearRetryWaiting(uid int64) {
	s.pendingMu.Lock()
	if pending := s.pendingUIDs[uid]; pending != nil && pending.retryWaiting {
		delete(s.pendingUIDs, uid)
	}
	s.pendingMu.Unlock()
}

func (s *CommercialRelaySyncer) requestReconciliation() {
	select {
	case s.reconcileQueue <- struct{}{}:
	default:
	}
}

func (s *CommercialRelaySyncer) runReconciliation(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.reconcileQueue:
			s.reconcileAll(ctx)
		}
	}
}

func (s *CommercialRelaySyncer) reconcileAll(ctx context.Context) {
	if !s.queueIdlePending(ctx) {
		return
	}
	var afterUID int64
	for {
		uids, err := s.store.ListCommercialReconcileUIDs(afterUID, 50)
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
			if !s.ensureQueued(ctx, uid, true) && ctx.Err() != nil {
				return
			}
		}
		if len(uids) < 50 {
			return
		}
		afterUID = uids[len(uids)-1]
	}
}

func (s *CommercialRelaySyncer) queueIdlePending(ctx context.Context) bool {
	s.pendingMu.Lock()
	requests := make([]commercialRelaySyncRequest, 0, len(s.pendingUIDs))
	for uid, pending := range s.pendingUIDs {
		if pending == nil || pending.queued || pending.inFlight || pending.retryWaiting {
			continue
		}
		pending.queued = true
		requests = append(requests, commercialRelaySyncRequest{uid: uid, generation: pending.generation})
	}
	s.pendingMu.Unlock()

	sort.Slice(requests, func(i, j int) bool { return requests[i].uid < requests[j].uid })
	for _, request := range requests {
		if !s.dispatchRequest(ctx, request, true) {
			return false
		}
	}
	return true
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
	if s.EnforcedFor(uid) && !commercialRelayHasBaselineEntitlement(summary) {
		if baselineStore, ok := s.store.(commercialRelayBaselineStore); ok {
			profile, budgets, baselineErr := commercialRelayBaselineForSummary(summary, relayUser)
			if baselineErr != nil {
				return nil, baselineErr
			}
			startsAt := commercialRelayBaselineStart(relayUser)
			created, baselineErr := baselineStore.EnsureCommercialRelayBaseline(uid, profile, budgets, startsAt)
			if baselineErr != nil {
				return nil, fmt.Errorf("create commercial relay baseline: %w", baselineErr)
			}
			if created {
				summary, err = s.store.GetCommercialSummary(uid)
				if err != nil {
					return nil, fmt.Errorf("reload commercial summary: %w", err)
				}
			}
		}
	}
	if err := validateCommercialRelayRequiredModels(summary, relayUser, managed); err != nil {
		return nil, err
	}
	updates, nextManaged := commercialRelayManagedPlan(uid, summary, relayUser, managed)
	modelScopes := commercialRelayModelScopes(summary, relayUser, managed)
	scopesChanged := !commercialRelayModelScopesMatch(relayUser.Limits.ModelScopes, modelScopes)
	sharedQuota := s.EnforcedFor(uid)
	if sharedQuota {
		updates, nextManaged = commercialRelaySharedManagedPlan(uid, summary, relayUser, managed)
	}
	sharedLimit := commercialRelaySharedLimit(summary)
	usageWindowStart := commercialRelayUsageWindowStartForSync(summary, relayUser)
	policyNeedsSync := sharedQuota && (!nearlyEqual(relayUser.Limits.MonthlyBudget.MaxLimit, sharedLimit) ||
		defaultRelayResetDuration(relayUser.Limits.MonthlyBudget.ResetDuration) != "1M" ||
		!sameCommercialRelayTimestamp(relayUser.UsageWindowStart, usageWindowStart))
	if len(updates) > 0 || scopesChanged || policyNeedsSync {
		payload := map[string]interface{}{}
		if len(updates) > 0 {
			payload["provider_config_budgets"] = updates
		}
		if scopesChanged {
			payload["model_scopes"] = modelScopes
		}
		if sharedQuota {
			payload["monthly_budget"] = sharedLimit
			payload["monthly_budget_duration"] = "1M"
			if usageWindowStart == "" {
				payload["usage_window_start"] = nil
			} else {
				payload["usage_window_start"] = usageWindowStart
			}
		}
		var response map[string]interface{}
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
		if sharedQuota {
			if err := verifyCommercialRelaySharedPolicy(sharedLimit, usageWindowStart, verified); err != nil {
				return nil, err
			}
		}
	}
	if err := s.store.ReplaceCommercialManagedRelayBudgets(uid, nextManaged); err != nil {
		return nil, fmt.Errorf("save managed relay budgets: %w", err)
	}
	return updates, nil
}

const (
	commercialRelayBaselineProfileFree   = "free"
	commercialRelayBaselineProfileLegacy = "legacy"
)

var commercialRelayFreeBudgets = map[string]float64{
	"MiniMax-M2.7":      1000,
	"MiniMax-M3":        500,
	"deepseek-v4-flash": 100,
}

func commercialRelayHasBaselineEntitlement(summary *types.CommercialSummary) bool {
	if summary == nil {
		return false
	}
	for _, entitlement := range summary.Entitlements {
		if entitlement == nil || !strings.EqualFold(strings.TrimSpace(entitlement.State), "active") {
			continue
		}
		source := strings.ToLower(strings.TrimSpace(entitlement.Source))
		if source == commercialRelayBaselineProfileFree || source == commercialRelayBaselineProfileLegacy {
			return true
		}
	}
	return false
}

func commercialRelayBaselineForSummary(summary *types.CommercialSummary, relayUser *commercialRelayUsageUser) (string, map[string]float64, error) {
	if summary != nil {
		for _, grant := range summary.Grants {
			if grant != nil && grant.AmountCNY > 0 && strings.EqualFold(strings.TrimSpace(grant.GrantType), "manual") {
				return commercialRelayBaselineProfileLegacy, nil, nil
			}
		}
		if len(summary.Entitlements) > 0 || len(summary.Grants) > 0 {
			budgets := make(map[string]float64, len(commercialRelayFreeBudgets))
			for model, amount := range commercialRelayFreeBudgets {
				budgets[model] = amount
			}
			return commercialRelayBaselineProfileFree, budgets, nil
		}
		// Paid upgrades revoke the prior free entitlement. A later refund
		// leaves only the immutable refund ledger entries, so use that audit
		// signal to recreate the default free baseline before reconciling the
		// stale shared Relay quota.
		for _, entry := range summary.Ledger {
			if entry != nil && strings.EqualFold(strings.TrimSpace(entry.SourceType), "refund") {
				budgets := make(map[string]float64, len(commercialRelayFreeBudgets))
				for model, amount := range commercialRelayFreeBudgets {
					budgets[model] = amount
				}
				return commercialRelayBaselineProfileFree, budgets, nil
			}
		}
	}
	if relayUser != nil && relayUser.Limits.MonthlyBudget.MaxLimit > commercialRelayBlockedLimit {
		return "", nil, fmt.Errorf("relay shared quota exists without a commercial baseline")
	}
	profile, budgets := commercialRelayBaseline(relayUser)
	if len(budgets) == 0 {
		return "", nil, fmt.Errorf("relay baseline quota is unavailable")
	}
	return profile, budgets, nil
}

func commercialRelayBaseline(relayUser *commercialRelayUsageUser) (string, map[string]float64) {
	budgets := map[string]float64{}
	allowed := map[string]bool{}
	hasScopes := relayUser != nil && len(relayUser.Limits.ModelScopes) > 0
	if hasScopes {
		for _, scope := range relayUser.Limits.ModelScopes {
			for _, model := range scope.AllowedModels {
				allowed[normalizeRelayModelName(model)] = true
			}
		}
	}
	if relayUser != nil {
		for _, limit := range relayUser.Limits.ModelLimits {
			model := strings.TrimSpace(limit.Model)
			if model == "" || model == "*" || limit.Budget.MaxLimit <= commercialRelayBlockedLimit {
				continue
			}
			if hasScopes && !allowed[normalizeRelayModelName(model)] {
				continue
			}
			if limit.Budget.MaxLimit > budgets[model] {
				budgets[model] = limit.Budget.MaxLimit
			}
		}
	}
	profile := commercialRelayBaselineProfileFree
	if len(budgets) != len(commercialRelayFreeBudgets) {
		profile = commercialRelayBaselineProfileLegacy
	} else {
		normalizedBudgets := map[string]float64{}
		for model, amount := range budgets {
			normalizedBudgets[normalizeRelayModelName(model)] = amount
		}
		for model, amount := range commercialRelayFreeBudgets {
			if !nearlyEqual(normalizedBudgets[normalizeRelayModelName(model)], amount) {
				profile = commercialRelayBaselineProfileLegacy
				break
			}
		}
	}
	return profile, budgets
}

func commercialRelayBaselineStart(relayUser *commercialRelayUsageUser) time.Time {
	if relayUser == nil {
		return time.Now().UTC()
	}
	if parsed, ok := parseCommercialRelayTime(relayUser.UsageWindowStart); ok {
		return parsed
	}
	var earliest time.Time
	for _, limit := range relayUser.Limits.ModelLimits {
		if limit.Budget.MaxLimit <= commercialRelayBlockedLimit {
			continue
		}
		if parsed, ok := parseCommercialRelayTime(limit.Budget.LastReset); ok && (earliest.IsZero() || parsed.Before(earliest)) {
			earliest = parsed
		}
	}
	if earliest.IsZero() {
		return time.Now().UTC()
	}
	return earliest
}

func parseCommercialRelayTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
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
	legacyManual := map[string]bool{}
	if summary != nil {
		for _, grant := range summary.Grants {
			if grant == nil || grant.AmountCNY <= 0 {
				continue
			}
			model := strings.TrimSpace(grant.Model)
			grantType := strings.ToLower(strings.TrimSpace(grant.GrantType))
			if model != "" && model != "*" && grantType == "manual" && commercialRelayGrantGoverned(summary, grantType) {
				// Retired manual models keep their value in the shared pool without requiring a live route.
				legacyManual[model] = true
				continue
			}
			if model != "" && model != "*" && commercialRelayGrantGoverned(summary, grantType) {
				required[model] = true
			}
		}
		for _, item := range managed {
			if item == nil || summary.TotalsByModel[strings.TrimSpace(item.Model)] <= 0 {
				continue
			}
			model := strings.TrimSpace(item.Model)
			if model != "" && model != "*" && (!legacyManual[model] || required[model]) {
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
	actual = canonicalCommercialRelayModelScopes(actual)
	expected = canonicalCommercialRelayModelScopes(expected)
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

// canonicalCommercialRelayModelScopes mirrors Relay Admin's model-scope
// merge contract. Relay Admin merges requested scopes that share any managed
// model into one scope, retaining non-overlapping managed/allowed models from
// the existing scope. Comparing the raw arrays would therefore report a
// permanent mismatch for valid overlapping provider families (for example a
// Terra/Sol provider alongside a Terra/Sol/Luna provider).
func canonicalCommercialRelayModelScopes(scopes []commercialRelayModelScope) []commercialRelayModelScope {
	canonical := make([]commercialRelayModelScope, 0, len(scopes))
	for _, scope := range scopes {
		managed := normalizedCommercialModels(scope.ManagedModels)
		if len(managed) == 0 {
			continue
		}
		allowed := normalizedCommercialModels(scope.AllowedModels)
		managedSet := map[string]bool{}
		for _, model := range managed {
			managedSet[strings.ToLower(model)] = true
		}
		filteredAllowed := make([]string, 0, len(allowed))
		for _, model := range allowed {
			if managedSet[strings.ToLower(model)] {
				filteredAllowed = append(filteredAllowed, model)
			}
		}
		update := commercialRelayModelScope{ManagedModels: managed, AllowedModels: filteredAllowed}
		merged := false
		for index := range canonical {
			current := canonical[index]
			currentManagedSet := map[string]bool{}
			for _, model := range current.ManagedModels {
				currentManagedSet[strings.ToLower(model)] = true
			}
			intersects := false
			for _, model := range update.ManagedModels {
				if currentManagedSet[strings.ToLower(model)] {
					intersects = true
					break
				}
			}
			if !intersects {
				continue
			}

			mergedManaged := append([]string(nil), update.ManagedModels...)
			updateManagedSet := map[string]bool{}
			for _, model := range update.ManagedModels {
				updateManagedSet[strings.ToLower(model)] = true
			}
			for _, model := range current.ManagedModels {
				if !updateManagedSet[strings.ToLower(model)] {
					mergedManaged = append(mergedManaged, model)
				}
			}
			mergedAllowed := append([]string(nil), update.AllowedModels...)
			mergedAllowedSet := map[string]bool{}
			for _, model := range mergedAllowed {
				mergedAllowedSet[strings.ToLower(model)] = true
			}
			for _, model := range current.AllowedModels {
				key := strings.ToLower(model)
				if updateManagedSet[key] || mergedAllowedSet[key] {
					continue
				}
				mergedAllowed = append(mergedAllowed, model)
				mergedAllowedSet[key] = true
			}
			canonical[index] = commercialRelayModelScope{
				ManagedModels: normalizedCommercialModels(mergedManaged),
				AllowedModels: normalizedCommercialModels(mergedAllowed),
			}
			merged = true
			break
		}
		if !merged {
			canonical = append(canonical, update)
		}
	}
	sort.Slice(canonical, func(i, j int) bool {
		left := commercialRelayModelSetKey(canonical[i].ManagedModels)
		right := commercialRelayModelSetKey(canonical[j].ManagedModels)
		if left == right {
			return commercialRelayModelSetKey(canonical[i].AllowedModels) < commercialRelayModelSetKey(canonical[j].AllowedModels)
		}
		return left < right
	})
	return canonical
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
			if commercialRelayGrantGoverned(summary, grantType) {
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

func commercialRelayGovernedGrant(grantType string) bool {
	switch strings.ToLower(strings.TrimSpace(grantType)) {
	case "order", "invite", "trial", "bonus", "free", "legacy", "operator_plan", "adjustment_credit", "adjustment_debit":
		return true
	default:
		return false
	}
}

func commercialRelayGrantGoverned(summary *types.CommercialSummary, grantType string) bool {
	if commercialRelayGovernedGrant(grantType) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(grantType), "manual") || summary == nil {
		return false
	}
	for _, entitlement := range summary.Entitlements {
		if entitlement != nil && strings.EqualFold(strings.TrimSpace(entitlement.State), "active") &&
			strings.EqualFold(strings.TrimSpace(entitlement.Source), commercialRelayBaselineProfileLegacy) {
			return true
		}
	}
	return false
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
	return commercialRelayManagedPlanForMode(uid, summary, relayUser, managed, false)
}

func commercialRelaySharedManagedPlan(uid int64, summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, managed []*types.CommercialManagedRelayBudget) ([]commercialRelayProviderBudgetUpdate, []*types.CommercialManagedRelayBudget) {
	return commercialRelayManagedPlanForMode(uid, summary, relayUser, managed, true)
}

func commercialRelayManagedPlanForMode(uid int64, summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, managed []*types.CommercialManagedRelayBudget, shared bool) ([]commercialRelayProviderBudgetUpdate, []*types.CommercialManagedRelayBudget) {
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
	sharedLimit := commercialRelaySharedLimit(summary)
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
			if shared {
				desiredByKey[key] = sharedLimit
			} else {
				desiredByKey[key] += amount
			}
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

func commercialRelaySharedLimit(summary *types.CommercialSummary) float64 {
	total := 0.0
	if summary != nil {
		total = summary.TotalCNY
		if total <= 0 {
			for _, amount := range summary.TotalsByModel {
				if amount > 0 {
					total += amount
				}
			}
		}
	}
	if total <= 0 {
		return commercialRelayBlockedLimit
	}
	return total
}

func commercialRelayUsageWindowStart(summary *types.CommercialSummary) string {
	var earliestPackage time.Time
	var earliestBaseline time.Time
	if summary != nil {
		for _, entitlement := range summary.Entitlements {
			if entitlement == nil || !strings.EqualFold(strings.TrimSpace(entitlement.State), "active") || entitlement.StartsAt.IsZero() {
				continue
			}
			source := strings.ToLower(strings.TrimSpace(entitlement.Source))
			if source == commercialRelayBaselineProfileFree || source == commercialRelayBaselineProfileLegacy {
				if earliestBaseline.IsZero() || entitlement.StartsAt.Before(earliestBaseline) {
					earliestBaseline = entitlement.StartsAt
				}
				continue
			}
			if earliestPackage.IsZero() || entitlement.StartsAt.Before(earliestPackage) {
				earliestPackage = entitlement.StartsAt
			}
		}
	}
	earliest := earliestPackage
	if earliest.IsZero() {
		earliest = earliestBaseline
	}
	if earliest.IsZero() {
		return ""
	}
	return earliest.UTC().Format(time.RFC3339)
}

func commercialRelayUsageWindowStartForSync(summary *types.CommercialSummary, relayUser *commercialRelayUsageUser) string {
	desired := commercialRelayUsageWindowStart(summary)
	if desired == "" || relayUser == nil || strings.TrimSpace(relayUser.UsageWindowStart) == "" {
		return desired
	}
	desiredTime, desiredOK := parseCommercialRelayTime(desired)
	currentTime, currentOK := parseCommercialRelayTime(relayUser.UsageWindowStart)
	if desiredOK && currentOK && currentTime.After(desiredTime) {
		return relayUser.UsageWindowStart
	}
	return desired
}

func sameCommercialRelayTimestamp(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return strings.TrimSpace(left) == strings.TrimSpace(right)
	}
	leftTime, leftOK := parseCommercialRelayTime(left)
	rightTime, rightOK := parseCommercialRelayTime(right)
	return leftOK && rightOK && leftTime.Truncate(time.Second).Equal(rightTime.Truncate(time.Second))
}

func verifyCommercialRelaySharedPolicy(limit float64, usageWindowStart string, relayUser *commercialRelayUsageUser) error {
	if relayUser == nil || !relayUser.Configured {
		return fmt.Errorf("relay shared quota verification failed: relay key is not configured")
	}
	if !nearlyEqual(relayUser.Limits.MonthlyBudget.MaxLimit, limit) ||
		defaultRelayResetDuration(relayUser.Limits.MonthlyBudget.ResetDuration) != "1M" {
		return fmt.Errorf("relay shared quota verification failed: monthly budget mismatch")
	}
	if !sameCommercialRelayTimestamp(relayUser.UsageWindowStart, usageWindowStart) {
		return fmt.Errorf("relay shared quota verification failed: usage window mismatch")
	}
	return nil
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
	seen := map[string]struct{}{}
	models := make([]string, 0, len(allowedModels))
	for _, value := range allowedModels {
		model := commercialRelayCanonicalModelName(value)
		if model == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(model)]; ok {
			continue
		}
		seen[strings.ToLower(model)] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return strings.TrimSpace(provider) + "\x00" + strings.Join(models, "\x00")
}

// commercialRelayCanonicalModelName keeps provider-config budget identity
// aligned with relay-admin.  The vision endpoint is an internal capability of
// the public DeepSeek V4 model, so relay-admin may persist both names while
// CatsCompany sends updates for the public name only.  Comparing raw model
// arrays would make the post-write verification fail forever for that alias.
func commercialRelayCanonicalModelName(value string) string {
	model := strings.TrimSpace(value)
	if strings.EqualFold(model, "deepseek-v4-flash-vision-exp") {
		return "deepseek-v4-flash"
	}
	return model
}
