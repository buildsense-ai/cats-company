package server

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

var errPublicationModelDescriptionUncertain = errors.New("publication.model_description_uncertain")

// Narrow capabilities: no model dispatch, confirmation, round claim, candidate
// writer or release method. GetDescription independently checks CURRENT private
// task authority, unlike GetRepairTask/GetModelCall historical evidence readers.
type publicationModelDescriptionStore interface {
	store.SkillHubPublicationRepairExecutionReader
	GetSkillHubPublicationRepairTask(context.Context, int64, string) (*store.SkillHubPublicationRepairTask, error)
	GetSkillHubPublicationRepairModelCall(context.Context, int64, string, int) (*store.SkillHubPublicationRepairModelCall, error)
	GetSkillHubPublicationDescription(context.Context, int64, string) (*store.SkillHubPublicationDescriptionDraft, error)
	ProposeSkillHubPublicationDescription(context.Context, int64, store.SkillHubPublicationRepairLease, store.SkillHubPublicationDescriptionProposal) (*store.SkillHubPublicationDescriptionDraft, error)
}

func snapshotPublicationModelDescription(d store.SkillHubPublicationDescriptionDraft) store.SkillHubPublicationDescriptionDraft {
	if d.Input.Proposal.EntryIndex != nil {
		v := *d.Input.Proposal.EntryIndex
		d.Input.Proposal.EntryIndex = &v
	}
	if d.ConfirmedAt != nil {
		v := *d.ConfirmedAt
		d.ConfirmedAt = &v
	}
	return d
}

// The model returns TEXT ONLY. Neither path, field nor entry index is chosen by
// the model. All blocking issues must identify the same supported selector;
// multiple distinct missing descriptions fail closed, never pick the first.
func publicationModelDescriptionProposal(report PublicationValidationReport, text string) (store.SkillHubPublicationDescriptionProposal, error) {
	var proposal *store.SkillHubPublicationDescriptionProposal
	for _, issue := range report.Issues {
		if issue.Category == "package_safety" {
			return store.SkillHubPublicationDescriptionProposal{}, store.ErrSkillHubPublicationDenied
		}
		if !issue.Blocking {
			continue
		}
		if issue.Code != "metadata.description_missing" || issue.Category != "format" || issue.RepairPolicy != "confirmation_required" {
			return store.SkillHubPublicationDescriptionProposal{}, store.ErrSkillHubPublicationDenied
		}
		p := store.SkillHubPublicationDescriptionProposal{RelativePath: issue.Location.RelativePath, Field: issue.Location.Field, Text: text}
		if issue.Location.EntryIndex != nil {
			v := *issue.Location.EntryIndex
			p.EntryIndex = &v
		}
		if p.Validate() != nil {
			return store.SkillHubPublicationDescriptionProposal{}, store.ErrSkillHubPublicationInvalid
		}
		if proposal != nil && !reflect.DeepEqual(*proposal, p) {
			return store.SkillHubPublicationDescriptionProposal{}, store.ErrSkillHubPublicationDenied
		}
		proposal = &p
	}
	if proposal == nil {
		return store.SkillHubPublicationDescriptionProposal{}, store.ErrSkillHubPublicationInvalid
	}
	return *proposal, nil
}

// retainPublicationModelDescription consumes an already retained model result.
// No provider/model invocation or automatic confirmation belongs here. It only
// records exact text for the existing human confirmation UI. Internal/unwired.
func retainPublicationModelDescription(parent context.Context, db publicationModelDescriptionStore, actor int64, lease store.SkillHubPublicationRepairLease, result *publicationRepairModelResult, report PublicationValidationReport, verifySource func(context.Context, store.SkillHubPublicationRepairTask) error) (*store.SkillHubPublicationDescriptionDraft, error) {
	if db == nil || actor <= 0 || lease.Validate() != nil || lease.Round != 1 || result == nil || verifySource == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	call := snapshotPublicationRepairModelCall(result.call)
	if call.Validate() != nil || call.Output == nil || call.DispatchLease.EntryID != lease.EntryID || call.DispatchLease.TaskID != lease.TaskID ||
		call.DispatchLease.Round != lease.Round || call.DispatchLease.Input != lease.Input || call.DispatchLease.Generation > lease.Generation ||
		(call.DispatchLease.Generation == lease.Generation && !samePublicationRepairModelLease(call.DispatchLease, lease)) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	report = snapshotPublicationRepairModelReport(report)
	evidence, err := publicationRepairEvidence(report, lease.Input.Evidence.CandidateHash)
	if err != nil || evidence != lease.Input.Evidence {
		return nil, store.ErrSkillHubPublicationConflict
	}
	proposal, err := publicationModelDescriptionProposal(report, call.Output.Text)
	if err != nil {
		return nil, err
	}
	// Read the immutable journal again: an in-process result alone is not proof
	// that output was retained, and may not override changed store evidence.
	checkCall := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		stored, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, lease.EntryID, lease.Round)
		if err != nil {
			return store.ErrSkillHubPublicationDenied
		}
		if stored == nil || stored.Validate() != nil || !reflect.DeepEqual(*stored, call) {
			return store.ErrSkillHubPublicationConflict
		}
		return ctx.Err()
	}
	if err := checkCall(); err != nil {
		return nil, err
	}
	roundHash, _ := lease.Input.IdentityHash()
	// Existing proposal recovery must precede pending execution reads, because
	// successful Propose atomically finishes the round at waiting_confirmation.
	readDraft := func() (*store.SkillHubPublicationDescriptionDraft, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d, err := db.GetSkillHubPublicationDescription(ctx, actor, lease.EntryID)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, nil
		}
		frozen := snapshotPublicationModelDescription(*d)
		t, err := db.GetSkillHubPublicationRepairTask(ctx, actor, lease.EntryID)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, store.ErrSkillHubPublicationConflict
		}
		task := snapshotPublicationRepairTask(*t)
		expected := store.SkillHubPublicationDescriptionDraftInput{TaskID: lease.TaskID, TaskIdentityHash: task.IdentityHash, Round: lease.Round, RoundIdentityHash: roundHash, Evidence: lease.Input.Evidence, Proposal: proposal}
		pending := snapshotPublicationRepairTask(task)
		if len(pending.Rounds) == 1 {
			pending.Rounds[0].Result = nil
		}
		if !validPublicationRepairModelTask(pending, lease) || frozen.ValidateFor(task) != nil ||
			!reflect.DeepEqual(frozen.Input, expected) || call.CreatedAt.Before(task.CreatedAt) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if err := verifySource(ctx, snapshotPublicationRepairTask(task)); err != nil {
			return nil, err
		}
		if err := checkCall(); err != nil {
			return nil, err
		}
		// GetDescription is the current-authority gate, including recovery after
		// proposal response loss. Historical reads alone never authorize return.
		latest, err := db.GetSkillHubPublicationDescription(ctx, actor, lease.EntryID)
		if err != nil {
			return nil, err
		}
		if latest == nil || !reflect.DeepEqual(*latest, frozen) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &frozen, nil
	}
	if existing, err := readDraft(); err != nil || existing != nil {
		return existing, err
	}
	t, err := db.GetSkillHubPublicationRepairExecutionTask(ctx, actor, lease)
	if err != nil {
		return nil, err
	}
	if t == nil || !validPublicationRepairModelTask(*t, lease) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	task := snapshotPublicationRepairTask(*t)
	if call.CreatedAt.Before(task.CreatedAt) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := verifySource(ctx, snapshotPublicationRepairTask(task)); err != nil {
		return nil, err
	}
	if err := checkCall(); err != nil {
		return nil, err
	}
	current, err := db.GetSkillHubPublicationRepairExecutionTask(ctx, actor, lease)
	if err != nil {
		return nil, err
	}
	if current == nil || !reflect.DeepEqual(*current, task) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Propose rechecks current authority and lease transactionally. It creates
	// no human confirmation, writes no candidate and spends no new repair round.
	supplied := proposal
	if proposal.EntryIndex != nil {
		v := *proposal.EntryIndex
		supplied.EntryIndex = &v
	}
	returned, writeErr := db.ProposeSkillHubPublicationDescription(ctx, actor, lease, supplied)
	if writeErr == nil && returned == nil {
		return nil, errPublicationModelDescriptionUncertain
	}
	var returnedSnapshot store.SkillHubPublicationDescriptionDraft
	if returned != nil {
		returnedSnapshot = snapshotPublicationModelDescription(*returned)
	}
	// Even success is read back with source/current-authority checks. On an
	// uncertain write, ONLY read recovery; never re-propose or invoke the model.
	recovered, readErr := readDraft()
	if readErr != nil || recovered == nil {
		return nil, errPublicationModelDescriptionUncertain
	}
	if writeErr == nil && !reflect.DeepEqual(returnedSnapshot, *recovered) {
		return nil, errPublicationModelDescriptionUncertain
	}
	return recovered, nil
}
