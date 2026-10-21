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
	mux.HandleFunc("POST /internal/service-runs/claim", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("POST /internal/service-runs/{runID}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"renewed": true})
	})
	mux.HandleFunc("POST /internal/service-runs/{runID}/complete", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&complete)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"completed": true})
		close(done)
	})
	mux.HandleFunc("POST /internal/service-runs/{runID}/fail", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("POST /internal/service-instances/{instanceID}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
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
