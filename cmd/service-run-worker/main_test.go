package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExpandCommand(t *testing.T) {
	run := claimRun{RunID: "run_1", SessionID: "sess_1", AgentSlug: "my-agent"}
	got := expandCommand(
		"agent --slug {slug} --run {run_id} --session {session_id} --input {input_file}",
		run, "/tmp/in.json")
	want := "agent --slug my-agent --run run_1 --session sess_1 --input /tmp/in.json"
	if got != want {
		t.Fatalf("expandCommand = %q, want %q", got, want)
	}
}

// worker 必须走公网前缀：/internal 只在 Compose 网络内可达（生产 nginx 直接 404）。
func TestRunPathUsesConfiguredPrefix(t *testing.T) {
	w := &worker{cfg: config{apiPrefix: defaultAPIPrefix}}
	if got := w.runPath("run_1", "complete"); got != "/api/service-worker/runs/run_1/complete" {
		t.Fatalf("runPath = %q", got)
	}
	// 前面有反向代理时可换前缀，结构保持 /runs/{id}/{action}。
	w.cfg.apiPrefix = "/custom-prefix"
	if got := w.runPath("run_1", "fail"); got != "/custom-prefix/runs/run_1/fail" {
		t.Fatalf("runPath = %q", got)
	}
}

func TestNormalizeAPIPrefix(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"", defaultAPIPrefix},
		{defaultAPIPrefix, defaultAPIPrefix},
		{"api/service-worker", "/api/service-worker"},
		{"/api/service-worker/", "/api/service-worker"},
		{"  /custom  ", "/custom"},
	}
	for _, c := range cases {
		if got := normalizeAPIPrefix(c.raw); got != c.want {
			t.Fatalf("normalizeAPIPrefix(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// 命令按应用下发时不要求 -cmd 也能启动（否则"一个 worker 服务多个 agent"根本起不来）。
func TestParseConfigAllowsCommandPerAgent(t *testing.T) {
	t.Setenv("CATS_SERVICE_INSTANCE_TOKEN", "tok")
	t.Setenv("CATS_USER_TOKEN", "")
	t.Setenv("CATSCO_WORKER_CMD", "")
	t.Setenv("CATSCO_WORKER_AGENTS", "")

	cfg, err := parseConfig([]string{"-agents", "a,b"})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.command != "" {
		t.Fatalf("command = %q, want empty", cfg.command)
	}
	if len(cfg.agents) != 2 || cfg.agents[0] != "a" || cfg.agents[1] != "b" {
		t.Fatalf("agents = %v", cfg.agents)
	}

	// 没有任何命令来源时仍可启动，缺失在 run 级别报 no_command。
	if _, err := parseConfig(nil); err != nil {
		t.Fatalf("parseConfig without cmd/agents: %v", err)
	}
	// 身份仍然是硬性要求（连接密钥或账号登录态二选一）。
	t.Setenv("CATS_SERVICE_INSTANCE_TOKEN", "")
	if _, err := parseConfig(nil); err == nil {
		t.Fatal("expected error for missing token")
	}
}

// 只给账号登录态也能启动：worker 会自己去换短期券，用户不用抄密钥。
func TestParseConfigAllowsUserToken(t *testing.T) {
	t.Setenv("CATS_WORKER_TOKEN", "")
	t.Setenv("CATS_SERVICE_INSTANCE_TOKEN", "")
	t.Setenv("CATS_USER_TOKEN", "")

	cfg, err := parseConfig([]string{"-user-token", "user-jwt", "-agents", "a"})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.token != "" || cfg.userToken != "user-jwt" {
		t.Fatalf("tokens = %q/%q", cfg.token, cfg.userToken)
	}
}

// ensureToken 用账号登录态换券，并在到期前续；换完就带上券去打业务接口。
func TestEnsureTokenExchangesUserToken(t *testing.T) {
	var mu sync.Mutex
	exchanges := 0
	var seenAuth []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/session"):
			exchanges++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ticket-1",
				"expires_at": time.Now().Add(12 * time.Hour).UTC().Format(time.RFC3339),
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	t.Cleanup(ts.Close)

	c := newHTTPClient(ts.URL, "")
	c.userToken = "user-jwt"
	c.apiPrefix = defaultAPIPrefix

	var out map[string]any
	if err := c.post(context.Background(), c.apiPrefix+"/claim", map[string]any{"limit": 1}, &out); err != nil {
		t.Fatalf("post: %v", err)
	}
	if exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1", exchanges)
	}
	if got := c.currentToken(); got != "ticket-1" {
		t.Fatalf("token = %q, want ticket-1", got)
	}
	// 第二次请求复用券，不再换。
	if err := c.post(context.Background(), c.apiPrefix+"/claim", map[string]any{"limit": 1}, &out); err != nil {
		t.Fatalf("second post: %v", err)
	}
	if exchanges != 1 {
		t.Fatalf("exchanges = %d after reuse, want 1", exchanges)
	}
	// 换券用账号态，业务请求用券。
	mu.Lock()
	defer mu.Unlock()
	if len(seenAuth) < 3 {
		t.Fatalf("requests = %d, want >=3", len(seenAuth))
	}
	if seenAuth[0] != "Bearer user-jwt" {
		t.Fatalf("exchange auth = %q", seenAuth[0])
	}
	if seenAuth[1] != "Bearer ticket-1" || seenAuth[2] != "Bearer ticket-1" {
		t.Fatalf("business auth = %q/%q", seenAuth[1], seenAuth[2])
	}
}

// 换券失败要显式报错，不能拿空 token 去静默重试。
func TestEnsureTokenFailsLoudly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	c := newHTTPClient(ts.URL, "")
	c.userToken = "expired-jwt"
	c.apiPrefix = defaultAPIPrefix
	if err := c.post(context.Background(), c.apiPrefix+"/claim", nil, nil); err == nil {
		t.Fatal("expected exchange failure")
	}
	if got := c.currentToken(); got != "" {
		t.Fatalf("token = %q, want empty on failure", got)
	}
}

func TestBuildResult(t *testing.T) {
	run := claimRun{RunID: "run_1", SessionID: "sess_1", AgentSlug: "x"}

	t.Run("plain text stdout", func(t *testing.T) {
		env, usage := buildResult(run, "  你好 world\n")
		if env["status"] != "completed" || env["contract_version"] != resultContractVersion {
			t.Fatalf("envelope = %+v", env)
		}
		output, _ := env["output"].(map[string]any)
		if output["text"] != "你好 world" {
			t.Fatalf("output = %+v", env["output"])
		}
		if usage != nil {
			t.Fatalf("usage = %v, want nil", usage)
		}
	})

	t.Run("structured stdout", func(t *testing.T) {
		stdout := `{"output":{"text":"ok","data":{"n":1}},"usage":{"prompt_tokens":12,"completion_tokens":3},"files":["https://f/1"]}`
		env, usage := buildResult(run, stdout)
		output, _ := env["output"].(map[string]any)
		if output["text"] != "ok" {
			t.Fatalf("output = %+v", output)
		}
		files, _ := env["files"].([]string)
		if len(files) != 1 || files[0] != "https://f/1" {
			t.Fatalf("files = %+v", env["files"])
		}
		usageMap, _ := usage.(map[string]any)
		if usageMap["prompt_tokens"].(float64) != 12 {
			t.Fatalf("usage = %+v", usage)
		}
		if env["usage"] == nil {
			t.Fatal("envelope should carry usage")
		}
	})
}

// 走一遍 worker 主循环：认领 → 执行 CLI → 回填完成。
func TestWorkerClaimsExecutesAndCompletes(t *testing.T) {
	const token = "test-instance-token"
	var (
		mu       sync.Mutex
		claimed  bool
		complete map[string]any
		done     = make(chan struct{})
		failMsg  string
	)

	mux := http.NewServeMux()
	authOK := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /api/service-worker/claim", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		var req struct {
			InstanceID string `json:"instance_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.InstanceID != "test-instance" {
			t.Errorf("instance_id = %q", req.InstanceID)
		}
		mu.Lock()
		defer mu.Unlock()
		if claimed {
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []any{}})
			return
		}
		claimed = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runs": []map[string]any{{
				"run_id":            "run_test_1",
				"session_id":        "sess_test_1",
				"agent_slug":        "ext-agent",
				"threshold_seconds": 60,
				"input":             map[string]any{"text": "hi"},
				"service_token":     "st_1",
			}},
		})
	})
	mux.HandleFunc("POST /api/service-worker/runs/{runID}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"renewed": true})
	})
	mux.HandleFunc("POST /api/service-worker/runs/{runID}/complete", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&complete)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"completed": true})
		close(done)
	})
	mux.HandleFunc("POST /api/service-worker/runs/{runID}/fail", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		var req struct {
			ErrorText string `json:"error_text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		failMsg = req.ErrorText
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		close(done)
	})
	mux.HandleFunc("POST /api/service-worker/instances/{instanceID}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	w := &worker{
		cfg: config{
			baseURL:    ts.URL,
			apiPrefix:  defaultAPIPrefix,
			token:      token,
			instanceID: "test-instance",
			command:    "echo {slug}",
			runTimeout: 10 * time.Second,
			pollEvery:  10 * time.Millisecond,
			heartbeat:  time.Second,
			once:       true,
		},
		client: newHTTPClient(ts.URL, token),
	}
	if err := w.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not report completion in time")
	}

	mu.Lock()
	defer mu.Unlock()
	if failMsg != "" {
		t.Fatalf("unexpected failure: %s", failMsg)
	}
	if complete == nil {
		t.Fatal("complete payload missing")
	}
	if complete["result_id"] != "res_run_test_1" {
		t.Fatalf("result_id = %v", complete["result_id"])
	}
	envelope, _ := complete["result"].(map[string]any)
	if envelope["status"] != "completed" || envelope["run_id"] != "run_test_1" {
		t.Fatalf("envelope = %+v", envelope)
	}
	output, _ := envelope["output"].(map[string]any)
	if got, _ := output["text"].(string); !strings.Contains(got, "ext-agent") {
		t.Fatalf("output.text = %q, want slug echo", got)
	}
}
