package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

type imageAttemptCategory string

type imageIdempotencyContextKey struct{}

const (
	imageAttemptSuccess       imageAttemptCategory = "success"
	imageAttemptTransient     imageAttemptCategory = "transient"
	imageAttemptProviderFatal imageAttemptCategory = "provider_fatal"
	imageAttemptRequestFatal  imageAttemptCategory = "request_fatal"
)

type imageAttemptResult struct {
	providerID string
	status     int
	headers    http.Header
	body       []byte
	category   imageAttemptCategory
	reason     string
	duration   time.Duration
}

type imageRaceOutcome string

const (
	imageRaceCompleted            imageRaceOutcome = "completed"
	imageRaceExhausted            imageRaceOutcome = "race_exhausted"
	imageRaceRequestRejected      imageRaceOutcome = "request_rejected"
	imageRaceProvidersUnavailable imageRaceOutcome = "providers_unavailable"
	imageRaceCancelled            imageRaceOutcome = "cancelled"
)

type imageRaceExecution struct {
	outcome          imageRaceOutcome
	winner           *imageAttemptResult
	winnerAttempt    int
	totalAttempts    int
	providerAttempts map[string]int
}

type imageProviderAttemptEvent struct {
	attempt  int
	result   imageAttemptResult
	started  bool
	terminal bool
}

var errAsyncImageResponse = errors.New("asynchronous image response is not a completed image")

func (h *ImageGenerationProxyHandler) eligibleImageProviders(operation imageProviderOperation, excluded map[string]struct{}, payload map[string]interface{}) []imageUpstreamProvider {
	providers := make([]imageUpstreamProvider, 0, len(h.providers))
	for _, provider := range h.providers {
		if !provider.supports(operation) {
			continue
		}
		if operation == imageOperationEdit && payload != nil {
			if _, hasMask := payload["mask"]; hasMask && provider.editTransport != imageEditTransportMultipart {
				continue
			}
		}
		if _, skip := excluded[provider.id]; skip {
			continue
		}
		providers = append(providers, provider)
	}
	return providers
}

func (h *ImageGenerationProxyHandler) runImageProviderLane(
	ctx context.Context,
	payload map[string]interface{},
	operation imageProviderOperation,
	provider imageUpstreamProvider,
	start <-chan struct{},
	ready *sync.WaitGroup,
	events chan<- imageProviderAttemptEvent,
	observer func(int, imageAttemptResult),
) {
	ready.Done()
	select {
	case <-start:
	case <-ctx.Done():
		return
	}
	for attempt := 1; attempt <= h.maxAttemptsPerProvider; attempt++ {
		if ctx.Err() != nil {
			return
		}
		select {
		case events <- imageProviderAttemptEvent{
			attempt: attempt,
			result:  imageAttemptResult{providerID: provider.id},
			started: true,
		}:
		case <-ctx.Done():
			return
		}

		result := h.callImageProvider(ctx, provider, payload, operation)
		if observer != nil {
			observer(attempt, result)
		}
		terminal := result.category == imageAttemptSuccess ||
			result.category == imageAttemptProviderFatal ||
			result.category == imageAttemptRequestFatal ||
			!shouldRetryImageAttempt(result) ||
			attempt >= h.maxAttemptsPerProvider ||
			ctx.Err() != nil
		select {
		case events <- imageProviderAttemptEvent{attempt: attempt, result: result, terminal: terminal}:
		case <-ctx.Done():
			return
		}
		if terminal {
			return
		}

		timer := time.NewTimer(imageRaceRoundBackoff(h.retryBackoff, attempt))
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func shouldRetryImageAttempt(result imageAttemptResult) bool {
	return result.category == imageAttemptTransient &&
		(result.status == http.StatusTooManyRequests || result.status >= http.StatusInternalServerError)
}

func (h *ImageGenerationProxyHandler) runImageRace(
	ctx context.Context,
	payload map[string]interface{},
	operation imageProviderOperation,
	observer func(int, imageAttemptResult),
) imageRaceExecution {
	providers := h.eligibleImageProviders(operation, nil, payload)
	execution := imageRaceExecution{providerAttempts: make(map[string]int, len(providers))}
	if len(providers) == 0 {
		execution.outcome = imageRaceProvidersUnavailable
		return execution
	}

	raceContext, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan imageProviderAttemptEvent, len(providers)*2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(len(providers))
	for _, provider := range providers {
		provider := provider
		go h.runImageProviderLane(raceContext, payload, operation, provider, start, &ready, events, observer)
	}
	ready.Wait()
	close(start)

	activeProviders := len(providers)
	requestRejected := 0
	for activeProviders > 0 {
		select {
		case event := <-events:
			if event.started {
				execution.totalAttempts++
				execution.providerAttempts[event.result.providerID] = event.attempt
				continue
			}
			if event.result.category == imageAttemptSuccess {
				winner := event.result
				execution.outcome = imageRaceCompleted
				execution.winner = &winner
				execution.winnerAttempt = event.attempt
				cancel()
				return execution
			}
			if event.terminal {
				activeProviders--
				if event.result.category == imageAttemptRequestFatal {
					requestRejected++
				}
			}
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				execution.outcome = imageRaceExhausted
			} else {
				execution.outcome = imageRaceCancelled
			}
			return execution
		}
	}

	if requestRejected == len(providers) {
		execution.outcome = imageRaceRequestRejected
	} else {
		execution.outcome = imageRaceProvidersUnavailable
	}
	return execution
}

func imageRaceRoundBackoff(base time.Duration, completedRounds int) time.Duration {
	if base <= 0 {
		base = defaultImageRaceBackoff
	}
	delay := base
	for round := 1; round < completedRounds && delay < 5*time.Second; round++ {
		delay *= 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
	return delay
}

func (h *ImageGenerationProxyHandler) callImageProvider(
	ctx context.Context,
	provider imageUpstreamProvider,
	payload map[string]interface{},
	operation imageProviderOperation,
) imageAttemptResult {
	startedAt := time.Now()
	result := imageAttemptResult{providerID: provider.id, category: imageAttemptTransient}

	request, err := buildImageProviderRequest(ctx, provider, payload, operation)
	if err != nil {
		result.category = imageAttemptRequestFatal
		result.reason = "request_encode_failed"
		result.duration = time.Since(startedAt)
		return result
	}

	response, err := provider.client.Do(request)
	if err != nil {
		result.reason = "network_error"
		if errors.Is(err, context.Canceled) {
			result.reason = "cancelled"
		} else if isImageGenerationTimeout(err) {
			result.reason = "timeout"
		}
		result.duration = time.Since(startedAt)
		return result
	}
	defer response.Body.Close()

	result.status = response.StatusCode
	result.headers = response.Header.Clone()
	responseBody, readErr := readLimitedImageResponse(response.Body, h.maxResponseBytes)
	if readErr != nil {
		result.reason = "response_too_large_or_unreadable"
		result.duration = time.Since(startedAt)
		return result
	}
	result.body = responseBody

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := validateCompletedImageResponse(responseBody, h.maxResponseBytes, requestedImageCount(payload)); err != nil {
			if errors.Is(err, errAsyncImageResponse) {
				result.category = imageAttemptProviderFatal
				result.reason = "asynchronous_response_unsupported"
				result.duration = time.Since(startedAt)
				return result
			}
			result.reason = "invalid_completed_image"
			result.duration = time.Since(startedAt)
			return result
		}
		result.category = imageAttemptSuccess
		result.reason = "completed_image"
		result.duration = time.Since(startedAt)
		return result
	}

	result.category, result.reason = classifyImageProviderHTTPStatus(response.StatusCode)
	result.duration = time.Since(startedAt)
	return result
}

func buildImageProviderRequest(
	ctx context.Context,
	provider imageUpstreamProvider,
	payload map[string]interface{},
	operation imageProviderOperation,
) (*http.Request, error) {
	providerPayload := make(map[string]interface{}, len(payload)+1)
	for key, value := range payload {
		providerPayload[key] = value
	}
	providerPayload["model"] = provider.model
	delete(providerPayload, "async")

	var body io.Reader
	contentType := "application/json"
	if operation == imageOperationEdit && provider.editTransport == imageEditTransportMultipart {
		encoded, multipartContentType, err := encodeMultipartImageEdit(providerPayload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
		contentType = multipartContentType
	} else {
		encoded, err := json.Marshal(providerPayload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint(operation), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+provider.apiKey)
	request.Header.Set("Content-Type", contentType)
	if requestWantsImageStream(payload) {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("User-Agent", "cats-company-image-proxy/2.0")
	if idempotencyKey, _ := ctx.Value(imageIdempotencyContextKey{}).(string); idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return request, nil
}

func encodeMultipartImageEdit(payload map[string]interface{}) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writeField := func(name, value string) error {
		if value == "" {
			return nil
		}
		return writer.WriteField(name, value)
	}

	for _, name := range []string{
		"model",
		"prompt",
		"size",
		"quality",
		"background",
		"output_format",
		"output_compression",
		"moderation",
		"input_fidelity",
		"partial_images",
		"stream",
		"user",
	} {
		value, exists := payload[name]
		if !exists {
			continue
		}
		fieldValue, err := imageFormFieldString(value)
		if err != nil {
			return nil, "", fmt.Errorf("multipart field %s: %w", name, err)
		}
		if name == "output_format" && fieldValue == "jpg" {
			fieldValue = "jpeg"
		}
		if err := writeField(name, fieldValue); err != nil {
			return nil, "", err
		}
	}
	if rawN, exists := payload["n"]; exists {
		n, err := imageFormFieldString(rawN)
		if err != nil {
			return nil, "", fmt.Errorf("multipart field n: %w", err)
		}
		if err := writeField("n", n); err != nil {
			return nil, "", err
		}
	}

	images, ok := payload["images"].([]interface{})
	if !ok || len(images) == 0 {
		return nil, "", errors.New("multipart image edit requires reference images")
	}
	for index, rawImage := range images {
		imageObject, ok := rawImage.(map[string]interface{})
		if !ok {
			return nil, "", fmt.Errorf("reference image %d is invalid", index+1)
		}
		dataURL, ok := imageObject["image_url"].(string)
		if !ok {
			return nil, "", fmt.Errorf("reference image %d has no image_url", index+1)
		}
		mediaType, contents, extension, err := decodeMultipartReference(dataURL)
		if err != nil {
			return nil, "", fmt.Errorf("reference image %d: %w", index+1, err)
		}
		headers := make(textproto.MIMEHeader)
		headers.Set("Content-Disposition", fmt.Sprintf(
			`form-data; name="image"; filename="reference-%02d.%s"`,
			index+1,
			extension,
		))
		headers.Set("Content-Type", mediaType)
		part, err := writer.CreatePart(headers)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(contents); err != nil {
			return nil, "", err
		}
	}
	if rawMask, exists := payload["mask"]; exists {
		maskURL, ok := rawMask.(string)
		if !ok {
			return nil, "", errors.New("multipart image edit mask is invalid")
		}
		mediaType, contents, extension, err := decodeMultipartReference(maskURL)
		if err != nil || mediaType != "image/png" {
			return nil, "", errors.New("multipart image edit mask must be PNG")
		}
		headers := make(textproto.MIMEHeader)
		headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="mask"; filename="mask.%s"`, extension))
		headers.Set("Content-Type", mediaType)
		part, err := writer.CreatePart(headers)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(contents); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func imageFormFieldString(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case json.Number:
		return typed.String(), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	default:
		return "", fmt.Errorf("unsupported value type %T", value)
	}
}

func decodeMultipartReference(value string) (string, []byte, string, error) {
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return "", nil, "", errors.New("invalid data URL")
	}
	var mediaType string
	var extension string
	switch value[:comma] {
	case "data:image/png;base64":
		mediaType, extension = "image/png", "png"
	case "data:image/jpeg;base64":
		mediaType, extension = "image/jpeg", "jpg"
	case "data:image/webp;base64":
		mediaType, extension = "image/webp", "webp"
	default:
		return "", nil, "", errors.New("unsupported data URL media type")
	}
	contents, err := base64.StdEncoding.Strict().DecodeString(value[comma+1:])
	if err != nil || len(contents) == 0 {
		return "", nil, "", errors.New("invalid base64 image")
	}
	return mediaType, contents, extension, nil
}

func readLimitedImageResponse(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultImageGenerationMaxResponseBytes
	}
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("image response exceeds %d bytes", maxBytes)
	}
	return body, nil
}

func classifyImageProviderHTTPStatus(status int) (imageAttemptCategory, string) {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return imageAttemptProviderFatal, "provider_auth_rejected"
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return imageAttemptProviderFatal, "provider_endpoint_unsupported"
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return imageAttemptTransient, "provider_temporarily_unavailable"
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return imageAttemptRequestFatal, "request_rejected"
	}
	if status >= 500 {
		return imageAttemptTransient, "provider_temporarily_unavailable"
	}
	if status >= 400 {
		return imageAttemptRequestFatal, "request_rejected"
	}
	return imageAttemptTransient, "unexpected_status"
}

func validateCompletedImageResponse(body []byte, maxImageBytes int64, expectedImages int) error {
	var envelope struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
		Data   []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errors.New("response is not valid JSON")
	}
	validImages := 0
	for _, item := range envelope.Data {
		if strings.TrimSpace(item.B64JSON) != "" {
			decodedLimit := maxImageBytes
			if decodedLimit <= 0 {
				decodedLimit = defaultImageGenerationMaxResponseBytes
			}
			if int64(base64.StdEncoding.DecodedLen(len(item.B64JSON))) > decodedLimit {
				continue
			}
			decoded, err := base64.StdEncoding.Strict().DecodeString(item.B64JSON)
			if err == nil && validateGeneratedImageBytes(decoded) == nil {
				validImages++
			}
		}
	}
	if expectedImages < 1 {
		expectedImages = 1
	}
	if validImages >= expectedImages {
		return nil
	}
	if strings.TrimSpace(envelope.TaskID) != "" {
		return errAsyncImageResponse
	}
	return fmt.Errorf("response contains %d valid completed image(s), expected %d", validImages, expectedImages)
}

func requestedImageCount(payload map[string]interface{}) int {
	if raw, exists := payload["n"]; exists {
		if value, ok := imageEditInteger(raw); ok && value >= 1 {
			return value
		}
	}
	return 1
}

func validateGeneratedImageBytes(contents []byte) error {
	if len(contents) < 12 {
		return errors.New("image is too small")
	}
	if bytes.Equal(contents[:4], []byte("RIFF")) && bytes.Equal(contents[8:12], []byte("WEBP")) {
		width, height, ok := webPDimensions(contents)
		if !ok || !reasonableGeneratedImageDimensions(width, height) {
			return errors.New("invalid WebP dimensions")
		}
		return nil
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(contents))
	if err != nil {
		return errors.New("image bytes cannot be decoded")
	}
	if !reasonableGeneratedImageDimensions(config.Width, config.Height) {
		return errors.New("image dimensions are outside the supported range")
	}
	return nil
}

func reasonableGeneratedImageDimensions(width, height int) bool {
	if width <= 0 || height <= 0 || width > 16_384 || height > 16_384 {
		return false
	}
	return int64(width)*int64(height) <= 100_000_000
}

func webPDimensions(contents []byte) (int, int, bool) {
	if len(contents) < 30 {
		return 0, 0, false
	}
	switch string(contents[12:16]) {
	case "VP8X":
		width := 1 + int(contents[24]) + int(contents[25])<<8 + int(contents[26])<<16
		height := 1 + int(contents[27]) + int(contents[28])<<8 + int(contents[29])<<16
		return width, height, true
	case "VP8L":
		if contents[20] != 0x2f {
			return 0, 0, false
		}
		width := 1 + int(contents[21]) + (int(contents[22]&0x3f) << 8)
		height := 1 + (int(contents[22]&0xc0) >> 6) + (int(contents[23]) << 2) + (int(contents[24]&0x0f) << 10)
		return width, height, true
	case "VP8 ":
		if contents[23] != 0x9d || contents[24] != 0x01 || contents[25] != 0x2a {
			return 0, 0, false
		}
		width := int(contents[26]) | int(contents[27]&0x3f)<<8
		height := int(contents[28]) | int(contents[29]&0x3f)<<8
		return width, height, true
	default:
		return 0, 0, false
	}
}

func newImageRaceID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}
