package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testPNGDataURL(marker byte) string {
	data := make([]byte, 24)
	copy(data, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	data[len(data)-1] = marker
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func imageEditBody(prompt string, images ...string) string {
	items := make([]map[string]string, 0, len(images))
	for _, image := range images {
		items = append(items, map[string]string{"image_url": image})
	}
	payload := map[string]interface{}{
		"prompt":        prompt,
		"images":        items,
		"model":         "client-selected-model",
		"n":             9,
		"size":          "1024x1024",
		"quality":       "medium",
		"output_format": "png",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func testDecodablePNGDataURL(width, height int, transparent bool) string {
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			alpha := uint8(255)
			if transparent && x >= width/3 && x < 2*width/3 && y >= height/3 && y < 2*height/3 {
				alpha = 0
			}
			canvas.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: alpha})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		panic(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func imageEditBodyWithMask(prompt, imageURL, maskURL string) string {
	payload := map[string]interface{}{
		"prompt": prompt,
		"images": []map[string]string{{"image_url": imageURL}},
		"mask":   maskURL,
		"size":   "1024x1024",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestValidateImageEditPayloadAcceptsMatchingTransparentPNGMask(t *testing.T) {
	payload := map[string]interface{}{
		"prompt": "remove the selected subject",
		"images": []interface{}{map[string]interface{}{"image_url": testDecodablePNGDataURL(24, 16, false)}},
		"mask":   testDecodablePNGDataURL(24, 16, true),
	}
	count, decodedBytes, err := validateImageEditPayload(payload, defaultImageEditReferenceLimits)
	if err != nil {
		t.Fatalf("validate mask payload: %v", err)
	}
	if count != 1 || decodedBytes <= 0 {
		t.Fatalf("count=%d decodedBytes=%d", count, decodedBytes)
	}
}

func TestValidateImageEditPayloadRejectsInvalidMasks(t *testing.T) {
	source := testDecodablePNGDataURL(24, 16, false)
	for _, testCase := range []struct {
		name string
		mask interface{}
	}{
		{name: "non string", mask: 42},
		{name: "mismatched dimensions", mask: testDecodablePNGDataURL(12, 16, true)},
		{name: "missing alpha channel", mask: testDecodablePNGDataURL(24, 16, false)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload := map[string]interface{}{
				"prompt": "remove object",
				"images": []interface{}{map[string]interface{}{"image_url": source}},
				"mask":   testCase.mask,
			}
			if _, _, err := validateImageEditPayload(payload, defaultImageEditReferenceLimits); err == nil {
				t.Fatal("expected mask validation error")
			}
		})
	}
}

func TestImageEditProxyHandlerForwardsReferencesAndForcesPolicy(t *testing.T) {
	var upstreamAuthorization string
	var upstreamPath string
	var upstreamPayload map[string]interface{}
	responseBody := testImageResponse(t, 12)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuthorization = r.Header.Get("Authorization")
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("failed to decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "provider-edit-1")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	handler := NewImageGenerationProxyHandler(
		upstream.URL+"/v1/images/generations",
		ImageGenerationProxyOptions{
			Timeout:             5 * time.Second,
			MaxEditRequestBytes: 1 << 20,
			Model:               "gpt-image-2",
			APIKey:              "provider-secret",
		},
	)
	imageURL := testPNGDataURL(1)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/images/edits",
		strings.NewReader(imageEditBody("preserve the character identity", imageURL)),
	)
	req.Header.Set("Authorization", "ApiKey catsco-bot-key")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rr := httptest.NewRecorder()

	handler.HandleEdit(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/images/edits" {
		t.Fatalf("expected edits upstream path, got %q", upstreamPath)
	}
	if upstreamAuthorization != "Bearer provider-secret" {
		t.Fatalf("client identity leaked or provider auth missing: %q", upstreamAuthorization)
	}
	if upstreamPayload["model"] != "gpt-image-2" || upstreamPayload["n"] != float64(1) {
		t.Fatalf("server image policy was not forced: %#v", upstreamPayload)
	}
	images, ok := upstreamPayload["images"].([]interface{})
	if !ok || len(images) != 1 {
		t.Fatalf("reference images were not preserved: %#v", upstreamPayload["images"])
	}
	image, ok := images[0].(map[string]interface{})
	if !ok || image["image_url"] != imageURL {
		t.Fatalf("reference image_url changed: %#v", images[0])
	}
	if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("X-Request-Id") != "provider-edit-1" {
		t.Fatalf("expected safe response headers, got %#v", rr.Header())
	}
}

func TestImageEditProxyHandlerRejectsInvalidRequests(t *testing.T) {
	jpegBytes := []byte{0xff, 0xd8, 0xff, 0x00}
	mismatchedPNG := "data:image/png;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
	validOne := testPNGDataURL(1)
	validTwo := testPNGDataURL(2)
	validThree := testPNGDataURL(3)
	validFour := testPNGDataURL(4)

	tests := []struct {
		name            string
		method          string
		contentType     string
		body            string
		maxRequestBytes int64
		wantStatus      int
	}{
		{name: "wrong method", method: http.MethodGet, contentType: "application/json", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong content type", method: http.MethodPost, contentType: "text/plain", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "invalid json", method: http.MethodPost, contentType: "application/json", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "missing prompt", method: http.MethodPost, contentType: "application/json", body: `{"images":[{"image_url":"` + validOne + `"}]}`, wantStatus: http.StatusBadRequest},
		{name: "missing images", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test"}`, wantStatus: http.StatusBadRequest},
		{name: "remote image URL", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"https://example.com/reference.png"}]}`, wantStatus: http.StatusBadRequest},
		{name: "unsupported media", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"data:image/gif;base64,R0lGODlh"}]}`, wantStatus: http.StatusBadRequest},
		{name: "invalid base64", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"data:image/png;base64,not-valid***"}]}`, wantStatus: http.StatusBadRequest},
		{name: "declared media mismatch", method: http.MethodPost, contentType: "application/json", body: imageEditBody("test", mismatchedPNG), wantStatus: http.StatusBadRequest},
		{name: "duplicate pixels", method: http.MethodPost, contentType: "application/json", body: imageEditBody("test", validOne, validOne), wantStatus: http.StatusBadRequest},
		{name: "too many references", method: http.MethodPost, contentType: "application/json", body: imageEditBody("test", validOne, validTwo, validThree, validFour), wantStatus: http.StatusBadRequest},
		{name: "unsupported top-level field", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"` + validOne + `"}],"mask":"forbidden"}`, wantStatus: http.StatusBadRequest},
		{name: "unsupported image field", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"` + validOne + `","name":"forbidden"}]}`, wantStatus: http.StatusBadRequest},
		{name: "invalid output size", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"` + validOne + `"}],"size":"9999x1"}`, wantStatus: http.StatusBadRequest},
		{name: "async unsupported", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"test","images":[{"image_url":"` + validOne + `"}],"async":true}`, wantStatus: http.StatusBadRequest},
		{name: "multiple JSON objects", method: http.MethodPost, contentType: "application/json", body: imageEditBody("one", validOne) + ` {}`, wantStatus: http.StatusBadRequest},
		{name: "request body too large", method: http.MethodPost, contentType: "application/json", body: imageEditBody("test", validOne), maxRequestBytes: 64, wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			maxRequestBytes := tc.maxRequestBytes
			if maxRequestBytes == 0 {
				maxRequestBytes = 1 << 20
			}
			handler := NewImageGenerationProxyHandler(
				"http://127.0.0.1:1/v1/images/generations",
				ImageGenerationProxyOptions{
					APIKey:              "provider-secret",
					MaxEditRequestBytes: maxRequestBytes,
				},
			)
			req := httptest.NewRequest(tc.method, "/v1/images/edits", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rr := httptest.NewRecorder()

			handler.HandleEdit(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "provider-secret") || strings.Contains(rr.Body.String(), validOne) {
				t.Fatalf("error response leaked a credential or reference image")
			}
		})
	}
}

func TestValidateImageEditPayloadEnforcesDecodedLimits(t *testing.T) {
	tests := []struct {
		name       string
		images     []string
		limits     imageEditReferenceLimits
		wantStatus int
	}{
		{
			name:       "per image decoded limit",
			images:     []string{testPNGDataURL(1)},
			limits:     imageEditReferenceLimits{maxImages: 3, maxImageBytes: 20, maxTotalBytes: 100},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "combined decoded limit",
			images:     []string{testPNGDataURL(1), testPNGDataURL(2)},
			limits:     imageEditReferenceLimits{maxImages: 3, maxImageBytes: 32, maxTotalBytes: 40},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(imageEditBody("test", tc.images...)), &payload); err != nil {
				t.Fatalf("failed to build test payload: %v", err)
			}
			_, _, err := validateImageEditPayload(payload, tc.limits)
			if err == nil {
				t.Fatalf("expected decoded limit error")
			}
			requestErr, ok := err.(*imageEditRequestError)
			if !ok || requestErr.status != tc.wantStatus {
				t.Fatalf("expected %d, got %#v", tc.wantStatus, err)
			}
		})
	}
}

func TestResolveImageOperationUpstreamURLUsesSiblingRoutes(t *testing.T) {
	configured, err := url.Parse("https://images.example.com/codex/v1/images/generations")
	if err != nil {
		t.Fatal(err)
	}
	editURL, err := resolveImageOperationUpstreamURL(configured, "edits")
	if err != nil || editURL.Path != "/codex/v1/images/edits" {
		t.Fatalf("unexpected edit URL: %v, %v", editURL, err)
	}

	configured, err = url.Parse("https://images.example.com/codex/v1/images/edits")
	if err != nil {
		t.Fatal(err)
	}
	generationURL, err := resolveImageOperationUpstreamURL(configured, "generations")
	if err != nil || generationURL.Path != "/codex/v1/images/generations" {
		t.Fatalf("unexpected generation URL: %v, %v", generationURL, err)
	}
}

func TestImageEditProxyHandlerRequiresOperationEndpoint(t *testing.T) {
	handler := NewImageGenerationProxyHandler(
		"https://images.example.com/custom-endpoint",
		ImageGenerationProxyOptions{APIKey: "provider-secret"},
	)
	if handler.ConfigError() != nil {
		t.Fatalf("generation route should preserve its existing custom endpoint: %v", handler.ConfigError())
	}
	if handler.EditConfigError() == nil {
		t.Fatalf("expected edits route to require a derivable sibling endpoint")
	}
}

func TestImageEditProxyHandlerFromEnvReadsEditLimit(t *testing.T) {
	t.Setenv("CATSCO_IMAGE_UPSTREAM_URL", "https://images.example.com/v1/images/generations")
	t.Setenv("CATSCO_IMAGE_UPSTREAM_API_KEY", "provider-secret")
	t.Setenv("CATSCO_IMAGE_UPSTREAM_API_KEY_FILE", "")
	t.Setenv("CATSCO_IMAGE_EDIT_MAX_REQUEST_BYTES", "123456")

	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.EditConfigError(); err != nil {
		t.Fatalf("unexpected edit configuration error: %v", err)
	}
	if handler.maxEditRequestBytes != 123456 {
		t.Fatalf("edit request limit was not applied: %d", handler.maxEditRequestBytes)
	}
	if !strings.HasSuffix(handler.editUpstreamURL, "/v1/images/edits") {
		t.Fatalf("edit upstream route was not derived: %q", handler.editUpstreamURL)
	}
}
