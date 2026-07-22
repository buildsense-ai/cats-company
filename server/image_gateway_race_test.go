package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func raceTestProvider(id, generationURL, editURL string, operations ...imageProviderOperation) imageUpstreamProvider {
	capabilities := make(map[imageProviderOperation]struct{}, len(operations))
	for _, operation := range operations {
		capabilities[operation] = struct{}{}
	}
	return imageUpstreamProvider{
		id:            id,
		generationURL: generationURL,
		editURL:       editURL,
		model:         "gpt-image-2",
		apiKey:        id + "-secret",
		client:        &http.Client{Timeout: 2 * time.Second},
		operations:    capabilities,
		editTransport: imageEditTransportJSONDataURL,
	}
}

func runRaceGeneration(t *testing.T, handler *ImageGenerationProxyHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.HandleGenerate(rr, req)
	return rr
}

func waitForScriptedCancellation(t *testing.T, upstream *scriptedImageUpstream) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		_, cancellations, _ := upstream.Snapshot()
		if cancellations > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("losing upstream was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestImageRaceFirstValidCompletedImageWins(t *testing.T) {
	slow := newScriptedImageUpstream(t, scriptedImageStep{delay: 300 * time.Millisecond, body: testImageResponse(t, 21)})
	fast := newScriptedImageUpstream(t, scriptedImageStep{delay: 40 * time.Millisecond, body: testImageResponse(t, 22)})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("slow", slow.URL(), "", imageOperationGeneration),
		raceTestProvider("fast", fast.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second})

	rr := runRaceGeneration(t, handler, `{"prompt":"test","async":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-CatsCo-Image-Provider") != "fast" || rr.Body.String() != testImageResponse(t, 22) {
		t.Fatalf("unexpected winner: provider=%q body=%s", rr.Header().Get("X-CatsCo-Image-Provider"), rr.Body.String())
	}
	if rr.Header().Get("X-CatsCo-Image-Total-Attempts") != "2" {
		t.Fatalf("both dispatched attempts were not counted: %q", rr.Header().Get("X-CatsCo-Image-Total-Attempts"))
	}
	waitForScriptedCancellation(t, slow)
	_, _, fastPayloads := fast.Snapshot()
	if _, sentAsync := fastPayloads[0]["async"]; sentAsync {
		t.Fatalf("race forwarded async task mode: %#v", fastPayloads[0])
	}
}

func TestImageRaceIgnoresFastErrorsAndIncompleteSuccess(t *testing.T) {
	tests := []struct {
		name string
		step scriptedImageStep
	}{
		{name: "fast 503", step: scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}},
		{name: "malformed 200", step: scriptedImageStep{status: http.StatusOK, body: `{"data":[{"b64_json":"not-base64"}]}`}},
		{name: "task id is not complete", step: scriptedImageStep{status: http.StatusOK, body: `{"task_id":"task-123","status":"processing"}`}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := newScriptedImageUpstream(t, tc.step)
			winner := newScriptedImageUpstream(t, scriptedImageStep{delay: 30 * time.Millisecond, body: testImageResponse(t, 31)})
			handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
				raceTestProvider("first", first.URL(), "", imageOperationGeneration),
				raceTestProvider("winner", winner.URL(), "", imageOperationGeneration),
			}, ImageGenerationProxyOptions{RaceDeadline: time.Second})

			rr := runRaceGeneration(t, handler, `{"prompt":"test"}`)
			if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Provider") != "winner" {
				t.Fatalf("status=%d provider=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Provider"), rr.Body.String())
			}
		})
	}
}

func TestImageRaceRejectsUnverifiedURLAndWaitsForCompletedImage(t *testing.T) {
	urlOnly := newScriptedImageUpstream(t, scriptedImageStep{
		body: `{"data":[{"url":"https://cdn.example.test/upstream-error.html"}]}`,
	})
	winner := newScriptedImageUpstream(t, scriptedImageStep{
		delay: 30 * time.Millisecond,
		body:  testImageResponse(t, 32),
	})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("url-only", urlOnly.URL(), "", imageOperationGeneration),
		raceTestProvider("winner", winner.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second})

	rr := runRaceGeneration(t, handler, `{"prompt":"test"}`)
	if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Provider") != "winner" {
		t.Fatalf("status=%d provider=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Provider"), rr.Body.String())
	}
}

func TestImageRaceCancelsHangingLoser(t *testing.T) {
	hanging := newScriptedImageUpstream(t, scriptedImageStep{waitForCancel: true})
	winner := newScriptedImageUpstream(t, scriptedImageStep{delay: 20 * time.Millisecond, body: testImageResponse(t, 41)})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("hanging", hanging.URL(), "", imageOperationGeneration),
		raceTestProvider("winner", winner.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second})

	rr := runRaceGeneration(t, handler, `{"prompt":"test"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	waitForScriptedCancellation(t, hanging)
}

func TestImageEditRaceUsesJSONAndMultipartTransportsConcurrently(t *testing.T) {
	jsonStarted := make(chan struct{}, 1)
	jsonCancelled := make(chan struct{}, 1)
	jsonUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonStarted <- struct{}{}
		<-r.Context().Done()
		jsonCancelled <- struct{}{}
	}))
	t.Cleanup(jsonUpstream.Close)
	type multipartCapture struct {
		prompt     string
		model      string
		files      int
		fileBytes  [][]byte
		authorized bool
	}
	captures := make(chan multipartCapture, 1)
	multipartUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(20 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		capture := multipartCapture{
			prompt:     r.FormValue("prompt"),
			model:      r.FormValue("model"),
			files:      len(r.MultipartForm.File["image"]),
			authorized: r.Header.Get("Authorization") == "Bearer multipart-secret",
		}
		for _, header := range r.MultipartForm.File["image"] {
			file, err := header.Open()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			contents, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			capture.fileBytes = append(capture.fileBytes, contents)
		}
		select {
		case <-jsonStarted:
		case <-time.After(time.Second):
			http.Error(w, "JSON provider did not start concurrently", http.StatusGatewayTimeout)
			return
		}
		captures <- capture
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testImageResponse(t, 52)))
	}))
	t.Cleanup(multipartUpstream.Close)

	jsonProvider := raceTestProvider("code-newcli", jsonUpstream.URL+"/v1/images/generations", jsonUpstream.URL+"/v1/images/edits", imageOperationGeneration, imageOperationEdit)
	multipartProvider := raceTestProvider("pptoken", multipartUpstream.URL+"/v1/images/generations", multipartUpstream.URL+"/v1/images/edits", imageOperationGeneration, imageOperationEdit)
	multipartProvider.apiKey = "multipart-secret"
	multipartProvider.editTransport = imageEditTransportMultipart
	handler := newImageGenerationProxyHandlerWithProviders(
		[]imageUpstreamProvider{jsonProvider, multipartProvider},
		ImageGenerationProxyOptions{RaceDeadline: time.Second},
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/images/edits",
		strings.NewReader(imageEditBody(
			"preserve identity",
			testPNGDataURL(61),
			testPNGDataURL(62),
			testPNGDataURL(63),
		)),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.HandleEdit(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Provider") != "pptoken" {
		t.Fatalf("status=%d provider=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Provider"), rr.Body.String())
	}
	capture := <-captures
	if capture.prompt != "preserve identity" || capture.model != "gpt-image-2" || capture.files != 3 || !capture.authorized {
		t.Fatalf("unexpected multipart request: %#v", capture)
	}
	for index, contents := range capture.fileBytes {
		if !imageBytesMatchMediaType(contents, "image/png") {
			t.Fatalf("multipart reference %d is not an image", index+1)
		}
	}
	select {
	case <-jsonCancelled:
	case <-time.After(time.Second):
		t.Fatal("losing JSON upstream was not cancelled")
	}
}

func TestImageRaceProviderLanesRetryIndependently(t *testing.T) {
	relayA := newScriptedImageUpstream(t, scriptedImageStep{waitForCancel: true})
	relayB := newScriptedImageUpstream(t,
		scriptedImageStep{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
		scriptedImageStep{body: testImageResponse(t, 71)},
	)
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("relay-a", relayA.URL(), "", imageOperationGeneration),
		raceTestProvider("relay-b", relayB.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second, RetryBackoff: 5 * time.Millisecond})

	rr := runRaceGeneration(t, handler, `{"prompt":"retry test"}`)
	if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Round") != "2" {
		t.Fatalf("status=%d round=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Round"), rr.Body.String())
	}
	aRequests, _, _ := relayA.Snapshot()
	bRequests, _, _ := relayB.Snapshot()
	if aRequests != 1 || bRequests != 2 {
		t.Fatalf("relay-a requests=%d relay-b requests=%d", aRequests, bRequests)
	}
	waitForScriptedCancellation(t, relayA)
}

func TestImageRaceCapsAttemptsPerProvider(t *testing.T) {
	rateLimited := scriptedImageStep{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`}
	relayA := newScriptedImageUpstream(t, rateLimited, rateLimited, scriptedImageStep{body: testImageResponse(t, 72)})
	relayB := newScriptedImageUpstream(t, rateLimited, rateLimited, scriptedImageStep{body: testImageResponse(t, 73)})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("relay-a", relayA.URL(), "", imageOperationGeneration),
		raceTestProvider("relay-b", relayB.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{
		RaceDeadline:           time.Second,
		RetryBackoff:           time.Millisecond,
		MaxAttemptsPerProvider: 2,
	})

	rr := runRaceGeneration(t, handler, `{"prompt":"bounded retries"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d round=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Round"), rr.Body.String())
	}
	aRequests, _, _ := relayA.Snapshot()
	bRequests, _, _ := relayB.Snapshot()
	if aRequests != 2 || bRequests != 2 || rr.Header().Get("X-CatsCo-Image-Total-Attempts") != "4" {
		t.Fatalf("attempts relay-a=%d relay-b=%d total=%q", aRequests, bRequests, rr.Header().Get("X-CatsCo-Image-Total-Attempts"))
	}
}

func TestImageRaceRetriesOnlyExplicitRateLimits(t *testing.T) {
	tests := []struct {
		name      string
		result    imageAttemptResult
		retryable bool
	}{
		{name: "429", result: imageAttemptResult{category: imageAttemptTransient, status: http.StatusTooManyRequests}, retryable: true},
		{name: "network error", result: imageAttemptResult{category: imageAttemptTransient, reason: "network_error"}},
		{name: "timeout", result: imageAttemptResult{category: imageAttemptTransient, reason: "timeout"}},
		{name: "5xx", result: imageAttemptResult{category: imageAttemptTransient, status: http.StatusServiceUnavailable}},
		{name: "invalid 200", result: imageAttemptResult{category: imageAttemptTransient, status: http.StatusOK, reason: "invalid_completed_image"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetryImageAttempt(tc.result); got != tc.retryable {
				t.Fatalf("retryable=%t, want %t", got, tc.retryable)
			}
		})
	}
}

func TestImageRaceDoesNotRetryAmbiguousProviderOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		first         scriptedImageStep
		clientTimeout time.Duration
	}{
		{name: "5xx", first: scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}},
		{name: "timeout", first: scriptedImageStep{delay: 50 * time.Millisecond, body: testImageResponse(t, 74)}, clientTimeout: 10 * time.Millisecond},
		{name: "invalid 200", first: scriptedImageStep{body: `{"data":[{"b64_json":"not-base64"}]}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ambiguous := newScriptedImageUpstream(t, tc.first, scriptedImageStep{body: testImageResponse(t, 75)})
			rejected := newScriptedImageUpstream(t, scriptedImageStep{status: http.StatusBadRequest, body: `{"error":"rejected"}`})
			provider := raceTestProvider("ambiguous", ambiguous.URL(), "", imageOperationGeneration)
			if tc.clientTimeout > 0 {
				provider.client.Timeout = tc.clientTimeout
			}
			handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
				provider,
				raceTestProvider("rejected", rejected.URL(), "", imageOperationGeneration),
			}, ImageGenerationProxyOptions{
				RaceDeadline:           time.Second,
				RetryBackoff:           time.Millisecond,
				MaxAttemptsPerProvider: 2,
			})

			rr := runRaceGeneration(t, handler, `{"prompt":"do not duplicate"}`)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			requests, _, _ := ambiguous.Snapshot()
			if requests != 1 {
				t.Fatalf("ambiguous provider requests=%d, want 1", requests)
			}
		})
	}
}

func TestImageRaceExcludesProviderAfterAuthenticationFailure(t *testing.T) {
	unauthorized := newScriptedImageUpstream(t, scriptedImageStep{status: http.StatusUnauthorized, body: `{"error":"bad key"}`})
	recovering := newScriptedImageUpstream(t,
		scriptedImageStep{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
		scriptedImageStep{body: testImageResponse(t, 73)},
	)
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("unauthorized", unauthorized.URL(), "", imageOperationGeneration),
		raceTestProvider("recovering", recovering.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second, RetryBackoff: time.Millisecond})

	rr := runRaceGeneration(t, handler, `{"prompt":"auth exclusion"}`)
	if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Round") != "2" {
		t.Fatalf("status=%d round=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Round"), rr.Body.String())
	}
	requests, _, _ := unauthorized.Snapshot()
	if requests != 1 {
		t.Fatalf("unauthorized provider requests=%d, want 1", requests)
	}
}

func TestImageRaceStopsWhenAllProvidersRejectRequest(t *testing.T) {
	relayA := newScriptedImageUpstream(t, scriptedImageStep{status: http.StatusBadRequest, body: `{"error":"invalid prompt"}`})
	relayB := newScriptedImageUpstream(t, scriptedImageStep{status: http.StatusUnprocessableEntity, body: `{"error":"content rejected"}`})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("relay-a", relayA.URL(), "", imageOperationGeneration),
		raceTestProvider("relay-b", relayB.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second, RetryBackoff: time.Millisecond})

	rr := runRaceGeneration(t, handler, `{"prompt":"rejected"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error.Code != string(imageRaceRequestRejected) {
		t.Fatalf("unexpected error body: %s", rr.Body.String())
	}
	if rr.Header().Get("X-CatsCo-Image-Rounds") != "1" {
		t.Fatalf("rounds=%q", rr.Header().Get("X-CatsCo-Image-Rounds"))
	}
}

func TestImageRaceStopsStartingRoundsAfterClientCancellation(t *testing.T) {
	relayA := newScriptedImageUpstream(t, scriptedImageStep{waitForCancel: true})
	relayB := newScriptedImageUpstream(t, scriptedImageStep{waitForCancel: true})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("relay-a", relayA.URL(), "", imageOperationGeneration),
		raceTestProvider("relay-b", relayB.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second, RetryBackoff: 200 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"cancel"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.HandleGenerate(rr, req)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		aRequests, _, _ := relayA.Snapshot()
		bRequests, _, _ := relayB.Snapshot()
		if aRequests == 1 && bRequests == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first round did not reach both providers")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled handler did not return")
	}
	time.Sleep(250 * time.Millisecond)
	aRequests, _, _ := relayA.Snapshot()
	bRequests, _, _ := relayB.Snapshot()
	if aRequests != 1 || bRequests != 1 {
		t.Fatalf("new round started after cancellation: relay-a=%d relay-b=%d", aRequests, bRequests)
	}
}

func TestImageRaceReturnsRaceExhaustedAtDeadline(t *testing.T) {
	relayA := newScriptedImageUpstream(t, scriptedImageStep{waitForCancel: true})
	relayB := newScriptedImageUpstream(t, scriptedImageStep{waitForCancel: true})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("relay-a", relayA.URL(), "", imageOperationGeneration),
		raceTestProvider("relay-b", relayB.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: 70 * time.Millisecond, RetryBackoff: 5 * time.Millisecond})

	rr := runRaceGeneration(t, handler, `{"prompt":"deadline"}`)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error.Code != string(imageRaceExhausted) {
		t.Fatalf("unexpected error body: %s", rr.Body.String())
	}
	rounds, err := strconv.Atoi(rr.Header().Get("X-CatsCo-Image-Rounds"))
	if err != nil || rounds < 1 {
		t.Fatalf("rounds=%q", rr.Header().Get("X-CatsCo-Image-Rounds"))
	}
	// Let requests already dispatched in the final round observe cancellation
	// before checking that no later round starts.
	time.Sleep(30 * time.Millisecond)
	aBefore, _, _ := relayA.Snapshot()
	bBefore, _, _ := relayB.Snapshot()
	time.Sleep(30 * time.Millisecond)
	aAfter, _, _ := relayA.Snapshot()
	bAfter, _, _ := relayB.Snapshot()
	if aAfter != aBefore || bAfter != bBefore {
		t.Fatalf("requests continued after deadline: before=%d/%d after=%d/%d", aBefore, bBefore, aAfter, bAfter)
	}
}
