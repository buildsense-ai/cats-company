package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const artifactOriginSecretHeader = "X-Catsco-Artifact-Origin-Secret"

type artifactFileAccess interface {
	CanReadResource(context.Context, int64, string, int64) bool
	CanUseResource(context.Context, int64, string) bool
}

// Ownership may be recorded only at a verified creation boundary, never on reads.
type artifactUploadRecorder interface {
	RecordUpload(context.Context, int64, string) error
}

// SetFileAccess enables message-based artifact reads and publish-source checks.
// Call this before registering the handler. A missing service fails closed.
func (h *CloudArtifactHandler) SetFileAccess(service *FileAccessService) {
	if h != nil {
		if service == nil {
			h.fileAccess = nil
		} else {
			h.fileAccess = service
		}
	}
}

// ArtifactResourceFromPath binds an escaped resource path to its configured
// origin and namespace using the same canonical key as message ingestion.
func ArtifactResourceFromPath(origin, escapedPath string) (string, bool) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		!strings.HasPrefix(escapedPath, "/") || strings.ContainsAny(escapedPath, "?#") {
		return "", false
	}
	resource := canonicalArtifactResource(origin + escapedPath)
	return resource, resource != ""
}

// HandleFileAuthorization is a worker callback behind ArtifactMiddleware, which
// verifies resource-scoped media sessions or ordinary human/bot authentication.
// The secret authenticates the worker; it never grants an actor file access.
func (h *CloudArtifactHandler) HandleFileAuthorization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Cookie, Authorization")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.fileAccess == nil || len(h.originSecret) < 32 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(artifactOriginSecretHeader)), []byte(h.originSecret)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	actorUID := UIDFromContext(r.Context())
	if actorUID <= 0 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	resource, ok := ArtifactResourceFromPath(r.URL.Query().Get("origin"), r.URL.Query().Get("path"))
	messageID := int64(0)
	if value := r.URL.Query().Get("message_id"); value != "" {
		var err error
		messageID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || messageID <= 0 {
			ok = false
		}
	}
	if !ok || !h.fileAccess.CanReadResource(r.Context(), actorUID, resource, messageID) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CloudArtifactHandler) readableArtifacts(r *http.Request, artifacts []cloudArtifact) []cloudArtifact {
	visible := make([]cloudArtifact, 0, len(artifacts))
	actorUID := UIDFromContext(r.Context())
	if h == nil || h.fileAccess == nil || actorUID <= 0 {
		return visible
	}
	for _, artifact := range artifacts {
		resource := canonicalArtifactResource(artifact.URL)
		if resource != "" && h.fileAccess.CanReadResource(r.Context(), actorUID, resource, 0) {
			visible = append(visible, artifact)
		}
	}
	return visible
}

// Never follow redirects from a configured artifact service. Even same-host
// redirects can change the intended endpoint; cross-origin redirects can leak
// management credentials or return untrusted content under a private URL.
func artifactHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: artifactUpstreamTimeout}
	}
	copy := *client
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

// Legacy public bases come only from the configured index, never a caller URL
// or an upstream operation. A node registry supplies its explicit public base.
func (h *CloudArtifactHandler) trustedArtifactPublicBase(publicBase string) (string, error) {
	if publicBase == "" {
		index, err := url.Parse(h.indexURL)
		if err != nil || !strings.HasSuffix(index.Path, "/artifacts-index.json") {
			return "", errors.New("artifact public base unavailable")
		}
		index.Path = strings.TrimSuffix(index.Path, "/artifacts-index.json")
		index.RawQuery, index.Fragment = "", ""
		publicBase = index.String()
	}
	base, err := parseCanonicalArtifactNodeURL(publicBase, false)
	if err != nil || !strings.HasPrefix(base, "https://") {
		return "", errors.New("artifact public base unavailable")
	}
	return base, nil
}

// Read current, protected metadata and authorize before sending any mutation.
// Agent ownership and metadata uploader fields never replace file permission.
func (h *CloudArtifactHandler) authorizeArtifactMutation(r *http.Request, uid int64, artifactID, suffix, collectionURL, token, publicBase string, agentUID int64) error {
	if h == nil || h.fileAccess == nil {
		return &artifactUpstreamError{status: http.StatusServiceUnavailable, code: "artifact_file_access_unavailable"}
	}
	publicBase, err := h.trustedArtifactPublicBase(publicBase)
	if err != nil {
		return &artifactUpstreamError{status: http.StatusServiceUnavailable, code: "artifact_management_unavailable"}
	}
	status := "active"
	if suffix == "/restore" {
		status = "deleted"
	}
	body, err := h.requestManagement(r, http.MethodGet, collectionURL+"?status="+status, nil, token)
	if err != nil {
		return err
	}
	var list cloudArtifactManagementList
	if json.Unmarshal(body, &list) != nil || validateManagedArtifactList(list, status) != nil {
		return &artifactUpstreamError{status: http.StatusBadGateway, code: "artifact_response_invalid"}
	}
	for _, artifact := range list.Artifacts {
		if artifact.ID != artifactID {
			continue
		}
		resource := canonicalArtifactResource(artifact.URL)
		if resource == "" || !strings.HasSuffix(resource, "/"+artifactID) ||
			(agentUID > 0 && artifact.AgentUID != strconv.FormatInt(agentUID, 10)) ||
			validateArtifactNodeURL(artifact.URL, publicBase, agentUID) != nil {
			return &artifactUpstreamError{status: http.StatusBadGateway, code: "artifact_response_invalid"}
		}
		if h.fileAccess.CanUseResource(r.Context(), uid, resource) {
			return nil
		}
		break
	}
	return &artifactUpstreamError{status: http.StatusNotFound, code: "artifact_not_found"}
}
