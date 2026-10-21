package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
)

// ServiceRunOrchestrator 是编排内部 API（实例↔平台；设计 §4.3/§6.3，计划① Task 8）。
// 鉴权=实例 Bearer token（env CATS_SERVICE_INSTANCE_TOKEN）；服务 token 现场签发
// （env CATS_SERVICE_TOKEN_SECRET，与 relay 同源），exp=+4h。网络层内网隔离之外
// 仍做应用层校验（纵深防御）。
type ServiceRunOrchestrator struct {
	store         store.ServiceAgentStore
	instanceToken string
	tokenSecret   string
	stopOnce      sync.Once
	stopCh        chan struct{}
	// receiptKicker 回注投递器（可选）：终态转换后即时唤醒投递（未注入=由投递器周期扫描兜底）。
	receiptKicker serviceReceiptKicker
}

// serviceReceiptKicker 是回注投递器的唤醒接口（由 service_run_receipt_delivery.go 实现）。
type serviceReceiptKicker interface {
	Kick()
}

const (
	serviceInstanceTokenEnv    = "CATS_SERVICE_INSTANCE_TOKEN"
	serviceTokenSecretEnv      = "CATS_SERVICE_TOKEN_SECRET"
	serviceTokenTTL            = 4 * time.Hour
	serviceClaimDefaultLimit   = 10
	serviceClaimMaxLimit       = 50
	serviceReapDefaultInterval = 15 * time.Second
	serviceReapTimeout         = 10 * time.Second
	// 心跳请求体上限（metrics 快照为小 JSON；防异常体打爆解析）。
	serviceInstanceHeartbeatLimit = 64 << 10
)

// ServiceWorkerAPIPrefix 是用户自建 worker 的公网入口前缀。
//
// /internal/ 只在 Compose 网络内可达（生产 nginx 对该前缀直接 return 404），所以
// 跑在用户自己电脑上的 service-run-worker 需要一个公网前缀；nginx 已经反向代理
// /api/，因此这里新增路由无需改 nginx。鉴权与 /internal 完全一致（实例 token）。
//
// 改动这里要同步 cmd/service-run-worker 的 defaultAPIPrefix。
const ServiceWorkerAPIPrefix = "/api/service-worker"

// ServiceClaimResult / ServiceClaimRun 是 claim 的下发契约（实例据此执行 run）。
type ServiceClaimResult struct {
	Runs []ServiceClaimRun `json:"runs"`
}

type ServiceClaimRun struct {
	RunID            string          `json:"run_id"`
	SessionID        string          `json:"session_id"`
	AgentSlug        string          `json:"agent_slug"`
	PackageHash      string          `json:"package_hash,omitempty"`
	DefinitionRev    int64           `json:"definition_rev"`
	ThresholdSeconds int             `json:"threshold_seconds"`
	Input            json.RawMessage `json:"input"`
	ServiceToken     string          `json:"service_token"`
	// Definition 是会话钉定的应用定义（worker 用来取 runner.command 等执行参数）。
	Definition json.RawMessage         `json:"definition,omitempty"`
	Checkpoint *ServiceClaimCheckpoint `json:"checkpoint,omitempty"`
}

type ServiceClaimCheckpoint struct {
	Revision int64           `json:"revision"`
	Snapshot json.RawMessage `json:"snapshot"`
}

func NewServiceRunOrchestrator(st store.ServiceAgentStore, instanceToken, tokenSecret string) *ServiceRunOrchestrator {
	return &ServiceRunOrchestrator{
		store:         st,
		instanceToken: instanceToken,
		tokenSecret:   tokenSecret,
		stopCh:        make(chan struct{}),
	}
}

// SetReceiptKicker 注入回注投递器（设计 §4.8-2；未注入时终态回执等投递器下轮扫描）。
func (o *ServiceRunOrchestrator) SetReceiptKicker(k serviceReceiptKicker) {
	o.receiptKicker = k
}

func (o *ServiceRunOrchestrator) kickReceiptDelivery() {
	if o.receiptKicker != nil {
		o.receiptKicker.Kick()
	}
}

func NewServiceRunOrchestratorFromEnv(st store.ServiceAgentStore) *ServiceRunOrchestrator {
	return NewServiceRunOrchestrator(
		st,
		strings.TrimSpace(os.Getenv(serviceInstanceTokenEnv)),
		strings.TrimSpace(os.Getenv(serviceTokenSecretEnv)),
	)
}

func (o *ServiceRunOrchestrator) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/service-runs/claim", o.withInstanceToken(o.handleClaim))
	mux.HandleFunc("POST /internal/service-runs/{runID}/checkpoint", o.withInstanceToken(o.handleCheckpoint))
	mux.HandleFunc("POST /internal/service-runs/{runID}/heartbeat", o.withInstanceToken(o.handleRenewLease))
	mux.HandleFunc("POST /internal/service-runs/{runID}/events", o.withInstanceToken(o.handleAppendEvent))
	mux.HandleFunc("POST /internal/service-runs/{runID}/complete", o.withInstanceToken(o.handleComplete))
	mux.HandleFunc("POST /internal/service-runs/{runID}/await-input", o.withInstanceToken(o.handleAwaitInput))
	mux.HandleFunc("POST /internal/service-runs/{runID}/fail", o.withInstanceToken(o.handleFailRun))
	mux.HandleFunc("POST /internal/service-instances/{instanceID}/heartbeat", o.withInstanceToken(o.handleInstanceHeartbeat))
	// 云端包分发（设计 A §2）：发布（写新版本行）与按内容 hash 拉取。
	mux.HandleFunc("POST /internal/service-agents/{slug}/publish", o.withInstanceToken(o.handlePublishServiceAgentPackage))
	mux.HandleFunc("GET /internal/service-agent-packages/{packageHash}", o.withInstanceToken(o.handleGetServiceAgentPackage))

	// 公网 worker 面（ServiceWorkerAPIPrefix）：用户自己电脑上跑的 service-run-worker
	// 必须走公网，而 nginx 对 /internal/ 一律 404（内部调用只留 Compose 网络）。
	// 这里只导出 worker 实际需要的那 5 条，鉴权与 /internal 完全相同（实例 token），
	// 包分发、checkpoint、events 等仍只在内部网络可用。
	mux.HandleFunc("POST "+ServiceWorkerAPIPrefix+"/claim", o.withInstanceToken(o.handleClaim))
	mux.HandleFunc("POST "+ServiceWorkerAPIPrefix+"/runs/{runID}/heartbeat", o.withInstanceToken(o.handleRenewLease))
	mux.HandleFunc("POST "+ServiceWorkerAPIPrefix+"/runs/{runID}/complete", o.withInstanceToken(o.handleComplete))
	mux.HandleFunc("POST "+ServiceWorkerAPIPrefix+"/runs/{runID}/fail", o.withInstanceToken(o.handleFailRun))
	mux.HandleFunc("POST "+ServiceWorkerAPIPrefix+"/instances/{instanceID}/heartbeat", o.withInstanceToken(o.handleInstanceHeartbeat))
}

// StartReaper 周期回收过期租约（orphaned 供其他实例接管，设计 §4.8-1）。
func (o *ServiceRunOrchestrator) StartReaper(interval time.Duration) {
	if interval <= 0 {
		interval = serviceReapDefaultInterval
	}
	reap := func() {
		ctx, cancel := context.WithTimeout(context.Background(), serviceReapTimeout)
		defer cancel()
		if _, err := o.ReapOnce(ctx); err != nil {
			log.Printf("service run reaper: %v", err)
		}
	}
	reap()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-o.stopCh:
			return
		case <-ticker.C:
			reap()
		}
	}
}

func (o *ServiceRunOrchestrator) Stop() {
	o.stopOnce.Do(func() { close(o.stopCh) })
}

func (o *ServiceRunOrchestrator) ReapOnce(ctx context.Context) (int, error) {
	reaped, err := o.store.ReapExpiredServiceRuns(ctx)
	if err != nil {
		return 0, err
	}
	if reaped > 0 {
		log.Printf("service run reaper: %d run(s) orphaned", reaped)
	}
	return reaped, nil
}

func (o *ServiceRunOrchestrator) withInstanceToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if o.instanceToken == "" || token == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(o.instanceToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (o *ServiceRunOrchestrator) handleClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Limit      int      `json:"limit"`
		InstanceID string   `json:"instance_id"`
		AgentSlugs []string `json:"agent_slugs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	req.InstanceID = strings.TrimSpace(req.InstanceID)
	if req.InstanceID == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "instance_id_required"})
		return
	}
	if req.Limit <= 0 {
		req.Limit = serviceClaimDefaultLimit
	}
	if req.Limit > serviceClaimMaxLimit {
		req.Limit = serviceClaimMaxLimit
	}
	runs, err := o.claimRuns(r.Context(), req.InstanceID, req.Limit, req.AgentSlugs)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "claim_failed"})
		return
	}
	result := ServiceClaimResult{Runs: make([]ServiceClaimRun, 0, len(runs))}
	for _, run := range runs {
		entry, err := o.buildClaimEntry(r.Context(), run)
		if err != nil {
			// 单条组装失败不阻断整批（租约到期后会被 reaper 回收重派）。
			log.Printf("service claim build entry run=%s: %v", run.RunID, err)
			continue
		}
		result.Runs = append(result.Runs, entry)
	}
	writeServiceJSON(w, http.StatusOK, result)
}

// serviceRunScopedClaimer 是可选的「按 agent slug 过滤」认领能力（PG/MySQL 适配器实现）。
// 未实现的存储（测试替身）退化为不带过滤的认领。
type serviceRunScopedClaimer interface {
	ClaimServiceRunsScoped(ctx context.Context, instanceID string, limit int, agentSlugs []string) ([]store.ServiceRun, error)
}

func (o *ServiceRunOrchestrator) claimRuns(ctx context.Context, instanceID string, limit int, agentSlugs []string) ([]store.ServiceRun, error) {
	slugs := make([]string, 0, len(agentSlugs))
	for _, slug := range agentSlugs {
		if trimmed := strings.TrimSpace(slug); trimmed != "" {
			slugs = append(slugs, trimmed)
		}
	}
	if len(slugs) == 0 {
		return o.store.ClaimServiceRuns(ctx, instanceID, limit)
	}
	if scoped, ok := o.store.(serviceRunScopedClaimer); ok {
		return scoped.ClaimServiceRunsScoped(ctx, instanceID, limit, slugs)
	}
	return o.store.ClaimServiceRuns(ctx, instanceID, limit)
}

func (o *ServiceRunOrchestrator) buildClaimEntry(ctx context.Context, run store.ServiceRun) (ServiceClaimRun, error) {
	session, err := o.store.GetServiceSession(ctx, run.SessionID)
	if err != nil {
		return ServiceClaimRun{}, fmt.Errorf("session: %w", err)
	}
	// 会话钉快照（设计 A §2）：定义按会话创建时固化的 definition_rev 取（防"半新半旧"）；
	// definition_rev<=0 或该版本缺失 → 存储层兑底最新版本。
	slug, threshold, definition, err := o.store.GetServiceAgentDefinition(ctx, session.AgentID, session.DefinitionRev)
	if err != nil {
		return ServiceClaimRun{}, fmt.Errorf("definition: %w", err)
	}
	var parsed struct {
		Models []string `json:"models"`
	}
	_ = json.Unmarshal(definition, &parsed)
	// 云端包分发：优先会话钉定的包 hash；空值（钉定上线前建的旧会话/未发布过包）
	// 回退最新版本 hash（无包/旧库 → 空，实例回退内置包）。
	packageHash := session.AssetsHash
	if packageHash == "" {
		packageHash, err = o.store.GetServiceAgentPackageHash(ctx, session.AgentID)
		if err != nil {
			log.Printf("service claim package hash agent=%d: %v", session.AgentID, err)
			packageHash = ""
		}
	}
	token := IssueServiceToken(o.tokenSecret, ServiceTokenClaims{
		CallerUID: session.CallerUID,
		Agent:     slug,
		Models:    parsed.Models,
		RunID:     run.RunID,
		Purpose:   "service",
		ExpiresAt: time.Now().Add(serviceTokenTTL),
	})
	entry := ServiceClaimRun{
		RunID:            run.RunID,
		SessionID:        run.SessionID,
		AgentSlug:        slug,
		PackageHash:      packageHash,
		DefinitionRev:    session.DefinitionRev,
		ThresholdSeconds: threshold,
		Input:            run.Input,
		ServiceToken:     token,
		Definition:       definition,
	}
	if revision, snapshot, err := o.store.LatestServiceCheckpoint(ctx, run.SessionID); err == nil && revision > 0 && len(snapshot) > 0 {
		entry.Checkpoint = &ServiceClaimCheckpoint{Revision: revision, Snapshot: snapshot}
	}
	return entry, nil
}

func (o *ServiceRunOrchestrator) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var req struct {
		Revision int64           `json:"revision"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Revision <= 0 || len(req.Snapshot) == 0 {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	run, err := o.store.GetServiceRun(r.Context(), runID)
	if err != nil {
		writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "run_not_found"})
		return
	}
	if err := o.store.SaveServiceCheckpoint(r.Context(), run.SessionID, req.Revision, req.Snapshot); err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "save_failed"})
		return
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"saved": true})
}

func (o *ServiceRunOrchestrator) handleRenewLease(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var req struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.InstanceID) == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	renewed, err := o.store.RenewServiceLease(r.Context(), runID, req.InstanceID)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "renew_failed"})
		return
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"renewed": renewed})
}

func (o *ServiceRunOrchestrator) handleAppendEvent(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var req struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Type) == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	seq, err := o.store.AppendServiceRunEvent(r.Context(), runID, req.Type, req.Payload)
	if err != nil {
		if errors.Is(err, store.ErrServiceRunNotFound) {
			writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "run_not_found"})
			return
		}
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "append_failed"})
		return
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"seq": seq})
}

func (o *ServiceRunOrchestrator) handleComplete(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var req struct {
		ResultID string          `json:"result_id"`
		Result   json.RawMessage `json:"result"`
		Usage    json.RawMessage `json:"usage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ResultID) == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	completed, err := o.store.CompleteServiceRun(r.Context(), runID, req.ResultID, req.Result, req.Usage)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrServiceRunNotFound):
			writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "run_not_found"})
		case errors.Is(err, store.ErrServiceRunConflict):
			writeServiceJSON(w, http.StatusConflict, map[string]any{"error": "state_conflict"})
		default:
			writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "complete_failed"})
		}
		return
	}
	if completed {
		// 生命周期回注：终态已落（含回执）→ 唤醒投递（在线即推）。
		o.kickReceiptDelivery()
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"completed": completed, "idempotent": !completed})
}

// handleAwaitInput 处理实例上报的「待补充输入」收尾（回合制返问；设计 §3.3-8）。
// 语义与 complete 一致（幂等重放/错态 409）；调用方补充 = 同一会话的下一次 tools/call。
func (o *ServiceRunOrchestrator) handleAwaitInput(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var req struct {
		ResultID string          `json:"result_id"`
		Result   json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ResultID) == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	awaited, err := o.store.AwaitServiceRunInput(r.Context(), runID, req.ResultID, req.Result)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrServiceRunNotFound):
			writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "run_not_found"})
		case errors.Is(err, store.ErrServiceRunConflict):
			writeServiceJSON(w, http.StatusConflict, map[string]any{"error": "state_conflict"})
		default:
			writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "await_input_failed"})
		}
		return
	}
	if awaited {
		o.kickReceiptDelivery()
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"awaiting_input": awaited, "idempotent": !awaited})
}

// handleFailRun 处理实例上报的不可恢复/可重试失败（设计 §4.8；计划② PlatformClient.failRun）。
// retryable=true → orphaned（可被接管重试）；false → failed（终态）。仅对 running 生效（store 层保障）。
func (o *ServiceRunOrchestrator) handleFailRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var req struct {
		ErrorText string `json:"error_text"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	if err := o.store.FailServiceRun(r.Context(), runID, req.ErrorText, req.Retryable); err != nil {
		if errors.Is(err, store.ErrServiceRunNotFound) {
			writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "run_not_found"})
			return
		}
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "fail_failed"})
		return
	}
	if !req.Retryable {
		// 不可重试失败=终态（回执已入箱）→ 唤醒投递。
		o.kickReceiptDelivery()
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (o *ServiceRunOrchestrator) handleInstanceHeartbeat(w http.ResponseWriter, r *http.Request) {
	instanceID := strings.TrimSpace(r.PathValue("instanceID"))
	if instanceID == "" || len(instanceID) > 64 {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "instance_id_required"})
		return
	}
	var req struct {
		ActiveRuns int            `json:"active_runs"`
		Metrics    map[string]any `json:"metrics"`
	}
	body := http.MaxBytesReader(w, r.Body, serviceInstanceHeartbeatLimit)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	// 心跳落库（监控数据源，设计 §7.1）；落库失败不影响回执语义（实例无需感知）。
	metrics, err := json.Marshal(req.Metrics)
	if err != nil || len(metrics) == 0 {
		metrics = json.RawMessage(`{}`)
	}
	if err := o.store.RecordServiceInstanceHeartbeat(r.Context(), instanceID, req.ActiveRuns, metrics); err != nil {
		log.Printf("service instance heartbeat persist instance=%s: %v", instanceID, err)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeServiceJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
