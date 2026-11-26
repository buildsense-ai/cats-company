package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	artifactWebMcpTypeToolsSync  = "tools_sync"
	artifactWebMcpTypeDetach     = "detach"
	artifactWebMcpTypeToolCall   = "tool_call"
	artifactWebMcpTypeToolResult = "tool_result"

	artifactWebMcpClusterKindToolCall   = "tool_call"
	artifactWebMcpClusterKindToolResult = "tool_result"
	artifactWebMcpClusterKindBotReplay  = "bot_replay"

	// Aligned below wspump.go maxMessageSize (64 KiB) leaving 16 KiB headroom for
	// JSON envelope fields so oversized payloads never trigger WS SetReadLimit.
	artifactWebMcpMaxPayloadSize = 48 * 1024

	// Per-user and per-bridge isolation caps (plus high global ceilings).
	artifactWebMcpMaxBridgesPerUser   = 32
	artifactWebMcpMaxBridges          = 8192
	artifactWebMcpMaxPendingPerBridge = 32
	artifactWebMcpMaxPendingPerUser   = 128
	artifactWebMcpMaxPendingCall      = 8192
	artifactWebMcpPendingCallTTL      = 30 * time.Second
	artifactWebMcpReaperInterval      = 10 * time.Second
)

// MsgArtifactWebMcpError is a structured error returned in tool_result frames.
type MsgArtifactWebMcpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MsgArtifactWebMcp carries bidirectional WebMCP tool discovery and RPC frames
// between an authenticated human Artifact host (bound via open_ref) and the
// owning Bot runtime. OpenRef is accepted only from the human host and is
// always stripped before forwarding to the Bot.
type MsgArtifactWebMcp struct {
	Type              string                  `json:"type"`
	OpenRef           string                  `json:"open_ref,omitempty"`
	BridgeID          string                  `json:"bridge_id,omitempty"`
	AppID             string                  `json:"app_id,omitempty"`
	TopicID           string                  `json:"topic_id,omitempty"`
	ActorUserID       string                  `json:"actor_user_id,omitempty"`
	CallID            string                  `json:"call_id,omitempty"`
	Name              string                  `json:"name,omitempty"`
	Arguments         map[string]interface{}  `json:"arguments,omitempty"`
	Tools             json.RawMessage         `json:"tools,omitempty"`
	IsError           bool                    `json:"is_error,omitempty"`
	Content           json.RawMessage         `json:"content,omitempty"`
	StructuredContent json.RawMessage         `json:"structured_content,omitempty"`
	Error             *MsgArtifactWebMcpError `json:"error,omitempty"`
	Reason            string                  `json:"reason,omitempty"`
}

type artifactWebMcpClusterEnvelope struct {
	Kind        string             `json:"kind"`
	Msg         *MsgArtifactWebMcp `json:"msg,omitempty"`
	BotUID      int64              `json:"bot_uid,omitempty"`
	SourceRoute redisRuntimeRoute  `json:"source_route,omitempty"`
}

type artifactWebMcpBridge struct {
	bridgeID    string
	openRef     string
	actorUID    int64
	agentUID    int64
	appID       string
	topicID     string
	humanClient *Client
	tools       json.RawMessage
	updatedAt   time.Time
}

type artifactWebMcpPendingCall struct {
	bridgeID    string
	callID      string
	appID       string
	topicID     string
	actorUID    int64
	agentUID    int64
	botClient   *Client
	botRoute    runtimeRoute
	humanClient *Client
	createdAt   time.Time
	expiresAt   time.Time
}

type artifactWebMcpRouter struct {
	mu           sync.Mutex
	byBridgeID   map[string]*artifactWebMcpBridge
	byOpenClient map[string]string // openRef + "\x00" + connID -> bridgeID
	pendingCalls map[string]*artifactWebMcpPendingCall
	now          func() time.Time
	callTTL      time.Duration
}

func newArtifactWebMcpRouter() *artifactWebMcpRouter {
	return &artifactWebMcpRouter{
		byBridgeID:   make(map[string]*artifactWebMcpBridge),
		byOpenClient: make(map[string]string),
		pendingCalls: make(map[string]*artifactWebMcpPendingCall),
		now:          time.Now,
		callTTL:      artifactWebMcpPendingCallTTL,
	}
}

func (r *artifactWebMcpRouter) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *artifactWebMcpRouter) pendingTTL() time.Duration {
	if r != nil && r.callTTL > 0 {
		return r.callTTL
	}
	return artifactWebMcpPendingCallTTL
}

func newArtifactWebMcpBridgeID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "awm_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func artifactWebMcpPendingKey(bridgeID, callID string) string {
	return bridgeID + "\x00" + callID
}

func artifactWebMcpOpenClientKey(openRef string, client *Client) string {
	connKey := ""
	if client != nil {
		connKey = client.connectionID
	}
	return openRef + "\x00" + connKey
}

func marshalArtifactWebMcpTextContent(message string) json.RawMessage {
	payload, err := json.Marshal([]map[string]string{
		{"type": "text", "text": message},
	})
	if err != nil {
		return json.RawMessage(`[{"type":"text","text":"internal error"}]`)
	}
	return json.RawMessage(payload)
}

func (h *Hub) SetArtifactOpenBindings(handler *ArtifactOpenBindingHandler) {
	if h == nil {
		return
	}
	h.artifactOpenBindings = handler
}

func (h *Hub) sendArtifactWebMcpToUser(uid int64, msg *ServerMessage) {
	if h == nil || uid <= 0 || msg == nil {
		return
	}
	if h.sharedRuntime != nil {
		if h.sharedRuntime.broadcastUserMessage(uid, msg) {
			return
		}
	}
	h.SendToUser(uid, msg)
}

func (h *Hub) sendArtifactWebMcpToBotTarget(botClient *Client, botRoute runtimeRoute, agentUID int64, msg *MsgArtifactWebMcp) {
	if h == nil || msg == nil {
		return
	}
	serverMsg := &ServerMessage{ArtifactWebMcp: msg}
	if botClient != nil {
		h.SendToClient(botClient, serverMsg)
		return
	}
	if botRoute.NodeID != "" && botRoute.ConnectionID != "" {
		if botRoute.NodeID == h.nodeID {
			if target := h.getClientByConnectionID(botRoute.ConnectionID); target != nil {
				h.SendToClient(target, serverMsg)
				return
			}
		} else if h.sharedRuntime != nil {
			h.sharedRuntime.deliverArtifactWebMcp(botRoute.NodeID, artifactWebMcpClusterEnvelope{
				Kind:        artifactWebMcpClusterKindToolResult,
				Msg:         msg,
				BotUID:      agentUID,
				SourceRoute: redisRouteFromRuntime(botRoute),
			}, time.Now())
			return
		}
	}
	if agentUID > 0 {
		h.sendArtifactWebMcpToUser(agentUID, serverMsg)
	}
}

func (h *Hub) handleArtifactWebMcp(client *Client, msg *MsgArtifactWebMcp) {
	if h == nil || client == nil || msg == nil || h.artifactWebMcp == nil {
		return
	}
	if client.deviceConnector != nil {
		return
	}
	msgType := strings.TrimSpace(msg.Type)
	switch msgType {
	case artifactWebMcpTypeToolsSync:
		h.handleArtifactWebMcpToolsSync(client, msg)
	case artifactWebMcpTypeDetach:
		h.handleArtifactWebMcpDetach(client, msg)
	case artifactWebMcpTypeToolCall:
		h.handleArtifactWebMcpToolCall(client, msg)
	case artifactWebMcpTypeToolResult:
		h.handleArtifactWebMcpToolResult(client, msg)
	}
}

func (h *Hub) resolveOpenBindingForWS(client *Client, openRef string) (artifactOpenBindingRecord, bool) {
	if h == nil || client == nil || h.artifactOpenBindings == nil || h.artifactOpenBindings.store == nil {
		return artifactOpenBindingRecord{}, false
	}
	ref := strings.TrimSpace(openRef)
	if ref == "" {
		return artifactOpenBindingRecord{}, false
	}
	now := time.Now()
	if h.artifactOpenBindings.now != nil {
		now = h.artifactOpenBindings.now()
	}
	if client.hasAuthSessionFingerprint {
		return h.artifactOpenBindings.store.lookup(ref, client.uid, client.authSessionFingerprint, now, false)
	}
	return h.artifactOpenBindings.store.lookupForActor(ref, client.uid, now)
}

func (h *Hub) handleArtifactWebMcpToolsSync(client *Client, msg *MsgArtifactWebMcp) {
	if client.accountType != types.AccountHuman {
		return
	}
	if len(msg.Tools) > artifactWebMcpMaxPayloadSize {
		return
	}
	record, ok := h.resolveOpenBindingForWS(client, msg.OpenRef)
	if !ok {
		return
	}
	toolsPayload := append(json.RawMessage(nil), msg.Tools...)
	if len(toolsPayload) == 0 {
		toolsPayload = json.RawMessage("[]")
	}

	r := h.artifactWebMcp
	now := r.currentTime()
	r.mu.Lock()
	ocKey := artifactWebMcpOpenClientKey(record.OpenRef, client)
	bridgeID := r.byOpenClient[ocKey]
	var bridge *artifactWebMcpBridge
	if bridgeID != "" {
		bridge = r.byBridgeID[bridgeID]
	}
	if bridge == nil {
		userBridges := 0
		for _, existing := range r.byBridgeID {
			if existing.actorUID == client.uid {
				userBridges++
			}
		}
		if userBridges >= artifactWebMcpMaxBridgesPerUser || len(r.byBridgeID) >= artifactWebMcpMaxBridges {
			r.mu.Unlock()
			return
		}
		var err error
		bridgeID, err = newArtifactWebMcpBridgeID()
		if err != nil {
			r.mu.Unlock()
			return
		}
		bridge = &artifactWebMcpBridge{
			bridgeID:    bridgeID,
			openRef:     record.OpenRef,
			actorUID:    client.uid,
			agentUID:    record.AgentUID,
			appID:       record.AppID,
			topicID:     record.TopicID,
			humanClient: client,
			tools:       toolsPayload,
			updatedAt:   now,
		}
		r.byBridgeID[bridgeID] = bridge
		r.byOpenClient[ocKey] = bridgeID
	} else {
		bridge.tools = toolsPayload
		bridge.updatedAt = now
	}
	out := &ServerMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:        artifactWebMcpTypeToolsSync,
			BridgeID:    bridge.bridgeID,
			AppID:       bridge.appID,
			TopicID:     bridge.topicID,
			ActorUserID: formatUID(bridge.actorUID),
			Tools:       append(json.RawMessage(nil), bridge.tools...),
		},
	}
	agentUID := bridge.agentUID
	r.mu.Unlock()

	h.sendArtifactWebMcpToUser(agentUID, out)
}

func (h *Hub) handleArtifactWebMcpDetach(client *Client, msg *MsgArtifactWebMcp) {
	if client.accountType != types.AccountHuman {
		return
	}
	openRef := strings.TrimSpace(msg.OpenRef)
	bridgeID := strings.TrimSpace(msg.BridgeID)
	reason := strings.TrimSpace(msg.Reason)
	if reason == "" {
		reason = "client_detached"
	}

	var detached *artifactWebMcpBridge
	var failedCalls []*artifactWebMcpPendingCall

	r := h.artifactWebMcp
	r.mu.Lock()
	if bridgeID == "" && openRef != "" {
		bridgeID = r.byOpenClient[artifactWebMcpOpenClientKey(openRef, client)]
	}
	if bridgeID != "" {
		if b, ok := r.byBridgeID[bridgeID]; ok && b.humanClient == client {
			detached = b
			delete(r.byBridgeID, bridgeID)
			delete(r.byOpenClient, artifactWebMcpOpenClientKey(b.openRef, client))
			failedCalls = r.popPendingCallsLocked(bridgeID)
		}
	}
	r.mu.Unlock()

	h.failPendingWebMcpCalls(failedCalls, "page_not_open", "Artifact page detached")
	if detached != nil {
		h.sendArtifactWebMcpToUser(detached.agentUID, &ServerMessage{
			ArtifactWebMcp: &MsgArtifactWebMcp{
				Type:     artifactWebMcpTypeDetach,
				BridgeID: detached.bridgeID,
				AppID:    detached.appID,
				TopicID:  detached.topicID,
				Reason:   reason,
			},
		})
	}
}

func (h *Hub) handleArtifactWebMcpToolCall(client *Client, msg *MsgArtifactWebMcp) {
	if client.accountType != types.AccountBot {
		return
	}
	bridgeID := strings.TrimSpace(msg.BridgeID)
	callID := strings.TrimSpace(msg.CallID)
	name := strings.TrimSpace(msg.Name)
	if bridgeID == "" || callID == "" || name == "" {
		return
	}
	botRoute := h.clientRoute(client)
	if h.dispatchArtifactWebMcpToolCallLocal(client, botRoute, client.uid, msg) {
		return
	}
	// Multi-node routing: if bridge is hosted on another Hub node, forward there.
	if h.sharedRuntime != nil {
		forwarded := h.sharedRuntime.deliverArtifactWebMcp("", artifactWebMcpClusterEnvelope{
			Kind:        artifactWebMcpClusterKindToolCall,
			Msg:         msg,
			BotUID:      client.uid,
			SourceRoute: redisRouteFromRuntime(botRoute),
		}, time.Now())
		if forwarded {
			return
		}
	}
	h.sendArtifactWebMcpToBotTarget(client, botRoute, client.uid, &MsgArtifactWebMcp{
		Type:     artifactWebMcpTypeToolResult,
		BridgeID: bridgeID,
		AppID:    msg.AppID,
		TopicID:  msg.TopicID,
		CallID:   callID,
		IsError:  true,
		Content:  marshalArtifactWebMcpTextContent("Artifact page is no longer open"),
		Error: &MsgArtifactWebMcpError{
			Code:    "page_not_open",
			Message: "Artifact page is no longer open",
		},
	})
}

// dispatchArtifactWebMcpToolCallLocal attempts to route a tool_call to a bridge
// owned by this Hub node. Returns true if the bridge was found on this node
// (even if rejected due to auth/limits/revocation, in which case an error reply
// has already been sent). Returns false only when bridgeID is not on this node.
func (h *Hub) dispatchArtifactWebMcpToolCallLocal(botClient *Client, botRoute runtimeRoute, botUID int64, msg *MsgArtifactWebMcp) bool {
	bridgeID := strings.TrimSpace(msg.BridgeID)
	callID := strings.TrimSpace(msg.CallID)
	name := strings.TrimSpace(msg.Name)
	if bridgeID == "" || callID == "" || name == "" {
		return false
	}

	replyError := func(appID, topicID, code, message string) {
		h.sendArtifactWebMcpToBotTarget(botClient, botRoute, botUID, &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolResult,
			BridgeID: bridgeID,
			AppID:    appID,
			TopicID:  topicID,
			CallID:   callID,
			IsError:  true,
			Content:  marshalArtifactWebMcpTextContent(message),
			Error: &MsgArtifactWebMcpError{
				Code:    code,
				Message: message,
			},
		})
	}

	r := h.artifactWebMcp
	now := r.currentTime()
	r.mu.Lock()
	expired := r.pruneExpiredPendingCallsLocked(now)
	bridge := r.byBridgeID[bridgeID]
	if bridge == nil {
		r.mu.Unlock()
		if len(expired) > 0 {
			h.failPendingWebMcpCalls(expired, "tool_call_timeout", "Artifact tool call timed out")
		}
		return false
	}
	if bridge.agentUID != botUID || bridge.humanClient == nil {
		appID := bridge.appID
		topicID := bridge.topicID
		r.mu.Unlock()
		if len(expired) > 0 {
			h.failPendingWebMcpCalls(expired, "tool_call_timeout", "Artifact tool call timed out")
		}
		replyError(appID, topicID, "page_not_open", "Artifact page is no longer open")
		return true
	}
	openRef := bridge.openRef
	humanClient := bridge.humanClient
	actorUID := bridge.actorUID
	appID := bridge.appID
	topicID := bridge.topicID
	r.mu.Unlock()

	if len(expired) > 0 {
		h.failPendingWebMcpCalls(expired, "tool_call_timeout", "Artifact tool call timed out")
	}

	// Re-verify open_ref TTL and revocation before forwarding.
	if _, ok := h.resolveOpenBindingForWS(humanClient, openRef); !ok {
		h.revokeArtifactWebMcpByOpenRef(openRef)
		replyError(appID, topicID, "page_not_open", "Artifact page is no longer open")
		return true
	}

	r.mu.Lock()
	if r.byBridgeID[bridgeID] == nil {
		r.mu.Unlock()
		replyError(appID, topicID, "page_not_open", "Artifact page is no longer open")
		return true
	}
	bridgePending := 0
	userPending := 0
	for _, p := range r.pendingCalls {
		if p.bridgeID == bridgeID {
			bridgePending++
		}
		if p.actorUID == actorUID {
			userPending++
		}
	}
	if bridgePending >= artifactWebMcpMaxPendingPerBridge ||
		userPending >= artifactWebMcpMaxPendingPerUser ||
		len(r.pendingCalls) >= artifactWebMcpMaxPendingCall {
		r.mu.Unlock()
		replyError(appID, topicID, "too_many_pending_calls", "Too many concurrent WebMCP tool calls for this artifact")
		return true
	}
	r.pendingCalls[artifactWebMcpPendingKey(bridgeID, callID)] = &artifactWebMcpPendingCall{
		bridgeID:    bridgeID,
		callID:      callID,
		appID:       appID,
		topicID:     topicID,
		actorUID:    actorUID,
		agentUID:    botUID,
		botClient:   botClient,
		botRoute:    botRoute,
		humanClient: humanClient,
		createdAt:   now,
		expiresAt:   now.Add(r.pendingTTL()),
	}
	r.mu.Unlock()

	h.SendToClient(humanClient, &ServerMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:      artifactWebMcpTypeToolCall,
			BridgeID:  bridgeID,
			AppID:     appID,
			TopicID:   topicID,
			CallID:    callID,
			Name:      name,
			Arguments: msg.Arguments,
		},
	})
	return true
}

func (h *Hub) handleArtifactWebMcpToolResult(client *Client, msg *MsgArtifactWebMcp) {
	if client.accountType != types.AccountHuman {
		return
	}
	bridgeID := strings.TrimSpace(msg.BridgeID)
	callID := strings.TrimSpace(msg.CallID)
	if bridgeID == "" || callID == "" {
		return
	}

	r := h.artifactWebMcp
	r.mu.Lock()
	key := artifactWebMcpPendingKey(bridgeID, callID)
	pending := r.pendingCalls[key]
	if pending == nil || pending.humanClient != client {
		r.mu.Unlock()
		return
	}
	delete(r.pendingCalls, key)
	r.mu.Unlock()

	if len(msg.Content)+len(msg.StructuredContent) > artifactWebMcpMaxPayloadSize {
		h.sendArtifactWebMcpToBotTarget(pending.botClient, pending.botRoute, pending.agentUID, &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolResult,
			BridgeID: pending.bridgeID,
			AppID:    pending.appID,
			TopicID:  pending.topicID,
			CallID:   pending.callID,
			IsError:  true,
			Content:  marshalArtifactWebMcpTextContent("Tool result exceeds maximum payload size"),
			Error: &MsgArtifactWebMcpError{
				Code:    "result_too_large",
				Message: "Tool result exceeds maximum payload size",
			},
		})
		return
	}

	h.sendArtifactWebMcpToBotTarget(pending.botClient, pending.botRoute, pending.agentUID, &MsgArtifactWebMcp{
		Type:              artifactWebMcpTypeToolResult,
		BridgeID:          pending.bridgeID,
		AppID:             pending.appID,
		TopicID:           pending.topicID,
		CallID:            pending.callID,
		IsError:           msg.IsError,
		Content:           msg.Content,
		StructuredContent: msg.StructuredContent,
		Error:             msg.Error,
	})
}

func (h *Hub) receiveArtifactWebMcpFromRuntime(env artifactWebMcpClusterEnvelope) bool {
	if h == nil || h.artifactWebMcp == nil {
		return false
	}
	sourceRoute := env.SourceRoute.toRuntimeRoute()
	switch env.Kind {
	case artifactWebMcpClusterKindToolCall:
		if env.Msg == nil || env.BotUID <= 0 {
			return false
		}
		return h.dispatchArtifactWebMcpToolCallLocal(nil, sourceRoute, env.BotUID, env.Msg)
	case artifactWebMcpClusterKindToolResult:
		if env.Msg == nil {
			return false
		}
		if sourceRoute.ConnectionID != "" {
			if target := h.getClientByConnectionID(sourceRoute.ConnectionID); target != nil {
				h.SendToClient(target, &ServerMessage{ArtifactWebMcp: env.Msg})
				return true
			}
		}
		if env.BotUID > 0 {
			h.SendToUser(env.BotUID, &ServerMessage{ArtifactWebMcp: env.Msg})
			return true
		}
		return false
	case artifactWebMcpClusterKindBotReplay:
		if env.BotUID <= 0 {
			return false
		}
		h.replayLocalArtifactWebMcpBridgesForBot(nil, sourceRoute, env.BotUID)
		return true
	}
	return false
}

// replayArtifactWebMcpBridgesForBot replays all active bridges owned by botClient
// across this node and any peer Hub nodes after a Bot (re)connects.
func (h *Hub) replayArtifactWebMcpBridgesForBot(botClient *Client) {
	if h == nil || botClient == nil || h.artifactWebMcp == nil {
		return
	}
	if botClient.accountType != types.AccountBot || botClient.deviceConnector != nil {
		return
	}
	botRoute := h.clientRoute(botClient)
	h.replayLocalArtifactWebMcpBridgesForBot(botClient, botRoute, botClient.uid)
	if h.sharedRuntime != nil {
		h.sharedRuntime.deliverArtifactWebMcp("", artifactWebMcpClusterEnvelope{
			Kind:        artifactWebMcpClusterKindBotReplay,
			BotUID:      botClient.uid,
			SourceRoute: redisRouteFromRuntime(botRoute),
		}, time.Now())
	}
}

func (h *Hub) replayLocalArtifactWebMcpBridgesForBot(botClient *Client, botRoute runtimeRoute, botUID int64) {
	if h == nil || h.artifactWebMcp == nil || botUID <= 0 {
		return
	}
	type replayItem struct {
		bridgeID string
		openRef  string
		appID    string
		topicID  string
		actorUID int64
		human    *Client
		tools    json.RawMessage
	}
	r := h.artifactWebMcp
	r.mu.Lock()
	var items []replayItem
	for _, b := range r.byBridgeID {
		if b.agentUID != botUID || b.humanClient == nil {
			continue
		}
		items = append(items, replayItem{
			bridgeID: b.bridgeID,
			openRef:  b.openRef,
			appID:    b.appID,
			topicID:  b.topicID,
			actorUID: b.actorUID,
			human:    b.humanClient,
			tools:    append(json.RawMessage(nil), b.tools...),
		})
	}
	r.mu.Unlock()

	for _, item := range items {
		if _, ok := h.resolveOpenBindingForWS(item.human, item.openRef); !ok {
			h.revokeArtifactWebMcpByOpenRef(item.openRef)
			continue
		}
		tools := item.tools
		if len(tools) == 0 {
			tools = json.RawMessage("[]")
		}
		h.sendArtifactWebMcpToBotTarget(botClient, botRoute, botUID, &MsgArtifactWebMcp{
			Type:        artifactWebMcpTypeToolsSync,
			BridgeID:    item.bridgeID,
			AppID:       item.appID,
			TopicID:     item.topicID,
			ActorUserID: formatUID(item.actorUID),
			Tools:       tools,
		})
	}
}

func (r *artifactWebMcpRouter) popPendingCallsLocked(bridgeID string) []*artifactWebMcpPendingCall {
	var out []*artifactWebMcpPendingCall
	for key, pending := range r.pendingCalls {
		if pending.bridgeID == bridgeID {
			out = append(out, pending)
			delete(r.pendingCalls, key)
		}
	}
	return out
}

func (r *artifactWebMcpRouter) pruneExpiredPendingCallsLocked(now time.Time) []*artifactWebMcpPendingCall {
	var expired []*artifactWebMcpPendingCall
	for key, pending := range r.pendingCalls {
		if !pending.expiresAt.IsZero() && !now.Before(pending.expiresAt) {
			expired = append(expired, pending)
			delete(r.pendingCalls, key)
		}
	}
	return expired
}

func (h *Hub) sweepExpiredArtifactWebMcpCalls() {
	if h == nil || h.artifactWebMcp == nil {
		return
	}
	r := h.artifactWebMcp
	r.mu.Lock()
	expired := r.pruneExpiredPendingCallsLocked(r.currentTime())
	r.mu.Unlock()
	if len(expired) > 0 {
		h.failPendingWebMcpCalls(expired, "tool_call_timeout", "Artifact tool call timed out")
	}
}

func (h *Hub) runArtifactWebMcpReaper() {
	ticker := time.NewTicker(artifactWebMcpReaperInterval)
	defer ticker.Stop()
	for range ticker.C {
		h.sweepExpiredArtifactWebMcpCalls()
	}
}

func (h *Hub) failPendingWebMcpCalls(calls []*artifactWebMcpPendingCall, code, message string) {
	content := marshalArtifactWebMcpTextContent(message)
	for _, pending := range calls {
		h.sendArtifactWebMcpToBotTarget(pending.botClient, pending.botRoute, pending.agentUID, &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolResult,
			BridgeID: pending.bridgeID,
			AppID:    pending.appID,
			TopicID:  pending.topicID,
			CallID:   pending.callID,
			IsError:  true,
			Content:  content,
			Error: &MsgArtifactWebMcpError{
				Code:    code,
				Message: message,
			},
		})
	}
}

func (h *Hub) cleanupArtifactWebMcpForClient(client *Client) {
	if h == nil || client == nil || h.artifactWebMcp == nil {
		return
	}
	var detached []*artifactWebMcpBridge
	var failedCalls []*artifactWebMcpPendingCall

	r := h.artifactWebMcp
	r.mu.Lock()
	for id, b := range r.byBridgeID {
		if b.humanClient == client {
			detached = append(detached, b)
			delete(r.byBridgeID, id)
			delete(r.byOpenClient, artifactWebMcpOpenClientKey(b.openRef, client))
			failedCalls = append(failedCalls, r.popPendingCallsLocked(id)...)
		}
	}
	// Also clean up any pending calls initiated by a disconnecting bot client.
	for key, pending := range r.pendingCalls {
		if pending.botClient == client {
			delete(r.pendingCalls, key)
		}
	}
	r.mu.Unlock()

	h.failPendingWebMcpCalls(failedCalls, "page_not_open", "Artifact page disconnected")
	for _, b := range detached {
		h.sendArtifactWebMcpToUser(b.agentUID, &ServerMessage{
			ArtifactWebMcp: &MsgArtifactWebMcp{
				Type:     artifactWebMcpTypeDetach,
				BridgeID: b.bridgeID,
				AppID:    b.appID,
				TopicID:  b.topicID,
				Reason:   "client_disconnected",
			},
		})
	}
}

func (h *Hub) revokeArtifactWebMcpByOpenRef(openRef string) {
	if h == nil || h.artifactWebMcp == nil {
		return
	}
	ref := strings.TrimSpace(openRef)
	if ref == "" {
		return
	}
	var detached []*artifactWebMcpBridge
	var failedCalls []*artifactWebMcpPendingCall

	r := h.artifactWebMcp
	r.mu.Lock()
	for id, b := range r.byBridgeID {
		if b.openRef == ref {
			detached = append(detached, b)
			delete(r.byBridgeID, id)
			delete(r.byOpenClient, artifactWebMcpOpenClientKey(b.openRef, b.humanClient))
			failedCalls = append(failedCalls, r.popPendingCallsLocked(id)...)
		}
	}
	r.mu.Unlock()

	h.failPendingWebMcpCalls(failedCalls, "page_not_open", "Artifact open binding revoked")
	for _, b := range detached {
		h.sendArtifactWebMcpToUser(b.agentUID, &ServerMessage{
			ArtifactWebMcp: &MsgArtifactWebMcp{
				Type:     artifactWebMcpTypeDetach,
				BridgeID: b.bridgeID,
				AppID:    b.appID,
				TopicID:  b.topicID,
				Reason:   "open_binding_revoked",
			},
		})
	}
}
