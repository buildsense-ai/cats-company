package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestVolcengineStreamingProviderAuthenticatesAndMapsTranscriptEvents(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "test-api-key" {
			t.Errorf("X-Api-Key=%q", got)
		}
		if got := r.Header.Get("X-Api-Resource-Id"); got != "volc.seedasr.sauc.duration" {
			t.Errorf("X-Api-Resource-Id=%q", got)
		}
		if got := r.Header.Get("X-Api-Connect-Id"); got != "request-1" {
			t.Errorf("X-Api-Connect-Id=%q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()

		messageType, initial, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		if messageType != websocket.BinaryMessage || len(initial) < 8 || initial[1] != 0x10 {
			t.Errorf("unexpected initial frame: %x", initial)
			return
		}
		length := int(binary.BigEndian.Uint32(initial[4:8]))
		var request map[string]interface{}
		if err := json.Unmarshal(initial[8:8+length], &request); err != nil {
			t.Error(err)
			return
		}
		if _, exists := request["app"]; exists {
			t.Errorf("new API-key request must not include legacy app credentials: %v", request["app"])
		}
		requestOptions, _ := request["request"].(map[string]interface{})
		if requestOptions["model_name"] != "bigmodel" {
			t.Errorf("request payload=%v", requestOptions)
		}
		if requestOptions["enable_nonstream"] != true {
			t.Errorf("request payload must enable two-pass recognition: %v", requestOptions)
		}
		if requestOptions["show_utterances"] != true {
			t.Errorf("request payload must expose stable utterances: %v", requestOptions)
		}
		audioOptions, _ := request["audio"].(map[string]interface{})
		if audioOptions["sample_rate"] != float64(16000) {
			t.Errorf("audio.sample_rate=%v", audioOptions["sample_rate"])
		}
		if _, exists := audioOptions["rate"]; exists {
			t.Errorf("v3 audio options must not include audio.rate: %v", audioOptions)
		}
		if _, exists := audioOptions["language"]; exists {
			t.Errorf("bigmodel_async must not include audio.language: %v", audioOptions)
		}

		_, audio, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		if len(audio) < 8 || audio[1] != 0x20 || int(binary.BigEndian.Uint32(audio[4:8])) != len(audio)-8 {
			t.Errorf("unexpected audio frame: %x", audio)
			return
		}

		if err := conn.WriteMessage(websocket.BinaryMessage, volcengineServerFrame(1, map[string]interface{}{
			"code": 1000,
			"result": []map[string]interface{}{{
				"text":       "你好",
				"utterances": []map[string]interface{}{{"text": "你好", "definite": false}},
			}},
		})); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, volcengineServerFrameWithFlags(2, 0x03, map[string]interface{}{
			"code": 1000,
			"result": map[string]interface{}{
				"text":       "你好世界",
				"utterances": []map[string]interface{}{{"text": "你好世界", "definite": true}},
			},
		})); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()

	provider, err := NewVolcengineStreamingProvider(VolcengineSTTConfig{
		WebSocketURL:   "ws" + strings.TrimPrefix(upstream.URL, "http"),
		APIKey:         "test-api-key",
		ResourceID:     "volc.seedasr.sauc.duration",
		ConnectTimeout: time.Second,
		allowTestURL:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Open(context.Background(), STTSessionRequest{UserID: 42, RequestID: "request-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.SendAudio([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}

	partial := <-stream.Events()
	if partial.Type != STTEventPartial || partial.Text != "你好" {
		t.Fatalf("partial=%#v", partial)
	}
	final := <-stream.Events()
	if final.Type != STTEventFinal || final.Text != "你好世界" {
		t.Fatalf("final=%#v", final)
	}
}

func TestVolcengineStreamingProviderRejectsNonDoubaoV2Configuration(t *testing.T) {
	for name, config := range map[string]VolcengineSTTConfig{
		"resource": {
			WebSocketURL: volcengineDoubaoStreamingV2URL,
			APIKey:       "test-api-key",
			ResourceID:   "volc.bigasr.sauc.duration",
		},
		"endpoint": {
			WebSocketURL: "wss://example.com/api/v3/sauc/bigmodel_async",
			APIKey:       "test-api-key",
			ResourceID:   volcengineResourceDuration,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewVolcengineStreamingProvider(config); err == nil {
				t.Fatalf("accepted non-Doubao-v2 configuration: %#v", config)
			}
		})
	}
}

func TestVolcengineDefiniteUtteranceMapsTheCompleteResultToADefiniteSnapshot(t *testing.T) {
	event, ok := parseVolcengineServerFrame(volcengineServerFrame(3, map[string]interface{}{
		"code": 1000,
		"result": map[string]interface{}{
			"text": "已经稳定的分句。下一句仍在识别",
			"utterances": []map[string]interface{}{
				{"text": "已经稳定的分句。", "definite": true},
				{"text": "下一句仍在识别", "definite": false},
			},
		},
	}))
	if !ok || event.Type != STTEventDefinite || event.Text != "已经稳定的分句。下一句仍在识别" {
		t.Fatalf("event=%#v ok=%v", event, ok)
	}
}

func TestVolcengineEmptyFinalResponseCompletesTheSession(t *testing.T) {
	event, ok := parseVolcengineServerFrame(volcengineServerFrameWithFlags(2, volcengineFinalFlags, map[string]interface{}{
		"code":   1000,
		"result": map[string]interface{}{"text": ""},
	}))
	if !ok || event.Type != STTEventFinal || event.Text != "" {
		t.Fatalf("event=%#v ok=%v", event, ok)
	}
}

func TestVolcengineAsyncAudioFramesUseAutoAssignedSequence(t *testing.T) {
	audio := volcengineClientAudioFrame([]byte{1, 2}, false)
	if audio[1] != 0x20 {
		t.Fatalf("audio frame=%x", audio)
	}
	if length := binary.BigEndian.Uint32(audio[4:8]); length != 2 {
		t.Fatalf("audio payload length=%d", length)
	}

	finish := volcengineClientAudioFrame(nil, true)
	if finish[1] != 0x22 {
		t.Fatalf("finish frame=%x", finish)
	}
	if length := binary.BigEndian.Uint32(finish[4:8]); length != 0 {
		t.Fatalf("finish payload length=%d", length)
	}
}

func TestVolcengineErrorFramePreservesProviderMessage(t *testing.T) {
	event, ok := parseVolcengineServerFrame(volcengineServerErrorFrame(45000000, map[string]interface{}{
		"error_code":    "45000000",
		"error_message": "invalid audio sample_rate",
	}))
	if !ok {
		t.Fatal("error frame was not parsed")
	}
	if event.Type != STTEventError || event.Code != "45000000" || event.Message != "invalid audio sample_rate" {
		t.Fatalf("event=%#v", event)
	}
}

func TestVolcengineErrorFramePreservesPlainTextMessage(t *testing.T) {
	event, ok := parseVolcengineServerFrame(volcengineServerErrorFrameBytes(45000000, []byte("invalid request payload")))
	if !ok || event.Message != "invalid request payload" {
		t.Fatalf("event=%#v ok=%v", event, ok)
	}
}

func TestVolcengineErrorFrameExtractsErrorField(t *testing.T) {
	event, ok := parseVolcengineServerFrame(volcengineServerErrorFrame(45000000, map[string]interface{}{
		"error": "sequence mismatch",
	}))
	if !ok || event.Message != "sequence mismatch" {
		t.Fatalf("event=%#v ok=%v", event, ok)
	}
}

func volcengineServerFrame(sequence int32, payload interface{}) []byte {
	return volcengineServerFrameWithFlags(sequence, 0x01, payload)
}

func volcengineServerFrameWithFlags(sequence int32, flags byte, payload interface{}) []byte {
	encoded, _ := json.Marshal(payload)
	frame := []byte{0x11, 0x90 | flags, 0x10, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(frame[4:8], uint32(sequence))
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(encoded)))
	return append(frame, encoded...)
}

func volcengineServerErrorFrame(code int32, payload interface{}) []byte {
	encoded, _ := json.Marshal(payload)
	return volcengineServerErrorFrameBytes(code, encoded)
}

func volcengineServerErrorFrameBytes(code int32, encoded []byte) []byte {
	frame := []byte{0x11, 0xf0, 0x10, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(frame[4:8], uint32(code))
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(encoded)))
	return append(frame, encoded...)
}
