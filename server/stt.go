package server

import (
	"context"
	"encoding/binary"
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
	sttVADRMSThreshold                     = 0.008
	sttStopReasonHardTimeout               = "hard_timeout"
	sttStopReasonAudioLimit                = "audio_limit"
	sttStopReasonIdleTimeout               = "idle_timeout"
)

func sttIsDurationStopReason(reason string) bool {
	switch reason {
	case sttStopReasonHardTimeout, sttStopReasonAudioLimit, sttStopReasonIdleTimeout:
		return true
	default:
		return false
	}
}

func sttTerminalErrorPayload(code, message, stopReason string) map[string]interface{} {
	payload := map[string]interface{}{"type": "error", "code": code, "message": message}
	if sttIsDurationStopReason(stopReason) {
		payload["stop_reason"] = stopReason
	}
	return payload
}

type STTEventType string

const (
	STTEventPartial  STTEventType = "partial"
	STTEventDefinite STTEventType = "definite"
	STTEventFinal    STTEventType = "final"
	STTEventError    STTEventType = "error"
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

type sttProviderTelemetry interface {
	Model() string
}

func sttProviderModel(provider STTProvider) string {
	if telemetry, ok := provider.(sttProviderTelemetry); ok {
		return telemetry.Model()
	}
	return provider.ID()
}

type VolcengineSTTConfig struct {
	WebSocketURL   string
	APIKey         string
	ResourceID     string
	ConnectTimeout time.Duration
	allowTestURL   bool
}

type STTConfig struct {
	Enabled          bool
	Provider         string
	TicketTTL        time.Duration
	MaxDuration      time.Duration
	IdleTimeout      time.Duration
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
		MaxDuration:      sttEnvDurationSeconds("CATSCO_STT_MAX_SESSION_SECONDS", 150*time.Second),
		IdleTimeout:      sttEnvDurationMilliseconds("CATSCO_STT_IDLE_TIMEOUT_MS", 15*time.Second),
		FinalTimeout:     sttEnvDurationMilliseconds("CATSCO_STT_FINAL_TIMEOUT_MS", 1200*time.Millisecond),
		MaxConcurrent:    sttEnvInt("CATSCO_STT_MAX_CONCURRENT", 40),
		HourlyAudioLimit: sttEnvDurationSeconds("CATSCO_STT_MAX_HOURLY_SECONDS", 24*time.Minute),
		DailyAudioLimit:  sttEnvDurationSeconds("CATSCO_STT_MAX_DAILY_SECONDS", time.Hour),
		Volcengine: VolcengineSTTConfig{
			WebSocketURL:   sttEnvString("VOLCENGINE_STT_WS_URL", volcengineDoubaoStreamingV2URL),
			APIKey:         strings.TrimSpace(os.Getenv("VOLCENGINE_STT_API_KEY")),
			ResourceID:     sttEnvString("VOLCENGINE_STT_RESOURCE_ID", volcengineResourceDuration),
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
	startedAt time.Time
	duration  time.Duration
}

func sttUsageOverlap(entry sttUsageEntry, windowStart, windowEnd time.Time) time.Duration {
	if entry.duration <= 0 || !windowEnd.After(windowStart) {
		return 0
	}
	entryEnd := entry.startedAt.Add(entry.duration)
	if !entryEnd.After(windowStart) || !entry.startedAt.Before(windowEnd) {
		return 0
	}
	start := entry.startedAt
	if start.Before(windowStart) {
		start = windowStart
	}
	end := entryEnd
	if end.After(windowEnd) {
		end = windowEnd
	}
	return end.Sub(start)
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

func (l *sttLimiter) acquire(uid int64, maxDuration time.Duration) (time.Duration, func(sttUsageEntry), error) {
	now := time.Now()
	hourStart := now.Add(-time.Hour)
	dayStart := now.Add(-24 * time.Hour)
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
		if !entry.startedAt.Add(entry.duration).After(dayStart) {
			continue
		}
		kept = append(kept, entry)
		hourly += sttUsageOverlap(entry, hourStart, now)
		daily += sttUsageOverlap(entry, dayStart, now)
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
	release := func(usage sttUsageEntry) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			delete(l.activeUsers, uid)
			if l.active > 0 {
				l.active--
			}
			if usage.duration > 0 {
				recordedAt := time.Now()
				if usage.startedAt.IsZero() || usage.startedAt.Add(usage.duration).After(recordedAt) {
					// A burst sender must not defer accepted audio into a future quota window.
					usage.startedAt = recordedAt.Add(-usage.duration)
				}
				l.usage[uid] = append(l.usage[uid], usage)
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
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 15 * time.Second
	}
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
		"max_session_seconds": sttDurationSecondsCeil(h.config.MaxDuration),
		"max_session_ms":      h.config.MaxDuration.Milliseconds(),
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

func sttAdmissionError(err error) (code, message string) {
	switch {
	case errors.Is(err, errSTTUserActive):
		return "session_active", "已有语音输入正在进行"
	case errors.Is(err, errSTTGlobalFull):
		return "capacity_full", "语音输入服务繁忙，请稍后再试"
	case errors.Is(err, errSTTQuota):
		return "quota_exhausted", "语音输入额度已用完，请稍后再试"
	default:
		return "admission_failed", "语音输入暂时无法开始，请稍后再试"
	}
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
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Browser WebSocket APIs hide HTTP bodies from failed upgrades. Admit after
	// the upgrade so quota and capacity failures remain actionable to callers.
	allowedDuration, release, err := h.limiter.acquire(claims.UID, h.config.MaxDuration)
	if err != nil {
		code, message := sttAdmissionError(err)
		_ = h.writeSTTJSON(conn, map[string]string{"type": "error", "code": code, "message": message})
		return
	}
	acceptedBytes := int64(0)
	var firstAcceptedAt time.Time
	defer func() {
		release(sttUsageEntry{
			startedAt: firstAcceptedAt,
			duration:  time.Duration(acceptedBytes) * time.Second / sttPCMBytesPerSecond,
		})
	}()

	sessionStartedAt := time.Now()
	requestID := newSTTRequestID()
	connectStarted := time.Now()
	var startedAt time.Time
	var firstPartialAt time.Time
	var stopStartedAt time.Time
	stopping := false
	finalTimer := (*time.Timer)(nil)
	var finalTimeout <-chan time.Time
	outcome := "disconnected"
	errorCode := ""
	stopReason := "client_disconnect"
	providerModel := sttProviderModel(h.provider)
	defer func() {
		if finalTimer != nil {
			finalTimer.Stop()
		}
		firstPartialMS := int64(-1)
		if !firstPartialAt.IsZero() {
			firstPartialMS = firstPartialAt.Sub(startedAt).Milliseconds()
		}
		stopToFinalMS := sttStopToFinalMilliseconds(outcome, stopStartedAt, time.Now())
		connectMS := int64(-1)
		if !startedAt.IsZero() {
			connectMS = startedAt.Sub(connectStarted).Milliseconds()
		}
		log.Printf("stt session: provider=%s model=%s uid=%d outcome=%s error_code=%s stop_reason=%s accepted_ms=%d retry_audio_ms=0 billed_usage_ms=-1 connection_ms=%d connect_ms=%d first_partial_ms=%d stop_to_final_ms=%d",
			h.provider.ID(), providerModel, claims.UID, outcome, errorCode, stopReason,
			acceptedBytes*1000/sttPCMBytesPerSecond, time.Since(sessionStartedAt).Milliseconds(),
			connectMS, firstPartialMS, stopToFinalMS)
	}()

	upstream, err := h.provider.Open(r.Context(), STTSessionRequest{UserID: claims.UID, RequestID: requestID})
	if err != nil {
		outcome = "error"
		errorCode = "provider_connect_failed"
		h.writeSTTJSON(conn, map[string]interface{}{"type": "error", "code": "provider_connect_failed", "message": "语音识别服务连接失败"})
		return
	}
	defer upstream.Close()

	maxBytes := int64(allowedDuration.Seconds() * sttPCMBytesPerSecond)
	if err := h.writeSTTJSON(conn, map[string]interface{}{
		"type":                "ready",
		"provider":            h.provider.ID(),
		"max_session_seconds": sttDurationSecondsCeil(allowedDuration),
		"max_session_ms":      allowedDuration.Milliseconds(),
	}); err != nil {
		outcome = "error"
		errorCode = "client_write_failed"
		return
	}

	startedAt = time.Now()
	hardTimer := time.NewTimer(allowedDuration + 2*time.Second)
	defer hardTimer.Stop()
	idleTimer := time.NewTimer(h.config.IdleTimeout)
	idleTimeout := idleTimer.C
	hardDeadlineAt := startedAt.Add(allowedDuration)
	idleDeadlineAt := startedAt.Add(h.config.IdleTimeout)
	defer idleTimer.Stop()
	markElapsedDurationBoundary := func() {
		if stopping || sttIsDurationStopReason(stopReason) {
			return
		}
		now := time.Now()
		switch {
		case !now.Before(hardDeadlineAt):
			stopReason = sttStopReasonHardTimeout
		case !now.Before(idleDeadlineAt):
			stopReason = sttStopReasonIdleTimeout
		}
	}

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
		if idleTimer.Stop() {
			idleTimeout = nil
		}
		stopStartedAt = time.Now()
		if err := upstream.Finish(); err != nil {
			outcome = "error"
			errorCode = "provider_finish_failed"
			_ = h.writeSTTJSON(conn, sttTerminalErrorPayload("provider_finish_failed", "语音识别结束失败", stopReason))
			return false
		}
		finalTimer = time.NewTimer(h.config.FinalTimeout)
		finalTimeout = finalTimer.C
		return true
	}
	for {
		select {
		case message := <-incoming:
			if message.err != nil {
				return
			}
			switch message.messageType {
			case websocket.BinaryMessage:
				// A browser can have audio frames queued while the event loop is
				// delayed (especially when a PWA is backgrounded). Classify the
				// elapsed boundary before forwarding a late frame so it cannot
				// refresh the idle deadline or hide the duration stop reason.
				markElapsedDurationBoundary()
				if sttIsDurationStopReason(stopReason) {
					if !finish() {
						return
					}
					continue
				}
				if stopping || len(message.payload) == 0 || len(message.payload) > sttMaxBrowserFrameBytes {
					continue
				}
				if acceptedBytes+int64(len(message.payload)) > maxBytes {
					stopReason = sttStopReasonAudioLimit
					if !finish() {
						return
					}
					continue
				}
				if err := upstream.SendAudio(message.payload); err != nil {
					markElapsedDurationBoundary()
					outcome = "error"
					errorCode = "provider_send_failed"
					_ = h.writeSTTJSON(conn, sttTerminalErrorPayload("provider_send_failed", "语音数据发送失败", stopReason))
					return
				}
				if firstAcceptedAt.IsZero() {
					firstAcceptedAt = time.Now()
				}
				acceptedBytes += int64(len(message.payload))
				markElapsedDurationBoundary()
				if sttIsDurationStopReason(stopReason) {
					if !finish() {
						return
					}
					continue
				}
				if sttPCMHasVoice(message.payload) {
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(h.config.IdleTimeout)
					idleTimeout = idleTimer.C
					idleDeadlineAt = time.Now().Add(h.config.IdleTimeout)
				}
			case websocket.TextMessage:
				var command struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(message.payload, &command) != nil {
					continue
				}
				switch command.Type {
				case "stop":
					stopReason = "client_stop"
					if !finish() {
						return
					}
				case "cancel":
					outcome = "cancelled"
					stopReason = "client_cancel"
					return
				}
			}
		case event, open := <-upstream.Events():
			if !open {
				markElapsedDurationBoundary()
				outcome = "error"
				errorCode = "provider_closed"
				_ = h.writeSTTJSON(conn, sttTerminalErrorPayload("provider_closed", "语音识别连接已断开，请重试", stopReason))
				return
			}
			switch event.Type {
			case STTEventPartial:
				if firstPartialAt.IsZero() {
					firstPartialAt = time.Now()
				}
				if event.Text == "" {
					continue
				}
				if err := h.writeSTTJSON(conn, map[string]interface{}{"type": "partial", "text": event.Text}); err != nil {
					return
				}
			case STTEventDefinite:
				if firstPartialAt.IsZero() {
					firstPartialAt = time.Now()
				}
				if event.Text == "" {
					continue
				}
				if err := h.writeSTTJSON(conn, map[string]interface{}{"type": "definite", "text": event.Text}); err != nil {
					return
				}
			case STTEventFinal:
				outcome = "success"
				if !stopping {
					markElapsedDurationBoundary()
					if !sttIsDurationStopReason(stopReason) {
						stopReason = "provider_final"
					}
				}
				finalPayload := map[string]interface{}{"type": "final", "text": event.Text}
				if sttIsDurationStopReason(stopReason) {
					finalPayload["stop_reason"] = stopReason
				}
				_ = h.writeSTTJSON(conn, finalPayload)
				return
			case STTEventError:
				markElapsedDurationBoundary()
				outcome = "error"
				errorCode = event.Code
				_ = h.writeSTTJSON(conn, sttTerminalErrorPayload(event.Code, event.Message, stopReason))
				return
			}
		case <-hardTimer.C:
			stopReason = sttStopReasonHardTimeout
			if !finish() {
				return
			}
		case <-idleTimeout:
			stopReason = sttStopReasonIdleTimeout
			if !finish() {
				return
			}
		case <-finalTimeout:
			outcome = "error"
			errorCode = "final_timeout"
			_ = h.writeSTTJSON(conn, map[string]interface{}{
				"type":        "error",
				"code":        "final_timeout",
				"message":     "语音识别未能及时完成，请重试",
				"stop_reason": stopReason,
			})
			return
		}
	}
}

func sttDurationSecondsCeil(duration time.Duration) int {
	if duration <= 0 {
		return 0
	}
	return int((duration + time.Second - 1) / time.Second)
}

func sttStopToFinalMilliseconds(outcome string, stoppedAt, completedAt time.Time) int64 {
	if outcome != "success" || stoppedAt.IsZero() {
		return -1
	}
	return completedAt.Sub(stoppedAt).Milliseconds()
}

func sttPCMHasVoice(payload []byte) bool {
	if len(payload) < 2 {
		return false
	}
	var sumSquares float64
	samples := len(payload) / 2
	for offset := 0; offset+1 < len(payload); offset += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(payload[offset:offset+2]))) / 32768
		sumSquares += sample * sample
	}
	return sumSquares/float64(samples) >= sttVADRMSThreshold*sttVADRMSThreshold
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
