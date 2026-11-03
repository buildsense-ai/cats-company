package server

import (
	"context"
	"errors"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationRepairFirstRoundControllerStore interface {
	publicationRepairDescriptionAttemptStore
	FinishSkillHubPublicationRepairRound(context.Context, int64, store.SkillHubPublicationRepairLease, store.SkillHubPublicationRepairResult) error
}

// runPublicationRepairFirstRoundAttempt adds only the durable stop checkpoint
// around the already-claimed, first-round description attempt. It is not a
// claimant, worker, route or production call site.
func runPublicationRepairFirstRoundAttempt(parent context.Context, db publicationRepairFirstRoundControllerStore, actor int64,
	lease store.SkillHubPublicationRepairLease, config publicationRepairModelConfig, ports publicationRepairModelPorts,
	reports publicationRepairValidationReportReader,
	verifyDescriptionSource func(context.Context, store.SkillHubPublicationRepairTask) error,
) (*store.SkillHubPublicationDescriptionDraft, error) {
	if nilPublicationRepairReader(db) || actor <= 0 || lease.Validate() != nil || lease.Round != 1 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	draft, err := runPublicationRepairDescriptionAttempt(parent, db, actor, lease, config, ports, reports, verifyDescriptionSource)
	if err == nil || !errors.Is(err, errPublicationRepairModelUncertain) {
		// In particular, a proposal-write uncertainty remains read-recoverable;
		// temporary source/report/store errors do not terminalize the task.
		return draft, err
	}
	result := store.SkillHubPublicationRepairResult{Outcome: "stopped", Code: "model_result_uncertain", Evidence: lease.Input.Evidence}
	if finishErr := db.FinishSkillHubPublicationRepairRound(parent, actor, lease, result); finishErr != nil {
		current, readErr := db.GetSkillHubPublicationRepairTask(parent, actor, lease.EntryID)
		if readErr != nil || current == nil || len(current.Rounds) != 1 ||
			!samePublicationRepairModelLease(current.Rounds[0].Lease, lease) || current.Rounds[0].Result == nil ||
			!reflect.DeepEqual(*current.Rounds[0].Result, result) || current.State() != "stopped" {
			return nil, errPublicationRepairModelUncertain
		}
		return nil, errPublicationRepairModelUncertainStopped
	}
	return nil, errPublicationRepairModelUncertainStopped
}
