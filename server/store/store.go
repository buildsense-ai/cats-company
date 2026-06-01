// Package store defines the database boundary used by Cats Company services.
package store

import "github.com/openchat/openchat/server/store/types"

// UserStore contains user and profile persistence operations.
type UserStore interface {
	CreateUser(u *types.User) (int64, error)
	GetUser(id int64) (*types.User, error)
	GetUserByUsername(username string) (*types.User, error)
	GetUserByEmail(email string) (*types.User, error)
	UpdateUserDisplayName(uid int64, displayName string) error
	UpdateUserPasswordHash(uid int64, passHash []byte) error
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
	ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error)
	GetBotOwner(botUID int64) (int64, error)
	DeleteBot(botUID int64) error
	SetTenantName(botUID int64, tenantName string) error
	GetTenantName(botUID int64) (string, error)
	SetBotVisibility(botUID int64, visibility string) error
}

// AgentAccessStore contains organization-style virtual employee access records.
type AgentAccessStore interface {
	ListAccessibleAgents(userUID int64) ([]*types.AgentRosterItem, error)
	GetAccessibleAgent(agentUID, userUID int64) (*types.AgentRosterItem, error)
	ListAgentAccess(agentUID int64) ([]*types.AgentAccess, error)
	GetAgentAccess(agentUID, userUID int64) (*types.AgentAccess, error)
	UpsertAgentAccess(agentUID, userUID, invitedBy int64, permission, status, source string) (*types.AgentAccess, error)
	UpdateAgentAccess(accessID, agentUID int64, permission, status string) (*types.AgentAccess, error)
	RevokeAgentAccess(accessID, agentUID int64) error
	AcceptAgentInvite(agentUID, userUID int64) (*types.AgentAccess, error)
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

// Store is the complete persistence boundary required by the current server.
type Store interface {
	UserStore
	FriendStore
	GroupStore
	MessageStore
	BotStore
	AgentAccessStore
	FeedbackStore
	AuthServiceStore
	CreateSchema() error
	HealthCheck() map[string]interface{}
	Close() error
}
