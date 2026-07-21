package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeImageProviderPool(t *testing.T, document interface{}) string {
	t.Helper()
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image-providers.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadImageUpstreamProvidersFile(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "relay-b-key")
	if err := os.WriteFile(secretPath, []byte("relay-b-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeImageProviderPool(t, map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"id":              "relay-a",
				"generation_url":  "http://127.0.0.1:18081/v1/images/generations",
				"edit_url":        "http://127.0.0.1:18081/v1/images/edits",
				"api_key":         "relay-a-secret",
				"operations":      []string{"generation", "edit"},
				"timeout_seconds": 12,
			},
			{
				"id":             "relay-b",
				"generation_url": "http://127.0.0.1:18082/v1/images/generations",
				"api_key_file":   secretPath,
				"operations":     []string{"generation"},
			},
		},
	})

	providers, err := loadImageUpstreamProvidersFile(path, "company-image-model", 30*time.Second)
	if err != nil {
		t.Fatalf("load provider pool: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers=%d", len(providers))
	}
	if providers[0].id != "relay-a" || providers[0].model != "company-image-model" || providers[0].client.Timeout != 12*time.Second {
		t.Fatalf("unexpected first provider: %#v", providers[0])
	}
	if !providers[0].supports(imageOperationGeneration) || !providers[0].supports(imageOperationEdit) {
		t.Fatalf("first provider capabilities were not loaded")
	}
	if providers[1].apiKey != "relay-b-secret" || providers[1].client.Timeout != 30*time.Second {
		t.Fatalf("second provider secret or timeout was not loaded")
	}
	if providers[1].supports(imageOperationEdit) {
		t.Fatalf("generation-only provider unexpectedly supports edits")
	}
}

func TestLoadImageUpstreamProvidersFileRejectsPartialOrAmbiguousConfig(t *testing.T) {
	tests := []struct {
		name     string
		document map[string]interface{}
		contains string
	}{
		{
			name: "one provider",
			document: map[string]interface{}{"providers": []map[string]interface{}{
				{"id": "relay-a", "generation_url": "https://a.example/v1/images/generations", "api_key": "secret-a", "operations": []string{"generation"}},
			}},
			contains: "2-3 providers",
		},
		{
			name: "duplicate ids",
			document: map[string]interface{}{"providers": []map[string]interface{}{
				{"id": "relay-a", "generation_url": "https://a.example/v1/images/generations", "api_key": "secret-a", "operations": []string{"generation"}},
				{"id": "relay-a", "generation_url": "https://b.example/v1/images/generations", "api_key": "secret-b", "operations": []string{"generation"}},
			}},
			contains: "duplicate id",
		},
		{
			name: "both secret forms",
			document: map[string]interface{}{"providers": []map[string]interface{}{
				{"id": "relay-a", "generation_url": "https://a.example/v1/images/generations", "api_key": "secret-a", "api_key_file": "ignored", "operations": []string{"generation"}},
				{"id": "relay-b", "generation_url": "https://b.example/v1/images/generations", "api_key": "secret-b", "operations": []string{"generation"}},
			}},
			contains: "only one",
		},
		{
			name: "unknown field",
			document: map[string]interface{}{"providers": []map[string]interface{}{
				{"id": "relay-a", "generation_url": "https://a.example/v1/images/generations", "api_key": "secret-a", "operations": []string{"generation"}, "surprise": true},
				{"id": "relay-b", "generation_url": "https://b.example/v1/images/generations", "api_key": "secret-b", "operations": []string{"generation"}},
			}},
			contains: "unknown field",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeImageProviderPool(t, tc.document)
			_, err := loadImageUpstreamProvidersFile(path, "gpt-image-2", time.Minute)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error=%v, want substring %q", err, tc.contains)
			}
			if strings.Contains(err.Error(), "secret-a") || strings.Contains(err.Error(), "secret-b") {
				t.Fatalf("configuration error leaked a secret: %v", err)
			}
		})
	}
}

func TestImageGenerationProxyHandlerFromEnvPrefersProviderPool(t *testing.T) {
	path := writeImageProviderPool(t, map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"id":             "relay-a",
				"generation_url": "http://127.0.0.1:18081/v1/images/generations",
				"edit_url":       "http://127.0.0.1:18081/v1/images/edits",
				"api_key":        "relay-a-secret",
				"operations":     []string{"generation", "edit"},
			},
			{
				"id":             "relay-b",
				"generation_url": "http://127.0.0.1:18082/v1/images/generations",
				"api_key":        "relay-b-secret",
				"operations":     []string{"generation"},
			},
		},
	})

	t.Setenv("CATSCO_IMAGE_UPSTREAMS_FILE", path)
	t.Setenv("CATSCO_IMAGE_UPSTREAM_URL", "not-a-valid-url")
	t.Setenv("CATSCO_IMAGE_UPSTREAM_API_KEY", "ignored-legacy-secret")
	t.Setenv("CATSCO_IMAGE_TIMEOUT_SECONDS", "45")
	t.Setenv("CATSCO_IMAGE_RACE_DEADLINE_SECONDS", "90")
	t.Setenv("CATSCO_IMAGE_RACE_BACKOFF_MS", "250")
	t.Setenv("CATSCO_IMAGE_MAX_RESPONSE_BYTES", "123456")

	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err != nil {
		t.Fatalf("pool configuration failed: %v", err)
	}
	if len(handler.providers) != 2 {
		t.Fatalf("providers=%d", len(handler.providers))
	}
	if handler.raceDeadline != 90*time.Second || handler.retryBackoff != 250*time.Millisecond {
		t.Fatalf("race settings not applied: deadline=%s backoff=%s", handler.raceDeadline, handler.retryBackoff)
	}
	if handler.maxResponseBytes != 123456 {
		t.Fatalf("max response bytes=%d", handler.maxResponseBytes)
	}
	if err := handler.EditConfigError(); err != nil {
		t.Fatalf("edit provider should be available: %v", err)
	}
	if handler.apiKey == "ignored-legacy-secret" {
		t.Fatal("legacy provider configuration was used instead of the pool")
	}
}

func TestProductionImageProviderExampleMatchesLoader(t *testing.T) {
	providers, err := loadImageUpstreamProvidersFile(
		filepath.Join("..", "deploy", "prod", "image-providers.example.json"),
		"gpt-image-2",
		30*time.Second,
	)
	if err != nil {
		t.Fatalf("load production provider example: %v", err)
	}
	if len(providers) != 2 || !providers[0].supports(imageOperationEdit) || providers[1].supports(imageOperationEdit) {
		t.Fatalf("unexpected example provider capabilities")
	}
}

func TestProviderPoolKeepsInvalidEditLimitScopedToEditRoute(t *testing.T) {
	path := writeImageProviderPool(t, map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"id":             "relay-a",
				"generation_url": "http://127.0.0.1:18081/v1/images/generations",
				"edit_url":       "http://127.0.0.1:18081/v1/images/edits",
				"api_key":        "relay-a-secret",
				"operations":     []string{"generation", "edit"},
			},
			{
				"id":             "relay-b",
				"generation_url": "http://127.0.0.1:18082/v1/images/generations",
				"api_key":        "relay-b-secret",
				"operations":     []string{"generation"},
			},
		},
	})
	t.Setenv("CATSCO_IMAGE_UPSTREAMS_FILE", path)
	t.Setenv("CATSCO_IMAGE_EDIT_MAX_REQUEST_BYTES", "invalid")

	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err != nil {
		t.Fatalf("generation config should remain available: %v", err)
	}
	if err := handler.EditConfigError(); err == nil {
		t.Fatal("invalid edit limit should disable the edit route")
	}
}
