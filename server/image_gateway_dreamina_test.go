package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dreaminaFallbackTestHandler(
	t *testing.T,
	worker *httptest.Server,
	status int,
) (*ImageGenerationProxyHandler, *atomic.Int32, []*scriptedImageUpstream) {
	t.Helper()
	first := newScriptedImageUpstream(t, scriptedImageStep{status: status, body: `{"error":"unavailable"}`})
	second := newScriptedImageUpstream(t, scriptedImageStep{status: status, body: `{"error":"unavailable"}`})
	third := newScriptedImageUpstream(t, scriptedImageStep{status: status, body: `{"error":"unavailable"}`})
	handler := newImageGenerationProxyHandlerWithProviders([]imageUpstreamProvider{
		raceTestProvider("first", first.URL(), first.server.URL+"/v1/images/edits", imageOperationGeneration, imageOperationEdit),
		raceTestProvider("second", second.URL(), second.server.URL+"/v1/images/edits", imageOperationGeneration, imageOperationEdit),
		raceTestProvider("third", third.URL(), third.server.URL+"/v1/images/edits", imageOperationGeneration, imageOperationEdit),
	}, ImageGenerationProxyOptions{
		RaceDeadline:           time.Second,
		MaxAttemptsPerProvider: 1,
	})
	calls := &atomic.Int32{}
	handler.dreaminaWorker = &dreaminaWorkerClient{
		baseURL: worker.URL,
		client: &http.Client{
			Timeout: time.Second,
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				return http.DefaultTransport.RoundTrip(request)
			}),
		},
	}
	return handler, calls, []*scriptedImageUpstream{first, second, third}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestImageRaceFallsBackToDreaminaWorker(t *testing.T) {
	var receivedUID string
	var receivedPayload map[string]interface{}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUID = r.Header.Get("X-CatsCo-Owner-UID")
		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			t.Fatalf("decode worker payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"dreamina_task_1","status":"processing"}`))
	}))
	t.Cleanup(worker.Close)
	handler, calls, image2Providers := dreaminaFallbackTestHandler(t, worker, http.StatusServiceUnavailable)

	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"fallback test","size":"1024x1024"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(42)))
	response := httptest.NewRecorder()
	handler.HandleGenerate(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("Dreamina calls=%d, want 1", calls.Load())
	}
	for index, provider := range image2Providers {
		requests, _, _ := provider.Snapshot()
		if requests != 1 {
			t.Fatalf("Image2 provider %d requests=%d, want 1 before Dreamina fallback", index+1, requests)
		}
	}
	if receivedUID != "42" || receivedPayload["prompt"] != "fallback test" {
		t.Fatalf("uid=%q payload=%#v", receivedUID, receivedPayload)
	}
	if response.Header().Get("X-CatsCo-Image-Provider") != "dreamina" {
		t.Fatalf("provider=%q", response.Header().Get("X-CatsCo-Image-Provider"))
	}
}

func TestImageRequestRejectionDoesNotFallBack(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Dreamina worker must not receive a rejected request")
	}))
	t.Cleanup(worker.Close)
	handler, calls, _ := dreaminaFallbackTestHandler(t, worker, http.StatusBadRequest)

	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"rejected"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(42)))
	response := httptest.NewRecorder()
	handler.HandleGenerate(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("Dreamina calls=%d, want 0", calls.Load())
	}
}

func TestExplicitDreaminaBypassesImageRace(t *testing.T) {
	var providerRole string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRole = r.Header.Get("X-CatsCo-Dreamina-Provider-Role")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"dreamina_task_1","status":"processing"}`))
	}))
	t.Cleanup(worker.Close)
	handler, calls, image2Providers := dreaminaFallbackTestHandler(t, worker, http.StatusInternalServerError)
	handler.configError = errors.New("Image2 is unavailable")

	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"direct dreamina"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(imageProviderPolicyHeader, "dreamina")
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(42)))
	response := httptest.NewRecorder()
	handler.HandleGenerate(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if calls.Load() != 1 || providerRole != "primary" {
		t.Fatalf("Dreamina calls=%d provider_role=%q", calls.Load(), providerRole)
	}
	for index, provider := range image2Providers {
		requests, _, _ := provider.Snapshot()
		if requests != 0 {
			t.Fatalf("Image2 provider %d requests=%d, want 0 for explicit Dreamina", index+1, requests)
		}
	}
	if response.Header().Get("X-CatsCo-Image-Race-Id") != "" {
		t.Fatalf("direct Dreamina request unexpectedly has race ID %q", response.Header().Get("X-CatsCo-Image-Race-Id"))
	}
}

func TestExplicitImage2DoesNotFallBack(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Dreamina worker must not receive an Image2-only request")
	}))
	t.Cleanup(worker.Close)
	handler, calls, image2Providers := dreaminaFallbackTestHandler(t, worker, http.StatusServiceUnavailable)

	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"image2 only"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(imageProviderPolicyHeader, "image2")
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(42)))
	response := httptest.NewRecorder()
	handler.HandleGenerate(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("Dreamina calls=%d, want 0", calls.Load())
	}
	for index, provider := range image2Providers {
		requests, _, _ := provider.Snapshot()
		if requests != 1 {
			t.Fatalf("Image2 provider %d requests=%d, want 1", index+1, requests)
		}
	}
}

func TestDreaminaTaskPollingPreservesOwner(t *testing.T) {
	var receivedUID string
	var receivedPath string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUID = r.Header.Get("X-CatsCo-Owner-UID")
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"dreamina_task_1","status":"completed","data":[{"b64_json":"abc"}]}`))
	}))
	t.Cleanup(worker.Close)
	handler := &ImageGenerationProxyHandler{
		dreaminaWorker:   &dreaminaWorkerClient{baseURL: worker.URL, client: worker.Client()},
		maxResponseBytes: defaultImageGenerationMaxResponseBytes,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/tasks/dreamina_task_1", nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(77)))
	response := httptest.NewRecorder()
	handler.HandleTask(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if receivedUID != "77" || receivedPath != "/v1/tasks/dreamina_task_1" {
		t.Fatalf("uid=%q path=%q", receivedUID, receivedPath)
	}
}
