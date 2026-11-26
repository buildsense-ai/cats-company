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
