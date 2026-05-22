// Package server implements the WebSocket hub and client connections for Cats Company.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub maintains the set of active clients and broadcasts messages.
type Hub struct {
	mu          sync.RWMutex
	clients     map[int64]map[*Client]struct{}
	register    chan *Client
	unregister  chan *Client
	presence    chan presenceEvent
	db          store.Store
	rateLimiter *RateLimiter
	botStats    *BotStats
	botConvo    botConvoTracker
}

type presenceEvent struct {
	uid  int64
	what string
}

// Client represents a single WebSocket connection.
type Client struct {
	hub         *Hub
	conn        *websocket.Conn
	uid         int64
	remoteAddr  string
	displayName string
	accountType types.AccountType
	send        chan []byte
	sendMu      sync.RWMutex
	sendClosed  bool
}

// NewHub creates a new Hub.
func NewHub(db store.Store, rl *RateLimiter) *Hub {
	hub := &Hub{
		clients:     make(map[int64]map[*Client]struct{}),
		register:    make(chan *Client, 256),
		unregister:  make(chan *Client, 256),
		presence:    make(chan presenceEvent, 256),
		db:          db,
		rateLimiter: rl,
		botStats:    NewBotStats(),
		botConvo:    botConvoTracker{counters: make(map[string]*botConvoCount)},
	}
	go hub.runPresence()
	return hub
}

// BotStats returns the hub's bot stats tracker.
func (h *Hub) BotStats() *BotStats {
	return h.botStats
}

// Run starts the hub's main loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			registration := h.registerClient(client)
			if !registration.accepted {
				log.Printf("client connection rejected: uid=%d addr=%s account=%s reason=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, registration.reason, registration.deviceCount, registration.onlineUsers)
				rejectWebSocketConnection(client.conn, registration.reason)
				continue
			}

			log.Printf("client connected: uid=%d addr=%s account=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, registration.deviceCount, registration.onlineUsers)
			if registration.firstConn {
				h.enqueuePresence(client.uid, "on")
			}

		case client := <-h.unregister:
			removed, lastConn, remaining, onlineUsers := h.removeClient(client)
			if !removed {
				continue
			}
			client.closeSend()
			log.Printf("client disconnected: uid=%d addr=%s account=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, remaining, onlineUsers)
			if lastConn {
				h.enqueuePresence(client.uid, "off")
			}
		}
	}
}

// OnlineCount returns the number of connected clients.
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// GetOnlineUIDs returns a list of online user IDs.
func (h *Hub) GetOnlineUIDs() []int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	uids := make([]int64, 0, len(h.clients))
	for uid := range h.clients {
		uids = append(uids, uid)
	}
	return uids
}

// BuildOnlineStatusList returns online status for accepted friends plus bots
// owned by the current user, so the web sidebar can show AI Apps status.
func BuildOnlineStatusList(db store.Store, hub *Hub, uid int64) ([]map[string]interface{}, error) {
	friends, err := db.GetFriends(uid)
	if err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{})
	onlineList := make([]map[string]interface{}, 0, len(friends))
	addUser := func(id int64) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		onlineList = append(onlineList, map[string]interface{}{
			"uid":    id,
			"online": hub != nil && hub.IsOnline(id),
		})
	}

	for _, friend := range friends {
		addUser(friend.ID)
	}

	bots, err := db.ListBotsByOwner(uid)
	if err != nil {
		log.Printf("online status: failed to list owner bots for uid=%d: %v", uid, err)
		return onlineList, nil
	}
	for _, bot := range bots {
		addUser(mapID(bot["id"]))
	}

	return onlineList, nil
}

func mapID(value interface{}) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		id, _ := v.Int64()
		return id
	default:
		return 0
	}
}

type clientRegistration struct {
	accepted    bool
	reason      string
	firstConn   bool
	deviceCount int
	onlineUsers int
}

func (h *Hub) registerClient(client *Client) clientRegistration {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.clients[client.uid]
	if client.accountType == types.AccountBot && len(clients) > 0 {
		return clientRegistration{
			accepted:    false,
			reason:      "bot already connected",
			deviceCount: len(clients),
			onlineUsers: len(h.clients),
		}
	}

	firstConn := len(clients) == 0
	if clients == nil {
		clients = make(map[*Client]struct{})
		h.clients[client.uid] = clients
	}
	clients[client] = struct{}{}

	return clientRegistration{
		accepted:    true,
		firstConn:   firstConn,
		deviceCount: len(clients),
		onlineUsers: len(h.clients),
	}
}

func rejectWebSocketConnection(conn *websocket.Conn, reason string) {
	if conn == nil {
		return
	}
	if reason == "" {
		reason = "connection rejected"
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(writeWait))
	_ = conn.Close()
}

func (h *Hub) addClient(client *Client) (firstConn bool, deviceCount int, onlineUsers int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.clients[client.uid]
	firstConn = len(clients) == 0
	if clients == nil {
		clients = make(map[*Client]struct{})
		h.clients[client.uid] = clients
	}
	clients[client] = struct{}{}

	return firstConn, len(clients), len(h.clients)
}

func (h *Hub) removeClient(client *Client) (removed bool, lastConn bool, remaining int, onlineUsers int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, ok := h.clients[client.uid]
	if !ok {
		return false, false, 0, len(h.clients)
	}
	if _, ok := clients[client]; !ok {
		return false, false, len(clients), len(h.clients)
	}

	delete(clients, client)
	removed = true
	remaining = len(clients)
	if remaining == 0 {
		delete(h.clients, client.uid)
		lastConn = true
	}

	return removed, lastConn, remaining, len(h.clients)
}

// broadcastPresence notifies friends and, for bots, their owner of online/offline status.
func (h *Hub) broadcastPresence(uid int64, what string) {
	if h.db == nil {
		return
	}
	friends, err := h.db.GetFriends(uid)
	if err != nil {
		log.Printf("presence: failed to get friends for uid=%d: %v", uid, err)
		return
	}
	msg := &ServerMessage{
		Pres: &MsgServerPres{
			Topic: "me",
			What:  what,
			Src:   formatUID(uid),
		},
	}
	recipients := make(map[int64]struct{}, len(friends)+1)
	for _, f := range friends {
		recipients[f.ID] = struct{}{}
	}
	if ownerID, err := h.db.GetBotOwner(uid); err == nil && ownerID > 0 {
		recipients[ownerID] = struct{}{}
	}
	for id := range recipients {
		h.SendToUser(id, msg)
	}
}

func (h *Hub) enqueuePresence(uid int64, what string) {
	select {
	case h.presence <- presenceEvent{uid: uid, what: what}:
	default:
		go h.broadcastPresence(uid, what)
	}
}

func (h *Hub) runPresence() {
	for evt := range h.presence {
		h.broadcastPresence(evt.uid, evt.what)
	}
}

// ServeWS handles WebSocket upgrade requests with JWT or API Key authentication.
func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	var uid int64
	acctType := types.AccountHuman
	displayName := ""

	// Try JWT token first
	tokenStr := r.URL.Query().Get("token")
	apiKeyStr := r.Header.Get("X-API-Key")
	if apiKeyStr == "" {
		apiKeyStr = r.URL.Query().Get("api_key")
	}

	if tokenStr != "" {
		claims, err := ParseToken(tokenStr)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		uid = claims.UID
		displayName = claims.Username
	} else if apiKeyStr != "" {
		// API Key authentication for bots
		parsedUID, err := ParseAPIKey(apiKeyStr)
		if err != nil {
			http.Error(w, "invalid api key format", http.StatusUnauthorized)
			return
		}
		// Verify the API key exists in database
		botUID, err := hub.db.GetBotByAPIKey(apiKeyStr)
		if err != nil || botUID != parsedUID {
			http.Error(w, "invalid api key", http.StatusUnauthorized)
			return
		}
		uid = parsedUID
		acctType = types.AccountBot
	} else {
		http.Error(w, "missing token or api_key", http.StatusUnauthorized)
		return
	}

	// Get user info for both JWT and API Key
	usr, _ := hub.db.GetUser(uid)
	if usr != nil {
		acctType = usr.AccountType
		if usr.DisplayName != "" {
			displayName = usr.DisplayName
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}

	client := &Client{
		hub:         hub,
		conn:        conn,
		uid:         uid,
		remoteAddr:  requestRemoteAddr(r),
		displayName: displayName,
		accountType: acctType,
		send:        make(chan []byte, 256),
	}

	registration := hub.registerClient(client)
	if !registration.accepted {
		log.Printf("client connection rejected: uid=%d addr=%s account=%s reason=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, registration.reason, registration.deviceCount, registration.onlineUsers)
		rejectWebSocketConnection(conn, registration.reason)
		return
	}

	log.Printf("client connected: uid=%d addr=%s account=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, registration.deviceCount, registration.onlineUsers)
	if registration.firstConn {
		hub.enqueuePresence(client.uid, "on")
	}

	go client.WritePump()
	go client.ReadPump(hub.handleMessage)
}

func requestRemoteAddr(r *http.Request) string {
	if r == nil {
		return ""
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}

	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			if addr := strings.TrimSpace(parts[0]); addr != "" {
				return addr
			}
		}
	}

	return r.RemoteAddr
}

// handleMessage dispatches incoming client messages.
func (h *Hub) handleMessage(client *Client, msg *ClientMessage) {
	switch {
	case msg.Pub != nil:
		h.handlePub(client, msg.Pub)
	case msg.Sub != nil:
		h.handleSub(client, msg.Sub)
	case msg.Note != nil:
		h.handleNote(client, msg.Note)
	case msg.Hi != nil:
		h.handleHi(client, client.displayName, msg.Hi)
	case msg.Get != nil:
		h.handleGet(client, msg.Get)
	}
}

// handleHi responds to the handshake message.
func (h *Hub) handleHi(client *Client, displayName string, msg *MsgClientHi) {
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:   msg.ID,
			Code: 200,
			Text: "ok",
			Params: map[string]interface{}{
				"ver":   "0.1.0",
				"build": "catscompany",
				"uid":   formatUID(client.uid),
				"name":  displayName,
			},
		},
	})
}

// handlePub handles a publish (send message) request.
func (h *Hub) handlePub(client *Client, msg *MsgClientPub) {
	uid := client.uid
	topic := msg.Topic
	if isStreamPub(msg) {
		h.handleStreamPub(client, msg, topic)
		return
	}

	// Rate limit check
	if h.rateLimiter != nil {
		if !h.rateLimiter.Allow(uid, client.accountType) {
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 429, Text: "rate limit exceeded"},
			})
			return
		}
	}

	req := messageRequestFromPub(msg)
	payload, err := normalizeMessageRequest(req)
	if err != nil {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: err.Error()},
		})
		return
	}

	// Route based on topic type
	if isGroupTopic(topic) {
		h.handleGroupPub(client, msg, topic, payload)
		return
	}

	// --- P2P message handling ---

	// Bot-to-Bot loop protection
	peerUID := extractPeerUID(topic, uid)
	if peerUID > 0 && !h.checkBotToBot(uid, peerUID) {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 429, Text: "bot-to-bot conversation limit reached"},
		})
		return
	}

	// Ensure topic exists
	h.db.CreateTopic(topic, "p2p", uid)

	if isTransientRuntimePayload(payload) {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{
				ID:    msg.ID,
				Topic: topic,
				Code:  200,
				Text:  "ok",
				Params: map[string]interface{}{
					"seq": 0,
				},
			},
		})
		h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, 0, client)
		return
	}

	msgID, err := saveNormalizedMessage(h.db, topic, uid, msg.ReplyTo, payload)
	if err != nil {
		log.Printf("save message error: %v", err)
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 500, Text: "save failed"},
		})
		return
	}

	// Confirm to sender
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:    msg.ID,
			Topic: topic,
			Code:  200,
			Text:  "ok",
			Params: map[string]interface{}{
				"seq": msgID,
			},
		},
	})

	h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, msgID, client)
}

func isStreamPub(msg *MsgClientPub) bool {
	if msg == nil {
		return false
	}
	msgType := strings.TrimSpace(firstNonEmpty(msg.Type, msg.MsgType))
	return msgType == "stream_delta" || msgType == "stream_cancel"
}

func (h *Hub) handleStreamPub(client *Client, msg *MsgClientPub, topic string) {
	uid := client.uid
	streamID := firstMetadataString(msg.Metadata, "stream_id")
	streamType := strings.TrimSpace(firstNonEmpty(msg.Type, msg.MsgType))
	if strings.TrimSpace(topic) == "" {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "topic required"},
		})
		return
	}
	if streamID == "" {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "stream_id required"},
		})
		return
	}

	_, displayContent := normalizeRawContent(msg.Content)
	delta := normalizeContentText(displayContent)

	if isGroupTopic(topic) {
		groupID := extractGroupID(topic)
		if groupID == 0 {
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "invalid group topic"},
			})
			return
		}

		isMember, err := h.db.IsGroupMember(groupID, uid)
		if err != nil || !isMember {
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 403, Text: "not a group member"},
			})
			return
		}

		isMuted, _ := h.db.IsMemberMuted(groupID, uid)
		if isMuted {
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 403, Text: "you are muted in this group"},
			})
			return
		}

		h.SendToClient(client, streamDeltaAck(msg.ID, topic, streamID))
		if delta != "" || streamType == "stream_cancel" {
			h.fanoutStreamEvent(uid, topic, streamType, delta, msg.Metadata, client)
		}
		return
	}

	peerUID := extractPeerUID(topic, uid)
	if peerUID == 0 {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "invalid p2p topic"},
		})
		return
	}

	h.db.CreateTopic(topic, "p2p", uid)
	h.SendToClient(client, streamDeltaAck(msg.ID, topic, streamID))
	if delta != "" || streamType == "stream_cancel" {
		h.fanoutStreamEvent(uid, topic, streamType, delta, msg.Metadata, client)
	}
}

func streamDeltaAck(id, topic, streamID string) *ServerMessage {
	return &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:    id,
			Topic: topic,
			Code:  200,
			Text:  "ok",
			Params: map[string]interface{}{
				"stream_id": streamID,
			},
		},
	}
}

func (h *Hub) fanoutStreamEvent(uid int64, topicID string, streamType string, content string, metadata map[string]interface{}, exclude *Client) {
	if h == nil {
		return
	}
	if streamType == "" {
		streamType = "stream_delta"
	}
	streamMetadata := map[string]interface{}{}
	for key, value := range metadata {
		streamMetadata[key] = value
	}
	streamMetadata["stream_event"] = strings.TrimPrefix(streamType, "stream_")

	dataMsg := &ServerMessage{
		Data: &MsgServerData{
			Topic:    topicID,
			From:     formatUID(uid),
			SeqID:    0,
			Content:  content,
			Type:     streamType,
			MsgType:  "text",
			Metadata: streamMetadata,
			Mode:     "stream",
			Role:     "assistant",
		},
	}

	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID == 0 {
			return
		}
		h.broadcastToGroup(groupID, dataMsg, uid)
		return
	}

	peerUID := extractPeerUID(topicID, uid)
	if peerUID == 0 {
		return
	}
	h.SendToUserExcept(uid, dataMsg, exclude)
	h.SendToUser(peerUID, dataMsg)
}

// handleGroupPub handles publishing a message to a group topic.
func (h *Hub) handleGroupPub(client *Client, msg *MsgClientPub, topic string, payload *normalizedMessagePayload) {
	uid := client.uid
	groupID := extractGroupID(topic)
	if groupID == 0 {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "invalid group topic"},
		})
		return
	}

	// Verify sender is a group member
	isMember, err := h.db.IsGroupMember(groupID, uid)
	if err != nil || !isMember {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 403, Text: "not a group member"},
		})
		return
	}

	// Check if member is muted
	isMuted, _ := h.db.IsMemberMuted(groupID, uid)
	if isMuted {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 403, Text: "you are muted in this group"},
		})
		return
	}

	if isTransientRuntimePayload(payload) {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{
				ID:    msg.ID,
				Topic: topic,
				Code:  200,
				Text:  "ok",
				Params: map[string]interface{}{
					"seq": 0,
				},
			},
		})
		h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, 0, client)
		return
	}

	msgID, err := saveNormalizedMessage(h.db, topic, uid, msg.ReplyTo, payload)
	if err != nil {
		log.Printf("save group message error: %v", err)
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 500, Text: "save failed"},
		})
		return
	}

	// Confirm to sender
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:    msg.ID,
			Topic: topic,
			Code:  200,
			Text:  "ok",
			Params: map[string]interface{}{
				"seq": msgID,
			},
		},
	})

	h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, msgID, client)
}

func messageRequestFromPub(msg *MsgClientPub) *SendMessageRequest {
	if msg == nil {
		return nil
	}
	return &SendMessageRequest{
		TopicID:       msg.Topic,
		Content:       msg.Content,
		ContentBlocks: msg.ContentBlocks,
		Metadata:      msg.Metadata,
		MsgType:       msg.MsgType,
		Type:          msg.Type,
		Mode:          msg.Mode,
		Role:          msg.Role,
		ReplyTo:       msg.ReplyTo,
	}
}

// broadcastToGroup sends a message to all online members of a group.
// If excludeUID > 0, that user is skipped.
func (h *Hub) broadcastToGroup(groupID int64, msg *ServerMessage, excludeUID int64) {
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		log.Printf("broadcastToGroup: failed to get members for group %d: %v", groupID, err)
		return
	}
	for _, m := range members {
		if m.UserID == excludeUID {
			continue
		}
		h.SendToUser(m.UserID, msg)
	}
}

// isGroupTopic checks if a topic ID is a group topic.
func isGroupTopic(topic string) bool {
	return len(topic) > 4 && topic[:4] == "grp_"
}

// extractGroupID extracts the group ID from a group topic string "grp_{id}".
func extractGroupID(topic string) int64 {
	if !isGroupTopic(topic) {
		return 0
	}
	return parseInt64(topic[4:])
}

// handleSub handles a subscribe request (join a topic).
func (h *Hub) handleSub(client *Client, msg *MsgClientSub) {
	// For now, just acknowledge the subscription
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:    msg.ID,
			Topic: msg.Topic,
			Code:  200,
			Text:  "ok",
		},
	})
}

// handleGet handles data retrieval requests (message history, online status).
func (h *Hub) handleGet(client *Client, msg *MsgClientGet) {
	uid := client.uid
	switch msg.What {
	case "online":
		// Return online status of friends and owned bots.
		onlineList, err := BuildOnlineStatusList(h.db, h, uid)
		if err != nil {
			return
		}
		h.SendToClient(client, &ServerMessage{
			Meta: &MsgServerMeta{
				ID:    msg.ID,
				Topic: msg.Topic,
				Sub:   onlineList,
			},
		})

	case "history":
		// Fetch messages after a given seq ID for reconnection
		sinceID := int64(msg.SeqID)
		msgs, err := h.db.GetMessagesSince(msg.Topic, sinceID, 100)
		if err != nil {
			log.Printf("get history error: %v", err)
			return
		}
		// Send each message as a data message
		for _, m := range msgs {
			h.SendToClient(client, &ServerMessage{
				Data: &MsgServerData{
					Topic:         m.TopicID,
					From:          formatUID(m.FromUID),
					SeqID:         int(m.ID),
					Content:       decodeStoredContent(m.Content),
					Type:          inferDisplayTypeFromStoredMessage(m.MsgType, m.Content, m.ContentBlocks),
					MsgType:       m.MsgType,
					ContentBlocks: m.ContentBlocks,
					Mode:          m.Mode,
					Role:          m.Role,
				},
			})
		}
		// Send ctrl to indicate history is complete
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{
				ID:    msg.ID,
				Topic: msg.Topic,
				Code:  200,
				Text:  "history complete",
			},
		})
	}
}

// handleNote handles typing indicators and read receipts.
func (h *Hub) handleNote(client *Client, msg *MsgClientNote) {
	uid := client.uid
	infoMsg := &ServerMessage{
		Info: &MsgServerInfo{
			Topic: msg.Topic,
			From:  formatUID(uid),
			What:  msg.What,
			SeqID: msg.SeqID,
		},
	}

	// Group topic: broadcast to all members except sender
	if isGroupTopic(msg.Topic) {
		groupID := extractGroupID(msg.Topic)
		if groupID == 0 {
			return
		}
		h.broadcastToGroup(groupID, infoMsg, uid)
		return
	}

	// P2P topic: send to peer
	peerUID := extractPeerUID(msg.Topic, uid)
	if peerUID == 0 {
		return
	}
	h.SendToUser(peerUID, infoMsg)
}

// formatUID converts a numeric UID to a string identifier.
func formatUID(uid int64) string {
	return fmt.Sprintf("usr%d", uid)
}

// extractPeerUID extracts the other user's ID from a p2p topic ID.
// Topic format: "p2p_{smallerUID}_{largerUID}"
func extractPeerUID(topic string, selfUID int64) int64 {
	if len(topic) < 5 || topic[:4] != "p2p_" {
		return 0
	}
	rest := topic[4:]
	for i, c := range rest {
		if c == '_' {
			uid1 := parseInt64(rest[:i])
			uid2 := parseInt64(rest[i+1:])
			if uid1 == selfUID {
				return uid2
			}
			return uid1
		}
	}
	return 0
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func (h *Hub) getClient(uid int64) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients[uid] {
		return client
	}
	return nil
}

// --- Bot-to-Bot loop protection ---

type botConvoTracker struct {
	mu       sync.Mutex
	counters map[string]*botConvoCount
}

type botConvoCount struct {
	count   int
	resetAt time.Time
}

const botConvoMaxTurns = 50 // max turns per 5 minutes between two bots
const botConvoWindow = 5 * time.Minute

func (h *Hub) checkBotToBot(senderUID, peerUID int64) bool {
	senderClient := h.getClient(senderUID)
	peerClient := h.getClient(peerUID)
	if senderClient == nil || peerClient == nil {
		return true
	}
	if senderClient.accountType != types.AccountBot || peerClient.accountType != types.AccountBot {
		return true // not bot-to-bot
	}

	// Generate a canonical key for this bot pair
	key := fmt.Sprintf("b2b_%d_%d", min64(senderUID, peerUID), max64(senderUID, peerUID))

	h.botConvo.mu.Lock()
	defer h.botConvo.mu.Unlock()

	cc, ok := h.botConvo.counters[key]
	now := time.Now()
	if !ok || now.After(cc.resetAt) {
		h.botConvo.counters[key] = &botConvoCount{count: 1, resetAt: now.Add(botConvoWindow)}
		return true
	}
	cc.count++
	if cc.count > botConvoMaxTurns {
		return false
	}
	return true
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// parseMentions extracts @usr123 style mentions from message content.
func parseMentions(content interface{}) []string {
	var text string
	switch v := content.(type) {
	case string:
		text = v
	case map[string]interface{}:
		if t, ok := v["text"].(string); ok {
			text = t
		}
	}

	if text == "" {
		return nil
	}

	// Match @usr123 pattern
	re := regexp.MustCompile(`@usr(\d+)`)
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	mentions := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) > 1 {
			uid := "usr" + m[1]
			if !seen[uid] {
				seen[uid] = true
				mentions = append(mentions, uid)
			}
		}
	}
	return mentions
}

// broadcastToGroupWithMentions sends a message to all online members with Bot @trigger filtering.
// Bots only receive the message if they are mentioned or if there are no mentions at all.
func (h *Hub) broadcastToGroupWithMentions(groupID int64, msg *ServerMessage, excludeUID int64, mentions []string, senderUID int64) {
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		log.Printf("broadcastToGroupWithMentions: failed to get members for group %d: %v", groupID, err)
		return
	}

	// Convert mentions to a set for quick lookup
	mentionSet := make(map[string]bool)
	for _, m := range mentions {
		mentionSet[m] = true
	}

	for _, m := range members {
		if m.UserID == excludeUID {
			continue
		}

		// Check if this is a Bot
		client := h.getClient(m.UserID)
		isBot := client != nil && client.accountType == types.AccountBot

		if isBot {
			// Bots only receive message if:
			// 1. They are mentioned, OR
			// 2. There are no mentions at all (broadcast to all)
			userIDStr := formatUID(m.UserID)
			if len(mentions) > 0 && !mentionSet[userIDStr] {
				// Bot not mentioned and there are mentions - skip
				continue
			}
		}

		h.SendToUser(m.UserID, msg)
	}
}
