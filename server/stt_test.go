package server

import (
	"context"
	"encoding/json"
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

func TestSTTHandlerAllowsOnlyOneActiveSessionPerUser(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      90 * time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 10 * time.Minute,
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
	if second != nil {
		second.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("second dial err=%v status=%v", err, sttResponseStatus(response))
	}
}

func TestSTTHandlerDoesNotPromotePartialWhenProviderCloses(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      90 * time.Second,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 10 * time.Minute,
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

func TestSTTHandlerStopsAfterSilentAudioIdleTimeout(t *testing.T) {
	provider := &fakeSTTProvider{}
	handler := NewSTTHandler(STTConfig{
		Enabled:          true,
		Provider:         "fake",
		TicketTTL:        time.Minute,
		MaxDuration:      90 * time.Second,
		IdleTimeout:      80 * time.Millisecond,
		FinalTimeout:     time.Second,
		MaxConcurrent:    4,
		HourlyAudioLimit: 10 * time.Minute,
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
	stream.events <- STTEvent{Type: STTEventFinal}
	_, terminal, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(terminal), `"type":"final"`) {
		t.Fatalf("terminal=%s err=%v", terminal, err)
	}
}

func sttResponseStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}
