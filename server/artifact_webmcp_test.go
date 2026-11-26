package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func decodeServerMsg(t *testing.T, ch chan []byte) *ServerMessage {
	t.Helper()
	select {
	case raw := <-ch:
		var msg ServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal server message: %v", err)
		}
		return &msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server message")
		return nil
	}
}

func assertNoServerMsg(t *testing.T, ch chan []byte) {
	t.Helper()
	select {
	case raw := <-ch:
		t.Fatalf("unexpected server message: %s", string(raw))
	default:
	}
}

func TestArtifactWebMcpEndToEndAndSecurity(t *testing.T) {
	hub := NewHub(nil, nil)
	bindings := NewArtifactOpenBindingHandler(hub, nil, nil)
	now := time.Now()
	bindings.now = func() time.Time { return now }

	human := &Client{
		hub:          hub,
		uid:          101,
		accountType:  types.AccountHuman,
		connectionID: "conn-human-1",
		send:         make(chan []byte, 16),
	}
	otherHuman := &Client{
		hub:          hub,
		uid:          102,
		accountType:  types.AccountHuman,
		connectionID: "conn-human-2",
		send:         make(chan []byte, 16),
	}
	bot := &Client{
		hub:          hub,
		uid:          365,
		accountType:  types.AccountBot,
		connectionID: "conn-bot-365",
		send:         make(chan []byte, 16),
	}
	otherBot := &Client{
		hub:          hub,
		uid:          366,
		accountType:  types.AccountBot,
		connectionID: "conn-bot-366",
		send:         make(chan []byte, 16),
	}
	hub.addClient(human)
	hub.addClient(otherHuman)
	hub.addClient(bot)
	hub.addClient(otherBot)

	issued, err := bindings.store.issue(artifactOpenBindingRecord{
		ArtifactOpenBinding: ArtifactOpenBinding{
			ContractVersion: ArtifactOpenBindingContractV1,
			TopicID:         "p2p_101_365",
			AgentUID:        365,
			AppID:           "my-canvas",
			AppOrigin:       "https://artifact.catsco.cc",
			ExpiresAt:       now.Add(30 * time.Minute),
		},
		actorUID: 101,
	}, now)
	if err != nil {
		t.Fatalf("issue open binding: %v", err)
	}

	// 1. Foreign human trying to use someone else's open_ref -> silently rejected.
	hub.handleMessage(otherHuman, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:    artifactWebMcpTypeToolsSync,
			OpenRef: issued.OpenRef,
			Tools:   json.RawMessage(`[{"name":"relayout"}]`),
		},
	})
	assertNoServerMsg(t, bot.send)

	// 2. Owning human syncs tools -> Bot receives tools_sync with canonical fields and NO open_ref.
	hub.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:    artifactWebMcpTypeToolsSync,
			OpenRef: issued.OpenRef,
			Tools:   json.RawMessage(`[{"name":"relayout","description":"Arrange nodes"}]`),
		},
	})
	syncMsg := decodeServerMsg(t, bot.send)
	if syncMsg.ArtifactWebMcp == nil {
		t.Fatalf("expected artifact_webmcp message, got %+v", syncMsg)
	}
	if syncMsg.ArtifactWebMcp.Type != artifactWebMcpTypeToolsSync {
		t.Fatalf("expected tools_sync, got %q", syncMsg.ArtifactWebMcp.Type)
	}
	if syncMsg.ArtifactWebMcp.OpenRef != "" {
		t.Fatalf("open_ref must never be forwarded to bot, got %q", syncMsg.ArtifactWebMcp.OpenRef)
	}
	if syncMsg.ArtifactWebMcp.AppID != "my-canvas" || syncMsg.ArtifactWebMcp.TopicID != "p2p_101_365" {
		t.Fatalf("unexpected canonical routing: %+v", syncMsg.ArtifactWebMcp)
	}
	bridgeID := syncMsg.ArtifactWebMcp.BridgeID
	if bridgeID == "" {
		t.Fatal("expected non-empty bridge_id")
	}

	// 3. Another bot cannot call this bridge -> page_not_open error, human receives nothing.
	hub.handleMessage(otherBot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolCall,
			BridgeID: bridgeID,
			CallID:   "call-evil",
			Name:     "relayout",
		},
	})
	evilRes := decodeServerMsg(t, otherBot.send)
	if evilRes.ArtifactWebMcp == nil || !evilRes.ArtifactWebMcp.IsError || evilRes.ArtifactWebMcp.Error == nil || evilRes.ArtifactWebMcp.Error.Code != "page_not_open" {
		t.Fatalf("expected page_not_open for foreign bot, got %+v", evilRes.ArtifactWebMcp)
	}
	assertNoServerMsg(t, human.send)

	// 4. Owning bot calls tool -> forwarded to owning human; human replies tool_result -> forwarded to bot.
	hub.handleMessage(bot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:      artifactWebMcpTypeToolCall,
			BridgeID:  bridgeID,
			CallID:    "call-1",
			Name:      "relayout",
			Arguments: map[string]interface{}{"mode": "grid"},
		},
	})
	callToHuman := decodeServerMsg(t, human.send)
	if callToHuman.ArtifactWebMcp == nil || callToHuman.ArtifactWebMcp.Type != artifactWebMcpTypeToolCall || callToHuman.ArtifactWebMcp.CallID != "call-1" {
		t.Fatalf("unexpected tool_call to human: %+v", callToHuman.ArtifactWebMcp)
	}

	hub.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:              artifactWebMcpTypeToolResult,
			OpenRef:           issued.OpenRef,
			BridgeID:          bridgeID,
			CallID:            "call-1",
			IsError:           false,
			Content:           json.RawMessage(`[{"type":"text","text":"{\"ok\":true}"}]`),
			StructuredContent: json.RawMessage(`{"ok":true}`),
		},
	})
	resultToBot := decodeServerMsg(t, bot.send)
	if resultToBot.ArtifactWebMcp == nil || resultToBot.ArtifactWebMcp.Type != artifactWebMcpTypeToolResult || resultToBot.ArtifactWebMcp.CallID != "call-1" || resultToBot.ArtifactWebMcp.IsError {
		t.Fatalf("unexpected tool_result to bot: %+v", resultToBot.ArtifactWebMcp)
	}
	if resultToBot.ArtifactWebMcp.OpenRef != "" {
		t.Fatalf("open_ref leaked in tool_result: %q", resultToBot.ArtifactWebMcp.OpenRef)
	}

	// 5. Pending tool call + human disconnect -> pending call fails with page_not_open AND bot receives detach.
	hub.handleMessage(bot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolCall,
			BridgeID: bridgeID,
			CallID:   "call-inflight",
			Name:     "relayout",
		},
	})
	_ = decodeServerMsg(t, human.send)
	hub.removeClient(human)

	msgA := decodeServerMsg(t, bot.send)
	msgB := decodeServerMsg(t, bot.send)
	var gotFail, gotDetach bool
	for _, m := range []*ServerMessage{msgA, msgB} {
		if m.ArtifactWebMcp == nil {
			continue
		}
		if m.ArtifactWebMcp.Type == artifactWebMcpTypeToolResult && m.ArtifactWebMcp.CallID == "call-inflight" && m.ArtifactWebMcp.IsError {
			gotFail = true
		}
		if m.ArtifactWebMcp.Type == artifactWebMcpTypeDetach && m.ArtifactWebMcp.BridgeID == bridgeID {
			gotDetach = true
		}
	}
	if !gotFail || !gotDetach {
		t.Fatalf("expected both inflight failure and detach on disconnect, gotFail=%v gotDetach=%v", gotFail, gotDetach)
	}

	// 6. Device connector must not be allowed to send ArtifactWebMcp.
	if deviceConnectorMessageAllowed(&ClientMessage{ArtifactWebMcp: &MsgArtifactWebMcp{Type: artifactWebMcpTypeToolsSync}}) {
		t.Fatal("deviceConnectorMessageAllowed must reject ArtifactWebMcp")
	}
}

func TestArtifactWebMcpOversizedResultAndTTLSweepAndFingerprint(t *testing.T) {
	hub := NewHub(nil, nil)
	bindings := NewArtifactOpenBindingHandler(hub, nil, nil)
	now := time.Now()
	bindings.now = func() time.Time { return now }
	hub.artifactWebMcp.now = func() time.Time { return now }
	hub.artifactWebMcp.callTTL = 10 * time.Second

	var validFP [32]byte
	validFP[0] = 0xAA
	var wrongFP [32]byte
	wrongFP[0] = 0xBB

	issued, err := bindings.store.issue(artifactOpenBindingRecord{
		ArtifactOpenBinding: ArtifactOpenBinding{
			AppID:     "canvas",
			TopicID:   "p2p_101_365",
			AgentUID:  365,
			ExpiresAt: now.Add(time.Hour),
		},
		actorUID:               101,
		authSessionFingerprint: validFP,
	}, now)
	if err != nil {
		t.Fatalf("issue open_ref: %v", err)
	}

	human := &Client{
		hub:                       hub,
		uid:                       101,
		accountType:               types.AccountHuman,
		connectionID:              "human-fp-1",
		authSessionFingerprint:    validFP,
		hasAuthSessionFingerprint: true,
		send:                      make(chan []byte, 16),
	}
	humanWrongFP := &Client{
		hub:                       hub,
		uid:                       101,
		accountType:               types.AccountHuman,
		connectionID:              "human-fp-bad",
		authSessionFingerprint:    wrongFP,
		hasAuthSessionFingerprint: true,
		send:                      make(chan []byte, 16),
	}
	bot := &Client{
		hub:          hub,
		uid:          365,
		accountType:  types.AccountBot,
		connectionID: "bot-fp-1",
		send:         make(chan []byte, 16),
	}
	hub.addClient(human)
	hub.addClient(humanWrongFP)
	hub.addClient(bot)

	// Wrong auth session fingerprint must be rejected.
	hub.handleMessage(humanWrongFP, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:    artifactWebMcpTypeToolsSync,
			OpenRef: issued.OpenRef,
			Tools:   json.RawMessage(`[{"name":"t"}]`),
		},
	})
	select {
	case raw := <-bot.send:
		t.Fatalf("wrong fingerprint should not sync tools, got %s", string(raw))
	default:
	}

	// Valid fingerprint syncs tools.
	hub.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:    artifactWebMcpTypeToolsSync,
			OpenRef: issued.OpenRef,
			Tools:   json.RawMessage(`[{"name":"big"}]`),
		},
	})
	syncMsg := decodeServerMsg(t, bot.send)
	bridgeID := syncMsg.ArtifactWebMcp.BridgeID

	// H1: Oversized tool_result replies with result_too_large error instead of silent drop.
	hub.handleMessage(bot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolCall,
			BridgeID: bridgeID,
			CallID:   "call-big",
			Name:     "big",
		},
	})
	_ = decodeServerMsg(t, human.send)

	bigBlob := make([]byte, artifactWebMcpMaxPayloadSize+1024)
	for i := range bigBlob {
		bigBlob[i] = 'a'
	}
	hub.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolResult,
			OpenRef:  issued.OpenRef,
			BridgeID: bridgeID,
			CallID:   "call-big",
			Content:  json.RawMessage(`"` + string(bigBlob) + `"`),
		},
	})
	bigReply := decodeServerMsg(t, bot.send)
	if bigReply.ArtifactWebMcp == nil || !bigReply.ArtifactWebMcp.IsError || bigReply.ArtifactWebMcp.Error == nil || bigReply.ArtifactWebMcp.Error.Code != "result_too_large" {
		t.Fatalf("expected result_too_large error reply, got %+v", bigReply.ArtifactWebMcp)
	}

	// H3: Pending call expires via TTL sweep and replies tool_call_timeout.
	hub.handleMessage(bot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolCall,
			BridgeID: bridgeID,
			CallID:   "call-expire",
			Name:     "big",
		},
	})
	_ = decodeServerMsg(t, human.send)
	now = now.Add(15 * time.Second)
	hub.sweepExpiredArtifactWebMcpCalls()
	timeoutReply := decodeServerMsg(t, bot.send)
	if timeoutReply.ArtifactWebMcp == nil || !timeoutReply.ArtifactWebMcp.IsError || timeoutReply.ArtifactWebMcp.Error == nil || timeoutReply.ArtifactWebMcp.Error.Code != "tool_call_timeout" {
		t.Fatalf("expected tool_call_timeout on sweep, got %+v", timeoutReply.ArtifactWebMcp)
	}
}

func TestArtifactWebMcpMultiNodeAndBotReconnectReplay(t *testing.T) {
	shared := newSharedMemoryRuntimeState()
	hubA := NewHubWithRuntime(nil, nil, shared, "node-A")
	hubB := NewHubWithRuntime(nil, nil, shared, "node-B")

	bindingsA := NewArtifactOpenBindingHandler(hubA, nil, nil)
	now := time.Now()
	bindingsA.now = func() time.Time { return now }

	issued, err := bindingsA.store.issue(artifactOpenBindingRecord{
		ArtifactOpenBinding: ArtifactOpenBinding{
			AppID:     "canvas-multi",
			TopicID:   "p2p_101_365",
			AgentUID:  365,
			ExpiresAt: now.Add(time.Hour),
		},
		actorUID: 101,
	}, now)
	if err != nil {
		t.Fatalf("issue open_ref: %v", err)
	}

	// Human connects to node-A, Bot connects to node-B.
	human := &Client{
		hub:          hubA,
		uid:          101,
		accountType:  types.AccountHuman,
		connectionID: "human-nodeA-1",
		send:         make(chan []byte, 16),
	}
	bot1 := &Client{
		hub:          hubB,
		uid:          365,
		accountType:  types.AccountBot,
		bodyID:       "body-365",
		connectionID: "bot-nodeB-1",
		send:         make(chan []byte, 16),
	}
	hubA.addClient(human)
	if _, err := hubB.bodyLeases.acquire(365, "body-365", bot1.connectionID); err != nil {
		t.Fatalf("acquire bot1 lease: %v", err)
	}
	if !hubB.registerClient(bot1) {
		t.Fatal("registerClient(bot1) failed")
	}

	// Human on node-A syncs tools -> Bot on node-B receives tools_sync.
	hubA.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:    artifactWebMcpTypeToolsSync,
			OpenRef: issued.OpenRef,
			Tools:   json.RawMessage(`[{"name":"zoom","description":"Zoom canvas"}]`),
		},
	})
	syncMsg := decodeServerMsg(t, bot1.send)
	if syncMsg.ArtifactWebMcp == nil || syncMsg.ArtifactWebMcp.Type != artifactWebMcpTypeToolsSync {
		t.Fatalf("expected cross-node tools_sync, got %+v", syncMsg.ArtifactWebMcp)
	}
	bridgeID := syncMsg.ArtifactWebMcp.BridgeID

	// Bot on node-B sends tool_call -> routed to Human on node-A -> tool_result returns to Bot on node-B.
	hubB.handleMessage(bot1, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:      artifactWebMcpTypeToolCall,
			BridgeID:  bridgeID,
			CallID:    "call-mn-1",
			Name:      "zoom",
			Arguments: map[string]interface{}{"level": 2},
		},
	})
	callToHuman := decodeServerMsg(t, human.send)
	if callToHuman.ArtifactWebMcp == nil || callToHuman.ArtifactWebMcp.CallID != "call-mn-1" {
		t.Fatalf("expected cross-node tool_call on node-A, got %+v", callToHuman.ArtifactWebMcp)
	}
	hubA.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolResult,
			OpenRef:  issued.OpenRef,
			BridgeID: bridgeID,
			CallID:   "call-mn-1",
			Content:  json.RawMessage(`[{"type":"text","text":"zoomed"}]`),
		},
	})
	resultToBot := decodeServerMsg(t, bot1.send)
	if resultToBot.ArtifactWebMcp == nil || resultToBot.ArtifactWebMcp.CallID != "call-mn-1" || resultToBot.ArtifactWebMcp.IsError {
		t.Fatalf("expected cross-node tool_result on node-B, got %+v", resultToBot.ArtifactWebMcp)
	}

	// H2: Bot disconnects and reconnects as bot2 on node-B -> Hub replays active bridge from node-A!
	hubB.removeClient(bot1)
	hubB.bodyLeases.release(365, "body-365", bot1.connectionID)
	bot2 := &Client{
		hub:          hubB,
		uid:          365,
		accountType:  types.AccountBot,
		bodyID:       "body-365",
		connectionID: "bot-nodeB-2",
		send:         make(chan []byte, 16),
	}
	if _, err := hubB.bodyLeases.acquire(365, "body-365", bot2.connectionID); err != nil {
		t.Fatalf("acquire bot2 lease: %v", err)
	}
	if !hubB.registerClient(bot2) {
		t.Fatal("registerClient(bot2) failed")
	}
	replayed := decodeServerMsg(t, bot2.send)
	if replayed.ArtifactWebMcp == nil || replayed.ArtifactWebMcp.Type != artifactWebMcpTypeToolsSync || replayed.ArtifactWebMcp.BridgeID != bridgeID {
		t.Fatalf("expected replayed tools_sync on bot reconnect, got %+v", replayed.ArtifactWebMcp)
	}
}

func TestArtifactWebMcpRedisRuntimeRouting(t *testing.T) {
	url, closeRedis := newRedisRuntimeTestServer(t)
	defer closeRedis()

	// 1. Single-node RedisRuntimeState: broadcast publish has only self (subscriberCount == 1),
	// so deliverArtifactWebMcp("", ...) returns false and an unknown bridge_id gets immediate page_not_open.
	soloState := newRedisRuntimeStateForTest(t, url, "webmcp-solo")
	defer soloState.Close()
	soloHub := NewHubWithRuntime(nil, nil, soloState, "node-solo")
	soloBot := &Client{
		hub:          soloHub,
		uid:          365,
		accountType:  types.AccountBot,
		bodyID:       "body-solo",
		connectionID: "bot-solo-1",
		send:         make(chan []byte, 16),
	}
	if _, err := soloHub.bodyLeases.acquire(365, "body-solo", soloBot.connectionID); err != nil {
		t.Fatalf("acquire soloBot lease: %v", err)
	}
	if !soloHub.registerClient(soloBot) {
		t.Fatal("registerClient(soloBot) failed")
	}
	soloHub.handleMessage(soloBot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolCall,
			BridgeID: "awm_nonexistent_solo",
			CallID:   "call-solo-404",
			Name:     "zoom",
		},
	})
	soloReply := decodeServerMsg(t, soloBot.send)
	if soloReply.ArtifactWebMcp == nil || !soloReply.ArtifactWebMcp.IsError || soloReply.ArtifactWebMcp.Error == nil || soloReply.ArtifactWebMcp.Error.Code != "page_not_open" {
		t.Fatalf("expected immediate page_not_open on single-node RedisRuntimeState, got %+v", soloReply.ArtifactWebMcp)
	}

	// 2. Two-node RedisRuntimeState: human on node-A, bot on node-B.
	// Exercises userMessageChannel broadcast (tools_sync, tool_call, bot reconnect replay)
	// and nodeInboxChannel direct reply (tool_result).
	stateA := newRedisRuntimeStateForTest(t, url, "webmcp-cluster")
	defer stateA.Close()
	stateB := newRedisRuntimeStateForTest(t, url, "webmcp-cluster")
	defer stateB.Close()

	hubA := NewHubWithRuntime(nil, nil, stateA, "node-a")
	hubB := NewHubWithRuntime(nil, nil, stateB, "node-b")
	// Ensure node heartbeats are active in Redis before cross-node inbox delivery.
	now := time.Now()
	bindingsA := NewArtifactOpenBindingHandler(hubA, nil, nil)
	bindingsA.now = func() time.Time { return now }

	issued, err := bindingsA.store.issue(artifactOpenBindingRecord{
		ArtifactOpenBinding: ArtifactOpenBinding{
			AppID:     "canvas-redis",
			TopicID:   "p2p_101_365",
			AgentUID:  365,
			ExpiresAt: now.Add(time.Hour),
		},
		actorUID: 101,
	}, now)
	if err != nil {
		t.Fatalf("issue open_ref: %v", err)
	}

	human := &Client{
		hub:          hubA,
		uid:          101,
		accountType:  types.AccountHuman,
		connectionID: "human-redis-a",
		send:         make(chan []byte, 16),
	}
	bot := &Client{
		hub:          hubB,
		uid:          365,
		accountType:  types.AccountBot,
		bodyID:       "body-redis-365",
		connectionID: "bot-redis-b1",
		send:         make(chan []byte, 16),
	}
	hubA.addClient(human)
	if _, err := hubB.bodyLeases.acquire(365, "body-redis-365", bot.connectionID); err != nil {
		t.Fatalf("acquire bot lease: %v", err)
	}
	if !hubB.registerClient(bot) {
		t.Fatal("registerClient(bot) failed")
	}

	// Human on node-a syncs tools -> broadcast via userMessageChannel -> Bot on node-b receives tools_sync.
	hubA.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:    artifactWebMcpTypeToolsSync,
			OpenRef: issued.OpenRef,
			Tools:   json.RawMessage(`[{"name":"pan","description":"Pan canvas"}]`),
		},
	})
	syncMsg := decodeServerMsg(t, bot.send)
	if syncMsg.ArtifactWebMcp == nil || syncMsg.ArtifactWebMcp.Type != artifactWebMcpTypeToolsSync {
		t.Fatalf("expected Redis cross-node tools_sync, got %+v", syncMsg.ArtifactWebMcp)
	}
	bridgeID := syncMsg.ArtifactWebMcp.BridgeID

	// Bot on node-b calls tool -> userMessageChannel broadcast -> Human on node-a receives tool_call.
	hubB.handleMessage(bot, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:      artifactWebMcpTypeToolCall,
			BridgeID:  bridgeID,
			CallID:    "call-redis-1",
			Name:      "pan",
			Arguments: map[string]interface{}{"dx": 10},
		},
	})
	callToHuman := decodeServerMsg(t, human.send)
	if callToHuman.ArtifactWebMcp == nil || callToHuman.ArtifactWebMcp.CallID != "call-redis-1" {
		t.Fatalf("expected Redis cross-node tool_call on node-a, got %+v", callToHuman.ArtifactWebMcp)
	}

	// Human on node-a replies tool_result -> nodeInboxChannel direct delivery -> Bot on node-b receives tool_result.
	hubA.handleMessage(human, &ClientMessage{
		ArtifactWebMcp: &MsgArtifactWebMcp{
			Type:     artifactWebMcpTypeToolResult,
			OpenRef:  issued.OpenRef,
			BridgeID: bridgeID,
			CallID:   "call-redis-1",
			Content:  json.RawMessage(`[{"type":"text","text":"panned"}]`),
		},
	})
	var resultToBot *ServerMessage
	for i := 0; i < 3; i++ {
		msg := decodeServerMsg(t, bot.send)
		if msg.ArtifactWebMcp != nil && msg.ArtifactWebMcp.Type == artifactWebMcpTypeToolResult {
			resultToBot = msg
			break
		}
	}
	if resultToBot == nil || resultToBot.ArtifactWebMcp.CallID != "call-redis-1" || resultToBot.ArtifactWebMcp.IsError {
		t.Fatalf("expected Redis nodeInboxChannel tool_result on node-b, got %+v", resultToBot)
	}

	// Bot disconnects and reconnects as bot2 on node-b -> Redis replay_request triggers tools_sync replay from node-a.
	hubB.removeClient(bot)
	hubB.bodyLeases.release(365, "body-redis-365", bot.connectionID)
	bot2 := &Client{
		hub:          hubB,
		uid:          365,
		accountType:  types.AccountBot,
		bodyID:       "body-redis-365",
		connectionID: "bot-redis-b2",
		send:         make(chan []byte, 16),
	}
	if _, err := hubB.bodyLeases.acquire(365, "body-redis-365", bot2.connectionID); err != nil {
		t.Fatalf("acquire bot2 lease: %v", err)
	}
	if !hubB.registerClient(bot2) {
		t.Fatal("registerClient(bot2) failed")
	}
	replayed := decodeServerMsg(t, bot2.send)
	if replayed.ArtifactWebMcp == nil || replayed.ArtifactWebMcp.Type != artifactWebMcpTypeToolsSync || replayed.ArtifactWebMcp.BridgeID != bridgeID {
		t.Fatalf("expected Redis replayed tools_sync on bot reconnect, got %+v", replayed.ArtifactWebMcp)
	}
}
