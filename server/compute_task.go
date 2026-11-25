package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openchat/openchat/server/store"
)

const (
	computeTaskQuoteTool        = "catsco_compute_task_quote"
	computeTaskStatusTool       = "catsco_compute_task_status"
	computeTaskQuoteTTL         = 5 * time.Minute
	computeTaskActivationWindow = 20 * time.Minute
	computeTaskMaxNodes         = 2
	computeTaskMaxWallSeconds   = 300
	computeTaskRoute            = "/api/compute/tasks/"
	computeTaskWorkerRoute      = "/api/compute/worker"
	computeTaskConfirmPage      = "/compute/confirm"
)

var computeTaskIDPattern = regexp.MustCompile(`^hpc_[0-9a-f]{32}$`)
var computeTaskSKUPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

// computeTaskSKU pins the Slurm-usable capacity and customer tariff of an
// operator-verified on-demand node shape in the managed HPC pool.
type computeTaskSKU struct {
	ID          string           `json:"id"`
	LogicalCPUs int              `json:"logical_cpus"`
	MemoryMiB   int              `json:"memory_mib"`
	Tariff      computeSlurmRate `json:"tariff"`
}

// computeTaskReceiptKicker 在 worker finish 成功后唤醒一轮回执投递
// （*ComputeTaskReceiptDeliverer 实现；未注入时靠周期扫描兜底）。
type computeTaskReceiptKicker interface {
	Kick()
}

type ComputeTaskHandler struct {
	store         store.ComputeTaskStore
	catalog       []computeTaskSKU
	workerToken   string
	allowed       map[int64]bool
	publicBaseURL string
	// receiptKicker 可选：装配处注入与投递器同一个实例，finish 成功即时投递。
	receiptKicker computeTaskReceiptKicker
}

// SetReceiptKicker 注入回执投递器的 Kick（不注入 = 只靠投递器周期扫描）。
// 传入 nil 指针也安全（deliverer 的 Kick 对 nil 接收者无操作）。
func (h *ComputeTaskHandler) SetReceiptKicker(kicker computeTaskReceiptKicker) {
	if h == nil {
		return
	}
	h.receiptKicker = kicker
}

func NewComputeTaskHandlerFromEnv(st store.ComputeTaskStore) (*ComputeTaskHandler, error) {
	file := strings.TrimSpace(os.Getenv("CATSCOMPANY_COMPUTE_TASK_CATALOG_FILE"))
	token := strings.TrimSpace(os.Getenv("CATSCOMPANY_COMPUTE_TASK_WORKER_TOKEN"))
	accounts := strings.TrimSpace(os.Getenv("CATSCOMPANY_COMPUTE_TASK_ACCOUNTS"))
	if file == "" && token == "" && accounts == "" {
		return nil, nil
	}
	if st == nil || len(token) < 32 || accounts == "" || file == "" {
		return nil, errors.New("compute task store, catalog, worker token and account allowlist are required")
	}
	allowed := make(map[int64]bool)
	for _, account := range strings.Split(accounts, ",") {
		uid, err := strconv.ParseInt(strings.TrimSpace(account), 10, 64)
		if err != nil || uid <= 0 {
			return nil, errors.New("invalid compute task account allowlist")
		}
		allowed[uid] = true
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("open compute task catalog: %w", err)
	}
	defer f.Close()
	var catalog []computeTaskSKU
	if err := decodeComputeTaskJSON(f, 64<<10, &catalog); err != nil {
		return nil, fmt.Errorf("decode compute task catalog: %w", err)
	}
	if len(catalog) == 0 || len(catalog) > 32 {
		return nil, errors.New("invalid compute task catalog size")
	}
	seen := map[string]bool{}
	for _, sku := range catalog {
		if !computeTaskSKUPattern.MatchString(sku.ID) || seen[sku.ID] || sku.LogicalCPUs < 1 || sku.MemoryMiB < 1 {
			return nil, errors.New("invalid compute task SKU capacity or ID")
		}
		seen[sku.ID] = true
	}
	return &ComputeTaskHandler{
		store: st, catalog: catalog, workerToken: token, allowed: allowed,
		publicBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL")), "/"),
	}, nil
}

func (h *ComputeTaskHandler) RegisterRoutes(mux *http.ServeMux, authenticate func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc(computeTaskRoute, authenticate(h.serveAccount))
	mux.HandleFunc(computeTaskWorkerRoute, h.serveWorker)
}

func newComputeTaskID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "hpc_" + hex.EncodeToString(random[:]), nil
}

func decodeComputeTaskJSON(r io.Reader, limit int64, result any) error {
	bounded := &io.LimitedReader{R: r, N: limit + 1}
	decoder := json.NewDecoder(bounded)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF || bounded.N == 0 {
		return errors.New("expected one bounded JSON value")
	}
	return nil
}

func computeTaskAllocation(spec store.ComputeTaskSpec) bool {
	return spec.Nodes > 0 && spec.Nodes <= computeTaskMaxNodes && spec.LogicalCPUs > 0 && spec.MemoryMiB > 0 &&
		spec.WallSeconds >= 60 && spec.WallSeconds <= computeTaskMaxWallSeconds && spec.WallSeconds%60 == 0
}

func computeTaskCharge(rate store.ComputeTaskRate, nodes int, seconds int64) (int64, bool) {
	if nodes < 1 || seconds < 0 || rate.UnitsPerNodePeriod < 1 || rate.PeriodSeconds < 1 || rate.IncrementSeconds < 1 {
		return 0, false
	}
	if seconds == 0 {
		return 0, true
	}
	billed := big.NewInt(1 + (seconds-1)/rate.IncrementSeconds)
	billed.Mul(billed, big.NewInt(rate.IncrementSeconds))
	billed.Mul(billed, big.NewInt(int64(nodes)))
	billed.Mul(billed, big.NewInt(rate.UnitsPerNodePeriod))
	billed.Add(billed, big.NewInt(rate.PeriodSeconds-1))
	billed.Quo(billed, big.NewInt(rate.PeriodSeconds))
	if !billed.IsInt64() || billed.Int64() > maxComputeJSONInteger {
		return 0, false
	}
	return billed.Int64(), true
}

func chooseComputeTaskQuote(spec store.ComputeTaskSpec, catalog []computeTaskSKU, now time.Time) (store.ComputeTaskQuote, bool) {
	var best store.ComputeTaskQuote
	maxSeconds := int64(computeTaskActivationWindow/time.Second) + int64(spec.WallSeconds)
	for _, sku := range catalog {
		if sku.LogicalCPUs < spec.LogicalCPUs || sku.MemoryMiB < spec.MemoryMiB {
			continue
		}
		price := estimateSlurmCredits(computePreviewTask{}, &sku.Tariff, now)
		if price.Rate == nil {
			continue
		}
		rate := store.ComputeTaskRate{UnitsPerNodePeriod: price.Rate.UnitsPerNodePeriod, PeriodSeconds: price.Rate.PeriodS, IncrementSeconds: price.Rate.IncrementS}
		amount, ok := computeTaskCharge(rate, spec.Nodes, maxSeconds)
		if !ok || amount == 0 || best.MaxUnits > 0 && (amount > best.MaxUnits || amount == best.MaxUnits && sku.ID >= best.SKU) {
			continue
		}
		best = store.ComputeTaskQuote{
			Spec: spec, SKU: sku.ID, SKUCPUs: sku.LogicalCPUs, SKUMemoryMiB: sku.MemoryMiB, Rate: rate,
			MaxUnits: amount, MaxNodeSeconds: int64(spec.Nodes) * maxSeconds,
			ExpiresAt: now.Add(computeTaskQuoteTTL),
		}
		if sku.Tariff.ValidUntil.Before(best.ExpiresAt) {
			best.ExpiresAt = sku.Tariff.ValidUntil
		}
	}
	return best, best.MaxUnits > 0
}

func computeTaskResult(result any, text string) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}, &mcp.TextContent{Text: string(data)}}}, result, nil
}

func (h *ComputeTaskHandler) addTools(srv *mcp.Server, uid int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        computeTaskQuoteTool,
		Description: "为固定 Cats 托管 Slurm CPU 验收作业报价。根据任务填写节点数、每节点逻辑 CPU 与 MiB 内存、最长作业时限。支持 1 至 2 节点和 60 至 300 秒的整分钟时限。返回规格、积分上限及本人确认链接。让用户在网页核对并确认。供应商实时库存和 ETA 未知。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, spec store.ComputeTaskSpec) (*mcp.CallToolResult, any, error) {
		if !computeTaskAllocation(spec) {
			return serviceToolError("invalid_compute_task"), nil, nil
		}
		quote, ok := chooseComputeTaskQuote(spec, h.catalog, time.Now().UTC())
		if !ok {
			return serviceToolError("no_priced_compute_shape"), nil, nil
		}
		id, err := newComputeTaskID()
		if err != nil {
			return serviceToolError("compute_quote_unavailable"), nil, nil
		}
		quote.ID = id
		if err := h.store.CreateComputeTaskQuote(ctx, uid, quote); err != nil {
			return serviceToolError("compute_quote_unavailable"), nil, nil
		}
		path := computeTaskConfirmPage + "?quote=" + id
		return computeTaskResult(struct {
			store.ComputeTaskQuote
			ConfirmURL     string `json:"confirm_url"`
			UnitsPerCredit int64  `json:"units_per_credit"`
		}{quote, h.publicBaseURL + path, store.ModelCreditUnitsPerCredit},
			fmt.Sprintf("%s，最多 %s 积分。请本人打开 %s 确认。", quote.SKU, computeCreditText(quote.MaxUnits), h.publicBaseURL+path))
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: computeTaskStatusTool, Description: "用 quote_id 查本账号的 Cats 算力任务、云操作号、Slurm 作业号、回收与实际扣费。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args struct {
		ID string `json:"quote_id"`
	}) (*mcp.CallToolResult, any, error) {
		if !computeTaskIDPattern.MatchString(args.ID) {
			return serviceToolError("compute_task_not_found"), nil, nil
		}
		run, err := h.store.GetComputeTask(ctx, uid, args.ID)
		if err != nil || run == nil {
			return serviceToolError("compute_task_not_found"), nil, nil
		}
		return computeTaskResult(run, "Cats 算力任务状态："+run.State)
	})
}

func computeTaskHTTPError(w http.ResponseWriter, status int, code string) {
	writeServiceJSON(w, status, map[string]string{"error": code})
}

func (h *ComputeTaskHandler) serveAccount(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	id := strings.TrimPrefix(r.URL.Path, computeTaskRoute)
	w.Header().Set("Cache-Control", "no-store")
	if !h.allowed[uid] || !computeTaskIDPattern.MatchString(id) {
		computeTaskHTTPError(w, http.StatusNotFound, "compute_task_not_found")
		return
	}
	var run *store.ComputeTaskRun
	var err error
	switch r.Method {
	case http.MethodGet:
		run, err = h.store.GetComputeTask(r.Context(), uid, id)
	case http.MethodPost:
		var in struct {
			MaxUnits int64 `json:"max_units"`
		}
		if decodeComputeTaskJSON(r.Body, 1024, &in) != nil || in.MaxUnits <= 0 {
			computeTaskHTTPError(w, http.StatusBadRequest, "invalid_compute_confirmation")
			return
		}
		run, err = h.store.ConfirmComputeTask(r.Context(), uid, id, in.MaxUnits, time.Now().UTC())
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch {
	case errors.Is(err, store.ErrComputeTaskNotFound) || err == nil && run == nil:
		computeTaskHTTPError(w, http.StatusNotFound, "compute_task_not_found")
	case errors.Is(err, store.ErrComputeQuoteExpired):
		computeTaskHTTPError(w, http.StatusConflict, "compute_quote_expired")
	case errors.Is(err, store.ErrComputeTaskConflict):
		computeTaskHTTPError(w, http.StatusConflict, "compute_quote_mismatch")
	case errors.Is(err, store.ErrModelCreditInsufficient):
		computeTaskHTTPError(w, http.StatusPaymentRequired, "compute_credit_insufficient")
	case err != nil:
		computeTaskHTTPError(w, http.StatusServiceUnavailable, "compute_task_unavailable")
	default:
		writeServiceJSON(w, http.StatusOK, struct {
			*store.ComputeTaskRun
			UnitsPerCredit int64 `json:"units_per_credit"`
		}{run, store.ModelCreditUnitsPerCredit})
	}
}

func (h *ComputeTaskHandler) serveWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.Method != http.MethodPost || !bearer || subtle.ConstantTimeCompare([]byte(token), []byte(h.workerToken)) != 1 {
		computeTaskHTTPError(w, http.StatusUnauthorized, "compute_worker_unauthorized")
		return
	}
	var in struct {
		Action   string                    `json:"action"`
		ID       string                    `json:"quote_id,omitempty"`
		Lease    string                    `json:"lease,omitempty"`
		Progress store.ComputeTaskProgress `json:"progress,omitempty"`
	}
	if decodeComputeTaskJSON(r.Body, 16<<10, &in) != nil {
		computeTaskHTTPError(w, http.StatusBadRequest, "invalid_compute_worker_request")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	if in.Action == "claim" {
		h.claim(ctx, w, now)
		return
	}
	if !computeTaskIDPattern.MatchString(in.ID) || !computeTaskIDPattern.MatchString(in.Lease) {
		computeTaskHTTPError(w, http.StatusBadRequest, "invalid_compute_worker_identity")
		return
	}
	var err error
	switch in.Action {
	case "save":
		err = h.store.SaveComputeTaskProgress(ctx, in.ID, in.Lease, in.Progress)
	case "finish":
		var run *store.ComputeTaskRun
		run, err = h.store.GetComputeTaskForWorker(ctx, in.ID)
		if err == nil {
			amount, success, valid := computeTaskSettlement(run, in.Progress, now)
			if !valid {
				computeTaskHTTPError(w, http.StatusUnprocessableEntity, "compute_release_unverified")
				return
			}
			err = h.store.FinishComputeTask(ctx, in.ID, in.Lease, in.Progress, amount, success, now)
		}
	default:
		computeTaskHTTPError(w, http.StatusBadRequest, "unknown_compute_worker_action")
		return
	}
	if err != nil {
		computeTaskHTTPError(w, http.StatusConflict, "compute_task_update_conflict")
		return
	}
	// finish 已经提交（回执与结算同事务入箱）：立刻唤醒投递器，别让调用方等到下一个
	// 周期扫描；Kick 丢了也有 5 秒兜底，不影响正确性。
	if in.Action == "finish" && h.receiptKicker != nil {
		h.receiptKicker.Kick()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ComputeTaskHandler) claim(ctx context.Context, w http.ResponseWriter, now time.Time) {
	lease, err := newComputeTaskID()
	if err != nil {
		computeTaskHTTPError(w, http.StatusServiceUnavailable, "compute_claim_unavailable")
		return
	}
	run, err := h.store.ClaimComputeTask(ctx, lease, now, now.Add(store.ComputeTaskLeaseTTL))
	if err != nil {
		computeTaskHTTPError(w, http.StatusServiceUnavailable, "compute_claim_unavailable")
		return
	}
	if run == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeServiceJSON(w, http.StatusOK, struct {
		*store.ComputeTaskRun
		Lease string `json:"lease"`
	}{run, run.Lease})
}

func computeTaskSettlement(run *store.ComputeTaskRun, progress store.ComputeTaskProgress, now time.Time) (amount int64, success, valid bool) {
	if run == nil || run.ConfirmedAt == nil || !reflect.DeepEqual(run.Progress, progress) ||
		progress.ReleasedAt == nil || progress.ReleasedAt.Before(*run.ConfirmedAt) || progress.ReleasedAt.After(now.Add(5*time.Second)) {
		return 0, false, false
	}
	if progress.QueueID != "" && progress.QueueDeleteOperation == "" {
		return 0, false, false
	}
	nodes := len(progress.NodeIDs)
	if nodes == 0 {
		return 0, false, progress.Phase == "rejected" && progress.Problem != "" && progress.SlurmJobID == "" &&
			progress.ExpandOperation == "" && progress.ProvisionStartedAt == nil && len(progress.InstanceIDs) == 0 && len(progress.VolumeIDs) == 0
	}
	if progress.Phase != "released" || progress.QueueID == "" || progress.ProvisionStartedAt == nil ||
		progress.ProvisionStartedAt.Before(*run.ConfirmedAt) || !progress.ReleasedAt.After(*progress.ProvisionStartedAt) || progress.ExpandOperation == "" {
		return 0, false, false
	}
	if nodes > run.Spec.Nodes || len(progress.NodeNames) != nodes || len(progress.InstanceIDs) != nodes || len(progress.VolumeIDs) < nodes || progress.ShrinkOperation == "" {
		return 0, false, false
	}
	success = progress.SlurmState == "COMPLETED" && progress.SlurmExitCode == "0:0" && progress.SlurmJobID != "" &&
		nodes == run.Spec.Nodes && slices.Equal(sortedComputeNodes(progress.SlurmNodeList), sortedComputeNodes(progress.NodeNames)) &&
		progress.SlurmAllocatedCPUs >= nodes*run.Spec.LogicalCPUs && progress.SlurmElapsed >= 0 && progress.SlurmElapsed <= int64(run.Spec.WallSeconds)
	if !success && progress.Problem == "" {
		return 0, false, false
	}
	seconds := int64(1 + (progress.ReleasedAt.Sub(*progress.ProvisionStartedAt)-1)/time.Second)
	if seconds > run.MaxNodeSeconds/int64(nodes) {
		return run.MaxUnits, success, true
	}
	amount, valid = computeTaskCharge(run.Rate, nodes, seconds)
	return min(amount, run.MaxUnits), success, valid
}

func sortedComputeNodes(names []string) []string {
	result := slices.Clone(names)
	slices.Sort(result)
	return result
}
