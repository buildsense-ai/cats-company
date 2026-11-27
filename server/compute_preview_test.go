package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestComputePreviewMCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slurm.json")
	handler := NewServiceAgentsMCPHandler(newFakeServiceAgentStore())
	handler.SetComputeSnapshotPath(path)
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	if !containsName(testToolNames(t, session), computeToolName) {
		t.Fatal("unified compute tool missing from tools/list")
	}
	handler.NotifySubscriptionChanged(441)
	if !containsName(testToolNames(t, session), computeToolName) {
		t.Fatal("subscription refresh removed unified compute tool")
	}
	writeSample := func(at time.Time, nodes, idle int, pricing any) {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"sampled_at": at, "nodes": nodes, "idle_nodes": idle, "logical_cpus_per_node": 2,
			"memory_mib_per_node": 3663, "gpus_per_node": 0, "pricing": pricing,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	call := func(task computePreviewTask, runtime json.RawMessage) (computePreviewResult, string) {
		t.Helper()
		args := map[string]any{"action": "preview", "task": task}
		if runtime != nil {
			args["agent_runtime"] = runtime
		}
		resp, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: computeToolName, Arguments: args})
		if err != nil || resp.IsError {
			t.Fatalf("preview: result=%+v err=%v", resp, err)
		}
		var result computePreviewResult
		data, err := json.Marshal(resp.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.SchemaVersion != computePreviewVersion || len(result.Options) != 2 || len(resp.Content) != 2 {
			t.Fatalf("unexpected preview: %s, content=%v", data, resp.Content)
		}
		cloud := result.Options[1]
		if cloud.Quote.Max != nil || cloud.ETAS != nil {
			t.Fatalf("a preview must not promise an ETA or price cap: %+v", cloud)
		}
		var fallback computePreviewResult
		if err := json.Unmarshal([]byte(resp.Content[1].(*mcp.TextContent).Text), &fallback); err != nil || !reflect.DeepEqual(fallback, result) {
			t.Fatalf("invalid textual JSON fallback: %+v, %v", fallback, err)
		}
		return result, resp.Content[0].(*mcp.TextContent).Text
	}
	shards := computePreviewTask{Topology: "independent_shards", Shards: 8, LogicalCPUs: 2, MemoryMiB: 1024}
	runtime := json.RawMessage(fmt.Sprintf(`{"scope":"agent_runtime","platform":"win32","cpuLogicalCount":16,"totalMemoryBytes":17179869184,"availableMemoryBytes":5798205849,"loadAverage1m":null,"sampledAt":%q,"nvidiaGpus":[{"name":"NVIDIA GeForce RTX 3060 Laptop GPU","totalMemoryMiB":6144,"freeMemoryMiB":4261}]}`, at.Format(time.RFC3339Nano)))
	result, _ := call(shards, nil)
	if result.Options[0].Fit != "unknown" || result.Options[1].Fit != "unknown" {
		t.Fatalf("missing samples must remain unknown: %+v", result.Options)
	}
	writeSample(at, 1, 1, nil)
	result, markdown := call(shards, runtime)
	if result.Options[0].Capacity != "available" || result.Options[1].Capacity != "available" ||
		result.Options[1].Quote.State != "unpriced" || result.Options[1].Quote.Estimate != nil ||
		!strings.Contains(markdown, "| 云集群 | 有足够空闲节点 | 待估算 | 待报价 | 待报价 |") {
		t.Fatalf("CPU preview: %+v\n%s", result.Options, markdown)
	}
	gpu := shards
	gpu.GPUCount, gpu.MemoryMiBPerGPU = 1, 2048
	result, _ = call(gpu, runtime)
	if result.Options[0].Capacity != "available" || result.Options[1].Fit != "does_not_fit" {
		t.Fatalf("GPU preview: %+v", result.Options)
	}
	result, _ = call(gpu, json.RawMessage(strings.Replace(string(runtime), `[{"name":"NVIDIA GeForce RTX 3060 Laptop GPU","totalMemoryMiB":6144,"freeMemoryMiB":4261}]`, "null", 1)))
	if result.Options[0].Fit != "unknown" {
		t.Fatalf("an unobserved GPU must remain unknown: %+v", result.Options[0])
	}
	distributed := computePreviewTask{Topology: "distributed", Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024}
	result, _ = call(distributed, runtime)
	if result.Options[0].Fit != "does_not_fit" || result.Options[1].Capacity != "needs_capacity" {
		t.Fatalf("two-node preview: %+v", result.Options)
	}
	writeSample(at, 2, 1, nil)
	result, _ = call(distributed, runtime)
	if result.Options[1].Capacity != "queued" {
		t.Fatalf("queue preview: %+v", result.Options)
	}
	rate := computeSlurmRate{CNYMicrosPerNode: 300_000, PeriodS: 3600, IncrementS: 60, ValidUntil: at.Add(time.Hour)}
	writeSample(at.Add(-3*time.Minute), 2, 2, rate)
	result, _ = call(distributed, runtime)
	if result.Options[1].Fit != "unknown" || result.Options[1].Reason != "slurm_sample_stale" || result.Options[1].Quote.Rate != nil {
		t.Fatalf("stale sample: %+v", result.Options[1])
	}
	writeSample(at, 1, 1, rate)
	result, markdown = call(shards, runtime)
	quote := result.Options[1].Quote
	if quote.State != "rate_only" || quote.Reason != "task_billable_time_missing" || quote.Estimate != nil ||
		quote.UnitsPerCredit == nil || *quote.UnitsPerCredit != 1_000_000 || quote.Rate == nil ||
		quote.Rate.UnitsPerNodePeriod != 30_000_000 || quote.Rate.PeriodS != 3600 || quote.Rate.IncrementS != 60 ||
		!strings.Contains(markdown, "每节点每小时 30 积分") {
		t.Fatalf("rate-only preview: %+v\n%s", quote, markdown)
	}
	shards.EstimatedBillableS = 601
	result, markdown = call(shards, runtime)
	quote = result.Options[1].Quote
	if quote.State != "estimated" || quote.Estimate == nil || *quote.Estimate != 44_000_000 || quote.BilledS != 660 ||
		!strings.Contains(markdown, "| 云集群 | 有足够空闲节点 | 待估算 | 约 44 积分 | 待正式报价 |") ||
		!strings.Contains(markdown, "每分片计费 660 秒") {
		t.Fatalf("shard estimate: %+v\n%s", quote, markdown)
	}
	distributed.EstimatedBillableS = 1800
	result, markdown = call(distributed, runtime)
	quote = result.Options[1].Quote
	if result.Options[1].Capacity != "needs_capacity" || quote.State != "estimated" || quote.Estimate == nil ||
		*quote.Estimate != 30_000_000 || quote.BilledS != 1800 || !strings.Contains(markdown, "每节点计费 1800 秒") {
		t.Fatalf("distributed estimate: %+v\n%s", result.Options[1], markdown)
	}
	smallRate := computeSlurmRate{CNYMicrosPerNode: 1, PeriodS: 3, IncrementS: 1, ValidUntil: rate.ValidUntil}
	writeSample(at, 1, 1, smallRate)
	single := shards
	single.Shards, single.EstimatedBillableS = 1, 1
	result, markdown = call(single, runtime)
	quote = result.Options[1].Quote
	if quote.Estimate == nil || *quote.Estimate != 34 || !strings.Contains(markdown, "约 0.000034 积分") {
		t.Fatalf("fractional credit must round up at ledger precision: %+v\n%s", quote, markdown)
	}
	writeSample(at, 1, 1, rate)
	shards.EstimatedBillableS = maxComputeJSONInteger - 1000
	result, _ = call(shards, runtime)
	if quote = result.Options[1].Quote; quote.State != "rate_only" || quote.Reason != "estimate_out_of_range" || quote.Estimate != nil {
		t.Fatalf("out-of-range estimate: %+v", quote)
	}
	for name, invalidRate := range map[string]any{
		"missing":        nil,
		"malformed":      "unknown",
		"missing period": computeSlurmRate{CNYMicrosPerNode: 286_550, IncrementS: 60, ValidUntil: rate.ValidUntil},
		"expired":        computeSlurmRate{CNYMicrosPerNode: 300_000, PeriodS: 3600, IncrementS: 60, ValidUntil: at},
	} {
		t.Run(name, func(t *testing.T) {
			writeSample(at, 2, 2, invalidRate)
			result, _ := call(distributed, runtime)
			cloud := result.Options[1]
			if cloud.Capacity != "available" || cloud.Quote.State != "unpriced" || cloud.Quote.Estimate != nil || cloud.Quote.Rate != nil {
				t.Fatalf("invalid pricing must preserve capacity and leave cost unknown: %+v", cloud)
			}
		})
	}
	for _, invalid := range []computePreviewTask{
		{Topology: "distributed", LogicalCPUs: 2, MemoryMiB: 1024},
		{Topology: "distributed", Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, EstimatedBillableS: -1},
	} {
		bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: computeToolName, Arguments: map[string]any{"action": "preview", "task": invalid}})
		if err != nil || !bad.IsError {
			t.Fatalf("invalid task unexpectedly accepted: %+v, %v", bad, err)
		}
	}
}

func previewCatalogFixture(now time.Time) []computeTaskSKU {
	valid := now.Add(time.Hour)
	return []computeTaskSKU{
		{ID: "c8e.large.2", LogicalCPUs: 2, MemoryMiB: 3584, Tariff: computeSlurmRate{CNYMicrosPerNode: 300_000, PeriodS: 3600, IncrementS: 60, ValidUntil: valid}},
		{ID: "s8e.2xlarge.2", LogicalCPUs: 8, MemoryMiB: 32_768, Tariff: computeSlurmRate{CNYMicrosPerNode: 1_010_000, PeriodS: 3600, IncrementS: 60, ValidUntil: valid}},
	}
}

// A zero-resident pool has no idle nodes to sample, so preview must still answer
// with the published shapes and tariffs instead of reporting an unusable tool.
func TestComputePreviewCatalogFallback(t *testing.T) {
	now := time.Now().UTC()
	catalog := previewCatalogFixture(now)
	preview := func(task computePreviewTask) (computePreviewOption, string) {
		t.Helper()
		_, structured, err := runComputePreview("", computePreviewArgs{Task: task}, catalog, 2, now)
		if err != nil || structured == nil {
			t.Fatalf("preview: %v", err)
		}
		result := structured.(computePreviewResult)
		if len(result.Options) != 2 || result.SchemaVersion != computePreviewVersion {
			t.Fatalf("unexpected preview shape: %+v", result)
		}
		return result.Options[1], computePreviewMarkdown(result)
	}

	shards := computePreviewTask{Topology: "independent_shards", Shards: 1, LogicalCPUs: 2, MemoryMiB: 1024, EstimatedBillableS: 600}
	cloud, markdown := preview(shards)
	if cloud.Source != "platform_catalog" || cloud.Fit != "fits" || cloud.Capacity != "unknown" ||
		cloud.Reason != "slurm_capacity_on_demand" || cloud.Catalog == nil || cloud.Catalog.SKU != "c8e.large.2" ||
		!cloud.Catalog.OnDemand || cloud.Slurm != nil || cloud.SampledAt != nil || cloud.ETAS != nil || cloud.Quote.Max != nil {
		t.Fatalf("catalog preview must stay unpromised: %+v", cloud)
	}
	if cloud.Quote.State != "estimated" || cloud.Quote.Estimate == nil || *cloud.Quote.Estimate != 5_000_000 || cloud.Quote.BilledS != 600 ||
		!strings.Contains(markdown, "按需开通，容量待确认") || !strings.Contains(markdown, "约 5 积分") ||
		!strings.Contains(markdown, "按需开通 c8e.large.2") {
		t.Fatalf("catalog estimate: %+v\n%s", cloud.Quote, markdown)
	}

	bigger := shards
	bigger.LogicalCPUs, bigger.MemoryMiB = 4, 16_384
	if cloud, _ = preview(bigger); cloud.Catalog == nil || cloud.Catalog.SKU != "s8e.2xlarge.2" || cloud.Fit != "fits" {
		t.Fatalf("smallest fitting shape: %+v", cloud)
	}

	oversized := shards
	oversized.LogicalCPUs = 16
	cloud, _ = preview(oversized)
	if cloud.Fit != "does_not_fit" || cloud.Reason != "no_published_shape" || cloud.Catalog != nil || cloud.Quote.Rate != nil {
		t.Fatalf("oversized shape: %+v", cloud)
	}

	many := shards
	many.Topology, many.Shards, many.Nodes = "distributed", 0, 3
	if cloud, _ = preview(many); cloud.Fit != "does_not_fit" || cloud.Reason != "nodes_exceed_catalog_limit" {
		t.Fatalf("node limit: %+v", cloud)
	}

	gpu := shards
	gpu.GPUCount, gpu.MemoryMiBPerGPU = 1, 8192
	if cloud, _ = preview(gpu); cloud.Fit != "does_not_fit" || cloud.Reason != "no_published_shape" {
		t.Fatalf("GPU shape: %+v", cloud)
	}
}

// The unified tool must answer preview without CATSCOMPANY_COMPUTE_SLURM_SNAPSHOT_FILE.
func TestComputePreviewWithoutSnapshotPath(t *testing.T) {
	const uid = 441
	now := time.Now().UTC()
	handler := NewServiceAgentsMCPHandler(newFakeServiceAgentStore())
	handler.SetComputeTasks(&ComputeTaskHandler{
		allowed: map[int64]bool{uid: true}, catalog: previewCatalogFixture(now),
	})
	ts := httptest.NewServer(mcpAuthStub(uid, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	resp, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      computeToolName,
		Arguments: map[string]any{"action": "preview", "task": map[string]any{"topology": "independent_shards", "shards": 1, "logical_cpus": 2, "memory_mib": 1024}},
	})
	if err != nil || resp.IsError {
		t.Fatalf("preview without snapshot: result=%+v err=%v", resp, err)
	}
	var result computePreviewResult
	data, err := json.Marshal(resp.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	cloud := result.Options[1]
	if cloud.Source != "platform_catalog" || cloud.Catalog == nil || cloud.Catalog.SKU != "c8e.large.2" ||
		cloud.Capacity != "unknown" || cloud.ETAS != nil {
		t.Fatalf("unexpected catalog preview through MCP: %+v", cloud)
	}
}
