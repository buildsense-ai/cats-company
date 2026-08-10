package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

const (
	STTProviderVolcengineDoubaoStreamingV2 = "volcengine-doubao-streaming-v2"
	sttPCMBytesPerSecond                   = 16000 * 2
	sttMaxBrowserFrameBytes                = 16 * 1024
	sttTicketType                          = "stt_ticket"
)

type STTEventType string

const (
	STTEventPartial STTEventType = "partial"
	STTEventFinal   STTEventType = "final"
	STTEventError   STTEventType = "error"
)

type STTEvent struct {
	Type    STTEventType
	Text    string
	Code    string
	Message string
}

type STTSessionRequest struct {
	UserID    int64
	RequestID string
}

type STTUpstream interface {
	SendAudio([]byte) error
	Finish() error
	Events() <-chan STTEvent
	Close() error
}

type STTProvider interface {
	ID() string
	Open(context.Context, STTSessionRequest) (STTUpstream, error)
}

type VolcengineSTTConfig struct {
	WebSocketURL   string
	APIKey         string
	ResourceID     string
	ConnectTimeout time.Duration
}

type STTConfig struct {
	Enabled          bool
	Provider         string
	TicketTTL        time.Duration
	MaxDuration      time.Duration
	FinalTimeout     time.Duration
	MaxConcurrent    int
	HourlyAudioLimit time.Duration
	DailyAudioLimit  time.Duration
	Volcengine       VolcengineSTTConfig
}

func STTConfigFromEnv() STTConfig {
	return STTConfig{
		Enabled:          sttEnvBool("CATSCO_STT_ENABLED", false),
		Provider:         sttEnvString("CATSCO_STT_PROVIDER", STTProviderVolcengineDoubaoStreamingV2),
		TicketTTL:        sttEnvDurationSeconds("CATSCO_STT_TICKET_TTL_SECONDS", 45*time.Second),
		MaxDuration:      sttEnvDurationSeconds("CATSCO_STT_MAX_SESSION_SECONDS", 90*time.Second),
		FinalTimeout:     sttEnvDurationMilliseconds("CATSCO_STT_FINAL_TIMEOUT_MS", 1200*time.Millisecond),
		MaxConcurrent:    sttEnvInt("CATSCO_STT_MAX_CONCURRENT", 40),
		HourlyAudioLimit: sttEnvDurationSeconds("CATSCO_STT_MAX_HOURLY_SECONDS", 10*time.Minute),
		DailyAudioLimit:  sttEnvDurationSeconds("CATSCO_STT_MAX_DAILY_SECONDS", time.Hour),
		Volcengine: VolcengineSTTConfig{
			WebSocketURL:   sttEnvString("VOLCENGINE_STT_WS_URL", "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_async"),
			APIKey:         strings.TrimSpace(os.Getenv("VOLCENGINE_STT_API_KEY")),
			ResourceID:     sttEnvString("VOLCENGINE_STT_RESOURCE_ID", "volc.seedasr.sauc.duration"),
			ConnectTimeout: sttEnvDurationMilliseconds("CATSCO_STT_CONNECT_TIMEOUT_MS", 2*time.Second),
		},
	}
}

func sttEnvString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func sttEnvBool(name string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func sttEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func sttEnvDurationSeconds(name string, fallback time.Duration) time.Duration {
	return time.Duration(sttEnvInt(name, int(fallback/time.Second))) * time.Second
}

func sttEnvDurationMilliseconds(name string, fallback time.Duration) time.Duration {
	return time.Duration(sttEnvInt(name, int(fallback/time.Millisecond))) * time.Millisecond
}

type sttTicketClaims struct {
	TokenType string `json:"token_type"`
	UID       int64  `json:"userId"`
	Provider  string `json:"provider"`
	jwt.RegisteredClaims
}

type sttUsageEntry struct {
	at       time.Time
	duration time.Duration
}

type sttLimiter struct {
	mu            sync.Mutex
	maxConcurrent int
	hourlyLimit   time.Duration
	dailyLimit    time.Duration
	active        int
	activeUsers   map[int64]struct{}
	usage         map[int64][]sttUsageEntry
}

func newSTTLimiter(config STTConfig) *sttLimiter {
	return &sttLimiter{
		maxConcurrent: config.MaxConcurrent,
		hourlyLimit:   config.HourlyAudioLimit,
		dailyLimit:    config.DailyAudioLimit,
		activeUsers:   make(map[int64]struct{}),
		usage:         make(map[int64][]sttUsageEntry),
	}
}

var (
	errSTTUserActive = errors.New("an STT session is already active for this user")
	errSTTGlobalFull = errors.New("STT concurrency limit reached")
	errSTTQuota      = errors.New("STT audio quota exhausted")
)

func (l *sttLimiter) acquire(uid int64, maxDuration time.Duration) (time.Duration, func(time.Duration), error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.activeUsers[uid]; exists {
		return 0, nil, errSTTUserActive
	}
	if l.active >= l.maxConcurrent {
		return 0, nil, errSTTGlobalFull
	}

	entries := l.usage[uid]
	kept := entries[:0]
	var hourly, daily time.Duration
	for _, entry := range entries {
		if now.Sub(entry.at) >= 24*time.Hour {
			continue
		}
		kept = append(kept, entry)
		daily += entry.duration
		if now.Sub(entry.at) < time.Hour {
			hourly += entry.duration
		}
	}
	l.usage[uid] = kept
	remaining := maxDuration
	if hourly >= l.hourlyLimit || daily >= l.dailyLimit {
		return 0, nil, errSTTQuota
	}
	if candidate := l.hourlyLimit - hourly; candidate < remaining {
		remaining = candidate
	}
	if candidate := l.dailyLimit - daily; candidate < remaining {
		remaining = candidate
	}
	if remaining <= 0 {
		return 0, nil, errSTTQuota
	}

	l.active++
	l.activeUsers[uid] = struct{}{}
	var once sync.Once
	release := func(accepted time.Duration) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			delete(l.activeUsers, uid)
			if l.active > 0 {
				l.active--
			}
			if accepted > 0 {
				l.usage[uid] = append(l.usage[uid], sttUsageEntry{at: time.Now(), duration: accepted})
			}
		})
	}
	return remaining, release, nil
}

type STTHandler struct {
	config       STTConfig
	provider     STTProvider
	configErr    error
	limiter      *sttLimiter
	usedTickets  map[string]time.Time
	usedTicketMu sync.Mutex
	upgrader     websocket.Upgrader
}

func NewSTTHandler(config STTConfig, provider STTProvider) *STTHandler {
	handler := &STTHandler{
		config:      config,
		provider:    provider,
		limiter:     newSTTLimiter(config),
		usedTickets: make(map[string]time.Time),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     sttSameOrigin,
		},
	}
	if !config.Enabled {
		handler.configErr = errors.New("streaming STT is disabled")
	} else if provider == nil {
		handler.configErr = errors.New("streaming STT provider is not configured")
	} else if config.Provider != provider.ID() {
		handler.configErr = fmt.Errorf("configured STT provider %q does not match %q", config.Provider, provider.ID())
	}
	return handler
}

func NewSTTHandlerFromEnv() *STTHandler {
	config := STTConfigFromEnv()
	if !config.Enabled {
		return NewSTTHandler(config, nil)
	}
	if config.Provider != STTProviderVolcengineDoubaoStreamingV2 {
		handler := NewSTTHandler(config, nil)
		handler.configErr = fmt.Errorf("unsupported STT provider %q", config.Provider)
		return handler
	}
	provider, err := NewVolcengineStreamingProvider(config.Volcengine)
	handler := NewSTTHandler(config, provider)
	if err != nil {
		handler.configErr = err
	}
	return handler
}

func (h *STTHandler) ConfigError() error {
	if h == nil {
		return errors.New("streaming STT handler is nil")
	}
	return h.configErr
}

func sttSameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

func (h *STTHandler) HandleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := h.ConfigError(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "streaming voice input is unavailable"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	now := time.Now()
	claims := sttTicketClaims{
		TokenType: sttTicketType,
		UID:       uid,
		Provider:  h.provider.ID(),
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        newSTTRequestID(),
			Issuer:    "catscompany",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(h.config.TicketTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create STT session"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"ticket":              signed,
		"provider":            h.provider.ID(),
		"expires_in_seconds":  int(h.config.TicketTTL / time.Second),
		"max_session_seconds": int(h.config.MaxDuration / time.Second),
	})
}

func (h *STTHandler) parseAndConsumeTicket(raw string) (*sttTicketClaims, error) {
	claims := &sttTicketClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid || claims.TokenType != sttTicketType || claims.UID <= 0 || claims.Provider != h.provider.ID() || claims.ID == "" {
		return nil, errors.New("invalid STT ticket")
	}

	h.usedTicketMu.Lock()
	defer h.usedTicketMu.Unlock()
	now := time.Now()
	for id, expiresAt := range h.usedTickets {
		if !expiresAt.After(now) {
			delete(h.usedTickets, id)
		}
	}
	if _, used := h.usedTickets[claims.ID]; used {
		return nil, errors.New("STT ticket already used")
	}
	expiresAt := now.Add(h.config.TicketTTL)
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}
	h.usedTickets[claims.ID] = expiresAt
	return claims, nil
}

type sttBrowserMessage struct {
	messageType int
	payload     []byte
	err         error
}

func (h *STTHandler) HandleRealtime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := h.ConfigError(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "streaming voice input is unavailable"})
		return
	}
	claims, err := h.parseAndConsumeTicket(strings.TrimSpace(r.URL.Query().Get("ticket")))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired STT ticket"})
		return
	}
	allowedDuration, release, err := h.limiter.acquire(claims.UID, h.config.MaxDuration)
	if err != nil {
		status := http.StatusTooManyRequests
		if errors.Is(err, errSTTUserActive) {
			status = http.StatusConflict
		} else if errors.Is(err, errSTTGlobalFull) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	acceptedBytes := int64(0)
	defer func() {
		release(time.Duration(acceptedBytes) * time.Second / sttPCMBytesPerSecond)
	}()

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	requestID := newSTTRequestID()
	connectStarted := time.Now()
	upstream, err := h.provider.Open(r.Context(), STTSessionRequest{UserID: claims.UID, RequestID: requestID})
	if err != nil {
		h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": "provider_connect_failed", "message": "语音识别服务连接失败"})
		return
	}
	defer upstream.Close()

	maxBytes := int64(allowedDuration.Seconds() * sttPCMBytesPerSecond)
	if err := h.writeSTTJSON(conn, map[string]interface{}{
		"type":                "ready",
		"provider":            h.provider.ID(),
		"max_session_seconds": int(allowedDuration / time.Second),
	}); err != nil {
		return
	}

	startedAt := time.Now()
	var firstPartialAt time.Time
	var stopStartedAt time.Time
	stopping := false
	finalTimer := (*time.Timer)(nil)
	var finalTimeout <-chan time.Time
	hardTimer := time.NewTimer(allowedDuration + 2*time.Second)
	defer hardTimer.Stop()

	incoming := make(chan sttBrowserMessage, 4)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		conn.SetReadLimit(sttMaxBrowserFrameBytes)
		for {
			messageType, payload, err := conn.ReadMessage()
			select {
			case incoming <- sttBrowserMessage{messageType: messageType, payload: payload, err: err}:
			case <-readerDone:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	finish := func() bool {
		if stopping {
			return true
		}
		stopping = true
		stopStartedAt = time.Now()
		if err := upstream.Finish(); err != nil {
			_ = h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": "provider_finish_failed", "message": "语音识别结束失败"})
			return false
		}
		finalTimer = time.NewTimer(h.config.FinalTimeout)
		finalTimeout = finalTimer.C
		return true
	}
	defer func() {
		if finalTimer != nil {
			finalTimer.Stop()
		}
		firstPartialMS := int64(-1)
		if !firstPartialAt.IsZero() {
			firstPartialMS = firstPartialAt.Sub(startedAt).Milliseconds()
		}
		stopToFinalMS := int64(-1)
		if !stopStartedAt.IsZero() {
			stopToFinalMS = time.Since(stopStartedAt).Milliseconds()
		}
		log.Printf("stt session: provider=%s uid=%d accepted_ms=%d connect_ms=%d first_partial_ms=%d stop_to_final_ms=%d",
			h.provider.ID(), claims.UID, acceptedBytes*1000/sttPCMBytesPerSecond,
			startedAt.Sub(connectStarted).Milliseconds(), firstPartialMS, stopToFinalMS)
	}()

	for {
		select {
		case message := <-incoming:
			if message.err != nil {
				return
			}
			switch message.messageType {
			case websocket.BinaryMessage:
				if stopping || len(message.payload) == 0 || len(message.payload) > sttMaxBrowserFrameBytes {
					continue
				}
				if acceptedBytes+int64(len(message.payload)) > maxBytes {
					if !finish() {
						return
					}
					continue
				}
				if err := upstream.SendAudio(message.payload); err != nil {
					_ = h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": "provider_send_failed", "message": "语音数据发送失败"})
					return
				}
				acceptedBytes += int64(len(message.payload))
			case websocket.TextMessage:
				var command struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(message.payload, &command) != nil {
					continue
				}
				switch command.Type {
				case "stop":
					if !finish() {
						return
					}
				case "cancel":
					return
				}
			}
		case event, open := <-upstream.Events():
			if !open {
				_ = h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": "provider_closed", "message": "语音识别连接已断开，请重试"})
				return
			}
			switch event.Type {
			case STTEventPartial:
				if firstPartialAt.IsZero() {
					firstPartialAt = time.Now()
				}
				if err := h.writeSTTJSON(conn, map[string]interface{}{"type": "partial", "text": event.Text}); err != nil {
					return
				}
			case STTEventFinal:
				_ = h.writeSTTJSON(conn, map[string]interface{}{"type": "final", "text": event.Text})
				return
			case STTEventError:
				_ = h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": event.Code, "message": event.Message})
				return
			}
		case <-hardTimer.C:
			if !finish() {
				return
			}
		case <-finalTimeout:
			_ = h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": "final_timeout", "message": "语音识别未能及时完成，请重试"})
			return
		}
	}
}

func (h *STTHandler) writeSTTJSON(conn *websocket.Conn, payload interface{}) error {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return conn.WriteJSON(payload)
}

func newSTTRequestID() string {
	if value, err := randomHex(16); err == nil && value != "" {
		return value
	}
	return fmt.Sprintf("stt-%d", time.Now().UnixNano())
}
