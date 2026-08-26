// Package store defines the database boundary used by Cats Company services.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

var ErrChannelAgentBindingAlreadyLinked = errors.New("channel agent binding already linked to another canonical user")
var ErrChannelAgentAccessAlreadyLinked = errors.New("channel agent access request already linked")
var ErrChannelNativeGroupEventBusy = errors.New("channel native group event is already being processed")
var ErrProjectNameConflict = errors.New("project name already exists")
var ErrProjectNotFound = errors.New("project not found")
var ErrProjectTopicNotFound = errors.New("project or topic not found")
var ErrGroupInviteRequestNotPending = errors.New("group invite request is not pending")
var ErrConversationTaskRunTerminal = errors.New("cannot resume a terminal task run; publish a new run_id")
var ErrConversationTaskRunSuperseded = errors.New("cannot complete a superseded task run while a newer run is active")
var ErrConversationTaskStatusStale = errors.New("cannot apply an older task status update")
var ErrBotSkillMutationBusy = errors.New("another bot skill mutation is active")
var ErrBotSkillMutationRecoveryRequired = errors.New("an expired bot skill mutation requires recovery")
var ErrBotSkillMutationIdempotencyConflict = errors.New("client request id was reused with different mutation facts")
var ErrBotSkillMutationNotFound = errors.New("bot skill mutation not found")
var ErrBotSkillMutationStateConflict = errors.New("bot skill mutation status changed")
var ErrBotSkillMutationLeaseExpired = errors.New("bot skill mutation lease expired")
var ErrBotSkillMutationVersionFactsConflict = errors.New("skill version facts do not match the candidate content")
var ErrBotSkillMutationAtomicCommitRequired = errors.New("bot definition commit requires the atomic mutation commit path")
var ErrBotSkillMutationDefinitionStale = errors.New("bot definition no longer matches the mutation base")
var ErrBotSkillMutationRuntimeMismatch = errors.New("bot runtime does not match the mutation")
var ErrBotSkillMutationActivationFactConflict = errors.New("bot skill activation fact conflicts with the recorded result")

const maxConversationTaskStatusFutureClockSkew = 5 * time.Minute

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
	UpsertPushSubscription(ctx context.Context, subscription *types.PushSubscription, maxSubscriptions int) (bool, error)
	ListPushSubscriptions(ctx context.Context, uid int64) ([]*types.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, uid int64, endpoint, registrationID string) error
	DeletePushSubscriptionsByEndpoint(ctx context.Context, uid int64, endpoint string) error
	DeletePushSubscriptionsByRegistrationID(ctx context.Context, uid int64, registrationID string) error
}

// ConversationNotificationPreferenceStore persists a user's decision to mute
// browser notifications for individual conversations. A missing row means the
// conversation follows the account's normal device-level notification setting.
type ConversationNotificationPreferenceStore interface {
	ListMutedConversationTopics(ctx context.Context, userID int64, topicIDs []string) (map[string]bool, error)
	SetConversationNotificationsMuted(ctx context.Context, userID int64, topicID string, muted bool) error
	IsConversationNotificationsMuted(ctx context.Context, userID int64, topicID string) (bool, error)
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

// MessageMetadataStore atomically persists normalized messages with their
// optional metadata. It is optional so focused stores can keep implementing
// the legacy MessageStore surface.
type MessageMetadataStore interface {
	SaveMessageWithMetadata(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}) (id int64, duplicate bool, err error)
}

// ConversationTaskStatusStore persists per-source runtime state and exposes a
// backwards-compatible aggregate status per topic.
type ConversationTaskStatusStore interface {
	// UpsertConversationTaskStatus writes the resolved publisher event time back
	// to status after commit so downstream observers share the store's ordering.
	UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error)
	GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error)
	GetConversationTaskStatuses(topicIDs []string) (map[string]*types.ConversationTaskStatus, error)
}

// ValidateConversationTaskStatusTransition enforces the per-source run
// lifecycle shared by every task-status store implementation.
func ValidateConversationTaskStatusTransition(current, next *types.ConversationTaskStatus, now time.Time) error {
	if current == nil || next == nil {
		return nil
	}
	if !current.EventUpdatedAt.IsZero() && !next.EventUpdatedAt.IsZero() && next.EventUpdatedAt.Before(current.EventUpdatedAt) {
		return ErrConversationTaskStatusStale
	}
	if current.RunID == next.RunID &&
		types.IsTerminalConversationTaskState(current.State) &&
		!types.IsTerminalConversationTaskState(next.State) {
		return ErrConversationTaskRunTerminal
	}
	if current.RunID != "" && current.RunID != next.RunID &&
		types.IsTerminalConversationTaskState(next.State) &&
		(current.State == "running" || current.State == "waiting") &&
		(current.ExpiresAt == nil || current.ExpiresAt.After(now)) {
		return ErrConversationTaskRunSuperseded
	}
	return nil
}

// PrepareConversationTaskStatusForStore separates publisher event ordering
// from server-observed liveness. Callers without an explicit EventUpdatedAt
// inherit the post-lock receivedAt value, while every write receives that same
// fresh server timestamp for recovery and reaping.
func PrepareConversationTaskStatusForStore(status *types.ConversationTaskStatus, receivedAt time.Time) *types.ConversationTaskStatus {
	if status == nil {
		return nil
	}
	prepared := *status
	if prepared.EventUpdatedAt.IsZero() {
		prepared.EventUpdatedAt = receivedAt
	}
	prepared.EventUpdatedAt = BoundConversationTaskStatusEventTime(prepared.EventUpdatedAt, receivedAt)
	// Both supported databases persist task event timestamps at microsecond
	// precision. Normalize before returning the value to in-memory observers so
	// their causal ordering uses the exact precision committed by the store.
	prepared.EventUpdatedAt = prepared.EventUpdatedAt.Truncate(time.Microsecond)
	prepared.UpdatedAt = receivedAt
	return &prepared
}

// BoundConversationTaskStatusEventTime prevents a badly skewed publisher
// clock from suppressing later lifecycle events for an extended period.
func BoundConversationTaskStatusEventTime(eventAt, receivedAt time.Time) time.Time {
	if eventAt.After(receivedAt.Add(maxConversationTaskStatusFutureClockSkew)) {
		return receivedAt
	}
	return eventAt
}

// ConversationTaskStatusRecoveryStore is optional. It lets the WebSocket hub
// recover active task states left behind when a bot process disconnects.
type ConversationTaskStatusRecoveryStore interface {
	ListActiveConversationTaskStatusesForSource(sourceUID int64, updatedBefore time.Time) ([]*types.ConversationTaskStatus, error)
	// ListAllActiveConversationTaskStatusesBefore returns every source run that
	// is still active (running/waiting) and was last updated before the cutoff.
	// It feeds the periodic/startup reaper that survives process crashes and
	// transient DB failures, so a missed in-process recovery timer does not
	// leave tasks active indefinitely.
	ListAllActiveConversationTaskStatusesBefore(updatedBefore time.Time) ([]*types.ConversationTaskStatus, error)
	// MarkConversationTaskStatusStaleIfUnchanged atomically marks a source run
	// stale only when it still matches the disconnected run and was not updated
	// after the disconnection. The bool reports whether a row actually changed;
	// fanout must be skipped when it is false (a concurrent writer won).
	//
	// generation is the cluster-wide bot connection generation that the recovery
	// timer snapshotted when it was scheduled. The store verifies inside the same
	// transaction that the bot's current generation still equals generation; a
	// newer connection generation (any node) wins and this call reports
	// updated=false. This closes the cross-node race where an old timer would
	// otherwise recover work owned by a fresh connection generation.
	MarkConversationTaskStatusStaleIfUnchanged(topicID string, sourceUID int64, runID string, disconnectedAt time.Time, generation uint64) (*types.ConversationTaskStatus, bool, error)
}

// ConversationTaskGenerationStore is optional. It persists a cluster-wide
// monotonic connection generation per bot so disconnected-task recovery can
// distinguish a fresh connection generation (possibly on another node) from
// the one that actually disconnected. Implementations must make both calls
// atomic and durable; the hub bumps the generation on every bot connection.
type ConversationTaskGenerationStore interface {
	// BumpBotConnectionGeneration atomically increments the generation for a
	// bot and returns the new value. Missing rows start at 1.
	BumpBotConnectionGeneration(botUID int64) (uint64, error)
	// BotConnectionGeneration returns the current generation for a bot, or 0
	// when the bot has never connected through this store.
	BotConnectionGeneration(botUID int64) (uint64, error)
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

// BotSkillsVisibilityStore is optional so focused Store test doubles do not
// need to implement the skills visibility write path.
type BotSkillsVisibilityStore interface {
	SetBotSkillsVisibility(botUID int64, visibility string) error
}

// BotProfileStore persists owner-defined assistant identity metadata without
// widening focused Store test doubles that do not exercise profile editing.
type BotProfileStore interface {
	UpdateBotProfile(botUID int64, role, description *string) error
}

// BotArtifactPolicyStore persists whether regular Agent members may publish
// shared artifacts. Owners remain able to publish and manage artifacts.
type BotArtifactPolicyStore interface {
	GetBotArtifactUploadPolicy(botUID int64) (bool, error)
	UpdateBotArtifactUploadPolicy(botUID int64, enabled bool) error
}

// BotSkillMutationPolicyStore persists the opt-in policy for the dedicated
// Skill mutation control plane. It does not authorize general Bot edits.
type BotSkillMutationPolicyStore interface {
	GetBotSkillMutationMode(botUID int64) (types.BotSkillMutationMode, error)
	UpdateBotSkillMutationMode(botUID int64, mode types.BotSkillMutationMode) error
}

// BotSkillMutationStore persists the versioned mutation state machine. The
// implementation serializes Begin per Bot, enforces idempotency, and advances
// status with compare-and-set semantics. Advancing to definition_committed is
// deliberately rejected until the dedicated method can update BotDefinition
// and the mutation fact in one database transaction.
type BotSkillMutationStore interface {
	BeginBotSkillMutation(input types.BotSkillMutationCreateInput, now time.Time, leaseTTL time.Duration) (*types.BotSkillMutation, bool, error)
	GetBotSkillMutation(botUID, mutationID int64) (*types.BotSkillMutation, error)
	GetBotSkillMutationByRequest(input types.BotSkillMutationCreateInput) (*types.BotSkillMutation, error)
	AdvanceBotSkillMutation(botUID, mutationID, expectedLeaseGeneration int64, expected, next types.BotSkillMutationStatus, patch types.BotSkillMutationTransition, now time.Time, leaseTTL time.Duration) (*types.BotSkillMutation, error)
	CommitBotSkillMutationDefinition(botUID, mutationID, expectedLeaseGeneration int64, now time.Time, leaseTTL time.Duration) (*types.BotSkillMutation, *types.BotDefinitionRecord, error)
	RenewBotSkillMutationLease(botUID, mutationID, expectedLeaseGeneration int64, expected types.BotSkillMutationStatus, now time.Time, leaseTTL time.Duration) (*types.BotSkillMutation, error)
	// RecoverBotSkillMutationLease takes over an expired non-terminal mutation
	// with a generation CAS. It is intentionally separate from Renew so an
	// active lease can never be silently stolen.
	RecoverBotSkillMutationLease(botUID, mutationID, expectedLeaseGeneration int64, expected types.BotSkillMutationStatus, now time.Time, leaseTTL time.Duration) (*types.BotSkillMutation, error)
}

// BotSkillMutationActivationStore is deliberately separate from the mutation
// coordinator boundary. Implementations must update BotDefinition Runtime
// apply state and the mutation activation fact in one database transaction.
type BotSkillMutationActivationStore interface {
	ActivateBotSkillMutation(input types.BotSkillMutationActivationInput, now time.Time) (*types.BotSkillMutation, *types.BotDefinitionRecord, bool, error)
	RecordBotSkillMutationActivationFailure(input types.BotSkillMutationActivationFailureInput, now time.Time) (*types.BotSkillMutation, *types.BotDefinitionRecord, bool, error)
}

// BotModelConfigStore is optional so existing narrow Store test doubles do not
// need cloud-model methods. Production database adapters implement it.
type BotModelConfigStore interface {
	GetBotModelConfig(botUID int64) (*types.BotModelConfig, error)
	MarkBotModelRuntimeProtocol(botUID int64, protocol string) (*types.BotModelConfig, error)
	SaveBotDesiredModelConfig(botUID int64, kind, modelID, reasoningEffort, customCiphertext string) (*types.BotModelConfig, error)
	AckBotModelConfig(botUID, revision int64, kind, modelID, reasoningEffort, applyError string) (*types.BotModelConfig, error)
}

type BotDefinitionStore interface {
	GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error)
	CreateBotDefinitionIfAbsent(botUID int64, definition types.BotDefinition) (*types.BotDefinitionRecord, error)
	UpdateBotDefinitionModel(botUID, expectedRevision int64, model types.BotDefinitionModel) (*types.BotDefinitionRecord, error)
	UpdateBotDefinitionPrompt(botUID, expectedRevision int64, prompt types.BotPromptDefinition) (*types.BotDefinitionRecord, error)
	UpdateBotDefinitionSkills(botUID, expectedRevision int64, skills []types.BotSkillRef) (*types.BotDefinitionRecord, error)
	UpdateBotPromptVisibility(botUID int64, visibility types.BotPromptVisibility) (*types.BotDefinitionRecord, error)
	ReportBotDefaultPrompt(botUID int64, snapshot types.BotDefaultPromptSnapshot) (*types.BotDefinitionRecord, bool, error)
	AckBotDefinition(botUID, revision int64, applyError string) (*types.BotDefinitionRecord, error)
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

// ImageUpscaleTaskStore persists the owner of an asynchronous image upscale
// task so polling remains authorized across restarts and server instances.
type ImageUpscaleTaskStore interface {
	UpsertImageUpscaleTaskOwner(ctx context.Context, processID string, ownerUID int64, expiresAt time.Time) error
	GetImageUpscaleTaskOwner(ctx context.Context, processID string, now time.Time) (ownerUID int64, found bool, err error)
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
