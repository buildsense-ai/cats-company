package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const defaultBotModelID = "minimax-m3"

type botModelOwnershipStore interface {
	GetBotOwner(botUID int64) (int64, error)
}

type BotModelConfigHandler struct {
	owners botModelOwnershipStore
	models store.BotModelConfigStore
}

type botModelCatalogItem struct {
	ID                     string   `json:"id"`
	Label                  string   `json:"label"`
	Description            string   `json:"description"`
	Provider               string   `json:"provider"`
	Protocol               string   `json:"protocol"`
	ContextWindowTokens    int64    `json:"context_window_tokens"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

var botModelCatalog = []botModelCatalogItem{
	{
		ID: "minimax-m2.7", Label: "MiniMax M2.7", Description: "标准额度，适合日常任务",
		Provider: "anthropic", Protocol: "Anthropic SDK", ContextWindowTokens: 204800,
	},
	{
		ID: "minimax-m3", Label: "MiniMax M3", Description: "支持多模态与长上下文",
		Provider: "anthropic", Protocol: "Anthropic SDK", ContextWindowTokens: 1000000,
	},
	{
		ID: "deepseek-v4-flash", Label: "DeepSeek V4 Flash", Description: "低额度 Flash，支持推理强度",
		Provider: "anthropic", Protocol: "Anthropic SDK", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"high", "max", "disabled"}, DefaultReasoningEffort: "high",
	},
	{
		ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", Description: "OpenAI Responses，支持精细推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium",
	},
	{
		ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", Description: "OpenAI Responses，支持精细推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium",
	},
	{
		ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", Description: "OpenAI Responses，支持精细推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium",
	},
}

type botModelUpdateRequest struct {
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type botModelAckRequest struct {
	Revision        int64  `json:"revision"`
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Error           string `json:"error,omitempty"`
}

func NewBotModelConfigHandler(owners botModelOwnershipStore, models store.BotModelConfigStore) *BotModelConfigHandler {
	return &BotModelConfigHandler{owners: owners, models: models}
}

// HandleOwnerConfig manages the cloud model selected by a bot owner.
func (h *BotModelConfigHandler) HandleOwnerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID := UIDFromContext(r.Context())
	botUID, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	if ownerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}
	actualOwner, err := h.owners.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}
	if h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot model configuration is unavailable"})
		return
	}

	storedConfig, err := h.models.GetBotModelConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot model configuration"})
		return
	}
	configured := storedConfig != nil && strings.TrimSpace(storedConfig.ModelID) != ""
	config := botModelConfigWithDefaults(storedConfig)
	if r.Method == http.MethodPatch {
		var req botModelUpdateRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if strings.EqualFold(strings.TrimSpace(req.ModelID), "local") {
			if configured {
				config, err = h.models.SaveBotDesiredModelConfig(botUID, "", "")
			} else {
				config = storedConfig
			}
		} else {
			model, reasoning, ok := normalizeBotModelSelection(req.ModelID, req.ReasoningEffort)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported model or reasoning effort"})
				return
			}
			selectionApplied := configured &&
				config.ModelID == model.ID &&
				config.ReasoningEffort == reasoning &&
				config.AppliedRevision == config.Revision &&
				config.AppliedModelID == model.ID &&
				config.AppliedReasoning == reasoning &&
				!(config.LastAttemptRevision == config.Revision && config.LastError != "")
			if !selectionApplied {
				config, err = h.models.SaveBotDesiredModelConfig(botUID, model.ID, reasoning)
			}
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot model configuration"})
			return
		}
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, botModelConfigResponse(botUID, storedConfig, true))
		return
	}
	writeJSON(w, http.StatusOK, botModelConfigResponse(botUID, config, true))
}

// HandleRuntimeConfig lets an authenticated bot read only its own desired model.
func (h *BotModelConfigHandler) HandleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if _, err := h.owners.GetBotOwner(botUID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bot api key required"})
		return
	}
	if h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot model configuration is unavailable"})
		return
	}
	config, err := h.models.GetBotModelConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot model configuration"})
		return
	}
	writeJSON(w, http.StatusOK, botModelConfigResponse(botUID, config, false))
}

// HandleRuntimeAck records a successful or failed apply for the current revision.
func (h *BotModelConfigHandler) HandleRuntimeAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if _, err := h.owners.GetBotOwner(botUID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bot api key required"})
		return
	}
	if h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot model configuration is unavailable"})
		return
	}
	var req botModelAckRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Revision < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	model, reasoning, ok := normalizeBotModelSelection(req.ModelID, req.ReasoningEffort)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported model or reasoning effort"})
		return
	}
	current, err := h.models.GetBotModelConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot model configuration"})
		return
	}
	if current == nil || strings.TrimSpace(current.ModelID) == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cloud bot model configuration is not enabled"})
		return
	}
	current = botModelConfigWithDefaults(current)
	if current.Revision != req.Revision || current.ModelID != model.ID || current.ReasoningEffort != reasoning {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot model configuration changed before it was applied"})
		return
	}
	applyError := strings.TrimSpace(req.Error)
	if len(applyError) > 500 {
		applyError = applyError[:500]
	}
	config, err := h.models.AckBotModelConfig(botUID, req.Revision, model.ID, reasoning, applyError)
	if errors.Is(err, store.ErrStaleBotModelRevision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot model configuration changed before it was applied"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to acknowledge bot model configuration"})
		return
	}
	writeJSON(w, http.StatusOK, botModelConfigResponse(botUID, config, false))
}

func botModelConfigWithDefaults(config *types.BotModelConfig) *types.BotModelConfig {
	if config == nil {
		config = &types.BotModelConfig{}
	}
	copy := *config
	if copy.ModelID == "" {
		copy.ModelID = defaultBotModelID
	}
	model, reasoning, ok := normalizeBotModelSelection(copy.ModelID, copy.ReasoningEffort)
	if !ok {
		copy.ModelID = defaultBotModelID
		copy.ReasoningEffort = ""
		return &copy
	}
	copy.ModelID = model.ID
	copy.ReasoningEffort = reasoning
	return &copy
}

func normalizeBotModelSelection(modelID, reasoning string) (botModelCatalogItem, string, bool) {
	normalizedModel := strings.ToLower(strings.TrimSpace(modelID))
	for _, model := range botModelCatalog {
		if model.ID != normalizedModel {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(reasoning))
		if len(model.ReasoningEfforts) == 0 {
			if value != "" && value != "default" {
				return botModelCatalogItem{}, "", false
			}
			return model, "", true
		}
		if value == "" || value == "default" {
			value = model.DefaultReasoningEffort
		}
		for _, option := range model.ReasoningEfforts {
			if option == value {
				return model, value, true
			}
		}
		return botModelCatalogItem{}, "", false
	}
	return botModelCatalogItem{}, "", false
}

func botModelConfigResponse(botUID int64, config *types.BotModelConfig, includeCatalog bool) map[string]interface{} {
	configured := config != nil && strings.TrimSpace(config.ModelID) != ""
	config = botModelConfigWithDefaults(config)
	desiredModelID := config.ModelID
	desiredReasoning := config.ReasoningEffort
	if !configured {
		desiredModelID = "local"
		desiredReasoning = ""
	}
	status := "local"
	if configured && config.LastAttemptRevision == config.Revision && config.LastError != "" {
		status = "failed"
	} else if configured && config.AppliedRevision == config.Revision && config.AppliedModelID == config.ModelID {
		status = "applied"
	} else if configured {
		status = "pending"
	}
	response := map[string]interface{}{
		"uid":        botUID,
		"configured": configured,
		"desired": map[string]interface{}{
			"model_id": desiredModelID, "reasoning_effort": desiredReasoning,
			"revision": config.Revision, "updated_at": config.UpdatedAt,
		},
		"applied": map[string]interface{}{
			"model_id": config.AppliedModelID, "reasoning_effort": config.AppliedReasoning,
			"revision": config.AppliedRevision, "applied_at": config.AppliedAt,
		},
		"status":     status,
		"last_error": config.LastError,
		"apply_mode": "runtime_reload",
	}
	if includeCatalog {
		response["models"] = botModelCatalog
	}
	return response
}
