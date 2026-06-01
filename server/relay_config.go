package server

import (
	"net/http"
	"os"
	"strings"
)

const defaultRelayBaseURL = "https://relay.catsco.cc"

type RelayConfigHandler struct{}

type relayEndpoint struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
}

type relayConfigResponse struct {
	BaseURL            string          `json:"base_url"`
	DefaultModel       string          `json:"default_model"`
	Endpoints          []relayEndpoint `json:"endpoints"`
	KeyHint            string          `json:"key_hint"`
	DocsURL            string          `json:"docs_url,omitempty"`
	SelfServiceEnabled bool            `json:"self_service_enabled"`
}

func NewRelayConfigHandler() *RelayConfigHandler {
	return &RelayConfigHandler{}
}

func relayEnv(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return strings.TrimRight(value, "/")
}

func relayBaseURL() string {
	return relayEnv("CATS_RELAY_PUBLIC_BASE_URL", defaultRelayBaseURL)
}

func (h *RelayConfigHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	baseURL := relayBaseURL()
	openAIBaseURL := relayEnv("CATS_RELAY_OPENAI_BASE_URL", baseURL+"/v1")
	anthropicBaseURL := relayEnv("CATS_RELAY_ANTHROPIC_BASE_URL", baseURL+"/anthropic")

	selfServiceEnabled := RelaySelfServiceEnabled()
	keyHint := "可以在这里生成自己的中转 Key。明文只展示一次，泄露后可撤销并重新生成。"
	if !selfServiceEnabled {
		keyHint = "访问凭证由 CatsCo 管理员发放。请妥善保存，泄露后可联系管理员撤销并重建。"
	}

	writeJSON(w, http.StatusOK, relayConfigResponse{
		BaseURL:      baseURL,
		DefaultModel: relayEnv("CATS_RELAY_DEFAULT_MODEL", "MiniMax-M2.7"),
		Endpoints: []relayEndpoint{
			{Protocol: "OpenAI-compatible", BaseURL: openAIBaseURL},
			{Protocol: "Anthropic-compatible", BaseURL: anthropicBaseURL},
		},
		KeyHint:            keyHint,
		DocsURL:            relayEnv("CATS_RELAY_DOCS_URL", baseURL),
		SelfServiceEnabled: selfServiceEnabled,
	})
}
