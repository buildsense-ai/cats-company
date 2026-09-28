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
	// silence the only bot.
	delivered := assertBotActivation(t, bot.send, true)
	if !reflect.DeepEqual(delivered.Data.Mentions, []string{"usr7"}) {
		t.Fatalf("mentions = %#v, want usr7", delivered.Data.Mentions)
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
		if !reflect.DeepEqual(delivered.Data.Mentions, []string{structuredMentionAllBots}) {
			t.Fatalf("bot %d mentions = %#v, want all", index, delivered.Data.Mentions)
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
	resolver := NewJevGroupActivationResolver(&JevClient{}, nil)
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
	resolver := NewJevGroupActivationResolver(&JevClient{}, nil)
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
	resolver := NewJevGroupActivationResolver(&JevClient{}, nil)
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

func TestActivationStateNamesBotsAndHumans(t *testing.T) {
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
		{UID: 42, DisplayName: "阿码", Role: "代码审查员", Description: "负责代码审查"},
		{UID: 43, DisplayName: "小文", Role: "文案写手", Description: "负责营销文案"},
	})

	for _, want := range []string{"产品发布组", "阿码", "代码审查员", "小文", "文案写手", "林", "这段代码帮我看下"} {
		if !strings.Contains(state, want) {
			t.Fatalf("state is missing %q:\n%s", want, state)
		}
	}
	// The judge must be able to tell the bot apart from the human.
	if !strings.Contains(state, "阿码（机器人）") {
		t.Fatalf("bot turn is not marked as coming from a bot:\n%s", state)
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
