package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var gptImage2SizePattern = regexp.MustCompile(`^(\d{3,4})x(\d{3,4})$`)

var imageGenerationFields = map[string]struct{}{
	"model":              {},
	"prompt":             {},
	"n":                  {},
	"size":               {},
	"quality":            {},
	"background":         {},
	"output_format":      {},
	"output_compression": {},
	"moderation":         {},
	"stream":             {},
	"partial_images":     {},
	"user":               {},
}

func validateImageGenerationPayload(payload map[string]interface{}) error {
	for key := range payload {
		if _, ok := imageGenerationFields[key]; !ok {
			return fmt.Errorf("unsupported image generation field: %s", key)
		}
	}
	if err := validateGPTImage2Model(payload); err != nil {
		return err
	}
	if prompt, ok := payload["prompt"].(string); !ok || strings.TrimSpace(prompt) == "" {
		return errors.New("prompt is required")
	} else if utf8.RuneCountInString(prompt) > 32_000 {
		return errors.New("prompt must not exceed 32000 characters")
	}
	return validateGPTImage2OutputOptions(payload)
}

func validateGPTImage2Model(payload map[string]interface{}) error {
	raw, exists := payload["model"]
	if !exists {
		return nil
	}
	model, ok := raw.(string)
	if !ok || model != defaultImageGenerationModel {
		return fmt.Errorf("model must be %s", defaultImageGenerationModel)
	}
	return nil
}

func validateGPTImage2OutputOptions(payload map[string]interface{}) error {
	if rawN, exists := payload["n"]; exists {
		n, ok := imageEditInteger(rawN)
		if !ok || n < 1 || n > 10 {
			return errors.New("n must be an integer from 1 to 10")
		}
	}
	if rawSize, exists := payload["size"]; exists {
		size, ok := rawSize.(string)
		if !ok || !validGPTImage2Size(size) {
			return errors.New("size must be auto or a valid GPT-Image-2 WIDTHxHEIGHT value")
		}
	}
	if rawQuality, exists := payload["quality"]; exists {
		quality, ok := rawQuality.(string)
		if !ok || !oneOf(quality, "low", "medium", "high", "auto") {
			return errors.New("quality is unsupported")
		}
	}
	if rawFormat, exists := payload["output_format"]; exists {
		format, ok := rawFormat.(string)
		if !ok || !oneOf(format, "png", "jpeg", "jpg", "webp") {
			return errors.New("output_format is unsupported")
		}
	}
	if rawBackground, exists := payload["background"]; exists {
		background, ok := rawBackground.(string)
		if !ok || !oneOf(background, "auto", "transparent", "opaque") {
			return errors.New("background is unsupported")
		}
		if background == "transparent" {
			format, _ := payload["output_format"].(string)
			if format == "jpeg" || format == "jpg" {
				return errors.New("transparent background requires png or webp output")
			}
		}
	}
	if rawCompression, exists := payload["output_compression"]; exists {
		value, ok := imageEditInteger(rawCompression)
		if !ok || value < 0 || value > 100 {
			return errors.New("output_compression must be an integer from 0 to 100")
		}
		format, _ := payload["output_format"].(string)
		if format != "jpeg" && format != "jpg" && format != "webp" {
			return errors.New("output_compression requires jpeg or webp output")
		}
	}
	if rawModeration, exists := payload["moderation"]; exists {
		moderation, ok := rawModeration.(string)
		if !ok || !oneOf(moderation, "auto", "low") {
			return errors.New("moderation is unsupported")
		}
	}
	stream := false
	if rawStream, exists := payload["stream"]; exists {
		value, ok := rawStream.(bool)
		if !ok {
			return errors.New("stream must be a boolean")
		}
		stream = value
	}
	if rawPartial, exists := payload["partial_images"]; exists {
		value, ok := imageEditInteger(rawPartial)
		if !ok || value < 0 || value > 3 {
			return errors.New("partial_images must be an integer from 0 to 3")
		}
		if !stream {
			return errors.New("partial_images requires stream=true")
		}
	}
	if rawUser, exists := payload["user"]; exists {
		if user, ok := rawUser.(string); !ok || strings.TrimSpace(user) == "" {
			return errors.New("user must be a non-empty string")
		}
	}
	return nil
}

func validGPTImage2Size(size string) bool {
	if size == "auto" {
		return true
	}
	match := gptImage2SizePattern.FindStringSubmatch(size)
	if match == nil {
		return false
	}
	width, _ := strconv.Atoi(match[1])
	height, _ := strconv.Atoi(match[2])
	if width%16 != 0 || height%16 != 0 || width > 3840 || height > 3840 {
		return false
	}
	pixels := int64(width) * int64(height)
	if pixels < 655_360 || pixels > 8_294_400 {
		return false
	}
	longEdge := math.Max(float64(width), float64(height))
	shortEdge := math.Min(float64(width), float64(height))
	return longEdge/shortEdge <= 3
}

func imageJSONNumber(value int) json.Number {
	return json.Number(strconv.Itoa(value))
}

func imageRequestErrorStatus(err error) int {
	var requestErr *imageEditRequestError
	if errors.As(err, &requestErr) {
		return requestErr.status
	}
	return http.StatusBadRequest
}
