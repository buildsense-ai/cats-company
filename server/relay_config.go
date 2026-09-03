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
	BaseURL      string          `json:"base_url"`
	DefaultModel string          `json:"default_model"`
	Endpoints    []relayEndpoint `json:"endpoints"`
	// Models is the cloud-authoritative public catalog consumed by desktop
	// clients. Runtime metadata includes the provider, model name, context
	// window, protocol mode, and capabilities without exposing credentials.
	Models             []botModelCatalogItem `json:"models,omitempty"`
	KeyHint            string                `json:"key_hint"`
	DocsURL            string                `json:"docs_url,omitempty"`
	SelfServiceEnabled bool                  `json:"self_service_enabled"`
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
	keyHint := "可以在这里生成、显示并复制自己的中转 Key。泄露后请立即重新生成或撤销。"
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
		Models:             relayPublicModelCatalog(),
		KeyHint:            keyHint,
		DocsURL:            relayEnv("CATS_RELAY_DOCS_URL", baseURL),
		SelfServiceEnabled: selfServiceEnabled,
	})
}

func relayPublicModelCatalog() []botModelCatalogItem {
	catalog := make([]botModelCatalogItem, len(botModelCatalog))
	copy(catalog, botModelCatalog)
	for i := range catalog {
		catalog[i].Available = true
		catalog[i].Runtime = catalogRuntimeDescriptor(catalog[i])
	}
	return catalog
}
