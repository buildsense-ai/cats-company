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

type GroupDeviceShareRequest struct {
	ID              string `json:"id"`
	RequesterUID    int64  `json:"requester_uid"`
	OwnerUID        int64  `json:"owner_uid"`
	TopicID         string `json:"topic_id"`
	DeviceID        string `json:"device_id,omitempty"`
	Description     string `json:"description"`
	ClientRequestID string `json:"client_request_id"`
	Status          string `json:"status"`
	Revision        int64  `json:"revision"`
	ShareID         string `json:"share_id,omitempty"`
	MessageID       int64  `json:"message_id,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// The durable permission is independent of the short-lived execution grant.
// Implementations must serialize approval/revocation and compare revisions.
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
	CreateGroupDeviceShareRequest(context.Context, GroupDeviceShareRequest) (*GroupDeviceShareRequest, error)
	GetGroupDeviceShareRequest(context.Context, string) (*GroupDeviceShareRequest, error)
	ResolveGroupDeviceShareRequest(context.Context, string, int64, string, *GroupDeviceShare) (*GroupDeviceShareRequest, error)
	SetGroupDeviceShareRequestMessage(context.Context, string, int64) error
	InvalidateDeviceShares(context.Context, int64, string) error
}
