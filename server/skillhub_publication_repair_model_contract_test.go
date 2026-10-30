package server

import (
	"context"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

type repairModelLostCompletionStore struct {
	*postgres.Adapter
	lose bool
}

func (s *repairModelLostCompletionStore) CompleteSkillHubPublicationRepairModelCall(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, id string, output store.SkillHubPublicationRepairModelOutput) (*store.SkillHubPublicationRepairModelCall, error) {
	c, err := s.Adapter.CompleteSkillHubPublicationRepairModelCall(ctx, actor, lease, id, output)
	if err == nil && s.lose {
		s.lose = false
		return nil, errors.New("simulated response loss after real model output commit")
	}
	return c, err
}

// Extends mandatory real JWT/HTTPS/PostgreSQL confirmation/continuation tests.
// Runtime source, retained material and paid provider remain SYNTHETIC ports.
// This is database-backed coordinator evidence, not production/paid-model E2E.
func verifyRepairModelExecutionPostgres(t *testing.T, ctx context.Context, db *postgres.Adapter, actor int64, lease store.SkillHubPublicationRepairLease, receipt store.SkillHubPublicationCloudReceipt) *publicationRepairModelResult {
	t.Helper()
	calls := 0
	config := publicationRepairModelConfig{Provider: "synthetic-provider", Model: "synthetic-model", MaxOutputTokens: 2048}
	ports := publicationRepairModelPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationRepairTask) error { return nil },
		PrepareMaterial: func(_ context.Context, task store.SkillHubPublicationRepairTask) (publicationRepairModelMaterial, error) {
			if task.Input.Receipt != receipt {
				t.Error("material receipt changed")
			}
			return publicationRepairModelMaterial{SourceText: "Synthetic retained private document reader skill; no actual Runtime/files accessed.", Report: publicationReportFixture(receipt).Validation}, nil
		},
		InvokeOnce: func(_ context.Context, request publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
			calls++
			if request.PayerUID != actor || request.Task.Receipt != receipt || request.Round != lease.Input {
				t.Error("synthetic provider identity changed")
			}
			return store.SkillHubPublicationRepairModelOutput{Text: "Read a document and return its contents."}, nil
		},
	}
	loss := &repairModelLostCompletionStore{Adapter: db, lose: true}
	first, err := runPublicationRepairModel(ctx, loss, actor, lease, config, ports)
	if err != nil || first == nil || first.call.Output == nil || calls != 1 {
		t.Fatal("real model journal completion response loss", err, calls)
	}
	// Reconstructed coordinator (no process-local grant/cache); the real store
	// returns false for dispatch and the same private output, not a paid retry.
	again, err := runPublicationRepairModel(ctx, db, actor, lease, config, ports)
	if err != nil || again == nil || again.call.CallID != first.call.CallID || again.call.OutputHash != first.call.OutputHash || calls != 1 {
		t.Fatal("real model journal replay", err, calls)
	}
	config.Model = "changed-model"
	if got, err := runPublicationRepairModel(ctx, db, actor, lease, config, ports); err == nil || got != nil || calls != 1 {
		t.Fatal("real journal permitted request rebind", err)
	}
	task, err := db.GetSkillHubPublicationRepairExecutionTask(ctx, actor, lease)
	if err != nil || task == nil || len(task.Rounds) != 1 || task.State() != "running" {
		t.Fatal("model coordinator changed repair state/budget", err)
	}
	t.Log("real PostgreSQL repair model coordinator: current task authority, original payer/request binding, lost committed output response read-only recovery, reconstructed replay with one synthetic provider call, changed model denied; no real Runtime/model/file writes")
	return again
}

type modelDescriptionLostProposalStore struct {
	*postgres.Adapter
	proposals int
}

func (s *modelDescriptionLostProposalStore) ProposeSkillHubPublicationDescription(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, p store.SkillHubPublicationDescriptionProposal) (*store.SkillHubPublicationDescriptionDraft, error) {
	s.proposals++
	_, err := s.Adapter.ProposeSkillHubPublicationDescription(ctx, actor, lease, p)
	if err != nil {
		return nil, err
	}
	return nil, errors.New("simulated response loss after real unconfirmed draft commit")
}

// Uses the durable model result from the preceding real PostgreSQL journal
// contract. Source verification/material/provider remain explicitly synthetic.
func verifyModelDescriptionRetentionPostgres(t *testing.T, ctx context.Context, db *postgres.Adapter, actor int64, lease store.SkillHubPublicationRepairLease, receipt store.SkillHubPublicationCloudReceipt) *store.SkillHubPublicationDescriptionDraft {
	t.Helper()
	result := verifyRepairModelExecutionPostgres(t, ctx, db, actor, lease, receipt)
	verify := func(context.Context, store.SkillHubPublicationRepairTask) error { return nil }
	report := publicationReportFixture(receipt).Validation
	loss := &modelDescriptionLostProposalStore{Adapter: db}
	draft, err := retainPublicationModelDescription(ctx, loss, actor, lease, result, report, verify)
	if err != nil || draft == nil || draft.ConfirmedAt != nil || draft.Input.Proposal.Text != result.call.Output.Text || loss.proposals != 1 {
		t.Fatal("real draft retention lost response recovery", err, loss.proposals)
	}
	// Reconstruct against the real adapter. Its execution reader now correctly
	// refuses the finished round; only current-authority draft GET may recover.
	if task, err := db.GetSkillHubPublicationRepairExecutionTask(ctx, actor, lease); err == nil || task != nil {
		t.Fatal("finished draft round still executable")
	}
	// Rebuild ALL private replay input from durable evidence, not the previous
	// process's result pointer or a process-local dispatch/confirmation grant.
	journal, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, lease.EntryID, lease.Round)
	if err != nil || journal == nil {
		t.Fatal("reconstruct model journal", err)
	}
	task, err := db.GetSkillHubPublicationRepairTask(ctx, actor, lease.EntryID)
	if err != nil || task == nil || len(task.Rounds) != 1 {
		t.Fatal("reconstruct draft task", err)
	}
	rebuilt := &publicationRepairModelResult{call: snapshotPublicationRepairModelCall(*journal)}
	again, err := retainPublicationModelDescription(ctx, db, actor, task.Rounds[0].Lease, rebuilt, report, verify)
	if err != nil || again == nil || again.ConfirmationKey != draft.ConfirmationKey || again.Input.Proposal.Text != draft.Input.Proposal.Text || again.ConfirmedAt != nil {
		t.Fatal("real reconstructed draft consumer replay", err)
	}
	task, err = db.GetSkillHubPublicationRepairTask(ctx, actor, lease.EntryID)
	if err != nil || task == nil || len(task.Rounds) != 1 || task.State() != "waiting_confirmation" {
		t.Fatal("draft consumer reset budget/state", err)
	}
	t.Log("real PostgreSQL model-to-description consumer: exact retained model text, unconfirmed draft/checkpoint atomic commit, lost committed proposal response read-only recovery, reconstructed consumer replay after pending lease finishes; same one round, no confirmation/model retry/file write; source/provider synthetic")
	return again
}
