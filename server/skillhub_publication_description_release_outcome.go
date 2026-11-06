package server

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionReleaseOutcomePorts struct {
	ReadCloud func(context.Context, store.SkillHubPublicationDescriptionApplication,
		store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error)
}

// Internal evidence only: no JSON/browser projection, polling loop or state write.
type publicationDescriptionReleaseOutcome struct {
	observation store.SkillHubPublicationDescriptionReleaseObservation
	status      publicationDescriptionCloudReleaseStatus
}

func (o *publicationDescriptionReleaseOutcome) terminal() bool {
	return o != nil && (o.status.State == "published" || o.status.State == "publish_failed")
}

// Read the exact derived outcome without invoking the live continuation or
// authorization/admission flow. Bracket peer I/O with active original-publisher
// checks so a stale checkpoint/permit cannot become viewing authority.
func readConfirmedPublicationDescriptionReleaseOutcome(ctx context.Context, db store.SkillHubPublicationDescriptionReleaseObservationReader,
	actor int64, id string, ports publicationDescriptionReleaseOutcomePorts,
) (*publicationDescriptionReleaseOutcome, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || ports.ReadCloud == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	baseline, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if baseline == nil || baseline.ValidateFor(actor, id) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	// Freeze all slices/pointers before a custom adapter gets access to records.
	frozen := snapshotDescriptionReleaseObservation(*baseline)
	replayed := true
	permit := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: frozen.Intent.ApplicationID,
		EntryID: id, Target: frozen.Intent.Target, IntentHash: frozen.Intent.IdentityHash, PermitID: frozen.Permit.ID,
		AuthorizedAt: frozen.Permit.AuthorizedAt.UTC().Format(time.RFC3339Nano), Replayed: &replayed}
	app := snapshotPublicationDescriptionApplication(frozen.Continuation.Application)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	status, err := ports.ReadCloud(ctx, app, frozen.Intent, permit)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate alternate adapters as strictly as the HTTPS client. Marshal/decode
	// also freezes optional result/error pointers without trusting adapter memory.
	if status == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	copy, err := decodePublicationDescriptionReleaseStatus(raw)
	if err != nil || copy.ApplicationID != app.ID || copy.TaskID != app.Input.Receipt.TaskID ||
		copy.PermitID != permit.PermitID || copy.PublicationReceipt != publicationControlReceipt(app.Input.Receipt) || copy.Target != frozen.Intent.Target {
		return nil, store.ErrSkillHubPublicationConflict
	}
	current, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ValidateFor(actor, id) != nil || !reflect.DeepEqual(*current, frozen) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &publicationDescriptionReleaseOutcome{observation: frozen, status: *copy}, nil
}

func snapshotDescriptionReleaseObservation(o store.SkillHubPublicationDescriptionReleaseObservation) store.SkillHubPublicationDescriptionReleaseObservation {
	o.Continuation = snapshotDescriptionContinuation(o.Continuation)
	return o
}
