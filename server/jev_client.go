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

// Jev is TypeSafe's System One model: one POST evaluates every named question
// against a shared piece of state, returning per-question answers. Group bot
// activation uses it to decide which members a message addresses, which plain
// mention matching cannot express.
//
// Requests travel through the CatsCo relay (an adapter lane) rather than
// calling TypeSafe directly, so the upstream credential stays on the relay host
// and usage is metered by the existing relay accounting.

const (
	defaultJevPath     = "/v1/systemone"
	defaultJevAttempts = 3
	defaultJevTimeout  = 2 * time.Second
	// maxJevStateBytes bounds the state we are willing to send. The upstream
	// accepts far more, but an activation prompt that large means the caller
	// built context wrongly, not that the model needs it.
	maxJevStateBytes = 64 * 1024
)

// JevClient evaluates yes/no questions against one piece of state.
type JevClient struct {
	baseURL    string
	apiKey     string
	model      string
	path       string
	attempts   int
	perAttempt time.Duration
	httpClient *http.Client
}

// JevQuestion is one named question in a request. Exactly one of the three
// typed fields carries criteria: Choice options, Score levels, or Noul bounds.
type JevQuestion struct {
	Type         string
	Instructions string
	Criteria     interface{}
}

type jevQuestionPayload struct {
	Type         string      `json:"type"`
	Instructions string      `json:"instructions,omitempty"`
	Criteria     interface{} `json:"criteria,omitempty"`
}

type jevRequestPayload struct {
	Model     string                        `json:"model"`
	State     interface{}                   `json:"state"`
	Questions map[string]jevQuestionPayload `json:"questions"`
}

type jevAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

type jevResponsePayload struct {
	Model   string               `json:"model"`
	Answers map[string]jevAnswer `json:"answers"`
}

// JevNoulAnswer is the "should this bot respond" probability for one question.
type JevNoulAnswer struct {
	Value float64
	Valid bool
}

// NewJevClientFromEnv builds the activation judge from environment
// configuration. It returns nil when no relay endpoint is configured, which
// callers treat as "activation judging unavailable" rather than an error.
func NewJevClientFromEnv() *JevClient {
	baseURL := strings.TrimSpace(os.Getenv("CATS_JEV_RELAY_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimRight(relayBaseURL(), "/")
	}
	if baseURL == "" {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("CATS_JEV_PATH"))
	if path == "" {
		path = defaultJevPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	model := strings.TrimSpace(os.Getenv("CATS_JEV_MODEL"))
	if model == "" {
		model = "jev-latest"
	}
	attempts := defaultJevAttempts
	if raw := strings.TrimSpace(os.Getenv("CATS_JEV_MAX_ATTEMPTS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 10 {
			attempts = parsed
		}
	}
	timeout := defaultJevTimeout
	if raw := strings.TrimSpace(os.Getenv("CATS_JEV_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
			timeout = time.Duration(seconds * float64(time.Second))
		}
	}
	client := &http.Client{
		// One shared transport keeps the TLS session alive between group
		// messages. A fresh handshake per activation costs hundreds of
		// milliseconds, which dwarfs the model's own evaluation time.
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
	}
	return &JevClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     strings.TrimSpace(os.Getenv("CATS_JEV_API_KEY")),
		model:      model,
		path:       path,
		attempts:   attempts,
		perAttempt: timeout,
		httpClient: client,
	}
}

// Enabled reports whether the client can issue requests.
func (c *JevClient) Enabled() bool {
	return c != nil && c.baseURL != "" && c.httpClient != nil
}

// Ask evaluates every question and returns the answers keyed by question name.
//
// A 401/422 is a configuration or request-shape problem, so it fails without
// retrying. Timeouts, transport failures, 429 and 5xx are transient and get the
// remaining attempts immediately: the expected volume sits far below the
// upstream limit, so backing off would only add latency without protecting
// anything.
func (c *JevClient) Ask(ctx context.Context, state string, questions map[string]JevQuestion) (map[string]jevAnswer, error) {
	if !c.Enabled() {
		return nil, errors.New("jev client is not configured")
	}
	if len(questions) == 0 {
		return nil, errors.New("jev request needs at least one question")
	}
	if len(state) > maxJevStateBytes {
		return nil, fmt.Errorf("jev state exceeds %d bytes", maxJevStateBytes)
	}

	payload := jevRequestPayload{
		Model:     c.model,
		State:     state,
		Questions: make(map[string]jevQuestionPayload, len(questions)),
	}
	for name, question := range questions {
		payload.Questions[name] = jevQuestionPayload{
			Type:         question.Type,
			Instructions: question.Instructions,
			Criteria:     question.Criteria,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode jev request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		answers, retryable, err := c.attempt(ctx, body)
		if err == nil {
			return answers, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("jev request failed after %d attempts: %w", c.attempts, lastErr)
}

func (c *JevClient) attempt(ctx context.Context, body []byte) (map[string]jevAnswer, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.path, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("build jev request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		// Transport failures and timeouts are transient.
		return nil, true, fmt.Errorf("jev transport: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
	}()

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return nil, true, fmt.Errorf("read jev response: %w", readErr)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		// fall through to decoding
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return nil, false, fmt.Errorf("jev rejected the relay credential (%d)", response.StatusCode)
	case response.StatusCode == http.StatusUnprocessableEntity, response.StatusCode == http.StatusBadRequest:
		return nil, false, fmt.Errorf("jev rejected the request shape (%d): %s", response.StatusCode, truncateUTF8(string(raw), 200))
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return nil, true, fmt.Errorf("jev transient failure (%d)", response.StatusCode)
	default:
		return nil, false, fmt.Errorf("jev unexpected status %d: %s", response.StatusCode, truncateUTF8(string(raw), 200))
	}

	var decoded jevResponsePayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, true, fmt.Errorf("decode jev response: %w", err)
	}
	if len(decoded.Answers) == 0 {
		return nil, true, errors.New("jev returned no answers")
	}
	return decoded.Answers, false, nil
}

// NoulAnswer reads a yes/no answer. A missing or malformed value is reported as
// invalid so callers can treat it as "no activation" instead of zero.
func (a jevAnswer) NoulAnswer() JevNoulAnswer {
	if a.Noul == nil {
		return JevNoulAnswer{}
	}
	return JevNoulAnswer{Value: *a.Noul, Valid: true}
}
