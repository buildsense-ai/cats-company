package server

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Synthetic CURRENT authority reader, deliberately separate from execution and
// repair history. Tests must not use history as the authorization decision.
type repairDraftSourceTestReader struct {
	source *repairSourceTestStore
	draft  *store.SkillHubPublicationDescriptionDraft
	denied bool
	reads  int
	hook   func(int)
}

func (s *repairDraftSourceTestReader) GetSkillHubPublicationDescription(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionDraft, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.reads++
	if s.hook != nil {
		s.hook(s.reads)
	}
	if s.denied || actor != 7 || id != s.source.task.Input.Receipt.Query.EntryID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if s.draft == nil {
		return nil, nil
	}
	d := snapshotPublicationModelDescription(*s.draft)
	return &d, nil
}

func (s *repairDraftSourceTestReader) GetSkillHubPublicationRepairTask(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationRepairTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t := snapshotPublicationRepairTask(s.source.task)
	return &t, nil // history deliberately remains readable after denial
}

func repairDraftSourceFixture(t *testing.T, s *repairSourceTestStore, confirmed, expired bool) *repairDraftSourceTestReader {
	t.Helper()
	task := snapshotPublicationRepairTask(s.task)
	if expired {
		task.Rounds[0].Lease.Until = time.Now().Add(-100 * time.Millisecond)
	}
	round := task.Rounds[0]
	task.Rounds[0].Result = &store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: task.Input.Evidence}
	hash, err := round.Lease.Input.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	index := int64(0)
	in := store.SkillHubPublicationDescriptionDraftInput{TaskID: task.ID, TaskIdentityHash: task.IdentityHash, Round: 1,
		RoundIdentityHash: hash, Evidence: task.Input.Evidence,
		Proposal: store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: "Read documents."}}
	key, err := in.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	d := &store.SkillHubPublicationDescriptionDraft{EntryID: task.Input.Receipt.Query.EntryID, Input: in, ConfirmationKey: key, CreatedAt: task.CreatedAt.Add(time.Millisecond)}
	if confirmed {
		at := task.CreatedAt.Add(2 * time.Millisecond)
		d.ConfirmedAt = &at
	}
	if err := d.ValidateFor(task); err != nil {
		t.Fatal(err)
	}
	s.task = task
	return &repairDraftSourceTestReader{source: s, draft: d}
}

func TestPublicationRepairModelDraftSourceUsesCurrentDraftAuthority(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "confirmed"}[confirmed], func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			reader := repairDraftSourceFixture(t, s, confirmed, true)
			f.h.publicationRepairSource.drafts = reader
			task := snapshotPublicationRepairTask(s.task)
			original := snapshotPublicationModelDescription(*reader.draft)
			if _, err := s.GetSkillHubPublicationRepairExecutionTask(context.Background(), 7, task.Rounds[0].Lease); err == nil {
				t.Fatal("finished/expired fixture still executable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.h.publicationRepairDescriptionSourceVerifier(7)(ctx, task) }()
			req := receiveRepairSourceCall(t, f, done)
			f.h.handleThinToolRPCResult(f.runtime, repairSourceReply(req))
			if err := waitRepairSource(t, done); err != nil {
				t.Fatal("read-only draft source rejected", err)
			}
			deliveryAckStatus(t, f.runtime, http.StatusOK)
			assertRepairSourceIdle(t, f)
			if reader.reads < 4 || !reflect.DeepEqual(*reader.draft, original) || !reflect.DeepEqual(s.task, task) {
				t.Fatal("missing current authority checks or mutated recovery")
			}
		})
	}
}

func TestPublicationRepairModelDraftSourceDeniesUnsafeRecovery(t *testing.T) {
	for _, mode := range []string{"denied_before", "missing_draft", "changed_draft", "changed_task", "cancelled_after", "source_changed", "confirmation_during_read", "nil_reader", "invalid_state"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newRepairSourceFixture(t)
			reader := repairDraftSourceFixture(t, s, false, true)
			f.h.publicationRepairSource.drafts = reader
			task := snapshotPublicationRepairTask(s.task)
			beforeRPC := true
			switch mode {
			case "denied_before":
				reader.denied = true
			case "missing_draft":
				reader.draft = nil
			case "changed_draft":
				reader.draft.Input.Proposal.Text = "different text"
			case "changed_task":
				s.task.Input.TopicID = "grp_456"
			case "nil_reader":
				f.h.publicationRepairSource.drafts = nil
			case "invalid_state":
				task.Rounds[0].Result.Outcome = "validated"
			case "confirmation_during_read":
				reader.hook = func(n int) {
					if n == 2 {
						at := time.Now()
						reader.draft.ConfirmedAt = &at
					}
				}
			default:
				beforeRPC = false
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if beforeRPC {
				if err := f.h.publicationRepairDescriptionSourceVerifier(7)(ctx, task); err == nil {
					t.Fatal("unsafe read authorized")
				}
				if len(f.runtime.send) != 0 {
					t.Fatal("unsafe authority reached Runtime")
				}
			} else {
				done := make(chan error, 1)
				go func() { done <- f.h.publicationRepairDescriptionSourceVerifier(7)(ctx, task) }()
				req := receiveRepairSourceCall(t, f, done)
				reply := repairSourceReply(req)
				if mode == "cancelled_after" {
					reader.denied = true
				} else {
					reply.Result.(map[string]interface{})["sourceSnapshotHash"] = "changed"
				}
				f.h.handleThinToolRPCResult(f.runtime, reply)
				if err := waitRepairSource(t, done); err == nil {
					t.Fatal("revoked/changed state recovered")
				}
				status := http.StatusConflict
				if mode == "source_changed" {
					status = http.StatusUnprocessableEntity
				}
				deliveryAckStatus(t, f.runtime, status)
			}
			assertRepairSourceIdle(t, f)
		})
	}
}

func TestPublicationRepairModelDraftSourceNoUnregisteredFallback(t *testing.T) {
	var h *Hub
	if err := h.publicationRepairDescriptionSourceVerifier(7)(context.Background(), store.SkillHubPublicationRepairTask{}); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("nil Hub authorized", err)
	}
}
