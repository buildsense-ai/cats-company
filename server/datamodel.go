// Package server defines the wire protocol data model for Cats Company.
package server

import (
	"encoding/json"

	"github.com/openchat/openchat/server/store/types"
)

// ClientMessage is the top-level client-to-server message envelope.
type ClientMessage struct {
	Hi          *MsgClientHi     `json:"hi,omitempty"`
	Acc         *MsgClientAcc    `json:"acc,omitempty"`
	Login       *MsgClientLogin  `json:"login,omitempty"`
	Sub         *MsgClientSub    `json:"sub,omitempty"`
	Pub         *MsgClientPub    `json:"pub,omitempty"`
	Get         *MsgClientGet    `json:"get,omitempty"`
	Set         *MsgClientSet    `json:"set,omitempty"`
	Del         *MsgClientDel    `json:"del,omitempty"`
	Note        *MsgClientNote   `json:"note,omitempty"`
	Friend      *MsgClientFriend `json:"friend,omitempty"`
	DeviceRPC   *MsgDeviceRPC    `json:"device_rpc,omitempty"`
	ThinToolRPC *MsgThinToolRPC  `json:"thin_tool_rpc,omitempty"`
	// ArtifactResult accepts only a receipt from a connected human preview.
	// Result submission itself uses the authenticated Bot HTTP endpoint.
	ArtifactResult *MsgArtifactResult `json:"artifact_result,omitempty"`
	// SkillMutationGrant is accepted only from the current authenticated Bot
	// runtime connection. It issues authorization; it never mutates a Skill.
	SkillMutationGrant *MsgSkillMutationGrant `json:"skill_mutation_grant,omitempty"`
}

// ServerMessage is the top-level server-to-client message envelope.
type ServerMessage struct {
	Ctrl               *MsgServerCtrl                `json:"ctrl,omitempty"`
	Data               *MsgServerData                `json:"data,omitempty"`
	TaskStatus         *types.ConversationTaskStatus `json:"task_status,omitempty"`
	Pres               *MsgServerPres                `json:"pres,omitempty"`
	Meta               *MsgServerMeta                `json:"meta,omitempty"`
	Info               *MsgServerInfo                `json:"info,omitempty"`
	Friend             *MsgServerFriend              `json:"friend,omitempty"`
	Notification       *MsgServerNotification        `json:"notification,omitempty"`
	DeviceRPC          *MsgDeviceRPC                 `json:"device_rpc,omitempty"`
	ThinToolRPC        *MsgThinToolRPC               `json:"thin_tool_rpc,omitempty"`
	ArtifactResult     *MsgArtifactResult            `json:"artifact_result,omitempty"`
	SkillMutationGrant *MsgSkillMutationGrant        `json:"skill_mutation_grant,omitempty"`

	// suppressPushNotification is server-only provenance. It must never be
	// serialized to a web client or be set from client-provided metadata.
	suppressPushNotification bool
	// artifactContextRef is an in-memory, recipient-scoped delivery capability.
	// It is never serialized directly and never persisted with a message.
	artifactContextRef *artifactContextDeliveryRef
}

// --- Client messages ---

type MsgClientHi struct {
	ID                 string             `json:"id,omitempty"`
	UserAgent          string             `json:"ua,omitempty"`
	Version            string             `json:"ver,omitempty"`
	Lang               string             `json:"lang,omitempty"`
	Visibility         string             `json:"visibility,omitempty"`
	PushSubscriptionID string             `json:"push_subscription_id,omitempty"`
	ActiveTopic        string             `json:"active_topic,omitempty"`
	Focused            bool               `json:"focused,omitempty"`
	Device             *MsgClientHiDevice `json:"device,omitempty"`
}

type MsgClientHiDevice struct {
	DeviceID       string             `json:"device_id"`
	DisplayName    string             `json:"display_name,omitempty"`
	BodyID         string             `json:"body_id,omitempty"`
	InstallationID string             `json:"installation_id,omitempty"`
	RuntimeRole    string             `json:"runtime_role,omitempty"`
	OS             string             `json:"os,omitempty"`
	Status         string             `json:"status,omitempty"`
	Capabilities   []string           `json:"capabilities,omitempty"`
	ModelStatus    *DeviceModelStatus `json:"model_status,omitempty"`
}

type MsgClientAcc struct {
	ID     string            `json:"id,omitempty"`
	User   string            `json:"user,omitempty"`
	Scheme string            `json:"scheme,omitempty"`
	Secret string            `json:"secret,omitempty"`
	Desc   map[string]string `json:"desc,omitempty"`
}

type MsgClientLogin struct {
	ID     string `json:"id,omitempty"`
	Scheme string `json:"scheme,omitempty"`
	Secret string `json:"secret,omitempty"`
}

type MsgClientSub struct {
	ID    string `json:"id,omitempty"`
	Topic string `json:"topic"`
}

type MsgClientPub struct {
	ID            string                 `json:"id,omitempty"`
	Topic         string                 `json:"topic"`
	ClientMsgID   string                 `json:"client_msg_id,omitempty"`
	Content       json.RawMessage        `json:"content,omitempty"`
	ContentBlocks []types.ContentBlock   `json:"content_blocks,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	MsgType       string                 `json:"msg_type,omitempty"`
	Type          string                 `json:"type,omitempty"`
	Mode          string                 `json:"mode,omitempty"`
	Role          string                 `json:"role,omitempty"`
	ReplyTo       int                    `json:"reply_to,omitempty"`
	Mentions      []string               `json:"mentions,omitempty"`
}

type MsgClientGet struct {
	ID    string `json:"id,omitempty"`
	Topic string `json:"topic"`
	What  string `json:"what,omitempty"`
	SeqID int    `json:"seq,omitempty"` // For history requests: fetch messages after this seq
}

type MsgClientSet struct {
	ID    string      `json:"id,omitempty"`
	Topic string      `json:"topic"`
	Desc  interface{} `json:"desc,omitempty"`
}

type MsgClientDel struct {
	ID    string `json:"id,omitempty"`
	Topic string `json:"topic,omitempty"`
	What  string `json:"what,omitempty"`
}

type MsgClientNote struct {
	Topic              string `json:"topic"`
	What               string `json:"what"` // "read", "recv", "kp" (key press / typing), "attention"
	SeqID              int    `json:"seq,omitempty"`
	Visibility         string `json:"visibility,omitempty"`
	PushSubscriptionID string `json:"push_subscription_id,omitempty"`
	ActiveTopic        string `json:"active_topic,omitempty"`
	Focused            bool   `json:"focused,omitempty"`
}

// MsgClientFriend is the new friend protocol message.
type MsgClientFriend struct {
	ID     string `json:"id,omitempty"`
	Action string `json:"action"` // "request", "accept", "reject", "block", "remove"
	UserID int64  `json:"user_id"`
	Msg    string `json:"msg,omitempty"`
}

// MsgDeviceRPC carries a backend-routed request/result between an agent body and
// a selected user device. It is intentionally outside regular chat data so RPC
// traffic is not persisted or replayed as conversation history.
type MsgDeviceRPC struct {
	ID                   string                 `json:"id,omitempty"`
	Type                 string                 `json:"type"` // "request" or "result"
	RequestID            string                 `json:"request_id"`
	GrantID              string                 `json:"grant_id,omitempty"`
	SessionKey           string                 `json:"session_key,omitempty"`
	TopicID              string                 `json:"topic_id,omitempty"`
	TopicType            string                 `json:"topic_type,omitempty"`
	ActorUserID          string                 `json:"actor_user_id,omitempty"`
	OwnerUserID          string                 `json:"owner_user_id,omitempty"`
	IdentitySource       string                 `json:"identity_source,omitempty"`
	AgentID              string                 `json:"agent_id,omitempty"`
	AgentBodyID          string                 `json:"agent_body_id,omitempty"`
	DeviceID             string                 `json:"device_id,omitempty"`
	DeviceBodyID         string                 `json:"device_body_id,omitempty"`
	DeviceInstallationID string                 `json:"device_installation_id,omitempty"`
	Operation            string                 `json:"operation,omitempty"`
	ToolName             string                 `json:"tool_name,omitempty"`
	Payload              map[string]interface{} `json:"payload,omitempty"`
	Result               interface{}            `json:"result,omitempty"`
	Error                *MsgDeviceRPCError     `json:"error,omitempty"`
	CreatedAt            int64                  `json:"created_at,omitempty"`
	ExpiresAt            int64                  `json:"expires_at,omitempty"`
}

type MsgDeviceRPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MsgSkillMutationGrant is the candidate-bound authorization exchange between
// one active Bot runtime and CatsCo. Actor, Bot, and runtime identities in the
// result are server-attributed and cannot be supplied by the request.
type MsgSkillMutationGrant struct {
	ID                         string             `json:"id,omitempty"`
	Type                       string             `json:"type"` // "request" or "result"
	RequestID                  string             `json:"request_id"`
	ClientRequestID            string             `json:"client_request_id,omitempty"`
	SourceTopicID              string             `json:"source_topic_id,omitempty"`
	SourceMessageID            int64              `json:"source_message_id,omitempty"`
	LocalSkillID               string             `json:"local_skill_id,omitempty"`
	Operation                  string             `json:"operation,omitempty"`
	CandidateContentHash       string             `json:"candidate_content_hash,omitempty"`
	CandidateSizeBytes         int64              `json:"candidate_size_bytes,omitempty"`
	ExpectedDefinitionRevision int64              `json:"expected_definition_revision,omitempty"`
	ExpectedPreviousHash       string             `json:"expected_previous_content_hash,omitempty"`
	BeforeReference            *types.BotSkillRef `json:"before_reference,omitempty"`
	Grant                      string             `json:"grant,omitempty"`
	ExpiresAt                  int64              `json:"expires_at,omitempty"`
	ActorUserID                string             `json:"actor_user_id,omitempty"`
	AgentID                    string             `json:"agent_id,omitempty"`
	RuntimeBodyID              string             `json:"runtime_body_id,omitempty"`
	Error                      *MsgDeviceRPCError `json:"error,omitempty"`
}

// MsgThinToolRPC carries a direct tool request/result between two connected
// runtimes. Cats Company only routes this message; tool permissions, argument
// validity, and execution errors are returned by the target runtime as-is.
type MsgThinToolRPC struct {
	ID                string                 `json:"id,omitempty"`
	Type              string                 `json:"type"` // "request" or "result"
	RequestID         string                 `json:"request_id"`
	TargetOwnerUserID string                 `json:"target_owner_user_id,omitempty"`
	TargetDeviceID    string                 `json:"target_device_id,omitempty"`
	DeviceID          string                 `json:"device_id,omitempty"`
	ToolName          string                 `json:"tool_name,omitempty"`
	Payload           map[string]interface{} `json:"payload,omitempty"`
	Result            interface{}            `json:"result,omitempty"`
	Error             *MsgDeviceRPCError     `json:"error,omitempty"`
	CreatedAt         int64                  `json:"created_at,omitempty"`
	ExpiresAt         int64                  `json:"expires_at,omitempty"`
}

// MsgArtifactResult is the transient transport between CatsCo and the exact
// browser preview that created an Artifact context snapshot. It is never a
// chat message and is never persisted as application data.
type MsgArtifactResult struct {
	Type                  string          `json:"type"` // "request" or "receipt"
	OriginNodeID          string          `json:"origin_node_id,omitempty"`
	ActorUID              string          `json:"actor_uid,omitempty"`
	ContextRef            string          `json:"context_ref,omitempty"`
	WritebackRef          string          `json:"writeback_ref,omitempty"`
	TopicID               string          `json:"topic_id,omitempty"`
	AgentUID              string          `json:"agent_uid,omitempty"`
	ArtifactID            string          `json:"artifact_id,omitempty"`
	DisplayedVersion      int64           `json:"displayed_version,omitempty"`
	SinkID                string          `json:"sink_id,omitempty"`
	ResultID              string          `json:"result_id"`
	ExpectedStateRevision string          `json:"expected_state_revision,omitempty"`
	Payload               json.RawMessage `json:"payload,omitempty"`
	Receipt               json.RawMessage `json:"receipt,omitempty"`
}

// --- Server messages ---

type MsgServerCtrl struct {
	ID     string      `json:"id,omitempty"`
	Topic  string      `json:"topic,omitempty"`
	Code   int         `json:"code"`
	Text   string      `json:"text,omitempty"`
	Params interface{} `json:"params,omitempty"`
}

type MsgServerData struct {
	Topic         string                 `json:"topic"`
	From          string                 `json:"from,omitempty"`
	SeqID         int                    `json:"seq"`
	Content       interface{}            `json:"content"`
	Type          string                 `json:"type,omitempty"`
	MsgType       string                 `json:"msg_type,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	ContentBlocks []types.ContentBlock   `json:"content_blocks,omitempty"`
	Mode          string                 `json:"mode,omitempty"`
	Role          string                 `json:"role,omitempty"`
	ReplyTo       int                    `json:"reply_to,omitempty"`
	Mentions      []string               `json:"mentions,omitempty"` // Structured @mention targets (for example ["usr123"] or ["all"]).
	MemberCount   int                    `json:"member_count,omitempty"`
}

type MsgServerPres struct {
	Topic string `json:"topic"`
	What  string `json:"what"` // "on", "off", "msg", "upd"
	Src   string `json:"src,omitempty"`
}

type MsgServerMeta struct {
	ID    string      `json:"id,omitempty"`
	Topic string      `json:"topic"`
	Desc  interface{} `json:"desc,omitempty"`
	Sub   interface{} `json:"sub,omitempty"`
}

type MsgServerInfo struct {
	Topic string `json:"topic"`
	From  string `json:"from"`
	What  string `json:"what"` // "read", "recv", "kp"
	SeqID int    `json:"seq,omitempty"`
}

// MsgServerFriend is the server-side friend notification.
type MsgServerFriend struct {
	Action string `json:"action"` // "request", "accepted", "rejected", "blocked", "removed"
	From   int64  `json:"from"`
	To     int64  `json:"to"`
	Msg    string `json:"msg,omitempty"`
}

// MsgServerNotification carries a privacy-minimized, non-chat notification.
// Keep this envelope free of artifact metadata; clients only need the event
// type and the already-approved user-visible message.
type MsgServerNotification struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
