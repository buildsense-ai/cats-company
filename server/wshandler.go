// Package server implements the WebSocket hub and client connections for Cats Company.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openchat/openchat/server/db/mysql"
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
	clients     map[int64]*Client
	register    chan *Client
	unregister  chan *Client
	db          *mysql.Adapter
	rateLimiter *RateLimiter
	botStats    *BotStats
	botConvo    botConvoTracker
}

// Client represents a single WebSocket connection.
type Client struct {
	hub         *Hub
	conn        *websocket.Conn
	uid         int64
	accountType types.AccountType
	send        chan []byte
}

// NewHub creates a new Hub.
func NewHub(db *mysql.Adapter, rl *RateLimiter) *Hub {
	return &Hub{
		clients:     make(map[int64]*Client),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		db:          db,
		rateLimiter: rl,
		botStats:    NewBotStats(),
		botConvo:    botConvoTracker{counters: make(map[string]*botConvoCount)},
	}
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
			h.mu.Lock()
			// Close existing connection for same user (single-device for now)
			if old, ok := h.clients[client.uid]; ok {
				close(old.send)
				old.conn.Close()
			}
			h.clients[client.uid] = client
			h.mu.Unlock()
			log.Printf("client connected: uid=%d (online: %d)", client.uid, h.OnlineCount())
			h.broadcastPresence(client.uid, "on")

		case client := <-h.unregister:
			h.mu.Lock()
			if cur, ok := h.clients[client.uid]; ok && cur == client {
				delete(h.clients, client.uid)
				close(client.send)
			}
			h.mu.Unlock()
			log.Printf("client disconnected: uid=%d (online: %d)", client.uid, h.OnlineCount())
			h.broadcastPresence(client.uid, "off")
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

// broadcastPresence notifies all friends of a user's online/offline status.
func (h *Hub) broadcastPresence(uid int64, what string) {
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
	for _, f := range friends {
		h.SendToUser(f.ID, msg)
	}
}

// ServeWS handles WebSocket upgrade requests with JWT or API Key authentication.
func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	var uid int64

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
	} else {
		http.Error(w, "missing token or api_key", http.StatusUnauthorized)
		return
	}

	// Look up account type for rate limiting
	usr, _ := hub.db.GetUser(uid)
	acctType := types.AccountHuman
	if usr != nil {
		acctType = usr.AccountType
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
		accountType: acctType,
		send:        make(chan []byte, 256),
	}

	hub.register <- client

	go client.WritePump()
	go client.ReadPump(hub.handleMessage)
}

// handleMessage dispatches incoming client messages.
func (h *Hub) handleMessage(uid int64, msg *ClientMessage) {
	switch {
	case msg.Pub != nil:
		h.handlePub(uid, msg.Pub)
	case msg.Sub != nil:
		h.handleSub(uid, msg.Sub)
	case msg.Note != nil:
		h.handleNote(uid, msg.Note)
	case msg.Hi != nil:
		h.handleHi(uid, msg.Hi)
	case msg.Get != nil:
		h.handleGet(uid, msg.Get)
	}
}

// handleHi responds to the handshake message.
func (h *Hub) handleHi(uid int64, msg *MsgClientHi) {
	h.SendToUser(uid, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:   msg.ID,
			Code: 200,
			Text: "ok",
			Params: map[string]interface{}{
				"ver":    "0.1.0",
				"build":  "catscompany",
				"uid":    formatUID(uid),
			},
		},
	})
}

// handlePub handles a publish (send message) request.
func (h *Hub) handlePub(uid int64, msg *MsgClientPub) {
	// Rate limit check
	if h.rateLimiter != nil {
		client := h.getClient(uid)
		acctType := types.AccountHuman
		if client != nil {
			acctType = client.accountType
		}
		if !h.rateLimiter.Allow(uid, acctType) {
			h.SendToUser(uid, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 429, Text: "rate limit exceeded"},
			})
			return
		}
	}

	topic := msg.Topic

	// Extract content - support both plain text and rich content
	var content string
	var msgType string = "text"
	switch v := msg.Content.(type) {
	case string:
		content = v
	case map[string]interface{}:
		// Rich content: { "type": "image", "payload": {...} }
		if t, ok := v["type"].(string); ok {
			msgType = t
		}
		contentBytes, _ := json.Marshal(v)
		content = string(contentBytes)
	default:
		contentBytes, _ := json.Marshal(msg.Content)
		content = string(contentBytes)
	}
	if content == "" {
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "empty content"},
		})
		return
	}

	// Route based on topic type
	if isGroupTopic(topic) {
		h.handleGroupPub(uid, msg, topic, content, msgType)
		return
	}

	// --- P2P message handling ---

	// Bot-to-Bot loop protection
	peerUID := extractPeerUID(topic, uid)
	if peerUID > 0 && !h.checkBotToBot(uid, peerUID) {
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 429, Text: "bot-to-bot conversation limit reached"},
		})
		return
	}

	// Ensure topic exists
	h.db.CreateTopic(topic, "p2p", uid)

	// Save to database (with optional reply_to)
	var msgID int64
	var err error
	if msg.ReplyTo > 0 {
		msgID, err = h.db.SaveMessageWithReply(topic, uid, content, msgType, int64(msg.ReplyTo))
	} else {
		msgID, err = h.db.SaveMessage(topic, uid, content, msgType)
	}
	if err != nil {
		log.Printf("save message error: %v", err)
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 500, Text: "save failed"},
		})
		return
	}

	// Confirm to sender
	h.SendToUser(uid, &ServerMessage{
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

	// Track bot stats
	if client := h.getClient(uid); client != nil && client.accountType == types.AccountBot {
		h.botStats.RecordSent(uid, topic)
	}
	if peerClient := h.getClient(peerUID); peerClient != nil && peerClient.accountType == types.AccountBot {
		h.botStats.RecordRecv(peerUID)
	}

	// Build the data message to deliver
	dataMsg := &ServerMessage{
		Data: &MsgServerData{
			Topic:   topic,
			From:    formatUID(uid),
			SeqID:   int(msgID),
			Content: msg.Content, // preserve original structure
			ReplyTo: msg.ReplyTo,
		},
	}

	// Deliver to the peer only (sender already got ctrl with seq)
	if peerUID > 0 {
		h.SendToUser(peerUID, dataMsg)
	}
}

// handleGroupPub handles publishing a message to a group topic.
func (h *Hub) handleGroupPub(uid int64, msg *MsgClientPub, topic, content, msgType string) {
	groupID := extractGroupID(topic)
	if groupID == 0 {
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 400, Text: "invalid group topic"},
		})
		return
	}

	// Verify sender is a group member
	isMember, err := h.db.IsGroupMember(groupID, uid)
	if err != nil || !isMember {
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 403, Text: "not a group member"},
		})
		return
	}

	// Check if member is muted
	isMuted, _ := h.db.IsMemberMuted(groupID, uid)
	if isMuted {
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 403, Text: "you are muted in this group"},
		})
		return
	}

	// Save to database (with optional reply_to)
	var msgID int64
	if msg.ReplyTo > 0 {
		msgID, err = h.db.SaveMessageWithReply(topic, uid, content, msgType, int64(msg.ReplyTo))
	} else {
		msgID, err = h.db.SaveMessage(topic, uid, content, msgType)
	}
	if err != nil {
		log.Printf("save group message error: %v", err)
		h.SendToUser(uid, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 500, Text: "save failed"},
		})
		return
	}

	// Confirm to sender
	h.SendToUser(uid, &ServerMessage{
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

	// Build the data message
	// Parse @mentions from content
	mentions := parseMentions(content)
	dataMsg := &ServerMessage{
		Data: &MsgServerData{
			Topic:    topic,
			From:     formatUID(uid),
			SeqID:    int(msgID),
			Content:  msg.Content,
			ReplyTo:  msg.ReplyTo,
			Mentions: mentions,
		},
	}

	// Broadcast to all group members except sender (sender already got ctrl with seq)
	// Pass mentions for Bot @trigger filtering
	h.broadcastToGroupWithMentions(groupID, dataMsg, uid, mentions, uid)
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
func (h *Hub) handleSub(uid int64, msg *MsgClientSub) {
	// For now, just acknowledge the subscription
	h.SendToUser(uid, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:    msg.ID,
			Topic: msg.Topic,
			Code:  200,
			Text:  "ok",
		},
	})
}

// handleGet handles data retrieval requests (message history, online status).
func (h *Hub) handleGet(uid int64, msg *MsgClientGet) {
	switch msg.What {
	case "online":
		// Return online status of friends
		friends, err := h.db.GetFriends(uid)
		if err != nil {
			return
		}
		onlineList := make([]map[string]interface{}, 0)
		for _, f := range friends {
			onlineList = append(onlineList, map[string]interface{}{
				"uid":    f.ID,
				"online": h.IsOnline(f.ID),
			})
		}
		h.SendToUser(uid, &ServerMessage{
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
			h.SendToUser(uid, &ServerMessage{
				Data: &MsgServerData{
					Topic:   m.TopicID,
					From:    formatUID(m.FromUID),
					SeqID:   int(m.ID),
					Content: m.Content,
				},
			})
		}
		// Send ctrl to indicate history is complete
		h.SendToUser(uid, &ServerMessage{
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
func (h *Hub) handleNote(uid int64, msg *MsgClientNote) {
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
	return h.clients[uid]
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
