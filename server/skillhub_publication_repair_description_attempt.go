package server

import (
	"context"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationRepairDescriptionAttemptStore interface {
	publicationRepairModelStore
	publicationModelDescriptionStore
}

// Internal first-round execution step, not a worker/route/tool or a grant to
// claim another round. Inputs come only from trusted stored admission/lease
// facts. Compose real source/material/provider ports explicitly; no env loader,
// production registration, automatic retry, confirmation, file write or publish.
//
// Existing draft recovery precedes pending execution: retaining a proposal
// finishes its round, so asking for an execution lease first would break restart
// recovery and could tempt a caller to spend a new round or call the model again.
func runPublicationRepairDescriptionAttempt(parent context.Context, db publicationRepairDescriptionAttemptStore, actor int64,
	lease store.SkillHubPublicationRepairLease, config publicationRepairModelConfig, ports publicationRepairModelPorts,
	reports publicationRepairValidationReportReader,
	verifyDescriptionSource func(context.Context, store.SkillHubPublicationRepairTask) error,
) (*store.SkillHubPublicationDescriptionDraft, error) {
	if nilPublicationRepairReader(db) || actor <= 0 || lease.Validate() != nil || lease.Round != 1 ||
		nilPublicationRepairReader(reports) || verifyDescriptionSource == nil ||
		ports.VerifySource == nil || ports.PrepareMaterial == nil || ports.InvokeOnce == nil ||
		!store.ValidSkillHubPublicationRepairWorker(config.Provider) || !store.ValidSkillHubPublicationRepairWorker(config.Model) ||
		config.MaxOutputTokens < 1 || config.MaxOutputTokens > 8192 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Unlike repair/model history, this read independently checks active original
	// human/Bot/body and private task membership even after a lease has finished.
	draft, err := db.GetSkillHubPublicationDescription(ctx, actor, lease.EntryID)
	if err != nil {
		return nil, err
	}
	var task store.SkillHubPublicationRepairTask
	var result *publicationRepairModelResult
	if draft != nil {
		frozen := snapshotPublicationModelDescription(*draft)
		current, err := db.GetSkillHubPublicationRepairTask(ctx, actor, lease.EntryID)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, store.ErrSkillHubPublicationConflict
		}
		task = snapshotPublicationRepairTask(*current)
		if !validPublicationRepairDraftSourceTask(task) || frozen.ValidateFor(task) != nil ||
			!samePublicationRepairModelLease(task.Rounds[0].Lease, lease) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if err := verifyDescriptionSource(ctx, snapshotPublicationRepairTask(task)); err != nil {
			return nil, err
		}
		// Rebuild private replay input from retained history, not process-local
		// output or any browser/model-supplied description. Consumer rechecks it.
		call, err := db.GetSkillHubPublicationRepairModelCall(ctx, actor, lease.EntryID, lease.Round)
		if err != nil || call == nil {
			return nil, errPublicationModelDescriptionUncertain
		}
		result = &publicationRepairModelResult{call: snapshotPublicationRepairModelCall(*call)}
	} else {
		current, err := db.GetSkillHubPublicationRepairExecutionTask(ctx, actor, lease)
		if err != nil {
			return nil, err
		}
		if current == nil || !validPublicationRepairModelTask(*current, lease) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		task = snapshotPublicationRepairTask(*current)
		result, err = runPublicationRepairModel(ctx, db, actor, lease, config, ports)
		if err != nil {
			return nil, err
		}
	}
	if err := verifyDescriptionSource(ctx, snapshotPublicationRepairTask(task)); err != nil {
		return nil, err
	}
	// Fetch the retained report again for the proposal consumer; never use a
	// model-chosen selector or trust an incidental mutable preparer callback.
	report, err := reports.Report(ctx, task.Input.Receipt)
	if err != nil || report == nil {
		return nil, errPublicationRepairMaterialPeer
	}
	r := snapshotPublicationRepairModelReport(report.Validation)
	evidence, err := publicationRepairEvidence(r, task.Input.Receipt.Query.SourceSnapshotHash)
	if err != nil || report.SchemaVersion != 1 || report.Receipt != publicationControlReceipt(task.Input.Receipt) || evidence != task.Input.Evidence {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := verifyDescriptionSource(ctx, snapshotPublicationRepairTask(task)); err != nil {
		return nil, err
	}
	retained, err := retainPublicationModelDescription(ctx, db, actor, lease, result, r, verifyDescriptionSource)
	if err != nil {
		return nil, err
	}
	if retained == nil {
		return nil, errPublicationModelDescriptionUncertain
	}
	// Return a private frozen value only after an independent current-authority
	// read. No assertion that waiting_confirmation means approval/publication.
	frozen := snapshotPublicationModelDescription(*retained)
	latest, err := db.GetSkillHubPublicationDescription(ctx, actor, lease.EntryID)
	if err != nil {
		return nil, err
	}
	if latest == nil || !reflect.DeepEqual(snapshotPublicationModelDescription(*latest), frozen) || ctx.Err() != nil {
		return nil, errPublicationModelDescriptionUncertain
	}
	return &frozen, nil
}
