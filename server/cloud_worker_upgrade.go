package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 云机器点数升配（用点数把已买到的 packed 容器原地换更大档位）。
//
// 产品规则（服务端强制，前端只负责展示）：
//   - 只卖「共享机器（packed_shared）」的升配，且只升不降。
//   - 价格基准 30 天，收的是差价按剩余时长折算：ceil((新档价-原档价) × 剩余秒 / 30天)，
//     精确到 micro unit（1 分 = 1 点 = 1000000 units），不四舍五入到整分更不取整月。
//   - 不收新的整月费用、不延长机器租期：升配只把剩下的时间换个更大的档。
//   - 报价 5 分钟有效，绑定 owner / 机器 / 当前档 / 到期时间 / 目标档 / 费用。
//   - 执行前先预占点数（monthly→paid，邀请赠送仅用于模型，禁止透支），
//     脚本确认成功才结算；适配器明确确认无副作用才释放预留；
//     超时/结果未知保留预占并进入 reconciling，由后台核实收尾。

const (
	machineUpgradeQuoteTTL = 5 * time.Minute
	// machineUpgradeTermSeconds 是价格基准时长（30 天）。
	machineUpgradeTermSeconds int64 = 30 * 24 * 60 * 60
	// machineUpgradeMaxRemainingSeconds 拦住「剩余时间离谱」的报价（超过一年），
	// 那说明机器 term 数据本身坏了，不该拿它算钱。
	machineUpgradeMaxRemainingSeconds int64 = 365 * 24 * 60 * 60
	// 容量拒绝的原始退出码，必须同时有结构化无副作用证据。
	machineUpgradeRefusedExitCode = 3
	// machineUpgradeReconcileStale 是「多久没动静就认为执行它的进程已经没了」。
	machineUpgradeReconcileStale = 3 * time.Minute
	// machineUpgradeReconcileBatch 是一次 reconcile 处理的上限。
	machineUpgradeReconcileBatch = 10
)

// 价格基准（分/30 天）。合同默认值，可用 CATSCO_MACHINE_PLAN_PRICES_FEN 覆盖，
// 例如 "light:5900,standard:9900,pro:19900,heavy:59900"。
var machineUpgradeDefaultPricesFen = map[string]int64{
	types.MachinePlanLight:    5900,
	types.MachinePlanStandard: 9900,
	types.MachinePlanPro:      19900,
	types.MachinePlanHeavy:    59900,
}

// machinePlanPricesFen returns the configured ladder. Unknown/negative entries
// keep the default so a typo cannot make one tier free.
func machinePlanPricesFen() map[string]int64 {
	prices := make(map[string]int64, len(machineUpgradeDefaultPricesFen))
	for plan, fen := range machineUpgradeDefaultPricesFen {
		prices[plan] = fen
	}
	for _, item := range strings.Split(os.Getenv("CATSCO_MACHINE_PLAN_PRICES_FEN"), ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if types.MachinePlanRank(name) == 0 {
			continue
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			continue
		}
		prices[name] = parsed
	}
	return prices
}

// machineUpgradeCostUnits is the prorated difference for the remaining term.
// It never rounds to a whole fen: the ledger is denominated in micro units, so a
// short remainder legitimately buys a fraction of a fen's worth of upgrade.
func machineUpgradeCostUnits(fromPlan, toPlan string, remaining time.Duration) (int64, bool) {
	return machinePlanPricing{base: fromPlan}.cost(toPlan, time.Time{}, time.Time{}.Add(remaining))
}

type machinePlanPricing struct {
	base string
	term *types.MachinePlanTerm
}

func (p machinePlanPricing) planAt(at time.Time) string {
	if p.term != nil && p.term.ExpiresAt.After(at) {
		return p.term.Plan
	}
	return p.base
}

type machineUpgradePriceSegment struct {
	FromPlan      string `json:"from_plan"`
	Remaining     int64  `json:"remaining_seconds"`
	DifferenceFen int64  `json:"difference_fen"`
}

func (p machinePlanPricing) cost(target string, start, end time.Time) (int64, bool) {
	cost, _, ok := p.price(target, start, end)
	return cost, ok
}

func (p machinePlanPricing) price(target string, start, end time.Time) (int64, []machineUpgradePriceSegment, bool) {
	seconds := int64(end.Sub(start) / time.Second)
	if seconds <= 0 || seconds > machineUpgradeMaxRemainingSeconds {
		return 0, nil, false
	}
	prices := machinePlanPricesFen()
	targetPrice, ok := prices[target]
	if !ok {
		return 0, nil, false
	}
	var numerator int64
	segments := make([]machineUpgradePriceSegment, 0, 2)
	add := func(plan string, duration int64) bool {
		if duration == 0 {
			return true
		}
		price, ok := prices[plan]
		if !ok || targetPrice <= price || targetPrice-price >
			(int64(^uint64(0)>>1)-machineUpgradeTermSeconds-numerator)/commercialTopupUnitsPerFen/duration {
			return false
		}
		difference := targetPrice - price
		numerator += difference * commercialTopupUnitsPerFen * duration
		segments = append(segments, machineUpgradePriceSegment{FromPlan: plan, Remaining: duration, DifferenceFen: difference})
		return true
	}
	// An early renewal can extend the machine beyond a previous paid upgrade.
	// Credit the higher tier only for the seconds it actually covers.
	if p.term != nil && p.term.ExpiresAt.After(start) {
		covered := min(seconds, int64(p.term.ExpiresAt.Sub(start)/time.Second))
		if !add(p.term.Plan, covered) {
			return 0, nil, false
		}
		seconds -= covered
	}
	if !add(p.base, seconds) {
		return 0, nil, false
	}
	return (numerator + machineUpgradeTermSeconds - 1) / machineUpgradeTermSeconds, segments, true
}

// machineUpgradeStore is the persistence this feature needs. It is optional: a
// deployment whose adapter does not implement it simply reports the feature as
// unavailable instead of shipping a button that cannot work.
type machineUpgradeStore interface {
	CreateMachineUpgradeQuote(*types.MachineUpgradeQuote) error
	GetMachineUpgradeQuote(uid int64, quoteID string) (*types.MachineUpgradeQuote, error)
	OpenMachineUpgrade(*types.MachineUpgradeOperation) (*types.MachineUpgradeOperation, bool, error)
	GetMachineUpgrade(uid int64, operationID string) (*types.MachineUpgradeOperation, error)
	StartMachineUpgradeCharge(operationID string, allowRetry bool) (*types.MachineUpgradeOperation, bool, error)
	CompleteMachineUpgrade(operationID, plan, basePlan string, machineExpiresAt time.Time) (*types.MachineUpgradeOperation, error)
	FailMachineUpgrade(operationID, message string) (*types.MachineUpgradeOperation, error)
	MarkMachineUpgradeReconciling(operationID, message string) (*types.MachineUpgradeOperation, error)
	ListMachineUpgradesForReconcile(staleAfter time.Duration, limit int) ([]*types.MachineUpgradeOperation, error)
	MachinePlanTerm(tenantName string) (*types.MachinePlanTerm, error)
	MachineUpgradeCreditBalance(uid int64) (int64, bool, error)
	MachineUpgradeOpenForTenant(tenantName string) (bool, error)
}

var (
	errMachineUpgradeConflict     = store.ErrMachineUpgradeConflict
	errMachineUpgradeNotFound     = store.ErrMachineUpgradeNotFound
	errMachineUpgradeInsufficient = store.ErrModelCreditInsufficient
)

// newMachineUpgradeID mints a quote/operation id inside the column's alphabet.
func newMachineUpgradeID(prefix string) string {
	random := make([]byte, 9)
	if _, err := rand.Read(random); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(random)
}

// machineUpgradeStoreFor returns the adapter when it supports the feature.
func (h *CloudWorkerHandler) machineUpgradeStoreFor() (machineUpgradeStore, bool) {
	if h == nil || h.db == nil {
		return nil, false
	}
	store, ok := h.db.(machineUpgradeStore)
	return store, ok
}

// lifecycleByName finds one of the owner's own workers. Ownership is the filter,
// so another tenant's name can never be upgraded through this path.
func (h *CloudWorkerHandler) lifecycleByName(uid int64, name string) (*types.CloudWorkerLifecycle, error) {
	name = strings.TrimSpace(name)
	if name == "" || uid <= 0 {
		return nil, nil
	}
	lister, ok := h.credits.(interface {
		ListCloudWorkerLifecycles(int64) ([]CloudWorkerLifecycle, error)
	})
	if !ok {
		return nil, errors.New("cloud worker lifecycle store unavailable")
	}
	lifecycles, err := lister.ListCloudWorkerLifecycles(uid)
	if err != nil {
		return nil, err
	}
	for i := range lifecycles {
		if lifecycles[i].TenantName == name && lifecycles[i].OwnerUID == uid {
			return &lifecycles[i], nil
		}
	}
	return nil, nil
}

// readMachinePlanPricing applies the same package and purchased-tier floors as
// the quote and its execution check.
func (h *CloudWorkerHandler) readMachinePlanPricing(store machineUpgradeStore, uid int64, tenant string) (machinePlanPricing, error) {
	plan := types.MachinePlanLight
	if reader, ok := h.credits.(interface {
		CloudWorkerLifecycleRuntimePlan(string) (string, error)
	}); ok {
		base, err := reader.CloudWorkerLifecycleRuntimePlan(tenant)
		if err != nil {
			return machinePlanPricing{}, err
		}
		if types.MachinePlanRank(strings.TrimSpace(base)) > types.MachinePlanRank(plan) {
			plan = strings.TrimSpace(base)
		}
	}
	if reader, ok := h.db.(interface {
		GetCommercialSummary(int64) (*types.CommercialSummary, error)
	}); ok {
		summary, err := reader.GetCommercialSummary(uid)
		if err != nil {
			return machinePlanPricing{}, err
		}
		if summary != nil && summary.CloudWorkerPackageTerm != nil {
			packagePlan := planRuntimeTier(summary.Plans, summary.CloudWorkerPackageTerm.PlanID)
			if types.MachinePlanRank(packagePlan) > types.MachinePlanRank(plan) {
				plan = packagePlan
			}
		}
	}
	term, err := store.MachinePlanTerm(tenant)
	if err != nil {
		return machinePlanPricing{}, err
	}
	if term == nil || term.OwnerUID != uid || types.MachinePlanRank(term.Plan) <= types.MachinePlanRank(plan) {
		term = nil
	}
	return machinePlanPricing{base: plan, term: term}, nil
}

func (h *CloudWorkerHandler) machineCurrentPlan(store machineUpgradeStore, uid int64, tenant string) (string, error) {
	pricing, err := h.readMachinePlanPricing(store, uid, tenant)
	if err != nil {
		return "", err
	}
	return pricing.planAt(time.Now().UTC()), nil
}

// machineUpgradeQuoteView is the GET response body.
func machineUpgradeQuoteView(tenant, fromPlan, toPlan string, costUnits, balanceUnits int64, quoteID string, expiresAt, workerExpiresAt time.Time, segments []machineUpgradePriceSegment) map[string]interface{} {
	return map[string]interface{}{
		"enabled":              true,
		"quote_id":             quoteID,
		"tenant_name":          tenant,
		"from_plan":            fromPlan,
		"to_plan":              toPlan,
		"cost_units":           costUnits,
		"units_per_credit":     commercialTopupUnitsPerFen,
		"balance_units":        balanceUnits,
		"expires_at":           expiresAt.UTC().Format(time.RFC3339),
		"expires_in_ms":        max(0, min(machineUpgradeQuoteTTL, time.Until(expiresAt)).Milliseconds()),
		"worker_expires_at":    workerExpiresAt.UTC().Format(time.RFC3339),
		"remaining_seconds":    int64(workerExpiresAt.Sub(expiresAt.Add(-machineUpgradeQuoteTTL)) / time.Second),
		"pricing_term_seconds": machineUpgradeTermSeconds,
		"pricing_segments":     segments,
		// Capacity is checked by the adapter when executing the quote.
		"needs_migration": false,
		"prices_fen":      machinePlanPricesFen(),
	}
}

func machineUpgradeDisabledView(tenant, fromPlan, toPlan string, reason string) map[string]interface{} {
	return map[string]interface{}{
		"enabled":          false,
		"tenant_name":      tenant,
		"from_plan":        fromPlan,
		"to_plan":          toPlan,
		"prices_fen":       machinePlanPricesFen(),
		"units_per_credit": commercialTopupUnitsPerFen,
		"needs_migration":  false,
		"reason":           reason,
	}
}

// HandleMachineUpgrade serves GET /api/cloud-workers/upgrade (quote or operation
// status) and POST /api/cloud-workers/upgrade (execute one quote).
func (h *CloudWorkerHandler) HandleMachineUpgrade(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if operationID := strings.TrimSpace(r.URL.Query().Get("operation_id")); operationID != "" {
			h.handleMachineUpgradeOperation(w, uid, operationID)
			return
		}
		h.handleMachineUpgradeQuote(w, uid, r)
	case http.MethodPost:
		h.handleMachineUpgradeExecute(w, r, uid)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *CloudWorkerHandler) handleMachineUpgradeOperation(w http.ResponseWriter, uid int64, operationID string) {
	store, ok := h.machineUpgradeStoreFor()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "machine upgrade is not available"})
		return
	}
	if len(operationID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid operation id"})
		return
	}
	op, err := store.GetMachineUpgrade(uid, operationID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load machine upgrade"})
		return
	}
	if op == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "machine upgrade not found"})
		return
	}
	writeJSON(w, http.StatusOK, machineUpgradeOperationView(op))
}

func machineUpgradeOperationView(op *types.MachineUpgradeOperation) map[string]interface{} {
	view := map[string]interface{}{
		"operation_id": op.OperationID,
		"status":       op.Status,
		"cost_units":   op.CostUnits,
		"from_plan":    op.FromPlan,
		"to_plan":      op.ToPlan,
		"tenant_name":  op.TenantName,
		"created_at":   op.CreatedAt.UTC().Format(time.RFC3339),
		"attempts":     op.Attempts,
		"needs_review": (op.Status == types.MachineUpgradeStatusRunning || op.Status == types.MachineUpgradeStatusReconciling) && op.Attempts >= types.MachineUpgradeMaxAutomaticAttempts,
	}
	if !op.MachineExpiresAt.IsZero() {
		view["worker_expires_at"] = op.MachineExpiresAt.UTC().Format(time.RFC3339)
	}
	if op.Message != "" {
		view["message"] = op.Message
	}
	if view["needs_review"] == true {
		view["message"] = "自动核实已暂停，待人工核实。点数仍在预占，请提供操作编号联系支持：" + op.OperationID
	}
	if op.FinishedAt != nil {
		view["finished_at"] = op.FinishedAt.UTC().Format(time.RFC3339)
	}
	return view
}

// observedMachinePlan returns a fresh-enough provider observation when the
// installed status adapter reports one.
func (h *CloudWorkerHandler) observedMachinePlan(tenant string) string {
	h.requestCloudStatusRefresh(false)
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if !h.statusLoaded || time.Since(h.statusUpdatedAt) > cloudWorkerStatusMaxTrustAge {
		return ""
	}
	return h.statusSnapshot[tenant].RuntimePlan
}

func (h *CloudWorkerHandler) handleMachineUpgradeQuote(w http.ResponseWriter, uid int64, r *http.Request) {
	tenant := strings.TrimSpace(r.URL.Query().Get("name"))
	toPlan := strings.TrimSpace(r.URL.Query().Get("plan"))
	readConfiguration := r.URL.Query().Get("view") == "configuration"
	if tenant == "" || (!readConfiguration && types.MachinePlanRank(toPlan) == 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "worker name and plan are required"})
		return
	}
	store, ok := h.machineUpgradeStoreFor()
	if !ok {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, "", toPlan, "点数升配未配置"))
		return
	}
	lifecycle, err := h.lifecycleByName(uid, tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load cloud worker"})
		return
	}
	if lifecycle == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cloud worker not found"})
		return
	}
	if !h.tenantRunsOnPackedShared(tenant) {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, "", toPlan, "只有共享机器支持点数升配"))
		return
	}
	if readConfiguration {
		pricing, err := h.readMachinePlanPricing(store, uid, tenant)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read machine plan"})
			return
		}
		now := time.Now().UTC()
		view := map[string]interface{}{"current_plan": pricing.planAt(now)}
		if plan := h.observedMachinePlan(tenant); plan != "" {
			view["runtime_plan"] = plan
		}
		if pricing.term != nil && pricing.term.ExpiresAt.After(now) {
			view["upgrade_expires_at"] = pricing.term.ExpiresAt.UTC().Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if h.changePlanScript == "" {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, "", toPlan, "升配通道未配置"))
		return
	}
	if lifecycle.State != "active" {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, "", toPlan, "机器当前状态不支持升配"))
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	if lifecycle.PackageExpiresAt.IsZero() || !lifecycle.PackageExpiresAt.After(now) {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, "", toPlan, "机器已过期，请先续费"))
		return
	}
	pricing, err := h.readMachinePlanPricing(store, uid, tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read machine plan"})
		return
	}
	fromPlan := pricing.planAt(now)
	if types.MachinePlanRank(toPlan) <= types.MachinePlanRank(h.observedMachinePlan(tenant)) {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, fromPlan, toPlan, "目标档位须高于机器当前运行配置"))
		return
	}
	if types.MachinePlanRank(toPlan) <= types.MachinePlanRank(fromPlan) {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, fromPlan, toPlan, "只能升级到更高档位"))
		return
	}
	pending, err := store.MachineUpgradeOpenForTenant(tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read machine upgrade state"})
		return
	}
	if pending {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, fromPlan, toPlan, "已有进行中的升配"))
		return
	}
	costUnits, segments, ok := pricing.price(toPlan, now, lifecycle.PackageExpiresAt)
	if !ok {
		writeJSON(w, http.StatusOK, machineUpgradeDisabledView(tenant, fromPlan, toPlan, "当前剩余时长不足以计算升配差价"))
		return
	}
	balanceUnits, inArrears, err := store.MachineUpgradeCreditBalance(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read credit balance"})
		return
	}
	if inArrears {
		view := machineUpgradeDisabledView(tenant, fromPlan, toPlan, "账户有欠费，请先结清")
		view["balance_units"] = balanceUnits
		writeJSON(w, http.StatusOK, view)
		return
	}
	quote := &types.MachineUpgradeQuote{
		QuoteID:         newMachineUpgradeID("mq"),
		UID:             uid,
		TenantName:      tenant,
		LifecycleID:     lifecycle.ID,
		FromPlan:        fromPlan,
		ToPlan:          toPlan,
		CostUnits:       costUnits,
		WorkerExpiresAt: lifecycle.PackageExpiresAt.UTC(),
		ExpiresAt:       now.Add(machineUpgradeQuoteTTL),
	}
	if err := store.CreateMachineUpgradeQuote(quote); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save machine upgrade quote"})
		return
	}
	writeJSON(w, http.StatusOK, machineUpgradeQuoteView(tenant, fromPlan, toPlan, costUnits, balanceUnits,
		quote.QuoteID, quote.ExpiresAt, quote.WorkerExpiresAt, segments))
}

func (h *CloudWorkerHandler) handleMachineUpgradeExecute(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Name            string `json:"name"`
		Plan            string `json:"plan"`
		QuoteID         string `json:"quote_id"`
		ClientRequestID string `json:"client_request_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Plan = strings.TrimSpace(req.Plan)
	req.QuoteID = strings.TrimSpace(req.QuoteID)
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.Name == "" || types.MachinePlanRank(req.Plan) == 0 || req.QuoteID == "" ||
		!commercialClientRequestPattern.MatchString(req.ClientRequestID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	store, ok := h.machineUpgradeStoreFor()
	if !ok || h.changePlanScript == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "machine upgrade is not available",
			"code":  "machine_upgrade_unavailable",
		})
		return
	}
	// 一个 owner 同时只能有一台机器在动，进程内锁先挡一次，数据库里的持久操作用
	// 唯一索引挡跨进程的第二次。
	opLock, locked := h.tryBeginOperation(w, uid)
	if !locked {
		return
	}
	defer opLock.Unlock()
	releaseGate := func() {}
	if gate, ok := h.credits.(commercialWorkerOwnerGate); ok {
		release, acquired, err := gate.AcquireCommercialWorkerOwnerGate(uid)
		if err != nil || !acquired {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "another cloud worker operation is already running for this account",
				"code":  "cloud_worker_operation_busy",
			})
			return
		}
		releaseGate = release
	}
	defer releaseGate()

	quote, err := store.GetMachineUpgradeQuote(uid, req.QuoteID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load machine upgrade quote"})
		return
	}
	if quote == nil || quote.TenantName != req.Name || quote.ToPlan != req.Plan {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "machine upgrade quote is not valid for this request",
			"code":  "machine_upgrade_quote_conflict",
		})
		return
	}
	// 先落操作（client_request_id 幂等）：重放直接返回已存的操作。这样「机器现在
	// 已经升过了」不会被误判成报价冲突，也绝不会二次扣费或二次执行。
	op, created, err := store.OpenMachineUpgrade(&types.MachineUpgradeOperation{
		OperationID:      newMachineUpgradeID("mu"),
		UID:              uid,
		TenantName:       req.Name,
		LifecycleID:      quote.LifecycleID,
		FromPlan:         quote.FromPlan,
		ToPlan:           quote.ToPlan,
		CostUnits:        quote.CostUnits,
		ClientRequestID:  req.ClientRequestID,
		QuoteID:          quote.QuoteID,
		MachineExpiresAt: quote.WorkerExpiresAt.UTC(),
	})
	if err != nil {
		if errors.Is(err, errMachineUpgradeConflict) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error": "another machine upgrade is already in progress",
				"code":  "machine_upgrade_conflict",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to open machine upgrade"})
		return
	}
	if !created {
		// 同一个 client_request_id 的重放：把已经存下来的操作原样返回，
		// 绝不再扣一次费，也不再跑一次脚本。
		writeJSON(w, http.StatusOK, machineUpgradeOperationView(op))
		return
	}
	// 只有真的要执行时才核对实时状态：报价过期、机器换过、档位变了、价钱变了，
	// 任何一项不一致都返回 409，让用户看到新价格而不是按旧价成交。此时还没动
	// 余额：把操作标记失败即可，别让它占着这台机器的「进行中」名额。
	now := time.Now().UTC()
	staleReason := ""
	if !quote.ExpiresAt.After(now) {
		staleReason = "machine upgrade quote has expired"
	}
	lifecycle, lcErr := h.lifecycleByName(uid, req.Name)
	if lcErr != nil {
		_, _ = store.FailMachineUpgrade(op.OperationID, "failed to load cloud worker before execution")
		writeJSON(w, http.StatusInternalServerError, machineUpgradeOperationView(op))
		return
	}
	if staleReason == "" {
		switch {
		case lifecycle == nil || lifecycle.ID != quote.LifecycleID || lifecycle.State != "active" ||
			!lifecycle.PackageExpiresAt.Equal(quote.WorkerExpiresAt) || !lifecycle.PackageExpiresAt.After(now) ||
			!h.tenantRunsOnPackedShared(req.Name):
			staleReason = "the machine changed since this quote was issued"
		case types.MachinePlanRank(quote.ToPlan) <= types.MachinePlanRank(h.observedMachinePlan(req.Name)):
			staleReason = "the target no longer exceeds the running machine plan"
		default:
			pricing, planErr := h.readMachinePlanPricing(store, uid, req.Name)
			if planErr != nil {
				_, _ = store.FailMachineUpgrade(op.OperationID, "failed to read machine plan before execution")
				writeJSON(w, http.StatusInternalServerError, machineUpgradeOperationView(op))
				return
			}
			if pricing.planAt(now) != quote.FromPlan {
				staleReason = "the machine plan changed since this quote was issued"
			} else if costUnits, ok := pricing.cost(quote.ToPlan, quote.ExpiresAt.Add(-machineUpgradeQuoteTTL), lifecycle.PackageExpiresAt); !ok || costUnits != quote.CostUnits {
				staleReason = "the machine upgrade price changed since this quote was issued"
			}
		}
	}
	if staleReason != "" {
		if failed, failErr := store.FailMachineUpgrade(op.OperationID, staleReason); failErr == nil && failed != nil {
			op = failed
		}
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":        staleReason,
			"code":         "machine_upgrade_quote_conflict",
			"operation_id": op.OperationID,
			"status":       op.Status,
		})
		return
	}
	started, proceed, err := store.StartMachineUpgradeCharge(op.OperationID, false)
	if err != nil {
		message := "failed to hold credit for the machine upgrade"
		code := "machine_upgrade_failed"
		status := http.StatusInternalServerError
		if errors.Is(err, errMachineUpgradeInsufficient) {
			message = "点数不足，未扣费"
			code = "machine_upgrade_insufficient_balance"
			status = http.StatusPaymentRequired
			// 余额不足时什么都没动：把操作标记失败，别让它占着这机器的
			// 「进行中」名额。
			if failed, failErr := store.FailMachineUpgrade(op.OperationID, message); failErr == nil && failed != nil {
				started = failed
			}
		} else if failed, failErr := store.FailMachineUpgrade(op.OperationID, message); failErr == nil && failed != nil {
			started = failed
		}
		view := map[string]interface{}{"error": message, "code": code}
		if started != nil {
			view["operation_id"] = started.OperationID
			view["status"] = started.Status
		}
		writeJSON(w, status, view)
		return
	}
	if !proceed {
		// 另一个请求已经把这笔操作领走了：返回它当下的状态。
		writeJSON(w, http.StatusOK, machineUpgradeOperationView(started))
		return
	}
	h.runMachineUpgradeScript(appliedMachineUpgrade{store: store, op: started}, false)
	latest, err := store.GetMachineUpgrade(uid, started.OperationID)
	if err != nil || latest == nil {
		writeJSON(w, http.StatusAccepted, machineUpgradeOperationView(started))
		return
	}
	writeJSON(w, http.StatusOK, machineUpgradeOperationView(latest))
}

// appliedMachineUpgrade bundles what the script runner needs.
type appliedMachineUpgrade struct {
	store machineUpgradeStore
	op    *types.MachineUpgradeOperation
}

// runMachineUpgradeScript performs the tier change and records the outcome.
//
// Only a clean exit (including the adapter's "noop") completes the operation, and
// only the documented refusal exit code releases the hold. Everything else —
// timeout, killed process, unreadable result — keeps the points held and parks
// the operation in reconciling, because the container may already be bigger.
func (h *CloudWorkerHandler) runMachineUpgradeScript(item appliedMachineUpgrade, reconciling bool) {
	op := item.op
	if op == nil {
		return
	}
	if reconciling {
		lifecycle, lifecycleErr := h.lifecycleByName(op.UID, op.TenantName)
		currentPlan, planErr := h.machineCurrentPlan(item.store, op.UID, op.TenantName)
		if lifecycleErr != nil || lifecycle == nil || lifecycle.ID != op.LifecycleID || lifecycle.State != "active" || !lifecycle.PackageExpiresAt.After(time.Now()) || !h.tenantRunsOnPackedShared(op.TenantName) || planErr != nil || types.MachinePlanRank(currentPlan) > types.MachinePlanRank(op.ToPlan) {
			_, _ = item.store.MarkMachineUpgradeReconciling(op.OperationID, "机器生命周期或档位已变化，预占保留，待人工核实")
			log.Printf("[cloud-worker] machine upgrade REVIEW_REQUIRED tenant=%s op=%s lifecycle_or_plan_changed", op.TenantName, op.OperationID)
			return
		}
	}
	out, err := h.runScript(h.changePlanScript, "--name", op.TenantName, "--plan", op.ToPlan, "--allow-migrate")
	if err == nil && !machineUpgradeSucceeded(out, op.ToPlan) {
		err = errors.New("machine upgrade adapter returned an unconfirmed result")
	}
	if err == nil {
		logCloudWorkerMachineUpgrade(op, "completed", strings.TrimSpace(out))
		if _, doneErr := item.store.CompleteMachineUpgrade(op.OperationID, op.ToPlan, op.FromPlan, op.MachineExpiresAt); doneErr != nil {
			// The machine is upgraded but the ledger could not settle: keep the
			// hold and let reconcile finish it, never refund a bigger machine.
			_, _ = item.store.MarkMachineUpgradeReconciling(op.OperationID, "扣费结算失败："+doneErr.Error())
			log.Printf("[cloud-worker] machine upgrade settle tenant=%s op=%s failed: %v", op.TenantName, op.OperationID, doneErr)
		}
		return
	}
	message := err.Error()
	if machineUpgradeRefused(err, out) {
		logCloudWorkerMachineUpgrade(op, "failed", strings.TrimSpace(out))
		if _, failErr := item.store.FailMachineUpgrade(op.OperationID, "升配被拒绝，未扣除点数："+message); failErr != nil {
			log.Printf("[cloud-worker] machine upgrade release tenant=%s op=%s failed: %v", op.TenantName, op.OperationID, failErr)
		}
		return
	}
	// Unknown outcome. Keep the hold: the container may already run the bigger
	// tier, and refunding now would hand out the upgrade for free.
	log.Printf("[cloud-worker] machine upgrade UNKNOWN tenant=%s op=%s reconciling=%v err=%v output=%s",
		op.TenantName, op.OperationID, reconciling, err, truncateWorkerOutput(out))
	if _, markErr := item.store.MarkMachineUpgradeReconciling(op.OperationID, "结果未知，正在核对："+message); markErr != nil {
		log.Printf("[cloud-worker] machine upgrade mark reconciling tenant=%s op=%s failed: %v", op.TenantName, op.OperationID, markErr)
	}
}

func machineUpgradeSucceeded(output, plan string) bool {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var receipt struct {
		Plan    string `json:"plan"`
		Status  string `json:"status"`
		Pending *bool  `json:"pending"`
	}
	return len(lines) > 0 && json.Unmarshal([]byte(lines[len(lines)-1]), &receipt) == nil &&
		receipt.Plan == plan && receipt.Pending != nil && !*receipt.Pending &&
		(receipt.Status == "changed" || receipt.Status == "noop")
}

// Refund only a documented capacity refusal with explicit evidence that no
// change (including an earlier pending attempt) exists. Text or exit code alone
// cannot prove this across SSH failures and partially applied upgrades.
func machineUpgradeRefused(err error, output string) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != machineUpgradeRefusedExitCode {
		return false
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var receipt struct {
		Status       string `json:"status"`
		Pending      *bool  `json:"pending"`
		NoSideEffect bool   `json:"no_side_effect"`
	}
	if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &receipt) != nil {
		return false
	}
	return receipt.NoSideEffect && receipt.Pending != nil && !*receipt.Pending &&
		(receipt.Status == "insufficient" || receipt.Status == "needs-migrate")
}

func logCloudWorkerMachineUpgrade(op *types.MachineUpgradeOperation, status, output string) {
	log.Printf("[cloud-worker] machine upgrade tenant=%s op=%s %s->%s status=%s cost=%d output=%s",
		op.TenantName, op.OperationID, op.FromPlan, op.ToPlan, status, op.CostUnits, truncateWorkerOutput(output))
}

// ReconcileMachineUpgrades finishes operations whose process died or whose
// script timed out. The change-plan adapter is idempotent (a tenant already on
// the tier answers "noop"), so re-running is the honest way to learn what really
// happened: success settles the hold, a documented refusal releases it, and an
// unknown result stays parked for the next sweep.
func (h *CloudWorkerHandler) ReconcileMachineUpgrades() {
	store, ok := h.machineUpgradeStoreFor()
	if !ok || h.changePlanScript == "" {
		return
	}
	ops, err := store.ListMachineUpgradesForReconcile(machineUpgradeReconcileStale, machineUpgradeReconcileBatch)
	if err != nil {
		log.Printf("[cloud-worker] machine upgrade reconcile list failed: %v", err)
		return
	}
	for _, op := range ops {
		if op == nil {
			continue
		}
		lock := h.ownerOperationLock(op.UID)
		if !lock.TryLock() {
			continue
		}
		func() {
			defer lock.Unlock()
			if gate, ok := h.credits.(commercialWorkerOwnerGate); ok {
				release, acquired, gateErr := gate.AcquireCommercialWorkerOwnerGate(op.UID)
				if gateErr != nil || !acquired {
					return
				}
				defer release()
			}
			latest, loadErr := store.GetMachineUpgrade(op.UID, op.OperationID)
			if loadErr != nil || latest == nil || time.Since(latest.UpdatedAt) < machineUpgradeReconcileStale {
				return
			}
			op = latest
			if op.Attempts >= types.MachineUpgradeMaxAutomaticAttempts {
				return
			}
			// A pending row never reached the atomic hold, so no script ran.
			if op.Status == types.MachineUpgradeStatusPending {
				_, _ = store.FailMachineUpgrade(op.OperationID, "升配执行前中断，未扣费，请重新报价")
				return
			}
			claimed, proceed, claimErr := store.StartMachineUpgradeCharge(op.OperationID, true)
			if claimErr != nil || !proceed {
				return
			}
			h.runMachineUpgradeScript(appliedMachineUpgrade{store: store, op: claimed}, true)
		}()
	}
}

// machineUpgradePlanOverride returns the tier a package-driven sync must not
// undercut: while a bought upgrade is in force, the container keeps the bigger
// tier even if the package that funded the machine is smaller.
func (h *CloudWorkerHandler) machineUpgradePlanOverride(tenant string) string {
	store, ok := h.machineUpgradeStoreFor()
	if !ok {
		return ""
	}
	term, err := store.MachinePlanTerm(tenant)
	if err != nil || term == nil {
		return ""
	}
	if !term.ExpiresAt.After(time.Now().UTC()) {
		return ""
	}
	return term.Plan
}

// effectiveMachinePlan picks the tier to push: the package's tier, unless the
// buyer paid for a bigger one for a window that still covers now.
func (h *CloudWorkerHandler) effectiveMachinePlan(tenant, packageTier string) string {
	override := h.machineUpgradePlanOverride(tenant)
	if types.MachinePlanRank(override) > types.MachinePlanRank(packageTier) {
		return override
	}
	return packageTier
}
