package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// TestTruncateWorkerOutput guards the server-log helper that bounds script
// output written on provision/operation failures (details must stay in the
// backend log, never echoed to the frontend).
func TestTruncateWorkerOutput(t *testing.T) {
	if got := truncateWorkerOutput("  ok  "); got != "ok" {
		t.Fatalf("truncate(short)=%q want ok", got)
	}
	if got := truncateWorkerOutput(""); got != "" {
		t.Fatalf("truncate(empty)=%q want empty", got)
	}
	long := strings.Repeat("x", 6000)
	got := truncateWorkerOutput(long)
	if len(got) > maxWorkerOutputLog+len("...(truncated)") {
		t.Fatalf("truncate(long) too long: %d", len(got))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("truncate(long) missing suffix: %q", got[len(got)-20:])
	}
}

func TestParseCloudWorkerRenewalExpiry(t *testing.T) {
	want := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	out := "provider diagnostic\n{" + `"status":"renewed","expires_at":"` + want.Format(time.RFC3339) + `"}`
	got, err := parseCloudWorkerRenewalExpiry(out)
	if err != nil {
		t.Fatalf("parse renewal expiry: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("expiry=%s want %s", got, want)
	}
	for _, tc := range []struct {
		name string
		out  string
	}{
		{"missing", `{"status":"renewed"}`},
		{"malformed", `not-json`},
		{"past", `{"expires_at":"2020-01-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseCloudWorkerRenewalExpiry(tc.out); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

type cloudWorkerTestStore struct {
	store.Store
	ownerBots         []map[string]interface{}
	deletedBots       []int64
	tenantNames       map[int64]string
	nextUID           int64
	friendPairs       map[string]bool
	setTenantNameFail bool
	creatorUser       *types.User
	botAPIKeys        map[int64]string
	botBodyIDs        map[int64]string
	botDefinitions    map[int64]*types.BotDefinitionRecord
	adminRecords      []types.CloudWorkerAdminRecord
}

// quotaCreditStub lets quota presentation tests exercise the durable credit
// branch without making the broad Store test double implement cloud-worker
// mutation methods.
type quotaCreditStub struct {
	total     int
	available int
}

func (s quotaCreditStub) CloudWorkerCreditSummary(int64) (int, int, error) {
	return s.total, s.available, nil
}
func (quotaCreditStub) ReserveCloudWorkerCredit(int64, string) (bool, error) { return false, nil }
func (quotaCreditStub) CommitCloudWorkerCredit(int64, string, int64, string, int) error {
	return nil
}
func (quotaCreditStub) ReleaseCloudWorkerCredit(int64, string) error            { return nil }
func (quotaCreditStub) ExtendCloudWorkerLifecycles(int64, time.Time, int) error { return nil }
func (quotaCreditStub) ListCloudWorkerLifecycleDue(time.Time, int) ([]CloudWorkerLifecycle, error) {
	return nil, nil
}
func (quotaCreditStub) MarkCloudWorkerLifecyclePending(int64, time.Time) error { return nil }
func (quotaCreditStub) ClaimCloudWorkerLifecycleDeletion(int64) (bool, error)  { return false, nil }
func (quotaCreditStub) MarkCloudWorkerLifecycleDeleted(int64, string) error    { return nil }

func (s *cloudWorkerTestStore) ListCloudWorkerAdminRecords() ([]types.CloudWorkerAdminRecord, error) {
	return append([]types.CloudWorkerAdminRecord(nil), s.adminRecords...), nil
}

func (s *cloudWorkerTestStore) GetUser(id int64) (*types.User, error) {
	if s.creatorUser != nil {
		return s.creatorUser, nil
	}
	return &types.User{Username: "creator", DisplayName: "Creator"}, nil
}

func (s *cloudWorkerTestStore) GetBotAPIKey(botUID int64) (string, error) {
	if value := s.botAPIKeys[botUID]; value != "" {
		return value, nil
	}
	return "test-bot-api-key", nil
}

func (s *cloudWorkerTestStore) GetBotBodyID(botUID int64) (string, error) {
	return s.botBodyIDs[botUID], nil
}

func (s *cloudWorkerTestStore) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	if definition := s.botDefinitions[botUID]; definition != nil {
		return definition, nil
	}
	return nil, errors.New("not found")
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
		case "tsv":
			// 真实 list-worker-images.sh TSV 契约：imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status
			body = "@echo off\r\necho 79f5b7f4-c06e-4f97-90fa-d69566f23d63\tcatsco-worker-1-4-8-f3f1f3e6\tv1.4.8\tf3f1f3e6\t1786066647\tactive\r\n"
		case "releases-tsv":
			body = "@echo off\r\necho 1.4.9\t1787066647\r\necho 1.4.8\t1786066647\r\n"
		case "status-tsv":
			// status-worker.sh TSV：实例、状态、镜像、镜像版本、实际应用版本。
			body = "@echo off\r\necho worker-bot-bot-a\trunning\t79f5b7f4-c06e-4f97-90fa-d69566f23d63\tv1.4.8\t1.4.7\r\necho worker-bot-bot-b\tcreating\t79f5b7f4-c06e-4f97-90fa-d69566f23d63\tv1.4.8\t\r\n"
		case "slow-status":
			body = "@echo off\r\nping 127.0.0.1 -n 2 >nul\r\necho worker-bot-bot-a\trunning\timg-slow\tv1.4.8\r\n"
		case "require-identity":
			// Credentials must arrive through the restricted file, never argv.
			body = "@echo off\r\nif not \"%3\"==\"--credential-file\" exit /b 1\r\nif not exist \"%4\" exit /b 1\r\necho %* | findstr /C:\"--bot-uid\" >nul || exit /b 1\r\necho ok\r\n"
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
	case "tsv":
		// 真实 list-worker-images.sh TSV 契约（printf 的 \\t 是字面 tab）
		body = "#!/bin/sh\nprintf '79f5b7f4-c06e-4f97-90fa-d69566f23d63\\tcatsco-worker-1-4-8-f3f1f3e6\\tv1.4.8\\tf3f1f3e6\\t1786066647\\tactive\\n'\n"
	case "releases-tsv":
		body = "#!/bin/sh\nprintf '1.4.9\\t1787066647\\n1.4.8\\t1786066647\\n'\n"
	case "status-tsv":
		// status-worker.sh TSV：实例、状态、镜像、镜像版本、实际应用版本。
		body = "#!/bin/sh\nprintf 'worker-bot-bot-a\\trunning\\t79f5b7f4-c06e-4f97-90fa-d69566f23d63\\tv1.4.8\\t1.4.7\\nworker-bot-bot-b\\tcreating\\t79f5b7f4-c06e-4f97-90fa-d69566f23d63\\tv1.4.8\\t\\n'\n"
	case "slow-status":
		body = "#!/bin/sh\nsleep 1\nprintf 'worker-bot-bot-a\\trunning\\timg-slow\\tv1.4.8\\n'\n"
	case "require-identity":
		// Credentials are read from the 0600 file; identity metadata remains argv.
		// （模拟 provision-worker.sh 写 localConfig 的必填身份），缺则 fail
		body = "#!/bin/sh\nlogin=\"\"; key=\"\"; cred=\"\"; bot=\"\"; user=\"\"; uname=\"\"; udisp=\"\"; prev=\"\"; for a in \"$@\"; do case \"$prev\" in --credential-file) cred=\"$a\";; --bot-uid) bot=\"$a\";; --user-uid) user=\"$a\";; --user-name) uname=\"$a\";; --user-display) udisp=\"$a\";; esac; prev=\"$a\"; done; [ -f \"$cred\" ] && login=\"$(sed -n '1p' \"$cred\")\" && key=\"$(sed -n '2p' \"$cred\")\"; [ -n \"$login\" ] && [ -n \"$key\" ] && [ -n \"$bot\" ] && [ -n \"$user\" ] && [ -n \"$uname\" ] && [ -n \"$udisp\" ] || { echo \"missing identity\" >&2; exit 1; }; echo ok\n"
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
	if p, ok := scripts["update"]; ok {
		cfg.UpdateScript = p
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
	if p, ok := scripts["releases"]; ok {
		cfg.ReleasesScript = p
	}
	if p, ok := scripts["status"]; ok {
		cfg.StatusScript = p
	}
	return cfg
}

func cloudWorkerRequest(uid int64, method, path string, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer test-owner-token")
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

func waitForCloudWorkerSnapshot(t *testing.T, h *CloudWorkerHandler, images bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.cacheMu.Lock()
		loaded := h.statusLoaded
		refreshing := h.statusRefreshing
		if images {
			loaded = h.imagesLoaded
			refreshing = h.imagesRefreshing
		}
		h.cacheMu.Unlock()
		if loaded {
			return
		}
		if !refreshing {
			t.Fatalf("cloud snapshot refresh stopped before producing a snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for cloud snapshot")
}

func waitForCloudWorkerReleaseSnapshot(t *testing.T, h *CloudWorkerHandler) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.cacheMu.Lock()
		loaded := h.releasesLoaded
		refreshing := h.releasesRefreshing
		h.cacheMu.Unlock()
		if loaded {
			return
		}
		if !refreshing {
			t.Fatalf("cloud release refresh stopped before producing a snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for cloud release snapshot")
}

func waitForCloudWorkerRefreshIdle(t *testing.T, h *CloudWorkerHandler, images bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.cacheMu.Lock()
		refreshing := h.statusRefreshing
		if images {
			refreshing = h.imagesRefreshing
		}
		h.cacheMu.Unlock()
		if !refreshing {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for cloud refresh to stop")
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

func TestCloudWorkerQuotaSummaryIncludesConsumedCredits(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("7=0")
	h.credits = quotaCreditStub{total: 1, available: 0}
	total, used, remaining := h.quotaSummary(7, 0)
	if total != 1 || used != 1 || remaining != 0 {
		t.Fatalf("consumed credit quota=(%d,%d,%d), want (1,1,0)", total, used, remaining)
	}
}

func TestCloudWorkerQuotaSummaryDoesNotCountAvailableCreditAsUsed(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("7=0")
	h.credits = quotaCreditStub{total: 1, available: 1}
	total, used, remaining := h.quotaSummary(7, 0)
	if total != 1 || used != 0 || remaining != 1 {
		t.Fatalf("available credit quota=(%d,%d,%d), want (1,0,1)", total, used, remaining)
	}
}

func TestCloudWorkerHandleList(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=5")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
		{"id": int64(2), "username": "bot-b", "display_name": "B"}, // self-hosted, excluded
	}
	ts.botDefinitions = map[int64]*types.BotDefinitionRecord{
		1: {DefaultPrompt: &types.BotDefaultPromptSnapshot{XiaoBaVersion: "1.4.9"}},
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
	if first["app_version"] != "" {
		t.Fatalf("app_version=%v want unknown runtime version", first["app_version"])
	}
	if first["cloud_status"] != "unavailable" {
		t.Fatalf("cloud_status=%v want unavailable when status probe is not configured", first["cloud_status"])
	}
	quota := out["quota"].(map[string]interface{})
	if quota["total"].(float64) != 5 || quota["used"].(float64) != 1 || quota["remaining"].(float64) != 4 {
		t.Fatalf("quota=%v", quota)
	}
}

func TestParseCloudWorkerStatusTSV(t *testing.T) {
	out := "worker-aaa\trunning\timg-1\tv1.2.3\t1.2.4\nworker-bbb\tcreating\timg-2\t\nother-instance\trunning\timg-1\tv1\t9.9.9\n"
	infos := parseCloudWorkerStatusTSV(out)
	if len(infos) != 2 {
		t.Fatalf("want 2 infos, got %d (%v)", len(infos), infos)
	}
	if got := infos["aaa"]; got.Status != "running" || got.ImageID != "img-1" || got.Version != "v1.2.3" || got.AppVersion != "1.2.4" {
		t.Fatalf("aaa info = %+v", got)
	}
	if got := infos["bbb"]; got.Status != "creating" || got.ImageID != "img-2" || got.Version != "" {
		t.Fatalf("bbb info = %+v", got)
	}
	if _, ok := infos["other-instance"]; ok {
		t.Fatalf("non-worker- prefixed instance should be ignored")
	}
	// 空/畸形行不崩溃（worker-x 无 tab 列被忽略，worker-y 正常计入）
	if got := parseCloudWorkerStatusTSV("\nworker-x\nworker-y\trunning\n"); len(got) != 1 {
		t.Fatalf("lenient parse got %v", got)
	}
}

func TestCloudWorkerHandleListFillsCloudStatus(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{"status": writeWorkerOpScript(t, "status-tsv")})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	waitForCloudWorkerSnapshot(t, h, false)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
		{"id": int64(2), "username": "bot-b", "display_name": "B", "tenant_name": "bot-bot-b"},
		{"id": int64(3), "username": "bot-c", "display_name": "C", "tenant_name": "bot-bot-c"}, // 无实例行 → missing
	}

	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	workers, _ := out["workers"].([]interface{})
	if len(workers) != 3 {
		t.Fatalf("want 3 workers, got %d", len(workers))
	}
	byTenant := map[string]map[string]interface{}{}
	for _, w := range workers {
		m := w.(map[string]interface{})
		byTenant[m["tenant_name"].(string)] = m
	}
	a := byTenant["bot-bot-a"]
	if a["cloud_status"] != "running" || a["cloud_version"] != "v1.4.8" || a["cloud_image_id"] != "79f5b7f4-c06e-4f97-90fa-d69566f23d63" {
		t.Fatalf("bot-a cloud facts = %v", a)
	}
	if a["app_version"] != "1.4.7" {
		t.Fatalf("bot-a app_version=%v want actual status version 1.4.7", a["app_version"])
	}
	b := byTenant["bot-bot-b"]
	if b["cloud_status"] != "creating" {
		t.Fatalf("bot-b cloud_status = %v", b["cloud_status"])
	}
	c := byTenant["bot-bot-c"]
	if c["cloud_status"] != "missing" {
		t.Fatalf("bot-c cloud_status=%v want missing", c["cloud_status"])
	}
}

func TestCloudWorkerHandleListDoesNotWaitForStatusScript(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{"status": writeWorkerOpScript(t, "slow-status")})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	started := time.Now()
	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("cloud worker list waited for provider script: %v", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	if out["status_refreshing"] != true {
		t.Fatalf("status_refreshing=%v want true", out["status_refreshing"])
	}

	// Let the background process complete before TempDir cleanup, and verify
	// that the next request immediately serves the completed snapshot.
	waitForCloudWorkerSnapshot(t, h, false)
	rec = httptest.NewRecorder()
	h.HandleList(rec, req)
	worker := decodeCloudWorkerList(t, rec)["workers"].([]interface{})[0].(map[string]interface{})
	if worker["cloud_status"] != "running" {
		t.Fatalf("cloud_status=%v want running", worker["cloud_status"])
	}
}

func TestCloudWorkerHandleListDoesNotTrustExpiredSnapshot(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{"status": writeWorkerOpScript(t, "status-tsv")})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	waitForCloudWorkerSnapshot(t, h, false)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	// Simulate a provider outage after a once-valid status. The old data remains
	// cached for diagnostics/recovery, but once it exceeds the trust window it
	// must not keep presenting "running" forever.
	h.statusScript = writeWorkerOpScript(t, "fail")
	h.cacheMu.Lock()
	h.statusUpdatedAt = time.Now().Add(-cloudWorkerStatusMaxTrustAge - time.Second)
	h.statusLastAttempt = time.Time{}
	h.cacheMu.Unlock()

	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)
	worker := decodeCloudWorkerList(t, rec)["workers"].([]interface{})[0].(map[string]interface{})
	if worker["cloud_status"] != "unavailable" {
		t.Fatalf("cloud_status=%v want unavailable for expired snapshot", worker["cloud_status"])
	}
	waitForCloudWorkerRefreshIdle(t, h, false)

	h.cacheMu.Lock()
	_, retained := h.statusSnapshot["bot-bot-a"]
	h.cacheMu.Unlock()
	if !retained {
		t.Fatal("failed refresh should retain the last good snapshot")
	}
}

func TestCloudWorkerHandleListStatusScriptFailureFallsBack(t *testing.T) {
	// 状态脚本失败时列表仍返回，并明确标记 unavailable（不伪装成加载中）。
	cfg := workerScriptCfg(t, "7=5", map[string]string{"status": writeWorkerOpScript(t, "fail")})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
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
		t.Fatalf("want 1 worker, got %d", len(workers))
	}
	first := workers[0].(map[string]interface{})
	if first["cloud_status"] != "unavailable" {
		t.Fatalf("cloud_status=%v want unavailable on script failure", first["cloud_status"])
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

func TestCloudWorkerHandleActionsNotConfigured(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("7=5")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	for _, tc := range []struct {
		path string
		code string
	}{
		{"/api/cloud-workers/bot-bot-a/update", "cloud_worker_update_unconfigured"},
		{"/api/cloud-workers/bot-bot-a/rollback", "cloud_worker_rollback_unconfigured"},
		{"/api/cloud-workers/bot-bot-a/reset", "cloud_worker_reset_unconfigured"},
	} {
		req := cloudWorkerRequest(7, http.MethodPost, tc.path, nil)
		rec := httptest.NewRecorder()
		// route through HandleSub so PathValue gets set, like the mux does
		h.HandleSub(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d want 503 body=%s", tc.path, rec.Code, rec.Body.String())
		}
		out := decodeCloudWorkerList(t, rec)
		if out["code"] != tc.code {
			t.Fatalf("%s code=%v want %s", tc.path, out["code"], tc.code)
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
	actions := out["actions"].(map[string]interface{})
	for _, action := range []string{"create", "update", "rollback", "reset", "delete"} {
		if actions[action] != false {
			t.Fatalf("actions[%q]=%v want false", action, actions[action])
		}
	}
}

func TestCloudWorkerHandleMetaWithImagesScript(t *testing.T) {
	// 用真实 list-worker-images.sh TSV 输出契约（配对 B4-1 bash 脚本）
	cfg := workerScriptCfg(t, "7=3", map[string]string{"images": writeWorkerOpScript(t, "tsv")})
	if cfg.ImagesScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	waitForCloudWorkerSnapshot(t, h, true)
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
	first := images[0].(map[string]interface{})
	if first["image_id"] != "79f5b7f4-c06e-4f97-90fa-d69566f23d63" {
		t.Fatalf("image_id=%v", first["image_id"])
	}
	if first["name"] != "catsco-worker-1-4-8-f3f1f3e6" {
		t.Fatalf("name=%v", first["name"])
	}
	if first["version"] != "v1.4.8" {
		t.Fatalf("version=%v", first["version"])
	}
}

func TestCloudWorkerHandleMetaSeparatesApplicationReleasesFromImages(t *testing.T) {
	cfg := workerScriptCfg(t, "7=3", map[string]string{
		"images":   writeWorkerOpScript(t, "tsv"),
		"releases": writeWorkerOpScript(t, "releases-tsv"),
	})
	if cfg.ImagesScript == "" || cfg.ReleasesScript == "" {
		t.Skip("no POSIX shell")
	}
	h, _ := newCloudWorkerTestHandlerCfg(cfg)
	waitForCloudWorkerSnapshot(t, h, true)
	waitForCloudWorkerReleaseSnapshot(t, h)

	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers/meta", nil)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	images := out["images"].([]interface{})
	releases := out["releases"].([]interface{})
	if images[0].(map[string]interface{})["version"] != "v1.4.8" {
		t.Fatalf("images=%v", images)
	}
	if releases[0].(map[string]interface{})["version"] != "1.4.9" {
		t.Fatalf("releases=%v", releases)
	}
}

func TestCloudWorkerHandleMetaReportsConfiguredActions(t *testing.T) {
	cfg := workerScriptCfg(t, "7=3", map[string]string{
		"provision": writeWorkerOpScript(t, "ok"),
		"update":    writeWorkerOpScript(t, "ok"),
		"destroy":   writeWorkerOpScript(t, "ok"),
	})
	h, _ := newCloudWorkerTestHandlerCfg(cfg)
	req := cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers/meta", nil)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	actions := out["actions"].(map[string]interface{})
	for action, want := range map[string]bool{
		"create": true, "update": true, "rollback": false, "reset": false, "delete": true,
	} {
		if actions[action] != want {
			t.Fatalf("actions[%q]=%v want %v", action, actions[action], want)
		}
	}
}

func TestParseImageLinesKeepsNewestSix(t *testing.T) {
	lines := []string{}
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("img-%d\tworker-%d\t1.4.%d\tcommit-%d\t%d\tactive", i, i, i, i, i*100))
	}
	images := parseImageLines(strings.Join(lines, "\n"))
	if len(images) != 6 {
		t.Fatalf("len(images)=%d want 6", len(images))
	}
	if images[0].Version != "1.4.8" || images[5].Version != "1.4.3" {
		t.Fatalf("versions=%v want newest 1.4.8..1.4.3", images)
	}
}

func TestParseReleaseLinesKeepsNewestUniquePublishedVersions(t *testing.T) {
	releases := parseReleaseLines(strings.Join([]string{
		"1.4.8\t1786066647",
		"v1.4.9\t1787066647",
		"1.4.9\t1787066646",
		"bad/version\t1788066647",
	}, "\n"))
	if len(releases) != 2 {
		t.Fatalf("releases=%v want 2", releases)
	}
	if releases[0].Version != "1.4.9" || releases[1].Version != "1.4.8" {
		t.Fatalf("releases=%v want 1.4.9,1.4.8", releases)
	}
}

func TestParseImageLines(t *testing.T) {
	// 真实 list-worker-images.sh TSV 契约（配对小仓 B4-1 bash 脚本）
	out := "79f5b7f4-c06e-4f97-90fa-d69566f23d63\tcatsco-worker-1-4-8-f3f1f3e6\tv1.4.8\tf3f1f3e6\t1786066647\tactive\n" +
		"# comment line\n" +
		"\n" +
		"imageID\tname\tversion\tcommit\tcreatedTime\tstatus\n" +
		"abc-123\tcatsco-worker-1-4-7-aaa\tv1.4.7\taaa\t1786000000\tactive\n"
	images := parseImageLines(out)
	if len(images) != 2 {
		t.Fatalf("want 2 images, got %d: %+v", len(images), images)
	}
	if images[0].ImageID != "79f5b7f4-c06e-4f97-90fa-d69566f23d63" || images[0].Name != "catsco-worker-1-4-8-f3f1f3e6" || images[0].Version != "v1.4.8" || images[0].Commit != "f3f1f3e6" {
		t.Fatalf("first row mismatch: %+v", images[0])
	}
	if images[0].CreatedTime != "1786066647" || images[0].Status != "active" {
		t.Fatalf("first row extra fields: %+v", images[0])
	}
	if images[1].ImageID != "abc-123" || images[1].Version != "v1.4.7" {
		t.Fatalf("second row mismatch: %+v", images[1])
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

func TestCloudWorkerHandleCreatePassesIdentity(t *testing.T) {
	// 链路验证：HandleCreate 必须把 worker owner token + bot/user 身份完整传给 provision
	// 脚本（--login-token/--api-key/--bot-uid/--user-uid/--user-name/--user-display）。
	// require-identity fake 缺任何一项都会 exit 1 -> HandleCreate 走 502。
	cfg := workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "require-identity")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.creatorUser = &types.User{Username: "alice", DisplayName: "Alice"}

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	req.Header.Set("Authorization", "Bearer testjwt")
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

// writeCredentialCaptureScript records the first line of --credential-file so
// tests can inspect the token without ever placing it in process argv.
func writeCredentialCaptureScript(t *testing.T, tokenPath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return ""
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "capture-credential.sh")
	body := fmt.Sprintf("#!/bin/sh\ncred=\"\"; prev=\"\"\nfor a in \"$@\"; do\n  case \"$prev\" in --credential-file) cred=\"$a\";; esac\n  prev=\"$a\"\ndone\n[ -f \"$cred\" ] || exit 1\nsed -n '1p' \"$cred\" > %q\n", tokenPath)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func assertPersistentWorkerToken(t *testing.T, tokenPath string, ownerUID int64, requestToken string) {
	t.Helper()
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read captured worker token: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	if got == "" {
		t.Fatal("worker token is empty")
	}
	if got == requestToken {
		t.Fatal("worker token must not reuse the request JWT")
	}
	claims, err := ParseToken(got)
	if err != nil {
		t.Fatalf("parse worker token: %v", err)
	}
	if claims.TokenType != persistentUserTokenType {
		t.Fatalf("worker token type=%q want %q", claims.TokenType, persistentUserTokenType)
	}
	if claims.UID != ownerUID {
		t.Fatalf("worker token uid=%d want %d", claims.UID, ownerUID)
	}
	if claims.ExpiresAt != nil {
		t.Fatalf("persistent worker token unexpectedly expires at %v", claims.ExpiresAt)
	}
}

func TestCloudWorkerHandleCreateUsesPersistentOwnerToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential capture test requires a POSIX shell")
	}
	tokenPath := filepath.Join(t.TempDir(), "create-token.txt")
	capture := writeCredentialCaptureScript(t, tokenPath)
	cfg := workerScriptCfg(t, "7=5", map[string]string{"provision": capture})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.creatorUser = &types.User{Username: "alice", Email: "alice@example.com", DisplayName: "Alice"}
	requestToken := "test-owner-token"
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-x", "display_name": "X",
	})
	req.Header.Set("Authorization", "Bearer "+requestToken)
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	assertPersistentWorkerToken(t, tokenPath, 7, requestToken)
}

func TestCloudWorkerHandleResetUsesPersistentOwnerToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential capture test requires a POSIX shell")
	}
	tokenPath := filepath.Join(t.TempDir(), "reset-token.txt")
	capture := writeCredentialCaptureScript(t, tokenPath)
	cfg := workerScriptCfg(t, "7=5", map[string]string{"reset": capture})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	ts.botAPIKeys = map[int64]string{1: "worker-specific-key"}
	ts.creatorUser = &types.User{Username: "alice", Email: "alice@example.com", DisplayName: "Alice"}
	requestToken := "test-owner-token"
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/reset", map[string]string{"version": "v1"})
	req.Header.Set("Authorization", "Bearer "+requestToken)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	assertPersistentWorkerToken(t, tokenPath, 7, requestToken)
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
	// --- provision fails but destroy succeeds: partially created instance is
	// cleaned up and the bot record is rolled back (no orphan) ---
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"provision": writeWorkerOpScript(t, "fail"),
		"destroy":   writeWorkerOpScript(t, "ok"),
	})
	if cfg.ProvisionScript == "" || cfg.DestroyScript == "" {
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
		t.Fatalf("want 1 rollback delete after successful cleanup, got %v", ts.deletedBots)
	}
	// tenant_name 现在总是在 provision 之前持久化（句柄先行），即使 destroy 清理后删 bot
	if len(ts.tenantNames) != 1 {
		t.Fatalf("tenant_name should be persisted (before provision) even when destroy cleaned up: %v", ts.tenantNames)
	}

	// --- provision fails and destroy is NOT configured: keep the bot record
	// as a retryable handle (tenant_name persisted) instead of deleting the
	// only record that can locate a possibly-running, still-billed instance ---
	h2, ts2 := newCloudWorkerTestHandlerCfg(workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "fail")}))
	req2 := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-y", "display_name": "Y",
	})
	rec2 := httptest.NewRecorder()
	h2.HandleCreate(rec2, req2)
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("no-destroy status=%d want 502 body=%s", rec2.Code, rec2.Body.String())
	}
	if len(ts2.deletedBots) != 0 {
		t.Fatalf("bot must be kept when no destroy script can clean up: %v", ts2.deletedBots)
	}
	if len(ts2.tenantNames) != 1 {
		t.Fatalf("tenant_name must be persisted as retryable state: %v", ts2.tenantNames)
	}

	// --- provision fails and destroy also fails: keep bot + tenant_name so
	// the roster still shows the worker and delete can be retried ---
	cfg3 := workerScriptCfg(t, "7=5", map[string]string{
		"provision": writeWorkerOpScript(t, "fail"),
		"destroy":   writeWorkerOpScript(t, "fail"),
	})
	h3, ts3 := newCloudWorkerTestHandlerCfg(cfg3)
	req3 := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
		"username": "bot-z", "display_name": "Z",
	})
	rec3 := httptest.NewRecorder()
	h3.HandleCreate(rec3, req3)
	if rec3.Code != http.StatusBadGateway {
		t.Fatalf("destroy-fail status=%d want 502 body=%s", rec3.Code, rec3.Body.String())
	}
	if len(ts3.deletedBots) != 0 {
		t.Fatalf("bot must be kept when destroy also fails: %v", ts3.deletedBots)
	}
	if len(ts3.tenantNames) != 1 {
		t.Fatalf("tenant_name must be persisted when destroy also fails: %v", ts3.tenantNames)
	}
}

func TestCloudWorkerHandleCreateSetTenantFails(t *testing.T) {
	// SetTenantName 现在总是在 provision（云资源创建）之前持久化；写入失败时云资源
	// 尚未创建，直接回滚删 bot 是安全的（不会产生孤儿实例）——与 destroy 脚本配置无关。
	cases := []struct {
		name string
		cfg  CloudWorkerConfig
	}{
		{"destroy ok", workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "ok"), "destroy": writeWorkerOpScript(t, "ok")})},
		{"destroy missing", workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "ok")})},
		{"destroy fail", workerScriptCfg(t, "7=5", map[string]string{"provision": writeWorkerOpScript(t, "ok"), "destroy": writeWorkerOpScript(t, "fail")})},
	}
	for _, tc := range cases {
		if tc.cfg.ProvisionScript == "" {
			t.Skip("no POSIX shell")
		}
		h, ts := newCloudWorkerTestHandlerCfg(tc.cfg)
		ts.setTenantNameFail = true
		req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{
			"username": "bot-x", "display_name": "X",
		})
		rec := httptest.NewRecorder()
		h.HandleCreate(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("[%s] status=%d want 500 body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if len(ts.deletedBots) != 1 {
			t.Fatalf("[%s] want 1 rollback delete (safe: no cloud resource created yet), got %v", tc.name, ts.deletedBots)
		}
		if len(ts.tenantNames) != 0 {
			t.Fatalf("[%s] tenant_name must not be persisted when SetTenantName failed: %v", tc.name, ts.tenantNames)
		}
	}
}

func TestCloudWorkerHandleRollbackResetSuccess(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"rollback": writeWorkerOpScript(t, "ok"),
		"update":   writeWorkerOpScript(t, "ok"),
		"reset":    writeWorkerOpScript(t, "ok"),
	})
	if cfg.RollbackScript == "" || cfg.UpdateScript == "" || cfg.ResetScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	// update and rollback forward the optional version selector.
	for _, path := range []string{
		"/api/cloud-workers/bot-bot-a/update",
		"/api/cloud-workers/bot-bot-a/rollback",
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

	// reset without a version selector succeeds (rebuild from latest image)
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/reset", nil)
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloudWorkerBusyOperationReturnsConflictWithoutQueueing(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"update":  writeWorkerOpScript(t, "ok"),
		"destroy": writeWorkerOpScript(t, "ok"),
	})
	if cfg.UpdateScript == "" || cfg.DestroyScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	h.opMu.Lock()
	defer h.opMu.Unlock()

	tests := []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodPost, "/api/cloud-workers", map[string]string{"username": "new-worker"}},
		{http.MethodPost, "/api/cloud-workers/bot-bot-a/update", map[string]string{"version": "v1.4.9"}},
		{http.MethodDelete, "/api/cloud-workers/bot-bot-a", nil},
	}
	for _, tc := range tests {
		req := cloudWorkerRequest(7, tc.method, tc.path, tc.body)
		rec := httptest.NewRecorder()
		if tc.path == "/api/cloud-workers" {
			h.HandleCreate(rec, req)
		} else {
			h.HandleSub(rec, req)
		}
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s status=%d want 409 body=%s", tc.path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Retry-After") != "15" {
			t.Fatalf("%s Retry-After=%q want 15", tc.path, rec.Header().Get("Retry-After"))
		}
		out := decodeCloudWorkerList(t, rec)
		if out["code"] != "cloud_worker_operation_busy" {
			t.Fatalf("%s code=%v", tc.path, out["code"])
		}
	}
}

// TestCloudWorkerHandleResetForwardsVersion asserts reset forwards an optional
// version selector to reset-worker.sh (which maps it to the matching image id,
// falling back to the latest image when omitted).
func TestCloudWorkerHandleResetForwardsVersion(t *testing.T) {
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

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/reset", map[string]string{"version": "v1"})
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset with version status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	if out["status"] != "ok" {
		t.Fatalf("reset status field=%v", out["status"])
	}
}

func TestCloudWorkerHandleUpdateRequiresExplicitApplicationVersion(t *testing.T) {
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"update": writeWorkerOpScript(t, "record"),
	})
	if cfg.UpdateScript == "" {
		t.Skip("no POSIX shell")
	}
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/update", map[string]string{})
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeCloudWorkerList(t, rec)
	if out["code"] != "cloud_worker_update_version_required" {
		t.Fatalf("code=%v", out["code"])
	}
}

// TestCloudWorkerHandleVersionForwarding asserts the exact argv passed to the
// scripts: update, rollback and reset all forward --version <v>; reset also
// receives a fresh, worker-specific identity from the database/request.
func TestCloudWorkerHandleVersionForwarding(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "argv.txt")
	writeArgv := func(name string) string {
		if runtime.GOOS == "windows" {
			name += ".cmd" // .cmd shim: %* expands to the full argument list
		}
		script := filepath.Join(dir, name)
		var body string
		if runtime.GOOS == "windows" {
			body = "@echo off\r\necho %* > \"" + recordFile + "\"\r\n"
		} else {
			body = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + recordFile + "\"\n"
		}
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"update":   writeArgv("update-op.sh"),
		"rollback": writeArgv("rollback-op.sh"),
		"reset":    writeArgv("reset-op.sh"),
	})
	h, ts := newCloudWorkerTestHandlerCfg(cfg)
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "display_name": "A", "tenant_name": "bot-bot-a"},
	}
	ts.botAPIKeys = map[int64]string{1: "worker-specific-key"}
	ts.botBodyIDs = map[int64]string{1: "worker-body-id"}
	ts.creatorUser = &types.User{Username: "owner-name", DisplayName: "Owner Display"}

	// update forwards the selected target.
	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/update", map[string]string{"version": "v1.4.9"})
	rec := httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	argv, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--version") || !strings.Contains(string(argv), "v1.4.9") {
		t.Fatalf("update argv=%q want --version v1.4.9", argv)
	}

	// rollback forwards the version selector
	req = cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/rollback", map[string]string{"version": "v1.4.7"})
	rec = httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rec.Code, rec.Body.String())
	}
	argv, err = os.ReadFile(recordFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--version") || !strings.Contains(string(argv), "v1.4.7") {
		t.Fatalf("rollback argv=%q want --version v1.4.7", argv)
	}

	// reset forwards the version selector too
	req = cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers/bot-bot-a/reset", map[string]string{"version": "v1.4.7"})
	rec = httptest.NewRecorder()
	h.HandleSub(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	argv, err = os.ReadFile(recordFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--version") || !strings.Contains(string(argv), "v1.4.7") {
		t.Fatalf("reset argv=%q want --version v1.4.7", argv)
	}
	for _, expected := range []string{
		"--credential-file", "--bot-uid", "1", "--user-uid", "7", "--user-name", "owner-name",
		"--user-display", "Owner Display", "--body-id", "worker-body-id",
	} {
		if !strings.Contains(string(argv), expected) {
			t.Fatalf("reset argv=%q missing %q", argv, expected)
		}
	}
	// The credential path is intentionally ephemeral; the fake script records
	// argv only, so verify no secret value leaked into it.
	if strings.Contains(string(argv), "test-owner-token") || strings.Contains(string(argv), "worker-specific-key") {
		t.Fatalf("reset argv leaked credential: %q", argv)
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

	// --- without destroy script: fail closed (503), record kept ---
	h2, ts2 := newCloudWorkerTestHandler("7=5")
	ts2.ownerBots = []map[string]interface{}{
		{"id": int64(2), "username": "bot-b", "display_name": "B", "tenant_name": "bot-bot-b"},
	}
	req2 := cloudWorkerRequest(7, http.MethodDelete, "/api/cloud-workers/bot-bot-b", nil)
	rec2 := httptest.NewRecorder()
	h2.HandleSub(rec2, req2)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-destroy status=%d want 503 body=%s", rec2.Code, rec2.Body.String())
	}
	if out := decodeCloudWorkerList(t, rec2); out["code"] != "cloud_worker_delete_unconfigured" {
		t.Fatalf("no-destroy code=%v want cloud_worker_delete_unconfigured", out["code"])
	}
	if len(ts2.deletedBots) != 0 {
		t.Fatalf("deletedBots=%v want 0 (fail closed)", ts2.deletedBots)
	}

	// --- without destroy script: force=1 is NOT honored (no public override;
	// any owner could otherwise bypass the fail-closed guard) ---
	req2f := cloudWorkerRequest(7, http.MethodDelete, "/api/cloud-workers/bot-bot-b?force=1", nil)
	rec2f := httptest.NewRecorder()
	h2.HandleSub(rec2f, req2f)
	if rec2f.Code != http.StatusServiceUnavailable {
		t.Fatalf("force status=%d want 503 body=%s", rec2f.Code, rec2f.Body.String())
	}
	if len(ts2.deletedBots) != 0 {
		t.Fatalf("deletedBots=%v want 0 (force must not bypass fail-closed)", ts2.deletedBots)
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

func TestCloudWorkerMuxRouting(t *testing.T) {
	// End-to-end route test through a real ServeMux, registered exactly like
	// server/cmd/server.go (minus JWT auth; requests carry the uid in context).
	// This guards the GET/POST split on /api/cloud-workers — the create path
	// must hit HandleCreate, not the GET-only HandleList.
	cfg := workerScriptCfg(t, "7=5", map[string]string{
		"provision": writeWorkerOpScript(t, "ok"),
		"update":    writeWorkerOpScript(t, "ok"),
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
		{http.MethodPost, "/api/cloud-workers/bot-bot-a/update", map[string]string{"version": "v1.4.9"}, http.StatusOK},
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
