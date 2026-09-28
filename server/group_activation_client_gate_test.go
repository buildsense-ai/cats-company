package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// clientWouldRespond mirrors XiaoBa's shouldActivateCatsCompanyMessage.
//
// The client deployed in the field still decides whether to answer from mentions
// and member_count. A server-only change is therefore only effective if it also
// satisfies that gate; otherwise the bot receives the message and drops it.
func clientWouldRespond(t *testing.T, delivered *ServerMessage, botUID int64) bool {
	t.Helper()
	if delivered == nil || delivered.Data == nil {
		t.Fatalf("no delivery to inspect")
	}
	data := delivered.Data
	if !strings.HasPrefix(data.Topic, "grp_") {
		return true
	}
	if data.MemberCount > 2 {
		target := formatUID(botUID)
		for _, mention := range data.Mentions {
			if mention == target {
				return true
			}
		}
		return false
	}
	return true
}

// TestClientGateSingleBotTwoMemberGroup is the baseline: one bot and one human
// is what the client already treats as "no addressing needed".
func TestClientGateSingleBotTwoMemberGroup(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(bot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"在吗"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 23, nil)

	delivered := assertBotActivation(t, bot.send, true)
	if !clientWouldRespond(t, &delivered, 42) {
		t.Fatalf("client would drop a two-member group message")
	}
}

// TestClientGateSingleBotMultiHumanGroup is the case the change exists to fix:
// one bot with several humans used to require an explicit mention.
func TestClientGateSingleBotMultiHumanGroup(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			8:  {ID: 8, AccountType: types.AccountHuman},
			9:  {ID: 9, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 9},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(bot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"帮我看下这个"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 24, nil)

	delivered := assertBotActivation(t, bot.send, true)
	if !clientWouldRespond(t, &delivered, 42) {
		t.Fatalf("client would drop a single-bot group message that the server activated")
	}
}

// TestClientGateMultiBotActivatedBot checks the same gate for a multi-bot group
// where the server activated exactly one member.
func TestClientGateMultiBotActivatedBot(t *testing.T) {
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
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr42 请处理"`),
		Mentions: []string{"usr42"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 25, nil)

	activated := assertBotActivation(t, botA.send, true)
	if !clientWouldRespond(t, &activated, 42) {
		t.Fatalf("client would drop an explicitly mentioned bot")
	}
	// The other bot must not answer, and the client must agree.
	skipped := assertBotActivation(t, botB.send, false)
	if clientWouldRespond(t, &skipped, 43) {
		t.Fatalf("client would answer a message that did not address it")
	}
}
