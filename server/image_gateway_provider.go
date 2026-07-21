package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	minImageRaceProviders = 2
	maxImageRaceProviders = 3
)

var imageProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type imageProviderOperation string

const (
	imageOperationGeneration imageProviderOperation = "generation"
	imageOperationEdit       imageProviderOperation = "edit"
)

type imageUpstreamProvider struct {
	id            string
	generationURL string
	editURL       string
	model         string
	apiKey        string
	client        *http.Client
	operations    map[imageProviderOperation]struct{}
}

func (p imageUpstreamProvider) supports(operation imageProviderOperation) bool {
	_, ok := p.operations[operation]
	return ok
}

func (p imageUpstreamProvider) endpoint(operation imageProviderOperation) string {
	switch operation {
	case imageOperationGeneration:
		return p.generationURL
	case imageOperationEdit:
		return p.editURL
	default:
		return ""
	}
}

type imageProviderPoolDocument struct {
	Providers []imageProviderFileEntry `json:"providers"`
}

type imageProviderFileEntry struct {
	ID             string   `json:"id"`
	GenerationURL  string   `json:"generation_url"`
	EditURL        string   `json:"edit_url"`
	Model          string   `json:"model"`
	APIKey         string   `json:"api_key"`
	APIKeyFile     string   `json:"api_key_file"`
	Operations     []string `json:"operations"`
	TimeoutSeconds int64    `json:"timeout_seconds"`
}

func loadImageUpstreamProvidersFile(path string, defaultModel string, defaultTimeout time.Duration) ([]imageUpstreamProvider, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return nil, errors.New("CATSCO_IMAGE_UPSTREAMS_FILE is not set")
	}
	contents, err := os.ReadFile(trimmedPath)
	if err != nil {
		return nil, fmt.Errorf("read CATSCO_IMAGE_UPSTREAMS_FILE: %w", err)
	}

	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var document imageProviderPoolDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode CATSCO_IMAGE_UPSTREAMS_FILE: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("CATSCO_IMAGE_UPSTREAMS_FILE must contain one JSON object")
	}
	if len(document.Providers) < minImageRaceProviders || len(document.Providers) > maxImageRaceProviders {
		return nil, fmt.Errorf("CATSCO_IMAGE_UPSTREAMS_FILE must configure %d-%d providers", minImageRaceProviders, maxImageRaceProviders)
	}

	modelFallback := strings.TrimSpace(defaultModel)
	if modelFallback == "" {
		modelFallback = defaultImageGenerationModel
	}
	if defaultTimeout <= 0 {
		defaultTimeout = defaultImageGenerationTimeout
	}

	seenIDs := make(map[string]struct{}, len(document.Providers))
	providers := make([]imageUpstreamProvider, 0, len(document.Providers))
	for index, entry := range document.Providers {
		provider, err := buildImageUpstreamProvider(entry, modelFallback, defaultTimeout)
		if err != nil {
			return nil, fmt.Errorf("image provider %d: %w", index+1, err)
		}
		if _, duplicate := seenIDs[provider.id]; duplicate {
			return nil, fmt.Errorf("image provider %d: duplicate id %q", index+1, provider.id)
		}
		seenIDs[provider.id] = struct{}{}
		providers = append(providers, provider)
	}
	return providers, nil
}

func buildImageUpstreamProvider(entry imageProviderFileEntry, defaultModel string, defaultTimeout time.Duration) (imageUpstreamProvider, error) {
	id := strings.TrimSpace(entry.ID)
	if !imageProviderIDPattern.MatchString(id) {
		return imageUpstreamProvider{}, errors.New("id must use 1-64 letters, digits, dots, underscores, or hyphens")
	}
	operations, err := parseImageProviderOperations(entry.Operations)
	if err != nil {
		return imageUpstreamProvider{}, err
	}

	apiKey, err := readImageProviderAPIKey(entry)
	if err != nil {
		return imageUpstreamProvider{}, err
	}
	if apiKey == "" {
		return imageUpstreamProvider{}, errors.New("api_key or api_key_file is required")
	}

	model := strings.TrimSpace(entry.Model)
	if model == "" {
		model = defaultModel
	}
	timeout := defaultTimeout
	if entry.TimeoutSeconds < 0 {
		return imageUpstreamProvider{}, errors.New("timeout_seconds must be positive")
	}
	if entry.TimeoutSeconds > 0 {
		timeout = time.Duration(entry.TimeoutSeconds) * time.Second
	}

	provider := imageUpstreamProvider{
		id:         id,
		model:      model,
		apiKey:     apiKey,
		client:     &http.Client{Timeout: timeout},
		operations: operations,
	}

	if provider.supports(imageOperationGeneration) {
		parsed, err := parseImageGenerationUpstreamURL(entry.GenerationURL)
		if err != nil {
			return imageUpstreamProvider{}, fmt.Errorf("generation_url: %w", err)
		}
		resolved, err := resolveImageOperationUpstreamURL(parsed, "generations")
		if err != nil {
			return imageUpstreamProvider{}, fmt.Errorf("generation_url: %w", err)
		}
		provider.generationURL = resolved.String()
	}

	if provider.supports(imageOperationEdit) {
		rawEditURL := strings.TrimSpace(entry.EditURL)
		if rawEditURL == "" {
			rawEditURL = strings.TrimSpace(entry.GenerationURL)
		}
		parsed, err := parseImageGenerationUpstreamURL(rawEditURL)
		if err != nil {
			return imageUpstreamProvider{}, fmt.Errorf("edit_url: %w", err)
		}
		resolved, err := resolveImageOperationUpstreamURL(parsed, "edits")
		if err != nil {
			return imageUpstreamProvider{}, fmt.Errorf("edit_url: %w", err)
		}
		provider.editURL = resolved.String()
	}

	return provider, nil
}

func parseImageProviderOperations(values []string) (map[imageProviderOperation]struct{}, error) {
	if len(values) == 0 {
		return nil, errors.New("operations must contain generation and/or edit")
	}
	operations := make(map[imageProviderOperation]struct{}, len(values))
	for _, value := range values {
		operation := imageProviderOperation(strings.TrimSpace(value))
		switch operation {
		case imageOperationGeneration, imageOperationEdit:
		default:
			return nil, fmt.Errorf("unsupported operation %q", value)
		}
		if _, duplicate := operations[operation]; duplicate {
			return nil, fmt.Errorf("duplicate operation %q", operation)
		}
		operations[operation] = struct{}{}
	}
	return operations, nil
}

func readImageProviderAPIKey(entry imageProviderFileEntry) (string, error) {
	inlineKey := strings.TrimSpace(entry.APIKey)
	keyFile := strings.TrimSpace(entry.APIKeyFile)
	if inlineKey != "" && keyFile != "" {
		return "", errors.New("set only one of api_key or api_key_file")
	}
	if keyFile == "" {
		return inlineKey, nil
	}
	contents, err := os.ReadFile(keyFile)
	if err != nil {
		return "", fmt.Errorf("read api_key_file: %w", err)
	}
	return strings.TrimSpace(string(contents)), nil
}
