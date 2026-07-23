package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type scriptedImageStep struct {
	delay         time.Duration
	status        int
	body          string
	waitForCancel bool
}

type scriptedImageUpstream struct {
	server *httptest.Server

	mu             sync.Mutex
	steps          []scriptedImageStep
	requests       int
	cancellations  int
	payloads       []map[string]interface{}
	authorizations []string
}

func newScriptedImageUpstream(t *testing.T, steps ...scriptedImageStep) *scriptedImageUpstream {
	t.Helper()
	upstream := &scriptedImageUpstream{steps: append([]scriptedImageStep(nil), steps...)}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid test payload", http.StatusBadRequest)
			return
		}

		upstream.mu.Lock()
		index := upstream.requests
		upstream.requests++
		upstream.payloads = append(upstream.payloads, payload)
		upstream.authorizations = append(upstream.authorizations, r.Header.Get("Authorization"))
		step := scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"script exhausted"}`}
		if index < len(upstream.steps) {
			step = upstream.steps[index]
		}
		upstream.mu.Unlock()

		if step.waitForCancel {
			<-r.Context().Done()
			upstream.mu.Lock()
			upstream.cancellations++
			upstream.mu.Unlock()
			return
		}
		if step.delay > 0 {
			select {
			case <-time.After(step.delay):
			case <-r.Context().Done():
				upstream.mu.Lock()
				upstream.cancellations++
				upstream.mu.Unlock()
				return
			}
		}

		status := step.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(step.body))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *scriptedImageUpstream) URL() string {
	return u.server.URL + "/v1/images/generations"
}

func (u *scriptedImageUpstream) Snapshot() (requests int, cancellations int, payloads []map[string]interface{}) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests, u.cancellations, append([]map[string]interface{}(nil), u.payloads...)
}

func (u *scriptedImageUpstream) AuthorizationSnapshot() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.authorizations...)
}

func testImageResponse(t *testing.T, marker byte) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: marker, G: 32, B: 64, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}
	payload := map[string]interface{}{
		"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(encoded.Bytes())}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode test response: %v", err)
	}
	return string(body)
}

func TestScriptedImageUpstreamTracksSequenceAndCancellation(t *testing.T) {
	upstream := newScriptedImageUpstream(t,
		scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`},
		scriptedImageStep{waitForCancel: true},
	)

	resp, err := http.Post(upstream.URL(), "application/json", bytes.NewBufferString(`{"prompt":"first"}`))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first status = %d", resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL(), bytes.NewBufferString(`{"prompt":"second"}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = http.DefaultClient.Do(req)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		requests, _, _ := upstream.Snapshot()
		if requests == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second request did not reach scripted upstream")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not return")
	}
	waitForScriptedCancellation(t, upstream)

	requests, cancellations, payloads := upstream.Snapshot()
	if requests != 2 || cancellations != 1 {
		t.Fatalf("requests=%d cancellations=%d", requests, cancellations)
	}
	if payloads[0]["prompt"] != "first" || payloads[1]["prompt"] != "second" {
		t.Fatalf("unexpected payloads: %#v", payloads)
	}
}
