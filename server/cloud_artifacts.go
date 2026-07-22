package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	artifactIndexContract   = "cloud-artifacts.index.v1"
	defaultArtifactIndexURL = "https://logs.catsco.fun:9000/artifacts/artifacts-index.json"
	artifactIndexMaxBytes   = 1 << 20
	artifactIndexTimeout    = 10 * time.Second
)

var artifactIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._-]*[a-z0-9])?$`)

// CloudArtifactHandler reads the public index maintained by the publishing Skill.
type CloudArtifactHandler struct {
	indexURL   string
	httpClient *http.Client
	configErr  error
}

type cloudArtifactIndex struct {
	ContractVersion string          `json:"contract_version"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
	Artifacts       []cloudArtifact `json:"artifacts"`
}

type cloudArtifact struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	UpdatedAt string `json:"updated_at"`
}

// NewCloudArtifactHandler builds a read-only proxy for a fixed artifact index URL.
func NewCloudArtifactHandler(indexURL string, client *http.Client) *CloudArtifactHandler {
	h := &CloudArtifactHandler{httpClient: client}
	if h.httpClient == nil {
		h.httpClient = &http.Client{Timeout: artifactIndexTimeout}
	}

	parsed, err := url.Parse(strings.TrimSpace(indexURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		h.configErr = fmt.Errorf("invalid CATSCO_ARTIFACT_INDEX_URL")
		return h
	}
	parsed.Fragment = ""
	h.indexURL = parsed.String()
	return h
}

// NewCloudArtifactHandlerFromEnv uses the configured index, or the current CatsCo artifact host.
func NewCloudArtifactHandlerFromEnv() *CloudArtifactHandler {
	indexURL := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_INDEX_URL"))
	if indexURL == "" {
		indexURL = defaultArtifactIndexURL
	}
	return NewCloudArtifactHandler(indexURL, nil)
}

// HandleList serves GET /api/artifacts for authenticated CatsCo users.
func (h *CloudArtifactHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if UIDFromContext(r.Context()) <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.configErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "artifact index is not configured"})
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.indexURL, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build artifact index request"})
		return
	}
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("User-Agent", "catsco-cloud-artifacts/1.0")

	resp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "artifact index is unavailable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "artifact index is unavailable"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, artifactIndexMaxBytes+1))
	if err != nil || len(body) > artifactIndexMaxBytes {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "artifact index response is invalid"})
		return
	}

	var index cloudArtifactIndex
	if err := json.Unmarshal(body, &index); err != nil || validateCloudArtifactIndex(index) != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "artifact index response is invalid"})
		return
	}
	if index.Artifacts == nil {
		index.Artifacts = []cloudArtifact{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, index)
}

func validateCloudArtifactIndex(index cloudArtifactIndex) error {
	if index.ContractVersion != artifactIndexContract {
		return errors.New("unsupported artifact index contract")
	}
	seen := make(map[string]struct{}, len(index.Artifacts))
	for _, artifact := range index.Artifacts {
		artifactURL, err := url.Parse(strings.TrimSpace(artifact.URL))
		if err != nil || artifactURL.Host == "" || (artifactURL.Scheme != "http" && artifactURL.Scheme != "https") {
			return errors.New("invalid artifact URL")
		}
		if !artifactIDPattern.MatchString(artifact.ID) || strings.TrimSpace(artifact.Title) == "" {
			return errors.New("invalid artifact identity")
		}
		if artifact.Kind != "html" && artifact.Kind != "mini_app" {
			return errors.New("invalid artifact kind")
		}
		if _, err := time.Parse(time.RFC3339, artifact.UpdatedAt); err != nil {
			return errors.New("invalid artifact timestamp")
		}
		if _, exists := seen[artifact.ID]; exists {
			return errors.New("duplicate artifact ID")
		}
		seen[artifact.ID] = struct{}{}
	}
	return nil
}
