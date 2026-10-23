// Command service-run-worker 认领并执行平台上的 service run（设计 §4.8 的实例侧实现）。
//
// 适用场景：把自己的 agent（本地/云端的 CLI 或常驻进程的包装脚本）接到平台上，
// 让别人（或自己）在 MCP 里以 `catsco_<slug>` 的形式调用，实际执行发生在**这台机器**上。
//
// 协议（平台侧已实现，见 server/service_agents_orchestrator.go）：
//
//	POST /internal/service-runs/claim                     认领（租约）
//	POST /internal/service-runs/{run}/heartbeat           续租
//	POST /internal/service-runs/{run}/events              追加事件
//	POST /internal/service-runs/{run}/complete            终态成功
//	POST /internal/service-runs/{run}/await-input         待补充输入
//	POST /internal/service-runs/{run}/fail                失败（retryable=true → 别的实例可重试）
//	POST /internal/service-instances/{id}/heartbeat       实例心跳
//
// 全部使用 `Authorization: Bearer $CATS_SERVICE_INSTANCE_TOKEN`。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	resultContractVersion = "catsco.service-run-result.v1"
	defaultBaseURL        = "https://app.catsco.cn"
	// defaultAPIPrefix 是平台面向外部 worker 的公网前缀。
	//
	// 不能用 /internal：那是 Compose 网络内的服务间通道（路径结构也不同），
	// 生产 nginx 对 /internal 直接 return 404。
	// 改这里要同步 server.ServiceWorkerAPIPrefix。
	defaultAPIPrefix = "/api/service-worker"
)

type config struct {
	baseURL    string
	apiPrefix  string
	token      string
	userToken  string
	instanceID string
	command    string
	agents     []string
	runTimeout time.Duration
	pollEvery  time.Duration
	heartbeat  time.Duration
	once       bool
}

type claimRun struct {
	RunID            string          `json:"run_id"`
	SessionID        string          `json:"session_id"`
	AgentSlug        string          `json:"agent_slug"`
	PackageHash      string          `json:"package_hash"`
	DefinitionRev    int64           `json:"definition_rev"`
	ThresholdSeconds int             `json:"threshold_seconds"`
	Input            json.RawMessage `json:"input"`
	ServiceToken     string          `json:"service_token"`
	// Definition 是应用定义；里面 runner.command 可以按应用指定不同的本机命令。
	Definition json.RawMessage `json:"definition"`
}

type claimResponse struct {
	Runs []claimRun `json:"runs"`
}

type worker struct {
	cfg    config
	client *httpClient
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.SetFlags(log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	w := &worker{cfg: cfg, client: newHTTPClient(cfg.baseURL, cfg.token)}
	w.client.userToken = cfg.userToken
	w.client.apiPrefix = cfg.apiPrefix
	if err := w.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("worker: %v", err)
	}
	log.Println("worker stopped")
}

func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("service-run-worker", flag.ContinueOnError)
	base := fs.String("base", envOr("CATSCO_BASE_URL", defaultBaseURL), "平台地址（https://app.catsco.cn）")
	apiPrefix := fs.String("api-prefix", envOr("CATSCO_WORKER_API_PREFIX", defaultAPIPrefix), "worker API 前缀（一般不用改；前面有反向代理时才需要）")
	token := fs.String("token", envOr("CATS_WORKER_TOKEN", os.Getenv("CATS_SERVICE_INSTANCE_TOKEN")), "worker 连接密钥（cwk_...）；不填时可给 -user-token 自动换券")
	userToken := fs.String("user-token", os.Getenv("CATS_USER_TOKEN"), "账号登录 token；用它换一张短期 worker 券（桌面端自动传入，用户不用手抄密钥）")
	instance := fs.String("instance", envOr("CATSCO_WORKER_INSTANCE_ID", defaultInstanceID()), "实例 id（≤64 字符）")
	command := fs.String("cmd", os.Getenv("CATSCO_WORKER_CMD"), "执行命令模板；可用占位符 {slug} {run_id} {session_id} {input_file}，输入 JSON 同时写到 stdin")
	agents := fs.String("agents", os.Getenv("CATSCO_WORKER_AGENTS"), "只认领这些 slug（逗号分隔）；空=认领所有应用（多租户下不推荐）")
	runTimeout := fs.Duration("timeout", 10*time.Minute, "单次 run 的执行超时")
	poll := fs.Duration("poll", 2*time.Second, "无任务时的轮询间隔")
	heartbeat := fs.Duration("heartbeat", 20*time.Second, "续租/实例心跳间隔")
	once := fs.Bool("once", false, "只认领一轮就退出（联调用）")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	cfg := config{
		baseURL:    strings.TrimRight(strings.TrimSpace(*base), "/"),
		apiPrefix:  normalizeAPIPrefix(*apiPrefix),
		token:      strings.TrimSpace(*token),
		userToken:  strings.TrimSpace(*userToken),
		instanceID: strings.TrimSpace(*instance),
		command:    strings.TrimSpace(*command),
		agents:     splitList(*agents),
		runTimeout: *runTimeout,
		pollEvery:  *poll,
		heartbeat:  *heartbeat,
		once:       *once,
	}
	if cfg.token == "" && cfg.userToken == "" {
		return cfg, errors.New("缺少身份：-token/CATS_WORKER_TOKEN（连接密钥），或 -user-token/CATS_USER_TOKEN（账号登录态，自动换券）")
	}
	// 公网 worker 面只认 per-user 凭据或短期 worker 券；旧的全实例 token 会在服务端 401。
	if cfg.token != "" && !strings.HasPrefix(cfg.token, "cwk_") {
		log.Printf("提示：凭据不是 cwk_ 前缀，公网 /api/service-worker 只接受网页生成的连接密钥或自动换取的 worker 券")
	}
	// -cmd 不是必填：命令可以按应用下发（definition.runner.command），
	// 一个 worker 就能同时服务多个 agent。只有"既没有 -cmd 也没限定 -agents"
	// 这种最容易全线失败启动才提醒一句（run 级别仍有 no_command 兜底）。
	if cfg.command == "" && len(cfg.agents) == 0 {
		log.Printf("提示：没有 -cmd，也没有 -agents；将认领所有应用的 run，只有声明了 runner.command 的应用才会成功")
	}
	if len(cfg.instanceID) == 0 || len(cfg.instanceID) > 64 {
		return cfg, errors.New("instance id 必须 1~64 字符")
	}
	return cfg, nil
}

func (w *worker) run(ctx context.Context) error {
	log.Printf("worker 启动 instance=%s base=%s prefix=%s", w.cfg.instanceID, w.cfg.baseURL, w.cfg.apiPrefix)
	w.sendInstanceHeartbeat(ctx, 0)

	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go w.instanceHeartbeatLoop(hbCtx)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		runs, err := w.claim(ctx)
		if err != nil {
			log.Printf("认领失败：%v", err)
			if w.cfg.once {
				return err
			}
			if !sleepCtx(ctx, w.cfg.pollEvery) {
				return ctx.Err()
			}
			continue
		}
		for _, run := range runs {
			w.executeRun(ctx, run)
		}
		if w.cfg.once {
			return nil
		}
		if len(runs) == 0 && !sleepCtx(ctx, w.cfg.pollEvery) {
			return ctx.Err()
		}
	}
}

func (w *worker) executeRun(ctx context.Context, run claimRun) {
	log.Printf("认领 run=%s agent=%s session=%s", run.RunID, run.AgentSlug, run.SessionID)

	runCtx, cancel := context.WithTimeout(ctx, w.cfg.runTimeout)
	defer cancel()

	if !w.renewLease(runCtx, run.RunID) {
		log.Printf("run=%s 续租失败（可能已被回收），跳过", run.RunID)
		return
	}
	if strings.TrimSpace(w.commandFor(run)) == "" {
		w.failRun(context.Background(), run.RunID,
			"no_command: 该应用没有声明 runner.command，worker 也没有配置 -cmd", false)
		return
	}
	hbCtx, stopHB := context.WithCancel(runCtx)
	defer stopHB()
	go func() {
		ticker := time.NewTicker(w.cfg.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if !w.renewLease(hbCtx, run.RunID) {
					log.Printf("run=%s 续租失败，取消执行", run.RunID)
					cancel()
					return
				}
			}
		}
	}()

	stdout, stderr, exitCode, runErr := w.execCommand(runCtx, run)
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			w.failRun(context.Background(), run.RunID, fmt.Sprintf("worker_timeout: 超过 %s", w.cfg.runTimeout), true)
			return
		}
		if ctx.Err() != nil {
			// worker 被中断：可重试，交给别的实例或重启后接管。
			w.failRun(context.Background(), run.RunID, "worker_shutdown", true)
			return
		}
		w.failRun(context.Background(), run.RunID, "spawn_failed: "+runErr.Error(), false)
		return
	}
	if exitCode != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if len(detail) > 2000 {
			detail = detail[len(detail)-2000:]
		}
		w.failRun(context.Background(), run.RunID, fmt.Sprintf("exit_%d: %s", exitCode, detail), false)
		return
	}

	envelope, usage := buildResult(run, stdout)
	if err := w.completeRun(context.Background(), run.RunID, envelope, usage); err != nil {
		log.Printf("run=%s 回填失败：%v", run.RunID, err)
		// 回填失败按可重试处理：租约到期后会被重新认领（对端 CLI 应保证幂等）。
		w.failRun(context.Background(), run.RunID, "complete_failed: "+err.Error(), true)
		return
	}
	log.Printf("run=%s 完成（stdout %d 字节）", run.RunID, len(stdout))
}

// buildResult 把 CLI 的 stdout 变成 run 结果：
//   - stdout 是 {"output":…,"usage":…} 形式 → 原样采用（结构化输出）
//   - 否则当纯文本，塞进 output.text
func buildResult(run claimRun, stdout string) (map[string]any, any) {
	trimmed := strings.TrimSpace(stdout)
	var structured struct {
		Output json.RawMessage `json:"output"`
		Files  []string        `json:"files"`
		Usage  json.RawMessage `json:"usage"`
	}
	var output any
	var usage any
	var files []string
	if strings.HasPrefix(trimmed, "{") && json.Unmarshal([]byte(trimmed), &structured) == nil && len(structured.Output) > 0 {
		_ = json.Unmarshal(structured.Output, &output)
		if len(structured.Usage) > 0 {
			_ = json.Unmarshal(structured.Usage, &usage)
		}
		files = structured.Files
	} else {
		output = map[string]any{"text": trimmed}
	}
	envelope := map[string]any{
		"contract_version": resultContractVersion,
		"run_id":           run.RunID,
		"session_id":       run.SessionID,
		"status":           "completed",
		"output":           output,
		"files":            files,
	}
	if usage != nil {
		envelope["usage"] = usage
	}
	return envelope, usage
}

func (w *worker) execCommand(ctx context.Context, run claimRun) (string, string, int, error) {
	input := run.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	dir, err := os.MkdirTemp("", "catsco-run-")
	if err != nil {
		return "", "", -1, err
	}
	defer os.RemoveAll(dir)

	inputPath := filepath.Join(dir, "input.json")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		return "", "", -1, err
	}

	command := expandCommand(w.commandFor(run), run, inputPath)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"CATSCO_RUN_ID="+run.RunID,
		"CATSCO_SESSION_ID="+run.SessionID,
		"CATSCO_AGENT="+run.AgentSlug,
		"CATSCO_SERVICE_TOKEN="+run.ServiceToken,
		"CATSCO_INPUT_FILE="+inputPath,
		fmt.Sprintf("CATSCO_THRESHOLD_SECONDS=%d", run.ThresholdSeconds),
	)
	cmd.Stdin = bytes.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

func expandCommand(template string, run claimRun, inputPath string) string {
	replacer := strings.NewReplacer(
		"{slug}", run.AgentSlug,
		"{run_id}", run.RunID,
		"{session_id}", run.SessionID,
		"{input_file}", inputPath,
	)
	return replacer.Replace(template)
}

// commandFor 选本次 run 的执行命令：
//  1. 应用定义里的 runner.command（发布时按应用填写 → 一个 worker 可服务多个 agent）
//  2. 兜底用 worker 启动参数 -cmd
//
// 两者都没有 → 返回空串，executeRun 以配置错误失败（不静默乱跑）。
func (w *worker) commandFor(run claimRun) string {
	if len(run.Definition) > 0 {
		var parsed struct {
			Runner struct {
				Command string `json:"command"`
			} `json:"runner"`
		}
		if err := json.Unmarshal(run.Definition, &parsed); err == nil {
			if command := strings.TrimSpace(parsed.Runner.Command); command != "" {
				return command
			}
		}
	}
	return w.cfg.command
}

func (w *worker) claim(ctx context.Context) ([]claimRun, error) {
	var resp claimResponse
	payload := map[string]any{"limit": 1, "instance_id": w.cfg.instanceID}
	if len(w.cfg.agents) > 0 {
		payload["agent_slugs"] = w.cfg.agents
	}
	if err := w.client.post(ctx, w.cfg.apiPrefix+"/claim", payload, &resp); err != nil {
		return nil, err
	}
	if len(w.cfg.agents) == 0 {
		return resp.Runs, nil
	}
	// 防御：存储层不支持按 slug 过滤时，把不属于自己的 run 立刻退回（可重试）。
	kept := make([]claimRun, 0, len(resp.Runs))
	for _, run := range resp.Runs {
		if w.serves(run.AgentSlug) {
			kept = append(kept, run)
			continue
		}
		log.Printf("run=%s 不属于本 worker（agent=%s），退回", run.RunID, run.AgentSlug)
		w.failRun(ctx, run.RunID, "not_served_by_this_worker", true)
	}
	return kept, nil
}

func (w *worker) serves(slug string) bool {
	for _, candidate := range w.cfg.agents {
		if candidate == slug {
			return true
		}
	}
	return false
}

func (w *worker) renewLease(ctx context.Context, runID string) bool {
	var resp struct {
		Renewed bool `json:"renewed"`
	}
	payload := map[string]any{"instance_id": w.cfg.instanceID}
	if err := w.client.post(ctx, w.runPath(runID, "heartbeat"), payload, &resp); err != nil {
		log.Printf("run=%s 续租请求失败：%v", runID, err)
		return false
	}
	return resp.Renewed
}

func (w *worker) completeRun(ctx context.Context, runID string, envelope map[string]any, usage any) error {
	payload := map[string]any{
		"result_id": "res_" + runID,
		"result":    envelope,
	}
	if usage != nil {
		payload["usage"] = usage
	}
	var resp map[string]any
	return w.client.post(ctx, w.runPath(runID, "complete"), payload, &resp)
}

func (w *worker) failRun(ctx context.Context, runID, message string, retryable bool) {
	payload := map[string]any{"error_text": message, "retryable": retryable}
	var resp map[string]any
	if err := w.client.post(ctx, w.runPath(runID, "fail"), payload, &resp); err != nil {
		log.Printf("run=%s 失败上报失败：%v", runID, err)
		return
	}
	log.Printf("run=%s 标记失败 retryable=%v: %s", runID, retryable, message)
}

func (w *worker) instanceHeartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * w.cfg.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sendInstanceHeartbeat(ctx, 0)
		}
	}
}

func (w *worker) sendInstanceHeartbeat(ctx context.Context, activeRuns int) {
	// agents/serve_all 让平台知道「这台机器能接哪些应用」：调用方点工具时平台先看有没有
	// 在线 worker 能接单，没有就直接说清楚（worker_offline），不必让人干等同步窗口。
	metrics := map[string]any{
		"worker": "service-run-worker",
		"os":     runtime.GOOS,
		"arch":   runtime.GOARCH,
		"pid":    os.Getpid(),
	}
	if len(w.cfg.agents) > 0 {
		metrics["agents"] = w.cfg.agents
		metrics["serve_all"] = false
	} else {
		// 没限定 -agents = 所有应用都接；但应用仍需声明 runner.command 才会成功。
		metrics["agents"] = []string{}
		metrics["serve_all"] = true
	}
	payload := map[string]any{
		"active_runs": activeRuns,
		"metrics":     metrics,
	}
	var resp map[string]any
	path := w.cfg.apiPrefix + "/instances/" + w.cfg.instanceID + "/heartbeat"
	if err := w.client.post(ctx, path, payload, &resp); err != nil {
		log.Printf("实例心跳失败：%v", err)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// runPath 拼接某次 run 的操作路径（前缀由 -api-prefix 决定，默认是公网前缀）。
func (w *worker) runPath(runID, action string) string {
	return w.cfg.apiPrefix + "/runs/" + runID + "/" + action
}

// normalizeAPIPrefix 归一化 -api-prefix：保证以 / 开头、不带尾部 /。
func normalizeAPIPrefix(raw string) string {
	prefix := strings.TrimSpace(raw)
	if prefix == "" {
		return defaultAPIPrefix
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "worker"
	}
	id := fmt.Sprintf("%s-%d", host, os.Getpid())
	if len(id) > 64 {
		id = id[len(id)-64:]
	}
	return id
}

// httpClient 是极简 JSON 客户端（含重试友好的超时设置）。
//
// 身份来源有两种，都可选：
//   - token：网页生成的长期连接密钥（cwk_...），适合服务器/无头部署；
//   - userToken：账号登录态，用来换一张短期 worker 券（桌面端在进程内传入）。
//     券到期前会自动续，用户全程不用看见任何密钥。
type httpClient struct {
	base      string
	token     string
	hc        *http.Client
	userToken string    // 账号登录态，只用于换券
	apiPrefix string    // 换券接口所在前缀
	refreshAt time.Time // 券到期前的时间点
	mu        sync.Mutex
}

func newHTTPClient(base, token string) *httpClient {
	return &httpClient{
		base:  base,
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
	}
}

type workerSessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// ensureToken 在没有可用 token（或券快过期）时用账号登录态换一张。
func (c *httpClient) ensureToken(ctx context.Context) error {
	if c.userToken == "" {
		return nil // 用长期密钥，不需要换
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && !c.refreshAt.IsZero() && time.Now().Before(c.refreshAt) {
		return nil
	}
	body, _ := json.Marshal(map[string]any{})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+c.apiPrefix+"/session", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.userToken)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("worker session: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out workerSessionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if strings.TrimSpace(out.Token) == "" {
		return errors.New("worker session: 平台没有返回券")
	}
	c.token = out.Token
	// 在到期的一半时间点续券：既不会太频繁，也不会卡在边界上。
	c.refreshAt = time.Now().Add(6 * time.Hour)
	if expires, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		if half := time.Until(expires) / 2; half > 0 {
			c.refreshAt = time.Now().Add(half)
		}
	}
	return nil
}

func (c *httpClient) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *httpClient) post(ctx context.Context, path string, payload any, out any) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.currentToken())

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
