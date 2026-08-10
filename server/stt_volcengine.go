package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type volcengineStreamingProvider struct {
	config VolcengineSTTConfig
	dialer websocket.Dialer
}

const (
	volcengineMessageTypeFullResponse = 0x09
	volcengineMessageTypeError        = 0x0f
	volcengineFinalFlags              = 0x03
)

func NewVolcengineStreamingProvider(config VolcengineSTTConfig) (STTProvider, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.WebSocketURL))
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return nil, errors.New("VOLCENGINE_STT_WS_URL must be a ws:// or wss:// URL")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("VOLCENGINE_STT_API_KEY is required")
	}
	if strings.TrimSpace(config.ResourceID) == "" {
		return nil, errors.New("VOLCENGINE_STT_RESOURCE_ID is required")
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 2 * time.Second
	}
	return &volcengineStreamingProvider{
		config: config,
		dialer: websocket.Dialer{HandshakeTimeout: config.ConnectTimeout},
	}, nil
}

func (p *volcengineStreamingProvider) ID() string {
	return STTProviderVolcengineDoubaoStreamingV2
}

func (p *volcengineStreamingProvider) Model() string {
	return p.config.ResourceID
}

func (p *volcengineStreamingProvider) Open(ctx context.Context, request STTSessionRequest) (STTUpstream, error) {
	headers := http.Header{}
	headers.Set("X-Api-Key", p.config.APIKey)
	headers.Set("X-Api-Resource-Id", p.config.ResourceID)
	headers.Set("X-Api-Request-Id", request.RequestID)
	headers.Set("X-Api-Connect-Id", request.RequestID)
	conn, response, err := p.dialer.DialContext(ctx, p.config.WebSocketURL, headers)
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("volcengine websocket status %d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("connect volcengine websocket: %w", err)
	}
	stream := &volcengineSTTUpstream{
		conn:   conn,
		events: make(chan STTEvent, 32),
		done:   make(chan struct{}),
	}
	initial, err := buildVolcengineInitialFrame(p.config, request)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := stream.writeBinary(initial); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send volcengine session request: %w", err)
	}
	go stream.readLoop()
	return stream, nil
}

func buildVolcengineInitialFrame(config VolcengineSTTConfig, request STTSessionRequest) ([]byte, error) {
	uid := strconv.FormatInt(request.UserID, 10)
	payload := map[string]interface{}{
		"user": map[string]interface{}{"uid": uid},
		"audio": map[string]interface{}{
			"format":      "pcm",
			"codec":       "raw",
			"sample_rate": 16000,
			"bits":        16,
			"channel":     1,
		},
		"request": map[string]interface{}{
			"model_name":      "bigmodel",
			"show_utterances": true,
			"result_type":     "full",
			"enable_itn":      true,
			"enable_punc":     true,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return volcengineClientFrame(0x10, 0x10, encoded), nil
}

func volcengineClientFrame(messageType, serialization byte, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = 0x11
	frame[1] = messageType
	frame[2] = serialization
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

type volcengineSTTUpstream struct {
	conn      *websocket.Conn
	events    chan STTEvent
	done      chan struct{}
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func (s *volcengineSTTUpstream) Events() <-chan STTEvent { return s.events }

func (s *volcengineSTTUpstream) SendAudio(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeBinaryLocked(volcengineClientAudioFrame(payload, false))
}

func (s *volcengineSTTUpstream) Finish() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeBinaryLocked(volcengineClientAudioFrame(nil, true))
}

func (s *volcengineSTTUpstream) writeBinary(payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeBinaryLocked(payload)
}

func (s *volcengineSTTUpstream) writeBinaryLocked(payload []byte) error {
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return s.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func volcengineClientAudioFrame(payload []byte, final bool) []byte {
	flags := byte(0x00)
	if final {
		flags = 0x02
	}
	frame := make([]byte, 8+len(payload))
	frame[0] = 0x11
	frame[1] = 0x20 | flags
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

func (s *volcengineSTTUpstream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.conn.Close()
	})
	return err
}

func (s *volcengineSTTUpstream) readLoop() {
	defer close(s.events)
	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		event, ok := parseVolcengineServerFrame(payload)
		if !ok {
			continue
		}
		select {
		case s.events <- event:
		case <-s.done:
			return
		}
		if event.Type == STTEventFinal || event.Type == STTEventError {
			return
		}
	}
}

func parseVolcengineServerFrame(frame []byte) (STTEvent, bool) {
	if len(frame) < 8 {
		return STTEvent{}, false
	}
	headerLength := int(frame[0]&0x0f) * 4
	if headerLength < 4 || len(frame) < headerLength+4 {
		return STTEvent{}, false
	}
	messageType := frame[1] >> 4
	flags := frame[1] & 0x0f
	compression := frame[2] & 0x0f
	payloadStart := headerLength
	sequence := int32(0)
	if messageType == 0x09 || messageType == 0x0b || messageType == 0x0f {
		if len(frame) < payloadStart+4 {
			return STTEvent{}, false
		}
		sequence = int32(binary.BigEndian.Uint32(frame[payloadStart : payloadStart+4]))
		payloadStart += 4
	}
	if len(frame) < payloadStart+4 {
		return STTEvent{}, false
	}
	payloadLength := int(binary.BigEndian.Uint32(frame[payloadStart : payloadStart+4]))
	payloadStart += 4
	if payloadLength < 0 || len(frame) < payloadStart+payloadLength {
		return STTEvent{}, false
	}
	payload := frame[payloadStart : payloadStart+payloadLength]
	if compression == 1 {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return STTEvent{Type: STTEventError, Code: "invalid_provider_response", Message: "语音识别服务返回无效数据"}, true
		}
		decompressed, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return STTEvent{Type: STTEventError, Code: "invalid_provider_response", Message: "语音识别服务返回无效数据"}, true
		}
		payload = decompressed
	}
	if messageType == volcengineMessageTypeError {
		message := volcengineErrorMessage(payload)
		if message == "" {
			message = "语音识别服务处理失败"
		}
		return STTEvent{Type: STTEventError, Code: strconv.FormatInt(int64(sequence), 10), Message: message}, true
	}
	if messageType != volcengineMessageTypeFullResponse {
		return STTEvent{}, false
	}
	var response struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return STTEvent{Type: STTEventError, Code: "invalid_provider_response", Message: "语音识别服务返回无效数据"}, true
	}
	if response.Code != 0 && response.Code != 1000 && response.Code != 20000000 {
		message := strings.TrimSpace(response.Message)
		if message == "" {
			message = "语音识别服务处理失败"
		}
		return STTEvent{Type: STTEventError, Code: strconv.Itoa(response.Code), Message: message}, true
	}
	text := volcengineResponseText(response.Result)
	isFinal := sequence < 0 || flags == volcengineFinalFlags
	if text == "" {
		if isFinal {
			return STTEvent{Type: STTEventFinal}, true
		}
		return STTEvent{}, false
	}
	eventType := STTEventPartial
	if isFinal {
		eventType = STTEventFinal
	}
	return STTEvent{Type: eventType, Text: text}, true
}

func volcengineErrorMessage(payload []byte) string {
	var response struct {
		ErrorMessage string `json:"error_message"`
		ErrorMsg     string `json:"error_msg"`
		Error        string `json:"error"`
		Message      string `json:"message"`
		Msg          string `json:"msg"`
	}
	if json.Unmarshal(payload, &response) == nil {
		for _, candidate := range []string{response.ErrorMessage, response.ErrorMsg, response.Error, response.Message, response.Msg} {
			if message := strings.TrimSpace(candidate); message != "" {
				return message
			}
		}
	}
	return strings.TrimSpace(string(payload))
}

func volcengineResponseText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var current struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &current) == nil {
		if text := strings.TrimSpace(current.Text); text != "" {
			return text
		}
	}
	var legacy []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &legacy) == nil && len(legacy) > 0 {
		return strings.TrimSpace(legacy[0].Text)
	}
	return ""
}
