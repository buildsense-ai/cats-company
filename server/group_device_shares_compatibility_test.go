package server

import (
	"errors"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
	"testing"
)

func groupShareCompatibilityFixture(t *testing.T) (*Hub, *Client, *groupSharingTestStore) {
	t.Helper()
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	db := &groupSharingTestStore{identityMessageStore: &identityMessageStore{
		owners:       map[int64]int64{42: 7},
		users:        map[int64]*types.User{7: {ID: 7, Username: "actor", AccountType: types.AccountHuman}, 8: {ID: 8, Username: "owner", AccountType: types.AccountHuman}, 42: {ID: 42, Username: "bot", AccountType: types.AccountBot}},
		groupMembers: []*types.GroupMember{{GroupID: 1, UserID: 7, Role: "member"}, {GroupID: 1, UserID: 8, Role: "member"}, {GroupID: 1, UserID: 42, Role: "member"}},
	}, share: store.GroupDeviceShare{DeviceEpoch: 1, ID: "review-share", OwnerUID: 8, GranteeUID: 7, TopicID: "grp_1", DeviceID: "shared-pc", Operations: []string{"read_file"}, Status: "active", Revision: 1}}
	h := NewHub(db, nil)
	bot := &Client{hub: h, uid: 42, accountType: types.AccountBot, bodyID: "cloud", connectionID: "review-cloud", send: make(chan []byte, 16)}
	bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability, groupDeviceShareCapability})
	h.addClient(bot)
	if _, e := h.bodyLeases.acquire(42, "cloud", bot.connectionID); e != nil {
		t.Fatal(e)
	}
	for _, owner := range []int64{7, 8} {
		id := "own-pc"
		if owner == 8 {
			id = "shared-pc"
		}
		d, e := h.userDevices.register(owner, RegisterUserDeviceRequest{DeviceID: id, BodyID: id, InstallationID: id, RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file"}})
		if e != nil {
			t.Fatal(e)
		}
		peer := &Client{uid: owner, accountType: types.AccountHuman, connectionID: id, send: make(chan []byte, 16)}
		peer.setRuntimeCapabilities([]string{groupDeviceShareCapability})
		h.addClient(peer)
		h.bindDeviceClient(owner, d, peer)
	}
	return h, bot, db
}

func TestOwnDeviceUnaffectedBySharingStoreFailure(t *testing.T) {
	h, bot, db := groupShareCompatibilityFixture(t)
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	if _, e := h.userDevices.confirmDeviceSelection(7, session, "own-pc", "test", "", 7); e != nil {
		t.Fatal(e)
	}
	healthy := h.buildCatscoIdentityMetadata(7, 42, "grp_1", 1, "read own pc")
	if grants, _ := healthy["device_grants"].([]ScopedDeviceGrant); len(grants) != 1 {
		t.Fatal("healthy fixture missing own grant")
	}
	db.failure = errors.New("only sharing permission query is unavailable")
	after := h.buildCatscoIdentityMetadata(7, 42, "grp_1", 2, "read own pc")
	grants, _ := after["device_grants"].([]ScopedDeviceGrant)
	selection, _ := after["device_selection"].(*DeviceSelection)
	if len(grants) != 1 || selection == nil || selection.Status != DeviceSelectionSelected {
		t.Fatalf("own-device regression: grants=%d selection=%+v bot=%d", len(grants), selection, bot.uid)
	}
}

func TestGroupDeviceContextHonorsExplicitTargetDecision(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			name := "sharing_store_healthy"
			if unavailable {
				name = "sharing_store_unavailable"
			}
			if enabled {
				name += "/explicit_enabled"
			} else {
				name += "/explicit_disabled"
			}
			t.Run(name, func(t *testing.T) {
				h, bot, db := groupShareCompatibilityFixture(t)
				session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
				if _, err := h.userDevices.confirmDeviceSelection(7, session, "own-pc", "test", "", 7); err != nil {
					t.Fatal(err)
				}
				if unavailable {
					db.failure = errors.New("sharing query unavailable")
				}
				own, missing := h.classifyUserDevices(7, h.userDevices.list(7))
				turn := h.groupDeviceTurnContext(7, "grp_1", bot, own, missing, enabled)
				if len(turn.Grants) != 1 || turn.Grants[0].OwnerUserID != "usr7" {
					t.Fatal("explicit target decision changed the own-device default grant")
				}
				want := 0
				if enabled {
					want = 2
					if unavailable {
						want = 1
					}
				}
				if len(turn.ExplicitGrants) != want {
					t.Fatalf("explicit grants = %d, want %d", len(turn.ExplicitGrants), want)
				}
			})
		}
	}
}

func TestUnassignedOwnDeviceRemainsExplicitDuringSharingStoreFailure(t *testing.T) {
	h, bot, db := groupShareCompatibilityFixture(t)
	db.failure = errors.New("sharing query unavailable")
	devices, unavailable := h.classifyUserDevices(7, h.userDevices.list(7))
	ctx := h.groupDeviceTurnContext(7, "grp_1", bot, devices, unavailable, xiaobaRuntimeV2EnabledForClient(bot))
	if ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil ||
		len(ctx.Grants) != 0 || len(ctx.ExplicitGrants) != 1 || ctx.ExplicitGrants[0].OwnerUserID != "usr7" {
		t.Fatal("sharing-store outage changed the empty default or prevented a direct own-device request")
	}
}

func TestActiveSharedGrantCanRenew(t *testing.T) {
	h, bot, _ := groupShareCompatibilityFixture(t)
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	if _, e := h.userDevices.confirmDeviceSelection(7, session, "shared-pc", "test", "", 8); e != nil {
		t.Fatal(e)
	}
	ctx := h.groupDeviceTurnContext(7, "grp_1", bot, nil, nil, xiaobaRuntimeV2EnabledForClient(bot))
	if len(ctx.Grants) != 1 {
		t.Fatal("fixture missing shared grant")
	}
	grant := ctx.Grants[0]
	if !h.groupDeviceGrantCurrent(grant) {
		t.Fatal("fixture permission is not active")
	}
	if _, ok := h.userDevices.lookupGrant(grant.GrantID); !ok {
		t.Fatal("fixture grant not persisted")
	}
	next, e := h.renewDeviceGrant(bot, &MsgDeviceGrantRenewal{Type: "request", RequestID: "review-renew", GrantID: grant.GrantID})
	if e != nil {
		t.Fatalf("active group permission cannot renew: %v", e)
	}
	if next.OwnerUserID != grant.OwnerUserID || next.ActorUserID != grant.ActorUserID || next.ShareID != grant.ShareID || next.ShareRevision != grant.ShareRevision {
		t.Fatal("renewal changed delegated authority")
	}
	db := h.db.(*groupSharingTestStore)
	db.share.Status = "revoked"
	db.share.Revision++
	if _, e := h.renewDeviceGrant(bot, &MsgDeviceGrantRenewal{Type: "request", RequestID: "review-revoked", GrantID: next.GrantID}); e == nil {
		t.Fatal("revoked sharing permission renewed")
	}

}

func TestSharingClaimsCannotUseLegacyNoGrantRoute(t *testing.T) {
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "")
	h, bot, _ := groupShareCompatibilityFixture(t)
	bot.setRuntimeCapabilities(nil)
	legacy := MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: "legacy-own", ToolName: "read_file", TargetOwnerUserID: "usr7", TargetDeviceID: "own-pc"}
	if err := h.authorizeOrdinaryThinToolRPC(bot, &legacy, 7, "own-pc", "read_file"); err != nil {
		t.Fatal("ordinary legacy own-device route regressed", err)
	}
	for _, claims := range []MsgThinToolRPC{
		{IdentitySource: groupDeviceShareSource}, {ShareID: "untrusted-share"}, {ShareRevision: 1},
		{TopicID: "grp_1", TopicType: "group", AgentID: "usr42"},
	} {
		request := legacy
		request.IdentitySource, request.ShareID, request.ShareRevision = claims.IdentitySource, claims.ShareID, claims.ShareRevision
		request.TopicID, request.TopicType, request.AgentID = claims.TopicID, claims.TopicType, claims.AgentID
		if err := h.authorizeOrdinaryThinToolRPC(bot, &request, 7, "own-pc", "read_file"); err == nil {
			t.Fatal("unverified sharing provenance used the legacy compatibility route")
		}
	}
}

func TestLegacyBotCannotTargetAnotherOwnerWithoutGrant(t *testing.T) {
	t.Setenv("CATSCO_DEVICE_GRANT_ENFORCEMENT", "")
	h, bot, db := groupShareCompatibilityFixture(t)
	bot.setRuntimeCapabilities(nil)
	for _, op := range []string{"read_file", "execute_shell"} {
		if _, err := h.userDevices.register(8, RegisterUserDeviceRequest{DeviceID: "shared-pc", RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file", "execute_shell"}}); err != nil {
			t.Fatal(err)
		}
		msg := &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: "legacy-cross-owner-" + op, ToolName: op, TargetOwnerUserID: "usr8", TargetDeviceID: "shared-pc"}
		if err := h.authorizeOrdinaryThinToolRPC(bot, msg, 8, "shared-pc", op); thinToolRPCAuthorizationErrorCode(err) != "device_authorization_required" {
			t.Fatalf("legacy cross-owner %s accepted: %v", op, err)
		}
	}
	delete(db.owners, bot.uid)
	if err := h.authorizeOrdinaryThinToolRPC(bot, &MsgThinToolRPC{}, 7, "own-pc", "read_file"); err == nil {
		t.Fatal("unknown Bot ownership must fail closed")
	}
}

func TestOnlySharedDeviceNeedsExplicitDefaultConfirmation(t *testing.T) {
	h, bot, _ := groupShareCompatibilityFixture(t)
	// Hide the owner's own device so the public picker has just one shared candidate.
	h.userDevices.unregister(7, "own-pc")
	snapshot := selectionSnapshot(t, NewDeviceHandler(h.db, h), 7, "grp_1", "usr42")
	if snapshot["status"] != "unspecified" || snapshot["selected_device_id"] != "" {
		t.Fatalf("picker suggested an implicit shared default: %+v", snapshot)
	}
	ctx := h.groupDeviceTurnContext(7, "grp_1", bot, nil, nil, xiaobaRuntimeV2EnabledForClient(bot))
	if ctx.Selection == nil || ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil {
		t.Fatalf("shared device became implicit default: %+v", ctx.Selection)
	}
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	pref, err := h.userDevices.selectionRecord(7, session)
	if err != nil || pref.DeviceID != "" {
		t.Fatalf("shared default persisted without confirmation: %+v %v", pref, err)
	}
	if _, err := h.userDevices.confirmDeviceSelection(7, session, "shared-pc", "user_confirmation", pref.Revision, 8); err != nil {
		t.Fatal(err)
	}
	ctx = h.groupDeviceTurnContext(7, "grp_1", bot, nil, nil, xiaobaRuntimeV2EnabledForClient(bot))
	if ctx.Selection.Status != DeviceSelectionSelected || ctx.Selection.OwnerUserID != "usr8" || len(ctx.Grants) == 0 {
		t.Fatalf("confirmed shared default unavailable: %+v", ctx)
	}
}
