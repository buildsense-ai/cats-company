package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 云端包分发：发布/拉取/claim 下发 package_hash（设计 A §2）。
func helloBundleFixture() ServiceAgentBundle {
	return ServiceAgentBundle{
		Schema: ServiceAgentBundleSchema,
		Slug:   "hello",
		Files: []ServiceAgentBundleFile{
			{Path: "agent.json", Content: `{"schema":"catsco.service-agent.v1","slug":"hello","prompt":{"system":"system-prompt.md"},"models":["MiniMax-M3"]}`},
			{Path: "system-prompt.md", Content: "你是 Hello 试点应用。\n"},
		},
	}
}

func TestServicePackagePublishAndDownload(t *testing.T) {
	st, _, _ := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)

	bundleJSON, _ := json.Marshal(helloBundleFixture())

	// 鉴权
	if status, _ := doInternal(t, "POST", base+"/internal/service-agents/hello/publish", "", string(bundleJSON)); status != http.StatusUnauthorized {
		t.Fatalf("publish without token status = %d, want 401", status)
	}
	// slug 不匹配（包内容 slug=hello，路由=custom）
	if status, payload := doInternal(t, "POST", base+"/internal/service-agents/custom/publish", "inst-token-1", string(bundleJSON)); status != http.StatusBadRequest {
		t.Fatalf("slug mismatch status = %d body=%s, want 400", status, payload)
	}
	// 未知 agent（包自洽但平台无此 agent）
	ghost := helloBundleFixture()
	ghost.Slug = "ghost"
	for i := range ghost.Files {
		ghost.Files[i].Content = strings.ReplaceAll(ghost.Files[i].Content, `"slug":"hello"`, `"slug":"ghost"`)
	}
	ghostJSON, _ := json.Marshal(ghost)
	if status, _ := doInternal(t, "POST", base+"/internal/service-agents/ghost/publish", "inst-token-1", string(ghostJSON)); status != http.StatusNotFound {
		t.Fatalf("unknown agent status = %d, want 404", status)
	}
	// 坏包
	if status, _ := doInternal(t, "POST", base+"/internal/service-agents/hello/publish", "inst-token-1", `{"schema":"x"}`); status != http.StatusBadRequest {
		t.Fatalf("invalid bundle status = %d, want 400", status)
	}

	// 发布成功
	status, payload := doInternal(t, "POST", base+"/internal/service-agents/hello/publish", "inst-token-1", string(bundleJSON))
	if status != http.StatusOK {
		t.Fatalf("publish status = %d body=%s", status, payload)
	}
	var published struct {
		Version     int64  `json:"version"`
		PackageHash string `json:"package_hash"`
	}
	if err := json.Unmarshal(payload, &published); err != nil {
		t.Fatalf("publish decode: %v", err)
	}
	if len(published.PackageHash) != 64 {
		t.Fatalf("package_hash = %q", published.PackageHash)
	}
	if want := CanonicalServiceBundleHash(bundleJSONToFiles(t, bundleJSON)); want != published.PackageHash {
		t.Fatalf("client-computed hash %s != server %s", want, published.PackageHash)
	}

	// 幂等重发：同 hash 不再新版本
	status, replay := doInternal(t, "POST", base+"/internal/service-agents/hello/publish", "inst-token-1", string(bundleJSON))
	if status != http.StatusOK {
		t.Fatalf("republish status = %d", status)
	}
	var replayed struct {
		Version     int64  `json:"version"`
		PackageHash string `json:"package_hash"`
	}
	_ = json.Unmarshal(replay, &replayed)
	if replayed.PackageHash != published.PackageHash || replayed.Version != published.Version {
		t.Fatalf("republish not idempotent: %+v vs %+v", replayed, published)
	}

	// force 重发：同 hash 也建新版本（回滚通道：重发旧包 → 新版本指向旧内容）
	status, forced := doInternal(t, "POST", base+"/internal/service-agents/hello/publish?force=1", "inst-token-1", string(bundleJSON))
	if status != http.StatusOK {
		t.Fatalf("force publish status = %d body=%s", status, forced)
	}
	var forcedResult struct {
		Version     int64  `json:"version"`
		PackageHash string `json:"package_hash"`
	}
	_ = json.Unmarshal(forced, &forcedResult)
	if forcedResult.Version != published.Version+1 {
		t.Fatalf("force version = %d, want %d", forcedResult.Version, published.Version+1)
	}
	if forcedResult.PackageHash != published.PackageHash {
		t.Fatalf("force hash = %q, want unchanged %q", forcedResult.PackageHash, published.PackageHash)
	}

	// 拉取：鉴权 / 未知 hash / 命中
	if status, _ := doInternal(t, "GET", base+"/internal/service-agent-packages/"+published.PackageHash, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("download without token status = %d, want 401", status)
	}
	if status, _ := doInternal(t, "GET", base+"/internal/service-agent-packages/deadbeef", "inst-token-1", ""); status != http.StatusNotFound {
		t.Fatalf("unknown hash status = %d, want 404", status)
	}
	status, fetched := doInternal(t, "GET", base+"/internal/service-agent-packages/"+published.PackageHash, "inst-token-1", "")
	if status != http.StatusOK {
		t.Fatalf("download status = %d body=%s", status, fetched)
	}
	if string(fetched) != string(bundleJSON) {
		t.Fatalf("downloaded bundle mismatch:\n%s\nvs\n%s", fetched, bundleJSON)
	}
}

func TestServiceClaimCarriesPackageHash(t *testing.T) {
	st, _, _ := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)
	bundleJSON, _ := json.Marshal(helloBundleFixture())

	status, payload := doInternal(t, "POST", base+"/internal/service-agents/hello/publish", "inst-token-1", string(bundleJSON))
	if status != http.StatusOK {
		t.Fatalf("publish status = %d body=%s", status, payload)
	}
	var published struct {
		PackageHash string `json:"package_hash"`
	}
	_ = json.Unmarshal(payload, &published)

	status, claimPayload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, claimPayload)
	}
	var result struct {
		Runs []struct {
			AgentSlug   string `json:"agent_slug"`
			PackageHash string `json:"package_hash"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(claimPayload, &result); err != nil {
		t.Fatalf("claim decode: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("claim runs = %d, want 1", len(result.Runs))
	}
	if result.Runs[0].AgentSlug != "hello" || result.Runs[0].PackageHash != published.PackageHash {
		t.Fatalf("claim run = %+v, want slug=hello hash=%s", result.Runs[0], published.PackageHash)
	}
}

// 无包（旧库/未发布）时 claim 不带 package_hash（实例回退内置包）。
func TestServiceClaimWithoutPackageHash(t *testing.T) {
	st, _, _ := seedOrchestratorFixture(t)
	_, base := newOrchestratorServer(t, st)

	status, claimPayload := doInternal(t, "POST", base+"/internal/service-runs/claim", "inst-token-1", `{"limit":5,"instance_id":"inst-a"}`)
	if status != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", status, claimPayload)
	}
	var result struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(claimPayload, &result); err != nil {
		t.Fatalf("claim decode: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("claim runs = %d, want 1", len(result.Runs))
	}
	if _, present := result.Runs[0]["package_hash"]; present {
		t.Fatalf("package_hash must be omitted when no package published: %v", result.Runs[0])
	}
}

// bundleJSONToFiles 把打包 JSON 还原成文件列表（发布脚本/实例侧同款解析路径）。
func bundleJSONToFiles(t *testing.T, raw []byte) []ServiceAgentBundleFile {
	t.Helper()
	var bundle ServiceAgentBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("bundle decode: %v", err)
	}
	return bundle.Files
}
