package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	artifactNodeConfigMaxBytes = 256 << 10
	artifactNodeTokenMaxBytes  = 4 << 10
	artifactDirectUIDToken     = "{uid}"
)

var artifactNodeIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._-]*[a-z0-9])?$`)
var artifactNodeTokenEnvPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type artifactNode struct {
	id              string
	publicBaseURL   string
	managementURL   string
	managementToken string
	rootPublicIndex bool
}

type artifactDirectURLTemplate struct {
	value string
}

type artifactNodeRegistry struct {
	nodes            map[string]artifactNode
	agents           map[int64]string
	fallbackToLegacy bool
}

type artifactNodeConfigDocument struct {
	Nodes            map[string]artifactNodeConfigEntry `json:"nodes"`
	Agents           map[string]string                  `json:"agents"`
	FallbackToLegacy bool                               `json:"fallback_to_legacy,omitempty"`
}

type artifactNodeConfigEntry struct {
	PublicBaseURL       string `json:"public_base_url"`
	ManagementURL       string `json:"management_url"`
	ManagementTokenEnv  string `json:"management_token_env"`
	ManagementTokenFile string `json:"management_token_file"`
}

func loadArtifactNodeRegistryFromEnv() (*artifactNodeRegistry, error) {
	inline := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_NODES_JSON"))
	filePath := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_NODES_FILE"))
	if inline == "" && filePath == "" {
		return nil, nil
	}
	if inline != "" && filePath != "" {
		return nil, errors.New("configure only one of CATSCO_ARTIFACT_NODES_JSON or CATSCO_ARTIFACT_NODES_FILE")
	}
	var data []byte
	if inline != "" {
		data = []byte(inline)
	} else {
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, fmt.Errorf("read artifact node config: %w", err)
		}
		if info.Size() > artifactNodeConfigMaxBytes {
			return nil, errors.New("artifact node config is too large")
		}
		data, err = os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read artifact node config: %w", err)
		}
	}
	applicationBaseURL := strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL"))
	if applicationBaseURL == "" {
		return nil, errors.New("CATSCO_PUBLIC_BASE_URL is required when artifact nodes are configured")
	}
	return parseArtifactNodeRegistry(data, os.LookupEnv, applicationBaseURL)
}

func loadArtifactDirectURLTemplateFromEnv() (*artifactDirectURLTemplate, error) {
	value := os.Getenv("CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE")
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	applicationBaseURL := strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL"))
	if applicationBaseURL == "" {
		return nil, errors.New(
			"CATSCO_PUBLIC_BASE_URL is required when the direct artifact URL template is configured",
		)
	}
	return parseArtifactDirectURLTemplate(value, applicationBaseURL)
}

func parseArtifactDirectURLTemplate(
	value, applicationBaseURL string,
) (*artifactDirectURLTemplate, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return nil, errors.New("invalid direct artifact URL template")
	}
	if strings.Count(value, artifactDirectUIDToken) != 1 {
		return nil, errors.New(
			"direct artifact URL template must contain exactly one {uid}",
		)
	}
	const sampleUID = "123456789"
	sample := strings.Replace(value, artifactDirectUIDToken, sampleUID, 1)
	publicBaseURL, err := parseArtifactNodeBaseURL(sample)
	if err != nil {
		return nil, fmt.Errorf("direct artifact URL template: %w", err)
	}
	parsed, _ := url.Parse(publicBaseURL)
	if parsed.Scheme != "https" {
		return nil, errors.New("direct artifact URL template must use HTTPS")
	}
	if !strings.Contains(parsed.Hostname(), sampleUID) ||
		strings.Contains(parsed.Path, sampleUID) {
		return nil, errors.New(
			"direct artifact URL template must place {uid} in the hostname",
		)
	}
	if !strings.HasSuffix(parsed.Path, "/artifacts") {
		return nil, errors.New(
			"direct artifact URL template must end with /artifacts",
		)
	}
	publicOrigin, err := parseArtifactURLOrigin(publicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("direct artifact URL template: %w", err)
	}
	applicationOrigin, err := parseArtifactURLOrigin(applicationBaseURL)
	if err != nil {
		return nil, fmt.Errorf("CATSCO_PUBLIC_BASE_URL: %w", err)
	}
	if publicOrigin == applicationOrigin {
		return nil, errors.New(
			"direct artifact URL template must use a different origin from CATSCO_PUBLIC_BASE_URL",
		)
	}
	return &artifactDirectURLTemplate{value: value}, nil
}

func (t *artifactDirectURLTemplate) resolve(agentUID int64) (artifactNode, error) {
	if t == nil || agentUID <= 0 {
		return artifactNode{}, errors.New("direct artifact URL template is unavailable")
	}
	agentID := strconv.FormatInt(agentUID, 10)
	publicBaseURL, err := parseArtifactNodeBaseURL(
		strings.Replace(t.value, artifactDirectUIDToken, agentID, 1),
	)
	if err != nil {
		return artifactNode{}, errors.New("direct artifact URL template is invalid")
	}
	return artifactNode{
		id:              "direct-" + agentID,
		publicBaseURL:   publicBaseURL,
		rootPublicIndex: true,
	}, nil
}

func parseArtifactNodeRegistry(
	data []byte,
	lookupEnv func(string) (string, bool),
	applicationBaseURL string,
) (*artifactNodeRegistry, error) {
	if len(data) == 0 || len(data) > artifactNodeConfigMaxBytes {
		return nil, errors.New("artifact node config is empty or too large")
	}
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var document artifactNodeConfigDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode artifact node config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("artifact node config contains trailing data")
	}
	if len(document.Nodes) == 0 || len(document.Agents) == 0 {
		return nil, errors.New("artifact node config requires non-empty nodes and agents")
	}
	applicationOrigin, err := parseArtifactURLOrigin(applicationBaseURL)
	if err != nil {
		return nil, fmt.Errorf("CATSCO_PUBLIC_BASE_URL: %w", err)
	}

	registry := &artifactNodeRegistry{
		nodes:            make(map[string]artifactNode, len(document.Nodes)),
		agents:           make(map[int64]string, len(document.Agents)),
		fallbackToLegacy: document.FallbackToLegacy,
	}
	for rawID, entry := range document.Nodes {
		nodeID := strings.TrimSpace(rawID)
		if nodeID != rawID || !artifactNodeIDPattern.MatchString(nodeID) {
			return nil, fmt.Errorf("invalid artifact node ID %q", rawID)
		}
		publicBaseURL, err := parseArtifactNodeBaseURL(entry.PublicBaseURL)
		if err != nil {
			return nil, fmt.Errorf("artifact node %s public URL: %w", nodeID, err)
		}
		publicOrigin, err := parseArtifactURLOrigin(publicBaseURL)
		if err != nil {
			return nil, fmt.Errorf("artifact node %s public URL: %w", nodeID, err)
		}
		if publicOrigin == applicationOrigin {
			return nil, fmt.Errorf(
				"artifact node %s public URL must use a different origin from CATSCO_PUBLIC_BASE_URL",
				nodeID,
			)
		}
		managementURL := strings.TrimSpace(entry.ManagementURL)
		if managementURL != entry.ManagementURL {
			return nil, fmt.Errorf("artifact node %s management URL is invalid", nodeID)
		}
		hasTokenSource := entry.ManagementTokenEnv != "" || entry.ManagementTokenFile != ""
		var token string
		if managementURL == "" {
			if hasTokenSource {
				return nil, fmt.Errorf("artifact node %s management configuration is incomplete", nodeID)
			}
		} else {
			managementURL, err = parseArtifactNodeManagementURL(managementURL)
			if err != nil {
				return nil, fmt.Errorf("artifact node %s management URL: %w", nodeID, err)
			}
			token, err = resolveArtifactNodeToken(entry, lookupEnv)
			if err != nil {
				return nil, fmt.Errorf("artifact node %s management token is unavailable", nodeID)
			}
		}
		registry.nodes[nodeID] = artifactNode{
			id:              nodeID,
			publicBaseURL:   publicBaseURL,
			managementURL:   managementURL,
			managementToken: token,
		}
	}

	for rawUID, rawNodeID := range document.Agents {
		agentUID, err := strconv.ParseInt(rawUID, 10, 64)
		if err != nil || agentUID <= 0 || rawUID != strconv.FormatInt(agentUID, 10) {
			return nil, fmt.Errorf("invalid artifact agent UID %q", rawUID)
		}
		nodeID := strings.TrimSpace(rawNodeID)
		if nodeID != rawNodeID {
			return nil, fmt.Errorf("invalid artifact node ID %q", rawNodeID)
		}
		if _, exists := registry.agents[agentUID]; exists {
			return nil, fmt.Errorf("duplicate artifact agent UID %d", agentUID)
		}
		if _, ok := registry.nodes[nodeID]; !ok {
			return nil, fmt.Errorf("artifact agent %d references unknown node %q", agentUID, nodeID)
		}
		registry.agents[agentUID] = nodeID
	}
	return registry, nil
}

func resolveArtifactNodeToken(entry artifactNodeConfigEntry, lookupEnv func(string) (string, bool)) (string, error) {
	tokenEnv := strings.TrimSpace(entry.ManagementTokenEnv)
	tokenFile := strings.TrimSpace(entry.ManagementTokenFile)
	if tokenEnv != entry.ManagementTokenEnv || tokenFile != entry.ManagementTokenFile {
		return "", errors.New("artifact node token source is invalid")
	}
	if (tokenEnv == "") == (tokenFile == "") {
		return "", errors.New("configure exactly one artifact node token source")
	}

	var token string
	if tokenEnv != "" {
		if !artifactNodeTokenEnvPattern.MatchString(tokenEnv) {
			return "", errors.New("invalid artifact node token environment variable")
		}
		value, ok := lookupEnv(tokenEnv)
		if !ok {
			return "", errors.New("artifact node token environment variable is unavailable")
		}
		token = strings.TrimSpace(value)
	} else {
		info, err := os.Stat(tokenFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() > artifactNodeTokenMaxBytes {
			return "", errors.New("artifact node token file is unavailable")
		}
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", errors.New("artifact node token file is unavailable")
		}
		token = strings.TrimSpace(string(data))
	}
	if len(token) < 32 || len(token) > artifactNodeTokenMaxBytes {
		return "", errors.New("artifact node token is invalid")
	}
	return token, nil
}

func (r *artifactNodeRegistry) resolve(agentUID int64) (artifactNode, error) {
	if r == nil || agentUID <= 0 {
		return artifactNode{}, errors.New("artifact node is unavailable")
	}
	nodeID, ok := r.agents[agentUID]
	if !ok {
		return artifactNode{}, fmt.Errorf("artifact agent %d has no configured node", agentUID)
	}
	node, ok := r.nodes[nodeID]
	if !ok {
		return artifactNode{}, errors.New("artifact node is unavailable")
	}
	return node, nil
}

func parseArtifactNodeBaseURL(value string) (string, error) {
	return parseCanonicalArtifactNodeURL(value, false)
}

func parseArtifactNodeManagementURL(value string) (string, error) {
	parsedValue, err := parseCanonicalArtifactNodeURL(value, true)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(parsedValue)
	if !strings.HasSuffix(parsed.Path, "/artifacts") {
		return "", errors.New("artifact management URL must end with /artifacts")
	}
	return parsed.String(), nil
}

type artifactURLOrigin struct {
	scheme   string
	hostname string
	port     string
}

func parseArtifactURLOrigin(value string) (artifactURLOrigin, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil {
		return artifactURLOrigin{}, errors.New("must be an absolute HTTP(S) URL")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return artifactURLOrigin{
		scheme:   strings.ToLower(parsed.Scheme),
		hostname: strings.TrimSuffix(strings.ToLower(parsed.Hostname()), "."),
		port:     port,
	}, nil
}

func parseCanonicalArtifactNodeURL(value string, management bool) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" || raw != value {
		return "", errors.New("invalid artifact node URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", errors.New("invalid artifact node URL")
	}
	trimmedPath := strings.TrimRight(parsed.Path, "/")
	cleanPath := path.Clean("/" + strings.TrimPrefix(trimmedPath, "/"))
	if cleanPath == "/" {
		cleanPath = ""
	}
	if trimmedPath != cleanPath {
		return "", errors.New("artifact node URL path must be canonical")
	}
	if management && cleanPath == "" {
		return "", errors.New("invalid artifact management URL")
	}
	parsed.Path = cleanPath
	return parsed.String(), nil
}

func validateArtifactNodeURL(value, publicBaseURL string, expectedAgentUID int64) error {
	if strings.TrimSpace(publicBaseURL) == "" {
		return nil
	}
	artifactURL, err := url.Parse(strings.TrimSpace(value))
	if err != nil || artifactURL.User != nil || artifactURL.RawQuery != "" || artifactURL.RawPath != "" {
		return errors.New("invalid artifact URL")
	}
	baseURL, err := url.Parse(publicBaseURL)
	if err != nil {
		return errors.New("invalid artifact node URL")
	}
	if !strings.EqualFold(artifactURL.Scheme, baseURL.Scheme) || !strings.EqualFold(artifactURL.Host, baseURL.Host) {
		return errors.New("artifact URL does not belong to the configured node")
	}
	basePath := strings.TrimRight(path.Clean("/"+strings.TrimPrefix(baseURL.Path, "/")), "/")
	artifactPath := path.Clean("/" + strings.TrimPrefix(artifactURL.Path, "/"))
	if basePath != "" && artifactPath != basePath && !strings.HasPrefix(artifactPath, basePath+"/") {
		return errors.New("artifact URL does not belong to the configured node")
	}
	if expectedAgentUID > 0 {
		agentPath := path.Join("/", basePath, "by-agent", strconv.FormatInt(expectedAgentUID, 10))
		if !strings.HasPrefix(artifactPath, agentPath+"/") {
			return errors.New("artifact URL does not belong to the configured agent")
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("artifact node config contains trailing data")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode artifact node config: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode artifact node config: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("artifact node config contains an invalid object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("artifact node config contains duplicate key %q at %s", key, location)
			}
			seen[key] = struct{}{}
			if err := consumeUniqueJSONValue(decoder, location+"."+key); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode artifact node config: %w", err)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
			index++
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode artifact node config: %w", err)
		}
	default:
		return errors.New("artifact node config contains an invalid delimiter")
	}
	return nil
}
