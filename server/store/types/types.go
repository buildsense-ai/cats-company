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

// PushSubscription stores the Web Push credentials for one browser endpoint.
type PushSubscription struct {
	ID             int64     `json:"id"`
	UID            int64     `json:"uid"`
	Endpoint       string    `json:"endpoint"`
	P256DH         string    `json:"p256dh"`
	Auth           string    `json:"auth"`
	RegistrationID string    `json:"registration_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
	PriceFen      int64              `json:"price_fen"`
	Currency      string             `json:"currency"`
	SaleState     string             `json:"sale_state"`
	PurchaseLimit int                `json:"purchase_limit"`
	MonthlyBudget float64            `json:"monthly_budget_cny,omitempty"`
	ModelBudgets  map[string]float64 `json:"model_budgets,omitempty"`
	// InternalQuotaTokens is an operator-only SOL-equivalent capacity reference.
	// Relay enforcement continues to use the CNY budgets above.
	InternalQuotaTokens int64     `json:"internal_quota_tokens,omitempty"`
	DurationDays        int       `json:"duration_days"`
	State               int       `json:"state"`
	SortOrder           int       `json:"sort_order"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// CommercialOrder is an immutable purchase snapshot plus its payment and
// fulfillment state. Provider secrets and raw callback bodies are never stored.
type CommercialOrder struct {
	ID                int64              `json:"id"`
	OrderNo           string             `json:"order_no"`
	UID               int64              `json:"uid"`
	PlanID            int64              `json:"plan_id"`
	PlanSlug          string             `json:"plan_slug"`
	PlanName          string             `json:"plan_name"`
	PlanDescription   string             `json:"plan_description,omitempty"`
	PlanDurationDays  int                `json:"plan_duration_days"`
	PlanMonthlyBudget float64            `json:"plan_monthly_budget_cny,omitempty"`
	PlanModelBudgets  map[string]float64 `json:"plan_model_budgets,omitempty"`
	AmountFen         int64              `json:"amount_fen"`
	Currency          string             `json:"currency"`
	Channel           string             `json:"channel"`
	Status            string             `json:"status"`
	ProviderTradeNo   string             `json:"provider_trade_no,omitempty"`
	CheckoutURL       string             `json:"checkout_url,omitempty"`
	ClientRequestID   string             `json:"-"`
	ExpiresAt         *time.Time         `json:"expires_at,omitempty"`
	PaidAt            *time.Time         `json:"paid_at,omitempty"`
	FulfilledAt       *time.Time         `json:"fulfilled_at,omitempty"`
	ClosedAt          *time.Time         `json:"closed_at,omitempty"`
	RefundRequestNo   string             `json:"refund_request_no,omitempty"`
	RefundedAt        *time.Time         `json:"refunded_at,omitempty"`
	LastError         string             `json:"-"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// CommercialPaymentConfirmation is the normalized, verified result emitted by
// a payment provider. PayloadHash is retained for audit without storing PII.
type CommercialPaymentConfirmation struct {
	Channel         string
	EventID         string
	ProviderTradeNo string
	AmountFen       int64
	Currency        string
	PaidAt          time.Time
	PayloadHash     string
}

// CommercialRefundConfirmation is the normalized result of a provider-side
// full refund. The request identifier is deterministic so retries remain
// idempotent when the provider succeeds but the local transaction is retried.
type CommercialRefundConfirmation struct {
	Channel         string
	EventID         string
	ProviderTradeNo string
	RefundRequestNo string
	AmountFen       int64
	Currency        string
	RefundedAt      time.Time
	PayloadHash     string
}

// CommercialPaymentEvent is the operator-safe payment callback audit record.
// Raw callback payloads and payment credentials are intentionally excluded.
type CommercialPaymentEvent struct {
	ID              int64     `json:"id"`
	Channel         string    `json:"channel"`
	EventID         string    `json:"event_id"`
	OrderNo         string    `json:"order_no"`
	ProviderTradeNo string    `json:"provider_trade_no,omitempty"`
	EventType       string    `json:"event_type"`
	Status          string    `json:"status"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// CommercialManagedRelayBudget records model budgets that CatsCompany owns.
// Expiry reconciliation only removes entries present in this table.
type CommercialManagedRelayBudget struct {
	UID           int64     `json:"uid"`
	Model         string    `json:"model"`
	Provider      string    `json:"provider"`
	AllowedModels []string  `json:"allowed_models"`
	MaxLimit      float64   `json:"max_limit"`
	ResetDuration string    `json:"reset_duration"`
	UpdatedAt     time.Time `json:"updated_at"`
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
	SourceRef     string     `json:"source_ref,omitempty"`
	Model         string     `json:"model"`
	AmountCNY     float64    `json:"amount_cny"`
	ResetDuration string     `json:"reset_duration"`
	EffectiveAt   time.Time  `json:"effective_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
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

// CommercialOperatorEvent records privileged commercial operations without
// retaining request bodies or payment credentials.
type CommercialOperatorEvent struct {
	ID         int64     `json:"id"`
	Service    string    `json:"service"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetRef  string    `json:"target_ref,omitempty"`
	StatusCode int       `json:"status_code"`
	CreatedAt  time.Time `json:"created_at"`
}

// CommercialOperationsOverview is the compact internal control-plane view.
// Aggregate counts are produced in one database query to keep dashboard reads
// inexpensive even as order and ledger history grows.
type CommercialOperationsOverview struct {
	GeneratedAt                time.Time                  `json:"generated_at"`
	PlansTotal                 int64                      `json:"plans_total"`
	PlansOnSale                int64                      `json:"plans_on_sale"`
	InvitesActive              int64                      `json:"invites_active"`
	InviteRedemptions          int64                      `json:"invite_redemptions"`
	InviteRedemptionsRemaining int64                      `json:"invite_redemptions_remaining"`
	OrdersTotal                int64                      `json:"orders_total"`
	OrdersByStatus             map[string]int64           `json:"orders_by_status"`
	RevenueMonthFen            int64                      `json:"revenue_month_fen"`
	ActiveEntitlements         int64                      `json:"active_entitlements"`
	EntitlementsExpiring7D     int64                      `json:"entitlements_expiring_7d"`
	ActiveGrants               int64                      `json:"active_grants"`
	ManagedUsers               int64                      `json:"managed_users"`
	RejectedPaymentEvents24H   int64                      `json:"rejected_payment_events_24h"`
	RecentPaymentEvents        []*CommercialPaymentEvent  `json:"recent_payment_events,omitempty"`
	RecentEntitlements         []*CommercialEntitlement   `json:"recent_entitlements,omitempty"`
	RecentOperatorEvents       []*CommercialOperatorEvent `json:"recent_operator_events,omitempty"`
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
	ID            int64                  `json:"id"`
	TopicID       string                 `json:"topic_id"`
	FromUID       int64                  `json:"from_uid"`
	ClientMsgID   string                 `json:"client_msg_id,omitempty"`
	Content       string                 `json:"content,omitempty"`
	ContentBlocks []ContentBlock         `json:"content_blocks,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	MsgType       string                 `json:"msg_type"` // "text", "image", "voice", "file"
	Mode          string                 `json:"mode,omitempty"`
	Role          string                 `json:"role,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
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
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Preview     string                  `json:"preview,omitempty"`
	IsGroup     bool                    `json:"is_group"`
	GroupID     int64                   `json:"group_id,omitempty"`
	FriendID    int64                   `json:"friend_id,omitempty"`
	AvatarURL   string                  `json:"avatar_url,omitempty"`
	IsBot       bool                    `json:"is_bot,omitempty"`
	HasBot      bool                    `json:"has_bot,omitempty"`
	IsAgentTask bool                    `json:"is_agent_task,omitempty"`
	MemberCount int                     `json:"member_count,omitempty"`
	AgentIDs    []int64                 `json:"agent_ids,omitempty"`
	IsOnline    bool                    `json:"is_online,omitempty"`
	LastTime    *time.Time              `json:"last_time,omitempty"`
	LatestSeq   int64                   `json:"latest_seq,omitempty"`
	ProjectID   int64                   `json:"project_id,omitempty"`
	ProjectName string                  `json:"project_name,omitempty"`
	TaskStatus  *ConversationTaskStatus `json:"task_status,omitempty"`
}

// ConversationTaskStatus is the latest persisted task/run state for a topic.
// It is intentionally separate from normal messages so runtime status can
// survive reloads without polluting the chat transcript.
type ConversationTaskStatus struct {
	TopicID   string    `json:"topic_id"`
	RunID     string    `json:"run_id,omitempty"`
	State     string    `json:"state"`
	Summary   string    `json:"summary,omitempty"`
	Error     string    `json:"error,omitempty"`
	SourceUID int64     `json:"source_uid,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// EventUpdatedAt orders publisher lifecycle events. UpdatedAt remains the
	// server-observed liveness timestamp used by recovery and reaping.
	EventUpdatedAt time.Time  `json:"-"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// IsTerminalConversationTaskState reports whether a task run has finished.
func IsTerminalConversationTaskState(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled" || state == "stale"
}

// Project groups existing conversation topics without copying their messages.
type Project struct {
	ID        int64     `json:"id"`
	OwnerUID  int64     `json:"owner_uid"`
	Name      string    `json:"name"`
	TaskCount int64     `json:"task_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectTopic is the owner-scoped assignment of one topic to one project.
type ProjectTopic struct {
	ProjectID   int64     `json:"project_id"`
	ProjectName string    `json:"project_name"`
	TopicID     string    `json:"topic_id"`
	CreatedAt   time.Time `json:"created_at"`
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

// BotSkillsVisibility controls who can inspect a bot's redacted skill list.
type BotSkillsVisibility string

const (
	BotSkillsOwner      BotSkillsVisibility = "owner"
	BotSkillsAuthorized BotSkillsVisibility = "authorized"
	BotSkillsPublic     BotSkillsVisibility = "public"
)

// BotConfig holds configuration for a registered bot.
type BotConfig struct {
	UserID           int64               `json:"user_id"`
	OwnerID          int64               `json:"owner_id"`
	APIEndpoint      string              `json:"api_endpoint,omitempty"`
	Model            string              `json:"model,omitempty"`
	Enabled          bool                `json:"enabled"`
	Visibility       BotVisibility       `json:"visibility"`
	SkillsVisibility BotSkillsVisibility `json:"skills_visibility"`
	BodyID           string              `json:"body_id,omitempty"`
	Config           map[string]string   `json:"config,omitempty"`
}

// BotModelConfig stores the cloud-selected model and the latest device apply
// result. Custom credentials are persisted only as authenticated ciphertext.
type BotModelConfig struct {
	Kind                string `json:"kind,omitempty"`
	ModelID             string `json:"model_id"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	CustomCiphertext    string `json:"custom_ciphertext,omitempty"`
	RuntimeProtocol     string `json:"runtime_protocol,omitempty"`
	RuntimeProtocolSeen string `json:"runtime_protocol_seen_at,omitempty"`
	Revision            int64  `json:"revision"`
	UpdatedAt           string `json:"updated_at,omitempty"`
	AppliedKind         string `json:"applied_kind,omitempty"`
	AppliedModelID      string `json:"applied_model_id,omitempty"`
	AppliedReasoning    string `json:"applied_reasoning_effort,omitempty"`
	AppliedRevision     int64  `json:"applied_revision,omitempty"`
	AppliedAt           string `json:"applied_at,omitempty"`
	LastAttemptRevision int64  `json:"last_attempt_revision,omitempty"`
	LastAttemptAt       string `json:"last_attempt_at,omitempty"`
	LastError           string `json:"last_error,omitempty"`
}

const BotDefinitionSchema = "xiaoba.bot-definition.v1"

// BotDefinitionModel is the portable model selection stored in the canonical
// bot definition. API keys are encrypted before reaching this persistence
// boundary.
type BotDefinitionModel struct {
	Kind                string   `json:"kind"`
	ModelID             string   `json:"modelId,omitempty"`
	ReasoningEffort     string   `json:"reasoningEffort,omitempty"`
	Protocol            string   `json:"protocol,omitempty"`
	APIBase             string   `json:"apiBase,omitempty"`
	Model               string   `json:"model,omitempty"`
	APIKeyCiphertext    string   `json:"apiKeyCiphertext,omitempty"`
	ContextWindowTokens int64    `json:"contextWindowTokens,omitempty"`
	MaxTokens           int64    `json:"maxTokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
}

type BotPromptDefinition struct {
	Selected           string `json:"selected"`
	CustomSystemPrompt string `json:"customSystemPrompt,omitempty"`
}

// BotPromptVisibility controls who may inspect the currently selected system
// prompt. Editing remains owner-only regardless of this value.
type BotPromptVisibility string

const (
	BotPromptOwner   BotPromptVisibility = "owner"
	BotPromptFriends BotPromptVisibility = "friends"
)

// BotDefaultPromptSnapshot is runtime-observed state, kept separate from the
// owner-managed definition so reporting a bundled default never changes the
// desired revision or overwrites an owner's selection.
type BotDefaultPromptSnapshot struct {
	Content        string `json:"content"`
	ContentHash    string `json:"contentHash"`
	XiaoBaVersion  string `json:"xiaobaVersion,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	ReportedAt     string `json:"reportedAt"`
}

// BotSkillRef identifies one exact, immutable SkillHub package version.
// Package content and display metadata remain owned by SkillHub.
type BotSkillRef struct {
	Source      string `json:"source"`
	SkillID     string `json:"skillId"`
	Version     string `json:"version"`
	ContentHash string `json:"contentHash"`
}

// BotDefinition is the deliberately small portable identity of a XiaoBa bot.
// Device runtime material, sessions, quotas, and device identities do not
// belong here. Skills are immutable SkillHub references rather than packages.
type BotDefinition struct {
	Schema string               `json:"schema"`
	BotID  string               `json:"botId"`
	Model  BotDefinitionModel   `json:"model"`
	Prompt *BotPromptDefinition `json:"prompt,omitempty"`
	Skills []BotSkillRef        `json:"skills"`
}

// BotDefinitionRuntime tracks application progress without polluting the
// portable definition itself.
type BotDefinitionRuntime struct {
	DesiredRevision     int64  `json:"desiredRevision"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	RuntimeProtocol     string `json:"runtimeProtocol,omitempty"`
	RuntimeProtocolSeen string `json:"runtimeProtocolSeenAt,omitempty"`
	AppliedKind         string `json:"appliedKind,omitempty"`
	AppliedModelID      string `json:"appliedModelId,omitempty"`
	AppliedReasoning    string `json:"appliedReasoningEffort,omitempty"`
	AppliedRevision     int64  `json:"appliedRevision,omitempty"`
	AppliedAt           string `json:"appliedAt,omitempty"`
	LastAttemptRevision int64  `json:"lastAttemptRevision,omitempty"`
	LastAttemptAt       string `json:"lastAttemptAt,omitempty"`
	LastError           string `json:"lastError,omitempty"`
}

// BotDefinitionSavedCustomModel keeps the encrypted alternate custom profile
// separate from the currently selected portable model.
type BotDefinitionSavedCustomModel struct {
	APIKeyCiphertext string `json:"apiKeyCiphertext,omitempty"`
}

type BotDefinitionRecord struct {
	Definition       BotDefinition
	Runtime          BotDefinitionRuntime
	SavedCustomModel *BotDefinitionSavedCustomModel
	PromptVisibility BotPromptVisibility
	DefaultPrompt    *BotDefaultPromptSnapshot
	Exists           bool
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

const (
	ChannelNativeGroupPending      = "pending"
	ChannelNativeGroupActive       = "active"
	ChannelNativeGroupDisconnected = "disconnected"
)

// ChannelNativeGroupBinding maps a native external-channel group to the
// CatsCo group created to persist and relay that conversation.
type ChannelNativeGroupBinding struct {
	ID                    int64     `json:"id"`
	Channel               string    `json:"channel"`
	ChannelAppID          string    `json:"channel_app_id,omitempty"`
	TenantKey             string    `json:"tenant_key,omitempty"`
	ConversationID        string    `json:"conversation_id"`
	ConversationName      string    `json:"conversation_name,omitempty"`
	OperatorChannelUserID string    `json:"operator_channel_user_id,omitempty"`
	OperatorActorUID      int64     `json:"operator_actor_uid,omitempty"`
	CanonicalUID          int64     `json:"canonical_uid,omitempty"`
	GroupID               int64     `json:"group_id,omitempty"`
	TopicID               string    `json:"topic_id,omitempty"`
	SourceKind            string    `json:"source_kind,omitempty"`
	SourceGroupID         int64     `json:"source_group_id,omitempty"`
	SourceAgentUID        int64     `json:"source_agent_uid,omitempty"`
	Status                string    `json:"status"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
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

const (
	WeixinClawBotTokenActive  = "active"
	WeixinClawBotTokenRevoked = "revoked"
	WeixinClawBotTokenExpired = "expired"
)

// WeixinClawBotContext stores the reply context needed by the iLink
// sendmessage API for one Weixin ClawBot user.
type WeixinClawBotContext struct {
	ContextToken string    `json:"context_token"`
	BotUserID    string    `json:"bot_user_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// WeixinClawBotToken stores a bot_token returned by the Weixin iLink ClawBot
// QR authorization flow. BotToken is intentionally omitted from JSON output.
type WeixinClawBotToken struct {
	ID             int64                           `json:"id"`
	TokenHash      string                          `json:"token_hash"`
	BotToken       string                          `json:"-"`
	TokenLast4     string                          `json:"token_last4,omitempty"`
	Status         string                          `json:"status"`
	OwnerUID       int64                           `json:"owner_uid"`
	ILinkBotID     string                          `json:"ilink_bot_id,omitempty"`
	ILinkUserID    string                          `json:"ilink_user_id,omitempty"`
	BaseURL        string                          `json:"base_url,omitempty"`
	SourceSceneKey string                          `json:"source_scene_key,omitempty"`
	GetUpdatesBuf  string                          `json:"-"`
	ContextTokens  map[string]WeixinClawBotContext `json:"-"`
	LastPollAt     *time.Time                      `json:"last_poll_at,omitempty"`
	LastUsedAt     *time.Time                      `json:"last_used_at,omitempty"`
	LastErrorAt    *time.Time                      `json:"last_error_at,omitempty"`
	LastError      string                          `json:"last_error,omitempty"`
	CreatedAt      time.Time                       `json:"created_at"`
	UpdatedAt      time.Time                       `json:"updated_at"`
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

const (
	ChannelPrivateTargetAgent = "agent"
	ChannelPrivateTargetGroup = "group"
)

// ChannelPrivateSelection is the current private-chat target selected by one
// external-channel identity. Native channel groups are intentionally excluded.
type ChannelPrivateSelection struct {
	Channel       string    `json:"channel"`
	ChannelAppID  string    `json:"-"`
	ChannelUserID string    `json:"-"`
	ActorUID      int64     `json:"actor_uid,omitempty"`
	TargetKind    string    `json:"target_kind"`
	AgentUID      int64     `json:"agent_uid,omitempty"`
	GroupID       int64     `json:"group_id,omitempty"`
	TopicID       string    `json:"topic_id,omitempty"`
	SelectedAt    time.Time `json:"selected_at"`
}

// ChannelPrivateIdentity identifies one external private-chat identity. The
// canonical CatsCo user is always supplied separately by the authenticated caller.
type ChannelPrivateIdentity struct {
	Channel       string
	ChannelAppID  string
	ChannelUserID string
}

type ChannelPrivateUnbindResult struct {
	Revoked bool
	Changed bool
}

// Group represents a chat group.
type Group struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	OwnerID      int64     `json:"owner_id"`
	Kind         string    `json:"kind,omitempty"`
	AvatarURL    string    `json:"avatar_url,omitempty"`
	Announcement string    `json:"announcement,omitempty"`
	HasBot       bool      `json:"has_bot,omitempty"`
	MemberCount  int       `json:"member_count,omitempty"`
	AgentIDs     []int64   `json:"agent_ids,omitempty"`
	MaxMembers   int       `json:"max_members"`
	CreatedAt    time.Time `json:"created_at"`
}

const (
	GroupKindStandard       = "standard"
	GroupKindAgentTask      = "agent_task"
	GroupKindChannelManaged = "channel_managed"
)

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

// GroupInviteStatus represents the approval state of a member-proposed invite.
type GroupInviteStatus string

const (
	GroupInvitePending  GroupInviteStatus = "pending"
	GroupInviteApproved GroupInviteStatus = "approved"
	GroupInviteRejected GroupInviteStatus = "rejected"
)

// GroupInviteRequest records a group member's proposal to invite another user.
type GroupInviteRequest struct {
	ID                 int64             `json:"id"`
	GroupID            int64             `json:"group_id"`
	InviterID          int64             `json:"inviter_id"`
	InviteeID          int64             `json:"invitee_id"`
	ResolverID         int64             `json:"resolver_id,omitempty"`
	Status             GroupInviteStatus `json:"status"`
	InviterUsername    string            `json:"inviter_username,omitempty"`
	InviterDisplayName string            `json:"inviter_display_name,omitempty"`
	InviteeUsername    string            `json:"invitee_username,omitempty"`
	InviteeDisplayName string            `json:"invitee_display_name,omitempty"`
	InviteeAvatarURL   string            `json:"invitee_avatar_url,omitempty"`
	InviteeIsBot       bool              `json:"invitee_is_bot,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// RateLimitConfig defines rate limits per account type.
type RateLimitConfig struct {
	AccountType  AccountType `json:"account_type"`
	MaxPerSecond int         `json:"max_per_second"`
	MaxPerMinute int         `json:"max_per_minute"`
	BurstSize    int         `json:"burst_size"`
}
