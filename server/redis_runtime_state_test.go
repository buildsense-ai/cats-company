package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/openchat/openchat/server/store/types"
	"github.com/redis/go-redis/v9"
)

type blockingAttentionZAddHook struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockingAttentionZAddHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *blockingAttentionZAddHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}

func (h *blockingAttentionZAddHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if cmd.Name() == "zadd" {
				h.once.Do(func() {
					close(h.entered)
					<-h.release
				})
				break
			}
		}
		return next(ctx, cmds)
	}
}

func TestRedisRuntimeBotBodyLeaseRejectsDifferentBodyAcrossStates(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "lease")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "lease")
	defer stateB.Close()

	now := time.Now().UTC()
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	hubA.bodyLeases.now = func() time.Time { return now }
	hubB.bodyLeases.now = func() time.Time { return now }

	if _, err := hubA.bodyLeases.acquire(42, "body-a", "conn-a"); err != nil {
		t.Fatalf("node-a acquire failed: %v", err)
	}
	if _, err := hubB.bodyLeases.acquire(42, "body-b", "conn-b"); !errors.Is(err, errBotBodyLeaseConflict) {
		t.Fatalf("node-b different body acquire error = %v, want conflict", err)
	}
	if _, err := hubB.bodyLeases.acquire(42, "body-a", "conn-b2"); err != nil {
		t.Fatalf("same body reconnect from node-b failed: %v", err)
	}
	if hubA.bodyLeases.release(42, "body-a", "conn-a") {
		t.Fatal("stale node-a connection must not release the replacement Redis lease")
	}
	if !hubB.bodyLeases.isCurrent(42, "body-a", "conn-b2") {
		t.Fatal("node-b replacement lease should be current")
	}
}

func TestRedisRuntimeRoutesDeviceRPCAcrossStates(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateAgent := newRedisRuntimeStateForTest(t, url, "rpc")
	defer stateAgent.Close()
	stateDevice := newRedisRuntimeStateForTest(t, url, "rpc")
	defer stateDevice.Close()

	hubAgent := NewHubWithRuntime(nil, nil, stateAgent, "node-agent")
	hubDevice := NewHubWithRuntime(nil, nil, stateDevice, "node-device")

	device, err := hubDevice.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID:       "alice-laptop",
		DisplayName:    "Alice Laptop",
		BodyID:         "body-device",
		InstallationID: "install-device",
		Capabilities:   []string{"read_file", "grep"},
	})
	if err != nil {
		t.Fatalf("register device on node-device: %v", err)
	}
	grants := hubAgent.userDevices.grantsForDevices(7, "p2p_7_42", "p2p", 42, "body-agent", []UserDevice{device})
	if len(grants) != 1 {
		t.Fatalf("redis grantsForDevices returned %d grants", len(grants))
	}

	agent := &Client{
		hub:         hubAgent,
		uid:         42,
		accountType: types.AccountBot,
		bodyID:      "body-agent",
		send:        make(chan []byte, 4),
	}
	target := &Client{
		hub:         hubDevice,
		uid:         77,
		accountType: types.AccountHuman,
		bodyID:      "body-device",
		send:        make(chan []byte, 4),
	}
	hubAgent.addClient(agent)
	hubAgent.bindClientRuntimeRoute(agent)
	hubDevice.addClient(target)
	hubDevice.bindDeviceClient(7, device, target)
	time.Sleep(50 * time.Millisecond)

	hubAgent.handleDeviceRPC(agent, &MsgDeviceRPC{
		ID:        "rpc-msg-1",
		Type:      "request",
		RequestID: "rpc-cross-redis",
		GrantID:   grants[0].GrantID,
		DeviceID:  device.DeviceID,
		Operation: "read_file",
		ToolName:  "read_file",
	})

	var forwarded ServerMessage
	decodeQueuedServerMessageEventually(t, target.send, &forwarded)
	if forwarded.DeviceRPC == nil || forwarded.DeviceRPC.RequestID != "rpc-cross-redis" {
		t.Fatalf("target on node-device received %#v, want cross-node rpc request", forwarded)
	}
	if forwarded.DeviceRPC.OwnerUserID != grants[0].OwnerUserID || forwarded.DeviceRPC.IdentitySource != grants[0].IdentitySource {
		t.Fatalf("redis cross-node request missing owner identity: %#v", forwarded.DeviceRPC)
	}
	var ack ServerMessage
	decodeQueuedServerMessage(t, agent.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("agent request ack = %#v, want 200", ack.Ctrl)
	}
	if pending := hubDevice.DeviceRPCStatus(7, "usr42"); len(pending) != 1 || !pending[0].RequesterConnected || !pending[0].TargetConnected {
		t.Fatalf("redis pending status from node-device = %#v, want connected request/target", pending)
	}

	hubDevice.handleDeviceRPC(target, &MsgDeviceRPC{
		ID:        "rpc-result-1",
		Type:      "result",
		RequestID: "rpc-cross-redis",
		Result:    map[string]interface{}{"ok": true},
	})

	var targetAck ServerMessage
	decodeQueuedServerMessage(t, target.send, &targetAck)
	if targetAck.Ctrl == nil || targetAck.Ctrl.Code != 200 {
		t.Fatalf("target result ack = %#v, want 200", targetAck.Ctrl)
	}
	var result ServerMessage
	decodeQueuedServerMessageEventually(t, agent.send, &result)
	if result.DeviceRPC == nil || result.DeviceRPC.Type != "result" || result.DeviceRPC.RequestID != "rpc-cross-redis" {
		t.Fatalf("agent on node-agent received %#v, want cross-node rpc result", result)
	}
	if result.DeviceRPC.OwnerUserID != grants[0].OwnerUserID || result.DeviceRPC.IdentitySource != grants[0].IdentitySource {
		t.Fatalf("redis cross-node result missing owner identity: %#v", result.DeviceRPC)
	}
	if pending := hubAgent.DeviceRPCStatus(7, "usr42"); len(pending) != 0 {
		t.Fatalf("redis pending should be cleared from node-agent: %#v", pending)
	}
}

func TestRedisRuntimeAggregatesMessagingAttentionAcrossStates(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "visibility")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "visibility")
	defer stateB.Close()

	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubB.addClient(page)
	hubB.bindClientRuntimeRoute(page)
	attention := messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true}
	hubB.setClientMessagingAttention(page, attention)

	if !hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("node-a should observe matching attention registered by node-b")
	}
	if hubA.hasMessagingClientAttention(42, "sub", "grp_8") || hubA.hasMessagingClientAttention(42, "other", "grp_7") {
		t.Fatal("different topic or subscription must not be suppressed")
	}

	hubB.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_8", Visible: true, Focused: true,
	})
	if hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("changing topics must replace the prior attention lease immediately")
	}
	if !hubA.hasMessagingClientAttention(42, "sub", "grp_8") {
		t.Fatal("changing topics should publish the replacement attention lease")
	}

	hubB.setClientPageVisibility(page, pageVisibilityHidden)
	if hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("hidden page should be removed from shared attention state")
	}

	now := time.Now().UTC()
	route := runtimeRoute{
		NodeID:       "node-b",
		ConnectionID: "page-b",
		ExpiresAt:    now.Add(time.Minute),
	}
	stateB.setMessagingClientAttention(42, route, attention, now, time.Second)
	if !stateA.hasMessagingClientAttention(42, "sub", "grp_7", now.Add(500*time.Millisecond)) {
		t.Fatal("fresh shared attention lease should be active")
	}
	if stateA.hasMessagingClientAttention(42, "sub", "grp_7", now.Add(2*time.Second)) {
		t.Fatal("expired shared attention lease should not suppress a push")
	}
}

func TestRedisRuntimeRefreshesMessagingAttentionLeaseOnHeartbeat(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "visibility-heartbeat")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "visibility-heartbeat")
	defer stateB.Close()

	now := time.Now().UTC()
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	hubA.deviceRPC.now = func() time.Time { return now }
	hubB.deviceRPC.now = func() time.Time { return now }

	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubB.addClient(page)
	hubB.bindClientRuntimeRoute(page)
	hubB.setClientMessagingAttention(page, messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true})
	if !hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("fresh matching attention should suppress a push")
	}

	now = now.Add(pageVisibilityLeaseTTL + time.Second)
	if hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("attention lease should expire without a heartbeat")
	}

	hubB.bindClientRuntimeRoute(page)
	if !hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("pong heartbeat should refresh the attention lease")
	}
}

func TestRedisRuntimeAttentionQueryFailsOpenForPushDelivery(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	state := newRedisRuntimeStateForTest(t, url, "attention-unavailable")
	closeRedis()

	if state.hasMessagingClientAttention(42, "sub", "grp_7", time.Now()) {
		t.Fatal("an unavailable runtime must not suppress a notification without a positive match")
	}
	_ = state.Close()
}

func TestRedisRuntimeDisconnectWaitsForInFlightAttentionPublication(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	state := newRedisRuntimeStateForTest(t, url, "attention-disconnect")
	defer state.Close()
	hub := NewHubWithRuntime(nil, nil, state, "node-a")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(page)
	hub.bindClientRuntimeRoute(page)

	hook := &blockingAttentionZAddHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	state.client.AddHook(hook)

	updateDone := make(chan struct{})
	go func() {
		hub.setClientMessagingAttention(page, messagingClientAttention{
			SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
		})
		close(updateDone)
	}()
	<-hook.entered

	hub.removeClient(page)
	clearDone := make(chan struct{})
	go func() {
		hub.clearClientRuntimeRoute(page)
		close(clearDone)
	}()
	select {
	case <-clearDone:
		t.Fatal("disconnect cleanup must wait for an in-flight attention publication")
	case <-time.After(25 * time.Millisecond):
	}

	close(hook.release)
	<-updateDone
	<-clearDone
	if state.hasMessagingClientAttention(42, "sub", "grp_7", time.Now()) {
		t.Fatal("a disconnected page must not leave a suppressing attention lease")
	}
}

func TestRedisRuntimeClearStaleDeviceRouteDoesNotRemoveReplacement(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()
	state := newRedisRuntimeStateForTest(t, url, "routes")
	defer state.Close()

	device := UserDevice{DeviceID: "alice-laptop"}
	now := time.Now()
	oldRoute := runtimeRoute{NodeID: "node-a", ConnectionID: "old-conn", ExpiresAt: now.Add(time.Minute)}
	newRoute := runtimeRoute{NodeID: "node-b", ConnectionID: "new-conn", ExpiresAt: now.Add(time.Minute)}

	state.bindUserDeviceRoute(7, device, oldRoute, now)
	state.bindUserDeviceRoute(7, device, newRoute, now)
	state.clearUserDeviceRoute(7, device.DeviceID, oldRoute)

	got, ok := state.userDeviceRoute(7, device.DeviceID, now)
	if !ok || !got.matches(newRoute) {
		t.Fatalf("device route = %+v ok=%v, want replacement route", got, ok)
	}
}

func TestRedisRuntimeRouteRequiresLiveNodeHeartbeat(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()
	state := newRedisRuntimeStateForTest(t, url, "heartbeat")
	defer state.Close()

	now := time.Now()
	route := runtimeRoute{NodeID: "node-a", ConnectionID: "conn-a", ExpiresAt: now.Add(time.Minute)}
	state.bindRuntimeRoute(route, now)
	if !state.routeConnected(route, now) {
		t.Fatal("fresh Redis route with live node heartbeat should be connected")
	}
	if err := state.client.Del(context.Background(), state.nodeKey(route.NodeID)).Err(); err != nil {
		t.Fatalf("delete node heartbeat: %v", err)
	}
	if state.routeConnected(route, now) {
		t.Fatal("route should not be connected after node heartbeat disappears")
	}
}

func newRedisRuntimeTestServer(t *testing.T) (string, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	return "redis://" + mr.Addr(), mr.Close
}

func newRedisRuntimeStateForTest(t *testing.T, redisURL string, name string) *RedisRuntimeState {
	t.Helper()
	state, err := NewRedisRuntimeState(context.Background(), RedisRuntimeOptions{
		URL:       redisURL,
		KeyPrefix: "test:" + name,
	})
	if err != nil {
		t.Fatalf("new redis runtime state: %v", err)
	}
	return state
}

func decodeQueuedServerMessageEventually(t *testing.T, ch <-chan []byte, msg *ServerMessage) {
	t.Helper()
	select {
	case raw := <-ch:
		if err := json.Unmarshal(raw, msg); err != nil {
			t.Fatalf("decode server message: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected queued server message")
	}
}
