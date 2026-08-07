package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type cloudWorkerTestStore struct {
	store.Store
	ownerBots         []map[string]interface{}
	deletedBots       []int64
	tenantNames       map[int64]string
	nextUID           int64
	friendPairs       map[string]bool
	setTenantNameFail bool
}

func (s *cloudWorkerTestStore) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	return s.ownerBots, nil
}

func (s *cloudWorkerTestStore) DeleteBot(botUID int64) error {
	s.deletedBots = append(s.deletedBots, botUID)
	return nil
}

func (s *cloudWorkerTestStore) SetTenantName(botUID int64, tenantName string) error {
	if s.setTenantNameFail {
		return errors.New("set tenant name failed")
	}
	if s.tenantNames == nil {
		s.tenantNames = map[int64]string{}
	}
	s.tenantNames[botUID] = tenantName
	return nil
}

func (s *cloudWorkerTestStore) GetTenantName(botUID int64) (string, error) {
	if s.tenantNames == nil {
		return "", errors.New("not found")
	}
	name, ok := s.tenantNames[botUID]
	if !ok {
		return "", errors.New("not found")
	}
	return name, nil
}

func (s *cloudWorkerTestStore) GetUserByUsername(username string) (*types.User, error) {
	return nil, nil
}

func (s *cloudWorkerTestStore) CreateUser(user *types.User) (int64, error) {
	if s.nextUID == 0 {
		s.nextUID = 100
	}
	s.nextUID++
	return s.nextUID, nil
}

func (s *cloudWorkerTestStore) SaveBotConfigWithOwner(uid, ownerID int64, apiEndpoint, model string) error {
	return nil
}

func (s *cloudWorkerTestStore) SaveAPIKey(uid int64, apiKey string) error {
	return nil
}

func (s *cloudWorkerTestStore) CreateFriendRequest(uid, with int64, note string) (int64, error) {
	return 1, nil
}

func (s *cloudWorkerTestStore) AcceptFriendRequest(uid, with int64) error {
	if s.friendPairs == nil {
		s.friendPairs = map[string]bool{}
	}
	s.friendPairs[agentPairKey(uid, with)] = true
	return nil
}

func newCloudWorkerTestHandler(quota string) (*CloudWorkerHandler, *cloudWorkerTestStore) {
	return newCloudWorkerTestHandlerCfg(CloudWorkerConfig{CreateQuota: quota})
}

func newCloudWorkerTestHandlerCfg(cfg CloudWorkerConfig) (*CloudWorkerHandler, *cloudWorkerTestStore) {
	ts := &cloudWorkerTestStore{}
	botHandler := NewBotHandler(ts)
	return NewCloudWorkerHandler(ts, botHandler, cfg), ts
}

// writeWorkerOpScript creates a tiny executable script whose behavior matches
// the requested kind: "ok" exits 0, "fail" exits 1, "record" echoes argv to
// stdout. Returns "" when no interpreter is available (POSIX host without sh)
// so callers can skip.
func writeWorkerOpScript(t *testing.T, behavior string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "worker-op.cmd")
		var body string
		switch behavior {
		case "ok":
			body = "@echo off\r\necho ok\r\n"
		case "fail":
			body = "@echo off\r\nexit /b 1\r\n"
		case "record":
			body = "@echo off\r\necho %*\r\n"
		default:
			t.Fatalf("unknown behavior %q", behavior)
		}
		if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return script
	}
	if _, err := exec.LookPath("sh"); err != nil {
		return ""
	}
	script := filepath.Join(dir, "worker-op.sh")
	var body string
	switch behavior {
	case "ok":
		body = "#!/bin/sh\necho ok\n"
	case "fail":
		body = "#!/bin/sh\nexit 1\n"
	case "record":
		body = "#!/bin/sh\necho \"$@\"\n"
	default:
		t.Fatalf("unknown behavior %q", behavior)
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// workerScriptCfg builds a CloudWorkerConfig with the named scripts attached.
func workerScriptCfg(t *testing.T, quota string, scripts map[string]string) CloudWorkerConfig {
	t.Helper()
	cfg := CloudWorkerConfig{CreateQuota: quota}
	if p, ok := scripts["provision"]; ok {
		cfg.ProvisionScript = p
	}
	if p, ok := scripts["reset"]; ok {
		cfg.ResetScript = p
	}
	if p, ok := scripts["rollback"]; ok {
		cfg.RollbackScript = p
	}
	if p, ok := scripts["destroy"]; ok {
		cfg.DestroyScript = p
	}
	if p, ok := scripts["images"]; ok {
		cfg.ImagesScript = p
	}
	return cfg
}

func cloudWorkerRequest(uid int64, method, path string, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	return req.WithContext(context.WithValue(req.Context(), uidKey, uid))
}

func decodeCloudWorkerList(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return out
}

func TestParseWorkerCreateQuota(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[int64]int
	}{
		{"empty", "", map[int64]int{}},
		{"single", "7=3", map[int64]int{7: 3}},
		{"multiple", "7=3;8=5", map[int64]int{7: 3, 8: 5}},
		{"comma sep", "7=3,8=5", map[int64]int{7: 3, 8: 5}},
		{"zero quota", "7=0", map[int64]int{7: 0}},
		{"bad entry ignored", "abc=3;7=2", map[int64]int{7: 2}},
		{"negative ignored", "7=-1", map[int64]int{}},
		{"whitespace", " 7 = 3 ", map[int64]int{7: 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseWorkerCreateQuota(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
			for uid, n := range c.want {
				if got[uid] != n {
					t.Fatalf("uid %d got %d want %d", uid, got[uid], n)
				}
			}
		})
	}
}

func TestCloudWorkerHandleList(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=5")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
		{"id": int64(2), "username": "bot-b", "display_name": "B"}, // self-hosted, excluded
	}

	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	workers, _ := out["workers"].([]interface{})
	if len(workers) != 1 {
		t.Fatalf("want 1 cloud worker, got %d (body=%s)", len(workers), rec.Body.String())
	}
	first := workers[0].(map[string]interface{})
	if first["tenant_name"] != "bot-bot-a" {
		t.Fatalf("tenant_name=%v", first["tenant_name"])
	}
	quota := out["quota"].(map[string]interface{})
	if quota["total"].(float64) != 5 || quota["used"].(float64) != 1 || quota["remaining"].(float64) != 4 {
		t.Fatalf("quota=%v", quota)
	}
}

func TestCloudWorkerHandleListNoQuotaConfigured(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("") // quota unset = disabled
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)

	out := decodeCloudWorkerList(t, rec)
	quota := out["quota"].(map[string]interface{})
	if quota["enabled"].(bool) != false {
		t.Fatalf("quota should be disabled, got %v", quota)
	}
}

func TestCloudWorkerHandleCreateNoQuota(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloudWorkerHandleCreateQuotaExhausted(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=1")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloudWorkerHandleCreateProvisionNotConfigured(t *testing.T) {
	// Quota available but no provision script → 503 and the bot account is
	// rolled back (deleted).
	h, ts := newCloudWorkerTestHandler("7=5")
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", rec.Code, rec.Body.String())
	}
	if len(ts.deletedBots) != 1 {
		t.Fatalf("want 1 rollback delete, got %v", ts.deletedBots)
	}
}

func TestCloudWorkerHandleRollbackResetNotConfigured(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=5")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	for _, path := range []string{
		"/api/cloud-workers/bot-bot-a/rollback",
		"/api/cloud-workers/bot-bot-a/reset",
	} {
		req := cloudWorkerRequest(7, http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		// route through HandleSub so PathValue gets set, like the mux does
		h.HandleSub(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d want 503 body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCloudWorkerHandleActionNotOwned(t *testing.T) {
	// owner 7 owns bot-bot-a; owner 8 owns nothing → 404 for owner 8.
	_, ts := newCloudWorkerTestHandler("7=5")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	h8, _ := newCloudWorkerTestHandler("8=5")

	req := cloudWorkerRequest(8, http.MethodPost, "/api/cloud-workers/bot-bot-a/reset", nil)
	rec := httptest.NewRecorder()
	h8.HandleSub(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloudWorkerRunScriptDirectExec(t *testing.T) {
	// runScript must execute the script directly (its shebang decides the
	// interpreter) rather than assuming PowerShell — the production server
	// image is a minimal Linux image without PowerShell. Cross-platform:
	// Windows uses a .cmd script, POSIX hosts use an sh shebang script.
	h, _ := newCloudWorkerTestHandler("7=1")
	dir := t.TempDir()
	var script string
	if runtime.GOOS == "windows" {
		script = filepath.Join(dir, "worker-op.cmd")
		if err := os.WriteFile(script, []byte("@echo off\r\necho ok-%1\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := exec.LookPath("sh"); err != nil {
			t.Skip("no sh in PATH")
		}
		script = filepath.Join(dir, "worker-op.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho ok-$1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	out, err := h.runScript(script, "hello")
	if err != nil {
		t.Fatalf("runScript direct exec failed: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "ok-hello") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestCloudWorkerHandleMetaQuota(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=3")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers/meta", nil)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	quota := out["quota"].(map[string]interface{})
	if quota["total"].(float64) != 3 || quota["remaining"].(float64) != 2 {
		t.Fatalf("quota=%v", quota)
	}
	if _, ok := out["images"]; ok {
		t.Fatalf("images should be absent when no images script configured, got %v", out["images"])
	}
}

func TestCloudWorkerHandleMetaWithImagesScript(t *testing.T) {
	cfg := workerScriptCfg(t, "7=3", map[string]string{"images": writeWorkerOpScript(t, "ok")})
	if cfg.ImagesScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers/meta", nil)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	images, ok := out["images"].([]interface{})
	if !ok || len(images) != 1 {
		t.Fatalf("images=%v want 1 entry", out["images"])
	}
}

func TestCloudWorkerHandleCreateSuccess(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	if out["tenant_name"] != "bot-bot-x" {
		t.Fatalf("tenant_name=%v", out["tenant_name"])
	}
	if out["deployment_status"] != "running" {
		t.Fatalf("deployment_status=%v want running", out["deployment_status"])
	}
	botUID := int64(out["uid"].(float64))
	if ts.tenantNames[botUID] != "bot-bot-x" {
		t.Fatalf("tenant_name not persisted: %v", ts.tenantNames)
	}
	if len(ts.deletedBots) != 0 {
		t.Fatalf("bot should not be rolled back on success: %v", ts.deletedBots)
	}
	if !ts.friendPairs[agentPairKey(7, botUID)] {
		t.Fatalf("friend was not auto added")
	}
}

func TestCloudWorkerHandleCreateInvalidUsername(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("7=5")
	for _, bad := range []string{"bad/name", "has space", "..", "x!", "UPPER"} {
		req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
			"username": bad, "display_name": "X",
		})
		rec := httptest.NewRecorder()
		h.HandleCreate(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("username %q status=%d want 400 body=%s", bad, rec.Code, rec.Body.String())
		}
	}
}

func TestCloudWorkerHandleCreateProvisionFails(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "fail")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502 body=%s", rec.Code, rec.Body.String())
	}
	if len(ts.deletedBots) != 1 {
		t.Fatalf("want 1 rollback delete, got %v", ts.deletedBots)
	}
}

func TestCloudWorkerHandleCreateSetTenantFails(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.setTenantNameFail = true

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
	if len(ts.deletedBots) != 1 {
		t.Fatalf("want 1 rollback delete, got %v", ts.deletedBots)
	}
}

func TestCloudWorkerHandleRollbackResetSuccess(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"rollback": writeWorkerOpScript(t, "ok"),
		"reset":    writeWorkerOpScript(t, "ok"),
	})
	if cfg.RollbackScript == "" || cfg.ResetScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	for _, path := range []string{
		"/api/cloud-workers/bot-bot-a/rollback",
		"/api/cloud-workers/bot-bot-a/reset",
	} {
		req := cloudWorkerRequest(7, http.MethodPost, path, map[string]string{"version": "v1"})
		rec := httptest.NewRecorder()
		h.HandleSub(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d want 200 body=%s", path, rec.Code, rec.Body.String())
		}
		out := decodeCloudWorkerList(t, rec)
		if out["status"] != "ok" {
			t.Fatalf("%s status field=%v", path, out["status"])
		}
	}
}

func TestCloudWorkerHandleDelete(t *testing.T) {
	// --- with destroy script: instance destroyed + bot removed ---
	cfg := workerScriptCfg(t, "7=5", map[string]string{"destroy": writeWorkerOpScript(t, "ok")})
	if cfg.DestroyScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	req := cloudWorkerRequest(7, http.MethodDelete, "/api/cloud-workers/bot-bot-a", nil)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	if out["status"] != "deleted" {
		t.Fatalf("status=%v", out["status"])
	}
	if len(ts.deletedBots) != 1 || ts.deletedBots[0] != 1 {
		t.Fatalf("deletedBots=%v want [1]", ts.deletedBots)
	}

	// --- without destroy script: DB removed + warning returned ---
	h2, ts2 := newCloudWorkerTestHandler("7=5")
	ts2.ownerBots = []map[string]interface{}{
		{"id": int64(2), "username": "bot-b", "display_name": "B", "tenant_name": "bot-bot-b"},
	}
	req2 := cloudWorkerRequest(7, http.MethodDelete, "/api/cloud-workers/bot-bot-b", nil)
	rec2 := httptest.NewRecorder()
	h2.HandleSub(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("no-destroy status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	out2 := decodeCloudWorkerList(t, rec2)
	if _, ok := out2["warning"]; !ok {
		t.Fatalf("expected warning without destroy script: %v", out2)
	}
	if len(ts2.deletedBots) != 1 {
		t.Fatalf("deletedBots=%v want 1", ts2.deletedBots)
	}

	// --- destroy failure: 502, bot kept ---
	cfg3 := workerScriptCfg(t, "7=5", map[string]string{"destroy": writeWorkerOpScript(t, "fail")})
	h3, ts3 := newCloudWorkerTestHandlerCfg(cfg3)
	ts3.ownerBots = []map[string]interface{}{
		{"id": int64(3), "username": "bot-c", "display_name": "C", "tenant_name": "bot-bot-c"},
	}
	req3 := cloudWorkerRequest(7, http.MethodDelete, "/api/cloud-workers/bot-bot-c", nil)
	rec3 := httptest.NewRecorder()
	h3.HandleSub(rec3, req3)
	if rec3.Code != http.StatusBadGateway {
		t.Fatalf("destroy-fail status=%d want 502 body=%s", rec3.Code, rec3.Body.String())
	}
	if len(ts3.deletedBots) != 0 {
		t.Fatalf("bot should be kept when destroy fails: %v", ts3.deletedBots)
	}

	// --- not owned → 404 ---
	h4, _ := newCloudWorkerTestHandler("8=5")
	req4 := cloudWorkerRequest(8, http.MethodDelete, "/api/cloud-workers/bot-bot-a", nil)
	rec4 := httptest.NewRecorder()
	h4.HandleSub(rec4, req4)
	if rec4.Code != http.StatusNotFound {
		t.Fatalf("not-owned status=%d want 404 body=%s", rec4.Code, rec4.Body.String())
	}
}

func TestCloudWorkerHandleSubRouteBoundaries(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=5")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/cloud-workers//rollback", http.StatusBadRequest}, // empty name
		{http.MethodPost, "/api/cloud-workers/unknown/foo", http.StatusNotFound},
		{http.MethodGet, "/api/cloud-workers/bot-bot-a", http.StatusMethodNotAllowed}, // {name} is DELETE
		{http.MethodPost, "/api/cloud-workers/bot-bot-a", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/cloud-workers/", http.StatusNotFound},
		{http.MethodGet, "/api/cloud-workers/meta/x", http.StatusNotFound},
	}
	for _, c := range cases {
		req := cloudWorkerRequest(7, c.method, c.path, nil)
		rec := httptest.NewRecorder()
		h.HandleSub(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%s %s status=%d want %d body=%s", c.method, c.path, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestCloudWorkerRunScriptVersionArg(t *testing.T) {
	script := writeWorkerOpScript(t, "record")
	if script == "" {
		t.Skip("no POSIX shell")
	}
	h, _ := newCloudWorkerTestHandler("7=1")
	out, err := h.runScript(script, "-Action", "rollback", "-Name", "bot-x", "-Version", "v1")
	if err != nil {
		t.Fatalf("runScript failed: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "-Version") || !strings.Contains(out, "v1") {
		t.Fatalf("version arg not forwarded: %q", out)
	}
}

func TestParseImageLines(t *testing.T) {
	got := parseImageLines("imageID name version commit\n# comment\nimg-123\n  \nname\n")
	if len(got) != 1 || got[0] != "img-123" {
		t.Fatalf("got %v want [img-123]", got)
	}
}

func TestCloudWorkerMuxRouting(t *testing.T) {
	// End-to-end route test through a real ServeMux, registered exactly like
	// server/cmd/server.go (minus JWT auth; requests carry the uid in context).
	// This guards the GET/POST split on /api/cloud-workers — the create path
	// must hit HandleCreate, not the GET-only HandleList.
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"provision": writeWorkerOpScript(t, "ok"),
		"rollback":  writeWorkerOpScript(t, "ok"),
		"reset":     writeWorkerOpScript(t, "ok"),
		"destroy":   writeWorkerOpScript(t, "ok"),
	})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cloud-workers", h.HandleList)
	mux.HandleFunc("POST /api/cloud-workers", h.HandleCreate)
	mux.HandleFunc("/api/cloud-workers/", h.HandleSub)

	cases := []struct {
		method, path string
		body         interface{}
		want         int
	}{
		{http.MethodGet, "/api/cloud-workers", nil, http.StatusOK},
		{http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-x", "display_name": "X"}, http.StatusCreated},
		{http.MethodGet, "/api/cloud-workers/meta", nil, http.StatusOK},
		{http.MethodPost, "/api/cloud-workers/bot-bot-a/rollback", nil, http.StatusOK},
		{http.MethodPost, "/api/cloud-workers/bot-bot-a/reset", nil, http.StatusOK},
		{http.MethodDelete, "/api/cloud-workers/bot-bot-a", nil, http.StatusOK},
	}
	for _, c := range cases {
		req := cloudWorkerRequest(7, c.method, c.path, c.body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%s %s status=%d want %d body=%s", c.method, c.path, rec.Code, c.want, rec.Body.String())
		}
	}
}
