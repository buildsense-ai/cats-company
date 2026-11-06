package server

// Agent-initiated application opening.
//
// A user can ask an Agent to open one of its applications ("open the
// topic-research app"). The Agent answers with an ordinary message carrying a
// structured request under the "app_open" metadata key; the viewer opens that
// application in the conversation's right-hand panel.
//
// The request is a claim, not an instruction. Before it is persisted the server
// checks that the sender really speaks for this conversation and really owns the
// application, then restamps the identity itself. A request that fails any check
// is dropped while the message itself is still delivered: a bad command must
// never cost the user the Agent's actual answer.

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

const (
	// appOpenMetadataKey carries the request.
	appOpenMetadataKey = "app_open"
	// AppOpenContractV1 is the only accepted contract_version.
	AppOpenContractV1 = "catsco.app-open.v1"
	// appOpenMaxAppIDRunes bounds one application id.
	appOpenMaxAppIDRunes = 128
)

// appOpenDocument is the canonical shape persisted after validation. The URL is
// deliberately absent: the address comes from the Agent's own catalogue on the
// viewer side, so nothing in a message can point the panel at another origin.
type appOpenDocument struct {
	AgentUID int64
	AppID    string
}

// hasAppOpenMetadata reports whether a metadata map claims to open an app.
func hasAppOpenMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	_, ok := metadata[appOpenMetadataKey]
	return ok
}

// metadataWithoutAppOpen drops the key, leaving the rest of the metadata intact.
func metadataWithoutAppOpen(metadata map[string]interface{}) map[string]interface{} {
	if !hasAppOpenMetadata(metadata) {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key == appOpenMetadataKey {
			continue
		}
		next[key] = value
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// normalizeAppOpen validates the shape of one request. It answers only "is this
// well formed": ownership is decided separately, against server state.
func normalizeAppOpen(value interface{}) (*appOpenDocument, error) {
	object, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("app_open must be an object")
	}
	contract, _ := object["contract_version"].(string)
	if strings.TrimSpace(contract) != AppOpenContractV1 {
		return nil, errors.New("app_open.contract_version is not supported")
	}
	appID, _ := object["app_id"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, errors.New("app_open.app_id is required")
	}
	if len([]rune(appID)) > appOpenMaxAppIDRunes {
		return nil, errors.New("app_open.app_id is too long")
	}
	agentUID, err := appOpenUID(object["agent_uid"])
	if err != nil && object["agent_uid"] != nil {
		return nil, errors.New("app_open.agent_uid is not a usable id")
	}
	return &appOpenDocument{AgentUID: agentUID, AppID: appID}, nil
}

// appOpenUID accepts the numeric encodings a JSON decoder can produce and
// rejects anything that could lose precision.
func appOpenUID(value interface{}) (int64, error) {
	var raw float64
	switch typed := value.(type) {
	case float64:
		raw = typed
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, errors.New("app_open.agent_uid must be numeric")
	}
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw != math.Trunc(raw) || raw <= 0 || raw > float64(int64(1)<<53) {
		return 0, errors.New("app_open.agent_uid is not a usable id")
	}
	return int64(raw), nil
}

// appOpenSenderSpeaksForTopic answers whether senderUID is the account whose
// application this conversation is about. It never consults the message.
func (h *Hub) appOpenSenderSpeaksForTopic(senderUID int64, topicID string) bool {
	if h == nil || h.db == nil || senderUID <= 0 || strings.TrimSpace(topicID) == "" {
		return false
	}
	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID <= 0 {
			return false
		}
		member, err := h.db.IsGroupMember(groupID, senderUID)
		if err != nil || !member {
			return false
		}
		return h.isBotUser(senderUID)
	}
	// A direct conversation: the sender must be one of its two parties, and a
	// bot account rather than the human on the other side.
	if extractPeerUID(topicID, senderUID) <= 0 {
		return false
	}
	return h.isBotUser(senderUID)
}

// validateAppOpenMetadata is the shared ingestion point for HTTP and WebSocket
// publishing. Well-formed, authorised requests are restamped with the sender's
// own identity and returned in the canonical persisted shape. Everything else is
// dropped — never propagated — while the surrounding message still goes through.
func (h *Hub) validateAppOpenMetadata(senderUID int64, topicID string, metadata map[string]interface{}) map[string]interface{} {
	if !hasAppOpenMetadata(metadata) {
		return metadata
	}
	stripped := metadataWithoutAppOpen(metadata)
	if h == nil || h.db == nil {
		return stripped
	}
	// Only the conversation's own Agent may ask; a human, or a bot with no
	// standing here, is not speaking for the conversation.
	if !h.appOpenSenderSpeaksForTopic(senderUID, topicID) {
		return stripped
	}
	document, err := normalizeAppOpen(metadata[appOpenMetadataKey])
	if err != nil || document == nil {
		return stripped
	}
	// The claim must not name some other account: accepting it would let one
	// Agent request an application it does not own. A command that omits the id
	// is fine — the sender is the only authority, so it is stamped from there.
	if document.AgentUID != 0 && document.AgentUID != senderUID {
		return stripped
	}
	document.AgentUID = senderUID
	if err := h.gatewayAnnotationsAppAuthorized(document.AgentUID, document.AppID); err != nil {
		return stripped
	}
	if stripped == nil {
		stripped = make(map[string]interface{}, 1)
	}
	stripped[appOpenMetadataKey] = map[string]interface{}{
		"contract_version": AppOpenContractV1,
		"app_id":           document.AppID,
		"agent_uid":        document.AgentUID,
	}
	return stripped
}
