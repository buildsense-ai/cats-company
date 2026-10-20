package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 外部执行后端（设计 §4.4 / P3-a）：`runner.type=http` 的 agent 由用户自己的服务执行。
//
// 平台职责：run 生命周期（已有）、请求转发与签名、超时、结果回填；执行完全在用户侧。
// 用户在 agent definition 里声明：
//
//	{"runner": {"type": "http", "url": "https://user.example/catsco/run", "timeout_seconds": 60}}
//
// 转发契约 `catsco.service-run-request.v1`：
//
//	POST <url>
//	X-Catsco-Signature: sha256=<hex(hmac_sha256(SECRET, body))>
//	X-Catsco-Run-Id: <run_id>
//	{"contract_version":"catsco.service-run-request.v1","run_id":"…","session_id":"…",
//	 "agent":"slug","caller_uid":441,"bot_uid":0,"input":{…},"deadline_ms":60000}
//
// 对端应答（同一次 HTTP 响应内）：
//
//	{"status":"completed","output":{"text":"…"},"files":[],"usage":{}}
//	{"status":"awaiting_input","output":{"question":"…"}}
//	{"status":"failed","error":"…","retryable":true}
//	{"status":"running"}   // 自行异步推进，之后用 catsco_run_status 轮询
const (
	serviceRunRequestContractVersion = "catsco.service-run-request.v1"

	externalRunnerSecretEnv     = "CATS_SERVICE_EXTERNAL_RUNNER_SECRET"
	externalRunnerAllowHTTPEnv  = "CATS_SERVICE_EXTERNAL_ALLOW_HTTP"
	externalRunnerAllowLocalEnv = "CATS_SERVICE_EXTERNAL_ALLOW_PRIVATE"

	externalRunnerDefaultTimeout = 60 * time.Second
	externalRunnerMaxTimeout     = 10 * time.Minute
	externalRunnerMaxResponse    = 8 << 20 // 8 MiB
)

// ServiceAgentRunnerSpec 是 definition.runner（缺省=平台自带的 codex 执行）。
type ServiceAgentRunnerSpec struct {
	Type           string `json:"type"`
	URL            string `json:"url,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// ParseServiceAgentRunnerSpec 从 definition JSON 解析 runner（无 runner 字段 → nil）。
func ParseServiceAgentRunnerSpec(definition json.RawMessage) *ServiceAgentRunnerSpec {
	if len(definition) == 0 {
		return nil
	}
	var parsed struct {
		Runner *ServiceAgentRunnerSpec `json:"runner"`
	}
	if err := json.Unmarshal(definition, &parsed); err != nil {
		return nil
	}
	if parsed.Runner == nil {
		return nil
	}
	spec := *parsed.Runner
	spec.Type = strings.ToLower(strings.TrimSpace(spec.Type))
	spec.URL = strings.TrimSpace(spec.URL)
	if spec.Type == "" {
		return nil
	}
	return &spec
}

// IsExternalRunner 判断该 runner 是否由平台侧转发执行（当前只实现 http）。
func (s *ServiceAgentRunnerSpec) IsExternal() bool {
	return s != nil && s.Type == "http"
}

// ServiceExternalRunner 负责把 run 转发到用户 endpoint 并把结果写回。
type ServiceExternalRunner struct {
	store        store.ServiceAgentStore
	client       *http.Client
	secret       []byte
	allowHTTP    bool
	allowPrivate bool
}

type ServiceExternalRunRequest struct {
	RunID        string
	SessionID    string
	AgentSlug    string
	CallerUID    int64
	CallerBotUID int64
	Input        json.RawMessage
	Spec         *ServiceAgentRunnerSpec
}

// NewServiceExternalRunnerFromEnv 构造外部转发器。
// 未配置 `CATS_SERVICE_EXTERNAL_RUNNER_SECRET` 时仍可构造，但带 runner 的调用会明确失败
// （不允许无签名裸奔；配置后无需改代码即生效）。
func NewServiceExternalRunnerFromEnv(st store.ServiceAgentStore) *ServiceExternalRunner {
	r := &ServiceExternalRunner{
		store:        st,
		secret:       []byte(strings.TrimSpace(os.Getenv(externalRunnerSecretEnv))),
		allowHTTP:    os.Getenv(externalRunnerAllowHTTPEnv) == "1",
		allowPrivate: os.Getenv(externalRunnerAllowLocalEnv) == "1",
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	r.client = &http.Client{Transport: transport}
	return r
}

// SetSecret 覆盖签名密钥（测试注入）。
func (r *ServiceExternalRunner) SetSecret(secret string) { r.secret = []byte(secret) }

// AllowInsecureTargets 允许 http / 内网地址（仅供测试与本地联调）。
func (r *ServiceExternalRunner) AllowInsecureTargets(allow bool) {
	r.allowHTTP = allow
	r.allowPrivate = allow
}

// SpecFor 取 agent 当前版本的 runner 声明（读不到/无 runner → nil）。
func (r *ServiceExternalRunner) SpecFor(ctx context.Context, agent store.ServiceAgentDescriptor) *ServiceAgentRunnerSpec {
	_, _, definitionJSON, err := r.store.GetServiceAgentDefinition(ctx, agent.ID, agent.Version)
	if err != nil {
		log.Printf("external runner: load definition agent=%s: %v", agent.Slug, err)
		return nil
	}
	return ParseServiceAgentRunnerSpec(definitionJSON)
}

// MaybeDispatch 若该 agent 声明了 http runner，则异步转发该 run（不阻塞调用方）。
// 调用方拿到的是「running 句柄」，结果写成 result 事件后 awaitRun/run_status 立刻可见。
func (r *ServiceExternalRunner) MaybeDispatch(ctx context.Context, agent store.ServiceAgentDescriptor, run store.ServiceRun, callerUID, callerBotUID int64) {
	if r == nil {
		return
	}
	spec := r.SpecFor(ctx, agent)
	if !spec.IsExternal() {
		return
	}
	req := ServiceExternalRunRequest{
		RunID:        run.RunID,
		SessionID:    run.SessionID,
		AgentSlug:    agent.Slug,
		CallerUID:    callerUID,
		CallerBotUID: callerBotUID,
		Input:        run.Input,
		Spec:         spec,
	}
	go r.execute(req)
}

func (r *ServiceExternalRunner) execute(req ServiceExternalRunRequest) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("external runner panic run=%s: %v", req.RunID, rec)
		}
	}()
	timeout := externalRunnerTimeout(req.Spec)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 先把 run 推进到 running（幂等）：submitted → running 带租约；
	// 抢不到（已被其他执行方接管/已终态）就放弃，避免重复执行。
	started, err := r.store.StartServiceRun(ctx, req.RunID, "external:"+req.AgentSlug, int(timeout.Seconds())+60)
	if err != nil {
		log.Printf("external runner: start run=%s: %v", req.RunID, err)
		return
	}
	if !started {
		log.Printf("external runner: run=%s already taken or terminal; skip", req.RunID)
		return
	}

	target, err := r.validateTarget(req.Spec.URL)
	if err != nil {
		r.fail(ctx, req, "invalid_runner_endpoint: "+err.Error())
		return
	}

	body, err := json.Marshal(map[string]any{
		"contract_version": serviceRunRequestContractVersion,
		"run_id":           req.RunID,
		"session_id":       req.SessionID,
		"agent":            req.AgentSlug,
		"caller_uid":       req.CallerUID,
		"bot_uid":          req.CallerBotUID,
		"input":            req.Input,
		"deadline_ms":      timeout.Milliseconds(),
	})
	if err != nil {
		r.fail(ctx, req, "encode_run_request_failed")
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		r.fail(ctx, req, "build_request_failed")
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "catsco-service-runner/1")
	httpReq.Header.Set("X-Catsco-Run-Id", req.RunID)
	if len(r.secret) > 0 {
		mac := hmac.New(sha256.New, r.secret)
		mac.Write(body)
		httpReq.Header.Set("X-Catsco-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		// 外部执行没有 worker 会「接管重跑」：可重试标记只会把 run 留成 orphaned 卡住，
		// 因此统一按终态失败返回（对端可在自己内部重试，或让调用方重新发起 run）。
		r.fail(ctx, req, "runner_unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.fail(ctx, req, fmt.Sprintf("runner_status_%d", resp.StatusCode))
		return
	}

	var payload struct {
		Status    string          `json:"status"`
		Output    json.RawMessage `json:"output,omitempty"`
		Files     []string        `json:"files,omitempty"`
		Usage     json.RawMessage `json:"usage,omitempty"`
		Error     string          `json:"error,omitempty"`
		Retryable bool            `json:"retryable,omitempty"` // 保留字段：当前实现恒按终态失败处理
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, externalRunnerMaxResponse))
	if err := dec.Decode(&payload); err != nil {
		r.fail(ctx, req, "invalid_runner_response: "+err.Error())
		return
	}

	envelope := ServiceRunEnvelope{
		ContractVersion: serviceRunResultContractVersion,
		RunID:           req.RunID,
		SessionID:       req.SessionID,
		Files:           payload.Files,
		Output:          payload.Output,
	}
	if len(envelope.Output) == 0 || string(envelope.Output) == "null" {
		envelope.Output = json.RawMessage(`{}`)
	}
	if len(payload.Usage) > 0 && string(payload.Usage) != "null" {
		var usage ServiceRunUsage
		if json.Unmarshal(payload.Usage, &usage) == nil {
			envelope.Usage = &usage
		}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		r.fail(ctx, req, "encode_result_failed")
		return
	}

	switch strings.ToLower(strings.TrimSpace(payload.Status)) {
	case string(ServiceRunCompleted):
		envelope.Status = ServiceRunCompleted
		encoded, _ = json.Marshal(envelope)
		r.finish(ctx, req, encoded, payload.Usage)
	case string(ServiceRunAwaitingInput):
		envelope.Status = ServiceRunAwaitingInput
		encoded, _ = json.Marshal(envelope)
		if _, err := r.store.AwaitServiceRunInput(ctx, req.RunID, externalResultID(req.RunID), encoded); err != nil {
			log.Printf("external runner: await input run=%s: %v", req.RunID, err)
			return
		}
		r.appendResultEvent(ctx, req, encoded)
	case string(ServiceRunFailed):
		r.fail(ctx, req, firstNonEmptyString(payload.Error, "runner_reported_failure"))
	case string(ServiceRunRunning):
		// 对端自行推进：保持 running，调用方用 catsco_run_status 轮询。
		log.Printf("external runner: run=%s still running at endpoint", req.RunID)
	default:
		r.fail(ctx, req, "unknown_runner_status: "+payload.Status)
	}
}

func (r *ServiceExternalRunner) finish(ctx context.Context, req ServiceExternalRunRequest, envelope, usage json.RawMessage) {
	r.appendResultEvent(ctx, req, envelope)
	completed, err := r.store.CompleteServiceRun(ctx, req.RunID, externalResultID(req.RunID), envelope, usage)
	if err != nil {
		log.Printf("external runner: complete run=%s: %v", req.RunID, err)
		return
	}
	if !completed {
		log.Printf("external runner: run=%s already terminal", req.RunID)
	}
}

func (r *ServiceExternalRunner) fail(ctx context.Context, req ServiceExternalRunRequest, message string) {
	if err := r.store.FailServiceRun(ctx, req.RunID, message, false); err != nil {
		log.Printf("external runner: fail run=%s: %v", req.RunID, err)
	}
}

func (r *ServiceExternalRunner) appendResultEvent(ctx context.Context, req ServiceExternalRunRequest, envelope json.RawMessage) {
	if _, err := r.store.AppendServiceRunEvent(ctx, req.RunID, "result", envelope); err != nil {
		log.Printf("external runner: append result run=%s: %v", req.RunID, err)
	}
}

// validateTarget 校验 endpoint：必须 https（除非显式放开），且不得指向内网/环回。
// 已知残余风险：DNS rebinding（解析后再变），MVP 接受，后续可加 IP 白名单/固定解析。
func (r *ServiceExternalRunner) validateTarget(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, errors.New("runner.url 为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("runner.url 非法: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !r.allowHTTP {
			return nil, errors.New("runner.url 必须使用 https（本地联调可设 CATS_SERVICE_EXTERNAL_ALLOW_HTTP=1）")
		}
	default:
		return nil, fmt.Errorf("不支持的协议 %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("runner.url 缺少主机名")
	}
	if r.allowPrivate {
		return u, nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return nil, errors.New("runner.url 不能指向 localhost")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("runner.url 域名解析失败: %w", err)
	}
	for _, ip := range ips {
		if isDisallowedTargetIP(ip) {
			return nil, fmt.Errorf("runner.url 指向受限地址 %s", ip.String())
		}
	}
	return u, nil
}

func isDisallowedTargetIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func externalRunnerTimeout(spec *ServiceAgentRunnerSpec) time.Duration {
	if spec == nil || spec.TimeoutSeconds <= 0 {
		return externalRunnerDefaultTimeout
	}
	timeout := time.Duration(spec.TimeoutSeconds) * time.Second
	if timeout > externalRunnerMaxTimeout {
		timeout = externalRunnerMaxTimeout
	}
	return timeout
}

// externalResultID 是 run 级幂等键（同一 run 重放 complete 不产生第二条结果）。
func externalResultID(runID string) string {
	return "res_" + strings.TrimPrefix(runID, "run_")
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// externalRunnerEnabled 供运维诊断：签名密钥是否已配置。
func externalRunnerEnabled() bool {
	return strings.TrimSpace(os.Getenv(externalRunnerSecretEnv)) != ""
}
