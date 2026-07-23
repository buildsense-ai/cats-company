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
	"sync/atomic"
	"time"
)

const (
	requiredImageRaceProviders  = 3
	maximumImageProviderAPIKeys = 4
)

var imageProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type imageProviderOperation string

const (
	imageOperationGeneration imageProviderOperation = "generation"
	imageOperationEdit       imageProviderOperation = "edit"
)

type imageProviderEditTransport string

const (
	imageEditTransportJSONDataURL imageProviderEditTransport = "json_data_url"
	imageEditTransportMultipart   imageProviderEditTransport = "multipart"
)

type imageUpstreamProvider struct {
	id            string
	generationURL string
	editURL       string
	model         string
	credentials   *imageProviderCredentials
	client        *http.Client
	operations    map[imageProviderOperation]struct{}
	editTransport imageProviderEditTransport
}

type imageProviderCredentials struct {
	keys      []string
	preferred atomic.Uint32
}

type imageProviderCredential struct {
	index  int
	apiKey string
}

func newImageProviderCredentials(keys []string) *imageProviderCredentials {
	return &imageProviderCredentials{keys: append([]string(nil), keys...)}
}

func (c *imageProviderCredentials) ordered() []imageProviderCredential {
	if c == nil || len(c.keys) == 0 {
		return nil
	}
	start := int(c.preferred.Load()) % len(c.keys)
	ordered := make([]imageProviderCredential, 0, len(c.keys))
	for offset := range len(c.keys) {
		index := (start + offset) % len(c.keys)
		ordered = append(ordered, imageProviderCredential{index: index, apiKey: c.keys[index]})
	}
	return ordered
}

func (c *imageProviderCredentials) prefer(index int) {
	if c == nil || index < 0 || index >= len(c.keys) {
		return
	}
	c.preferred.Store(uint32(index))
}

func (c *imageProviderCredentials) primary() string {
	ordered := c.ordered()
	if len(ordered) == 0 {
		return ""
	}
	return ordered[0].apiKey
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
	APIKeyFiles    []string `json:"api_key_files"`
	EditTransport  string   `json:"edit_transport"`
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
	if len(document.Providers) != requiredImageRaceProviders {
		return nil, fmt.Errorf(
			"CATSCO_IMAGE_UPSTREAMS_FILE must configure exactly %d providers",
			requiredImageRaceProviders,
		)
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
	editTransport, err := parseImageProviderEditTransport(entry.EditTransport)
	if err != nil {
		return imageUpstreamProvider{}, err
	}

	apiKeys, err := readImageProviderAPIKeys(entry)
	if err != nil {
		return imageUpstreamProvider{}, err
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
		id:          id,
		model:       model,
		credentials: newImageProviderCredentials(apiKeys),
		client:      &http.Client{Timeout: timeout},
		operations: map[imageProviderOperation]struct{}{
			imageOperationGeneration: {},
			imageOperationEdit:       {},
		},
		editTransport: editTransport,
	}

	parsedGenerationURL, err := parseImageGenerationUpstreamURL(entry.GenerationURL)
	if err != nil {
		return imageUpstreamProvider{}, fmt.Errorf("generation_url: %w", err)
	}
	resolvedGenerationURL, err := resolveImageOperationUpstreamURL(parsedGenerationURL, "generations")
	if err != nil {
		return imageUpstreamProvider{}, fmt.Errorf("generation_url: %w", err)
	}
	provider.generationURL = resolvedGenerationURL.String()

	parsedEditURL, err := parseImageGenerationUpstreamURL(entry.EditURL)
	if err != nil {
		return imageUpstreamProvider{}, fmt.Errorf("edit_url: %w", err)
	}
	resolvedEditURL, err := resolveImageOperationUpstreamURL(parsedEditURL, "edits")
	if err != nil {
		return imageUpstreamProvider{}, fmt.Errorf("edit_url: %w", err)
	}
	provider.editURL = resolvedEditURL.String()

	return provider, nil
}

func parseImageProviderEditTransport(value string) (imageProviderEditTransport, error) {
	transport := imageProviderEditTransport(strings.TrimSpace(value))
	switch transport {
	case imageEditTransportJSONDataURL, imageEditTransportMultipart:
		return transport, nil
	default:
		return "", fmt.Errorf("edit_transport must be %q or %q", imageEditTransportJSONDataURL, imageEditTransportMultipart)
	}
}

func readImageProviderAPIKeys(entry imageProviderFileEntry) ([]string, error) {
	inlineKey := strings.TrimSpace(entry.APIKey)
	keyFile := strings.TrimSpace(entry.APIKeyFile)
	hasKeyFiles := entry.APIKeyFiles != nil
	configuredForms := 0
	for _, configured := range []bool{inlineKey != "", keyFile != "", hasKeyFiles} {
		if configured {
			configuredForms++
		}
	}
	if configuredForms > 1 {
		return nil, errors.New("set only one of api_key, api_key_file, or api_key_files")
	}

	var keys []string
	switch {
	case inlineKey != "":
		keys = []string{inlineKey}
	case keyFile != "":
		key, err := readImageProviderAPIKeyFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read api_key_file: %w", err)
		}
		keys = []string{key}
	case hasKeyFiles:
		if len(entry.APIKeyFiles) == 0 {
			return nil, errors.New("api_key_files must contain at least one file")
		}
		if len(entry.APIKeyFiles) > maximumImageProviderAPIKeys {
			return nil, fmt.Errorf("api_key_files must not contain more than %d files", maximumImageProviderAPIKeys)
		}
		keys = make([]string, 0, len(entry.APIKeyFiles))
		for index, rawPath := range entry.APIKeyFiles {
			path := strings.TrimSpace(rawPath)
			if path == "" {
				return nil, fmt.Errorf("api_key_files[%d] is empty", index)
			}
			key, err := readImageProviderAPIKeyFile(path)
			if err != nil {
				return nil, fmt.Errorf("read api_key_files[%d]: %w", index, err)
			}
			keys = append(keys, key)
		}
	default:
		return nil, errors.New("api_key, api_key_file, or api_key_files is required")
	}

	seen := make(map[string]struct{}, len(keys))
	for index, key := range keys {
		if key == "" {
			return nil, fmt.Errorf("image provider credential %d is empty", index+1)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("image provider credentials must be unique")
		}
		seen[key] = struct{}{}
	}
	return keys, nil
}

func readImageProviderAPIKeyFile(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(contents)), nil
}
