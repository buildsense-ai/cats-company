package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeSTTProvider struct {
	mu       sync.Mutex
	sessions []*fakeSTTUpstream
}

func (p *fakeSTTProvider) ID() string { return "fake" }

func (p *fakeSTTProvider) Open(context.Context, STTSessionRequest) (STTUpstream, error) {
	stream := &fakeSTTUpstream{
		events:   make(chan STTEvent, 8),
		finished: make(chan struct{}),
	}
	p.mu.Lock()
	p.sessions = append(p.sessions, stream)
	p.mu.Unlock()
	return stream, nil
}

type fakeSTTUpstream struct {
	events   chan STTEvent
	finished chan struct{}
	finish   sync.Once
}

func (s *fakeSTTUpstream) SendAudio([]byte) error { return nil }
func (s *fakeSTTUpstream) Finish() error {
	if s.finished != nil {
		s.finish.Do(func() { close(s.finished) })
	}
	return nil
}
func (s *fakeSTTUpstream) Events() <-chan STTEvent { return s.events }
func (s *fakeSTTUpstream) Close() error            { return nil }

func authenticatedSTTHandler(handler http.HandlerFunc, uid int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := &JWTClaims{UID: uid, Username: "voice-user"}
		handler(w, r.WithContext(contextWithClaims(r.Context(), claims)))
	}
}

func issueSTTTicket(t *testing.T, baseURL string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/stt/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("ticket status=%d", response.StatusCode)
	}
	var payload struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ticket == "" {
		t.Fatal("missing ticket")
	}
	return payload.Ticket
}

func TestSTTConfigDefaultsMatchProductLimits(t *testing.T) {
	for _, name := range []string{
		"CATSCO_STT_MAX_SESSION_SECONDS",
		"CATSCO_STT_MAX_HOURLY_SECONDS",
		"CATSCO_STT_MAX_DAILY_SECONDS",
	} {
		t.Setenv(name, "")
	}

	config := STTConfigFromEnv()
	if config.MaxDuration != 150*time.Second {
		t.Fatalf("MaxDuration=%s, want 2m30s", config.MaxDuration)
	}
	if config.IdleTimeout != 10*time.Second {
		t.Fatalf("IdleTimeout=%s, want 10s", config.IdleTimeout)
	}
	if config.IdleGrace != 3*time.Second {
		t.Fatalf("IdleGrace=%s, want 3s", config.IdleGrace)
	}
	if config.HourlyAudioLimit != 24*time.Minute {
		t.Fatalf("HourlyAudioLimit=%s, want 24m", config.HourlyAudioLimit)
	}
	if config.DailyAudioLimit != time.Hour {
		t.Fatalf("DailyAudioLimit=%s, want 1h", config.DailyAudioLimit)
	}
}

func TestSTTLimiterReturnsRemainingConfiguredQuota(t *testing.T) {
	limiter := newSTTLimiter(STTConfig{
		MaxConcurrent:    1,
		HourlyAudioLimit: 24 * time.Minute,
		DailyAudioLimit:  time.Hour,
	})

	remaining, release, err := limiter.acquire(42, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 150*time.Second {
		t.Fatalf("first remaining=%s, want 2m30s", remaining)
	}
	release(sttUsageEntry{
		startedAt: time.Now().Add(-23 * time.Minute),
		duration:  23 * time.Minute,
	})

	remaining, release, err = limiter.acquire(42, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != time.Minute {
		t.Fatalf("hourly remaining=%s, want 1m", remaining)
	}
	release(sttUsageEntry{startedAt: time.Now().Add(-time.Minute), duration: time.Minute})
}

func TestSTTLimiterCountsOnlyAudioOverlappingRollingWindows(t *testing.T) {
	now := time.Now()
	limiter := newSTTLimiter(STTConfig{
		MaxConcurrent:    1,
		HourlyAudioLimit: 30 * time.Second,
		DailyAudioLimit:  35 * time.Second,
	})

	_, releaseDaily, err := limiter.acquire(42, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	releaseDaily(sttUsageEntry{
		startedAt: now.Add(-24*time.Hour - 10*time.Second),
		duration:  20 * time.Second,
	})

	_, releaseHourly, err := limiter.acquire(42, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	releaseHourly(sttUsageEntry{
		startedAt: now.Add(-time.Hour - 10*time.Second),
		duration:  20 * time.Second,
	})

	allowed, release, err := limiter.acquire(42, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release(sttUsageEntry{})
	if allowed < 4*time.Second || allowed > 6*time.Second {
		t.Fatalf("allowed duration=%s, want about 5s after window overlap", allowed)
	}
}

func TestSTTLimiterChargesBurstAudioImmediately(t *testing.T) {
	limiter := newSTTLimiter(STTConfig{
		MaxConcurrent:    1,
		HourlyAudioLimit: 30 * time.Second,
		DailyAudioLimit:  time.Minute,
	})
	_, release, err := limiter.acquire(42, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release(sttUsageEntry{startedAt: time.Now(), duration: 30 * time.Second})

	if _, _, err := limiter.acquire(42, 150*time.Second); !errors.Is(err, errSTTQuota) {
		t.Fatalf("burst audio quota error=%v, want %v", err, errSTTQuota)
	}
}

func TestSTTHandlerAllowsOnlyOneActiveSessionPerUser(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      150 * time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 24 * time.Minute,
		DailyAudioLimit:  time.Hour,
	}, provider)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 42))
	mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	firstTicket := issueSTTTicket(t, server.URL)
	wsBase := "ws" + strings.TrimPrefix(server.URL, "http")
	first, response, err := websocket.DefaultDialer.Dial(wsBase+"/api/stt/realtime?ticket="+firstTicket, nil)
	if err != nil {
		if response != nil {
			t.Fatalf("first dial status=%d err=%v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	defer first.Close()

	_, ready, err := first.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ready), `"type":"ready"`) {
		t.Fatalf("ready=%s", ready)
	}

	secondTicket := issueSTTTicket(t, server.URL)
	second, response, err := websocket.DefaultDialer.Dial(wsBase+"/api/stt/realtime?ticket="+secondTicket, nil)
	if err != nil {
		t.Fatalf("second dial err=%v status=%v", err, sttResponseStatus(response))
	}
	defer second.Close()
	_, denied, err := second.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(denied), `"type":"error"`) || !strings.Contains(string(denied), `"code":"session_active"`) {
		t.Fatalf("admission error=%s", denied)
	}
}

func TestSTTHandlerReturnsStructuredQuotaErrorAfterWebSocketUpgrade(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      150 * time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 30 * time.Second,
		DailyAudioLimit:  time.Hour,
	}, provider)
	handler.limiter.usage[42] = []sttUsageEntry{{
		startedAt: time.Now().Add(-45 * time.Second),
		duration:  45 * time.Second,
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 42))
	mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	ticket := issueSTTTicket(t, server.URL)
	conn, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/stt/realtime?ticket="+ticket,
		nil,
	)
	if err != nil {
		t.Fatalf("dial err=%v status=%v", err, sttResponseStatus(response))
	}
	defer conn.Close()
	_, denied, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(denied), `"type":"error"`) || !strings.Contains(string(denied), `"code":"quota_exhausted"`) {
		t.Fatalf("admission error=%s", denied)
	}
}

func TestSTTHandlerDoesNotPromotePartialWhenProviderCloses(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      150 * time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 24 * time.Minute,
		DailyAudioLimit:  time.Hour,
	}, provider)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 7))
	mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	ticket := issueSTTTicket(t, server.URL)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/stt/realtime?ticket="+ticket,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	stream := provider.sessions[0]
	provider.mu.Unlock()
	stream.events <- STTEvent{Type: STTEventPartial, Text: "未完成文本"}
	close(stream.events)

	_, partial, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(partial), `"type":"partial"`) {
		t.Fatalf("partial=%s err=%v", partial, err)
	}
	_, terminal, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(terminal), `"type":"final"`) || !strings.Contains(string(terminal), `"code":"provider_closed"`) {
		t.Fatalf("terminal=%s", terminal)
	}
}

func TestSTTHandlerForwardsACompleteDefiniteSnapshotBeforeLaterPartial(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      150 * time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 24 * time.Minute,
		DailyAudioLimit:  time.Hour,
	}, provider)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 8))
	mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	ticket := issueSTTTicket(t, server.URL)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/stt/realtime?ticket="+ticket,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	stream := provider.sessions[0]
	provider.mu.Unlock()
	stream.events <- STTEvent{Type: STTEventDefinite, Text: "已经稳定的前半句。正在识别的后半句"}

	var definite struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if _, payload, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(payload, &definite); err != nil {
		t.Fatal(err)
	}
	if definite.Type != "definite" || definite.Text != "已经稳定的前半句。正在识别的后半句" {
		t.Fatalf("definite=%#v", definite)
	}

	stream.events <- STTEvent{Type: STTEventPartial, Text: "已经稳定的前半句。正在识别的后半句，继续修订"}

	var partial struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if _, payload, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(payload, &partial); err != nil {
		t.Fatal(err)
	}
	if partial.Type != "partial" || partial.Text != "已经稳定的前半句。正在识别的后半句，继续修订" {
		t.Fatalf("partial=%#v", partial)
	}

	stream.events <- STTEvent{Type: STTEventFinal, Text: "已经稳定的前半句。正在识别的后半句。"}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
}

func TestSTTHandlerStopsAfterSilentAudioIdleTimeout(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      150 * time.Second,
		IdleTimeout:      80 * time.Millisecond,
		IdleGrace:        40 * time.Millisecond,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 24 * time.Minute,
		DailyAudioLimit:  time.Hour,
	}, provider)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 9))
	mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	ticket := issueSTTTicket(t, server.URL)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/stt/realtime?ticket="+ticket,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	stream := provider.sessions[0]
	provider.mu.Unlock()
	silence := make([]byte, 3200)
	for range 3 {
		if err := conn.WriteMessage(websocket.BinaryMessage, silence); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-stream.finished:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("silent PCM kept the STT session active past the idle timeout")
	}
	_, warning, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(warning), `"type":"idle_warning"`) {
		t.Fatalf("warning=%s err=%v", warning, err)
	}
	stream.events <- STTEvent{Type: STTEventFinal}
	_, terminal, err := conn.ReadMessage()
	var terminalPayload struct {
		Type       string `json:"type"`
		StopReason string `json:"stop_reason"`
	}
	if err == nil {
		err = json.Unmarshal(terminal, &terminalPayload)
	}
	if err != nil || terminalPayload.Type != "final" || terminalPayload.StopReason != sttStopReasonIdleTimeout {
		t.Fatalf("terminal=%s err=%v", terminal, err)
	}
}

func TestSTTHandlerKeepsExplicitStopReasonAfterAdvertisedDeadline(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      25 * time.Millisecond,
		IdleTimeout:      time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 24 * time.Minute,
		DailyAudioLimit:  time.Hour,
	}, provider)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 10))
	mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	ticket := issueSTTTicket(t, server.URL)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/stt/realtime?ticket="+ticket,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	stream := provider.sessions[0]
	provider.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"stop"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.finished:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("explicit stop did not finish the provider stream")
	}

	stream.events <- STTEvent{Type: STTEventFinal, Text: "用户主动结束的内容"}
	_, terminal, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(terminal, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "final" {
		t.Fatalf("terminal=%s", terminal)
	}
	if _, ok := payload["stop_reason"]; ok {
		t.Fatalf("explicit stop was classified as a duration boundary: %s", terminal)
	}
}

func TestSTTHandlerCarriesClientBoundaryStopReason(t *testing.T) {
	for _, test := range []struct {
		name   string
		reason string
	}{
		{name: "duration", reason: sttStopReasonDurationLimit},
		{name: "lifecycle", reason: sttStopReasonLifecycleStop},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeSTTProvider{}
			handler := NewSTTHandler(STTConfig{
				Enabled:          true,
				Provider:         "fake",
				TicketTTL:        time.Minute,
				MaxDuration:      time.Second,
				IdleTimeout:      time.Second,
				FinalTimeout:     time.Second,
				MaxConcurrent:    4,
				HourlyAudioLimit: 24 * time.Minute,
				DailyAudioLimit:  time.Hour,
			}, provider)

			mux := http.NewServeMux()
			mux.HandleFunc("/api/stt/sessions", authenticatedSTTHandler(handler.HandleSession, 11))
			mux.HandleFunc("/api/stt/realtime", handler.HandleRealtime)
			server := httptest.NewServer(mux)
			defer server.Close()

			ticket := issueSTTTicket(t, server.URL)
			conn, _, err := websocket.DefaultDialer.Dial(
				"ws"+strings.TrimPrefix(server.URL, "http")+"/api/stt/realtime?ticket="+ticket,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}

			provider.mu.Lock()
			stream := provider.sessions[0]
			provider.mu.Unlock()
			command := []byte(`{"type":"stop","stop_reason":"` + test.reason + `"}`)
			if err := conn.WriteMessage(websocket.TextMessage, command); err != nil {
				t.Fatal(err)
			}
			select {
			case <-stream.finished:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("client boundary stop did not finish the provider stream")
			}

			stream.events <- STTEvent{Type: STTEventFinal, Text: "边界内容"}
			_, terminal, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Type       string `json:"type"`
				StopReason string `json:"stop_reason"`
			}
			if err := json.Unmarshal(terminal, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Type != "final" || payload.StopReason != test.reason {
				t.Fatalf("terminal=%s want stop_reason=%s", terminal, test.reason)
			}
		})
	}
}

func TestSTTStopToFinalMillisecondsOnlyIncludesSuccessfulFinals(t *testing.T) {
	stoppedAt := time.Unix(100, 0)
	completedAt := stoppedAt.Add(275 * time.Millisecond)
	if got := sttStopToFinalMilliseconds("success", stoppedAt, completedAt); got != 275 {
		t.Fatalf("successful stop-to-final=%d", got)
	}
	if got := sttStopToFinalMilliseconds("error", stoppedAt, completedAt); got != -1 {
		t.Fatalf("failed stop-to-final=%d", got)
	}
}

func TestSTTTerminalErrorPayloadCarriesBoundaryReason(t *testing.T) {
	payload := sttTerminalErrorPayload("provider_closed", "closed", sttStopReasonHardTimeout)
	if got := payload["stop_reason"]; got != sttStopReasonHardTimeout {
		t.Fatalf("duration stop reason=%v", got)
	}

	payload = sttTerminalErrorPayload("final_timeout", "timeout", sttStopReasonIdleTimeout)
	if got := payload["stop_reason"]; got != sttStopReasonIdleTimeout {
		t.Fatalf("idle boundary reason=%v", got)
	}

	payload = sttTerminalErrorPayload("final_timeout", "timeout", sttStopReasonLifecycleStop)
	if got := payload["stop_reason"]; got != sttStopReasonLifecycleStop {
		t.Fatalf("lifecycle stop reason=%v", got)
	}

	payload = sttTerminalErrorPayload("provider_closed", "closed", "client_stop")
	if _, ok := payload["stop_reason"]; ok {
		t.Fatalf("unexpected client stop reason in generic payload: %#v", payload)
	}
}

func sttResponseStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}
