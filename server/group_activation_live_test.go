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
	t.Setenv("CATS_JEV_ENABLED", "true")
	t.Setenv("CATS_JEV_RELAY_BASE_URL", baseURL)
	t.Setenv("CATS_JEV_API_KEY", apiKey)
	client := NewJevClientFromEnv()
	if !client.Enabled() {
		t.Fatalf("live judge client is not configured")
	}

	resolver := NewJevGroupActivationResolver(client)

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
		wantNot     []int64
		wantNone    bool
	}{
		{
			// An open request that nobody has taken reaches someone. Which
			// member answers is the judge's call: with roles gone the decision
			// is about participation, not about matching a job title.
			name:      "open request reaches a member",
			senderUID: 7,
			turns:     []GroupActivationTurn{{Speaker: "林", Text: "这段 Python 报空指针，帮我看下"}},
			wantAny:   []int64{42, 43, 44},
		},
		{
			// A request addressed by name reaches that member and nobody else.
			name:      "named request reaches the named member",
			senderUID: 7,
			turns:     []GroupActivationTurn{{Speaker: "林", Text: "小文，帮我写一句发布公告"}},
			wantAny:   []int64{43},
			wantNot:   []int64{42, 44},
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
			wantNot: []int64{44},
		},
		{
			// A sequence keeps the later member out until its turn. Without
			// this the judge treats "阿码 goes first, then 小文" as work for
			// both and the second member answers out of order.
			name:      "later step waits its turn",
			senderUID: 7,
			turns:     []GroupActivationTurn{{Speaker: "林", Text: "分两步：阿码先整理数据，完成后交给小文写报告"}},
			wantAny:   []int64{42},
			wantNot:   []int64{43},
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
			// wantAny lists acceptable responders: the judge picks among them,
			// so any one of them satisfies the case.
			matched := false
			for _, uid := range tc.wantAny {
				if _, ok := decision.Activated[uid]; ok {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("none of %v was activated: %v", tc.wantAny, decision.Activated)
			}
			// wantNot guards the cases where reaching the wrong member is the
			// bug: a sequence must not wake the later step, and a named request
			// must not wake anyone else.
			for _, uid := range tc.wantNot {
				if _, ok := decision.Activated[uid]; ok {
					t.Fatalf("bot %d should not have been activated: %v", uid, decision.Activated)
				}
			}
		})
	}
}
