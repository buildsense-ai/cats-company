package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/openchat/openchat/server/store"
)

func permanentSharedTurn(t *testing.T, h *Hub, bot *Client) ScopedDeviceGrant {
	t.Helper()
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	if _, err := h.userDevices.confirmDeviceSelection(7, session, "shared-pc", "user_confirmation", "", 8); err != nil {
		t.Fatal(err)
	}
	ctx := h.groupDeviceTurnContext(7, "grp_1", bot, nil, nil)
	if len(ctx.Grants) != 1 {
		t.Fatalf("missing shared route: %+v", ctx)
	}
	return ctx.Grants[0]
}

func TestGroupSharedGrantHasNoTimeLeaseAndRevokesImmediately(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			h, bot, db := groupShareCompatibilityFixture(t)
			h.userDevices.withSelectionStore(&durableSelectionTestStore{})
			now := time.Now().Truncate(time.Millisecond)
			h.userDevices.now = func() time.Time { return now }
			var mr *miniredis.Miniredis
			var auth *RedisDeviceAuthorizationStore
			if backend == "redis" {
				mr = miniredis.RunT(t)
				auth = authorizationStoreForTest(t, "redis://"+mr.Addr())
				h.deviceAuthorization = auth
				h.userDevices.withAuthorizationStore(auth)
			}
			grant := permanentSharedTurn(t, h, bot)
			if !grant.Permanent || grant.ExpiresAt != permanentDeviceGrantExpiresAt || grant.RenewableUntil != 0 {
				t.Fatalf("sharing still uses a time lease: %+v", grant)
			}
			for _, advance := range []time.Duration{11 * time.Minute, 25 * time.Hour, 365 * 24 * time.Hour} {
				now = now.Add(advance)
				if mr != nil {
					mr.FastForward(advance)
				}
				h.userDevices.touch(8, "shared-pc")
				req := &MsgThinToolRPC{GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, TargetDeviceID: grant.DeviceID, TargetOwnerUserID: grant.OwnerUserID, ToolName: "read_file"}
				if err := h.authorizeOrdinaryThinToolRPC(bot, req, 8, "shared-pc", "read_file"); err != nil {
					t.Fatalf("elapsed %s without renewal: %v", advance, err)
				}
				if req.ExpiresAt <= unixMillis(now) {
					t.Fatal("active permission produced an expired request")
				}
			}
			db.share.Status = "revoked"
			db.share.Revision++
			if _, ok := h.userDevices.lookupGrant(grant.GrantID); ok {
				t.Fatal("revoked permanent permission remained usable")
			}
			h.revokeGroupShareGrants(grant.ShareID)
			if auth != nil && mr.Exists(auth.state.deviceGrantKey(grant.GrantID)) {
				t.Fatal("revocation left a permanent Redis grant")
			}
		})
	}
}

type changedGroupShareBindingStore struct{ *groupSharingTestStore }

func (s *changedGroupShareBindingStore) GetDeviceShareBinding(context.Context, int64, string) (*store.GroupDeviceShareBinding, error) {
	return &store.GroupDeviceShareBinding{Epoch: 2, Active: true}, nil
}

func TestGroupPermanentPermissionStillChecksCurrentAuthority(t *testing.T) {
	for _, change := range []string{"actor leaves", "owner leaves", "bot leaves", "actor disabled", "owner disabled", "bot disabled", "binding changed", "operation removed", "selection changed", "storage unavailable"} {
		t.Run(change, func(t *testing.T) {
			h, bot, db := groupShareCompatibilityFixture(t)
			grant := permanentSharedTurn(t, h, bot)
			switch change {
			case "actor leaves", "owner leaves", "bot leaves":
				remove := int64(7)
				if change == "owner leaves" {
					remove = 8
				}
				if change == "bot leaves" {
					remove = 42
				}
				members := db.groupMembers[:0]
				for _, m := range db.groupMembers {
					if m.UserID != remove {
						members = append(members, m)
					}
				}
				db.groupMembers = members
			case "actor disabled":
				db.users[7].State = 1
			case "owner disabled":
				db.users[8].State = 1
			case "bot disabled":
				db.users[42].State = 1
			case "binding changed":
				h.db = &changedGroupShareBindingStore{db}
			case "operation removed":
				db.share.Operations = []string{"glob"}
			case "selection changed":
				if _, err := h.userDevices.confirmDeviceSelection(7, grant.SessionKey, "", deviceSelectionUnspecifiedSource, grant.SelectionRevision, 7); err != nil {
					t.Fatal(err)
				}
			case "storage unavailable":
				db.failure = context.DeadlineExceeded
			}
			if _, ok := h.userDevices.lookupGrant(grant.GrantID); ok {
				t.Fatalf("%s did not invalidate permission", change)
			}
		})
	}
}

func TestSharedRPCUsesOwnerProtocolAndRestoresActualCaller(t *testing.T) {
	for _, transport := range []string{"device", "thin"} {
		t.Run(transport, func(t *testing.T) {
			h, bot, _ := groupShareCompatibilityFixture(t)
			pc := h.getDeviceClient(8, "shared-pc")
			pc.setRuntimeCapabilities(nil) // Existing Connector, unaware of group sharing.
			grant := permanentSharedTurn(t, h, bot)
			requestID := "owner-protocol-" + transport
			if transport == "device" {
				h.handleMessage(bot, &ClientMessage{DeviceRPC: &MsgDeviceRPC{Type: "request", RequestID: requestID, GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, DeviceID: grant.DeviceID, Operation: "read_file", ToolName: "read_file"}})
				var wire ServerMessage
				decodeQueuedServerMessage(t, pc.send, &wire)
				req := wire.DeviceRPC
				if req == nil || req.ActorUserID != "usr8" || req.OwnerUserID != "usr8" || req.IdentitySource == groupDeviceShareSource || req.ShareID != "" || req.ShareRevision != 0 || req.SessionKey != buildCatsCoSessionKey("grp_1", "group", "usr42", 8) {
					t.Fatalf("not an ordinary owner request: %+v", req)
				}
				if req.ExpiresAt <= time.Now().UnixMilli() || req.ExpiresAt > time.Now().Add(defaultDeviceRPCTTL).UnixMilli() {
					t.Fatal("Device RPC lost its bounded transport deadline")
				}
				pending, ok := h.deviceRPC.get(requestID)
				if !ok || pending.actorUserID != "usr7" || pending.sessionKey != grant.SessionKey {
					t.Fatal("server lost actual caller")
				}
				reply := *req
				reply.Type = "result"
				reply.Result = map[string]any{"ok": true, "content": "from pc"}
				h.handleMessage(pc, &ClientMessage{DeviceRPC: &reply})
				found := false
				for len(bot.send) > 0 {
					var response ServerMessage
					decodeQueuedServerMessage(t, bot.send, &response)
					if response.DeviceRPC != nil {
						found = true
						if response.DeviceRPC.ActorUserID != "usr7" || response.DeviceRPC.OwnerUserID != "usr8" || response.DeviceRPC.SessionKey != grant.SessionKey {
							t.Fatal("owner execution leaked into caller identity", response.DeviceRPC)
						}
					}
				}
				if !found {
					t.Fatal("missing result")
				}
			} else {
				h.handleMessage(bot, &ClientMessage{ThinToolRPC: &MsgThinToolRPC{Type: "request", RequestID: requestID, GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, TargetOwnerUserID: grant.OwnerUserID, TargetDeviceID: grant.DeviceID, ToolName: "read_file", Payload: map[string]any{"args": map[string]any{"path": "test-only"}}}})
				var wire ServerMessage
				decodeQueuedServerMessage(t, pc.send, &wire)
				req := wire.ThinToolRPC
				if req == nil || req.ActorUserID != "usr8" || req.TargetOwnerUserID != "usr8" || req.IdentitySource == groupDeviceShareSource || req.ShareID != "" || req.ShareRevision != 0 || req.SessionKey != buildCatsCoSessionKey("grp_1", "group", "usr42", 8) {
					t.Fatalf("not an ordinary owner thin request: %+v", req)
				}
				if req.ExpiresAt <= time.Now().UnixMilli() || req.ExpiresAt > time.Now().Add(defaultThinToolRPCTTL).UnixMilli() {
					t.Fatal("Thin RPC lost its bounded transport deadline")
				}
				pending, ok := h.thinToolRPC.get(requestID)
				if !ok || pending.sharingScope == nil || pending.sharingScope.ActorUserID != "usr7" || pending.sharingScope.Payload != nil {
					t.Fatal("server lost caller or stored tool payload")
				}
				reply := *req
				reply.Type = "result"
				reply.Result = map[string]any{"ok": true, "content": "from pc"}
				h.handleMessage(pc, &ClientMessage{ThinToolRPC: &reply})
				found := false
				for len(bot.send) > 0 {
					var response ServerMessage
					decodeQueuedServerMessage(t, bot.send, &response)
					if response.ThinToolRPC != nil {
						found = true
						if response.ThinToolRPC.ActorUserID != "usr7" || response.ThinToolRPC.SessionKey != grant.SessionKey || response.ThinToolRPC.ShareID != grant.ShareID {
							t.Fatal("result lost real caller", response.ThinToolRPC)
						}
					}
				}
				if !found {
					t.Fatal("missing thin result")
				}
			}
			events := h.listDeviceAudit(8, 20)
			found := false
			for _, event := range events {
				if event.Phase == "rpc_forwarded" {
					found = true
					if event.ActorUserID != "usr7" || event.OwnerUserID != "usr8" || event.SessionKey != grant.SessionKey {
						t.Fatalf("wrong audit attribution: %+v", event)
					}
				}
			}
			if !found {
				t.Fatal("missing server execution audit")
			}
		})
	}
}

func TestPermanentSharedRedisIndexSurvivesLegacyExpiryAndCleansUp(t *testing.T) {
	for _, revoke := range []string{"share", "selection", "device", "single"} {
		t.Run(revoke, func(t *testing.T) {
			mr := miniredis.RunT(t)
			auth := authorizationStoreForTest(t, "redis://"+mr.Addr())
			h, _, _ := groupShareCompatibilityFixture(t)
			h.deviceAuthorization = auth
			now := time.Now().UnixMilli()
			legacy := ScopedDeviceGrant{Kind: "user_device_grant", Source: "catscompany", Status: "active", IdentityTrust: "server_canonical", IdentitySource: groupDeviceShareSource, GrantID: "old-shared", ShareID: "share-index", ShareRevision: 1, ActorUserID: "usr7", OwnerUserID: "usr8", DeviceID: "pc", SessionKey: "session", CreatedAt: now, ExpiresAt: now + (10 * time.Minute).Milliseconds()}
			auth.rememberDeviceGrants([]ScopedDeviceGrant{legacy}, now)
			permanent := legacy
			permanent.GrantID = "new-shared"
			permanent.Permanent = true
			permanent.ExpiresAt = permanentDeviceGrantExpiresAt
			auth.rememberDeviceGrants([]ScopedDeviceGrant{permanent}, now)
			// An older client may still append a bounded grant after the new grant.
			auth.rememberDeviceGrants([]ScopedDeviceGrant{legacy}, now)
			index := auth.state.key("device_grant_share", permanent.ShareID)
			if mr.TTL(index) != 0 || mr.TTL(auth.state.deviceGrantKey(permanent.GrantID)) != 0 {
				t.Fatal("permanent grant/index received a TTL")
			}
			// Explicitly remove the bounded record; expired legacy IDs must not mask
			// whether permanent-index cleanup itself works.
			auth.revokeDeviceGrant(legacy)
			mr.FastForward(365 * 24 * time.Hour)
			if !mr.Exists(index) {
				t.Fatal("legacy TTL deleted permanent share index")
			}
			switch revoke {
			case "share":
				h.revokeGroupShareGrants(permanent.ShareID)
			case "selection":
				auth.revokeDeviceGrantsForSelection(7, "session")
			case "device":
				auth.revokeDeviceGrantsForDevice(8, "pc")
			case "single":
				auth.revokeDeviceGrant(permanent)
			}
			if mr.Exists(index) || mr.Exists(auth.state.deviceGrantKey(permanent.GrantID)) {
				t.Fatal("revocation leaked permanent grant/index")
			}
		})
	}
}

// Observe the authoritative permission store rather than a particular helper
// name. A new ordinary device transport must be added to this contract table.
type observedGroupPermissionStore struct {
	*groupSharingTestStore
	reads          int
	changedBinding bool
}

func (s *observedGroupPermissionStore) GetGroupDeviceShare(ctx context.Context, id string) (*store.GroupDeviceShare, error) {
	s.reads++
	return s.groupSharingTestStore.GetGroupDeviceShare(ctx, id)
}
func (s *observedGroupPermissionStore) GetDeviceShareBinding(ctx context.Context, owner int64, device string) (*store.GroupDeviceShareBinding, error) {
	if s.changedBinding {
		return &store.GroupDeviceShareBinding{Epoch: 2, Active: true}, nil
	}
	return s.groupSharingTestStore.GetDeviceShareBinding(ctx, owner, device)
}

func TestGroupSharedRPCRevalidatesPermanentGrantBeforeDispatch(t *testing.T) {
	for _, transport := range []string{"device", "thin"} {
		t.Run(transport, func(t *testing.T) {
			for _, backend := range []string{"memory", "redis"} {
				t.Run(backend, func(t *testing.T) {
					for _, authority := range []string{"active", "revoked", "revision changed", "actor leaves", "binding changed", "operation removed", "storage unavailable"} {
						t.Run(authority, func(t *testing.T) {
							h, bot, db := groupShareCompatibilityFixture(t)
							pc := h.getDeviceClient(8, "shared-pc")
							pc.setRuntimeCapabilities(nil)
							var auth *RedisDeviceAuthorizationStore
							if backend == "redis" {
								mr := miniredis.RunT(t)
								auth = authorizationStoreForTest(t, "redis://"+mr.Addr())
								h.deviceAuthorization = auth
								h.userDevices.withAuthorizationStore(auth)
							}
							grant := permanentSharedTurn(t, h, bot)
							observed := &observedGroupPermissionStore{groupSharingTestStore: db}
							h.db = observed
							// Do not run any invalidation/cleanup hook. The stale grant remains in
							// the cache exactly as it could remain in a Bot's pending context.
							switch authority {
							case "revoked":
								db.share.Status = "revoked"
							case "revision changed":
								db.share.Revision++
							case "actor leaves":
								db.groupMembers = db.groupMembers[1:]
							case "binding changed":
								observed.changedBinding = true
							case "operation removed":
								db.share.Operations = []string{"glob"}
							case "storage unavailable":
								db.failure = context.DeadlineExceeded
							}
							var cached ScopedDeviceGrant
							var exists bool
							if auth != nil {
								cached, exists = auth.lookupDeviceGrant(grant.GrantID, time.Now().UnixMilli())
							} else {
								h.userDevices.mu.RLock()
								cached, exists = h.userDevices.grants[grant.GrantID]
								h.userDevices.mu.RUnlock()
							}
							if !exists || !cached.Permanent || cached.Status != "active" {
								t.Fatal("fixture must retain an active permanent cached grant")
							}
							if observed.reads != 0 {
								t.Fatal("fixture validated permission before exercising dispatch")
							}
							requestID := "revalidate-cached-" + transport
							if transport == "device" {
								h.handleMessage(bot, &ClientMessage{DeviceRPC: &MsgDeviceRPC{Type: "request", RequestID: requestID, GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, DeviceID: grant.DeviceID, Operation: "read_file", ToolName: "read_file"}})
							} else {
								h.handleMessage(bot, &ClientMessage{ThinToolRPC: &MsgThinToolRPC{Type: "request", RequestID: requestID, GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, TargetOwnerUserID: grant.OwnerUserID, TargetDeviceID: grant.DeviceID, ToolName: "read_file"}})
							}
							if observed.reads == 0 {
								t.Fatal("RPC dispatched/rejected without consulting the current permission store")
							}
							if authority == "active" {
								var forwarded ServerMessage
								decodeQueuedServerMessage(t, pc.send, &forwarded)
								if transport == "device" && (forwarded.DeviceRPC == nil || forwarded.DeviceRPC.ActorUserID != "usr8") {
									t.Fatal("valid control did not reach owner RPC")
								}
								if transport == "thin" && (forwarded.ThinToolRPC == nil || forwarded.ThinToolRPC.ActorUserID != "usr8") {
									t.Fatal("valid control did not reach owner Thin RPC")
								}
								return
							}
							if len(pc.send) != 0 {
								t.Fatal("stale permanent grant reached the device")
							}
							if _, ok := h.deviceRPC.get(requestID); ok {
								t.Fatal("rejected device RPC retained a pending operation")
							}
							if _, ok := h.thinToolRPC.get(requestID); ok {
								t.Fatal("rejected Thin RPC retained a pending operation")
							}
							rejected := false
							for len(bot.send) > 0 {
								var response ServerMessage
								decodeQueuedServerMessage(t, bot.send, &response)
								if response.Ctrl != nil && response.Ctrl.Code >= 400 {
									rejected = true
								}
								if response.ThinToolRPC != nil && response.ThinToolRPC.Error != nil {
									rejected = true
								}
							}
							if !rejected {
								t.Fatal("caller did not receive a denial")
							}
						})
					}
				})
			}
		})
	}
}

func TestLiveDeviceGrantSnapshotExplicitlyClearsAfterRevocation(t *testing.T) {
	h, bot, db := groupShareCompatibilityFixture(t)
	h.userDevices.unregister(7, "own-pc")
	permanentSharedTurn(t, h, bot)
	before := h.buildCatscoIdentityMetadata(7, 42, "grp_1", 1, "read shared")
	if grants, ok := before["device_grants"].([]ScopedDeviceGrant); !ok || len(grants) != 1 {
		t.Fatal("fixture missing live grant")
	}
	db.share.Status = "revoked"
	db.share.Revision++
	after := h.buildCatscoIdentityMetadata(7, 42, "grp_1", 2, "permissions changed")
	data, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	grants, ok := wire["device_grants"].([]any)
	if !ok || len(grants) != 0 {
		t.Fatalf("revoked live message must explicitly encode []: %s", data)
	}
	envelope, ok := wire["device_target_authorizations"].(map[string]any)
	if !ok {
		t.Fatal("missing complete explicit-device snapshot")
	}
	explicit, ok := envelope["grants"].([]any)
	if !ok || len(explicit) != 0 {
		t.Fatalf("explicit snapshot must also clear: %s", data)
	}
}
