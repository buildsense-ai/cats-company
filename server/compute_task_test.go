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
		catalog: []computeTaskSKU{{
			ID: "c8e.large.2", LogicalCPUs: 2, MemoryMiB: 3584,
			Tariff: computeSlurmRate{CNYMicrosPerNode: 300_000, PeriodS: 3600, IncrementS: 60, ValidUntil: time.Now().Add(time.Hour)},
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
	invalidSpec := map[string]any{"action": "quote", "task": store.ComputeTaskSpec{Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, WallSeconds: 60}}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: computeToolName, Arguments: invalidSpec})
	if err != nil || !invalid.IsError || !strings.Contains(invalid.Content[0].(*mcp.TextContent).Text, "invalid_compute_task") {
		t.Fatalf("missing resource cap must be rejected: result=%+v err=%v", invalid, err)
	}
	spec := store.ComputeTaskSpec{Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, WallSeconds: 60, MaxResourceSecondsPerNode: 600}
	quoted, err := session.CallTool(ctx, &mcp.CallToolParams{Name: computeToolName, Arguments: map[string]any{"action": "quote", "task": spec}})
	if err != nil || quoted.IsError {
		t.Fatalf("quote: result=%+v err=%v", quoted, err)
	}
	if st.uid != uid || st.quote.Spec != spec || st.quote.MaxUnits != 10_000_000 || st.quote.MaxNodeSeconds != 1200 || !computeTaskIDPattern.MatchString(st.quote.ID) {
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
