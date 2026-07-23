package server

import (
	"encoding/json"
	"fmt"
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

func completeProviderEntry(id, baseURL, apiKey, transport string) map[string]interface{} {
	return map[string]interface{}{
		"id":             id,
		"generation_url": baseURL + "/v1/images/generations",
		"edit_url":       baseURL + "/v1/images/edits",
		"api_key":        apiKey,
		"edit_transport": transport,
	}
}

func completeProviderDocument() map[string]interface{} {
	return map[string]interface{}{
		"providers": []map[string]interface{}{
			completeProviderEntry("code-newcli", "http://127.0.0.1:18081", "secret-a", "json_data_url"),
			completeProviderEntry("pptoken", "http://127.0.0.1:18082", "secret-b", "multipart"),
			completeProviderEntry("codexapis", "http://127.0.0.1:18083", "secret-c", "multipart"),
		},
	}
}

func TestLoadImageUpstreamProvidersFileRequiresThreeCompleteProviders(t *testing.T) {
	document := completeProviderDocument()
	providersDocument := document["providers"].([]map[string]interface{})
	secretPath := filepath.Join(t.TempDir(), "pptoken-key")
	if err := os.WriteFile(secretPath, []byte("pptoken-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(providersDocument[1], "api_key")
	providersDocument[1]["api_key_file"] = secretPath
	providersDocument[0]["timeout_seconds"] = 12

	providers, err := loadImageUpstreamProvidersFile(writeImageProviderPool(t, document), "company-image-model", 30*time.Second)
	if err != nil {
		t.Fatalf("load provider pool: %v", err)
	}
	if len(providers) != 3 {
		t.Fatalf("providers=%d", len(providers))
	}
	if providers[0].id != "code-newcli" || providers[0].model != "company-image-model" || providers[0].client.Timeout != 12*time.Second {
		t.Fatalf("unexpected first provider: %#v", providers[0])
	}
	if providers[0].editTransport != imageEditTransportJSONDataURL {
		t.Fatalf("first edit transport=%q", providers[0].editTransport)
	}
	if providers[1].credentials.primary() != "pptoken-secret" || providers[1].client.Timeout != 30*time.Second {
		t.Fatalf("unexpected second provider: %#v", providers[1])
	}
	if providers[1].editTransport != imageEditTransportMultipart {
		t.Fatalf("second edit transport=%q", providers[1].editTransport)
	}
	if providers[2].id != "codexapis" || providers[2].editTransport != imageEditTransportMultipart {
		t.Fatalf("unexpected third provider: %#v", providers[2])
	}
	for _, provider := range providers {
		if !provider.supports(imageOperationGeneration) || !provider.supports(imageOperationEdit) {
			t.Fatalf("provider %q is not complete", provider.id)
		}
	}
}

func TestLoadImageUpstreamProvidersFileSupportsCredentialFileRotation(t *testing.T) {
	directory := t.TempDir()
	primaryPath := filepath.Join(directory, "pptoken-primary.key")
	fallbackPath := filepath.Join(directory, "pptoken-fallback.key")
	if err := os.WriteFile(primaryPath, []byte("primary-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallbackPath, []byte("fallback-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	document := completeProviderDocument()
	pptoken := document["providers"].([]map[string]interface{})[1]
	delete(pptoken, "api_key")
	pptoken["api_key_files"] = []string{primaryPath, fallbackPath}

	providers, err := loadImageUpstreamProvidersFile(
		writeImageProviderPool(t, document),
		"gpt-image-2",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("load provider pool: %v", err)
	}
	credentials := providers[1].credentials.ordered()
	if len(credentials) != 2 ||
		credentials[0].apiKey != "primary-secret" ||
		credentials[1].apiKey != "fallback-secret" {
		t.Fatalf("unexpected credential pool")
	}
}

func TestReadImageProviderAPIKeysRejectsInvalidRotationConfig(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.key")
	duplicatePath := filepath.Join(directory, "duplicate.key")
	if err := os.WriteFile(firstPath, []byte("same-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicatePath, []byte("same-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		entry    imageProviderFileEntry
		contains string
	}{
		{
			name: "mixed single and multiple files",
			entry: imageProviderFileEntry{
				APIKeyFile:  firstPath,
				APIKeyFiles: []string{duplicatePath},
			},
			contains: "only one",
		},
		{
			name:     "empty file list",
			entry:    imageProviderFileEntry{APIKeyFiles: []string{}},
			contains: "at least one",
		},
		{
			name: "too many files",
			entry: imageProviderFileEntry{APIKeyFiles: []string{
				firstPath,
				firstPath,
				firstPath,
				firstPath,
				firstPath,
			}},
			contains: "not contain more than 4",
		},
		{
			name: "empty path",
			entry: imageProviderFileEntry{
				APIKeyFiles: []string{firstPath, " "},
			},
			contains: "api_key_files[1] is empty",
		},
		{
			name: "duplicate credential contents",
			entry: imageProviderFileEntry{
				APIKeyFiles: []string{firstPath, duplicatePath},
			},
			contains: "must be unique",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readImageProviderAPIKeys(tc.entry)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error=%v, want substring %q", err, tc.contains)
			}
			if strings.Contains(err.Error(), "same-secret") {
				t.Fatalf("configuration error leaked a secret: %v", err)
			}
		})
	}
}

func TestLoadImageUpstreamProvidersFileRejectsIncompleteOrAmbiguousConfig(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(map[string]interface{})
		contains string
	}{
		{
			name: "one provider",
			mutate: func(document map[string]interface{}) {
				document["providers"] = document["providers"].([]map[string]interface{})[:1]
			},
			contains: "exactly 3 providers",
		},
		{
			name: "two providers",
			mutate: func(document map[string]interface{}) {
				document["providers"] = document["providers"].([]map[string]interface{})[:2]
			},
			contains: "exactly 3 providers",
		},
		{
			name: "four providers",
			mutate: func(document map[string]interface{}) {
				providers := document["providers"].([]map[string]interface{})
				document["providers"] = append(
					providers,
					completeProviderEntry("fourth", "http://127.0.0.1:18084", "secret-d", "multipart"),
				)
			},
			contains: "exactly 3 providers",
		},
		{
			name: "duplicate ids",
			mutate: func(document map[string]interface{}) {
				document["providers"].([]map[string]interface{})[1]["id"] = "code-newcli"
			},
			contains: "duplicate id",
		},
		{
			name: "missing edit endpoint",
			mutate: func(document map[string]interface{}) {
				delete(document["providers"].([]map[string]interface{})[1], "edit_url")
			},
			contains: "edit_url",
		},
		{
			name: "missing edit transport",
			mutate: func(document map[string]interface{}) {
				delete(document["providers"].([]map[string]interface{})[1], "edit_transport")
			},
			contains: "edit_transport",
		},
		{
			name: "both secret forms",
			mutate: func(document map[string]interface{}) {
				document["providers"].([]map[string]interface{})[0]["api_key_file"] = "ignored"
			},
			contains: "only one",
		},
		{
			name: "unknown field",
			mutate: func(document map[string]interface{}) {
				document["providers"].([]map[string]interface{})[0]["surprise"] = true
			},
			contains: "unknown field",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			document := completeProviderDocument()
			tc.mutate(document)
			_, err := loadImageUpstreamProvidersFile(writeImageProviderPool(t, document), "gpt-image-2", time.Minute)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error=%v, want substring %q", err, tc.contains)
			}
			if strings.Contains(err.Error(), "secret-a") || strings.Contains(err.Error(), "secret-b") {
				t.Fatalf("configuration error leaked a secret: %v", err)
			}
		})
	}
}

func TestImageGenerationProxyHandlerFromEnvPrefersCompleteProviderPool(t *testing.T) {
	path := writeImageProviderPool(t, completeProviderDocument())
	t.Setenv("CATSCO_IMAGE_UPSTREAMS_FILE", path)
	t.Setenv("CATSCO_IMAGE_UPSTREAM_URL", "not-a-valid-url")
	t.Setenv("CATSCO_IMAGE_UPSTREAM_API_KEY", "ignored-legacy-secret")
	t.Setenv("CATSCO_IMAGE_TIMEOUT_SECONDS", "260")
	t.Setenv("CATSCO_IMAGE_RACE_DEADLINE_SECONDS", "270")
	t.Setenv("CATSCO_IMAGE_RACE_BACKOFF_MS", "250")
	t.Setenv("CATSCO_IMAGE_RACE_MAX_ATTEMPTS_PER_PROVIDER", "3")
	t.Setenv("CATSCO_IMAGE_MAX_RESPONSE_BYTES", "123456")

	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err != nil {
		t.Fatalf("pool configuration failed: %v", err)
	}
	if !handler.raceEnabled || len(handler.providers) != 3 {
		t.Fatalf("race pool not enabled: providers=%d", len(handler.providers))
	}
	if handler.raceDeadline != 270*time.Second || handler.retryBackoff != 250*time.Millisecond || handler.maxAttemptsPerProvider != 3 {
		t.Fatalf("race settings not applied: deadline=%s backoff=%s max_attempts=%d", handler.raceDeadline, handler.retryBackoff, handler.maxAttemptsPerProvider)
	}
	if handler.maxResponseBytes != 123456 {
		t.Fatalf("max response bytes=%d", handler.maxResponseBytes)
	}
	if err := handler.EditConfigError(); err != nil {
		t.Fatalf("all providers should support edit: %v", err)
	}
	if handler.apiKey == "ignored-legacy-secret" {
		t.Fatal("legacy provider configuration was used instead of the pool")
	}
}

func TestImageGenerationProxyHandlerRejectsRaceDeadlineBeyondCallerBudget(t *testing.T) {
	t.Setenv("CATSCO_IMAGE_UPSTREAMS_FILE", writeImageProviderPool(t, completeProviderDocument()))
	t.Setenv("CATSCO_IMAGE_RACE_DEADLINE_SECONDS", "286")
	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err == nil || !strings.Contains(err.Error(), "must not exceed 285 seconds") {
		t.Fatalf("unexpected config error: %v", err)
	}
}

func TestImageGenerationProxyHandlerRejectsUnboundedProviderAttempts(t *testing.T) {
	t.Setenv("CATSCO_IMAGE_UPSTREAMS_FILE", writeImageProviderPool(t, completeProviderDocument()))
	t.Setenv("CATSCO_IMAGE_RACE_MAX_ATTEMPTS_PER_PROVIDER", "5")
	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err == nil || !strings.Contains(err.Error(), "must not exceed 4") {
		t.Fatalf("unexpected config error: %v", err)
	}
}

func TestProductionImageProviderExampleMatchesLoader(t *testing.T) {
	examplePath := filepath.Join("..", "deploy", "prod", "image-providers.example.json")
	contents, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	var document imageProviderPoolDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	secretDirectory := t.TempDir()
	for index := range document.Providers {
		entry := &document.Providers[index]
		if len(entry.APIKeyFiles) > 0 {
			entry.APIKeyFiles = nil
			for keyIndex := 0; keyIndex < 2; keyIndex++ {
				path := filepath.Join(secretDirectory, fmt.Sprintf("provider-%d-key-%d", index, keyIndex))
				if err := os.WriteFile(path, []byte(fmt.Sprintf("test-secret-%d-%d", index, keyIndex)), 0o600); err != nil {
					t.Fatal(err)
				}
				entry.APIKeyFiles = append(entry.APIKeyFiles, path)
			}
			continue
		}
		path := filepath.Join(secretDirectory, fmt.Sprintf("provider-%d-key", index))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("test-secret-%d", index)), 0o600); err != nil {
			t.Fatal(err)
		}
		entry.APIKeyFile = path
	}
	providers, err := loadImageUpstreamProvidersFile(
		writeImageProviderPool(t, document),
		"gpt-image-2",
		260*time.Second,
	)
	if err != nil {
		t.Fatalf("load production provider example: %v", err)
	}
	if len(providers) != 3 ||
		providers[0].id != "code-newcli" ||
		providers[1].id != "pptoken" ||
		providers[2].id != "codexapis" {
		t.Fatalf("unexpected production provider example")
	}
	if providers[0].editTransport != imageEditTransportJSONDataURL ||
		providers[1].editTransport != imageEditTransportMultipart ||
		providers[2].editTransport != imageEditTransportMultipart {
		t.Fatalf("unexpected production edit transports")
	}
	if len(providers[1].credentials.ordered()) != 2 {
		t.Fatal("production pptoken example did not keep both credentials")
	}
}

func TestProviderPoolKeepsInvalidEditLimitScopedToEditRoute(t *testing.T) {
	t.Setenv("CATSCO_IMAGE_UPSTREAMS_FILE", writeImageProviderPool(t, completeProviderDocument()))
	t.Setenv("CATSCO_IMAGE_EDIT_MAX_REQUEST_BYTES", "invalid")

	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err != nil {
		t.Fatalf("generation config should remain available: %v", err)
	}
	if err := handler.EditConfigError(); err == nil {
		t.Fatal("invalid edit limit should disable the edit route")
	}
}
