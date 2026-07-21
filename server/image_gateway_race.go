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
	"net/http"
	"net/url"
	"strings"
	"time"
)

type imageAttemptCategory string

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

type imageRaceRoundResult struct {
	winner  *imageAttemptResult
	results []imageAttemptResult
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
	outcome     imageRaceOutcome
	winner      *imageAttemptResult
	winnerRound int
	rounds      int
}

var errAsyncImageResponse = errors.New("asynchronous image response is not a completed image")

func (h *ImageGenerationProxyHandler) eligibleImageProviders(operation imageProviderOperation, excluded map[string]struct{}) []imageUpstreamProvider {
	providers := make([]imageUpstreamProvider, 0, len(h.providers))
	for _, provider := range h.providers {
		if !provider.supports(operation) {
			continue
		}
		if _, skip := excluded[provider.id]; skip {
			continue
		}
		providers = append(providers, provider)
	}
	return providers
}

func (h *ImageGenerationProxyHandler) runImageRaceRound(
	ctx context.Context,
	payload map[string]interface{},
	operation imageProviderOperation,
	providers []imageUpstreamProvider,
	roundNumber int,
	observer func(int, imageAttemptResult),
) imageRaceRoundResult {
	roundContext, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan imageAttemptResult, len(providers))
	for _, provider := range providers {
		provider := provider
		go func() {
			result := h.callImageProvider(roundContext, provider, payload, operation)
			if observer != nil {
				observer(roundNumber, result)
			}
			results <- result
		}()
	}

	round := imageRaceRoundResult{results: make([]imageAttemptResult, 0, len(providers))}
	for range providers {
		select {
		case result := <-results:
			round.results = append(round.results, result)
			if result.category == imageAttemptSuccess {
				winner := result
				round.winner = &winner
				cancel()
				return round
			}
		case <-ctx.Done():
			return round
		}
	}
	return round
}

func (h *ImageGenerationProxyHandler) runImageRace(
	ctx context.Context,
	payload map[string]interface{},
	operation imageProviderOperation,
	observer func(int, imageAttemptResult),
) imageRaceExecution {
	execution := imageRaceExecution{}
	excluded := make(map[string]struct{})
	requestRejected := make(map[string]struct{})
	totalProviders := len(h.eligibleImageProviders(operation, nil))

	for {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				execution.outcome = imageRaceExhausted
			} else {
				execution.outcome = imageRaceCancelled
			}
			return execution
		}

		providers := h.eligibleImageProviders(operation, excluded)
		if len(providers) == 0 {
			if totalProviders > 0 && len(requestRejected) == totalProviders {
				execution.outcome = imageRaceRequestRejected
			} else {
				execution.outcome = imageRaceProvidersUnavailable
			}
			return execution
		}

		execution.rounds++
		round := h.runImageRaceRound(ctx, payload, operation, providers, execution.rounds, observer)
		for _, result := range round.results {
			switch result.category {
			case imageAttemptProviderFatal:
				excluded[result.providerID] = struct{}{}
			case imageAttemptRequestFatal:
				excluded[result.providerID] = struct{}{}
				requestRejected[result.providerID] = struct{}{}
			}
		}
		if round.winner != nil {
			execution.outcome = imageRaceCompleted
			execution.winner = round.winner
			execution.winnerRound = execution.rounds
			return execution
		}
		if len(h.eligibleImageProviders(operation, excluded)) == 0 {
			if totalProviders > 0 && len(requestRejected) == totalProviders {
				execution.outcome = imageRaceRequestRejected
			} else {
				execution.outcome = imageRaceProvidersUnavailable
			}
			return execution
		}
		if ctx.Err() != nil {
			continue
		}
		backoff := imageRaceRoundBackoff(h.retryBackoff, execution.rounds)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
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

	providerPayload := make(map[string]interface{}, len(payload)+1)
	for key, value := range payload {
		providerPayload[key] = value
	}
	providerPayload["model"] = provider.model
	providerPayload["n"] = 1
	delete(providerPayload, "async")

	body, err := json.Marshal(providerPayload)
	if err != nil {
		result.category = imageAttemptRequestFatal
		result.reason = "request_encode_failed"
		result.duration = time.Since(startedAt)
		return result
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint(operation), bytes.NewReader(body))
	if err != nil {
		result.category = imageAttemptProviderFatal
		result.reason = "request_build_failed"
		result.duration = time.Since(startedAt)
		return result
	}
	request.Header.Set("Authorization", "Bearer "+provider.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-image-proxy/2.0")

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
		if err := validateCompletedImageResponse(responseBody, h.maxResponseBytes); err != nil {
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

func validateCompletedImageResponse(body []byte, maxImageBytes int64) error {
	var envelope struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
		Data   []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errors.New("response is not valid JSON")
	}
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
				return nil
			}
		}
		if validateGeneratedImageURL(item.URL) == nil {
			return nil
		}
	}
	if strings.TrimSpace(envelope.TaskID) != "" {
		return errAsyncImageResponse
	}
	return errors.New("response does not contain a completed PNG, JPEG, WebP, or image URL")
}

func validateGeneratedImageURL(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("image URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("image URL is invalid")
	}
	if parsed.Scheme != "https" {
		return errors.New("image URL must use HTTPS")
	}
	return nil
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
