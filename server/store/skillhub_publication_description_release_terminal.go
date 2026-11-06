package store

import (
	"context"
	"regexp"
	"time"
)

type SkillHubPublicationDescriptionReleaseResult struct {
	SkillID     string `json:"skillId"`
	Version     string `json:"version"`
	PackageID   string `json:"packageId"`
	Checksum    string `json:"checksumSha256"`
	PublishedAt string `json:"publishedAt"`
}

// Immutable terminal evidence only, not repair history or execution authority.
type SkillHubPublicationDescriptionReleaseTerminal struct {
	ApplicationID string                                       `json:"applicationId"`
	EntryID       string                                       `json:"entryId"`
	PermitID      string                                       `json:"permitId"`
	IntentHash    string                                       `json:"intentHash"`
	Target        SkillHubPublicationCommitTarget              `json:"target"`
	State         string                                       `json:"state"`
	Result        *SkillHubPublicationDescriptionReleaseResult `json:"result"`
	ErrorCode     string                                       `json:"errorCode"`
}

type SkillHubPublicationDescriptionReleaseTerminalReceipt struct {
	Terminal     SkillHubPublicationDescriptionReleaseTerminal
	IdentityHash string
	RecordedAt   time.Time
}

var descriptionReleasePackageID = regexp.MustCompile(`^pkg_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

func (t SkillHubPublicationDescriptionReleaseTerminal) Digest() (string, error) {
	if !ValidSkillHubPublicationDescriptionApplicationID(t.ApplicationID) || !ValidSkillHubPublicationDeliveryEntryID(t.EntryID) ||
		!publicationDescriptionReleasePermitID.MatchString(t.PermitID) || !publicationSHA256.MatchString(t.IntentHash) || t.Target.Validate() != nil {
		return "", ErrSkillHubPublicationInvalid
	}
	switch t.State {
	case "published":
		p := t.Result
		if p == nil || t.ErrorCode != "" || p.SkillID != t.Target.SkillID || p.Version != t.Target.Version || p.PublishedAt != t.Target.ReleaseAt ||
			!descriptionReleasePackageID.MatchString(p.PackageID) || !publicationSHA256.MatchString(p.Checksum) {
			return "", ErrSkillHubPublicationInvalid
		}
	case "publish_failed":
		if t.Result != nil {
			return "", ErrSkillHubPublicationInvalid
		}
		valid := false
		for _, s := range []string{"unavailable", "copy_changed", "rejected", "publisher_changed", "conflict", "artifact_changed", "invalid", "lease_lost"} {
			valid = valid || t.ErrorCode == "skillhub.publication_description_release_commit_"+s
		}
		if !valid {
			return "", ErrSkillHubPublicationInvalid
		}
	default:
		return "", ErrSkillHubPublicationInvalid
	}
	return repairIdentity("catsco-publication-description-terminal-v1", t)
}

func (t SkillHubPublicationDescriptionReleaseTerminal) ValidateFor(o SkillHubPublicationDescriptionReleaseObservation) error {
	if _, err := t.Digest(); err != nil {
		return err
	}
	if o.ValidateFor(o.Intent.ActorUID, t.EntryID) != nil || t.ApplicationID != o.Intent.ApplicationID ||
		t.PermitID != o.Permit.ID || t.IntentHash != o.Intent.IdentityHash || t.Target != o.Intent.Target {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

func (r SkillHubPublicationDescriptionReleaseTerminalReceipt) ValidateFor(o SkillHubPublicationDescriptionReleaseObservation) error {
	h, err := r.Terminal.Digest()
	if err != nil || h != r.IdentityHash || r.RecordedAt.IsZero() || r.RecordedAt.Before(o.Permit.AuthorizedAt) || r.Terminal.ValidateFor(o) != nil {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

// Historical reads never authorize a run or reconcile Cloud as a side effect.
type SkillHubPublicationDescriptionReleaseTerminalReader interface {
	SkillHubPublicationDescriptionReleaseObservationReader
	GetSkillHubPublicationDescriptionReleaseTerminal(context.Context, int64, string) (*SkillHubPublicationDescriptionReleaseTerminalReceipt, error)
}

type SkillHubPublicationDescriptionReleaseTerminalStore interface {
	SkillHubPublicationDescriptionReleaseTerminalReader
	RecordSkillHubPublicationDescriptionReleaseTerminal(context.Context, int64, SkillHubPublicationDescriptionReleaseObservation, SkillHubPublicationDescriptionReleaseTerminal) (*SkillHubPublicationDescriptionReleaseTerminalReceipt, error)
}
