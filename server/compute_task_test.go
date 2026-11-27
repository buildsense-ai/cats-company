package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openchat/openchat/server/store"
)

type computeSettlementStore struct {
	store.ComputeTaskStore
	run      *store.ComputeTaskRun
	charged  int64
	finished bool
}

func (s *computeSettlementStore) GetComputeTaskForWorker(context.Context, string) (*store.ComputeTaskRun, error) {
	return s.run, nil
}

func (s *computeSettlementStore) FinishComputeTask(_ context.Context, _, _ string, _ store.ComputeTaskProgress, charge int64, success bool, _ time.Time) error {
	s.charged, s.finished = charge, success
	return nil
}

func TestComputeWorkerRequiresSavedReleaseBeforeSettlement(t *testing.T) {
	id := "hpc_" + strings.Repeat("1", 32)
	lease := "hpc_" + strings.Repeat("2", 32)
	confirmed := time.Now().UTC().Add(-time.Hour)
	started := confirmed.Add(time.Minute)
	released := started.Add(20 * time.Minute)
	st := &computeSettlementStore{run: &store.ComputeTaskRun{
		ConfirmedAt: &confirmed,
		ComputeTaskQuote: store.ComputeTaskQuote{
			Spec:     store.ComputeTaskSpec{Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, WallSeconds: 60},
			Rate:     store.ComputeTaskRate{UnitsPerNodePeriod: 30_000_000, PeriodSeconds: 3600, IncrementSeconds: 60},
			MaxUnits: 5_000_000, MaxNodeSeconds: 600,
		},
	}}
	handler := &ComputeTaskHandler{store: st, workerToken: strings.Repeat("t", 32)}
	progress := store.ComputeTaskProgress{
		Phase: "released", QueueID: "pt-owned", QueueCreateOperation: "job-create", QueueDeleteOperation: "job-delete",
		ExpandOperation: "job-expand", ShrinkOperation: "job-shrink", ProvisionStartedAt: &started, ReleasedAt: &released,
		NodeIDs: []string{"nd-a", "nd-b"}, NodeNames: []string{"wn1", "wn2"}, InstanceIDs: []string{"ecs-a", "ecs-b"}, VolumeIDs: []string{"evs-a", "evs-b"},
		SlurmJobID: "12", SlurmState: "COMPLETED", SlurmExitCode: "0:0", SlurmNodeList: []string{"wn2", "wn1"}, SlurmElapsed: 15, SlurmAllocatedCPUs: 4,
	}
	for _, tc := range []struct {
		name   string
		change func(*store.ComputeTaskProgress)
		saved  bool
		want   int
	}{
		{name: "unpersisted", want: http.StatusUnprocessableEntity},
		{name: "disk still present", change: func(p *store.ComputeTaskProgress) { p.ReleasedAt = nil }, saved: true, want: http.StatusUnprocessableEntity},
		{name: "unverified shrink", change: func(p *store.ComputeTaskProgress) { p.ShrinkOperation = "" }, saved: true, want: http.StatusUnprocessableEntity},
		{name: "different Slurm nodes", change: func(p *store.ComputeTaskProgress) { p.SlurmNodeList = []string{"wn1", "wn3"} }, saved: true, want: http.StatusUnprocessableEntity},
		{name: "settle at the confirmed cap", saved: true, want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := progress
			if tc.change != nil {
				tc.change(&p)
			}
			if tc.saved {
				st.run.Progress = p
			}
			body, err := json.Marshal(map[string]any{"action": "finish", "quote_id": id, "lease": lease, "progress": p})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, computeTaskWorkerRoute, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+handler.workerToken)
			response := httptest.NewRecorder()
			handler.serveWorker(response, req)
			if response.Code != tc.want {
				t.Fatalf("HTTP %d, want %d: %s", response.Code, tc.want, response.Body)
			}
			if tc.want == http.StatusNoContent && (!st.finished || st.charged != st.run.MaxUnits) {
				t.Fatalf("settlement: success=%v charged=%d", st.finished, st.charged)
			}
		})
	}
}

type computeTaskMCPStore struct {
	store.ComputeTaskStore
	uid   int64
	quote store.ComputeTaskQuote
}

func (s *computeTaskMCPStore) CreateComputeTaskQuote(_ context.Context, uid int64, quote store.ComputeTaskQuote) error {
	s.uid, s.quote = uid, quote
	return nil
}

func (s *computeTaskMCPStore) GetComputeTask(_ context.Context, uid int64, id string) (*store.ComputeTaskRun, error) {
	if uid != s.uid || id != s.quote.ID {
		return nil, store.ErrComputeTaskNotFound
	}
	return &store.ComputeTaskRun{ComputeTaskQuote: s.quote, State: "quoted"}, nil
}

func TestComputeTaskMCPQuoteAndStatus(t *testing.T) {
	const uid = 441
	st := &computeTaskMCPStore{}
	handler := NewServiceAgentsMCPHandler(newFakeServiceAgentStore())
	handler.SetComputeTasks(&ComputeTaskHandler{
		store: st, publicBaseURL: "https://cats.example", allowed: map[int64]bool{uid: true},
		maxWallSeconds: 3600,
		catalog: []computeTaskSKU{{
			ID: "c8e.large.2", LogicalCPUs: 2, MemoryMiB: 3584,
			Tariff: computeSlurmRate{CNYMicrosPerNode: 300_000, PeriodS: 3600, IncrementS: 60, ValidUntil: time.Now().Add(time.Hour)},
		}, {
			ID: "expired", LogicalCPUs: 16, MemoryMiB: 32_768,
			Tariff: computeSlurmRate{CNYMicrosPerNode: 300_000, PeriodS: 3600, IncrementS: 60, ValidUntil: time.Now().Add(-time.Hour)},
		}},
	})
	allowed := httptest.NewServer(mcpAuthStub(uid, handler))
	t.Cleanup(allowed.Close)
	other := httptest.NewServer(mcpAuthStub(uid+1, handler))
	t.Cleanup(other.Close)
	session := newMCPSession(t, allowed.URL)
	handler.NotifySubscriptionChanged(uid)
	names := testToolNames(t, session)
	otherNames := testToolNames(t, newMCPSession(t, other.URL))
	if !containsName(names, computeToolName) || containsName(otherNames, computeToolName) {
		t.Fatalf("unexpected unified tool visibility: allowed=%v other=%v", names, otherNames)
	}
	ctx := context.Background()
	directory, err := session.CallTool(ctx, &mcp.CallToolParams{Name: computeToolName, Arguments: map[string]any{"action": "catalog"}})
	if err != nil || directory.IsError {
		t.Fatalf("catalog: result=%+v err=%v", directory, err)
	}
	var catalog struct {
		SKUs []struct {
			ID   string                `json:"id"`
			Rate store.ComputeTaskRate `json:"rate"`
		} `json:"skus"`
		MaxNodesPerTask int `json:"max_nodes_per_task"`
		MaxWallSeconds  int `json:"max_wall_seconds"`
	}
	if err := json.Unmarshal([]byte(directory.Content[1].(*mcp.TextContent).Text), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.SKUs) != 1 || catalog.SKUs[0].ID != "c8e.large.2" || catalog.MaxNodesPerTask != 2 || catalog.MaxWallSeconds != 3600 || st.quote.ID != "" {
		t.Fatalf("invalid read-only catalog result: %+v", catalog)
	}
	// Callers describe the job shape only; the platform derives the billing ceiling and
	// ignores any caller-supplied value.
	spec := store.ComputeTaskSpec{Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, WallSeconds: 1800, MaxResourceSecondsPerNode: 60}
	invalidLong := spec
	invalidLong.WallSeconds = 3660
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: computeToolName, Arguments: map[string]any{"action": "quote", "task": invalidLong}})
	if err != nil || !invalid.IsError || st.quote.ID != "" {
		t.Fatalf("quote above configured limit: result=%+v err=%v", invalid, err)
	}
	quoted, err := session.CallTool(ctx, &mcp.CallToolParams{Name: computeToolName, Arguments: map[string]any{"action": "quote", "task": spec}})
	if err != nil || quoted.IsError {
		t.Fatalf("quote: result=%+v err=%v", quoted, err)
	}
	ceiling, ok := computeTaskBillingCeiling(spec.WallSeconds, spec.Nodes)
	oneNode, okNode := computeTaskCharge(catalog.SKUs[0].Rate, 1, ceiling)
	if !ok || !okNode || ceiling != 1800 || oneNode != 15_000_000 {
		t.Fatalf("derived ceiling %d one-node charge %d (%v/%v)", ceiling, oneNode, ok, okNode)
	}
	if st.uid != uid || st.quote.Spec.WallSeconds != spec.WallSeconds || st.quote.Spec.MaxResourceSecondsPerNode != int(ceiling) ||
		st.quote.MaxUnits != 2*oneNode || st.quote.MaxNodeSeconds != 2*ceiling || !computeTaskIDPattern.MatchString(st.quote.ID) {
		t.Fatalf("stored quote for %d: %+v", st.uid, st.quote)
	}
	if !strings.Contains(quoted.Content[0].(*mcp.TextContent).Text, "https://cats.example/compute/confirm?quote="+st.quote.ID) {
		t.Fatalf("missing confirmation URL: %+v", quoted.Content)
	}
	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: computeToolName, Arguments: map[string]any{"action": "status", "quote_id": st.quote.ID}})
	if err != nil || status.IsError || !strings.Contains(status.Content[0].(*mcp.TextContent).Text, "quoted") {
		t.Fatalf("status: result=%+v err=%v", status, err)
	}
}
func TestComputeTaskAdmissionPolicyFromEnv(t *testing.T) {
	for _, name := range []string{
		"CATSCOMPANY_COMPUTE_MAX_TOTAL_NODES",
		"CATSCOMPANY_COMPUTE_MAX_CONCURRENT_TASKS",
		"CATSCOMPANY_COMPUTE_MAX_USER_NODES",
		"CATSCOMPANY_COMPUTE_MAX_USER_TASKS",
	} {
		t.Setenv(name, "")
	}
	policy, err := computeTaskAdmissionPolicyFromEnv()
	if err != nil || !policy.Unlimited() {
		t.Fatalf("empty capacity settings must keep unlimited behavior: %#v %v", policy, err)
	}
	t.Setenv("CATSCOMPANY_COMPUTE_MAX_TOTAL_NODES", "8")
	t.Setenv("CATSCOMPANY_COMPUTE_MAX_CONCURRENT_TASKS", "3")
	t.Setenv("CATSCOMPANY_COMPUTE_MAX_USER_NODES", "2")
	t.Setenv("CATSCOMPANY_COMPUTE_MAX_USER_TASKS", "1")
	policy, err = computeTaskAdmissionPolicyFromEnv()
	if err != nil || policy != (store.ComputeTaskAdmissionPolicy{MaxTotalNodes: 8, MaxConcurrentTasks: 3, MaxUserNodes: 2, MaxUserTasks: 1}) {
		t.Fatalf("capacity settings must parse as a shared policy: %#v %v", policy, err)
	}
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"negative", "-1"},
		{"fraction", "1.5"},
		{"text", "many"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CATSCOMPANY_COMPUTE_MAX_TOTAL_NODES", tc.value)
			if _, err := computeTaskAdmissionPolicyFromEnv(); err == nil {
				t.Fatalf("invalid capacity setting %q must be rejected", tc.value)
			}
		})
	}
}

func TestComputeTaskMaxWallSecondsFromEnv(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		valid bool
	}{
		{"", 0, true}, {"0", 0, true}, {"7200", 7200, true},
		{"300.0", 0, false}, {"-60", 0, false}, {"301", 0, false},
	} {
		t.Setenv("CATSCOMPANY_COMPUTE_MAX_WALL_SECONDS", tc.value)
		got, err := computeTaskMaxWallSecondsFromEnv()
		if (err == nil) != tc.valid || got != tc.want {
			t.Errorf("setting %q: got %d, %v", tc.value, got, err)
		}
	}
	handler := &ComputeTaskHandler{}
	spec := store.ComputeTaskSpec{Nodes: 1, LogicalCPUs: 1, MemoryMiB: 256, WallSeconds: 7200}
	if !handler.acceptsAllocation(spec) {
		t.Fatal("unset operational limit rejected a two-hour task")
	}
	if ceiling, ok := computeTaskBillingCeiling(spec.WallSeconds, spec.Nodes); !ok || ceiling != 7200 {
		t.Fatalf("derived ceiling = %d, %v; want 7200", ceiling, ok)
	}
	handler.maxWallSeconds = 3600
	if handler.acceptsAllocation(spec) {
		t.Fatal("accepted a job above the configured operational limit")
	}
}

func TestComputeTaskWorkloadIsOptionalAndValidated(t *testing.T) {
	handler := &ComputeTaskHandler{}
	base := store.ComputeTaskSpec{Nodes: 1, LogicalCPUs: 1, MemoryMiB: 256, WallSeconds: 600}
	if !handler.acceptsAllocation(base) {
		t.Fatal("builtin job without workload must stay accepted")
	}
	good := base
	good.Workload = &store.ComputeTaskWorkload{Command: "foamRun -solver incompressibleFluid",
		Image: "docker://openfoam/openfoam12-paraview512:latest"}
	if !handler.acceptsAllocation(good) {
		t.Fatal("valid caller workload was rejected")
	}
	withDir := base
	withDir.Workload = &store.ComputeTaskWorkload{Command: "ls", WorkDir: "/home/galaxy/runs/case1"}
	if !handler.acceptsAllocation(withDir) {
		t.Fatal("absolute work directory was rejected")
	}
	for _, invalid := range []*store.ComputeTaskWorkload{
		{Command: "   "},
		{Command: strings.Repeat("a", 8193)},
		{Command: "true", Image: "docker://bad image"},
		{Command: "true", Image: "relative/path.sif"},
		{Command: "true", WorkDir: "relative/dir"},
	} {
		spec := base
		spec.Workload = invalid
		if handler.acceptsAllocation(spec) {
			t.Fatalf("accepted invalid workload %+v", invalid)
		}
	}
}
