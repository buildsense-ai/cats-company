package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func repairDescriptionAttemptFixture(t *testing.T) (*modelDescriptionTestStore, store.SkillHubPublicationRepairLease, publicationRepairModelConfig,
	publicationRepairModelPorts, publicationRepairValidationReportReader, func(context.Context, store.SkillHubPublicationRepairTask) error, *int) {
	t.Helper()
	base, lease, cfg, ports, calls := repairModelTestFixture(t)
	db := &modelDescriptionTestStore{repairModelTestStore: base}
	report := publicationReportFixture(db.task.Input.Receipt).Validation
	reports := repairMaterialReportReaderFunc(func(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		if r != db.task.Input.Receipt {
			t.Error("report receipt rebound")
		}
		return &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(r), Validation: snapshotPublicationRepairModelReport(report)}, nil
	})
	verify := func(ctx context.Context, task store.SkillHubPublicationRepairTask) error {
		db.mu.Lock()
		defer db.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if db.deny {
			return store.ErrSkillHubPublicationDenied
		}
		if !reflect.DeepEqual(task, db.task) {
			return store.ErrSkillHubPublicationConflict
		}
		return nil // explicitly synthetic source verifier
	}
	return db, lease, cfg, ports, reports, verify, calls
}

func TestPublicationRepairModelDescriptionAttemptRetainsAndRecovers(t *testing.T) {
	for _, mode := range []string{"success", "lost_output_commit_reply", "lost_draft_commit_reply", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
			if mode == "lost_output_commit_reply" {
				db.completeError = errors.New("private lost model completion")
			}
			if mode == "lost_draft_commit_reply" {
				db.writeError = errors.New("private lost draft commit")
			}
			first, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
			if err != nil || first == nil || first.ConfirmedAt != nil || db.task.State() != "waiting_confirmation" || *calls != 1 || db.proposals != 1 || db.commits != 1 {
				t.Fatal("first attempt", err, *calls, db.proposals)
			}
			if mode == "confirmed" {
				at := time.Now()
				db.draft.ConfirmedAt = &at
			}
			// Recovery MUST NOT ask for pending execution, prepare model material or
			// invoke a provider after the round finishes. These poison ports prove it.
			db.readHook = func(int) error {
				t.Error("recovery requested execution lease")
				return store.ErrSkillHubPublicationDenied
			}
			ports.VerifySource = func(context.Context, store.SkillHubPublicationRepairTask, store.SkillHubPublicationRepairLease) error {
				t.Error("recovery used execution verifier")
				return errors.New("forbidden")
			}
			ports.PrepareMaterial = func(context.Context, store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
				t.Error("recovery prepared model input")
				return publicationRepairModelMaterial{}, errors.New("forbidden")
			}
			ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
				t.Error("recovery invoked model")
				return store.SkillHubPublicationRepairModelOutput{}, errors.New("forbidden")
			}
			again, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
			if err != nil || again == nil || again.ConfirmationKey != first.ConfirmationKey || again.Input.Proposal.Text != first.Input.Proposal.Text ||
				(again.ConfirmedAt != nil) != (mode == "confirmed") || *calls != 1 || db.proposals != 1 || len(db.task.Rounds) != 1 {
				t.Fatal("restart recovery", err)
			}
		})
	}
}

func TestPublicationRepairModelDescriptionAttemptPendingRetainedOutput(t *testing.T) {
	db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
	if _, err := runPublicationRepairModel(context.Background(), db, 1, lease, cfg, ports); err != nil {
		t.Fatal(err)
	}
	draft, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
	if err != nil || draft == nil || *calls != 1 || db.proposals != 1 {
		t.Fatal("retained-output restart redispatched", err, *calls)
	}
}

func TestPublicationRepairModelDescriptionAttemptReportRecoveryDoesNotRedispatch(t *testing.T) {
	db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
	failed := repairMaterialReportReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		return nil, errors.New("private lost report read")
	})
	if out, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, failed, verify); out != nil || err == nil || *calls != 1 || db.proposals != 0 {
		t.Fatal("report failure did not preserve retained output", err)
	}
	if out, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify); err != nil || out == nil || *calls != 1 || db.proposals != 1 {
		t.Fatal("report read recovery redispatched model", err)
	}
}

func TestPublicationRepairModelDescriptionAttemptConcurrentCallers(t *testing.T) {
	db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *store.SkillHubPublicationDescriptionDraft, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, _ := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
			results <- out
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result != nil {
			succeeded++
		}
	}
	if succeeded == 0 || *calls != 1 || db.commits != 1 || len(db.task.Rounds) != 1 || db.task.State() != "waiting_confirmation" {
		t.Fatal("concurrent attempts duplicated dispatch or lost the draft", succeeded, *calls, db.commits)
	}
}

func TestPublicationRepairModelDescriptionAttemptStopsWithoutUnsafeContinuation(t *testing.T) {
	for _, mode := range []string{"provider_uncertain", "source_denied", "report_error", "changed_report", "wrong_receipt", "nil_report", "proposal_uncommitted", "proposal_nil", "authority_after_model", "authority_after_proposal", "source_after_report"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
			original := reports
			switch mode {
			case "provider_uncertain":
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					return store.SkillHubPublicationRepairModelOutput{}, errors.New("private provider URL/secret")
				}
			case "source_denied":
				verify = func(context.Context, store.SkillHubPublicationRepairTask) error {
					return store.ErrSkillHubPublicationDenied
				}
			case "report_error":
				reports = repairMaterialReportReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
					return nil, errors.New("private report error")
				})
			case "changed_report", "wrong_receipt", "nil_report":
				reports = repairMaterialReportReaderFunc(func(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
					out, err := original.Report(ctx, r)
					if mode == "nil_report" {
						return nil, nil
					}
					if mode == "changed_report" {
						out.Validation.ReportID = "changed"
					} else {
						out.Receipt.RequestHash = "changed"
					}
					return out, err
				})
			case "proposal_uncommitted":
				db.noCommit = true
				db.writeError = errors.New("private failed proposal")
			case "proposal_nil":
				db.nilReply = true
			case "authority_after_model":
				invoke := ports.InvokeOnce
				ports.InvokeOnce = func(ctx context.Context, r publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					out, err := invoke(ctx, r)
					db.deny = true
					return out, err
				}
			case "authority_after_proposal":
				db.proposeHook = func(*store.SkillHubPublicationDescriptionDraft) { db.deny = true }
			case "source_after_report":
				reports = repairMaterialReportReaderFunc(func(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
					out, err := original.Report(ctx, r)
					db.deny = true
					return out, err
				})
			}
			draft, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
			if err == nil || draft != nil || *calls > 1 || len(db.task.Rounds) != 1 || db.proposals > 1 {
				t.Fatal("unsafe continuation", err, *calls, db.proposals)
			}
			if mode == "provider_uncertain" {
				if !errors.Is(err, errPublicationRepairModelUncertain) {
					t.Fatal("private provider failure leaked", err)
				}
				if again, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify); again != nil || !errors.Is(err, errPublicationRepairModelUncertain) || *calls != 1 {
					t.Fatal("uncertain provider redispatched", err, *calls)
				}
			}
		})
	}
}

func TestPublicationRepairModelDescriptionAttemptRecoveryAuthorityAndBinding(t *testing.T) {
	for _, mode := range []string{"denied", "missing_journal", "changed_journal", "lease", "task", "report", "source", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
			first, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "denied":
				db.deny = true
			case "missing_journal":
				db.call = nil
			case "changed_journal":
				db.call.Output.Text = "changed"
			case "lease":
				lease.Generation++
			case "task":
				db.task.Input.TopicID = "grp_456"
			case "report":
				reports = repairMaterialReportReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
					return nil, errors.New("private")
				})
			case "source":
				verify = func(context.Context, store.SkillHubPublicationRepairTask) error {
					return store.ErrSkillHubPublicationStale
				}
			case "expiry":
				// Simulate time advancing by moving ALL retained fixture timestamps
				// together, never changing identities, hashes, rounds or authority.
				shift := -3 * time.Minute
				lease.Until = lease.Until.Add(shift)
				db.task.CreatedAt = db.task.CreatedAt.Add(shift)
				db.task.Rounds[0].Lease.Until = lease.Until
				db.call.CreatedAt = db.call.CreatedAt.Add(shift)
				db.call.DispatchLease.Until = lease.Until
				at := db.call.CompletedAt.Add(shift)
				db.call.CompletedAt = &at
				db.draft.CreatedAt = db.draft.CreatedAt.Add(shift)
			}
			draft, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, cfg, ports, reports, verify)
			if mode == "expiry" {
				if err != nil || draft == nil || draft.ConfirmationKey != first.ConfirmationKey {
					t.Fatal("expired finished lease blocked read-only recovery", err)
				}
			} else if err == nil || draft != nil {
				t.Fatal("unsafe recovery", err)
			}
			if *calls != 1 || db.proposals != 1 {
				t.Fatal("recovery repeated side effects")
			}
		})
	}
}

func TestPublicationRepairModelDescriptionAttemptRealHTTPComposition(t *testing.T) {
	var keys, invocations atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/users/1/key/reveal":
			keys.Add(1)
			io.WriteString(w, repairProviderKeyJSON)
		case "/anthropic/v1/messages":
			invocations.Add(1)
			io.WriteString(w, repairProviderAnswerJSON)
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	db, lease, _, ports, reports, verify, _ := repairDescriptionAttemptFixture(t)
	p := repairProviderTestClient(t, s)
	ports.InvokeOnce = p.invokeOnce
	db.writeError = errors.New("lost committed proposal response")
	first, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, p.config.Model, ports, reports, verify)
	if err != nil || first == nil || first.ConfirmedAt != nil {
		t.Fatal("HTTP model->unconfirmed draft", err)
	}
	ports.InvokeOnce = repairProviderTestClient(t, s).invokeOnce
	again, err := runPublicationRepairDescriptionAttempt(context.Background(), db, 1, lease, p.config.Model, ports, reports, verify)
	if err != nil || again == nil || again.ConfirmationKey != first.ConfirmationKey || keys.Load() != 1 || invocations.Load() != 1 || db.commits != 1 {
		t.Fatal("reconstructed HTTPS composition", err)
	}
}

func TestPublicationRepairModelDescriptionAttemptRejectsInvalidInput(t *testing.T) {
	for _, mode := range []string{"nil_db", "nil_reports", "nil_verifier", "nil_provider", "actor", "round", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
			var persistence publicationRepairDescriptionAttemptStore = db
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "nil_db":
				persistence = (*modelDescriptionTestStore)(nil)
			case "nil_reports":
				reports = nil
			case "nil_verifier":
				verify = nil
			case "nil_provider":
				ports.InvokeOnce = nil
			case "actor": // checked below
			case "round":
				lease.Round = 2
			case "cancelled":
				cancel()
			}
			actor := int64(1)
			if mode == "actor" {
				actor = 0
			}
			if out, err := runPublicationRepairDescriptionAttempt(ctx, persistence, actor, lease, cfg, ports, reports, verify); err == nil || out != nil || *calls != 0 || db.proposals != 0 {
				t.Fatal("invalid attempt reached work", err)
			}
		})
	}
}
