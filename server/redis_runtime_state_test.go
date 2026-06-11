package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/openchat/openchat/server/store/types"
)

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
	if pending := hubAgent.DeviceRPCStatus(7, "usr42"); len(pending) != 0 {
		t.Fatalf("redis pending should be cleared from node-agent: %#v", pending)
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
