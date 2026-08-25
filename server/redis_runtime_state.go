package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisRuntimeKeyPrefix = "catsco:runtime"
	redisRuntimeNodeTTL          = 30 * time.Second
	redisRuntimeHeartbeatEvery   = 10 * time.Second
	// Runtime writes run on WebSocket paths. Bound them so a Redis outage cannot
	// indefinitely delay a newer attention update or a disconnect.
	redisRuntimeWriteTimeout            = 150 * time.Millisecond
	redisMessagingAttentionProbeTimeout = 150 * time.Millisecond
	redisUserDeviceRetentionTTL         = 24 * time.Hour
)

// RedisRuntimeOptions configures the optional shared runtime state used by
// multi-instance CatsCo deployments.
type RedisRuntimeOptions struct {
	URL       string
	KeyPrefix string
}

// RedisRuntimeState stores runtime-only state in Redis, including visibility
// leases, and uses Redis pub/sub to deliver device RPC messages to the node
// that owns the target connection.
type RedisRuntimeState struct {
	client *redis.Client
	prefix string

	ctx    context.Context
	cancel context.CancelFunc

	mu                    sync.Mutex
	closed                bool
	workers               sync.WaitGroup
	closeOnce             sync.Once
	closeErr              error
	nodes                 map[string]*Hub
	subscribed            map[string]struct{}
	attentionProbeWaiters map[string]chan bool
}

// NewRedisRuntimeState connects to Redis and verifies it is reachable. If this
// returns an error, callers should not silently fall back to process-local state
// in production.
func NewRedisRuntimeState(ctx context.Context, opts RedisRuntimeOptions) (*RedisRuntimeState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	redisURL := strings.TrimSpace(opts.URL)
	if redisURL == "" {
		return nil, fmt.Errorf("redis runtime URL is required")
	}
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis runtime URL: %w", err)
	}
	client := redis.NewClient(redisOpts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis runtime unavailable: %w", err)
	}
	child, cancel := context.WithCancel(context.Background())
	prefix := strings.Trim(strings.TrimSpace(opts.KeyPrefix), ":")
	if prefix == "" {
		prefix = defaultRedisRuntimeKeyPrefix
	}
	return &RedisRuntimeState{
		client:                client,
		prefix:                prefix,
		ctx:                   child,
		cancel:                cancel,
		nodes:                 make(map[string]*Hub),
		subscribed:            make(map[string]struct{}),
		attentionProbeWaiters: make(map[string]chan bool),
	}, nil
}

func (s *RedisRuntimeState) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		s.workers.Wait()
		if s.client != nil {
			s.closeErr = s.client.Close()
		}
	})
	return s.closeErr
}

func (s *RedisRuntimeState) runtimeMode() string {
	return "redis"
}

func (s *RedisRuntimeState) runtimeRouteState() string {
	if s == nil || s.client == nil {
		return "unavailable"
	}
	ctx, cancel := context.WithTimeout(s.ctx, 500*time.Millisecond)
	defer cancel()
	if err := s.client.Ping(ctx).Err(); err != nil {
		return "unavailable"
	}
	return "ready"
}

func (s *RedisRuntimeState) registerRuntimeNode(nodeID string, hub *Hub) {
	if s == nil || s.client == nil || nodeID == "" || hub == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.nodes[nodeID] = hub
	_, alreadySubscribed := s.subscribed[nodeID]
	if !alreadySubscribed {
		s.subscribed[nodeID] = struct{}{}
		s.workers.Add(2)
	}
	s.mu.Unlock()

	_ = s.touchRuntimeNode(nodeID)
	if !alreadySubscribed {
		go func() {
			defer s.workers.Done()
			s.runRuntimeNodeHeartbeat(nodeID)
		}()
		go func() {
			defer s.workers.Done()
			s.runDeviceRPCInbox(nodeID)
		}()
	}
}

func (s *RedisRuntimeState) bindRuntimeRoute(route runtimeRoute, now time.Time) {
	if s == nil || s.client == nil || route.NodeID == "" || route.ConnectionID == "" {
		return
	}
	if !route.validAt(now) {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	_ = s.touchRuntimeNodeWithContext(ctx, route.NodeID)
	payload, err := json.Marshal(redisRouteFromRuntime(route))
	if err != nil {
		return
	}
	ttl := ttlUntil(now, route.ExpiresAt)
	if ttl <= 0 {
		return
	}
	_ = s.client.Set(ctx, s.routeKey(route), payload, ttl).Err()
}

func (s *RedisRuntimeState) clearRuntimeRoute(route runtimeRoute) {
	if s == nil {
		return
	}
	s.clearMatchingRuntimeRoute(s.routeKey(route), route)
}

func (s *RedisRuntimeState) broadcastUserMessage(uid int64, msg *ServerMessage) bool {
	if s == nil || s.client == nil || uid <= 0 || msg == nil {
		return false
	}
	payload, err := json.Marshal(redisUserMessageEnvelope{UID: uid, Msg: msg})
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	subscriberCount, err := s.client.Publish(ctx, s.userMessageChannel(), payload).Result()
	return err == nil && subscriberCount > 0
}

func (s *RedisRuntimeState) clearMatchingRuntimeRoute(key string, route runtimeRoute) {
	if s == nil || s.client == nil || key == "" || route.NodeID == "" || route.ConnectionID == "" {
		return
	}
	// This runs in the Hub's serialized disconnect path. Keep the complete
	// transaction bounded so one unavailable Redis connection cannot delay a
	// later client's local removal and make stale attention suppress a Push.
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	_ = s.client.Watch(ctx, func(tx *redis.Tx) error {
		current, ok := s.getRouteWithClient(ctx, tx, key)
		if !ok || !current.matches(route) {
			return nil
		}
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, key)
			return nil
		})
		return err
	}, key)
}

func (s *RedisRuntimeState) deliverDeviceRPC(route runtimeRoute, msg *MsgDeviceRPC, now time.Time) bool {
	if s == nil || s.client == nil || msg == nil || route.NodeID == "" || route.ConnectionID == "" {
		return false
	}
	if !s.routeConnected(route, now) {
		return false
	}
	if hub := s.localHub(route.NodeID); hub != nil {
		return hub.sendDeviceRPCToLocalRoute(route, msg)
	}
	payload, err := json.Marshal(redisDeviceRPCEnvelope{
		Route: redisRouteFromRuntime(route),
		Msg:   msg,
	})
	if err != nil {
		return false
	}
	return s.client.Publish(s.ctx, s.nodeInboxChannel(route.NodeID), payload).Err() == nil
}

func (s *RedisRuntimeState) deliverThinToolRPC(route runtimeRoute, msg *MsgThinToolRPC, now time.Time) bool {
	if s == nil || s.client == nil || msg == nil || route.NodeID == "" || route.ConnectionID == "" {
		return false
	}
	if !s.routeConnected(route, now) {
		return false
	}
	if hub := s.localHub(route.NodeID); hub != nil {
		return hub.sendThinToolRPCToLocalRoute(route, msg)
	}
	payload, err := json.Marshal(redisThinToolRPCEnvelope{
		Route: redisRouteFromRuntime(route),
		Msg:   msg,
	})
	if err != nil {
		return false
	}
	return s.client.Publish(s.ctx, s.nodeInboxChannel(route.NodeID), payload).Err() == nil
}

func (s *RedisRuntimeState) deliverArtifactResult(route runtimeRoute, msg *MsgArtifactResult, now time.Time) bool {
	if s == nil || s.client == nil || msg == nil || route.NodeID == "" || route.ConnectionID == "" {
		return false
	}
	if !s.routeConnected(route, now) {
		return false
	}
	if hub := s.localHub(route.NodeID); hub != nil {
		return hub.sendArtifactResultToLocalRoute(route, msg)
	}
	payload, err := json.Marshal(redisRuntimeEnvelope{
		Route:          redisRouteFromRuntime(route),
		ArtifactResult: msg,
	})
	if err != nil {
		return false
	}
	return s.client.Publish(s.ctx, s.nodeInboxChannel(route.NodeID), payload).Err() == nil
}

func (s *RedisRuntimeState) deliverArtifactResultReceipt(nodeID string, msg *MsgArtifactResult, sourceRoute runtimeRoute, now time.Time) bool {
	if s == nil || s.client == nil || nodeID == "" || msg == nil || !s.runtimeNodeAlive(nodeID) {
		return false
	}
	if hub := s.localHub(nodeID); hub != nil {
		return hub.acceptArtifactResultReceipt(msg, sourceRoute)
	}
	payload, err := json.Marshal(redisRuntimeEnvelope{
		ArtifactResultReceipt:      msg,
		ArtifactResultReceiptRoute: redisRouteFromRuntime(sourceRoute),
	})
	if err != nil {
		return false
	}
	return s.client.Publish(s.ctx, s.nodeInboxChannel(nodeID), payload).Err() == nil
}

func (s *RedisRuntimeState) routeConnected(route runtimeRoute, now time.Time) bool {
	if s == nil || s.client == nil || !route.validAt(now) {
		return false
	}
	if hub := s.localHub(route.NodeID); hub != nil && hub.getClientByConnectionID(route.ConnectionID) != nil {
		return true
	}
	if !s.runtimeNodeAlive(route.NodeID) {
		return false
	}
	_, ok := s.getRoute(s.routeKey(route))
	return ok
}

func redisMessagingClientAttentionMember(route runtimeRoute, attention messagingClientAttention) string {
	identity := messagingClientAttentionIdentity(route)
	payload, err := json.Marshal(attention.normalized())
	if err != nil {
		return identity
	}
	return identity + "\n" + base64.RawURLEncoding.EncodeToString(payload)
}

func parseRedisMessagingClientAttentionMember(member string) (string, messagingClientAttention) {
	identity, encoded, found := strings.Cut(member, "\n")
	if !found {
		return member, messagingClientAttention{}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return identity, messagingClientAttention{}
	}
	var attention messagingClientAttention
	if err := json.Unmarshal(decoded, &attention); err != nil {
		return identity, messagingClientAttention{}
	}
	return identity, attention.normalized()
}

func (s *RedisRuntimeState) setMessagingClientAttention(uid int64, route runtimeRoute, attention messagingClientAttention, now time.Time, ttl time.Duration) error {
	if s == nil || s.client == nil || uid <= 0 || messagingClientAttentionIdentity(route) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	attention = attention.normalized()
	if !attention.suppressible() || ttl <= 0 {
		return s.applyMessagingClientAttentionClear(ctx, uid, route)
	}
	if !route.validAt(now) {
		return nil
	}

	if err := s.applyMessagingClientAttentionClear(ctx, uid, route); err != nil {
		return err
	}
	member := redisMessagingClientAttentionMember(route, attention)
	expiresAt := now.Add(ttl)
	_, err := s.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZAdd(ctx, s.messagingClientAttentionKey(uid), redis.Z{
			Score:  float64(expiresAt.UnixMilli()),
			Member: member,
		})
		pipe.Expire(ctx, s.messagingClientAttentionKey(uid), ttl)
		return nil
	})
	if err != nil {
		return fmt.Errorf("publish messaging attention: %w", err)
	}
	return nil
}

func (s *RedisRuntimeState) clearMessagingClientAttention(uid int64, route runtimeRoute) error {
	if s == nil || s.client == nil || uid <= 0 || messagingClientAttentionIdentity(route) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	return s.applyMessagingClientAttentionClear(ctx, uid, route)
}

func (s *RedisRuntimeState) applyMessagingClientAttentionClear(ctx context.Context, uid int64, route runtimeRoute) error {
	key := s.messagingClientAttentionKey(uid)
	members, err := s.client.ZRange(ctx, key, 0, -1).Result()
	if err != nil {
		return fmt.Errorf("list messaging attention: %w", err)
	}
	identity := messagingClientAttentionIdentity(route)
	remove := make([]interface{}, 0, 1)
	for _, member := range members {
		memberIdentity, _ := parseRedisMessagingClientAttentionMember(member)
		if memberIdentity == identity {
			remove = append(remove, member)
		}
	}
	if len(remove) > 0 {
		if _, err := s.client.ZRem(ctx, key, remove...).Result(); err != nil {
			return fmt.Errorf("clear messaging attention: %w", err)
		}
	}
	return nil
}

func (s *RedisRuntimeState) hasMessagingClientAttention(requestingNodeID string, uid int64, subscriptionID, topic string, now time.Time) bool {
	if s == nil || s.client == nil || uid <= 0 {
		return false
	}
	// Suppression is an optimization, never a delivery prerequisite. Bound the
	// entire lookup and owner confirmation so a partition or a stuck Redis
	// operation turns into an extra Push instead of delaying one indefinitely.
	ctx, cancel := context.WithTimeout(s.ctx, redisMessagingAttentionProbeTimeout)
	defer cancel()
	key := s.messagingClientAttentionKey(uid)
	pipe := s.client.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%d", now.UnixMilli()))
	members := pipe.ZRange(ctx, key, 0, -1)
	if _, err := pipe.Exec(ctx); err != nil {
		// Suppression requires positive proof that this exact subscription is
		// focused on this exact topic. On a runtime outage, prefer a possible
		// duplicate over silently missing a cross-device notification.
		return false
	}
	for _, member := range members.Val() {
		identity, attention := parseRedisMessagingClientAttentionMember(member)
		route := messagingClientAttentionRoute(identity)
		if attention.suppresses(subscriptionID, topic) &&
			s.confirmMessagingClientAttention(ctx, requestingNodeID, uid, route, subscriptionID, topic) {
			return true
		}
	}
	return false
}

func (s *RedisRuntimeState) acquireBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) (botBodyLeaseResult, error) {
	if s == nil || s.client == nil {
		return botBodyLeaseResult{}, nil
	}
	if botUID <= 0 || bodyID == "" || connectionID == "" || nodeID == "" {
		return botBodyLeaseResult{}, errInvalidBotBodyID
	}
	key := s.botLeaseKey(botUID)
	next := buildSharedBotBodyLease(botUID, bodyID, connectionID, nodeID, now, ttl)
	for i := 0; i < 8; i++ {
		var result botBodyLeaseResult
		var leaseErr error
		err := s.client.Watch(s.ctx, func(tx *redis.Tx) error {
			existing, ok := s.getBotLeaseWithClient(s.ctx, tx, key)
			if ok && isSharedLeaseActive(existing, now) {
				if existing.bodyID != bodyID {
					if isLegacyBotBodyID(existing.bodyID) && !isLegacyBotBodyID(bodyID) {
						result = botBodyLeaseResult{Lease: next, Replaced: true}
					} else {
						result = botBodyLeaseResult{Lease: existing}
						leaseErr = errBotBodyLeaseConflict
						return nil
					}
				} else {
					result = botBodyLeaseResult{Lease: next, Replaced: true}
				}
			} else {
				result = botBodyLeaseResult{Lease: next}
			}
			payload, err := json.Marshal(redisBotBodyLeaseFromRuntime(next))
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(s.ctx, key, payload, ttl)
				return nil
			})
			return err
		}, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return botBodyLeaseResult{}, err
		}
		return result, leaseErr
	}
	return botBodyLeaseResult{}, fmt.Errorf("redis bot lease contention")
}

func (s *RedisRuntimeState) botBodyLeaseConflict(botUID int64, bodyID string, now time.Time) (botBodyLease, bool) {
	if s == nil || botUID <= 0 || bodyID == "" {
		return botBodyLease{}, false
	}
	lease, ok := s.getBotLease(s.botLeaseKey(botUID))
	if !ok || !isSharedLeaseActive(lease, now) || lease.bodyID == bodyID {
		return botBodyLease{}, false
	}
	if isLegacyBotBodyID(lease.bodyID) && !isLegacyBotBodyID(bodyID) {
		return botBodyLease{}, false
	}
	return lease, true
}

func (s *RedisRuntimeState) botBodyLeaseStatus(botUID int64, now time.Time) (botBodyLease, bool) {
	if s == nil || botUID <= 0 {
		return botBodyLease{}, false
	}
	lease, ok := s.getBotLease(s.botLeaseKey(botUID))
	if !ok || !isSharedLeaseActive(lease, now) {
		return botBodyLease{}, false
	}
	return lease, true
}

func (s *RedisRuntimeState) botBodyLeaseIsCurrent(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time) bool {
	if s == nil || botUID <= 0 || bodyID == "" || connectionID == "" || nodeID == "" {
		return false
	}
	lease, ok := s.getBotLease(s.botLeaseKey(botUID))
	return ok &&
		isSharedLeaseActive(lease, now) &&
		lease.bodyID == bodyID &&
		lease.connectionID == connectionID &&
		lease.nodeID == nodeID
}

func (s *RedisRuntimeState) releaseBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string) bool {
	if s == nil || s.client == nil || botUID <= 0 || bodyID == "" || connectionID == "" || nodeID == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	key := s.botLeaseKey(botUID)
	released := false
	_ = s.client.Watch(ctx, func(tx *redis.Tx) error {
		lease, ok := s.getBotLeaseWithClient(ctx, tx, key)
		if !ok || lease.bodyID != bodyID || lease.connectionID != connectionID || lease.nodeID != nodeID {
			return nil
		}
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, key)
			return nil
		})
		if err == nil {
			released = true
		}
		return err
	}, key)
	return released
}

func (s *RedisRuntimeState) renewBotBodyLease(botUID int64, bodyID string, connectionID string, nodeID string, now time.Time, ttl time.Duration) bool {
	if s == nil || botUID <= 0 || bodyID == "" || connectionID == "" || nodeID == "" {
		return false
	}
	key := s.botLeaseKey(botUID)
	renewed := false
	_ = s.client.Watch(s.ctx, func(tx *redis.Tx) error {
		lease, ok := s.getBotLeaseWithClient(s.ctx, tx, key)
		if !ok || lease.bodyID != bodyID || lease.connectionID != connectionID || lease.nodeID != nodeID {
			return nil
		}
		lease.expiresAt = now.Add(ttl)
		payload, err := json.Marshal(redisBotBodyLeaseFromRuntime(lease))
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(s.ctx, key, payload, ttl)
			return nil
		})
		if err == nil {
			renewed = true
		}
		return err
	}, key)
	return renewed
}

func (s *RedisRuntimeState) registerUserDevice(ownerUID int64, req RegisterUserDeviceRequest, now time.Time) (UserDevice, error) {
	if s == nil || ownerUID <= 0 {
		return UserDevice{}, fmt.Errorf("invalid owner")
	}
	deviceID, err := normalizeUserDeviceID(req.DeviceID)
	if err != nil {
		return UserDevice{}, err
	}
	device := UserDevice{
		Kind:           "user_device",
		Source:         "catscompany",
		OwnerUID:       ownerUID,
		OwnerUserID:    formatUID(ownerUID),
		BotUID:         normalizeDeviceBotUID(req.BotUID),
		DeviceID:       deviceID,
		DisplayName:    normalizeDeviceText(req.DisplayName),
		OS:             normalizeDeviceOS(req.OS),
		BodyID:         normalizeDeviceText(req.BodyID),
		InstallationID: normalizeDeviceText(req.InstallationID),
		RuntimeRole:    normalizeDeviceRuntimeRole(req.RuntimeRole),
		Status:         normalizeDeviceStatus(req.Status),
		Capabilities:   normalizeDeviceCapabilities(req.Capabilities),
		ModelStatus:    normalizeDeviceModelStatus(req.ModelStatus, now),
		RegisteredAt:   unixMillis(now),
		LastSeenAt:     unixMillis(now),
	}
	if existing, ok := s.getUserDevice(ownerUID, deviceID); ok && existing.RegisteredAt > 0 {
		device.RegisteredAt = existing.RegisteredAt
	}
	if err := s.saveUserDevice(ownerUID, device); err != nil {
		return UserDevice{}, err
	}
	return device, nil
}

func (s *RedisRuntimeState) unregisterUserDevice(ownerUID int64, deviceID string) {
	if s == nil || s.client == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	_, _ = s.client.Pipelined(s.ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(s.ctx, s.userDeviceKey(ownerUID, normalizedDeviceID))
		pipe.Del(s.ctx, s.userDeviceRouteKey(ownerUID, normalizedDeviceID))
		pipe.SRem(s.ctx, s.userDeviceSetKey(ownerUID), normalizedDeviceID)
		return nil
	})
}

func (s *RedisRuntimeState) listUserDevices(ownerUID int64) []UserDevice {
	if s == nil || ownerUID <= 0 {
		return nil
	}
	ids, err := s.client.SMembers(s.ctx, s.userDeviceSetKey(ownerUID)).Result()
	if err != nil || len(ids) == 0 {
		return nil
	}
	out := make([]UserDevice, 0, len(ids))
	for _, deviceID := range ids {
		device, ok := s.getUserDevice(ownerUID, deviceID)
		if !ok {
			_ = s.client.SRem(s.ctx, s.userDeviceSetKey(ownerUID), deviceID).Err()
			continue
		}
		out = append(out, device)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenAt != out[j].LastSeenAt {
			return out[i].LastSeenAt > out[j].LastSeenAt
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

func (s *RedisRuntimeState) activeUserDevice(ownerUID int64, deviceID string, now time.Time, ttl time.Duration) (UserDevice, bool) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return UserDevice{}, false
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return UserDevice{}, false
	}
	device, ok := s.getUserDevice(ownerUID, normalizedDeviceID)
	if !ok || !isActiveDevice(device, now, ttl) {
		return UserDevice{}, false
	}
	return device, true
}

func (s *RedisRuntimeState) touchUserDevice(ownerUID int64, deviceID string, now time.Time) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return
	}
	normalizedDeviceID, err := normalizeUserDeviceID(deviceID)
	if err != nil {
		return
	}
	device, ok := s.getUserDevice(ownerUID, normalizedDeviceID)
	if !ok {
		return
	}
	device.Status = "online"
	device.LastSeenAt = unixMillis(now)
	_ = s.saveUserDevice(ownerUID, device)
}

func (s *RedisRuntimeState) bindUserDeviceRoute(ownerUID int64, device UserDevice, route runtimeRoute, now time.Time) {
	if s == nil || s.client == nil || ownerUID <= 0 || device.DeviceID == "" || route.NodeID == "" || route.ConnectionID == "" {
		return
	}
	s.bindRuntimeRoute(route, now)
	payload, err := json.Marshal(redisRouteFromRuntime(route))
	if err != nil {
		return
	}
	ttl := ttlUntil(now, route.ExpiresAt)
	if ttl <= 0 {
		return
	}
	_ = s.client.Set(s.ctx, s.userDeviceRouteKey(ownerUID, device.DeviceID), payload, ttl).Err()
}

func (s *RedisRuntimeState) clearUserDeviceRoute(ownerUID int64, deviceID string, route runtimeRoute) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return
	}
	s.clearMatchingRuntimeRoute(s.userDeviceRouteKey(ownerUID, deviceID), route)
}

func (s *RedisRuntimeState) userDeviceRoute(ownerUID int64, deviceID string, now time.Time) (runtimeRoute, bool) {
	if s == nil || ownerUID <= 0 || deviceID == "" {
		return runtimeRoute{}, false
	}
	route, ok := s.getRoute(s.userDeviceRouteKey(ownerUID, deviceID))
	if !ok || !route.validAt(now) {
		return runtimeRoute{}, false
	}
	return route, true
}

func (s *RedisRuntimeState) rememberDeviceGrants(grants []ScopedDeviceGrant, now int64) {
	if s == nil || len(grants) == 0 {
		return
	}
	for _, grant := range grants {
		if grant.GrantID == "" || grant.ExpiresAt <= now {
			continue
		}
		payload, err := json.Marshal(grant)
		if err != nil {
			continue
		}
		ttl := time.Duration(grant.ExpiresAt-now) * time.Millisecond
		_ = s.client.Set(s.ctx, s.deviceGrantKey(grant.GrantID), payload, ttl).Err()
	}
}

func (s *RedisRuntimeState) lookupDeviceGrant(grantID string, now int64) (ScopedDeviceGrant, bool) {
	if s == nil || grantID == "" {
		return ScopedDeviceGrant{}, false
	}
	raw, err := s.client.Get(s.ctx, s.deviceGrantKey(grantID)).Result()
	if err != nil {
		return ScopedDeviceGrant{}, false
	}
	var grant ScopedDeviceGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		return ScopedDeviceGrant{}, false
	}
	if grant.Status != "active" || grant.ExpiresAt <= now {
		return ScopedDeviceGrant{}, false
	}
	return grant, true
}

func (s *RedisRuntimeState) deviceSelectionPreference(actorUID int64, sessionKey string, now int64) (string, bool) {
	if s == nil || actorUID <= 0 || sessionKey == "" {
		return "", false
	}
	raw, err := s.client.Get(s.ctx, s.deviceSelectionPreferenceKey(actorUID, sessionKey)).Result()
	if err != nil {
		return "", false
	}
	var preference deviceSelectionPreference
	if err := json.Unmarshal([]byte(raw), &preference); err != nil {
		return "", false
	}
	if preference.DeviceID == "" || preference.ExpiresAt <= now {
		return "", false
	}
	return preference.DeviceID, true
}

func (s *RedisRuntimeState) rememberDeviceSelection(actorUID int64, sessionKey string, preference deviceSelectionPreference) {
	if s == nil || actorUID <= 0 || sessionKey == "" || preference.DeviceID == "" {
		return
	}
	now := time.Now()
	expiresAt := time.UnixMilli(preference.ExpiresAt)
	ttl := ttlUntil(now, expiresAt)
	if ttl <= 0 {
		return
	}
	payload, err := json.Marshal(preference)
	if err != nil {
		return
	}
	_ = s.client.Set(s.ctx, s.deviceSelectionPreferenceKey(actorUID, sessionKey), payload, ttl).Err()
}

func (s *RedisRuntimeState) addDeviceRPCPending(pending deviceRPCPendingRecord, now time.Time) (bool, string) {
	if s == nil || pending.requestID == "" || pending.expiresAt.IsZero() {
		return false, "invalid"
	}
	key := s.deviceRPCPendingKey(pending.requestID)
	allKey := s.deviceRPCPendingAllKey()
	ownerKey := s.deviceRPCPendingOwnerKey(pending.ownerUID)
	deviceKey := s.deviceRPCPendingDeviceKey(pending.ownerUID, pending.deviceID)
	record := redisDeviceRPCPendingFromRuntime(pending)
	payload, err := json.Marshal(record)
	if err != nil {
		return false, "invalid"
	}
	for i := 0; i < 8; i++ {
		var failure string
		err := s.client.Watch(s.ctx, func(tx *redis.Tx) error {
			if exists, _ := tx.Exists(s.ctx, key).Result(); exists > 0 {
				failure = "duplicate"
				return nil
			}
			active, err := s.listDeviceRPCPendingRecords(tx, allKey, now)
			if err != nil {
				return err
			}
			var agentCount, deviceCount int
			for _, item := range active {
				if item.agentUID == pending.agentUID {
					agentCount++
				}
				if item.ownerUID == pending.ownerUID && item.deviceID == pending.deviceID {
					deviceCount++
				}
			}
			if agentCount >= maxDeviceRPCPendingPerAgent {
				failure = "agent_limit"
				return nil
			}
			if deviceCount >= maxDeviceRPCPendingPerDevice {
				failure = "device_limit"
				return nil
			}
			ttl := ttlUntil(now, pending.expiresAt)
			if ttl <= 0 {
				failure = "invalid"
				return nil
			}
			_, err = tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(s.ctx, key, payload, ttl)
				pipe.SAdd(s.ctx, allKey, pending.requestID)
				pipe.SAdd(s.ctx, ownerKey, pending.requestID)
				pipe.SAdd(s.ctx, deviceKey, pending.requestID)
				pipe.Expire(s.ctx, allKey, defaultDeviceRPCTTL*4)
				pipe.Expire(s.ctx, ownerKey, defaultDeviceRPCTTL*4)
				pipe.Expire(s.ctx, deviceKey, defaultDeviceRPCTTL*4)
				return nil
			})
			return err
		}, key, allKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return false, "unavailable"
		}
		if failure != "" {
			return false, failure
		}
		return true, ""
	}
	return false, "contention"
}

func (s *RedisRuntimeState) getDeviceRPCPending(requestID string, now time.Time) (deviceRPCPendingRecord, bool) {
	if s == nil || requestID == "" {
		return deviceRPCPendingRecord{}, false
	}
	record, ok := s.getDeviceRPCPendingRecord(requestID)
	if !ok || !now.Before(record.expiresAt) {
		return deviceRPCPendingRecord{}, false
	}
	return record, true
}

func (s *RedisRuntimeState) finishDeviceRPCPending(requestID string) {
	if s == nil || requestID == "" {
		return
	}
	record, ok := s.getDeviceRPCPendingRecord(requestID)
	if !ok {
		_ = s.client.Del(s.ctx, s.deviceRPCPendingKey(requestID)).Err()
		_ = s.client.SRem(s.ctx, s.deviceRPCPendingAllKey(), requestID).Err()
		return
	}
	s.removeDeviceRPCPending(record)
}

func (s *RedisRuntimeState) listDeviceRPCPendingByOwner(ownerUID int64) []deviceRPCPendingRecord {
	if s == nil || ownerUID <= 0 {
		return nil
	}
	ids, err := s.client.SMembers(s.ctx, s.deviceRPCPendingOwnerKey(ownerUID)).Result()
	if err != nil || len(ids) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]deviceRPCPendingRecord, 0, len(ids))
	for _, requestID := range ids {
		record, ok := s.getDeviceRPCPendingRecord(requestID)
		if !ok || !now.Before(record.expiresAt) {
			if ok {
				s.removeDeviceRPCPending(record)
			} else {
				_ = s.client.SRem(s.ctx, s.deviceRPCPendingOwnerKey(ownerUID), requestID).Err()
			}
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].createdAt.Before(out[j].createdAt)
	})
	return out
}

func (s *RedisRuntimeState) expireDeviceRPCPending(now time.Time) []deviceRPCPendingRecord {
	if s == nil {
		return nil
	}
	ids, err := s.client.SMembers(s.ctx, s.deviceRPCPendingAllKey()).Result()
	if err != nil || len(ids) == 0 {
		return nil
	}
	var expired []deviceRPCPendingRecord
	for _, requestID := range ids {
		record, ok := s.claimExpiredDeviceRPCPending(requestID, now)
		if ok {
			expired = append(expired, record)
		}
	}
	return expired
}

func (s *RedisRuntimeState) saveDeviceConnectorPairing(pairing deviceConnectorPairing, ttl time.Duration) error {
	if s == nil || s.client == nil || pairing.PairingID == "" || pairing.PairingCode == "" {
		return fmt.Errorf("invalid pairing")
	}
	if ttl <= 0 {
		ttl = ttlUntil(time.Now(), pairing.ExpiresAt)
	}
	if ttl <= 0 {
		return fmt.Errorf("invalid pairing ttl")
	}
	payload, err := json.Marshal(pairing)
	if err != nil {
		return err
	}
	_, err = s.client.Pipelined(s.ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(s.ctx, s.deviceConnectorPairingIDKey(pairing.PairingID), payload, ttl)
		pipe.Set(s.ctx, s.deviceConnectorPairingCodeKey(pairing.PairingCode), pairing.PairingID, ttl)
		return nil
	})
	return err
}

func (s *RedisRuntimeState) getDeviceConnectorPairing(pairingID string, now time.Time) (deviceConnectorPairing, bool) {
	if s == nil || strings.TrimSpace(pairingID) == "" {
		return deviceConnectorPairing{}, false
	}
	raw, err := s.client.Get(s.ctx, s.deviceConnectorPairingIDKey(strings.TrimSpace(pairingID))).Result()
	if err != nil {
		return deviceConnectorPairing{}, false
	}
	var pairing deviceConnectorPairing
	if err := json.Unmarshal([]byte(raw), &pairing); err != nil {
		return deviceConnectorPairing{}, false
	}
	if !now.Before(pairing.ExpiresAt) {
		return deviceConnectorPairing{}, false
	}
	return pairing, true
}

func (s *RedisRuntimeState) consumeDeviceConnectorPairing(code string, now time.Time) (deviceConnectorPairing, bool) {
	if s == nil || strings.TrimSpace(code) == "" {
		return deviceConnectorPairing{}, false
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	codeKey := s.deviceConnectorPairingCodeKey(normalized)
	var out deviceConnectorPairing
	consumed := false
	_ = s.client.Watch(s.ctx, func(tx *redis.Tx) error {
		pairingID, err := tx.Get(s.ctx, codeKey).Result()
		if err != nil || pairingID == "" {
			return nil
		}
		idKey := s.deviceConnectorPairingIDKey(pairingID)
		raw, err := tx.Get(s.ctx, idKey).Result()
		if err != nil {
			return nil
		}
		var pairing deviceConnectorPairing
		if err := json.Unmarshal([]byte(raw), &pairing); err != nil {
			return nil
		}
		if !now.Before(pairing.ExpiresAt) || !pairing.ConsumedAt.IsZero() {
			return nil
		}
		pairing.ConsumedAt = now
		payload, err := json.Marshal(pairing)
		if err != nil {
			return err
		}
		ttl := ttlUntil(now, pairing.ExpiresAt)
		if ttl <= 0 {
			return nil
		}
		_, err = tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(s.ctx, idKey, payload, ttl)
			pipe.Del(s.ctx, codeKey)
			return nil
		})
		if err == nil {
			out = pairing
			consumed = true
		}
		return err
	}, codeKey)
	return out, consumed
}

func (s *RedisRuntimeState) appendDeviceAudit(ownerUID int64, event DeviceAuditEvent) {
	if s == nil || s.client == nil || ownerUID <= 0 {
		return
	}
	event.OwnerUserID = formatUID(ownerUID)
	if event.CreatedAt == 0 {
		event.CreatedAt = unixMillis(time.Now())
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	key := s.deviceAuditKey(ownerUID)
	_, _ = s.client.Pipelined(s.ctx, func(pipe redis.Pipeliner) error {
		pipe.LPush(s.ctx, key, payload)
		pipe.LTrim(s.ctx, key, 0, maxDeviceAuditEvents-1)
		pipe.Expire(s.ctx, key, 30*24*time.Hour)
		return nil
	})
}

func (s *RedisRuntimeState) listDeviceAudit(ownerUID int64, limit int) []DeviceAuditEvent {
	if s == nil || s.client == nil || ownerUID <= 0 {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	raw, err := s.client.LRange(s.ctx, s.deviceAuditKey(ownerUID), 0, int64(limit-1)).Result()
	if err != nil || len(raw) == 0 {
		return nil
	}
	out := make([]DeviceAuditEvent, 0, len(raw))
	for _, item := range raw {
		var event DeviceAuditEvent
		if err := json.Unmarshal([]byte(item), &event); err == nil {
			out = append(out, event)
		}
	}
	return out
}

func (s *RedisRuntimeState) revokeDeviceConnectorDevice(ownerUID int64, deviceID string, revokedAt time.Time) {
	if s == nil || s.client == nil || ownerUID <= 0 || strings.TrimSpace(deviceID) == "" {
		return
	}
	_ = s.client.Set(s.ctx, s.deviceConnectorRevokedDeviceKey(ownerUID, deviceID), unixMillis(revokedAt), deviceConnectorTokenTTL).Err()
}

func (s *RedisRuntimeState) revokeDeviceConnectorToken(tokenID string, expiresAt time.Time) {
	if s == nil || s.client == nil || strings.TrimSpace(tokenID) == "" {
		return
	}
	ttl := ttlUntil(time.Now(), expiresAt)
	if ttl <= 0 {
		ttl = deviceConnectorTokenTTL
	}
	_ = s.client.Set(s.ctx, s.deviceConnectorRevokedTokenKey(tokenID), "1", ttl).Err()
}

func (s *RedisRuntimeState) isDeviceConnectorRevoked(claims *DeviceConnectorClaims, now time.Time) bool {
	if s == nil || s.client == nil || claims == nil {
		return false
	}
	if exists, err := s.client.Exists(s.ctx, s.deviceConnectorRevokedTokenKey(claims.ID)).Result(); err == nil && exists > 0 {
		return true
	}
	raw, err := s.client.Get(s.ctx, s.deviceConnectorRevokedDeviceKey(claims.UID, claims.DeviceID)).Result()
	if err != nil || raw == "" {
		return false
	}
	revokedAt := time.UnixMilli(parseInt64(raw))
	if claims.IssuedAt == nil {
		return true
	}
	return !claims.IssuedAt.Time.After(revokedAt)
}

func (s *RedisRuntimeState) localHub(nodeID string) *Hub {
	if s == nil || nodeID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodes[nodeID]
}

// confirmMessagingClientAttention treats Redis as a locator only. A stale
// member can never suppress delivery by itself: its owning runtime node must
// confirm the live WebSocket connection and its current local attention.
func (s *RedisRuntimeState) confirmMessagingClientAttention(ctx context.Context, requestingNodeID string, uid int64, route runtimeRoute, subscriptionID, topic string) bool {
	if s == nil || s.client == nil || !route.validAt(time.Now()) || uid <= 0 || requestingNodeID == "" {
		return false
	}
	if hub := s.localHub(route.NodeID); hub != nil {
		return hub.hasLocalMessagingClientAttentionForRoute(uid, route, subscriptionID, topic)
	}
	probeID, err := randomHex(16)
	if err != nil || probeID == "" {
		return false
	}
	result := make(chan bool, 1)
	s.mu.Lock()
	s.attentionProbeWaiters[probeID] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.attentionProbeWaiters, probeID)
		s.mu.Unlock()
	}()

	payload, err := json.Marshal(redisRuntimeEnvelope{
		MessagingAttentionProbe: &redisMessagingAttentionProbe{
			ID:             probeID,
			ReplyNodeID:    requestingNodeID,
			UID:            uid,
			Route:          redisRouteFromRuntime(route),
			SubscriptionID: subscriptionID,
			Topic:          topic,
		},
	})
	receivers, err := s.client.Publish(ctx, s.nodeInboxChannel(route.NodeID), payload).Result()
	if err != nil || receivers == 0 {
		return false
	}

	select {
	case active := <-result:
		return active
	case <-ctx.Done():
		return false
	}
}

func (s *RedisRuntimeState) handleMessagingAttentionProbe(nodeID string, probe *redisMessagingAttentionProbe) {
	if s == nil || s.client == nil || probe == nil || probe.ID == "" || probe.ReplyNodeID == "" || probe.Route.NodeID != nodeID {
		return
	}
	active := false
	if hub := s.localHub(nodeID); hub != nil {
		active = hub.hasLocalMessagingClientAttentionForRoute(
			probe.UID,
			probe.Route.toRuntimeRoute(),
			probe.SubscriptionID,
			probe.Topic,
		)
	}
	payload, err := json.Marshal(redisRuntimeEnvelope{
		MessagingAttentionProbeReply: &redisMessagingAttentionProbeReply{
			ID:     probe.ID,
			Active: active,
		},
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisMessagingAttentionProbeTimeout)
	defer cancel()
	_ = s.client.Publish(ctx, s.nodeInboxChannel(probe.ReplyNodeID), payload).Err()
}

func (s *RedisRuntimeState) resolveMessagingAttentionProbe(reply *redisMessagingAttentionProbeReply) {
	if s == nil || reply == nil || reply.ID == "" {
		return
	}
	s.mu.Lock()
	result := s.attentionProbeWaiters[reply.ID]
	delete(s.attentionProbeWaiters, reply.ID)
	s.mu.Unlock()
	if result == nil {
		return
	}
	select {
	case result <- reply.Active:
	default:
	}
}

func (s *RedisRuntimeState) runRuntimeNodeHeartbeat(nodeID string) {
	ticker := time.NewTicker(redisRuntimeHeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			_ = s.touchRuntimeNode(nodeID)
		}
	}
}

func (s *RedisRuntimeState) runDeviceRPCInbox(nodeID string) {
	pubsub := s.client.Subscribe(s.ctx, s.nodeInboxChannel(nodeID), s.userMessageChannel())
	defer pubsub.Close()
	ch := pubsub.Channel()
	for {
		select {
		case <-s.ctx.Done():
			return
		case item, ok := <-ch:
			if !ok {
				return
			}
			var envelope redisRuntimeEnvelope
			if item.Channel == s.userMessageChannel() {
				var userMessage redisUserMessageEnvelope
				if err := json.Unmarshal([]byte(item.Payload), &userMessage); err == nil && userMessage.UID > 0 && userMessage.Msg != nil {
					if hub := s.localHub(nodeID); hub != nil {
						hub.SendToUser(userMessage.UID, userMessage.Msg)
					}
				}
				continue
			}
			if err := json.Unmarshal([]byte(item.Payload), &envelope); err != nil {
				continue
			}
			if envelope.MessagingAttentionProbeReply != nil {
				s.resolveMessagingAttentionProbe(envelope.MessagingAttentionProbeReply)
				continue
			}
			if envelope.MessagingAttentionProbe != nil {
				s.handleMessagingAttentionProbe(nodeID, envelope.MessagingAttentionProbe)
				continue
			}
			if envelope.ArtifactResultReceipt != nil {
				if hub := s.localHub(nodeID); hub != nil {
					hub.acceptArtifactResultReceipt(
						envelope.ArtifactResultReceipt,
						envelope.ArtifactResultReceiptRoute.toRuntimeRoute(),
					)
				}
				continue
			}
			route := envelope.Route.toRuntimeRoute()
			if route.NodeID != nodeID || !route.validAt(time.Now()) {
				continue
			}
			if hub := s.localHub(nodeID); hub != nil {
				if envelope.DeviceRPC != nil || envelope.Msg != nil {
					hub.sendDeviceRPCToLocalRoute(route, firstDeviceRPCMessage(envelope.DeviceRPC, envelope.Msg))
				} else if envelope.ThinToolRPC != nil {
					hub.sendThinToolRPCToLocalRoute(route, envelope.ThinToolRPC)
				} else if envelope.ArtifactResult != nil {
					hub.sendArtifactResultToLocalRoute(route, envelope.ArtifactResult)
				}
			}
		}
	}
}

func (s *RedisRuntimeState) touchRuntimeNode(nodeID string) error {
	if s == nil || s.client == nil || nodeID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	return s.touchRuntimeNodeWithContext(ctx, nodeID)
}

func (s *RedisRuntimeState) touchRuntimeNodeWithContext(ctx context.Context, nodeID string) error {
	if s == nil || s.client == nil || nodeID == "" {
		return nil
	}
	return s.client.Set(ctx, s.nodeKey(nodeID), "1", redisRuntimeNodeTTL).Err()
}

func (s *RedisRuntimeState) runtimeNodeAlive(nodeID string) bool {
	if s == nil || s.client == nil || nodeID == "" {
		return false
	}
	exists, err := s.client.Exists(s.ctx, s.nodeKey(nodeID)).Result()
	return err == nil && exists > 0
}

func (s *RedisRuntimeState) saveUserDevice(ownerUID int64, device UserDevice) error {
	payload, err := json.Marshal(device)
	if err != nil {
		return err
	}
	_, err = s.client.Pipelined(s.ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(s.ctx, s.userDeviceKey(ownerUID, device.DeviceID), payload, redisUserDeviceRetentionTTL)
		pipe.SAdd(s.ctx, s.userDeviceSetKey(ownerUID), device.DeviceID)
		pipe.Expire(s.ctx, s.userDeviceSetKey(ownerUID), redisUserDeviceRetentionTTL)
		return nil
	})
	return err
}

func (s *RedisRuntimeState) getUserDevice(ownerUID int64, deviceID string) (UserDevice, bool) {
	raw, err := s.client.Get(s.ctx, s.userDeviceKey(ownerUID, deviceID)).Result()
	if err != nil {
		return UserDevice{}, false
	}
	var device UserDevice
	if err := json.Unmarshal([]byte(raw), &device); err != nil {
		return UserDevice{}, false
	}
	device.OwnerUID = ownerUID
	device.OwnerUserID = formatUID(ownerUID)
	return device, true
}

func (s *RedisRuntimeState) getRoute(key string) (runtimeRoute, bool) {
	return s.getRouteWithClient(s.ctx, s.client, key)
}

type redisGetter interface {
	Get(ctx context.Context, key string) *redis.StringCmd
}

func (s *RedisRuntimeState) getRouteWithClient(ctx context.Context, client redisGetter, key string) (runtimeRoute, bool) {
	raw, err := client.Get(ctx, key).Result()
	if err != nil {
		return runtimeRoute{}, false
	}
	var route redisRuntimeRoute
	if err := json.Unmarshal([]byte(raw), &route); err != nil {
		return runtimeRoute{}, false
	}
	return route.toRuntimeRoute(), true
}

func (s *RedisRuntimeState) getBotLease(key string) (botBodyLease, bool) {
	return s.getBotLeaseWithClient(s.ctx, s.client, key)
}

func (s *RedisRuntimeState) getBotLeaseWithClient(ctx context.Context, client redisGetter, key string) (botBodyLease, bool) {
	raw, err := client.Get(ctx, key).Result()
	if err != nil {
		return botBodyLease{}, false
	}
	var lease redisBotBodyLease
	if err := json.Unmarshal([]byte(raw), &lease); err != nil {
		return botBodyLease{}, false
	}
	return lease.toRuntimeLease(), true
}

func (s *RedisRuntimeState) listDeviceRPCPendingRecords(client redisGetter, setKey string, now time.Time) ([]deviceRPCPendingRecord, error) {
	ids, err := s.client.SMembers(s.ctx, setKey).Result()
	if err != nil {
		return nil, err
	}
	out := make([]deviceRPCPendingRecord, 0, len(ids))
	for _, requestID := range ids {
		record, ok := s.getDeviceRPCPendingRecordWithClient(client, requestID)
		if !ok {
			continue
		}
		if !now.Before(record.expiresAt) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *RedisRuntimeState) getDeviceRPCPendingRecord(requestID string) (deviceRPCPendingRecord, bool) {
	return s.getDeviceRPCPendingRecordWithClient(s.client, requestID)
}

func (s *RedisRuntimeState) getDeviceRPCPendingRecordWithClient(client redisGetter, requestID string) (deviceRPCPendingRecord, bool) {
	raw, err := client.Get(s.ctx, s.deviceRPCPendingKey(requestID)).Result()
	if err != nil {
		return deviceRPCPendingRecord{}, false
	}
	var record redisDeviceRPCPending
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return deviceRPCPendingRecord{}, false
	}
	return record.toRuntimeRecord(), true
}

func (s *RedisRuntimeState) claimExpiredDeviceRPCPending(requestID string, now time.Time) (deviceRPCPendingRecord, bool) {
	key := s.deviceRPCPendingKey(requestID)
	var out deviceRPCPendingRecord
	claimed := false
	_ = s.client.Watch(s.ctx, func(tx *redis.Tx) error {
		record, ok := s.getDeviceRPCPendingRecordWithClient(tx, requestID)
		if !ok {
			_ = s.client.SRem(s.ctx, s.deviceRPCPendingAllKey(), requestID).Err()
			return nil
		}
		if now.Before(record.expiresAt) {
			return nil
		}
		_, err := tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(s.ctx, key)
			pipe.SRem(s.ctx, s.deviceRPCPendingAllKey(), requestID)
			pipe.SRem(s.ctx, s.deviceRPCPendingOwnerKey(record.ownerUID), requestID)
			pipe.SRem(s.ctx, s.deviceRPCPendingDeviceKey(record.ownerUID, record.deviceID), requestID)
			return nil
		})
		if err == nil {
			out = record
			claimed = true
		}
		return err
	}, key)
	return out, claimed
}

func (s *RedisRuntimeState) removeDeviceRPCPending(record deviceRPCPendingRecord) {
	_, _ = s.client.Pipelined(s.ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(s.ctx, s.deviceRPCPendingKey(record.requestID))
		pipe.SRem(s.ctx, s.deviceRPCPendingAllKey(), record.requestID)
		pipe.SRem(s.ctx, s.deviceRPCPendingOwnerKey(record.ownerUID), record.requestID)
		pipe.SRem(s.ctx, s.deviceRPCPendingDeviceKey(record.ownerUID, record.deviceID), record.requestID)
		return nil
	})
}

func (s *RedisRuntimeState) key(parts ...string) string {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, s.prefix)
	for _, part := range parts {
		part = strings.Trim(part, ":")
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ":")
}

func (s *RedisRuntimeState) nodeKey(nodeID string) string {
	return s.key("node", keyPart(nodeID))
}

func (s *RedisRuntimeState) nodeInboxChannel(nodeID string) string {
	return s.key("inbox", keyPart(nodeID))
}

func (s *RedisRuntimeState) userMessageChannel() string {
	return s.key("user_message")
}

func (s *RedisRuntimeState) routeKey(route runtimeRoute) string {
	return s.key("route", keyPart(route.NodeID), keyPart(route.ConnectionID))
}

func (s *RedisRuntimeState) messagingClientAttentionKey(uid int64) string {
	// Do not reuse visible_messaging: binaries before this change use ZCard on
	// that key and would treat any v2 member as broad user visibility. A
	// dedicated key makes mixed-version deployments fail open instead.
	return s.key("messaging_attention", fmt.Sprintf("%d", uid))
}

func (s *RedisRuntimeState) botLeaseKey(botUID int64) string {
	return s.key("bot_lease", fmt.Sprintf("%d", botUID))
}

func (s *RedisRuntimeState) userDeviceSetKey(ownerUID int64) string {
	return s.key("user_devices", fmt.Sprintf("%d", ownerUID))
}

func (s *RedisRuntimeState) userDeviceKey(ownerUID int64, deviceID string) string {
	return s.key("user_device", fmt.Sprintf("%d", ownerUID), keyPart(deviceID))
}

func (s *RedisRuntimeState) userDeviceRouteKey(ownerUID int64, deviceID string) string {
	return s.key("user_device_route", fmt.Sprintf("%d", ownerUID), keyPart(deviceID))
}

func (s *RedisRuntimeState) deviceGrantKey(grantID string) string {
	return s.key("device_grant", keyPart(grantID))
}

func (s *RedisRuntimeState) deviceSelectionPreferenceKey(actorUID int64, sessionKey string) string {
	return s.key("device_preference", fmt.Sprintf("%d", actorUID), keyPart(sessionKey))
}

func (s *RedisRuntimeState) deviceRPCPendingKey(requestID string) string {
	return s.key("device_rpc", keyPart(requestID))
}

func (s *RedisRuntimeState) deviceRPCPendingAllKey() string {
	return s.key("device_rpc_pending")
}

func (s *RedisRuntimeState) deviceRPCPendingOwnerKey(ownerUID int64) string {
	return s.key("device_rpc_owner", fmt.Sprintf("%d", ownerUID))
}

func (s *RedisRuntimeState) deviceRPCPendingDeviceKey(ownerUID int64, deviceID string) string {
	return s.key("device_rpc_device", fmt.Sprintf("%d", ownerUID), keyPart(deviceID))
}

func (s *RedisRuntimeState) deviceConnectorPairingIDKey(pairingID string) string {
	return s.key("device_connector_pairing", keyPart(pairingID))
}

func (s *RedisRuntimeState) deviceConnectorPairingCodeKey(code string) string {
	return s.key("device_connector_pairing_code", keyPart(strings.ToUpper(strings.TrimSpace(code))))
}

func (s *RedisRuntimeState) deviceAuditKey(ownerUID int64) string {
	return s.key("device_audit", fmt.Sprintf("%d", ownerUID))
}

func (s *RedisRuntimeState) deviceConnectorRevokedDeviceKey(ownerUID int64, deviceID string) string {
	return s.key("device_connector_revoked_device", fmt.Sprintf("%d", ownerUID), keyPart(deviceID))
}

func (s *RedisRuntimeState) deviceConnectorRevokedTokenKey(tokenID string) string {
	return s.key("device_connector_revoked_token", keyPart(tokenID))
}

func keyPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func ttlUntil(now time.Time, expiresAt time.Time) time.Duration {
	if expiresAt.IsZero() {
		return 0
	}
	ttl := expiresAt.Sub(now)
	if ttl < time.Millisecond {
		return 0
	}
	return ttl
}

type redisRuntimeRoute struct {
	NodeID       string `json:"node_id"`
	ConnectionID string `json:"connection_id"`
	ExpiresAt    int64  `json:"expires_at"`
}

func redisRouteFromRuntime(route runtimeRoute) redisRuntimeRoute {
	return redisRuntimeRoute{
		NodeID:       route.NodeID,
		ConnectionID: route.ConnectionID,
		ExpiresAt:    unixMillis(route.ExpiresAt),
	}
}

func (r redisRuntimeRoute) toRuntimeRoute() runtimeRoute {
	return runtimeRoute{
		NodeID:       r.NodeID,
		ConnectionID: r.ConnectionID,
		ExpiresAt:    time.UnixMilli(r.ExpiresAt),
	}
}

type redisBotBodyLease struct {
	BotUID       int64  `json:"bot_uid"`
	BodyID       string `json:"body_id"`
	ConnectionID string `json:"connection_id"`
	NodeID       string `json:"node_id"`
	AcquiredAt   int64  `json:"acquired_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

func redisBotBodyLeaseFromRuntime(lease botBodyLease) redisBotBodyLease {
	return redisBotBodyLease{
		BotUID:       lease.botUID,
		BodyID:       lease.bodyID,
		ConnectionID: lease.connectionID,
		NodeID:       lease.nodeID,
		AcquiredAt:   unixMillis(lease.acquiredAt),
		ExpiresAt:    unixMillis(lease.expiresAt),
	}
}

func (l redisBotBodyLease) toRuntimeLease() botBodyLease {
	return botBodyLease{
		botUID:       l.BotUID,
		bodyID:       l.BodyID,
		connectionID: l.ConnectionID,
		nodeID:       l.NodeID,
		acquiredAt:   time.UnixMilli(l.AcquiredAt),
		expiresAt:    time.UnixMilli(l.ExpiresAt),
	}
}

type redisDeviceRPCEnvelope struct {
	Route     redisRuntimeRoute `json:"route"`
	Msg       *MsgDeviceRPC     `json:"msg"`
	DeviceRPC *MsgDeviceRPC     `json:"device_rpc,omitempty"`
}

type redisThinToolRPCEnvelope struct {
	Route redisRuntimeRoute `json:"route"`
	Msg   *MsgThinToolRPC   `json:"thin_tool_rpc"`
}

type redisUserMessageEnvelope struct {
	UID int64          `json:"uid"`
	Msg *ServerMessage `json:"msg"`
}

type redisMessagingAttentionProbe struct {
	ID             string            `json:"id"`
	ReplyNodeID    string            `json:"reply_node_id"`
	UID            int64             `json:"uid"`
	Route          redisRuntimeRoute `json:"route"`
	SubscriptionID string            `json:"subscription_id"`
	Topic          string            `json:"topic"`
}

type redisMessagingAttentionProbeReply struct {
	ID     string `json:"id"`
	Active bool   `json:"active"`
}

type redisRuntimeEnvelope struct {
	Route                        redisRuntimeRoute                  `json:"route"`
	Msg                          *MsgDeviceRPC                      `json:"msg,omitempty"`
	DeviceRPC                    *MsgDeviceRPC                      `json:"device_rpc,omitempty"`
	ThinToolRPC                  *MsgThinToolRPC                    `json:"thin_tool_rpc,omitempty"`
	ArtifactResult               *MsgArtifactResult                 `json:"artifact_result,omitempty"`
	MessagingAttentionProbe      *redisMessagingAttentionProbe      `json:"messaging_attention_probe,omitempty"`
	MessagingAttentionProbeReply *redisMessagingAttentionProbeReply `json:"messaging_attention_probe_reply,omitempty"`
	ArtifactResultReceipt        *MsgArtifactResult                 `json:"artifact_result_receipt,omitempty"`
	ArtifactResultReceiptRoute   redisRuntimeRoute                  `json:"artifact_result_receipt_route,omitempty"`
}

func firstDeviceRPCMessage(values ...*MsgDeviceRPC) *MsgDeviceRPC {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

type redisDeviceRPCPending struct {
	RequestID       string       `json:"request_id"`
	RequesterRoute  runtimeRoute `json:"requester_route"`
	TargetRoute     runtimeRoute `json:"target_route"`
	AgentUID        int64        `json:"agent_uid"`
	AgentID         string       `json:"agent_id"`
	AgentBodyID     string       `json:"agent_body_id"`
	ActorUID        int64        `json:"actor_uid"`
	ActorUserID     string       `json:"actor_user_id"`
	OwnerUID        int64        `json:"owner_uid"`
	OwnerUserID     string       `json:"owner_user_id"`
	IdentitySource  string       `json:"identity_source"`
	SessionKey      string       `json:"session_key"`
	TopicID         string       `json:"topic_id"`
	TopicType       string       `json:"topic_type"`
	GrantID         string       `json:"grant_id"`
	DeviceID        string       `json:"device_id"`
	DeviceBodyID    string       `json:"device_body_id"`
	DeviceInstallID string       `json:"device_install_id"`
	Operation       string       `json:"operation"`
	ToolName        string       `json:"tool_name"`
	CreatedAt       int64        `json:"created_at"`
	ExpiresAt       int64        `json:"expires_at"`
}

func redisDeviceRPCPendingFromRuntime(record deviceRPCPendingRecord) redisDeviceRPCPending {
	return redisDeviceRPCPending{
		RequestID:       record.requestID,
		RequesterRoute:  record.requesterRoute,
		TargetRoute:     record.targetRoute,
		AgentUID:        record.agentUID,
		AgentID:         record.agentID,
		AgentBodyID:     record.agentBodyID,
		ActorUID:        record.actorUID,
		ActorUserID:     record.actorUserID,
		OwnerUID:        record.ownerUID,
		OwnerUserID:     record.ownerUserID,
		IdentitySource:  record.identitySource,
		SessionKey:      record.sessionKey,
		TopicID:         record.topicID,
		TopicType:       record.topicType,
		GrantID:         record.grantID,
		DeviceID:        record.deviceID,
		DeviceBodyID:    record.deviceBodyID,
		DeviceInstallID: record.deviceInstallID,
		Operation:       record.operation,
		ToolName:        record.toolName,
		CreatedAt:       unixMillis(record.createdAt),
		ExpiresAt:       unixMillis(record.expiresAt),
	}
}

func (r redisDeviceRPCPending) toRuntimeRecord() deviceRPCPendingRecord {
	ownerUID := r.OwnerUID
	if ownerUID <= 0 {
		ownerUID = r.ActorUID
	}
	ownerUserID := r.OwnerUserID
	if ownerUserID == "" && ownerUID > 0 {
		ownerUserID = formatUID(ownerUID)
	}
	return deviceRPCPendingRecord{
		requestID:       r.RequestID,
		requesterRoute:  r.RequesterRoute,
		targetRoute:     r.TargetRoute,
		agentUID:        r.AgentUID,
		agentID:         r.AgentID,
		agentBodyID:     r.AgentBodyID,
		actorUID:        r.ActorUID,
		actorUserID:     r.ActorUserID,
		ownerUID:        ownerUID,
		ownerUserID:     ownerUserID,
		identitySource:  r.IdentitySource,
		sessionKey:      r.SessionKey,
		topicID:         r.TopicID,
		topicType:       r.TopicType,
		grantID:         r.GrantID,
		deviceID:        r.DeviceID,
		deviceBodyID:    r.DeviceBodyID,
		deviceInstallID: r.DeviceInstallID,
		operation:       r.Operation,
		toolName:        r.ToolName,
		createdAt:       time.UnixMilli(r.CreatedAt),
		expiresAt:       time.UnixMilli(r.ExpiresAt),
	}
}
