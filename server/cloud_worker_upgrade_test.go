package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// machineUpgradeTestDB 给云机器升配的单测提供一个真实走 HTTP 层的假持久层：
// 报价、操作、权益三张表都在内存里，但接口形状和 postgres 适配器一致。
type machineUpgradeTestDB struct {
	*cloudWorkerTestStore
	deployments map[string]types.CloudWorkerDeployment
	quotes      map[string]*types.MachineUpgradeQuote
	operations  map[string]*types.MachineUpgradeOperation
	terms       map[string]*types.MachinePlanTerm
	balance     int64
	inArrears   bool
	completeErr error
	scriptCalls []string
	summary     *types.CommercialSummary
	summaryErr  error
}

func newMachineUpgradeTestDB() *machineUpgradeTestDB {
	return &machineUpgradeTestDB{
		cloudWorkerTestStore: &cloudWorkerTestStore{},
		deployments:          map[string]types.CloudWorkerDeployment{},
		quotes:               map[string]*types.MachineUpgradeQuote{},
		operations:           map[string]*types.MachineUpgradeOperation{},
		terms:                map[string]*types.MachinePlanTerm{},
	}
}

func (d *machineUpgradeTestDB) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return d.summary, d.summaryErr
}

func (d *machineUpgradeTestDB) GetCloudWorkerDeployment(tenant string) (*types.CloudWorkerDeployment, error) {
	if value, ok := d.deployments[tenant]; ok {
		copy := value
		return &copy, nil
	}
	return nil, nil
}

func (d *machineUpgradeTestDB) CreateMachineUpgradeQuote(quote *types.MachineUpgradeQuote) error {
	copy := *quote
	d.quotes[quote.QuoteID] = &copy
	return nil
}

func (d *machineUpgradeTestDB) GetMachineUpgradeQuote(uid int64, quoteID string) (*types.MachineUpgradeQuote, error) {
	quote := d.quotes[quoteID]
	if quote == nil || quote.UID != uid {
		return nil, nil
	}
	copy := *quote
	return &copy, nil
}

func (d *machineUpgradeTestDB) OpenMachineUpgrade(op *types.MachineUpgradeOperation) (*types.MachineUpgradeOperation, bool, error) {
	for _, existing := range d.operations {
		if existing.UID == op.UID && existing.ClientRequestID == op.ClientRequestID {
			if existing.TenantName != op.TenantName || existing.ToPlan != op.ToPlan || existing.CostUnits != op.CostUnits {
				return nil, false, store.ErrMachineUpgradeConflict
			}
			copy := *existing
			return &copy, false, nil
		}
		if existing.TenantName == op.TenantName && existing.Status != types.MachineUpgradeStatusCompleted &&
			existing.Status != types.MachineUpgradeStatusFailed {
			return nil, false, store.ErrMachineUpgradeConflict
		}
	}
	now := time.Now().UTC()
	stored := *op
	stored.Status = types.MachineUpgradeStatusPending
	stored.CreatedAt = now
	stored.UpdatedAt = now
	d.operations[stored.OperationID] = &stored
	copy := stored
	return &copy, true, nil
}

func (d *machineUpgradeTestDB) GetMachineUpgrade(uid int64, operationID string) (*types.MachineUpgradeOperation, error) {
	op := d.operations[operationID]
	if op == nil || op.UID != uid {
		return nil, nil
	}
	copy := *op
	return &copy, nil
}

func (d *machineUpgradeTestDB) StartMachineUpgradeCharge(operationID string, allowRetry bool) (*types.MachineUpgradeOperation, bool, error) {
	op := d.operations[operationID]
	if op == nil {
		return nil, false, store.ErrMachineUpgradeNotFound
	}
	switch op.Status {
	case types.MachineUpgradeStatusCompleted, types.MachineUpgradeStatusFailed:
		copy := *op
		return &copy, false, nil
	case types.MachineUpgradeStatusRunning, types.MachineUpgradeStatusReconciling:
		if !allowRetry || time.Since(op.UpdatedAt) < machineUpgradeReconcileStale {
			copy := *op
			return &copy, false, nil
		}
	}
	if op.Status == types.MachineUpgradeStatusPending {
		if d.inArrears || d.balance < op.CostUnits {
			return nil, false, store.ErrModelCreditInsufficient
		}
		d.balance -= op.CostUnits
	}
	now := time.Now().UTC()
	op.Status = types.MachineUpgradeStatusRunning
	op.Held = true
	op.Attempts++
	op.StartedAt = &now
	op.UpdatedAt = now
	copy := *op
	return &copy, true, nil
}

func (d *machineUpgradeTestDB) CompleteMachineUpgrade(operationID, plan, basePlan string, machineExpiresAt time.Time) (*types.MachineUpgradeOperation, error) {
	if d.completeErr != nil {
		return nil, d.completeErr
	}
	op := d.operations[operationID]
	if op == nil {
		return nil, store.ErrMachineUpgradeNotFound
	}
	now := time.Now().UTC()
	op.Status = types.MachineUpgradeStatusCompleted
	op.Held = false
	op.Settled = true
	op.FinishedAt = &now
	op.UpdatedAt = now
	d.terms[op.TenantName] = &types.MachinePlanTerm{
		TenantName: op.TenantName, OwnerUID: op.UID, Plan: plan, BasePlan: basePlan,
		OperationID: op.OperationID, ExpiresAt: machineExpiresAt, UpdatedAt: now,
	}
	copy := *op
	return &copy, nil
}

func (d *machineUpgradeTestDB) FailMachineUpgrade(operationID, message string) (*types.MachineUpgradeOperation, error) {
	op := d.operations[operationID]
	if op == nil {
		return nil, store.ErrMachineUpgradeNotFound
	}
	if op.Status == types.MachineUpgradeStatusCompleted {
		copy := *op
		return &copy, nil
	}
	if op.Held {
		d.balance += op.CostUnits
	}
	now := time.Now().UTC()
	op.Status = types.MachineUpgradeStatusFailed
	op.Held = false
	op.Message = message
	op.FinishedAt = &now
	op.UpdatedAt = now
	copy := *op
	return &copy, nil
}

func (d *machineUpgradeTestDB) MarkMachineUpgradeReconciling(operationID, message string) (*types.MachineUpgradeOperation, error) {
	op := d.operations[operationID]
	if op == nil {
		return nil, store.ErrMachineUpgradeNotFound
	}
	op.Status = types.MachineUpgradeStatusReconciling
	op.Message = message
	op.UpdatedAt = time.Now().UTC()
	copy := *op
	return &copy, nil
}

func (d *machineUpgradeTestDB) ListMachineUpgradesForReconcile(stale time.Duration, _ int) ([]*types.MachineUpgradeOperation, error) {
	out := make([]*types.MachineUpgradeOperation, 0, len(d.operations))
	for _, op := range d.operations {
		if time.Since(op.UpdatedAt) >= stale && (op.Status == types.MachineUpgradeStatusPending || op.Status == types.MachineUpgradeStatusRunning || op.Status == types.MachineUpgradeStatusReconciling) {
			copy := *op
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (d *machineUpgradeTestDB) MachinePlanTerm(tenantName string) (*types.MachinePlanTerm, error) {
	term := d.terms[tenantName]
	if term == nil {
		return nil, nil
	}
	copy := *term
	return &copy, nil
}

func (d *machineUpgradeTestDB) MachineUpgradeCreditBalance(int64) (int64, bool, error) {
	return d.balance, d.inArrears, nil
}

func (d *machineUpgradeTestDB) MachineUpgradeOpenForTenant(tenantName string) (bool, error) {
	for _, op := range d.operations {
		if op.TenantName == tenantName && (op.Status == types.MachineUpgradeStatusPending ||
			op.Status == types.MachineUpgradeStatusRunning || op.Status == types.MachineUpgradeStatusReconciling) {
			return true, nil
		}
	}
	return false, nil
}

// machineUpgradeTestHandler wires a packed worker the owner really has, plus a
// credit balance, so the quote path runs end to end.
func machineUpgradeTestHandler(t *testing.T, script string) (*CloudWorkerHandler, *machineUpgradeTestDB) {
	t.Helper()
	const uid = int64(77)
	db := newMachineUpgradeTestDB()
	db.deployments["bot-worker"] = types.CloudWorkerDeployment{Profile: types.CloudWorkerPackedShared, Env: map[string]string{}}
	db.balance = 20000 * commercialTopupUnitsPerFen // 20000 点：够买一整月的最贵档
	h := NewCloudWorkerHandler(db, NewBotHandler(db), CloudWorkerConfig{ChangePlanScript: script})
	h.credits = &platformLifecycleStub{lifecycles: []CloudWorkerLifecycle{{
		ID: 9, OwnerUID: uid, TenantName: "bot-worker", State: "active",
		PackageExpiresAt: time.Now().UTC().Add(15 * 24 * time.Hour),
	}}}
	return h, db
}

func machineUpgradeRequest(method, target string, body string, uid int64) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return req.WithContext(context.WithValue(req.Context(), uidKey, uid))
}

func TestMachineUpgradePriceLadderDefaultsAndOverride(t *testing.T) {
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "")
	prices := machinePlanPricesFen()
	want := map[string]int64{types.MachinePlanLight: 5900, types.MachinePlanStandard: 9900, types.MachinePlanPro: 19900, types.MachinePlanHeavy: 59900}
	for plan, fen := range want {
		if prices[plan] != fen {
			t.Fatalf("price of %s=%d want %d", plan, prices[plan], fen)
		}
	}
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "pro:29900,bogus:1,heavy:-5")
	prices = machinePlanPricesFen()
	if prices[types.MachinePlanPro] != 29900 {
		t.Fatalf("override not applied: %#v", prices)
	}
	if prices[types.MachinePlanHeavy] != 59900 {
		t.Fatalf("an invalid override must keep the default: %#v", prices)
	}
}

func TestMachineUpgradeCostIsProratedByRemainingTerm(t *testing.T) {
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "")
	// 整月：差价 9900-5900 = 4000 分 = 4000 点。
	full, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanStandard, 30*24*time.Hour)
	if !ok || full != 4000*commercialTopupUnitsPerFen {
		t.Fatalf("a full 30-day window must cost the whole difference: %d ok=%v", full, ok)
	}
	// 半个月：一半，且向上取整到 micro unit。
	half, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanStandard, 15*24*time.Hour)
	if !ok || half != 2000*commercialTopupUnitsPerFen {
		t.Fatalf("half of the window must cost half the difference: %d ok=%v", half, ok)
	}
	// 极短剩余时间：仍然要收钱，且只是差价的一小部分，不能算成 0（0 会被
	// 数据库约束拒绝，也等于白送）。
	tinyCost, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanStandard, time.Second)
	if !ok || tinyCost <= 0 || tinyCost >= half/1000 {
		t.Fatalf("a one-second remainder must cost a small non-zero amount: %d ok=%v", tinyCost, ok)
	}
	// 不会超过整月差价：剩余时间超过 30 天按真实比例（这里只验证正数）。
	longer, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanStandard, 45*24*time.Hour)
	if !ok || longer <= full {
		t.Fatalf("a longer remaining term must cost more: %d vs %d", longer, full)
	}
	// 降档/同档/未知档/没有剩余时间都不卖。
	for _, tc := range []struct{ from, to string }{
		{types.MachinePlanPro, types.MachinePlanStandard},
		{types.MachinePlanStandard, types.MachinePlanStandard},
		{"unknown", types.MachinePlanPro},
	} {
		if _, ok := machineUpgradeCostUnits(tc.from, tc.to, 30*24*time.Hour); ok {
			t.Fatalf("%s -> %s must not be quotable", tc.from, tc.to)
		}
	}
	if _, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanPro, 0); ok {
		t.Fatal("an expired term must not be quotable")
	}
	if _, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanPro, 400*24*time.Hour); ok {
		t.Fatal("an implausible term must not be quotable")
	}
}

func TestMachineUpgradeConfigurationIsOwnerScopedAndReadOnly(t *testing.T) {
	h, db := machineUpgradeTestHandler(t, "")
	db.terms["bot-worker"] = &types.MachinePlanTerm{
		OwnerUID: 77, Plan: types.MachinePlanStandard, ExpiresAt: time.Now().Add(time.Hour),
	}
	path := "/api/cloud-workers/upgrade?name=bot-worker&view=configuration"
	other := httptest.NewRecorder()
	h.HandleMachineUpgrade(other, machineUpgradeRequest(http.MethodGet, path, "", 78))
	if other.Code != http.StatusNotFound {
		t.Fatalf("another owner could read configuration: %d %s", other.Code, other.Body.String())
	}
	rec := httptest.NewRecorder()
	h.HandleMachineUpgrade(rec, machineUpgradeRequest(http.MethodGet, path, "", 77))
	var view struct {
		CurrentPlan string `json:"current_plan"`
		ExpiresAt   string `json:"upgrade_expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); rec.Code != http.StatusOK || err != nil ||
		view.CurrentPlan != types.MachinePlanStandard || view.ExpiresAt != db.terms["bot-worker"].ExpiresAt.UTC().Format(time.RFC3339) {
		t.Fatalf("unexpected configuration: %d %s", rec.Code, rec.Body.String())
	}
	if len(db.quotes) != 0 || len(db.operations) != 0 {
		t.Fatal("configuration read created a quote or operation")
	}
}

func TestMachineUpgradeRejectsTargetAlreadyRunning(t *testing.T) {
	h, db := machineUpgradeTestHandler(t, "change-plan-stub")
	quoted := machineUpgradeQuoteForTest(t, h, 77)
	h.statusLoaded = true
	h.statusUpdatedAt = time.Now()
	h.statusSnapshot = parseCloudWorkerStatusTSV("worker-bot-worker\trunning\t-\t-\t1.6.9\t-\t\tpacked\tpro\n")
	rec := httptest.NewRecorder()
	h.HandleMachineUpgrade(rec, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=pro", "", 77))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) || len(db.quotes) != 1 {
		t.Fatalf("same tier quote must be disabled: %d %s", rec.Code, rec.Body.String())
	}
	before := db.balance
	rec = httptest.NewRecorder()
	h.HandleMachineUpgrade(rec, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade", `{"name":"bot-worker","plan":"pro","quote_id":"`+quoted.QuoteID+`","client_request_id":"already-running-1"}`, 77))
	if rec.Code != http.StatusConflict || db.balance != before {
		t.Fatalf("same running tier must not charge: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMachineUpgradeAfterEarlyRenewalProratesEachTier(t *testing.T) {
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "")
	start := time.Now().UTC().Truncate(time.Second)
	const half = 15 * 24 * time.Hour
	pricing := machinePlanPricing{base: types.MachinePlanLight, term: &types.MachinePlanTerm{
		Plan: types.MachinePlanStandard, ExpiresAt: start.Add(half),
	}}
	const want = 12000 * commercialTopupUnitsPerFen // 5000 for standard → pro, 7000 for light → pro.
	if got, ok := pricing.cost(types.MachinePlanPro, start, start.Add(2*half)); !ok || got != want {
		t.Fatalf("combined cost = %d, %v, want %d", got, ok, want)
	}
	_, segments, ok := pricing.price(types.MachinePlanPro, start, start.Add(2*half))
	const halfSeconds = int64(half / time.Second)
	if !ok || len(segments) != 2 ||
		segments[0] != (machineUpgradePriceSegment{FromPlan: types.MachinePlanStandard, Remaining: halfSeconds, DifferenceFen: 10000}) ||
		segments[1] != (machineUpgradePriceSegment{FromPlan: types.MachinePlanLight, Remaining: halfSeconds, DifferenceFen: 14000}) {
		t.Fatalf("incorrect split price breakdown: %+v", segments)
	}
	if got := pricing.planAt(start.Add(half)); got != types.MachinePlanLight {
		t.Fatalf("expired upgrade credited as %q", got)
	}

	h, db := machineUpgradeTestHandler(t, "change-plan-stub")
	h.credits.(*platformLifecycleStub).lifecycles[0].PackageExpiresAt = start.Add(2 * half)
	pricing.term.OwnerUID = 77
	db.terms["bot-worker"] = pricing.term
	quote := machineUpgradeQuoteForTest(t, h, 77)
	quotedAt := db.quotes[quote.QuoteID].ExpiresAt.Add(-machineUpgradeQuoteTTL)
	expected, _ := pricing.cost(types.MachinePlanPro, quotedAt, start.Add(2*half))
	if quote.FromPlan != types.MachinePlanStandard || quote.CostUnits != expected {
		t.Fatalf("unexpected split quote: %+v", quote)
	}
	// The covered portion changes while the current tier stays the same.
	pricing.term.ExpiresAt = start.Add(half / 2)
	before := db.balance
	rec := httptest.NewRecorder()
	h.HandleMachineUpgrade(rec, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade", `{"name":"bot-worker","plan":"pro","quote_id":"`+quote.QuoteID+`","client_request_id":"split-price-stale"}`, 77))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "price changed") || db.balance != before {
		t.Fatalf("changed split quote must not charge: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMachineUpgradeHeavyQuoteShowsLegacyProration(t *testing.T) {
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "")
	start := time.Unix(0, 0).UTC()
	const remaining = 1973782
	end := start.Add(remaining * time.Second)
	cost, segments, ok := (machinePlanPricing{base: types.MachinePlanLight}).price(types.MachinePlanHeavy, start, end)
	if !ok || cost != 41120458334 || len(segments) != 1 ||
		segments[0] != (machineUpgradePriceSegment{FromPlan: types.MachinePlanLight, Remaining: remaining, DifferenceFen: 54000}) {
		t.Fatalf("legacy quote: %d, %+v, %v", cost, segments, ok)
	}
	full, ok := machineUpgradeCostUnits(types.MachinePlanLight, types.MachinePlanHeavy, 30*24*time.Hour)
	if !ok || full != 54000*commercialTopupUnitsPerFen {
		t.Fatalf("full price: %d, %v", full, ok)
	}
}

func TestMachineUpgradeQuoteRequiresPackedOwnedActiveWorker(t *testing.T) {
	const uid = int64(77)
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "")
	h, db := machineUpgradeTestHandler(t, "change-plan-stub")

	// 正常报价：绑定 owner、机器、当前档、到期时间、目标档与费用。
	recorder := httptest.NewRecorder()
	requestStarted := time.Now()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=pro", "", uid))
	if recorder.Code != http.StatusOK {
		t.Fatalf("quote: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var quote struct {
		Enabled        bool                         `json:"enabled"`
		QuoteID        string                       `json:"quote_id"`
		FromPlan       string                       `json:"from_plan"`
		ToPlan         string                       `json:"to_plan"`
		CostUnits      int64                        `json:"cost_units"`
		BalanceUnits   int64                        `json:"balance_units"`
		NeedsMigration bool                         `json:"needs_migration"`
		Prices         map[string]int64             `json:"prices_fen"`
		UnitsPerCredit int64                        `json:"units_per_credit"`
		WorkerExpires  string                       `json:"worker_expires_at"`
		ExpiresAt      string                       `json:"expires_at"`
		ExpiresInMS    *int64                       `json:"expires_in_ms"`
		PricingTerm    int64                        `json:"pricing_term_seconds"`
		Segments       []machineUpgradePriceSegment `json:"pricing_segments"`
		Reason         string                       `json:"reason"`
		Extra          map[string]string            `json:"-"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode quote: %v %s", err, recorder.Body.String())
	}
	if !quote.Enabled || quote.QuoteID == "" || quote.FromPlan != types.MachinePlanLight || quote.ToPlan != types.MachinePlanPro {
		t.Fatalf("unexpected quote: %s", recorder.Body.String())
	}
	if quote.NeedsMigration {
		t.Fatal("packed containers change tier in place; needs_migration must stay false")
	}
	if quote.UnitsPerCredit != commercialTopupUnitsPerFen || quote.Prices[types.MachinePlanPro] != 19900 {
		t.Fatalf("quote must carry the price basis: %s", recorder.Body.String())
	}
	if quote.PricingTerm != machineUpgradeTermSeconds || len(quote.Segments) != 1 || quote.Segments[0].DifferenceFen != 14000 {
		t.Fatalf("quote must explain the remaining-term discount: %s", recorder.Body.String())
	}
	if quote.CostUnits <= 0 || quote.BalanceUnits != 20000*commercialTopupUnitsPerFen {
		t.Fatalf("quote must carry cost and balance: %s", recorder.Body.String())
	}
	expires, err := time.Parse(time.RFC3339, quote.ExpiresAt)
	if err != nil || quote.ExpiresInMS == nil || *quote.ExpiresInMS < 0 || *quote.ExpiresInMS > machineUpgradeQuoteTTL.Milliseconds() {
		t.Fatalf("quote must carry a valid remaining duration: %s", recorder.Body.String())
	}
	if expires.After(time.Now().Add(machineUpgradeQuoteTTL)) {
		t.Fatalf("quote TTL must be at most 5 minutes: %s", quote.ExpiresAt)
	}
	reported := time.Duration(*quote.ExpiresInMS) * time.Millisecond
	if difference := reported - max(0, time.Until(expires)); difference < -time.Millisecond || difference > time.Since(requestStarted) {
		t.Fatalf("remaining duration %v does not match quote expiry %s", reported, expires)
	}
	if len(db.quotes) != 1 {
		t.Fatalf("quote was not persisted: %#v", db.quotes)
	}

	// 降档：enabled=false 并说明原因，不产生报价。
	recorder = httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=light", "", uid))
	if err := json.Unmarshal(recorder.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode downgrade quote: %v %s", err, recorder.Body.String())
	}
	if quote.Enabled || quote.Reason == "" {
		t.Fatalf("a downgrade must be refused with a reason: %s", recorder.Body.String())
	}

	// 别人的机器：404，不泄露是否存在。
	recorder = httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=pro", "", uid+1))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("another owner's worker must 404: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// 非共享机器：明确说明只支持共享机器。
	db.deployments["bot-worker"] = types.CloudWorkerDeployment{Profile: types.CloudWorkerPrivateNAT, Env: map[string]string{}}
	recorder = httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=pro", "", uid))
	if err := json.Unmarshal(recorder.Body.Bytes(), &quote); err != nil {
		t.Fatalf("decode dedicated quote: %v %s", err, recorder.Body.String())
	}
	if quote.Enabled {
		t.Fatalf("dedicated workers must not be upgraded: %s", recorder.Body.String())
	}
}

func TestMachineUpgradeExecuteChargesOnceAndWritesTerm(t *testing.T) {
	const uid = int64(77)
	script := writeMachineUpgradeTestScript(t, `{"plan":"pro","status":"changed","pending":false}`, 0)
	if script == "" {
		t.Skip("no shell available for the change-plan stub")
	}
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "")
	h, db := machineUpgradeTestHandler(t, script)
	before := db.balance

	quote := machineUpgradeQuoteForTest(t, h, uid)
	body := `{"name":"bot-worker","plan":"pro","quote_id":"` + quote.QuoteID + `","client_request_id":"upgrade-request-1"}`
	recorder := httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade", body, uid))
	if recorder.Code != http.StatusOK {
		t.Fatalf("execute: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var op struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
		CostUnits   int64  `json:"cost_units"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &op); err != nil || op.OperationID == "" {
		t.Fatalf("decode operation: %v %s", err, recorder.Body.String())
	}
	if op.Status != types.MachineUpgradeStatusCompleted {
		t.Fatalf("a clean script exit must complete: %s", recorder.Body.String())
	}
	if db.balance != before-op.CostUnits {
		t.Fatalf("balance must drop by exactly the quoted cost: %d -> %d (cost %d)", before, db.balance, op.CostUnits)
	}
	term := db.terms["bot-worker"]
	if term == nil || term.Plan != types.MachinePlanPro || term.OperationID != op.OperationID {
		t.Fatalf("upgrade entitlement was not written: %#v", term)
	}

	// 同一个 client_request_id 再来一次：返回同一条操作，不再扣费、不再执行。
	calls := len(db.operations)
	recorder = httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade", body, uid))
	if recorder.Code != http.StatusOK {
		t.Fatalf("replay: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if db.balance != before-op.CostUnits {
		t.Fatalf("a replay charged again: %d", db.balance)
	}
	if len(db.operations) != calls {
		t.Fatalf("a replay created a second operation: %d -> %d", calls, len(db.operations))
	}

	// 套餐续费（ApplyPlanToOwner）不能把已经买到的 pro 降回 light。
	packageTier := types.MachinePlanLight
	if got := h.effectiveMachinePlan("bot-worker", packageTier); got != types.MachinePlanPro {
		t.Fatalf("a package renewal pulled the machine back to %s", got)
	}
}

func TestMachineUpgradeExecuteRejectsStaleQuote(t *testing.T) {
	const uid = int64(77)
	h, db := machineUpgradeTestHandler(t, "change-plan-stub")
	quote := machineUpgradeQuoteForTest(t, h, uid)
	// 报价过期：必须 409，不能按旧价成交。
	stored := db.quotes[quote.QuoteID]
	stored.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	recorder := httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade",
		`{"name":"bot-worker","plan":"pro","quote_id":"`+quote.QuoteID+`","client_request_id":"upgrade-stale-1"}`, uid))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("an expired quote must conflict: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 操作行会被留下来并标记失败：既没有扣费，也不占着这台机器的「进行中」名额。
	for _, op := range db.operations {
		if op.Status != types.MachineUpgradeStatusFailed || op.Held || op.Settled || op.Attempts != 0 {
			t.Fatalf("a refused quote must leave a failed, unheld operation: %#v", op)
		}
	}
	for _, term := range db.terms {
		t.Fatalf("a refused quote wrote an entitlement: %#v", term)
	}
}

func TestMachineUpgradeExecuteInsufficientBalanceChargesNothing(t *testing.T) {
	const uid = int64(77)
	script := writeMachineUpgradeTestScript(t, `{"plan":"pro","status":"changed","pending":false}`, 0)
	if script == "" {
		t.Skip("no shell available for the change-plan stub")
	}
	h, db := machineUpgradeTestHandler(t, script)
	db.balance = 1 // 远远不够
	quote := machineUpgradeQuoteForTest(t, h, uid)
	recorder := httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade",
		`{"name":"bot-worker","plan":"pro","quote_id":"`+quote.QuoteID+`","client_request_id":"upgrade-poor-1"}`, uid))
	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("insufficient balance must be 402: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if db.balance != 1 {
		t.Fatalf("an insufficient balance must not be charged: %d", db.balance)
	}
	if term := db.terms["bot-worker"]; term != nil {
		t.Fatalf("no entitlement may be written without payment: %#v", term)
	}
}

func TestMachineUpgradeRefusalReleasesButTimeoutKeepsHold(t *testing.T) {
	const uid = int64(77)
	script := writeMachineUpgradeTestScript(t, `{"status":"insufficient","pending":false,"no_side_effect":true}`, 3)
	if script == "" {
		t.Skip("no shell available for the change-plan stub")
	}
	h, db := machineUpgradeTestHandler(t, script)
	before := db.balance
	quote := machineUpgradeQuoteForTest(t, h, uid)
	recorder := httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodPost, "/api/cloud-workers/upgrade",
		`{"name":"bot-worker","plan":"pro","quote_id":"`+quote.QuoteID+`","client_request_id":"upgrade-full-1"}`, uid))
	var op struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &op); err != nil {
		t.Fatalf("decode refusal: %v %s", err, recorder.Body.String())
	}
	if op.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("an explicit capacity refusal must fail, not stay open: %s", recorder.Body.String())
	}
	if db.balance != before {
		t.Fatalf("a refusal must refund the hold: %d -> %d", before, db.balance)
	}

	// 结果未知（超时）必须保留预占并进入 reconciling，绝不退款。
	if machineUpgradeRefused(&exitCodeError{code: 75}, "") {
		t.Fatal("an exit code alone cannot prove a side-effect-free refusal")
	}
	if machineUpgradeRefused(context.DeadlineExceeded, "") {
		t.Fatal("a timeout must not be treated as a refusal")
	}
	if machineUpgradeRefused(&exitCodeError{code: 1}, "connection reset") {
		t.Fatal("an unknown failure must not release the hold")
	}
}

type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return "exit status" }
func (e *exitCodeError) ExitCode() int { return e.code }

// machineUpgradeQuoteForTest asks the handler for a quote and returns it.
func machineUpgradeQuoteForTest(t *testing.T, h *CloudWorkerHandler, uid int64) types.MachineUpgradeQuote {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.HandleMachineUpgrade(recorder, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=pro", "", uid))
	if recorder.Code != http.StatusOK {
		t.Fatalf("quote: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Enabled         bool   `json:"enabled"`
		QuoteID         string `json:"quote_id"`
		FromPlan        string `json:"from_plan"`
		ToPlan          string `json:"to_plan"`
		CostUnits       int64  `json:"cost_units"`
		WorkerExpiresAt string `json:"worker_expires_at"`
		ExpiresAt       string `json:"expires_at"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode quote: %v %s", err, recorder.Body.String())
	}
	if !response.Enabled {
		t.Fatalf("quote was not enabled: %s", recorder.Body.String())
	}
	expires, _ := time.Parse(time.RFC3339, response.ExpiresAt)
	workerExpires, _ := time.Parse(time.RFC3339, response.WorkerExpiresAt)
	return types.MachineUpgradeQuote{
		QuoteID: response.QuoteID, UID: uid, TenantName: "bot-worker", LifecycleID: 9,
		FromPlan: response.FromPlan, ToPlan: response.ToPlan, CostUnits: response.CostUnits,
		ExpiresAt: expires, WorkerExpiresAt: workerExpires,
	}
}

var _ = errors.Is

func writeMachineUpgradeTestScript(t *testing.T, receipt string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upgrade.sh")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n", receipt, code)
	if runtime.GOOS == "windows" {
		path = filepath.Join(t.TempDir(), "upgrade.cmd")
		body = fmt.Sprintf("@echo off\r\necho %s\r\nexit /b %d\r\n", receipt, code)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMachineUpgradeRefusalRequiresExplicitNoSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		code         int
		want         bool
	}{
		{"clear refusal", `{"status":"insufficient","pending":false,"no_side_effect":true}`, 3, true},
		{"needs migration", `{"status":"needs-migrate","pending":false,"no_side_effect":true}`, 3, true},
		{"earlier partial", `{"status":"insufficient","pending":true,"no_side_effect":true}`, 3, false},
		{"missing pending", `{"status":"insufficient","no_side_effect":true}`, 3, false},
		{"missing proof", `{"status":"insufficient","pending":false}`, 3, false},
		{"wrong code", `{"status":"insufficient","pending":false,"no_side_effect":true}`, 1, false},
		{"wrong status", `{"status":"failed","pending":false,"no_side_effect":true}`, 3, false},
		{"malformed", `{"status":`, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := machineUpgradeRefused(&exitCodeError{code: tc.code}, tc.output); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestMachineUpgradeDelayedConfirmationKeepsQuotePrice(t *testing.T) {
	h, db := machineUpgradeTestHandler(t, writeMachineUpgradeTestScript(t, `{"plan":"pro","status":"changed","pending":false}`, 0))
	q := machineUpgradeQuoteForTest(t, h, 77)
	// Simulate an offer issued a minute ago, with its original prorated price.
	stored := db.quotes[q.QuoteID]
	stored.ExpiresAt = stored.ExpiresAt.Add(-time.Minute)
	lc := h.credits.(*platformLifecycleStub).lifecycles[0]
	stored.CostUnits, _ = machineUpgradeCostUnits(stored.FromPlan, stored.ToPlan, lc.PackageExpiresAt.Sub(stored.ExpiresAt.Add(-machineUpgradeQuoteTTL)))
	rec := httptest.NewRecorder()
	h.HandleMachineUpgrade(rec, machineUpgradeRequest("POST", "/api/cloud-workers/upgrade", `{"name":"bot-worker","plan":"pro","quote_id":"`+q.QuoteID+`","client_request_id":"delayed-quote-1"}`, 77))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"completed"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMachineUpgradeRejectsOverflowPrice(t *testing.T) {
	t.Setenv("CATSCO_MACHINE_PLAN_PRICES_FEN", "heavy:9223372036854775807")
	if cost, ok := machineUpgradeCostUnits("light", "heavy", 30*24*time.Hour); ok {
		t.Fatalf("unsafe price accepted: %d", cost)
	}
}

func TestMachineUpgradeUnknownOutcomeReconcilesWithoutSecondCharge(t *testing.T) {
	h, db := machineUpgradeTestHandler(t, writeMachineUpgradeTestScript(t, `{"status":"failed","pending":true}`, 1))
	q := machineUpgradeQuoteForTest(t, h, 77)
	before := db.balance
	rec := httptest.NewRecorder()
	h.HandleMachineUpgrade(rec, machineUpgradeRequest("POST", "/api/cloud-workers/upgrade", `{"name":"bot-worker","plan":"pro","quote_id":"`+q.QuoteID+`","client_request_id":"unknown-outcome-1"}`, 77))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"reconciling"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, op := range db.operations {
		op.UpdatedAt = time.Now().Add(-4 * time.Minute)
	}
	h.changePlanScript = writeMachineUpgradeTestScript(t, `{"plan":"pro","status":"noop","pending":false}`, 0)
	h.ReconcileMachineUpgrades()
	for _, op := range db.operations {
		if op.Status != types.MachineUpgradeStatusCompleted || op.Attempts != 2 {
			t.Fatalf("%+v", op)
		}
	}
	if db.balance != before-q.CostUnits {
		t.Fatalf("retry charged again: %d", db.balance)
	}
	h.ReconcileMachineUpgrades()
	if db.balance != before-q.CostUnits {
		t.Fatal("terminal operation charged again")
	}
}

func TestMachineUpgradeSuccessRequiresFinalTargetReceipt(t *testing.T) {
	for _, output := range []string{"ok", `{}`, `{"plan":"light","status":"changed","pending":false}`, `{"plan":"pro","status":"changed","pending":true}`, `{"plan":"pro","status":"changed"}`} {
		if machineUpgradeSucceeded(output, "pro") {
			t.Fatalf("unconfirmed success accepted: %s", output)
		}
	}
	if !machineUpgradeSucceeded(`{"plan":"pro","status":"noop","pending":false}`, "pro") {
		t.Fatal("confirmed noop rejected")
	}
}

func TestMachineUpgradePlanSyncDoesNotLeakTierAcrossWorkers(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	script := filepath.Join(t.TempDir(), "sync.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\n"
	if runtime.GOOS == "windows" {
		script = filepath.Join(t.TempDir(), "sync.cmd")
		body = "@echo off\r\necho %* >> \"" + logPath + "\"\r\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	h, db := machineUpgradeTestHandler(t, script)
	lister := h.credits.(*platformLifecycleStub)
	lister.lifecycles = append(lister.lifecycles, CloudWorkerLifecycle{ID: 10, OwnerUID: 77, TenantName: "bot-second", State: "active"})
	db.terms["bot-worker"] = &types.MachinePlanTerm{Plan: "pro", ExpiresAt: time.Now().Add(time.Hour)}
	h.ApplyPlanToOwner(77, "light")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--name bot-worker --plan pro") || !strings.Contains(string(data), "--name bot-second --plan light") {
		t.Fatalf("wrong tiers: %s", data)
	}
}

func TestMachineUpgradeQuoteDoesNotSellTierAlreadyGrantedByPackage(t *testing.T) {
	h, db := machineUpgradeTestHandler(t, "unused-script")
	db.terms["bot-worker"] = &types.MachinePlanTerm{
		TenantName: "bot-worker", OwnerUID: 77, Plan: types.MachinePlanStandard,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	db.summary = &types.CommercialSummary{
		CloudWorkerPackageTerm: &types.CloudWorkerPackageTerm{PlanID: 806},
		Plans:                  []*types.CommercialPlan{{ID: 806, Slug: "catsco-team", CloudWorkerRuntimePlan: types.MachinePlanHeavy}},
	}
	response := httptest.NewRecorder()
	h.HandleMachineUpgrade(response, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=pro", "", 77))
	var view map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || view["enabled"] != false || view["from_plan"] != types.MachinePlanHeavy || len(db.quotes) != 0 {
		t.Fatalf("must not sell a downgrade from the package tier: status=%d view=%v", response.Code, view)
	}
	db.summaryErr = errors.New("package lookup unavailable")
	response = httptest.NewRecorder()
	h.HandleMachineUpgrade(response, machineUpgradeRequest(http.MethodGet, "/api/cloud-workers/upgrade?name=bot-worker&plan=heavy", "", 77))
	if response.Code != http.StatusInternalServerError || len(db.quotes) != 0 {
		t.Fatalf("unreadable package must not issue a quote: status=%d", response.Code)
	}
}
