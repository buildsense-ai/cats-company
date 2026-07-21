package server

import (
	"context"
	"encoding/json"
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

func TestImageRaceFiltersProvidersByEditCapability(t *testing.T) {
	generationOnly := newScriptedImageUpstream(t, scriptedImageStep{body: testImageResponse(t, 51)})
	editCapable := newScriptedImageUpstream(t, scriptedImageStep{body: testImageResponse(t, 52)})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("generation-only", generationOnly.URL(), "", imageOperationGeneration),
		raceTestProvider("edit-capable", editCapable.URL(), editCapable.URL(), imageOperationGeneration, imageOperationEdit),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/images/edits",
		strings.NewReader(imageEditBody("preserve identity", testPNGDataURL(61))),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.HandleEdit(rr, req)

	if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Provider") != "edit-capable" {
		t.Fatalf("status=%d provider=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Provider"), rr.Body.String())
	}
	generationRequests, _, _ := generationOnly.Snapshot()
	editRequests, _, _ := editCapable.Snapshot()
	if generationRequests != 0 || editRequests != 1 {
		t.Fatalf("generation-only requests=%d edit-capable requests=%d", generationRequests, editRequests)
	}
}

func TestImageRaceRetriesRoundsUntilCompletedImage(t *testing.T) {
	relayA := newScriptedImageUpstream(t,
		scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`},
		scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`},
	)
	relayB := newScriptedImageUpstream(t,
		scriptedImageStep{status: http.StatusBadGateway, body: `{"error":"temporary"}`},
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
	if aRequests < 1 || aRequests > 2 || bRequests != 2 {
		t.Fatalf("relay-a requests=%d relay-b requests=%d", aRequests, bRequests)
	}
}

func TestImageRaceHasNoFixedLowRetryLimit(t *testing.T) {
	temporary := scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}
	relayA := newScriptedImageUpstream(t, temporary, temporary, temporary, temporary)
	relayB := newScriptedImageUpstream(t, temporary, temporary, temporary, scriptedImageStep{body: testImageResponse(t, 72)})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("relay-a", relayA.URL(), "", imageOperationGeneration),
		raceTestProvider("relay-b", relayB.URL(), "", imageOperationGeneration),
	}, ImageGenerationProxyOptions{RaceDeadline: time.Second, RetryBackoff: time.Millisecond})

	rr := runRaceGeneration(t, handler, `{"prompt":"fourth round"}`)
	if rr.Code != http.StatusOK || rr.Header().Get("X-CatsCo-Image-Round") != "4" {
		t.Fatalf("status=%d round=%q body=%s", rr.Code, rr.Header().Get("X-CatsCo-Image-Round"), rr.Body.String())
	}
}

func TestImageRaceExcludesProviderAfterAuthenticationFailure(t *testing.T) {
	unauthorized := newScriptedImageUpstream(t, scriptedImageStep{status: http.StatusUnauthorized, body: `{"error":"bad key"}`})
	recovering := newScriptedImageUpstream(t,
		scriptedImageStep{status: http.StatusServiceUnavailable, body: `{"error":"temporary"}`},
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
	relayA := newScriptedImageUpstream(t)
	relayB := newScriptedImageUpstream(t)
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
	relayA := newScriptedImageUpstream(t)
	relayB := newScriptedImageUpstream(t)
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
