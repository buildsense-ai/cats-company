// Package store defines the database boundary used by Cats Company services.
package store

import (
	"errors"

	"github.com/openchat/openchat/server/store/types"
)

var ErrChannelAgentBindingAlreadyLinked = errors.New("channel agent binding already linked to another canonical user")

// UserStore contains user and profile persistence operations.
type UserStore interface {
	CreateUser(u *types.User) (int64, error)
	GetUser(id int64) (*types.User, error)
	GetUserByUsername(username string) (*types.User, error)
	GetUserByEmail(email string) (*types.User, error)
	ListAdminUsers(query string, accountType types.AccountType, limit, offset int) ([]*types.User, error)
	CountAdminUsers(query string, accountType types.AccountType) (int, error)
	UpdateUserDisplayName(uid int64, displayName string) error
	UpdateUserPasswordHash(uid int64, passHash []byte) error
	UpdateUserState(uid int64, state int) error
	SearchUsers(query string, limit int) ([]*types.User, error)
	UpdateUser(id int64, displayName, avatarURL string) error
	UpdateUserAvatar(id int64, avatarURL string) error
}

// FriendStore contains friend relationship persistence operations.
type FriendStore interface {
	CreateFriendRequest(fromUID, toUID int64, message string) (int64, error)
	AcceptFriendRequest(fromUID, toUID int64) error
	RejectFriendRequest(fromUID, toUID int64) error
	BlockUser(uid, blockedUID int64) error
	RemoveFriend(uid1, uid2 int64) error
	GetFriends(uid int64) ([]*types.User, error)
	GetPendingRequests(uid int64) ([]*types.FriendRequest, error)
	AreFriends(uid1, uid2 int64) (bool, error)
	IsBlocked(uid, blockedUID int64) (bool, error)
}

// GroupStore contains group chat persistence operations.
type GroupStore interface {
	CreateGroup(name string, ownerID int64) (int64, error)
	GetGroup(groupID int64) (*types.Group, error)
	AddGroupMember(groupID, userID int64, role string) error
	RemoveGroupMember(groupID, userID int64) error
	GetGroupMembers(groupID int64) ([]*types.GroupMember, error)
	GetUserGroups(userID int64) ([]*types.Group, error)
	IsGroupMember(groupID, userID int64) (bool, error)
	GetGroupMemberCount(groupID int64) (int, error)
	GetGroupBotCount(groupID int64) (int, error)
	UpdateMemberRole(groupID, userID int64, role string) error
	DeleteGroup(groupID int64) error
	GetMemberRole(groupID, userID int64) (string, error)
	IsMemberMuted(groupID, userID int64) (bool, error)
	SetMemberMuted(groupID, userID int64, muted bool) error
	CanManageMember(groupID, actorID, targetID int64) (bool, error)
	SetGroupAnnouncement(groupID int64, announcement string) error
	UpdateGroupProfile(groupID int64, name, avatarURL string) error
	IsUserBot(userID int64) (bool, error)
}

// MessageStore contains topic and message persistence operations.
type MessageStore interface {
	CreateTopic(id, topicType string, ownerID int64) error
	SaveMessage(topicID string, fromUID int64, content, msgType string) (int64, error)
	SaveMessageWithBlocks(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string) (int64, error)
	SaveMessageWithReply(topicID string, fromUID int64, content, msgType string, replyTo int64) (int64, error)
	SaveMessageIdempotent(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string) (id int64, duplicate bool, err error)
	GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error)
	GetMessages(topicID string, limit, offset int) ([]*types.Message, error)
	GetLatestMessages(topicID string, limit, offset int) ([]*types.Message, error)
	GetLatestMessagesForTopics(topicIDs []string) (map[string]*types.Message, error)
}

// BotStore contains bot account and bot configuration persistence operations.
type BotStore interface {
	SaveBotConfig(uid int64, apiEndpoint, model string) error
	SaveBotConfigWithOwner(uid, ownerID int64, apiEndpoint, model string) error
	GetBotConfig(uid int64) (*types.BotConfig, error)
	ListBots() ([]map[string]interface{}, error)
	ToggleBotEnabled(uid int64) error
	SaveAPIKey(uid int64, apiKey string) error
	GetBotDebugMessages(uid int64, limit int) ([]*types.Message, error)
	GetBotByAPIKey(apiKey string) (int64, error)
	GetBotAPIKey(botUID int64) (string, error)
	EnsureBotBodyBinding(botUID int64, bodyID string) (string, bool, error)
	SetBotBodyBinding(botUID int64, bodyID string) error
	GetBotBodyID(botUID int64) (string, error)
	ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error)
	GetBotOwner(botUID int64) (int64, error)
	DeleteBot(botUID int64) error
	SetTenantName(botUID int64, tenantName string) error
	GetTenantName(botUID int64) (string, error)
	SetBotVisibility(botUID int64, visibility string) error
}

// FeedbackStore contains user feedback persistence operations.
type FeedbackStore interface {
	CreateFeedbackReport(report *types.FeedbackReport) (int64, error)
}

// AuthServiceStore contains service-to-service account center credentials.
type AuthServiceStore interface {
	CreateAuthService(service *types.AuthService) (int64, error)
	ListAuthServices() ([]*types.AuthService, error)
	GetAuthServiceByTokenHash(tokenHash string) (*types.AuthService, error)
	RevokeAuthService(id int64) error
	TouchAuthServiceLastUsed(id int64) error
}

// ChannelAgentBindingStore contains the optional persistence boundary for
// channel QR entries and external-user-to-agent bindings.
type ChannelAgentBindingStore interface {
	EnsureChannelAgentEntry(entry *types.ChannelAgentEntry) (*types.ChannelAgentEntry, error)
	ListChannelAgentEntries(ownerUID, agentUID int64) ([]*types.ChannelAgentEntry, error)
	RegenerateChannelAgentEntry(id, ownerUID int64, sceneKey string) (*types.ChannelAgentEntry, error)
	GetChannelAgentEntryBySceneKey(sceneKey string) (*types.ChannelAgentEntry, error)
	UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBinding(query types.ChannelAgentBindingQuery) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBindingForActor(channel, channelAppID string, actorUID, agentUID int64) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error)
	LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64) (*types.ChannelAgentBinding, error)
}

// Store is the complete persistence boundary required by the current server.
type Store interface {
	UserStore
	FriendStore
	GroupStore
	MessageStore
	BotStore
	FeedbackStore
	AuthServiceStore
	CreateSchema() error
	HealthCheck() map[string]interface{}
	Close() error
}
