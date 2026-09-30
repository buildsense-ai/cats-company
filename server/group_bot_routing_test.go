package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type agentTaskGroupRoutingStore struct {
	*identityMessageStore
	group *types.Group
}

func (s *agentTaskGroupRoutingStore) GetGroup(groupID int64) (*types.Group, error) {
	return s.group, nil
}

// assertBotActivation reads one delivery and checks the activation verdict.
//
// Group delivery and activation are separate: every bot receives the message so
// it can read the conversation, and the activated flag says whether this message
// addresses it. Tests therefore assert on the flag rather than on whether a
// message arrived at all.
func assertBotActivation(t *testing.T, ch <-chan []byte, wantActivated bool) ServerMessage {
	t.Helper()
	var delivered ServerMessage
	decodeQueuedServerMessage(t, ch, &delivered)
	if delivered.Data == nil {
		t.Fatalf("delivered message has no data")
	}
	if delivered.Data.Activated == nil {
		t.Fatalf("delivered message is missing the activation verdict")
	}
	if *delivered.Data.Activated != wantActivated {
		t.Fatalf("activated = %v, want %v", *delivered.Data.Activated, wantActivated)
	}
	return delivered
}

// Group delivery and activation are separate concerns. Every member receives a
// group message so bots can read the conversation, while the activated flag says
// whether the message addresses that bot. These tests therefore assert on the
// flag rather than on whether a delivery happened at all.

func TestGroupFanoutLargeGroupHumanMessageWithoutMentionsDeliversButSkipsAllBots(t *testing.T) {
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

	// Both bots see the message as background; neither opens a turn.
	assertBotActivation(t, botA.send, false)
	assertBotActivation(t, botB.send, false)
}

func TestGroupFanoutMultiBotWithoutMentionLeavesEveryBotInactive(t *testing.T) {
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
		Content: json.RawMessage(`"继续处理这个任务"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 34, nil)

	assertBotActivation(t, botA.send, false)
	assertBotActivation(t, botB.send, false)
}

func TestGroupFanoutMentionOverridesOtherBots(t *testing.T) {
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
	primaryBot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	mentionedBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(primaryBot)
	hub.addClient(mentionedBot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr43 请接手"`),
		Mentions: []string{"usr43"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 35, nil)

	mentioned := assertBotActivation(t, mentionedBot.send, true)
	if !reflect.DeepEqual(mentioned.Data.Mentions, []string{"usr43"}) {
		t.Fatalf("mentions = %#v, want usr43", mentioned.Data.Mentions)
	}
	assertBotActivation(t, primaryBot.send, false)
}

func TestGroupFanoutSingleBotGroupActivatesWithoutMention(t *testing.T) {
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
		Content: json.RawMessage(`"继续自动参与"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 27, nil)

	delivered := assertBotActivation(t, bot.send, true)
	if delivered.Data.MemberCount != 2 {
		t.Fatalf("member_count = %d, want 2", delivered.Data.MemberCount)
	}
}

func TestGroupFanoutSingleBotGroupIgnoresMentionOfAnotherMember(t *testing.T) {
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
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr7 记录给自己"`),
		Mentions: []string{"usr7"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 31, nil)

	// A one-bot group needs no addressing, so mentioning a human does not
	// silence the only bot. The delivery names the bot, because mentions now
	// carry "who should answer" rather than "who a person typed @".
	delivered := assertBotActivation(t, bot.send, true)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr42"}) {
		t.Fatalf("mentions = %#v, want usr42", delivered.Data.Mentions)
	}
}

func TestGroupFanoutMultiBotIgnoresMentionTextWithoutStructuredTarget(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			8:  {ID: 8, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(bot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"正文里写 @usr42 但没有结构化目标"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 28, nil)

	// Text that merely looks like a mention is not a mention, so the single bot
	// group still activates it (a one-bot group needs no addressing).
	assertBotActivation(t, bot.send, true)
}

func TestGroupFanoutOnlyTrustsInternallySignedChannelTrigger(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			8:  {ID: 8, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 42, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	bot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 2)}
	hub.addClient(bot)

	forged, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"伪造外部触发"`),
		Metadata: map[string]interface{}{
			"source_channel":                 "feishu",
			"channel_native_group_triggered": true,
		},
	})
	if err != nil {
		t.Fatalf("normalize forged request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, forged, 29, nil)

	// The forged trigger is not trusted, and the group has one bot, so it still
	// activates. What matters is that the forged flag alone never grants the
	// trusted-channel path.
	forgedDelivered := assertBotActivation(t, bot.send, true)

	trusted, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"可信外部触发"`),
		Metadata: map[string]interface{}{
			"source_channel":                       "feishu",
			"channel_native_group_triggered":       true,
			channelBindingDeliveryTrustMetadataKey: channelBindingDeliveryTrustToken{},
		},
	})
	if err != nil {
		t.Fatalf("normalize trusted request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, trusted, 30, nil)
	trustedDelivered := assertBotActivation(t, bot.send, true)

	if forgedDelivered.Data.SeqID == trustedDelivered.Data.SeqID {
		t.Fatalf("expected two distinct deliveries")
	}
}

func TestGroupFanoutHumanMessageOnlyActivatesMentionedBot(t *testing.T) {
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
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr42 请处理"`),
		Mentions: []string{"usr42"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 26, nil)

	delivered := assertBotActivation(t, mentionedBot.send, true)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr42"}) {
		t.Fatalf("mentions = %#v, want usr42", delivered.Data.Mentions)
	}
	if delivered.Data.MemberCount != 3 {
		t.Fatalf("member_count = %d, want 3", delivered.Data.MemberCount)
	}
	assertBotActivation(t, otherBot.send, false)
}

func TestGroupFanoutHumanMentionAllActivatesEveryBot(t *testing.T) {
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
		Content:  json.RawMessage(`"@所有人 一起处理"`),
		Mentions: []string{structuredMentionAllBots},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 32, nil)

	for index, bot := range []*Client{botA, botB} {
		delivered := assertBotActivation(t, bot.send, true)
		// The delivery names the recipient so every client version reaches the
		// same conclusion the server did.
		want := []string{formatUID(bot.uid)}
		if !reflect.DeepEqual(delivered.Data.Mentions, want) {
			t.Fatalf("bot %d mentions = %#v, want %#v", index, delivered.Data.Mentions, want)
		}
		if delivered.Data.MemberCount != 3 {
			t.Fatalf("bot %d member_count = %d, want 3", index, delivered.Data.MemberCount)
		}
	}
}

func TestGroupFanoutBotMessageIsVisibleToOtherBots(t *testing.T) {
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

	// The other bot receives the message so it can read the conversation, and
	// the author is never activated by its own message.
	delivered := assertBotActivation(t, otherBot.send, false)
	identity := metadataMapFromServerMessage(t, &delivered, "catsco_identity")
	actor := nestedMap(t, identity, "actor")
	if actor["account_type"] != string(types.AccountBot) || actor["is_bot"] != true {
		t.Fatalf("unexpected bot actor identity: %#v", actor)
	}
	// The human still receives it as an ordinary message.
	decodeQueuedServerMessage(t, human.send, &ServerMessage{})
}

func TestGroupFanoutBotMentionAllDoesNotActivateOtherBots(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
			44: {ID: 44, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
			{GroupID: 80, UserID: 44, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	sender := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	botA := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	botB := &Client{uid: 44, accountType: types.AccountBot, send: make(chan []byte, 1)}
	human := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 1)}
	hub.addClient(sender)
	hub.addClient(botA)
	hub.addClient(botB)
	hub.addClient(human)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@所有人 我已经完成"`),
		Mentions: []string{structuredMentionAllBots},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(42, "grp_80", 0, payload, 33, sender)

	// "@all" from a bot addresses the other bots. Without the deterministic
	// resolver the judge decides, and "@all" short-circuits to activating every
	// candidate except the author.
	assertBotActivation(t, botA.send, true)
	assertBotActivation(t, botB.send, true)
	decodeQueuedServerMessage(t, human.send, &ServerMessage{})
}

func TestGroupFanoutBotMentionActivatesMentionedBot(t *testing.T) {
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
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr43 请继续处理"`),
		Mentions: []string{"usr43"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(42, "grp_80", 0, payload, 25, sender)

	// Handing work over by naming the next owner must wake that owner, which is
	// how a bot-to-bot handover works.
	delivered := assertBotActivation(t, mentionedBot.send, true)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr43"}) {
		t.Fatalf("mentions = %#v, want usr43", delivered.Data.Mentions)
	}
	assertBotActivation(t, otherBot.send, false)
}

// --- rule ordering ---

// --- agent-task groups ---
//
// An agent-task group exists to finish one piece of work. A message that
// addresses nobody still needs its owner, otherwise the task stalls with no
// reply and no notice. These cases pin that rule and its handover behaviour.

// An agent-task group is a group like any other once judging is installed: a
// message that addresses nobody reaches nobody. The old rule handed such a
// message to the first agent, which meant every message produced a reply even
// when the group had nothing to do with it.
func TestGroupFanoutMultiBotAgentTaskWithoutMentionReachesNobody(t *testing.T) {
	baseStore := &identityMessageStore{
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
	store := &agentTaskGroupRoutingStore{
		identityMessageStore: baseStore,
		group: &types.Group{
			ID:       80,
			Kind:     types.GroupKindAgentTask,
			AgentIDs: []int64{42, 43},
		},
	}
	hub := NewHub(store, nil)
	primaryBot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	collaboratorBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(primaryBot)
	hub.addClient(collaboratorBot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"继续处理这个任务"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 34, nil)

	assertBotActivation(t, primaryBot.send, false)
	assertBotActivation(t, collaboratorBot.send, false)
}

func TestGroupFanoutMultiBotAgentTaskMentionOverridesPrimaryBot(t *testing.T) {
	baseStore := &identityMessageStore{
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
	store := &agentTaskGroupRoutingStore{
		identityMessageStore: baseStore,
		group: &types.Group{
			ID:       80,
			Kind:     types.GroupKindAgentTask,
			AgentIDs: []int64{42, 43},
		},
	}
	hub := NewHub(store, nil)
	primaryBot := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 1)}
	collaboratorBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(primaryBot)
	hub.addClient(collaboratorBot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr43 请接手"`),
		Mentions: []string{"usr43"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 35, nil)

	// Naming someone overrides the default owner.
	delivered := assertBotActivation(t, collaboratorBot.send, true)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr43"}) {
		t.Fatalf("mentions = %#v, want usr43", delivered.Data.Mentions)
	}
	assertBotActivation(t, primaryBot.send, false)
}

func TestGroupFanoutAgentTaskPromotesRemainingBotAfterPrimaryRemoval(t *testing.T) {
	baseStore := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			8:  {ID: 8, AccountType: types.AccountHuman},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 8},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	store := &agentTaskGroupRoutingStore{
		identityMessageStore: baseStore,
		group: &types.Group{
			ID:       80,
			Kind:     types.GroupKindAgentTask,
			AgentIDs: []int64{43},
		},
	}
	hub := NewHub(store, nil)
	remainingBot := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(remainingBot)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Content: json.RawMessage(`"原机器人已移除，请继续"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 36, nil)

	// Only one bot remains, so the single-bot rule covers the message: a lone
	// bot needs no addressing. This no longer depends on an agent-task default.
	delivered := assertBotActivation(t, remainingBot.send, true)
	if delivered.Data.MemberCount != 3 {
		t.Fatalf("member_count = %d, want 3", delivered.Data.MemberCount)
	}
}

// assertNoQueuedServerMessage fails when a message was delivered that should not
// have been.
func assertNoQueuedServerMessage(t *testing.T, ch <-chan []byte) {
	t.Helper()
	select {
	case raw := <-ch:
		t.Fatalf("unexpected queued server message: %s", raw)
	default:
	}
}

func TestDeterministicActivationSingleBotAlwaysActivates(t *testing.T) {
	decision := deterministicGroupActivation(GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 7},
			{UserID: 42, IsBot: true},
		},
	})
	if decision.Source != activationSourceSingleBot {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceSingleBot)
	}
	if _, ok := decision.Activated[42]; !ok {
		t.Fatalf("single bot was not activated: %#v", decision.Activated)
	}
	if decision.Degraded {
		t.Fatalf("single-bot routing must not report degradation")
	}
}

func TestDeterministicActivationMentionBeatsJudging(t *testing.T) {
	decision := deterministicGroupActivation(GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 7},
			{UserID: 42, IsBot: true},
			{UserID: 43, IsBot: true},
		},
		Mentions: []string{"usr43"},
	})
	if decision.Source != activationSourceMention {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceMention)
	}
	if _, ok := decision.Activated[43]; !ok {
		t.Fatalf("mentioned bot was not activated: %#v", decision.Activated)
	}
	if _, ok := decision.Activated[42]; ok {
		t.Fatalf("unmentioned bot was activated: %#v", decision.Activated)
	}
}

func TestDeterministicActivationNeverActivatesTheAuthor(t *testing.T) {
	decision := deterministicGroupActivation(GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 42, IsBot: true},
			{UserID: 43, IsBot: true},
		},
		SenderUID:   42,
		SenderIsBot: true,
		Mentions:    []string{"usr42"},
	})
	// Mentioning yourself must not restart your own turn.
	if _, ok := decision.Activated[42]; ok {
		t.Fatalf("the author activated itself: %#v", decision.Activated)
	}
	if len(decision.Activated) != 0 {
		t.Fatalf("mentioning only the author activated someone: %#v", decision.Activated)
	}
}

func TestDeterministicActivationBotHandoverStillWorks(t *testing.T) {
	decision := deterministicGroupActivation(GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 42, IsBot: true},
			{UserID: 43, IsBot: true},
		},
		SenderUID:   42,
		SenderIsBot: true,
		Mentions:    []string{"usr43"},
	})
	if decision.Source != activationSourceMention {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceMention)
	}
	if _, ok := decision.Activated[43]; !ok {
		t.Fatalf("handover target was not activated: %#v", decision.Activated)
	}
}

func TestDeterministicActivationWithoutMentionLeavesBotsIdle(t *testing.T) {
	decision := deterministicGroupActivation(GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 7},
			{UserID: 42, IsBot: true},
			{UserID: 43, IsBot: true},
		},
	})
	if len(decision.Activated) != 0 {
		t.Fatalf("bots activated without a mention: %#v", decision.Activated)
	}
	if decision.Degraded {
		t.Fatalf("a missing mention is a designed outcome, not a degradation")
	}
}

func TestJevResolverFallsBackWhenClientIsUnavailable(t *testing.T) {
	resolver := NewJevGroupActivationResolver(&JevClient{})
	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 7},
			{UserID: 42, IsBot: true},
			{UserID: 43, IsBot: true},
		},
	})
	if !decision.Degraded {
		t.Fatalf("expected degradation when the judge is unavailable")
	}
	if len(decision.Activated) != 0 {
		t.Fatalf("degradation must not activate anyone: %#v", decision.Activated)
	}
}

func TestJevResolverSingleBotSkipsJudging(t *testing.T) {
	resolver := NewJevGroupActivationResolver(&JevClient{})
	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 7},
			{UserID: 42, IsBot: true},
		},
	})
	if decision.Source != activationSourceSingleBot {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceSingleBot)
	}
	if decision.Degraded {
		t.Fatalf("a single-bot group must not need the judge")
	}
}

func TestJevResolverMentionSkipsJudging(t *testing.T) {
	resolver := NewJevGroupActivationResolver(&JevClient{})
	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		Members: []*types.GroupMember{
			{UserID: 7},
			{UserID: 42, IsBot: true},
			{UserID: 43, IsBot: true},
		},
		Mentions: []string{"usr43"},
	})
	if decision.Source != activationSourceMention {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceMention)
	}
	if _, ok := decision.Activated[43]; !ok {
		t.Fatalf("mentioned bot was not activated: %#v", decision.Activated)
	}
}

// --- prompt construction ---

func TestActivationStateNamesMembersAndMarksTheirKind(t *testing.T) {
	state := activationState(GroupActivationRequest{
		GroupName: "产品发布组",
		Members: []*types.GroupMember{
			{UserID: 7, DisplayName: "林"},
			{UserID: 42, IsBot: true, DisplayName: "阿码"},
			{UserID: 43, IsBot: true, DisplayName: "小文"},
		},
		JudgeContext: func() GroupJudgeContext {
			return GroupJudgeContext{
				GroupName: "产品发布组",
				RecentTurns: []GroupActivationTurn{
					{Speaker: "林", Text: "这段代码帮我看下"},
					{Speaker: "阿码", IsBot: true, Text: "收到"},
				},
			}
		},
	}, []GroupActivationBot{
		{UID: 42, DisplayName: "阿码"},
		{UID: 43, DisplayName: "小文"},
	})

	// The state is a JSON object so each part carries a name, and every member
	// is marked as a bot or a user so the judge can tell them apart.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(state), &decoded); err != nil {
		t.Fatalf("state is not valid JSON: %v\n%s", err, state)
	}
	group, ok := decoded["group"].(map[string]any)
	if !ok || group["name"] != "产品发布组" {
		t.Fatalf("group name missing from state: %s", state)
	}

	members, ok := decoded["members"].([]any)
	if !ok || len(members) != 3 {
		t.Fatalf("expected three members, got %v: %s", decoded["members"], state)
	}
	kinds := map[string]string{}
	for _, entry := range members {
		member, _ := entry.(map[string]any)
		name, _ := member["name"].(string)
		kind, _ := member["type"].(string)
		kinds[name] = kind
	}
	if kinds["阿码"] != "bot" || kinds["小文"] != "bot" || kinds["林"] != "user" {
		t.Fatalf("member kinds are wrong: %#v", kinds)
	}

	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("expected two transcript entries, got %v: %s", decoded["messages"], state)
	}
	last, _ := messages[1].(map[string]any)
	if last["from"] != "阿码" || last["type"] != "bot" || last["text"] != "收到" {
		t.Fatalf("bot turn is not marked as coming from a bot: %#v", last)
	}
}

// The judge is asked about participation, not about a job title, so the prompt
// must not carry the owner-defined role or description.
func TestActivationStateCarriesNoRoleOrDescription(t *testing.T) {
	state := activationState(GroupActivationRequest{
		GroupName: "产品发布组",
		Members: []*types.GroupMember{
			{UserID: 42, IsBot: true, DisplayName: "阿码"},
		},
	}, []GroupActivationBot{{UID: 42, DisplayName: "阿码"}})

	for _, unwanted := range []string{"代码审查员", "文案写手", "role", "description"} {
		if strings.Contains(state, unwanted) {
			t.Fatalf("state should not carry %q:\n%s", unwanted, state)
		}
	}
}

// A bot's display name must reach the prompt. Naming bots by uid would not line
// up with the transcript, which speaks display names.
func TestActivationBotNameUsesDisplayName(t *testing.T) {
	bots := activationBots([]*types.GroupMember{
		{UserID: 42, IsBot: true, DisplayName: "阿码"},
		{UserID: 43, IsBot: true},
	})
	if len(bots) != 2 {
		t.Fatalf("expected two bots, got %d", len(bots))
	}
	if got := activationBotName(bots[0]); got != "阿码" {
		t.Fatalf("display name not carried through: %q", got)
	}
	// A bot with no display name still needs an identity the judge can cite.
	if got := activationBotName(bots[1]); got != "usr43" {
		t.Fatalf("missing display name should fall back to uid, got %q", got)
	}
}

func TestMergeActivationTurnsFoldsConsecutiveBotOutput(t *testing.T) {
	turns := mergeActivationTurns([]GroupActivationTurn{
		{Speaker: "林", Text: "帮我看看"},
		{Speaker: "阿码", IsBot: true, Text: "开始检查"},
		{Speaker: "阿码", IsBot: true, Text: "定位到第 42 行"},
		{Speaker: "阿码", IsBot: true, Text: "审查完成"},
		{Speaker: "小文", IsBot: true, Text: "公告写好了"},
	})
	if len(turns) != 3 {
		t.Fatalf("merged turn count = %d, want 3: %#v", len(turns), turns)
	}
	if turns[1].Text != "开始检查 定位到第 42 行 审查完成" {
		t.Fatalf("consecutive bot output was not folded: %q", turns[1].Text)
	}
	// The human message survives ahead of the folded bot output.
	if turns[0].Speaker != "林" {
		t.Fatalf("human turn was displaced: %#v", turns[0])
	}
}
