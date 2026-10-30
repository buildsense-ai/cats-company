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
func verifyRepairModelExecutionPostgres(t *testing.T, ctx context.Context, db *postgres.Adapter, actor int64, lease store.SkillHubPublicationRepairLease, receipt store.SkillHubPublicationCloudReceipt) string {
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
	return again.call.Output.Text
}
