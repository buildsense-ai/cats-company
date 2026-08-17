package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func imageUpscaleTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var body bytes.Buffer
	if err := jpeg.Encode(&body, image.NewRGBA(image.Rect(0, 0, width, height)), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func imageUpscaleTestRequest(t *testing.T, targetURL string, imageBytes []byte, width, height int) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image_file", "source.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(imageBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("target_width", strconv.Itoa(width)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("target_height", strconv.Itoa(height)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("model", "Standard V2"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, targetURL, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

type topazFakeCapture struct {
	mu             sync.Mutex
	apiKeys        []string
	downloadKeys   []string
	submitCalls    int
	statusCalls    int
	downloadCalls  int
	imageBytes     []byte
	model          string
	outputWidth    string
	outputHeight   string
	outputFormat   string
	processID      string
	statusSequence []string
}

func newTopazFakeServer(t *testing.T, result []byte, statusSequence ...string) (*httptest.Server, *topazFakeCapture) {
	t.Helper()
	capture := &topazFakeCapture{
		processID:      "fake-process-1",
		statusSequence: append([]string(nil), statusSequence...),
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/image/v1/enhance/async", func(w http.ResponseWriter, r *http.Request) {
		capture.mu.Lock()
		capture.submitCalls++
		capture.apiKeys = append(capture.apiKeys, r.Header.Get("X-API-Key"))
		capture.mu.Unlock()
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			http.Error(w, "invalid multipart", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "missing image", http.StatusBadRequest)
			return
		}
		defer file.Close()
		imageBytes, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "cannot read image", http.StatusBadRequest)
			return
		}
		capture.mu.Lock()
		capture.imageBytes = imageBytes
		capture.model = r.FormValue("model")
		capture.outputWidth = r.FormValue("output_width")
		capture.outputHeight = r.FormValue("output_height")
		capture.outputFormat = r.FormValue("output_format")
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"process_id": capture.processID,
			"source_id":  "fake-source-1",
			"eta":        time.Now().Add(time.Second).Unix(),
		})
	})
	mux.HandleFunc("/image/v1/status/fake-process-1", func(w http.ResponseWriter, r *http.Request) {
		capture.mu.Lock()
		capture.statusCalls++
		capture.apiKeys = append(capture.apiKeys, r.Header.Get("X-API-Key"))
		index := capture.statusCalls - 1
		status := "Completed"
		if index < len(capture.statusSequence) {
			status = capture.statusSequence[index]
		}
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"process_id":    "fake-process-1",
			"status":        status,
			"progress":      100,
			"output_width":  64,
			"output_height": 36,
			"output_format": "jpeg",
		})
	})
	mux.HandleFunc("/image/v1/download/fake-process-1", func(w http.ResponseWriter, r *http.Request) {
		capture.mu.Lock()
		capture.downloadCalls++
		capture.apiKeys = append(capture.apiKeys, r.Header.Get("X-API-Key"))
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"download_url": server.URL + "/download/fake-process-1"})
	})
	mux.HandleFunc("/download/fake-process-1", func(w http.ResponseWriter, r *http.Request) {
		capture.mu.Lock()
		capture.downloadKeys = append(capture.downloadKeys, r.Header.Get("X-API-Key"))
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(result)
	})
	server = httptest.NewServer(mux)
	return server, capture
}

func TestImageUpscaleProxySubmitsTopazTaskAndDownloadsResult(t *testing.T) {
	source := imageUpscaleTestJPEG(t, 32, 18)
	result := imageUpscaleTestJPEG(t, 64, 36)
	upstream, capture := newTopazFakeServer(t, result)
	defer upstream.Close()

	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{
		APIKey:  "provider-secret",
		Timeout: time.Second,
	})
	submit := imageUpscaleTestRequest(t, "/v1/images/upscale", source, 64, 36)
	submitRecorder := httptest.NewRecorder()
	handler.HandleUpscale(submitRecorder, submit)
	if submitRecorder.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitRecorder.Code, submitRecorder.Body.String())
	}
	var task map[string]interface{}
	if err := json.Unmarshal(submitRecorder.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if task["task_id"] != "fake-process-1" || task["status"] != "processing" {
		t.Fatalf("unexpected task response: %#v", task)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/images/upscale/tasks/fake-process-1?target_width=64&target_height=36", nil)
	statusRecorder := httptest.NewRecorder()
	handler.HandleUpscaleTask(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("task status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}
	if !bytes.Equal(statusRecorder.Body.Bytes(), result) {
		t.Fatal("gateway changed the downloaded result bytes")
	}
	if statusRecorder.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content type=%q", statusRecorder.Header().Get("Content-Type"))
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.submitCalls != 1 || capture.statusCalls != 1 || capture.downloadCalls != 1 {
		t.Fatalf("unexpected provider calls: submit=%d status=%d download=%d", capture.submitCalls, capture.statusCalls, capture.downloadCalls)
	}
	if !bytes.Equal(capture.imageBytes, source) {
		t.Fatal("gateway changed the source image bytes")
	}
	if capture.model != "Standard V2" || capture.outputWidth != "64" || capture.outputHeight != "36" || capture.outputFormat != "jpeg" {
		t.Fatalf("unexpected Topaz fields: model=%q width=%q height=%q format=%q", capture.model, capture.outputWidth, capture.outputHeight, capture.outputFormat)
	}
	for _, key := range capture.apiKeys {
		if key != "provider-secret" {
			t.Fatalf("unexpected Topaz API key header %q", key)
		}
	}
	for _, key := range capture.downloadKeys {
		if key != "" {
			t.Fatalf("provider key leaked to presigned download: %q", key)
		}
	}
}

func TestImageUpscaleProxyWaitsForPendingTaskWithoutResubmitting(t *testing.T) {
	result := imageUpscaleTestJPEG(t, 64, 36)
	upstream, capture := newTopazFakeServer(t, result, "Pending", "Processing", "Completed")
	defer upstream.Close()
	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{APIKey: "secret"})

	submit := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 32, 18), 64, 36)
	submitRecorder := httptest.NewRecorder()
	handler.HandleUpscale(submitRecorder, submit)
	if submitRecorder.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d", submitRecorder.Code)
	}
	for index := 0; index < 3; index++ {
		request := httptest.NewRequest(http.MethodGet, "/v1/images/upscale/tasks/fake-process-1?target_width=64&target_height=36", nil)
		recorder := httptest.NewRecorder()
		handler.HandleUpscaleTask(recorder, request)
		if index < 2 && recorder.Code != http.StatusAccepted {
			t.Fatalf("poll %d status=%d body=%s", index, recorder.Code, recorder.Body.String())
		}
		if index == 2 && recorder.Code != http.StatusOK {
			t.Fatalf("final status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.submitCalls != 1 {
		t.Fatalf("submit calls=%d, want 1", capture.submitCalls)
	}
	if capture.statusCalls != 3 {
		t.Fatalf("status calls=%d, want 3", capture.statusCalls)
	}
}

func TestImageUpscaleProxyRejectsInvalidInputBeforeSubmitting(t *testing.T) {
	upstream, capture := newTopazFakeServer(t, imageUpscaleTestJPEG(t, 64, 36))
	defer upstream.Close()
	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{APIKey: "secret"})

	tooLargeTarget := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 2, 2), 7681, 4320)
	recorder := httptest.NewRecorder()
	handler.HandleUpscale(recorder, tooLargeTarget)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("target status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	invalidImage := imageUpscaleTestRequest(t, "/v1/images/upscale", []byte("not an image"), 3840, 2160)
	recorder = httptest.NewRecorder()
	handler.HandleUpscale(recorder, invalidImage)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("image status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.submitCalls != 0 {
		t.Fatalf("provider was called %d times for invalid input", capture.submitCalls)
	}
}

func TestImageUpscaleProxyDoesNotRetryProviderSubmission(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"temporarily unavailable"}`))
	}))
	defer upstream.Close()
	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{APIKey: "secret"})
	request := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 2, 2), 3840, 2160)
	recorder := httptest.NewRecorder()
	handler.HandleUpscale(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if calls != 1 {
		t.Fatalf("provider calls=%d, want 1", calls)
	}
}

func TestImageUpscaleProxyRejectsUnexpectedOutputSize(t *testing.T) {
	upstream, _ := newTopazFakeServer(t, imageUpscaleTestJPEG(t, 32, 18))
	defer upstream.Close()
	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{APIKey: "secret"})

	submit := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 16, 9), 64, 36)
	submitRecorder := httptest.NewRecorder()
	handler.HandleUpscale(submitRecorder, submit)
	if submitRecorder.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d", submitRecorder.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/images/upscale/tasks/fake-process-1?target_width=64&target_height=36", nil)
	recorder := httptest.NewRecorder()
	handler.HandleUpscaleTask(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestImageUpscaleProxyLoadsKeyFromFile(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "upscale-key")
	if err := os.WriteFile(secretPath, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATSCO_IMAGE_UPSCALE_API_KEY", "ignored")
	t.Setenv("CATSCO_IMAGE_UPSCALE_API_KEY_FILE", secretPath)
	t.Setenv("CATSCO_IMAGE_UPSCALE_URL", "http://127.0.0.1:1/image/v1/enhance/async")
	t.Setenv("CATSCO_IMAGE_UPSCALE_MODEL", "Standard V2")
	handler := NewImageUpscaleProxyHandlerFromEnv()
	if handler.ConfigError() != nil {
		t.Fatal(handler.ConfigError())
	}
	if handler.apiKey != "file-secret" {
		t.Fatalf("api key=%q", handler.apiKey)
	}
	if handler.submitURL.Path != "/image/v1/enhance/async" {
		t.Fatalf("submit path=%q", handler.submitURL.Path)
	}
}

func TestImageUpscaleProxyRetriesStatusWithoutResubmitting(t *testing.T) {
	result := imageUpscaleTestJPEG(t, 64, 36)
	statusCalls := 0
	submitCalls := 0
	serverURL := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/image/v1/enhance/async", func(w http.ResponseWriter, _ *http.Request) {
		submitCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"process_id": "retry-status-task"})
	})
	mux.HandleFunc("/image/v1/status/retry-status-task", func(w http.ResponseWriter, _ *http.Request) {
		statusCalls++
		if statusCalls < 3 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"process_id":    "retry-status-task",
			"status":        "Completed",
			"output_width":  64,
			"output_height": 36,
		})
	})
	mux.HandleFunc("/image/v1/download/retry-status-task", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"download_url": serverURL + "/download/retry-status-task"})
	})
	mux.HandleFunc("/download/retry-status-task", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(result)
	})
	upstream := httptest.NewServer(mux)
	serverURL = upstream.URL
	defer upstream.Close()

	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{
		APIKey:  "secret",
		Timeout: time.Second,
	})
	submit := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 32, 18), 64, 36)
	submitRecorder := httptest.NewRecorder()
	handler.HandleUpscale(submitRecorder, submit)
	if submitRecorder.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitRecorder.Code, submitRecorder.Body.String())
	}

	poll := httptest.NewRequest(http.MethodGet, "/v1/images/upscale/tasks/retry-status-task", nil)
	pollRecorder := httptest.NewRecorder()
	handler.HandleUpscaleTask(pollRecorder, poll)
	if pollRecorder.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", pollRecorder.Code, pollRecorder.Body.String())
	}
	if submitCalls != 1 || statusCalls != 3 {
		t.Fatalf("unexpected provider calls: submit=%d status=%d", submitCalls, statusCalls)
	}
}

func TestImageUpscaleProxyRetriesDownloadWithoutResubmitting(t *testing.T) {
	result := imageUpscaleTestJPEG(t, 64, 36)
	submitCalls := 0
	downloadCalls := 0
	serverURL := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/image/v1/enhance/async", func(w http.ResponseWriter, _ *http.Request) {
		submitCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"process_id": "retry-download-task"})
	})
	mux.HandleFunc("/image/v1/status/retry-download-task", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"process_id":    "retry-download-task",
			"status":        "Completed",
			"output_width":  64,
			"output_height": 36,
		})
	})
	mux.HandleFunc("/image/v1/download/retry-download-task", func(w http.ResponseWriter, _ *http.Request) {
		downloadCalls++
		if downloadCalls == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"download_url": serverURL + "/download/retry-download-task"})
	})
	mux.HandleFunc("/download/retry-download-task", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(result)
	})
	upstream := httptest.NewServer(mux)
	serverURL = upstream.URL
	defer upstream.Close()

	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{
		APIKey:  "secret",
		Timeout: time.Second,
	})
	submit := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 32, 18), 64, 36)
	submitRecorder := httptest.NewRecorder()
	handler.HandleUpscale(submitRecorder, submit)
	if submitRecorder.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitRecorder.Code, submitRecorder.Body.String())
	}

	poll := httptest.NewRequest(http.MethodGet, "/v1/images/upscale/tasks/retry-download-task", nil)
	pollRecorder := httptest.NewRecorder()
	handler.HandleUpscaleTask(pollRecorder, poll)
	if pollRecorder.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", pollRecorder.Code, pollRecorder.Body.String())
	}
	if submitCalls != 1 || downloadCalls != 2 {
		t.Fatalf("unexpected provider calls: submit=%d download=%d", submitCalls, downloadCalls)
	}
}

func TestImageUpscaleProxyRejectsOversizedRequestWith413(t *testing.T) {
	upstream, capture := newTopazFakeServer(t, imageUpscaleTestJPEG(t, 64, 36))
	defer upstream.Close()
	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{
		APIKey:          "secret",
		MaxRequestBytes: 128,
	})

	request := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 32, 18), 64, 36)
	recorder := httptest.NewRecorder()
	handler.HandleUpscale(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.submitCalls != 0 {
		t.Fatalf("provider was called %d times", capture.submitCalls)
	}
}

func TestImageUpscaleProxyRejectsTargetSmallerThanSource(t *testing.T) {
	upstream, capture := newTopazFakeServer(t, imageUpscaleTestJPEG(t, 64, 36))
	defer upstream.Close()
	handler := NewImageUpscaleProxyHandler(upstream.URL+"/image/v1/enhance/async", ImageUpscaleProxyOptions{APIKey: "secret"})

	request := imageUpscaleTestRequest(t, "/v1/images/upscale", imageUpscaleTestJPEG(t, 64, 36), 32, 18)
	recorder := httptest.NewRecorder()
	handler.HandleUpscale(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.submitCalls != 0 {
		t.Fatalf("provider was called %d times", capture.submitCalls)
	}
}
