package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if !containsName(testToolNames(t, session), computePreviewToolName) {
		t.Fatal("compute preview missing from tools/list")
	}
	handler.NotifySubscriptionChanged(441)
	if !containsName(testToolNames(t, session), computePreviewToolName) {
		t.Fatal("subscription refresh removed compute preview")
	}
	writeSample := func(at time.Time, nodes, idle int) {
		t.Helper()
		data := fmt.Sprintf(`{"sampled_at":%q,"nodes":%d,"idle_nodes":%d,"logical_cpus_per_node":2,"memory_mib_per_node":3663,"gpus_per_node":0}`, at.Format(time.RFC3339Nano), nodes, idle)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	call := func(task computePreviewTask, runtime json.RawMessage) (computePreviewResult, string) {
		t.Helper()
		args := map[string]any{"task": task}
		if runtime != nil {
			args["agent_runtime"] = runtime
		}
		resp, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: computePreviewToolName, Arguments: args})
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
		if cloud.Quote.State != "unpriced" || cloud.Quote.Estimate != nil || cloud.Quote.Max != nil || cloud.ETAS != nil {
			t.Fatalf("preview used unverified timing or billing: %+v", cloud)
		}
		var fallback computePreviewResult
		if err := json.Unmarshal([]byte(resp.Content[1].(*mcp.TextContent).Text), &fallback); err != nil || fallback.SchemaVersion != result.SchemaVersion {
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
	writeSample(at, 1, 1)
	result, markdown := call(shards, runtime)
	if result.Options[0].Capacity != "available" || result.Options[1].Capacity != "available" ||
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
	writeSample(at, 2, 1)
	result, _ = call(distributed, runtime)
	if result.Options[1].Capacity != "queued" {
		t.Fatalf("queue preview: %+v", result.Options)
	}
	writeSample(at.Add(-3*time.Minute), 2, 2)
	result, _ = call(distributed, runtime)
	if result.Options[1].Fit != "unknown" || result.Options[1].Reason != "slurm_sample_stale" {
		t.Fatalf("stale sample: %+v", result.Options[1])
	}
	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: computePreviewToolName, Arguments: map[string]any{"task": computePreviewTask{Topology: "distributed", LogicalCPUs: 2, MemoryMiB: 1024}}})
	if err != nil || !bad.IsError {
		t.Fatalf("invalid task unexpectedly accepted: %+v, %v", bad, err)
	}
}
