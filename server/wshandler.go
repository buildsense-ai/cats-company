// Package server implements the WebSocket hub and client connections for Cats Company.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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

const (
	pageVisibilityVisible         = "visible"
	pageVisibilityHidden          = "hidden"
	maxPushNotificationBodyRunes  = 180
	maxPushNotificationTitleRunes = 80
	// Visibility leases cover a missed heartbeat but expire promptly after a
	// crashed node, so stale pages do not suppress pushes indefinitely.
	pageVisibilityLeaseTTL = 2 * pongWait
)

// Hub maintains the set of active clients and broadcasts messages.
type Hub struct {
	mu                         sync.RWMutex
	clients                    map[int64]map[*Client]struct{}
	clientsByConn              map[string]*Client
	register                   chan *Client
	unregister                 chan *Client
	presence                   chan presenceEvent
	db                         store.Store
	rateLimiter                *RateLimiter
	botStats                   *BotStats
	botConvo                   botConvoTracker
	nodeID                     string
	sharedRuntime              sharedRuntimeState
	bodyLeases                 *botBodyLeaseManager
	userDevices                *userDeviceRegistry
	deviceAudit                *deviceAuditLog
	deviceRevokes              *deviceConnectorRevocationList
	deviceClients              map[int64]map[string]*Client
	deviceRPC                  *deviceRPCRouter
	thinToolRPC                *thinToolRPCRouter
	botRuntimeCredentials      *botRuntimeCredentialSigner
	skillMutationGrants        *skillMutationGrantSigner
	artifactPreviewSessions    *artifactPreviewSessionSigner
	channelOut                 *ChannelOutboundDispatcher
	groupTurns                 *groupAgentTurnTracker
	artifactContextResolver    ArtifactContextResolver
	artifactTaskIntentResolver ArtifactTaskIntentResolver
	artifactRuntimeResolver    ArtifactRuntimeManifestResolver
	artifactContextSnapshots   *artifactContextSnapshotStore
	artifactResultWritebacks   *artifactResultWritebackStore
	artifactTasks              *artifactTaskStore
	push                       *PushNotificationService
	agentPush                  *agentPushTurnCoordinator
	taskGrace                  time.Duration
	// taskReaperInterval is how often the disconnected-task recovery reaper
	// scans durable rows. It complements the per-disconnect time.AfterFunc so
	// a crashed/restarted process or transient DB error cannot permanently
	// skip recovery.
	taskReaperInterval time.Duration
	// botConnectionEpochs is incremented every time a bot registers a new
	// connection. Disconnected-task recovery timers snapshot the current epoch
	// and skip recovery when a newer connection generation appeared, so an old
	// timer never marks work owned by a fresh connection as stale.
	botConnectionEpochs map[int64]uint64
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
	botRuntimeCredential *botRuntimeCredentialClaims
	messagingAttention   messagingClientAttention
	messagingAttentionMu sync.RWMutex
	attentionSyncMu      sync.Mutex
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
	runtimeCredentialSigner, _ := newBotRuntimeCredentialSigner(jwtSecret, time.Now)
	grantSigner, _ := newSkillMutationGrantSigner(jwtSecret, time.Now)
	previewSessionSigner, _ := newArtifactPreviewSessionSigner(jwtSecret)
	hub := &Hub{
		clients:                 make(map[int64]map[*Client]struct{}),
		clientsByConn:           make(map[string]*Client),
		register:                make(chan *Client, 256),
		unregister:              make(chan *Client, 256),
		presence:                make(chan presenceEvent, 256),
		db:                      db,
		rateLimiter:             rl,
		botStats:                NewBotStats(),
		botConvo:                botConvoTracker{counters: make(map[string]*botConvoCount)},
		nodeID:                  nodeID,
		sharedRuntime:           shared,
		bodyLeases:              newBotBodyLeaseManager(defaultBotBodyLeaseTTL).withSharedRuntime(shared, nodeID),
		userDevices:             newUserDeviceRegistry(defaultUserDeviceTTL).withSharedRuntime(shared),
		deviceAudit:             newDeviceAuditLog(),
		deviceRevokes:           newDeviceConnectorRevocationList(),
		deviceClients:           make(map[int64]map[string]*Client),
		deviceRPC:               newDeviceRPCRouter(defaultDeviceRPCTTL).withSharedRuntime(shared),
		thinToolRPC:             newThinToolRPCRouter(defaultThinToolRPCTTL),
		botRuntimeCredentials:   runtimeCredentialSigner,
		skillMutationGrants:     grantSigner,
		artifactPreviewSessions: previewSessionSigner,
		groupTurns:              newGroupAgentTurnTracker(defaultGroupAgentTurnTTL),
		artifactContextSnapshots: newArtifactContextSnapshotStore(
			artifactContextSnapshotTTLDefault,
			artifactContextTombstoneTTLDefault,
			artifactContextSnapshotMaxEntries,
		),
		artifactResultWritebacks: newArtifactResultWritebackStore(
			artifactWritebackTTLDefault,
			artifactResultDeliveryTTLDefault,
			artifactResultStoreMaxEntries,
		),
		artifactTasks: newArtifactTaskStore(
			artifactTaskTTLDefault,
			artifactTaskTombstoneTTLDefault,
			artifactTaskAgentFinishGrace,
			artifactTaskStoreMaxEntries,
		).withRuntimeStore(db),
		agentPush:           newAgentPushTurnCoordinator(),
		taskGrace:           90 * time.Second,
		taskReaperInterval:  30 * time.Second,
		botConnectionEpochs: make(map[int64]uint64),
	}
	if shared != nil {
		shared.registerRuntimeNode(nodeID, hub)
	}
	go hub.runPresence()
	go hub.runDeviceRPCTimeouts()
	// Periodic + startup reaper for disconnected bot task recovery. The
	// disconnect-triggered time.AfterFunc can be lost on process crash/restart
	// or a transient DB error; the reaper re-scans durable rows so a missed
	// timer still converges to stale (review 2026-08-05).
	go hub.runConversationTaskReaper()
	go hub.runArtifactRuntimeRunReaper()
	return hub
}

// SetPushNotificationService enables optional Web Push delivery for users who
// do not currently have a visible messaging page.
func (h *Hub) SetPushNotificationService(service *PushNotificationService) {
	if h != nil {
		h.push = service
	}
}

// NotifyCloudArtifactShared sends the generic owner notification over every
// connected messaging session and queues the same privacy-minimized message
// for offline Web Push subscribers.
func (h *Hub) NotifyCloudArtifactShared(ownerUID int64) {
	if h == nil || ownerUID <= 0 {
		return
	}
	message := &ServerMessage{Notification: &MsgServerNotification{
		Type:    "cloud_artifact_shared",
		Message: "有新文件在云端共享",
	}}
	delivered := false
	if h.sharedRuntime != nil {
		delivered = h.sharedRuntime.broadcastUserMessage(ownerUID, message)
	}
	if !delivered {
		h.SendToUser(ownerUID, message)
	}
	if !h.IsOnline(ownerUID) && h.push != nil && h.push.Enabled() {
		if !h.push.EnqueueToUser(ownerUID, PushNotification{
			Title: "CatsCo",
			Body:  "有新文件在云端共享",
			URL:   "/",
			Tag:   "catsco-cloud-artifact-shared",
		}) {
			log.Printf("cloud artifact: owner push notification was not queued for uid=%d", ownerUID)
		}
	}
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

// BotRuntimeOnline reports cluster-wide runtime presence. BotBodyStatus is
// intentionally scoped to this hub's registered WebSocket clients because it
// exposes connection details; callers that only need a presence signal must
// also account for a valid lease held by another node.
func (h *Hub) BotRuntimeOnline(botUID int64) bool {
	if h == nil || botUID <= 0 {
		return false
	}
	return h.BotBodyStatus(botUID).Active || h.botOnlineElsewhere(botUID)
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

// botOnlineElsewhere reports whether the bot holds an active body lease owned
// by another node. With a shared runtime (Redis / shared memory) the lease is
// cluster-wide, so a bot that reconnected on node B must not be recovered as
// stale by node A. Process-local clients are covered by Hub.IsOnline; a lease
// owned by this node is intentionally ignored so a crash-leaked local lease is
// still reconciled by the database compare-and-set.
func (h *Hub) botOnlineElsewhere(botUID int64) bool {
	if h == nil || h.bodyLeases == nil || botUID <= 0 {
		return false
	}
	lease, ok := h.bodyLeases.status(botUID)
	if !ok || lease.nodeID == "" {
		return false
	}
	return lease.nodeID != h.nodeID
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
			h.cancelThinToolRPCRequestsByRequesterRoute(h.clientRoute(client))
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
				if client.accountType == types.AccountBot {
					h.scheduleDisconnectedBotTaskRecovery(client.uid, time.Now())
				}
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

// BuildOnlineStatusList returns online status for accepted friends, owned bots,
// and Agents shared through a group so every task icon can show availability.
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
	} else {
		for _, bot := range bots {
			addUser(mapID(bot["id"]), true)
		}
	}

	groups, err := db.GetUserGroups(uid)
	if err != nil {
		log.Printf("online status: failed to list group agents for uid=%d: %v", uid, err)
		return onlineList, nil
	}
	for _, group := range groups {
		if group == nil {
			continue
		}
		for _, agentID := range group.AgentIDs {
			addUser(agentID, true)
		}
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

	// A bot registering a new connection starts a new connection generation:
	// recovery timers scheduled for an earlier disconnection must not recover
	// tasks owned by this generation. When a cluster-wide generation store is
	// available the bump must be durable BEFORE this connection is accepted:
	// a locally-only bump is invisible to other nodes (botConnectionEpoch reads
	// the persisted value), so accepting a bot whose generation was not
	// persisted would let an old timer recover the fresh connection's work.
	// Fail closed: reject the connection instead of carrying tasks without a
	// durable generation (review 2026-08-05).
	if client.accountType == types.AccountBot {
		if genStore, ok := h.db.(store.ConversationTaskGenerationStore); ok {
			bumped, err := genStore.BumpBotConnectionGeneration(client.uid)
			if err != nil {
				log.Printf("client connect: bump bot connection generation failed uid=%d, rejecting connection: %v", client.uid, err)
				client.closeSend()
				if client.conn != nil {
					_ = client.conn.Close()
				}
				return false
			}
			h.mu.Lock()
			h.botConnectionEpochs[client.uid] = bumped
			h.mu.Unlock()
		} else {
			// No generation store (single-process deployments): the per-process
			// map is the only fence and there are no other nodes to agree with.
			h.mu.Lock()
			h.botConnectionEpochs[client.uid]++
			h.mu.Unlock()
		}
	}

	firstConn, deviceCount, onlineUsers, replaced := h.addRegisteredClient(client)
	for _, stale := range replaced {
		staleRoute := h.clientRoute(stale)
		h.cancelThinToolRPCRequestsByRequesterRoute(staleRoute)
		h.clearClientRuntimeRoute(stale)
		h.unbindDeviceClient(stale)
		h.cancelThinToolRPCRequestsByTargetRoute(staleRoute)
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
	if err := h.syncClientMessagingAttention(client); err != nil {
		log.Printf("messaging attention: bind uid=%d connection=%s: %v", client.uid, client.connectionID, err)
	}
}

func (h *Hub) clearClientRuntimeRoute(client *Client) {
	if h == nil || client == nil || h.sharedRuntime == nil {
		return
	}
	client.attentionSyncMu.Lock()
	defer client.attentionSyncMu.Unlock()
	route := h.clientRoute(client)
	if err := h.sharedRuntime.clearMessagingClientAttention(client.uid, route); err != nil {
		log.Printf("messaging attention: clear uid=%d connection=%s: %v", client.uid, client.connectionID, err)
	}
	h.sharedRuntime.clearRuntimeRoute(route)
}

func (h *Hub) bindDeviceClient(ownerUID int64, device UserDevice, client *Client) {
	if h == nil || client == nil || ownerUID <= 0 || device.DeviceID == "" {
		return
	}
	h.mu.Lock()

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
	var replacedRoute runtimeRoute
	if existing := ownerDevices[device.DeviceID]; existing != nil && existing != client {
		replacedRoute = h.clientRoute(existing)
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
	h.mu.Unlock()

	if replacedRoute.NodeID != "" && replacedRoute.ConnectionID != "" {
		h.cancelThinToolRPCRequestsByTargetRoute(replacedRoute)
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

func normalizePageVisibility(value string) string {
	if value == pageVisibilityVisible {
		return pageVisibilityVisible
	}
	return pageVisibilityHidden
}

func (h *Hub) setClientPageVisibility(client *Client, visibility string) {
	if h == nil || client == nil {
		return
	}
	client.messagingAttentionMu.Lock()
	client.messagingAttention.Visible = normalizePageVisibility(visibility) == pageVisibilityVisible
	client.messagingAttentionMu.Unlock()
	if err := h.syncClientMessagingAttention(client); err != nil {
		log.Printf("messaging attention: visibility uid=%d connection=%s: %v", client.uid, client.connectionID, err)
	}
}

func (h *Hub) setClientMessagingAttention(client *Client, attention messagingClientAttention) {
	if h == nil || client == nil {
		return
	}
	client.messagingAttentionMu.Lock()
	client.messagingAttention = attention.normalized()
	client.messagingAttentionMu.Unlock()
	if err := h.syncClientMessagingAttention(client); err != nil {
		log.Printf("messaging attention: update uid=%d connection=%s: %v", client.uid, client.connectionID, err)
	}
}

func (h *Hub) clientMessagingAttention(client *Client) messagingClientAttention {
	if client == nil {
		return messagingClientAttention{}
	}
	client.messagingAttentionMu.RLock()
	defer client.messagingAttentionMu.RUnlock()
	return client.messagingAttention
}

func (h *Hub) syncClientMessagingAttention(client *Client) error {
	if h == nil || client == nil || client.deviceConnector != nil || h.sharedRuntime == nil {
		return nil
	}
	client.attentionSyncMu.Lock()
	defer client.attentionSyncMu.Unlock()
	attention := h.clientMessagingAttention(client)
	h.mu.RLock()
	uid := client.uid
	_, connected := h.clients[uid][client]
	h.mu.RUnlock()
	if !connected {
		return nil
	}

	route := h.clientRoute(client)
	now := nowForRoute(h)
	return h.sharedRuntime.setMessagingClientAttention(
		uid,
		route,
		attention,
		now,
		pageVisibilityLeaseTTL,
	)
}

func hasMessagingClientAttention(clients map[*Client]struct{}, subscriptionID, topic string) bool {
	for client := range clients {
		if client == nil || client.deviceConnector != nil {
			continue
		}
		client.messagingAttentionMu.RLock()
		suppresses := client.messagingAttention.suppresses(subscriptionID, topic)
		client.messagingAttentionMu.RUnlock()
		if suppresses {
			return true
		}
	}
	return false
}

func (h *Hub) hasMessagingClientAttention(uid int64, subscriptionID, topic string) bool {
	if h == nil || uid <= 0 {
		return false
	}
	h.mu.RLock()
	localVisible := hasMessagingClientAttention(h.clients[uid], subscriptionID, topic)
	h.mu.RUnlock()
	if localVisible {
		return true
	}
	return h.sharedRuntime != nil && h.sharedRuntime.hasMessagingClientAttention(h.nodeID, uid, subscriptionID, topic, nowForRoute(h))
}

// hasLocalMessagingClientAttentionForRoute confirms the exact connection that
// advertised attention. Redis records only locate a candidate; remote nodes
// must ask this owner before they suppress a Push.
func (h *Hub) hasLocalMessagingClientAttentionForRoute(uid int64, route runtimeRoute, subscriptionID, topic string) bool {
	if h == nil || route.NodeID != h.nodeID || route.ConnectionID == "" || uid <= 0 {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	client := h.clientsByConn[route.ConnectionID]
	if client == nil || client.uid != uid || client.deviceConnector != nil {
		return false
	}
	client.messagingAttentionMu.RLock()
	defer client.messagingAttentionMu.RUnlock()
	suppresses := client.messagingAttention.suppresses(subscriptionID, topic)
	return suppresses
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

// broadcastPresence notifies friends and, for bots, their owner and fellow
// group members of online/offline status.
func (h *Hub) broadcastPresence(uid int64, what string) {
	if h.db == nil {
		return
	}
	friends, err := h.db.GetFriends(uid)
	if err != nil {
		log.Printf("presence: failed to get friends for uid=%d: %v", uid, err)
		friends = nil
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
	if ownerID, err := h.db.GetBotOwner(uid); err == nil {
		if ownerID > 0 {
			recipients[ownerID] = struct{}{}
		}
		groups, groupErr := h.db.GetUserGroups(uid)
		if groupErr != nil {
			log.Printf("presence: failed to get groups for bot uid=%d: %v", uid, groupErr)
		} else {
			for _, group := range groups {
				if group == nil {
					continue
				}
				members, memberErr := h.db.GetGroupMembers(group.ID)
				if memberErr != nil {
					log.Printf("presence: failed to get members for group=%d: %v", group.ID, memberErr)
					continue
				}
				for _, member := range members {
					if member != nil && member.UserID != uid {
						recipients[member.UserID] = struct{}{}
					}
				}
			}
		}
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
	var runtimeCredentialClaims *botRuntimeCredentialClaims
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
		runtimeCredential := extractBotRuntimeCredential(r)
		if runtimeCredential != "" {
			if err != nil || strings.TrimSpace(installationID) == "" {
				http.Error(w, "Bot Runtime credential requires body and installation ids", http.StatusBadRequest)
				return
			}
			if hub == nil || hub.botRuntimeCredentials == nil {
				http.Error(w, "Bot Runtime credential service unavailable", http.StatusServiceUnavailable)
				return
			}
			claims, verifyErr := hub.botRuntimeCredentials.verify(runtimeCredential)
			if verifyErr != nil {
				http.Error(w, "invalid Bot Runtime credential", http.StatusUnauthorized)
				return
			}
			if claims.BotUID != uid || claims.BodyID != bodyID || claims.InstallationID != installationID {
				http.Error(w, "Bot Runtime credential does not match this Runtime", http.StatusForbidden)
				return
			}
			runtimeCredentialClaims = claims
		}
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
		hub:                  hub,
		conn:                 conn,
		uid:                  uid,
		remoteAddr:           requestRemoteAddr(r),
		displayName:          displayName,
		accountType:          acctType,
		bodyID:               bodyID,
		installationID:       installationID,
		connectionID:         connectionID,
		deviceConnector:      connectorClaims,
		botRuntimeCredential: runtimeCredentialClaims,
		send:                 make(chan []byte, 256),
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
	case msg.ThinToolRPC != nil:
		h.handleThinToolRPC(client, msg.ThinToolRPC)
	case msg.ArtifactResult != nil:
		h.handleArtifactResultReceipt(client, msg.ArtifactResult)
	case msg.SkillMutationGrant != nil:
		h.handleSkillMutationGrant(client, msg.SkillMutationGrant)
	}
}

func deviceConnectorMessageAllowed(msg *ClientMessage) bool {
	if msg == nil {
		return false
	}
	if msg.Acc != nil || msg.Login != nil || msg.Sub != nil || msg.Pub != nil || msg.Get != nil || msg.Set != nil || msg.Del != nil || msg.Note != nil || msg.Friend != nil || msg.ArtifactResult != nil || msg.SkillMutationGrant != nil {
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
	if msg.ThinToolRPC != nil {
		actions++
		if strings.ToLower(strings.TrimSpace(msg.ThinToolRPC.Type)) != thinToolRPCTypeResult {
			return false
		}
	}
	return actions == 1
}

// handleHi responds to the handshake message.
func (h *Hub) handleHi(client *Client, displayName string, msg *MsgClientHi) {
	h.setClientMessagingAttention(client, messagingClientAttention{
		SubscriptionID: msg.PushSubscriptionID,
		ActiveTopic:    msg.ActiveTopic,
		Visible:        normalizePageVisibility(msg.Visibility) == pageVisibilityVisible,
		Focused:        msg.Focused,
	})
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
	features := []string{"client_msg_id", "device_rpc", "thin_tool_rpc"}
	params := map[string]interface{}{
		"ver":   "0.1.0",
		"build": "catscompany",
		"uid":   formatUID(client.uid),
		"name":  displayName,
	}
	if client.accountType == types.AccountHuman && client.deviceConnector == nil && h.artifactPreviewSessions != nil {
		if session, err := h.artifactPreviewSessions.issue(client.uid, h.clientRoute(client)); err == nil {
			params["artifact_preview_session"] = session
			features = append(features, "artifact_result_precise_delivery")
		}
	}
	params["features"] = features
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
		if h.isChannelManagedGroup(groupID) && accountType == types.AccountHuman {
			return http.StatusNotFound, "group not found"
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
		if h.isChannelManagedGroup(groupID) && accountType == types.AccountHuman {
			return http.StatusNotFound, "group not found"
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

func (h *Hub) isChannelManagedGroup(groupID int64) bool {
	if h == nil || h.db == nil || groupID <= 0 {
		return true
	}
	groups, ok := h.db.(store.ChannelManagedGroupStore)
	if !ok {
		log.Printf("channel-managed group lookup unavailable for group %d", groupID)
		return true
	}
	managed, err := groups.IsChannelManagedGroup(groupID)
	if err != nil {
		log.Printf("channel-managed group lookup failed for group %d: %v", groupID, err)
		return true
	}
	return managed
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
	candidateMetadata := payload.Metadata
	payload.Metadata, payload.ArtifactContextRef = h.extractArtifactContextDelivery(uid, topic, candidateMetadata)
	_, payload.ArtifactTaskRef, err = h.extractArtifactTaskDelivery(uid, topic, payload.ClientMsgID, candidateMetadata)
	if err != nil {
		code := 400
		if errors.Is(err, errArtifactTaskDeliveryPending) {
			code = 409
		}
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: code, Text: err.Error()},
		})
		return
	}
	if payload.ArtifactTaskRef != nil && (isTransientRuntimePayload(payload) || isTaskStatusPayload(payload)) {
		h.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: 400, Text: "Artifact task references require a persisted visible message"},
		})
		return
	}
	if payload.ArtifactTaskRef != nil && !payload.ArtifactTaskRef.AlreadyDelivered &&
		!h.IsOnline(payload.ArtifactTaskRef.AgentUID) {
		h.artifactTasks.failDelivery(payload.ArtifactTaskRef, "agent_offline", "Artifact task Agent is not connected")
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: 409, Text: "Artifact task Agent is offline"},
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

	if isTaskStatusPayload(payload) {
		h.handleTaskStatusPub(client, msg, topic, payload)
		return
	}

	result, err := saveNormalizedMessage(h.db, topic, uid, msg.ReplyTo, payload)
	if err != nil {
		if payload.ArtifactTaskRef != nil {
			h.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
		}
		log.Printf("save message error: %v", err)
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 500, Text: "save failed"},
		})
		return
	}

	if payload.ArtifactTaskRef != nil {
		if result.Duplicate {
			if !payload.ArtifactTaskRef.AlreadyDelivered {
				h.artifactTasks.failDelivery(payload.ArtifactTaskRef, "turn_delivery_conflict", "Artifact task message identity could not be reconciled")
				h.SendToClient(client, &ServerMessage{
					Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: 409, Text: "Artifact task delivery conflict"},
				})
				return
			}
		} else if !h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, result.ID, client) {
			h.artifactTasks.failDelivery(payload.ArtifactTaskRef, "turn_delivery_failed", "Artifact task turn could not be delivered to the online Agent")
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: 503, Text: "Artifact task turn delivery failed"},
			})
			return
		}
	}

	// Confirm to sender only after an Artifact task turn has reached the Agent queue.
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

	if !result.Duplicate && payload.ArtifactTaskRef == nil {
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
		if h.isChannelManagedGroup(groupID) && client.accountType == types.AccountHuman {
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 404, Text: "group not found"},
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

		if streamType == "stream_cancel" {
			targetBotUID, members, code, text := h.authorizeGroupStreamCancel(groupID, uid, msg.Metadata)
			if code != 0 {
				h.SendToClient(client, &ServerMessage{
					Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: code, Text: text},
				})
				return
			}
			h.SendToClient(client, streamDeltaAck(msg.ID, topic, streamID))
			h.fanoutGroupStreamCancel(uid, topic, streamID, targetBotUID, msg.Metadata, members)
			h.groupTurns.clear(groupID, targetBotUID)
			return
		}

		h.SendToClient(client, streamDeltaAck(msg.ID, topic, streamID))
		if delta != "" {
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
	for key, value := range metadataWithoutArtifactContext(metadata) {
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

	if isTaskStatusPayload(payload) {
		h.handleTaskStatusPub(client, msg, topic, payload)
		return
	}

	result, err := saveNormalizedMessage(h.db, topic, uid, msg.ReplyTo, payload)
	if err != nil {
		if payload.ArtifactTaskRef != nil {
			h.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
		}
		log.Printf("save group message error: %v", err)
		h.SendToClient(client, &ServerMessage{
			Ctrl: &MsgServerCtrl{ID: msg.ID, Code: 500, Text: "save failed"},
		})
		return
	}

	if payload.ArtifactTaskRef != nil {
		if result.Duplicate {
			if !payload.ArtifactTaskRef.AlreadyDelivered {
				h.artifactTasks.failDelivery(payload.ArtifactTaskRef, "turn_delivery_conflict", "Artifact task message identity could not be reconciled")
				h.SendToClient(client, &ServerMessage{
					Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: 409, Text: "Artifact task delivery conflict"},
				})
				return
			}
		} else if !h.fanoutNormalizedMessage(uid, topic, msg.ReplyTo, payload, result.ID, client) {
			h.artifactTasks.failDelivery(payload.ArtifactTaskRef, "turn_delivery_failed", "Artifact task turn could not be delivered to the online Agent")
			h.SendToClient(client, &ServerMessage{
				Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: topic, Code: 503, Text: "Artifact task turn delivery failed"},
			})
			return
		}
	}

	// Confirm to sender only after an Artifact task turn has reached the Agent queue.
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

	if !result.Duplicate && payload.ArtifactTaskRef == nil {
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
		Mentions:      msg.Mentions,
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
		if h.isChannelManagedGroup(groupID) {
			isBot, err := h.db.IsUserBot(m.UserID)
			if err != nil || !isBot {
				continue
			}
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
		Ctrl:                     msg.Ctrl,
		Data:                     &data,
		Pres:                     msg.Pres,
		Meta:                     msg.Meta,
		Info:                     msg.Info,
		Friend:                   msg.Friend,
		suppressPushNotification: msg.suppressPushNotification,
		artifactContextRef:       msg.artifactContextRef,
		artifactTaskRef:          msg.artifactTaskRef,
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
	if client == nil || msg == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(msg.What), "attention") {
		h.setClientMessagingAttention(client, messagingClientAttention{
			SubscriptionID: msg.PushSubscriptionID,
			ActiveTopic:    msg.ActiveTopic,
			Visible:        normalizePageVisibility(msg.Visibility) == pageVisibilityVisible,
			Focused:        msg.Focused,
		})
		return
	}
	if strings.EqualFold(strings.TrimSpace(msg.What), "visibility") {
		h.setClientPageVisibility(client, msg.Visibility)
		return
	}
	uid := client.uid
	if code, _ := h.validateTopicReadAccess(uid, client.accountType, msg.Topic); code != 0 {
		return
	}
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

func shouldNotifyOfflineForMessage(msg *ServerMessage) bool {
	if msg == nil || msg.Data == nil || msg.Data.SeqID <= 0 {
		return false
	}
	if msg.suppressPushNotification {
		return false
	}

	data := msg.Data
	displayType := strings.ToLower(strings.TrimSpace(firstNonEmpty(data.Type, data.MsgType)))
	if !isUserVisibleMessageType(displayType) {
		return false
	}
	return !isInternalAgentWorkingMessage(displayType, data.Content, data.ContentBlocks)
}

func (h *Hub) enqueueOfflineUserPush(uid int64, topic, body string) bool {
	if h == nil || h.push == nil || !h.push.Enabled() || uid <= 0 {
		return false
	}
	user, err := h.db.GetUser(uid)
	if err != nil || user == nil || user.AccountType != types.AccountHuman || user.State != 0 {
		return false
	}
	notification := PushNotification{
		Title: h.pushNotificationTitle(uid, topic),
		Body:  firstNonEmpty(pushNotificationExcerpt(body), "你有一条新消息"),
		URL:   "/",
		Tag:   "catsco-new-message",
	}
	return h.push.EnqueueToUserWhen(uid, notification, func(ctx context.Context) bool {
		return !h.isConversationNotificationsMuted(ctx, uid, topic)
	}, func(subscription *types.PushSubscription) bool {
		return !h.hasMessagingClientAttention(uid, pushSubscriptionID(subscription.Endpoint), topic)
	})
}

func (h *Hub) isConversationNotificationsMuted(ctx context.Context, uid int64, topic string) bool {
	preferences, ok := h.db.(store.ConversationNotificationPreferenceStore)
	if !ok {
		return false
	}
	muted, err := preferences.IsConversationNotificationsMuted(ctx, uid, topic)
	if err != nil {
		log.Printf("push notification: failed to load conversation preference for uid=%d topic=%q: %v", uid, topic, err)
		// Preference storage is an optional filter. If it is unavailable, keep
		// the normal account-level delivery behavior instead of silently
		// dropping every conversation notification for this user.
		return false
	}
	return muted
}

func (h *Hub) pushNotificationTitle(uid int64, topic string) string {
	if h == nil || h.db == nil {
		return "CatsCo"
	}
	if titleDB, ok := h.db.(conversationTitleStore); ok {
		if titles, err := titleDB.GetConversationTitles(uid, []string{topic}); err == nil {
			if title := strings.TrimSpace(titles[topic]); title != "" {
				return truncateUTF8(title, maxPushNotificationTitleRunes)
			}
		}
	}
	if isGroupTopic(topic) {
		group, err := h.db.GetGroup(extractGroupID(topic))
		if err == nil && group != nil {
			if name := strings.TrimSpace(group.Name); name != "" {
				return truncateUTF8(name, maxPushNotificationTitleRunes)
			}
		}
	} else if peerUID := extractPeerUID(topic, uid); peerUID > 0 {
		user, err := h.db.GetUser(peerUID)
		if err == nil && user != nil {
			if name := strings.TrimSpace(firstNonEmpty(user.DisplayName, user.Username)); name != "" {
				return truncateUTF8(name, maxPushNotificationTitleRunes)
			}
		}
	}
	return "CatsCo"
}

func (h *Hub) notifyOfflineUserForMessage(uid, senderUID int64, msg *ServerMessage, senderPublishesTaskStatus bool) {
	topic := ""
	if msg != nil && msg.Data != nil {
		topic = msg.Data.Topic
	}
	body := pushNotificationMessageBody(msg)
	if !senderPublishesTaskStatus {
		h.enqueueOfflineUserPush(uid, topic, body)
		return
	}
	if h == nil || h.agentPush == nil {
		return
	}
	deliver := func(notificationBody string) bool {
		return h.enqueueOfflineUserPush(uid, topic, notificationBody)
	}
	h.agentPush.observeVisibleMessageBody(uid, senderUID, msg, body, deliver)
}

func pushNotificationMessageBody(msg *ServerMessage) string {
	if msg == nil || msg.Data == nil {
		return ""
	}
	var texts []string
	for _, block := range msg.Data.ContentBlocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "text", "assistant_text":
			if text := strings.TrimSpace(firstNonEmpty(block.Text, block.Content)); text != "" {
				texts = append(texts, text)
			}
		}
	}
	if len(texts) > 0 {
		return pushNotificationExcerpt(strings.Join(texts, " "))
	}
	if text := pushNotificationContentText(msg.Data.Content); text != "" && !hasInternalAgentContentBlocks(msg.Data.ContentBlocks) {
		return pushNotificationExcerpt(text)
	}
	displayType := strings.ToLower(strings.TrimSpace(firstNonEmpty(msg.Data.Type, msg.Data.MsgType)))
	for _, block := range msg.Data.ContentBlocks {
		blockType := strings.ToLower(strings.TrimSpace(block.Type))
		if blockType != "" && blockType != "text" && blockType != "assistant_text" && !isInternalAgentContentBlock(blockType) {
			displayType = blockType
			break
		}
	}
	switch displayType {
	case "image":
		return "发来了一张图片"
	case "file":
		return "发来了一个文件"
	case "voice", "audio":
		return "发来了一条语音消息"
	default:
		return ""
	}
}

func hasInternalAgentContentBlocks(blocks []types.ContentBlock) bool {
	for _, block := range blocks {
		if isInternalAgentContentBlock(block.Type) {
			return true
		}
	}
	return false
}

func pushNotificationContentText(content interface{}) string {
	switch value := content.(type) {
	case string:
		return value
	case map[string]interface{}:
		if blockType, ok := value["type"].(string); ok && isInternalAgentContentBlock(blockType) {
			return ""
		}
		for _, key := range []string{"text", "content", "message", "summary", "value", "output", "answer", "result"} {
			if text := pushNotificationContentText(value[key]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	case []interface{}:
		texts := make([]string, 0, len(value))
		for _, item := range value {
			if text := strings.TrimSpace(pushNotificationContentText(item)); text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n")
	case []string:
		return strings.Join(value, "\n")
	case json.RawMessage:
		var decoded interface{}
		if json.Unmarshal(value, &decoded) == nil {
			return pushNotificationContentText(decoded)
		}
	case []byte:
		return pushNotificationContentText(json.RawMessage(value))
	}
	return ""
}

func pushNotificationExcerpt(value string) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(value, "\x00", "")), " ")
	if value == "" {
		return ""
	}
	truncated := truncateUTF8(value, maxPushNotificationBodyRunes)
	if truncated != value {
		return truncateUTF8(value, maxPushNotificationBodyRunes-1) + "…"
	}
	return value
}

// broadcastToGroupWithMentions sends a message to all online members with bot activation filtering.
// Agent-task groups route unmentioned human messages to their current default agent.
// Explicit mentions target other agents, while two-member groups preserve legacy automatic activation.
func (h *Hub) broadcastToGroupWithMentions(groupID int64, msg *ServerMessage, excludeUID int64, mentions []string, senderUID int64, trustedChannelTrigger bool) bool {
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		log.Printf("broadcastToGroupWithMentions: failed to get members for group %d: %v", groupID, err)
		return false
	}
	taskDelivered := false
	shouldNotifyOffline := shouldNotifyOfflineForMessage(msg)

	memberCount := len(members)
	if msg != nil && msg.Data != nil {
		msg.Data.MemberCount = memberCount
	}

	// Convert structured mentions to a set for quick lookup.
	mentionSet := make(map[string]bool)
	for _, m := range mentions {
		mentionSet[m] = true
	}

	channelManaged := h.isChannelManagedGroup(groupID)
	senderIsBot := h.isBotUser(senderUID)
	senderPublishesTaskStatus := h.isTaskStatusPublisher(senderUID)
	mentionAllBots := mentionSet[structuredMentionAllBots] && !senderIsBot
	defaultAgentUID := int64(0)
	if !trustedChannelTrigger && !senderIsBot && memberCount > 2 && len(mentionSet) == 0 {
		group, groupErr := h.db.GetGroup(groupID)
		if groupErr == nil && group != nil && group.Kind == types.GroupKindAgentTask && len(group.AgentIDs) > 0 {
			// The first current task agent is the default. If it leaves, the
			// next current agent takes over; other agents still require @.
			defaultAgentUID = group.AgentIDs[0]
		}
	}
	for _, m := range members {
		if m.UserID == excludeUID {
			continue
		}

		isBot := m.IsBot
		if !isBot {
			if client := h.getClient(m.UserID); client != nil {
				isBot = client.accountType == types.AccountBot
			}
		}
		if channelManaged && !isBot {
			continue
		}

		if isBot {
			userIDStr := formatUID(m.UserID)
			requiresMention := !trustedChannelTrigger && (senderIsBot || memberCount > 2)
			if requiresMention && !mentionAllBots && !mentionSet[userIDStr] && m.UserID != defaultAgentUID {
				continue
			}
			if !senderIsBot && isGroupAgentTurnRequest(msg) {
				h.groupTurns.begin(groupID, m.UserID, senderUID, msg.Data.SeqID)
			}
		}

		out := msg
		if msg != nil && msg.Data != nil && senderUID > 0 {
			metadata := withCatscoIdentityMetadata(
				msg.Data.Metadata,
				h.buildCatscoIdentityMetadata(senderUID, m.UserID, msg.Data.Topic, int64(msg.Data.SeqID), normalizeContentText(msg.Data.Content), catscoIdentityMetadataOptions{SourceMetadata: msg.Data.Metadata}),
			)
			metadata = withXiaobaRuntimeMetadata(metadata, h.buildXiaobaRuntimeMetadata(senderUID, m.UserID, msg.Data.Topic))
			metadata = withArtifactContextDeliveryRef(
				metadataWithoutArtifactContext(metadata),
				h.validatedArtifactContextDeliveryRef(senderUID, msg.Data.Topic, msg.artifactContextRef, m.UserID),
				m.UserID,
			)
			validatedTaskDelivery := h.validatedArtifactTaskDeliveryRef(
				senderUID,
				msg.Data.Topic,
				msg.artifactTaskRef,
				m.UserID,
			)
			metadata = withArtifactTaskDeliveryRef(
				metadata,
				validatedTaskDelivery,
				m.UserID,
			)
			out = cloneDataMessageWithMetadata(
				msg,
				metadata,
			)
			out.artifactTaskRef = validatedTaskDelivery
		}
		if msg != nil && msg.artifactTaskRef != nil && msg.artifactTaskRef.AgentUID == m.UserID {
			// Never deliver a task-shaped message to its target Agent after the
			// reserved delivery stopped validating; that would become an ordinary
			// turn without artifact_task_ref.
			if out == nil || out.artifactTaskRef == nil {
				continue
			}
			if out.artifactTaskRef.AlreadyDelivered {
				taskDelivered = true
			} else if h.sendToUserExceptConfirmed(m.UserID, out, nil) > 0 {
				taskDelivered = h.artifactTasks.confirmDelivery(out.artifactTaskRef)
			}
		} else {
			h.SendToUser(m.UserID, out)
		}
		if !isBot && shouldNotifyOffline {
			h.notifyOfflineUserForMessage(m.UserID, senderUID, out, senderPublishesTaskStatus)
		}
	}
	return taskDelivered
}
