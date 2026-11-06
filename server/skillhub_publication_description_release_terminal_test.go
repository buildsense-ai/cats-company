package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type terminalOutcomeTestStore struct {
	*descriptionOutcomeTestReader
	receipt *store.SkillHubPublicationDescriptionReleaseTerminalReceipt
	writes  int
	lose    bool
}

func (db *terminalOutcomeTestStore) GetSkillHubPublicationDescriptionReleaseTerminal(context.Context, int64, string) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	return db.receipt, nil
}
func (db *terminalOutcomeTestStore) RecordSkillHubPublicationDescriptionReleaseTerminal(_ context.Context, actor int64, o store.SkillHubPublicationDescriptionReleaseObservation, t store.SkillHubPublicationDescriptionReleaseTerminal) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	db.writes++
	if actor != o.Intent.ActorUID || !reflect.DeepEqual(o, db.observation) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	h, err := t.Digest()
	if err != nil {
		return nil, err
	}
	if db.receipt == nil {
		db.receipt = &store.SkillHubPublicationDescriptionReleaseTerminalReceipt{Terminal: t, IdentityHash: h, RecordedAt: o.Permit.AuthorizedAt.Add(time.Second)}
	}
	if db.receipt.IdentityHash != h {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if db.lose {
		db.lose = false
		return nil, context.DeadlineExceeded
	}
	return db.receipt, nil
}

func TestRecordConfirmedPublicationDescriptionReleaseTerminal(t *testing.T) {
	for _, state := range []string{"queued", "publishing", "committing", "published", "publish_failed"} {
		t.Run(state, func(t *testing.T) {
			r, s := descriptionOutcomeFixture(t)
			db := &terminalOutcomeTestStore{descriptionOutcomeTestReader: r}
			s.State = state
			if state == "published" {
				s.Result = &PublicationCommitCloudResult{SkillID: s.Target.SkillID, Version: s.Target.Version, PublishedAt: s.Target.ReleaseAt, Checksum: strings.Repeat("a", 64), PackageID: "pkg_12345678-1234-4123-8123-123456789abc"}
			}
			if state == "publish_failed" {
				code := "skillhub.publication_description_release_commit_unavailable"
				s.ErrorCode = &code
			}
			ports := publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
				return &s, nil
			}}
			id, actor := r.observation.Intent.EntryID, r.observation.Intent.ActorUID
			terminal := state == "published" || state == "publish_failed"
			db.lose = terminal
			first, err := recordConfirmedPublicationDescriptionReleaseTerminal(context.Background(), db, actor, id, ports)
			if !terminal {
				if err != nil || first != nil || db.writes != 0 {
					t.Fatal("progress persisted", first, err)
				}
				return
			}
			if !errors.Is(err, context.DeadlineExceeded) || first != nil {
				t.Fatal("lost commit reply misreported", first, err)
			}
			saved := *db.receipt
			got, err := recordConfirmedPublicationDescriptionReleaseTerminal(context.Background(), db, actor, id, ports)
			if err != nil || got == nil || !reflect.DeepEqual(*got, saved) {
				t.Fatal("terminal recovery", got, err)
			}
			if got.Terminal.Result != nil {
				got.Terminal.Result.Checksum = "changed"
				if db.receipt.Terminal.Result.Checksum == "changed" {
					t.Fatal("receipt alias")
				}
			}
		})
	}
}

func TestRecordConfirmedPublicationDescriptionReleaseTerminalRejectsUncertainCloud(t *testing.T) {
	r, _ := descriptionOutcomeFixture(t)
	db := &terminalOutcomeTestStore{descriptionOutcomeTestReader: r}
	ports := publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
		return nil, context.DeadlineExceeded
	}}
	if got, err := recordConfirmedPublicationDescriptionReleaseTerminal(context.Background(), db, r.observation.Intent.ActorUID, r.observation.Intent.EntryID, ports); got != nil || !errors.Is(err, context.DeadlineExceeded) || db.writes != 0 {
		t.Fatal("unknown Cloud result persisted", got, err)
	}
}
