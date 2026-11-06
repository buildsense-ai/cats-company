package store

import "context"

// Retained release evidence for the original active human publisher. Unlike a
// continuation, this historical view grants no current Bot/source authority.
type SkillHubPublicationDescriptionReleaseObservation struct {
	Continuation SkillHubPublicationDescriptionContinuation
	Intent       SkillHubPublicationDescriptionReleaseIntent
	Permit       SkillHubPublicationDescriptionReleasePermit
}

func (o SkillHubPublicationDescriptionReleaseObservation) ValidateFor(actor int64, id string) error {
	if actor <= 0 || !ValidSkillHubPublicationDeliveryEntryID(id) || o.Intent.ActorUID != actor || o.Intent.EntryID != id ||
		o.Intent.ValidateFor(o.Continuation) != nil || o.Permit.Validate() != nil || o.Permit.Intent != o.Intent {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

// Read-only, original-publisher-only evidence; never issues/replays a permit or
// requires a live Runtime. Implementations must check the active human account.
type SkillHubPublicationDescriptionReleaseObservationReader interface {
	GetSkillHubPublicationDescriptionReleaseObservation(context.Context, int64, string) (*SkillHubPublicationDescriptionReleaseObservation, error)
}
