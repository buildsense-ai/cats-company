package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// ArtifactLaunchHandler issues a one-time Artifact code for the signed-in user,
// so an application opened from CatsCompany can identify the visitor and the
// conversation it came from. It mirrors the existing Artifact upstream pattern:
// environment configuration, a bounded timeout, a bounded response and a
// whitelisted error mapping. The gateway holds the pseudonym secret, so nothing
// secret is returned to the browser beyond the one-time code.
const (
	artifactLaunchPath        = "/_gateway/codes"
	artifactLaunchUserAgent   = "catsco-artifact-launch/1.0"
	artifactLaunchMaxBody     = 4 << 10
	artifactLaunchMaxTopicLen = 128
)

var artifactLaunchAppPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

type ArtifactLaunchHandler struct {
	gatewayURL   string
	controlToken string
	httpClient   *http.Client
	configErr    error
}

type artifactLaunchRequest struct {
	App     string `json:"app"`
	TopicID string `json:"topic_id"`
}

// ArtifactLaunchResult is what the browser receives. It carries the one-time
// code, so the client only has to follow LaunchURL.
type ArtifactLaunchResult struct {
	AppID     string `json:"app_id"`
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	LaunchURL string `json:"launch_url"`
}

func NewArtifactLaunchHandlerFromEnv() *ArtifactLaunchHandler {
	h := &ArtifactLaunchHandler{
		gatewayURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_GATEWAY_URL")), "/"),
		controlToken: strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_GATEWAY_TOKEN")),
		httpClient:   &http.Client{Timeout: artifactUpstreamTimeout},
	}
	if h.gatewayURL == "" || h.controlToken == "" {
		h.configErr = errors.New("artifact gateway is not configured")
		return h
	}
	parsed, err := url.Parse(h.gatewayURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" {
		h.configErr = errors.New("invalid CATSCO_ARTIFACT_GATEWAY_URL")
	}
	return h
}

// Enabled reports whether the handler can serve requests, so callers can decide
// between returning 503 and registering the route at all.
func (h *ArtifactLaunchHandler) Enabled() bool {
	return h != nil && h.configErr == nil
}

// HandleLaunch turns the current login session into a one-time Artifact code.
// The owner of the code is always the authenticated caller: the request body
// cannot name a user, and the conversation id is only a hint for the
// application.
func (h *ArtifactLaunchHandler) HandleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if !h.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "artifact_gateway_unavailable"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, artifactLaunchMaxBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_request_invalid"})
		return
	}
	if len(body) > artifactLaunchMaxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "artifact_request_too_large"})
		return
	}

	var request artifactLaunchRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_request_invalid"})
			return
		}
	}

	app := strings.TrimSpace(request.App)
	if !artifactLaunchAppPattern.MatchString(app) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_app_invalid"})
		return
	}
	topic := strings.TrimSpace(request.TopicID)
	if len(topic) > artifactLaunchMaxTopicLen || strings.ContainsAny(topic, "\r\n\x00") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_topic_invalid"})
		return
	}

	result, status, code := h.requestCode(r.Context(), app, uid, UsernameFromContext(r.Context()), r.Host, topic)
	if code != "" {
		writeJSON(w, status, map[string]string{"error": code})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// requestCode asks the gateway for a one-time code. username anchors the code
// to a platform account, and host is a hint the gateway matches against its own
// allow-list before falling back; both are forwarded verbatim.
func (h *ArtifactLaunchHandler) requestCode(ctx context.Context, app string, uid int64, username, host, topic string) (ArtifactLaunchResult, int, string) {
	var empty ArtifactLaunchResult
	payload, err := json.Marshal(map[string]any{
		"app":      app,
		"uid":      fmt.Sprint(uid),
		"topic":    topic,
		"username": username,
		"host":     host,
	})
	if err != nil {
		return empty, http.StatusInternalServerError, "artifact_request_invalid"
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.gatewayURL+artifactLaunchPath, strings.NewReader(string(payload)))
	if err != nil {
		return empty, http.StatusBadGateway, "artifact_gateway_unavailable"
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+h.controlToken)
	request.Header.Set("User-Agent", artifactLaunchUserAgent)

	response, err := h.httpClient.Do(request)
	if err != nil {
		return empty, http.StatusBadGateway, "artifact_gateway_unavailable"
	}
	defer response.Body.Close()
	responseBody, readErr := readArtifactResponse(response.Body)
	if readErr != nil {
		return empty, http.StatusBadGateway, "artifact_gateway_unavailable"
	}
	switch {
	case response.StatusCode == http.StatusNotFound:
		return empty, http.StatusNotFound, "artifact_app_not_found"
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		// Our own credential with the gateway is wrong; do not pass this on as
		// a client authentication problem.
		return empty, http.StatusBadGateway, "artifact_gateway_unauthorized"
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return empty, http.StatusBadGateway, "artifact_gateway_unavailable"
	}

	var upstream struct {
		AppID     string `json:"app_id"`
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
		LaunchURL string `json:"launch_url"`
	}
	if err := json.Unmarshal(responseBody, &upstream); err != nil || upstream.Code == "" {
		return empty, http.StatusBadGateway, "artifact_gateway_unavailable"
	}
	if !launchURLBelongsToGateway(upstream.LaunchURL, h.gatewayURL) {
		return empty, http.StatusBadGateway, "artifact_gateway_unavailable"
	}
	return ArtifactLaunchResult{
		AppID:     upstream.AppID,
		Code:      upstream.Code,
		ExpiresAt: upstream.ExpiresAt,
		LaunchURL: upstream.LaunchURL,
	}, http.StatusOK, ""
}

// launchURLBelongsToGateway requires the launch URL to be the same origin as the
// gateway. A prefix comparison is not enough: `https://gateway.example.evil.com/`
// shares the gateway's prefix while pointing somewhere else entirely.
func launchURLBelongsToGateway(launchURL, gatewayURL string) bool {
	launch, err := url.Parse(launchURL)
	if err != nil || launch.Host == "" {
		return false
	}
	gateway, err := url.Parse(gatewayURL)
	if err != nil || gateway.Host == "" {
		return false
	}
	return launch.Scheme == gateway.Scheme && strings.EqualFold(launch.Host, gateway.Host)
}
