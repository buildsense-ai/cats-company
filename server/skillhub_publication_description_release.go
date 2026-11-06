package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

// publicationDescriptionReleasePorts are trusted server-side adapters. They
// deliberately keep source verification, Cloud planning and Cloud permit
// authorization separate so a caller cannot turn a description confirmation
// into an implicit publication.
type publicationDescriptionReleasePorts struct {
	publicationDescriptionContinuationPorts
	PlanRetainedCopy func(context.Context, store.SkillHubPublicationDescriptionApplication, bool) (*publicationDescriptionReleasePlan, error)
	AuthorizeCloud   func(context.Context, store.SkillHubPublicationDescriptionApplication,
		store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error)
	AdmitCloud func(context.Context, store.SkillHubPublicationDescriptionApplication,
		store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseAdmission, error)
}

type publicationDescriptionReleaseStore interface {
	publicationDescriptionContinuationStore
	publicationDescriptionReleaseAuthorizeStore
}

var _ publicationDescriptionReleaseStore = (*postgres.Adapter)(nil)

// authorizeConfirmedPublicationDescriptionReleaseFlow is the private handoff
// coordinator for a separately confirmed derived release. The caller must
// have already durably recorded an explicit human release decision through
// RecordSkillHubPublicationDescriptionReleaseIntent; confirmation of the
// description draft alone is not sufficient. This remains an internal,
// default-off adapter: it does not register a route, worker, model tool or
// legacy commit path.
func authorizeConfirmedPublicationDescriptionReleaseFlow(ctx context.Context, db publicationDescriptionReleaseStore,
	actor int64, id string, ports publicationDescriptionReleasePorts,
) (*publicationDescriptionAuthorizedRelease, error) {
	if db == nil || ports.PlanRetainedCopy == nil || ports.AuthorizeCloud == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	continuation, err := continueConfirmedPublicationDescription(ctx, db, actor, id, ports.publicationDescriptionContinuationPorts)
	if err != nil {
		return nil, err
	}
	// Read and validate the durable human release intent before asking Cloud to
	// reserve a target. Its target is the sole source of version-policy truth;
	// no caller-supplied flag can cause a speculative Cloud reservation.
	intent, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if intent == nil || intent.ActorUID != actor || intent.ValidateFor(continuation.checkpoint) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	planned, err := planConfirmedPublicationDescription(ctx, db, actor, id, continuation, intent.Target.ConfirmVersionPublish, publicationDescriptionReleasePlanPorts{
		VerifySource:     ports.VerifySource,
		PlanRetainedCopy: ports.PlanRetainedCopy,
	})
	if err != nil {
		return nil, err
	}
	return authorizeConfirmedPublicationDescriptionRelease(ctx, db, actor, id, planned, publicationDescriptionReleaseAuthorizePorts{
		VerifySource:   ports.VerifySource,
		AuthorizeCloud: ports.AuthorizeCloud,
	})
}

type publicationDescriptionAdmittedRelease struct {
	authorized *publicationDescriptionAuthorizedRelease
	admission  publicationDescriptionCloudReleaseAdmission
}

// Admission is a distinct, irreversible handoff. It is reachable only after
// the actor's exact release intent and Cloud permit have both been verified.
// A valid replay may report a terminal state; callers must inspect State
// instead of interpreting a nil error as an in-progress publication.
func admitConfirmedPublicationDescriptionReleaseFlow(ctx context.Context, db publicationDescriptionReleaseStore,
	actor int64, id string, ports publicationDescriptionReleasePorts,
) (*publicationDescriptionAdmittedRelease, error) {
	if ports.AdmitCloud == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	authorized, err := authorizeConfirmedPublicationDescriptionReleaseFlow(ctx, db, actor, id, ports)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ports.VerifySource(ctx, snapshotPublicationDescriptionApplication(authorized.checkpoint.Application)); err != nil {
		return nil, err
	}
	current, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	intent, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Validate() != nil || !reflect.DeepEqual(*current, authorized.checkpoint) ||
		intent == nil || !reflect.DeepEqual(*intent, authorized.intent) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	admission, err := ports.AdmitCloud(ctx, snapshotPublicationDescriptionApplication(authorized.checkpoint.Application),
		authorized.intent, authorized.permit)
	if err != nil {
		return nil, err
	}
	if admission == nil || admission.SchemaVersion != 1 || admission.ApplicationID != authorized.intent.ApplicationID ||
		admission.TaskID != authorized.checkpoint.Application.Input.Receipt.TaskID || admission.PermitID != authorized.permit.PermitID ||
		admission.Replayed == nil || !oneOf(admission.State, "queued", "publishing", "committing", "published", "publish_failed") {
		return nil, store.ErrSkillHubPublicationConflict
	}
	admissionSnapshot := *admission
	replayed := *admission.Replayed
	admissionSnapshot.Replayed = &replayed
	return &publicationDescriptionAdmittedRelease{authorized: authorized, admission: admissionSnapshot}, nil
}
