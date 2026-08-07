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
	ownerBots   []map[string]interface{}
	deletedBots []int64
	tenantNames map[int64]string
	nextUID     int64
	friendPairs map[string]bool
}

func (s *cloudWorkerTestStore) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	return s.ownerBots, nil
}

func (s *cloudWorkerTestStore) DeleteBot(botUID int64) error {
	s.deletedBots = append(s.deletedBots, botUID)
	return nil
}

func (s *cloudWorkerTestStore) SetTenantName(botUID int64, tenantName string) error {
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
	ts := &cloudWorkerTestStore{}
	cfg := CloudWorkerConfig{
		CreateQuota: quota,
	}
	botHandler := NewBotHandler(ts, nil)
	return NewCloudWorkerHandler(ts, botHandler, cfg), ts
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

	out, err := h.runScript(context.Background(), script, "hello")
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
