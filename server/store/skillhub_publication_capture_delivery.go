package store

import (
	"context"
	"time"
)

const SkillHubPublicationCaptureLeaseDuration = 40 * time.Second

// Capture retries are transport attempts, not the three format-repair rounds.
// No file content, upload bearer or model output belongs in this ledger.
type SkillHubPublicationCaptureLease struct {
	EntryID   string
	Binding   SkillHubPublicationDeliveryBinding
	Token     string
	Attempt   int
	ExpiresAt time.Time
}

func (l SkillHubPublicationCaptureLease) Validate() error {
	if !ValidSkillHubPublicationDeliveryEntryID(l.EntryID) || l.Binding.Validate() != nil ||
		!ValidSkillHubPublicationDeliveryToken(l.Token) || l.Attempt < 1 || l.Attempt > SkillHubPublicationDeliveryAttempts || l.ExpiresAt.IsZero() {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationCaptureDeliveryStore interface {
	ListSkillHubPublicationCaptureDeliveries(context.Context, SkillHubPublicationRuntimeSelector, int) ([]string, error)
	ClaimSkillHubPublicationCaptureDelivery(context.Context, string, SkillHubPublicationDeliveryBinding) (*SkillHubPublicationCaptureLease, error)
	// Outcomes: captured (requires durable evidence), retry, rejected.
	FinishSkillHubPublicationCaptureDelivery(context.Context, SkillHubPublicationCaptureLease, string) error
}
