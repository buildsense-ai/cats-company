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
	computeToolName       = "catsco_compute"
	computeTaskQuoteTool  = "catsco_compute_task_quote"
	computeTaskStatusTool = "catsco_compute_task_status"
	computeTaskQuoteTTL   = 5 * time.Minute
	computeTaskMaxNodes   = 2
	// Bound the Slurm time conversion, including the two-minute deadline offset.
	computeTaskMaxSlurmWallSeconds = 1<<31 - 1 - 120
	computeTaskRoute               = "/api/compute/tasks/"
	computeTaskWorkerRoute         = "/api/compute/worker"
	computeTaskConfirmPage         = "/compute/confirm"
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
	store          store.ComputeTaskStore
	catalog        []computeTaskSKU
	workerToken    string
	allowed        map[int64]bool
	publicBaseURL  string
	capacityPolicy store.ComputeTaskAdmissionPolicy
	maxWallSeconds int
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

func computeTaskAdmissionPolicyFromEnv() (store.ComputeTaskAdmissionPolicy, error) {
	var policy store.ComputeTaskAdmissionPolicy
	values := []struct {
		name  string
		value *int
	}{
		{"CATSCOMPANY_COMPUTE_MAX_TOTAL_NODES", &policy.MaxTotalNodes},
		{"CATSCOMPANY_COMPUTE_MAX_CONCURRENT_TASKS", &policy.MaxConcurrentTasks},
		{"CATSCOMPANY_COMPUTE_MAX_USER_NODES", &policy.MaxUserNodes},
		{"CATSCOMPANY_COMPUTE_MAX_USER_TASKS", &policy.MaxUserTasks},
	}
	for _, item := range values {
		raw := strings.TrimSpace(os.Getenv(item.name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || value < 0 {
			return store.ComputeTaskAdmissionPolicy{}, fmt.Errorf("%s must be a non-negative integer", item.name)
		}
		*item.value = int(value)
	}
	return policy, nil
}

func computeTaskMaxWallSecondsFromEnv() (int, error) {
	const key = "CATSCOMPANY_COMPUTE_MAX_WALL_SECONDS"
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 || seconds%60 != 0 || int64(seconds) > computeTaskMaxSlurmWallSeconds {
		return 0, fmt.Errorf("%s must be zero or a supported whole number of minutes expressed in seconds", key)
	}
	return seconds, nil
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
	capacityPolicy, err := computeTaskAdmissionPolicyFromEnv()
	if err != nil {
		return nil, err
	}
	maxWallSeconds, err := computeTaskMaxWallSecondsFromEnv()
	if err != nil {
		return nil, err
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
		publicBaseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL")), "/"),
		capacityPolicy: capacityPolicy,
		maxWallSeconds: maxWallSeconds,
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

func (h *ComputeTaskHandler) maxNodesPerTask() int {
	limit := computeTaskMaxNodes
	for _, capacity := range []int{h.capacityPolicy.MaxTotalNodes, h.capacityPolicy.MaxUserNodes} {
		if capacity > 0 {
			limit = min(limit, capacity)
		}
	}
	return limit
}

// computeTaskBillingCeiling is the per-node billable ceiling: exactly the job wall time the
// caller asked for. Provisioning, Slurm hand-off and release are operator cost and never
// extend what a user can be charged.
func computeTaskBillingCeiling(wallSeconds, nodes int) (int64, bool) {
	if nodes < 1 || wallSeconds < 60 || wallSeconds%60 != 0 || int64(wallSeconds) > computeTaskMaxSlurmWallSeconds {
		return 0, false
	}
	if int64(wallSeconds) > maxComputeJSONInteger/int64(nodes) {
		return 0, false
	}
	return int64(wallSeconds), true
}

func (h *ComputeTaskHandler) acceptsAllocation(spec store.ComputeTaskSpec) bool {
	if _, ok := computeTaskBillingCeiling(spec.WallSeconds, spec.Nodes); !ok {
		return false
	}
	return spec.Nodes <= h.maxNodesPerTask() && spec.LogicalCPUs > 0 && spec.MemoryMiB > 0 &&
		(h.maxWallSeconds == 0 || spec.WallSeconds <= h.maxWallSeconds)
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

func (sku computeTaskSKU) rate(now time.Time) (store.ComputeTaskRate, bool) {
	price := estimateSlurmCredits(computePreviewTask{}, &sku.Tariff, now)
	if price.Rate == nil {
		return store.ComputeTaskRate{}, false
	}
	return store.ComputeTaskRate{UnitsPerNodePeriod: price.Rate.UnitsPerNodePeriod, PeriodSeconds: price.Rate.PeriodS, IncrementSeconds: price.Rate.IncrementS}, true
}

func chooseComputeTaskQuote(spec store.ComputeTaskSpec, catalog []computeTaskSKU, now time.Time) (store.ComputeTaskQuote, bool) {
	var best store.ComputeTaskQuote
	ceiling, ok := computeTaskBillingCeiling(spec.WallSeconds, spec.Nodes)
	if !ok {
		return best, false
	}
	spec.MaxResourceSecondsPerNode = int(ceiling)
	for _, sku := range catalog {
		if sku.LogicalCPUs < spec.LogicalCPUs || sku.MemoryMiB < spec.MemoryMiB {
			continue
		}
		rate, available := sku.rate(now)
		if !available {
			continue
		}
		amount, ok := computeTaskCharge(rate, spec.Nodes, ceiling)
		if !ok || amount == 0 || best.MaxUnits > 0 && (amount > best.MaxUnits || amount == best.MaxUnits && sku.ID >= best.SKU) {
			continue
		}
		best = store.ComputeTaskQuote{
			Spec: spec, SKU: sku.ID, SKUCPUs: sku.LogicalCPUs, SKUMemoryMiB: sku.MemoryMiB, Rate: rate,
			MaxUnits: amount, MaxNodeSeconds: int64(spec.Nodes) * ceiling,
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
func computeTaskInputSchema(maxWallSeconds int) map[string]any {
	wallSeconds := map[string]any{"type": "integer", "minimum": 60, "multipleOf": 60, "description": "quote 的 Slurm 作业最长运行秒数，按实际任务填写整分钟"}
	if maxWallSeconds > 0 {
		wallSeconds["maximum"] = maxWallSeconds
	}
	taskProperties := map[string]any{
		"topology":             map[string]any{"type": "string", "enum": []string{"independent_shards", "distributed"}, "description": "preview 时使用。independent_shards 表示独立分片，distributed 表示分布式节点"},
		"shards":               map[string]any{"type": "integer", "minimum": 1, "description": "preview 的独立分片数，topology=independent_shards 时填写"},
		"nodes":                map[string]any{"type": "integer", "minimum": 1, "description": "quote 的节点数，或 preview 分布式任务的节点数。报价节点上限可通过 catalog 查询"},
		"logical_cpus":         map[string]any{"type": "integer", "minimum": 1, "description": "每分片或每节点最低逻辑 CPU 数"},
		"memory_mib":           map[string]any{"type": "integer", "minimum": 1, "description": "每分片或每节点最低内存 MiB"},
		"gpu_count":            map[string]any{"type": "integer", "minimum": 0, "description": "preview 的每分片或每节点最低 GPU 数，没有 GPU 需求填 0"},
		"memory_mib_per_gpu":   map[string]any{"type": "integer", "minimum": 0, "description": "preview 的每块 GPU 最低显存 MiB"},
		"wall_seconds":         wallSeconds,
		"estimated_billable_s": map[string]any{"type": "integer", "minimum": 0, "description": "preview 的预览假设时长，独立任务按每分片填写，分布式任务按每节点填写。没有依据时省略"},
	}
	runtimeProperties := map[string]any{
		"scope":                map[string]any{"type": "string", "enum": []string{"agent_runtime"}, "description": "必须为 agent_runtime"},
		"platform":             map[string]any{"type": "string", "description": "本机平台名称"},
		"sampledAt":            map[string]any{"type": "string", "format": "date-time", "description": "资源采样时间"},
		"cpuLogicalCount":      map[string]any{"type": "integer", "description": "本机逻辑 CPU 数"},
		"totalMemoryBytes":     map[string]any{"type": "integer", "description": "本机总内存字节数"},
		"availableMemoryBytes": map[string]any{"type": "integer", "description": "本机当前可用内存字节数"},
		"nvidiaGpus":           map[string]any{"type": "array", "description": "本机 NVIDIA GPU 采样", "items": map[string]any{"type": "object"}},
	}
	return map[string]any{
		"type":        "object",
		"description": "Cats 统一算力入口。catalog 查询报价规格和限制，preview 比较资源，quote 创建需本人确认的报价，status 查询任务。",
		"properties": map[string]any{
			"action":        map[string]any{"type": "string", "enum": []string{"catalog", "preview", "quote", "status"}, "description": "catalog=报价规格和限制，preview=资源比较，quote=正式报价，status=查询已创建任务"},
			"task":          map[string]any{"type": "object", "description": "preview 或 quote 的任务参数。字段含义和必填条件由 action 决定", "properties": taskProperties},
			"agent_runtime": map[string]any{"type": "object", "description": "可选的 local_compute_resources 采样结果，仅 preview 使用", "properties": runtimeProperties},
			"quote_id":      map[string]any{"type": "string", "description": "status 使用的报价编号，形如 hpc_ 加 32 位小写十六进制字符"},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"action": map[string]any{"const": "catalog"}}},
			map[string]any{"properties": map[string]any{"action": map[string]any{"const": "preview"}}, "required": []string{"action", "task"}},
			map[string]any{"properties": map[string]any{"action": map[string]any{"const": "quote"}}, "required": []string{"action", "task"}},
			map[string]any{"properties": map[string]any{"action": map[string]any{"const": "status"}}, "required": []string{"action", "quote_id"}},
		},
	}
}

func (h *ComputeTaskHandler) taskCatalog(now time.Time) (*mcp.CallToolResult, any, error) {
	if h == nil || h.store == nil {
		return serviceToolError("compute_task_unavailable"), nil, nil
	}
	type pricedSKU struct {
		ID          string                `json:"id"`
		LogicalCPUs int                   `json:"logical_cpus_per_node"`
		MemoryMiB   int                   `json:"memory_mib_per_node"`
		Rate        store.ComputeTaskRate `json:"rate"`
		ValidUntil  time.Time             `json:"valid_until"`
	}
	result := struct {
		SKUs            []pricedSKU `json:"skus"`
		MaxNodesPerTask int         `json:"max_nodes_per_task"`
		MaxWallSeconds  int         `json:"max_wall_seconds,omitempty"`
		UnitsPerCredit  int64       `json:"units_per_credit"`
	}{
		SKUs: make([]pricedSKU, 0, len(h.catalog)), MaxNodesPerTask: h.maxNodesPerTask(),
		MaxWallSeconds: h.maxWallSeconds, UnitsPerCredit: store.ModelCreditUnitsPerCredit,
	}
	for _, sku := range h.catalog {
		if rate, ok := sku.rate(now); ok {
			result.SKUs = append(result.SKUs, pricedSKU{
				ID: sku.ID, LogicalCPUs: sku.LogicalCPUs, MemoryMiB: sku.MemoryMiB,
				Rate: rate, ValidUntil: sku.Tariff.ValidUntil,
			})
		}
	}
	return computeTaskResult(result, "Cats 按同规格整节点计费。下列为当前有效报价规格，实时库存需在执行时确认。")
}

func (h *ComputeTaskHandler) createQuote(ctx context.Context, uid int64, spec store.ComputeTaskSpec) (*mcp.CallToolResult, any, error) {
	if h == nil || h.store == nil {
		return serviceToolError("compute_task_unavailable"), nil, nil
	}
	if !h.acceptsAllocation(spec) {
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
}

func (h *ComputeTaskHandler) taskStatus(ctx context.Context, uid int64, id string) (*mcp.CallToolResult, any, error) {
	if h == nil || h.store == nil {
		return serviceToolError("compute_task_unavailable"), nil, nil
	}
	if !computeTaskIDPattern.MatchString(id) {
		return serviceToolError("compute_task_not_found"), nil, nil
	}
	run, err := h.store.GetComputeTask(ctx, uid, id)
	if err != nil || run == nil {
		return serviceToolError("compute_task_not_found"), nil, nil
	}
	return computeTaskResult(run, "Cats 算力任务状态："+run.State)
}

func (h *ComputeTaskHandler) addUnifiedTool(srv *mcp.Server, uid int64, snapshotPath string) {
	tool := &mcp.Tool{
		Name:        computeToolName,
		Description: "Cats 统一算力入口。action=catalog 查询可报价的单节点规格、费率和任务限制，action=preview 比较当前 Agent 设备和 Cats 托管 Slurm，action=quote 根据每节点 CPU、内存、节点数和作业时限生成需本人确认的整节点报价，action=status 查询任务状态和实际扣费。计费从平台观察到节点开始，按实际存活时长结算，每节点上限由平台按作业时限自动推导，调用方不需要填写。报价不会自动开通资源。",
		InputSchema: computeTaskInputSchema(h.maxWallSeconds),
	}
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Action       string          `json:"action"`
			Task         json.RawMessage `json:"task"`
			AgentRuntime json.RawMessage `json:"agent_runtime"`
			QuoteID      string          `json:"quote_id"`
		}
		if len(req.Params.Arguments) == 0 || json.Unmarshal(req.Params.Arguments, &args) != nil {
			return serviceToolError("invalid_compute_arguments"), nil
		}
		var result *mcp.CallToolResult
		var structured any
		var err error
		switch strings.TrimSpace(args.Action) {
		case "catalog":
			result, structured, err = h.taskCatalog(time.Now().UTC())
		case "preview":
			if snapshotPath == "" {
				return serviceToolError("compute_preview_unavailable"), nil
			}
			var previewArgs computePreviewArgs
			if decodeErr := json.Unmarshal(req.Params.Arguments, &previewArgs); decodeErr != nil {
				return serviceToolError("invalid_compute_requirements"), nil
			}
			result, structured, err = runComputePreview(snapshotPath, previewArgs)
		case "quote":
			var request struct {
				Task store.ComputeTaskSpec `json:"task"`
			}
			if decodeErr := json.Unmarshal(req.Params.Arguments, &request); decodeErr != nil {
				return serviceToolError("invalid_compute_task"), nil
			}
			result, structured, err = h.createQuote(ctx, uid, request.Task)
		case "status":
			result, structured, err = h.taskStatus(ctx, uid, strings.TrimSpace(args.QuoteID))
		default:
			return serviceToolError("compute_action_required"), nil
		}
		if err == nil && result != nil && structured != nil {
			result.StructuredContent = structured
		}
		return result, err
	})
}

func (h *ComputeTaskHandler) addTools(srv *mcp.Server, uid int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        computeTaskQuoteTool,
		Description: "为 Cats 托管 Slurm CPU 作业报价。按实际任务填写整分钟作业时限、节点数、每节点 CPU 和内存；平台据此推导每节点计费上限并在确认时预留额度。当前可报价规格和限制可通过 catsco_compute 的 catalog 查询。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, spec store.ComputeTaskSpec) (*mcp.CallToolResult, any, error) {
		return h.createQuote(ctx, uid, spec)
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
	var run *store.ComputeTaskRun
	if admissionStore, ok := h.store.(store.ComputeTaskAdmissionStore); ok {
		run, err = admissionStore.ClaimComputeTaskWithPolicy(ctx, lease, now, now.Add(store.ComputeTaskLeaseTTL), h.capacityPolicy)
	} else {
		run, err = h.store.ClaimComputeTask(ctx, lease, now, now.Add(store.ComputeTaskLeaseTTL))
	}
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
