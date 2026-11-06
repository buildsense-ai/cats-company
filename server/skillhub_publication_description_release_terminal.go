package server

import (
	"context"

	"github.com/openchat/openchat/server/store"
)

// Persist only a strictly validated terminal observation. In-progress or lost
// peer responses never become durable failure/success and never clear fences.
func recordConfirmedPublicationDescriptionReleaseTerminal(ctx context.Context, db store.SkillHubPublicationDescriptionReleaseTerminalStore,
	actor int64, id string, ports publicationDescriptionReleaseOutcomePorts,
) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	if db == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	outcome, err := readConfirmedPublicationDescriptionReleaseOutcome(ctx, db, actor, id, ports)
	if err != nil {
		return nil, err
	}
	if !outcome.terminal() {
		return nil, nil
	}
	s := outcome.status
	t := store.SkillHubPublicationDescriptionReleaseTerminal{ApplicationID: s.ApplicationID, EntryID: id, PermitID: s.PermitID,
		IntentHash: outcome.observation.Intent.IdentityHash, Target: s.Target, State: s.State}
	if s.Result != nil {
		p := s.Result
		t.Result = &store.SkillHubPublicationDescriptionReleaseResult{
			SkillID: p.SkillID, Version: p.Version, PackageID: p.PackageID, Checksum: p.Checksum, PublishedAt: p.PublishedAt}
	}
	if s.ErrorCode != nil {
		t.ErrorCode = *s.ErrorCode
	}
	if t.ValidateFor(outcome.observation) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	r, err := db.RecordSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, outcome.observation, t)
	if err != nil {
		return nil, err
	}
	h, _ := t.Digest()
	if r == nil || r.ValidateFor(outcome.observation) != nil || r.IdentityHash != h {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copy := *r
	if r.Terminal.Result != nil {
		p := *r.Terminal.Result
		copy.Terminal.Result = &p
	}
	return &copy, nil
}
