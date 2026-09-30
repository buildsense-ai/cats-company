package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetActivationPromptCache clears the memo between cases. The cache is a
// package-level value so that a running server reads the file once per second;
// a test that changes the file needs to start from a clean slate.
func resetActivationPromptCache(t *testing.T) {
	t.Helper()
	activationPromptCache.mu.Lock()
	activationPromptCache.path = ""
	activationPromptCache.loadedAt = time.Time{}
	activationPromptCache.prompt = ActivationPrompt{}
	activationPromptCache.mu.Unlock()
}

// Without an override file the shipped wording is used.
func TestActivationPromptDefaultsWithoutOverride(t *testing.T) {
	t.Setenv(defaultJevPromptFileEnv, "")
	resetActivationPromptCache(t)

	prompt := loadActivationPrompt()
	if prompt.Instructions != defaultActivationInstructions {
		t.Fatalf("instructions = %q, want the default", prompt.Instructions)
	}
	if prompt.CriteriaTrue != defaultActivationCriteriaTrue {
		t.Fatalf("criteria_true = %q, want the default", prompt.CriteriaTrue)
	}
	if prompt.CriteriaFalse != defaultActivationCriteriaFalse {
		t.Fatalf("criteria_false = %q, want the default", prompt.CriteriaFalse)
	}
}

// A file overrides the wording, which is what lets an operator tune the prompt
// on a running host instead of opening a pull request.
func TestActivationPromptFileOverridesWording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.json")
	body := `{"instructions":"{name} 要不要回？","criteria_true":"要","criteria_false":"不要"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	t.Setenv(defaultJevPromptFileEnv, path)
	resetActivationPromptCache(t)

	prompt := loadActivationPrompt()
	if prompt.Instructions != "{name} 要不要回？" {
		t.Fatalf("instructions = %q", prompt.Instructions)
	}
	if prompt.CriteriaTrue != "要" || prompt.CriteriaFalse != "不要" {
		t.Fatalf("criteria = %q / %q", prompt.CriteriaTrue, prompt.CriteriaFalse)
	}
}

// A partial file keeps the fields it did not set, so tuning one criterion does
// not mean restating the others.
func TestActivationPromptFileOverridesOnlyWhatItSets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.json")
	if err := os.WriteFile(path, []byte(`{"criteria_false":"never"}`), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	t.Setenv(defaultJevPromptFileEnv, path)
	resetActivationPromptCache(t)

	prompt := loadActivationPrompt()
	if prompt.CriteriaFalse != "never" {
		t.Fatalf("criteria_false = %q", prompt.CriteriaFalse)
	}
	if prompt.Instructions != defaultActivationInstructions {
		t.Fatalf("instructions should keep the default, got %q", prompt.Instructions)
	}
	if prompt.CriteriaTrue != defaultActivationCriteriaTrue {
		t.Fatalf("criteria_true should keep the default, got %q", prompt.CriteriaTrue)
	}
}

// A broken override must not take judging down. An unreadable or malformed file
// falls back to the shipped wording rather than silencing the group.
func TestActivationPromptFallsBackWhenFileIsUnusable(t *testing.T) {
	cases := map[string]string{
		"missing":    "",
		"malformed":  "{not json",
		"blank body": `{"instructions":"   "}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prompt.json")
			if body != "" {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatalf("write override: %v", err)
				}
			}
			t.Setenv(defaultJevPromptFileEnv, path)
			resetActivationPromptCache(t)

			prompt := loadActivationPrompt()
			if prompt.Instructions != defaultActivationInstructions {
				t.Fatalf("instructions = %q, want the default", prompt.Instructions)
			}
			if prompt.CriteriaTrue != defaultActivationCriteriaTrue {
				t.Fatalf("criteria_true = %q, want the default", prompt.CriteriaTrue)
			}
		})
	}
}

// The placeholder is what keeps one template usable for every member.
func TestRenderActivationPromptSubstitutesName(t *testing.T) {
	got := renderActivationPrompt("Should {name} reply?", "阿码")
	if got != "Should 阿码 reply?" {
		t.Fatalf("render = %q", got)
	}
	// A template with no placeholder is returned unchanged.
	if got := renderActivationPrompt("static", "阿码"); got != "static" {
		t.Fatalf("render = %q", got)
	}
}

// Every question must name its own bot, so the answers stay comparable and the
// judge is never asked about an unnamed member.
func TestJudgeBuildsOneNamedQuestionPerBot(t *testing.T) {
	t.Setenv(defaultJevPromptFileEnv, "")
	resetActivationPromptCache(t)

	prompt := loadActivationPrompt()
	bots := []GroupActivationBot{{UID: 42, DisplayName: "阿码"}, {UID: 43, DisplayName: "小文"}}
	questions := make(map[string]JevQuestion, len(bots))
	for _, bot := range bots {
		name := activationBotName(bot)
		questions[activationQuestionKey(bot.UID)] = JevQuestion{
			Type:         "noul",
			Instructions: renderActivationPrompt(prompt.Instructions, name),
			Criteria: map[string]string{
				"true":  renderActivationPrompt(prompt.CriteriaTrue, name),
				"false": renderActivationPrompt(prompt.CriteriaFalse, name),
			},
		}
	}

	if len(questions) != 2 {
		t.Fatalf("expected two questions, got %d", len(questions))
	}
	for uid, name := range map[int64]string{42: "阿码", 43: "小文"} {
		question, ok := questions[activationQuestionKey(uid)]
		if !ok {
			t.Fatalf("missing question for bot %d", uid)
		}
		if !strings.Contains(question.Instructions, name) {
			t.Fatalf("instructions do not name %s: %q", name, question.Instructions)
		}
		criteria, _ := question.Criteria.(map[string]string)
		if !strings.Contains(criteria["true"], name) {
			t.Fatalf("criteria_true does not name %s: %q", name, criteria["true"])
		}
		if strings.Contains(question.Instructions, activationPromptNamePlaceholder) {
			t.Fatalf("placeholder left unsubstituted: %q", question.Instructions)
		}
	}
}
