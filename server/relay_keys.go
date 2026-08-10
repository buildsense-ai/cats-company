package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultRelayAdminTimeout = 15 * time.Second

type RelayKeyHandler struct {
	admin                     *RelayAdminClient
	deviceModelStatusResolver func(uid int64) (DeviceModelStatus, bool)
}

type RelayAdminClient struct {
	baseURL string
	token   string
	client  *http.Client
}

type relayAdminError struct {
	status  int
	message string
}

func (e relayAdminError) Error() string {
	return e.message
}

type relayKeyRequest struct {
	Name string `json:"name,omitempty"`
}

type relayKeyProxyRequest struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
}

type relayKeyResponse struct {
	Configured bool          `json:"configured"`
	Key        *relayKeyInfo `json:"key,omitempty"`
}

type relayKeyInfo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Prefix    string  `json:"prefix,omitempty"`
	State     string  `json:"state"`
	Key       string  `json:"key,omitempty"`
	CreatedAt string  `json:"created_at,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
	RevokedAt *string `json:"revoked_at,omitempty"`
}

type relayUsageResponse struct {
	Configured bool               `json:"configured"`
	Summary    *relayUsageSummary `json:"summary,omitempty"`
}

type relayUsageSummary struct {
	Source           string  `json:"source,omitempty"`
	Model            string  `json:"model"`
	Provider         string  `json:"provider,omitempty"`
	QuotaConfigured  bool    `json:"quota_configured"`
	Percent          float64 `json:"percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	Status           string  `json:"status"`
	ResetDuration    string  `json:"reset_duration,omitempty"`
	LastReset        string  `json:"last_reset,omitempty"`
	UsedCNY          float64 `json:"-"`
	LimitCNY         float64 `json:"-"`
	RemainingCNY     float64 `json:"-"`
}

func NewRelayKeyHandlerFromEnv() *RelayKeyHandler {
	return &RelayKeyHandler{admin: NewRelayAdminClientFromEnv()}
}

func (h *RelayKeyHandler) SetDeviceModelStatusResolver(resolver func(uid int64) (DeviceModelStatus, bool)) {
	if h == nil {
		return
	}
	h.deviceModelStatusResolver = resolver
}

func NewRelayAdminClientFromEnv() *RelayAdminClient {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_URL")), "/")
	token := strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_TOKEN"))
	if baseURL == "" || token == "" {
		return nil
	}
	timeout := defaultRelayAdminTimeout
	if raw := strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return &RelayAdminClient{
		baseURL: baseURL,
		token:   token,
		client:  &http.Client{Timeout: timeout},
	}
}

func RelaySelfServiceEnabled() bool {
	return strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_URL")) != "" &&
		strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_TOKEN")) != ""
}

func (h *RelayKeyHandler) HandleKey(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "relay self-service is not configured"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.forward(w, r, http.MethodGet, uid, "", nil)
	case http.MethodPost:
		var req relayKeyRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		h.forward(w, r, http.MethodPost, uid, "", relayKeyProxyRequest{
			Name:     strings.TrimSpace(req.Name),
			Username: UsernameFromContext(r.Context()),
		})
	case http.MethodDelete:
		h.forward(w, r, http.MethodDelete, uid, "", nil)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *RelayKeyHandler) HandleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h.admin == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "relay self-service is not configured"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	h.forward(w, r, http.MethodPost, uid, "/rotate", nil)
}

func (h *RelayKeyHandler) HandleReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h.admin == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "relay self-service is not configured"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	h.forward(w, r, http.MethodPost, uid, "/reveal", nil)
}

func (h *RelayKeyHandler) HandleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if source == "" && model == "" && h.deviceModelStatusResolver != nil {
		if status, ok := h.deviceModelStatusResolver(uid); ok {
			source = strings.ToLower(strings.TrimSpace(status.Source))
			model = strings.TrimSpace(status.Model)
		}
	}
	if source == "custom" || normalizeRelayModelName(model) == "custom" || strings.EqualFold(model, "自定义模型") {
		customModel := model
		if customModel == "" || normalizeRelayModelName(customModel) == "custom" || strings.EqualFold(customModel, "自定义模型") {
			customModel = "自定义模型"
		}
		writeJSON(w, http.StatusOK, relayUsageResponse{
			Configured: true,
			Summary: &relayUsageSummary{
				Source: "custom",
				Model:  customModel,
				Status: "custom",
			},
		})
		return
	}
	if h.admin == nil {
		writeJSON(w, http.StatusOK, relayUsageResponse{Configured: false})
		return
	}
	user, err := fetchRelayUsageForUID(r.Context(), h.admin, uid)
	if err != nil {
		status := http.StatusBadGateway
		if relayErr, ok := err.(relayAdminError); ok {
			if relayErr.status >= 400 && relayErr.status < 500 {
				status = relayErr.status
			}
		}
		writeJSON(w, status, map[string]string{"error": "relay admin request failed"})
		return
	}
	if model == "" {
		model = relayEnv("CATS_RELAY_DEFAULT_MODEL", "MiniMax-M2.7")
	}
	writeJSON(w, http.StatusOK, buildRelayUsageResponse(user, model))
}

func (h *RelayKeyHandler) forward(w http.ResponseWriter, r *http.Request, method string, uid int64, suffix string, body interface{}) {
	var out relayKeyResponse
	err := h.admin.Do(r.Context(), method, fmt.Sprintf("/internal/users/%d/key%s", uid, suffix), body, &out)
	if err != nil {
		status := http.StatusBadGateway
		if relayErr, ok := err.(relayAdminError); ok {
			if relayErr.status >= 400 && relayErr.status < 500 {
				status = relayErr.status
			} else if relayErr.status == http.StatusServiceUnavailable {
				status = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, status, map[string]string{"error": "relay admin request failed"})
		return
	}
	if method == http.MethodGet || method == http.MethodDelete {
		stripRelayPlaintext(&out)
	}
	writeJSON(w, http.StatusOK, out)
}

func stripRelayPlaintext(out *relayKeyResponse) {
	if out != nil && out.Key != nil {
		out.Key.Key = ""
	}
}

func fetchRelayUsageForUID(ctx context.Context, admin *RelayAdminClient, uid int64) (*commercialRelayUsageUser, error) {
	if admin == nil {
		return nil, nil
	}
	var out commercialRelayUsageResponse
	err := admin.Do(ctx, http.MethodGet, fmt.Sprintf("/internal/usage/users?search=%d&limit=1&include_governance=1", uid), nil, &out)
	if err != nil {
		return nil, err
	}
	for i := range out.Users {
		if out.Users[i].UID == uid {
			return &out.Users[i], nil
		}
	}
	return nil, nil
}

func fetchRelayLimitsForUID(ctx context.Context, admin *RelayAdminClient, uid int64) (*commercialRelayUsageUser, error) {
	if admin == nil {
		return nil, nil
	}
	var out commercialRelayUsageUser
	if err := admin.Do(ctx, http.MethodGet, fmt.Sprintf("/internal/users/%d/key/limits", uid), nil, &out); err != nil {
		return nil, err
	}
	out.UID = uid
	return &out, nil
}

func buildRelayUsageResponse(user *commercialRelayUsageUser, preferredModel string) relayUsageResponse {
	if user == nil || !user.Configured {
		return relayUsageResponse{Configured: false}
	}
	limit, ok := findRelayUsageModel(user.Limits.ModelLimits, preferredModel)
	if !ok {
		if strings.TrimSpace(preferredModel) != "" {
			return relayUsageResponse{Configured: true}
		}
		limit, ok = pickRelayUsageModel(user.Limits.ModelLimits)
		if !ok {
			return relayUsageResponse{Configured: true}
		}
	}
	used := limit.Budget.CurrentUsage
	maxLimit := limit.Budget.MaxLimit
	percent := 0.0
	remainingPercent := 0.0
	status := "normal"
	if maxLimit > 0 {
		percent = used / maxLimit * 100
		remainingPercent = math.Max(0, 100-percent)
		if used > maxLimit+0.000001 {
			status = "over_limit"
		} else if percent >= 90 {
			status = "high"
		}
	}
	return relayUsageResponse{
		Configured: true,
		Summary: &relayUsageSummary{
			Source:           "relay",
			Model:            limit.Model,
			Provider:         limit.Provider,
			QuotaConfigured:  maxLimit > 0,
			Percent:          percent,
			RemainingPercent: remainingPercent,
			UsedCNY:          used,
			LimitCNY:         maxLimit,
			RemainingCNY:     math.Max(0, maxLimit-used),
			Status:           status,
			ResetDuration:    limit.Budget.ResetDuration,
			LastReset:        limit.Budget.LastReset,
		},
	}
}

func findRelayUsageModel(limits []commercialRelayModelLimit, model string) (commercialRelayModelLimit, bool) {
	target := normalizeRelayModelName(model)
	if target == "" {
		return commercialRelayModelLimit{}, false
	}
	for _, limit := range limits {
		if limit.Budget.MaxLimit <= 0 {
			continue
		}
		if normalizeRelayModelName(limit.Model) == target {
			return limit, true
		}
		for _, allowed := range limit.AllowedModels {
			if normalizeRelayModelName(allowed) == target {
				return limit, true
			}
		}
	}
	return commercialRelayModelLimit{}, false
}

func normalizeRelayModelName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func pickRelayUsageModel(limits []commercialRelayModelLimit) (commercialRelayModelLimit, bool) {
	var best commercialRelayModelLimit
	found := false
	bestScore := -1.0
	for _, limit := range limits {
		model := strings.TrimSpace(limit.Model)
		if model == "" || model == "*" || limit.Budget.MaxLimit <= 0 {
			continue
		}
		score := limit.Budget.CurrentUsage / limit.Budget.MaxLimit
		if !found || score > bestScore {
			best = limit
			bestScore = score
			found = true
		}
	}
	return best, found
}

func (c *RelayAdminClient) Do(ctx context.Context, method string, path string, body interface{}, out interface{}) error {
	if c == nil || c.baseURL == "" || c.token == "" {
		return relayAdminError{status: http.StatusServiceUnavailable, message: "relay admin is not configured"}
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := "relay admin request failed"
		var parsed map[string]string
		if len(data) > 0 && json.Unmarshal(data, &parsed) == nil && parsed["error"] != "" {
			message = parsed["error"]
		}
		return relayAdminError{status: resp.StatusCode, message: message}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
