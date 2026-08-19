package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDreaminaWorkerTimeout = 150 * time.Second
	dreaminaTaskPathPrefix       = "/v1/tasks/"
)

type dreaminaWorkerClient struct {
	baseURL string
	client  *http.Client
}

func configureDreaminaWorkerFromEnv(handler *ImageGenerationProxyHandler) {
	rawURL := strings.TrimSpace(os.Getenv("CATSCO_DREAMINA_WORKER_URL"))
	if rawURL == "" {
		return
	}
	parsed, err := parseDreaminaWorkerURL(rawURL)
	if err != nil {
		handler.dreaminaConfigError = err
		return
	}
	timeoutSeconds, err := parsePositiveInt64Env(
		"CATSCO_DREAMINA_WORKER_TIMEOUT_SECONDS",
		int64(defaultDreaminaWorkerTimeout/time.Second),
	)
	if err != nil {
		handler.dreaminaConfigError = err
		return
	}
	handler.dreaminaWorker = &dreaminaWorkerClient{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

func parseDreaminaWorkerURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("invalid CATSCO_DREAMINA_WORKER_URL: %q", rawURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("CATSCO_DREAMINA_WORKER_URL must use HTTP or HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("CATSCO_DREAMINA_WORKER_URL must not contain a query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func (h *ImageGenerationProxyHandler) forwardDreaminaFallback(
	w http.ResponseWriter,
	r *http.Request,
	payload map[string]interface{},
	operation imageProviderOperation,
	referenceCount int,
	referenceBytes int64,
	raceID string,
	execution imageRaceExecution,
	startedAt time.Time,
) bool {
	return h.forwardDreaminaRequest(
		w,
		r,
		payload,
		operation,
		referenceCount,
		referenceBytes,
		"fallback",
		raceID,
		execution,
		startedAt,
	)
}

func (h *ImageGenerationProxyHandler) forwardDreaminaRequest(
	w http.ResponseWriter,
	r *http.Request,
	payload map[string]interface{},
	operation imageProviderOperation,
	referenceCount int,
	referenceBytes int64,
	providerRole string,
	raceID string,
	execution imageRaceExecution,
	startedAt time.Time,
) bool {
	if h.dreaminaWorker == nil && h.dreaminaConfigError == nil {
		return false
	}
	if h.dreaminaConfigError != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "dreamina_unavailable",
				"message": "Dreamina fallback is not configured correctly.",
			},
		})
		return true
	}

	body, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image request"})
		return true
	}
	endpoint := h.dreaminaWorker.baseURL + "/v1/images/generations"
	if operation == imageOperationEdit {
		endpoint = h.dreaminaWorker.baseURL + "/v1/images/edits"
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build Dreamina fallback request"})
		return true
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-image-proxy/2.0")
	request.Header.Set("X-CatsCo-Owner-UID", strconv.FormatInt(UIDFromContext(r.Context()), 10))
	request.Header.Set("X-CatsCo-Dreamina-Provider-Role", providerRole)
	if providerRole == "fallback" {
		request.Header.Set("X-CatsCo-Dreamina-Fallback-From", "image2")
		request.Header.Set("X-CatsCo-Dreamina-Fallback-Reason", "image2_race_exhausted")
	}

	response, err := h.dreaminaWorker.client.Do(request)
	if err != nil {
		log.Printf("[image-dreamina] request failed operation=%s role=%s race_id=%s uid=%d error=%v", operation, providerRole, raceID, UIDFromContext(r.Context()), err)
		errorDetails := map[string]interface{}{
			"code":    "dreamina_unavailable",
			"message": "Dreamina image generation is temporarily unavailable.",
		}
		if raceID != "" {
			errorDetails["race_id"] = raceID
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": errorDetails,
		})
		return true
	}
	defer response.Body.Close()

	responseBody, readErr := readLimitedImageResponse(response.Body, h.maxResponseBytes)
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "dreamina_invalid_response",
				"message": "Dreamina fallback returned an unreadable response.",
				"race_id": raceID,
			},
		})
		return true
	}

	maxAttempts := maximumImageProviderAttempts(execution.providerAttempts)
	copyImageGenerationResponseHeaders(w.Header(), response.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-CatsCo-Image-Provider", "dreamina")
	if raceID != "" {
		w.Header().Set("X-CatsCo-Image-Race-Id", raceID)
		w.Header().Set("X-CatsCo-Image-Rounds", fmt.Sprintf("%d", maxAttempts))
		w.Header().Set("X-CatsCo-Image-Total-Attempts", fmt.Sprintf("%d", execution.totalAttempts))
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
	log.Printf("[image-dreamina] completed operation=%s role=%s race_id=%s image2_outcome=%s attempts=%d uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d", operation, providerRole, raceID, execution.outcome, execution.totalAttempts, UIDFromContext(r.Context()), referenceCount, referenceBytes, response.StatusCode, time.Since(startedAt).Milliseconds())
	return true
}

// HandleTask proxies polling for a Dreamina task while preserving CatsCo ownership.
func (h *ImageGenerationProxyHandler) HandleTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h.dreaminaConfigError != nil || h.dreaminaWorker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": map[string]string{
				"code":    "dreamina_unavailable",
				"message": "Dreamina task service is unavailable.",
			},
		})
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, dreaminaTaskPathPrefix)
	if taskID == "" || strings.Contains(taskID, "/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	endpoint := h.dreaminaWorker.baseURL + dreaminaTaskPathPrefix + url.PathEscape(taskID)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build Dreamina task request"})
		return
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-image-proxy/2.0")
	request.Header.Set("X-CatsCo-Owner-UID", strconv.FormatInt(UIDFromContext(r.Context()), 10))

	response, err := h.dreaminaWorker.client.Do(request)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error": map[string]string{
				"code":    "dreamina_unavailable",
				"message": "Dreamina task service is temporarily unavailable.",
			},
		})
		return
	}
	defer response.Body.Close()
	responseBody, readErr := readLimitedImageResponse(response.Body, h.maxResponseBytes)
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": map[string]string{
				"code":    "dreamina_invalid_response",
				"message": "Dreamina task service returned an unreadable response.",
			},
		})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-CatsCo-Image-Provider", "dreamina")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}
