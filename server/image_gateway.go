package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultImageGenerationModel                  = "gpt-image-2"
	defaultImageGenerationTimeout                = 540 * time.Second
	defaultImageRaceDeadline                     = 270 * time.Second
	maximumImageRaceDeadline                     = 285 * time.Second
	defaultImageRaceBackoff                      = 750 * time.Millisecond
	defaultImageRaceAttemptsPerProvider          = 2
	maximumImageRaceAttemptsPerProvider          = 4
	defaultImageGenerationMaxRequestBytes  int64 = 1 << 20   // 1 MiB
	defaultImageEditMaxRequestBytes        int64 = 840 << 20 // 16 inputs at the upstream 50 MiB per-file limit, plus multipart overhead.
	defaultImageGenerationMaxResponseBytes int64 = 512 << 20 // Supports multi-image, high-resolution base64 responses.
	imageProviderPolicyHeader                    = "X-CatsCo-Image-Provider"
)

// ImageGenerationProxyOptions configures the authenticated image-generation proxy.
type ImageGenerationProxyOptions struct {
	Timeout                time.Duration
	RaceDeadline           time.Duration
	RetryBackoff           time.Duration
	MaxAttemptsPerProvider int
	MaxRequestBytes        int64
	MaxEditRequestBytes    int64
	MaxResponseBytes       int64
	Model                  string
	APIKey                 string
}

// ImageGenerationProxyHandler keeps the provider credential on the CatsCo server.
type ImageGenerationProxyHandler struct {
	providers              []imageUpstreamProvider
	raceDeadline           time.Duration
	retryBackoff           time.Duration
	maxAttemptsPerProvider int
	maxResponseBytes       int64
	raceEnabled            bool
	dreaminaWorker         *dreaminaWorkerClient
	dreaminaConfigError    error

	// These aliases keep the single-provider constructor and rollback path stable.
	upstreamURL         string
	editUpstreamURL     string
	model               string
	apiKey              string
	client              *http.Client
	maxRequestBytes     int64
	maxEditRequestBytes int64
	configError         error
	editConfigError     error
}

// NewImageGenerationProxyHandler builds a proxy for an OpenAI-compatible generations endpoint.
func NewImageGenerationProxyHandler(upstreamURL string, opts ImageGenerationProxyOptions) *ImageGenerationProxyHandler {
	handler := &ImageGenerationProxyHandler{
		model:               strings.TrimSpace(opts.Model),
		apiKey:              strings.TrimSpace(opts.APIKey),
		maxRequestBytes:     opts.MaxRequestBytes,
		maxEditRequestBytes: opts.MaxEditRequestBytes,
		maxResponseBytes:    opts.MaxResponseBytes,
	}

	if handler.model == "" {
		handler.model = defaultImageGenerationModel
	}
	if handler.maxRequestBytes <= 0 {
		handler.maxRequestBytes = defaultImageGenerationMaxRequestBytes
	}
	if handler.maxEditRequestBytes <= 0 {
		handler.maxEditRequestBytes = defaultImageEditMaxRequestBytes
	}
	if handler.maxResponseBytes <= 0 {
		handler.maxResponseBytes = defaultImageGenerationMaxResponseBytes
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultImageGenerationTimeout
	}
	handler.raceDeadline = opts.RaceDeadline
	if handler.raceDeadline <= 0 {
		handler.raceDeadline = timeout
	}
	handler.retryBackoff = opts.RetryBackoff
	if handler.retryBackoff <= 0 {
		handler.retryBackoff = defaultImageRaceBackoff
	}
	handler.maxAttemptsPerProvider = opts.MaxAttemptsPerProvider
	if handler.maxAttemptsPerProvider <= 0 {
		handler.maxAttemptsPerProvider = defaultImageRaceAttemptsPerProvider
	}
	handler.client = &http.Client{Timeout: timeout}

	parsedURL, err := parseImageGenerationUpstreamURL(upstreamURL)
	if err != nil {
		handler.configError = err
		return handler
	}
	if handler.apiKey == "" {
		handler.configError = errors.New("CATSCO_IMAGE_UPSTREAM_API_KEY is not set")
		return handler
	}
	generationURL, err := resolveImageOperationUpstreamURL(parsedURL, "generations")
	if err != nil {
		handler.configError = err
		return handler
	}
	handler.upstreamURL = generationURL.String()
	editURL, err := resolveImageOperationUpstreamURL(parsedURL, "edits")
	if err != nil {
		handler.editConfigError = err
	} else {
		handler.editUpstreamURL = editURL.String()
	}
	operations := map[imageProviderOperation]struct{}{imageOperationGeneration: {}}
	if handler.editUpstreamURL != "" {
		operations[imageOperationEdit] = struct{}{}
	}
	handler.providers = []imageUpstreamProvider{{
		id:            "legacy",
		generationURL: handler.upstreamURL,
		editURL:       handler.editUpstreamURL,
		model:         handler.model,
		apiKey:        handler.apiKey,
		client:        handler.client,
		operations:    operations,
		editTransport: imageEditTransportMultipart,
	}}
	return handler
}

// NewImageGenerationProxyHandlerFromEnv loads server-side provider configuration.
func NewImageGenerationProxyHandlerFromEnv() *ImageGenerationProxyHandler {
	timeoutSeconds, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_TIMEOUT_SECONDS",
		int64(defaultImageGenerationTimeout/time.Second),
	)
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}

	maxRequestBytes, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_MAX_REQUEST_BYTES",
		defaultImageGenerationMaxRequestBytes,
	)
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}
	maxEditRequestBytes, editLimitErr := parsePositiveInt64Env(
		"CATSCO_IMAGE_EDIT_MAX_REQUEST_BYTES",
		defaultImageEditMaxRequestBytes,
	)
	maxResponseBytes, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_MAX_RESPONSE_BYTES",
		defaultImageGenerationMaxResponseBytes,
	)
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}
	raceDeadlineSeconds, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_RACE_DEADLINE_SECONDS",
		int64(defaultImageRaceDeadline/time.Second),
	)
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}
	retryBackoffMS, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_RACE_BACKOFF_MS",
		int64(defaultImageRaceBackoff/time.Millisecond),
	)
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}

	poolFile := strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSTREAMS_FILE"))
	if poolFile != "" {
		if time.Duration(raceDeadlineSeconds)*time.Second > maximumImageRaceDeadline {
			return &ImageGenerationProxyHandler{configError: fmt.Errorf(
				"CATSCO_IMAGE_RACE_DEADLINE_SECONDS must not exceed %d seconds",
				int(maximumImageRaceDeadline/time.Second),
			)}
		}
		maxAttemptsPerProvider, attemptsErr := parsePositiveInt64Env(
			"CATSCO_IMAGE_RACE_MAX_ATTEMPTS_PER_PROVIDER",
			defaultImageRaceAttemptsPerProvider,
		)
		if attemptsErr != nil {
			return &ImageGenerationProxyHandler{configError: attemptsErr}
		}
		if maxAttemptsPerProvider > maximumImageRaceAttemptsPerProvider {
			return &ImageGenerationProxyHandler{configError: fmt.Errorf(
				"CATSCO_IMAGE_RACE_MAX_ATTEMPTS_PER_PROVIDER must not exceed %d",
				maximumImageRaceAttemptsPerProvider,
			)}
		}
		providers, loadErr := loadImageUpstreamProvidersFile(
			poolFile,
			os.Getenv("CATSCO_IMAGE_MODEL"),
			time.Duration(timeoutSeconds)*time.Second,
		)
		if loadErr != nil {
			return &ImageGenerationProxyHandler{configError: loadErr}
		}
		handler := newImageGenerationProxyHandlerWithProviders(providers, ImageGenerationProxyOptions{
			RaceDeadline:           time.Duration(raceDeadlineSeconds) * time.Second,
			RetryBackoff:           time.Duration(retryBackoffMS) * time.Millisecond,
			MaxAttemptsPerProvider: int(maxAttemptsPerProvider),
			MaxRequestBytes:        maxRequestBytes,
			MaxEditRequestBytes:    maxEditRequestBytes,
			MaxResponseBytes:       maxResponseBytes,
		})
		if editLimitErr != nil {
			handler.editConfigError = editLimitErr
		}
		configureDreaminaWorkerFromEnv(handler)
		return handler
	}

	apiKey, err := readImageGenerationAPIKeyFromEnv()
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}

	handler := NewImageGenerationProxyHandler(
		os.Getenv("CATSCO_IMAGE_UPSTREAM_URL"),
		ImageGenerationProxyOptions{
			Timeout:             time.Duration(timeoutSeconds) * time.Second,
			RaceDeadline:        time.Duration(raceDeadlineSeconds) * time.Second,
			RetryBackoff:        time.Duration(retryBackoffMS) * time.Millisecond,
			MaxRequestBytes:     maxRequestBytes,
			MaxEditRequestBytes: maxEditRequestBytes,
			MaxResponseBytes:    maxResponseBytes,
			Model:               os.Getenv("CATSCO_IMAGE_MODEL"),
			APIKey:              apiKey,
		},
	)
	if editLimitErr != nil {
		handler.editConfigError = editLimitErr
	}
	configureDreaminaWorkerFromEnv(handler)
	return handler
}

func newImageGenerationProxyHandlerWithProviders(providers []imageUpstreamProvider, opts ImageGenerationProxyOptions) *ImageGenerationProxyHandler {
	handler := &ImageGenerationProxyHandler{
		providers:              append([]imageUpstreamProvider(nil), providers...),
		raceEnabled:            true,
		raceDeadline:           opts.RaceDeadline,
		retryBackoff:           opts.RetryBackoff,
		maxAttemptsPerProvider: opts.MaxAttemptsPerProvider,
		maxRequestBytes:        opts.MaxRequestBytes,
		maxEditRequestBytes:    opts.MaxEditRequestBytes,
		maxResponseBytes:       opts.MaxResponseBytes,
	}
	if handler.raceDeadline <= 0 {
		handler.raceDeadline = defaultImageRaceDeadline
	}
	if handler.retryBackoff <= 0 {
		handler.retryBackoff = defaultImageRaceBackoff
	}
	if handler.maxAttemptsPerProvider <= 0 {
		handler.maxAttemptsPerProvider = defaultImageRaceAttemptsPerProvider
	}
	if handler.maxRequestBytes <= 0 {
		handler.maxRequestBytes = defaultImageGenerationMaxRequestBytes
	}
	if handler.maxEditRequestBytes <= 0 {
		handler.maxEditRequestBytes = defaultImageEditMaxRequestBytes
	}
	if handler.maxResponseBytes <= 0 {
		handler.maxResponseBytes = defaultImageGenerationMaxResponseBytes
	}

	var generationProviders int
	var editProviders int
	for _, provider := range handler.providers {
		if provider.supports(imageOperationGeneration) {
			generationProviders++
		}
		if provider.supports(imageOperationEdit) {
			editProviders++
		}
	}
	if len(handler.providers) == 0 {
		handler.configError = errors.New("image provider pool is empty")
	} else if generationProviders != len(handler.providers) {
		handler.configError = errors.New("every image race provider must support generation")
	}
	if editProviders != len(handler.providers) {
		handler.editConfigError = errors.New("every image race provider must support edit")
	}

	if len(handler.providers) > 0 {
		first := handler.providers[0]
		handler.upstreamURL = first.generationURL
		handler.editUpstreamURL = first.editURL
		handler.model = first.model
		handler.apiKey = first.apiKey
		handler.client = first.client
	}
	return handler
}

// ConfigError returns the startup or configuration error, if any.
func (h *ImageGenerationProxyHandler) ConfigError() error {
	return h.configError
}

// EditConfigError returns the reference-image route configuration error, if any.
func (h *ImageGenerationProxyHandler) EditConfigError() error {
	if h.configError != nil {
		return h.configError
	}
	return h.editConfigError
}

// HandleGenerate handles POST /v1/images/generations.
func (h *ImageGenerationProxyHandler) HandleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	providerPolicy, err := requestedImageProviderPolicy(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if h.configError != nil && providerPolicy != "dreamina" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": h.configError.Error()})
		return
	}
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Content-Type must be application/json"})
		return
	}

	payload, status, err := readImageJSONPayload(w, r, h.maxRequestBytes, "image generation")
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	prompt, _ := payload["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
		return
	}
	if err := validateImageGenerationPayload(payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.forwardImageRequest(w, r, payload, imageOperationGeneration, 0, 0)
}

func requestedImageProviderPolicy(r *http.Request) (string, error) {
	providerPolicy := strings.ToLower(strings.TrimSpace(r.Header.Get(imageProviderPolicyHeader)))
	if providerPolicy == "" {
		providerPolicy = strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_IMAGE_DEFAULT_PROVIDER")))
		if providerPolicy == "" {
			providerPolicy = "auto"
		}
	}
	if providerPolicy != "auto" && providerPolicy != "image2" && providerPolicy != "dreamina" {
		return "", fmt.Errorf("%s must be auto, image2, or dreamina", imageProviderPolicyHeader)
	}
	return providerPolicy, nil
}

func readImageJSONPayload(w http.ResponseWriter, r *http.Request, maxBytes int64, requestName string) (map[string]interface{}, int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()

	var payload map[string]interface{}
	if err := decoder.Decode(&payload); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("%s request body is too large", requestName)
		}
		return nil, http.StatusBadRequest, errors.New("invalid JSON request body")
	}
	if payload == nil {
		return nil, http.StatusBadRequest, errors.New("invalid JSON request body")
	}

	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, http.StatusBadRequest, errors.New("request body must contain one JSON object")
	}
	return payload, 0, nil
}

func (h *ImageGenerationProxyHandler) forwardImageRequest(
	w http.ResponseWriter,
	r *http.Request,
	payload map[string]interface{},
	operation imageProviderOperation,
	referenceCount int,
	referenceBytes int64,
) {
	providerPolicy, err := requestedImageProviderPolicy(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if requestWantsImageStream(payload) {
		if providerPolicy == "dreamina" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "streaming is only available through the image2 provider"})
			return
		}
		h.forwardStreamingImageRequest(w, r, payload, operation, referenceCount, referenceBytes)
		return
	}
	if providerPolicy == "dreamina" {
		if !h.forwardDreaminaRequest(
			w,
			r,
			payload,
			operation,
			referenceCount,
			referenceBytes,
			"primary",
			"",
			imageRaceExecution{},
			time.Now(),
		) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"error": map[string]string{
					"code":    "dreamina_unavailable",
					"message": "Dreamina image generation is unavailable.",
				},
			})
		}
		return
	}

	if !h.raceEnabled {
		h.forwardSingleImageRequest(w, r, payload, operation, referenceCount, referenceBytes)
		return
	}

	requesterUID := UIDFromContext(r.Context())
	startedAt := time.Now()
	raceID := newImageRaceID()
	providers := h.eligibleImageProviders(operation, nil, payload)
	if len(providers) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image operation has no configured provider"})
		return
	}

	requestContext := r.Context()
	if idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idempotencyKey != "" {
		requestContext = context.WithValue(requestContext, imageIdempotencyContextKey{}, idempotencyKey)
	}
	ctx, cancel := context.WithTimeout(requestContext, h.raceDeadline)
	defer cancel()
	execution := h.runImageRace(ctx, payload, operation, func(attemptNumber int, attempt imageAttemptResult) {
		log.Printf("[image-race] attempt operation=%s race_id=%s attempt=%d provider=%s uid=%d category=%s reason=%s status=%d duration_ms=%d", operation, raceID, attemptNumber, attempt.providerID, requesterUID, attempt.category, attempt.reason, attempt.status, attempt.duration.Milliseconds())
	})
	if execution.outcome == imageRaceCancelled {
		log.Printf("[image-race] cancelled operation=%s race_id=%s attempts=%d uid=%d duration_ms=%d", operation, raceID, execution.totalAttempts, requesterUID, time.Since(startedAt).Milliseconds())
		return
	}
	if execution.outcome != imageRaceCompleted {
		if providerPolicy == "auto" && (execution.outcome == imageRaceExhausted || execution.outcome == imageRaceProvidersUnavailable) {
			if h.forwardDreaminaFallback(
				w,
				r,
				payload,
				operation,
				referenceCount,
				referenceBytes,
				raceID,
				execution,
				startedAt,
			) {
				return
			}
		}
		status := http.StatusServiceUnavailable
		code := string(execution.outcome)
		message := "image providers are unavailable"
		switch execution.outcome {
		case imageRaceExhausted:
			status = http.StatusGatewayTimeout
			message = "no image provider returned a valid completed image before the deadline"
		case imageRaceRequestRejected:
			status = http.StatusBadRequest
			message = "all image providers rejected the request"
		}
		maxAttempts := maximumImageProviderAttempts(execution.providerAttempts)
		log.Printf("[image-race] failed operation=%s race_id=%s outcome=%s attempts=%d provider_attempts=%v uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d", operation, raceID, execution.outcome, execution.totalAttempts, execution.providerAttempts, requesterUID, referenceCount, referenceBytes, status, time.Since(startedAt).Milliseconds())
		w.Header().Set("X-CatsCo-Image-Race-Id", raceID)
		w.Header().Set("X-CatsCo-Image-Rounds", fmt.Sprintf("%d", maxAttempts))
		w.Header().Set("X-CatsCo-Image-Total-Attempts", fmt.Sprintf("%d", execution.totalAttempts))
		writeJSON(w, status, map[string]interface{}{
			"error": map[string]interface{}{
				"code":              code,
				"message":           message,
				"race_id":           raceID,
				"rounds":            maxAttempts,
				"attempts":          execution.totalAttempts,
				"provider_attempts": execution.providerAttempts,
			},
		})
		return
	}

	winner := execution.winner
	copyImageGenerationResponseHeaders(w.Header(), winner.headers)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-CatsCo-Image-Race-Id", raceID)
	w.Header().Set("X-CatsCo-Image-Provider", winner.providerID)
	w.Header().Set("X-CatsCo-Image-Round", fmt.Sprintf("%d", execution.winnerAttempt))
	w.Header().Set("X-CatsCo-Image-Attempt", fmt.Sprintf("%d", execution.winnerAttempt))
	w.Header().Set("X-CatsCo-Image-Total-Attempts", fmt.Sprintf("%d", execution.totalAttempts))
	w.WriteHeader(winner.status)
	_, _ = w.Write(winner.body)
	log.Printf("[image-race] completed operation=%s race_id=%s attempt=%d attempts=%d provider=%s uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d", operation, raceID, execution.winnerAttempt, execution.totalAttempts, winner.providerID, requesterUID, referenceCount, referenceBytes, winner.status, time.Since(startedAt).Milliseconds())
}

func (h *ImageGenerationProxyHandler) forwardSingleImageRequest(
	w http.ResponseWriter,
	r *http.Request,
	payload map[string]interface{},
	operation imageProviderOperation,
	referenceCount int,
	referenceBytes int64,
) {
	if len(h.providers) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image operation has no configured provider"})
		return
	}
	provider := h.providers[0]
	requestContext := r.Context()
	if idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idempotencyKey != "" {
		requestContext = context.WithValue(requestContext, imageIdempotencyContextKey{}, idempotencyKey)
	}
	upstreamRequest, err := buildImageProviderRequest(requestContext, provider, payload, operation)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image request"})
		return
	}

	requesterUID := UIDFromContext(r.Context())
	startedAt := time.Now()
	response, err := provider.client.Do(upstreamRequest)
	if err != nil {
		status := http.StatusBadGateway
		if isImageGenerationTimeout(err) {
			status = http.StatusGatewayTimeout
		}
		log.Printf("[image-proxy] upstream request failed operation=%s uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d error=%v", operation, requesterUID, referenceCount, referenceBytes, status, time.Since(startedAt).Milliseconds(), err)
		writeJSON(w, status, map[string]string{"error": "image " + string(operation) + " upstream request failed"})
		return
	}
	defer response.Body.Close()

	copyImageGenerationResponseHeaders(w.Header(), response.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	if _, err := io.Copy(w, response.Body); err != nil {
		log.Printf("[image-proxy] response copy failed operation=%s uid=%d reference_count=%d reference_bytes=%d status=%d error=%v", operation, requesterUID, referenceCount, referenceBytes, response.StatusCode, err)
		return
	}
	log.Printf("[image-proxy] request completed operation=%s uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d", operation, requesterUID, referenceCount, referenceBytes, response.StatusCode, time.Since(startedAt).Milliseconds())
}

func requestWantsImageStream(payload map[string]interface{}) bool {
	stream, _ := payload["stream"].(bool)
	return stream
}

func (h *ImageGenerationProxyHandler) forwardStreamingImageRequest(
	w http.ResponseWriter,
	r *http.Request,
	payload map[string]interface{},
	operation imageProviderOperation,
	referenceCount int,
	referenceBytes int64,
) {
	providers := h.eligibleImageProviders(operation, nil, payload)
	if len(providers) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "streaming image operation has no configured image2 provider"})
		return
	}
	provider := providers[0]
	upstreamRequest, err := buildImageProviderRequest(r.Context(), provider, payload, operation)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid streaming image request"})
		return
	}
	if idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idempotencyKey != "" {
		upstreamRequest.Header.Set("Idempotency-Key", idempotencyKey)
	}
	startedAt := time.Now()
	response, err := provider.client.Do(upstreamRequest)
	if err != nil {
		status := http.StatusBadGateway
		if isImageGenerationTimeout(err) {
			status = http.StatusGatewayTimeout
		}
		writeJSON(w, status, map[string]string{"error": "streaming image upstream request failed"})
		return
	}
	defer response.Body.Close()
	copyImageGenerationResponseHeaders(w.Header(), response.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-CatsCo-Image-Provider", provider.id)
	w.WriteHeader(response.StatusCode)
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32<<10)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("[image-proxy] streaming response failed operation=%s provider=%s reference_count=%d reference_bytes=%d duration_ms=%d error=%v", operation, provider.id, referenceCount, referenceBytes, time.Since(startedAt).Milliseconds(), readErr)
			}
			return
		}
	}
}

func maximumImageProviderAttempts(attempts map[string]int) int {
	maximum := 0
	for _, count := range attempts {
		if count > maximum {
			maximum = count
		}
	}
	return maximum
}

func parseImageGenerationUpstreamURL(rawURL string) (*url.URL, error) {
	trimmedURL := strings.TrimSpace(rawURL)
	if trimmedURL == "" {
		return nil, errors.New("CATSCO_IMAGE_UPSTREAM_URL is not set")
	}
	parsedURL, err := url.Parse(trimmedURL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil {
		return nil, fmt.Errorf("invalid CATSCO_IMAGE_UPSTREAM_URL: %q", trimmedURL)
	}

	switch parsedURL.Scheme {
	case "https":
		return parsedURL, nil
	case "http":
		hostname := strings.ToLower(parsedURL.Hostname())
		ip := net.ParseIP(hostname)
		if hostname == "localhost" || (ip != nil && ip.IsLoopback()) {
			return parsedURL, nil
		}
	}
	return nil, errors.New("CATSCO_IMAGE_UPSTREAM_URL must use HTTPS outside localhost")
}

func resolveImageOperationUpstreamURL(configuredURL *url.URL, operation string) (*url.URL, error) {
	resolved := *configuredURL
	trimmedPath := strings.TrimRight(resolved.Path, "/")
	for _, currentOperation := range []string{"generations", "edits"} {
		suffix := "/images/" + currentOperation
		if strings.HasSuffix(trimmedPath, suffix) {
			resolved.Path = strings.TrimSuffix(trimmedPath, suffix) + "/images/" + operation
			resolved.RawPath = ""
			return &resolved, nil
		}
	}
	if operation == "generations" {
		resolved.Path = trimmedPath
		resolved.RawPath = ""
		return &resolved, nil
	}
	return nil, errors.New("CATSCO_IMAGE_UPSTREAM_URL must end with /images/generations or /images/edits to enable reference images")
}

func readImageGenerationAPIKeyFromEnv() (string, error) {
	secretFile := strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSTREAM_API_KEY_FILE"))
	if secretFile != "" {
		contents, err := os.ReadFile(secretFile)
		if err != nil {
			return "", fmt.Errorf("failed to read CATSCO_IMAGE_UPSTREAM_API_KEY_FILE: %w", err)
		}
		return strings.TrimSpace(string(contents)), nil
	}
	return strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSTREAM_API_KEY")), nil
}

func copyImageGenerationResponseHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Retry-After", "X-Request-Id"} {
		if value := strings.TrimSpace(src.Get(name)); value != "" {
			dst.Set(name, value)
		}
	}
}

func isImageGenerationTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
