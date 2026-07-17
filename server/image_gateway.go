package server

import (
	"bytes"
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
	defaultImageGenerationModel                 = "gpt-image-2"
	defaultImageGenerationTimeout               = 540 * time.Second
	defaultImageGenerationMaxRequestBytes int64 = 1 << 20  // 1 MiB
	defaultImageEditMaxRequestBytes       int64 = 24 << 20 // 24 MiB, including base64 expansion.
)

// ImageGenerationProxyOptions configures the authenticated image-generation proxy.
type ImageGenerationProxyOptions struct {
	Timeout             time.Duration
	MaxRequestBytes     int64
	MaxEditRequestBytes int64
	Model               string
	APIKey              string
}

// ImageGenerationProxyHandler keeps the provider credential on the CatsCo server.
type ImageGenerationProxyHandler struct {
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
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultImageGenerationTimeout
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

	apiKey, err := readImageGenerationAPIKeyFromEnv()
	if err != nil {
		return &ImageGenerationProxyHandler{configError: err}
	}

	handler := NewImageGenerationProxyHandler(
		os.Getenv("CATSCO_IMAGE_UPSTREAM_URL"),
		ImageGenerationProxyOptions{
			Timeout:             time.Duration(timeoutSeconds) * time.Second,
			MaxRequestBytes:     maxRequestBytes,
			MaxEditRequestBytes: maxEditRequestBytes,
			Model:               os.Getenv("CATSCO_IMAGE_MODEL"),
			APIKey:              apiKey,
		},
	)
	if editLimitErr != nil {
		handler.editConfigError = editLimitErr
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
	if h.configError != nil {
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

	// Server policy owns provider selection and prevents accidental batches.
	payload["model"] = h.model
	payload["n"] = 1
	h.forwardImageRequest(w, r, payload, h.upstreamURL, "generation", 0, 0)
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
	upstreamURL string,
	operation string,
	referenceCount int,
	referenceBytes int64,
) {
	upstreamBody, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image " + operation + " request"})
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build upstream image request"})
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+h.apiKey)
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("User-Agent", "cats-company-image-proxy/1.0")

	requesterUID := UIDFromContext(r.Context())
	startedAt := time.Now()
	resp, err := h.client.Do(upstreamReq)
	if err != nil {
		status := http.StatusBadGateway
		if isImageGenerationTimeout(err) {
			status = http.StatusGatewayTimeout
		}
		log.Printf("[image-proxy] upstream request failed operation=%s uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d error=%v", operation, requesterUID, referenceCount, referenceBytes, status, time.Since(startedAt).Milliseconds(), err)
		writeJSON(w, status, map[string]string{"error": "image " + operation + " upstream request failed"})
		return
	}
	defer resp.Body.Close()

	copyImageGenerationResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("[image-proxy] response copy failed operation=%s uid=%d reference_count=%d reference_bytes=%d status=%d error=%v", operation, requesterUID, referenceCount, referenceBytes, resp.StatusCode, err)
		return
	}
	log.Printf("[image-proxy] request completed operation=%s uid=%d reference_count=%d reference_bytes=%d status=%d duration_ms=%d", operation, requesterUID, referenceCount, referenceBytes, resp.StatusCode, time.Since(startedAt).Milliseconds())
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
