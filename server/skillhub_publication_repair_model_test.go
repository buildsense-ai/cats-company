package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type repairModelTestStore struct {
	mu                                      sync.Mutex
	task                                    store.SkillHubPublicationRepairTask
	call                                    *store.SkillHubPublicationRepairModelCall
	reads, starts, completes, histories     int
	readHook                                func(int) error
	startError, completeError, historyError error
	startHook                               func(*store.SkillHubPublicationRepairModelCall, *bool)
	completeHook                            func(*store.SkillHubPublicationRepairModelCall)
}

func (s *repairModelTestStore) GetSkillHubPublicationRepairExecutionTask(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease) (*store.SkillHubPublicationRepairTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.readHook != nil {
		if err := s.readHook(s.reads); err != nil {
			return nil, err
		}
	}
	task := snapshotPublicationRepairTask(s.task)
	return &task, nil
}
func (s *repairModelTestStore) StartSkillHubPublicationRepairModelCall(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, in store.SkillHubPublicationRepairModelCallInput) (*store.SkillHubPublicationRepairModelCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	created := s.call == nil
	if created {
		s.call = &store.SkillHubPublicationRepairModelCall{CallID: "pubmodel_" + strings.Repeat("e", 32), Input: in, DispatchLease: lease, CreatedAt: time.Now()}
	}
	if s.call.Input != in {
		return nil, false, store.ErrSkillHubPublicationConflict
	}
	if s.startError != nil {
		return nil, false, s.startError
	}
	c := snapshotPublicationRepairModelCall(*s.call)
	if s.startHook != nil {
		s.startHook(&c, &created)
	}
	return &c, created, nil
}
func (s *repairModelTestStore) CompleteSkillHubPublicationRepairModelCall(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, id string, output store.SkillHubPublicationRepairModelOutput) (*store.SkillHubPublicationRepairModelCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.call == nil || s.call.CallID != id {
		return nil, store.ErrSkillHubPublicationConflict
	}
	now := time.Now()
	s.call.Output = &output
	s.call.OutputHash = output.Hash()
	s.call.CompletedAt = &now
	if s.completeError != nil {
		return nil, s.completeError
	}
	c := snapshotPublicationRepairModelCall(*s.call)
	if s.completeHook != nil {
		s.completeHook(&c)
	}
	return &c, nil
}
func (s *repairModelTestStore) GetSkillHubPublicationRepairModelCall(context.Context, int64, string, int) (*store.SkillHubPublicationRepairModelCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histories++
	if s.historyError != nil {
		return nil, s.historyError
	}
	if s.call == nil {
		return nil, nil
	}
	c := snapshotPublicationRepairModelCall(*s.call)
	return &c, nil
}

func repairModelTestFixture(t *testing.T) (*repairModelTestStore, store.SkillHubPublicationRepairLease, publicationRepairModelConfig, publicationRepairModelPorts, *int) {
	t.Helper()
	app, _ := applicationTestFixture(t)
	in := store.SkillHubPublicationRepairInput{Receipt: app.app.Input.Receipt, TopicID: "grp_123"}
	report := publicationReportFixture(in.Receipt).Validation
	evidence, err := publicationRepairEvidence(report, in.Receipt.Query.SourceSnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	in.Evidence = evidence
	hash, _ := in.IdentityHash()
	now := time.Now()
	lease := store.SkillHubPublicationRepairLease{EntryID: in.Receipt.Query.EntryID, TaskID: "pubrepair_" + strings.Repeat("d", 32), Round: 1,
		Input: store.SkillHubPublicationRepairRoundInput{RequestID: "repair-model-1", Evidence: evidence}, WorkerID: "model-worker", Token: strings.Repeat("a", 32), Generation: 1, Until: now.Add(time.Minute * 2)}
	db := &repairModelTestStore{task: store.SkillHubPublicationRepairTask{ID: lease.TaskID, Input: in, IdentityHash: hash, CreatedAt: now.Add(-time.Second),
		Rounds: []store.SkillHubPublicationRepairRound{{Lease: lease}}}}
	calls := new(int)
	ports := publicationRepairModelPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationRepairTask) error { return nil },
		PrepareMaterial: func(context.Context, store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
			return publicationRepairModelMaterial{SourceText: "# Document reader\nRead document contents.", Report: report}, nil
		},
		InvokeOnce: func(ctx context.Context, r publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
			*calls++
			if r.PayerUID != 1 || r.Policy != publicationRepairModelPolicy || r.System != publicationRepairModelSystem {
				t.Error("request changed payer/policy")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 61*time.Second {
				t.Error("provider request unbounded")
			}
			return store.SkillHubPublicationRepairModelOutput{Text: "Read a document and return its contents."}, nil
		},
	}
	return db, lease, publicationRepairModelConfig{Provider: "test-provider", Model: "test-model", MaxOutputTokens: 2048}, ports, calls
}

func TestPublicationRepairModelRecoveryAndRequestBinding(t *testing.T) {
	db, lease, config, ports, calls := repairModelTestFixture(t)
	db.completeError = errors.New("lost completion reply PRIVATE")
	first, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
	if err != nil || first == nil || *calls != 1 || db.histories != 1 {
		t.Fatal("recover committed completion", err, *calls)
	}
	db.completeError = nil
	replay, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
	if err != nil || replay == nil || !reflect.DeepEqual(first, replay) || *calls != 1 || db.completes != 1 {
		t.Fatal("replay reissued provider", err, *calls)
	}
	first.call.Output.Text = "caller mutation"
	if replay.call.Output.Text == first.call.Output.Text || db.call.Output.Text == first.call.Output.Text {
		t.Fatal("output pointers shared")
	}
	for _, change := range []string{"provider", "model", "tokens", "source", "report_prose", "payer"} {
		t.Run(change, func(t *testing.T) {
			cfg, changed, actor := config, ports, int64(1)
			switch change {
			case "provider":
				cfg.Provider = "another-provider"
			case "model":
				cfg.Model = "another-model"
			case "tokens":
				cfg.MaxOutputTokens++
			case "payer":
				actor = 2
			case "source", "report_prose":
				changed.PrepareMaterial = func(ctx context.Context, task store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
					m, err := ports.PrepareMaterial(ctx, task)
					m.Report = snapshotPublicationRepairModelReport(m.Report)
					if change == "source" {
						m.SourceText += "changed"
					} else {
						m.Report.Issues[0].Message = "changed display prose"
					}
					return m, err
				}
			}
			if result, err := runPublicationRepairModel(context.Background(), db, actor, lease, cfg, changed); err == nil || result != nil || *calls != 1 {
				t.Fatal("changed request dispatched", err)
			}
		})
	}
	encoded, _ := json.Marshal(replay)
	if string(encoded) != "{}" {
		t.Fatal("private model result serialized")
	}
}

func TestPublicationRepairModelUncertainNeverRepeats(t *testing.T) {
	for _, mode := range []string{"lost_start", "provider_error", "invalid_output", "oversize", "cancelled_provider", "lost_complete_and_read"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, config, ports, calls := repairModelTestFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "lost_start":
				db.startError = errors.New("PRIVATE lost start")
			case "provider_error", "invalid_output", "oversize", "cancelled_provider":
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					if mode == "cancelled_provider" {
						cancel()
					}
					if mode == "provider_error" {
						return store.SkillHubPublicationRepairModelOutput{}, errors.New("PRIVATE upstream response")
					}
					if mode == "oversize" {
						return store.SkillHubPublicationRepairModelOutput{Text: strings.Repeat("x", 65537)}, nil
					}
					return store.SkillHubPublicationRepairModelOutput{}, nil
				}
			case "lost_complete_and_read":
				db.completeError = errors.New("PRIVATE complete")
				db.historyError = errors.New("PRIVATE read")
			}
			result, err := runPublicationRepairModel(ctx, db, 1, lease, config, ports)
			if result != nil || !errors.Is(err, errPublicationRepairModelUncertain) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("uncertain leaked/succeeded", err)
			}
			db.startError = nil
			db.historyError = nil
			count := *calls
			_, _ = runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
			if *calls != count {
				t.Fatal("uncertain provider request reissued")
			}
		})
	}
}

func TestPublicationRepairModelRejectsUnsafeMaterialBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"empty", "oversize", "utf8", "nul", "report_hash", "truncated", "passed", "safety", "manual", "prepare_error", "source_error"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, config, ports, calls := repairModelTestFixture(t)
			prepare := ports.PrepareMaterial
			ports.PrepareMaterial = func(ctx context.Context, task store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
				m, _ := prepare(ctx, task)
				m.Report = snapshotPublicationRepairModelReport(m.Report)
				switch mode {
				case "empty":
					m.SourceText = ""
				case "oversize":
					m.SourceText = strings.Repeat("x", 1024*1024+1)
				case "utf8":
					m.SourceText = string([]byte{0xff})
				case "nul":
					m.SourceText = "x\x00y"
				case "report_hash":
					m.Report.ReportID = "22345678-1234-4123-8123-123456789abc"
				case "truncated":
					m.Report.Truncated = true
				case "passed":
					m.Report.Outcome = "passed"
					m.Report.NextAction = "none"
				case "safety":
					m.Report.Issues[0].Category = "package_safety"
				case "manual":
					m.Report.Issues[0].Code = "metadata.other"
					m.Report.Issues[0].RepairPolicy = "manual"
				case "prepare_error":
					return m, errors.New("PRIVATE preparer")
				}
				// Bind the report into the fixture for safety/manual cases so the
				// policy, not an incidental hash mismatch, has to refuse dispatch.
				return m, nil
			}
			if mode == "safety" || mode == "manual" {
				m, _ := ports.PrepareMaterial(context.Background(), db.task)
				e, _ := publicationRepairEvidence(m.Report, lease.Input.Evidence.CandidateHash)
				db.task.Input.Evidence = e
				db.task.IdentityHash, _ = db.task.Input.IdentityHash()
				lease.Input.Evidence = e
				db.task.Rounds[0].Lease = lease
			}
			if mode == "source_error" {
				ports.VerifySource = func(context.Context, store.SkillHubPublicationRepairTask) error {
					return store.ErrSkillHubPublicationDenied
				}
			}
			if r, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports); r != nil || err == nil || db.starts != 0 || *calls != 0 {
				t.Fatal("unsafe material invoked provider", err, db.starts, *calls)
			}
		})
	}
}

func TestPublicationRepairModelUnsupportedSelectorNeverDispatches(t *testing.T) {
	for _, mode := range []string{"distinct", "no_index", "field", "path"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, config, ports, calls := repairModelTestFixture(t)
			material, err := ports.PrepareMaterial(context.Background(), db.task)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "distinct":
				other := material.Report.Issues[0]
				index := int64(1)
				other.Location.EntryIndex = &index
				material.Report.Issues = append(material.Report.Issues, other)
			case "no_index":
				material.Report.Issues[0].Location.EntryIndex = nil
			case "field":
				material.Report.Issues[0].Location.Field = "name"
			case "path":
				material.Report.Issues[0].Location.RelativePath = "README.md"
			}
			evidence, err := publicationRepairEvidence(material.Report, lease.Input.Evidence.CandidateHash)
			if err != nil {
				t.Fatal("fixture must pass complete report validation", err)
			}
			lease.Input.Evidence = evidence
			db.task.Input.Evidence = evidence
			db.task.IdentityHash, _ = db.task.Input.IdentityHash()
			db.task.Rounds[0].Lease = lease
			ports.PrepareMaterial = func(context.Context, store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
				return material, nil
			}
			if got, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports); err == nil || got != nil || *calls != 0 || db.starts != 0 {
				t.Fatal("unsupported description selector consumed paid dispatch", err)
			}
		})
	}
}

func TestPublicationRepairModelAuthorityRecheckedAtEveryBoundary(t *testing.T) {
	for boundary := 1; boundary <= 12; boundary++ {
		t.Run(time.Duration(boundary).String(), func(t *testing.T) {
			db, lease, config, ports, calls := repairModelTestFixture(t)
			db.readHook = func(n int) error {
				if n == boundary {
					return store.ErrSkillHubPublicationDenied
				}
				return nil
			}
			result, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
			if db.reads < boundary {
				return
			}
			if err == nil || result != nil {
				t.Fatal("authority loss succeeded", boundary)
			}
			if boundary <= 7 && *calls != 0 {
				t.Fatal("provider before authority checkpoint", boundary, *calls)
			}
		})
	}
}

func TestPublicationRepairModelExactTaskAndLease(t *testing.T) {
	for _, mode := range []string{"task_id", "identity", "round_result", "task_receipt", "round_two", "no_round", "lease", "nil_port", "bad_config", "cancelled", "nearly_expired"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, config, ports, calls := repairModelTestFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "task_id":
				db.task.ID = "pubrepair_" + strings.Repeat("f", 32)
			case "identity":
				db.task.IdentityHash = strings.Repeat("f", 64)
			case "round_result":
				db.task.Rounds[0].Result = &store.SkillHubPublicationRepairResult{Outcome: "validated", Evidence: lease.Input.Evidence}
			case "task_receipt":
				db.task.Input.Receipt.Query.EntryID = "pubentry_" + strings.Repeat("f", 32)
			case "round_two":
				lease.Round = 2
			case "no_round":
				db.task.Rounds = nil
			case "lease":
				db.task.Rounds[0].Lease.Token = strings.Repeat("f", 32)
			case "nil_port":
				ports.InvokeOnce = nil
			case "bad_config":
				config.MaxOutputTokens = 8193
			case "cancelled":
				cancel()
			case "nearly_expired":
				lease.Until = time.Now().Add(time.Second)
			}
			if r, err := runPublicationRepairModel(ctx, db, 1, lease, config, ports); r != nil || err == nil || *calls != 0 || db.starts != 0 {
				t.Fatal("invalid scope dispatched", err)
			}
		})
	}
}

func TestPublicationRepairModelPortPointerIsolation(t *testing.T) {
	db, lease, config, ports, _ := repairModelTestFixture(t)
	verify := ports.VerifySource
	ports.VerifySource = func(ctx context.Context, task store.SkillHubPublicationRepairTask) error {
		task.Rounds[0].Lease.Input.RequestID = "mutated"
		return verify(ctx, task)
	}
	invoke := ports.InvokeOnce
	ports.InvokeOnce = func(ctx context.Context, r publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
		*r.Report.Issues[0].Location.EntryIndex = 99
		r.Report.Issues[0].Message = "mutated model request"
		return invoke(ctx, r)
	}
	if r, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports); err != nil || r == nil {
		t.Fatal("port mutated baseline", err)
	}
	if db.task.Rounds[0].Lease.Input.RequestID != lease.Input.RequestID {
		t.Fatal("task port mutated store")
	}
}

func TestPublicationRepairModelLeaseTakeoverRecoversWithoutInvoke(t *testing.T) {
	db, lease, config, ports, calls := repairModelTestFixture(t)
	first, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
	if err != nil {
		t.Fatal(err)
	}
	lease.WorkerID = "replacement"
	lease.Generation++
	lease.Token = strings.Repeat("b", 32)
	lease.Until = lease.Until.Add(time.Minute)
	db.task.Rounds[0].Lease = lease
	replay, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
	if err != nil || replay == nil || !reflect.DeepEqual(first, replay) || *calls != 1 {
		t.Fatal("takeover invoked/changed output", err)
	}
}

func TestPublicationRepairModelRefusesMalformedJournalEvidence(t *testing.T) {
	for _, stage := range []string{"start", "complete"} {
		for _, mode := range []string{"id", "entry", "task", "round", "input", "request_hash", "future_generation", "created", "partial_output"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				db, lease, config, ports, calls := repairModelTestFixture(t)
				mutate := func(c *store.SkillHubPublicationRepairModelCall) {
					switch mode {
					case "id":
						c.CallID = "wrong"
					case "entry":
						c.DispatchLease.EntryID = "pubentry_" + strings.Repeat("f", 32)
					case "task":
						c.DispatchLease.TaskID = "pubrepair_" + strings.Repeat("f", 32)
					case "round":
						c.DispatchLease.Round = 2
					case "input":
						c.DispatchLease.Input.RequestID = "different-request"
					case "request_hash":
						c.Input.RequestHash = strings.Repeat("f", 64)
					case "future_generation":
						c.DispatchLease.Generation++
					case "created":
						c.CreatedAt = db.task.CreatedAt.Add(-time.Second)
					case "partial_output":
						c.Output = &store.SkillHubPublicationRepairModelOutput{Text: "partial"}
						c.OutputHash = ""
					}
				}
				if stage == "start" {
					db.startHook = func(c *store.SkillHubPublicationRepairModelCall, _ *bool) { mutate(c) }
				} else {
					db.completeHook = mutate
				}
				if r, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports); r != nil || !errors.Is(err, errPublicationRepairModelUncertain) {
					t.Fatal("malformed journal accepted", err)
				}
				if stage == "start" && *calls != 0 {
					t.Fatal("malformed start invoked provider")
				}
			})
		}
	}
}

func TestPublicationRepairModelConcurrentDispatch(t *testing.T) {
	db, lease, config, ports, calls := repairModelTestFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	invoke := ports.InvokeOnce
	ports.InvokeOnce = func(ctx context.Context, r publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
		close(entered)
		<-release
		return invoke(ctx, r)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
		completed <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first provider not entered")
	}
	second, err := runPublicationRepairModel(context.Background(), db, 1, lease, config, ports)
	close(release)
	firstErr := <-completed
	if second != nil || !errors.Is(err, errPublicationRepairModelUncertain) || firstErr != nil || *calls != 1 {
		t.Fatal("concurrent replay dispatched", err, firstErr, *calls)
	}
}

func TestPublicationRepairModelRequestHashIncludesPrivateBytesAndScope(t *testing.T) {
	db, lease, config, ports, _ := repairModelTestFixture(t)
	m, _ := ports.PrepareMaterial(context.Background(), db.task)
	request := publicationRepairModelRequest{PayerUID: 1, Config: config, Policy: publicationRepairModelPolicy, System: publicationRepairModelSystem,
		SourceText: m.SourceText, Report: m.Report, Task: db.task.Input, TaskID: db.task.ID, TaskIdentityHash: db.task.IdentityHash, Round: lease.Input}
	original, err := publicationRepairModelRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*publicationRepairModelRequest){
		"payer":     func(r *publicationRepairModelRequest) { r.PayerUID++ },
		"provider":  func(r *publicationRepairModelRequest) { r.Config.Provider += "x" },
		"model":     func(r *publicationRepairModelRequest) { r.Config.Model += "x" },
		"tokens":    func(r *publicationRepairModelRequest) { r.Config.MaxOutputTokens++ },
		"policy":    func(r *publicationRepairModelRequest) { r.Policy += "x" },
		"system":    func(r *publicationRepairModelRequest) { r.System += "x" },
		"source":    func(r *publicationRepairModelRequest) { r.SourceText += "x" },
		"report":    func(r *publicationRepairModelRequest) { r.Report.Issues[0].Message += "x" },
		"topic":     func(r *publicationRepairModelRequest) { r.Task.TopicID = "grp_124" },
		"receipt":   func(r *publicationRepairModelRequest) { r.Task.Receipt.Query.Epoch++ },
		"task_id":   func(r *publicationRepairModelRequest) { r.TaskID += "x" },
		"task_hash": func(r *publicationRepairModelRequest) { r.TaskIdentityHash += "x" },
		"round":     func(r *publicationRepairModelRequest) { r.Round.RequestID += "x" },
	} {
		t.Run(name, func(t *testing.T) {
			r := request
			r.Report = snapshotPublicationRepairModelReport(r.Report)
			mutate(&r)
			if hash, err := publicationRepairModelRequestHash(r); err != nil || hash == original {
				t.Fatal("request hash omitted field", err)
			}
		})
	}
	raw, err := json.Marshal(request)
	if err != nil || strings.Contains(string(raw), m.SourceText) || strings.Contains(string(raw), publicationRepairModelSystem) {
		t.Fatal("private source/prompt serialized", err)
	}
}
