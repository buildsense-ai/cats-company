package server

import (
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type groupStreamCancelStore struct {
	store.Store
	members    []*types.GroupMember
	mutedUsers map[int64]bool
	taskStatus *types.ConversationTaskStatus
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

func (s *groupStreamCancelStore) IsMemberMuted(_ int64, userID int64) (bool, error) {
	return s.mutedUsers[userID], nil
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

func (s *groupStreamCancelStore) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	if status == nil {
		return nil, nil
	}
	prepared := prepareTestConversationTaskStatus(status)
	s.taskStatus = prepared
	return prepared, nil
}

func (s *groupStreamCancelStore) GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error) {
	if s.taskStatus == nil || s.taskStatus.TopicID != topicID || s.taskStatus.SourceUID != sourceUID {
		return nil, nil
	}
	copyOfStatus := *s.taskStatus
	return &copyOfStatus, nil
}

func (s *groupStreamCancelStore) GetConversationTaskStatuses(topicIDs []string) (map[string]*types.ConversationTaskStatus, error) {
	statuses := make(map[string]*types.ConversationTaskStatus)
	if s.taskStatus == nil {
		return statuses, nil
	}
	for _, topicID := range topicIDs {
		if topicID == s.taskStatus.TopicID {
			copyOfStatus := *s.taskStatus
			statuses[topicID] = &copyOfStatus
			break
		}
	}
	return statuses, nil
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
	hub.groupTurns.begin(80, 42, 7, 1)

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
	hub.groupTurns.begin(80, 42, 7, 1)

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

func TestGroupStreamCancelRejectsInvalidRequesterOrTargetWithoutFanout(t *testing.T) {
	tests := []struct {
		name           string
		members        []*types.GroupMember
		mutedRequester bool
		targetBotUID   int64
	}{
		{
			name: "muted requester",
			members: []*types.GroupMember{
				{GroupID: 80, UserID: 7},
				{GroupID: 80, UserID: 42, IsBot: true},
			},
			mutedRequester: true,
			targetBotUID:   42,
		},
		{
			name: "target is a human group member",
			members: []*types.GroupMember{
				{GroupID: 80, UserID: 7},
				{GroupID: 80, UserID: 8},
				{GroupID: 80, UserID: 42, IsBot: true},
			},
			targetBotUID: 8,
		},
		{
			name: "target bot is outside the group",
			members: []*types.GroupMember{
				{GroupID: 80, UserID: 7},
				{GroupID: 80, UserID: 8},
				{GroupID: 80, UserID: 42, IsBot: true},
			},
			targetBotUID: 99,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &groupStreamCancelStore{
				members: tc.members,
				mutedUsers: map[int64]bool{
					7: tc.mutedRequester,
				},
			}
			hub := NewHub(db, nil)
			requester := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
			observer := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 4)}
			groupBot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
			outsideBot := &Client{uid: 99, accountType: types.AccountBot, send: make(chan []byte, 4)}
			hub.addClient(requester)
			hub.addClient(observer)
			hub.addClient(groupBot)
			hub.addClient(outsideBot)
			hub.groupTurns.begin(80, 42, 7, 1)

			hub.handleStreamPub(requester, streamCancelMessage("denied", tc.targetBotUID), "grp_80")

			var denied ServerMessage
			decodeQueuedServerMessage(t, requester.send, &denied)
			if denied.Ctrl == nil || denied.Ctrl.Code != 403 {
				t.Fatalf("cancel response = %#v, want 403", denied.Ctrl)
			}
			if drainOne(requester.send) ||
				drainOne(observer.send) ||
				drainOne(groupBot.send) ||
				drainOne(outsideBot.send) {
				t.Fatal("denied cancel must not be fanned out")
			}
			if !hub.groupTurns.initiatedBy(80, 42, 7) {
				t.Fatal("denied cancel must not clear the active group-agent turn")
			}
		})
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
	hub.groupTurns.begin(80, 42, 7, 1)

	cancel := streamCancelMessage("allowed", 42)
	cancel.Metadata[artifactContextRefMetadataKey] = "acr_private"
	cancel.Metadata[artifactContextMetadataKey] = map[string]interface{}{"secret": true}
	cancel.Metadata[artifactRefMetadataKey] = map[string]interface{}{"id": "private"}
	cancel.Metadata[artifactPageContextMetadataKey] = map[string]interface{}{"selected_text": "private"}
	hub.handleStreamPub(initiator, cancel, "grp_80")

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
		for _, key := range []string{artifactContextRefMetadataKey, artifactContextMetadataKey, artifactRefMetadataKey, artifactPageContextMetadataKey} {
			if _, exists := received.Data.Metadata[key]; exists {
				t.Fatalf("%s group cancel leaked %q: %#v", name, key, received.Data.Metadata)
			}
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

func TestGroupStreamCancelDoesNotTransferAnActiveTurnToAnotherRequester(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	initiator := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	secondRequester := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 8)}
	hub.addClient(initiator)
	hub.addClient(secondRequester)
	hub.addClient(bot)

	hub.broadcastToGroupWithMentions(80, groupTextMessage(7, 101, "first request"), 7, []string{"usr42"}, 7, false)
	drainMessages(secondRequester.send)
	drainMessages(bot.send)
	if !hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("the first requester must own the active agent turn")
	}

	hub.broadcastToGroupWithMentions(80, groupTextMessage(8, 102, "second request"), 8, []string{"usr42"}, 8, false)
	drainMessages(initiator.send)
	drainMessages(bot.send)
	if !hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("a second request must not replace the active turn initiator")
	}
	if hub.groupTurns.initiatedBy(80, 42, 8) {
		t.Fatal("the second requester must not inherit the active turn")
	}

	hub.handleStreamPub(secondRequester, streamCancelMessage("second-requester", 42), "grp_80")
	var denied ServerMessage
	decodeQueuedServerMessage(t, secondRequester.send, &denied)
	if denied.Ctrl == nil || denied.Ctrl.Code != 403 {
		t.Fatalf("second requester cancel response = %#v, want 403", denied.Ctrl)
	}
	if drainOne(bot.send) || drainOne(initiator.send) {
		t.Fatal("the denied cancel must not be fanned out")
	}

	hub.handleStreamPub(initiator, streamCancelMessage("initiator", 42), "grp_80")
	var ack ServerMessage
	decodeQueuedServerMessage(t, initiator.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("initiator cancel response = %#v, want 200", ack.Ctrl)
	}
}

func TestGroupStreamCancelSurvivesAnIntermediateAgentReply(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	initiator := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	observer := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 8)}
	hub.addClient(initiator)
	hub.addClient(observer)
	hub.addClient(bot)

	hub.broadcastToGroupWithMentions(80, groupTextMessage(7, 201, "start work"), 7, []string{"usr42"}, 7, false)
	drainMessages(observer.send)
	drainMessages(bot.send)

	hub.broadcastToGroupWithMentions(80, groupTextMessage(42, 202, "intermediate answer"), 42, nil, 42, false)
	drainMessages(initiator.send)
	drainMessages(observer.send)
	if !hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("an ordinary agent reply must not finish the active turn")
	}

	hub.handleStreamPub(initiator, streamCancelMessage("after-intermediate", 42), "grp_80")
	var ack ServerMessage
	decodeQueuedServerMessage(t, initiator.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("cancel after intermediate reply = %#v, want 200", ack.Ctrl)
	}
}

func TestGroupStreamCancelTransfersOnlyAfterExplicitTurnCompletion(t *testing.T) {
	db := &groupStreamCancelStore{
		members: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(db, nil)
	firstRequester := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 16)}
	secondRequester := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 16)}
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 16)}
	hub.addClient(firstRequester)
	hub.addClient(secondRequester)
	hub.addClient(bot)

	hub.broadcastToGroupWithMentions(80, groupTextMessage(7, 301, "first turn"), 7, []string{"usr42"}, 7, false)
	drainMessages(secondRequester.send)
	drainMessages(bot.send)
	publishGroupTaskStatus(t, hub, bot, "run-a", "running")
	drainMessages(firstRequester.send)
	drainMessages(secondRequester.send)

	hub.broadcastToGroupWithMentions(80, groupTextMessage(8, 302, "premature second turn"), 8, []string{"usr42"}, 8, false)
	drainMessages(firstRequester.send)
	drainMessages(bot.send)
	if !hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("the first requester must retain ownership while run-a is active")
	}

	hub.broadcastToGroupWithMentions(80, groupTextMessage(42, 303, "intermediate answer"), 42, nil, 42, false)
	drainMessages(firstRequester.send)
	drainMessages(secondRequester.send)
	if !hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("the intermediate answer must not complete run-a")
	}

	publishGroupTaskStatus(t, hub, bot, "run-a", "completed")
	drainMessages(firstRequester.send)
	drainMessages(secondRequester.send)
	if hub.groupTurns.initiatedBy(80, 42, 7) {
		t.Fatal("the matching completed event must close run-a")
	}

	hub.broadcastToGroupWithMentions(80, groupTextMessage(8, 304, "next second turn"), 8, []string{"usr42"}, 8, false)
	drainMessages(firstRequester.send)
	drainMessages(bot.send)
	if !hub.groupTurns.initiatedBy(80, 42, 8) {
		t.Fatal("the second requester may own the next turn only after run-a completes")
	}

	hub.handleStreamPub(secondRequester, streamCancelMessage("second-turn", 42), "grp_80")
	var ack ServerMessage
	decodeQueuedServerMessage(t, secondRequester.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("second requester next-turn cancel response = %#v, want 200", ack.Ctrl)
	}
}

func TestGroupAgentTurnIgnoresAStaleTerminalStatusFromThePreviousRun(t *testing.T) {
	tracker := newGroupAgentTurnTracker(defaultGroupAgentTurnTTL)
	if !tracker.begin(80, 42, 7, 401) {
		t.Fatal("expected the first turn to start")
	}
	tracker.observeTaskStatus(80, 42, "run-a", "running")
	tracker.observeTaskStatus(80, 42, "run-a", "completed")

	if !tracker.begin(80, 42, 8, 402) {
		t.Fatal("expected the next turn to start after run-a completed")
	}
	tracker.observeTaskStatus(80, 42, "run-a", "completed")
	if !tracker.initiatedBy(80, 42, 8) {
		t.Fatal("a duplicate terminal status from run-a must not clear the next turn")
	}

	tracker.observeTaskStatus(80, 42, "run-b", "running")
	tracker.observeTaskStatus(80, 42, "run-a", "failed")
	if !tracker.initiatedBy(80, 42, 8) {
		t.Fatal("a mismatched terminal status must not clear the active run")
	}
	tracker.observeTaskStatus(80, 42, "run-b", "completed")
	if tracker.initiatedBy(80, 42, 8) {
		t.Fatal("the matching terminal status must clear the active run")
	}
}

func groupTextMessage(senderUID int64, seqID int, content string) *ServerMessage {
	return &ServerMessage{Data: &MsgServerData{
		Topic:   "grp_80",
		From:    formatUID(senderUID),
		SeqID:   seqID,
		Content: content,
		Type:    "text",
		MsgType: "text",
	}}
}

func publishGroupTaskStatus(t *testing.T, hub *Hub, bot *Client, runID, state string) {
	t.Helper()
	hub.handleTaskStatusPub(
		bot,
		&MsgClientPub{ID: "status-" + runID + "-" + state, Topic: "grp_80"},
		"grp_80",
		&normalizedMessagePayload{
			DisplayType:         taskStatusType,
			ExplicitDisplayType: true,
			DisplayContent: map[string]interface{}{
				"run_id": runID,
				"state":  state,
			},
		},
	)
	var ack ServerMessage
	decodeQueuedServerMessage(t, bot.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("task status %s/%s response = %#v, want 200", runID, state, ack.Ctrl)
	}
}

func drainMessages(messages <-chan []byte) {
	for drainOne(messages) {
	}
}
