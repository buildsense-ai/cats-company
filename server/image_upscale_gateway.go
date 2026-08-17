package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultImageUpscaleURL                     = "https://api.topazlabs.com/image/v1/enhance/async"
	defaultImageUpscaleRequestTimeout          = 45 * time.Second
	defaultImageUpscaleMaxRequestBytes   int64 = 64 << 20
	defaultImageUpscaleMaxSourceBytes    int64 = 30_000_000
	defaultImageUpscaleMaxResponseBytes  int64 = 128 << 20
	defaultImageUpscaleMaxTargetEdge           = 7680
	defaultImageUpscaleModel                   = "Standard V2"
	defaultImageUpscaleRetryAfterSeconds       = 2
	imageUpscaleMaxSafeGETAttempts             = 3
	imageUpscaleRetryBaseDelay                 = 250 * time.Millisecond
	imageUpscaleMaxRetryDelay                  = 5 * time.Second
	imageUpscaleTaskPathPrefix                 = "/v1/images/upscale/tasks/"
	defaultImageUpscaleTaskOwnerTTL            = 24 * time.Hour
)

var allowedImageUpscaleModels = map[string]struct{}{
	"Standard V2":       {},
	"Low Resolution V2": {},
}

// ImageUpscaleProxyOptions configures the server-side Topaz adapter.
type ImageUpscaleProxyOptions struct {
	APIKey           string
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxSourceBytes   int64
	MaxResponseBytes int64
	MaxTargetEdge    int
	Model            string
}

// ImageUpscaleProxyHandler keeps the provider key on the CatsCo server and
// exposes a small task/status/download facade to clients.
type ImageUpscaleProxyHandler struct {
	submitURL        *url.URL
	statusBaseURL    *url.URL
	downloadBaseURL  *url.URL
	apiKey           string
	model            string
	providerClient   *http.Client
	downloadClient   *http.Client
	maxRequestBytes  int64
	maxSourceBytes   int64
	maxResponseBytes int64
	maxTargetEdge    int
	taskOwnersMu     sync.Mutex
	taskOwners       map[string]imageUpscaleTaskOwner
	taskOwnerTTL     time.Duration
	now              func() time.Time
	configError      error
}

type imageUpscaleTaskOwner struct {
	uid       int64
	expiresAt time.Time
}

type imageUpscaleProviderHTTPError struct {
	status     int
	retryAfter string
}

func (e *imageUpscaleProviderHTTPError) Error() string {
	return fmt.Sprintf("image upscale provider returned HTTP %d", e.status)
}

type imageUpscaleRequestError struct {
	status  int
	message string
}

func (e *imageUpscaleRequestError) Error() string {
	return e.message
}

type topazUpscaleSubmitResponse struct {
	ProcessID string `json:"process_id"`
	ETA       int64  `json:"eta"`
}

type topazUpscaleStatusResponse struct {
	ProcessID    string `json:"process_id"`
	Status       string `json:"status"`
	Progress     int    `json:"progress"`
	ETA          int64  `json:"eta"`
	OutputWidth  int    `json:"output_width"`
	OutputHeight int    `json:"output_height"`
	Model        string `json:"model"`
}

type topazUpscaleDownloadResponse struct {
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
	Data        struct {
		URL         string `json:"url"`
		DownloadURL string `json:"download_url"`
	} `json:"data"`
}

// NewImageUpscaleProxyHandler builds a Topaz async adapter. upstreamURL may
// be the full /enhance/async endpoint or the /image/v1 API base.
func NewImageUpscaleProxyHandler(upstreamURL string, opts ImageUpscaleProxyOptions) *ImageUpscaleProxyHandler {
	handler := &ImageUpscaleProxyHandler{
		apiKey:           strings.TrimSpace(opts.APIKey),
		maxRequestBytes:  opts.MaxRequestBytes,
		maxSourceBytes:   opts.MaxSourceBytes,
		maxResponseBytes: opts.MaxResponseBytes,
		maxTargetEdge:    opts.MaxTargetEdge,
		model:            strings.TrimSpace(opts.Model),
		taskOwners:       make(map[string]imageUpscaleTaskOwner),
		taskOwnerTTL:     defaultImageUpscaleTaskOwnerTTL,
		now:              time.Now,
	}
	if handler.maxRequestBytes <= 0 {
		handler.maxRequestBytes = defaultImageUpscaleMaxRequestBytes
	}
	if handler.maxSourceBytes <= 0 {
		handler.maxSourceBytes = defaultImageUpscaleMaxSourceBytes
	}
	if handler.maxResponseBytes <= 0 {
		handler.maxResponseBytes = defaultImageUpscaleMaxResponseBytes
	}
	if handler.maxTargetEdge <= 0 {
		handler.maxTargetEdge = defaultImageUpscaleMaxTargetEdge
	}
	if handler.maxTargetEdge > defaultImageUpscaleMaxTargetEdge {
		handler.maxTargetEdge = defaultImageUpscaleMaxTargetEdge
	}
	if handler.model == "" {
		handler.model = defaultImageUpscaleModel
	}
	if _, ok := allowedImageUpscaleModels[handler.model]; !ok {
		handler.configError = fmt.Errorf("unsupported CATSCO_IMAGE_UPSCALE_MODEL: %s", handler.model)
		return handler
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultImageUpscaleRequestTimeout
	}
	noRedirect := func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	handler.providerClient = &http.Client{Timeout: timeout, CheckRedirect: noRedirect}
	handler.downloadClient = &http.Client{Timeout: timeout, CheckRedirect: noRedirect}

	endpoints, err := parseImageUpscaleEndpoints(upstreamURL)
	if err != nil {
		handler.configError = err
		return handler
	}
	handler.submitURL = endpoints.submit
	handler.statusBaseURL = endpoints.statusBase
	handler.downloadBaseURL = endpoints.downloadBase
	if handler.apiKey == "" {
		handler.configError = errors.New("CATSCO_IMAGE_UPSCALE_API_KEY is not set")
	}
	return handler
}

type imageUpscaleEndpoints struct {
	submit       *url.URL
	statusBase   *url.URL
	downloadBase *url.URL
}

// NewImageUpscaleProxyHandlerFromEnv loads server-side provider configuration.
func NewImageUpscaleProxyHandlerFromEnv() *ImageUpscaleProxyHandler {
	timeoutSeconds, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_UPSCALE_TIMEOUT_SECONDS",
		int64(defaultImageUpscaleRequestTimeout/time.Second),
	)
	if err != nil {
		return &ImageUpscaleProxyHandler{configError: err}
	}
	maxRequestBytes, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_UPSCALE_MAX_REQUEST_BYTES",
		defaultImageUpscaleMaxRequestBytes,
	)
	if err != nil {
		return &ImageUpscaleProxyHandler{configError: err}
	}
	maxSourceBytes, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_UPSCALE_MAX_SOURCE_BYTES",
		defaultImageUpscaleMaxSourceBytes,
	)
	if err != nil {
		return &ImageUpscaleProxyHandler{configError: err}
	}
	maxResponseBytes, err := parsePositiveInt64Env(
		"CATSCO_IMAGE_UPSCALE_MAX_RESPONSE_BYTES",
		defaultImageUpscaleMaxResponseBytes,
	)
	if err != nil {
		return &ImageUpscaleProxyHandler{configError: err}
	}
	maxTargetEdge := int64(defaultImageUpscaleMaxTargetEdge)
	if raw := strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSCALE_MAX_TARGET_EDGE")); raw != "" {
		maxTargetEdge, err = parsePositiveInt64Env("CATSCO_IMAGE_UPSCALE_MAX_TARGET_EDGE", maxTargetEdge)
		if err != nil {
			return &ImageUpscaleProxyHandler{configError: err}
		}
	}
	if maxTargetEdge > defaultImageUpscaleMaxTargetEdge {
		return &ImageUpscaleProxyHandler{configError: fmt.Errorf(
			"CATSCO_IMAGE_UPSCALE_MAX_TARGET_EDGE must not exceed %d",
			defaultImageUpscaleMaxTargetEdge,
		)}
	}
	apiKey, err := readImageUpscaleAPIKeyFromEnv()
	if err != nil {
		return &ImageUpscaleProxyHandler{configError: err}
	}
	upstreamURL := strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSCALE_URL"))
	if upstreamURL == "" {
		upstreamURL = defaultImageUpscaleURL
	}
	model := strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSCALE_MODEL"))
	if model == "" {
		model = defaultImageUpscaleModel
	}
	return NewImageUpscaleProxyHandler(upstreamURL, ImageUpscaleProxyOptions{
		APIKey:           apiKey,
		Timeout:          time.Duration(timeoutSeconds) * time.Second,
		MaxRequestBytes:  maxRequestBytes,
		MaxSourceBytes:   maxSourceBytes,
		MaxResponseBytes: maxResponseBytes,
		MaxTargetEdge:    int(maxTargetEdge),
		Model:            model,
	})
}

// ConfigError returns the provider configuration error, if any.
func (h *ImageUpscaleProxyHandler) ConfigError() error {
	return h.configError
}

func (h *ImageUpscaleProxyHandler) registerTaskOwner(taskID string, uid int64) {
	now := h.now()
	h.taskOwnersMu.Lock()
	defer h.taskOwnersMu.Unlock()
	h.removeExpiredTaskOwnersLocked(now)
	h.taskOwners[taskID] = imageUpscaleTaskOwner{
		uid:       uid,
		expiresAt: now.Add(h.taskOwnerTTL),
	}
}

func (h *ImageUpscaleProxyHandler) taskOwnedBy(taskID string, uid int64) bool {
	now := h.now()
	h.taskOwnersMu.Lock()
	defer h.taskOwnersMu.Unlock()
	h.removeExpiredTaskOwnersLocked(now)
	owner, ok := h.taskOwners[taskID]
	return ok && owner.uid == uid
}

func (h *ImageUpscaleProxyHandler) removeExpiredTaskOwnersLocked(now time.Time) {
	for taskID, owner := range h.taskOwners {
		if !now.Before(owner.expiresAt) {
			delete(h.taskOwners, taskID)
		}
	}
}

// HandleUpscale handles POST /v1/images/upscale. It submits exactly one
// Topaz job and returns a CatsCo task envelope; the client polls the task route.
func (h *ImageUpscaleProxyHandler) HandleUpscale(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h.configError != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image upscale service unavailable"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}

	contents, mediaType, extension, targetWidth, targetHeight, model, err := h.readUpscaleRequest(w, r)
	if err != nil {
		status := http.StatusBadRequest
		var requestErr *imageUpscaleRequestError
		if errors.As(err, &requestErr) {
			status = requestErr.status
		}
		var providerErr *imageUpscaleProviderHTTPError
		if errors.As(err, &providerErr) {
			status = imageUpscalePublicStatus(providerErr.status)
		}
		writeJSON(w, status, map[string]string{"error": imageUpscalePublicError(status)})
		return
	}

	body, contentType, err := buildTopazUpscaleMultipart(contents, mediaType, extension, targetWidth, targetHeight, model)
	if err != nil {
		log.Printf("[image-upscale] failed stage=encode uid=%d error=%v", UIDFromContext(r.Context()), err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "image upscale request could not be prepared"})
		return
	}

	submission, err := h.submitTopazTask(r.Context(), body, contentType)
	if err != nil {
		h.writeProviderFailureWithContext(w, r, err, "submit")
		return
	}
	h.registerTaskOwner(submission.ProcessID, uid)

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.Itoa(defaultImageUpscaleRetryAfterSeconds))
	w.Header().Set("X-CatsCo-Image-Provider", "topaz")
	w.Header().Set("X-CatsCo-Image-Upscale-Task", submission.ProcessID)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"id":             submission.ProcessID,
		"task_id":        submission.ProcessID,
		"status":         "processing",
		"provider":       "topaz",
		"retry_after_ms": defaultImageUpscaleRetryAfterSeconds * 1000,
		"eta":            submission.ETA,
	})
	log.Printf("[image-upscale] submitted uid=%d task=%s target=%dx%d model=%s source_bytes=%d", uid, submission.ProcessID, targetWidth, targetHeight, model, len(contents))
}

// HandleUpscaleTask handles GET /v1/images/upscale/tasks/{process_id}.
func (h *ImageUpscaleProxyHandler) HandleUpscaleTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h.configError != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image upscale service unavailable"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, imageUpscaleTaskPathPrefix)
	if !validImageUpscaleTaskID(taskID) || strings.Contains(taskID, "/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "image upscale task not found"})
		return
	}
	if !h.taskOwnedBy(taskID, uid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "image upscale task not found"})
		return
	}
	targetWidth, targetHeight, err := optionalImageUpscaleTarget(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	status, err := h.fetchTopazStatus(r.Context(), taskID)
	if err != nil {
		h.writeProviderFailureWithContext(w, r, err, "status")
		return
	}

	switch strings.ToLower(strings.TrimSpace(status.Status)) {
	case "pending", "processing":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", strconv.Itoa(defaultImageUpscaleRetryAfterSeconds))
		w.Header().Set("X-CatsCo-Image-Provider", "topaz")
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"id":             taskID,
			"task_id":        taskID,
			"status":         "processing",
			"provider":       "topaz",
			"progress":       status.Progress,
			"eta":            status.ETA,
			"retry_after_ms": defaultImageUpscaleRetryAfterSeconds * 1000,
		})
		return
	case "failed", "cancelled", "canceled":
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": map[string]string{
				"code":    "topaz_task_failed",
				"message": "Topaz image upscale task failed.",
			},
			"task_id": taskID,
		})
		return
	case "completed":
		// Continue to the download path below.
	default:
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": map[string]string{
				"code":    "topaz_unknown_status",
				"message": "Topaz returned an unknown task status.",
			},
			"task_id": taskID,
		})
		return
	}

	if targetWidth > 0 && (status.OutputWidth != targetWidth || status.OutputHeight != targetHeight) {
		log.Printf("[image-upscale] failed stage=status_size task=%s expected=%dx%d actual=%dx%d", taskID, targetWidth, targetHeight, status.OutputWidth, status.OutputHeight)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "image upscale result has an unexpected size"})
		return
	}

	output, mediaType, width, height, err := h.downloadTopazResult(r.Context(), taskID, targetWidth, targetHeight)
	if err != nil {
		h.writeProviderFailureWithContext(w, r, err, "download")
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(output)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-CatsCo-Image-Provider", "topaz")
	w.Header().Set("X-CatsCo-Image-Upscale-Task", taskID)
	w.Header().Set("X-CatsCo-Image-Upscale-Width", strconv.Itoa(width))
	w.Header().Set("X-CatsCo-Image-Upscale-Height", strconv.Itoa(height))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(output)
}

func (h *ImageUpscaleProxyHandler) readUpscaleRequest(w http.ResponseWriter, r *http.Request) ([]byte, string, string, int, int, string, error) {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" {
		return nil, "", "", 0, 0, "", errors.New("Content-Type must be multipart/form-data")
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			return nil, "", "", 0, 0, "", &imageUpscaleRequestError{status: http.StatusRequestEntityTooLarge, message: "image upscale request body is too large"}
		}
		return nil, "", "", 0, 0, "", errors.New("invalid multipart image upscale request")
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	targetWidth, err := imageUpscaleTargetDimension(r.FormValue("target_width"), "target_width", h.maxTargetEdge)
	if err != nil {
		return nil, "", "", 0, 0, "", err
	}
	targetHeight, err := imageUpscaleTargetDimension(r.FormValue("target_height"), "target_height", h.maxTargetEdge)
	if err != nil {
		return nil, "", "", 0, 0, "", err
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = h.model
	}
	if _, ok := allowedImageUpscaleModels[model]; !ok {
		return nil, "", "", 0, 0, "", fmt.Errorf("unsupported image upscale model: %s", model)
	}

	files := r.MultipartForm.File["image_file"]
	if len(files) == 0 {
		files = r.MultipartForm.File["image"]
	}
	if len(files) != 1 {
		return nil, "", "", 0, 0, "", errors.New("exactly one image_file is required")
	}
	file, err := files[0].Open()
	if err != nil {
		return nil, "", "", 0, 0, "", errors.New("image_file could not be read")
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, h.maxSourceBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, "", "", 0, 0, "", errors.New("image_file could not be read")
	}
	if len(contents) == 0 {
		return nil, "", "", 0, 0, "", errors.New("image_file is empty")
	}
	if int64(len(contents)) > h.maxSourceBytes {
		return nil, "", "", 0, 0, "", &imageUpscaleRequestError{status: http.StatusRequestEntityTooLarge, message: "image_file exceeds the source size limit"}
	}
	inputMediaType, extension := detectImageUpscaleMediaType(contents)
	if inputMediaType == "" {
		return nil, "", "", 0, 0, "", errors.New("image_file must contain PNG or JPEG bytes")
	}
	config, _, decodeErr := image.DecodeConfig(bytes.NewReader(contents))
	if decodeErr != nil || config.Width < 1 || config.Height < 1 {
		return nil, "", "", 0, 0, "", errors.New("image_file is not a readable PNG or JPEG")
	}
	if targetWidth < config.Width || targetHeight < config.Height {
		return nil, "", "", 0, 0, "", fmt.Errorf("target %dx%d is not larger than source %dx%d", targetWidth, targetHeight, config.Width, config.Height)
	}
	return contents, inputMediaType, extension, targetWidth, targetHeight, model, nil
}

func (h *ImageUpscaleProxyHandler) submitTopazTask(ctx context.Context, body []byte, contentType string) (topazUpscaleSubmitResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.submitURL.String(), bytes.NewReader(body))
	if err != nil {
		return topazUpscaleSubmitResponse{}, err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-API-Key", h.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-image-upscaler/1.0")
	response, err := h.providerClient.Do(request)
	if err != nil {
		return topazUpscaleSubmitResponse{}, err
	}
	defer response.Body.Close()
	responseBody, err := readImageUpscaleBody(response.Body, 2<<20)
	if err != nil {
		return topazUpscaleSubmitResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return topazUpscaleSubmitResponse{}, &imageUpscaleProviderHTTPError{
			status:     response.StatusCode,
			retryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
		}
	}
	var payload topazUpscaleSubmitResponse
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return topazUpscaleSubmitResponse{}, fmt.Errorf("invalid Topaz submit response: %w", err)
	}
	if payload.ProcessID == "" {
		payload.ProcessID = strings.TrimSpace(response.Header.Get("X-Process-ID"))
	}
	if !validImageUpscaleTaskID(payload.ProcessID) {
		return topazUpscaleSubmitResponse{}, errors.New("Topaz submit response did not contain a valid process_id")
	}
	return payload, nil
}

func (h *ImageUpscaleProxyHandler) fetchTopazStatus(ctx context.Context, taskID string) (topazUpscaleStatusResponse, error) {
	var lastErr error
	for attempt := 0; attempt < imageUpscaleMaxSafeGETAttempts; attempt++ {
		status, err := h.fetchTopazStatusOnce(ctx, taskID)
		if err == nil {
			return status, nil
		}
		lastErr = err
		if attempt+1 >= imageUpscaleMaxSafeGETAttempts || !shouldRetryImageUpscaleGET(err) {
			break
		}
		if err := waitForImageUpscaleRetry(ctx, err, attempt); err != nil {
			return topazUpscaleStatusResponse{}, err
		}
	}
	return topazUpscaleStatusResponse{}, lastErr
}

func (h *ImageUpscaleProxyHandler) fetchTopazStatusOnce(ctx context.Context, taskID string) (topazUpscaleStatusResponse, error) {
	endpoint := imageUpscaleTaskURL(h.statusBaseURL, taskID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return topazUpscaleStatusResponse{}, err
	}
	request.Header.Set("X-API-Key", h.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-image-upscaler/1.0")
	response, err := h.providerClient.Do(request)
	if err != nil {
		return topazUpscaleStatusResponse{}, err
	}
	defer response.Body.Close()
	body, err := readImageUpscaleBody(response.Body, 2<<20)
	if err != nil {
		return topazUpscaleStatusResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return topazUpscaleStatusResponse{}, &imageUpscaleProviderHTTPError{
			status:     response.StatusCode,
			retryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
		}
	}
	var payload topazUpscaleStatusResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return topazUpscaleStatusResponse{}, fmt.Errorf("invalid Topaz status response: %w", err)
	}
	if responseTaskID := strings.TrimSpace(payload.ProcessID); responseTaskID != "" && responseTaskID != taskID {
		return topazUpscaleStatusResponse{}, fmt.Errorf("Topaz status response contained process_id %q for task %q", responseTaskID, taskID)
	}
	return payload, nil
}

func (h *ImageUpscaleProxyHandler) downloadTopazResult(ctx context.Context, taskID string, targetWidth, targetHeight int) ([]byte, string, int, int, error) {
	var lastErr error
	for attempt := 0; attempt < imageUpscaleMaxSafeGETAttempts; attempt++ {
		output, mediaType, width, height, err := h.downloadTopazResultOnce(ctx, taskID, targetWidth, targetHeight)
		if err == nil {
			return output, mediaType, width, height, nil
		}
		lastErr = err
		if attempt+1 >= imageUpscaleMaxSafeGETAttempts || !shouldRetryImageUpscaleGET(err) {
			break
		}
		if err := waitForImageUpscaleRetry(ctx, err, attempt); err != nil {
			return nil, "", 0, 0, err
		}
	}
	return nil, "", 0, 0, lastErr
}

func (h *ImageUpscaleProxyHandler) downloadTopazResultOnce(ctx context.Context, taskID string, targetWidth, targetHeight int) ([]byte, string, int, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, imageUpscaleTaskURL(h.downloadBaseURL, taskID), nil)
	if err != nil {
		return nil, "", 0, 0, err
	}
	request.Header.Set("X-API-Key", h.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-image-upscaler/1.0")
	response, err := h.providerClient.Do(request)
	if err != nil {
		return nil, "", 0, 0, err
	}
	defer response.Body.Close()
	body, err := readImageUpscaleBody(response.Body, 2<<20)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", 0, 0, &imageUpscaleProviderHTTPError{
			status:     response.StatusCode,
			retryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
		}
	}
	var payload topazUpscaleDownloadResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", 0, 0, fmt.Errorf("invalid Topaz download response: %w", err)
	}
	downloadURL := strings.TrimSpace(payload.DownloadURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(payload.URL)
	}
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(payload.Data.DownloadURL)
	}
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(payload.Data.URL)
	}
	parsedDownloadURL, err := parseImageUpscaleDownloadURL(downloadURL)
	if err != nil {
		return nil, "", 0, 0, err
	}

	imageRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedDownloadURL.String(), nil)
	if err != nil {
		return nil, "", 0, 0, err
	}
	imageRequest.Header.Set("Accept", "image/jpeg, image/png")
	imageResponse, err := h.downloadClient.Do(imageRequest)
	if err != nil {
		return nil, "", 0, 0, err
	}
	defer imageResponse.Body.Close()
	imageBody, err := readImageUpscaleBody(imageResponse.Body, h.maxResponseBytes)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if imageResponse.StatusCode < 200 || imageResponse.StatusCode >= 300 {
		return nil, "", 0, 0, &imageUpscaleProviderHTTPError{
			status:     imageResponse.StatusCode,
			retryAfter: strings.TrimSpace(imageResponse.Header.Get("Retry-After")),
		}
	}
	mediaType, width, height, err := inspectImageUpscaleOutput(imageBody)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if targetWidth > 0 && (width != targetWidth || height != targetHeight) {
		return nil, "", 0, 0, fmt.Errorf("Topaz returned %dx%d; requested %dx%d", width, height, targetWidth, targetHeight)
	}
	return imageBody, mediaType, width, height, nil
}

func shouldRetryImageUpscaleGET(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var providerErr *imageUpscaleProviderHTTPError
	if errors.As(err, &providerErr) {
		return providerErr.status == http.StatusRequestTimeout ||
			providerErr.status == http.StatusConflict ||
			providerErr.status == http.StatusTooEarly ||
			providerErr.status == http.StatusTooManyRequests ||
			providerErr.status >= 500
	}
	if isImageGenerationTimeout(err) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func waitForImageUpscaleRetry(ctx context.Context, err error, attempt int) error {
	delay := imageUpscaleRetryBaseDelay * time.Duration(1<<attempt)
	var providerErr *imageUpscaleProviderHTTPError
	if errors.As(err, &providerErr) {
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(providerErr.retryAfter)); parseErr == nil && seconds >= 0 {
			delay = time.Duration(seconds) * time.Second
		}
	}
	if delay > imageUpscaleMaxRetryDelay {
		delay = imageUpscaleMaxRetryDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func buildTopazUpscaleMultipart(contents []byte, mediaType, extension string, targetWidth, targetHeight int, model string) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="input.%s"`, extension))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(contents); err != nil {
		return nil, "", err
	}
	for name, value := range map[string]string{
		"model":         model,
		"output_width":  strconv.Itoa(targetWidth),
		"output_height": strconv.Itoa(targetHeight),
		"output_format": "jpeg",
		"crop_to_fill":  "false",
	} {
		if err := writer.WriteField(name, value); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func parseImageUpscaleEndpoints(rawURL string) (imageUpscaleEndpoints, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		trimmed = defaultImageUpscaleURL
	}
	parsed, err := parseImageUpscaleURL(trimmed, "CATSCO_IMAGE_UPSCALE_URL")
	if err != nil {
		return imageUpscaleEndpoints{}, err
	}
	base := *parsed
	pathValue := strings.TrimRight(base.Path, "/")
	if strings.HasSuffix(pathValue, "/enhance/async") {
		pathValue = strings.TrimSuffix(pathValue, "/enhance/async")
	} else if strings.HasSuffix(pathValue, "/enhance") {
		pathValue = strings.TrimSuffix(pathValue, "/enhance")
	}
	if pathValue == "" {
		pathValue = "/image/v1"
	}
	base.Path = pathValue
	base.RawPath = ""
	submit := base
	submit.Path = pathValue + "/enhance/async"
	statusBase := base
	statusBase.Path = pathValue + "/status"
	downloadBase := base
	downloadBase.Path = pathValue + "/download"
	return imageUpscaleEndpoints{submit: &submit, statusBase: &statusBase, downloadBase: &downloadBase}, nil
}

func parseImageUpscaleURL(rawURL, name string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s is invalid", name)
	}
	switch parsed.Scheme {
	case "https":
		return parsed, nil
	case "http":
		hostname := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(hostname)
		if hostname == "localhost" || (ip != nil && ip.IsLoopback()) {
			return parsed, nil
		}
	}
	return nil, fmt.Errorf("%s must use HTTPS outside localhost", name)
}

func parseImageUpscaleDownloadURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("Topaz download response contained an invalid URL")
	}
	switch parsed.Scheme {
	case "https":
		return parsed, nil
	case "http":
		hostname := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(hostname)
		if hostname == "localhost" || (ip != nil && ip.IsLoopback()) {
			return parsed, nil
		}
	}
	return nil, errors.New("Topaz download URL must use HTTPS outside localhost")
}

func imageUpscaleTaskURL(base *url.URL, taskID string) string {
	resolved := *base
	resolved.Path = strings.TrimRight(base.Path, "/") + "/" + url.PathEscape(taskID)
	resolved.RawPath = ""
	return resolved.String()
}

func validImageUpscaleTaskID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\?#") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func imageUpscaleTargetDimension(value, name string, max int) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 || parsed > max {
		return 0, fmt.Errorf("%s must be an integer from 1 to %d", name, max)
	}
	return parsed, nil
}

func optionalImageUpscaleTarget(values url.Values) (int, int, error) {
	widthValue := strings.TrimSpace(values.Get("target_width"))
	heightValue := strings.TrimSpace(values.Get("target_height"))
	if widthValue == "" && heightValue == "" {
		return 0, 0, nil
	}
	if widthValue == "" || heightValue == "" {
		return 0, 0, errors.New("target_width and target_height must be provided together")
	}
	width, err := imageUpscaleTargetDimension(widthValue, "target_width", defaultImageUpscaleMaxTargetEdge)
	if err != nil {
		return 0, 0, err
	}
	height, err := imageUpscaleTargetDimension(heightValue, "target_height", defaultImageUpscaleMaxTargetEdge)
	if err != nil {
		return 0, 0, err
	}
	return width, height, nil
}

func readImageUpscaleBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("image upscale response is too large")
	}
	return body, nil
}

func inspectImageUpscaleOutput(contents []byte) (string, int, int, error) {
	mediaType, _ := detectImageUpscaleMediaType(contents)
	if mediaType == "" {
		return "", 0, 0, errors.New("Topaz returned an unsupported image format")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(contents))
	if err != nil || config.Width < 1 || config.Height < 1 {
		return "", 0, 0, errors.New("Topaz returned an unreadable image")
	}
	return mediaType, config.Width, config.Height, nil
}

func detectImageUpscaleMediaType(contents []byte) (string, string) {
	for _, candidate := range []struct {
		mediaType string
		extension string
	}{
		{mediaType: "image/png", extension: "png"},
		{mediaType: "image/jpeg", extension: "jpg"},
	} {
		if imageBytesMatchMediaType(contents, candidate.mediaType) {
			return candidate.mediaType, candidate.extension
		}
	}
	return "", ""
}

func imageUpscalePublicStatus(providerStatus int) int {
	switch providerStatus {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return http.StatusBadRequest
	case http.StatusRequestEntityTooLarge:
		return http.StatusRequestEntityTooLarge
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case http.StatusNotFound:
		return http.StatusNotFound
	default:
		return http.StatusBadGateway
	}
}

func imageUpscalePublicError(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "image upscale request was rejected"
	case http.StatusRequestEntityTooLarge:
		return "image upscale request is too large"
	case http.StatusTooManyRequests:
		return "image upscale service is busy"
	case http.StatusNotFound:
		return "image upscale task was not found"
	case http.StatusAccepted:
		return "image upscale task is still processing"
	default:
		return "image upscale service unavailable"
	}
}

func (h *ImageUpscaleProxyHandler) writeProviderFailureWithContext(w http.ResponseWriter, r *http.Request, err error, stage string) {
	status := http.StatusBadGateway
	message := "image upscale service unavailable"
	code := "image_upscale_provider_failed"
	var providerErr *imageUpscaleProviderHTTPError
	if errors.As(err, &providerErr) {
		status = imageUpscalePublicStatus(providerErr.status)
		if stage == "submit" {
			if providerErr.status == http.StatusNotFound || providerErr.status == http.StatusConflict {
				status = http.StatusBadGateway
			}
			code = "image_upscale_submission_failed"
			message = "image upscale task could not be submitted"
		} else if stage == "download" {
			if status == http.StatusNotFound {
				status = http.StatusBadGateway
			}
			code = "image_upscale_download_failed"
			message = "image upscale result could not be downloaded"
		} else if providerErr.status == http.StatusNotFound {
			code = "image_upscale_task_not_found"
			message = "image upscale task was not found"
		} else {
			message = imageUpscalePublicError(status)
		}
		if providerErr.retryAfter != "" {
			w.Header().Set("Retry-After", providerErr.retryAfter)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || isImageGenerationTimeout(err) {
		status = http.StatusGatewayTimeout
		if stage == "submit" {
			code = "image_upscale_submission_unknown"
			message = "image upscale submission outcome is unknown; the task was not automatically resubmitted"
		} else if stage == "status" {
			code = "image_upscale_status_unknown"
			message = "image upscale task status is temporarily unknown; the task was not resubmitted"
		} else {
			code = "image_upscale_download_failed"
			message = "image upscale result download timed out"
		}
	}
	log.Printf("[image-upscale] failed stage=%s uid=%d status=%d code=%s error=%v", stage, UIDFromContext(r.Context()), status, code, err)
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func readImageUpscaleAPIKeyFromEnv() (string, error) {
	secretFile := strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSCALE_API_KEY_FILE"))
	if secretFile != "" {
		contents, err := os.ReadFile(filepath.Clean(secretFile))
		if err != nil {
			return "", fmt.Errorf("failed to read CATSCO_IMAGE_UPSCALE_API_KEY_FILE: %w", err)
		}
		return strings.TrimSpace(string(contents)), nil
	}
	return strings.TrimSpace(os.Getenv("CATSCO_IMAGE_UPSCALE_API_KEY")), nil
}
