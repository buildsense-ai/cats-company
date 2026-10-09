package server

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// The activation log is what makes a judgement inspectable. Without it an empty
// result is ambiguous: a correct "nobody" and a criteria that never fires look
// the same, because Activated keeps only the winners.
func TestLogActivationDecisionRecordsScoresAndSource(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	hub := &Hub{}
	hub.logActivationDecision(80, GroupActivationRequest{SenderUID: 7}, GroupActivationDecision{
		Activated: map[int64]float64{42: 0.88},
		Scores:    map[int64]float64{42: 0.88, 43: 0.12},
		Source:    activationSourceJev,
	})

	line := buf.String()
	for _, want := range []string{
		"group activation:",
		"group=80",
		"sender=usr7",
		"source=jev",
		"degraded=false",
		"42:0.88",
		"43:0.12",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line is missing %q:\n%s", want, line)
		}
	}
}

// A degraded judgement is the case an operator most needs to see, so the log
// must say so rather than looking like an ordinary empty result.
func TestLogActivationDecisionMarksDegradation(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	hub := &Hub{}
	hub.logActivationDecision(80, GroupActivationRequest{SenderUID: 42, SenderIsBot: true}, GroupActivationDecision{
		Source:   activationSourceDegraded,
		Degraded: true,
	})

	line := buf.String()
	for _, want := range []string{"degraded=true", "source=jev_unavailable", "sender=usr42(bot)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line is missing %q:\n%s", want, line)
		}
	}
}

// The judge must report every candidate's score, not only the winners: that is
// the difference between "nobody should answer" and "the prompt is broken".
func TestScoreAnswersKeepsBelowThresholdCandidates(t *testing.T) {
	resolver := &JevGroupActivationResolver{threshold: defaultActivationThreshold}

	decision := resolver.scoreAnswers(map[string]jevAnswer{
		"bot_42": {Type: "noul", Noul: ptrFloat(0.88)},
		"bot_43": {Type: "noul", Noul: ptrFloat(0.12)},
	}, []GroupActivationBot{
		{UID: 42, DisplayName: "阿码"},
		{UID: 43, DisplayName: "小文"},
	})

	if len(decision.Activated) != 1 {
		t.Fatalf("expected one activation, got %v", decision.Activated)
	}
	if _, ok := decision.Activated[42]; !ok {
		t.Fatalf("bot 42 should be activated: %v", decision.Activated)
	}
	if len(decision.Scores) != 2 {
		t.Fatalf("expected a score for both candidates, got %v", decision.Scores)
	}
	if decision.Scores[43] != 0.12 {
		t.Fatalf("below-threshold score missing or wrong: %v", decision.Scores)
	}
	if decision.Source != activationSourceJev {
		t.Fatalf("source = %s", decision.Source)
	}
}

// A group of two bots with no judge keeps its pre-existing behaviour: an
// unaddressed message reaches nobody rather than being guessed at.
func TestDeterministicActivationWithoutMentionReachesNobody(t *testing.T) {
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

func ptrFloat(value float64) *float64 { return &value }
