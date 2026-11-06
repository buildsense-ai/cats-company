package store

import (
	"context"
	"time"
)

// Scheduling lease only, never permission to execute/retry a publication.
type SkillHubPublicationDescriptionReconcileLease struct {
	ActorUID      int64
	EntryID       string
	ApplicationID string
	Token         string
	Generation    int64
	ExpiresAt     time.Time
}

func (l SkillHubPublicationDescriptionReconcileLease) Validate() error {
	if l.ActorUID <= 0 || !ValidSkillHubPublicationDeliveryEntryID(l.EntryID) || !ValidSkillHubPublicationDescriptionApplicationID(l.ApplicationID) ||
		!ValidSkillHubPublicationDeliveryToken(l.Token) || l.Generation < 1 || l.ExpiresAt.IsZero() {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// Existing permits are the queue. No new intent, permit, task or candidate is
// admitted by this interface. Observation retries do not consume repair rounds.
type SkillHubPublicationDescriptionReconcileStore interface {
	SkillHubPublicationDescriptionReleaseTerminalStore
	ClaimSkillHubPublicationDescriptionReconcile(context.Context, SkillHubPublicationCancellationScope) (*SkillHubPublicationDescriptionReconcileLease, error)
	FinishSkillHubPublicationDescriptionReconcile(context.Context, SkillHubPublicationDescriptionReconcileLease, string) error
}
