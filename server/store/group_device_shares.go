package store

import (
	"context"
	"errors"
)

var ErrDeviceShareConflict = errors.New("device sharing changed; refresh and confirm again")

type GroupDeviceShare struct {
	DeviceEpoch int64    `json:"-"`
	ID          string   `json:"id"`
	OwnerUID    int64    `json:"owner_uid"`
	GranteeUID  int64    `json:"grantee_uid"`
	TopicID     string   `json:"topic_id"`
	DeviceID    string   `json:"device_id"`
	Operations  []string `json:"operations"`
	Status      string   `json:"status"`
	Revision    int64    `json:"revision"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// The durable permission is independent of the short-lived execution grant.
// Implementations must serialize authorization and revocation and compare revisions.
type GroupDeviceShareBinding struct {
	Epoch  int64
	Active bool
}

type GroupDeviceShareStore interface {
	GetDeviceShareBinding(context.Context, int64, string) (*GroupDeviceShareBinding, error)
	RegisterDeviceShareBinding(context.Context, int64, string, string, int64) error
	ListGroupDeviceShares(context.Context, string, int64) ([]GroupDeviceShare, error)
	GetGroupDeviceShare(context.Context, string) (*GroupDeviceShare, error)
	SaveGroupDeviceShare(context.Context, GroupDeviceShare, int64) (*GroupDeviceShare, error)
	InvalidateDeviceShares(context.Context, int64, string) error
}
