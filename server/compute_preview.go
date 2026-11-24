package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	computePreviewToolName = "catsco_compute_preview"
	computePreviewVersion  = "catsco.compute-preview.v1"
	maxComputeSnapshotSize = 64 << 10
)

// SetComputeSnapshotPath enables the read-only tool at startup. The snapshot
// comes from an operator-controlled Slurm collector on a local filesystem.
func (h *ServiceAgentsMCPHandler) SetComputeSnapshotPath(path string) {
	if filepath.IsAbs(path) {
		h.computeSnapshotPath = path
	}
}

type computePreviewTask struct {
	Topology           string `json:"topology" jsonschema:"independent_shards 或 distributed"`
	Shards             int    `json:"shards,omitempty" jsonschema:"独立分片数，至少 1"`
	Nodes              int    `json:"nodes,omitempty" jsonschema:"分布式任务要求的独立节点数，至少 1"`
	LogicalCPUs        int    `json:"logical_cpus" jsonschema:"单分片或单节点最低逻辑 CPU 数"`
	MemoryMiB          int    `json:"memory_mib" jsonschema:"单分片或单节点最低内存 MiB"`
	GPUCount           int    `json:"gpu_count,omitempty" jsonschema:"单分片或单节点最低 GPU 数，无 GPU 需求填 0"`
	MemoryMiBPerGPU    int    `json:"memory_mib_per_gpu,omitempty" jsonschema:"每块 GPU 所需的最低显存 MiB"`
	EstimatedBillableS int64  `json:"estimated_billable_s,omitempty" jsonschema:"预计云端计费秒数。独立任务填每分片，分布式任务填每节点。根据实测或用户提供的时长填写，无依据则省略"`
}

func (task computePreviewTask) valid() bool {
	validTopology := task.Topology == "independent_shards" && task.Shards > 0 && task.Nodes == 0 ||
		task.Topology == "distributed" && task.Nodes > 0 && task.Shards == 0
	return validTopology && task.LogicalCPUs > 0 && task.MemoryMiB > 0 && task.GPUCount >= 0 &&
		task.MemoryMiBPerGPU >= 0 && (task.MemoryMiBPerGPU == 0 || task.GPUCount > 0) && task.EstimatedBillableS >= 0
}

type computeRuntimeGPU struct {
	Name           string `json:"name,omitempty"`
	TotalMemoryMiB *int   `json:"totalMemoryMiB"`
	FreeMemoryMiB  *int   `json:"freeMemoryMiB"`
}

type computeRuntimeSample struct {
	Scope                string              `json:"scope"`
	Platform             string              `json:"platform,omitempty"`
	LoadAverage1m        *float64            `json:"loadAverage1m,omitempty"`
	SampledAt            time.Time           `json:"sampledAt"`
	CPULogicalCount      int                 `json:"cpuLogicalCount"`
	TotalMemoryBytes     *int64              `json:"totalMemoryBytes"`
	AvailableMemoryBytes *int64              `json:"availableMemoryBytes"`
	NvidiaGPUs           []computeRuntimeGPU `json:"nvidiaGpus"`
}

type computeSlurmSample struct {
	SampledAt       time.Time `json:"sampled_at"`
	Nodes           *int      `json:"nodes"`
	IdleNodes       *int      `json:"idle_nodes"`
	CPUsPerNode     int       `json:"logical_cpus_per_node"`
	MemoryMiB       int       `json:"memory_mib_per_node"`
	GPUsPerNode     *int      `json:"gpus_per_node"`
	MemoryMiBPerGPU *int      `json:"memory_mib_per_gpu"`
	rate            *computeSlurmRate
}

type computePreviewArgs struct {
	Task    computePreviewTask    `json:"task"`
	Runtime *computeRuntimeSample `json:"agent_runtime,omitempty" jsonschema:"从 local_compute_resources 复制的当前 Agent 设备采样字段。没有采样则省略"`
}

type computePreviewQuote struct {
	State          string              `json:"state"`
	Estimate       *int64              `json:"estimate_units"`
	Max            *int64              `json:"max_units"`
	UnitsPerCredit *int64              `json:"units_per_credit"`
	Rate           *computePreviewRate `json:"rate,omitempty"`
	BilledS        int64               `json:"billed_s_per_work_unit,omitempty"`
	Reason         string              `json:"reason_code,omitempty"`
}

type computePreviewOption struct {
	ID        string                `json:"option_id"`
	Fit       string                `json:"fit"`
	Capacity  string                `json:"capacity"`
	Reason    string                `json:"reason_code"`
	Source    string                `json:"source"`
	SampledAt *time.Time            `json:"sampled_at"`
	Runtime   *computeRuntimeSample `json:"agent_runtime,omitempty"`
	Slurm     *computeSlurmSample   `json:"slurm,omitempty"`
	ETAS      *[2]int               `json:"eta_s"`
	Quote     computePreviewQuote   `json:"quote"`
}

type computePreviewResult struct {
	SchemaVersion string                 `json:"schema_version"`
	Task          computePreviewTask     `json:"task"`
	Options       []computePreviewOption `json:"options"`
}

func freshComputeSample(sample, now time.Time, maxAge time.Duration) bool {
	return !sample.IsZero() && !sample.After(now) && now.Sub(sample) <= maxAge
}

func previewAgentRuntime(task computePreviewTask, sample *computeRuntimeSample, now time.Time) computePreviewOption {
	zero, unitsPerCredit := int64(0), commercialTopupUnitsPerFen
	option := computePreviewOption{ID: "agent_runtime", Source: "agent_reported", Fit: "unknown", Capacity: "unknown", Reason: "runtime_sample_missing", Quote: computePreviewQuote{State: "free", Estimate: &zero, Max: &zero, UnitsPerCredit: &unitsPerCredit}}
	if task.Nodes > 1 {
		option.Fit, option.Reason = "does_not_fit", "single_runtime_only"
		return option
	}
	if sample == nil || sample.Scope != "agent_runtime" {
		return option
	}
	option.SampledAt = &sample.SampledAt
	if !freshComputeSample(sample.SampledAt, now, 2*time.Minute) {
		option.Reason = "runtime_sample_stale"
		return option
	}
	option.Runtime = sample
	const bytesPerMiB = 1 << 20
	if sample.CPULogicalCount > 0 && sample.CPULogicalCount < task.LogicalCPUs ||
		sample.TotalMemoryBytes != nil && *sample.TotalMemoryBytes > 0 && *sample.TotalMemoryBytes/bytesPerMiB < int64(task.MemoryMiB) {
		option.Fit, option.Reason = "does_not_fit", "runtime_cpu_or_memory_insufficient"
		return option
	}
	if sample.CPULogicalCount <= 0 || sample.TotalMemoryBytes == nil || *sample.TotalMemoryBytes <= 0 {
		return option
	}
	if task.GPUCount > 0 {
		// The runtime probe covers NVIDIA only. Missing or unmatched devices
		// cannot rule out other accelerators, so keep their fit unknown.
		var total, free int
		for _, gpu := range sample.NvidiaGPUs {
			if task.MemoryMiBPerGPU == 0 || gpu.TotalMemoryMiB != nil && *gpu.TotalMemoryMiB >= task.MemoryMiBPerGPU {
				total++
				if task.MemoryMiBPerGPU > 0 && gpu.FreeMemoryMiB != nil && *gpu.FreeMemoryMiB >= task.MemoryMiBPerGPU {
					free++
				}
			}
		}
		if total < task.GPUCount {
			option.Reason = "runtime_gpu_unverified"
			return option
		}
		if free < task.GPUCount {
			option.Fit, option.Reason = "fits", "runtime_gpu_capacity_unverified"
			return option
		}
	}
	option.Fit, option.Reason = "fits", "runtime_memory_unverified"
	if sample.AvailableMemoryBytes != nil && *sample.AvailableMemoryBytes >= 0 {
		option.Capacity, option.Reason = "available", "runtime_sample_matches"
		if *sample.AvailableMemoryBytes/bytesPerMiB < int64(task.MemoryMiB) {
			option.Capacity, option.Reason = "insufficient", "runtime_memory_busy"
		}
	}
	return option
}

func readComputeSlurmSample(path string) *computeSlurmSample {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxComputeSnapshotSize {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxComputeSnapshotSize+1))
	if err != nil || len(data) > maxComputeSnapshotSize {
		return nil
	}
	var snapshot struct {
		computeSlurmSample
		Pricing json.RawMessage `json:"pricing"`
	}
	if json.Unmarshal(data, &snapshot) != nil {
		return nil
	}
	sample := &snapshot.computeSlurmSample
	if sample.Nodes == nil || sample.IdleNodes == nil ||
		*sample.Nodes < 0 || *sample.IdleNodes < 0 || *sample.IdleNodes > *sample.Nodes ||
		sample.CPUsPerNode <= 0 || sample.MemoryMiB <= 0 || sample.GPUsPerNode != nil && *sample.GPUsPerNode < 0 ||
		sample.MemoryMiBPerGPU != nil && *sample.MemoryMiBPerGPU <= 0 {
		return nil
	}
	var rate computeSlurmRate
	if json.Unmarshal(snapshot.Pricing, &rate) == nil {
		sample.rate = &rate
	}
	return sample
}

func previewSlurm(task computePreviewTask, sample *computeSlurmSample, now time.Time) computePreviewOption {
	option := computePreviewOption{ID: "slurm", Source: "platform_slurm_snapshot", Fit: "unknown", Capacity: "unknown", Reason: "slurm_sample_missing", Quote: computePreviewQuote{State: "unpriced"}}
	if sample == nil {
		return option
	}
	option.SampledAt = &sample.SampledAt
	if !freshComputeSample(sample.SampledAt, now, 2*time.Minute) {
		option.Reason = "slurm_sample_stale"
		return option
	}
	option.Slurm = sample
	if sample.CPUsPerNode < task.LogicalCPUs || sample.MemoryMiB < task.MemoryMiB ||
		sample.GPUsPerNode != nil && *sample.GPUsPerNode < task.GPUCount ||
		sample.MemoryMiBPerGPU != nil && *sample.MemoryMiBPerGPU < task.MemoryMiBPerGPU {
		option.Fit, option.Reason = "does_not_fit", "slurm_node_shape_insufficient"
		return option
	}
	if task.GPUCount > 0 && sample.GPUsPerNode == nil || task.MemoryMiBPerGPU > 0 && sample.MemoryMiBPerGPU == nil {
		option.Reason = "slurm_gpu_unverified"
		return option
	}
	needed := max(1, task.Nodes)
	option.Fit = "fits"
	option.Quote = estimateSlurmCredits(task, sample.rate, now)
	switch {
	case *sample.IdleNodes >= needed:
		option.Capacity, option.Reason = "available", "slurm_idle_nodes_available"
	case *sample.Nodes >= needed:
		option.Capacity, option.Reason = "queued", "slurm_idle_nodes_insufficient"
	default:
		option.Capacity, option.Reason = "needs_capacity", "slurm_nodes_insufficient"
	}
	return option
}

func computePreviewMarkdown(result computePreviewResult) string {
	labels := map[string]string{
		"runtime_sample_missing": "待采样", "runtime_sample_stale": "采样过期", "runtime_gpu_unverified": "GPU 待核对",
		"runtime_cpu_or_memory_insufficient": "CPU 或内存不足", "single_runtime_only": "需要多台节点",
		"runtime_memory_unverified": "可用内存待核对", "runtime_gpu_capacity_unverified": "可用显存待核对",
		"runtime_sample_matches": "采样资源匹配", "runtime_memory_busy": "可用内存不足",
		"slurm_sample_missing": "待采样", "slurm_sample_stale": "采样过期", "slurm_node_shape_insufficient": "当前队列节点规格不足",
		"slurm_gpu_unverified": "GPU 待核对", "slurm_idle_nodes_available": "有足够空闲节点", "slurm_idle_nodes_insufficient": "空闲节点不足，可能排队",
		"slurm_nodes_insufficient": "节点规格匹配，需补充节点",
	}
	var text strings.Builder
	text.WriteString("| 运行位置 | 资源与容量 | 预计完工 | 预计积分 | 费用上限 |\n| --- | --- | --- | --- | --- |\n")
	fmt.Fprintf(&text, "| 当前 Agent 设备 | %s | 待估算 | 0 算力积分 | 0 算力积分 |\n", labels[result.Options[0].Reason])
	cloud := result.Options[1]
	estimate, limit := "待报价", "待报价"
	if cloud.Quote.Rate != nil {
		estimate, limit = "待估算", "待正式报价"
	}
	if cloud.Quote.Estimate != nil {
		estimate = "约 " + computeCreditText(*cloud.Quote.Estimate) + " 积分"
	}
	fmt.Fprintf(&text, "| 云集群 | %s | 待估算 | %s | %s |\n", labels[cloud.Reason], estimate, limit)
	text.WriteString("\n当前设备的 0 积分仅指新增的 Cats 算力费。云端待正式报价和用户确认。")
	if rate := cloud.Quote.Rate; rate != nil {
		period := fmt.Sprintf("每 %d 秒", rate.PeriodS)
		if rate.PeriodS == 3600 {
			period = "每小时"
		}
		fmt.Fprintf(&text, " 云端试算费率为每节点%s %s 积分。", period, computeCreditText(rate.UnitsPerNodePeriod))
		if cloud.Quote.BilledS > 0 {
			workUnit := "节点"
			if result.Task.Topology == "independent_shards" {
				workUnit = "分片"
			}
			fmt.Fprintf(&text, " 本次按每%s计费 %d 秒试算。", workUnit, cloud.Quote.BilledS)
		}
	}
	if result.Options[0].Capacity == "available" {
		text.WriteString("\n\n要用当前 Agent 设备运行，还是等云端报价再决定？")
	}
	return text.String()
}

func (h *ServiceAgentsMCPHandler) addComputePreviewTool(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        computePreviewToolName,
		Description: "在资源密集型任务开始前，查询当前 Agent 设备与云集群的匹配、容量和预计积分。先用 local_compute_resources 采样当前运行设备，该设备可能在云端。若有实测或用户提供的时长，填 estimated_billable_s。用返回的 Markdown 表格向用户说明选项并询问偏好。预计积分由服务端试算，云端费用上限和付费确认另行处理。此工具只读。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, args computePreviewArgs) (*mcp.CallToolResult, any, error) {
		if !args.Task.valid() {
			return serviceToolError("invalid_compute_requirements"), nil, nil
		}
		slurm := readComputeSlurmSample(h.computeSnapshotPath)
		now := time.Now()
		result := computePreviewResult{
			SchemaVersion: computePreviewVersion,
			Task:          args.Task,
			Options:       []computePreviewOption{previewAgentRuntime(args.Task, args.Runtime, now), previewSlurm(args.Task, slurm, now)},
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: computePreviewMarkdown(result)},
			&mcp.TextContent{Text: string(data)},
		}}, result, nil
	})
}
