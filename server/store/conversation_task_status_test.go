package store

import (
	"errors"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestValidateConversationTaskStatusTransitionRejectsStaleTerminalForSupersededActiveRun(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	current := &types.ConversationTaskStatus{
		RunID:     "run-new",
		State:     "running",
		ExpiresAt: &expiresAt,
	}
	staleTerminal := &types.ConversationTaskStatus{
		RunID: "run-old",
		State: "completed",
	}

	err := ValidateConversationTaskStatusTransition(current, staleTerminal, now)
	if !errors.Is(err, ErrConversationTaskRunSuperseded) {
		t.Fatalf("ValidateConversationTaskStatusTransition() error = %v, want %v", err, ErrConversationTaskRunSuperseded)
	}
}

func TestValidateConversationTaskStatusTransitionAllowsTerminalForExpiredSupersededRun(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	current := &types.ConversationTaskStatus{
		RunID:     "run-new",
		State:     "waiting",
		ExpiresAt: &expiresAt,
	}
	staleTerminal := &types.ConversationTaskStatus{
		RunID: "run-old",
		State: "completed",
	}

	if err := ValidateConversationTaskStatusTransition(current, staleTerminal, now); err != nil {
		t.Fatalf("ValidateConversationTaskStatusTransition() error = %v, want nil", err)
	}
}

func TestValidateConversationTaskStatusTransitionRejectsOlderPublisherUpdate(t *testing.T) {
	current := &types.ConversationTaskStatus{
		RunID: "run-new", State: "running",
		UpdatedAt:      time.Date(2026, time.August, 8, 3, 0, 1, 0, time.UTC),
		EventUpdatedAt: time.Date(2026, time.August, 8, 3, 0, 2, 0, time.UTC),
	}
	stale := &types.ConversationTaskStatus{
		RunID: "run-old", State: "waiting",
		UpdatedAt:      time.Date(2026, time.August, 8, 3, 0, 3, 0, time.UTC),
		EventUpdatedAt: time.Date(2026, time.August, 8, 3, 0, 1, 0, time.UTC),
	}
	if err := ValidateConversationTaskStatusTransition(current, stale, time.Now()); !errors.Is(err, ErrConversationTaskStatusStale) {
		t.Fatalf("ValidateConversationTaskStatusTransition() error = %v, want %v", err, ErrConversationTaskStatusStale)
	}
}

func TestPrepareConversationTaskStatusForStoreSeparatesEventAndLivenessTimes(t *testing.T) {
	eventAt := time.Date(2026, time.August, 8, 3, 0, 0, 0, time.UTC)
	receivedAt := eventAt.Add(time.Hour)
	input := &types.ConversationTaskStatus{UpdatedAt: eventAt}

	prepared := PrepareConversationTaskStatusForStore(input, receivedAt)
	if !prepared.EventUpdatedAt.Equal(eventAt) {
		t.Fatalf("event_updated_at=%v, want %v", prepared.EventUpdatedAt, eventAt)
	}
	if !prepared.UpdatedAt.Equal(receivedAt) {
		t.Fatalf("updated_at=%v, want %v", prepared.UpdatedAt, receivedAt)
	}
	if !input.EventUpdatedAt.IsZero() || !input.UpdatedAt.Equal(eventAt) {
		t.Fatalf("PrepareConversationTaskStatusForStore mutated input: %+v", input)
	}
}

func TestPrepareConversationTaskStatusForStoreBoundsExtremeFutureEventTime(t *testing.T) {
	receivedAt := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	prepared := PrepareConversationTaskStatusForStore(&types.ConversationTaskStatus{
		EventUpdatedAt: receivedAt.Add(24 * time.Hour),
	}, receivedAt)
	if !prepared.EventUpdatedAt.Equal(receivedAt) {
		t.Fatalf("event_updated_at=%v, want bounded receipt time %v", prepared.EventUpdatedAt, receivedAt)
	}
}
