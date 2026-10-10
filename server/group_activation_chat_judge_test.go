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

// chatJudgeStub stands in for the relay's Anthropic endpoint.
type chatJudgeStub struct {
	server *httptest.Server
	calls  int32
	reply  string
	status int
}

func newChatJudgeStub(t *testing.T, reply string) *chatJudgeStub {
	t.Helper()
	stub := &chatJudgeStub{reply: reply}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.calls, 1)
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("read chat judge request: %v", err)
		}
		if stub.status != 0 && stub.status != http.StatusOK {
			w.WriteHeader(stub.status)
			_, _ = w.Write([]byte(`{"error":"stub"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": stub.reply}},
		})
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *chatJudgeStub) judge() *chatJudge {
	return &chatJudge{
		baseURL:    s.server.URL,
		path:       defaultChatJudgePath,
		model:      defaultChatJudgeModel,
		attempts:   1,
		httpClient: s.server.Client(),
	}
}

func (s *chatJudgeStub) callCount() int {
	return int(atomic.LoadInt32(&s.calls))
}

// The chat backend must be off unless both judging and the backend are
// explicitly selected, so a deployment that never opted in keeps its behaviour.
func TestChatJudgeRequiresExplicitSelection(t *testing.T) {
	t.Setenv("CATS_JEV_ENABLED", "")
	t.Setenv("CATS_JEV_BACKEND", "chat")
	t.Setenv("CATS_JEV_RELAY_BASE_URL", "https://relay.example")
	if judge := NewChatJudgeFromEnv(); judge != nil {
		t.Fatalf("the chat judge was built while judging is disabled")
	}

	t.Setenv("CATS_JEV_ENABLED", "true")
	t.Setenv("CATS_JEV_BACKEND", "")
	if judge := NewChatJudgeFromEnv(); judge != nil {
		t.Fatalf("the chat judge was built without CATS_JEV_BACKEND=chat")
	}

	t.Setenv("CATS_JEV_BACKEND", "chat")
	judge := NewChatJudgeFromEnv()
	if judge == nil || !judge.Enabled() {
		t.Fatalf("the chat judge was not built for CATS_JEV_BACKEND=chat")
	}
	if judge.model != defaultChatJudgeModel {
		t.Fatalf("model = %q, want %q", judge.model, defaultChatJudgeModel)
	}
}

// A verdict keyed by question name is the whole contract between the chat
// backend and the activation rules.
func TestChatJudgeParsesAVerdict(t *testing.T) {
	stub := newChatJudgeStub(t, `{"bot_365": 1, "bot_553": 0}`)
	answers, err := stub.judge().Ask(context.Background(), `{"group":{"name":"g"}}`, map[string]JevQuestion{
		"bot_365": {Type: "noul", Instructions: "Should Saturday reply?"},
		"bot_553": {Type: "noul", Instructions: "Should Monday reply?"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if value := answers["bot_365"].NoulAnswer(); !value.Valid || value.Value != 1 {
		t.Fatalf("bot_365 = %#v, want 1", value)
	}
	if value := answers["bot_553"].NoulAnswer(); !value.Valid || value.Value != 0 {
		t.Fatalf("bot_553 = %#v, want 0", value)
	}
}

// Models narrate. The verdict is the last balanced object, so prose around it
// must not turn a usable answer into a failure.
func TestChatJudgeReadsVerdictFromNarration(t *testing.T) {
	stub := newChatJudgeStub(t, "Looking at the room, only Monday should answer.\n\n{\"bot_365\": 0, \"bot_553\": 1}\n")
	answers, err := stub.judge().Ask(context.Background(), "{}", map[string]JevQuestion{
		"bot_365": {Type: "noul", Instructions: "Should Saturday reply?"},
		"bot_553": {Type: "noul", Instructions: "Should Monday reply?"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if value := answers["bot_553"].NoulAnswer(); !value.Valid || value.Value != 1 {
		t.Fatalf("bot_553 = %#v, want 1", value)
	}
}

// The verdict decoder accepts the shapes a model actually emits.
func TestChatJudgeAcceptsBooleansAndStrings(t *testing.T) {
	cases := map[string]float64{
		`{"bot_1": 1}`:     1,
		`{"bot_1": 0}`:     0,
		`{"bot_1": true}`:  1,
		`{"bot_1": false}`: 0,
		`{"bot_1": "yes"}`: 1,
		`{"bot_1": "no"}`:  0,
	}
	for reply, want := range cases {
		scores, err := parseChatJudgeVerdict(reply, map[string]JevQuestion{"bot_1": {Type: "noul"}})
		if err != nil {
			t.Fatalf("%s: %v", reply, err)
		}
		if scores["bot_1"] != want {
			t.Fatalf("%s -> %v, want %v", reply, scores["bot_1"], want)
		}
	}
}

// A name that was not asked about must not become a candidate.
func TestChatJudgeIgnoresUnknownKeys(t *testing.T) {
	scores, err := parseChatJudgeVerdict(
		`{"bot_1": 1, "bot_999": 1, "Saturday": 1}`,
		map[string]JevQuestion{"bot_1": {Type: "noul"}},
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(scores) != 1 || scores["bot_1"] != 1 {
		t.Fatalf("scores = %v, want only bot_1", scores)
	}
}

// A reply that answers nobody is a failure, not a silent "nobody should reply".
// The two must stay distinguishable or a broken model looks like a quiet room.
func TestChatJudgeRejectsAnEmptyVerdict(t *testing.T) {
	if _, err := parseChatJudgeVerdict(`{"unrelated": 1}`, map[string]JevQuestion{"bot_1": {Type: "noul"}}); err == nil {
		t.Fatalf("an empty verdict was accepted")
	}
	if _, err := parseChatJudgeVerdict("no json here", map[string]JevQuestion{"bot_1": {Type: "noul"}}); err == nil {
		t.Fatalf("a non-JSON reply was accepted")
	}
}

// A malformed verdict is retried, because the model was asked for a strict
// shape and usually complies on a second try.
func TestChatJudgeRetriesAMalformedVerdict(t *testing.T) {
	stub := newChatJudgeStub(t, "I cannot answer that.")
	judge := stub.judge()
	judge.attempts = 2
	if _, err := judge.Ask(context.Background(), "{}", map[string]JevQuestion{"bot_1": {Type: "noul"}}); err == nil {
		t.Fatalf("a malformed verdict was accepted")
	}
	if stub.callCount() != 2 {
		t.Fatalf("calls = %d, want 2 attempts", stub.callCount())
	}
}

// A credential failure is not transient and must not be retried.
func TestChatJudgeDoesNotRetryOnAuthFailure(t *testing.T) {
	stub := newChatJudgeStub(t, "{}")
	stub.status = http.StatusUnauthorized
	judge := stub.judge()
	judge.attempts = 3
	if _, err := judge.Ask(context.Background(), "{}", map[string]JevQuestion{"bot_1": {Type: "noul"}}); err == nil {
		t.Fatalf("an auth failure was ignored")
	}
	if stub.callCount() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", stub.callCount())
	}
}

// The prompt must carry the criteria, not a paraphrase: both backends have to
// judge the same question for their verdicts to be comparable.
func TestChatJudgePromptCarriesTheCriteria(t *testing.T) {
	prompt := buildChatJudgePrompt(`{"group":{"name":"g"}}`, map[string]JevQuestion{
		"bot_553": {Type: "noul", Instructions: "Should Monday reply?", Criteria: map[string]string{
			"true":  "the message names Monday",
			"false": "the message is aimed at another member",
		}},
		"bot_365": {Type: "noul", Instructions: "Should Saturday reply?", Criteria: map[string]string{
			"true":  "the message names Saturday",
			"false": "the message is aimed at another member",
		}},
	})
	for _, want := range []string{
		"Should Monday reply?", "the message names Monday", "the message is aimed at another member",
		"Should Saturday reply?", "bot_553", "bot_365", "JSON",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
	// Stable order keeps runs comparable.
	if strings.Index(prompt, "bot_365") > strings.Index(prompt, "bot_553") {
		t.Fatalf("question order is not stable:\n%s", prompt)
	}
}

// The resolver must accept either backend through the same interface.
func TestResolverAcceptsAnyJudge(t *testing.T) {
	stub := newChatJudgeStub(t, `{"bot_553": 1, "bot_365": 0}`)
	resolver := NewGroupActivationResolver(stub.judge())
	if !resolver.client.Enabled() {
		t.Fatalf("the resolver rejected the chat judge")
	}

	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members: []*types.GroupMember{
			{UserID: 7, DisplayName: "林"},
			{UserID: 365, IsBot: true, DisplayName: "Saturday"},
			{UserID: 553, IsBot: true, DisplayName: "Monday"},
		},
	})
	if decision.Source != activationSourceJev {
		t.Fatalf("source = %s, want %s", decision.Source, activationSourceJev)
	}
	if _, ok := decision.Activated[553]; !ok {
		t.Fatalf("the chat verdict did not activate Monday: %v", decision.Activated)
	}
	if _, ok := decision.Activated[365]; ok {
		t.Fatalf("the chat verdict wrongly activated Saturday: %v", decision.Activated)
	}
}

// A resolver with no judge installed must report degraded rather than panic.
func TestResolverWithoutAJudgeReportsDegraded(t *testing.T) {
	resolver := NewGroupActivationResolver(nil)
	decision := resolver.Resolve(context.Background(), GroupActivationRequest{
		GroupID:   80,
		SenderUID: 7,
		Members: []*types.GroupMember{
			{UserID: 7, DisplayName: "林"},
			{UserID: 365, IsBot: true, DisplayName: "Saturday"},
			{UserID: 553, IsBot: true, DisplayName: "Monday"},
		},
	})
	if !decision.Degraded || decision.Source != activationSourceDegraded {
		t.Fatalf("decision = %#v, want degraded", decision)
	}
}
