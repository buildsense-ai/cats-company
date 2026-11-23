package store

import (
	"context"
	"fmt"
)

// ConversationDeviceSelection is a durable routing preference, not permission
// to execute tools. Execution still needs a new, short-lived scoped grant.
// A blank DeviceID is a versioned initial slot, or an explicit unpinned choice
// when Source is user_unspecified. Neither is an execution permission.
type ConversationDeviceSelection struct {
	OwnerUID  int64
	DeviceID  string
	Revision  string
	Source    string
	UpdatedAt int64
}

type ConversationDeviceSelectionStore interface {
	GetConversationDeviceSelection(ctx context.Context, actorUID int64, sessionKey string) (*ConversationDeviceSelection, error)
	CompareConversationDeviceSelection(ctx context.Context, actorUID int64, sessionKey, expectedRevision string, next ConversationDeviceSelection) (bool, error)
}

func ValidateDeviceSelectionScope(actorUID int64, sessionKey string) error {
	if actorUID <= 0 || sessionKey == "" || len(sessionKey) > 768 {
		return fmt.Errorf("invalid device selection scope")
	}
	for _, char := range sessionKey {
		if char > 127 {
			return fmt.Errorf("device selection session must be canonical ASCII")
		}
	}
	return nil
}

func ValidateDeviceSelectionRecord(expected string, next ConversationDeviceSelection) error {
	if len(expected) > 64 || next.Revision == "" || len(next.Revision) > 64 || len(next.DeviceID) > 128 || len(next.Source) > 64 || next.UpdatedAt <= 0 {
		return fmt.Errorf("invalid device selection record")
	}
	return nil
}
