package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultImageEditMaxReferences           = 3
	defaultImageEditMaxReferenceBytes int64 = 8 << 20  // 8 MiB decoded per image.
	defaultImageEditMaxTotalBytes     int64 = 16 << 20 // 16 MiB decoded across references.
	defaultImageEditMaxPromptRunes          = 12_000
)

var imageEditSizePattern = regexp.MustCompile(`^(\d{3,4})x(\d{3,4})$`)

type imageEditReferenceLimits struct {
	maxImages     int
	maxImageBytes int64
	maxTotalBytes int64
}

var defaultImageEditReferenceLimits = imageEditReferenceLimits{
	maxImages:     defaultImageEditMaxReferences,
	maxImageBytes: defaultImageEditMaxReferenceBytes,
	maxTotalBytes: defaultImageEditMaxTotalBytes,
}

type imageEditRequestError struct {
	status  int
	message string
}

func (e *imageEditRequestError) Error() string {
	return e.message
}

// HandleEdit handles POST /v1/images/edits for reference-guided generation.
func (h *ImageGenerationProxyHandler) HandleEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := h.EditConfigError(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Content-Type must be application/json"})
		return
	}

	payload, status, err := readImageJSONPayload(w, r, h.maxEditRequestBytes, "image edit")
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	referenceCount, referenceBytes, err := validateImageEditPayload(payload, defaultImageEditReferenceLimits)
	if err != nil {
		status := http.StatusBadRequest
		var requestErr *imageEditRequestError
		if errors.As(err, &requestErr) {
			status = requestErr.status
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	h.forwardImageRequest(
		w,
		r,
		payload,
		imageOperationEdit,
		referenceCount,
		referenceBytes,
	)
}

func validateImageEditPayload(
	payload map[string]interface{},
	limits imageEditReferenceLimits,
) (int, int64, error) {
	allowedFields := map[string]struct{}{
		"model":              {},
		"prompt":             {},
		"images":             {},
		"n":                  {},
		"size":               {},
		"quality":            {},
		"output_format":      {},
		"background":         {},
		"output_compression": {},
		"moderation":         {},
		"input_fidelity":     {},
		"async":              {},
	}
	for key := range payload {
		if _, ok := allowedFields[key]; !ok {
			return 0, 0, &imageEditRequestError{
				status:  http.StatusBadRequest,
				message: "unsupported image edit field: " + key,
			}
		}
	}

	prompt, ok := payload["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "prompt is required"}
	}
	if utf8.RuneCountInString(prompt) > defaultImageEditMaxPromptRunes {
		return 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "prompt is too long"}
	}

	if err := validateImageEditOutputOptions(payload); err != nil {
		return 0, 0, err
	}

	images, ok := payload["images"].([]interface{})
	if !ok || len(images) < 1 || len(images) > limits.maxImages {
		return 0, 0, &imageEditRequestError{
			status:  http.StatusBadRequest,
			message: "images must contain 1-3 reference images",
		}
	}

	var totalBytes int64
	seenDigests := make(map[[sha256.Size]byte]struct{}, len(images))
	for index, rawImage := range images {
		image, ok := rawImage.(map[string]interface{})
		if !ok || len(image) != 1 {
			return 0, 0, &imageEditRequestError{
				status:  http.StatusBadRequest,
				message: "each reference image must contain only image_url",
			}
		}
		imageURL, ok := image["image_url"].(string)
		if !ok || imageURL == "" {
			return 0, 0, &imageEditRequestError{
				status:  http.StatusBadRequest,
				message: "images[" + strconv.Itoa(index) + "].image_url is required",
			}
		}
		decodedBytes, digest, err := validateImageEditDataURL(imageURL, limits.maxImageBytes)
		if err != nil {
			return 0, 0, err
		}
		if _, duplicate := seenDigests[digest]; duplicate {
			return 0, 0, &imageEditRequestError{
				status:  http.StatusBadRequest,
				message: "reference images must not contain duplicate pixels",
			}
		}
		seenDigests[digest] = struct{}{}
		totalBytes += decodedBytes
		if totalBytes > limits.maxTotalBytes {
			return 0, 0, &imageEditRequestError{
				status:  http.StatusRequestEntityTooLarge,
				message: "reference images exceed the decoded total size limit",
			}
		}
	}
	return len(images), totalBytes, nil
}

func validateImageEditOutputOptions(payload map[string]interface{}) error {
	if rawSize, exists := payload["size"]; exists {
		size, ok := rawSize.(string)
		if !ok {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "size must be a string"}
		}
		match := imageEditSizePattern.FindStringSubmatch(size)
		if match == nil {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "size must look like 1024x1024"}
		}
		width, _ := strconv.Atoi(match[1])
		height, _ := strconv.Atoi(match[2])
		if width < 512 || height < 512 || width > 3840 || height > 3840 || width > 3*height || height > 3*width {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "size is outside the supported range"}
		}
	}
	if rawQuality, exists := payload["quality"]; exists {
		quality, ok := rawQuality.(string)
		if !ok || !oneOf(quality, "low", "medium", "high", "auto") {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "quality is unsupported"}
		}
	}
	if rawFormat, exists := payload["output_format"]; exists {
		format, ok := rawFormat.(string)
		if !ok || !oneOf(format, "png", "jpeg", "jpg", "webp") {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "output_format is unsupported"}
		}
	}
	if rawBackground, exists := payload["background"]; exists {
		background, ok := rawBackground.(string)
		if !ok || !oneOf(background, "auto", "transparent", "opaque") {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "background is unsupported"}
		}
	}
	if rawCompression, exists := payload["output_compression"]; exists {
		value, ok := imageEditInteger(rawCompression)
		if !ok || value < 0 || value > 100 {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "output_compression must be an integer from 0 to 100"}
		}
	}
	if rawModeration, exists := payload["moderation"]; exists {
		moderation, ok := rawModeration.(string)
		if !ok || !oneOf(moderation, "auto", "low") {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "moderation is unsupported"}
		}
	}
	if rawFidelity, exists := payload["input_fidelity"]; exists {
		fidelity, ok := rawFidelity.(string)
		if !ok || !oneOf(fidelity, "low", "high") {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "input_fidelity is unsupported"}
		}
	}
	if rawAsync, exists := payload["async"]; exists {
		async, ok := rawAsync.(bool)
		if !ok {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "async must be a boolean"}
		}
		if async {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "async image edits are not supported"}
		}
	}
	return nil
}

func imageEditInteger(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		return parsed, err == nil
	case float64:
		parsed := int(typed)
		return parsed, typed == float64(parsed)
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func validateImageEditDataURL(value string, maxDecodedBytes int64) (int64, [sha256.Size]byte, error) {
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return 0, [sha256.Size]byte{}, &imageEditRequestError{
			status:  http.StatusBadRequest,
			message: "reference image_url must be a base64 PNG, JPEG, or WebP data URL",
		}
	}
	header := value[:comma]
	encoded := value[comma+1:]
	var mediaType string
	switch header {
	case "data:image/png;base64":
		mediaType = "image/png"
	case "data:image/jpeg;base64":
		mediaType = "image/jpeg"
	case "data:image/webp;base64":
		mediaType = "image/webp"
	default:
		return 0, [sha256.Size]byte{}, &imageEditRequestError{
			status:  http.StatusBadRequest,
			message: "reference image_url must be a base64 PNG, JPEG, or WebP data URL",
		}
	}
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return 0, [sha256.Size]byte{}, &imageEditRequestError{status: http.StatusBadRequest, message: "reference image base64 is invalid"}
	}
	estimatedBytes := int64(base64.StdEncoding.DecodedLen(len(encoded)))
	if estimatedBytes > maxDecodedBytes+2 {
		return 0, [sha256.Size]byte{}, &imageEditRequestError{
			status:  http.StatusRequestEntityTooLarge,
			message: "a reference image exceeds the decoded size limit",
		}
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return 0, [sha256.Size]byte{}, &imageEditRequestError{status: http.StatusBadRequest, message: "reference image base64 is invalid"}
	}
	if int64(len(decoded)) > maxDecodedBytes {
		return 0, [sha256.Size]byte{}, &imageEditRequestError{
			status:  http.StatusRequestEntityTooLarge,
			message: "a reference image exceeds the decoded size limit",
		}
	}
	if !imageBytesMatchMediaType(decoded, mediaType) {
		return 0, [sha256.Size]byte{}, &imageEditRequestError{
			status:  http.StatusBadRequest,
			message: "reference image bytes do not match the declared media type",
		}
	}
	return int64(len(decoded)), sha256.Sum256(decoded), nil
}

func imageBytesMatchMediaType(decoded []byte, mediaType string) bool {
	switch mediaType {
	case "image/png":
		return len(decoded) >= 24 && bytes.Equal(decoded[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	case "image/jpeg":
		return len(decoded) >= 3 && decoded[0] == 0xff && decoded[1] == 0xd8 && decoded[2] == 0xff
	case "image/webp":
		return len(decoded) >= 12 && string(decoded[:4]) == "RIFF" && string(decoded[8:12]) == "WEBP"
	default:
		return false
	}
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
