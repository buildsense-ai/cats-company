package server

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestHubTracksMultipleConnectionsPerUser(t *testing.T) {
	hub := NewHub(nil, nil)
	clientA := &Client{uid: 42, send: make(chan []byte, 1)}
	clientB := &Client{uid: 42, send: make(chan []byte, 1)}
	clientC := &Client{uid: 99, send: make(chan []byte, 1)}

	first, devices, online := hub.addClient(clientA)
	if !first || devices != 1 || online != 1 {
		t.Fatalf("first add = (%v, %d, %d), want (true, 1, 1)", first, devices, online)
	}

	first, devices, online = hub.addClient(clientB)
	if first || devices != 2 || online != 1 {
		t.Fatalf("second add = (%v, %d, %d), want (false, 2, 1)", first, devices, online)
	}

	first, devices, online = hub.addClient(clientC)
	if !first || devices != 1 || online != 2 {
		t.Fatalf("third add = (%v, %d, %d), want (true, 1, 2)", first, devices, online)
	}

	if !hub.IsOnline(42) || !hub.IsOnline(99) {
		t.Fatal("expected both users to be online")
	}

	removed, last, remaining, online := hub.removeClient(clientA)
	if !removed || last || remaining != 1 || online != 2 {
		t.Fatalf("remove first client = (%v, %v, %d, %d), want (true, false, 1, 2)", removed, last, remaining, online)
	}

	removed, last, remaining, online = hub.removeClient(clientB)
	if !removed || !last || remaining != 0 || online != 1 {
		t.Fatalf("remove last client = (%v, %v, %d, %d), want (true, true, 0, 1)", removed, last, remaining, online)
	}

	if hub.IsOnline(42) {
		t.Fatal("expected uid 42 to be offline after removing all connections")
	}
}

func TestHubTracksMessagingAttentionWithoutBroadcastingInternalNotes(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(client)

	hub.handleMessage(client, &ClientMessage{
		Note: &MsgClientNote{
			What:               "attention",
			Visibility:         "visible",
			Focused:            true,
			ActiveTopic:        "grp_7",
			PushSubscriptionID: "subscription-a",
		},
	})
	if !hub.hasMessagingClientAttention(42, "subscription-a", "grp_7") {
		t.Fatal("focused visible page on the target topic should suppress its subscription")
	}
	if drainOne(client.send) {
		t.Fatal("messaging attention note must not be broadcast as an info message")
	}
	if hub.hasMessagingClientAttention(42, "subscription-a", "grp_8") {
		t.Fatal("a different active topic must not suppress the target conversation")
	}
	if hub.hasMessagingClientAttention(42, "subscription-b", "grp_7") {
		t.Fatal("another device subscription must not be suppressed")
	}
}

func TestMessagingAttentionRequiresCompleteFocusedVisibleIdentity(t *testing.T) {
	tests := []struct {
		name      string
		attention messagingClientAttention
		want      bool
	}{
		{name: "focused visible target", attention: messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true}, want: true},
		{name: "hidden", attention: messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Focused: true}},
		{name: "blurred", attention: messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true}},
		{name: "legacy missing subscription", attention: messagingClientAttention{ActiveTopic: "grp_7", Visible: true, Focused: true}},
		{name: "missing topic", attention: messagingClientAttention{SubscriptionID: "sub", Visible: true, Focused: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.attention.suppresses("sub", "grp_7"); got != test.want {
				t.Fatalf("suppresses = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMessagingAttentionTreatsUnknownVisibilityAsHidden(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(client)

	for _, visibility := range []string{"", "unknown", "VISIBLE "} {
		hub.handleNote(client, &MsgClientNote{
			What:               "attention",
			Visibility:         visibility,
			Focused:            true,
			ActiveTopic:        "grp_7",
			PushSubscriptionID: "subscription-a",
		})
		if hub.hasMessagingClientAttention(42, "subscription-a", "grp_7") {
			t.Fatalf("visibility %q must fail open", visibility)
		}
	}

	hub.handleNote(client, &MsgClientNote{
		What:               "attention",
		Visibility:         pageVisibilityVisible,
		Focused:            true,
		ActiveTopic:        "grp_7",
		PushSubscriptionID: "subscription-a",
	})
	if !hub.hasMessagingClientAttention(42, "subscription-a", "grp_7") {
		t.Fatal("exact visible state should suppress the matching subscription")
	}
}

func TestHandshakeMessagingAttentionTreatsUnknownVisibilityAsHidden(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{uid: 42, send: make(chan []byte, 4)}
	hub.addClient(client)

	for _, visibility := range []string{"", "unknown", "VISIBLE "} {
		hub.handleHi(client, "", &MsgClientHi{
			Visibility:         visibility,
			Focused:            true,
			ActiveTopic:        "grp_7",
			PushSubscriptionID: "subscription-a",
		})
		if hub.hasMessagingClientAttention(42, "subscription-a", "grp_7") {
			t.Fatalf("handshake visibility %q must fail open", visibility)
		}
	}

	hub.handleHi(client, "", &MsgClientHi{
		Visibility:         pageVisibilityVisible,
		Focused:            true,
		ActiveTopic:        "grp_7",
		PushSubscriptionID: "subscription-a",
	})
	if !hub.hasMessagingClientAttention(42, "subscription-a", "grp_7") {
		t.Fatal("exact visible handshake should suppress the matching subscription")
	}
}

func TestAnyFocusedTabOnSharedSubscriptionSuppressesOnlyItsActiveTopic(t *testing.T) {
	hub := NewHub(nil, nil)
	otherTopic := &Client{uid: 42, send: make(chan []byte, 1)}
	targetTopic := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(otherTopic)
	hub.addClient(targetTopic)

	hub.setClientMessagingAttention(otherTopic, messagingClientAttention{
		SubscriptionID: "shared-sub", ActiveTopic: "grp_8", Visible: true, Focused: true,
	})
	hub.setClientMessagingAttention(targetTopic, messagingClientAttention{
		SubscriptionID: "shared-sub", ActiveTopic: "grp_7", Visible: true, Focused: true,
	})

	if !hub.hasMessagingClientAttention(42, "shared-sub", "grp_7") {
		t.Fatal("any focused tab on the target topic should suppress the shared subscription")
	}
	hub.setClientMessagingAttention(targetTopic, messagingClientAttention{
		SubscriptionID: "shared-sub", ActiveTopic: "grp_7", Visible: true, Focused: false,
	})
	if hub.hasMessagingClientAttention(42, "shared-sub", "grp_7") {
		t.Fatal("tabs focused only on other topics must not suppress the target topic")
	}
}

func TestSharedRuntimeAggregatesMessagingAttentionAcrossHubs(t *testing.T) {
	shared := newSharedMemoryRuntimeState()
	hubA := NewHubWithRuntime(nil, nil, shared, "node-a")
	hubB := NewHubWithRuntime(nil, nil, shared, "node-b")

	localHidden := &Client{uid: 42, send: make(chan []byte, 1)}
	remoteVisible := &Client{uid: 42, send: make(chan []byte, 1)}
	hubA.addClient(localHidden)
	hubB.addClient(remoteVisible)

	attention := messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true}
	hubA.setClientMessagingAttention(localHidden, messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7"})
	hubB.setClientMessagingAttention(remoteVisible, attention)
	if !hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("node-a should observe matching attention on node-b")
	}
	hubB.clearClientRuntimeRoute(remoteVisible)
	if hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("disconnecting the remote page should clear its shared attention lease")
	}
	hubB.setClientMessagingAttention(remoteVisible, attention)

	hubB.setClientMessagingAttention(remoteVisible, messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_8", Visible: true, Focused: true})
	if hubA.hasMessagingClientAttention(42, "sub", "grp_7") {
		t.Fatal("a different topic should not suppress a push")
	}

	now := time.Now()
	route := runtimeRoute{NodeID: "node-b", ConnectionID: "expired", ExpiresAt: now.Add(time.Second)}
	shared.setMessagingClientAttention(42, route, attention, now, time.Second)
	if !shared.hasMessagingClientAttention("", 42, "sub", "grp_7", now.Add(500*time.Millisecond)) {
		t.Fatal("fresh shared attention lease should be active")
	}
	if shared.hasMessagingClientAttention("", 42, "sub", "grp_7", now.Add(2*time.Second)) {
		t.Fatal("expired shared attention lease should not suppress a push")
	}
}

func TestHubMessagingAttentionReadsAndWritesAreRaceSafe(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(client)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			hub.setClientMessagingAttention(client, messagingClientAttention{SubscriptionID: "sub", ActiveTopic: "grp_7", Visible: true, Focused: true})
		}()
		go func() {
			defer wg.Done()
			_ = hub.hasMessagingClientAttention(42, "sub", "grp_7")
		}()
	}
	wg.Wait()
}

func TestSendToUserExceptAndSendToClient(t *testing.T) {
	hub := NewHub(nil, nil)
	clientA := &Client{uid: 7, send: make(chan []byte, 1)}
	clientB := &Client{uid: 7, send: make(chan []byte, 1)}
	clientC := &Client{uid: 8, send: make(chan []byte, 1)}

	hub.addClient(clientA)
	hub.addClient(clientB)
	hub.addClient(clientC)

	msg := &ServerMessage{Ctrl: &MsgServerCtrl{Code: 200, Text: "ok"}}

	hub.SendToUserExcept(7, msg, clientA)
	if !drainOne(clientB.send) {
		t.Fatal("expected included sibling connection to receive the message")
	}
	if drainOne(clientA.send) {
		t.Fatal("did not expect excluded connection to receive the message")
	}
	if drainOne(clientC.send) {
		t.Fatal("did not expect another user's connection to receive the message")
	}

	hub.SendToClient(clientC, msg)
	if !drainOne(clientC.send) {
		t.Fatal("expected direct connection send to deliver exactly once")
	}
	if drainOne(clientA.send) || drainOne(clientB.send) {
		t.Fatal("did not expect direct connection send to fan out")
	}
}

func TestP2PStreamCancelFansOutToPeerWithoutEchoingToSenderConnection(t *testing.T) {
	hub := NewHub(nil, nil)
	sender := &Client{uid: 7, send: make(chan []byte, 1)}
	senderSibling := &Client{uid: 7, send: make(chan []byte, 1)}
	bot := &Client{uid: 42, send: make(chan []byte, 1)}
	hub.addClient(sender)
	hub.addClient(senderSibling)
	hub.addClient(bot)

	hub.fanoutStreamEvent(7, "p2p_7_42", "stream_cancel", "", map[string]interface{}{
		"stream_id": "cancel-1",
		"control":   "interrupt",
	}, sender)

	if drainOne(sender.send) {
		t.Fatal("the originating connection must receive only its pub ack, not a duplicate cancel event")
	}

	for name, messages := range map[string]<-chan []byte{
		"sender sibling": senderSibling.send,
		"bot peer":       bot.send,
	} {
		var received ServerMessage
		decodeQueuedServerMessage(t, messages, &received)
		if received.Data == nil || received.Data.Type != "stream_cancel" {
			t.Fatalf("%s received %#v, want stream_cancel data", name, received.Data)
		}
		if received.Data.Metadata["stream_event"] != "cancel" || received.Data.Metadata["control"] != "interrupt" {
			t.Fatalf("%s cancel metadata = %#v", name, received.Data.Metadata)
		}
	}
}

func TestDeviceConnectorConnectionsDoNotReceiveUserMessagesOrSetPresence(t *testing.T) {
	hub := NewHub(nil, nil)
	connector := &Client{
		uid:             7,
		deviceConnector: &DeviceConnectorClaims{UID: 7, DeviceID: "alice-laptop"},
		send:            make(chan []byte, 1),
	}
	human := &Client{uid: 7, send: make(chan []byte, 1)}

	first, devices, online := hub.addClient(connector)
	if first || devices != 1 || online != 1 {
		t.Fatalf("connector add = (%v, %d, %d), want (false, 1, 1)", first, devices, online)
	}
	if hub.IsOnline(7) {
		t.Fatal("device connector alone must not make the user chat-online")
	}

	first, devices, online = hub.addClient(human)
	if !first || devices != 2 || online != 1 {
		t.Fatalf("human add after connector = (%v, %d, %d), want (true, 2, 1)", first, devices, online)
	}
	if !hub.IsOnline(7) {
		t.Fatal("human connection should make the user chat-online")
	}

	msg := &ServerMessage{Ctrl: &MsgServerCtrl{Code: 200, Text: "ok"}}
	hub.SendToUser(7, msg)
	if !drainOne(human.send) {
		t.Fatal("expected human connection to receive user message")
	}
	if drainOne(connector.send) {
		t.Fatal("device connector must not receive ordinary user messages")
	}

	removed, last, remaining, online := hub.removeClient(human)
	if !removed || !last || remaining != 1 || online != 1 {
		t.Fatalf("human remove with connector remaining = (%v, %v, %d, %d), want (true, true, 1, 1)", removed, last, remaining, online)
	}
	if hub.IsOnline(7) {
		t.Fatal("device connector remaining must not keep user chat-online")
	}
}

func TestDeviceConnectorRejectsMixedWebSocketEnvelope(t *testing.T) {
	hub := NewHub(nil, nil)
	connector := &Client{
		uid:  7,
		send: make(chan []byte, 1),
		deviceConnector: &DeviceConnectorClaims{
			UID:      7,
			DeviceID: "alice-laptop",
			Scopes:   []string{"device:ws", "device:register", "device:rpc_result"},
		},
	}

	hub.handleMessage(connector, &ClientMessage{
		Hi: &MsgClientHi{ID: "hi-1"},
		Pub: &MsgClientPub{
			ID:      "pub-1",
			Topic:   "p2p_7_42",
			Content: json.RawMessage(`"hello"`),
		},
	})

	var ack ServerMessage
	decodeQueuedServerMessage(t, connector.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 403 {
		t.Fatalf("mixed envelope ack = %#v, want 403", ack.Ctrl)
	}
}

func drainOne(ch <-chan []byte) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
