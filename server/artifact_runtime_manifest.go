package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	artifactRuntimeManifestContract = "catsco.artifact-manifest.v4"
	artifactRuntimeVersion01        = "0.1"
	artifactRuntimeVersion02        = "0.2"
	artifactRuntimeManifestMaxBytes = 64 * 1024
	artifactRuntimeManifestTTL      = 5 * time.Minute
	artifactRuntimeManifestCacheMax = 1024
	artifactRuntimeSurfaceMaxItems  = 32
	artifactRuntimeStateMaxItems    = 32
)

var artifactRuntimeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

type ArtifactRuntimeSurface struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type ArtifactRuntimeStateDeclaration struct {
	Namespace string `json:"namespace"`
	Mode      string `json:"mode"`
}

type ArtifactRuntimeManifest struct {
	Version  string                            `json:"version"`
	Surfaces []ArtifactRuntimeSurface          `json:"surfaces"`
	State    []ArtifactRuntimeStateDeclaration `json:"state"`
}

func (m ArtifactRuntimeManifest) allowsNamespace(namespace string, write bool) bool {
	for _, declaration := range m.State {
		if declaration.Namespace != namespace {
			continue
		}
		return !write || declaration.Mode == "read-write"
	}
	return false
}

func (m ArtifactRuntimeManifest) allowsSurface(surfaceID string) bool {
	for _, surface := range m.Surfaces {
		if surface.ID == surfaceID {
			return true
		}
	}
	return false
}

type ArtifactRuntimeManifestResolver interface {
	ResolveArtifactRuntimeManifest(ctx context.Context, record ArtifactContextRecord, displayedVersion int64) (ArtifactRuntimeManifest, error)
}

type artifactRuntimeManifestCacheEntry struct {
	manifest  ArtifactRuntimeManifest
	expiresAt time.Time
}

func (h *CloudArtifactHandler) ResolveArtifactRuntimeManifest(
	ctx context.Context,
	record ArtifactContextRecord,
	displayedVersion int64,
) (ArtifactRuntimeManifest, error) {
	if h == nil || !validArtifactContextRecord(record, record.ID) || displayedVersion <= 0 {
		return ArtifactRuntimeManifest{}, errors.New("invalid Artifact Runtime manifest request")
	}
	manifestURL, err := artifactTaskVersionManifestURL(record, displayedVersion)
	if err != nil {
		return ArtifactRuntimeManifest{}, err
	}
	if cached, ok := h.cachedArtifactRuntimeManifest(manifestURL); ok {
		return cached, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return ArtifactRuntimeManifest{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "catsco-artifact-runtime/0.2")
	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return ArtifactRuntimeManifest{}, fmt.Errorf("Artifact Runtime manifest unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ArtifactRuntimeManifest{}, fmt.Errorf("Artifact Runtime manifest returned %d", response.StatusCode)
	}
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.String() != manifestURL {
		return ArtifactRuntimeManifest{}, errors.New("Artifact Runtime manifest redirect is not allowed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, artifactRuntimeManifestMaxBytes+1))
	if err != nil || len(body) < 2 || len(body) > artifactRuntimeManifestMaxBytes {
		return ArtifactRuntimeManifest{}, errors.New("Artifact Runtime manifest exceeds size limits")
	}
	manifest, err := parseArtifactRuntimeManifest(body)
	if err != nil {
		return ArtifactRuntimeManifest{}, err
	}
	h.storeArtifactRuntimeManifest(manifestURL, manifest)
	return manifest, nil
}

func (h *CloudArtifactHandler) cachedArtifactRuntimeManifest(key string) (ArtifactRuntimeManifest, bool) {
	if h == nil {
		return ArtifactRuntimeManifest{}, false
	}
	now := time.Now()
	h.artifactRuntimeManifestCacheMu.Lock()
	defer h.artifactRuntimeManifestCacheMu.Unlock()
	entry, ok := h.artifactRuntimeManifestCache[key]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(h.artifactRuntimeManifestCache, key)
		}
		return ArtifactRuntimeManifest{}, false
	}
	return entry.manifest, true
}

func (h *CloudArtifactHandler) storeArtifactRuntimeManifest(key string, manifest ArtifactRuntimeManifest) {
	if h == nil {
		return
	}
	ttl := h.artifactRuntimeManifestTTL
	if ttl <= 0 {
		ttl = artifactRuntimeManifestTTL
	}
	h.artifactRuntimeManifestCacheMu.Lock()
	defer h.artifactRuntimeManifestCacheMu.Unlock()
	if h.artifactRuntimeManifestCache == nil {
		h.artifactRuntimeManifestCache = make(map[string]artifactRuntimeManifestCacheEntry)
	}
	now := time.Now()
	for cachedKey, entry := range h.artifactRuntimeManifestCache {
		if !now.Before(entry.expiresAt) {
			delete(h.artifactRuntimeManifestCache, cachedKey)
		}
	}
	if len(h.artifactRuntimeManifestCache) >= artifactRuntimeManifestCacheMax {
		oldestKey := ""
		var oldestExpiry time.Time
		for cachedKey, entry := range h.artifactRuntimeManifestCache {
			if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = cachedKey
				oldestExpiry = entry.expiresAt
			}
		}
		delete(h.artifactRuntimeManifestCache, oldestKey)
	}
	h.artifactRuntimeManifestCache[key] = artifactRuntimeManifestCacheEntry{
		manifest: manifest, expiresAt: now.Add(ttl),
	}
}

func parseArtifactRuntimeManifest(body []byte) (ArtifactRuntimeManifest, error) {
	if err := validateArtifactRuntimeJSONTokens(body); err != nil {
		return ArtifactRuntimeManifest{}, fmt.Errorf("Artifact Runtime manifest is invalid: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]interface{}
	if err := decoder.Decode(&root); err != nil || ensureJSONEOF(decoder) != nil || root == nil {
		return ArtifactRuntimeManifest{}, errors.New("Artifact Runtime manifest is invalid JSON")
	}
	allowedRoot := map[string]bool{
		"contract_version": true, "purpose": true, "views": true, "entities": true,
		"entrypoints": true, "observation_capabilities": true, "result_sinks": true,
		"task_intents": true, "runtime": true,
	}
	for key := range root {
		if !allowedRoot[key] || artifactTaskUnsafeKey(key) {
			return ArtifactRuntimeManifest{}, fmt.Errorf("Artifact Runtime manifest contains unsupported field %q", key)
		}
	}
	if root["contract_version"] != artifactRuntimeManifestContract {
		return ArtifactRuntimeManifest{}, fmt.Errorf("Artifact Runtime requires %s", artifactRuntimeManifestContract)
	}
	runtimeValue, ok := root["runtime"].(map[string]interface{})
	if !ok {
		return ArtifactRuntimeManifest{}, errors.New("Artifact Runtime manifest requires runtime")
	}
	for key := range runtimeValue {
		if artifactTaskUnsafeKey(key) || (key != "version" && key != "surfaces" && key != "state") {
			return ArtifactRuntimeManifest{}, fmt.Errorf("Artifact Runtime contains unsupported field %q", key)
		}
	}
	version, ok := artifactRuntimeText(runtimeValue["version"], 16)
	if !ok || (version != artifactRuntimeVersion01 && version != artifactRuntimeVersion02) {
		return ArtifactRuntimeManifest{}, fmt.Errorf(
			"Artifact Runtime version must be %s or %s",
			artifactRuntimeVersion01,
			artifactRuntimeVersion02,
		)
	}
	surfaces, err := parseArtifactRuntimeSurfaces(runtimeValue["surfaces"])
	if err != nil {
		return ArtifactRuntimeManifest{}, err
	}
	states, err := parseArtifactRuntimeStateDeclarations(runtimeValue["state"])
	if err != nil {
		return ArtifactRuntimeManifest{}, err
	}
	return ArtifactRuntimeManifest{Version: version, Surfaces: surfaces, State: states}, nil
}

func parseArtifactRuntimeSurfaces(value interface{}) ([]ArtifactRuntimeSurface, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > artifactRuntimeSurfaceMaxItems {
		return nil, fmt.Errorf("Artifact Runtime surfaces must contain 1-%d items", artifactRuntimeSurfaceMaxItems)
	}
	result := make([]ArtifactRuntimeSurface, 0, len(items))
	seen := make(map[string]bool, len(items))
	for index, raw := range items {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Artifact Runtime surfaces[%d] must be an object", index)
		}
		for key := range entry {
			if artifactTaskUnsafeKey(key) || (key != "id" && key != "title") {
				return nil, fmt.Errorf("Artifact Runtime surfaces[%d] contains unsupported field %q", index, key)
			}
		}
		id, idOK := artifactRuntimeName(entry["id"])
		if !idOK || seen[id] {
			return nil, fmt.Errorf("Artifact Runtime surfaces[%d].id is invalid or duplicated", index)
		}
		seen[id] = true
		title := ""
		if rawTitle, hasTitle := entry["title"]; hasTitle {
			var titleOK bool
			title, titleOK = artifactRuntimeText(rawTitle, 128)
			if !titleOK {
				return nil, fmt.Errorf("Artifact Runtime surfaces[%d].title is invalid", index)
			}
		}
		result = append(result, ArtifactRuntimeSurface{ID: id, Title: title})
	}
	return result, nil
}

func parseArtifactRuntimeStateDeclarations(value interface{}) ([]ArtifactRuntimeStateDeclaration, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > artifactRuntimeStateMaxItems {
		return nil, fmt.Errorf("Artifact Runtime state must contain 1-%d items", artifactRuntimeStateMaxItems)
	}
	result := make([]ArtifactRuntimeStateDeclaration, 0, len(items))
	seen := make(map[string]bool, len(items))
	for index, raw := range items {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Artifact Runtime state[%d] must be an object", index)
		}
		for key := range entry {
			if artifactTaskUnsafeKey(key) || (key != "namespace" && key != "mode") {
				return nil, fmt.Errorf("Artifact Runtime state[%d] contains unsupported field %q", index, key)
			}
		}
		namespace, namespaceOK := artifactRuntimeName(entry["namespace"])
		mode, modeOK := artifactRuntimeText(entry["mode"], 32)
		if !namespaceOK || seen[namespace] || !modeOK || mode != "read-write" {
			return nil, fmt.Errorf("Artifact Runtime state[%d] is invalid or duplicated", index)
		}
		seen[namespace] = true
		result = append(result, ArtifactRuntimeStateDeclaration{Namespace: namespace, Mode: mode})
	}
	return result, nil
}

func artifactRuntimeName(value interface{}) (string, bool) {
	text, ok := artifactRuntimeText(value, 64)
	return text, ok && artifactRuntimeNamePattern.MatchString(text)
}

func artifactRuntimeText(value interface{}, maxRunes int) (string, bool) {
	text, ok := value.(string)
	return text, ok && text != "" && text == strings.TrimSpace(text) && utf8.ValidString(text) &&
		utf8.RuneCountInString(text) <= maxRunes && !strings.ContainsAny(text, "\x00\r\n")
}
