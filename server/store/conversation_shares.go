package store

import (
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// ConversationShareState describes whether a capability link may still expose
// its immutable conversation excerpt.
type ConversationShareState string

const (
	ConversationShareStateActive  ConversationShareState = "active"
	ConversationShareStateRevoked ConversationShareState = "revoked"
)

// ConversationShare is the private control record for a shared conversation
// excerpt. TokenHash is deliberately one-way: the raw capability only exists
// in the URL returned when the share is created.
type ConversationShare struct {
	ID        string                 `json:"id"`
	OwnerUID  int64                  `json:"owner_uid"`
	TopicID   string                 `json:"topic_id"`
	TokenHash string                 `json:"-"`
	Title     string                 `json:"title"`
	State     ConversationShareState `json:"state"`
	ExpiresAt *time.Time             `json:"expires_at,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	RevokedAt *time.Time             `json:"revoked_at,omitempty"`
}

// ConversationShareItem is a sanitized, immutable message snapshot. Snapshot
// is JSON rather than a reference to a source message so public reads can
// never accidentally inherit new history or source metadata.
type ConversationShareItem struct {
	ID              string `json:"id"`
	ShareID         string `json:"share_id"`
	Position        int    `json:"position"`
	SourceMessageID int64  `json:"-"`
	Speaker         string `json:"speaker"`
	Snapshot        string `json:"snapshot"`
}

// ConversationShareAsset is an isolated copy of one attachment included in a
// snapshot. StorageKey is private server-side state and is never sent to a
// visitor.
type ConversationShareAsset struct {
	ID         string `json:"id"`
	ShareID    string `json:"share_id"`
	ItemID     string `json:"item_id"`
	StorageKey string `json:"-"`
	Name       string `json:"name"`
	MimeType   string `json:"mime_type"`
	Size       int64  `json:"size"`
	Kind       string `json:"kind"`
}

// ConversationShareStore is intentionally optional so focused test stores and
// deployments can fail closed until the feature's schema is present.
type ConversationShareStore interface {
	CreateConversationShare(share *ConversationShare, items []*ConversationShareItem, assets []*ConversationShareAsset) error
	GetConversationShareByID(shareID string) (*ConversationShare, error)
	GetConversationShareByTokenHash(tokenHash string) (*ConversationShare, error)
	ListConversationShares(ownerUID int64, topicID string) ([]*ConversationShare, error)
	GetConversationShareItems(shareID string) ([]*ConversationShareItem, error)
	GetConversationShareAsset(shareID, assetID string) (*ConversationShareAsset, error)
	RevokeConversationShare(ownerUID int64, shareID string) (bool, error)
}

// MessagesByIDStore is an optional narrow lookup used to turn an explicit,
// user-selected set of message IDs into a server-authoritative snapshot.
type MessagesByIDStore interface {
	GetMessagesByIDs(topicID string, ids []int64) ([]*types.Message, error)
}
