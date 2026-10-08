package server

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// Group activation judging prompt, kept out of the compiled code so the wording
// can be tuned on a running host without a rebuild or a restart.
//
// The defaults below are the shipped prompt. A deployment may point
// CATS_JEV_PROMPT_FILE at a JSON file to override them; the file is re-read
// when it changes, so an operator edits the file and the next group message
// uses the new wording.
//
// The override is deliberately small: it carries only the question text and
// the two criteria, not the state layout. The state is assembled from live
// data (group name, roster, transcript) whose shape the code owns, and letting
// a file reshape it would let a typo break every judgement at once.

const (
	// defaultJevPromptFileEnv names the override file. Judging uses the built-in
	// wording unless a deployment points this at a file, which keeps the code
	// free of a hidden filesystem dependency.
	defaultJevPromptFileEnv = "CATS_JEV_PROMPT_FILE"
	// promptFileReloadInterval bounds how often the file is re-read. Judging
	// already costs a network round trip, so this is a cache TTL rather than a
	// budget: a change lands within a second of the edit.
	promptFileReloadInterval = time.Second
)

// ActivationPrompt is the tunable half of one judgement.
//
// Instructions and the two criteria are templates: the judge substitutes the
// bot's display name for {name}. Keeping one template per field (rather than
// one per bot) means an override stays short and every bot is asked the same
// question, which is what makes the answers comparable.
type ActivationPrompt struct {
	// Instructions is the yes/no question put to the model.
	Instructions string `json:"instructions"`
	// CriteriaTrue and CriteriaFalse describe what a yes and a no mean.
	CriteriaTrue  string `json:"criteria_true"`
	CriteriaFalse string `json:"criteria_false"`
}

// The shipped wording asks whether this member would naturally answer, rather
// than whether the message matches some category. A category list ("is this
// chat, is this a status report") has to be extended every time a new kind of
// message appears, and it silently drops anything the list forgot; asking about
// the member's own participation generalises to companion and human-facing uses
// as well.
//
// "a request that nobody has taken yet and {name} is able to handle" is what
// lets an unaddressed request reach someone. Without it the judge treats an
// open question as addressed to nobody and the group stays silent.
//
// The false branch names the two real reasons to stay out — the message is
// aimed elsewhere, or this member's step comes later — so a sequence like
// "Saturday goes first, then Monday" keeps Monday out until its turn.
const (
	// defaultActivationInstructions is the yes/no question itself.
	defaultActivationInstructions = "Should {name} reply to the latest message?"
	// defaultActivationCriteriaTrue and _False are the two descriptions a Noul
	// question carries. They are separate constants because an override may
	// replace one without the other.
	defaultActivationCriteriaTrue = "Yes: the message is something {name} would naturally respond to — it names {name}, " +
		"continues work {name} is doing, assigns the current step to {name}, or is a request that nobody has " +
		"taken yet and {name} is able to handle."
	defaultActivationCriteriaFalse = "No: the message is aimed at another member, {name}'s part comes later, or " +
		"{name} has nothing to add."
)

// activationPromptNamePlaceholder is substituted with the bot's display name.
const activationPromptNamePlaceholder = "{name}"

// promptFileOverride is the on-disk shape. Every field is optional so a file
// may tune one criterion without restating the rest.
type promptFileOverride struct {
	Instructions  *string `json:"instructions"`
	CriteriaTrue  *string `json:"criteria_true"`
	CriteriaFalse *string `json:"criteria_false"`
}

// promptCache memoises the parsed file between re-reads.
type promptCache struct {
	mu       sync.Mutex
	path     string
	loadedAt time.Time
	prompt   ActivationPrompt
}

var activationPromptCache promptCache

// loadActivationPrompt returns the prompt to use now.
//
// A missing, unreadable, or malformed file falls back to the shipped wording:
// a broken override must not take judging down, and the operator still sees
// the default behaviour rather than silence.
func loadActivationPrompt() ActivationPrompt {
	base := ActivationPrompt{
		Instructions:  defaultActivationInstructions,
		CriteriaTrue:  defaultActivationCriteriaTrue,
		CriteriaFalse: defaultActivationCriteriaFalse,
	}
	path := strings.TrimSpace(os.Getenv(defaultJevPromptFileEnv))
	if path == "" {
		return base
	}

	activationPromptCache.mu.Lock()
	defer activationPromptCache.mu.Unlock()

	now := time.Now()
	if activationPromptCache.path == path && now.Sub(activationPromptCache.loadedAt) < promptFileReloadInterval {
		return activationPromptCache.prompt
	}

	prompt := base
	if raw, err := os.ReadFile(path); err == nil {
		var override promptFileOverride
		if json.Unmarshal(raw, &override) == nil {
			applyPromptOverride(&prompt, override)
		}
	}

	activationPromptCache.path = path
	activationPromptCache.loadedAt = now
	activationPromptCache.prompt = prompt
	return prompt
}

// applyPromptOverride replaces only the fields the file actually set, and only
// when the replacement is non-blank. An empty string is far more likely to be a
// half-finished edit than an intent to ask the model nothing.
func applyPromptOverride(prompt *ActivationPrompt, override promptFileOverride) {
	if override.Instructions != nil {
		if value := strings.TrimSpace(*override.Instructions); value != "" {
			prompt.Instructions = value
		}
	}
	if override.CriteriaTrue != nil {
		if value := strings.TrimSpace(*override.CriteriaTrue); value != "" {
			prompt.CriteriaTrue = value
		}
	}
	if override.CriteriaFalse != nil {
		if value := strings.TrimSpace(*override.CriteriaFalse); value != "" {
			prompt.CriteriaFalse = value
		}
	}
}

// render substitutes the member's display name into a template.
func renderActivationPrompt(template, name string) string {
	return strings.ReplaceAll(template, activationPromptNamePlaceholder, name)
}
