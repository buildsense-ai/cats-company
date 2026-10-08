package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// judgeStub is a stand-in for the relay's Jev lane. It answers every question
// with the score the case wants and counts the calls, so a test can prove the
// judge was never invoked rather than merely that its answer was ignored.
type judgeStub struct {
	server *httptest.Server
	calls  int32
	score  float64
}

func newJudgeStub(t *testing.T, score float64) *judgeStub {
	t.Helper()
	stub := &judgeStub{score: score}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.calls, 1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read judge request: %v", err)
		}
		var payload jevRequestPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Errorf("decode judge request: %v", err)
		}
		answers := make(map[string]jevAnswer, len(payload.Questions))
		for name := range payload.Questions {
			value := stub.score
			answers[name] = jevAnswer{Type: "noul", Noul: &value}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(jevResponsePayload{
			Model:   "jev-latest",
			Answers: answers,
		}); err != nil {
			t.Errorf("encode judge response: %v", err)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *judgeStub) resolver() *JevGroupActivationResolver {
	return &JevGroupActivationResolver{
		client: &JevClient{
			baseURL:    s.server.URL,
			path:       defaultJevPath,
			model:      "jev-latest",
			attempts:   1,
			httpClient: s.server.Client(),
		},
		threshold: defaultActivationThreshold,
	}
}

func (s *judgeStub) callCount() int {
	return int(atomic.LoadInt32(&s.calls))
}

func judgeTestMembers() []*types.GroupMember {
	return []*types.GroupMember{
		{UserID: 7, DisplayName: "林"},
		{UserID: 42, IsBot: true, DisplayName: "阿码"},
		{UserID: 43, IsBot: true, DisplayName: "小文"},
	}
}

// Agent working traffic must never reach the judge.
//
// The transcript already refuses to show the judge tool calls, tool results,
// thinking, runtime plans, stream deltas and task status. Judging them anyway
// asks the model about text it cannot see, so the answer can only come from
// imagination. In the field this was not theoretical: on a group that ran away,
// tool traffic was 91% of one bot's messages and a bare "execute_shell" scored
// 0.56-0.60 for the other bot, which was enough to wake it and keep the loop
// going.
func TestWorkingTrafficNeverReachesTheJudge(t *testing.T) {
	working := []string{
		"tool_use",
		"tool_result",
		"thinking",
		"runtime_plan",
		"stream_delta",
		"stream_cancel",
		"task_status",
		"debug",
	}
	for _, displayType := range working {
		t.Run(displayType, func(t *testing.T) {
			stub := newJudgeStub(t, 0.95)
			resolver := stub.resolver()

			decision := resolver.Resolve(context.Background(), GroupActivationRequest{
				GroupID:         80,
				SenderUID:       7,
				Members:         judgeTestMembers(),
				NotConversation: true,
			})

			if stub.callCount() != 0 {
				t.Fatalf("%s reached the judge: %d calls", displayType, stub.callCount())
			}
			if decision.Source != activationSourceNotConversation {
				t.Fatalf("source = %s, want %s", decision.Source, activationSourceNotConversation)
			}
			if len(decision.Activated) != 0 {
				t.Fatalf("working traffic activated %v", decision.Activated)
			}
		})
	}
}

// The classifier behind the gate must agree with the transcript's own rule, so
// a message the judge cannot see is also a message that cannot trigger judging.
func TestJudgeableActivationMessageMatchesTranscriptVisibility(t *testing.T) {
	cases := []struct {
		name        string
		msg         *ServerMessage
		wantJudge   bool
		wantDisplay string
	}{
		{
			name:      "plain text is conversation",
			msg:       &ServerMessage{Data: &MsgServerData{Type: "text", Content: "帮我看下这段"}},
			wantJudge: true,
		},
		{
			name: "a tool call is not conversation",
			msg: &ServerMessage{Data: &MsgServerData{
				Type:          "text",
				Content:       "execute_shell",
				ContentBlocks: []types.ContentBlock{{Type: "tool_use", Name: "execute_shell"}},
			}},
		},
		{
			name: "a tool result is not conversation",
			msg: &ServerMessage{Data: &MsgServerData{
				Type:          "text",
				Content:       "Command completed",
				ContentBlocks: []types.ContentBlock{{Type: "tool_result", Content: "ok"}},
			}},
		},
		{
			name: "a typed tool message is not conversation",
			msg:  &ServerMessage{Data: &MsgServerData{Type: "tool_use", Content: "glob"}},
		},
		{
			name:      "an image is conversation",
			msg:       &ServerMessage{Data: &MsgServerData{Type: "image", Content: "[文件] abc"}},
			wantJudge: true,
		},
		{
			name: "a message with no data cannot be judged",
			msg:  &ServerMessage{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isJudgeableActivationMessage(tc.msg); got != tc.wantJudge {
				t.Fatalf("isJudgeableActivationMessage = %v, want %v", got, tc.wantJudge)
			}
		})
	}
}

// Mute is a lock, not just a silence. A muted member cannot post, so activating
// one spends a judgement, a delivery and a whole model turn to produce a write
// the server will reject.
//
// Naming the muted member must not be reinterpreted as an open request either:
// "@a muted bot" is a designation, and if the named member cannot answer the
// correct outcome is that nobody does — not that the judge hands the work to
// somebody else the person did not ask for.
func TestMutedMemberIsNeverActivatedEvenByMention(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members:   judgeTestMembers(),
		Mentions:  []string{"usr42"},
		MutedUIDs: []int64{42},
	})

	if stub.callCount() != 0 {
		t.Fatalf("a muted member's mention still reached the judge: %d calls", stub.callCount())
	}
	if len(decision.Activated) != 0 {
		t.Fatalf("a mention of a muted member activated someone: %v", decision.Activated)
	}
	if decision.Source != activationSourceNoCandidate {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceNoCandidate)
	}
}

// A mention that names a reachable member alongside a muted one still reaches
// the reachable member.
func TestMentionOfReachableMemberSurvivesAMutedOne(t *testing.T) {
	stub := newJudgeStub(t, 0.1)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members:   judgeTestMembers(),
		Mentions:  []string{"usr42", "usr43"},
		MutedUIDs: []int64{42},
	})

	if stub.callCount() != 0 {
		t.Fatalf("an explicit mention called the judge: %d calls", stub.callCount())
	}
	if _, ok := decision.Activated[42]; ok {
		t.Fatalf("the muted member was activated: %v", decision.Activated)
	}
	if _, ok := decision.Activated[43]; !ok {
		t.Fatalf("the reachable named member was dropped: %v", decision.Activated)
	}
}

// A mute in a two-bot group leaves the other member addressable.
func TestMutedMemberIsDroppedFromCandidates(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members:   judgeTestMembers(),
		MutedUIDs: []int64{42},
	})

	if decision.Source != activationSourceJev {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceJev)
	}
	if _, ok := decision.Scores[42]; ok {
		t.Fatalf("a muted member was offered to the judge: %v", decision.Scores)
	}
	if _, ok := decision.Activated[43]; !ok {
		t.Fatalf("the remaining member was not activated: %v", decision.Activated)
	}
}

// A bot that is already running is not asked whether it should run. The answer
// cannot change anything, and asking is what let two bots keep waking each
// other on every progress line.
func TestWorkingMemberIsNotJudged(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:     80,
		SenderUID:   7,
		Members:     judgeTestMembers(),
		WorkingUIDs: func() []int64 { return []int64{42} },
	})

	if decision.Source != activationSourceJev {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceJev)
	}
	if _, ok := decision.Scores[42]; ok {
		t.Fatalf("a working member was judged: %v", decision.Scores)
	}
	if _, ok := decision.Activated[43]; !ok {
		t.Fatalf("the idle member was not activated: %v", decision.Activated)
	}
}

// Activating a bot and judging with Jev are separate. An explicit mention is a
// person addressing a member directly, costs no model call, and the client
// folds the message into the running turn — so a busy member stays reachable.
func TestBusyMemberStaysReachableByMention(t *testing.T) {
	stub := newJudgeStub(t, 0.1)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:     80,
		SenderUID:   7,
		Members:     judgeTestMembers(),
		Mentions:    []string{"usr42"},
		WorkingUIDs: func() []int64 { return []int64{42} },
	})

	if stub.callCount() != 0 {
		t.Fatalf("an explicit mention called the judge: %d calls", stub.callCount())
	}
	if _, ok := decision.Activated[42]; !ok {
		t.Fatalf("a busy member named by hand was not activated: %v", decision.Activated)
	}
	if decision.Source != activationSourceMention {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceMention)
	}
}

// When every candidate is filtered out there is nothing to ask about, so the
// judge is not called at all: "nobody should reply" is already known.
func TestNoCandidateSkipsTheJudge(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:     80,
		SenderUID:   7,
		Members:     judgeTestMembers(),
		WorkingUIDs: func() []int64 { return []int64{42, 43} },
	})

	if stub.callCount() != 0 {
		t.Fatalf("the judge was called with no candidates: %d calls", stub.callCount())
	}
	if decision.Source != activationSourceNoCandidate {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceNoCandidate)
	}
	if len(decision.Activated) != 0 {
		t.Fatalf("nobody was addressable but %v was activated", decision.Activated)
	}
}

// The author is never a candidate, and when it is the only member left there is
// no judgement to make.
func TestAuthorAloneLeavesNothingToJudge(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:     80,
		SenderUID:   42,
		SenderIsBot: true,
		Members:     judgeTestMembers(),
		WorkingUIDs: func() []int64 { return []int64{43} },
	})

	if stub.callCount() != 0 {
		t.Fatalf("the judge was called with only the author left: %d calls", stub.callCount())
	}
	if decision.Source != activationSourceNoCandidate {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceNoCandidate)
	}
}

// A single-bot group still activates with no judgement at all. The filter must
// not turn the common case into a model call.
func TestSingleBotGroupStillSkipsJudging(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members: []*types.GroupMember{
			{UserID: 7, DisplayName: "林"},
			{UserID: 42, IsBot: true, DisplayName: "阿码"},
		},
	})

	if stub.callCount() != 0 {
		t.Fatalf("a single-bot group called the judge: %d calls", stub.callCount())
	}
	if _, ok := decision.Activated[42]; !ok {
		t.Fatalf("the lone bot was not activated: %v", decision.Activated)
	}
	if decision.Source != activationSourceSingleBot {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceSingleBot)
	}
}

// A muted lone bot is unreachable, because every reply it produced would be
// rejected. Without this the group would look addressed while staying silent.
func TestMutedLoneBotIsNotActivated(t *testing.T) {
	stub := newJudgeStub(t, 0.95)
	resolver := stub.resolver()

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members: []*types.GroupMember{
			{UserID: 7, DisplayName: "林"},
			{UserID: 42, IsBot: true, DisplayName: "阿码"},
		},
		MutedUIDs: []int64{42},
	})

	if len(decision.Activated) != 0 {
		t.Fatalf("a muted lone bot was activated: %v", decision.Activated)
	}
	if decision.Source != activationSourceNoCandidate {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceNoCandidate)
	}
}

// The deterministic path (no judge installed) must apply the same filters, or
// the fallback would route work to a bot that cannot answer.
func TestDeterministicPathAppliesTheSameFilters(t *testing.T) {
	decision := deterministicGroupActivation(GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members:   judgeTestMembers(),
		Mentions:  []string{"usr42"},
		MutedUIDs: []int64{42},
	})
	if len(decision.Activated) != 0 {
		t.Fatalf("the deterministic path activated a muted member: %v", decision.Activated)
	}
	if decision.Source != activationSourceNoCandidate {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceNoCandidate)
	}

	// A muted member is dropped from the unaddressed fallback too.
	unaddressed := deterministicGroupActivation(GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members:   judgeTestMembers(),
		MutedUIDs: []int64{42},
	})
	if _, ok := unaddressed.Activated[42]; ok {
		t.Fatalf("the deterministic path activated a muted member: %v", unaddressed.Activated)
	}

	// Working traffic is not conversation here either.
	notConversation := deterministicGroupActivation(GroupActivationRequest{
		GroupID:         80,
		SenderUID:       7,
		Members:         judgeTestMembers(),
		NotConversation: true,
	})
	if len(notConversation.Activated) != 0 {
		t.Fatalf("the deterministic path activated on working traffic: %v", notConversation.Activated)
	}
	if notConversation.Source != activationSourceNotConversation {
		t.Fatalf("source = %s, want %s", notConversation.Source, activationSourceNotConversation)
	}
}

// The turn tracker is what tells the activation path who is busy. A reserved
// turn counts, and a cleared one stops counting so a finished bot is reachable
// again.
func TestTurnTrackerReportsActiveBots(t *testing.T) {
	tracker := newGroupAgentTurnTracker(0)
	if got := tracker.activeBots(80); len(got) != 0 {
		t.Fatalf("a fresh tracker reported %v as busy", got)
	}
	if !tracker.begin(80, 42, 7, 1) {
		t.Fatalf("reserving the turn failed")
	}
	active := tracker.activeBots(80)
	if len(active) != 1 || active[0] != 42 {
		t.Fatalf("activeBots = %v, want [42]", active)
	}
	tracker.clear(80, 42)
	if got := tracker.activeBots(80); len(got) != 0 {
		t.Fatalf("a cleared turn still counted as busy: %v", got)
	}
}

// End to end through the broadcaster: a tool message must be delivered so other
// bots can read it, but must not activate anyone.
func TestToolMessageDeliversWithoutActivating(t *testing.T) {
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
	stub := newJudgeStub(t, 0.95)
	hub.groupActivation = stub.resolver()

	botA := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	botB := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(botA)
	hub.addClient(botB)

	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "grp_80",
		Type:    "tool_use",
		Content: json.RawMessage(`"execute_shell"`),
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 30, nil)

	if stub.callCount() != 0 {
		t.Fatalf("a tool message reached the judge: %d calls", stub.callCount())
	}
	// Delivery still happens: bots must be able to read the conversation.
	deliveredA := assertBotActivation(t, botA.send, false)
	if deliveredA.Data == nil {
		t.Fatalf("the tool message was not delivered")
	}
	assertBotActivation(t, botB.send, false)
}

// The same end-to-end path for a muted member: the message is delivered so the
// group stays readable, but the muted bot is never marked as addressed.
func TestMutedMemberIsNotMarkedActivated(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true, Muted: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	hub := NewHub(store, nil)
	stub := newJudgeStub(t, 0.95)
	hub.groupActivation = stub.resolver()

	botA := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	botB := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 4)}
	hub.addClient(botA)
	hub.addClient(botB)

	// The human names the muted bot directly; it must still not be activated.
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID:  "grp_80",
		Content:  json.RawMessage(`"@usr42 请处理"`),
		Mentions: []string{"usr42"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	hub.fanoutNormalizedMessage(7, "grp_80", 0, payload, 31, nil)

	// The muted bot is delivered the message but not addressed by it.
	assertBotActivation(t, botA.send, false)
	// The idle bot must not be activated either. Naming the muted member is a
	// designation, so it cannot silently become a request for someone else.
	assertBotActivation(t, botB.send, false)
	if stub.callCount() != 0 {
		t.Fatalf("a muted-only mention reached the judge: %d calls", stub.callCount())
	}
}

// activationMutedUIDs reads the roster rather than the muted flag on the
// message, so a mute takes effect on the very next message.
func TestActivationMutedUIDsReadsTheRoster(t *testing.T) {
	uids := activationMutedUIDs([]*types.GroupMember{
		nil,
		{UserID: 7},
		{UserID: 42, IsBot: true, Muted: true},
		{UserID: 43, IsBot: true},
	})
	if len(uids) != 1 || uids[0] != 42 {
		t.Fatalf("activationMutedUIDs = %v, want [42]", uids)
	}
}

// groupTopicID must match the form the group handlers store, or a topic-scoped
// lookup would silently find nothing and the busy filter would stop working.
func TestGroupTopicIDMatchesStoredForm(t *testing.T) {
	if got := groupTopicID(4852); got != "grp_4852" {
		t.Fatalf("groupTopicID(4852) = %q, want grp_4852", got)
	}
	if got := groupTopicID(0); got != "" {
		t.Fatalf("groupTopicID(0) = %q, want empty", got)
	}
	if extractGroupID(groupTopicID(4852)) != 4852 {
		t.Fatalf("groupTopicID does not round-trip through extractGroupID")
	}
}

// The busy filter must survive the store being absent, which is the case for
// focused test stores and any deployment that does not implement the optional
// interface. It degrades to "nobody is known to be working", the pre-change
// behaviour, rather than blocking activation.
func TestWorkingLookupDegradesWhenStoreIsUnavailable(t *testing.T) {
	hub := NewHub(&identityMessageStore{}, nil)
	if got := hub.activationWorkingUIDs(80); len(got) != 0 {
		t.Fatalf("activationWorkingUIDs = %v, want none", got)
	}
	// A reserved turn is still reported even without the store.
	hub.groupTurns.begin(80, 42, 7, 1)
	got := hub.activationWorkingUIDs(80)
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("activationWorkingUIDs = %v, want [42]", got)
	}
}

// A store error must not be fatal: activation continues with what it knows.
func TestWorkingLookupIgnoresStoreErrors(t *testing.T) {
	hub := NewHub(&failingTaskStatusStore{
		identityMessageStore: &identityMessageStore{},
	}, nil)
	if got := hub.activationWorkingUIDs(80); len(got) != 0 {
		t.Fatalf("a store error changed the working set: %v", got)
	}
}

type failingTaskStatusStore struct {
	*identityMessageStore
}

func (s *failingTaskStatusStore) ListActiveConversationTaskStatusSources(string) ([]int64, error) {
	return nil, io.ErrUnexpectedEOF
}

// The busy-member lookup costs a store query, so it must only run on the path
// that actually judges. Agent working traffic is the bulk of a busy group's
// messages, and paying a query per tool call would trade one waste for another.
func TestWorkingLookupOnlyRunsWhenJudging(t *testing.T) {
	cases := []struct {
		name      string
		req       GroupActivationRequest
		wantProbe bool
	}{
		{
			name: "working traffic short-circuits before the lookup",
			req: GroupActivationRequest{
				GroupID: 80, SenderUID: 7, Members: judgeTestMembers(), NotConversation: true,
			},
		},
		{
			name: "a single-bot group needs no lookup",
			req: GroupActivationRequest{
				GroupID: 80, SenderUID: 7,
				Members: []*types.GroupMember{
					{UserID: 7, DisplayName: "林"},
					{UserID: 42, IsBot: true, DisplayName: "阿码"},
				},
			},
		},
		{
			name: "an explicit mention needs no lookup",
			req: GroupActivationRequest{
				GroupID: 80, SenderUID: 7, Members: judgeTestMembers(), Mentions: []string{"usr42"},
			},
		},
		{
			name: "the judging path does consult it",
			req: GroupActivationRequest{
				GroupID: 80, SenderUID: 7, Members: judgeTestMembers(),
			},
			wantProbe: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newJudgeStub(t, 0.5)
			resolver := stub.resolver()
			probed := false
			tc.req.WorkingUIDs = func() []int64 {
				probed = true
				return nil
			}
			resolver.Resolve(context.Background(), tc.req)
			if probed != tc.wantProbe {
				t.Fatalf("working lookup ran = %v, want %v", probed, tc.wantProbe)
			}
		})
	}
}

// The judge's own request must not contain a candidate that was filtered out.
// This is the contract the whole change rests on: the filters are about what
// the model is asked, not about post-processing its answer.
func TestJudgeRequestOmitsFilteredCandidates(t *testing.T) {
	var captured jevRequestPayload
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		answers := make(map[string]jevAnswer, len(captured.Questions))
		for name := range captured.Questions {
			value := 0.95
			answers[name] = jevAnswer{Type: "noul", Noul: &value}
		}
		_ = json.NewEncoder(w).Encode(jevResponsePayload{Answers: answers})
	}))
	defer stub.Close()

	resolver := &JevGroupActivationResolver{
		client: &JevClient{
			baseURL:    stub.URL,
			path:       defaultJevPath,
			model:      "jev-latest",
			attempts:   1,
			httpClient: stub.Client(),
		},
		threshold: defaultActivationThreshold,
	}
	resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:     80,
		SenderUID:   7,
		Members:     judgeTestMembers(),
		MutedUIDs:   []int64{42},
		WorkingUIDs: func() []int64 { return []int64{43} },
	})

	if len(captured.Questions) != 0 {
		t.Fatalf("filtered candidates were still asked about: %v", captured.Questions)
	}
	state, _ := captured.State.(string)
	if state != "" && !strings.Contains(state, "{") {
		t.Fatalf("unexpected state encoding: %q", state)
	}
}
