// Package types defines core data types for Cats Company.
package types

import (
	"strings"
	"time"
)

// AccountType distinguishes human users from bots/services internally.
type AccountType string

const (
	AccountHuman   AccountType = "human"
	AccountBot     AccountType = "bot"
	AccountService AccountType = "service"
)

// User represents a registered user in the system.
type User struct {
	ID          int64       `json:"id"`
	Username    string      `json:"username"`
	Email       string      `json:"email,omitempty"`
	Phone       string      `json:"phone,omitempty"`
	DisplayName string      `json:"display_name"`
	AvatarURL   string      `json:"avatar_url,omitempty"`
	AccountType AccountType `json:"-"`             // internal only, never exposed to other users
	BotDisclose bool        `json:"bot,omitempty"` // if true, disclose bot identity to other users
	PassHash    []byte      `json:"-"`
	State       int         `json:"state"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// AuthService represents a trusted internal service which can verify CatsCo
// user tokens through the account center.
type AuthService struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix,omitempty"`
	TokenHash   string     `json:"-"`
	Scopes      []string   `json:"scopes,omitempty"`
	State       int        `json:"state"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CommercialPlan describes an operator-managed relay package. It is the
// commercial source record; relay-admin/Bifrost remain the execution layer.
type CommercialPlan struct {
	ID            int64              `json:"id"`
	Slug          string             `json:"slug"`
	Name          string             `json:"name"`
	Description   string             `json:"description,omitempty"`
	MonthlyBudget float64            `json:"monthly_budget_cny"`
	ModelBudgets  map[string]float64 `json:"model_budgets,omitempty"`
	DurationDays  int                `json:"duration_days"`
	State         int                `json:"state"`
	SortOrder     int                `json:"sort_order"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// CommercialInviteCode grants a plan entitlement when redeemed by a user.
type CommercialInviteCode struct {
	ID             int64      `json:"id"`
	Code           string     `json:"code"`
	PlanID         int64      `json:"plan_id"`
	PlanSlug       string     `json:"plan_slug,omitempty"`
	PlanName       string     `json:"plan_name,omitempty"`
	MaxRedemptions int        `json:"max_redemptions"`
	RedeemedCount  int        `json:"redeemed_count"`
	State          int        `json:"state"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Note           string     `json:"note,omitempty"`
	CreatedByUID   int64      `json:"created_by_uid,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// CommercialEntitlement is a user's active or historical package assignment.
type CommercialEntitlement struct {
	ID        int64      `json:"id"`
	UID       int64      `json:"uid"`
	PlanID    int64      `json:"plan_id"`
	PlanSlug  string     `json:"plan_slug,omitempty"`
	PlanName  string     `json:"plan_name,omitempty"`
	Source    string     `json:"source"`
	SourceRef string     `json:"source_ref,omitempty"`
	State     string     `json:"state"`
	StartsAt  time.Time  `json:"starts_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// CommercialQuotaGrant is an operator/audit-friendly grant source. The grant
// may later be synced to relay-admin/Bifrost model budgets.
type CommercialQuotaGrant struct {
	ID            int64      `json:"id"`
	UID           int64      `json:"uid"`
	PlanID        int64      `json:"plan_id,omitempty"`
	InviteCodeID  int64      `json:"invite_code_id,omitempty"`
	GrantType     string     `json:"grant_type"`
	Model         string     `json:"model"`
	AmountCNY     float64    `json:"amount_cny"`
	ResetDuration string     `json:"reset_duration"`
	EffectiveAt   time.Time  `json:"effective_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Note          string     `json:"note,omitempty"`
	OperatorUID   int64      `json:"operator_uid,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// CommercialLedgerEntry records quota mutations independently from model
// provider usage. Usage deduction still comes from relay-admin/Bifrost.
type CommercialLedgerEntry struct {
	ID         int64     `json:"id"`
	UID        int64     `json:"uid"`
	Model      string    `json:"model"`
	AmountCNY  float64   `json:"amount_cny"`
	EntryType  string    `json:"entry_type"`
	SourceType string    `json:"source_type"`
	SourceID   int64     `json:"source_id,omitempty"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// CommercialSummary is the user/admin view of commercial relay allocation.
type CommercialSummary struct {
	UID           int64                    `json:"uid"`
	Plans         []*CommercialPlan        `json:"plans,omitempty"`
	Entitlements  []*CommercialEntitlement `json:"entitlements,omitempty"`
	Grants        []*CommercialQuotaGrant  `json:"grants,omitempty"`
	Ledger        []*CommercialLedgerEntry `json:"ledger,omitempty"`
	TotalsByModel map[string]float64       `json:"totals_by_model,omitempty"`
	TotalCNY      float64                  `json:"total_cny"`
}

// FriendStatus represents the state of a friend relationship.
type FriendStatus string

const (
	FriendPending  FriendStatus = "pending"
	FriendAccepted FriendStatus = "accepted"
	FriendRejected FriendStatus = "rejected"
	FriendBlocked  FriendStatus = "blocked"
)

// FriendRequest represents a friend relationship between two users.
type FriendRequest struct {
	ID           int64        `json:"id"`
	FromUserID   int64        `json:"from_user_id"`
	ToUserID     int64        `json:"to_user_id"`
	FromUsername string       `json:"from_username,omitempty"`
	DisplayName  string       `json:"display_name,omitempty"`
	Status       FriendStatus `json:"status"`
	Message      string       `json:"message,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// Topic represents a chat topic (conversation).
type Topic struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // "p2p", "group"
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Message represents a chat message.
type Message struct {
	ID            int64          `json:"id"`
	TopicID       string         `json:"topic_id"`
	FromUID       int64          `json:"from_uid"`
	Content       string         `json:"content,omitempty"`
	ContentBlocks []ContentBlock `json:"content_blocks,omitempty"`
	MsgType       string         `json:"msg_type"` // "text", "image", "voice", "file"
	Mode          string         `json:"mode,omitempty"`
	Role          string         `json:"role,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// ContentBlock represents a block of content in code mode.
type ContentBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	Thinking  string                 `json:"thinking,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
	IsError   bool                   `json:"is_error,omitempty"`
}

// ConversationSummary is the lightweight chat-list payload for a topic.
type ConversationSummary struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Preview   string     `json:"preview,omitempty"`
	IsGroup   bool       `json:"is_group"`
	GroupID   int64      `json:"group_id,omitempty"`
	FriendID  int64      `json:"friend_id,omitempty"`
	AvatarURL string     `json:"avatar_url,omitempty"`
	IsBot     bool       `json:"is_bot,omitempty"`
	HasBot    bool       `json:"has_bot,omitempty"`
	IsOnline  bool       `json:"is_online,omitempty"`
	LastTime  *time.Time `json:"last_time,omitempty"`
	LatestSeq int64      `json:"latest_seq,omitempty"`
}

// RichContent is the unified message payload structure.
// All messages use this format: { "type": "text"|"image"|"file"|"card"|"link_preview", "payload": {...} }
type RichContent struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// ImagePayload is the payload for image messages.
type ImagePayload struct {
	FileKey   string `json:"file_key"`
	URL       string `json:"url"`
	Thumbnail string `json:"thumbnail,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// FilePayload is the payload for file messages.
type FilePayload struct {
	FileKey  string `json:"file_key"`
	URL      string `json:"url"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type,omitempty"`
}

// FeedbackAttachment is a screenshot or file attached to a user feedback report.
type FeedbackAttachment struct {
	FileKey string `json:"file_key"`
	URL     string `json:"url"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Type    string `json:"type,omitempty"`
}

// FeedbackReport is a user-submitted bug report or product suggestion.
type FeedbackReport struct {
	ID          int64                `json:"id"`
	UserID      int64                `json:"user_id"`
	Category    string               `json:"category"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description"`
	PageURL     string               `json:"page_url,omitempty"`
	UserAgent   string               `json:"user_agent,omitempty"`
	Status      string               `json:"status"`
	Attachments []FeedbackAttachment `json:"attachments,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

// LinkPreviewPayload is the payload for link preview messages.
type LinkPreviewPayload struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
}

// CardPayload is the payload for card messages (structured content).
type CardPayload struct {
	Title   string       `json:"title"`
	Text    string       `json:"text,omitempty"`
	Image   string       `json:"image,omitempty"`
	Buttons []CardButton `json:"buttons,omitempty"`
}

// CardButton is a button in a card message.
type CardButton struct {
	Label  string `json:"label"`
	Action string `json:"action"` // "url", "copy", "callback"
	Value  string `json:"value"`
}

// BotVisibility controls whether a bot is discoverable via search.
type BotVisibility string

const (
	BotPublic  BotVisibility = "public"
	BotPrivate BotVisibility = "private"
)

// BotConfig holds configuration for a registered bot.
type BotConfig struct {
	UserID      int64             `json:"user_id"`
	OwnerID     int64             `json:"owner_id"`
	APIEndpoint string            `json:"api_endpoint,omitempty"`
	Model       string            `json:"model,omitempty"`
	Enabled     bool              `json:"enabled"`
	Visibility  BotVisibility     `json:"visibility"`
	BodyID      string            `json:"body_id,omitempty"`
	Config      map[string]string `json:"config,omitempty"`
}

const (
	ChannelAgentAccessPublic           = "public"
	ChannelAgentAccessApprovalRequired = "approval_required"
)

const (
	ChannelAgentBindingActive          = "active"
	ChannelAgentBindingPendingLogin    = "pending_login"
	ChannelAgentBindingPendingApproval = "pending_approval"
	ChannelAgentBindingRejected        = "rejected"
	ChannelAgentBindingRevoked         = "revoked"
)

// NormalizeChannelAgentAccessMode returns the persisted entry access mode.
func NormalizeChannelAgentAccessMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ChannelAgentAccessPublic:
		return ChannelAgentAccessPublic
	case ChannelAgentAccessApprovalRequired:
		return ChannelAgentAccessApprovalRequired
	default:
		return ChannelAgentAccessApprovalRequired
	}
}

// ChannelAgentEntry is a shareable QR/link entry for a virtual employee on an
// external chat channel such as Weixin or Feishu.
type ChannelAgentEntry struct {
	ID           int64      `json:"id"`
	SceneKey     string     `json:"scene_key"`
	Channel      string     `json:"channel"`
	ChannelAppID string     `json:"channel_app_id,omitempty"`
	AccessMode   string     `json:"access_mode"`
	OwnerUID     int64      `json:"owner_uid"`
	AgentUID     int64      `json:"agent_uid"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

// ChannelAgentAccessRequest records a private entry scan which still needs the
// agent owner to accept the channel actor as an agent friend.
type ChannelAgentAccessRequest struct {
	ID                      int64      `json:"id"`
	EntryID                 int64      `json:"entry_id"`
	Channel                 string     `json:"channel"`
	ChannelAppID            string     `json:"channel_app_id,omitempty"`
	ChannelUserID           string     `json:"channel_user_id"`
	ChannelConversationID   string     `json:"channel_conversation_id,omitempty"`
	ChannelConversationType string     `json:"channel_conversation_type,omitempty"`
	ActorUID                int64      `json:"actor_uid"`
	OwnerUID                int64      `json:"owner_uid"`
	AgentUID                int64      `json:"agent_uid"`
	Status                  string     `json:"status"`
	ReviewedByUID           int64      `json:"reviewed_by_uid,omitempty"`
	RequestedAt             time.Time  `json:"requested_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	ReviewedAt              *time.Time `json:"reviewed_at,omitempty"`
}

// ChannelAgentBinding records which external-channel identity should talk to
// which virtual employee by default.
type ChannelAgentBinding struct {
	ID                      int64      `json:"id"`
	Channel                 string     `json:"channel"`
	ChannelAppID            string     `json:"channel_app_id,omitempty"`
	ChannelUserID           string     `json:"channel_user_id"`
	ChannelConversationID   string     `json:"channel_conversation_id,omitempty"`
	ChannelConversationType string     `json:"channel_conversation_type,omitempty"`
	ActorUID                int64      `json:"actor_uid,omitempty"`
	CanonicalUID            int64      `json:"canonical_uid,omitempty"`
	DeviceAccessEnabled     bool       `json:"device_access_enabled,omitempty"`
	OwnerUID                int64      `json:"owner_uid"`
	AgentUID                int64      `json:"agent_uid"`
	EntryID                 int64      `json:"entry_id,omitempty"`
	Status                  string     `json:"status"`
	BoundAt                 time.Time  `json:"bound_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LastUsedAt              *time.Time `json:"last_used_at,omitempty"`
}

// ChannelIdentityMobileLink is a one-time QR scene generated from an existing
// CatsCo web identity so the same human user can bind Weixin/Feishu without
// repeating the full CatsCo login or friend-request flow.
type ChannelIdentityMobileLink struct {
	ID           int64      `json:"id"`
	SceneKey     string     `json:"scene_key"`
	EntryID      int64      `json:"entry_id"`
	Channel      string     `json:"channel"`
	ChannelAppID string     `json:"channel_app_id,omitempty"`
	CanonicalUID int64      `json:"canonical_uid"`
	Status       string     `json:"status"`
	ExpiresAt    time.Time  `json:"expires_at"`
	ConsumedAt   *time.Time `json:"consumed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ChannelGroupMobileLink is a one-time QR scene generated from a CatsCo group
// member so the same human can continue that group from Weixin/Feishu.
type ChannelGroupMobileLink struct {
	ID           int64      `json:"id"`
	SceneKey     string     `json:"scene_key"`
	Channel      string     `json:"channel"`
	ChannelAppID string     `json:"channel_app_id,omitempty"`
	CanonicalUID int64      `json:"canonical_uid"`
	GroupID      int64      `json:"group_id"`
	TopicID      string     `json:"topic_id"`
	Status       string     `json:"status"`
	ExpiresAt    time.Time  `json:"expires_at"`
	ConsumedAt   *time.Time `json:"consumed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ChannelGroupBinding maps a Weixin/Feishu identity to a CatsCo group topic.
// It is separate from ChannelAgentBinding: the user is joining an existing
// group conversation, not selecting or adding a virtual employee.
type ChannelGroupBinding struct {
	ID                      int64      `json:"id"`
	Channel                 string     `json:"channel"`
	ChannelAppID            string     `json:"channel_app_id,omitempty"`
	ChannelUserID           string     `json:"channel_user_id"`
	ChannelConversationID   string     `json:"channel_conversation_id,omitempty"`
	ChannelConversationType string     `json:"channel_conversation_type,omitempty"`
	ActorUID                int64      `json:"actor_uid,omitempty"`
	CanonicalUID            int64      `json:"canonical_uid"`
	GroupID                 int64      `json:"group_id"`
	TopicID                 string     `json:"topic_id"`
	Status                  string     `json:"status"`
	BoundAt                 time.Time  `json:"bound_at"`
	SelectedAt              time.Time  `json:"selected_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LastUsedAt              *time.Time `json:"last_used_at,omitempty"`
}

// ChannelAgentBindingQuery is the normalized lookup key used by channel
// adapters before they create a model session route.
type ChannelAgentBindingQuery struct {
	Channel                 string
	ChannelAppID            string
	ChannelUserID           string
	ChannelConversationID   string
	ChannelConversationType string
	AgentUID                int64
	ActorUID                int64
}

// ChannelGroupBindingQuery is the normalized lookup key used by channel
// adapters before they route an inbound mobile-channel message to a group.
type ChannelGroupBindingQuery struct {
	Channel                 string
	ChannelAppID            string
	ChannelUserID           string
	ChannelConversationID   string
	ChannelConversationType string
	ActorUID                int64
	GroupID                 int64
	TopicID                 string
}

// ChannelAgentRoute records the current virtual employee selected inside one
// external-channel conversation. It is deliberately separate from bindings:
// bindings are access relationships, routes are the latest user choice.
type ChannelAgentRoute struct {
	ID                      int64      `json:"id"`
	Channel                 string     `json:"channel"`
	ChannelAppID            string     `json:"channel_app_id,omitempty"`
	ChannelUserID           string     `json:"channel_user_id"`
	ChannelConversationID   string     `json:"channel_conversation_id,omitempty"`
	ChannelConversationType string     `json:"channel_conversation_type,omitempty"`
	ActorUID                int64      `json:"actor_uid,omitempty"`
	AgentUID                int64      `json:"agent_uid"`
	Source                  string     `json:"source,omitempty"`
	SelectedAt              time.Time  `json:"selected_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LastUsedAt              *time.Time `json:"last_used_at,omitempty"`
}

// ChannelAgentRouteQuery is the normalized lookup key for current-agent
// selection in an external-channel conversation.
type ChannelAgentRouteQuery struct {
	Channel                 string
	ChannelAppID            string
	ChannelUserID           string
	ChannelConversationID   string
	ChannelConversationType string
	ActorUID                int64
}

// Group represents a chat group.
type Group struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	OwnerID      int64     `json:"owner_id"`
	AvatarURL    string    `json:"avatar_url,omitempty"`
	Announcement string    `json:"announcement,omitempty"`
	HasBot       bool      `json:"has_bot,omitempty"`
	MaxMembers   int       `json:"max_members"`
	CreatedAt    time.Time `json:"created_at"`
}

// GroupMember represents a member of a group.
type GroupMember struct {
	ID       int64     `json:"id"`
	GroupID  int64     `json:"group_id"`
	UserID   int64     `json:"user_id"`
	Role     string    `json:"role"` // "owner", "admin", "member"
	Muted    bool      `json:"muted,omitempty"`
	JoinedAt time.Time `json:"joined_at"`
	// Joined fields from user table (populated by queries)
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	IsBot       bool   `json:"is_bot,omitempty"`
}

// RateLimitConfig defines rate limits per account type.
type RateLimitConfig struct {
	AccountType  AccountType `json:"account_type"`
	MaxPerSecond int         `json:"max_per_second"`
	MaxPerMinute int         `json:"max_per_minute"`
	BurstSize    int         `json:"burst_size"`
}
