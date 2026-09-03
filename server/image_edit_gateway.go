package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultImageEditMaxReferences           = 16
	defaultImageEditMaxReferenceBytes int64 = 50 << 20  // Official per-image upper bound.
	defaultImageEditMaxTotalBytes     int64 = 800 << 20 // 16 images at the official per-image limit.
	defaultImageEditMaxPromptRunes          = 32_000
)

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
	providerPolicy, err := requestedImageProviderPolicy(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.EditConfigError(); err != nil && providerPolicy != "dreamina" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Content-Type"})
		return
	}

	var payload map[string]interface{}
	var referenceCount int
	var referenceBytes int64
	var status int
	switch mediaType {
	case "application/json":
		payload, status, err = readImageJSONPayload(w, r, h.maxEditRequestBytes, "image edit")
		if err == nil {
			referenceCount, referenceBytes, err = validateImageEditPayload(payload, defaultImageEditReferenceLimits)
		}
	case "multipart/form-data":
		payload, referenceCount, referenceBytes, err = readOfficialMultipartImageEditPayload(w, r, h.maxEditRequestBytes, defaultImageEditReferenceLimits)
	default:
		err = &imageEditRequestError{status: http.StatusBadRequest, message: "Content-Type must be multipart/form-data or application/json"}
	}
	if err != nil {
		if status == 0 {
			status = imageRequestErrorStatus(err)
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

func readOfficialMultipartImageEditPayload(
	w http.ResponseWriter,
	r *http.Request,
	maxRequestBytes int64,
	limits imageEditReferenceLimits,
) (map[string]interface{}, int, int64, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, 0, 0, &imageEditRequestError{status: http.StatusRequestEntityTooLarge, message: "image edit request body is too large"}
		}
		return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "invalid multipart image edit request"}
	}
	if r.MultipartForm == nil {
		return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "invalid multipart image edit request"}
	}
	defer r.MultipartForm.RemoveAll()

	payload := make(map[string]interface{})
	for field, values := range r.MultipartForm.Value {
		if len(values) != 1 {
			return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "multipart field must occur once: " + field}
		}
		value := values[0]
		switch field {
		case "model", "prompt", "size", "quality", "background", "output_format", "moderation", "input_fidelity", "user":
			payload[field] = value
		case "n", "output_compression", "partial_images":
			if _, err := strconv.Atoi(value); err != nil {
				return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: field + " must be an integer"}
			}
			payload[field] = json.Number(value)
		case "stream":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "stream must be a boolean"}
			}
			payload[field] = parsed
		default:
			return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "unsupported multipart image edit field: " + field}
		}
	}

	for field := range r.MultipartForm.File {
		if field != "image" && field != "image[]" && field != "mask" {
			return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "unsupported multipart image edit file field: " + field}
		}
	}
	imageHeaders := append(r.MultipartForm.File["image"], r.MultipartForm.File["image[]"]...)
	if len(imageHeaders) < 1 || len(imageHeaders) > limits.maxImages {
		return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: fmt.Sprintf("image must contain 1-%d files", limits.maxImages)}
	}
	images := make([]interface{}, 0, len(imageHeaders))
	var totalBytes int64
	for index, header := range imageHeaders {
		dataURL, decodedBytes, err := multipartImageDataURL(header, limits.maxImageBytes, false)
		if err != nil {
			return nil, 0, 0, &imageEditRequestError{status: imageRequestErrorStatus(err), message: fmt.Sprintf("image %d: %s", index+1, err.Error())}
		}
		totalBytes += decodedBytes
		if totalBytes > limits.maxTotalBytes {
			return nil, 0, 0, &imageEditRequestError{status: http.StatusRequestEntityTooLarge, message: "image files exceed the decoded total size limit"}
		}
		images = append(images, map[string]interface{}{"image_url": dataURL})
	}
	payload["images"] = images

	maskHeaders := r.MultipartForm.File["mask"]
	if len(maskHeaders) > 1 {
		return nil, 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must occur at most once"}
	}
	if len(maskHeaders) == 1 {
		maskURL, decodedBytes, err := multipartImageDataURL(maskHeaders[0], limits.maxImageBytes, true)
		if err != nil {
			return nil, 0, 0, err
		}
		totalBytes += decodedBytes
		if totalBytes > limits.maxTotalBytes {
			return nil, 0, 0, &imageEditRequestError{status: http.StatusRequestEntityTooLarge, message: "image files and mask exceed the decoded total size limit"}
		}
		payload["mask"] = maskURL
	}

	referenceCount, validatedBytes, err := validateImageEditPayload(payload, limits)
	if err != nil {
		return nil, 0, 0, err
	}
	return payload, referenceCount, validatedBytes, nil
}

func multipartImageDataURL(header interface {
	Open() (multipart.File, error)
}, maxBytes int64, mask bool) (string, int64, error) {
	file, err := header.Open()
	if err != nil {
		return "", 0, &imageEditRequestError{status: http.StatusBadRequest, message: "cannot read uploaded image"}
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", 0, &imageEditRequestError{status: http.StatusBadRequest, message: "cannot read uploaded image"}
	}
	if int64(len(contents)) > maxBytes {
		return "", 0, &imageEditRequestError{status: http.StatusRequestEntityTooLarge, message: "uploaded image exceeds the 50 MiB limit"}
	}
	mediaType := http.DetectContentType(contents)
	switch mediaType {
	case "image/png":
	case "image/jpeg":
		if mask {
			return "", 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be PNG with an alpha channel"}
		}
	case "image/webp":
		if mask {
			return "", 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be PNG with an alpha channel"}
		}
	default:
		return "", 0, &imageEditRequestError{status: http.StatusBadRequest, message: "uploaded image must be PNG, JPEG, or WebP"}
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(contents), int64(len(contents)), nil
}

func validateImageEditPayload(
	payload map[string]interface{},
	limits imageEditReferenceLimits,
) (int, int64, error) {
	allowedFields := map[string]struct{}{
		"model":              {},
		"prompt":             {},
		"images":             {},
		"mask":               {},
		"n":                  {},
		"size":               {},
		"quality":            {},
		"output_format":      {},
		"background":         {},
		"output_compression": {},
		"moderation":         {},
		"input_fidelity":     {},
		"stream":             {},
		"partial_images":     {},
		"user":               {},
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
	if err := validateGPTImage2Model(payload); err != nil {
		return 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: err.Error()}
	}

	if err := validateImageEditOutputOptions(payload); err != nil {
		return 0, 0, err
	}

	images, ok := payload["images"].([]interface{})
	if !ok || len(images) < 1 || len(images) > limits.maxImages {
		return 0, 0, &imageEditRequestError{
			status:  http.StatusBadRequest,
			message: fmt.Sprintf("images must contain 1-%d reference images", limits.maxImages),
		}
	}

	var totalBytes int64
	var firstImageURL string
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
		if index == 0 {
			firstImageURL = imageURL
		}
		decodedBytes, _, err := validateImageEditDataURL(imageURL, limits.maxImageBytes)
		if err != nil {
			return 0, 0, err
		}
		totalBytes += decodedBytes
		if totalBytes > limits.maxTotalBytes {
			return 0, 0, &imageEditRequestError{
				status:  http.StatusRequestEntityTooLarge,
				message: "reference images exceed the decoded total size limit",
			}
		}
	}
	if rawMask, exists := payload["mask"]; exists {
		maskURL, ok := rawMask.(string)
		if !ok || strings.TrimSpace(maskURL) == "" {
			return 0, 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be a PNG data URL"}
		}
		maskBytes, err := validateImageEditMask(maskURL, firstImageURL, limits.maxImageBytes)
		if err != nil {
			return 0, 0, err
		}
		totalBytes += maskBytes
		if totalBytes > limits.maxTotalBytes {
			return 0, 0, &imageEditRequestError{status: http.StatusRequestEntityTooLarge, message: "reference images and mask exceed the decoded total size limit"}
		}
	}
	return len(images), totalBytes, nil
}

func validateImageEditMask(maskURL, firstImageURL string, maxDecodedBytes int64) (int64, error) {
	if !strings.HasPrefix(maskURL, "data:image/png;base64,") {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be a base64 PNG data URL"}
	}
	maskBytes, _, err := validateImageEditDataURL(maskURL, maxDecodedBytes)
	if err != nil {
		var requestErr *imageEditRequestError
		if errors.As(err, &requestErr) && requestErr.status == http.StatusRequestEntityTooLarge {
			return 0, &imageEditRequestError{status: requestErr.status, message: "mask exceeds the decoded size limit"}
		}
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be a valid base64 PNG data URL"}
	}
	_, sourceBytes, err := decodeImageEditDataURL(firstImageURL)
	if err != nil {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "the first image must be valid when mask is provided"}
	}
	_, decodedMask, err := decodeImageEditDataURL(maskURL)
	if err != nil {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be a valid base64 PNG data URL"}
	}
	sourceWidth, sourceHeight, err := decodedImageDimensions(sourceBytes)
	if err != nil {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "the first image must be decodable when mask is provided"}
	}
	maskConfig, err := png.DecodeConfig(bytes.NewReader(decodedMask))
	if err != nil {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask must be a decodable PNG"}
	}
	if sourceWidth != maskConfig.Width || sourceHeight != maskConfig.Height {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask dimensions must match the first image"}
	}
	if maskConfig.Width <= 0 || maskConfig.Height <= 0 || maskConfig.Width > 8192 || maskConfig.Height > 8192 || int64(maskConfig.Width)*int64(maskConfig.Height) > 64_000_000 {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask dimensions exceed the supported limit"}
	}
	if len(decodedMask) < 26 || (decodedMask[25] != 4 && decodedMask[25] != 6) {
		return 0, &imageEditRequestError{status: http.StatusBadRequest, message: "mask PNG must contain an alpha channel"}
	}
	return maskBytes, nil
}

func validateImageEditOutputOptions(payload map[string]interface{}) error {
	if err := validateGPTImage2OutputOptions(payload); err != nil {
		return &imageEditRequestError{status: http.StatusBadRequest, message: err.Error()}
	}
	if rawFidelity, exists := payload["input_fidelity"]; exists {
		fidelity, ok := rawFidelity.(string)
		if !ok || !oneOf(fidelity, "high", "low") {
			return &imageEditRequestError{status: http.StatusBadRequest, message: "input_fidelity must be high or low"}
		}
	}
	return nil
}

func decodedImageDimensions(contents []byte) (int, int, error) {
	if len(contents) >= 12 && bytes.Equal(contents[:4], []byte("RIFF")) && bytes.Equal(contents[8:12], []byte("WEBP")) {
		width, height, ok := webPDimensions(contents)
		if !ok {
			return 0, 0, errors.New("invalid WebP dimensions")
		}
		return width, height, nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(contents))
	if err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
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

func decodeImageEditDataURL(value string) (string, []byte, error) {
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return "", nil, errors.New("invalid data URL")
	}
	var mediaType string
	switch value[:comma] {
	case "data:image/png;base64":
		mediaType = "image/png"
	case "data:image/jpeg;base64":
		mediaType = "image/jpeg"
	case "data:image/webp;base64":
		mediaType = "image/webp"
	default:
		return "", nil, errors.New("unsupported data URL media type")
	}
	encoded := value[comma+1:]
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return "", nil, errors.New("invalid base64 image")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || !imageBytesMatchMediaType(decoded, mediaType) {
		return "", nil, errors.New("invalid base64 image")
	}
	return mediaType, decoded, nil
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
