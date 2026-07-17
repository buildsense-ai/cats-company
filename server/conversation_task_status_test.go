package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestNormalizeConversationTaskStatusDefaultsActiveExpiry(t *testing.T) {
	status, err := normalizeConversationTaskStatus(42, "p2p_7_42", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{
			"state":   "running",
			"run_id":  "run-1",
			"summary": "building",
		},
	})
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.ExpiresAt == nil {
		t.Fatal("running status should receive a default expiry")
	}
	if got := status.ExpiresAt.Sub(status.UpdatedAt); got != defaultActiveTaskStatusTTL {
		t.Fatalf("default expiry=%s, want %s", got, defaultActiveTaskStatusTTL)
	}
}

func TestNormalizeConversationTaskStatusPreservesExplicitExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 7, 18, 8, 30, 0, 0, time.UTC)
	status, err := normalizeConversationTaskStatus(42, "grp_9", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{
			"state":      "waiting",
			"expires_at": expiresAt.Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.ExpiresAt == nil || !status.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at=%v, want %v", status.ExpiresAt, expiresAt)
	}
}

func TestNormalizeConversationTaskStatusLeavesTerminalExpiryOptional(t *testing.T) {
	status, err := normalizeConversationTaskStatus(42, "grp_9", &normalizedMessagePayload{
		DisplayContent: map[string]interface{}{"state": "completed"},
	})
	if err != nil {
		t.Fatalf("normalize task status: %v", err)
	}
	if status.ExpiresAt != nil {
		t.Fatalf("terminal expiry=%v, want nil", status.ExpiresAt)
	}
}

func TestValidateTaskStatusTransitionRejectsLateProgressForTerminalRun(t *testing.T) {
	current := taskStatusForTransition("run-1", "completed", 42)
	next := taskStatusForTransition("run-1", "running", 42)
	if err := validateTaskStatusTransition(current, next); err == nil {
		t.Fatal("expected terminal run to reject late progress")
	}
}

func TestValidateTaskStatusTransitionRejectsAnotherActiveSource(t *testing.T) {
	current := taskStatusForTransition("run-1", "running", 42)
	next := taskStatusForTransition("run-2", "running", 43)
	if err := validateTaskStatusTransition(current, next); err == nil {
		t.Fatal("expected active status to reject another source")
	}
}

func taskStatusForTransition(runID, state string, sourceUID int64) *types.ConversationTaskStatus {
	return &types.ConversationTaskStatus{RunID: runID, State: state, SourceUID: sourceUID}
}
