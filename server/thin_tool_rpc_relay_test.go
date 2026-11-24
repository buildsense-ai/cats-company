package server

import (
	"testing"
	"time"
)

func TestThinToolRPCRelayBindsResultToOriginalRoutes(t *testing.T) {
	for _, changed := range []string{"none", "source", "requester"} {
		t.Run(changed, func(t *testing.T) {
			h, bot, _ := groupShareCompatibilityFixture(t)
			pc := h.getDeviceClient(8, "shared-pc")
			requester, target := h.clientRoute(bot), h.clientRoute(pc)
			pending := thinToolRPCPending{requestID: "relay-result", requester: bot, requesterRoute: requester, targetRoute: target, targetOwnerUID: 8, targetDeviceID: "shared-pc", toolName: "read_file", createdAt: time.Now(), expiresAt: time.Now().Add(time.Minute), sharingScope: &MsgThinToolRPC{Type: "request", RequestID: "relay-result", ActorUserID: "usr7", TargetOwnerUserID: "usr8", SessionKey: "original-actor-session", ShareID: "original-share"}}
			if !h.thinToolRPC.add(pending) {
				t.Fatal("fixture pending rejected")
			}
			source := target
			if changed == "source" {
				source.ConnectionID = "replacement-connection"
			}
			if changed == "requester" {
				requester = h.clientRoute(h.getDeviceClient(7, "own-pc"))
			}
			ok := h.receiveThinToolRPCFromRuntime(requester, &MsgThinToolRPC{Type: "result", RequestID: pending.requestID, ActorUserID: "usr8", Result: map[string]any{"ok": true}}, &thinToolRPCRelay{ResultSource: source})
			_, stillPending := h.thinToolRPC.get(pending.requestID)
			if changed != "none" {
				if ok || !stillPending || len(bot.send) > 0 {
					t.Fatal("wrong result route consumed or delivered the original pending request")
				}
				return
			}
			if !ok || stillPending {
				t.Fatal("valid result did not clear its original pending request")
			}
			var message ServerMessage
			decodeQueuedServerMessage(t, bot.send, &message)
			if message.ThinToolRPC == nil || message.ThinToolRPC.ActorUserID != "usr7" || message.ThinToolRPC.SessionKey != "original-actor-session" || message.ThinToolRPC.Payload != nil {
				t.Fatal("result lost actor identity or retained a tool payload")
			}
			if h.receiveThinToolRPCFromRuntime(requester, message.ThinToolRPC, &thinToolRPCRelay{ResultSource: source}) {
				t.Fatal("duplicate result delivered twice")
			}
		})
	}
}

func TestThinToolRPCRelayRejectsExpiredAndMismatchedRequests(t *testing.T) {
	for _, changed := range []string{"expired", "device", "requester"} {
		t.Run(changed, func(t *testing.T) {
			h, bot, _ := groupShareCompatibilityFixture(t)
			pc := h.getDeviceClient(8, "shared-pc")
			relay := &thinToolRPCRelay{RequesterRoute: h.clientRoute(bot), TargetOwnerUID: 8, TargetDeviceID: "shared-pc", ToolName: "read_file", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			switch changed {
			case "expired":
				relay.ExpiresAt = time.Now().Add(-time.Second)
			case "device":
				relay.TargetDeviceID = "other-device"
			case "requester":
				relay.RequesterRoute.ConnectionID = "gone-requester"
			}
			if h.receiveThinToolRPCFromRuntime(h.clientRoute(pc), &MsgThinToolRPC{Type: "request", RequestID: "invalid-relay"}, relay) {
				t.Fatal("invalid relay request delivered")
			}
			if _, ok := h.thinToolRPC.get("invalid-relay"); ok || len(pc.send) > 0 {
				t.Fatal("invalid relay retained pending state or reached device")
			}
		})
	}
}
