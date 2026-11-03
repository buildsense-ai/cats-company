package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type firstRoundControllerTestStore struct {
	*modelDescriptionTestStore
	finishCalls              int
	taskReadCalls            int
	finishError              error
	finishCommitsBeforeError bool
	taskReadError            error
}

func (s *firstRoundControllerTestStore) FinishSkillHubPublicationRepairRound(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, result store.SkillHubPublicationRepairResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.finishError != nil {
		if s.finishCommitsBeforeError && actor == 1 && validPublicationRepairModelTask(s.task, lease) && result.Validate() == nil {
			s.task.Rounds[0].Result = &result
		}
		return s.finishError
	}
	if actor != 1 || !validPublicationRepairModelTask(s.task, lease) || result.Validate() != nil {
		return store.ErrSkillHubPublicationStale
	}
	s.task.Rounds[0].Result = &result
	return nil
}

func (s *firstRoundControllerTestStore) GetSkillHubPublicationRepairTask(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationRepairTask, error) {
	s.taskReadCalls++
	if s.taskReadError != nil {
		return nil, s.taskReadError
	}
	return s.modelDescriptionTestStore.GetSkillHubPublicationRepairTask(ctx, actor, id)
}

func TestPublicationRepairModelFirstRoundControllerPersistsOnlyModelUncertainty(t *testing.T) {
	for _, mode := range []string{"provider_uncertain", "start_before_fence", "start_after_fence", "report_read_error", "proposal_recovery_uncertain", "finish_lost", "finish_lost_after_commit", "finish_lost_read_error", "completion_committed_history_read_lost"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
			controller := &firstRoundControllerTestStore{modelDescriptionTestStore: db}
			switch mode {
			case "provider_uncertain":
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					return store.SkillHubPublicationRepairModelOutput{}, errors.New("private provider outcome")
				}
			case "start_before_fence":
				controller.startError = errors.New("temporary fence transaction failure")
			case "start_after_fence":
				controller.startError = errors.New("lost fence commit reply")
				controller.startCommitBeforeError = true
			case "completion_committed_history_read_lost":
				controller.completeError = errors.New("completion reply lost")
				controller.historyError = errors.New("history temporarily unavailable")
			case "report_read_error":
				reports = repairMaterialReportReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
					return nil, errors.New("temporary report peer failure")
				})
			case "proposal_recovery_uncertain":
				controller.draftHook = func(read int) error {
					if read == 3 {
						return store.ErrSkillHubPublicationDenied
					}
					return nil
				}
				controller.writeError = errors.New("lost proposal reply")
			case "finish_lost":
				controller.finishError = errors.New("lost stop checkpoint reply")
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					return store.SkillHubPublicationRepairModelOutput{}, errors.New("private provider outcome")
				}
			case "finish_lost_after_commit":
				controller.finishError = errors.New("lost stop checkpoint reply")
				controller.finishCommitsBeforeError = true
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					return store.SkillHubPublicationRepairModelOutput{}, errors.New("private provider outcome")
				}
			case "finish_lost_read_error":
				controller.finishError = errors.New("lost stop checkpoint reply")
				controller.taskReadError = errors.New("read back failed")
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					return store.SkillHubPublicationRepairModelOutput{}, errors.New("private provider outcome")
				}
			}

			draft, err := runPublicationRepairFirstRoundAttempt(context.Background(), controller, 1, lease, cfg, ports, reports, verify)
			if err == nil || draft != nil {
				t.Fatal("uncertain attempt reported success", err)
			}
			if mode == "provider_uncertain" || mode == "start_after_fence" || mode == "finish_lost" || mode == "finish_lost_after_commit" || mode == "finish_lost_read_error" {
				if !errors.Is(err, errPublicationRepairModelUncertain) || controller.finishCalls != 1 {
					t.Fatal("model uncertainty was not durably stopped", err, controller.finishCalls)
				}
				if mode == "provider_uncertain" || mode == "start_after_fence" {
					want := store.SkillHubPublicationRepairResult{Outcome: "stopped", Code: "model_result_uncertain", Evidence: lease.Input.Evidence}
					wantCalls := 1
					if mode == "start_after_fence" {
						wantCalls = 0
					}
					if !errors.Is(err, errPublicationRepairModelUncertainStopped) || !reflect.DeepEqual(*controller.task.Rounds[0].Result, want) || controller.task.State() != "stopped" || *calls != wantCalls || controller.taskReadCalls != 0 {
						t.Fatal("wrong durable uncertainty checkpoint", mode)
					}
				} else if mode == "finish_lost_after_commit" {
					want := store.SkillHubPublicationRepairResult{Outcome: "stopped", Code: "model_result_uncertain", Evidence: lease.Input.Evidence}
					if !errors.Is(err, errPublicationRepairModelUncertainStopped) || !reflect.DeepEqual(*controller.task.Rounds[0].Result, want) || controller.task.State() != "stopped" || controller.taskReadCalls != 1 || *calls != 1 {
						t.Fatal("lost stop reply was not recovered from the exact committed checkpoint")
					}
				} else if controller.taskReadCalls != 1 || (mode == "finish_lost" && controller.task.Rounds[0].Result != nil) ||
					(mode == "finish_lost_read_error" && errors.Is(err, errPublicationRepairModelUncertainStopped)) {
					t.Fatal("unconfirmed stop checkpoint was treated as committed", mode)
				}
			} else if controller.finishCalls != 0 || (mode == "start_before_fence" &&
				(controller.task.State() == "stopped" || controller.task.Rounds[0].Result != nil || *calls != 0)) ||
				(mode == "completion_committed_history_read_lost" &&
					(controller.task.State() == "stopped" || controller.task.Rounds[0].Result != nil || controller.draft != nil || *calls != 1)) ||
				(mode == "proposal_recovery_uncertain" &&
					(controller.task.State() != "waiting_confirmation" || controller.draft == nil || *calls != 1)) ||
				(mode == "report_read_error" && controller.task.Rounds[0].Result != nil) {
				t.Fatal("recoverable/transient error incorrectly stopped or lost task state", mode)
			}
			if mode == "completion_committed_history_read_lost" {
				controller.historyError = nil
				draft, err := runPublicationRepairFirstRoundAttempt(context.Background(), controller, 1, lease, cfg, ports, reports, verify)
				if err != nil || draft == nil || *calls != 1 {
					t.Fatal("retained output was not recovered without redispatch", err, *calls)
				}
			}
			if mode == "start_before_fence" {
				controller.startError = nil
				draft, err := runPublicationRepairFirstRoundAttempt(context.Background(), controller, 1, lease, cfg, ports, reports, verify)
				if err != nil || draft == nil || *calls != 1 {
					t.Fatal("retry after an uncommitted fence did not complete", err, *calls)
				}
			}
		})
	}
}

func TestPublicationRepairModelFirstRoundControllerReplaysExistingDraftWithoutStop(t *testing.T) {
	db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
	controller := &firstRoundControllerTestStore{modelDescriptionTestStore: db}
	first, err := runPublicationRepairFirstRoundAttempt(context.Background(), controller, 1, lease, cfg, ports, reports, verify)
	if err != nil || first == nil {
		t.Fatal("first draft", err)
	}
	again, err := runPublicationRepairFirstRoundAttempt(context.Background(), controller, 1, lease, cfg, ports, reports, verify)
	if err != nil || again == nil || again.ConfirmationKey != first.ConfirmationKey || *calls != 1 || controller.finishCalls != 0 {
		t.Fatal("draft replay changed state or redispatched", err)
	}
}
