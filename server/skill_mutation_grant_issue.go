package server

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	skillMutationGrantMessageTypeRequest = "request"
	skillMutationGrantMessageTypeResult  = "result"
	skillMutationSourceMessageMaxAge     = 24 * time.Hour
)

var (
	errSkillMutationGrantUnavailable  = errors.New("skill mutation authorization is unavailable")
	errSkillMutationRuntimeIdentity   = errors.New("current Bot runtime identity is not active")
	errSkillMutationRuntimeCredential = errors.New("trusted Bot Runtime credential is required")
	errSkillMutationSourceMessage     = errors.New("source message is not an eligible human message for this Bot")
	errSkillMutationActorForbidden    = errors.New("source actor is not allowed to mutate this Bot")
	errSkillMutationDefinitionStale   = errors.New("BotDefinition revision or previous Skill version is stale")
)

type skillMutationPolicyReader interface {
	GetBotSkillMutationMode(botUID int64) (types.BotSkillMutationMode, error)
}

type skillMutationDefinitionReader interface {
	GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error)
}

func (h *Hub) handleSkillMutationGrant(client *Client, msg *MsgSkillMutationGrant) {
	if msg == nil {
		return
	}
	requestID, ok := normalizeDeviceRPCRequestID(msg.RequestID)
	if !ok {
		h.sendSkillMutationGrantError(client, msg, "invalid_request", "request_id is required")
		return
	}
	msg.RequestID = requestID
	if strings.ToLower(strings.TrimSpace(msg.Type)) != skillMutationGrantMessageTypeRequest {
		h.sendSkillMutationGrantError(client, msg, "invalid_request", "unknown skill_mutation_grant type")
		return
	}
	if h == nil || h.db == nil || h.skillMutationGrants == nil {
		h.sendSkillMutationGrantError(client, msg, "unavailable", errSkillMutationGrantUnavailable.Error())
		return
	}
	input, err := h.authorizeSkillMutationGrant(client, msg)
	if err != nil {
		code := "invalid_request"
		switch {
		case errors.Is(err, errSkillMutationGrantUnavailable):
			code = "unavailable"
		case errors.Is(err, errSkillMutationRuntimeIdentity):
			code = "runtime_identity_invalid"
		case errors.Is(err, errSkillMutationRuntimeCredential):
			code = "runtime_credential_required"
		case errors.Is(err, errSkillMutationSourceMessage):
			code = "source_message_invalid"
		case errors.Is(err, errSkillMutationActorForbidden):
			code = "forbidden"
		case errors.Is(err, errSkillMutationDefinitionStale):
			code = "definition_stale"
		}
		h.sendSkillMutationGrantError(client, msg, code, err.Error())
		return
	}
	raw, claims, err := h.skillMutationGrants.issue(input)
	if err != nil {
		h.sendSkillMutationGrantError(client, msg, "invalid_request", "invalid Skill mutation candidate")
		return
	}
	h.SendToClient(client, &ServerMessage{SkillMutationGrant: &MsgSkillMutationGrant{
		ID:              msg.ID,
		Type:            skillMutationGrantMessageTypeResult,
		RequestID:       requestID,
		ClientRequestID: claims.ClientRequestID,
		Grant:           raw,
		ExpiresAt:       claims.ExpiresAt.Time.UnixMilli(),
		ActorUserID:     formatUID(claims.ActorUserUID),
		AgentID:         formatUID(claims.BotUID),
		RuntimeBodyID:   claims.RuntimeBodyID,
	}})
}

func (h *Hub) authorizeSkillMutationGrant(client *Client, msg *MsgSkillMutationGrant) (skillMutationGrantInput, error) {
	if h == nil || h.db == nil || h.bodyLeases == nil || client == nil || msg == nil ||
		client.hub != h || client.accountType != types.AccountBot || client.uid <= 0 ||
		strings.TrimSpace(client.bodyID) == "" || isLegacyBotBodyID(client.bodyID) ||
		!h.bodyLeases.isCurrent(client.uid, client.bodyID, client.connectionID) {
		return skillMutationGrantInput{}, errSkillMutationRuntimeIdentity
	}
	runtimeCredential := client.botRuntimeCredential
	if runtimeCredential == nil || runtimeCredential.BotUID != client.uid ||
		runtimeCredential.BodyID != client.bodyID || runtimeCredential.InstallationID != client.installationID ||
		!botRuntimeCredentialHasScope(runtimeCredential, botRuntimeSkillMutationScope) {
		return skillMutationGrantInput{}, errSkillMutationRuntimeCredential
	}
	messages, ok := h.db.(store.MessageAroundStore)
	if !ok {
		return skillMutationGrantInput{}, fmt.Errorf("message lookup: %w", errSkillMutationGrantUnavailable)
	}
	source, err := exactSkillMutationSourceMessage(messages, msg.SourceTopicID, msg.SourceMessageID)
	if err != nil {
		return skillMutationGrantInput{}, errSkillMutationSourceMessage
	}
	now := h.skillMutationGrants.now().UTC()
	if source.CreatedAt.IsZero() || source.CreatedAt.After(now.Add(skillMutationGrantClockSkew)) ||
		now.Sub(source.CreatedAt.UTC()) > skillMutationSourceMessageMaxAge {
		return skillMutationGrantInput{}, errSkillMutationSourceMessage
	}
	actor, err := h.db.GetUser(source.FromUID)
	if err != nil || actor == nil || actor.State != 0 || actor.AccountType != types.AccountHuman ||
		strings.HasPrefix(strings.TrimSpace(actor.Username), "ch_weixin_") ||
		strings.HasPrefix(strings.TrimSpace(actor.Username), "ch_weixin_clawbot_") ||
		strings.HasPrefix(strings.TrimSpace(actor.Username), "ch_feishu_") {
		return skillMutationGrantInput{}, errSkillMutationSourceMessage
	}
	if err := h.validateSkillMutationConversationAccess(source.TopicID, source.FromUID, client.uid); err != nil {
		return skillMutationGrantInput{}, err
	}
	ownerUID, err := h.db.GetBotOwner(client.uid)
	if err != nil || ownerUID <= 0 {
		return skillMutationGrantInput{}, fmt.Errorf("Bot owner lookup: %w", errSkillMutationGrantUnavailable)
	}
	if runtimeCredential.OwnerUID != ownerUID {
		return skillMutationGrantInput{}, errSkillMutationRuntimeCredential
	}
	policies, ok := h.db.(skillMutationPolicyReader)
	if !ok {
		return skillMutationGrantInput{}, fmt.Errorf("mutation policy store: %w", errSkillMutationGrantUnavailable)
	}
	mode, err := policies.GetBotSkillMutationMode(client.uid)
	if err != nil {
		return skillMutationGrantInput{}, fmt.Errorf("mutation policy lookup: %w", errSkillMutationGrantUnavailable)
	}
	if mode != types.BotSkillMutationOwnerOnly && mode != types.BotSkillMutationSharedLive {
		return skillMutationGrantInput{}, fmt.Errorf("mutation policy value: %w", errSkillMutationGrantUnavailable)
	}
	if mode == types.BotSkillMutationOwnerOnly && source.FromUID != ownerUID {
		return skillMutationGrantInput{}, errSkillMutationActorForbidden
	}
	definitions, ok := h.db.(skillMutationDefinitionReader)
	if !ok {
		return skillMutationGrantInput{}, fmt.Errorf("BotDefinition store: %w", errSkillMutationGrantUnavailable)
	}
	record, err := definitions.GetBotDefinition(client.uid)
	if err != nil || record == nil || !record.Exists {
		return skillMutationGrantInput{}, fmt.Errorf("BotDefinition lookup: %w", errSkillMutationGrantUnavailable)
	}
	if record.Runtime.DesiredRevision != msg.ExpectedDefinitionRevision {
		return skillMutationGrantInput{}, errSkillMutationDefinitionStale
	}
	operation, ok := types.ParseBotSkillMutationOperation(msg.Operation)
	if !ok || operation == types.BotSkillMutationRollback {
		return skillMutationGrantInput{}, errors.New("invalid Skill mutation operation")
	}
	if operation == types.BotSkillMutationReplace && !definitionContainsExactSkillReference(record, msg.BeforeReference) {
		return skillMutationGrantInput{}, errSkillMutationDefinitionStale
	}
	mutation := types.BotSkillMutationCreateInput{
		BotUID:                      client.uid,
		LocalSkillID:                msg.LocalSkillID,
		ActorUserUID:                source.FromUID,
		SourceTopicID:               source.TopicID,
		SourceMessageID:             source.ID,
		RuntimeBodyID:               client.bodyID,
		ClientRequestID:             msg.ClientRequestID,
		Operation:                   operation,
		CandidateContentHash:        msg.CandidateContentHash,
		ExpectedDefinitionRevision:  msg.ExpectedDefinitionRevision,
		ExpectedPreviousContentHash: msg.ExpectedPreviousHash,
		BeforeReference:             msg.BeforeReference,
	}
	return skillMutationGrantInput{Mutation: mutation, CandidateSizeBytes: msg.CandidateSizeBytes}, nil
}

func exactSkillMutationSourceMessage(messages store.MessageAroundStore, topicID string, messageID int64) (*types.Message, error) {
	topicID = strings.TrimSpace(topicID)
	if messages == nil || topicID == "" || messageID <= 0 {
		return nil, errSkillMutationSourceMessage
	}
	items, err := messages.GetMessagesAround(topicID, messageID, 1)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item != nil && item.ID == messageID && item.TopicID == topicID {
			return item, nil
		}
	}
	return nil, errSkillMutationSourceMessage
}

func (h *Hub) validateSkillMutationConversationAccess(topicID string, actorUID, botUID int64) error {
	if h == nil || h.db == nil || actorUID <= 0 || botUID <= 0 {
		return errSkillMutationSourceMessage
	}
	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID <= 0 {
			return errSkillMutationSourceMessage
		}
		actorMember, actorErr := h.db.IsGroupMember(groupID, actorUID)
		botMember, botErr := h.db.IsGroupMember(groupID, botUID)
		if actorErr != nil || botErr != nil || !actorMember || !botMember {
			return errSkillMutationActorForbidden
		}
		return nil
	}
	if topicID != p2pTopicID(actorUID, botUID) {
		return errSkillMutationSourceMessage
	}
	if code, _ := validateAgentP2PMessageAccess(h.db, actorUID, types.AccountHuman, botUID); code != 0 {
		return errSkillMutationActorForbidden
	}
	return nil
}

func definitionContainsExactSkillReference(record *types.BotDefinitionRecord, expected *types.BotSkillRef) bool {
	if record == nil || expected == nil {
		return false
	}
	matches := 0
	for _, current := range record.Definition.Skills {
		if current == *expected {
			matches++
		}
	}
	return matches == 1
}

func (h *Hub) sendSkillMutationGrantError(client *Client, request *MsgSkillMutationGrant, code, message string) {
	if h == nil || client == nil || request == nil {
		return
	}
	h.SendToClient(client, &ServerMessage{SkillMutationGrant: &MsgSkillMutationGrant{
		ID:        request.ID,
		Type:      skillMutationGrantMessageTypeResult,
		RequestID: request.RequestID,
		Error:     &MsgDeviceRPCError{Code: code, Message: message},
	}})
}
