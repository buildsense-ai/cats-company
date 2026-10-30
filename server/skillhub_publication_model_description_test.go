package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Deliberately has no Confirm, candidate writer, publisher or model method of
// its own. The consumer accepts only the narrow description-store interface.
type modelDescriptionTestStore struct {
	*repairModelTestStore
	draft                          *store.SkillHubPublicationDescriptionDraft
	proposals, commits, draftReads int
	deny                           bool
	writeError                     error
	noCommit, nilReply             bool
	draftHook                      func(int) error
	proposeHook                    func(*store.SkillHubPublicationDescriptionDraft)
	argumentHook                   func(store.SkillHubPublicationDescriptionProposal)
}

func (s *modelDescriptionTestStore) GetSkillHubPublicationRepairExecutionTask(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease) (*store.SkillHubPublicationRepairTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.deny || actor != 1 {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if !validPublicationRepairModelTask(s.task, lease) || !lease.Until.After(time.Now()) {
		return nil, store.ErrSkillHubPublicationStale
	}
	task := snapshotPublicationRepairTask(s.task)
	return &task, nil
}
func (s *modelDescriptionTestStore) GetSkillHubPublicationRepairTask(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationRepairTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	task := snapshotPublicationRepairTask(s.task)
	return &task, nil // Historical reader does NOT grant current authority.
}
func (s *modelDescriptionTestStore) GetSkillHubPublicationDescription(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draftReads++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.draftHook != nil {
		if err := s.draftHook(s.draftReads); err != nil {
			return nil, err
		}
	}
	if s.deny || actor != 1 || id != s.task.Input.Receipt.Query.EntryID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if s.draft == nil {
		return nil, nil
	}
	d := snapshotPublicationModelDescription(*s.draft)
	return &d, nil
}
func (s *modelDescriptionTestStore) ProposeSkillHubPublicationDescription(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, p store.SkillHubPublicationDescriptionProposal) (*store.SkillHubPublicationDescriptionDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposals++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.deny || actor != 1 {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if s.noCommit {
		return nil, s.writeError
	}
	if s.draft != nil {
		if !reflect.DeepEqual(s.draft.Input.Proposal, p) {
			return nil, store.ErrSkillHubPublicationConflict
		}
	} else {
		if !validPublicationRepairModelTask(s.task, lease) || !lease.Until.After(time.Now()) {
			return nil, store.ErrSkillHubPublicationStale
		}
		rh, _ := lease.Input.IdentityHash()
		in := store.SkillHubPublicationDescriptionDraftInput{TaskID: lease.TaskID, TaskIdentityHash: s.task.IdentityHash, Round: lease.Round, RoundIdentityHash: rh, Evidence: lease.Input.Evidence, Proposal: p}
		key, err := in.IdentityHash()
		if err != nil {
			return nil, err
		}
		s.draft = &store.SkillHubPublicationDescriptionDraft{EntryID: lease.EntryID, Input: in, ConfirmationKey: key, CreatedAt: time.Now()}
		frozen := snapshotPublicationModelDescription(*s.draft)
		s.draft = &frozen
		s.task.Rounds[0].Result = &store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: lease.Input.Evidence}
		s.commits++
	}
	if s.proposeHook != nil {
		s.proposeHook(s.draft)
	}
	if s.writeError != nil {
		return nil, s.writeError
	}
	if s.nilReply {
		return nil, nil
	}
	if s.argumentHook != nil {
		s.argumentHook(p)
	}
	d := snapshotPublicationModelDescription(*s.draft)
	return &d, nil
}

func modelDescriptionFixture(t *testing.T, text string) (*modelDescriptionTestStore, store.SkillHubPublicationRepairLease, *publicationRepairModelResult, PublicationValidationReport, *int) {
	t.Helper()
	db, lease, cfg, ports, calls := repairModelTestFixture(t)
	invoke := ports.InvokeOnce
	ports.InvokeOnce = func(ctx context.Context, r publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
		_, err := invoke(ctx, r)
		return store.SkillHubPublicationRepairModelOutput{Text: text}, err
	}
	result, err := runPublicationRepairModel(context.Background(), db, 1, lease, cfg, ports)
	if err != nil {
		t.Fatal(err)
	}
	return &modelDescriptionTestStore{repairModelTestStore: db}, lease, result, publicationReportFixture(db.task.Input.Receipt).Validation, calls
}

func TestPublicationRepairModelDescriptionRetainsExactTextAndRecovers(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lost_commit_reply"}[lost], func(t *testing.T) {
			text := "  Read documents.\nReturn their contents.\t "
			db, lease, result, report, calls := modelDescriptionFixture(t, text)
			if lost {
				db.writeError = errors.New("PRIVATE lost commit response")
			}
			verify := func(context.Context, store.SkillHubPublicationRepairTask) error { return nil }
			first, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify)
			if err != nil || first == nil || first.Input.Proposal.Text != text || first.ConfirmedAt != nil || db.commits != 1 || db.proposals != 1 || *calls != 1 || db.task.State() != "waiting_confirmation" {
				t.Fatal("retain exact unconfirmed draft", err)
			}
			replay, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify)
			if err != nil || !reflect.DeepEqual(first, replay) || db.proposals != 1 || *calls != 1 || len(db.task.Rounds) != 1 {
				t.Fatal("replay wrote/recalled", err)
			}
			*first.Input.Proposal.EntryIndex = 9
			if *db.draft.Input.Proposal.EntryIndex != 0 || *replay.Input.Proposal.EntryIndex != 0 {
				t.Fatal("draft pointers shared")
			}
			now := time.Now()
			db.draft.ConfirmedAt = &now
			confirmed, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify)
			if err != nil || confirmed.ConfirmedAt == nil || !confirmed.ConfirmedAt.Equal(now) || db.proposals != 1 {
				t.Fatal("human confirmation not preserved", err)
			}
		})
	}
}

func TestPublicationRepairModelDescriptionUncertainWritesNeverRetry(t *testing.T) {
	for _, mode := range []string{"no_commit", "nil_success", "recovery_denied", "recovery_source_lost", "cancelled_write", "changed_reply"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, result, report, calls := modelDescriptionFixture(t, "Read documents.")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			verifyCount := 0
			verify := func(context.Context, store.SkillHubPublicationRepairTask) error {
				verifyCount++
				if mode == "recovery_source_lost" && verifyCount > 1 {
					return store.ErrSkillHubPublicationDenied
				}
				return nil
			}
			switch mode {
			case "no_commit":
				db.noCommit = true
				db.writeError = errors.New("PRIVATE uncommitted")
			case "nil_success":
				db.nilReply = true
			case "recovery_denied":
				db.proposeHook = func(*store.SkillHubPublicationDescriptionDraft) { db.deny = true }
			case "cancelled_write":
				db.proposeHook = func(*store.SkillHubPublicationDescriptionDraft) { cancel() }
			case "changed_reply":
				db.draftHook = func(n int) error {
					if n == 2 {
						db.draft.Input.Proposal.Text = "changed"
						db.draft.ConfirmationKey, _ = db.draft.Input.IdentityHash()
					}
					return nil
				}
			}
			got, err := retainPublicationModelDescription(ctx, db, 1, lease, result, report, verify)
			if got != nil || !errors.Is(err, errPublicationModelDescriptionUncertain) || strings.Contains(err.Error(), "PRIVATE") || db.proposals != 1 || *calls != 1 {
				t.Fatal("uncertain write retried/leaked", err, db.proposals)
			}
		})
	}
}

func TestPublicationRepairModelDescriptionRejectsChangedEvidence(t *testing.T) {
	for _, mode := range []string{"actor", "journal_missing", "journal_error", "journal_changed", "journal_output", "report", "source", "task", "expired", "cancelled", "nil_result", "nil_source", "later_round"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, result, report, calls := modelDescriptionFixture(t, "Read documents.")
			actor := int64(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			verify := func(context.Context, store.SkillHubPublicationRepairTask) error { return nil }
			switch mode {
			case "actor":
				actor = 2
			case "journal_missing":
				db.call = nil
			case "journal_error":
				db.historyError = errors.New("PRIVATE journal")
			case "journal_changed":
				db.call.CallID = "pubmodel_" + strings.Repeat("f", 32)
			case "journal_output":
				db.call.Output.Text = "other"
				db.call.OutputHash = db.call.Output.Hash()
			case "report":
				report.Issues[0].Location.Field = "name"
			case "source":
				verify = func(context.Context, store.SkillHubPublicationRepairTask) error {
					return store.ErrSkillHubPublicationDenied
				}
			case "task":
				db.task.IdentityHash = strings.Repeat("f", 64)
			case "expired":
				lease.Until = time.Now().Add(-time.Second)
			case "cancelled":
				cancel()
			case "nil_result":
				result = nil
			case "nil_source":
				verify = nil
			case "later_round":
				lease.Round = 2
			}
			got, err := retainPublicationModelDescription(ctx, db, actor, lease, result, report, verify)
			if got != nil || err == nil || strings.Contains(err.Error(), "PRIVATE") || db.proposals != 0 || *calls != 1 {
				t.Fatal("changed evidence reached write", err)
			}
		})
	}
}

func TestPublicationRepairModelDescriptionSelectorAndText(t *testing.T) {
	db, _, _, _, _ := modelDescriptionFixture(t, "Read documents.")
	base := publicationReportFixture(db.task.Input.Receipt).Validation
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"exact", " \n Read docs.\t ", true}, {"max", strings.Repeat("x", 8192), true}, {"blank", " \t\n", false}, {"oversize", strings.Repeat("x", 8193), false}, {"utf8", string([]byte{255}), false}, {"nul", "read\x00doc", false}, {"control", "read\x7fdoc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := publicationModelDescriptionProposal(base, tc.text)
			if (err == nil) != tc.valid || (err == nil && p.Text != tc.text) {
				t.Fatal("text normalized or wrongly accepted", err)
			}
		})
	}
	for _, mode := range []string{"duplicate", "distinct", "json", "json_index", "md_no_index", "path", "field", "manual", "safety_warning", "no_blocking"} {
		t.Run(mode, func(t *testing.T) {
			r := snapshotPublicationRepairModelReport(base)
			valid := false
			switch mode {
			case "duplicate":
				r.Issues = append(r.Issues, r.Issues[0])
				valid = true
			case "distinct":
				other := r.Issues[0]
				index := int64(1)
				other.Location.EntryIndex = &index
				r.Issues = append(r.Issues, other)
			case "json":
				r.Issues[0].Location.RelativePath = "skill.json"
				r.Issues[0].Location.EntryIndex = nil
				valid = true
			case "json_index":
				r.Issues[0].Location.RelativePath = "skill.json"
			case "md_no_index":
				r.Issues[0].Location.EntryIndex = nil
			case "path":
				r.Issues[0].Location.RelativePath = "../SKILL.md"
			case "field":
				r.Issues[0].Location.Field = "name"
			case "manual":
				r.Issues[0].RepairPolicy = "manual"
			case "safety_warning":
				other := r.Issues[0]
				other.Category = "package_safety"
				other.Blocking = false
				r.Issues = append(r.Issues, other)
			case "no_blocking":
				r.Issues[0].Blocking = false
			}
			_, err := publicationModelDescriptionProposal(r, "Read documents.")
			if (err == nil) != valid {
				t.Fatal("selector/policy mismatch", err)
			}
		})
	}
}

func TestPublicationRepairModelDescriptionChecksAuthorityOnRecovery(t *testing.T) {
	// An existing draft is read once before source verification and again
	// afterwards. Each checkpoint must be reached, not silently skipped.
	for boundary := 1; boundary <= 2; boundary++ {
		t.Run(time.Duration(boundary).String(), func(t *testing.T) {
			db, lease, result, report, _ := modelDescriptionFixture(t, "Read documents.")
			verify := func(context.Context, store.SkillHubPublicationRepairTask) error { return nil }
			if _, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify); err != nil {
				t.Fatal(err)
			}
			db.draftReads = 0
			db.draftHook = func(n int) error {
				if n == boundary {
					return store.ErrSkillHubPublicationDenied
				}
				return nil
			}
			got, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify)
			if db.draftReads != boundary {
				t.Fatalf("authority checkpoint %d was not the stopping read (draft reads = %d)", boundary, db.draftReads)
			}
			if got != nil || !errors.Is(err, store.ErrSkillHubPublicationDenied) {
				t.Fatal("authority loss returned draft")
			}
			if db.proposals != 1 {
				t.Fatal("recovery retried proposal")
			}
		})
	}
}

func TestPublicationRepairModelDescriptionAuthorityRevokedDuringRecoveryVerification(t *testing.T) {
	db, lease, result, report, calls := modelDescriptionFixture(t, "Read documents.")
	verify := func(context.Context, store.SkillHubPublicationRepairTask) error { return nil }
	if _, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify); err != nil {
		t.Fatal(err)
	}
	db.draftReads = 0
	verified := 0
	verify = func(context.Context, store.SkillHubPublicationRepairTask) error {
		verified++
		// The historical task/model readers still succeed; only the final
		// current-authority draft GET can observe this revocation.
		db.mu.Lock()
		db.deny = true
		db.mu.Unlock()
		return nil
	}
	got, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify)
	if got != nil || !errors.Is(err, store.ErrSkillHubPublicationDenied) || verified != 1 || db.draftReads != 2 {
		t.Fatalf("revocation during verification not checked: draft=%v err=%v verifications=%d reads=%d", got != nil, err, verified, db.draftReads)
	}
	if db.proposals != 1 || db.commits != 1 || *calls != 1 || len(db.task.Rounds) != 1 {
		t.Fatal("revoked recovery repeated model/proposal/round")
	}
}

func TestPublicationRepairModelDescriptionPortPointerIsolation(t *testing.T) {
	db, lease, result, report, _ := modelDescriptionFixture(t, "Read documents.")
	db.argumentHook = func(p store.SkillHubPublicationDescriptionProposal) { *p.EntryIndex = 77 }
	verify := func(_ context.Context, task store.SkillHubPublicationRepairTask) error {
		task.Rounds[0].Lease.Token = "mutated"
		if task.Rounds[0].Result != nil {
			task.Rounds[0].Result.Outcome = "stopped"
		}
		*report.Issues[0].Location.EntryIndex = 99 // Consumer must freeze caller report.
		result.call.Output.Text = "mutated"        // Consumer must freeze caller result.
		return nil
	}
	got, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify)
	if err != nil || got == nil || got.Input.Proposal.Text != "Read documents." || *got.Input.Proposal.EntryIndex != 0 {
		t.Fatal("port mutated frozen proposal", err)
	}
}

func TestPublicationRepairModelDescriptionRejectsChangedRecovery(t *testing.T) {
	for _, mode := range []string{"text", "key", "selector", "task_identity", "task_created", "lease", "checkpoint", "journal", "source"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, result, report, calls := modelDescriptionFixture(t, "Read documents.")
			verify := func(context.Context, store.SkillHubPublicationRepairTask) error { return nil }
			if _, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "text":
				db.draft.Input.Proposal.Text = "changed"
				db.draft.ConfirmationKey, _ = db.draft.Input.IdentityHash()
			case "key":
				db.draft.ConfirmationKey = strings.Repeat("f", 64)
			case "selector":
				*db.draft.Input.Proposal.EntryIndex = 1
				db.draft.ConfirmationKey, _ = db.draft.Input.IdentityHash()
			case "task_identity":
				db.task.IdentityHash = strings.Repeat("f", 64)
			case "task_created":
				db.task.CreatedAt = time.Time{}
			case "lease":
				db.task.Rounds[0].Lease.Generation++
			case "checkpoint":
				db.task.Rounds[0].Result.Outcome = "validated"
			case "journal":
				db.call.Output.Text = "changed"
				db.call.OutputHash = db.call.Output.Hash()
			case "source":
				verify = func(context.Context, store.SkillHubPublicationRepairTask) error {
					return store.ErrSkillHubPublicationDenied
				}
			}
			if got, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, verify); got != nil || err == nil || db.proposals != 1 || *calls != 1 {
				t.Fatal("changed recovery accepted/rewrote", err)
			}
		})
	}
}

func TestPublicationRepairModelDescriptionTakeoverUsesOriginalOutput(t *testing.T) {
	db, lease, result, report, calls := modelDescriptionFixture(t, "Read documents.")
	lease.Generation++
	lease.WorkerID = "replacement-worker"
	lease.Token = strings.Repeat("b", 32)
	db.task.Rounds[0].Lease = lease
	got, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, func(context.Context, store.SkillHubPublicationRepairTask) error { return nil })
	if err != nil || got == nil || got.ConfirmedAt != nil || db.commits != 1 || *calls != 1 || db.call.DispatchLease.Generation != 1 {
		t.Fatal("takeover rebound/redispatched original model output", err)
	}
}

func TestPublicationRepairModelDescriptionConcurrentConsumers(t *testing.T) {
	db, lease, result, report, calls := modelDescriptionFixture(t, "Read documents.")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := retainPublicationModelDescription(context.Background(), db, 1, lease, result, report, func(context.Context, store.SkillHubPublicationRepairTask) error { return nil })
			if err == nil && (d == nil || d.ConfirmedAt != nil || d.Input.Proposal.Text != "Read documents.") {
				t.Error("concurrent consumer invalid success")
			}
		}()
	}
	wg.Wait()
	if db.commits != 1 || *calls != 1 || len(db.task.Rounds) != 1 {
		t.Fatal("duplicate draft/model/round", db.commits, *calls)
	}
}
