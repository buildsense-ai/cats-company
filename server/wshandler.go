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
	mu            sync.RWMutex
	clients       map[int64]map[*Client]struct{}
	clientsByConn map[string]*Client
	register      chan *Client
	unregister    chan *Client
	presence      chan presenceEvent
	db            store.Store
	rateLimiter   *RateLimiter
	botStats      *BotStats
	botConvo      botConvoTracker
	nodeID        string
	sharedRuntime sharedRuntimeState
	bodyLeases    *botBodyLeaseManager
	userDevices   *userDeviceRegistry
	deviceAudit   *deviceAuditLog
	deviceRevokes *deviceConnectorRevocationList
	deviceClients map[int64]map[string]*Client
	deviceRPC     *deviceRPCRouter
	channelOut    *ChannelOutboundDispatcher
}

type presenceEvent struct {
	uid  int64
	what string
}

// Client represents a single WebSocket connection.
type Client struct {
	hub                  *Hub
	conn                 *websocket.Conn
	uid                  int64
	remoteAddr           string
	displayName          string
	accountType          types.AccountType
	bodyID               string
	installationID       string
	connectionID         string
	deviceOwnerUID       int64
	deviceID             string
	deviceBodyID         string
	deviceInstallationID string
	deviceConnector      *DeviceConnectorClaims
	send                 chan []byte
	sendMu               sync.RWMutex
	sendClosed           bool
}

// NewHub creates a new Hub.
func NewHub(db store.Store, rl *RateLimiter) *Hub {
	return NewHubWithRuntime(db, rl, nil, "")
}

func NewHubWithRuntime(db store.Store, rl *RateLimiter, shared sharedRuntimeState, nodeID string) *Hub {
	if strings.TrimSpace(nodeID) == "" {
		nodeID = newRuntimeNodeID()
	}
	hub := &Hub{
		clients:       make(map[int64]map[*Client]struct{}),
		clientsByConn: make(map[string]*Client),
		register:      make(chan *Client, 256),
		unregister:    make(chan *Client, 256),
		presence:      make(chan presenceEvent, 256),
		db:            db,
		rateLimiter:   rl,
		botStats:      NewBotStats(),
		botConvo:      botConvoTracker{counters: make(map[string]*botConvoCount)},
		nodeID:        nodeID,
		sharedRuntime: shared,
		bodyLeases:    newBotBodyLeaseManager(defaultBotBodyLeaseTTL).withSharedRuntime(shared, nodeID),
		userDevices:   newUserDeviceRegistry(defaultUserDeviceTTL).withSharedRuntime(shared),
		deviceAudit:   newDeviceAuditLog(),
		deviceRevokes: newDeviceConnectorRevocationList(),
		deviceClients: make(map[int64]map[string]*Client),
		deviceRPC:     newDeviceRPCRouter(defaultDeviceRPCTTL).withSharedRuntime(shared),
	}
	if shared != nil {
		shared.registerRuntimeNode(nodeID, hub)
	}
	go hub.runPresence()
	go hub.runDeviceRPCTimeouts()
	return hub
}

// BotStats returns the hub's bot stats tracker.
func (h *Hub) BotStats() *BotStats {
	return h.botStats
}

func (h *Hub) BotBodyStatus(botUID int64) BotBodyStatus {
	status := BotBodyStatus{BotUID: botUID, State: "offline", Active: false}
	if h == nil || h.bodyLeases == nil || botUID <= 0 {
		return status
	}
	status.RuntimeMode = h.RuntimeMode()
	status.RouteState = h.RuntimeRouteState()
	lease, ok := h.bodyLeases.status(botUID)
	if !ok {
		return status
	}

	if !h.hasRegisteredBotBodyClient(lease) {
		status.BodyID = lease.bodyID
		status.Bound = lease.bodyID != ""
		return status
	}

	connectedAt := lease.acquiredAt
	expiresAt := lease.expiresAt
	ttl := time.Until(expiresAt).Milliseconds()
	if ttl < 0 {
		ttl = 0
	}
	if h.bodyLeases != nil && h.bodyLeases.now != nil {
		ttl = expiresAt.Sub(h.bodyLeases.now()).Milliseconds()
		if ttl < 0 {
			ttl = 0
		}
	}
	status.State = "online"
	status.Active = true
	status.BodyID = lease.bodyID
	status.Bound = lease.bodyID != ""
	status.ConnectedAt = &connectedAt
	status.LeaseExpiresAt = &expiresAt
	status.LeaseTTLMS = ttl
	return status
}

func (h *Hub) hasRegisteredBotBodyClient(lease botBodyLease) bool {
	if h == nil || lease.botUID <= 0 || lease.bodyID == "" || lease.connectionID == "" {
		return false
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients[lease.botUID] {
		if client.accountType == types.AccountBot &&
			client.bodyID == lease.bodyID &&
			client.connectionID == lease.connectionID {
			return true
		}
	}
	return false
}

func (h *Hub) RuntimeMode() string {
	if h != nil && h.bodyLeases != nil {
		return h.bodyLeases.runtimeMode()
	}
	return "process"
}

func (h *Hub) RuntimeRouteState() string {
	if h == nil {
		return "unavailable"
	}
	if h.sharedRuntime != nil {
		return h.sharedRuntime.runtimeRouteState()
	}
	return "process_local"
}

// Run starts the hub's main loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.registerClient(client)

		case client := <-h.unregister:
			removed, lastConn, remaining, onlineUsers := h.removeClient(client)
			if !removed {
				continue
			}
			client.closeSend()
			h.releaseBotBodyLease(client)
			h.clearClientRuntimeRoute(client)
			h.unbindDeviceClient(client)
			if client.accountType == types.AccountBot {
				log.Printf("client disconnected: uid=%d addr=%s account=%s body=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, client.bodyID, remaining, onlineUsers)
			} else {
				log.Printf("client disconnected: uid=%d addr=%s account=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, remaining, onlineUsers)
			}
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
	addUser := func(id int64, isBot bool) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		online := hub != nil && hub.IsOnline(id)
		if isBot {
			online = hub != nil && hub.BotBodyStatus(id).Active
		}
		seen[id] = struct{}{}
		onlineList = append(onlineList, map[string]interface{}{
			"uid":    id,
			"online": online,
		})
	}

	for _, friend := range friends {
		addUser(friend.ID, friend.AccountType == types.AccountBot || friend.BotDisclose)
	}

	bots, err := db.ListBotsByOwner(uid)
	if err != nil {
		log.Printf("online status: failed to list owner bots for uid=%d: %v", uid, err)
		return onlineList, nil
	}
	for _, bot := range bots {
		addUser(mapID(bot["id"]), true)
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

func (h *Hub) addClient(client *Client) (firstConn bool, deviceCount int, onlineUsers int) {
	h.ensureClientRuntimeRoute(client)
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.clients[client.uid]
	firstConn = client.deviceConnector == nil && !hasMessagingClient(clients)
	if clients == nil {
		clients = make(map[*Client]struct{})
		h.clients[client.uid] = clients
	}
	clients[client] = struct{}{}
	if client.connectionID != "" {
		h.clientsByConn[client.connectionID] = client
	}

	return firstConn, len(clients), len(h.clients)
}

func (h *Hub) registerClient(client *Client) bool {
	if client == nil {
		return false
	}
	if client.accountType == types.AccountBot && !h.bodyLeases.isCurrent(client.uid, client.bodyID, client.connectionID) {
		client.closeSend()
		if client.conn != nil {
			_ = client.conn.Close()
		}
		return false
	}

	firstConn, deviceCount, onlineUsers, replaced := h.addRegisteredClient(client)
	for _, stale := range replaced {
		h.clearClientRuntimeRoute(stale)
		h.unbindDeviceClient(stale)
		stale.closeSend()
		if stale.conn != nil {
			_ = stale.conn.Close()
		}
	}

	if client.accountType == types.AccountBot {
		log.Printf("client connected: uid=%d addr=%s account=%s body=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, client.bodyID, deviceCount, onlineUsers)
	} else {
		log.Printf("client connected: uid=%d addr=%s account=%s (devices: %d, online users: %d)", client.uid, client.remoteAddr, client.accountType, deviceCount, onlineUsers)
	}
	if firstConn {
		h.enqueuePresence(client.uid, "on")
	}
	h.bindClientRuntimeRoute(client)
	return true
}

func (h *Hub) addRegisteredClient(client *Client) (firstConn bool, deviceCount int, onlineUsers int, replaced []*Client) {
	h.ensureClientRuntimeRoute(client)
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.clients[client.uid]
	firstConn = client.deviceConnector == nil && !hasMessagingClient(clients)
	if clients == nil {
		clients = make(map[*Client]struct{})
		h.clients[client.uid] = clients
	}

	if client.accountType == types.AccountBot && client.bodyID != "" {
		for existing := range clients {
			shouldReplace := existing.accountType == types.AccountBot && existing.bodyID == client.bodyID
			if existing.accountType == types.AccountBot && isLegacyBotBodyID(existing.bodyID) && !isLegacyBotBodyID(client.bodyID) {
				shouldReplace = true
			}
			if shouldReplace {
				delete(clients, existing)
				replaced = append(replaced, existing)
			}
		}
	}
	clients[client] = struct{}{}
	if client.connectionID != "" {
		h.clientsByConn[client.connectionID] = client
	}

	return firstConn, len(clients), len(h.clients), replaced
}

func (h *Hub) ensureClientRuntimeRoute(client *Client) {
	if h == nil || client == nil {
		return
	}
	if client.hub == nil {
		client.hub = h
	}
	if client.connectionID == "" {
		client.connectionID = newBotBodyConnectionID()
	}
}

func (h *Hub) clientRoute(client *Client) runtimeRoute {
	if h == nil || client == nil || client.connectionID == "" {
		return runtimeRoute{}
	}
	return runtimeRoute{
		NodeID:       h.nodeID,
		ConnectionID: client.connectionID,
		ExpiresAt:    nowForRoute(h).Add(defaultUserDeviceTTL),
	}
}

func (h *Hub) bindClientRuntimeRoute(client *Client) {
	if h == nil || client == nil || h.sharedRuntime == nil {
		return
	}
	route := h.clientRoute(client)
	now := nowForRoute(h)
	route.ExpiresAt = now.Add(defaultUserDeviceTTL)
	h.sharedRuntime.bindRuntimeRoute(route, now)
}

func (h *Hub) clearClientRuntimeRoute(client *Client) {
	if h == nil || client == nil || h.sharedRuntime == nil {
		return
	}
	h.sharedRuntime.clearRuntimeRoute(h.clientRoute(client))
}

func (h *Hub) bindDeviceClient(ownerUID int64, device UserDevice, client *Client) {
	if h == nil || client == nil || ownerUID <= 0 || device.DeviceID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.deviceClients == nil {
		h.deviceClients = make(map[int64]map[string]*Client)
	}
	if client.deviceOwnerUID > 0 && client.deviceID != "" && (client.deviceOwnerUID != ownerUID || client.deviceID != device.DeviceID) {
		if previousOwnerDevices := h.deviceClients[client.deviceOwnerUID]; previousOwnerDevices != nil && previousOwnerDevices[client.deviceID] == client {
			delete(previousOwnerDevices, client.deviceID)
			if len(previousOwnerDevices) == 0 {
				delete(h.deviceClients, client.deviceOwnerUID)
			}
		}
	}
	ownerDevices := h.deviceClients[ownerUID]
	if ownerDevices == nil {
		ownerDevices = make(map[string]*Client)
		h.deviceClients[ownerUID] = ownerDevices
	}
	if existing := ownerDevices[device.DeviceID]; existing != nil && existing != client {
		existing.deviceOwnerUID = 0
		existing.deviceID = ""
		existing.deviceBodyID = ""
		existing.deviceInstallationID = ""
	}
	ownerDevices[device.DeviceID] = client
	client.deviceOwnerUID = ownerUID
	client.deviceID = device.DeviceID
	client.deviceBodyID = device.BodyID
	client.deviceInstallationID = device.InstallationID
	if h.sharedRuntime != nil {
		route := h.clientRoute(client)
		now := nowForRoute(h)
		route.ExpiresAt = now.Add(defaultUserDeviceTTL)
		h.sharedRuntime.bindRuntimeRoute(route, now)
		h.sharedRuntime.bindUserDeviceRoute(ownerUID, device, route, now)
	}
}

func (h *Hub) disconnectDeviceConnector(ownerUID int64, deviceID string, reason string) {
	if h == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	var client *Client
	h.mu.RLock()
	if ownerDevices := h.deviceClients[ownerUID]; ownerDevices != nil {
		client = ownerDevices[deviceID]
	}
	h.mu.RUnlock()
	if client == nil || client.deviceConnector == nil {
		return
	}
	h.disconnectClient(client, reason)
}

func (h *Hub) unbindDeviceClient(client *Client) {
	if h == nil || client == nil || client.deviceOwnerUID <= 0 || client.deviceID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	ownerDevices := h.deviceClients[client.deviceOwnerUID]
	if ownerDevices != nil && ownerDevices[client.deviceID] == client {
		delete(ownerDevices, client.deviceID)
		if len(ownerDevices) == 0 {
			delete(h.deviceClients, client.deviceOwnerUID)
		}
	}
	if h.sharedRuntime != nil {
		h.sharedRuntime.clearUserDeviceRoute(client.deviceOwnerUID, client.deviceID, h.clientRoute(client))
	}
	client.deviceOwnerUID = 0
	client.deviceID = ""
	client.deviceBodyID = ""
	client.deviceInstallationID = ""
}

func (h *Hub) getDeviceClient(ownerUID int64, deviceID string) *Client {
	if h == nil || ownerUID <= 0 || deviceID == "" {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.deviceClients[ownerUID][deviceID]
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
	if client.connectionID != "" && h.clientsByConn[client.connectionID] == client {
		delete(h.clientsByConn, client.connectionID)
	}
	removed = true
	remaining = len(clients)
	if remaining == 0 {
		delete(h.clients, client.uid)
		lastConn = client.deviceConnector == nil
	} else if client.deviceConnector == nil && !hasMessagingClient(clients) {
		lastConn = true
	}

	return removed, lastConn, remaining, len(h.clients)
}

func hasMessagingClient(clients map[*Client]struct{}) bool {
	for client := range clients {
		if client != nil && client.deviceConnector == nil {
			return true
		}
	}
	return false
}

func (h *Hub) releaseBotBodyLease(client *Client) {
	if client == nil || client.accountType != types.AccountBot {
		return
	}
	h.bodyLeases.release(client.uid, client.bodyID, client.connectionID)
}

func (h *Hub) renewBotBodyLease(client *Client) {
	if h == nil || client == nil || client.accountType != types.AccountBot {
		return
	}
	h.bodyLeases.renew(client.uid, client.bodyID, client.connectionID)
}

func (h *Hub) getClientByConnectionID(connectionID string) *Client {
	if h == nil || connectionID == "" {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clientsByConn[connectionID]
}

func (h *Hub) sendDeviceRPCToLocalRoute(route runtimeRoute, msg *MsgDeviceRPC) bool {
	if h == nil || route.ConnectionID == "" {
		return false
	}
	client := h.getClientByConnectionID(route.ConnectionID)
	if client == nil {
		return false
	}
	h.SendToClient(client, &ServerMessage{DeviceRPC: msg})
	return true
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
	isBotAPIKey := false
	var connectorClaims *DeviceConnectorClaims
	bodyID := ""
	installationID := ""
	connectionID := ""

	// Try JWT token first
	tokenStr := r.URL.Query().Get("token")
	connectorTokenStr := extractDeviceConnectorToken(r)
	apiKeyStr := r.Header.Get("X-API-Key")
	if apiKeyStr == "" {
		apiKeyStr = r.URL.Query().Get("api_key")
	}

	if connectorTokenStr != "" {
		claims, err := ParseDeviceConnectorToken(connectorTokenStr)
		if err != nil {
			http.Error(w, "invalid device connector token", http.StatusUnauthorized)
			return
		}
		if !deviceConnectorHasScope(claims, "device:ws") {
			http.Error(w, "device connector token cannot open websocket", http.StatusForbidden)
			return
		}
		if hub.isDeviceConnectorRevoked(claims) {
			http.Error(w, "device connector token has been revoked", http.StatusForbidden)
			return
		}
		uid = claims.UID
		displayName = firstNonEmpty(claims.DisplayName, claims.Username, claims.DeviceID)
		usr, err := hub.db.GetUser(uid)
		if err != nil || usr == nil {
			http.Error(w, "invalid device connector token", http.StatusUnauthorized)
			return
		}
		if usr.State != 0 {
			http.Error(w, "user account is disabled", http.StatusForbidden)
			return
		}
		if usr.AccountType != types.AccountHuman {
			http.Error(w, "device connector requires a human owner", http.StatusForbidden)
			return
		}
		acctType = types.AccountHuman
		connectorClaims = claims
		bodyID = claims.DeviceID
		installationID = firstNonEmpty(claims.InstallationID, claims.DeviceID)
	} else if tokenStr != "" {
		claims, err := ParseToken(tokenStr)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		uid = claims.UID
		displayName = claims.Username
		usr, err := hub.db.GetUser(uid)
		if err != nil || usr == nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if usr.State != 0 {
			http.Error(w, "user account is disabled", http.StatusForbidden)
			return
		}
		acctType = usr.AccountType
		if usr.DisplayName != "" {
			displayName = usr.DisplayName
		}
	} else if apiKeyStr != "" {
		parsedUID, err := ParseAPIKey(apiKeyStr)
		if err != nil {
			http.Error(w, "invalid api key format", http.StatusUnauthorized)
			return
		}
		botUID, err := hub.db.GetBotByAPIKey(apiKeyStr)
		if err != nil || botUID != parsedUID {
			http.Error(w, "invalid api key", http.StatusUnauthorized)
			return
		}
		usr, err := hub.db.GetUser(parsedUID)
		if err != nil || usr == nil {
			http.Error(w, "invalid api key", http.StatusUnauthorized)
			return
		}
		if usr.State != 0 {
			http.Error(w, "user account is disabled", http.StatusForbidden)
			return
		}
		uid = parsedUID
		acctType = usr.AccountType
		isBotAPIKey = true
		if usr.DisplayName != "" {
			displayName = usr.DisplayName
		}
	} else {
		http.Error(w, "missing token or api_key", http.StatusUnauthorized)
		return
	}

	if tokenStr != "" && acctType == types.AccountBot {
		http.Error(w, "bot websocket connections must use api key authentication", http.StatusForbidden)
		return
	}

	if isBotAPIKey {
		var err error
		bodyID, err = normalizeBotBodyID(r.Header.Get(botBodyIDHeader))
		installationID = normalizeDeviceText(r.Header.Get(botInstallationIDHeader))
		if err != nil {
			if strings.TrimSpace(r.Header.Get(botBodyIDHeader)) != "" || botBodyIDStrictMode() {
				http.Error(w, "missing or invalid bot body id", http.StatusBadRequest)
				return
			}
			bodyID = legacyBotBodyID(uid)
			boundBodyID, err := hub.db.GetBotBodyID(uid)
			if err != nil {
				log.Printf("legacy bot body lookup failed: uid=%d err=%v", uid, err)
				http.Error(w, "failed to verify bot body binding", http.StatusInternalServerError)
				return
			}
			if boundBodyID != "" {
				http.Error(w, fmt.Sprintf("bot is bound to body %s; update agent to send %s", boundBodyID, botBodyIDHeader), http.StatusConflict)
				return
			}
			log.Printf("legacy bot websocket without %s accepted temporarily: uid=%d addr=%s", botBodyIDHeader, uid, requestRemoteAddr(r))
		} else {
			boundBodyID, allowed, err := hub.db.EnsureBotBodyBinding(uid, bodyID)
			if err != nil {
				log.Printf("bot body binding failed: uid=%d body=%s err=%v", uid, bodyID, err)
				http.Error(w, "failed to verify bot body binding", http.StatusInternalServerError)
				return
			}
			if !allowed {
				if existing, ok := hub.bodyLeases.conflicts(uid, bodyID); ok {
					http.Error(w, fmt.Sprintf("bot already connected from body %s", existing.bodyID), http.StatusConflict)
					return
				}
				if err := hub.db.SetBotBodyBinding(uid, bodyID); err != nil {
					log.Printf("bot body auto rebind failed: uid=%d old_body=%s new_body=%s err=%v", uid, boundBodyID, bodyID, err)
					http.Error(w, "failed to update bot body binding", http.StatusInternalServerError)
					return
				}
				log.Printf("bot body auto rebound: uid=%d old_body=%s new_body=%s addr=%s", uid, boundBodyID, bodyID, requestRemoteAddr(r))
			}
		}
		if existing, ok := hub.bodyLeases.conflicts(uid, bodyID); ok {
			http.Error(w, fmt.Sprintf("bot already connected from body %s", existing.bodyID), http.StatusConflict)
			return
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	if isBotAPIKey {
		connectionID = newBotBodyConnectionID()
		result, err := hub.bodyLeases.acquire(uid, bodyID, connectionID)
		if err != nil {
			closeBotBodyRejectedConn(conn, result.Lease)
			return
		}
	}

	client := &Client{
		hub:             hub,
		conn:            conn,
		uid:             uid,
		remoteAddr:      requestRemoteAddr(r),
		displayName:     displayName,
		accountType:     acctType,
		bodyID:          bodyID,
		installationID:  installationID,
		connectionID:    connectionID,
		deviceConnector: connectorClaims,
		send:            make(chan []byte, 256),
	}

	if !hub.registerClient(client) {
		hub.bodyLeases.release(uid, bodyID, connectionID)
		return
	}

	go client.WritePump()
	go client.ReadPump(hub.handleMessage)
}

func closeBotBodyRejectedConn(conn *websocket.Conn, lease botBodyLease) {
	reason := "bot already connected"
	if lease.bodyID != "" {
		reason = fmt.Sprintf("bot already connected from body %s", lease.bodyID)
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason),
		time.Now().Add(writeWait),
	)
	_ = conn.Close()
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
	if client != nil && client.deviceConnector != nil && !deviceConnectorMessageAllowed(msg) {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{
				Code: http.StatusForbidden,
				Text: "device connector connections may only register a device and return device_rpc results",
			},
		})
		return
	}
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
	case msg.DeviceRPC != nil:
		h.handleDeviceRPC(client, msg.DeviceRPC)
	}
}

func deviceConnectorMessageAllowed(msg *ClientMessage) bool {
	if msg == nil {
		return false
	}
	if msg.Acc != nil || msg.Login != nil || msg.Sub != nil || msg.Pub != nil || msg.Get != nil || msg.Set != nil || msg.Del != nil || msg.Note != nil || msg.Friend != nil {
		return false
	}
	actions := 0
	if msg.Hi != nil {
		actions++
	}
	if msg.DeviceRPC != nil {
		actions++
		if strings.ToLower(strings.TrimSpace(msg.DeviceRPC.Type)) != deviceRPCTypeResult {
			return false
		}
	}
	return actions == 1
}

// handleHi responds to the handshake message.
func (h *Hub) handleHi(client *Client, displayName string, msg *MsgClientHi) {
	deviceParams, ok := h.bindClientDeviceFromHi(client, msg)
	if !ok {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{
				ID:   msg.ID,
				Code: 400,
				Text: "invalid device",
			},
		})
		return
	}
	params := map[string]interface{}{
		"ver":      "0.1.0",
		"build":    "catscompany",
		"features": []string{"client_msg_id", "device_rpc"},
		"uid":      formatUID(client.uid),
		"name":     displayName,
	}
	if client.accountType == types.AccountBot && client.bodyID != "" {
		params["body_lease"] = h.BotBodyStatus(client.uid)
	}
	if deviceParams != nil {
		params["device"] = deviceParams
	}
	h.SendToClient(client, &ServerMessage{
		Ctrl: &MsgServerCtrl{
			ID:     msg.ID,
			Code:   200,
			Text:   "ok",
			Params: params,
		},
	})
}

func (h *Hub) validateMessagePublish(uid int64, accountType types.AccountType, topic string, applyRateLimit bool) (int, string) {
	if h == nil {
		return 0, ""
	}
	if applyRateLimit && h.rateLimiter != nil {
		if !h.rateLimiter.Allow(uid, accountType) {
			return http.StatusTooManyRequests, "rate limit exceeded"
		}
	}

	if isGroupTopic(topic) {
		groupID := extractGroupID(topic)
		if groupID == 0 {
			return http.StatusBadRequest, "invalid group topic"
		}
		isMember, err := h.db.IsGroupMember(groupID, uid)
		if err != nil || !isMember {
			return http.StatusForbidden, "not a group member"
		}
		isMuted, _ := h.db.IsMemberMuted(groupID, uid)
		if isMuted {
			return http.StatusForbidden, "you are muted in this group"
		}
		return 0, ""
	}

	peerUID := extractPeerUID(topic, uid)
	if peerUID == 0 {
		return http.StatusBadRequest, "invalid p2p topic"
	}
	if code, text := validateAgentP2PMessageAccess(h.db, uid, accountType, peerUID); code != 0 {
		return code, text
	}
	if !h.checkBotToBot(uid, peerUID) {
		return http.StatusTooManyRequests, "bot-to-bot conversation limit reached"
	}
	return 0, ""
}

func (h *Hub) validateTopicReadAccess(uid int64, accountType types.AccountType, topic string) (int, string) {
	if h == nil {
		return 0, ""
	}
	if h.db == nil {
		return http.StatusInternalServerError, "topic access unavailable"
	}
	if isGroupTopic(topic) {
		groupID := extractGroupID(topic)
		if groupID == 0 {
			return http.StatusBadRequest, "invalid group topic"
		}
		isMember, err := h.db.IsGroupMember(groupID, uid)
		if err != nil || !isMember {
			return http.StatusForbidden, "not a group member"
		}
		return 0, ""
	}

	peerUID := extractPeerUID(topic, uid)
	if peerUID == 0 {
		return http.StatusBadRequest, "invalid p2p topic"
	}
	if code, text := validateAgentP2PMessageAccess(h.db, uid, accountType, peerUID); code != 0 {
		return code, text
	}
	return 0, ""
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

	if code, text := h.validateMessagePublish(uid, client.accountType, topic, false); code != 0 {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: code, Text: text},
		})
		return
	}

	// Route based on topic type
	if isGroupTopic(topic) {
		h.handleGroupPub(client, msg, topic, payload)
		return
	}

	// --- P2P message handling ---

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

	result, err := saveNormalizedMessage(h.db, topic, uid, msg.ReplyTo, payload)
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
				"seq":           result.ID,
				"duplicate":     result.Duplicate,
				"client_msg_id": payload.ClientMsgID,
			},
		},
	})

	if !result.Duplicate {
		h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, result.ID, client)
	}
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
	if code, text := h.validateMessagePublish(uid, client.accountType, topic, false); code != 0 {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: code, Text: text},
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

	result, err := saveNormalizedMessage(h.db, topic, uid, msg.ReplyTo, payload)
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
				"seq":           result.ID,
				"duplicate":     result.Duplicate,
				"client_msg_id": payload.ClientMsgID,
			},
		},
	})

	if !result.Duplicate {
		h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, result.ID, client)
	}
}

func messageRequestFromPub(msg *MsgClientPub) *SendMessageRequest {
	if msg == nil {
		return nil
	}
	return &SendMessageRequest{
		TopicID:       msg.Topic,
		ClientMsgID:   msg.ClientMsgID,
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

func cloneDataMessageWithMetadata(msg *ServerMessage, metadata map[string]interface{}) *ServerMessage {
	if msg == nil || msg.Data == nil {
		return msg
	}
	data := *msg.Data
	data.Metadata = metadata
	return &ServerMessage{
		Ctrl:   msg.Ctrl,
		Data:   &data,
		Pres:   msg.Pres,
		Meta:   msg.Meta,
		Info:   msg.Info,
		Friend: msg.Friend,
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
	if code, text := h.validateTopicReadAccess(client.uid, client.accountType, msg.Topic); code != 0 {
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{
				ID:    msg.ID,
				Topic: msg.Topic,
				Code:  code,
				Text:  text,
			},
		})
		return
	}
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
		if code, text := h.validateTopicReadAccess(uid, client.accountType, msg.Topic); code != 0 {
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{
					ID:    msg.ID,
					Topic: msg.Topic,
					Code:  code,
					Text:  text,
				},
			})
			return
		}
		// Fetch messages after a given seq ID for reconnection
		sinceID := int64(msg.SeqID)
		msgs, err := h.db.GetMessagesSince(msg.Topic, sinceID, 100)
		if err != nil {
			log.Printf("get history error: %v", err)
			return
		}
		// Send each message as a data message
		for _, m := range msgs {
			data := h.historyMessageDataForRecipient(client.uid, m)
			if data == nil {
				continue
			}
			h.SendToClient(client, &ServerMessage{
				Data: data,
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
			if uid2 == selfUID {
				return uid1
			}
			return 0
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
		if client.deviceConnector != nil {
			continue
		}
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

		out := msg
		if msg != nil && msg.Data != nil && senderUID > 0 {
			out = cloneDataMessageWithMetadata(
				msg,
				withCatscoIdentityMetadata(
					msg.Data.Metadata,
					h.buildCatscoIdentityMetadata(senderUID, m.UserID, msg.Data.Topic, int64(msg.Data.SeqID), normalizeContentText(msg.Data.Content)),
				),
			)
		}
		h.SendToUser(m.UserID, out)
	}
}
