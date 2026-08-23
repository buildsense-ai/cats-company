package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultSkillHubBaseURL            = "https://skillhub.catsco.fun:19990"
	defaultSkillHubTimeout            = 15 * time.Second
	defaultSkillHubMaxResponse  int64 = 2 << 20
	skillHubSkillsPrefix              = "/api/skills"
	skillHubPrivateMetadataPath       = "/api/bot/private-skill-metadata"
	skillHubPrivateHistoryPath        = "/api/bot/private-skill-history"
)

// SkillHubProxyHandler exposes the public SkillHub catalogue through CatsCo.
// The upstream URL is fixed at startup; callers cannot turn this into an open proxy.
type SkillHubProxyHandler struct {
	baseURL         *url.URL
	client          *http.Client
	maxResponseSize int64
	configError     error
}

type SkillHubProxyOptions struct {
	Timeout         time.Duration
	MaxResponseSize int64
	Client          *http.Client
}

type privateSkillMetadataRequest struct {
	References []privateSkillMetadataReference `json:"references"`
}

type privateSkillMetadataReference struct {
	SkillID              string `json:"skillId"`
	Version              string `json:"version"`
	DisplayName          string `json:"displayName,omitempty"`
	RevisionNumber       int64  `json:"revisionNumber,omitempty"`
	LastChangedByUserUID int64  `json:"lastChangedByUserUid,omitempty"`
	LastChangedAt        string `json:"lastChangedAt,omitempty"`
	ChangeSource         string `json:"changeSource,omitempty"`
}

type privateSkillMetadataResponse struct {
	Skills []privateSkillMetadataReference `json:"skills"`
}

type privateSkillHistoryRequest struct {
	SkillID              string `json:"skillId"`
	Limit                int64  `json:"limit,omitempty"`
	BeforeRevisionNumber int64  `json:"beforeRevisionNumber,omitempty"`
}

type privateSkillHistoryResponse struct {
	SkillID                  string                          `json:"skillId"`
	Versions                 []privateSkillMetadataReference `json:"versions"`
	NextBeforeRevisionNumber int64                           `json:"nextBeforeRevisionNumber,omitempty"`
}

func NewSkillHubProxyHandler(baseURL string, options SkillHubProxyOptions) *SkillHubProxyHandler {
	h := &SkillHubProxyHandler{maxResponseSize: options.MaxResponseSize}
	if h.maxResponseSize <= 0 {
		h.maxResponseSize = defaultSkillHubMaxResponse
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		h.configError = fmt.Errorf("invalid SkillHub base URL")
		return h
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	h.baseURL = parsed

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultSkillHubTimeout
	}
	if options.Client != nil {
		clientCopy := *options.Client
		h.client = &clientCopy
	} else {
		h.client = &http.Client{Timeout: timeout}
	}
	// Keep every redirect on the configured SkillHub origin. A fixed initial
	// URL alone is not enough because net/http follows cross-origin redirects
	// by default, which could otherwise turn this catalogue proxy into SSRF.
	h.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != h.baseURL.Scheme || req.URL.Host != h.baseURL.Host {
			return errors.New("SkillHub redirect changed origin")
		}
		return nil
	}
	return h
}

func NewSkillHubProxyHandlerFromEnv() *SkillHubProxyHandler {
	baseURL := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultSkillHubBaseURL
	}
	timeout := defaultSkillHubTimeout
	if raw := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := time.ParseDuration(raw + "s"); err == nil && seconds > 0 {
			timeout = seconds
		}
	}
	return NewSkillHubProxyHandler(baseURL, SkillHubProxyOptions{Timeout: timeout})
}

// HandleSkills handles GET /api/skillhub/skills and forwards only catalogue query parameters.
func (h *SkillHubProxyHandler) HandleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	query := url.Values{}
	for _, key := range []string{"q", "category", "agent_version", "platform"} {
		for _, value := range r.URL.Query()[key] {
			query.Add(key, value)
		}
	}
	h.proxyJSON(w, r, skillHubSkillsPrefix, query)
}

// HandleSkill handles GET /api/skillhub/skills/{skillId}[/{version route}].
func (h *SkillHubProxyHandler) HandleSkill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	const prefix = "/api/skillhub/skills/"
	escapedPath := r.URL.EscapedPath()
	if !strings.HasPrefix(escapedPath, prefix) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "skill not found"})
		return
	}
	rawParts := strings.Split(strings.TrimPrefix(escapedPath, prefix), "/")
	for _, rawPart := range rawParts {
		if strings.Contains(strings.ToLower(rawPart), "%2f") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skillId"})
			return
		}
	}
	parts, err := decodeSkillPathParts(rawParts)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skillId"})
		return
	}
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skillId is required"})
		return
	}
	if len(parts) >= 2 && parts[len(parts)-2] == "versions" {
		if len(parts) < 3 || parts[len(parts)-1] == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version is required"})
			return
		}
		skillParts := parts[:len(parts)-2]
		if err := validateSkillPathParts(skillParts); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skillId"})
			return
		}
		version := parts[len(parts)-1]
		if !validSkillPathPart(version) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid version"})
			return
		}
		path := skillHubSkillsPrefix + "/" + encodeSkillPath(skillParts) + "/versions/" + version
		h.proxyJSON(w, r, path, nil)
		return
	}
	if err := validateSkillPathParts(parts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skillId"})
		return
	}
	h.proxyJSON(w, r, skillHubSkillsPrefix+"/"+encodeSkillPath(parts), nil)
}

// ResolvePrivateSkillMetadata asks SkillHub for display-only metadata using
// the target Bot's own API key. Package contents and the API key never leave
// the server-side request.
func (h *SkillHubProxyHandler) ResolvePrivateSkillMetadata(
	ctx context.Context,
	botID string,
	apiKey string,
	references []types.BotSkillRef,
) (map[string]BotSkillDisplayMetadata, error) {
	if h == nil || h.configError != nil || h.baseURL == nil {
		return nil, errors.New("SkillHub is not configured")
	}
	botID = strings.TrimSpace(botID)
	apiKey = strings.TrimSpace(apiKey)
	if botID == "" || apiKey == "" {
		return nil, errors.New("Bot SkillHub credentials are unavailable")
	}
	if len(references) == 0 {
		return map[string]BotSkillDisplayMetadata{}, nil
	}
	if len(references) > maxBotSkillRefs {
		return nil, errors.New("too many private Skill metadata references")
	}
	requested := make(map[string]struct{}, len(references))
	clean := make([]privateSkillMetadataReference, 0, len(references))
	for _, reference := range references {
		skillID := strings.TrimSpace(reference.SkillID)
		version := strings.TrimSpace(reference.Version)
		if !isPrivateBotSkillReference(skillID) || !validBotSkillID(skillID) || !validBotSkillRefPart(version, maxBotSkillVersionBytes) {
			return nil, errors.New("invalid private Skill metadata reference")
		}
		key := botSkillMetadataKey(skillID, version)
		if _, exists := requested[key]; exists {
			continue
		}
		requested[key] = struct{}{}
		clean = append(clean, privateSkillMetadataReference{SkillID: skillID, Version: version})
	}
	body, err := json.Marshal(privateSkillMetadataRequest{References: clean})
	if err != nil {
		return nil, err
	}
	target := *h.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + skillHubPrivateMetadataPath
	target.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "ApiKey "+apiKey)
	request.Header.Set("X-CatsCo-Bot-Id", botID)
	request.Header.Set("User-Agent", "cats-company-skillhub-private-metadata/1.0")
	response, err := h.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := readLimited(response.Body, h.maxResponseSize)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("SkillHub private metadata request failed: %d", response.StatusCode)
	}
	var decoded privateSkillMetadataResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, errors.New("SkillHub returned invalid private metadata")
	}
	metadata := make(map[string]BotSkillDisplayMetadata, len(decoded.Skills))
	for _, skill := range decoded.Skills {
		key := botSkillMetadataKey(strings.TrimSpace(skill.SkillID), strings.TrimSpace(skill.Version))
		name := strings.TrimSpace(skill.DisplayName)
		if _, ok := requested[key]; !ok || !validBotSkillDisplayName(name) {
			continue
		}
		presentation := BotSkillDisplayMetadata{DisplayName: name}
		if skill.RevisionNumber > 0 && skill.RevisionNumber <= 1_000_000_000 {
			presentation.RevisionNumber = skill.RevisionNumber
		}
		if skill.LastChangedByUserUID > 0 {
			presentation.LastChangedByUserUID = skill.LastChangedByUserUID
		}
		if lastChangedAt := strings.TrimSpace(skill.LastChangedAt); len(lastChangedAt) <= 64 {
			if _, err := time.Parse(time.RFC3339Nano, lastChangedAt); err == nil {
				presentation.LastChangedAt = lastChangedAt
			}
		}
		switch skill.ChangeSource {
		case "runtime_backup", "conversation_mutation":
			presentation.ChangeSource = skill.ChangeSource
		}
		metadata[key] = presentation
	}
	return metadata, nil
}

// ResolvePrivateSkillHistory asks SkillHub for one Bot-private Skill's
// immutable content revisions. It validates and rebuilds the response so
// package identity and audit-only fields can never reach browser clients.
func (h *SkillHubProxyHandler) ResolvePrivateSkillHistory(
	ctx context.Context,
	botID string,
	apiKey string,
	skillID string,
	limit int64,
	beforeRevisionNumber int64,
) (BotSkillVersionHistory, error) {
	result := BotSkillVersionHistory{Versions: []BotSkillVersionHistoryEntry{}}
	if h == nil || h.configError != nil || h.baseURL == nil {
		return result, errors.New("SkillHub is not configured")
	}
	botID = strings.TrimSpace(botID)
	apiKey = strings.TrimSpace(apiKey)
	skillID = strings.TrimSpace(skillID)
	if botID == "" || apiKey == "" {
		return result, errors.New("Bot SkillHub credentials are unavailable")
	}
	if !isPrivateBotSkillReference(skillID) || !validBotSkillID(skillID) {
		return result, errors.New("invalid private Skill history reference")
	}
	if limit < 1 || limit > 50 || beforeRevisionNumber < 0 || beforeRevisionNumber > 1_000_000_000 {
		return result, errors.New("invalid private Skill history pagination")
	}
	body, err := json.Marshal(privateSkillHistoryRequest{
		SkillID: skillID, Limit: limit, BeforeRevisionNumber: beforeRevisionNumber,
	})
	if err != nil {
		return result, err
	}
	target := *h.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + skillHubPrivateHistoryPath
	target.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "ApiKey "+apiKey)
	request.Header.Set("X-CatsCo-Bot-Id", botID)
	request.Header.Set("User-Agent", "cats-company-skillhub-private-history/1.0")
	response, err := h.client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	responseBody, err := readLimited(response.Body, h.maxResponseSize)
	if err != nil {
		return result, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, fmt.Errorf("SkillHub private history request failed: %d", response.StatusCode)
	}
	var decoded privateSkillHistoryResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return result, errors.New("SkillHub returned invalid private history")
	}
	if strings.TrimSpace(decoded.SkillID) != skillID {
		return result, errors.New("SkillHub returned mismatched private history")
	}
	result.SkillID = skillID
	seen := make(map[int64]struct{}, len(decoded.Versions))
	for _, version := range decoded.Versions {
		versionSkillID := strings.TrimSpace(version.SkillID)
		exactVersion := strings.TrimSpace(version.Version)
		name := strings.TrimSpace(version.DisplayName)
		if versionSkillID != skillID || !validBotSkillRefPart(exactVersion, maxBotSkillVersionBytes) ||
			version.RevisionNumber < 1 || version.RevisionNumber > 1_000_000_000 {
			continue
		}
		if name != "" && !validBotSkillDisplayName(name) {
			continue
		}
		if _, exists := seen[version.RevisionNumber]; exists {
			continue
		}
		seen[version.RevisionNumber] = struct{}{}
		entry := BotSkillVersionHistoryEntry{
			Source: "skillhub", SkillID: skillID, Version: exactVersion,
			DisplayName: name, RevisionNumber: version.RevisionNumber,
		}
		if lastChangedAt := strings.TrimSpace(version.LastChangedAt); len(lastChangedAt) <= 64 {
			if _, parseErr := time.Parse(time.RFC3339Nano, lastChangedAt); parseErr == nil {
				entry.LastChangedAt = lastChangedAt
			}
		}
		switch version.ChangeSource {
		case "runtime_backup", "conversation_mutation":
			entry.ChangeSource = version.ChangeSource
		}
		result.Versions = append(result.Versions, entry)
	}
	if decoded.NextBeforeRevisionNumber > 0 && decoded.NextBeforeRevisionNumber <= 1_000_000_000 {
		result.NextBeforeRevisionNumber = decoded.NextBeforeRevisionNumber
	}
	return result, nil
}

func isPrivateBotSkillReference(skillID string) bool {
	return strings.HasPrefix(skillID, "priv_") || strings.HasPrefix(skillID, "private/")
}

func botSkillMetadataKey(skillID string, version string) string {
	return skillID + "\x00" + version
}

func (h *SkillHubProxyHandler) proxyJSON(w http.ResponseWriter, r *http.Request, path string, query url.Values) {
	if h == nil || h.configError != nil || h.baseURL == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "SkillHub is not configured"})
		return
	}
	target := *h.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build SkillHub request"})
		return
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cats-company-skillhub-proxy/1.0")
	response, err := h.client.Do(request)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "SkillHub is unavailable"})
		return
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body, h.maxResponseSize)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "SkillHub response is too large"})
		} else {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to read SkillHub response"})
		}
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusTooManyRequests {
			status = response.StatusCode
		}
		writeJSON(w, status, map[string]string{"error": "SkillHub request failed"})
		return
	}
	if !json.Valid(body) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "SkillHub returned invalid JSON"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

var errResponseTooLarge = errors.New("response too large")

func readLimited(reader io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return nil, errResponseTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errResponseTooLarge
	}
	return data, nil
}

func validateSkillPathParts(parts []string) error {
	if len(parts) == 0 {
		return errors.New("skill id is empty")
	}
	for _, part := range parts {
		if !validSkillPathPart(part) {
			return errors.New("invalid skill path")
		}
	}
	return nil
}

func validSkillPathPart(part string) bool {
	return part != "" && part != "." && part != ".." && !strings.ContainsAny(part, `/\\`) && !strings.ContainsAny(part, "\x00\r\n")
}

func encodeSkillPath(parts []string) string {
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		encoded = append(encoded, part)
	}
	return strings.Join(encoded, "/")
}

func decodeSkillPathParts(rawParts []string) ([]string, error) {
	parts := make([]string, 0, len(rawParts))
	for _, raw := range rawParts {
		part, err := url.PathUnescape(raw)
		if err != nil || !validSkillPathPart(part) {
			return nil, errors.New("invalid skill path")
		}
		parts = append(parts, part)
	}
	return parts, nil
}
