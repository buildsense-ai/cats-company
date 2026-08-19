package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/openchat/openchat/server/store/types"
	"github.com/redis/go-redis/v9"
)

type failingAttentionMutationHook struct {
	enabled *atomic.Bool
}

type failingAttentionProbeReplyHook struct {
	enabled *atomic.Bool
}

type stalledRuntimeWriteHook struct {
	enabled *atomic.Bool
}

type stalledRedisGetHook struct {
	enabled *atomic.Bool
	started chan<- struct{}
}

func (h failingAttentionMutationHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h failingAttentionMutationHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.enabled.Load() && cmd.Name() == "zrange" {
			return errors.New("injected attention mutation failure")
		}
		return next(ctx, cmd)
	}
}

func (h failingAttentionMutationHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (h failingAttentionProbeReplyHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h failingAttentionProbeReplyHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.enabled.Load() && cmd.Name() == "publish" {
			return errors.New("injected attention probe reply failure")
		}
		return next(ctx, cmd)
	}
}

func (h failingAttentionProbeReplyHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (h stalledRuntimeWriteHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h stalledRuntimeWriteHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.enabled.Load() && (cmd.Name() == "set" || cmd.Name() == "zrange") {
			<-ctx.Done()
			return ctx.Err()
		}
		return next(ctx, cmd)
	}
}

func (h stalledRuntimeWriteHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (h stalledRedisGetHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h stalledRedisGetHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.enabled.Load() && cmd.Name() == "get" {
			select {
			case h.started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		}
		return next(ctx, cmd)
	}
}

func (h stalledRedisGetHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRedisMessagingAttentionUsesDedicatedKeyDuringRollingUpgrade(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "attention-rollout")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "attention-rollout")
	defer stateB.Close()
	legacyKey := stateA.key("visible_messaging", "42")
	if got := stateA.messagingClientAttentionKey(42); got == legacyKey {
		t.Fatal("exact attention must not reuse the legacy visibility key")
	}
	if err := stateA.client.ZAdd(context.Background(), legacyKey, redis.Z{
		Score:  float64(time.Now().Add(time.Minute).UnixMilli()),
		Member: "legacy-visible-connection",
	}).Err(); err != nil {
		t.Fatalf("seed legacy visibility member: %v", err)
	}
	if stateB.hasMessagingClientAttention("node-b", 42, "sub", "grp_7", time.Now()) {
		t.Fatal("v2 readers must ignore legacy broad-visibility members")
	}

	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(page)
	hubA.bindClientRuntimeRoute(page)
	hubA.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	if count, err := stateA.client.ZCard(context.Background(), legacyKey).Result(); err != nil || count != 1 {
		t.Fatalf("new attention must not write the legacy key: count=%d err=%v", count, err)
	}
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_7") }, "v2 readers should confirm the dedicated attention key")
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

	eventually(t, func() bool { return hubA.hasMessagingClientAttention(42, "sub", "grp_7") }, "node-a should observe matching attention registered by node-b")
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

}

func TestRedisRuntimeRefreshesMessagingAttentionLeaseOnHeartbeat(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "visibility-heartbeat")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "visibility-heartbeat")
	defer stateB.Close()

	now := time.Now().UTC()
	var clockUnixNano atomic.Int64
	clockUnixNano.Store(now.UnixNano())
	clock := func() time.Time {
		return time.Unix(0, clockUnixNano.Load()).UTC()
	}
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	hubA.deviceRPC.now = clock
	hubB.deviceRPC.now = clock

	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubB.addClient(page)
	hubB.bindClientRuntimeRoute(page)
	hubB.setClientMessagingAttention(page, messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true})
	eventually(t, func() bool { return hubA.hasMessagingClientAttention(42, "sub", "grp_7") }, "fresh matching attention should suppress a push")

	now = now.Add(pageVisibilityLeaseTTL + time.Second)
	clockUnixNano.Store(now.UnixNano())
	if hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("attention lease should expire without a heartbeat")
	}

	hubB.bindClientRuntimeRoute(page)
	eventually(t, func() bool { return hubA.hasMessagingClientAttention(42, "sub", "grp_7") }, "pong heartbeat should refresh the attention lease")
}

func TestRedisRuntimeAttentionQueryFailsOpenForPushDelivery(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	state := newRedisRuntimeStateForTest(t, url, "attention-unavailable")
	closeRedis()

	if state.hasMessagingClientAttention("node-a", 42, "sub", "grp_7", time.Now()) {
		t.Fatal("an unavailable runtime must not suppress a notification without a positive match")
	}
	_ = state.Close()
}

func TestRedisRuntimeWriteTimeoutKeepsAttentionFailOpen(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	state := newRedisRuntimeStateForTest(t, url, "attention-write-timeout")
	defer state.Close()
	var stallWrites atomic.Bool
	// Install client hooks before NewHubWithRuntime starts its background Redis
	// timeout worker. go-redis does not support mutating its hook chain while a
	// command is in flight.
	state.client.AddHook(stalledRuntimeWriteHook{enabled: &stallWrites})
	hub := NewHubWithRuntime(nil, nil, state, "node-a")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(page)

	stallWrites.Store(true)

	start := time.Now()
	hub.bindClientRuntimeRoute(page)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stalled route write blocked websocket work for %s", elapsed)
	}

	start = time.Now()
	hub.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stalled attention write blocked websocket work for %s", elapsed)
	}
	if !hub.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("local current attention should remain usable when Redis writes time out")
	}

	hub.setClientPageVisibility(page, pageVisibilityHidden)
	if hub.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("a newer hidden state must fail open even when its Redis clear times out")
	}
}

func TestRedisRuntimeRouteClearTimeoutDoesNotLeaveLaterDisconnectSuppressingPush(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "route-clear-timeout")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "route-clear-timeout")
	defer stateB.Close()
	var stallRouteClear atomic.Bool
	var failAttentionClear atomic.Bool
	routeClearStarted := make(chan struct{}, 1)
	stateA.client.AddHook(stalledRedisGetHook{enabled: &stallRouteClear, started: routeClearStarted})
	stateA.client.AddHook(failingAttentionMutationHook{enabled: &failAttentionClear})
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	first := &Client{uid: 42, send: make(chan []byte, 1)}
	visible := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(first)
	hubA.bindClientRuntimeRoute(first)
	hubA.addClient(visible)
	hubA.bindClientRuntimeRoute(visible)
	hubA.setClientMessagingAttention(visible, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_7") }, "precondition: remote node should confirm active attention")

	go hubA.Run()

	stallRouteClear.Store(true)
	hubA.unregister <- first
	select {
	case <-routeClearStarted:
	case <-time.After(time.Second):
		t.Fatal("first disconnect did not reach its stalled runtime route clear")
	}
	stallRouteClear.Store(false)
	failAttentionClear.Store(true)
	hubA.unregister <- visible

	eventually(t, func() bool {
		return hubA.getClientByConnectionID(visible.connectionID) == nil
	}, "bounded first route clear should let the Hub remove the later disconnect")
	if hubB.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("a stale Redis attention member for a disconnected client must fail open")
	}
}

func TestRedisRuntimeDisconnectCleanupTransactionsTimeOut(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	state := newRedisRuntimeStateForTest(t, url, "disconnect-cleanup-timeout")
	defer state.Close()
	now := time.Now()
	route := runtimeRoute{NodeID: "node-a", ConnectionID: "conn-a", ExpiresAt: now.Add(time.Minute)}
	device := UserDevice{DeviceID: "alice-laptop"}
	state.bindUserDeviceRoute(7, device, route, now)
	if _, err := state.acquireBotBodyLease(42, "body-a", "bot-conn", "node-a", now, time.Minute); err != nil {
		t.Fatalf("seed bot lease: %v", err)
	}

	var stallGets atomic.Bool
	stallStarted := make(chan struct{}, 2)
	state.client.AddHook(stalledRedisGetHook{enabled: &stallGets, started: stallStarted})
	stallGets.Store(true)

	assertStalledCleanupReturns(t, stallStarted, func() {
		state.clearUserDeviceRoute(7, device.DeviceID, route)
	}, "device route clear")
	assertStalledCleanupReturns(t, stallStarted, func() {
		state.releaseBotBodyLease(42, "body-a", "bot-conn", "node-a")
	}, "bot lease release")
}

func assertStalledCleanupReturns(t *testing.T, started <-chan struct{}, cleanup func(), description string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("%s did not reach its stalled Redis read", description)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not respect the runtime cleanup deadline", description)
	}
}

func TestRedisRuntimeProbeFailsOpenAfterAttentionMutationFailure(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "attention-mutation-failure")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "attention-mutation-failure")
	defer stateB.Close()
	var failMutations atomic.Bool
	stateA.client.AddHook(failingAttentionMutationHook{enabled: &failMutations})
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(page)
	hubA.bindClientRuntimeRoute(page)
	hubA.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_7") }, "precondition: remote node should confirm active attention")

	failMutations.Store(true)
	hubA.setClientPageVisibility(page, pageVisibilityHidden)
	if hubB.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("a stale Redis member must not suppress after its owner page becomes hidden")
	}
}

func TestRedisRuntimeProbeFailsOpenWhenOwnerCannotReply(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "attention-probe-partition")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "attention-probe-partition")
	defer stateB.Close()
	var failReplies atomic.Bool
	stateA.client.AddHook(failingAttentionProbeReplyHook{enabled: &failReplies})
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(page)
	hubA.bindClientRuntimeRoute(page)
	hubA.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_7") }, "precondition: remote node should confirm active attention")

	failReplies.Store(true)
	if hubB.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("a missing owner confirmation must fail open and deliver Push")
	}
}

func TestRedisRuntimeProbeRejectsStaleTopicAfterMutationFailure(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "attention-stale-topic")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "attention-stale-topic")
	defer stateB.Close()
	var failMutations atomic.Bool
	stateA.client.AddHook(failingAttentionMutationHook{enabled: &failMutations})
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(page)
	hubA.bindClientRuntimeRoute(page)
	hubA.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_7") }, "precondition: remote node should confirm initial attention")

	failMutations.Store(true)
	hubA.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_8", Visible: true, Focused: true,
	})
	if hubB.hasMessagingClientAttention(42, "sub", "grp_7") || hubB.hasMessagingClientAttention(42, "sub", "grp_8") {
		t.Fatal("failed topic persistence must not let an old or unknown topic suppress Push")
	}

	failMutations.Store(false)
	hubA.bindClientRuntimeRoute(page)
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_8") }, "the current topic should suppress after its normal heartbeat persists it")
}

func TestRedisRuntimeProbeRejectsStaleConnectionAfterDisconnect(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	stateA := newRedisRuntimeStateForTest(t, url, "attention-stale-connection")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "attention-stale-connection")
	defer stateB.Close()
	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	page := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(page)
	hubA.bindClientRuntimeRoute(page)
	hubA.setClientMessagingAttention(page, messagingClientAttention{
		SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})
	eventually(t, func() bool { return hubB.hasMessagingClientAttention(42, "sub", "grp_7") }, "precondition: remote node should confirm active attention")

	hubA.removeClient(page)
	if hubB.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("a disconnected connection must not suppress from a stale Redis member")
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

func TestRedisRuntimeExpiredDeviceRPCPendingRemainsClaimableForTimeout(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()
	state := newRedisRuntimeStateForTest(t, url, "pending-expiry")
	defer state.Close()

	now := time.Now()
	pending := deviceRPCPendingRecord{
		requestID: "rpc-expired-pending",
		agentUID:  42,
		ownerUID:  7,
		deviceID:  "alice-laptop",
		createdAt: now,
		expiresAt: now.Add(time.Second),
	}
	if ok, reason := state.addDeviceRPCPending(pending, now); !ok {
		t.Fatalf("add pending: %s", reason)
	}
	if expired := state.expireDeviceRPCPending(pending.expiresAt); len(expired) != 1 || expired[0].requestID != pending.requestID {
		t.Fatalf("expired pending was not claimable: %#v", expired)
	}
	if _, ok := state.getDeviceRPCPending(pending.requestID, now); ok {
		t.Fatal("claimed timeout pending remains readable")
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

func eventually(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !condition() {
		t.Fatalf("timed out waiting for %s", description)
	}
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
