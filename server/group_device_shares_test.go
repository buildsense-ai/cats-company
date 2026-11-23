package server

import (
	"context"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type groupSharingTestStore struct {
	*identityMessageStore
	store.GroupDeviceShareStore
	share   store.GroupDeviceShare
	failure error
}

func (s *groupSharingTestStore) GetDeviceShareBinding(_ context.Context, _ int64, _ string) (*store.GroupDeviceShareBinding, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	return &store.GroupDeviceShareBinding{Epoch: 1, Active: true}, nil
}
func (s *groupSharingTestStore) GetMemberRole(groupID, uid int64) (string, error) {
	for _, member := range s.groupMembers {
		if member.GroupID == groupID && member.UserID == uid {
			return member.Role, nil
		}
	}
	return "", errors.New("not a member")
}
func (s *groupSharingTestStore) GetGroupDeviceShare(_ context.Context, id string) (*store.GroupDeviceShare, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	if id != s.share.ID {
		return nil, nil
	}
	copy := s.share
	return &copy, nil
}
func (s *groupSharingTestStore) ListGroupDeviceShares(_ context.Context, _ string, _ int64) ([]store.GroupDeviceShare, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	return []store.GroupDeviceShare{s.share}, nil
}

func TestGroupDeviceSharingRevocationAndProtocolFence(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	db := &groupSharingTestStore{identityMessageStore: &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "requester", AccountType: types.AccountHuman},
			8:  {ID: 8, Username: "owner", AccountType: types.AccountHuman},
			42: {ID: 42, Username: "bot", AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{{GroupID: 1, UserID: 7, Role: "member"}, {GroupID: 1, UserID: 8, Role: "member"}, {GroupID: 1, UserID: 42, Role: "member"}},
	}, share: store.GroupDeviceShare{DeviceEpoch: 1, ID: "share-b", OwnerUID: 8, GranteeUID: 7, TopicID: "grp_1", DeviceID: "same-device", Operations: []string{"read_file"}, Status: "active", Revision: 1}}
	hub := NewHub(db, nil)
	bot := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud", send: make(chan []byte, 8)}
	bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability, groupDeviceShareCapability})
	hub.addClient(bot)
	device, err := hub.userDevices.register(8, RegisterUserDeviceRequest{DeviceID: "same-device", RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file", "write_file"}})
	if err != nil {
		t.Fatal(err)
	}
	peer := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	peer.setRuntimeCapabilities([]string{groupDeviceShareCapability})
	hub.addClient(peer)
	hub.bindDeviceClient(8, device, peer)
	// Build a fresh group turn through the real metadata entry point. An own
	// single-device pre-pass must not silently choose before shared candidates
	// have been merged into the directory.
	own, err := hub.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "own-device", RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	ownPeer := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	hub.addClient(ownPeer)
	hub.bindDeviceClient(7, own, ownPeer)
	identity := hub.buildCatscoIdentityMetadata(7, 42, "grp_1", 1, "read my computer")
	initial, ok := identity["device_selection"].(*DeviceSelection)
	if !ok || initial.Status != DeviceSelectionNeedsSelection || initial.CandidateCount != 2 {
		t.Fatal("group silently selected before merging shared devices", initial)
	}
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	preference, err := hub.userDevices.selectionRecord(7, session)
	if err != nil || preference.DeviceID != "" {
		t.Fatal("fresh group persisted an implicit own-device preference", preference, err)
	}
	if _, err := hub.userDevices.confirmDeviceSelection(7, session, "same-device", "user_confirmation", preference.Revision, 8); err != nil {
		t.Fatal(err)
	}
	turn := hub.groupDeviceTurnContext(7, "grp_1", bot, nil, nil, xiaobaRuntimeV2EnabledForClient(bot))
	if len(turn.Grants) != 1 || turn.Grants[0].OwnerUserID != "usr8" || turn.Grants[0].ActorUserID != "usr7" || turn.Grants[0].ShareID != "share-b" {
		t.Fatalf("shared identity lost: %+v", turn)
	}
	grant := turn.Grants[0]
	if len(grant.Operations) != 1 || grant.Operations[0] != DeviceGrantReadFile {
		t.Fatal("share capability intersection failed")
	}
	request := &MsgThinToolRPC{GrantID: grant.GrantID, SessionKey: grant.SessionKey, ActorUserID: grant.ActorUserID, TargetOwnerUserID: grant.OwnerUserID, TargetDeviceID: grant.DeviceID, ToolName: "read_file"}
	if err := hub.authorizeOrdinaryThinToolRPC(bot, request, 8, device.DeviceID, "read_file"); err != nil {
		t.Fatal("valid shared request denied", err)
	}
	for _, mutate := range []func(*ScopedDeviceGrant){
		func(g *ScopedDeviceGrant) { g.ActorUserID = "usr8" },
		func(g *ScopedDeviceGrant) { g.OwnerUserID = "usr7" },
		func(g *ScopedDeviceGrant) { g.TopicID = "grp_2" },
		func(g *ScopedDeviceGrant) { g.AgentID = "" },
		func(g *ScopedDeviceGrant) { g.AgentID = "usr7" },
		func(g *ScopedDeviceGrant) { g.ShareRevision++ },
		func(g *ScopedDeviceGrant) { g.ShareID = "other-share" },
		func(g *ScopedDeviceGrant) { g.Operations = []DeviceGrantOperation{DeviceGrantWriteFile} },
	} {
		forged := grant
		mutate(&forged)
		if hub.groupDeviceGrantCurrent(forged) {
			t.Fatal("forged permission scope accepted", forged)
		}
	}
	if err := hub.authorizeOrdinaryThinToolRPC(bot, &MsgThinToolRPC{ToolName: "read_file"}, 8, device.DeviceID, "read_file"); err == nil {
		t.Fatal("new peer bypassed grants in compatibility mode")
	}
	peer.setRuntimeCapabilities(nil)
	if err := hub.authorizeOrdinaryThinToolRPC(bot, request, 8, device.DeviceID, "read_file"); err != nil {
		t.Fatal("owner-protocol receiver was rejected", err)
	}
	peer.setRuntimeCapabilities([]string{groupDeviceShareCapability})
	db.share.Status = "revoked"
	db.share.Revision++
	if _, ok := hub.userDevices.lookupGrant(grant.GrantID); ok {
		t.Fatal("revoked shared grant was returned")
	}
	if err := hub.authorizeOrdinaryThinToolRPC(bot, request, 8, device.DeviceID, "read_file"); err == nil {
		t.Fatal("revoked grant still executes")
	}
	db.share.Status = "active"
	db.share.Revision++
	if err := hub.authorizeOrdinaryThinToolRPC(bot, request, 8, device.DeviceID, "read_file"); err == nil {
		t.Fatal("new approval revived the old grant")
	}
	db.failure = errors.New("permission store unavailable")
	if err := hub.authorizeOrdinaryThinToolRPC(bot, request, 8, device.DeviceID, "read_file"); err == nil {
		t.Fatal("permission storage failure allowed shared execution")
	}
}
