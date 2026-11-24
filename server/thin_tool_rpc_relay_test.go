package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestThinToolRPCRelayQuotaIsolation(t *testing.T) {
	state := newSharedMemoryRuntimeState()
	origin := newHubWithState(nil, nil, state, "quota-origin", nil)
	target := newHubWithState(nil, nil, state, "quota-device", nil)
	newBot := func(uid int64) *Client {
		bot := &Client{uid: uid, accountType: types.AccountBot, send: make(chan []byte, 1)}
		origin.addClient(bot)
		origin.bindClientRuntimeRoute(bot)
		return bot
	}
	botA, botB := newBot(42), newBot(43)
	pcs := make([]*Client, 3)
	const owner = 7
	for i := range pcs {
		id := fmt.Sprintf("quota-pc-%d", i)
		device, err := target.userDevices.register(owner, RegisterUserDeviceRequest{DeviceID: id, BodyID: id, InstallationID: id, RuntimeRole: "desktop", Status: "online"})
		if err != nil {
			t.Fatal(err)
		}
		pc := &Client{uid: owner, accountType: types.AccountHuman, send: make(chan []byte, 1)}
		target.addClient(pc)
		target.bindDeviceClient(owner, device, pc)
		pcs[i] = pc
	}
	dispatch := func(bot, pc *Client, id string) bool {
		now := time.Now()
		relay := &thinToolRPCRelay{
			RequesterRoute: origin.clientRoute(bot), TargetOwnerUID: owner, TargetDeviceID: pc.deviceID,
			ToolName: "read_file", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
		}
		request := &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: id, TargetDeviceID: pc.deviceID, ToolName: relay.ToolName}
		target.receiveThinToolRPCFromRuntime(target.clientRoute(pc), request, relay)
		select {
		case <-pc.send:
			return true
		default:
			return false
		}
	}
	for i := 0; i < maxThinToolRPCPendingPerRequester; i++ {
		pc := pcs[i%2]
		if !dispatch(botA, pc, fmt.Sprintf("quota-a-%d", i)) {
			t.Fatalf("Bot A rejected below its quota on request %d", i)
		}
	}
	if !dispatch(botB, pcs[2], "quota-b-unrelated") {
		t.Fatal("Bot B was blocked by Bot A's full quota")
	}
	if dispatch(botA, pcs[2], "quota-a-overflow") {
		t.Fatal("Bot A bypassed its own quota by choosing a different device")
	}
	if dispatch(botB, pcs[0], "quota-b-device-full") {
		t.Fatal("device quota did not account for other requesters")
	}
}

func TestThinToolRPCRelayBindsResultToOriginalRoutes(t *testing.T) {
	for _, changed := range []string{"none", "source", "requester", "previous_request"} {
		t.Run(changed, func(t *testing.T) {
			h, bot, _ := groupShareCompatibilityFixture(t)
			pc := h.getDeviceClient(8, "shared-pc")
			requester, target := h.clientRoute(bot), h.clientRoute(pc)
			pending := thinToolRPCPending{requestID: "relay-result", requester: bot, requesterRoute: requester, targetRoute: target, targetOwnerUID: 8, targetDeviceID: "shared-pc", toolName: "read_file", createdAt: time.Now(), expiresAt: time.Now().Add(time.Minute), sharingScope: &MsgThinToolRPC{Type: "request", RequestID: "relay-result", ActorUserID: "usr7", TargetOwnerUserID: "usr8", SessionKey: "original-actor-session", ShareID: "original-share"}}
			if rejection := h.thinToolRPC.add(pending); rejection != nil {
				t.Fatalf("fixture pending rejected: %+v", rejection)
			}
			source := target
			if changed == "source" {
				source.ConnectionID = "replacement-connection"
			}
			if changed == "requester" {
				requester = h.clientRoute(h.getDeviceClient(7, "own-pc"))
			}
			result := &MsgThinToolRPC{Type: "result", RequestID: pending.requestID, ActorUserID: "usr8", Result: map[string]any{"ok": true}}
			proof := &thinToolRPCRelay{ResultSource: source}
			if changed == "previous_request" {
				result.Result = nil
				result.Error = &MsgDeviceRPCError{Code: thinToolRPCBusyCode}
				proof.CreatedAt = pending.createdAt.Add(-time.Second)
				proof.ExpiresAt = pending.expiresAt.Add(-time.Second)
			}
			ok := h.receiveThinToolRPCFromRuntime(requester, result, proof)
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
