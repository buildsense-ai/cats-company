package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultRelayAdminTimeout = 15 * time.Second

type RelayKeyHandler struct {
	admin *RelayAdminClient
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

func NewRelayKeyHandlerFromEnv() *RelayKeyHandler {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_URL")), "/")
	token := strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_TOKEN"))
	if baseURL == "" || token == "" {
		return &RelayKeyHandler{}
	}
	timeout := defaultRelayAdminTimeout
	if raw := strings.TrimSpace(os.Getenv("CATS_RELAY_ADMIN_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return &RelayKeyHandler{
		admin: &RelayAdminClient{
			baseURL: baseURL,
			token:   token,
			client:  &http.Client{Timeout: timeout},
		},
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

func (h *RelayKeyHandler) forward(w http.ResponseWriter, r *http.Request, method string, uid int64, suffix string, body interface{}) {
	var out relayKeyResponse
	err := h.admin.Do(r.Context(), method, fmt.Sprintf("/internal/users/%d/key%s", uid, suffix), body, &out)
	if err != nil {
		status := http.StatusBadGateway
		message := "relay admin request failed"
		if relayErr, ok := err.(relayAdminError); ok {
			message = relayErr.message
			if relayErr.status >= 400 && relayErr.status < 500 {
				status = relayErr.status
			} else if relayErr.status == http.StatusServiceUnavailable {
				status = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, status, map[string]string{"error": message})
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
