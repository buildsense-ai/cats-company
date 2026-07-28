package server

import (
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type groupStreamCancelStore struct {
	store.Store
	members []*types.GroupMember
}

func (s *groupStreamCancelStore) IsChannelManagedGroup(int64) (bool, error) {
	return false, nil
}

func (s *groupStreamCancelStore) IsGroupMember(_ int64, userID int64) (bool, error) {
	for _, member := range s.members {
		if member != nil && member.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

func (s *groupStreamCancelStore) IsMemberMuted(int64, int64) (bool, error) {
	return false, nil
}

func (s *groupStreamCancelStore) GetGroupMembers(int64) ([]*types.GroupMember, error) {
	return s.members, nil
}

func (s *groupStreamCancelStore) GetUser(userID int64) (*types.User, error) {
	accountType := types.AccountHuman
	for _, member := range s.members {
		if member != nil && member.UserID == userID && member.IsBot {
			accountType = types.AccountBot
			break
		}
	}
	return &types.User{ID: userID, AccountType: accountType}, nil
}

func streamCancelMessage(id string, targetBotUID int64) *MsgClientPub {
	metadata := map[string]interface{}{
		"stream_id":    "cancel-" + id,
		"stream_event": "cancel",
		"control":      "interrupt",
	}
	if targetBotUID > 0 {
		metadata["target_bot_uid"] = targetBotUID
	}
	return &MsgClientPub{
		ID:       id,
		Topic:    "grp_80",
		Type:     "stream_cancel",
		MsgType:  "stream_cancel",
		Metadata: metadata,
	}
}

func TestGroupStreamCancelRejectsThirdMemberAfterTurnStarts(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	initiator := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(initiator)
	hub.addClient(bot)
	hub.groupTurns.record(80, 42, 7)

	thirdMember := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	db.members = append(db.members, &types.GroupMember{GroupID: 80, UserID: 8})
	hub.addClient(thirdMember)

	hub.handleStreamPub(thirdMember, streamCancelMessage("forged", 42), "grp_80")

	var denied ServerMessage
	decodeQueuedServerMessage(t, thirdMember.send, &denied)
	if denied.Ctrl == nil || denied.Ctrl.Code != 403 {
		t.Fatalf("forged cancel response = %#v, want 403", denied.Ctrl)
	}
	if drainOne(bot.send) || drainOne(initiator.send) {
		t.Fatal("forged cancel must not be fanned out")
	}
}

func TestGroupStreamCancelRequiresTargetAgentInMultiMemberGroup(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	initiator := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(initiator)
	hub.addClient(bot)
	hub.groupTurns.record(80, 42, 7)

	hub.handleStreamPub(initiator, streamCancelMessage("missing-target", 0), "grp_80")

	var denied ServerMessage
	decodeQueuedServerMessage(t, initiator.send, &denied)
	if denied.Ctrl == nil || denied.Ctrl.Code != 403 {
		t.Fatalf("targetless cancel response = %#v, want 403", denied.Ctrl)
	}
	if drainOne(bot.send) {
		t.Fatal("targetless multi-member cancel must not reach an agent")
	}
}

func TestTwoMemberGroupStreamCancelInfersItsOnlyAgent(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(human)
	hub.addClient(bot)

	hub.handleStreamPub(human, streamCancelMessage("two-member", 0), "grp_80")

	var ack ServerMessage
	decodeQueuedServerMessage(t, human.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("two-member cancel response = %#v, want 200", ack.Ctrl)
	}
	var received ServerMessage
	decodeQueuedServerMessage(t, bot.send, &received)
	if received.Data == nil || firstMetadataInt64(received.Data.Metadata, "target_bot_uid") != 42 {
		t.Fatalf("inferred agent cancel = %#v", received.Data)
	}
}

func TestTwoAgentGroupStreamCancelIsRejected(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	requester := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	targetBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(requester)
	hub.addClient(targetBot)

	hub.handleStreamPub(requester, streamCancelMessage("bot-forged", 43), "grp_80")

	var denied ServerMessage
	decodeQueuedServerMessage(t, requester.send, &denied)
	if denied.Ctrl == nil || denied.Ctrl.Code != 403 {
		t.Fatalf("agent cancel response = %#v, want 403", denied.Ctrl)
	}
	if drainOne(targetBot.send) {
		t.Fatal("an agent must not be allowed to interrupt a peer agent")
	}
}

func TestGroupStreamCancelAllowsInitiatorAndTargetsOnlyTheirAgent(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	initiator := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	observer := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	targetBot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	otherBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(initiator)
	hub.addClient(observer)
	hub.addClient(targetBot)
	hub.addClient(otherBot)
	hub.groupTurns.record(80, 42, 7)

	hub.handleStreamPub(initiator, streamCancelMessage("allowed", 42), "grp_80")

	var ack ServerMessage
	decodeQueuedServerMessage(t, initiator.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("initiator cancel response = %#v, want 200", ack.Ctrl)
	}
	for name, messages := range map[string]<-chan []byte{
		"target bot": targetBot.send,
		"observer":   observer.send,
	} {
		var received ServerMessage
		decodeQueuedServerMessage(t, messages, &received)
		if received.Data == nil || received.Data.Type != "stream_cancel" {
			t.Fatalf("%s received %#v, want stream_cancel", name, received.Data)
		}
		if firstMetadataInt64(received.Data.Metadata, "target_bot_uid") != 42 {
			t.Fatalf("%s cancel metadata = %#v", name, received.Data.Metadata)
		}
	}
	if drainOne(otherBot.send) {
		t.Fatal("cancel must not interrupt another agent")
	}
}

func TestHumanMessageRecordsAuthoritativeGroupAgentTurn(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(human)
	hub.addClient(bot)

	hub.broadcastToGroupWithMentions(
		80,
		&ServerMessage{Data: &MsgServerData{
			Topic:   "grp_80",
			From:    "usr7",
			SeqID:   101,
			Content: "please help",
			Type:    "text",
			MsgType: "text",
		}},
		7,
		[]string{"usr42"},
		7,
		false,
	)

	if !hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("the routed human request must authorize its initiator for that agent turn")
	}
	if hub.groupTurns.initiatedBy(80, 42, 8) {
		t.Fatal("another member must not inherit cancel authorization")
	}
}
