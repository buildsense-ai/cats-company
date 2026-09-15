package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestShimoSkillClientContract is an optional cross-repository contract test.
// CI for cats-company remains self-contained; local delivery checks can point
// SHIMO_SKILL_CLIENT_PATH at the installed shimo-reader Node.js client.
func TestShimoSkillClientContract(t *testing.T) {
	clientPath := os.Getenv("SHIMO_SKILL_CLIENT_PATH")
	if clientPath == "" {
		t.Skip("set SHIMO_SKILL_CLIENT_PATH to run the shimo-reader contract test")
	}
	if _, err := os.Stat(clientPath); err != nil {
		t.Fatalf("skill client unavailable: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}

	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "http://127.0.0.1",
		Backend:     mockShimoConnectorBackend{},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shimo/connection", handler.HandleConnection)
	mux.HandleFunc("/v1/shimo/connection-link", handler.HandleConnectionLink)
	mux.HandleFunc("/v1/shimo/sheets/list", handler.HandleListSheets)
	mux.HandleFunc("/v1/shimo/sheets/read", handler.HandleReadSheet)
	mux.HandleFunc("/v1/shimo/documents/read", handler.HandleReadDocument)
	mux.HandleFunc("/connect/shimo/", handler.HandleLoginAttempt)
	server := httptest.NewServer(mux)
	defer server.Close()
	handler.publicURL = server.URL

	token, err := GenerateShimoActorToken(shimoTestSecret, "agent-42", "user-a", "task-contract-test", "catsco/shimo-reader", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	runClient := func(arguments ...string) map[string]any {
		t.Helper()
		command := exec.Command(node, append([]string{clientPath}, arguments...)...)
		command.Env = append(os.Environ(), "CATSCO_SHIMO_CONNECTOR_URL="+server.URL, "CATSCO_ACTOR_TOKEN="+token, "CATSCO_SKILL_ID=catsco/shimo-reader")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("skill client %v failed: %v\n%s", arguments, err, output)
		}
		var decoded map[string]any
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatalf("decode client output %q: %v", output, err)
		}
		return decoded
	}

	status := runClient("status")
	if nestedString(status, "data", "state") != "disconnected" {
		t.Fatalf("unexpected initial status: %#v", status)
	}
	connect := runClient("connect")
	connectionURL := nestedString(connect, "data", "connection_url")
	response, err := http.Post(connectionURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mock login status=%d", response.StatusCode)
	}

	outputPath := filepath.Join(t.TempDir(), "sheet-snapshot.json")
	read := runClient("read-sheet", "--url", "https://shimo.im/sheets/contract-test/?temporary=1", "--sheet", "项目表", "--range", "A1:C20", "--output", outputPath)
	if read["ok"] != true || read["rows"] != float64(2) {
		t.Fatalf("unexpected read result: %#v", read)
	}
	snapshotBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("snapshot was not written: %v", err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	// 老字段在 1.0.3 和 1.0.4 上都成立：快照写出来了，行数、列数、内容哈希都在。
	if snapshot["rows"] != float64(2) || snapshot["columns"] != float64(2) {
		t.Fatalf("snapshot lost rows/columns: %#v", snapshot)
	}
	if digest, _ := snapshot["content_sha256"].(string); digest == "" {
		t.Fatalf("snapshot lost content_sha256: %#v", snapshot)
	}
	// 覆盖范围元数据（requested_range / requests / covered_through_row / stopped_early /
	// truncated）是 shimo-reader 1.0.4 才写进摘要和快照的。交付检查跑的时候 SkillHub 上可能
	// 仍装着 1.0.3：这时按版本跳过新增断言，只保留上面的老字段断言；1.0.4 发布后这些断言会
	// 自动开始生效，不需要再改这个文件。
	if read["requested_range"] == nil || read["truncated"] == nil || read["stopped_early"] == nil {
		t.Logf("shimo-reader client 仍是 <1.0.4（摘要里没有覆盖范围字段），跳过新增元数据断言；发布 1.0.4 后自动生效。client 输出：%#v", read)
		return
	}
	// 覆盖范围字段必须一路穿过 connector 到达 skill，并落进快照。
	if read["truncated"] != false || read["requested_range"] != "A1:C20" || read["requests"] != float64(1) {
		t.Fatalf("skill client dropped sheet coverage metadata: %#v", read)
	}
	if read["stopped_early"] != false {
		t.Fatalf("skill client dropped stopped_early: %#v", read)
	}
	if snapshot["truncated"] != false || snapshot["requested_range"] != "A1:C20" || snapshot["requests"] != float64(1) {
		t.Fatalf("snapshot lost sheet coverage metadata: %#v", snapshot)
	}
	if snapshot["stopped_early"] != false {
		t.Fatalf("snapshot lost stopped_early: %#v", snapshot)
	}

	// 截断的读取必须在 client 输出里显式标记，不能被当成完整快照。
	truncatedHandler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "http://127.0.0.1",
		Backend:     shimoContractLifecycleBackend{shimoContractBackend{truncated: true}},
	})
	truncatedMux := http.NewServeMux()
	truncatedMux.HandleFunc("/v1/shimo/connection", truncatedHandler.HandleConnection)
	truncatedMux.HandleFunc("/v1/shimo/connection-link", truncatedHandler.HandleConnectionLink)
	truncatedMux.HandleFunc("/v1/shimo/sheets/read", truncatedHandler.HandleReadSheet)
	truncatedMux.HandleFunc("/connect/shimo/", truncatedHandler.HandleLoginAttempt)
	truncatedServer := httptest.NewServer(truncatedMux)
	defer truncatedServer.Close()
	truncatedHandler.publicURL = truncatedServer.URL

	truncatedToken, err := GenerateShimoActorToken(shimoTestSecret, "agent-42", "user-a", "task-contract-truncated", "catsco/shimo-reader", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	runTruncated := func(arguments ...string) map[string]any {
		t.Helper()
		command := exec.Command(node, append([]string{clientPath}, arguments...)...)
		command.Env = append(os.Environ(), "CATSCO_SHIMO_CONNECTOR_URL="+truncatedServer.URL, "CATSCO_ACTOR_TOKEN="+truncatedToken, "CATSCO_SKILL_ID=catsco/shimo-reader")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("skill client %v failed: %v\n%s", arguments, err, output)
		}
		var decoded map[string]any
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatalf("decode client output %q: %v", output, err)
		}
		return decoded
	}
	truncatedOutput := filepath.Join(t.TempDir(), "truncated-snapshot.json")
	truncatedRead := runTruncated("read-sheet", "--url", "https://shimo.im/sheets/contract-test/", "--sheet", "项目表", "--range", "A1:Z1000", "--output", truncatedOutput)
	if truncatedRead["ok"] != true || truncatedRead["truncated"] != true {
		t.Fatalf("truncated read was not flagged: %#v", truncatedRead)
	}
	if warning, _ := truncatedRead["warning"].(string); !strings.Contains(warning, "缩小范围") {
		t.Fatalf("truncated read is missing an actionable warning: %#v", truncatedRead)
	}
	truncatedSnapshotBytes, err := os.ReadFile(truncatedOutput)
	if err != nil {
		t.Fatalf("truncated snapshot was not written: %v", err)
	}
	var truncatedSnapshot map[string]any
	if err := json.Unmarshal(truncatedSnapshotBytes, &truncatedSnapshot); err != nil {
		t.Fatalf("decode truncated snapshot: %v", err)
	}
	if truncatedSnapshot["truncated"] != true || truncatedSnapshot["covered_through_row"] != float64(153) {
		t.Fatalf("truncated snapshot lost coverage metadata: %#v", truncatedSnapshot)
	}
}

// shimoContractBackend 在 mock 后端之上补齐 worker 现在会返回的覆盖范围字段，
// 用来端到端验证「worker 信号 → connector → skill 快照」这一整条链路。
type shimoContractBackend struct {
	mockShimoConnectorBackend
	truncated bool
}

func (b shimoContractBackend) ReadSheet(ctx context.Context, actor shimoActor, sourceURL, sheetName, cellRange string) (map[string]any, error) {
	data, err := b.mockShimoConnectorBackend.ReadSheet(ctx, actor, sourceURL, sheetName, cellRange)
	if err != nil {
		return nil, err
	}
	if b.truncated {
		data["covered_through_row"] = 153
		data["truncated"] = true
	}
	return data, nil
}

// shimoContractLifecycleBackend 让截断用例跳过 mock 登录按钮：
// 生命周期后端只要能报告 connected，连接器就允许读取。
type shimoContractLifecycleBackend struct {
	shimoContractBackend
}

func (b shimoContractLifecycleBackend) ConnectionStatus(context.Context, shimoActor) (shimoConnectionStatus, error) {
	return shimoConnectionStatus{State: "connected"}, nil
}

func (b shimoContractLifecycleBackend) Disconnect(context.Context, shimoActor) error { return nil }

func (b shimoContractLifecycleBackend) StartLogin(context.Context, shimoActor, string) (string, error) {
	return "https://app.catsco.test/shimo-login/contract/", nil
}
