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
	finishCalls int
	finishError error
}

func (s *firstRoundControllerTestStore) FinishSkillHubPublicationRepairRound(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, result store.SkillHubPublicationRepairResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.finishError != nil {
		return s.finishError
	}
	if actor != 1 || !validPublicationRepairModelTask(s.task, lease) || result.Validate() != nil {
		return store.ErrSkillHubPublicationStale
	}
	s.task.Rounds[0].Result = &result
	return nil
}

func TestPublicationRepairModelFirstRoundControllerPersistsOnlyModelUncertainty(t *testing.T) {
	for _, mode := range []string{"provider_uncertain", "report_read_error", "proposal_recovery_uncertain", "finish_lost"} {
		t.Run(mode, func(t *testing.T) {
			db, lease, cfg, ports, reports, verify, calls := repairDescriptionAttemptFixture(t)
			controller := &firstRoundControllerTestStore{modelDescriptionTestStore: db}
			switch mode {
			case "provider_uncertain":
				ports.InvokeOnce = func(context.Context, publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
					*calls++
					return store.SkillHubPublicationRepairModelOutput{}, errors.New("private provider outcome")
				}
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
			}

			draft, err := runPublicationRepairFirstRoundAttempt(context.Background(), controller, 1, lease, cfg, ports, reports, verify)
			if err == nil || draft != nil {
				t.Fatal("uncertain attempt reported success", err)
			}
			if mode == "provider_uncertain" || mode == "finish_lost" {
				if !errors.Is(err, errPublicationRepairModelUncertain) || controller.finishCalls != 1 {
					t.Fatal("model uncertainty was not durably stopped", err, controller.finishCalls)
				}
				if mode == "provider_uncertain" {
					want := store.SkillHubPublicationRepairResult{Outcome: "stopped", Code: "model_result_uncertain", Evidence: lease.Input.Evidence}
					if !reflect.DeepEqual(*controller.task.Rounds[0].Result, want) || controller.task.State() != "stopped" || *calls != 1 {
						t.Fatal("wrong durable uncertainty checkpoint")
					}
				} else if controller.task.Rounds[0].Result != nil {
					t.Fatal("lost stop reply was assumed committed")
				}
			} else if controller.finishCalls != 0 || (mode == "proposal_recovery_uncertain" &&
				(controller.task.State() != "waiting_confirmation" || controller.draft == nil || *calls != 1)) ||
				(mode == "report_read_error" && controller.task.Rounds[0].Result != nil) {
				t.Fatal("recoverable/transient error incorrectly stopped or lost task state", mode)
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
