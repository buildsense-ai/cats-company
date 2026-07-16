package server

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestGroupFanoutHumanMessageWithoutMentionsReachesAllBots(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	botA := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	botB := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(botA)
	hub.addClient(botB)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"大家好"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 23, nil)

	decodeQueuedServerMessage(t, botA.send, &ServerMessage{})
	decodeQueuedServerMessage(t, botB.send, &ServerMessage{})
}

func TestGroupFanoutHumanMessageOnlyWakesMentionedBot(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	mentionedBot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	otherBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(mentionedBot)
	hub.addClient(otherBot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"@usr42 请处理"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 26, nil)

	var delivered ServerMessage
	decodeQueuedServerMessage(t, mentionedBot.send, &delivered)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr42"}) {
		t.Fatalf("mentions = %#v, want usr42", delivered.Data.Mentions)
	}
	assertNoQueuedServerMessage(t, otherBot.send)
}

func TestGroupFanoutBotMessageWithoutMentionsSkipsOtherBots(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	sender := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	otherBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 1)}
	hub.addClient(sender)
	hub.addClient(otherBot)
	hub.addClient(human)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"收到，等待安排"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(42, "grp_80", 0, payload, 24, sender)

	var delivered ServerMessage
	decodeQueuedServerMessage(t, human.send, &delivered)
	identity := metadataMapFromServerMessage(t, &delivered, "catsco_identity")
	actor := nestedMap(t, identity, "actor")
	if actor["account_type"] != string(types.AccountBot) || actor["is_bot"] != true {
		t.Fatalf("unexpected bot actor identity: %#v", actor)
	}
	assertNoQueuedServerMessage(t, otherBot.send)
}

func TestGroupFanoutBotMessageOnlyWakesMentionedBot(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
			44: {ID: 44, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
			{GroupID: 80, UserID: 44, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	sender := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	mentionedBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	otherBot := &Client{uid: 44, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(sender)
	hub.addClient(mentionedBot)
	hub.addClient(otherBot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"@usr43 请继续处理"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(42, "grp_80", 0, payload, 25, sender)

	var delivered ServerMessage
	decodeQueuedServerMessage(t, mentionedBot.send, &delivered)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr43"}) {
		t.Fatalf("mentions = %#v, want usr43", delivered.Data.Mentions)
	}
	assertNoQueuedServerMessage(t, otherBot.send)
}

func assertNoQueuedServerMessage(t *testing.T, ch <-chan []byte) {
	t.Helper()
	select {
	case raw := <-ch:
		t.Fatalf("unexpected queued server message: %s", raw)
	default:
	}
}
