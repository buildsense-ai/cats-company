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

	artifactWebMcpMaxBridges     = 4096
	artifactWebMcpMaxPendingCall = 4096
	artifactWebMcpMaxPayloadSize = 600 * 1024
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

type artifactWebMcpBridge struct {
	bridgeID    string
	openRef     string
	actorUID    int64
	agentUID    int64
	appID       string
	topicID     string
	humanClient *Client
	updatedAt   time.Time
}

type artifactWebMcpPendingCall struct {
	bridgeID    string
	callID      string
	appID       string
	topicID     string
	agentUID    int64
	botClient   *Client
	humanClient *Client
	createdAt   time.Time
}

type artifactWebMcpRouter struct {
	mu           sync.Mutex
	byBridgeID   map[string]*artifactWebMcpBridge
	byOpenClient map[string]string // openRef + ":" + connID -> bridgeID
	pendingCalls map[string]*artifactWebMcpPendingCall
}

func newArtifactWebMcpRouter() *artifactWebMcpRouter {
	return &artifactWebMcpRouter{
		byBridgeID:   make(map[string]*artifactWebMcpBridge),
		byOpenClient: make(map[string]string),
		pendingCalls: make(map[string]*artifactWebMcpPendingCall),
	}
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

func (h *Hub) SetArtifactOpenBindings(handler *ArtifactOpenBindingHandler) {
	if h == nil {
		return
	}
	h.artifactOpenBindings = handler
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
	toolsPayload := msg.Tools
	if len(toolsPayload) == 0 {
		toolsPayload = json.RawMessage("[]")
	}

	r := h.artifactWebMcp
	r.mu.Lock()
	ocKey := artifactWebMcpOpenClientKey(record.OpenRef, client)
	bridgeID := r.byOpenClient[ocKey]
	var bridge *artifactWebMcpBridge
	if bridgeID != "" {
		bridge = r.byBridgeID[bridgeID]
	}
	if bridge == nil {
		if len(r.byBridgeID) >= artifactWebMcpMaxBridges {
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
			updatedAt:   time.Now(),
		}
		r.byBridgeID[bridgeID] = bridge
		r.byOpenClient[ocKey] = bridgeID
	} else {
		bridge.updatedAt = time.Now()
	}
	out := &ServerMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolsSync,
			BridgeID: bridge.bridgeID,
			AppID:    bridge.appID,
			TopicID:  bridge.topicID,
			Tools:    toolsPayload,
		},
	}
	agentUID := bridge.agentUID
	r.mu.Unlock()

	h.SendToUser(agentUID, out)
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
		h.SendToUser(detached.agentUID, &ServerMessage{
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

	replyNotOpen := func(appID, topicID string) {
		h.SendToClient(client, &ServerMessage{
			ArtifactWebMcp: &MsgArtifactWebMcp{
				Type:     artifactWebMcpTypeToolResult,
				BridgeID: bridgeID,
				AppID:    appID,
				TopicID:  topicID,
				CallID:   callID,
				IsError:  true,
				Content:  json.RawMessage(`[{"type":"text","text":"Artifact page is no longer open"}]`),
				Error: &MsgArtifactWebMcpError{
					Code:    "page_not_open",
					Message: "Artifact page is no longer open",
				},
			},
		})
	}

	r := h.artifactWebMcp
	r.mu.Lock()
	bridge := r.byBridgeID[bridgeID]
	if bridge == nil || bridge.agentUID != client.uid || bridge.humanClient == nil {
		r.mu.Unlock()
		replyNotOpen(msg.AppID, msg.TopicID)
		return
	}
	openRef := bridge.openRef
	humanClient := bridge.humanClient
	appID := bridge.appID
	topicID := bridge.topicID
	r.mu.Unlock()

	// Re-verify open_ref TTL and revocation before forwarding.
	if _, ok := h.resolveOpenBindingForWS(humanClient, openRef); !ok {
		h.revokeArtifactWebMcpByOpenRef(openRef)
		replyNotOpen(appID, topicID)
		return
	}

	r.mu.Lock()
	// Re-check bridge presence after open_ref lookup.
	if r.byBridgeID[bridgeID] == nil || len(r.pendingCalls) >= artifactWebMcpMaxPendingCall {
		r.mu.Unlock()
		replyNotOpen(appID, topicID)
		return
	}
	r.pendingCalls[artifactWebMcpPendingKey(bridgeID, callID)] = &artifactWebMcpPendingCall{
		bridgeID:    bridgeID,
		callID:      callID,
		appID:       appID,
		topicID:     topicID,
		agentUID:    client.uid,
		botClient:   client,
		humanClient: humanClient,
		createdAt:   time.Now(),
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
	if len(msg.Content)+len(msg.StructuredContent) > artifactWebMcpMaxPayloadSize {
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

	out := &ServerMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:              artifactWebMcpTypeToolResult,
			BridgeID:          pending.bridgeID,
			AppID:             pending.appID,
			TopicID:           pending.topicID,
			CallID:            pending.callID,
			IsError:           msg.IsError,
			Content:           msg.Content,
			StructuredContent: msg.StructuredContent,
			Error:             msg.Error,
		},
	}
	if pending.botClient != nil {
		h.SendToClient(pending.botClient, out)
	} else {
		h.SendToUser(pending.agentUID, out)
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

func (h *Hub) failPendingWebMcpCalls(calls []*artifactWebMcpPendingCall, code, message string) {
	for _, pending := range calls {
		out := &ServerMessage{
			ArtifactWebMcp: &MsgArtifactWebMcp{
				Type:     artifactWebMcpTypeToolResult,
				BridgeID: pending.bridgeID,
				AppID:    pending.appID,
				TopicID:  pending.topicID,
				CallID:   pending.callID,
				IsError:  true,
				Content:  json.RawMessage(`[{"type":"text","text":"` + message + `"}]`),
				Error: &MsgArtifactWebMcpError{
					Code:    code,
					Message: message,
				},
			},
		}
		if pending.botClient != nil {
			h.SendToClient(pending.botClient, out)
		} else {
			h.SendToUser(pending.agentUID, out)
		}
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
		h.SendToUser(b.agentUID, &ServerMessage{
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
		h.SendToUser(b.agentUID, &ServerMessage{
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
