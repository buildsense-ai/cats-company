package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type descriptionOutcomeTestReader struct {
	observation store.SkillHubPublicationDescriptionReleaseObservation
	calls       int
	onRead      func(int) error
}

func (r *descriptionOutcomeTestReader) GetSkillHubPublicationDescriptionReleaseObservation(_ context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseObservation, error) {
	r.calls++
	if r.onRead != nil {
		if err := r.onRead(r.calls); err != nil {
			return nil, err
		}
	}
	if actor != r.observation.Intent.ActorUID || id != r.observation.Intent.EntryID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	copy := snapshotDescriptionReleaseObservation(r.observation)
	return &copy, nil
}

func descriptionOutcomeFixture(t *testing.T) (*descriptionOutcomeTestReader, publicationDescriptionCloudReleaseStatus) {
	t.Helper()
	_, planned, intent, permit := releaseAuthorizeFixture(t)
	r := &descriptionOutcomeTestReader{observation: store.SkillHubPublicationDescriptionReleaseObservation{
		Continuation: planned.checkpoint, Intent: intent, Permit: store.SkillHubPublicationDescriptionReleasePermit{
			Intent: intent, ID: permit.PermitID, AuthorizedAt: intent.RequestedAt.Add(1e9)}}}
	if err := r.observation.ValidateFor(intent.ActorUID, intent.EntryID); err != nil {
		t.Fatal(err)
	}
	return r, releaseStatusFixture(planned.checkpoint.Application, *permit)
}

func TestReadConfirmedPublicationDescriptionReleaseOutcome(t *testing.T) {
	for _, state := range []string{"queued", "publishing", "committing", "published", "publish_failed"} {
		t.Run(state, func(t *testing.T) {
			r, status := descriptionOutcomeFixture(t)
			status.State = state
			if state == "published" {
				status.Result = &PublicationCommitCloudResult{SkillID: status.Target.SkillID, Version: status.Target.Version,
					PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64), PublishedAt: status.Target.ReleaseAt}
			}
			if state == "publish_failed" {
				code := "skillhub.publication_description_release_commit_rejected"
				status.ErrorCode = &code
			}
			baseline := snapshotDescriptionReleaseObservation(r.observation)
			calls := 0
			ports := publicationDescriptionReleaseOutcomePorts{ReadCloud: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication,
				intent store.SkillHubPublicationDescriptionReleaseIntent, p publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
				calls++
				if app.ID != baseline.Continuation.Application.ID || intent != baseline.Intent || p.PermitID != baseline.Permit.ID || p.validateFor(app, intent) != nil {
					t.Fatal("unbound observation")
				}
				// Adapter mutations must not affect the stored observation.
				app.Result.RequestBytes++
				return &status, nil
			}}
			got, err := readConfirmedPublicationDescriptionReleaseOutcome(context.Background(), r, baseline.Intent.ActorUID, baseline.Intent.EntryID, ports)
			if err != nil || got == nil || got.status.State != state || got.terminal() != (state == "published" || state == "publish_failed") ||
				calls != 1 || r.calls != 2 || !reflect.DeepEqual(baseline, r.observation) {
				t.Fatal("outcome", got, err, calls, r.calls)
			}
			if status.Result != nil {
				status.Result.Checksum = "changed"
				if got.status.Result.Checksum == "changed" {
					t.Fatal("result alias")
				}
			}
			if status.ErrorCode != nil {
				*status.ErrorCode = "changed"
				if *got.status.ErrorCode == "changed" {
					t.Fatal("error alias")
				}
			}
		})
	}
}

func TestReadConfirmedPublicationDescriptionReleaseOutcomeFailsClosed(t *testing.T) {
	for _, mode := range []string{"foreign", "disabled-before", "disabled-after", "changed-after", "invalid-permit", "invalid-intent", "nil", "foreign-status", "bad-terminal", "lost-reply", "cancelled", "cancelled-after"} {
		t.Run(mode, func(t *testing.T) {
			r, status := descriptionOutcomeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			actor, id := r.observation.Intent.ActorUID, r.observation.Intent.EntryID
			calls := 0
			ports := publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
				calls++
				switch mode {
				case "cancelled-after":
					cancel()
				case "nil":
					return nil, nil
				case "foreign-status":
					status.PermitID += "a"
				case "bad-terminal":
					status.State = "published"
				case "lost-reply":
					return nil, context.DeadlineExceeded
				}
				return &status, nil
			}}
			if mode == "foreign" {
				actor++
			}
			if mode == "cancelled" {
				cancel()
			}
			if mode == "invalid-permit" {
				r.observation.Permit.ID = "invalid"
			}
			if mode == "invalid-intent" {
				r.observation.Intent.IdentityHash = strings.Repeat("f", 64)
			}
			r.onRead = func(n int) error {
				if mode == "disabled-before" || (mode == "disabled-after" && n == 2) {
					return store.ErrSkillHubPublicationDenied
				}
				if mode == "changed-after" && n == 2 {
					r.observation.Permit.AuthorizedAt = r.observation.Permit.AuthorizedAt.Add(1)
				}
				return nil
			}
			got, err := readConfirmedPublicationDescriptionReleaseOutcome(ctx, r, actor, id, ports)
			if got != nil || err == nil {
				t.Fatal("unsafe result", got, err)
			}
			if mode == "foreign" || mode == "disabled-before" || mode == "cancelled" || mode == "invalid-permit" || mode == "invalid-intent" {
				if calls != 0 {
					t.Fatal("unauthorized peer call")
				}
			} else if calls != 1 {
				t.Fatal("unexpected retry", calls)
			}
			if mode == "lost-reply" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("lost failure swallowed", err)
				}
				ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					return &status, nil
				}
				if got, err := readConfirmedPublicationDescriptionReleaseOutcome(ctx, r, actor, id, ports); err != nil || got == nil || got.terminal() {
					t.Fatal("read recovery", got, err)
				}
			}
		})
	}
}
