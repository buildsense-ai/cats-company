// Package store defines the database boundary used by Cats Company services.
package store

import (
	"errors"

	"github.com/openchat/openchat/server/store/types"
)

var ErrChannelAgentBindingAlreadyLinked = errors.New("channel agent binding already linked to another canonical user")
var ErrChannelAgentAccessAlreadyLinked = errors.New("channel agent access request already linked")
var ErrChannelNativeGroupEventBusy = errors.New("channel native group event is already being processed")
var ErrProjectNameConflict = errors.New("project name already exists")
var ErrProjectNotFound = errors.New("project not found")
var ErrProjectTopicNotFound = errors.New("project or topic not found")
var ErrGroupInviteRequestNotPending = errors.New("group invite request is not pending")

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

// PushSubscriptionStore persists optional Web Push subscriptions without
// expanding the core Store boundary used by focused adapters and test stores.
type PushSubscriptionStore interface {
	UpsertPushSubscription(subscription *types.PushSubscription) error
	ListPushSubscriptions(uid int64) ([]*types.PushSubscription, error)
	DeletePushSubscription(uid int64, endpoint string) error
	DeletePushSubscriptionByEndpoint(endpoint string) error
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

// GroupInviteRequestStore persists member-proposed group invitations. It is
// optional so focused test stores and non-SQL adapters do not need to provide it.
type GroupInviteRequestStore interface {
	CreateGroupInviteRequest(groupID, inviterID, inviteeID int64) (*types.GroupInviteRequest, error)
	GetGroupInviteRequest(requestID int64) (*types.GroupInviteRequest, error)
	ListPendingGroupInviteRequests(groupID int64) ([]*types.GroupInviteRequest, error)
	ApproveGroupInviteRequest(requestID, resolverID int64) (*types.GroupInviteRequest, error)
	RejectGroupInviteRequest(requestID, resolverID int64) (*types.GroupInviteRequest, error)
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

// ConversationTaskStatusStore persists per-source runtime state and exposes a
// backwards-compatible aggregate status per topic.
type ConversationTaskStatusStore interface {
	UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error)
	GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error)
	GetConversationTaskStatuses(topicIDs []string) (map[string]*types.ConversationTaskStatus, error)
}

// ProjectStore persists user-owned projects and assignments for conversations the user can access.
// It remains optional so narrow server test stores do not need project methods.
type ProjectStore interface {
	CreateProject(ownerUID int64, name string) (*types.Project, error)
	ListProjects(ownerUID int64) ([]*types.Project, error)
	RenameProject(ownerUID, projectID int64, name string) error
	DeleteProject(ownerUID, projectID int64) error
	AssignTopicToProject(ownerUID, projectID int64, topicID string) error
	RemoveTopicFromProject(ownerUID int64, topicID string) error
	ListProjectTopics(ownerUID int64) ([]*types.ProjectTopic, error)
}

// ProjectTopicStore is the read-only boundary used by conversation summaries.
type ProjectTopicStore interface {
	ListProjectTopics(ownerUID int64) ([]*types.ProjectTopic, error)
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

// BotModelConfigStore is optional so existing narrow Store test doubles do not
// need cloud-model methods. Production database adapters implement it.
type BotModelConfigStore interface {
	GetBotModelConfig(botUID int64) (*types.BotModelConfig, error)
	MarkBotModelRuntimeProtocol(botUID int64, protocol string) (*types.BotModelConfig, error)
	SaveBotDesiredModelConfig(botUID int64, kind, modelID, reasoningEffort, customCiphertext string) (*types.BotModelConfig, error)
	AckBotModelConfig(botUID, revision int64, kind, modelID, reasoningEffort, applyError string) (*types.BotModelConfig, error)
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
	ListChannelAgentEntriesByChannelApp(channel, channelAppID string) ([]*types.ChannelAgentEntry, error)
	RegenerateChannelAgentEntry(id, ownerUID int64, sceneKey, channelAppID string) (*types.ChannelAgentEntry, error)
	GetChannelAgentEntryByID(id int64) (*types.ChannelAgentEntry, error)
	GetChannelAgentEntryBySceneKey(sceneKey string) (*types.ChannelAgentEntry, error)
	ListChannelAgentBindingsForAgent(ownerUID, agentUID int64) ([]*types.ChannelAgentBinding, error)
	RequestChannelAgentAccess(request *types.ChannelAgentAccessRequest) (*types.ChannelAgentAccessRequest, error)
	ResolveChannelAgentAccessRequest(query types.ChannelAgentBindingQuery) (*types.ChannelAgentAccessRequest, error)
	ApproveChannelAgentAccessRequestsForActor(actorUID, agentUID, reviewerUID int64) ([]*types.ChannelAgentBinding, error)
	RejectChannelAgentAccessRequestsForActor(actorUID, agentUID, reviewerUID int64) error
	ActivateChannelAgentBindingsForCanonicalUser(canonicalUID, agentUID, reviewerUID int64) ([]*types.ChannelAgentBinding, error)
	RejectChannelAgentBindingsForCanonicalUser(canonicalUID, agentUID, reviewerUID int64) error
	UpsertChannelAgentBinding(binding *types.ChannelAgentBinding) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBinding(query types.ChannelAgentBindingQuery) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBindingForActor(channel, channelAppID string, actorUID, agentUID int64) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBindingForCanonical(channel, channelAppID string, canonicalUID, agentUID int64) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentBindingForChannelUser(channel, channelAppID, channelUserID string) (*types.ChannelAgentBinding, error)
	ResolveChannelAgentDeviceAccessBindingForActorAny(actorUID, agentUID int64) (*types.ChannelAgentBinding, error)
	LinkChannelAgentBindingCanonicalUser(bindingID, actorUID, agentUID, canonicalUID int64, enableDeviceAccess bool) (*types.ChannelAgentBinding, error)
	CreateChannelIdentityMobileLink(link *types.ChannelIdentityMobileLink) (*types.ChannelIdentityMobileLink, error)
	GetChannelIdentityMobileLink(sceneKey string) (*types.ChannelIdentityMobileLink, error)
	ConsumeChannelIdentityMobileLink(sceneKey, channel, channelAppID string) (*types.ChannelIdentityMobileLink, error)
	CreateChannelGroupMobileLink(link *types.ChannelGroupMobileLink) (*types.ChannelGroupMobileLink, error)
	GetChannelGroupMobileLink(sceneKey string) (*types.ChannelGroupMobileLink, error)
	ConsumeChannelGroupMobileLink(sceneKey, channel, channelAppID string) (*types.ChannelGroupMobileLink, error)
	UpsertChannelGroupBinding(binding *types.ChannelGroupBinding) (*types.ChannelGroupBinding, error)
	ResolveChannelGroupBinding(query types.ChannelGroupBindingQuery) (*types.ChannelGroupBinding, error)
	ListChannelGroupBindingsForTopic(topicID string) ([]*types.ChannelGroupBinding, error)
	UpsertChannelAgentRoute(route *types.ChannelAgentRoute) (*types.ChannelAgentRoute, error)
	ResolveChannelAgentRoute(query types.ChannelAgentRouteQuery) (*types.ChannelAgentRoute, error)
	UpsertWeixinClawBotToken(token *types.WeixinClawBotToken) (*types.WeixinClawBotToken, error)
	GetWeixinClawBotTokenByID(id int64) (*types.WeixinClawBotToken, error)
	GetWeixinClawBotTokenByHash(tokenHash string) (*types.WeixinClawBotToken, error)
	ListActiveWeixinClawBotTokens() ([]*types.WeixinClawBotToken, error)
	UpdateWeixinClawBotTokenPollState(id int64, getUpdatesBuf string, contextTokens map[string]types.WeixinClawBotContext) error
	MarkWeixinClawBotTokenError(id int64, status string, message string) error
}

// ChannelPrivateBindingStore manages a CatsCo user's current private-channel
// selection without changing native external-channel groups.
type ChannelPrivateBindingStore interface {
	ListChannelPrivateSelections(canonicalUID int64, channel string) ([]*types.ChannelPrivateSelection, error)
	RevokeChannelPrivateSelection(canonicalUID int64, expected *types.ChannelPrivateSelection) (*types.ChannelPrivateUnbindResult, error)
}

// ChannelNativeGroupStore is the optional persistence boundary for native
// external-channel groups. It is deliberately not embedded in Store so
// existing Store implementations and mocks do not need to implement it.
type ChannelNativeGroupStore interface {
	ApplyChannelNativeGroupMembershipEvent(binding *types.ChannelNativeGroupBinding, added bool, eventID string, eventTime int64) (bool, int64, error)
	CompleteChannelNativeGroupMembershipEvent(binding *types.ChannelNativeGroupBinding, eventID string, claimToken int64) (bool, error)
	ReleaseChannelNativeGroupMembershipEvent(binding *types.ChannelNativeGroupBinding, eventID string, claimToken int64) error
	EnsureChannelNativeGroup(binding *types.ChannelNativeGroupBinding, groupName string, memberUIDs []int64) (*types.ChannelNativeGroupBinding, bool, error)
	ResolveChannelNativeGroup(channel, appID, tenantKey, conversationID string) (*types.ChannelNativeGroupBinding, error)
	SetChannelNativeGroupStatus(channel, appID, tenantKey, conversationID, status string) error
	ListChannelNativeGroupsForTopic(topicID string) ([]*types.ChannelNativeGroupBinding, error)
}

// ChannelManagedGroupStore exposes the product-level visibility boundary for
// channel-owned sessions without requiring lightweight Store mocks to support it.
type ChannelManagedGroupStore interface {
	IsChannelManagedGroup(groupID int64) (bool, error)
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
