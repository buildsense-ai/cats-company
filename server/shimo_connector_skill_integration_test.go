package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("snapshot was not written: %v", err)
	}
}
