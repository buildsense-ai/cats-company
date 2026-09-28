package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// TestJevResolverLiveEndToEnd exercises the real judge against the real API.
//
// It is skipped unless CATS_JEV_LIVE_API_KEY is set, because it depends on an
// external service and a credential. Everything it asserts is the behaviour the
// product needs: a request reaches the member whose job it is, a handover
// reaches the next owner, and small talk reaches nobody.
func TestJevResolverLiveEndToEnd(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("CATS_JEV_LIVE_API_KEY"))
	if apiKey == "" {
		t.Skip("set CATS_JEV_LIVE_API_KEY to run the live judge test")
	}
	baseURL := strings.TrimSpace(os.Getenv("CATS_JEV_LIVE_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.typesafe.ai"
	}
	t.Setenv("CATS_JEV_RELAY_BASE_URL", baseURL)
	t.Setenv("CATS_JEV_API_KEY", apiKey)
	client := NewJevClientFromEnv()
	if !client.Enabled() {
		t.Fatalf("live judge client is not configured")
	}

	functions := staticBotFunctions{functions: map[int64]types.BotFunction{
		42: {UID: 42, Role: "code_review", Description: "负责代码审查、bug 定位、代码质量"},
		43: {UID: 43, Role: "writing", Description: "负责营销文案、发布公告"},
		44: {UID: 44, Role: "research", Description: "负责数据统计、报表"},
	}}
	resolver := NewJevGroupActivationResolver(client, functions)

	members := []*types.GroupMember{
		{UserID: 7, DisplayName: "林"},
		{UserID: 42, IsBot: true, DisplayName: "阿码"},
		{UserID: 43, IsBot: true, DisplayName: "小文"},
		{UserID: 44, IsBot: true, DisplayName: "析数"},
	}

	cases := []struct {
		name        string
		senderUID   int64
		senderIsBot bool
		turns       []GroupActivationTurn
		wantAny     []int64
		wantNone    bool
	}{
		{
			name:      "code request reaches the reviewer",
			senderUID: 7,
			turns:     []GroupActivationTurn{{Speaker: "林", Text: "这段 Python 报空指针，帮我看下"}},
			wantAny:   []int64{42},
		},
		{
			name:      "copy request reaches the writer",
			senderUID: 7,
			turns:     []GroupActivationTurn{{Speaker: "林", Text: "帮我写一句发布公告"}},
			wantAny:   []int64{43},
		},
		{
			// A bot handing work over names the next owner in its message. The
			// handover is delivered as a bot message, so activation runs with
			// senderIsBot set and the judge reads the named owner from the text.
			name:        "handover reaches the next owner",
			senderUID:   42,
			senderIsBot: true,
			turns: []GroupActivationTurn{
				{Speaker: "林", Text: "这段代码帮我看下"},
				{Speaker: "阿码", IsBot: true, Text: "代码审查完成，发现 2 处风险。发布公告这块需要小文来写。"},
			},
			wantAny: []int64{43},
		},
		{
			name:      "small talk reaches nobody",
			senderUID: 7,
			turns:     []GroupActivationTurn{{Speaker: "林", Text: "今天下午茶谁去拿？"}},
			wantNone:  true,
		},
		{
			// A bot reporting progress names no next owner, so nobody is asked
			// to act and the chain stops.
			name:        "progress report reaches nobody",
			senderUID:   42,
			senderIsBot: true,
			turns: []GroupActivationTurn{
				{Speaker: "林", Text: "这段代码帮我看下"},
				{Speaker: "阿码", IsBot: true, Text: "代码审查已完成，未发现空指针问题。"},
			},
			wantNone: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turns := tc.turns
			decision := resolver.Resolve(context.Background(), GroupActivationRequest{
				GroupID:     80,
				SenderUID:   tc.senderUID,
				SenderIsBot: tc.senderIsBot,
				Members:     members,
				JudgeContext: func() GroupJudgeContext {
					return GroupJudgeContext{GroupName: "产品发布组", RecentTurns: turns}
				},
			})
			if decision.Degraded {
				t.Fatalf("judging degraded: %s", decision.Source)
			}
			if decision.Source != activationSourceJev {
				t.Fatalf("source = %s, want %s", decision.Source, activationSourceJev)
			}
			t.Logf("activated=%v source=%s", decision.Activated, decision.Source)

			if tc.wantNone {
				if len(decision.Activated) != 0 {
					t.Fatalf("expected no activation, got %v", decision.Activated)
				}
				return
			}
			for _, uid := range tc.wantAny {
				if _, ok := decision.Activated[uid]; !ok {
					t.Fatalf("bot %d was not activated: %v", uid, decision.Activated)
				}
			}
		})
	}
}

type staticBotFunctions struct {
	functions map[int64]types.BotFunction
}

func (s staticBotFunctions) GetBotFunctions(uids []int64) (map[int64]types.BotFunction, error) {
	out := make(map[int64]types.BotFunction, len(uids))
	for _, uid := range uids {
		if function, ok := s.functions[uid]; ok {
			out[uid] = function
		}
	}
	return out, nil
}
