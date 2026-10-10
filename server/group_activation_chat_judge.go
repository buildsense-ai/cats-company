package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The activation judge is a transport concern, not a policy one. Jev answers
// the per-bot questions natively; a chat model can answer the same questions
// when asked for a strict JSON verdict. Keeping both behind one interface means
// the activation rules, the prompt wording and the scoring path stay identical
// whichever one is installed.
//
// The chat backend exists because the Jev lane can go away for reasons outside
// this service — the upstream began answering 451 "not available in your
// region", which turned every multi-bot message into a retry-then-degrade
// cycle. A deployment needs a way to keep judging with a model it can still
// reach.

const (
	defaultChatJudgePath     = "/anthropic/v1/messages"
	defaultChatJudgeModel    = "deepseek-flash"
	defaultChatJudgeAttempts = 3
	// defaultChatJudgeTimeout bounds one attempt. Activation runs on the message
	// hot path and blocks delivery, so the bound matters more than headroom:
	// three attempts at this value cap the worst case near the Jev path's. A
	// measured deepseek-flash verdict takes about 0.9s, so this leaves room for a
	// slow call without letting a hung one hold the group for long.
	defaultChatJudgeTimeout = 3 * time.Second
	// maxChatJudgeAnswerBytes bounds the verdict we will read back. The answer
	// is a small JSON object; anything larger means the model ignored the
	// instruction and is not a verdict we can use.
	maxChatJudgeAnswerBytes = 16 * 1024
)

// activationJudge answers the per-bot activation questions.
type activationJudge interface {
	// Enabled reports whether the judge can issue requests at all.
	Enabled() bool
	// Ask answers every named question against one piece of state. The returned
	// map is keyed the same way as the questions map.
	Ask(ctx context.Context, state string, questions map[string]JevQuestion) (map[string]jevAnswer, error)
}

// chatJudge answers activation questions with a chat model over the relay's
// Anthropic-protocol endpoint.
//
// It is deliberately a thin adapter: it renders the same questions the Jev
// backend receives into a strict JSON instruction, then converts the reply back
// into the same answer shape. Nothing about which bot should reply is decided
// here.
type chatJudge struct {
	baseURL    string
	apiKey     string
	model      string
	path       string
	attempts   int
	httpClient *http.Client
}

// NewChatJudgeFromEnv builds the chat-model judge from environment
// configuration. It returns nil unless judging is explicitly enabled and the
// chat backend is selected, so an existing deployment is unaffected.
func NewChatJudgeFromEnv() *chatJudge {
	if !jevEnabled() {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("CATS_JEV_BACKEND")), "chat") {
		return nil
	}
	baseURL := strings.TrimSpace(os.Getenv("CATS_JEV_RELAY_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimRight(relayBaseURL(), "/")
	}
	if baseURL == "" {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("CATS_JEV_CHAT_PATH"))
	if path == "" {
		path = defaultChatJudgePath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	model := strings.TrimSpace(os.Getenv("CATS_JEV_MODEL"))
	if model == "" {
		model = defaultChatJudgeModel
	}
	attempts := defaultChatJudgeAttempts
	if raw := strings.TrimSpace(os.Getenv("CATS_JEV_MAX_ATTEMPTS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 10 {
			attempts = parsed
		}
	}
	timeout := defaultChatJudgeTimeout
	if raw := strings.TrimSpace(os.Getenv("CATS_JEV_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
			timeout = time.Duration(seconds * float64(time.Second))
		}
	}
	return &chatJudge{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   strings.TrimSpace(os.Getenv("CATS_JEV_API_KEY")),
		model:    model,
		path:     path,
		attempts: attempts,
		httpClient: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   3 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   3 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
			Timeout: timeout,
		},
	}
}

// Enabled reports whether the client can issue requests.
func (c *chatJudge) Enabled() bool {
	return c != nil && c.baseURL != "" && c.httpClient != nil
}

type chatJudgeRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	Thinking  map[string]string `json:"thinking,omitempty"`
	Messages  []chatJudgeTurn   `json:"messages"`
}

type chatJudgeTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatJudgeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Ask renders the questions into one instruction and reads back a JSON verdict.
func (c *chatJudge) Ask(ctx context.Context, state string, questions map[string]JevQuestion) (map[string]jevAnswer, error) {
	if !c.Enabled() {
		return nil, errors.New("chat judge is not configured")
	}
	if len(questions) == 0 {
		return nil, errors.New("chat judge needs at least one question")
	}
	if len(state) > maxJevStateBytes {
		return nil, fmt.Errorf("chat judge state exceeds %d bytes", maxJevStateBytes)
	}

	prompt := buildChatJudgePrompt(state, questions)
	body, err := json.Marshal(chatJudgeRequest{
		Model:     c.model,
		MaxTokens: 512,
		Thinking:  map[string]string{"type": "disabled"},
		Messages:  []chatJudgeTurn{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("encode chat judge request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		answers, retryable, err := c.attempt(ctx, body, questions)
		if err == nil {
			return answers, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("chat judge request failed after %d attempts: %w", c.attempts, lastErr)
}

func (c *chatJudge) attempt(ctx context.Context, body []byte, questions map[string]JevQuestion) (map[string]jevAnswer, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.path, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("build chat judge request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	if c.apiKey != "" {
		request.Header.Set("x-api-key", c.apiKey)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, true, fmt.Errorf("chat judge transport: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
	}()

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxChatJudgeAnswerBytes))
	if readErr != nil {
		return nil, true, fmt.Errorf("read chat judge response: %w", readErr)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		// fall through to decoding
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return nil, false, fmt.Errorf("chat judge rejected the relay credential (%d)", response.StatusCode)
	case response.StatusCode == http.StatusBadRequest, response.StatusCode == http.StatusUnprocessableEntity:
		return nil, false, fmt.Errorf("chat judge rejected the request shape (%d): %s", response.StatusCode, truncateUTF8(string(raw), 200))
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return nil, true, fmt.Errorf("chat judge transient failure (%d)", response.StatusCode)
	default:
		return nil, false, fmt.Errorf("chat judge unexpected status %d: %s", response.StatusCode, truncateUTF8(string(raw), 200))
	}

	var decoded chatJudgeResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, true, fmt.Errorf("decode chat judge response: %w", err)
	}
	text := ""
	for _, block := range decoded.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil, true, errors.New("chat judge returned no text")
	}

	scores, err := parseChatJudgeVerdict(text, questions)
	if err != nil {
		// A malformed verdict is worth one more attempt: the model is asked for
		// a strict shape and usually complies.
		return nil, true, err
	}
	answers := make(map[string]jevAnswer, len(scores))
	for name, value := range scores {
		score := value
		answers[name] = jevAnswer{Type: "noul", Noul: &score}
	}
	return answers, false, nil
}

// buildChatJudgePrompt renders the state and the per-bot questions into one
// instruction whose answer is a JSON object keyed by question name.
//
// The wording is deliberately minimal: the same criteria the Jev backend gets,
// plus the exact output shape. Everything that decides who should reply lives
// in the criteria, so both backends judge the same question.
func buildChatJudgePrompt(state string, questions map[string]JevQuestion) string {
	var builder strings.Builder
	builder.WriteString("Decide which members should reply to the latest message in this group chat.\n\n")
	builder.WriteString("State:\n")
	builder.WriteString(state)
	builder.WriteString("\n\nQuestions:\n")

	// Deterministic order keeps the prompt stable between runs, which matters
	// when comparing a chat verdict against a Jev one.
	for _, name := range sortedQuestionKeys(questions) {
		question := questions[name]
		builder.WriteString(fmt.Sprintf("- %q: %s\n", name, strings.TrimSpace(question.Instructions)))
		if criteria, ok := question.Criteria.(map[string]string); ok {
			if value := strings.TrimSpace(criteria["true"]); value != "" {
				builder.WriteString(fmt.Sprintf("  answer 1 when: %s\n", value))
			}
			if value := strings.TrimSpace(criteria["false"]); value != "" {
				builder.WriteString(fmt.Sprintf("  answer 0 when: %s\n", value))
			}
		}
	}

	builder.WriteString("\nReply with ONLY a JSON object mapping each question key to 0 or 1, ")
	builder.WriteString("for example {\"bot_1\": 1, \"bot_2\": 0}. No prose, no markdown fences.")
	return builder.String()
}

// parseChatJudgeVerdict reads the JSON object out of the model's reply.
//
// It scans for the last balanced object so a model that narrates before
// answering still yields a verdict, and it ignores keys that were not asked
// about so a stray name cannot invent a candidate.
func parseChatJudgeVerdict(text string, questions map[string]JevQuestion) (map[string]float64, error) {
	object, err := lastJSONObject(text)
	if err != nil {
		return nil, fmt.Errorf("chat judge verdict is not JSON: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(object), &raw); err != nil {
		return nil, fmt.Errorf("chat judge verdict is not a JSON object: %w", err)
	}

	scores := make(map[string]float64, len(questions))
	for name := range questions {
		value, ok := raw[name]
		if !ok {
			continue
		}
		score, err := decodeVerdictValue(value)
		if err != nil {
			return nil, fmt.Errorf("chat judge verdict for %s: %w", name, err)
		}
		scores[name] = score
	}
	if len(scores) == 0 {
		return nil, errors.New("chat judge verdict answered none of the questions")
	}
	return scores, nil
}

// decodeVerdictValue accepts a number or a boolean. Both appear in practice and
// both mean the same thing here.
func decodeVerdictValue(raw json.RawMessage) (float64, error) {
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		if boolean {
			return 1, nil
		}
		return 0, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "1", "true", "yes":
			return 1, nil
		case "0", "false", "no":
			return 0, nil
		}
	}
	return 0, fmt.Errorf("unsupported value %s", truncateUTF8(string(raw), 40))
}

// lastJSONObject returns the last balanced {...} run in the text.
func lastJSONObject(text string) (string, error) {
	depth := 0
	start := -1
	last := ""
	for index, char := range text {
		switch char {
		case '{':
			if depth == 0 {
				start = index
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					last = text[start : index+1]
				}
			}
		}
	}
	if last == "" {
		return "", errors.New("no JSON object found")
	}
	return last, nil
}

// sortedQuestionKeys returns the question names in a stable order.
func sortedQuestionKeys(questions map[string]JevQuestion) []string {
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}
